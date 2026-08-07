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

// TestAutoCloseStaleMergedTasks guards the deterministic closure loop: a
// merged PR with no pending_req flips the task to done; pending_req
// (requirement delta) and closed tasks stay untouched; already-done tasks
// are no-ops.
func TestAutoCloseStaleMergedTasks(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTask := func(id, status, merge, pending string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\ntitle: T" + id + "\nstatus: " + status + "\nmerge_status: " + merge + "\npending_req: " + pending + "\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	statusOf := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			t.Fatalf("parse: %v", err)
		}
		return fm.Status
	}

	stale := writeTask("001", "implementing", "merged", "false")  // should close
	delta := writeTask("002", "refining", "merged", "true")       // pending_req: keep
	closed := writeTask("003", "closed", "merged", "false")       // terminal: keep
	already := writeTask("004", "done", "merged", "false")        // no-op
	unmerged := writeTask("005", "implementing", "", "false")     // no PR: keep

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)

	if n := runner.autoCloseStaleMergedTasks(); n != 1 {
		t.Fatalf("auto-closed %d, want 1", n)
	}
	if got := statusOf(stale); got != "done" {
		t.Fatalf("stale merged task = %q, want done", got)
	}
	if got := statusOf(delta); got != "refining" {
		t.Fatalf("pending_req task = %q, want refining (untouched)", got)
	}
	if got := statusOf(closed); got != "closed" {
		t.Fatalf("closed task = %q, want closed (untouched)", got)
	}
	if got := statusOf(already); got != "done" {
		t.Fatalf("already done task = %q, want done", got)
	}
	if got := statusOf(unmerged); got != "implementing" {
		t.Fatalf("unmerged task = %q, want implementing (untouched)", got)
	}

	// Idempotent: second run closes nothing new.
	if n := runner.autoCloseStaleMergedTasks(); n != 0 {
		t.Fatalf("second run auto-closed %d, want 0 (idempotent)", n)
	}
}
