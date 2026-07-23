package yamlfrontmatter

import (
	"os"
	"path/filepath"
	"strings"
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

	t.Run("quoted numeric hours", func(t *testing.T) {
		fm, err := Parse([]byte(`---
estimated_hours: "40"
actual_hours: "42"
---
`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fm.EstimatedHours != 40 {
			t.Errorf("estimated_hours = %v, want 40", fm.EstimatedHours)
		}
		if fm.ActualHours != 42 {
			t.Errorf("actual_hours = %v, want 42", fm.ActualHours)
		}
	})

	t.Run("non-numeric quoted hours", func(t *testing.T) {
		_, err := Parse([]byte(`---
actual_hours: "forty-two"
---
`))
		if err == nil {
			t.Error("expected error for non-numeric actual_hours")
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

func TestValidate(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid file", func(t *testing.T) {
		path := filepath.Join(dir, "valid.md")
		os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\n---\n# Body\n"), 0644)
		if err := Validate(path); err != nil {
			t.Errorf("expected valid, got: %v", err)
		}
	})

	t.Run("corrupted file", func(t *testing.T) {
		path := filepath.Join(dir, "corrupt.md")
		// Simulates OMP agent writing orphaned text after grill_context: ""
		os.WriteFile(path, []byte("---\nid: \"001\"\ngrill_context: \"\"\n  orphaned text\n---\n# Body\n"), 0644)
		if err := Validate(path); err == nil {
			t.Error("expected error for corrupted file, got nil")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if err := Validate(filepath.Join(dir, "nope.md")); err == nil {
			t.Error("expected error for missing file")
		}
	})
}

func TestRepair(t *testing.T) {
	dir := t.TempDir()

	t.Run("already valid", func(t *testing.T) {
		path := filepath.Join(dir, "ok.md")
		os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\n---\n# Body\n"), 0644)
		if err := Repair(path); err != nil {
			t.Errorf("repair should be no-op on valid file: %v", err)
		}
	})

	t.Run("removes orphaned text and preserves lists", func(t *testing.T) {
		path := filepath.Join(dir, "corrupt-list.md")
		// Corrupt: orphaned text after grill_context line, plus a valid multi-line blocked_by
		os.WriteFile(path, []byte(
			"---\nid: \"061\"\nstatus: needs-grilling\ngrill_context: \"\"\n"+
				"  需求成熟度评估 immature\n"+
				"  建议追问维度：\n"+
				"blocked_by:\n"+
				"  - TASK-010\n"+
				"  - TASK-020\n"+
				"grill_prev_status: \"\"\n---\n# Body text\n"), 0644)

		if err := Repair(path); err != nil {
			t.Fatalf("repair failed: %v", err)
		}

		data, _ := os.ReadFile(path)
		content := string(data)

		// Valid fields preserved
		for _, want := range []string{`id: "061"`, `status: needs-grilling`, `grill_context: ""`, `grill_prev_status: ""`} {
			if !contains(content, want) {
				t.Errorf("missing preserved field %q:\n%s", want, content)
			}
		}
		// Multi-line list preserved
		for _, want := range []string{`blocked_by:`, `  - TASK-010`, `  - TASK-020`} {
			if !contains(content, want) {
				t.Errorf("missing list item %q:\n%s", want, content)
			}
		}
		// Orphaned text removed
		for _, bad := range []string{"需求成熟度评估", "建议追问维度"} {
			if contains(content, bad) {
				t.Errorf("orphaned text not removed: %q", bad)
			}
		}
		// Body preserved
		if !contains(content, "# Body text") {
			t.Error("body content lost")
		}
		// Repaired file validates
		if err := Validate(path); err != nil {
			t.Errorf("repaired file still invalid: %v", err)
		}
	})

	t.Run("no frontmatter", func(t *testing.T) {
		path := filepath.Join(dir, "no-fm.md")
		os.WriteFile(path, []byte("# No frontmatter\n"), 0644)
		if err := Repair(path); err == nil {
			t.Error("expected error for file without frontmatter")
		}
	})
}

func TestUpdateDeclinesCorruptedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.md")
	// Corrupt file: text that is not valid YAML in the frontmatter block
	os.WriteFile(path, []byte(
		"---\nid: \"061\"\nstatus: needs-grilling\n"+
			"非法的YAML键名无冒号\n---\n# Body\n"), 0644)

	err := Update(path, map[string]interface{}{"status": "refining"})
	if err == nil {
		t.Fatal("expected Update to fail on corrupted frontmatter")
	}
	if !strings.Contains(err.Error(), "parse frontmatter") {
		t.Errorf("expected 'parse frontmatter' in error, got: %v", err)
	}
}

func TestValidateTaskDocumentUnescapedTag(t *testing.T) {
	dir := t.TempDir()

	t.Run("rejects unescaped <id> in body", func(t *testing.T) {
		path := filepath.Join(dir, "unescaped.md")
		os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\nproject: test\nreq_doc: Projects/test/REQ-001.md\n---\n# Title\n- AC: <id> in body.\n"), 0644)
		if err := ValidateTaskDocument(path); err == nil {
			t.Error("expected error for unescaped <id> in body")
		}
	})

	t.Run("accepts escaped \\<id\\> in body", func(t *testing.T) {
		path := filepath.Join(dir, "escaped.md")
		os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\nproject: test\nreq_doc: Projects/test/REQ-001.md\n---\n# Title\n- AC: \\<id\\> escaped.\n"), 0644)
		if err := ValidateTaskDocument(path); err != nil {
			t.Errorf("unexpected error for escaped \\<id\\>: %v", err)
		}
	})
}

func TestEscapeBodyTags(t *testing.T) {
	result := escapeBodyTags("use <id> and <slug> here")
	if !strings.Contains(result, "\\<id\\>") {
		t.Errorf("expected escaped \\<id\\>, got %q", result)
	}
	if !strings.Contains(result, "\\<slug\\>") {
		t.Errorf("expected escaped \\<slug\\>, got %q", result)
	}
	// Already escaped should be unchanged
	rawEscaped := string([]byte{'u', 's', 'e', ' ', '\\', '<', 'i', 'd', '\\', '>', ' ', 'h', 'e', 'r', 'e'})
	if escapeBodyTags(rawEscaped) != rawEscaped {
		t.Error("already-escaped tag should be unchanged")
	}
}

func TestUpdatePreservesFileOnInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.md")
	original := "---\nid: \"001\"\nstatus: ready\n---\n# Body\n"
	os.WriteFile(path, []byte(original), 0644)

	err := Update(path, map[string]interface{}{"status": "refining"})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !contains(string(data), "status: refining") {
		t.Error("status not updated")
	}
}

func TestUpdatePreservesBlockScalar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "block.md")
	// File with a block scalar field
	original := "---\nid: \"001\"\nstatus: needs-grilling\ngrill_context: |\n  first question\n  second question\nassignee: gpt\n---\n# Body\n"
	os.WriteFile(path, []byte(original), 0644)

	// Update an unrelated field — block scalar content must survive.
	err := Update(path, map[string]interface{}{"status": "refining"})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	for _, want := range []string{"first question", "second question", "grill_context: |"} {
		if !contains(content, want) {
			t.Errorf("block scalar content lost: missing %q", want)
		}
	}
	if !contains(content, "status: refining") {
		t.Error("status not updated")
	}
}

func TestUpdateClearsBlockScalar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "block-clear.md")
	original := "---\nid: \"001\"\nstatus: needs-grilling\ngrill_context: |\n  first question\n  second question\n---\n# Body\n"
	os.WriteFile(path, []byte(original), 0644)

	// Clear the block scalar field — set it to empty string.
	err := Update(path, map[string]interface{}{
		"grill_context": "",
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	// Check file is valid
	fm, err := Parse(data)
	if err != nil {
		t.Fatalf("result is invalid: %v\n%s", err, content)
	}
	if fm.GrillContext != "" {
		t.Errorf("GrillContext = %q, want empty", fm.GrillContext)
	}
	// Old block scalar content must not remain
	if contains(content, "first question") || contains(content, "second question") {
		t.Error("block scalar continuation lines not removed")
	}
}

func TestUpdateFieldOrderPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "order.md")
	original := "---\nid: \"001\"\ntitle: Test\nstatus: ready\nassignee: default\n---\n# Body\n"
	os.WriteFile(path, []byte(original), 0644)

	err := Update(path, map[string]interface{}{"plan_version": 2, "updated": "2024-01-01T00:00:00+08:00"})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	// id must appear before plan_version (new field appended at end).
	idPos := strings.Index(content, "id:")
	pvPos := strings.Index(content, "plan_version:")
	if idPos < 0 || pvPos < 0 {
		t.Fatal("missing expected fields")
	}
	if idPos > pvPos {
		t.Errorf("id (pos %d) should appear before plan_version (pos %d)", idPos, pvPos)
	}
}

