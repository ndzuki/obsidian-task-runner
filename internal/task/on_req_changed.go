package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	project_pkg "github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// REQ change types, annotated by the modifier (user/PM/refining session) in
// the requirement's change record (`> 变更类型:` line). The daemon routes
// already-delivered (done) tasks accordingly: breaking reopens the task,
// additive keeps it terminal and suggests a new TASK, cosmetic is ignored.
// An unannotated change defaults to breaking (conservative).
const (
	ReqChangeBreaking = "breaking"
	ReqChangeAdditive = "additive"
	ReqChangeCosmetic = "cosmetic"
)

// ActionReqAdditive marks a requirement delta that left a done task terminal.
const ActionReqAdditive = "req_additive"

// reqChangeTypeRE matches `> 变更类型:` lines in requirement change records.
var reqChangeTypeRE = regexp.MustCompile(`(?m)^>\s*变更类型:\s*(breaking|additive|cosmetic)`)

// reqChangeInfo reads the requirement once and returns its current SHA-256
// hash and the change type annotated on the LATEST change record. A read
// failure returns empty values so the caller falls back to conservative
// (breaking) handling.
func reqChangeInfo(vaultPath, reqRelPath string) (hash, changeType string) {
	data, err := os.ReadFile(filepath.Join(vaultPath, reqRelPath))
	if err != nil {
		return "", ""
	}
	sum := sha256.Sum256(data)
	hash = "sha256:" + hex.EncodeToString(sum[:])
	if m := reqChangeTypeRE.FindAllSubmatch(data, -1); len(m) > 0 {
		changeType = string(m[len(m)-1][1])
	}
	return hash, changeType
}

// REQFilenameRE matches REQ-<id>-<slug>.md
var reqFilenameRE = regexp.MustCompile(`^REQ-(?P<id>\d+)-(?P<slug>.+)\.md$`)

