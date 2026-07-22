package task

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestOnReqChanged_NeedsGrilling_PendingReq(t *testing.T) {
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqsDir := filepath.Join(projDir, "Requirements")
	os.MkdirAll(tasksDir, 0755)
	os.MkdirAll(reqsDir, 0755)

	reqPath := filepath.Join(reqsDir, "REQ-099-test-req.md")
	os.WriteFile(reqPath, []byte(`---
id: "099"
title: Test Requirement
---
# Test Requirement
要做什么: test
`), 0644)

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

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md")
	if len(results) != 1 {
		t.Fatalf("expected 1 affected result, got %d", len(results))
	}

	r := results[0]
	if r.Action != "pending_req" {
		t.Errorf("Action = %q, want pending_req", r.Action)
	}
	if r.OldStatus != "needs-grilling" {
		t.Errorf("OldStatus = %q, want needs-grilling", r.OldStatus)
	}

	// Verify: status stays needs-grilling, only pending_req is set
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if fm.Status != "needs-grilling" {
		t.Errorf("Status = %q, want needs-grilling (stays)", fm.Status)
	}
	if !fm.PendingReq {
		t.Error("PendingReq should be true after REQ change")
	}
	// grill state stays — only pending_req is set
	if !fm.PlanApproved {
		t.Error("PlanApproved should stay true")
	}
	if !fm.GrillDone {
		t.Error("GrillDone should stay true")
	}
	if fm.GrillContext == "" {
		t.Error("GrillContext should not be cleared")
	}
}

func TestOnReqChanged_NeedsGrilling_GrillDoneStillTrue(t *testing.T) {
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqsDir := filepath.Join(projDir, "Requirements")
	os.MkdirAll(tasksDir, 0755)
	os.MkdirAll(reqsDir, 0755)

	reqPath := filepath.Join(reqsDir, "REQ-100-test-req.md")
	os.WriteFile(reqPath, []byte(`---
id: "100"
title: Test Req 100
---
# Test Req 100
`), 0644)

	taskContent := `---
id: "100"
title: Test Task 100
project: test
status: needs-grilling
plan_approved: false
grill_done: false
grill_context: ""
assignee: gpt
req_doc: Projects/001-test/Requirements/REQ-100-test-req
---
# TASK-100
`
	taskPath := filepath.Join(tasksDir, "TASK-100-test.md")
	os.WriteFile(taskPath, []byte(taskContent), 0644)

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-100-test-req.md")
	if len(results) != 1 {
		t.Fatalf("expected 1 affected result, got %d", len(results))
	}
	if results[0].Action != "pending_req" {
		t.Fatalf("Action = %q, want pending_req", results[0].Action)
	}

	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)

	if fm.Status != "needs-grilling" {
		t.Errorf("Status = %q, want needs-grilling", fm.Status)
	}
	if !fm.PendingReq {
		t.Error("PendingReq should be true after REQ change")
	}
}
