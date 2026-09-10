package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

const documentationGateVersion = 1

type documentationGate struct {
	Version int
	Status  string
	Gaps    []string
	TaskID  string
	BatchID string
}

// processDocumentationClosures materializes PM-owned documentation gaps as
// ordinary REQ/TASK work before a stage decision can complete the project.
// It is deterministic and idempotent: the Stage-Review frontmatter records
// the generated task id, while a source marker on the generated REQ repairs
// the narrow crash window between task creation and that write-back.
func (r *Runner) processDocumentationClosures() int {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	created := 0
	for _, entry := range projects {
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, entry.Name())
		reviewPath := filepath.Join(projDir, "Notes", stageReviewName)
		gate, err := readDocumentationGate(reviewPath)
		if err != nil || gate.Version != documentationGateVersion || gate.Status != "gap" {
			continue
		}
		if _, wasCreated, err := r.ensureDocumentationClosureTask(projDir, reviewPath, gate); err != nil {
			r.logger.Printf("project %s: documentation closure: %v", entry.Name(), err)
		} else if wasCreated {
			created++
		}
	}
	return created
}

func readDocumentationGate(reviewPath string) (documentationGate, error) {
	data, err := os.ReadFile(reviewPath)
	if err != nil {
		return documentationGate{}, err
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return documentationGate{}, err
	}
	gate := documentationGate{
		Version: extraInt(fm.Extra["documentation_gate_version"]),
		Status:  strings.ToLower(strings.TrimSpace(extraString(fm.Extra["documentation_gate"]))),
		Gaps:    extraStrings(fm.Extra["documentation_gaps"]),
		TaskID:  strings.TrimPrefix(extraString(fm.Extra["documentation_task"]), "TASK-"),
		BatchID: extraString(fm.Extra["documentation_batch"]),
	}
	return gate, nil
}

func extraString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return ""
	}
}

func extraInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func extraStrings(v any) []string {
	var out []string
	switch xs := v.(type) {
	case []string:
		for _, x := range xs {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
	case []any:
		for _, v := range xs {
			if x := extraString(v); x != "" {
				out = append(out, x)
			}
		}
	}
	return out
}

// ensureDocumentationClosureTask returns the generated task id and whether
// this call created it. A versioned gate with an invalid status or an empty
// gap list is rejected instead of silently completing the project.
func (r *Runner) ensureDocumentationClosureTask(projDir, reviewPath string, gate documentationGate) (string, bool, error) {
	if gate.Version != documentationGateVersion {
		return "", false, nil // legacy Stage-Review: no deterministic gate
	}
	switch gate.Status {
	case "pass", "not_applicable":
		return "", false, nil
	case "gap":
	default:
		return "", false, fmt.Errorf("documentation_gate must be pass, gap, or not_applicable (got %q)", gate.Status)
	}
	if len(gate.Gaps) == 0 {
		return "", false, fmt.Errorf("documentation_gate=gap requires documentation_gaps")
	}
	reviewData, err := os.ReadFile(reviewPath)
	if err != nil {
		return "", false, err
	}
	reviewFM, err := yamlfrontmatter.Parse(reviewData)
	if err != nil || reviewFM == nil {
		return "", false, fmt.Errorf("parse stage review: %w", err)
	}
	reviewStage := strings.TrimSpace(reviewFM.Stage)
	if reviewStage == "" {
		return "", false, fmt.Errorf("stage review has no stage")
	}
	batchID := documentationBatchID(reviewStage, gate.Gaps)
	if gate.BatchID != "" && gate.BatchID != batchID {
		return "", false, fmt.Errorf("documentation_batch %q does not match current gaps", gate.BatchID)
	}

	planPath := filepath.Join(projDir, "Notes", stagePlanName)
	planData, err := os.ReadFile(planPath)
	if err != nil {
		return "", false, fmt.Errorf("read stage plan: %w", err)
	}
	targetStage, appendPhase, err := documentationTargetStage(string(planData), reviewStage)
	if err != nil {
		return "", false, err
	}
	// Crash recovery and idempotency use a stable batch identity (stage +
	// normalized gaps hash), not just the stage name. Repair both TASK stage
	// and Stage-Plan membership before writing the review marker.
	if id := findDocumentationClosureTask(projDir, batchID); id != "" {
		if err := repairDocumentationClosureState(projDir, planPath, string(planData), targetStage, appendPhase, id); err != nil {
			return "", false, err
		}
		if err := yamlfrontmatter.Update(reviewPath, map[string]any{"documentation_task": "TASK-" + id, "documentation_batch": batchID}); err != nil {
			return "", false, err
		}
		return id, false, nil
	}
	id, err := nextProjectWorkID(projDir)
	if err != nil {
		return "", false, err
	}
	project := strings.TrimSpace(reviewFM.Project)
	if project == "" {
		project = projectNameFromTasks(filepath.Join(projDir, "Tasks"))
	}
	if project == "" {
		project = filepath.Base(projDir)
	}
	assignee := r.documentationAssignee(projDir, reviewStage)
	reqName := fmt.Sprintf("REQ-%s-documentation-closure-%s.md", id, strings.ToLower(strings.ReplaceAll(reviewStage, " ", "-")))
	reqRel := filepath.Join("Projects", filepath.Base(projDir), "Requirements", reqName)
	reqPath := filepath.Join(r.cfg.ObsidianVault, reqRel)
	if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
		return "", false, err
	}
	now := time.Now().Format(time.RFC3339)
	var gapLines strings.Builder
	for _, gap := range gate.Gaps {
		fmt.Fprintf(&gapLines, "- [ ] %s\n", gap)
	}
	req := fmt.Sprintf(`---
id: "%s"
title: "项目交付文档收口（%s）"
project: "%s"
priority: P2
stage: "%s"
tags:
  - documentation
  - documentation-closure
documentation_closure: true
documentation_review_stage: "%s"
documentation_batch: "%s"
created: "%s"
updated: "%s"
---
# 项目交付文档收口（%s）

## 要做什么

补齐 PM 在 %s 阶段评审中发现的项目文档缺口。PM 对完整性负责；本任务 assignee 对技术事实、命令和配置示例的准确性负责。

## 完成标准

%s- [ ] README 或文档导航能从项目入口发现新增/既有能力
- [ ] 安装、配置、运行、验证和主要故障恢复步骤可由陌生使用者执行
- [ ] 文档中的命令、路径、配置键和默认行为已对照当前实现验证
- [ ] 不写入个人路径、私有网关、凭据、常驻服务名或其他个性化默认值
- [ ] 自动化只修改项目自有资产，不覆盖用户配置或环境

## 来源

- Stage Review: %s
- Documentation gate version: %d
`, id, reviewStage, project, targetStage, reviewStage, batchID, now, now, reviewStage, reviewStage, gapLines.String(), filepath.ToSlash(strings.TrimPrefix(reviewPath, r.cfg.ObsidianVault+string(os.PathSeparator))), documentationGateVersion)
	if err := os.WriteFile(reqPath, []byte(req), 0o644); err != nil {
		return "", false, fmt.Errorf("write documentation requirement: %w", err)
	}

	results := task.OnReqChanged(r.cfg.ObsidianVault, reqRel, assignee)
	taskPath := filepath.Join(projDir, "Tasks", task.TaskFilenameForReq(reqRel))
	if len(results) == 0 {
		if _, statErr := os.Stat(taskPath); statErr != nil {
			_ = os.Remove(reqPath)
			return "", false, fmt.Errorf("documentation task was not created")
		}
	}
	if appendPhase {
		if err := appendDocumentationPhase(planPath, string(planData), targetStage, id); err != nil {
			return "", false, err
		}
	} else {
		if err := appendTaskToStagePlan(planPath, string(planData), targetStage, id); err != nil {
			return "", false, err
		}
	}
	if err := yamlfrontmatter.Update(reviewPath, map[string]any{"documentation_task": "TASK-" + id, "documentation_batch": batchID}); err != nil {
		return "", false, err
	}
	r.logger.Printf("project %s: documentation closure TASK-%s created in %s", project, id, targetStage)
	return id, true, nil
}