// ParseReqFilename parses the filename and returns (id, slug) or empty strings.
func ParseReqFilename(path string) (id, slug string) {
	name := filepath.Base(path)
	m := reqFilenameRE.FindStringSubmatch(name)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// TaskFilenameForReq derives the task filename from a requirement path.
func TaskFilenameForReq(reqRelPath string) string {
	id, slug := ParseReqFilename(reqRelPath)
	if id == "" {
		return ""
	}
	return fmt.Sprintf("TASK-%s-%s.md", id, slug)
}

// AffectedResult describes what happened to a task during on-req-changed.
type AffectedResult struct {
	TaskID    string `json:"task_id"`
	File      string `json:"file"`
	Action    string `json:"action"`
	OldStatus string `json:"old_status,omitempty"`
}

// OnReqChanged 处理需求文件变更并更新受影响的任务。
// defaultAssignee（vault-map 顶层 default_assignee）预写进新建 TASK 的
// 委派字段；为空则保持旧行为（blocked 等待人工补填 assignee）。
func OnReqChanged(vaultPath, reqRelPath, defaultAssignee string) []AffectedResult {
	projectsDir := filepath.Join(vaultPath, "Projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil
	}

	reqID, _ := ParseReqFilename(reqRelPath)
	// 一次读取：当前 REQ hash（供已吸收变更去重）+ 最近变更记录标注的类型。
	reqHash, reqChangeType := reqChangeInfo(vaultPath, reqRelPath)
	var affected []AffectedResult
	// True when any task's req_doc matched this REQ — guards the create
	// fallback: cosmetic/absorbed changes produce no results, and a task
	// whose filename does not match the canonical derivation must not be
	// duplicated by a new TASK.
	matchedAny := false
	direct := 0 // results produced by the direct req_doc pass (create fallback only considers these)
	// processed tracks every task path already written by the direct pass so
	// the reverse dependency propagation never double-updates it.
	processed := make(map[string]bool)
	projEntries, _ := os.ReadDir(projectsDir)
	for _, proj := range projEntries {
		if !proj.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			taskPath := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(taskPath)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				continue
			}
			if fm.ReqDoc == "" {
				continue
			}

			if fm.ID == reqID && sameProjectRequirementPath(fm.ReqDoc, reqRelPath) && normalizePath(fm.ReqDoc) != normalizePath(reqRelPath) {
				updates := requirementChangeUpdates(fm.Status)
				updates["req_doc"] = normalizeReqDoc(reqRelPath)
				if fm.Status == "done" {
					// Renaming the REQ reopens a delivered task just like a
					// breaking change: without the generation reset the
					// already-MERGED old PR would short-circuit the next
					// merge (generationResetUpdates rationale).
					for key, value := range generationResetUpdates(fm) {
						updates[key] = value
					}
				}
				if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
					fmt.Fprintf(os.Stderr, "Error renaming requirement on %s: %v\n", taskPath, err)
					continue
				}
				affected = append(affected, AffectedResult{TaskID: fm.ID, File: entry.Name(), Action: "rename_req", OldStatus: fm.Status})
				processed[taskPath] = true
				direct++
				continue
			}
			// Normalize paths for comparison
			taskReq := normalizePath(fm.ReqDoc)
			reqPath := normalizePath(reqRelPath)
			if !pathsMatch(taskReq, reqPath) {
				continue
			}
			matchedAny = true

			// The current REQ content already matches the task's last audit
			// hash — the change was absorbed by refining (or the write is a
			// refining/PM session writing back its own audit records). Skip
			// so those self-writes do not re-open tasks or re-notify —
			// unless the task is frozen in a stale terminal (done + newer
			// plan + unmerged checkpoint, TASK-018): absorbing would keep
			// the undelivered increment locked forever, so route it through
			// the done branch (breaking reopen) instead.
			if reqHash != "" && fm.RefineReqHash != "" && fm.RefineReqHash == reqHash {
				if fm.Status != "done" || fm.PlanVersion < 2 || fm.CheckpointCommit == "" {
					continue
				}
			}

			if res := applyReqChangeToTask(taskPath, fm, entry.Name(), reqChangeType); res != nil {
				affected = append(affected, *res)
				direct++
			}
			processed[taskPath] = true
		}
	}

	// Reverse propagation: REQ-B whose frontmatter depends_on includes this
	// REQ consumes its contracts. A breaking (or unannotated) change to
	// REQ-A must therefore re-open REQ-B's tasks for re-alignment — without
	// this, a merged upstream contract change is only discovered later by an
	// audit or a gate failure (TASK-058: REQ-058 vs merged TASK-079
	// canonical contracts diverged until the audit caught it).
	affected = append(affected, propagateReqChangeToDependents(vaultPath, reqID, reqChangeType, processed)...)

	// Fallback: auto-create task if no existing task matched
	if direct == 0 && !matchedAny {
		created := createTaskForReq(vaultPath, reqRelPath, defaultAssignee)
		if created != nil {
			affected = append(affected, *created)
		}
	}

	return affected
}

// applyReqChangeToTask applies the direct REQ-change updates for one task
// (the status switch formerly inlined in OnReqChanged) and reports the
// result. Branches that write nothing still report warn_only/additive so the
// caller's result accounting matches the historical contract — except the
// done+cosmetic case, which historically produced NO result and must stay
// silent (the daemon treats an absent result as "nothing to notify").
func applyReqChangeToTask(taskPath string, fm *yamlfrontmatter.Frontmatter, fileName, reqChangeType string) *AffectedResult {
	result := AffectedResult{TaskID: fm.ID, File: fileName, OldStatus: fm.Status}
	switch fm.Status {
	case "blocked", "ready", "refining", "planning", "needs-grilling":
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"pending_req": true}); err != nil {
			fmt.Fprintf(os.Stderr, "Error marking pending_req on %s: %v\n", taskPath, err)
			result.Action = "warn_only"
			return &result
		}
		result.Action = "pending_req"
		return &result
	case "plan-review":
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"status":            "refining",
			"pending_req":       true,
			"plan_approved":     false,
			"grill_done":        false,
			"grill_context":     "",
			"grill_prev_status": "",
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error resetting plan-review task %s: %v\n", taskPath, err)
			result.Action = "warn_only"
			return &result
		}
		result.Action = "reset_to_ready"
		return &result
	case "implementing":
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"pending_req":    true,
			"merge_approved": false,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error marking pending_req on %s: %v\n", taskPath, err)
			result.Action = "warn_only"
			return &result
		}
		result.Action = "pending_req"
		return &result
	case "review", "conflict":
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"status":         "refining",
			"pending_req":    true,
			"merge_approved": false,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting %s to refining on %s: %v\n", fm.Status, taskPath, err)
			result.Action = "warn_only"
			return &result
		}
		result.Action = "pending_req"
		return &result
	case "done":
		// 已交付终态任务的 REQ 变更按类型路由：breaking/未标注 →
		// 重开新一轮交付；additive → 保持终态，提示新建 TASK 承接
		// 增量；cosmetic → 忽略（不改契约，不打扰，零结果）。
		switch reqChangeType {
		case ReqChangeAdditive:
			result.Action = ActionReqAdditive
			return &result
		case ReqChangeCosmetic:
			return nil
		}
		updates := map[string]interface{}{"status": "refining", "pending_req": true}
		for key, value := range generationResetUpdates(fm) {
			updates[key] = value
		}
		if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting %s to refining on %s: %v\n", fm.Status, taskPath, err)
			result.Action = "warn_only"
			return &result
		}
		result.Action = "pending_req"
		return &result
	case "closed":
		// closed is terminal; REQ change does not reopen
		result.Action = "warn_only"
		return &result
	default:
		result.Action = "warn_only"
		return &result
	}
}

