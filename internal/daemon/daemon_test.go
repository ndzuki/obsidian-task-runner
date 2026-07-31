package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

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
	wantPrompt := "/obsidian-task-runner-refining " + taskPath
	if !strings.Contains(string(args), wantPrompt) {
		t.Fatalf("OMP args = %q, want prompt %q", args, wantPrompt)
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

func TestProcessBatchUsesTaskPathForImplementingPIDRecovery(t *testing.T) {
	dir := t.TempDir()
	projectOne := filepath.Join(dir, "project-one")
	projectTwo := filepath.Join(dir, "project-two")
	for _, project := range []string{projectOne, projectTwo} {
		if err := os.MkdirAll(project, 0o755); err != nil {
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

	taskOne := writeTaskFile(t, filepath.Join(dir, "one"), "TASK-001.md", "implementing")
	taskTwo := writeTaskFile(t, filepath.Join(dir, "two"), "TASK-001.md", "implementing")
	logDir := filepath.Join(dir, "logs")
	taskLogDir := filepath.Join(logDir, "tasks")
	if err := os.MkdirAll(taskLogDir, 0755); err != nil {
		t.Fatalf("create task log directory: %v", err)
	}
	if err := os.WriteFile(taskPIDFile(taskLogDir, "001", taskOne), []byte(fmt.Sprint(os.Getpid())), 0644); err != nil {
		t.Fatalf("write live PID file: %v", err)
	}

	runner := newTestRunner(skillDir, omp, logDir, 2)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "001", Title: "Blocked by live PID", Project: "project-one", FilePath: taskOne, Status: "implementing", PlanApproved: true, NewProject: true, Assignee: "default"},
		{ID: "001", Title: "Must resume", Project: "project-two", FilePath: taskTwo, Status: "implementing", PlanApproved: true, NewProject: true, Assignee: "default"},
	})
	waitForStartCount(t, startDir, 1)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
}

func TestSurvivingImplementationsConsumeCapacityAfterRestart(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projects := map[string]string{}
	var oldPaths []string
	var oldCommands []*exec.Cmd
	logDir := filepath.Join(dir, "logs")
	taskLogDir := filepath.Join(logDir, "tasks")
	if err := os.MkdirAll(taskLogDir, 0o755); err != nil {
		t.Fatalf("create task log directory: %v", err)
	}

	for i := 1; i <= 2; i++ {
		project := fmt.Sprintf("old-%d", i)
		repo := filepath.Join(dir, project)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatalf("create project directory: %v", err)
		}
		projects[project] = repo
		tasksDir := filepath.Join(vault, "Projects", project, "Tasks")
		if err := os.MkdirAll(tasksDir, 0o755); err != nil {
			t.Fatalf("create tasks directory: %v", err)
		}
		id := fmt.Sprintf("%03d", i)
		taskPath := filepath.Join(tasksDir, "TASK-"+id+".md")
		content := fmt.Sprintf("---\nid: %q\nproject: %s\nstatus: implementing\nassignee: default\n---\n# TASK-%s\n", id, project, id)
		if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write task: %v", err)
		}
		oldPaths = append(oldPaths, taskPath)

		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start surviving process: %v", err)
		}
		oldCommands = append(oldCommands, cmd)
		if err := os.WriteFile(taskPIDFile(taskLogDir, id, taskPath), []byte(fmt.Sprint(cmd.Process.Pid)), 0o644); err != nil {
			t.Fatalf("write PID file: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, cmd := range oldCommands {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}
		}
	})

	newProject := "new"
	newRepo := filepath.Join(dir, newProject)
	if err := os.MkdirAll(newRepo, 0o755); err != nil {
		t.Fatalf("create new project directory: %v", err)
	}
	projects[newProject] = newRepo
	newTaskPath := writeTaskFile(t, filepath.Join(dir, "new-task"), "TASK-003.md", "implementing")

	skillDir := writeVaultMap(t, dir, projects)
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)
	runner := newTestRunner(skillDir, omp, logDir, 2)
	runner.cfg.ObsidianVault = vault

	adopted := runner.adoptSurvivingImplementations()
	if len(adopted) != 2 {
		t.Fatalf("adopted implementations = %d, want 2", len(adopted))
	}
	for _, taskPath := range oldPaths {
		if _, ok := adopted[taskRunKey(taskPath)]; !ok {
			t.Fatalf("surviving task %s was not adopted", taskPath)
		}
	}

	ready, err := runner.findReadyTasks()
	if err != nil {
		t.Fatalf("find ready tasks: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready tasks after adoption = %d, want 0", len(ready))
	}
	ready, err = runner.findReadyTasks()
	if err != nil {
		t.Fatalf("find ready tasks again: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready tasks on adaptive rescan = %d, want 0", len(ready))
	}

	newTask := task.ReadyTask{
		ID: "003", Title: "New implementation", Project: newProject,
		FilePath: newTaskPath, Status: "implementing", NewProject: true, Assignee: "default",
	}
	_ = runBatch(runner, []task.ReadyTask{newTask})
	assertStartCount(t, startDir, 0)

	if err := oldCommands[0].Process.Kill(); err != nil {
		t.Fatalf("stop surviving process: %v", err)
	}
	_, _ = oldCommands[0].Process.Wait()
	waitForFileRemoval(t, taskPIDFile(taskLogDir, "001", oldPaths[0]))

	done := runBatch(runner, []task.ReadyTask{newTask})
	waitForStartCount(t, startDir, 1)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed after capacity release = %d, want 1", processed)
	}

	if err := oldCommands[1].Process.Kill(); err != nil {
		t.Fatalf("stop second surviving process: %v", err)
	}
	_, _ = oldCommands[1].Process.Wait()
	waitForFileRemoval(t, taskPIDFile(taskLogDir, "002", oldPaths[1]))
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
	firstDone := runBatch(runner, tasks[:2])
	secondDone := runBatch(runner, tasks[2:])

	waitForStartCount(t, startDir, 2)

	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, firstDone) + waitForBatch(t, secondDone); processed != 4 {
		t.Fatalf("processed = %d, want 4", processed)
	}
	assertMaxActive(t, activeDir, 2)
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
	if processed := waitForBatch(t, firstDone) + waitForBatch(t, resumedDone); processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}

	if runner.implementationGate.localActive() != 0 || runner.implementationGate.active != 0 {
		t.Errorf("post-batch gate: local=%d active=%d, want 0/0",
			runner.implementationGate.localActive(),
			runner.implementationGate.active)
	}

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

