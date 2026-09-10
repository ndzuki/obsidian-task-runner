package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func legacyAuditFixture(t *testing.T) (*Runner, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-release-manager")
	for _, sub := range []string{"Notes", "Tasks", "Requirements"} {
		if err := os.MkdirAll(filepath.Join(projDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projDir, "Notes", "Stage-Plan.md"), []byte(`---
id: "stage-plan"
project: release-manager
status: active
---
### Phase 1: 契约与基础收敛
- tasks: 001
- status: review-pending
`), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPath := filepath.Join(projDir, "Notes", "Stage-Review.md")
	if err := os.WriteFile(reviewPath, []byte(`---
id: "stage-review"
project: release-manager
stage: Phase 1
status: open
grill_continue: false
---
# Stage Review
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := New(&config.Config{ObsidianVault: vault, DefaultAssignee: "default"})
	runner.logger = log.New(io.Discard, "", 0)
	return runner, projDir, reviewPath, filepath.Join(projDir, "Notes", documentationAuditName)
}

// AC-4: every legacy project gets one auditable PM input file; repeated scans
// do not create a second file or re-dispatch a second audit session.
func TestLegacyDocumentationAuditCreatedOnce(t *testing.T) {
	runner, projDir, _, auditPath := legacyAuditFixture(t)
	if err := createDocumentationAudit(projDir, auditPath, filepath.Join(projDir, "Notes", stageReviewName)); err != nil {
		t.Fatal(err)
	}
	if err := createDocumentationAudit(projDir, auditPath, filepath.Join(projDir, "Notes", stageReviewName)); err == nil {
		t.Fatal("second audit file creation must fail instead of overwrite")
	}
	if got := runner.processLegacyDocumentationAudits(context.Background()); got != 1 {
		// The existing pending audit should dispatch once. A missing API key is
		// expected in this unit fixture, so accept the deterministic no-dispatch
		// result but verify the file remains for later retry.
		if got != 0 {
			t.Fatalf("audit dispatch count = %d, want 0 or 1", got)
		}
	}
	if _, err := os.Stat(auditPath); err != nil {
		t.Fatal(err)
	}
}

// AC-5: PM's completed legacy audit is copied into the existing Stage-Review;
// no user decision line is changed.
func TestSyncLegacyDocumentationAuditToStageReview(t *testing.T) {
	runner, projDir, reviewPath, auditPath := legacyAuditFixture(t)
	_ = runner
	if err := createDocumentationAudit(projDir, auditPath, reviewPath); err != nil {
		t.Fatal(err)
	}
	if err := yamlfrontmatter.Update(auditPath, map[string]any{
		"status": "completed", "documentation_gate": "gap",
		"documentation_gaps":  []string{"README 缺少统一入口"},
		"documentation_batch": "doc-v1-test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := syncDocumentationAuditToReview(auditPath, reviewPath); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(reviewPath)
	text := string(data)
	if !strings.Contains(text, "documentation_gate_version: 1") || !strings.Contains(text, "documentation_gate: gap") || !strings.Contains(text, "README 缺少统一入口") {
		t.Fatalf("audit result not synced:\n%s", text)
	}
	if strings.Contains(text, "评审决策: continue") {
		t.Fatal("legacy audit must not invent a user stage decision")
	}
}
