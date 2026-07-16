package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

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

	taskOne := writeTaskFile(t, dir, "TASK-001.md", "ready")
	taskTwo := writeTaskFile(t, dir, "TASK-002.md", "ready")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)

	done := runBatch(runner, []task.ReadyTask{
		{ID: "001", Title: "One", Project: "project-one", FilePath: taskOne, Status: "ready", Assignee: "flash"},
		{ID: "002", Title: "Two", Project: "project-two", FilePath: taskTwo, Status: "ready", Assignee: "flash"},
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

	taskOne := writeTaskFile(t, filepath.Join(dir, "one"), "TASK-001.md", "ready")
	taskTwo := writeTaskFile(t, filepath.Join(dir, "two"), "TASK-001.md", "ready")
	if taskRunKey(taskOne) == taskRunKey(taskTwo) {
		t.Fatal("different task files must have distinct run keys")
	}

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "001", Title: "One", Project: "project-one", FilePath: taskOne, Status: "ready", Assignee: "flash"},
		{ID: "001", Title: "Two", Project: "project-two", FilePath: taskTwo, Status: "ready", Assignee: "flash"},
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

func TestProcessBatchUsesTaskPathForImplementingPIDRecovery(t *testing.T) {
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
		{ID: "001", Title: "Blocked by live PID", Project: "project-one", FilePath: taskOne, Status: "implementing", PlanApproved: true, NewProject: true, Assignee: "flash"},
		{ID: "001", Title: "Must resume", Project: "project-two", FilePath: taskTwo, Status: "implementing", PlanApproved: true, NewProject: true, Assignee: "flash"},
	})
	waitForStartCount(t, startDir, 1)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
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
		{ID: "011", Title: "One", Project: "shared", FilePath: taskOne, Status: "plan-review", PlanApproved: true, Assignee: "flash"},
		{ID: "012", Title: "Two", Project: "shared", FilePath: taskTwo, Status: "plan-review", PlanApproved: true, Assignee: "flash"},
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

	taskOne := writeTaskFile(t, dir, "TASK-021.md", "ready")
	taskTwo := writeTaskFile(t, dir, "TASK-022.md", "ready")
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 0)
	done := runBatch(runner, []task.ReadyTask{
		{ID: "021", Title: "One", Project: "project-one", FilePath: taskOne, Status: "ready", Assignee: "flash"},
		{ID: "022", Title: "Two", Project: "project-two", FilePath: taskTwo, Status: "ready", Assignee: "flash"},
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

	worktree, err := ensureTaskWorktree(repo, "007")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	if worktree == repo {
		t.Fatal("worktree must not reuse the primary repository directory")
	}
	if output, err := exec.Command("git", "-C", worktree, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "true" {
		t.Fatalf("validate worktree: %v: %s", err, output)
	}

	reused, err := ensureTaskWorktree(repo, "007")
	if err != nil {
		t.Fatalf("reuse worktree: %v", err)
	}
	if reused != worktree {
		t.Fatalf("reused worktree = %q, want %q", reused, worktree)
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
		{name: "unapproved plan", task: task.ReadyTask{Status: "plan-review"}, want: false},
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
		if err := os.WriteFile(releaseFile, nil, 0644); err != nil {
			t.Errorf("release barrier during cleanup: %v", err)
		}
	})
	script := "#!/bin/sh\nmkdir -p \"$START_DIR\"\nprintf '%s\\n' \"$PWD\" > \"$START_DIR/$$\"\nwhile [ ! -f \"$RELEASE_FILE\" ]; do sleep 0.01; done\n"
	if err := os.WriteFile(omp, []byte(script), 0755); err != nil {
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
