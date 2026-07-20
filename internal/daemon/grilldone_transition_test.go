package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestGrillDoneTransition_PlanApproved(t *testing.T) {
	// Simulates TASK-069: needs-grilling + plan_approved=true
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-release-manager", "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	taskContent := `---
id: "069"
title: Artifact Lifecycle Policy
project: release-manager
project_id: "001"
status: needs-grilling
plan_approved: true
merge_approved: false
grill_done: false
grill_context: "test context"
assignee: gpt
priority: P2
---
# TASK-069
`
	taskPath := filepath.Join(tasksDir, "TASK-069-artifact-lifecycle-policy.md")
	if err := os.WriteFile(taskPath, []byte(taskContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Test 1: FindReadyTasks picks it up with correct fields
	ready, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready task, got %d", len(ready))
	}

	rt := ready[0]
	if rt.ID != "069" {
		t.Errorf("ID = %q, want %q", rt.ID, "069")
	}
	if rt.Status != "needs-grilling" {
		t.Errorf("Status = %q, want %q", rt.Status, "needs-grilling")
	}
	if !rt.PlanApproved {
		t.Error("PlanApproved should be true")
	}
	if rt.GrillDone {
		t.Error("GrillDone should be false initially")
	}

	// Test 2: Simulate the transition (exactly what prepareBatch does)
	transitioned := false
	if rt.Status == "needs-grilling" {
		if rt.GrillDone || rt.PlanApproved {
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"status":            "plan-review",
				"grill_done":        true,
				"grill_context":     "",
				"grill_prev_status": "",
			}); err != nil {
				t.Fatal(err)
			}
			transitioned = true
		}
	}
	if !transitioned {
		t.Fatal("expected transition to plan-review")
	}

	// Test 3: Verify frontmatter after transition
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm == nil {
		t.Fatal("frontmatter is nil")
	}

	if fm.Status != "plan-review" {
		t.Errorf("Status = %q, want plan-review", fm.Status)
	}
	if !fm.GrillDone {
		t.Error("GrillDone should be true after transition")
	}
	if fm.GrillContext != "" {
		t.Errorf("GrillContext = %q, want empty", fm.GrillContext)
	}
	if !fm.PlanApproved {
		t.Error("PlanApproved should be preserved")
	}

	// Test 4: Next scan picks it up as plan-review (Round 2 ready)
	ready2, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready2) != 1 {
		t.Fatalf("expected 1 ready task for Round 2, got %d", len(ready2))
	}
	if ready2[0].Status != "plan-review" {
		t.Errorf("Status = %q, want plan-review", ready2[0].Status)
	}
	if !ready2[0].GrillDone {
		t.Error("GrillDone should be true in ReadyTask")
	}
}

func TestGrillDoneTransition_StillWaiting(t *testing.T) {
	// Both false → should NOT transition
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-release-manager", "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	taskContent := `---
id: "070"
title: Still Grilling
project: release-manager
status: needs-grilling
plan_approved: false
grill_done: false
grill_context: "still needs grilling"
assignee: gpt
---
# TASK-070
`
	taskPath := filepath.Join(tasksDir, "TASK-070-still-grilling.md")
	if err := os.WriteFile(taskPath, []byte(taskContent), 0644); err != nil {
		t.Fatal(err)
	}

	ready, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready task, got %d", len(ready))
	}

	rt := ready[0]
	if rt.GrillDone || rt.PlanApproved {
		t.Error("should NOT transition when both grill_done and plan_approved are false")
	} else {
		t.Log("correctly skipped — still waiting for grilling")
	}

	// Verify status unchanged
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "needs-grilling" {
		t.Errorf("Status = %q, want needs-grilling", fm.Status)
	}
}

