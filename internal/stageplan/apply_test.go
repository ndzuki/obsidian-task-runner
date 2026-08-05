package stageplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestApplyUsesInjectedClock guards the ApplyOptions.Now injection: the
// Stage-Plan `updated` stamp must come from the injected clock, not
// time.Now() (deterministic output for tests and daemon logs).
func TestApplyUsesInjectedClock(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Tasks")
	notesDir := filepath.Join(dir, "Notes")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: \"001\"\nproject: test\nstatus: implementing\n---\n# 001\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-001-a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 8, 5, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))

	res, err := Apply(tasksDir, notesDir, "test",
		Options{MinTasksPerPhase: 1, MaxPhases: 4},
		ApplyOptions{Now: fixed})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Phases) != 1 || res.Staged != 1 || !res.Created {
		t.Fatalf("apply result = %+v, want 1 phase / 1 staged / created", res)
	}
	plan, err := os.ReadFile(filepath.Join(notesDir, "Stage-Plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "updated: " + fixed.Format(time.RFC3339)
	if !strings.Contains(string(plan), want) {
		t.Fatalf("stage plan updated stamp = %q missing, want %q\n%s", want, want, string(plan))
	}
}

// TestApplyDryRunWritesNothing guards the dry-run path: phases are computed
// and returned, but neither the plan file nor the stage fields are touched.
func TestApplyDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Tasks")
	notesDir := filepath.Join(dir, "Notes")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: \"001\"\nproject: test\nstatus: implementing\n---\n# 001\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-001-a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(tasksDir, notesDir, "test",
		Options{MinTasksPerPhase: 1, MaxPhases: 4},
		ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Phases) != 1 {
		t.Fatalf("dry-run phases = %d, want 1", len(res.Phases))
	}
	if _, err := os.Stat(filepath.Join(notesDir, "Stage-Plan.md")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create Stage-Plan.md")
	}
	raw, err := os.ReadFile(filepath.Join(tasksDir, "TASK-001-a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stage:") {
		t.Fatal("dry-run must not write stage fields")
	}
}
