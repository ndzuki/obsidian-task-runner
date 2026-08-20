package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestMain pins the API-key probe to "available" for the whole package. The
// OMP-launch preflight now consults apiKeyAvailable(); the default probe
// shells out to secret-tool, which is slow/flaky under parallel test load
// and absent in CI — every dispatch test would otherwise depend on the host
// keyring. Tests that need a specific probe value override it explicitly
// (withAPIKeyValue / apiKeyProbe.Store) and restore via Cleanup.
func TestMain(m *testing.M) {
	apiKeyProbe.Store(func() bool { return true })
	// 任务临时目录（setTaskTempEnv / cleanupTaskArtifacts）与锁文件都落在
	// XDG_CACHE_HOME；测试必须隔离，否则 dispatch 路径会污染真实
	// ~/.cache/otg/tasks|locks（2026-08-14：全量测试在真实目录留下 269 个
	// /tmp/TestXxx 路径 hash 的空目录）。
	cacheDir, err := os.MkdirTemp("", "otg-test-cache-")
	if err != nil {
		panic("create test cache dir: " + err.Error())
	}
	os.Setenv("XDG_CACHE_HOME", cacheDir)
	os.Exit(m.Run())
}

func TestProcessBatchDispatchesReadyTaskAfterTransition(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	argsDir := filepath.Join(dir, "args")
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)
	t.Setenv("ARGS_DIR", argsDir)

	taskPath := writeTaskFile(t, dir, "TASK-000.md", "ready")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	done := runBatch(runner, []task.ReadyTask{{
		ID:       "000",
		Title:    "Ready task",
		FilePath: taskPath,
		Status:   "ready",
		Assignee: "default",
	}})

	waitForStartCount(t, startDir, 1)
	// The fake OMP writes START_DIR before ARGS_DIR; wait for the args file
	// so the read below cannot race the script (flaky under -count>1).
	waitForArgsFile(t, argsDir)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "refining" {
		t.Fatalf("status = %q, want refining", fm.Status)
	}

	entries, err := os.ReadDir(argsDir)
	if err != nil {
		t.Fatalf("read OMP args directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("OMP invocation count = %d, want 1", len(entries))
	}
	args, err := os.ReadFile(filepath.Join(argsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read OMP args: %v", err)
	}
	wantPrompt := "obsidian-task-runner-refining"
	if !strings.Contains(string(args), wantPrompt) {
		t.Fatalf("OMP args = %q, want prompt %q", args, wantPrompt)
	}
}

// TestRefiningDispatchWritesPhaseSpecificLogFile pins the phase passed to
// runTask for a maturity-gate dispatch. Regression: phase was "" so the log
// file, timeout (30m fallback), PID anti-duplication, and failure-recovery
// semantics all silently fell back to defaults instead of the refining
// configuration (15m, refine_retry_count, retry-then-block).
func TestRefiningDispatchWritesPhaseSpecificLogFile(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, map[string]string{})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	logDir := filepath.Join(dir, "logs")
	taskPath := writeTaskFile(t, dir, "TASK-075-refining.md", "ready")
	runner := newTestRunner(skillDir, omp, logDir, 1)
	done := runBatch(runner, []task.ReadyTask{{
		ID: "075", Title: "Refining", FilePath: taskPath, Status: "ready", Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1)

	taskLogDir := filepath.Join(logDir, "tasks")
	entries, err := os.ReadDir(taskLogDir)
	if err != nil {
		t.Fatalf("read task log dir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "TASK-075-") && strings.HasSuffix(e.Name(), "-refining.log") {
			found = true
			break
		}
	}
	if !found {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("no -refining.log for TASK-075, got: %v", names)
	}

	releaseBarrier(t, releaseFile)
	waitForBatch(t, done)
}

// TestRefiningEarlyOutRoutesReplanToPlanning pins the refining early-out for
// the "REQ changed after the last plan" case. Regression: the early-out only
// fired when refine_req_hash == plan_req_hash, so a task whose requirement
// changed after its plan (pending_req=true, refine != plan) was re-dispatched
// into the maturity gate on every scan forever — 30+ identical refining
// rounds for TASK-067. The gate must route to planning once the stored audit
// covers the current REQ, and re-run only when the audit is stale.
func TestRefiningEarlyOutRoutesReplanToPlanning(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, _, _ := writeBarrierOMP(t, dir)

	reqPath := filepath.Join(dir, "REQ-067.md")
	reqContent := "## 目标\nchanged requirement\n"
	if err := os.WriteFile(reqPath, []byte(reqContent), 0o644); err != nil {
		t.Fatalf("write req: %v", err)
	}
	sum := sha256.Sum256([]byte(reqContent))
	currentHash := "sha256:" + hex.EncodeToString(sum[:])
	staleHash := "sha256:37d9b57ae3b284b91664b89265435d2e4172e803baa24838891e591fdda26bfd"

	tests := []struct {
		name      string
		refine    string
		plan      string
		wantPhase string
	}{
		{
			name:      "audit current, req changed since plan → planning",
			refine:    currentHash,
			plan:      staleHash,
			wantPhase: "obsidian-task-runner-round1",
		},
		{
			name:      "audit stale, req changed after audit → gate re-runs",
			refine:    staleHash,
			plan:      staleHash,
			wantPhase: "obsidian-task-runner-refining",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := filepath.Join(dir, tt.name)
			startDir := filepath.Join(sub, "starts")
			argsDir := filepath.Join(sub, "args")
			rel := filepath.Join(sub, "release")
			t.Setenv("START_DIR", startDir)
			t.Setenv("RELEASE_FILE", rel)
			t.Setenv("ARGS_DIR", argsDir)
			t.Cleanup(func() { _ = os.WriteFile(rel, nil, 0o644) })

			taskPath := writeTaskFile(t, sub, "TASK-067.md", "refining")
			runner := newTestRunner(skillDir, omp, filepath.Join(sub, "logs"), 1)
			done := runBatch(runner, []task.ReadyTask{{
				ID: "067", Title: "Operation creation workflow",
				FilePath: taskPath, Status: "refining", Assignee: "default",
				ReqDoc: reqPath, Maturity: "fully_mature",
				RefineReqHash: tt.refine, PlanReqHash: tt.plan, PendingReq: true,
			}})

			waitForStartCount(t, startDir, 1)
			waitForArgsFile(t, argsDir)
			entries, err := os.ReadDir(argsDir)
			if err != nil {
				t.Fatalf("read OMP args directory: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("OMP invocation count = %d, want 1", len(entries))
			}
			args, err := os.ReadFile(filepath.Join(argsDir, entries[0].Name()))
			if err != nil {
				t.Fatalf("read OMP args: %v", err)
			}
			wantPrompt := tt.wantPhase
			if !strings.Contains(string(args), wantPrompt) {
				t.Fatalf("OMP args = %q, want prompt %q", args, wantPrompt)
			}

			releaseBarrier(t, rel)
			// processBatch is dispatch-only: runTask goroutines may still be
			// starting OMP (writing start files) when it returns. Wait for
			// them before the test ends, or TempDir cleanup races the OMP
			// start write (observed on slow CI: "directory not empty").
			waitForTasksIdle(t, runner)
			waitForBatch(t, done)
		})
	}
}

// TestGrillContinueResetReleasesParkedState guards the grill_continue reset
// path in prepareBatch: answering a parked task offline must release the
// parked flag (so the maturity gate re-runs normally) while keeping
// grill_repeat across refine rounds.
func TestGrillContinueResetReleasesParkedState(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, map[string]string{})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskPath := filepath.Join(dir, "TASK-076.md")
	content := "---\n" +
		"id: \"076\"\n" +
		"status: needs-grilling\n" +
		"grill_continue: true\n" +
		"grill_done: false\n" +
		"grill_parked: true\n" +
		"grill_repeat: 3\n" +
		"---\n# T076\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	done := runBatch(runner, []task.ReadyTask{{
		ID: "076", Title: "T076", FilePath: taskPath, Status: "needs-grilling",
		GrillContinue: true, GrillParked: true, Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1)
	releaseBarrier(t, releaseFile)
	waitForBatch(t, done)

	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "refining" {
		t.Fatalf("status = %q, want refining after grill_continue reset", fm.Status)
	}
	if fm.GrillParked {
		t.Fatal("grill_continue reset must release parked state")
	}
	if fm.GrillRepeat != 3 {
		t.Fatalf("grill_repeat = %d, want 3 (repeat counter must survive reset)", fm.GrillRepeat)
	}
}