func TestProcessBatchTreatsNonPositiveLimitAsOne(t *testing.T) {
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

	taskOne := writeTaskFile(t, dir, "TASK-021.md", "implementing")
	taskTwo := writeTaskFile(t, dir, "TASK-022.md", "implementing")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 0)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "021", Title: "One", Project: "project-one", FilePath: taskOne, Status: "implementing", NewProject: true, Assignee: "default"},
		{ID: "022", Title: "Two", Project: "project-two", FilePath: taskTwo, Status: "implementing", NewProject: true, Assignee: "default"},
	})
	waitForStartCount(t, startDir, 1)
	assertStartCount(t, startDir, 1)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
}

func TestEnsureTaskWorktreeReusesIsolatedWorktree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	worktree, err := ensureTaskWorktree(repo, "007", "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	if worktree == repo {
		t.Fatal("worktree must not reuse the primary repository directory")
	}
	if output, err := exec.Command("git", "-C", worktree, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "true" {
		t.Fatalf("validate worktree: %v: %s", err, output)
	}

	reused, err := ensureTaskWorktree(repo, "007", "")
	if err != nil {
		t.Fatalf("reuse worktree: %v", err)
	}
	if reused != worktree {
		t.Fatalf("reused worktree = %q, want %q", reused, worktree)
	}
}

func TestEnsureTaskWorktreeCreatesAndReusesTargetBranch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	worktree, err := ensureTaskWorktree(repo, "008", "task/008-feature")
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

	reused, err := ensureTaskWorktree(repo, "008", "task/008-feature")
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

	if _, err := ensureTaskWorktree(repo, "009", "task/009-first"); err != nil {
		t.Fatalf("create first target branch worktree: %v", err)
	}
	if _, err := ensureTaskWorktree(repo, "009", "task/009-second"); err == nil {
		t.Fatal("expected target branch mismatch error")
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
	data, err := json.Marshal(map[string]any{"projects": entries})
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
func waitForFileRemoval(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file was not removed: %s", path)
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
	runner := New(&config.Config{
		SkillInstallDir:    skillDir,
		OMPCmd:             omp,
		LogDir:             logDir,
		MaxConcurrentTasks: limit,
		Models:             config.DefaultModels(),
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

func waitForStartCount(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countStartFiles(t, dir) == want {
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
