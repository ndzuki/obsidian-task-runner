package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
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
		taskIDs, err := r.depIDs(tasksDir)
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

// depIDs returns the set of task ids present in a project's Tasks dir.
func (r *Runner) depIDs(tasksDir string) (map[string]bool, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(entries))
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
			continue
		}
		ids[fm.ID] = true
	}
	return ids, nil
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
				"TASK-"+strings.Join(ids, " / TASK-")+" 计划修改同一文件 "+file+"，并行合并可能冲突；建议串行或划分文件所有权")
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
