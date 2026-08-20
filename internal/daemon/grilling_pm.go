package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// grillingConsolidationBatchLimit bounds PM coordinator sessions per scan
// (config grilling_consolidation_batch): one heavy cross-task analysis per
// round by default, the rest waits for the next scan.
const grillingConsolidationBatchLimit = 1 // fallback when config is unset

// errPMInFlight guards against concurrent PM sessions for the same target
// (decision list for distribute, task group for consolidate). A session
// takes 3-10 minutes and its trigger signal stays true until the session
// finishes and records state — without this dedup every scan re-dispatches
// and stacks concurrent sessions (observed: 5 distribute processes on one
// list, 14:53-14:54; consolidate has the same shape when a fresh dispute
// bypasses the 4h cooldown every scan).
var errPMInFlight = errors.New("grilling pm session already in flight")

// grillingDecisionListName is the project-level decision list filename.
const grillingDecisionListName = "Grilling-Decisions.md"

// processGrillingConsolidation dispatches the project-level PM coordinator
// for needs-grilling tasks that share a req_doc or carry repeat disputes.
// Runs after processBatch, once per scan, never blocking dispatched tasks
// (they run in their own goroutines).
//
// Priority order:
//  1. distribute — a project decision list was answered by the user
//     (grill_continue=true) and parked tasks are waiting for the answers.
//  2. consolidate — a group of tasks shares a req_doc with un-parked members,
//     or a single task has a repeat dispute (grill_repeat >= 2).
func (r *Runner) processGrillingConsolidation(ctx context.Context) int {
	if ctx.Err() != nil {
		return 0
	}
	pending, err := task.FindGrillingTasks(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("grilling pm scan: %v", err)
		return 0
	}
	// In-flight tasks without a `stage` field under an active Stage-Plan join
	// the consolidation input: PM assigns them to an existing stage or
	// proposes a new one (stage-plan upkeep is automatic, not user-driven —
	// the user only decides when a NEW stage is warranted).
	unstaged, err := task.FindUnstagedTasks(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("unstaged scan: %v", err)
		return 0
	}
	if len(unstaged) > 0 {
		seen := make(map[string]bool, len(pending))
		for _, t := range pending {
			seen[t.FilePath] = true
		}
		for _, t := range unstaged {
			if !seen[t.FilePath] {
				pending = append(pending, t)
				seen[t.FilePath] = true
			}
		}
	}
	if len(pending) == 0 {
		return 0
	}

	byProject := make(map[string][]task.GrillingTask)
	for _, t := range pending {
		byProject[t.Project] = append(byProject[t.Project], t)
	}

	// Priority 1: distribute decision lists — either the Grilling-Decisions
	// list (parked tasks waiting) or a Stage-Review file (stage gate
	// answered: continue/supplement/end). The list is user-edited and
	// daemon-maintained: the user only fills in answers, and distribution
	// happens AUTOMATICALLY once every decision is answered (no manual
	// grill_continue needed); the manual flag remains only for partial /
	// revised-answer batches.
	for _, project := range sortedProjectKeys(byProject) {
		listPath := grillingDecisionListPath(r.cfg.ObsidianVault, project)
		if listPath == "" || !hasParked(byProject[project]) {
			continue
		}
		// 项目级暂停开关：清单 status=paused 时用户明确搁置，答案即使
		// 已填写也不分发写回、不派发 PM 会话——只有用户手动改回 open 才恢复。
		if grillingListPaused(listPath) {
			r.logger.Printf("project %s: decision list paused, distribution held", project)
			continue
		}
		answered := grillingListAnswered(listPath) // manual flag
		pending := grillingDecisionPending(listPath)
		total := grillingDecisionTotal(listPath)
		// Changed-since-last-distribute is the core signal: the user edited
		// the list after the last distribution (new answers, revised
		// answers, or a consolidation append). It covers the partial-batch
		// tail (distribute → user fills the rest → auto-dispatch) and the
		// revised-answer case (user overwrites an answered decision →
		// auto-dispatch again), neither of which the plain status flag can
		// see.
		changed := grillingListChangedSinceDistribute(listPath)
		// All answered with pending changes — auto-dispatch; only when the
		// list actually HAS decision points (an empty list is "nothing to
		// do", not "fully answered").
		autoDispatch := changed && pending == 0 && total > 0
		if answered && pending > 0 || autoDispatch {
			if err := r.runGrillingPM(ctx, "distribute", listPath); err != nil {
				if errors.Is(err, errAPIKeyUnavailable) {
					return 0 // retry next scan
				}
				if errors.Is(err, errPMInFlight) {
					r.logger.Printf("project %s: grilling pm distribute already in flight, skip", project)
					continue
				}
				r.logger.Printf("project %s: grilling pm distribute: %v", project, err)
				continue
			}
			r.logger.Printf("project %s: grilling pm distribute dispatched (%d pending decisions%s)", project, pending, map[bool]string{true: ", auto (fully answered)", false: ""}[autoDispatch])
			return 1
		}
		if answered && pending == 0 && !changed {
			// Fully answered AND nothing changed since the last distribute:
			// reset the flag so the user's repeated grill_continue=true
			// cannot spin an empty session, and tell them the list is
			// complete. The toast is debounced per project per day — a
			// re-set flag must not re-notify.
			r.logger.Printf("project %s: decision list fully answered and distributed (%d decisions), closing", project, grillingDecisionTotal(listPath))
			_ = yamlfrontmatter.Update(listPath, map[string]interface{}{"grill_continue": false, "status": "answered"})
			if !r.diagNotified("grilling|answered|" + project + "|" + time.Now().Format("2006-01-02")) {
				notify.SendTaskAction("grilling", "Grilling-Decisions", "✅", "决策清单已全部回答",
					"无需再次设置 grill_continue；清单已关闭，任务按已答决策恢复流转。", r.cfg.Notifications.Desktop)
			}
			continue
		}
		revPath := stageReviewPath(r.cfg.ObsidianVault, project)
		if revPath != "" && grillingListAnswered(revPath) {
			// The state flip is daemon-deterministic: Stage-Plan status
			// transitions happen here, before the PM session — the PM
			// session still runs afterwards for REQ annotations and the
			// knowledge sink (Mode 2.5 remainder).
			if r.flipStageReviewDecision(ctx) {
				r.logger.Printf("project %s: stage-plan flipped by daemon, PM session continues for annotations", project)
			}
			if err := r.runGrillingPM(ctx, "distribute", revPath); err != nil {
				if errors.Is(err, errAPIKeyUnavailable) {
					return 0 // retry next scan
				}
				if errors.Is(err, errPMInFlight) {
					r.logger.Printf("project %s: stage review distribute already in flight, skip", project)
					continue
				}
				r.logger.Printf("project %s: stage review distribute: %v", project, err)
				continue
			}
			r.logger.Printf("project %s: stage review distribute dispatched", project)
			return 1
		}
	}

	// Priority 1.5: a stage whose tasks all landed (done + merged) triggers
	// one PM stage-review — the per-stage delivery gate with user scorecard.
	if r.processStageReviews(ctx) > 0 {
		return 1
	}

	// Priority 2: consolidate groups that need cross-task coordination, up
	// to grilling_consolidation_batch sessions per scan (default 1).
	batch := r.cfg.GrillingConsolidationBatch
	if batch <= 0 {
		batch = grillingConsolidationBatchLimit
	}
	dispatched := 0
	for _, project := range sortedProjectKeys(byProject) {
		if listPath := grillingDecisionListPath(r.cfg.ObsidianVault, project); listPath != "" && grillingListPaused(listPath) {
			r.logger.Printf("project %s: decision list paused, consolidation held", project)
			continue
		}
		group := groupByReqDoc(byProject[project])
		for _, req := range sortedProjectKeys(group) {
			members := group[req]
			if !needsConsolidation(members) {
				continue
			}
			// Cooldown: a group whose PM session produced no state change
			// (e.g. no Stage-Plan yet, dispute already consolidated, unstaged
			// task the PM chose not to attach) must not hog the per-scan
			// batch slot forever — other projects would starve (observed:
			// 003 re-dispatched every scan while release-manager never got a
			// slot). Only a genuinely fresh dispute (unparked, non-unstaged
			// member) resets the cooldown.
			if last, ok := r.consolidatedAt.Load(req); ok {
				if time.Since(last.(time.Time)) < 4*time.Hour && !hasFreshDispute(members) {
					r.logger.Printf("grilling pm consolidate %s: cooldown (dispatched %v ago), skip", req, time.Since(last.(time.Time)).Round(time.Minute))
					continue
				}
			}
			paths := make([]string, 0, len(members))
			for _, m := range members {
				paths = append(paths, m.FilePath)
			}
			if err := r.runGrillingPM(ctx, "consolidate", paths...); err != nil {
				if errors.Is(err, errAPIKeyUnavailable) {
					r.logger.Printf("grilling pm consolidate %s: api key unavailable, retry next scan", req)
					return dispatched // retry next scan
				}
				if errors.Is(err, errPMInFlight) {
					r.logger.Printf("grilling pm consolidate %s: already in flight, skip", req)
					continue
				}
				r.logger.Printf("grilling pm consolidate %s: %v", req, err)
				continue
			}
			// Cooldown records only SYNCHRONOUS success (session started).
			// Synchronous failures (API key, in-flight) stay retryable on the
			// next scan instead of parking for 4h; an async failure inside
			// the session still parks the group, which is fine — the storm
			// path (fresh dispute) bypasses the cooldown anyway.
			r.consolidatedAt.Store(req, time.Now())
			r.logger.Printf("grilling pm consolidate dispatched: %s (%d tasks)", req, len(members))
			dispatched++
			if dispatched >= batch {
				return dispatched
			}
		}
	}
	return dispatched
}