func (r *Runner) documentationAssignee(projDir, reviewStage string) string {
	if r.cfg.DefaultAssignee != "" {
		return r.cfg.DefaultAssignee
	}
	stageID := stageIDFor(reviewStage)
	entries, _ := os.ReadDir(filepath.Join(projDir, "Tasks"))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(projDir, "Tasks", entry.Name()))
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err == nil && fm != nil && fm.Stage == stageID && fm.Assignee != "" {
			return fm.Assignee
		}
	}
	return ""
}

func documentationBatchID(reviewStage string, gaps []string) string {
	normalized := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		if gap = strings.Join(strings.Fields(gap), " "); gap != "" {
			normalized = append(normalized, gap)
		}
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(reviewStage) + "\n" + strings.Join(normalized, "\n")))
	return "doc-v1-" + hex.EncodeToString(sum[:8])
}

func findDocumentationClosureTask(projDir, batchID string) string {
	reqDir := filepath.Join(projDir, "Requirements")
	entries, _ := os.ReadDir(reqDir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "REQ-") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(reqDir, entry.Name()))
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil || extraString(fm.Extra["documentation_batch"]) != batchID {
			continue
		}
		taskPath := filepath.Join(projDir, "Tasks", task.TaskFilenameForReq(entry.Name()))
		if _, err := os.Stat(taskPath); err == nil {
			return fm.ID
		}
	}
	return ""
}

func repairDocumentationClosureState(projDir, planPath, plan, targetStage string, appendPhase bool, taskID string) error {
	taskPath := filepath.Join(projDir, "Tasks")
	entries, err := os.ReadDir(taskPath)
	if err != nil {
		return err
	}
	found := ""
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasPrefix(entry.Name(), "TASK-"+taskID+"-") || entry.Name() == "TASK-"+taskID+".md") {
			found = filepath.Join(taskPath, entry.Name())
			break
		}
	}
	if found == "" {
		return fmt.Errorf("documentation TASK-%s missing", taskID)
	}
	data, err := os.ReadFile(found)
	if err != nil {
		return err
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return fmt.Errorf("parse documentation TASK-%s: %w", taskID, err)
	}
	if fm.Stage != targetStage {
		if err := yamlfrontmatter.Update(found, map[string]any{"stage": targetStage}); err != nil {
			return err
		}
	}
	if appendPhase {
		return appendDocumentationPhase(planPath, plan, targetStage, taskID)
	}
	return appendTaskToStagePlan(planPath, plan, targetStage, taskID)
}

func nextProjectWorkID(projDir string) (string, error) {
	maxID := 0
	for _, subdir := range []string{"Requirements", "Tasks"} {
		entries, err := os.ReadDir(filepath.Join(projDir, subdir))
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		for _, entry := range entries {
			parts := strings.SplitN(entry.Name(), "-", 3)
			if len(parts) < 2 {
				continue
			}
			if n, err := strconv.Atoi(parts[1]); err == nil && n > maxID {
				maxID = n
			}
		}
	}
	return fmt.Sprintf("%03d", maxID+1), nil
}

func documentationTargetStage(plan, reviewStage string) (stageID string, appendPhase bool, err error) {
	phases := parseStagePlanContent(plan)
	current := -1
	maxStage := 0
	want := strings.TrimSpace(reviewStage)
	for i, phase := range phases {
		if id := stageIDFor(phase.Name); id != "" {
			if n, convErr := strconv.Atoi(strings.TrimPrefix(id, "P")); convErr == nil && n > maxStage {
				maxStage = n
			}
		}
		if strings.EqualFold(phase.Name, want) || strings.HasPrefix(phase.Name, want+":") {
			current = i
		}
	}
	if current < 0 {
		return "", false, fmt.Errorf("review stage %q not found in Stage-Plan", reviewStage)
	}
	if current+1 < len(phases) {
		id := stageIDFor(phases[current+1].Name)
		if id == "" {
			return "", false, fmt.Errorf("next phase has no stage id")
		}
		return id, false, nil
	}
	return fmt.Sprintf("P%d", maxStage+1), true, nil
}

