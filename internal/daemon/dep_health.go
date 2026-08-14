package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// depHealthThresholds bound the project-health warnings so a healthy project
// never fires a toast and a stuck one fires at most once per day.
const (
	rebaselineWarnInFlight      = 20 // in-flight tasks above which a queue is suspicious
	rebaselineWarnMergedNotDone = 5  // tasks merged but never marked done (stale closure)
	stageEmptyWarn              = 5  // in-flight tasks without a stage (topology lost)
	phaseOversizeWarn           = 8  // tasks in one in-progress phase (split-worthy)
)

// autoCloseStaleMergedTasks closes the stale-closure loop: a task whose PR
// is merged (merge_status=merged) but whose status never reached done is a
// delivered deliverable with outdated bookkeeping — the merge itself is the
// deterministic evidence (Round 2 verified, CI passed, gh pr merge done).
// Such tasks flip to done automatically; tasks with pending_req (requirement
// delta needs re-implementation) and closed tasks (deliberate terminal) are
// left untouched. One notification per task.
func (r *Runner) autoCloseStaleMergedTasks() int {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	closed := 0
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projClosed := 0
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.MergeStatus != "merged" {
				continue
			}
			if fm.Status == "done" || fm.Status == "closed" || fm.PendingReq {
				continue
			}
			// A task that re-entered the planning lifecycle after its baseline
			// PR merged (incremental replan: plan_version >= 2) still owes a new
			// delivery — the historical merge_status=merged is the baseline's
			// evidence, not the increment's. Auto-closing it would silently drop
			// the pending increment (observed: TASK-068 — plan v3 was written to
			// plan-review and closed the next scan once Round 1 cleared
			// pending_req). plan_version < 2 keeps the original stale-closure
			// semantics (single delivery whose PR merged).
			if fm.PlanVersion >= 2 {
				continue
			}
			if err := yamlfrontmatter.Update(path, map[string]interface{}{"status": "done"}); err != nil {
				r.logger.Printf("auto-close %s: %v", entry.Name(), err)
				continue
			}
			projClosed++
			closed++
			r.logger.Printf("health %s: TASK-%s auto-closed to done (PR merged, no pending_req)", projectEntry.Name(), fm.ID)
			if !r.diagNotified("autoclose|" + fm.ID) {
				notify.SendTaskAction(fm.ID, fm.Title, "✅", "任务自动收口",
					"PR 已合入且无需求增量，状态自动从 "+fm.Status+" 转为 done（daemon 确定性收口）。", r.cfg.Notifications.Desktop)
			}
		}
		if projClosed > 0 {
			r.updateRoadmap(projectEntry.Name(), "任务自动收口", fmt.Sprintf("%d 个任务自动收口为 done（PR merged 且无 pending_req）", projClosed))
		}
	}
	return closed
}

// detectStaleDoneReopens reopens done tasks locked in a stale terminal: a
// task whose frontmatter claims delivery (status=done + merge_status=merged)
// while carrying a newer plan (plan_version >= 2) and a checkpoint_commit
// that is NOT an ancestor of origin/main is an undelivered increment frozen
// behind a terminal state — the daemon would never dispatch it again
// (TASK-018: an external frontmatter write overwrote the reopen back to the
// baseline done; downstream TASK-071 starved on the fake done via the
// dependency gate). The reopen applies the same generation reset as a
// breaking REQ change so the merge flow later creates a fresh PR. Tasks
// whose checkpoint is an ancestor (or whose repo cannot be resolved, or the
// git check is inconclusive) are left untouched — conservative, never
// guesses. Idempotent: reopened tasks leave done, so later scans no-op.
func (r *Runner) detectStaleDoneReopens() int {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	reopened := 0
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		// Team projects (manual-mode delivery) merge through the forge UI,
		// which may squash the feature branch: the checkpoint commit then
		// never becomes an origin/main ancestor even though the delivery
		// landed. merge_status=merged is written by the remote-merge probe
		// only after the head actually reached the default branch, so it is
		// the authoritative delivery evidence — skip the ancestry re-check
		// to avoid false reopens of delivered team tasks.
		if projectIsTeam(filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json"), stripNumericPrefix(projectEntry.Name())) {
			continue
		}
		projReopened := 0
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				continue
			}
			if fm.Status != "done" || fm.PlanVersion < 2 || fm.CheckpointCommit == "" {
				continue
			}
			if fm.MergeStatus != "merged" {
				// done without a merged PR: the DoneReopensMerge path owns it.
				continue
			}
			repoDir := r.repoDirForTask(fm.Project)
			if repoDir == "" {
				continue
			}
			if checkpointAncestor(repoDir, fm.CheckpointCommit) {
				continue // genuinely delivered (checkpoint is in origin/main)
			}
			// Stale terminal: reopen like a breaking REQ change.
			updates := map[string]interface{}{
				"status":              "refining",
				"pending_req":         true,
				"merge_approved":      false,
				"reopen_count":        fm.ReopenCount + 1,
				"target_branch":       "",
				"pr_url":              "",
				"merge_status":        "",
				"completed":           "",
				"knowledge_extracted": false,
			}
			if err := yamlfrontmatter.Update(path, updates); err != nil {
				r.logger.Printf("stale-done reopen %s: %v", entry.Name(), err)
				continue
			}
			projReopened++
			reopened++
			r.logger.Printf("health %s: TASK-%s stale done reopened to refining (checkpoint %s not in origin/main, undelivered increment)", projectEntry.Name(), fm.ID, fm.CheckpointCommit)
			if !r.diagNotified("staledone|" + fm.ID) {
				notify.SendTaskAction(fm.ID, fm.Title, "🔄", "陈旧终态自动重开",
					"任务标记 done 但携带未合入的 checkpoint（plan v"+fmt.Sprint(fm.PlanVersion)+"），已按 breaking 语义重开 refining；请确认该增量是否仍需交付。", r.cfg.Notifications.Desktop)
			}
		}
		if projReopened > 0 {
			r.updateRoadmap(projectEntry.Name(), "陈旧终态自动重开", fmt.Sprintf("%d 个 done 任务因未合入 checkpoint 自动重开 refining", projReopened))
		}
	}
	return reopened
}