// runGrillingPM invokes one execution session running the PM coordinator skill.
// Sessions run ASYNC — a consolidate/distribute/stage-review session takes
// 3-10 minutes, and running it synchronously stalls the scan loop (no
// scans, no logs, daemon looks hung; observed repeatedly during stage-plan
// consolidation). The scan loop dispatches and moves on; completion is
// logged by the goroutine. PM sessions mutate TASK frontmatter via
// otg update-status, which serializes on the per-file flock, so concurrency
// with task dispatch is safe.
func (r *Runner) runGrillingPM(ctx context.Context, mode string, args ...string) error {
	if !apiKeyAvailable() {
		return errAPIKeyUnavailable
	}
	timeoutMin := r.cfg.PhaseTimeoutMinutes["refining"]
	if timeoutMin <= 0 {
		timeoutMin = 15
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMin)*time.Minute)

	// Per-target in-flight dedup: mark BEFORE the async goroutine starts so a
	// re-entrant scan cannot stack a second session (distribute for the same
	// list, consolidate for the same task group) while the first is running.
	var listPath string
	var inflightKey string
	if mode == "distribute" && len(args) > 0 {
		listPath = args[0]
		inflightKey = "distribute:" + listPath
	} else if mode == "consolidate" && len(args) > 0 {
		// Group key = the first task path: directory traversal order is
		// stable, so a req_doc group keeps the same first member across
		// scans. A member ADDED mid-session changes join(args) but not
		// args[0] — the join-based key would spawn a concurrent session for
		// the enlarged group (exactly the storm we guard against); the
		// stable key defers it to the next scan after the session ends.
		inflightKey = "consolidate:" + args[0]
	}
	if inflightKey != "" {
		if _, loaded := r.pmInFlight.LoadOrStore(inflightKey, true); loaded {
			cancel()
			return errPMInFlight
		}
	}

	prompt := "/obsidian-task-runner-pm " + mode + " " + strings.Join(args, " ")
	go func() {
		defer cancel()
		if inflightKey != "" {
			defer r.pmInFlight.Delete(inflightKey)
		}
		// Snapshot the answer state BEFORE the session runs — the
		// notification decision compares pre-session vs post-session hashes.
		var stored, beforeHash string
		if mode == "distribute" && len(args) > 0 {
			if data, err := os.ReadFile(listPath); err == nil {
				beforeHash = grillingAnswersHash(string(data))
				if fm, perr := yamlfrontmatter.Parse(data); perr == nil && fm != nil {
					stored, _ = fm.Extra["distributed_answers_hash"].(string)
				}
			}
		}
		output, runErr := r.execPMSession(runCtx, prompt, mode, args)
		if runErr != nil {
			if runCtx.Err() != nil && ctx.Err() != nil {
				// Interrupted by daemon shutdown; retry on next scan.
				r.logger.Printf("grilling pm %s interrupted (shutdown)", mode)
				return
			}
			r.logger.Printf("grilling pm %s failed: %v (output: %s)", mode, runErr, summarizeOutput(output))
			if mode == "distribute" {
				// The user set grill_continue or finished the list and hears
				// nothing when the session fails — surface the failure and
				// that the list stays open.
				notify.SendTaskAction("grilling", "Grilling-Decisions", "❌", "决策分发失败",
					"清单未关闭，答案未写回；可稍后重试（重新设置 grill_continue: true 或等自动分发）。", r.cfg.Notifications.Desktop)
			}
			return
		}
		r.logger.Printf("grilling pm %s ok: %s", mode, summarizeOutput(output))
		switch mode {
		case "distribute":
			// Record the answer snapshot right after the session so the
			// next change detection is exact (daemon-side, deterministic —
			// not left to the LLM session). A failed hash write would leave
			// changed=true forever and re-dispatch every scan — surface it.
			var afterHash string
			if listPath != "" {
				if data, err := os.ReadFile(listPath); err == nil {
					afterHash = grillingAnswersHash(string(data))
					if uerr := yamlfrontmatter.Update(listPath, map[string]interface{}{
						"distributed_answers_hash": afterHash,
						"last_distributed_at":      time.Now().Format(time.RFC3339),
					}); uerr != nil {
						r.logger.Printf("grilling pm distribute %s: record answer hash failed: %v", listPath, uerr)
					}
				}
			}
			// The user answers the decision list and then hears nothing —
			// surface what the distribution consumed and what stays blocked.
			// Convergence: only notify when this session actually consumed
			// NEW answers relative to the last distribution (first
			// distribution, or the pre-session answer hash differs from the
			// stored one). A no-change session (user re-set grill_continue
			// on a fully answered list) must stay silent instead of
			// re-notifying. Note: the PM session itself never edits the
			// list's answer region (it writes REQs), so before-vs-after
			// comparison would always be equal — the stored hash is the
			// correct baseline.
			if stored == "" || (beforeHash != "" && beforeHash != stored) {
				notify.SendTaskAction("grilling", "Grilling-Decisions", "📨", "决策分发完成",
					"清单答案已写回 REQ；未答决策点对应任务保持阻塞。全部回答后会自动分发，无需手动操作。", r.cfg.Notifications.Desktop)
			} else {
				r.logger.Printf("grilling pm distribute %s: no new answer changes, notification skipped", listPath)
			}
		case "consolidate":
			// Parked tasks are silent by design — but the user must know
			// there are decisions waiting (the D-11..D-18 pile-up root cause:
			// nobody told them the list needed answers).
			proj := "?"
			if len(args) > 0 {
				if idx := strings.Index(args[0], "Projects/"); idx >= 0 {
					rest := args[0][idx+len("Projects/"):]
					proj = strings.SplitN(rest, "/", 2)[0]
				}
			}
			notify.SendTaskAction("grilling", "Grilling-Decisions", "📋", "需求统筹完成",
				"项目级决策清单可能已新增待答项：Projects/"+proj+"/Notes/Grilling-Decisions.md — 填写答案后自动分发。", r.cfg.Notifications.Desktop)
		}
	}()
	return nil
}