// documentationGateAllowsDecision is the deterministic distribution guard.
// Legacy reviews (no version) remain compatible. A versioned gap only opens
// after its generated task is done+merged.
func documentationGateAllowsDecision(projDir, reviewPath string) bool {
	data, err := os.ReadFile(reviewPath)
	if err != nil {
		return false
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false
	}
	gate, err := readDocumentationGate(reviewPath)
	if err != nil {
		return false
	}
	if gate.Version == 0 {
		return true // successfully parsed legacy review
	}
	if gate.Version != documentationGateVersion {
		return false
	}
	switch gate.Status {
	case "pass", "not_applicable":
		return true
	case "gap":
		expectedBatch := documentationBatchID(strings.TrimSpace(fm.Stage), gate.Gaps)
		return gate.BatchID == expectedBatch && gate.TaskID != "" && documentationTaskLanded(projDir, gate.TaskID)
	default:
		return false
	}
}

func documentationTaskLanded(projDir, taskID string) bool {
	tasksDir := filepath.Join(projDir, "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasPrefix(entry.Name(), "TASK-"+taskID+"-") && entry.Name() != "TASK-"+taskID+".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
		if err != nil {
			return false
		}
		fm, err := yamlfrontmatter.Parse(data)
		return err == nil && fm != nil && fm.Status == "done" && fm.MergeStatus == "merged"
	}
	return false
}

func appendTaskToStagePlan(planPath, plan, stageID, taskID string) error {
	phases := parseStagePlanContent(plan)
	for _, phase := range phases {
		if stageIDFor(phase.Name) != stageID {
			continue
		}
		headings := stagePhaseRE.FindAllStringIndex(plan, -1)
		for i, loc := range headings {
			end := len(plan)
			if i+1 < len(headings) {
				end = headings[i+1][0]
			}
			block := plan[loc[0]:end]
			firstLine := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(block, "\n", 2)[0], "###"))
			if !strings.HasPrefix(firstLine, phase.Name) {
				continue
			}
			m := stageTasksRE.FindStringSubmatchIndex(block)
			if m == nil {
				return fmt.Errorf("stage %s has no tasks line", stageID)
			}
			current := block[m[2]:m[3]]
			if strings.Contains(current, taskID) {
				return nil
			}
			updated := current
			if strings.Contains(updated, "（") {
				parts := strings.SplitN(updated, "（", 2)
				updated = strings.TrimRight(parts[0], " ,") + ", " + taskID + "（" + parts[1]
			} else {
				updated = strings.TrimRight(updated, " ,") + ", " + taskID
			}
			out := plan[:loc[0]+m[2]] + updated + plan[loc[0]+m[3]:]
			return os.WriteFile(planPath, []byte(out), 0o644)
		}
	}
	return fmt.Errorf("target documentation stage %s not found", stageID)
}

func appendDocumentationPhase(planPath, plan, stageID, taskID string) error {
	n, err := strconv.Atoi(strings.TrimPrefix(stageID, "P"))
	if err != nil || n <= 0 {
		return fmt.Errorf("invalid documentation stage %q", stageID)
	}
	block := fmt.Sprintf("\n### Phase %d: 项目文档交付收口\n- 目标: 补齐最终文档缺口，使陌生使用者可独立安装、配置、运行与排障\n- tasks: %s（参考；权威判定按 stage 字段）\n- status: planned\n- 评审: （待定）\n", n, taskID)
	return os.WriteFile(planPath, []byte(strings.TrimRight(plan, "\n")+"\n"+block), 0o644)
}