// repoDirForTask resolves a project's registered checkout path without side
// effects (no promotion/checkout — read-only scan). Empty when unresolved.
//
// Note: vault-fallback projects (registered with a path inside the Vault
// rather than a standalone checkout) resolve to the Vault repo — the
// checkpoint commit will not exist there, so checkpointAncestor reports
// "inconclusive" and the task is left untouched (conservative: never
// reopen on uncertainty). The read-only design intentionally skips the
// ensureProjectCheckout promotion used by resolveRepo.
func (r *Runner) repoDirForTask(projectName string) string {
	if projectName == "" {
		return ""
	}
	mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
	result := project.ResolveProject(mapFile, projectName, false)
	if result.Status == "error" {
		if mapped := project.MatchVaultDir(mapFile, projectName); mapped != "" {
			result = project.ResolveProject(mapFile, mapped, false)
		}
	}
	if result.Status == "error" || result.Path == "" {
		return ""
	}
	return result.Path
}

// checkpointAncestor reports whether commit is an ancestor of the local
// origin/main ref. Failures (missing ref/repo, git unavailable) are treated
// as ancestor — never reopen on uncertainty.
func checkpointAncestor(repoDir, commit string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, "refs/remotes/origin/main")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false // clean "not an ancestor"
		}
		return true // inconclusive: do not touch
	}
	return true
}

// recoverUnExtractedKnowledge 重试已交付任务的知识提取：daemon 在 merge
// 写回与提取 goroutine 之间被杀、优雅停机截断、或部分提取失败，都会让
// done+merged 任务留下 knowledge_extracted=false。done+merged 是终态，
// 除此之外没有任何路径会再触碰它——教训此前被静默丢失。PR 合入是
// "应当已提取"的确定性证据；本扫描让重试自动且幂等（marker 短路
// 重复提取，ADR/踩坑写入是整文件覆盖，重跑安全）。每轮 scan 执行；
// marker 已置位时开销极小——每个已交付任务一次 frontmatter 解析。
func (r *Runner) recoverUnExtractedKnowledge() int {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	retried := 0
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.KnowledgeExtracted || fm.MergeStatus != "merged" {
				continue
			}
			if fm.Status != "done" || fm.PendingReq {
				continue
			}
			r.logger.Printf("health %s: TASK-%s delivered but knowledge not extracted, re-extracting", projectEntry.Name(), fm.ID)
			// 完整管道：提取 + 重分类/提升 + 应用记录 + verified 翻转
			// + INDEX + 检索库同步。内部失败写 knowledge_extract_error
			// + 通知并保持 marker false，下一轮 scan 再次重试。
			r.extractProjectKnowledge(projectEntry.Name(), path)
			retried++
		}
	}
	return retried
}