// execPMSession runs one PM coordinator session through the DSH phase
// executor and returns its stdout plus any process error.
func (r *Runner) execPMSession(runCtx context.Context, prompt, mode string, args []string) ([]byte, error) {
	spec := PhaseSpec{
		Phase:           "pm",
		Model:           r.cfg.Model("default"),
		ReasoningEffort: "low", // 统筹/分发是确定性为主的任务，low 足够
		SkillPrompt:     prompt,
		Timeout:         runCtxTimeout(runCtx),
	}
	executor := r.phaseExecutor
	if executor == nil {
		executor = newPhaseExecutor(r.cfg)
		r.phaseExecutor = executor
	}
	handle, err := executor.Start(runCtx, spec, TaskSnapshot{})
	if err != nil {
		return nil, err
	}
	result, err := handle.Wait()
	if err != nil {
		return nil, err
	}
	if result == nil || result.Code != OutcomeSuccess {
		if runCtx.Err() != nil {
			return nil, runCtx.Err()
		}
		reason := "pm session failed"
		if result != nil && result.Error != "" {
			reason = result.Error
		}
		return nil, errors.New(reason)
	}
	return []byte(result.Stdout), nil
}

// runCtxTimeout derives the deadline remaining from a context (best-effort;
// zero means no deadline).
func runCtxTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	return 0
}