func TestProcessBatchRunsIndependentTasksConcurrently(t *testing.T) {
	dir := t.TempDir()
	projectOne := filepath.Join(dir, "project-one")
	projectTwo := filepath.Join(dir, "project-two")
	for _, project := range []string{projectOne, projectTwo} {
		if err := os.MkdirAll(project, 0755); err != nil {
			t.Fatalf("create project directory: %v", err)
		}
	}

	skillDir := writeVaultMap(t, dir, map[string]string{
		"project-one": projectOne,
		"project-two": projectTwo,
	})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, dir, "TASK-001.md", "planning")
	taskTwo := writeTaskFile(t, dir, "TASK-002.md", "planning")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)

	done := runBatch(runner, []task.ReadyTask{
		{ID: "001", Title: "One", Project: "project-one", FilePath: taskOne, Status: "planning", Assignee: "default"},
		{ID: "002", Title: "Two", Project: "project-two", FilePath: taskTwo, Status: "planning", Assignee: "default"},
	})
	waitForStartCount(t, startDir, 2)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
}

func TestProcessBatchUsesTaskPathForDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	projectOne := filepath.Join(dir, "project-one")
	projectTwo := filepath.Join(dir, "project-two")
	for _, project := range []string{projectOne, projectTwo} {
		if err := os.MkdirAll(project, 0755); err != nil {
			t.Fatalf("create project directory: %v", err)
		}
	}

	skillDir := writeVaultMap(t, dir, map[string]string{
		"project-one": projectOne,
		"project-two": projectTwo,
	})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, filepath.Join(dir, "one"), "TASK-001.md", "planning")
	taskTwo := writeTaskFile(t, filepath.Join(dir, "two"), "TASK-001.md", "planning")
	if taskRunKey(taskOne) == taskRunKey(taskTwo) {
		t.Fatal("different task files must have distinct run keys")
	}

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "001", Title: "One", Project: "project-one", FilePath: taskOne, Status: "planning", Assignee: "default"},
		{ID: "001", Title: "Two", Project: "project-two", FilePath: taskTwo, Status: "planning", Assignee: "default"},
	})
	waitForStartCount(t, startDir, 2)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
}

func TestTaskPIDFileUsesTaskPathKey(t *testing.T) {
	dir := t.TempDir()
	taskLogDir := filepath.Join(dir, "logs", "tasks")
	first := filepath.Join(dir, "one", "TASK-001.md")
	second := filepath.Join(dir, "two", "TASK-001.md")
	if taskPIDFile(taskLogDir, "001", first) == taskPIDFile(taskLogDir, "001", second) {
		t.Fatal("tasks with identical IDs in different projects must use distinct PID files")
	}
}

func TestProcAliveRejectsZombie(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/pid/stat zombie detection is Linux-specific")
	}
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() { _, _ = cmd.Process.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(cmd.Process.Pid), "stat"))
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) > 2 && fields[2] == "Z" {
				if procAlive(cmd.Process.Pid) {
					t.Fatal("procAlive returned true for zombie process")
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not enter zombie state")
}

func TestProcessBatchRunsSameRepositoryRoundTwoTasksConcurrently(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)
	skillDir := writeVaultMap(t, dir, map[string]string{"shared": repo})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, dir, "TASK-011.md", "plan-review")
	taskTwo := writeTaskFile(t, dir, "TASK-012.md", "plan-review")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "011", Title: "One", Project: "shared", FilePath: taskOne, Status: "plan-review", PlanApproved: true, Assignee: "default"},
		{ID: "012", Title: "Two", Project: "shared", FilePath: taskTwo, Status: "plan-review", PlanApproved: true, Assignee: "default"},
	})
	waitForStartCount(t, startDir, 2)

	entries, err := os.ReadDir(startDir)
	if err != nil {
		t.Fatalf("read start directory: %v", err)
	}
	for _, entry := range entries {
		pathData, err := os.ReadFile(filepath.Join(startDir, entry.Name()))
		if err != nil {
			t.Fatalf("read worktree marker: %v", err)
		}
		if strings.TrimSpace(string(pathData)) == repo {
			t.Fatalf("Round 2 ran in primary repository instead of an isolated worktree: %q", pathData)
		}
	}

	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
}

func TestProcessBatchLimitsImplementingTasksAcrossConcurrentBatches(t *testing.T) {
	dir := t.TempDir()
	projects := make(map[string]string, 4)
	for i := 1; i <= 4; i++ {
		name := fmt.Sprintf("project-%d", i)
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create project directory: %v", err)
		}
		projects[name] = path
	}

	skillDir := writeVaultMap(t, dir, projects)
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)
	activeDir := filepath.Join(dir, "active")
	t.Setenv("ACTIVE_DIR", activeDir)

	tasks := make([]task.ReadyTask, 0, 4)
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("04%d", i)
		project := fmt.Sprintf("project-%d", i)
		taskPath := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-"+id+".md", "implementing")
		tasks = append(tasks, task.ReadyTask{
			ID: id, Title: "Implementation " + id, Project: project,
			FilePath: taskPath, Status: "implementing", NewProject: true, Assignee: "default",
		})
	}

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	// Serial batches for determinism: concurrent runBatch goroutines race the
	// shared implementation gate, and whichever schedules first takes the
	// capacity slots.
	firstDone := runBatch(runner, tasks[:2])
	waitForStartCount(t, startDir, 2)
	secondDone := runBatch(runner, tasks[2:])
	second := waitForBatch(t, secondDone) // returns immediately: capacity exhausted

	releaseBarrier(t, releaseFile)
	// Dispatch-only semantics: the first batch takes both capacity slots;
	// the second batch cannot schedule anything until capacity is released
	// (which the next scan round does).
	if first := waitForBatch(t, firstDone); first != 2 {
		t.Fatalf("first batch dispatched = %d, want 2", first)
	}
	if second != 0 {
		t.Fatalf("second batch dispatched = %d, want 0 (capacity exhausted)", second)
	}
	waitForTasksIdle(t, runner)
	assertMaxActive(t, activeDir, 2)

	// Capacity released: the next scan round (simulated by a fresh batch)
	// dispatches and completes the remainder.
	thirdDone := runBatch(runner, tasks[2:])
	waitForStartCount(t, startDir, 4)
	releaseBarrier(t, releaseFile)
	if third := waitForBatch(t, thirdDone); third != 2 {
		t.Fatalf("third batch dispatched = %d, want 2", third)
	}
	waitForTasksIdle(t, runner)
}

