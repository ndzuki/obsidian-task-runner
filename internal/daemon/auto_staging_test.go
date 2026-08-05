package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestProcessAutoStagingPhasesUnstagedTasks guards the deterministic
// staging loop: in-flight tasks without a stage get phases + frontmatter
// stage fields in one pass, no PM session involved.
func TestProcessAutoStagingPhasesUnstagedTasks(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTask := func(name, id, status, blockedBy string) {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nproject: test\nstatus: " + status + "\nblocked_by: [" + blockedBy + "]\n---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask("TASK-001-a.md", "001", "implementing", "")
	writeTask("TASK-002-b.md", "002", "implementing", "\"TASK-001\"")
	writeTask("TASK-003-c.md", "003", "done", "") // excluded

	runner := New(&config.Config{
		ObsidianVault: vault,
		StageMinPerPhase: 1,
		StageMaxPhases:   4,
	})
	runner.logger = log.New(io.Discard, "", 0)

	if n := runner.processAutoStaging(); n != 2 {
		t.Fatalf("staged %d, want 2", n)
	}
	// Stage-Plan created with both phases.
	planPath := filepath.Join(projDir, "Notes", "Stage-Plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), "### Phase 1:") || !contains(string(data), "### Phase 2:") {
		t.Fatalf("stage plan missing phases:\n%s", string(data))
	}
	// frontmatter stage fields written.
	for name, want := range map[string]string{"TASK-001-a.md": "P1", "TASK-002-b.md": "P2"} {
		raw, err := os.ReadFile(filepath.Join(tasksDir, name))
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if fm.Stage != want {
			t.Fatalf("task %s stage = %q, want %q", name, fm.Stage, want)
		}
	}
	// Idempotent second run: nothing to stage.
	if n := runner.processAutoStaging(); n != 0 {
		t.Fatalf("second run staged %d, want 0 (idempotent)", n)
	}
}

// TestProcessAutoStagingAppendsToExistingPlan guards incremental behavior:
// a new unstaged task gets a NEW phase appended, existing phases untouched.
func TestProcessAutoStagingAppendsToExistingPlan(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	notesDir := filepath.Join(projDir, "Notes")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `---
id: "stage-plan"
project: test
status: active
updated: 2026-08-05T00:00:00+08:00
---

## 阶段列表

### Phase 1: 已定
- tasks: 001
- status: in-progress
`
	if err := os.WriteFile(filepath.Join(notesDir, "Stage-Plan.md"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTask := func(name, id, status string) {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nproject: test\nstatus: " + status + "\nstage: \"P1\"\n---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask("TASK-001-a.md", "001", "implementing") // staged P1
	// New task WITHOUT stage.
	content := "---\nid: \"009\"\nproject: test\nstatus: implementing\n---\n# 009\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-009-z.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{
		ObsidianVault: vault,
		StageMinPerPhase: 1,
		StageMaxPhases:   4,
	})
	runner.logger = log.New(io.Discard, "", 0)

	if n := runner.processAutoStaging(); n != 1 {
		t.Fatalf("staged %d, want 1 (only the new task)", n)
	}
	raw, err := os.ReadFile(filepath.Join(tasksDir, "TASK-009-z.md"))
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Stage != "P2" {
		t.Fatalf("new task stage = %q, want P2 (appended after existing Phase 1)", fm.Stage)
	}
	plan, err := os.ReadFile(filepath.Join(notesDir, "Stage-Plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(plan), "### Phase 1: 已定") || !contains(string(plan), "### Phase 2:") {
		t.Fatalf("existing phase must be preserved and new phase appended:\n%s", string(plan))
	}
}

// TestProjectNameFromTasksFallback guards the directory-name derivation:
// tasks without a frontmatter project field fall back to the parent
// directory name with the numeric prefix stripped.
func TestProjectNameFromTasksFallback(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No frontmatter project field.
	content := "---\nid: \"001\"\nstatus: implementing\n---\n# 001\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-001-a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectNameFromTasks(tasksDir); got != filepath.Base(dir) {
		t.Fatalf("projectNameFromTasks = %q, want directory name %q", got, filepath.Base(dir))
	}

	// Numeric-prefixed directory: 001-release-manager → release-manager.
	prefixed := filepath.Join(dir, "001-release-manager", "Tasks")
	if err := os.MkdirAll(prefixed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefixed, "TASK-002-b.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectNameFromTasks(prefixed); got != "release-manager" {
		t.Fatalf("projectNameFromTasks = %q, want release-manager", got)
	}

	// Frontmatter project wins over directory.
	withProj := filepath.Join(dir, "003-other", "Tasks")
	if err := os.MkdirAll(withProj, 0o755); err != nil {
		t.Fatal(err)
	}
	contentProj := "---\nid: \"003\"\nproject: realname\nstatus: implementing\n---\n# 003\n"
	if err := os.WriteFile(filepath.Join(withProj, "TASK-003-c.md"), []byte(contentProj), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectNameFromTasks(withProj); got != "realname" {
		t.Fatalf("projectNameFromTasks = %q, want frontmatter project realname", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
