package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// TestNormalizeCacheConvergence guards the mtime+size short-circuit behind
// scan-time normalization: unchanged documents are skipped (no re-read, no
// rewrite), a rewritten document converges after one pass (the post-write
// stamp is recorded), and an external edit is picked up on the next pass.
func TestNormalizeCacheConvergence(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-demo", "Tasks")
	reqsDir := filepath.Join(vault, "Projects", "001-demo", "Requirements")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reqsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-001-legacy.md")
	reqPath := filepath.Join(reqsDir, "REQ-001-legacy.md")
	// Legacy documents missing created/updated/tags.
	legacy := "---\nid: \"001\"\ntitle: Legacy\n---\n# Body\n"
	if err := os.WriteFile(taskPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reqPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)

	// First pass rewrites both (fields backfilled) and records stamps.
	runner.syncTaskSchemaDefaults()
	runner.syncReqSchemaDefaults()
	taskData, _ := os.ReadFile(taskPath)
	reqData, _ := os.ReadFile(reqPath)
	if !strings.Contains(string(taskData), "created:") || !strings.Contains(string(reqData), "created:") {
		t.Fatal("first pass did not backfill created")
	}
	taskMtime := mtimeOf(t, taskPath)
	reqMtime := mtimeOf(t, reqPath)

	// Second pass: no rewrite, mtime stable.
	time.Sleep(10 * time.Millisecond) // ensure any rewrite would bump mtime
	runner.syncTaskSchemaDefaults()
	runner.syncReqSchemaDefaults()
	if got := mtimeOf(t, taskPath); got != taskMtime {
		t.Fatalf("task rewritten on second pass (mtime %v → %v)", taskMtime, got)
	}
	if got := mtimeOf(t, reqPath); got != reqMtime {
		t.Fatalf("req rewritten on second pass (mtime %v → %v)", reqMtime, got)
	}

	// External edit (strip created): next pass re-backfills.
	broken := "---\nid: \"001\"\ntitle: Legacy\n---\n# Body\n"
	if err := os.WriteFile(taskPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	runner.syncTaskSchemaDefaults()
	if data, _ := os.ReadFile(taskPath); !strings.Contains(string(data), "created:") {
		t.Fatal("external edit not re-normalized")
	}
}

func mtimeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().UnixNano()
}