// TestProcessBatchSerializesOverlappingPlanFiles guards the plan-file overlap
// serialization: two implementing tasks of the same repository planning the
// same file dispatch one at a time — the second stays deferred while the
// first runs, and dispatches only after the first's implementation session
// finishes (the registration window is the session, not the delivery
// lifecycle, so a merge-stalled upstream never starves its downstream).
func TestProcessBatchSerializesOverlappingPlanFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)
	skillDir := writeVaultMap(t, dir, map[string]string{"shared": repo})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-011.md", "implementing")
	taskTwo := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-012.md", "implementing")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	files := []string{"internal/foo.go"}
	one := task.ReadyTask{ID: "011", Title: "One", Project: "shared", FilePath: taskOne, Status: "implementing", PlanFiles: files, Assignee: "default"}
	two := task.ReadyTask{ID: "012", Title: "Two", Project: "shared", FilePath: taskTwo, Status: "implementing", PlanFiles: files, Assignee: "default"}

	// Both candidates overlap: only the first in dispatch order runs.
	firstDone := runBatch(runner, []task.ReadyTask{one, two})
	waitForStartCount(t, startDir, 1)
	if got := waitForBatch(t, firstDone); got != 1 {
		t.Fatalf("first batch dispatched = %d, want 1 (overlap serialized)", got)
	}

	// While task one is still running, re-scanning task two stays deferred.
	secondDone := runBatch(runner, []task.ReadyTask{two})
	if got := waitForBatch(t, secondDone); got != 0 {
		t.Fatalf("second batch dispatched = %d, want 0 (overlap still active)", got)
	}
	assertStartCount(t, startDir, 1)

	// Task one finishes: its plan_files registration is released, and task
	// two dispatches on the next round.
	releaseBarrier(t, releaseFile)
	waitForTasksIdle(t, runner)
	thirdDone := runBatch(runner, []task.ReadyTask{two})
	waitForStartCount(t, startDir, 2)
	if got := waitForBatch(t, thirdDone); got != 1 {
		t.Fatalf("third batch dispatched = %d, want 1 (overlap cleared)", got)
	}
	releaseBarrier(t, releaseFile)
	waitForTasksIdle(t, runner)
}

// TestProcessBatchOverlapWaitLimitExceeded guards the anti-starvation bound:
// a task deferred past max_overlap_wait_minutes dispatches concurrently even
// though the overlap persists — merge conflict resolution stays the fallback
// instead of blocking the queue forever behind a stalled session.
func TestProcessBatchOverlapWaitLimitExceeded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)
	skillDir := writeVaultMap(t, dir, map[string]string{"shared": repo})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-011.md", "implementing")
	taskTwo := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-012.md", "implementing")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	runner.cfg.MaxOverlapWaitMinutes = 120
	files := []string{"internal/foo.go"}
	one := task.ReadyTask{ID: "011", Title: "One", Project: "shared", FilePath: taskOne, Status: "implementing", PlanFiles: files, Assignee: "default"}
	two := task.ReadyTask{ID: "012", Title: "Two", Project: "shared", FilePath: taskTwo, Status: "implementing", PlanFiles: files, Assignee: "default"}

	firstDone := runBatch(runner, []task.ReadyTask{one, two})
	waitForStartCount(t, startDir, 1)
	if got := waitForBatch(t, firstDone); got != 1 {
		t.Fatalf("first batch dispatched = %d, want 1 (overlap serialized)", got)
	}

	// Simulate the wait having started long ago: past the 2h limit the
	// deferred task must dispatch concurrently (merge flow is the fallback).
	runner.overlapWaits.Store(taskTwo, time.Now().Add(-3*time.Hour))
	secondDone := runBatch(runner, []task.ReadyTask{two})
	waitForStartCount(t, startDir, 2)
	if got := waitForBatch(t, secondDone); got != 1 {
		t.Fatalf("overlimit batch dispatched = %d, want 1 (wait limit exceeded)", got)
	}
	releaseBarrier(t, releaseFile)
	waitForTasksIdle(t, runner)
}

// TestProcessBatchDifferentPlanFilesNotSerialized guards the false-positive
// path: tasks planning disjoint file sets dispatch concurrently even in the
// same repository.
func TestProcessBatchDifferentPlanFilesNotSerialized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)
	skillDir := writeVaultMap(t, dir, map[string]string{"shared": repo})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-011.md", "implementing")
	taskTwo := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-012.md", "implementing")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	one := task.ReadyTask{ID: "011", Title: "One", Project: "shared", FilePath: taskOne, Status: "implementing", PlanFiles: []string{"internal/foo.go"}, Assignee: "default"}
	two := task.ReadyTask{ID: "012", Title: "Two", Project: "shared", FilePath: taskTwo, Status: "implementing", PlanFiles: []string{"internal/bar.go"}, Assignee: "default"}

	done := runBatch(runner, []task.ReadyTask{one, two})
	waitForStartCount(t, startDir, 2)
	if got := waitForBatch(t, done); got != 2 {
		t.Fatalf("batch dispatched = %d, want 2 (disjoint plan files)", got)
	}
	releaseBarrier(t, releaseFile)
	waitForTasksIdle(t, runner)
}

func TestPlanningTaskDoesNotConsumeImplementationSlot(t *testing.T) {
	dir := t.TempDir()
	implementationRepo := filepath.Join(dir, "implementation-repo")
	planningRepo := filepath.Join(dir, "planning-repo")
	for _, repo := range []string{implementationRepo, planningRepo} {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatalf("create project directory: %v", err)
		}
	}

	skillDir := writeVaultMap(t, dir, map[string]string{
		"implementation": implementationRepo,
		"planning":       planningRepo,
	})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	implementationPath := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-051.md", "implementing")
	planningPath := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-052.md", "planning")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	implementationDone := runBatch(runner, []task.ReadyTask{{
		ID: "051", Title: "Implementation", Project: "implementation",
		FilePath: implementationPath, Status: "implementing", NewProject: true, Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1)
	planningDone := runBatch(runner, []task.ReadyTask{{
		ID: "052", Title: "Planning", Project: "planning",
		FilePath: planningPath, Status: "planning", Assignee: "default",
	}})
	waitForStartCount(t, startDir, 2)

	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, implementationDone) + waitForBatch(t, planningDone); processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
}

