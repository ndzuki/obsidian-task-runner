package yamlfrontmatter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	t.Run("valid frontmatter", func(t *testing.T) {
		content := []byte(`---
id: "001"
title: "Test Task"
status: ready
plan_approved: true
plan_version: 2
tags:
  - backend
  - devops
blocked_by: []
---
# Body text
`)
		fm, err := Parse(content)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fm.ID != "001" {
			t.Errorf("id = %q, want %q", fm.ID, "001")
		}
		if fm.Title != "Test Task" {
			t.Errorf("title = %q, want %q", fm.Title, "Test Task")
		}
		if fm.Status != "ready" {
			t.Errorf("status = %q, want %q", fm.Status, "ready")
		}
		if !fm.PlanApproved {
			t.Error("plan_approved = false, want true")
		}
		if fm.PlanVersion != 2 {
			t.Errorf("plan_version = %d, want 2", fm.PlanVersion)
		}
		if len(fm.Tags) != 2 {
			t.Errorf("tags len = %d, want 2", len(fm.Tags))
		}
	})

	t.Run("no frontmatter", func(t *testing.T) {
		fm, err := Parse([]byte("# Just a heading"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fm != nil {
			t.Error("expected nil frontmatter")
		}
	})

	t.Run("empty frontmatter", func(t *testing.T) {
		fm, err := Parse([]byte("---\n---\nbody"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fm == nil {
			t.Fatal("expected non-nil frontmatter")
		}
		if fm.Status != "" {
			t.Errorf("status = %q, want empty", fm.Status)
		}
	})

	t.Run("unclosed frontmatter", func(t *testing.T) {
		_, err := Parse([]byte("---\nid: \"001\""))
		if err == nil {
			t.Error("expected error for unclosed frontmatter")
		}
	})

	t.Run("assignee values", func(t *testing.T) {
		tests := []struct {
			assignee string
			valid    bool
		}{
			{"deepseek", true},
			{"gpt", true},
			{"codex", false},
			{"claude", false},
			{"", false},
		}
		valid := map[string]bool{"deepseek": true, "gpt": true}
		for _, tt := range tests {
			if valid[tt.assignee] != tt.valid {
				t.Errorf("assignee %q valid = %v, want %v", tt.assignee, valid[tt.assignee], tt.valid)
			}
		}
	})
}

func TestUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-001-test.md")

	original := `---
id: "001"
title: "Original Title"
status: ready
plan_approved: false
plan_version: 0
assignee: ""
created: ""
updated: ""
completed: ""
---
# Body content
`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	err := Update(path, map[string]interface{}{
		"status":        "plan-review",
		"plan_version":  1,
		"plan_approved": true,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(updated)

	checks := []string{
		`status: plan-review`,
		`plan_version: 1`,
		`plan_approved: true`,
		`updated: "`, // timestamp was set
	}
	for _, c := range checks {
		if !contains(content, c) {
			t.Errorf("expected %q in updated content:\n%s", c, content)
		}
	}

	// Body preserved
	if !contains(content, "# Body content") {
		t.Error("body content lost")
	}
}

func TestUpdateNewField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-001-test.md")

	original := "---\nid: \"001\"\nstatus: ready\n---\n# Body\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	err := Update(path, map[string]interface{}{
		"target_branch": "task/001-foo",
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(updated)
	if !contains(content, `target_branch: task/001-foo`) {
		t.Errorf("new field missing:\n%s", content)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && search(s, sub)
}

func search(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
