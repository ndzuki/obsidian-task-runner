package daemon

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// TestSetTaskTempEnv 验证任务子进程获得专属临时目录：TMPDIR/TMP/TEMP/
// GOTMPDIR 全部指向 ~/.cache/otg/tasks/<runkey>，且原值被替换而非追加。
// 回归背景：go test/go build/mktemp 的临时产物此前落在全局 /tmp，
// 任务终态后无法按任务归属回收。
func TestSetTaskTempEnv(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMPDIR", "/original/tmp")
	cmd := &exec.Cmd{}
	taskPath := filepath.Join(t.TempDir(), "TASK-001.md")
	if err := setTaskTempEnv(cmd, taskPath); err != nil {
		t.Fatalf("setTaskTempEnv: %v", err)
	}
	runKey := taskRunKey(taskPath)
	want := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "otg", "tasks", runKey)
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"} {
		got := envValue(cmd.Env, key)
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if envValue(cmd.Env, "TMPDIR") == "/original/tmp" {
		t.Error("TMPDIR must not retain the original value")
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("task temp dir not created: %v", err)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

// TestCleanupTaskArtifacts 验证终态任务清理：done+merged 任务移除其专属
// 临时目录与 PID 文件；非终态任务（implementing）不受影响。
func TestCleanupTaskArtifacts(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	repo := createRepository(t, dir)
	runner := New(&config.Config{
		ObsidianVault: vault,
		LogDir:        filepath.Join(dir, "logs"),
	})
	runner.logger = log.New(io.Discard, "", 0)

	// done+merged 任务：临时目录 + PID 文件应被清理。
	donePath := writeTaskWithFM(t, filepath.Join(vault, "Projects", "demo", "Tasks"),
		"TASK-001-done.md", "status: done\nmerge_status: merged\n")
	doneKey := taskRunKey(donePath)
	doneTmp := taskTempDirForKey(doneKey)
	if err := os.MkdirAll(doneTmp, 0o700); err != nil {
		t.Fatalf("mkdir task temp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(doneTmp, "artifact.bin"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp artifact: %v", err)
	}
	pidPath := taskPIDFile(runner.taskLogDir(), "test", donePath)
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		t.Fatalf("mkdir task log dir: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	runner.cleanupTaskArtifacts(donePath, repo)

	if _, err := os.Stat(doneTmp); err == nil {
		t.Errorf("done task temp dir must be removed: %s", doneTmp)
	}
	if _, err := os.Stat(pidPath); err == nil {
		t.Errorf("done task PID file must be removed: %s", pidPath)
	}

	// implementing 任务：临时目录保留。
	livePath := writeTaskWithFM(t, filepath.Join(vault, "Projects", "demo", "Tasks"),
		"TASK-002-live.md", "status: implementing\n")
	liveTmp := taskTempDirForKey(taskRunKey(livePath))
	if err := os.MkdirAll(liveTmp, 0o700); err != nil {
		t.Fatalf("mkdir live task temp: %v", err)
	}
	runner.cleanupTaskArtifacts(livePath, repo)
	if _, err := os.Stat(liveTmp); err != nil {
		t.Errorf("live task temp dir must be kept: %v", err)
	}
}

// TestCleanupTaskArtifactsSkippedWhileActive 验证 taskRuns 调度保护：
// 任务在调度中（taskRuns 有记录）即使已终态也不清理。
func TestCleanupTaskArtifactsSkippedWhileActive(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	taskPath := writeTaskWithFM(t, filepath.Join(dir, "Tasks"), "TASK-001.md", "status: done\nmerge_status: merged\n")
	runner := New(&config.Config{ObsidianVault: filepath.Join(dir, "vault")})
	runner.logger = log.New(io.Discard, "", 0)
	tmp := taskTempDirForKey(taskRunKey(taskPath))
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatalf("mkdir task temp: %v", err)
	}
	runner.taskRuns.Store(taskRunKey(taskPath), struct{}{})
	runner.cleanupTaskArtifacts(taskPath, "")
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("active task temp dir must survive cleanup: %v", err)
	}
	runner.taskRuns.Delete(taskRunKey(taskPath))
	runner.cleanupTaskArtifacts(taskPath, "")
	if _, err := os.Stat(tmp); err == nil {
		t.Errorf("inactive done task temp dir must be removed")
	}
}