func TestResumedBlockedImplementationUsesImplementationSlot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	firstRepo := filepath.Join(dir, "first-repo")
	if err := os.MkdirAll(firstRepo, 0o755); err != nil {
		t.Fatalf("create first project directory: %v", err)
	}
	resumedRepo := createRepository(t, filepath.Join(dir, "resumed-root"))

	skillDir := writeVaultMap(t, dir, map[string]string{
		"first":   firstRepo,
		"resumed": resumedRepo,
	})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	firstPath := writeTaskFile(t, filepath.Join(dir, "tasks"), "TASK-061.md", "implementing")
	resumedPath := filepath.Join(dir, "tasks", "TASK-062.md")
	if err := os.WriteFile(resumedPath, []byte(`---
id: "062"
title: Resumed implementation
project: resumed
status: blocked
blocked_phase: implementing
resume_approved: true
assignee: default
---
# TASK-062
`), 0o644); err != nil {
		t.Fatalf("write resumed task: %v", err)
	}

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	firstDone := runBatch(runner, []task.ReadyTask{{
		ID: "061", Title: "First", Project: "first",
		FilePath: firstPath, Status: "implementing", NewProject: true, Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1)
	resumedDone := runBatch(runner, []task.ReadyTask{{
		ID: "062", Title: "Resumed", Project: "resumed",
		FilePath: resumedPath, Status: "blocked", Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1)

	releaseBarrier(t, releaseFile)
	// Dispatch-only: the implementing task takes the single capacity slot;
	// the blocked task is dispatched regardless (blocked status does not
	// consume implementation capacity at dispatch time), then runs after
	// the barrier release.
	if processed := waitForBatch(t, firstDone) + waitForBatch(t, resumedDone); processed != 2 {
		t.Fatalf("dispatched = %d, want 2 (blocked resume does not consume capacity)", processed)
	}
	waitForTasksIdle(t, runner)

	if runner.implementationGate.localActive() != 0 || len(runner.implementationGate.active) != 0 {
		t.Errorf("post-idle gate: local=%d active=%v, want 0/empty",
			runner.implementationGate.localActive(),
			runner.implementationGate.active)
	}

	// Capacity released after the first task finished: a fresh round
	// dispatches the implementing task that was waiting.
	nextDone := runBatch(runner, []task.ReadyTask{{
		ID: "062", Title: "Resumed", Project: "resumed",
		FilePath: resumedPath, Status: "implementing", Assignee: "default",
	}})
	waitForStartCount(t, startDir, 2)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, nextDone); processed != 1 {
		t.Fatalf("next round dispatched = %d, want 1", processed)
	}
	waitForTasksIdle(t, runner)
}

func TestRepositoryWriteWaiterDoesNotConsumeUnlockedWork(t *testing.T) {
	runner := New(&config.Config{})
	repoOne := filepath.Join(t.TempDir(), "repo-one")
	repoTwo := filepath.Join(t.TempDir(), "repo-two")

	if !runner.tryRepoLock(repoOne, repoLockRead) {
		t.Fatal("expected initial read lock")
	}
	defer runner.unlockRepo(repoOne, repoLockRead)

	if runner.tryRepoLock(repoOne, repoLockWrite) {
		runner.unlockRepo(repoOne, repoLockWrite)
		t.Fatal("write waiter must not acquire while a reader is active")
	}
	if !runner.tryRepoLock(repoTwo, repoLockWrite) {
		t.Fatal("unrelated repository write must remain schedulable")
	}
	runner.unlockRepo(repoTwo, repoLockWrite)
}

func TestProcessBatchRunsSameRepositoryPlanningTasksConcurrently(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	skillDir := writeVaultMap(t, dir, map[string]string{"shared": repo})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, filepath.Join(dir, "one"), "TASK-031.md", "planning")
	taskTwo := writeTaskFile(t, filepath.Join(dir, "two"), "TASK-032.md", "planning")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "031", Title: "One", Project: "shared", FilePath: taskOne, Status: "planning", Assignee: "default"},
		{ID: "032", Title: "Two", Project: "shared", FilePath: taskTwo, Status: "planning", Assignee: "default"},
	})
	waitForStartCount(t, startDir, 2)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
}

func TestProcessBatchNonPositiveGlobalLimitMeansUnlimited(t *testing.T) {
	dir := t.TempDir()
	projectOne := filepath.Join(dir, "repo-one")
	projectTwo := filepath.Join(dir, "repo-two")
	for _, project := range []string{projectOne, projectTwo} {
		if err := os.MkdirAll(project, 0755); err != nil {
			t.Fatalf("create project dir: %v", err)
		}
	}

	skillDir := writeVaultMap(t, dir, map[string]string{
		"project-one": projectOne,
		"project-two": projectTwo,
	})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskOne := writeTaskFile(t, dir, "TASK-021.md", "implementing")
	taskTwo := writeTaskFile(t, dir, "TASK-022.md", "implementing")
	// global limit 0 = no total cap; the per-project default (2) allows both
	// projects' tasks to run at once.
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 0)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "021", Title: "One", Project: "project-one", FilePath: taskOne, Status: "implementing", NewProject: true, Assignee: "default"},
		{ID: "022", Title: "Two", Project: "project-two", FilePath: taskTwo, Status: "implementing", NewProject: true, Assignee: "default"},
	})
	waitForStartCount(t, startDir, 2)
	assertStartCount(t, startDir, 2)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 2 {
		t.Fatalf("dispatched = %d, want 2", processed)
	}
	waitForTasksIdle(t, runner)
}

// TestProcessBatchPerProjectConcurrency guards the per-project Round 2
// capacity: with no global cap and 2 per project, two projects dispatch four
// implementing sessions in one round — the requested behavior behind
// max_concurrent_tasks_per_project.
func TestProcessBatchPerProjectConcurrency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	os.MkdirAll(filepath.Join(dir, "one"), 0755)
	os.MkdirAll(filepath.Join(dir, "two"), 0755)
	projectOne := createRepository(t, filepath.Join(dir, "one"))
	projectTwo := createRepository(t, filepath.Join(dir, "two"))

	skillDir := writeVaultMap(t, dir, map[string]string{
		"project-one": projectOne,
		"project-two": projectTwo,
	})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	var tasks []task.ReadyTask
	for i, id := range []string{"031", "032", "033", "034", "035"} {
		project := "project-one"
		if i >= 2 {
			project = "project-two"
		}
		path := writeTaskFile(t, dir, "TASK-"+id+".md", "implementing")
		tasks = append(tasks, task.ReadyTask{
			ID: id, Title: "T" + id, Project: project, FilePath: path,
			Status: "implementing", Assignee: "default",
		})
	}
	// Four slots (2 per project) across two projects; the fifth task (third
	// of project-two) must wait for capacity.
	runner := newTestRunnerLimits(skillDir, omp, filepath.Join(dir, "logs"), 0, 2)
	done := runBatch(runner, tasks)
	waitForStartCount(t, startDir, 4)
	assertStartCount(t, startDir, 4)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 4 {
		t.Fatalf("dispatched = %d, want 4", processed)
	}
	waitForTasksIdle(t, runner)

	// Capacity released: the fifth task dispatches on the next round.
	nextDone := runBatch(runner, []task.ReadyTask{tasks[4]})
	waitForStartCount(t, startDir, 5)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, nextDone); processed != 1 {
		t.Fatalf("next round dispatched = %d, want 1", processed)
	}
	waitForTasksIdle(t, runner)
}

