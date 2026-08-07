package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestSyncDependencyInheritance guards REQ depends_on → TASK blocked_by
// propagation: empty blocked_by inherits, explicit blocked_by never
// overwritten, cross-project refs keep their project prefix, and refs whose
// canonical task does not exist yet are skipped.
func TestSyncDependencyInheritance(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqDir := filepath.Join(projDir, "Requirements")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reqDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeReq := func(id, deps string) {
		t.Helper()
		content := "---\nid: \"" + id + "\"\ntitle: Req\nstatus: defined\ndepends_on: " + deps + "\n---\n# Req\n"
		if err := os.WriteFile(filepath.Join(reqDir, "REQ-"+id+"-r.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask := func(id, blocked string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nstatus: ready\nreq_doc: Projects/001-test/Requirements/REQ-" + id + "-r.md\n"
		if blocked != "" {
			content += "blocked_by:\n  - \"" + blocked + "\"\n"
		}
		content += "---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	blockedOf := func(path string) []string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return fm.BlockedBy
	}

	// REQ-001 depends on 023 (task exists), 999 (no task), cross-project.
	writeReq("001", `["023", "999", "other:REQ-005"]`)
	// REQ-002 depends on nothing.
	writeReq("002", `[]`)
	inherited := writeTask("001", "")
	writeTask("002", "")
	writeTask("023", "") // canonical task for the 023 dependency
	explicit := writeTask("003", "010")

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.syncDependencyInheritance()

	got := blockedOf(inherited)
	want := []string{"023", "other:005"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TASK-001 blocked_by = %v, want %v", got, want)
	}
	if got := blockedOf(explicit); !reflect.DeepEqual(got, []string{"010"}) {
		t.Fatalf("TASK-003 explicit blocked_by = %v, want [010] (never overwritten)", got)
	}

	// Idempotent: second run leaves the inherited list untouched.
	runner.syncDependencyInheritance()
	if got := blockedOf(inherited); !reflect.DeepEqual(got, want) {
		t.Fatalf("second run blocked_by = %v, want %v (idempotent)", got, want)
	}
}
