package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// writePausedDecisionList writes a project decision list with status=paused —
// the project-level pause switch the user sets while the requirement is being
// thought through.
func writePausedDecisionList(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
id: "grilling-decisions"
project: test
status: paused
grill_continue: true
---

### D-1: REQ-025 — 问题
- 决策: 采纳方案 A
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPrepareBatchHoldsGrillingTasksWhenListPaused guards the project-level
// pause switch: with the decision list status=paused, every grilling-flow
// task of that project is held — parked tasks stay silent, un-parked tasks
// get no reminder, and a grill_continue=true task is NOT reset into refining.
// Only the user flipping the list status back to open resumes the pipeline —
// manually, or automatically when the associated REQ is updated (both are
// user-acting signals).
func TestPrepareBatchHoldsGrillingTasksWhenListPaused(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	listPath := filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md")

	// Parked task (dispute escalated into the list).
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 3)
	// Un-parked task waiting for grilling resolution.
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-026.md"), "026", "Projects/001-test/Requirements/REQ-026.md", "test", false, 0)
	// grill_continue=true task: offline answers ready, would re-enter refining.
	contPath := filepath.Join(tasksDir, "TASK-027.md")
	cont := "---\n" +
		"id: \"027\"\n" +
		"title: T027\n" +
		"project: test\n" +
		"req_doc: Projects/001-test/Requirements/REQ-027.md\n" +
		"status: needs-grilling\n" +
		"grill_done: false\n" +
		"grill_continue: true\n" +
		"grill_parked: false\n" +
		"grill_repeat: 0\n" +
		"---\n# T027\n"
	if err := os.WriteFile(contPath, []byte(cont), 0o644); err != nil {
		t.Fatal(err)
	}

	writePausedDecisionList(t, listPath)
	runner := &Runner{cfg: &config.Config{ObsidianVault: vault}, logger: log.New(io.Discard, "", 0)}
	tasks := []task.ReadyTask{
		{ID: "025", Project: "test", FilePath: filepath.Join(tasksDir, "TASK-025.md"), Status: "needs-grilling", GrillParked: true, Assignee: "default"},
		{ID: "026", Project: "test", FilePath: filepath.Join(tasksDir, "TASK-026.md"), Status: "needs-grilling", GrillParked: false, Assignee: "default"},
		{ID: "027", Project: "test", FilePath: contPath, Status: "needs-grilling", GrillContinue: true, Assignee: "default"},
	}
	if pending := runner.prepareBatch(tasks); len(pending) != 0 {
		t.Fatalf("paused list must hold all grilling tasks, got %d pending", len(pending))
	}
	// The grill_continue task must NOT have been reset to refining while paused.
	if fm := mustParse(t, contPath); fm.Status != "needs-grilling" || !fm.GrillContinue {
		t.Fatalf("grill_continue task mutated under paused list: status=%s continue=%v", fm.Status, fm.GrillContinue)
	}

	// Control: with the list open, the same grill_continue task is reset and
	// proceeds into refining dispatch. Only task 027 is re-fed — 025/026 would
	// trigger real Kitty decision-tab / reminder side effects in the open state.
	if err := yamlfrontmatter.Update(listPath, map[string]interface{}{"status": "open"}); err != nil {
		t.Fatal(err)
	}
	runner.prepareBatch(tasks[2:3])
	if fm := mustParse(t, contPath); fm.Status != "refining" || fm.GrillContinue {
		t.Fatalf("open list should reset grill_continue task to refining, got status=%s continue=%v", fm.Status, fm.GrillContinue)
	}
}

// TestGrillingPMHoldsDistributeWhenListPaused guards the pause switch in the
// PM dispatch path: a fully answered list with status=paused must NOT spawn a
// distribute session — answers only flow back once the pause lifts (user
// manually sets status=open, or daemon auto-activates on a REQ update).
func TestGrillingPMHoldsDistributeWhenListPaused(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 3)
	listPath := filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md")
	writePausedDecisionList(t, listPath)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}
	if n := runner.processGrillingConsolidation(context.Background()); n != 0 {
		t.Fatalf("processed = %d, want 0 (paused list must not distribute)", n)
	}
	if _, err := os.Stat(argsPath); err == nil {
		t.Fatal("paused list must not spawn a PM distribute session")
	}
}

// TestGrillingPMHoldsConsolidateWhenListPaused guards the pause switch in the
// consolidate path: a group of un-parked needs-grilling tasks sharing a
// req_doc would normally trigger a PM consolidate session, but with the
// project decision list status=paused no new disputes may be appended and no
// PM session spawns — the user is still thinking the requirement through.
func TestGrillingPMHoldsConsolidateWhenListPaused(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	// Two un-parked tasks sharing a req_doc → needsConsolidation=true.
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", false, 1)
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-026.md"), "026", "Projects/001-test/Requirements/REQ-025.md", "test", false, 1)
	listPath := filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md")
	writePausedDecisionList(t, listPath)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}
	if n := runner.processGrillingConsolidation(context.Background()); n != 0 {
		t.Fatalf("processed = %d, want 0 (paused list must not consolidate)", n)
	}
	if _, err := os.Stat(argsPath); err == nil {
		t.Fatal("paused list must not spawn a PM consolidate session")
	}
}
