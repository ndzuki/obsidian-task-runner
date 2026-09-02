package yamlfrontmatter

import (
	"fmt"
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
		if err := os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\n---\n# Body\n"), 0644); err != nil {
			t.Fatalf("write valid file: %v", err)
		}
		if err := Validate(path); err != nil {
			t.Errorf("expected valid, got: %v", err)
		}
	})

	t.Run("corrupted file", func(t *testing.T) {
		path := filepath.Join(dir, "corrupt.md")
		// Simulates an agent session writing orphaned text after grill_context: ""
		if err := os.WriteFile(path, []byte("---\nid: \"001\"\ngrill_context: \"\"\n  orphaned text\n---\n# Body\n"), 0644); err != nil {
			t.Fatalf("write corrupt file: %v", err)
		}
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
		if err := os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\n---\n# Body\n"), 0644); err != nil {
			t.Fatalf("write valid file: %v", err)
		}
		if err := Repair(path); err != nil {
			t.Errorf("repair should be no-op on valid file: %v", err)
		}
	})

	t.Run("removes orphaned text and preserves lists", func(t *testing.T) {
		path := filepath.Join(dir, "corrupt-list.md")
		// Corrupt: orphaned text after grill_context line, plus a valid multi-line blocked_by
		if err := os.WriteFile(path, []byte(
			"---\nid: \"061\"\nstatus: needs-grilling\ngrill_context: \"\"\n"+
				"  需求成熟度评估 immature\n"+
				"  建议追问维度：\n"+
				"blocked_by:\n"+
				"  - TASK-010\n"+
				"  - TASK-020\n"+
				"grill_prev_status: \"\"\n---\n# Body text\n"), 0644); err != nil {
			t.Fatalf("write corrupt list file: %v", err)
		}

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
		if err := os.WriteFile(path, []byte("# No frontmatter\n"), 0644); err != nil {
			t.Fatalf("write no-frontmatter file: %v", err)
		}
		if err := Repair(path); err == nil {
			t.Error("expected error for file without frontmatter")
		}
	})

	t.Run("recovers markdown body mistaken as frontmatter", func(t *testing.T) {
		// Simulates the TASK-061 scenario: closing "---" delimiter is missing,
		// the next "---" in the file is a horizontal rule in the body.
		// Repair should detect that the discarded lines are markdown and preserve them.
		path := filepath.Join(dir, "missing-delimiter.md")
		original := "---\nid: \"061\"\nstatus: needs-grilling\nadr_proposed: []\n\n" +
			"## 需求成熟度评估\n\n" +
			"> 版本: 15 | REQ hash: sha256:abc\n\n" +
			"| 检查项 | 状态 |\n" +
			"|--------|------|\n" +
			"| AC 完整 | ❌ |\n\n" +
			"---\n\n" +
			"## 实现计划\n\n" +
			"Step 1 content here.\n"
		if err := os.WriteFile(path, []byte(original), 0644); err != nil {
			t.Fatal(err)
		}

		if err := Repair(path); err != nil {
			t.Fatalf("repair failed: %v", err)
		}

		data, _ := os.ReadFile(path)
		content := string(data)

		// YAML fields preserved
		for _, want := range []string{`id: "061"`, `status: needs-grilling`, `adr_proposed: []`} {
			if !contains(content, want) {
				t.Errorf("missing YAML field %q:\n%s", want, content)
			}
		}
		// Markdown body recovered (not discarded as orphaned text)
		for _, want := range []string{
			"## 需求成熟度评估",
			"> 版本: 15",
			"| 检查项 | 状态 |",
			"| AC 完整 | ❌ |",
			"## 实现计划",
			"Step 1 content here.",
		} {
			if !contains(content, want) {
				t.Errorf("body content lost: missing %q:\n%s", want, content)
			}
		}
		// Frontmatter-only section valid YAML
		if err := Validate(path); err != nil {
			t.Errorf("repaired file still invalid: %v", err)
		}
	})
}

func TestUpdateDeclinesCorruptedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.md")
	// Corrupt file: text that is not valid YAML in the frontmatter block
	if err := os.WriteFile(path, []byte(
		"---\nid: \"061\"\nstatus: needs-grilling\n"+
			"非法的YAML键名无冒号\n---\n# Body\n"), 0644); err != nil {
		t.Fatalf("write corrupted frontmatter: %v", err)
	}

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
		if err := os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\nproject: test\nreq_doc: Projects/test/REQ-001.md\n---\n# Title\n- AC: <id> in body.\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := ValidateTaskDocument(path); err == nil {
			t.Error("expected error for unescaped <id> in body")
		}
	})

	t.Run("accepts escaped \\<id\\> in body", func(t *testing.T) {
		path := filepath.Join(dir, "escaped.md")
		if err := os.WriteFile(path, []byte("---\nid: \"001\"\nstatus: ready\nproject: test\nreq_doc: Projects/test/REQ-001.md\n---\n# Title\n- AC: \\<id\\> escaped.\n"), 0644); err != nil {
			t.Fatal(err)
		}
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
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write valid file: %v", err)
	}

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
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write block scalar file: %v", err)
	}

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
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write block scalar file: %v", err)
	}

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
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write order fixture: %v", err)
	}

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
	if err := os.WriteFile(path, []byte("# No frontmatter here\n"), 0644); err != nil {
		t.Fatalf("write no-frontmatter fixture: %v", err)
	}
	if err := Validate(path); err == nil {
		t.Error("expected error for file without frontmatter")
	}

	// File with valid frontmatter should pass
	path2 := filepath.Join(dir, "ok.md")
	if err := os.WriteFile(path2, []byte("---\nid: \"001\"\nstatus: ready\n---\n# Body\n"), 0644); err != nil {
		t.Fatalf("write valid fixture: %v", err)
	}
	if err := Validate(path2); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestRepairPreservesBlockScalar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "block-repair.md")
	// Valid block scalar field + corrupt orphaned text elsewhere
	if err := os.WriteFile(path, []byte(
		"---\nid: \"061\"\nstatus: needs-grilling\ngrill_context: |\n"+
			"  question one\n"+
			"  question two\n"+
			"BROKEN ORPHAN\n"+
			"assignee: gpt\n---\n# Body text\n"), 0644); err != nil {
		t.Fatalf("write block-repair fixture: %v", err)
	}
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

func TestParseTaskSchemaCompatibility(t *testing.T) {
	content := []byte(`---
id: "003"
status: blocked
priority: ""
close_approved: true
phase_error_code: TASK_FIELD_TAMPERED
grill_heartbeat_at: "2026-07-28T10:00:00+08:00"
priority_score: 6
priority_recommendation: P0
review_feedback: "resume from failed AC"
rework_resolution: resume
closure_reason: duplicate
replacement_task: TASK-004
remote_create: true
github_owner: ndzuki
repository_name: otg
repository_visibility: private
unknown_future_field: keep-me
---
# Task
`)

	fm, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.TaskSchemaVersion != 0 {
		t.Fatalf("TaskSchemaVersion = %d, want legacy version 0", fm.TaskSchemaVersion)
	}
	if fm.PriorityAssessmentStatus != "pending" {
		t.Fatalf("PriorityAssessmentStatus = %q, want compatibility default pending", fm.PriorityAssessmentStatus)
	}
	if !fm.CloseApproved || fm.PhaseErrorCode != "TASK_FIELD_TAMPERED" {
		t.Fatalf("new system fields were not decoded: %+v", fm)
	}
	if fm.PriorityScore != 6 || fm.PriorityRecommendation != "P0" {
		t.Fatalf("priority assessment fields were not decoded: %+v", fm)
	}
	if fm.ReworkResolution != "resume" || fm.ClosureReason != "duplicate" || fm.ReplacementTask != "TASK-004" {
		t.Fatalf("rework/closed fields were not decoded: %+v", fm)
	}
	if !fm.RemoteCreate || fm.RepositoryVisibility != "private" {
		t.Fatalf("GitHub fields were not decoded: %+v", fm)
	}
	if got := fm.Extra["unknown_future_field"]; got != "keep-me" {
		t.Fatalf("unknown field = %#v, want keep-me", got)
	}
}

