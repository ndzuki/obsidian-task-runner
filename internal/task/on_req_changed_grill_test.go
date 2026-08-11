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
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}
	if err := os.MkdirAll(reqsDir, 0755); err != nil {
		t.Fatalf("create requirements directory: %v", err)
	}

	reqPath := filepath.Join(reqsDir, "REQ-099-test-req.md")
	if err := os.WriteFile(reqPath, []byte(`---
id: "099"
title: Test Requirement
---
# Test Requirement
要做什么: test
`), 0644); err != nil {
		t.Fatalf("write requirement: %v", err)
	}

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
	if err := os.WriteFile(taskPath, []byte(taskContent), 0644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
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
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}
	if err := os.MkdirAll(reqsDir, 0755); err != nil {
		t.Fatalf("create requirements directory: %v", err)
	}

	reqPath := filepath.Join(reqsDir, "REQ-100-test-req.md")
	if err := os.WriteFile(reqPath, []byte(`---
id: "100"
title: Test Req 100
---
# Test Req 100
`), 0644); err != nil {
		t.Fatalf("write requirement: %v", err)
	}

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
	if err := os.WriteFile(taskPath, []byte(taskContent), 0644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-100-test-req.md", "")
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

func TestOnReqChanged_RenameUpdatesCanonicalTask(t *testing.T) {
	vault := t.TempDir()
	projectDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projectDir, "Tasks")
	reqsDir := filepath.Join(projectDir, "Requirements")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	if err := os.MkdirAll(reqsDir, 0o755); err != nil {
		t.Fatalf("create requirements: %v", err)
	}
	newReq := filepath.Join(reqsDir, "REQ-099-renamed.md")
	if err := os.WriteFile(newReq, []byte("# Renamed\n"), 0o644); err != nil {
		t.Fatalf("write REQ: %v", err)
	}
	taskPath := writeTask(t, tasksDir, "TASK-099-old.md", `
id: "099"
project: test
status: plan-review
assignee: gpt
req_doc: Projects/001-test/Requirements/REQ-099-old.md
plan_approved: true
`)

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-renamed.md", "")
	if len(results) != 1 || results[0].Action != "rename_req" {
		t.Fatalf("results = %+v, want rename_req", results)
	}
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)
	if fm.ReqDoc != "Projects/001-test/Requirements/REQ-099-renamed.md" || fm.Status != "refining" || fm.PlanApproved {
		t.Fatalf("renamed TASK = %+v", fm)
	}
}

func TestOnReqDeletedBlocksCanonicalTask(t *testing.T) {
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	taskPath := writeTask(t, tasksDir, "TASK-100-missing.md", `
id: "100"
project: test
status: review
assignee: gpt
req_doc: Projects/001-test/Requirements/REQ-100-missing.md
plan_approved: true
merge_approved: true
`)

	results := OnReqDeleted(vault, "Projects/001-test/Requirements/REQ-100-missing.md")
	if len(results) != 1 || results[0].Action != "req_missing" {
		t.Fatalf("results = %+v, want req_missing", results)
	}
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)
	if fm.Status != "blocked" || fm.PhaseErrorCode != "REQ_MISSING" || fm.PlanApproved || fm.MergeApproved {
		t.Fatalf("deleted REQ TASK = %+v", fm)
	}
}
