package knowledge

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanProjectUsage(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	taskPath := func(project, name string) string {
		p := filepath.Join(vault, "Projects", project, "Tasks")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(p, name)
	}
	write := func(path, refs, applied string) {
		content := "---\nid: \"001\"\ntitle: T\nstatus: done\nknowledge_refs:\n" + refs + "\nknowledge_applied: \"" + applied + "\"\n---\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(taskPath("alpha", "TASK-001-a.md"),
		"  - core/go/connect-rpc.md\n  - References/core/go/wire-di.md\n  - core/go/connect-rpc.md", "2/3")
	write(taskPath("beta", "TASK-001-b.md"),
		"  - core/go/connect-rpc.md", "")

	usage, err := ScanProjectUsage(vault)
	if err != nil {
		t.Fatalf("ScanProjectUsage: %v", err)
	}
	// Dedup + prefix normalization: both forms map to core/go/connect-rpc.md.
	if got := usage.DocProjects["core/go/connect-rpc.md"]; !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("DocProjects[connect-rpc] = %v, want [alpha beta]", got)
	}
	if got := usage.DocProjects["core/go/wire-di.md"]; !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("DocProjects[wire-di] = %v, want [alpha]", got)
	}
	if len(usage.ProjectRefs["alpha"]) != 2 {
		t.Fatalf("ProjectRefs[alpha] = %v, want 2 refs", usage.ProjectRefs["alpha"])
	}
	if usage.ProjectApplied["alpha"] != 1 || usage.ProjectApplied["beta"] != 0 {
		t.Fatalf("ProjectApplied = %v, want alpha=1 beta=0", usage.ProjectApplied)
	}
	if len(usage.ProjectRefs["beta"]) != 1 {
		t.Fatalf("ProjectRefs[beta] = %v, want 1 ref", usage.ProjectRefs["beta"])
	}
}
