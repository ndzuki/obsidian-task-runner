package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

func writeStagePlan(t *testing.T, notesDir, content string) string {
	t.Helper()
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(notesDir, "Stage-Plan.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseStagePlan(t *testing.T) {
	dir := t.TempDir()
	plan := writeStagePlan(t, filepath.Join(dir, "Notes"), `---
id: "stage-plan"
project: test
---

## 阶段列表

### Phase 1: 核心链路
- 目标: 可演示的发布流程
- tasks: TASK-003, TASK-005,TASK-006
- status: in-progress

### Phase 2: 扩展能力
- 目标: 审计与通知
- tasks: 029, 031
- status: planned
`)
	phases := parseStagePlan(plan)
	if len(phases) != 2 {
		t.Fatalf("parsed %d phases, want 2", len(phases))
	}
	if phases[0].Status != "in-progress" || phases[1].Status != "planned" {
		t.Fatalf("phase statuses = %q/%q", phases[0].Status, phases[1].Status)
	}
	wantTasks := []string{"003", "005", "006"}
	if len(phases[0].Tasks) != len(wantTasks) {
		t.Fatalf("phase 1 tasks = %v, want %v", phases[0].Tasks, wantTasks)
	}
	for i := range wantTasks {
		if phases[0].Tasks[i] != wantTasks[i] {
			t.Fatalf("phase 1 tasks = %v, want %v", phases[0].Tasks, wantTasks)
		}
	}
	if phases[1].Tasks[0] != "029" || phases[1].Tasks[1] != "031" {
		t.Fatalf("phase 2 tasks = %v", phases[1].Tasks)
	}
}

func TestParseStagePlanMalformedBlockSkipped(t *testing.T) {
	dir := t.TempDir()
	plan := writeStagePlan(t, filepath.Join(dir, "Notes"), `---
id: "stage-plan"
---

### Phase 1: 缺 tasks 行
- status: in-progress

### Phase 2: 完整
- tasks: 001
- status: planned
`)
	phases := parseStagePlan(plan)
	if len(phases) != 1 || !strings.HasPrefix(phases[0].Name, "Phase 2") {
		t.Fatalf("malformed block must be skipped, got %+v", phases)
	}
}

func TestStageTasksLanded(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskFM := func(name, id, status, mergeStatus, stage string) {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nstatus: " + status + "\nmerge_status: " + mergeStatus + "\nstage: \"" + stage + "\"\n---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTaskFM("TASK-001-a.md", "001", "done", "merged", "P1")
	writeTaskFM("TASK-002-b.md", "002", "done", "conflict-resolve-attempted", "P1")
	writeTaskFM("TASK-003-c.md", "003", "implementing", "", "P1")
	writeTaskFM("TASK-004-d.md", "004", "done", "merged", "P2") // other stage: ignored

	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)

	landed, reviewable, _ := runner.stageTasksState(dir, "P2")
	if !landed || !reviewable {
		t.Fatalf("P2 (single done+merged task) should land+reviewable, got landed=%v reviewable=%v", landed, reviewable)
	}
	landed, reviewable, _ = runner.stageTasksState(dir, "P1")
	if landed || reviewable {
		t.Fatalf("P1 has an in-flight task, must not be landed or reviewable, got landed=%v reviewable=%v", landed, reviewable)
	}
	landed, reviewable, _ = runner.stageTasksState(dir, "P9")
	if landed || reviewable {
		t.Fatalf("stage with no tasks must not land, got landed=%v reviewable=%v", landed, reviewable)
	}
}

// TestStageTasksStateBlockedRemainderReviewable guards the anti-deadlock
// relaxation: a phase whose remaining tasks are all blocked (nothing
// dispatchable) is reviewable even though not landed — the PM stage-review
// then advises wait / narrow / split instead of the phase staying silent.
func TestStageTasksStateBlockedRemainderReviewable(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskFM := func(name, id, status, mergeStatus, stage string) {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nstatus: " + status + "\nmerge_status: " + mergeStatus + "\nstage: \"" + stage + "\"\n---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTaskFM("TASK-001-a.md", "001", "done", "merged", "P1")
	writeTaskFM("TASK-002-b.md", "002", "blocked", "", "P1")
	writeTaskFM("TASK-003-c.md", "003", "closed", "merged", "P1") // closed never blocks
	writeTaskFM("TASK-004-d.md", "004", "blocked", "", "P2")

	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)

	landed, reviewable, blockers := runner.stageTasksState(dir, "P1")
	if landed {
		t.Fatal("P1 has a blocked task, must not be landed")
	}
	if !reviewable {
		t.Fatal("P1 remainder is all blocked, must be reviewable")
	}
	if len(blockers) != 1 || blockers[0] != "002" {
		t.Fatalf("P1 blockers = %v, want [002]", blockers)
	}

	// A dispatchable remainder (implementing) makes the phase unreviewable.
	writeTaskFM("TASK-005-e.md", "005", "implementing", "", "P1")
	_, reviewable, _ = runner.stageTasksState(dir, "P1")
	if reviewable {
		t.Fatal("P1 with an implementing task must not be reviewable")
	}
}

// TestProcessStageReviewsDispatchesOnCompletion guards the stage gate: a
// stage whose tasks all landed triggers exactly one stage-review dispatch;
// a stage with a task still in flight does not.
func TestProcessStageReviewsDispatchesOnCompletion(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStagePlan(t, filepath.Join(projDir, "Notes"), `---
id: "stage-plan"
---

### Phase 1: 核心链路
- tasks: 001
- status: in-progress
`)
	// Task done but NOT merged: no dispatch yet.
	content := "---\nid: \"001\"\nstatus: done\nmerge_status: checks-pending\nstage: \"P1\"\n---\n# 001\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-001-a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ompLog := filepath.Join(dir, "dsh.log")
	fakeDSH := filepath.Join(dir, "fake-dsh")
	script := "#!/bin/bash\necho \"ARGS=$@\" >> " + ompLog + "\nexit 0\n"
	if err := os.WriteFile(fakeDSH, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := New(&config.Config{
		ObsidianVault: vault,
		Executor:      "dsh",
		DSHCmd:        fakeDSH,
		LogDir:        filepath.Join(dir, "logs"),
		Models:        config.DefaultModels(),
	})
	runner.logger = log.New(io.Discard, "", 0)

	if n := runner.processStageReviews(runner.daemonCtx); n != 0 {
		t.Fatal("stage-review must not dispatch while a task is unmerged")
	}
	if _, err := os.Stat(ompLog); !os.IsNotExist(err) {
		t.Fatal("DSH must not run before stage completion")
	}

	// Task merges: dispatch fires with the stage-plan path argument.
	content = "---\nid: \"001\"\nstatus: done\nmerge_status: merged\nstage: \"P1\"\n---\n# 001\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-001-a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := runner.processStageReviews(runner.daemonCtx); n != 1 {
		t.Fatalf("stage-review dispatch count = %d, want 1", n)
	}
	data := waitForPmArgs(t, ompLog)
	if !strings.Contains(data, "stage-review") {
		t.Fatalf("DSH args = %q, want stage-review mode", data)
	}
	if !strings.Contains(data, "Stage-Plan.md") {
		t.Fatalf("DSH args = %q, want Stage-Plan.md path", data)
	}
}
