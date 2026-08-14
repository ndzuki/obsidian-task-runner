package yamlfrontmatter

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// lockFiles 列出当前锁目录中的所有 otg-task 锁文件。
func lockFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "otg", "locks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read lock dir: %v", err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths
}

// TestCleanStaleTaskLocks 覆盖三条判据：过期且无人持有 → 删除；
// 未过期 → 保留；过期但被持有（flock 未释放）→ 保留。
// 回归背景：acquireTaskLock 曾在 /tmp 只创建不删除，8/11-8/14 累积
// 15325 个锁文件，无 swap 机器上直接占不可回收的 shmem 内存。
func TestCleanStaleTaskLocks(t *testing.T) {
	old := time.Now().Add(-25 * time.Hour)

	t.Run("stale unlocked removed", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		unlock, err := acquireTaskLock(filepath.Join(t.TempDir(), "TASK-001.md"))
		if err != nil {
			t.Fatalf("acquire lock: %v", err)
		}
		unlock()
		files := lockFiles(t)
		if len(files) != 1 {
			t.Fatalf("want 1 lock file after acquire+release, got %d", len(files))
		}
		if err := os.Chtimes(files[0], old, old); err != nil {
			t.Fatalf("age lock file: %v", err)
		}
		CleanStaleTaskLocks()
		if got := lockFiles(t); len(got) != 0 {
			t.Fatalf("stale unlocked lock file should be removed, got %v", got)
		}
	})

	t.Run("fresh kept", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		unlock, err := acquireTaskLock(filepath.Join(t.TempDir(), "TASK-002.md"))
		if err != nil {
			t.Fatalf("acquire lock: %v", err)
		}
		unlock()
		CleanStaleTaskLocks()
		if got := lockFiles(t); len(got) != 1 {
			t.Fatalf("fresh lock file should be kept, got %d", len(got))
		}
	})

	t.Run("stale held kept", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		unlock, err := acquireTaskLock(filepath.Join(t.TempDir(), "TASK-003.md"))
		if err != nil {
			t.Fatalf("acquire lock: %v", err)
		}
		defer unlock()
		files := lockFiles(t)
		if len(files) != 1 {
			t.Fatalf("want 1 lock file while held, got %d", len(files))
		}
		if err := os.Chtimes(files[0], old, old); err != nil {
			t.Fatalf("age held lock file: %v", err)
		}
		CleanStaleTaskLocks()
		if got := lockFiles(t); len(got) != 1 {
			t.Fatalf("held lock file must survive cleanup, got %d", len(got))
		}
	})
}

// TestTaskLockDirUsesCacheNotTemp 锁定锁目录不再指向 /tmp：
// 回归背景 —— /tmp 为 tmpfs 且无 swap 时，锁文件残留占不可回收内存。
func TestTaskLockDirUsesCacheNotTemp(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := taskLockDir()
	want := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "otg", "locks")
	if dir != want {
		t.Fatalf("taskLockDir = %q, want %q", dir, want)
	}
	if dir == os.TempDir() {
		t.Fatal("taskLockDir must not fall back to os.TempDir when XDG cache is available")
	}
}