// propagateReqChangeToDependents marks tasks pending whose REQ depends_on
// the changed REQ. Only breaking (or unannotated) changes propagate —
// additive/cosmetic deltas are backward-compatible and dependents re-align
// the next time their own REQ is refined. The pass is conservative: it never
// reopens a done/closed dependent task (its own REQ did not change), but it
// does force re-alignment for every in-flight dependent so an upstream
// contract break cannot silently reach a later audit or gate failure.
func propagateReqChangeToDependents(vaultPath, reqID, reqChangeType string, processed map[string]bool) []AffectedResult {
	if reqID == "" || reqChangeType == ReqChangeAdditive || reqChangeType == ReqChangeCosmetic {
		return nil
	}
	projectsDir := filepath.Join(vaultPath, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var affected []AffectedResult
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		reqsDir := filepath.Join(projectsDir, proj.Name(), "Requirements")
		reqEntries, err := os.ReadDir(reqsDir)
		if err != nil {
			continue
		}
		for _, entry := range reqEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			reqPath := filepath.Join(reqsDir, entry.Name())
			data, err := os.ReadFile(reqPath)
			if err != nil {
				continue
			}
			rf, err := yamlfrontmatter.Parse(data)
			if err != nil || rf == nil || !containsReqID(rf.DependsOn, reqID) {
				continue
			}
			relReq := filepath.ToSlash(filepath.Join("Projects", proj.Name(), "Requirements", entry.Name()))
			tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
			tEntries, _ := os.ReadDir(tasksDir)
			for _, te := range tEntries {
				if te.IsDir() || !strings.HasSuffix(te.Name(), ".md") {
					continue
				}
				taskPath := filepath.Join(tasksDir, te.Name())
				if processed[taskPath] {
					continue
				}
				td, err := os.ReadFile(taskPath)
				if err != nil {
					continue
				}
				tfm, err := yamlfrontmatter.Parse(td)
				if err != nil || tfm == nil || tfm.ReqDoc == "" {
					continue
				}
				if !pathsMatch(normalizePath(tfm.ReqDoc), normalizePath(relReq)) {
					continue
				}
				if tfm.Status == "done" || tfm.Status == "closed" {
					continue // its own REQ unchanged; terminal stays terminal
				}
				updates := map[string]interface{}{"pending_req": true}
				switch tfm.Status {
				case "plan-review":
					updates["status"] = "refining"
					updates["plan_approved"] = false
					updates["grill_done"] = false
					updates["grill_context"] = ""
					updates["grill_prev_status"] = ""
				case "review", "conflict":
					updates["status"] = "refining"
					updates["merge_approved"] = false
				}
				if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
					fmt.Fprintf(os.Stderr, "Error propagating upstream REQ change to %s: %v\n", taskPath, err)
					continue
				}
				affected = append(affected, AffectedResult{
					TaskID: tfm.ID, File: te.Name(),
					Action: "pending_req_dependent", OldStatus: tfm.Status,
				})
				processed[taskPath] = true
			}
		}
	}
	return affected
}

