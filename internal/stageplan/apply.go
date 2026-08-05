package stageplan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ApplyResult describes one staging run.
type ApplyResult struct {
	Phases  []Phase
	Staged  int  // tasks whose stage field was written
	Created bool // Stage-Plan.md was created (not appended)
}

// ApplyOptions tunes a staging run.
type ApplyOptions struct {
	Force bool      // rewrite Stage-Plan.md from scratch and re-derive every in-flight task
	DryRun bool     // compute phases only; write nothing
	Now    time.Time // clock injection for tests (zero = time.Now)
}

// Apply derives delivery phases for a project's in-flight tasks and applies
// them: writes Notes/Stage-Plan.md (creating it, or appending phases for
// tasks that still have no stage) and backfills the frontmatter `stage`
// field on every affected task. Idempotent: when every in-flight task is
// already staged, it returns an empty result without touching anything.
func Apply(tasksDir, notesDir, project string, opts Options, ao ApplyOptions) (*ApplyResult, error) {
	tasks, err := CollectInFlightTasks(tasksDir)
	if err != nil {
		return nil, err
	}
	if ao.Force {
		for i := range tasks {
			tasks[i].Stage = ""
		}
	}
	if len(tasks) == 0 {
		return &ApplyResult{}, nil
	}

	phases := BuildPhases(tasks, opts)
	if len(phases) == 0 {
		return &ApplyResult{}, nil
	}

	res := &ApplyResult{Phases: phases}

	existing, _ := os.ReadFile(filepath.Join(notesDir, "Stage-Plan.md"))
	offset := 0
	if ao.Force {
		existing = nil
		res.Created = true
	} else if len(existing) > 0 {
		offset = maxPhaseNumber(string(existing))
	} else {
		res.Created = true
	}

	if ao.DryRun {
		return res, nil
	}

	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		return nil, err
	}
	now := ao.Now
	if now.IsZero() {
		now = time.Now()
	}
	if err := WriteStagePlan(notesDir, project, existing, phases, offset, now); err != nil {
		return nil, err
	}

	for _, p := range phases {
		stageID := fmt.Sprintf("P%d", offset+p.Number)
		for _, id := range p.Tasks {
			path := taskPathForID(tasksDir, id)
			if path == "" {
				continue
			}
			if err := yamlfrontmatter.Update(path, map[string]interface{}{"stage": stageID}); err != nil {
				return nil, fmt.Errorf("task %s: stage write: %w", id, err)
			}
			res.Staged++
		}
	}
	return res, nil
}

// CollectInFlightTasks reads every task document in the project's Tasks dir
// and projects the frontmatter fields the phaser needs. Done/closed tasks
// are excluded.
func CollectInFlightTasks(tasksDir string) ([]TaskInfo, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, err
	}
	var tasks []TaskInfo
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
		if fm.Status == "done" || fm.Status == "closed" {
			continue
		}
		info := TaskInfo{
			ID:       fm.ID,
			Title:    fm.Title,
			Epic:     fm.Epic,
			Priority: fm.Priority,
			Stage:    fm.Stage,
		}
		for _, ref := range fm.BlockedBy {
			id := strings.TrimPrefix(ref, "TASK-")
			if idx := strings.Index(id, ":"); idx > 0 {
				continue // cross-project reference: not a phase-order constraint
			}
			info.BlockedBy = append(info.BlockedBy, id)
		}
		tasks = append(tasks, info)
	}
	return tasks, nil
}

// taskPathForID maps a task id back to its file (files are
// TASK-<id>-<slug>.md; frontmatter id is authoritative).
func taskPathForID(tasksDir, id string) string {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-"+id+"-") && entry.Name() != "TASK-"+id+".md" {
			continue
		}
		return filepath.Join(tasksDir, entry.Name())
	}
	return ""
}

// maxPhaseNumber extracts the highest "### Phase N:" number from an existing
// stage plan so incremental runs continue numbering.
func maxPhaseNumber(content string) int {
	max := 0
	for _, line := range strings.Split(content, "\n") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "### Phase %d:", &n); err == nil && n > max {
			max = n
		}
	}
	return max
}

// WriteStagePlan creates Notes/Stage-Plan.md (or appends phases to an
// existing plan) in the pm-skill-compatible format the daemon parses.
func WriteStagePlan(notesDir, project string, existing []byte, phases []Phase, offset int, now time.Time) error {
	path := filepath.Join(notesDir, "Stage-Plan.md")
	var sb strings.Builder
	existingContent := string(existing)
	if len(existingContent) == 0 {
		fmt.Fprintf(&sb, `---
id: "stage-plan"
project: %s
status: active
updated: %s
---

# Stage Plan — %s

> 阶段化交付计划（依赖拓扑分层，`+"`otg stage-plan`"+`/daemon 自动生成）。
> 阶段归属权威判定 = TASK/REQ frontmatter `+"`stage`"+` 字段（daemon 按字段聚合检测完成）。
> 阶段收尾由 PM stage-review 评审，用户决定继续 / 补充建议到下一阶段 / 结束。

## 阶段列表

`, project, now.Format(time.RFC3339), project)
	} else {
		// Update the frontmatter `updated` stamp of the existing plan.
		sb.WriteString(updatePlanTimestamp(existingContent, now))
		sb.WriteString("\n")
	}

	for _, p := range phases {
		number := offset + p.Number
		status := "planned"
		if number == 1 {
			status = "in-progress"
		}
		fmt.Fprintf(&sb, "### Phase %d: %s\n", number, p.Name)
		fmt.Fprintf(&sb, "- 目标: （PM 补充一句话可演示成果）\n")
		fmt.Fprintf(&sb, "- tasks: %s（参考；权威判定按 stage 字段）\n", strings.Join(p.Tasks, ", "))
		fmt.Fprintf(&sb, "- status: %s\n", status)
		fmt.Fprintf(&sb, "- 评审: （待定）\n\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// updatePlanTimestamp refreshes the `updated:` frontmatter line of an
// existing Stage-Plan while keeping the rest verbatim.
func updatePlanTimestamp(content string, now time.Time) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "updated:") {
			lines[i] = "updated: " + now.Format(time.RFC3339)
			break
		}
	}
	return strings.Join(lines, "\n")
}
