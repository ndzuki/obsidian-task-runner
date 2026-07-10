package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func writeTask(dir, name, frontmatter string) string {
	path := filepath.Join(dir, name)
	content := "---\n" + strings.TrimSpace(frontmatter) + "\n---\n# Task\n"
	os.WriteFile(path, []byte(content), 0644)
	return path
}

func TestIsValidAssignee(t *testing.T) {
	if !IsValidAssignee("deepseek") {
		t.Error("deepseek should be valid")
	}
	if !IsValidAssignee("gemini") {
		t.Error("gemini should be valid (any non-empty)")
	}
	if IsValidAssignee("") {
		t.Error("empty should NOT be valid")
	}
}

func TestIsAutoUnblockable(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	os.MkdirAll(tasksDir, 0755)

	path := writeTask(tasksDir, "TASK-001.md", `
id: "001"
title: "Test"
project: my-project
status: blocked
assignee: deepseek
blocked_by: []
`)
	data, _ := os.ReadFile(path)
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatal("parse failed")
	}

	if !IsAutoUnblockable(fm) {
		t.Error("should be auto-unblockable")
	}

	// Test with missing assignee
	path2 := writeTask(tasksDir, "TASK-002.md", `
id: "002"
title: "Test"
project: my-project
status: blocked
assignee: ""
blocked_by: []
`)
	data2, _ := os.ReadFile(path2)
	fm2, _ := yamlfrontmatter.Parse(data2)
	if IsAutoUnblockable(fm2) {
		t.Error("should NOT be auto-unblockable without assignee")
	}
}

func TestFindReadyTasks(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Projects", "001-test", "Tasks")

	os.MkdirAll(tasksDir, 0755)
	// Ready task
	writeTask(tasksDir, "TASK-001-ready.md", `
id: "001"
title: "Ready Task"
project: my-app
status: ready
assignee: deepseek
priority: P1
`)

	// Blocked task with valid assignee → should be auto-unblocked
	writeTask(tasksDir, "TASK-002-blocked.md", `
id: "002"
title: "Blocked but Fillable"
project: my-app
status: blocked
assignee: deepseek
blocked_by: []
priority: P0
`)

	// Blocked with no assignee → not ready
	writeTask(tasksDir, "TASK-003-no-assignee.md", `
id: "003"
title: "No Assignee"
project: my-app
status: blocked
assignee: ""
blocked_by: []
`)

	tasks, err := FindReadyTasks(dir)
	if err != nil {
		t.Fatalf("FindReadyTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	// P0 should come first
	if tasks[0].ID != "002" {
		t.Errorf("first task id = %q, want 002 (P0 first)", tasks[0].ID)
	}
	if tasks[1].ID != "001" {
		t.Errorf("second task id = %q, want 001", tasks[1].ID)
	}
}