// containsReqID reports whether the depends_on list contains the given
// requirement id (exact "065" match; substrings like "0650" must not match).
func containsReqID(dependsOn []string, reqID string) bool {
	for _, d := range dependsOn {
		if strings.TrimSpace(d) == reqID {
			return true
		}
	}
	return false
}

func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return strings.TrimSuffix(p, ".md")
}

// pathsMatch returns true only when both normalized paths are identical.
// Basename fallback is explicitly prohibited to avoid cross-project collisions.
func pathsMatch(a, b string) bool {
	return a == b
}

func normalizeReqDoc(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if filepath.Ext(path) == "" {
		path += ".md"
	}
	return path
}

func sameProjectRequirementPath(oldPath, newPath string) bool {
	oldClean := strings.Split(normalizeReqDoc(oldPath), "/")
	newClean := strings.Split(normalizeReqDoc(newPath), "/")
	return len(oldClean) >= 4 && len(newClean) >= 4 && oldClean[0] == "Projects" && newClean[0] == "Projects" && oldClean[1] == newClean[1]
}

func requirementChangeUpdates(status string) map[string]interface{} {
	updates := map[string]interface{}{"pending_req": true}
	switch status {
	case "plan-review":
		updates["status"] = "refining"
		updates["plan_approved"] = false
	case "review", "conflict", "done":
		updates["status"] = "refining"
		updates["merge_approved"] = false
	}
	return updates
}

// generationResetUpdates returns the frontmatter updates that reset a
// delivered task's generation facts when a breaking requirement change (or a
// REQ rename) reopens it for a new delivery round. The old PR/branch facts
// must not be reused: an already-MERGED old PR makes the merge flow converge
// to done immediately, so the new delivery could never be merged (TASK-018
// lesson). Round 2 writes the new branch afterwards and the merge flow
// creates a fresh PR.
func generationResetUpdates(fm *yamlfrontmatter.Frontmatter) map[string]interface{} {
	gen := fm.Generation
	if gen < 1 {
		gen = 1 // 兼容从未经 normalize 的旧文档
	}
	return map[string]interface{}{
		"generation":          gen + 1,
		"merge_approved":      false,
		"reopen_count":        fm.ReopenCount + 1,
		"target_branch":       "",
		"pr_url":              "",
		"merge_status":        "",
		"completed":           "",
		"knowledge_extracted": false,
		// 新交付代际不得继承上一代的知识提炼退避（retry_count/retry_until），
		// 否则重开后的正常提炼可能被旧 backoff 挡住。
		"knowledge_extract_retry_count": 0,
		"knowledge_extract_retry_until": "",
	}
}

func OnReqDeleted(vaultPath, reqRelPath string) []AffectedResult {
	var affected []AffectedResult
	projectsDir := filepath.Join(vaultPath, "Projects")
	projects, _ := os.ReadDir(projectsDir)
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, project.Name(), "Tasks")
		entries, _ := os.ReadDir(tasksDir)
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || normalizePath(fm.ReqDoc) != normalizePath(reqRelPath) {
				continue
			}
			// 不变量 #5：pending_req 仅在新 planning 成功后清 false。REQ 被
			// 删除时保持 pending_req 原值——若为 true（需求已变更但未吸收），
			// 人工 resume 后必须走 refining 重新规划，而不是拿旧实现继续。
			if err := yamlfrontmatter.Update(path, map[string]interface{}{
				"status":        "blocked",
				"plan_approved": false, "merge_approved": false, "resume_approved": false,
				"phase_error_code": "REQ_MISSING", "phase_error": "requirement document was deleted",
			}); err != nil {
				continue
			}
			affected = append(affected, AffectedResult{TaskID: fm.ID, File: entry.Name(), Action: "req_missing", OldStatus: fm.Status})
		}
	}
	return affected
}