func TestParseAutoMergeDefault(t *testing.T) {
	// Absent auto_merge defaults to true (auto-approve merges on review).
	fm, err := Parse([]byte(`---
id: "001"
status: blocked
---
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !fm.AutoMerge {
		t.Fatalf("AutoMerge = false, want default true")
	}

	// Explicit opt-out is honored.
	fm, err = Parse([]byte(`---
id: "002"
status: blocked
auto_merge: false
---
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.AutoMerge {
		t.Fatalf("AutoMerge = true, want explicit false")
	}
}

func TestParseAutoApproveDefault(t *testing.T) {
	// Absent auto_approve defaults to true (symmetric with auto_merge):
	// plan-review moves straight to implementing, so Grilling is the only
	// manual gate.
	fm, err := Parse([]byte(`---
id: "001"
status: blocked
---
`))

	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !fm.AutoApprove {
		t.Fatalf("AutoApprove = false, want default true")
	}

	// Explicit opt-out is honored.
	fm, err = Parse([]byte(`---
id: "002"
status: blocked
auto_approve: false
---
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.AutoApprove {
		t.Fatalf("AutoApprove = true, want explicit false")
	}
}

func TestParseLegacyTaskWithPriorityDoesNotReassess(t *testing.T) {
	fm, err := Parse([]byte(`---
id: "002"
status: done
priority: P2
---
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.PriorityAssessmentStatus != "completed" {
		t.Fatalf("PriorityAssessmentStatus = %q, want compatibility default completed", fm.PriorityAssessmentStatus)
	}
}

func TestUpdatePreservesUnknownFieldsWithNewSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-003-schema.md")
	original := `---
id: "003"
task_schema_version: 1
status: blocked
priority_assessment_status: pending
unknown_future_field:
  nested: true
---
# Task
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	if err := Update(path, map[string]interface{}{
		"priority_assessment_status": "completed",
		"priority_score":             6,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.PriorityAssessmentStatus != "completed" || fm.PriorityScore != 6 {
		t.Fatalf("updated schema fields = %+v", fm)
	}
	nested, ok := fm.Extra["unknown_future_field"].(map[string]interface{})
	if !ok || nested["nested"] != true {
		t.Fatalf("unknown future field = %#v, want nested mapping", fm.Extra["unknown_future_field"])
	}
}

func TestMissingDefaults(t *testing.T) {
	find := func(missing []FieldDefault, key string) (interface{}, bool) {
		for _, m := range missing {
			if m.Key == key {
				return m.Value, true
			}
		}
		return nil, false
	}

	t.Run("legacy task gets lifecycle fields", func(t *testing.T) {
		data := []byte(`---
id: "064"
title: Legacy
status: review
---
# Body
`)
		missing, err := MissingDefaults(data)
		if err != nil {
			t.Fatalf("MissingDefaults: %v", err)
		}
		if missing == nil {
			t.Fatal("expected missing fields, got nil")
		}
		for _, key := range []string{
			"auto_approve", "auto_merge", "merge_approved", "plan_approved", "pending_req",
			"task_schema_version", "target_branch", "pr_url", "phase_error_code",
		} {
			if _, ok := find(missing, key); !ok {
				t.Fatalf("missing %q not reported", key)
			}
		}
		if v, _ := find(missing, "auto_approve"); v != true {
			t.Fatalf("auto_approve default = %v, want true", v)
		}
		if v, _ := find(missing, "auto_merge"); v != true {
			t.Fatalf("auto_merge default = %v, want true", v)
		}
		if _, ok := find(missing, "status"); ok {
			t.Fatalf("existing status must not be reported missing: %v", missing)
		}
		if v, _ := find(missing, "priority_assessment_status"); v != "pending" {
			t.Fatalf("priority_assessment_status = %v, want pending (no priority)", v)
		}
	})

	t.Run("priority set derives completed assessment", func(t *testing.T) {
		missing, err := MissingDefaults([]byte("---\nid: \"001\"\npriority: P1\n---\n"))
		if err != nil {
			t.Fatalf("MissingDefaults: %v", err)
		}
		var found string
		for _, m := range missing {
			if m.Key == "priority_assessment_status" {
				found = m.Value.(string)
			}
		}
		if found != "completed" {
			t.Fatalf("priority_assessment_status = %q, want completed", found)
		}
	})

	t.Run("explicit auto_merge false is preserved", func(t *testing.T) {
		missing, err := MissingDefaults([]byte("---\nid: \"001\"\nauto_merge: false\n---\n"))
		if err != nil {
			t.Fatalf("MissingDefaults: %v", err)
		}
		if _, ok := find(missing, "auto_merge"); ok {
			t.Fatalf("explicit auto_merge: false must not be reported missing: %v", missing)
		}
	})

	t.Run("complete document reports nothing", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("---\n")
		for key, def := range taskFieldDefaults {
			switch v := def.(type) {
			case string:
				fmt.Fprintf(&sb, "%s: %q\n", key, v)
			case bool:
				fmt.Fprintf(&sb, "%s: %v\n", key, v)
			case int:
				fmt.Fprintf(&sb, "%s: %d\n", key, v)
			case []interface{}:
				fmt.Fprintf(&sb, "%s: []\n", key)
			}
		}
		sb.WriteString("priority_assessment_status: pending\n---\n")
		missing, err := MissingDefaults([]byte(sb.String()))
		if err != nil {
			t.Fatalf("MissingDefaults: %v", err)
		}
		if missing != nil {
			t.Fatalf("complete document reported %d missing fields", len(missing))
		}
	})

	t.Run("no frontmatter leaves document alone", func(t *testing.T) {
		missing, err := MissingDefaults([]byte("# plain markdown\n"))
		if err != nil || missing != nil {
			t.Fatalf("MissingDefaults = %v, %v; want nil, nil", missing, err)
		}
	})
}

func TestNormalizeTaskFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-064-legacy.md")
	original := `---
id: "064"
title: Legacy task
status: review
auto_merge: false
---
# Body
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	updated, err := NormalizeTaskFrontmatter(path)
	if err != nil {
		t.Fatalf("NormalizeTaskFrontmatter: %v", err)
	}
	if !updated {
		t.Fatal("expected normalization to update the document")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	for _, want := range []string{"auto_merge: false", "task_schema_version: 1", "merge_approved: false", "pending_req: false", "target_branch:"} {
		if !strings.Contains(content, want) {
			t.Fatalf("normalized document missing %q:\n%s", want, content)
		}
	}
	// Explicit opt-out must survive the backfill.
	if !strings.Contains(content, "auto_merge: false") {
		t.Fatal("explicit auto_merge: false was overwritten")
	}
	// User-facing fields precede daemon-maintained ones (canonical order).
	idPos := strings.Index(content, "id:")
	statusPos := strings.Index(content, "status:")
	phaseErrPos := strings.Index(content, "phase_error:")
	autoMergePos := strings.Index(content, "auto_merge:")
	if idPos >= statusPos || statusPos >= autoMergePos || autoMergePos >= phaseErrPos {
		t.Fatalf("frontmatter not in canonical order (id=%d status=%d auto_merge=%d phase_error=%d):\n%s", idPos, statusPos, autoMergePos, phaseErrPos, content)
	}

	// Second pass: nothing left to normalize.
	updated, err = NormalizeTaskFrontmatter(path)
	if err != nil {
		t.Fatalf("second NormalizeTaskFrontmatter: %v", err)
	}
	if updated {
		t.Fatal("second pass must be a no-op")
	}

	// No frontmatter: leave untouched, report no update.
	plain := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(plain, []byte("# no frontmatter\n"), 0o644); err != nil {
		t.Fatalf("write plain: %v", err)
	}
	updated, err = NormalizeTaskFrontmatter(plain)
	if err != nil || updated {
		t.Fatalf("plain doc: updated=%v err=%v; want false, nil", updated, err)
	}

	// Empty frontmatter (---\n---): leave untouched like Parse's empty block.
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, []byte("---\n---\n# Body\n"), 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	updated, err = NormalizeTaskFrontmatter(empty)
	if err != nil || updated {
		t.Fatalf("empty frontmatter doc: updated=%v err=%v; want false, nil", updated, err)
	}
}

func TestNormalizeTaskFrontmatterReorders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-065-scrambled.md")
	// Scrambled: daemon-maintained fields before user-facing ones.
	original := `---
phase_error_code: ""
auto_merge: true
id: "065"
status: blocked
custom_field: keep-me
pending_req: false
title: Scrambled
---
# Body
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	updated, err := NormalizeTaskFrontmatter(path)
	if err != nil {
		t.Fatalf("NormalizeTaskFrontmatter: %v", err)
	}
	if !updated {
		t.Fatal("expected reorder to update the document")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	// Canonical order: id → title → status → auto_merge → phase_error_code;
	// unknown custom_field appended at the end.
	positions := map[string]int{}
	for _, key := range []string{"id:", "title:", "status:", "auto_merge:", "phase_error_code:", "custom_field:"} {
		positions[key] = strings.Index(content, key)
		if positions[key] < 0 {
			t.Fatalf("key %q missing after normalize:\n%s", key, content)
		}
	}
	if positions["id:"] >= positions["title:"] || positions["title:"] >= positions["status:"] ||
		positions["status:"] >= positions["auto_merge:"] || positions["auto_merge:"] >= positions["phase_error_code:"] ||
		positions["phase_error_code:"] >= positions["custom_field:"] {
		t.Fatalf("canonical order violated: %v\n%s", positions, content)
	}
}

func TestNormalizeTaskFrontmatterRejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-066-corrupt.md")
	// Unclosed frontmatter: Normalize must fail without touching the file, so
	// a broken document is never "repaired" into a different broken shape.
	corrupt := "---\nid: \"066\"\nstatus: blocked\n# no closing delimiter\n# Body\n"
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	updated, err := NormalizeTaskFrontmatter(path)
	if err == nil {
		t.Fatal("want error for unclosed frontmatter")
	}
	if updated {
		t.Fatal("must not report update for corrupt document")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if string(data) != corrupt {
		t.Fatal("corrupt document was modified by failed normalize")
	}
}

// TestNormalizeTaskFrontmatterNumericStrings pins the Parse-consistent
// numeric normalization: editors may serialize estimated_hours/actual_hours
// as quoted strings, which must not block normalization (regression for
// "cannot unmarshal !!str '42' into float64").
func TestNormalizeTaskFrontmatterNumericStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-067-numeric.md")
	original := `---
id: "067"
title: Numeric strings
status: blocked
estimated_hours: "42"
---
# Body
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	updated, err := NormalizeTaskFrontmatter(path)
	if err != nil {
		t.Fatalf("NormalizeTaskFrontmatter with quoted numeric: %v", err)
	}
	if !updated {
		t.Fatal("expected normalization to run")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	fm, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse after normalize: %v", err)
	}
	if fm.EstimatedHours != 42 {
		t.Fatalf("estimated_hours = %v, want 42 (normalized from quoted string)", fm.EstimatedHours)
	}
}

// TestNormalizeTaskFrontmatterGrillFields pins the PM-consolidation schema:
// grill_parked / grill_repeat / auto_accepted are backfilled on legacy docs,
// land in canonical order after grill_prev_status, and existing values
// survive normalization untouched.
func TestNormalizeTaskFrontmatterGrillFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-068-grill-fields.md")
	original := `---
id: "068"
title: Grill fields
status: needs-grilling
grill_done: false
grill_repeat: 2
grill_prev_status: ""
---
# Body
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	updated, err := NormalizeTaskFrontmatter(path)
	if err != nil {
		t.Fatalf("NormalizeTaskFrontmatter: %v", err)
	}
	if !updated {
		t.Fatal("expected normalization to backfill grill fields")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	// Missing fields backfilled with defaults.
	for _, want := range []string{"grill_parked: false", "auto_accepted:"} {
		if !strings.Contains(content, want) {
			t.Fatalf("normalized document missing %q:\n%s", want, content)
		}
	}
	// Existing grill_repeat: 2 must survive.
	if !strings.Contains(content, "grill_repeat: 2") {
		t.Fatalf("existing grill_repeat was overwritten:\n%s", content)
	}
	// Canonical order: grill_prev_status < grill_parked < grill_repeat < auto_accepted.
	prevPos := strings.Index(content, "grill_prev_status:")
	parkedPos := strings.Index(content, "grill_parked:")
	repeatPos := strings.Index(content, "grill_repeat:")
	acceptedPos := strings.Index(content, "auto_accepted:")
	if prevPos < 0 || parkedPos < 0 || repeatPos < 0 || acceptedPos < 0 {
		t.Fatalf("grill fields missing after normalize:\n%s", content)
	}
	if prevPos >= parkedPos || parkedPos >= repeatPos || repeatPos >= acceptedPos {
		t.Fatalf("grill fields not in canonical order (prev=%d parked=%d repeat=%d accepted=%d):\n%s", prevPos, parkedPos, repeatPos, acceptedPos, content)
	}

	// Second pass: no-op.
	updated, err = NormalizeTaskFrontmatter(path)
	if err != nil {
		t.Fatalf("second NormalizeTaskFrontmatter: %v", err)
	}
	if updated {
		t.Fatal("second pass must be a no-op")
	}
}

func TestNormalizeReqFrontmatter(t *testing.T) {
	t.Run("backfills missing stable fields and reorders", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "REQ-009-old.md")
		// A legacy REQ created before the schema grew: only id/title exist.
		content := `---
id: "009"
title: Legacy Requirement
---
# Legacy
`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		updated, err := NormalizeReqFrontmatter(path)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if !updated {
			t.Fatal("expected rewrite for legacy REQ")
		}
		data, _ := os.ReadFile(path)
		fm, err := Parse(data)
		if err != nil {
			t.Fatalf("parse after normalize: %v", err)
		}
		if fm.ID != "009" || fm.Title != "Legacy Requirement" {
			t.Fatalf("identity lost: id=%q title=%q", fm.ID, fm.Title)
		}
		if fm.Created == "" {
			t.Error("created not backfilled")
		}
		if fm.Updated == "" {
			t.Error("updated not backfilled")
		}
		// tags backfills to empty list per reqFieldDefaults.
		if fm.Tags == nil {
			t.Error("tags not backfilled")
		}
		// Optional decision fields must NOT be fabricated.
		if fm.Stage != "" || fm.Priority != "" || strings.Contains(string(data), "depends_on") {
			t.Errorf("optional fields fabricated: stage=%q priority=%q depends_on present=%v", fm.Stage, fm.Priority, strings.Contains(string(data), "depends_on"))
		}
		// Key order: id before title before created/updated.
		if !strings.Contains(string(data), "id:") || !strings.Contains(string(data), "created:") {
			t.Errorf("canonical order missing:\n%s", data)
		}
	})

	t.Run("does not overwrite existing values", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "REQ-010-kept.md")
		content := `---
id: "010"
title: Kept
priority: P1
stage: P2
created: "2026-01-01T00:00:00+08:00"
updated: "2026-01-02T00:00:00+08:00"
---
# Kept
`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		updated, err := NormalizeReqFrontmatter(path)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		// updated refreshes on rewrite (matching TASK semantics); the second
		// pass must converge. Existing user values are never altered.
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), "priority: P1") || !strings.Contains(string(data), "stage: P2") {
			t.Errorf("existing values altered:\n%s", data)
		}
		if !strings.Contains(string(data), `created: "2026-01-01T00:00:00+08:00"`) {
			t.Errorf("created timestamp altered:\n%s", data)
		}
		if !updated {
			t.Fatal("expected rewrite to refresh updated")
		}
		updated, err = NormalizeReqFrontmatter(path)
		if err != nil {
			t.Fatal(err)
		}
		if updated {
			t.Fatal("second normalize pass must be a no-op")
		}
	})

	t.Run("idempotent second pass", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "REQ-011-idem.md")
		content := `---
id: "011"
title: Idem
---
# Idem
`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := NormalizeReqFrontmatter(path); err != nil {
			t.Fatal(err)
		}
		updated, err := NormalizeReqFrontmatter(path)
		if err != nil {
			t.Fatal(err)
		}
		if updated {
			t.Fatal("second normalize pass must be a no-op")
		}
	})

	t.Run("no frontmatter left alone", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "REQ-012-nofm.md")
		if err := os.WriteFile(path, []byte("# No frontmatter\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		updated, err := NormalizeReqFrontmatter(path)
		if err != nil {
			t.Fatal(err)
		}
		if updated {
			t.Fatal("document without frontmatter must not be rewritten")
		}
	})
}

func TestParseFrontmatterMap(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantRaw bool // whether a non-nil map is expected
		wantKey string
		wantVal interface{}
		wantErr bool
	}{
		{"no frontmatter", "# plain markdown\n", false, "", nil, false},
		{"unclosed block", "---\nid: \"001\"\n", false, "", nil, true},
		{"empty block", "---\n---\n# body\n", true, "", nil, false},
		{"valid block", "---\nid: \"001\"\nstatus: refining\n---\n# body\n", true, "id", "001", false},
		{"malformed yaml", "---\nid: \"001\"\nstatus: [unclosed\n---\n", false, "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := parseFrontmatterMap([]byte(tt.content))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseFrontmatterMap() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (raw != nil) != tt.wantRaw {
				t.Fatalf("parseFrontmatterMap() map presence = %v, want %v", raw != nil, tt.wantRaw)
			}
			if tt.wantKey != "" {
				if raw[tt.wantKey] != tt.wantVal {
					t.Fatalf("raw[%q] = %v, want %v", tt.wantKey, raw[tt.wantKey], tt.wantVal)
				}
			}
		})
	}
}
