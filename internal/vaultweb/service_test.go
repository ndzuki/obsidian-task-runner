package vaultweb

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestVault builds a vault with two projects and several tasks plus a
// design library in the first project.
func newTestVault(t *testing.T) string {
	t.Helper()
	vault := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(vault, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	task := func(projectDir, file, id, title, status, priority, extra string) {
		write(filepath.Join("Projects", projectDir, "Tasks", file),
			"---\nid: \""+id+"\"\ntitle: "+title+"\nproject: demo\nproject_id: \"001\"\nreq_doc: REQ-"+id+".md\nassignee: default\nstatus: "+status+"\npriority: "+priority+"\nplan_version: 2\ngeneration: 1\n"+extra+"---\n# "+title+"\n")
	}
	task("001-demo", "TASK-001-demo.md", "001", "Add KB search", "implementing", "P1", "")
	task("001-demo", "TASK-002-demo.md", "002", "Fix watcher", "blocked", "P2", "blocked_by:\n  - \"001\"\nphase_error_code: DOCUMENT_INVALID\n")
	task("001-demo", "TASK-003-demo.md", "003", "Ship v1", "done", "P0", "")
	task("002-other", "TASK-010-other.md", "010", "Other project", "refining", "P3", "")

	// Design library for 001-demo.
	write(filepath.Join("Projects", "001-demo", "Design", "glossary.md"), "---\nschema: glossary-v1\n---\n# Glossary\nOrder = 订单\n")
	write(filepath.Join("Projects", "001-demo", "Design", "contracts", "order-api.md"), "---\nschema: contract-v1\nid: order-api\ntitle: Order API\n---\n# Contract\n")
	write(filepath.Join("Projects", "001-demo", "Design", "decisions", "ADR-001.md"), "---\nschema: decision-v1\nid: ADR-001\ntitle: Storage\nstatus: accepted\n---\n# Decision\n")
	write(filepath.Join("Projects", "001-demo", "Design", "waves", "wave-0.md"), "---\nschema: wave-v1\nid: wave-0\ntitle: Contract first\n---\n# Wave 0\n")
	return vault
}

func TestProjects(t *testing.T) {
	s := New(newTestVault(t))
	projects, err := s.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("projects=%d, want 2", len(projects))
	}
	demo := projects[0]
	if demo.ID != "001" || demo.Name != "demo" || demo.DirName != "001-demo" {
		t.Fatalf("demo project fields wrong: %+v", demo)
	}
	if demo.TaskCount != 3 || demo.ByStatus["blocked"] != 1 || demo.ByStatus["done"] != 1 || demo.ByStatus["implementing"] != 1 {
		t.Fatalf("demo counts wrong: %+v", demo)
	}
}

func TestTasksAllStatuses(t *testing.T) {
	s := New(newTestVault(t))
	tasks, err := s.Tasks("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks=%d, want 3 (blocked and done included)", len(tasks))
	}
	byID := map[string]TaskDTO{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	if byID["002"].Status != "blocked" || byID["002"].PhaseErrorCode != "DOCUMENT_INVALID" {
		t.Fatalf("blocked task wrong: %+v", byID["002"])
	}
	if len(byID["002"].BlockedBy) != 1 || byID["002"].BlockedBy[0] != "001" {
		t.Fatalf("blocked_by wrong: %+v", byID["002"].BlockedBy)
	}
}

func TestPathSafetyProjectTraversal(t *testing.T) {
	s := New(newTestVault(t))
	for _, bad := range []string{"../etc/passwd", "../../", "..", "001-demo/../../x", "missing"} {
		if _, err := s.Tasks(bad); err == nil {
			t.Fatalf("Tasks(%q) should fail, got nil error", bad)
		}
	}
}

func TestViewsWhitelist(t *testing.T) {
	s := New(newTestVault(t))
	ids := s.Views()
	if len(ids) != 4 {
		t.Fatalf("views=%v, want 4 whitelisted ids", ids)
	}
	// Unknown view rejected.
	if _, err := s.View("demo", "arbitrary-dataview-query"); err == nil {
		t.Fatal("unknown view must be rejected")
	}
	if _, err := s.View("demo", "../etc/passwd"); err == nil {
		t.Fatal("traversal view id must be rejected")
	}
	// Known views resolve.
	blocked, err := s.View("demo", "tasks-blocked")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Rows) != 1 || blocked.Rows[0]["id"] != "002" {
		t.Fatalf("tasks-blocked rows wrong: %+v", blocked.Rows)
	}
	overview, err := s.View("demo", "tasks-overview")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Rows) != 3 {
		t.Fatalf("tasks-overview rows=%d, want 3", len(overview.Rows))
	}
	running, err := s.View("demo", "tasks-running")
	if err != nil {
		t.Fatal(err)
	}
	if len(running.Rows) != 1 || running.Rows[0]["id"] != "001" {
		t.Fatalf("tasks-running rows wrong: %+v", running.Rows)
	}
}

func TestDesignSummary(t *testing.T) {
	s := New(newTestVault(t))
	sum, err := s.DesignSummary("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Valid || sum.Revision != 0 || len(sum.Contracts) != 1 || len(sum.Decisions) != 1 || len(sum.Waves) != 1 || !sum.HasGlossary {
		t.Fatalf("design summary wrong: %+v", sum)
	}
	// Project without a design library → empty but valid (no error).
	other, err := s.DesignSummary("other")
	if err != nil {
		t.Fatal(err)
	}
	if other.Valid {
		t.Fatalf("project without design library should be invalid, got %+v", other)
	}
}

func TestDesignArtifact(t *testing.T) {
	s := New(newTestVault(t))
	content, err := s.DesignArtifact("demo", "contract", "order-api.md")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("contract content empty")
	}
	// Traversal name rejected.
	for _, bad := range []string{"../glossary.md", "../../x", "a/b", ".."} {
		if _, err := s.DesignArtifact("demo", "contract", bad); err == nil {
			t.Fatalf("artifact name %q must be rejected", bad)
		}
	}
	// Unknown kind rejected.
	if _, err := s.DesignArtifact("demo", "js", "x.js"); err == nil {
		t.Fatal("unknown artifact kind must be rejected")
	}
}
