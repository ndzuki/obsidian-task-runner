package notify

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// debounceFiles 列出当前 debounce 目录中的 kitty debounce 文件。
func debounceFiles(t *testing.T) []string {
	t.Helper()
	dir := kittyDebounceDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read debounce dir: %v", err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "otg-kitty-grilling-") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths
}

// TestCleanStaleKittyDebounceFiles 覆盖三条判据：过期且无人持有 → 删除；
// 未过期 → 保留；过期但被持有 → 保留。
// 回归背景：grilling debounce 文件（otg-kitty-grilling-*.lock）曾在 /tmp
// 只创建不删除，tmpfs 无 swap 机器上残留占不可回收内存。
func TestCleanStaleKittyDebounceFiles(t *testing.T) {
	old := time.Now().Add(-25 * time.Hour)

	t.Run("stale unlocked removed", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		path := kittyDebounceFile("test-001")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir debounce dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
			t.Fatalf("write debounce file: %v", err)
		}
		oldTime := time.Now().Add(-25 * time.Hour)
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("age debounce file: %v", err)
		}
		CleanStaleKittyDebounceFiles()
		if got := debounceFiles(t); len(got) != 0 {
			t.Fatalf("stale unlocked debounce file should be removed, got %v", got)
		}
	})

	t.Run("fresh kept", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		path := kittyDebounceFile("test-002")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir debounce dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
			t.Fatalf("write debounce file: %v", err)
		}
		CleanStaleKittyDebounceFiles()
		if got := debounceFiles(t); len(got) != 1 {
			t.Fatalf("fresh debounce file should be kept, got %d", len(got))
		}
	})

	t.Run("stale held kept", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		path := kittyDebounceFile("test-003")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir debounce dir: %v", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatalf("open debounce file: %v", err)
		}
		defer f.Close()
		// 持有中：flock 不释放（清理的 NB 探测必须失败 → 保留）
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatalf("flock debounce file: %v", err)
		}
		defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age held debounce file: %v", err)
		}
		CleanStaleKittyDebounceFiles()
		if got := debounceFiles(t); len(got) != 1 {
			t.Fatalf("held debounce file must survive cleanup, got %d", len(got))
		}
	})
}

// TestKittyDebounceDirUsesCacheNotTemp 锁定 debounce 目录不再指向 /tmp：
// 回归背景 —— /tmp 为 tmpfs 且无 swap 时，debounce 文件残留占不可回收内存。
func TestKittyDebounceDirUsesCacheNotTemp(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := kittyDebounceDir()
	want := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "otg", "locks")
	if dir != want {
		t.Fatalf("kittyDebounceDir = %q, want %q", dir, want)
	}
	if dir == os.TempDir() {
		t.Fatal("kittyDebounceDir must not fall back to os.TempDir when XDG cache is available")
	}
}
