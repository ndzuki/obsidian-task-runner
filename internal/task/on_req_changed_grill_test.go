package task

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestOnReqChanged_NeedsGrilling_ResetToReady(t *testing.T) {
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqsDir := filepath.Join(projDir, "Requirements")
	os.MkdirAll(tasksDir, 0755)
	os.MkdirAll(reqsDir, 0755)

	// Create requirement doc
	reqPath := filepath.Join(reqsDir, "REQ-099-test-req.md")
	os.WriteFile(reqPath, []byte(`---
id: "099"
title: Test Requirement
---
# Test Requirement
要做什么: test
`), 0644)

	// Create task in needs-grilling with grill_done=true (stale)
	taskContent := `---
id: "099"
title: Test Task
project: test
status: needs-grilling
plan_approved: true
grill_done: true
grill_context: "old grilling context"
assignee: gpt
req_doc: Projects/001-test/Requirements/REQ-099-test-req
---
# TASK-099
`
	taskPath := filepath.Join(tasksDir, "TASK-099-test.md")
	os.WriteFile(taskPath, []byte(taskContent), 0644)

	// Trigger OnReqChanged — simulates requirement doc change
	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md")
	if len(results) != 1 {
		t.Fatalf("expected 1 affected result, got %d", len(results))
	}

	r := results[0]
	if r.Action != "reset_to_ready" {
		t.Errorf("Action = %q, want reset_to_ready (was falling into warn_only before fix)", r.Action)
	}
	if r.OldStatus != "needs-grilling" {
		t.Errorf("OldStatus = %q, want needs-grilling", r.OldStatus)
	}

	// Verify frontmatter after reset
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if fm.Status != "ready" {
		t.Errorf("Status = %q, want ready", fm.Status)
	}
	if fm.PlanApproved {
		t.Error("PlanApproved should be false after reset")
	}
	if fm.GrillDone {
		t.Error("GrillDone should be false after reset (was stale true)")
	}
	if fm.GrillContext != "" {
		t.Errorf("GrillContext = %q, want empty after reset", fm.GrillContext)
	}
}

func TestOnReqChanged_NeedsGrilling_NotStaleGrillDone(t *testing.T) {
	// Verify that after reset, the next daemon scan does NOT auto-transition
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqsDir := filepath.Join(projDir, "Requirements")
	os.MkdirAll(tasksDir, 0755)
	os.MkdirAll(reqsDir, 0755)

	// Create requirement
	reqPath := filepath.Join(reqsDir, "REQ-100-test.md")
	os.WriteFile(reqPath, []byte(`---
id: "100"
title: Test Req 100
---
# Test
要做什么: test
`), 0644)

	// Create task in needs-grilling
	taskContent := `---
id: "100"
title: Test 100
project: test
status: needs-grilling
plan_approved: false
grill_done: false
grill_context: "some context"
assignee: gpt
req_doc: Projects/001-test/Requirements/REQ-100-test
---
# TASK-100
`
	taskPath := filepath.Join(tasksDir, "TASK-100-test.md")
	os.WriteFile(taskPath, []byte(taskContent), 0644)

	// Trigger OnReqChanged
	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-100-test.md")
	if len(results) != 1 {
		t.Fatalf("expected 1 affected result, got %d", len(results))
	}
	if results[0].Action != "reset_to_ready" {
		t.Fatalf("Action = %q, want reset_to_ready", results[0].Action)
	}

	// Verify status=ready, grill_done=false
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)

	if fm.Status != "ready" {
		t.Fatalf("Status = %q, want ready", fm.Status)
	}

	// Now simulate the daemon's IsReady + find ready path:
	// Should be picked up as ready
	ready, err := FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready task, got %d", len(ready))
	}
	if ready[0].Status != "ready" {
		t.Errorf("ReadyTask.Status = %q, want ready", ready[0].Status)
	}
	if ready[0].GrillDone {
		t.Error("ReadyTask.GrillDone should be false after OnReqChanged reset")
	}
}