// validateDependencyRefs surfaces broken dependency references before they
// silently starve a task: a blocked_by / depends_on id that does not exist in
// the project can never be satisfied, so the gated task waits forever without
// any signal. Logs once and notifies once per (project, reference).
func (r *Runner) validateDependencyRefs() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		tasksDir := filepath.Join(projDir, "Tasks")
		taskIDs, taskStatus, closedReason, closedReplacement, unparsable, err := r.depIDs(tasksDir)
		if err != nil {
			continue
		}

		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.Status == "" {
				continue // unparsable / stateless docs join no health statistic
			}
			for _, ref := range fm.BlockedBy {
				if strings.Contains(ref, ":") {
					continue // cross-project reference: resolved via vault-map, not checked here
				}
				id := strings.TrimPrefix(ref, "TASK-")
				if taskIDs[id] {
					// Only non-terminal tasks can be starved by a closed
					// upstream. already-implemented closures delivered their
					// work, and duplicates with a replacement resolve through
					// the replacement; neither is a permanent dependency risk.
					if fm.Status != "done" && fm.Status != "closed" && closedNeverSatisfiable(taskStatus[id], closedReason[id], closedReplacement[id]) {
						key := projectEntry.Name() + "|blocked_by_closed|" + fm.ID + "->" + id
						if !r.diagNotified(key) {
							r.logger.Printf("health %s: TASK-%s blocked_by references closed TASK-%s (never satisfiable)", projectEntry.Name(), fm.ID, id)
							r.notifyDiag(projectEntry.Name(), "依赖引用已关闭任务",
								"TASK-"+fm.ID+" 的 blocked_by 引用了已关闭的 TASK-"+id+"（closed 为终态且无已交付替代，依赖永不满足）；请修正引用或重新打开该任务（otg update-status blocked_by=...）")
						}
					}
					continue
				}
				if unparsable[id] {
					// 目标文件存在但 frontmatter 当前解析失败（如 OMP 会话写回
					// 的瞬时坏窗口）——不是悬空引用，跳过本轮，下一轮重新校验；
					// 避免把短暂解析失败误报为"引用不存在的任务"。
					r.logger.Printf("health %s: TASK-%s blocked_by references TASK-%s which exists but failed to parse; deferring", projectEntry.Name(), fm.ID, id)
					continue
				}
				key := projectEntry.Name() + "|blocked_by|" + fm.ID + "->" + id
				if r.diagNotified(key) {
					continue
				}
				r.logger.Printf("health %s: TASK-%s blocked_by references missing TASK-%s", projectEntry.Name(), fm.ID, id)
				r.notifyDiag(projectEntry.Name(), "依赖引用失效",
					"TASK-"+fm.ID+" 的 blocked_by 引用了不存在的 TASK-"+id+"，依赖永不满足；请修正引用（otg update-status blocked_by=...）")
			}
		}
	}
}

// closedNeverSatisfiable reports whether a closed upstream blocks its
// downstream forever. Closure reasons accept both the documented hyphenated
// form and the legacy underscored form.
func closedNeverSatisfiable(status, reason, replacement string) bool {
	if status != "closed" {
		return false
	}
	normalized := strings.ReplaceAll(reason, "-", "_")
	switch normalized {
	case "already_implemented":
		return false
	case "duplicate":
		return replacement == ""
	default:
		return true
	}
}

// depIDs returns parseable task identities and lifecycle metadata, plus ids
// whose files exist but are temporarily unparsable. The closure metadata lets
// dependency diagnostics distinguish delivered already-implemented closures
// from genuinely unsatisfiable cancelled/wont-fix closures.
func (r *Runner) depIDs(tasksDir string) (ids map[string]bool, statusByID, closedReason, closedReplacement map[string]string, unparsable map[string]bool, err error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	ids = make(map[string]bool, len(entries))
	statusByID = make(map[string]string, len(entries))
	closedReason = make(map[string]string, len(entries))
	closedReplacement = make(map[string]string, len(entries))
	unparsable = make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil || fm.ID == "" {
			// 文件存在但无法解析：按文件名前缀提取 id，供调用方区分
			// "暂时解析失败" 与 "任务不存在"。
			id := strings.TrimPrefix(entry.Name(), "TASK-")
			if i := strings.IndexByte(id, '-'); i >= 0 {
				id = id[:i]
			}
			if id != "" {
				unparsable[id] = true
			}
			continue
		}
		ids[fm.ID] = true
		statusByID[fm.ID] = fm.Status
		closedReason[fm.ID] = fm.ClosureReason
		closedReplacement[fm.ID] = fm.ReplacementTask
	}
	return ids, statusByID, closedReason, closedReplacement, unparsable, nil
}