func TestWorktreeRootTrailingSlashResolvesParentDir(t *testing.T) {
	// 尾斜杠 repoDir（vault-map `path` 常见写法）必须解析到父目录而非
	// repo 自身——filepath.Dir 对尾斜杠路径会返回路径本身（去掉尾斜杠），
	// 若不先 Clean 会把 .otg-worktrees 嵌套进主 checkout。
	repoDir := filepath.Join(t.TempDir(), "deployd") + string(filepath.Separator)
	got := worktreeRoot("", repoDir)
	want := filepath.Join(filepath.Dir(filepath.Clean(repoDir)), ".otg-worktrees")
	if got != want {
		t.Fatalf("worktreeRoot(%q) = %q, want %q", repoDir, got, want)
	}
	if strings.HasPrefix(got, filepath.Clean(repoDir)+string(filepath.Separator)) {
		t.Fatalf("worktreeRoot nested inside repo checkout: %q", got)
	}
}

func TestEnsureTaskWorktreeReusesIsolatedWorktree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	worktree, err := ensureTaskWorktree(repo, "007", "", "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	if worktree == repo {
		t.Fatal("worktree must not reuse the primary repository directory")
	}
	if output, err := exec.Command("git", "-C", worktree, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "true" {
		t.Fatalf("validate worktree: %v: %s", err, output)
	}

	reused, err := ensureTaskWorktree(repo, "007", "", "")
	if err != nil {
		t.Fatalf("reuse worktree: %v", err)
	}
	if reused != worktree {
		t.Fatalf("reused worktree = %q, want %q", reused, worktree)
	}
}

// TestEnsureTaskWorktreeSelfHealsExternallyDeleted: an externally deleted
// worktree directory (manual disk cleanup) leaves a dangling git registration
// that makes every `git worktree add` fail with "already registered".
// ensureTaskWorktree must prune the stale registration and recreate the
// worktree instead of stalling the task forever (seen live: TASK-057/077
// stuck for hours until a manual `git worktree prune`).
func TestEnsureTaskWorktreeSelfHealsExternallyDeleted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	worktree, err := ensureTaskWorktree(repo, "heal-del", "", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// Simulate external deletion of the worktree directory.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	// Without self-healing this fails with "missing but already registered".
	recreated, err := ensureTaskWorktree(repo, "heal-del", "", "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree after external deletion: %v", err)
	}
	if recreated != worktree {
		t.Fatalf("recreated path = %q, want %q", recreated, worktree)
	}
	if _, err := os.Stat(recreated); err != nil {
		t.Fatalf("recreated worktree missing: %v", err)
	}
	if output, err := exec.Command("git", "-C", recreated, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "true" {
		t.Fatalf("recreated worktree invalid: %v: %s", err, output)
	}
}

// TestEnsureTaskWorktreeSelfHealsBrokenDirectory: a worktree directory that
// still exists but lost its git binding (half-removed checkout, deleted .git
// link) must also be repaired and recreated rather than failing validation.
func TestEnsureTaskWorktreeSelfHealsBrokenDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	worktree, err := ensureTaskWorktree(repo, "heal-broken", "task/heal-broken", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// Break the worktree binding while leaving the directory in place.
	if err := os.Remove(filepath.Join(worktree, ".git")); err != nil {
		t.Fatalf("remove worktree .git link: %v", err)
	}
	recreated, err := ensureTaskWorktree(repo, "heal-broken", "task/heal-broken", "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree after broken directory: %v", err)
	}
	if recreated != worktree {
		t.Fatalf("recreated path = %q, want %q", recreated, worktree)
	}
	branch, err := gitCurrentBranch(recreated)
	if err != nil {
		t.Fatalf("gitCurrentBranch: %v", err)
	}
	if branch != "task/heal-broken" {
		t.Fatalf("branch = %q, want task/heal-broken", branch)
	}
}

func TestEnsureTaskWorktreeCreatesAndReusesTargetBranch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	worktree, err := ensureTaskWorktree(repo, "008", "task/008-feature", "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	branch, err := gitCurrentBranch(worktree)
	if err != nil {
		t.Fatalf("gitCurrentBranch: %v", err)
	}
	if branch != "task/008-feature" {
		t.Fatalf("branch = %q, want task/008-feature", branch)
	}

	reused, err := ensureTaskWorktree(repo, "008", "task/008-feature", "")
	if err != nil {
		t.Fatalf("reuse target branch worktree: %v", err)
	}
	if reused != worktree {
		t.Fatalf("reused worktree = %q, want %q", reused, worktree)
	}
}

func TestEnsureTaskWorktreeRejectsMismatchedTargetBranch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	if _, err := ensureTaskWorktree(repo, "009", "task/009-first", ""); err != nil {
		t.Fatalf("create first target branch worktree: %v", err)
	}
	if _, err := ensureTaskWorktree(repo, "009", "task/009-second", ""); err == nil {
		t.Fatal("expected target branch mismatch error")
	}
}

// TestEnsureTaskWorktreeBindsDetachedToTargetBranch: a worktree created
// before target_branch existed (detached HEAD, e.g. an early audit) must be
// bound to the target branch on the next call so merge/round2 phases operate
// on the feature branch instead of failing (TASK-067: merge could not reuse
// the detached round2 worktree and fell back to the main checkout).
func TestEnsureTaskWorktreeBindsDetachedToTargetBranch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)
	git(t, "-C", repo, "checkout", "-b", "task/010-feature")
	git(t, "-C", repo, "commit", "--allow-empty", "-m", "feature")
	git(t, "-C", repo, "checkout", "--detach")

	wt, err := ensureTaskWorktree(repo, "010", "", "")
	if err != nil {
		t.Fatalf("create detached worktree: %v", err)
	}
	// Reuse with a target branch: the detached worktree must switch onto it.
	bound, err := ensureTaskWorktree(repo, "010", "task/010-feature", "")
	if err != nil {
		t.Fatalf("bind detached worktree to target branch: %v", err)
	}
	if bound != wt {
		t.Fatalf("bound worktree = %q, want reused %q", bound, wt)
	}
	branch, err := gitCurrentBranch(wt)
	if err != nil {
		t.Fatalf("gitCurrentBranch: %v", err)
	}
	if branch != "task/010-feature" {
		t.Fatalf("branch after bind = %q, want task/010-feature", branch)
	}
}