func TestGrillDoneTransition_GrillDoneOnly(t *testing.T) {
	// grill_done=true, plan_approved=false → transitions to plan-review, but NOT Round 2 ready
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-release-manager", "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	taskContent := `---
id: "071"
title: Grill Done Not Approved
project: release-manager
status: needs-grilling
plan_approved: false
grill_done: true
grill_context: ""
assignee: gpt
---
# TASK-071
`
	taskPath := filepath.Join(tasksDir, "TASK-071-grill-done.md")
	if err := os.WriteFile(taskPath, []byte(taskContent), 0644); err != nil {
		t.Fatal(err)
	}

	ready, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready task, got %d", len(ready))
	}

	rt := ready[0]
	if !rt.GrillDone {
		t.Fatal("GrillDone should be true")
	}

	// Transition to plan-review
	if rt.Status == "needs-grilling" {
		if rt.GrillDone || rt.PlanApproved {
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"status":            "plan-review",
				"grill_done":        true,
				"grill_context":     "",
				"grill_prev_status": "",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if fm.Status != "plan-review" {
		t.Errorf("Status = %q, want plan-review", fm.Status)
	}
	if fm.PlanApproved {
		t.Error("PlanApproved should still be false")
	}

	// plan-review without plan_approved → NOT ready for Round 2
	ready2, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready2) != 0 {
		t.Errorf("expected 0 ready tasks (plan-review without plan_approved), got %d", len(ready2))
	}
}

func TestGrillDoneTransition_ImplementingBounceNoPlan(t *testing.T) {
	// needs-grilling + grill_prev_status=implementing + plan_version=0
	// → auto-transition to plan-review (task needs a plan, not more grilling)
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-release-manager", "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	taskContent := `---
id: "072"
title: Bounced Without Plan
project: release-manager
project_id: "001"
status: needs-grilling
plan_approved: false
plan_version: 0
grill_done: false
grill_prev_status: implementing
grill_context: "implementation blocked — no plan"
assignee: gpt
---
# TASK-072
`
	taskPath := filepath.Join(tasksDir, "TASK-072-bounce-no-plan.md")
	if err := os.WriteFile(taskPath, []byte(taskContent), 0644); err != nil {
		t.Fatal(err)
	}

	ready, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready task, got %d", len(ready))
	}

	rt := ready[0]
	if rt.Status != "needs-grilling" {
		t.Fatalf("Status = %q, want needs-grilling", rt.Status)
	}
	if rt.GrillPrevStatus != "implementing" {
		t.Fatalf("GrillPrevStatus = %q, want implementing", rt.GrillPrevStatus)
	}
	if rt.PlanVersion != 0 {
		t.Fatalf("PlanVersion = %d, want 0", rt.PlanVersion)
	}
	if rt.GrillDone {
		t.Error("GrillDone should be false initially")
	}

	// Simulate prepareBatch: implementing bounce + plan_version=0 → plan-review
	transitioned := false
	if rt.Status == "needs-grilling" && rt.GrillPrevStatus == "implementing" && rt.PlanVersion == 0 {
		t.Log("bounced from implementing with no plan → auto plan-review")
		updates := map[string]interface{}{
			"status":            "plan-review",
			"grill_done":        true,
			"grill_context":     "",
			"grill_prev_status": "",
		}
		if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
			t.Fatal(err)
		}
		transitioned = true
	}
	if !transitioned {
		t.Fatal("expected auto-transition to plan-review")
	}

	// Verify frontmatter after transition
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatal("failed to parse frontmatter after transition")
	}

	if fm.Status != "plan-review" {
		t.Errorf("Status = %q, want plan-review", fm.Status)
	}
	if !fm.GrillDone {
		t.Error("GrillDone should be true after transition")
	}
	if fm.GrillContext != "" {
		t.Errorf("GrillContext = %q, want empty", fm.GrillContext)
	}
	if fm.GrillPrevStatus != "" {
		t.Errorf("GrillPrevStatus = %q, want empty", fm.GrillPrevStatus)
	}

	// plan-review without plan_approved → NOT ready for Round 2
	ready2, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready2) != 0 {
		t.Errorf("expected 0 ready tasks (plan-review without plan_approved), got %d", len(ready2))
	}
}
