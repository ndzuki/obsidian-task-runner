package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// grillingConsolidationBatchLimit bounds PM coordinator sessions per scan
// (config grilling_consolidation_batch): one heavy cross-task analysis per
// round by default, the rest waits for the next scan.
const grillingConsolidationBatchLimit = 1 // fallback when config is unset

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
			// complete.
			r.logger.Printf("project %s: decision list fully answered and distributed (%d decisions), closing", project, grillingDecisionTotal(listPath))
			_ = yamlfrontmatter.Update(listPath, map[string]interface{}{"grill_continue": false, "status": "answered"})
			notify.SendTaskAction("grilling", "Grilling-Decisions", "✅", "决策清单已全部回答",
				"无需再次设置 grill_continue；清单已关闭，任务按已答决策恢复流转。", r.cfg.Notifications.Desktop)
			continue
		}
		revPath := stageReviewPath(r.cfg.ObsidianVault, project)
		if revPath != "" && grillingListAnswered(revPath) {
			if err := r.runGrillingPM(ctx, "distribute", revPath); err != nil {
				if errors.Is(err, errAPIKeyUnavailable) {
					return 0 // retry next scan
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
			r.consolidatedAt.Store(req, time.Now())
			if err := r.runGrillingPM(ctx, "consolidate", paths...); err != nil {
				if errors.Is(err, errAPIKeyUnavailable) {
					r.logger.Printf("grilling pm consolidate %s: api key unavailable, retry next scan", req)
					return dispatched // retry next scan
				}
				r.logger.Printf("grilling pm consolidate %s: %v", req, err)
				continue
			}
			r.logger.Printf("grilling pm consolidate dispatched: %s (%d tasks)", req, len(members))
			dispatched++
			if dispatched >= batch {
				return dispatched
			}
		}
	}
	return dispatched
}

// runGrillingPM invokes one OMP session running the PM coordinator skill.
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

	prompt := "/obsidian-task-runner-pm " + mode + " " + strings.Join(args, " ")
	cmd := exec.CommandContext(runCtx, r.cfg.OMPCmd,
		"--model", r.cfg.Model("default"), "--auto-approve", "-p", prompt)
	// Graceful timeout/shutdown: SIGTERM first, hard-kill after WaitDelay.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	go func() {
		defer cancel()
		output, runErr := cmd.Output()
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
			// not left to the LLM session).
			if listPath := args[0]; listPath != "" {
				if data, err := os.ReadFile(listPath); err == nil {
					_ = yamlfrontmatter.Update(listPath, map[string]interface{}{
						"distributed_answers_hash": grillingAnswersHash(string(data)),
						"last_distributed_at":      time.Now().Format(time.RFC3339),
					})
				}
			}
			// The user answers the decision list and then hears nothing —
			// surface what the distribution consumed and what stays blocked.
			notify.SendTaskAction("grilling", "Grilling-Decisions", "📨", "决策分发完成",
				"清单答案已写回 REQ；未答决策点对应任务保持阻塞。全部回答后会自动分发，无需手动操作。", r.cfg.Notifications.Desktop)
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
func decisionAnswered(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	for _, placeholder := range []string{"<用户填写>", "确认 / 修改（列出修改）/ 不拆分", "继续 / supplement:{建议} / end"} {
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

// grillingListPaused reports whether the decision list's frontmatter status
// is paused/closed — the user's opt-out from reminder noise while a
// requirement is still being thought through. Reminders (Kitty decision tab)
// are suppressed, but distribution still works: answering the list and
// setting grill_continue=true dispatches normally.
func grillingListPaused(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false
	}
	return fm.Status == "paused" || fm.Status == "closed"
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