// detectPlanFileOverlaps warns when two concurrently implementing tasks of
// the same project plan to modify the same file (Round 1 writes plan_files).
// The conflict would otherwise only surface at merge time — this moves the
// signal to scheduling time so the owner can serialize or split ownership.
func (r *Runner) detectPlanFileOverlaps() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		byFile := make(map[string][]string)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.Status != "implementing" || len(fm.PlanFiles) == 0 {
				continue
			}
			for _, f := range fm.PlanFiles {
				byFile[f] = append(byFile[f], fm.ID)
			}
		}
		for file, ids := range byFile {
			if len(ids) < 2 {
				continue
			}
			key := projectEntry.Name() + "|overlap|" + file
			if r.diagNotified(key) {
				continue
			}
			r.logger.Printf("health %s: plan file overlap %s: TASK-%s", projectEntry.Name(), file, strings.Join(ids, ", TASK-"))
			r.notifyDiag(projectEntry.Name(), "计划文件重叠",
				"TASK-"+strings.Join(ids, " / TASK-")+" 计划修改同一文件 "+file+"，调度器已自动排队串行（按 stage/priority 顺序，前序实现会话结束后继续；重叠超过 max_overlap_wait_minutes 将放行并发，merge 冲突走自动修复兜底）")
		}
	}
}

// projectHealthDiagnostics summarizes per-project delivery health each scan
// and warns once per project per day when a stuck-queue signature appears:
//
//   - many merged-but-never-done tasks + a large in-flight queue → the
//     classic rebaseline trigger (status drifted from code facts);
//   - many in-flight tasks without a stage → dependency topology is lost and
//     auto-staging has nothing to work with;
//   - an in-progress phase holding too many tasks → split-worthy.
func (r *Runner) projectHealthDiagnostics() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		tasksDir := filepath.Join(projDir, "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		inFlight, stageEmpty, mergedNotDone := 0, 0, 0
		stageCount := make(map[string]int)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.Status == "" {
				continue // unparsable / stateless docs join no health statistic
			}
			switch fm.Status {
			case "implementing", "plan-review", "refining", "blocked", "ready", "planning", "needs-grilling":
				inFlight++
				if fm.Stage == "" {
					stageEmpty++
				}
				if fm.Stage != "" {
					stageCount[fm.Stage]++
				}
			}
			if fm.MergeStatus == "merged" && fm.Status != "done" && fm.Status != "closed" {
				mergedNotDone++
			}
		}
		r.logger.Printf("health %s: in-flight=%d stage-empty=%d merged-not-done=%d", projectEntry.Name(), inFlight, stageEmpty, mergedNotDone)
		if inFlight == 0 {
			continue
		}

		today := time.Now().Format("2006-01-02")
		if mergedNotDone >= rebaselineWarnMergedNotDone && inFlight >= rebaselineWarnInFlight {
			if !r.diagNotified(projectEntry.Name() + "|rebaseline|" + today) {
				r.notifyDiag(projectEntry.Name(), "交付健康预警",
					"in-flight="+itoa(inFlight)+" 个任务，其中 "+itoa(mergedNotDone)+" 个已合入但未收口——状态与代码事实脱节，建议运行 project-rebaseline skill 收口并重排阶段")
			}
		}
		if stageEmpty >= stageEmptyWarn && !r.diagNotified(projectEntry.Name()+"|stage-empty|"+today) {
			r.notifyDiag(projectEntry.Name(), "阶段缺失预警",
				itoa(stageEmpty)+" 个进行中任务无 stage，依赖拓扑失效；建议 otg stage-plan init 或由 PM 统筹归组")
		}
		// Phase oversize: in-progress phase holding more tasks than a single
		// delivery can absorb (split-worthy signal).
		planPath := filepath.Join(projDir, "Notes", stagePlanName)
		if data, err := os.ReadFile(planPath); err == nil {
			for _, phase := range parseStagePlan(string(data)) {
				if phase.Status != "in-progress" {
					continue
				}
				stageID := stageIDFor(phase.Name)
				if stageID != "" && stageCount[stageID] > phaseOversizeWarn && !r.diagNotified(projectEntry.Name()+"|oversize|"+today) {
					r.notifyDiag(projectEntry.Name(), "阶段规模预警",
						"阶段 "+phase.Name+" 持有 "+itoa(stageCount[stageID])+" 个任务，超过单阶段建议上限；建议 PM 拆阶段或收窄范围")
				}
			}
		}
	}
}

// diagNotified reports (and marks) whether a diagnostic toast was already
// sent for key — one-shot per key, keys carry dates for daily resets.
func (r *Runner) diagNotified(key string) bool {
	if _, ok := r.diagNotifyAt.Load(key); ok {
		return true
	}
	r.diagNotifyAt.Store(key, time.Now())
	return false
}

// notifyDiag sends a desktop toast (if enabled) for a diagnostic warning.
func (r *Runner) notifyDiag(project, title, msg string) {
	notify.SendTaskAction(project, project, "⚠️", title, msg, r.cfg.Notifications.Desktop)
}

// itoa is a tiny helper keeping the diagnostics strings readable.
func itoa(n int) string {
	return strconv.Itoa(n)
}
