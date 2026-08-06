package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindPriorityTasks(t *testing.T) {
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	writeTask(t, tasksDir, "TASK-001-pending.md", `
id: "001"
title: Pending
project: test
status: blocked
assignee: ""
req_doc: Projects/001-test/Requirements/REQ-001-pending.md
priority: ""
priority_assessment_status: pending
`)
	writeTask(t, tasksDir, "TASK-002-running-stale.md", `
id: "002"
title: Stale
project: test
status: blocked
req_doc: Projects/001-test/Requirements/REQ-002-stale.md
priority: ""
priority_assessment_status: running
priority_assessment_started_at: "2026-07-28T09:00:00+08:00"
`)
	writeTask(t, tasksDir, "TASK-003-completed.md", `
id: "003"
title: Completed
project: test
status: blocked
req_doc: Projects/001-test/Requirements/REQ-003-completed.md
priority: P2
priority_assessment_status: completed
`)

	got, err := FindPriorityTasks(vault, time.Date(2026, 7, 28, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if err != nil {
		t.Fatalf("FindPriorityTasks: %v", err)
	}
	if len(got) != 2 || got[0].ID != "001" || got[1].ID != "002" {
		t.Fatalf("priority tasks = %+v, want pending and stale running", got)
	}
}

// TestFindPriorityTasksSkipsLateStageTasks guards the state filter: a task
// already in planning (or later) must not be picked up for assessment —
// priority drives scheduling only in the early stages, and assessing late
// tasks would spawn a wasted OMP session per scan.
func TestFindPriorityTasksSkipsLateStageTasks(t *testing.T) {
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	writeTask(t, tasksDir, "TASK-001-ready.md", `
id: "001"
title: Ready
project: test
status: ready
assignee: ""
req_doc: Projects/001-test/Requirements/REQ-001.md
priority: ""
priority_assessment_status: pending
`)
	writeTask(t, tasksDir, "TASK-002-planning.md", `
id: "002"
title: Planning
project: test
status: planning
assignee: ""
req_doc: Projects/001-test/Requirements/REQ-002.md
priority: ""
priority_assessment_status: pending
`)
	writeTask(t, tasksDir, "TASK-003-review.md", `
id: "003"
title: Review
project: test
status: review
assignee: ""
req_doc: Projects/001-test/Requirements/REQ-003.md
priority: ""
priority_assessment_status: pending
`)
	got, err := FindPriorityTasks(vault, time.Now())
	if err != nil {
		t.Fatalf("FindPriorityTasks: %v", err)
	}
	if len(got) != 1 || got[0].ID != "001" {
		t.Fatalf("priority tasks = %+v, want only the ready task", got)
	}
}