// createTaskForReq 根据新需求自动创建 TASK 文件。
// defaultAssignee（vault-map default_assignee）预写任务委派；为空保持旧行为。
func createTaskForReq(vaultPath, reqRelPath, defaultAssignee string) *AffectedResult {
	id, slug := ParseReqFilename(reqRelPath)
	if id == "" || slug == "" {
		return nil
	}

	// Derive project directory from the requirement's path.
	// New structure: Projects/<project>/Requirements/REQ-xxx.md → project = <project>
	// Old structure:   Requirements/REQ-xxx.md → project = <id>-<slug> (backward compat)
	projectDir := deriveProjectDir(reqRelPath, id, slug)
	tasksDir := filepath.Join(vaultPath, "Projects", projectDir, "Tasks")
	reqDir := filepath.Join(vaultPath, "Projects", projectDir, "Requirements")
	targetName := TaskFilenameForReq(reqRelPath)
	if targetName == "" {
		return nil
	}
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		return nil
	}
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		return nil
	}

	if _, err := os.Stat(filepath.Join(tasksDir, targetName)); err == nil {
		return nil
	}

	// Read requirement for metadata
	reqPath := filepath.Join(vaultPath, reqRelPath)
	reqData, err := os.ReadFile(reqPath)
	if err != nil {
		return nil
	}
	reqContent := string(reqData)
	reqFM, _ := yamlfrontmatter.Parse(reqData)

	projName := ""
	projectID := project_pkg.ExtractProjectID(projectDir)
	priority := ""
	priorityAssessmentStatus := "pending"
	epic := ""
	reviewer := ""
	author := ""
	tagsList := []string{}
	stage := ""
	if reqFM != nil {
		projName = reqFM.Project
		if reqFM.ProjectID != "" {
			projectID = reqFM.ProjectID
		}
		priority = reqFM.Priority
		if priority != "" {
			priorityAssessmentStatus = "completed"
		}
		epic = reqFM.Epic
		reviewer = reqFM.Reviewer
		author = reqFM.Author
		tagsList = reqFM.Tags
		stage = reqFM.Stage // stage 从 REQ 继承；拆分落地时 PM 写入子 REQ
	}

	// Resolve project field for vault-map matching.
	// Priority: REQ frontmatter → vault-map match on projectDir → projectDir fallback.
	if projName == "" {
		projName = resolveProjectField(projectDir)
	}

	title := firstHeading(reqContent)
	if title == "" {
		title = strings.ReplaceAll(slug, "-", " ")
	}

	now := time.Now().Format("2006-01-02T15:04:05-07:00")
	// 模型委派：vault-map default_assignee 预写 assignee 使任务可直接调度；
	// 为空则保持人工补填门禁。
	assignee := defaultAssignee
	assigneeStatus := "✅ 已委派（可改）"
	assigneeNote := ""
	if assignee == "" {
		assigneeStatus = "🔴 必填（vault-map.json models 的 key，如 default）"
		assigneeNote = "> ⚠️ **任务已暂停在 blocked。** 请在 frontmatter 中补 `assignee` 后保存，daemon 自动进入 refining → maturity gate。"
	} else {
		assigneeNote = "> ✅ **任务已创建并默认委派 `" + assignee + "`**——daemon 下一轮 scan 自动解锁为 ready，进入 priority 评估 → refining → maturity gate。如需换模型，改 `assignee` 后保存即可。"
	}

	// Build task markdown
	tags := ""
	if len(tagsList) > 0 {
		tags = "  - " + strings.Join(tagsList, "\n  - ")
	} else {
		tags = "  - "
	}

	summary := extractSection(reqContent, "要做什么")
	ac := extractSection(reqContent, "完成标准", "验收标准")

	taskMD := fmt.Sprintf(`---
id: "%s"
title: "%s"
project: "%s"
project_id: "%s"
task_schema_version: 1
template: ""
status: blocked
plan_approved: false
merge_approved: false
adr_approved: false
resume_approved: false
close_approved: false
pending_req: false
maturity: ""
refine_version: 0
refine_req_hash: ""
plan_req_hash: ""
plan_version: 0
checkpoint_commit: ""
refine_retry_count: 0
refine_error: ""
planning_retry_count: 0
blocked_phase: ""
phase_error: ""
phase_error_code: ""
phase_log: ""
grill_owner: ""
grill_started_at: ""
grill_heartbeat_at: ""
grill_timeout_minutes: 30
grill_done: false
grill_resolution: ""
grill_context: ""
grill_prev_status: ""
req_refine_count: 0
auto_approve: true
auto_merge: true
off_peak_only: false
created: "%s"
updated: "%s"
completed: ""
priority: "%s"
priority_assessment_status: %s
priority_assessment_attempts: 0
priority_assessment_started_at: ""
priority_assessed_at: ""
priority_assessed_value: ""
priority_impact: ""
priority_urgency: ""
priority_workaround: ""
priority_score: 0
priority_confidence: ""
priority_reason: ""
priority_recommendation: ""
due_date: ""
estimated_hours: 0
actual_hours: 0
assignee: "%s"
reviewer: "%s"
req_doc: %s
author: "%s"
tags:
%s
epic: "%s"
parent: ""
blocked_by: []
target_branch: ""
pr_url: ""
reopen_count: 0
merge_status: ""
approved_head: ""
merge_retry_count: 0
merge_retry_not_before: ""
target_env: staging
stage: "%s"
review_feedback: ""
rework_resolution: ""
closure_reason: ""
closure_note: ""
replacement_task: ""
remote_create: false
github_owner: ""
repository_name: ""
repository_visibility: ""
repository_description: ""
---

# TASK-%s: %s

## 需求摘要

%s

## 验收标准

%s

## 人工 Review 提醒

自动从 %s 生成。请确认以下字段：

| 字段 | 当前值 | 需要填？ |
|------|--------|---------|
| project | %s | %s |
| assignee | %s | %s |

%s

---

## 需求成熟度评估
<!-- 🤖 refining Skill 写入 -->

---

## 执行摘要
| 轮次 | 阶段 | 计划版本 | 状态 | 时间戳 |
|------|------|---------|------|--------|
| 1 | — | v0 | ⏳ blocked（等待填字段） | —

---

## 实现计划
### v1 · PENDING

---

## 实现记录
### Round 1 · PENDING

---

## 验收记录
### Round 1 · PENDING

---

## 变更记录
1. %s — 任务创建，status=blocked
`, id, title, projName, projectID, now, now, priority, priorityAssessmentStatus,
		assignee, reviewer, reqRelPath, author, tags, epic, stage, id, title, summary, ac, reqRelPath,
		projName, map[bool]string{true: "✅", false: "🔴 必填"}[projName != ""], assignee, assigneeStatus, assigneeNote, "`"+now+"`")

	targetPath := filepath.Join(tasksDir, targetName)
	if err := os.WriteFile(targetPath, []byte(taskMD), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating task: %v\n", err)
		return nil
	}

	return &AffectedResult{
		TaskID: id, File: targetName, Action: "create_task",
	}
}