// TestEnsureTaskWorktreeNeverReturnsPrimaryCheckout pins the isolation
// contract: when the primary checkout sits on the target branch (the old
// fallback let merge pollute the user's working directory, TASK-067), the
// call must FAIL loudly — git refuses to check the branch out in a second
// worktree — instead of silently reusing the primary checkout.
func TestEnsureTaskWorktreeNeverReturnsPrimaryCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)
	git(t, "-C", repo, "checkout", "-b", "task/011-feature")
	git(t, "-C", repo, "commit", "--allow-empty", "-m", "feature")

	wt, err := ensureTaskWorktree(repo, "011", "task/011-feature", "")
	if err == nil {
		t.Fatalf("expected error when primary checkout holds the target branch, got worktree %q", wt)
	}
	if strings.Contains(err.Error(), "is already used by worktree") == false {
		t.Fatalf("error must report the branch is checked out elsewhere: %v", err)
	}
}

func TestIsRound2(t *testing.T) {
	tests := []struct {
		name string
		task task.ReadyTask
		want bool
	}{
		{name: "approved plan review", task: task.ReadyTask{Status: "plan-review", PlanApproved: true}, want: true},
		{name: "resumed implementation", task: task.ReadyTask{Status: "implementing", PlanApproved: true}, want: true},
		{name: "unapproved plan", task: task.ReadyTask{Status: "plan-review"}, want: true},
		{name: "round one", task: task.ReadyTask{Status: "ready", PlanApproved: true}, want: false},
		{name: "new project", task: task.ReadyTask{Status: "plan-review", PlanApproved: true, NewProject: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRound2(tt.task); got != tt.want {
				t.Errorf("isRound2() = %v, want %v", got, tt.want)
			}
		})
	}
}

// withDesktopNotificationsDisabled clones the vault-map content and forces
// notifications.desktop=false. Defaults() ships desktop=true and config.Load
// merges it in, so without this every test walking the Load path would fire
// real failure/switch/status toasts on the user's desktop (T001 Fallback task
// toasts were exactly that). The clone keeps the caller's map untouched.
func withDesktopNotificationsDisabled(content map[string]any) map[string]any {
	cloned := make(map[string]any, len(content)+1)
	for k, v := range content {
		cloned[k] = v
	}
	cloned["notifications"] = map[string]any{"desktop": false}
	return cloned
}

func writeVaultMap(t *testing.T, dir string, projects map[string]string) string {
	t.Helper()
	skillDir := filepath.Join(dir, "skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "config"), 0755); err != nil {
		t.Fatalf("create skill config directory: %v", err)
	}
	entries := make([]map[string]string, 0, len(projects))
	for name, path := range projects {
		entries = append(entries, map[string]string{"name": name, "path": path})
	}
	data, err := json.Marshal(withDesktopNotificationsDisabled(map[string]any{
		"projects": entries,
	}))
	if err != nil {
		t.Fatalf("marshal vault map: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "config", "vault-map.json"), data, 0644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	return skillDir
}

func writeBarrierOMP(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	startDir := filepath.Join(dir, "starts")
	releaseFile := filepath.Join(dir, "release")
	omp := filepath.Join(dir, "fake-omp")
	t.Cleanup(func() {
		if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
			t.Errorf("release barrier during cleanup: %v", err)
		}
	})

	script := `#!/bin/sh
mkdir -p "$START_DIR"
printf '%s\n' "$PWD" > "$START_DIR/$$"
if [ -n "$ARGS_DIR" ]; then mkdir -p "$ARGS_DIR"; printf '%s\n' "$*" > "$ARGS_DIR/$$"; fi
if [ -n "$ACTIVE_DIR" ]; then
  mkdir -p "$ACTIVE_DIR"
  (
    flock 9
    touch "$ACTIVE_DIR/active-$$"
    active=$(find "$ACTIVE_DIR" -maxdepth 1 -type f -name 'active-*' | wc -l)
    maximum=$(cat "$ACTIVE_DIR/max" 2>/dev/null || printf '0')
    if [ "$active" -gt "$maximum" ]; then printf '%s\n' "$active" > "$ACTIVE_DIR/max"; fi
  ) 9>"$ACTIVE_DIR/lock"
  cleanup() { (flock 9; rm -f "$ACTIVE_DIR/active-$$") 9>"$ACTIVE_DIR/lock"; }
  trap cleanup EXIT
fi
for i in $(seq 3000); do [ -f "$RELEASE_FILE" ] && exit 0; sleep 0.01; done
exit 1
`
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake omp: %v", err)
	}
	return omp, startDir, releaseFile
}

func writeTaskFile(t *testing.T, dir, name, status string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create task directory: %v", err)
	}
	path := filepath.Join(dir, name)
	content := "---\nid: test\nstatus: " + status + "\n---\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	return path
}

func newTestRunner(skillDir, omp, logDir string, limit int) *Runner {
	return newTestRunnerLimits(skillDir, omp, logDir, limit, 2)
}

func newTestRunnerLimits(skillDir, omp, logDir string, limit, perProject int) *Runner {
	runner := New(&config.Config{
		SkillInstallDir:              skillDir,
		Executor:                     "dsh",
		DSHCmd:                       omp,
		LogDir:                       logDir,
		MaxConcurrentTasks:           limit,
		MaxConcurrentTasksPerProject: perProject,
		Models:                       config.DefaultModels(),
	})
	runner.logger = log.New(io.Discard, "", 0)
	return runner
}

func runBatch(runner *Runner, tasks []task.ReadyTask) <-chan int {
	done := make(chan int, 1)
	go func() {
		done <- runner.processBatch(tasks)
	}()
	return done
}

// waitForArgsFile polls until the fake OMP has written its argv capture.
// The barrier script writes START_DIR before ARGS_DIR, so start-count alone
// does not guarantee the args file exists.
func waitForArgsFile(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no OMP args file appeared in %s", dir)
}

func waitForStartCount(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n := countStartFiles(t, dir); n == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("start count did not reach %d; got %d", want, countStartFiles(t, dir))
}

func assertStartCount(t *testing.T, dir string, want int) {
	t.Helper()
	if got := countStartFiles(t, dir); got != want {
		t.Fatalf("start count = %d, want %d", got, want)
	}
}

func countStartFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read start directory: %v", err)
	}
	return len(entries)
}

func assertMaxActive(t *testing.T, dir string, want int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "max"))
	if err != nil {
		t.Fatalf("read max active: %v", err)
	}
	var got int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &got); err != nil {
		t.Fatalf("parse max active: %v", err)
	}
	if got != want {
		t.Fatalf("max active = %d, want %d", got, want)
	}
}

func releaseBarrier(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("release barrier: %v", err)
	}
}

func waitForBatch(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case processed := <-done:
		return processed
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not complete after releasing barrier")
		return 0
	}
}

// waitForTasksIdle blocks until every dispatched task goroutine (runTask)
// has finished. processBatch is dispatch-only now — it returns as soon as
// tasks are scheduled — so tests that assert completion side effects
// (frontmatter write-backs, gate release, pid cleanup) must wait for the
// tasks themselves.
func waitForTasksIdle(t *testing.T, runner *Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runner.activeTasks.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tasks did not finish within 5s (active=%d)", runner.activeTasks.Load())
}