func TestValidateRejectsNoFrontmatter(t *testing.T) {
	dir := t.TempDir()

	// File without frontmatter
	path := filepath.Join(dir, "no-fm.md")
	os.WriteFile(path, []byte("# No frontmatter here\n"), 0644)
	if err := Validate(path); err == nil {
		t.Error("expected error for file without frontmatter")
	}

	// File with valid frontmatter should pass
	path2 := filepath.Join(dir, "ok.md")
	os.WriteFile(path2, []byte("---\nid: \"001\"\nstatus: ready\n---\n# Body\n"), 0644)
	if err := Validate(path2); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestRepairPreservesBlockScalar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "block-repair.md")
	// Valid block scalar field + corrupt orphaned text elsewhere
	os.WriteFile(path, []byte(
		"---\nid: \"061\"\nstatus: needs-grilling\ngrill_context: |\n"+
			"  question one\n"+
			"  question two\n"+
			"BROKEN ORPHAN\n"+
			"assignee: gpt\n---\n# Body text\n"), 0644)

	if err := Repair(path); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	// Block scalar content preserved
	for _, want := range []string{"question one", "question two", "grill_context: |"} {
		if !contains(content, want) {
			t.Errorf("block scalar content lost: missing %q", want)
		}
	}
	// Orphaned text removed
	if contains(content, "BROKEN ORPHAN") {
		t.Error("orphaned text not removed")
	}
	// Valid field preserved
	if !contains(content, "assignee: gpt") {
		t.Error("valid field lost")
	}
	// Repaired file validates
	fm, err := Parse(data)
	if err != nil {
		t.Fatalf("repaired file invalid: %v\n%s", err, content)
	}
	if fm.GrillContext != "question one\nquestion two\n" {
		t.Errorf("GrillContext = %q, want multi-line content", fm.GrillContext)
	}
}