// grillingDecisionListPath resolves the project decision list path, or ""
// when the project has no vault directory.
func grillingDecisionListPath(vaultPath, project string) string {
	projDir := resolveVaultProjectDir(vaultPath, project)
	if projDir == "" {
		return ""
	}
	return filepath.Join(projDir, "Notes", grillingDecisionListName)
}

// grillingListAnswered reports whether the decision list frontmatter has
// grill_continue=true (user finished answering).
func grillingListAnswered(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false
	}
	return fm.GrillContinue
}

// grillingListDistributed reports whether the list frontmatter status is
// "answered" — the pm distribute Step 4 terminal marker meaning the answers
// were already written back. A list with status=open has never been
// distributed even when every decision line is filled.
func grillingListDistributed(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false
	}
	return fm.Status == "answered"
}

// grillingListChangedSinceDistribute reports whether the decision ANSWERS
// changed since the last distribution. Answer-hash comparison (not mtime):
// formatting-only edits and pm maintenance writes (log lines, timestamps)
// do not change the answers and must not re-trigger distribution; a changed
// answer (fill, revise) must. A missing stored hash means "never
// distributed" → changed.
func grillingListChangedSinceDistribute(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false
	}
	stored, _ := fm.Extra["distributed_answers_hash"].(string)
	if stored == "" {
		return true // never distributed (or legacy list)
	}
	return grillingAnswersHash(string(data)) != stored
}