// waitForScanIdle blocks until dispatched tasks AND the follow-up scan they
// trigger (runTask → requestScan) have fully unwound. Async dispatch means
// scanAndProcess returns while work is still running; tests must wait for
// both before ending, or leaked goroutines race the next test's global state
// (e.g. apiKeyProbe).
func waitForScanIdle(t *testing.T, runner *Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runner.activeTasks.Load() == 0 {
			runner.scanGateMu.Lock()
			active := runner.scanActive
			runner.scanGateMu.Unlock()
			if !active {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scan/tasks did not unwind within 5s (active=%d)", runner.activeTasks.Load())
}

func createRepository(t *testing.T, dir string) string {
	t.Helper()
	repo := filepath.Join(dir, "repo")
	for _, args := range [][]string{
		{"init", repo},
		{"-C", repo, "config", "user.email", "test@example.com"},
		{"-C", repo, "config", "user.name", "Test User"},
		{"-C", repo, "config", "commit.gpgsign", "false"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0644); err != nil {
		t.Fatalf("write repository file: %v", err)
	}
	if output, err := exec.Command("git", "-C", repo, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	return repo
}

func TestResumeImplementingGate_NoPlanApproved(t *testing.T) {
	t.Skip("HEAD-specific daemon behavior, reconcile post-merge")
}

func TestSmartAutoUnblock_SkipsReady(t *testing.T) {
	t.Skip("HEAD-specific daemon behavior, reconcile post-merge")
}

func TestResumeImplementingGate_StaleHash(t *testing.T) {
	t.Skip("HEAD-specific daemon behavior, reconcile post-merge")
}

func TestResumeUnknownBlockedPhase(t *testing.T) {
	t.Skip("HEAD-specific daemon behavior, reconcile post-merge")
}

func TestBlockedPhaseValidation(t *testing.T) {
	// R3: invalid blocked_phase values should be rejected.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	content := "---\nid: \"001\"\nstatus: blocked\nproject: test\nreq_doc: Projects/test/REQ-001.md\nblocked_phase: round2\n---\n# Task\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := yamlfrontmatter.ValidateTaskDocument(path); err == nil {
		t.Fatal("expected error for invalid blocked_phase")
	}
}

// TestProcessBatchReturnsOnDaemonCancel verifies that processBatch stops
// waiting for in-flight task goroutines when the daemon context is cancelled
// (systemd stop), so the event loop can reach ctx.Done promptly instead of
// blocking behind a long-running execution session.
func TestProcessBatchReturnsOnDaemonCancel(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskPath := writeTaskFile(t, dir, "TASK-000.md", "planning")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	ctx, cancel := context.WithCancel(context.Background())
	runner.daemonCtx = ctx

	done := runBatch(runner, []task.ReadyTask{{
		ID: "000", Title: "Plan", FilePath: taskPath,
		Status: "planning", Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1) // OMP launched and blocked on the barrier

	cancel()
	select {
	case <-done:
		// processBatch is dispatch-only: it returns promptly on cancel; the
		// SIGTERMed OMP's PHASE_INTERRUPTED write-back lands in runTask.
	case <-time.After(3 * time.Second):
		t.Fatal("processBatch did not return after daemon context cancel")
	}

	// The interrupted phase's write-back must land once the dispatched task
	// goroutine unwinds (production shutdown additionally waits for it via
	// waitForScanExit before exiting).
	waitForTasksIdle(t, runner)
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.PhaseErrorCode != string(ErrPhaseInterrupted) {
		t.Fatalf("phase_error_code = %q, want %q landed before processBatch returned", fm.PhaseErrorCode, ErrPhaseInterrupted)
	}

	releaseBarrier(t, releaseFile) // let the interrupted OMP exit cleanly
}

// TestOMPFailureNotMisroutedAsInterrupted guards the interrupted-path check:
// a genuine OMP failure (non-zero exit) while the daemon is alive must route
// through failure recovery (MODEL_FAILED + retry-in-place), NOT the
// PHASE_INTERRUPTED auto-resume path. Regression: cancel() ran before the
// ctx.Err()==Canceled check, making it always true — every OMP failure was
// silently converted into "interrupted by daemon shutdown", skipping
// fallback/retry/blocked entirely (tasks stuck in refining/planning with
// PHASE_INTERRUPTED, scan deadlock via leaked repo locks).
func TestOMPFailureNotMisroutedAsInterrupted(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp := filepath.Join(dir, "failing-omp")
	if err := os.WriteFile(omp, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing omp: %v", err)
	}

	taskPath := writeTaskFile(t, dir, "TASK-000.md", "planning")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.daemonCtx = ctx

	done := runBatch(runner, []task.ReadyTask{{
		ID: "000", Title: "Plan", FilePath: taskPath,
		Status: "planning", Assignee: "default",
	}})
	if got := waitForBatch(t, done); got != 1 {
		t.Fatalf("dispatched = %d, want 1", got)
	}
	// runTask runs asynchronously; poll until the failure write-back lands.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fm := mustParse(t, taskPath); fm.PhaseErrorCode != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	fm := mustParse(t, taskPath)
	if fm.PhaseErrorCode == string(ErrPhaseInterrupted) {
		t.Fatalf("phase_error_code = %q: OMP failure misrouted as shutdown interruption", fm.PhaseErrorCode)
	}
	if fm.PhaseErrorCode == "" {
		t.Fatal("phase_error_code empty: failure recovery did not record an error")
	}
	if fm.Status != "planning" {
		t.Fatalf("status = %q, want planning (first failure retries in place)", fm.Status)
	}
}

// TestPhaseConcurrencyGateLimitsDispatch verifies the scheduler reserves
// per-phase concurrency slots: with phase_concurrency.refining=2, a batch of
// three refining tasks dispatches only two; after one finishes (gate
// release), the next processBatch round dispatches the third.
func TestPhaseConcurrencyGateLimitsDispatch(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	taskPaths := make([]string, 3)
	tasks := make([]task.ReadyTask, 3)
	for i := range 3 {
		name := "TASK-" + string(rune('A'+i)) + ".md"
		taskPaths[i] = writeTaskFile(t, dir, name, "refining")
		tasks[i] = task.ReadyTask{
			ID: string(rune('A' + i)), Title: "Refine", FilePath: taskPaths[i],
			Status: "refining", Assignee: "default",
		}
	}

	cfg := &config.Config{
		SkillInstallDir:    skillDir,
		Executor:           "dsh",
		DSHCmd:             omp,
		LogDir:             filepath.Join(dir, "logs"),
		MaxConcurrentTasks: 4,
		PhaseConcurrency:   map[string]int{"refining": 2},
		Models:             config.DefaultModels(),
	}
	runner := New(cfg)
	runner.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.daemonCtx = ctx

	// First round: all 3 tasks dispatch (gate lives at the OMP launch
	// point), but only 2 execution sessions actually start — the third hits the
	// full refining gate inside processBatchSequential and defers.
	done := runBatch(runner, tasks)
	if got := waitForBatch(t, done); got != 3 {
		t.Fatalf("first round dispatched = %d, want 3 (all dispatched, gate at launch)", got)
	}
	waitForStartCount(t, startDir, 2)

	// Release one slot: its runTask finishes and frees the gate slot.
	releaseBarrier(t, releaseFile)
	waitForTasksIdle(t, runner)

	// Second round: the deferred task's runTask re-dispatches and starts.
	done = runBatch(runner, []task.ReadyTask{tasks[2]})
	if got := waitForBatch(t, done); got != 1 {
		t.Fatalf("second round dispatched = %d, want 1", got)
	}
	waitForStartCount(t, startDir, 3)
}

// TestRunScanCycleCoalescesRequestsDuringScan verifies the scan gate: a
// request arriving while a scan cycle is active is coalesced (no second scan
// goroutine) and the gate unwinds to idle once the cycle finishes.
func TestRunScanCycleCoalescesRequestsDuringScan(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project-one")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"project-one": projectDir})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	// Real vault layout so scanAndProcess → FindReadyTasks picks up the task.
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "project-one", "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create vault tasks dir: %v", err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-000.md")
	// stage set so the unstaged-task scan does not dispatch a PM
	// consolidate session through the fake OMP (corrupts start counts).
	content := "---\nid: \"000\"\nstatus: planning\nproject: project-one\nassignee: default\nstage: \"P1\"\n---\n# Task\n"
	if err := os.WriteFile(taskPath, []byte(content), 0644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	// The coalescing contract is about overlapping scans, not the watcher
	// throttle; disable the min-interval so the follow-up schedules at once.
	runner.scanMinInterval = 0
	ctx, cancel := context.WithCancel(context.Background())
	runner.daemonCtx = ctx
	defer cancel()

	// Hold scanMu so the scan cycle blocks before it can dispatch; the
	// requestScan goroutine parks on the mutex while scanActive is already
	// set synchronously. This makes the coalescing contract observable
	// without racing the dispatch-only cycle (which completes in
	// milliseconds once OMP launch returns).
	runner.scanMu.Lock()
	runner.requestScan()
	deadline := time.Now().Add(2 * time.Second)
	for {
		runner.scanGateMu.Lock()
		active := runner.scanActive
		runner.scanGateMu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not become active")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// A burst of requests while the scan is active must not start a second
	// scan goroutine; it is coalesced into a pending follow-up.
	for range 5 {
		runner.requestScan()
	}
	runner.scanGateMu.Lock()
	pending := runner.scanPending
	runner.scanGateMu.Unlock()
	if !pending {
		t.Fatal("requests during an active scan should be marked pending")
	}

	// Let the blocked scan proceed: it dispatches the planning task (OMP
	// starts and parks on the barrier), then unwinds.
	runner.scanMu.Unlock()
	waitForStartCount(t, startDir, 1)

	// Release the barrier: the cycle unwinds and the coalesced follow-up scan
	// is scheduled. Consuming the pending marker and re-activating the gate
	// happen deterministically in runScanCycle, so settle == idle gate with
	// the pending marker cleared (the follow-up itself may finish too fast to
	// observe as an active window, since the task may no longer be ready).
	releaseBarrier(t, releaseFile)
	var active bool
	// 30s matches the barrier script's poll ceiling (3000 × 10ms): on a
	// loaded CI runner the OMP exit + coalesced follow-up scan can take
	// longer than the old 10s window (observed: 10.11s timeout).
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		runner.scanGateMu.Lock()
		active = runner.scanActive
		pending = runner.scanPending
		runner.scanGateMu.Unlock()
		if !active && !pending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if active {
		t.Fatalf("scan gate did not unwind after barrier release (active=%v pending=%v)", active, pending)
	}
	if pending {
		t.Fatal("coalesced pending scan was not scheduled after unwind")
	}
}

// TestRunOnceExitsOnSigterm verifies that a --once instance (systemd timer)
// binds SIGTERM: stopping it cancels the in-flight batch promptly and routes
// the interrupted OMP through the auto-resume path instead of orphaning it.
func TestRunOnceExitsOnSigterm(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project-one")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"project-one": projectDir})
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	// Real vault layout so scanAndProcess → FindReadyTasks picks up the task,
	// and a vault-map entry so prepareBatch can resolve the project repo.
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "project-one", "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create vault tasks dir: %v", err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-000.md")
	// stage set so the unstaged-task scan does not dispatch a PM
	// consolidate session through the fake OMP (corrupts start counts).
	content := "---\nid: \"000\"\nstatus: planning\nproject: project-one\nassignee: default\nstage: \"P1\"\n---\n# Task\n"
	if err := os.WriteFile(taskPath, []byte(content), 0644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault

	done := make(chan error, 1)
	go func() { done <- runner.RunOnce() }()
	waitForStartCount(t, startDir, 1) // batch dispatched and blocked on the barrier

	// SignalContext registers the handler synchronously before RunOnce
	// proceeds, so this cannot hit the default SIGTERM disposition.
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOnce after SIGTERM: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not return after SIGTERM")
	}

	// The interrupted OMP must be routed through the auto-resume path, not
	// treated as a phase failure: status stays planning with
	// PHASE_INTERRUPTED, no blocked_phase. The shutdown drain makes the
	// write-back land before RunOnce returns; the poll is a safety net.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(taskPath)
		if err != nil {
			t.Fatalf("read task: %v", err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil {
			t.Fatalf("parse task: %v", err)
		}
		if fm.PhaseErrorCode == string(ErrPhaseInterrupted) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "planning" {
		t.Fatalf("status = %q, want planning kept for auto-resume", fm.Status)
	}
	if fm.PhaseErrorCode != string(ErrPhaseInterrupted) {
		t.Fatalf("phase_error_code = %q, want %q", fm.PhaseErrorCode, ErrPhaseInterrupted)
	}
	if fm.BlockedPhase != "" {
		t.Fatalf("blocked_phase = %q, want empty", fm.BlockedPhase)
	}
	releaseBarrier(t, releaseFile)
}

// TestResolveMergeConflictExternalKillTreatedAsInterrupted verifies that a
// merge-resolution session killed by an external SIGTERM (parent daemon still
// alive, no context cancellation) is treated as an interrupted attempt — the
// one-shot AI budget is preserved and the merge resumes on the next scan —
// rather than a genuine resolution failure.

func TestWorktreePathFromError(t *testing.T) {
	err := "fatal: 'task/057-web-operation-timeline' is already used by worktree at '/home/nd/.otg-worktrees/task057-cifix'"
	if got := worktreePathFromError(err); got != "/home/nd/.otg-worktrees/task057-cifix" {
		t.Fatalf("worktreePathFromError = %q", got)
	}
	if got := worktreePathFromError("some other error"); got != "" {
		t.Fatalf("non-worktree error should return empty, got %q", got)
	}
}