// deriveProjectDir extracts the project directory name from a requirement path.
// New structure: "Projects/001-release-manager/Requirements/REQ-002-demo.md" → "001-release-manager"
// Old structure: "Requirements/REQ-001-demo.md" → "001-demo" (backward compatible)
func deriveProjectDir(reqRelPath, id, slug string) string {
	// Require "Projects/" prefix for the new structure
	projPrefix := "Projects/"
	if strings.HasPrefix(reqRelPath, projPrefix) {
		rest := strings.TrimPrefix(reqRelPath, projPrefix)
		// rest = "001-release-manager/Requirements/REQ-002-demo.md"
		idx := strings.Index(rest, string(filepath.Separator))
		if idx > 0 {
			return rest[:idx]
		}
	}
	// Old flat structure: use id-slug as project directory
	return fmt.Sprintf("%s-%s", id, slug)
}

// resolveProjectField maps a Vault project directory name to a vault-map project key.
// Falls back to projectDir if vault-map is unavailable or no match found.
func resolveProjectField(projectDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return projectDir
	}
	mapFile := filepath.Join(home, ".dsh", "skills", "obsidian-task-runner", "config", "vault-map.json")
	if mapped := project_pkg.MatchVaultDir(mapFile, projectDir); mapped != "" {
		return mapped
	}
	return projectDir
}

func firstHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

func extractSection(content string, headings ...string) string {
	inSection := false
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			title := strings.TrimPrefix(trimmed, "## ")
			for _, h := range headings {
				if title == h {
					inSection = true
					break
				} else {
					inSection = false
				}
			}
			continue
		}
		if inSection && trimmed != "" && !strings.HasPrefix(trimmed, "<!--") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "<!-- 请从需求文档补充摘要 -->"
	}
	return strings.Join(lines, "\n")
}

// PrintAffected outputs affected results as JSON.
func PrintAffected(results []AffectedResult) {
	data, _ := json.Marshal(results)
	fmt.Println(string(data))
}