// grillingAnswersHash computes a stable hash over the decision answers
// (D-n block headings + their 决策: values + the split-confirmation line).
// Comment lines, log entries and timestamps outside the answer region are
// excluded, so only genuine answer changes move the hash.
func grillingAnswersHash(content string) string {
	var sb strings.Builder
	blocks := decisionBlockRE.FindAllStringIndex(content, -1)
	for i, loc := range blocks {
		end := len(content)
		if i+1 < len(blocks) {
			end = blocks[i+1][0]
		}
		block := content[loc[0]:end]
		heading := block
		if idx := strings.Index(block, "\n"); idx >= 0 {
			heading = block[:idx]
		}
		sb.WriteString(strings.TrimSpace(heading))
		sb.WriteString("|")
		if m := decisionLineRE.FindStringSubmatch(block); m != nil {
			sb.WriteString(strings.TrimSpace(m[1]))
		}
		sb.WriteString(";")
	}
	// Bound the split-confirmation match to the "## 拆分确认" section so a
	// "- 拆分:" line inside a decision body (unlikely but possible) cannot
	// leak into the hash.
	if sec := sectionAfter(content, "## 拆分确认"); sec != "" {
		if m := splitLineRE.FindStringSubmatch(sec); m != nil {
			sb.WriteString("split|")
			sb.WriteString(strings.TrimSpace(m[1]))
		}
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// sectionAfter returns the content starting at the first occurrence of the
// heading (exclusive), or "" when the heading is absent.
func sectionAfter(content, heading string) string {
	idx := strings.Index(content, heading)
	if idx < 0 {
		return ""
	}
	return content[idx+len(heading):]
}

// decisionBlockRE matches a decision point block heading in the list.
// decisionLineRE matches the answer line ("决策: <用户填写>").
// splitLineRE matches the split-confirmation line ("拆分: <确认 / …>").
var (
	decisionBlockRE = regexp.MustCompile(`(?m)^### D-\d+:`)
	decisionLineRE  = regexp.MustCompile(`(?m)^- 决策:\s*(.*)$`)
	splitLineRE     = regexp.MustCompile(`(?m)^- 拆分:\s*(.*)$`)
)

// grillingDecisionCounts parses the decision list and counts total and
// unfilled decision points. A decision point counts as pending when its
// "决策:" line is empty or still the placeholder; the split-confirmation
// line is counted the same way. This gives the daemon a deterministic
// signal to (a) skip empty distribute round-trips when everything is
// answered and (b) tell the user how many decisions remain.
func grillingDecisionCounts(path string) (total, pending int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	content := string(data)
	blocks := decisionBlockRE.FindAllStringIndex(content, -1)
	for i, loc := range blocks {
		total++
		// Bound the block at the next heading so a malformed block without
		// its own "决策:" line cannot swallow the next block's answer.
		end := len(content)
		if i+1 < len(blocks) {
			end = blocks[i+1][0]
		}
		block := content[loc[0]:end]
		if m := decisionLineRE.FindStringSubmatch(block); m != nil {
			if !decisionAnswered(m[1]) {
				pending++
			}
		}
	}
	if m := splitLineRE.FindStringSubmatch(content); m != nil {
		total++
		if !decisionAnswered(m[1]) {
			pending++
		}
	}
	return total, pending
}

// decisionAnswered reports whether an answer line carries a real answer
// (not empty and not the placeholder).
//
// Placeholder matching is deliberately loose: the PM skill template writes
// `{用户填写}`, older list revisions carry `<用户填写>`, and field copies
// render as `（用户填写）` — all three must count as unanswered, otherwise a
// list whose answers are all placeholders reports pending=0 and the daemon
// never opens the decision tab (and worse, auto-distributes an empty batch
// every scan, inflating the list log).
func decisionAnswered(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if strings.Contains(v, "用户填写") {
		return false
	}
	for _, placeholder := range []string{"确认 / 修改（列出修改）/ 不拆分", "继续 / supplement:{建议} / end"} {
		if strings.Contains(v, placeholder) {
			return false
		}
	}
	return true
}

// grillingDecisionPending is the pending count shorthand.
func grillingDecisionPending(path string) int {
	_, pending := grillingDecisionCounts(path)
	return pending
}

// grillingDecisionPendingForTask counts pending (unanswered) decision points
// sourced from a specific task (their block carries "- 来源任务: TASK-<id>").
// A dispute park — grill_parked=true because conflicts escalated into the
// project-level list — must stay parked until PM distribute consumes the
// answers. This is how parkedFactRecovery distinguishes a dispute park
// (recovery gate = the decision list, answered only by PM distribute) from a
// prerequisite-gate park (D-19 style, gate = blocked_by facts converging, no
// list entry for the task). Without it TASK-068 un-parked every scan on its
// landed blocked_by while D-88/89/90 stayed unanswered, looping refining.
func grillingDecisionPendingForTask(path, taskID string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	content := string(data)
	blocks := decisionBlockRE.FindAllStringIndex(content, -1)
	pending := 0
	ref := "- 来源任务: TASK-" + taskID
	for i, loc := range blocks {
		end := len(content)
		if i+1 < len(blocks) {
			end = blocks[i+1][0]
		}
		block := content[loc[0]:end]
		if !strings.Contains(block, ref) {
			continue
		}
		if m := decisionLineRE.FindStringSubmatch(block); m != nil {
			if !decisionAnswered(m[1]) {
				pending++
			}
		}
	}
	return pending
}

// grillingListPaused reports whether the decision list's frontmatter status
// is paused/closed — the project-level pause switch. While paused, every
// automated flow for the project's grilling tasks is held: no reminders, no
// Kitty decision tab, no grill_continue reset, no PM distribute/consolidate
// dispatch, no parked-task un-parking. The pause lifts when the user acts:
// manually setting the list status to "open", or updating the associated REQ
// (daemon auto-activates — the user actively supplementing the requirement).
//
// Accepts "pause" (user typo variant seen in the field), "paused" and
// "closed", case-insensitively.
func grillingListPaused(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false
	}
	switch strings.ToLower(fm.Status) {
	case "paused", "pause", "closed":
		return true
	}
	return false
}

// activatePausedDecisionList flips a project's decision list from paused
// back to open when the requirement driving it was updated. A REQ update is
// the user/team actively supplementing the requirement — the same "user
// acting" signal as manually setting status=open — so the pause is lifted:
// reminders and the downstream flow (tasks re-entering refining via
// pending_req, maturity gate, consolidate re-evaluating new requirements
// against existing disputes, planning) pick up again, and the user aligns
// via Grilling before tasks resume.
// Returns true when the list existed and was paused.
func activatePausedDecisionList(vaultPath, project string) (bool, error) {
	path := filepath.Join(vaultPath, "Projects", project, "Notes", "Grilling-Decisions.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false, nil
	}
	switch strings.ToLower(fm.Status) {
	case "paused", "pause", "closed":
		// Fall through to reactivate.
	default:
		return false, nil
	}
	if err := yamlfrontmatter.Update(path, map[string]interface{}{
		"status":  "open",
		"updated": time.Now().Format(time.RFC3339),
	}); err != nil {
		return false, err
	}
	return true, nil
}

// grillingDecisionTotal is the total count shorthand.
func grillingDecisionTotal(path string) int {
	total, _ := grillingDecisionCounts(path)
	return total
}

// needsConsolidation reports whether a req_doc group requires a PM session:
// a shared req_doc with at least one un-parked member, or a lone task whose
// dispute repeated enough to park. Fully parked groups already have their
// questions in the decision list and must not be re-consolidated.
func needsConsolidation(members []task.GrillingTask) bool {
	if len(members) == 0 {
		return false
	}
	if len(members) == 1 {
		// Single-task consolidation: repeated identical disputes (grill_repeat)
		// OR a requirement that keeps churning replans (plan_version >= 3, e.g.
		// TASK-066's 15 no-op replans) OR an unstaged in-flight task (stage
		// plan upkeep) escalate to the project-level decision list so the user
		// answers once instead of per round.
		return !members[0].GrillParked && (members[0].GrillRepeat >= 2 || members[0].PlanVersion >= 3 || members[0].Unstaged)
	}
	for _, m := range members {
		if !m.GrillParked {
			return true
		}
	}
	return false
}

func hasParked(members []task.GrillingTask) bool {
	for _, m := range members {
		if m.GrillParked {
			return true
		}
	}
	return false
}

// hasFreshDispute reports whether any member carries genuinely new
// cross-task work: unparked AND not merely an unstaged task. Unstaged-only
// groups (stage-plan upkeep) must respect the cooldown — re-dispatching
// them every scan starves other projects when the PM session cannot
// converge (e.g. no Stage-Plan and nothing to attach).
func hasFreshDispute(members []task.GrillingTask) bool {
	for _, m := range members {
		if !m.GrillParked && !m.Unstaged {
			return true
		}
	}
	return false
}

func groupByReqDoc(members []task.GrillingTask) map[string][]task.GrillingTask {
	group := make(map[string][]task.GrillingTask)
	for _, m := range members {
		group[m.ReqDoc] = append(group[m.ReqDoc], m)
	}
	return group
}

func sortedProjectKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func summarizeOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if len(trimmed) > 300 {
		return trimmed[:300] + "..."
	}
	return trimmed
}
