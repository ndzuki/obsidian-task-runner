package task

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindGrillingTasksCarriesDependencyFacts guards the dependency-closure
// fields the PM consolidate session consumes via <dependency_context>: without
// blocked_by/blocks/stage/design_replan_version the PM cannot triage
// cross-REQ conflicts before asking the user another round of questions.
func TestFindGrillingTasksCarriesDependencyFacts(t *testing.T) {
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-demo", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
id: "065"
title: Dev env
project: demo
req_doc: Projects/001-demo/Requirements/REQ-065-dev.md
status: needs-grilling
blocked_by: ["071", "072"]
blocks: ["066"]
stage: P4
plan_version: 18
design_replan_version: 0
grill_parked: false
grill_repeat: 0
---
# TASK-065
`
	path := filepath.Join(tasksDir, "TASK-065-dev.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FindGrillingTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("FindGrillingTasks len=%d, want 1", len(got))
	}
	g := got[0]
	if g.ID != "065" || g.PlanVersion != 18 || g.DesignReplanVersion != 0 {
		t.Fatalf("replan facts not carried: %+v", g)
	}
	if len(g.BlockedBy) != 2 || g.BlockedBy[0] != "071" || len(g.Blocks) != 1 || g.Blocks[0] != "066" {
		t.Fatalf("dependency edges not carried: blocked_by=%v blocks=%v", g.BlockedBy, g.Blocks)
	}
	if g.Stage != "P4" {
		t.Fatalf("stage=%q, want P4", g.Stage)
	}
}
