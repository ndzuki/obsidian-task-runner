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

func documentationClosureFixture(t *testing.T, gate, gaps, taskRef string) (*Runner, string, string) {
	t.Helper()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	for _, sub := range []string{"Notes", "Tasks", "Requirements"} {
		if err := os.MkdirAll(filepath.Join(projDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plan := `---
id: "stage-plan"
project: test
status: active
---
# Stage Plan

### Phase 1: 核心链路
- tasks: 001
- status: review-pending
- 评审: （待定）
`
	if err := os.WriteFile(filepath.Join(projDir, "Notes", "Stage-Plan.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "Tasks", "TASK-001-core.md"), []byte(`---
id: "001"
title: "Core"
project: "test"
status: done
merge_status: merged
stage: "P1"
assignee: "default"
---
# Core
`), 0o644); err != nil {
		t.Fatal(err)
	}
	review := `---
id: "stage-review"
project: test
stage: Phase 1
status: open
grill_continue: true
documentation_gate_version: 1
documentation_gate: ` + gate + `
documentation_gaps:
` + gaps + `documentation_task: "` + taskRef + `"
---
# Stage Review

## 评审决策
- 评审决策: continue
`
	reviewPath := filepath.Join(projDir, "Notes", "Stage-Review.md")
	if err := os.WriteFile(reviewPath, []byte(review), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := New(&config.Config{ObsidianVault: vault, DefaultAssignee: "default"})
	runner.logger = log.New(io.Discard, "", 0)
	return runner, projDir, reviewPath
}

// AC-1: PM declares documentation gaps → daemon creates one ordinary
// documentation REQ/TASK, assigns it to the next phase, and records the task
// back on Stage-Review. Re-scanning is idempotent.
func TestProcessDocumentationClosuresCreatesOrdinaryTask(t *testing.T) {
	runner, projDir, reviewPath := documentationClosureFixture(t, "gap", "  - README 缺少统一入口\n  - 配置参考未覆盖所有 CLI\n", "")

	if got := runner.processDocumentationClosures(); got != 1 {
		t.Fatalf("created = %d, want 1", got)
	}
	if got := runner.processDocumentationClosures(); got != 0 {
		t.Fatalf("second scan created = %d, want 0", got)
	}

	reqPath := filepath.Join(projDir, "Requirements", "REQ-002-documentation-closure-phase-1.md")
	taskPath := filepath.Join(projDir, "Tasks", "TASK-002-documentation-closure-phase-1.md")
	for _, path := range []string{reqPath, taskPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated file %s: %v", path, err)
		}
	}
	taskData, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	taskFM, err := yamlfrontmatter.Parse(taskData)
	if err != nil {
		t.Fatal(err)
	}
	if taskFM.Stage != "P2" || taskFM.Assignee != "default" {
		t.Fatalf("generated task stage/assignee = %q/%q, want P2/default", taskFM.Stage, taskFM.Assignee)
	}
	planData, _ := os.ReadFile(filepath.Join(projDir, "Notes", "Stage-Plan.md"))
	if !strings.Contains(string(planData), "### Phase 2: 项目文档交付收口") || !strings.Contains(string(planData), "- tasks: 002") {
		t.Fatalf("documentation phase missing:\n%s", planData)
	}
	reviewData, _ := os.ReadFile(reviewPath)
	if !strings.Contains(string(reviewData), `documentation_task: TASK-002`) {
		t.Fatalf("review task marker missing:\n%s", reviewData)
	}
}

// AC-2: a versioned gap is a completion gate. The user's continue/end
// decision cannot advance the Stage-Plan until the generated documentation
// task is done+merged; after landing, the same decision proceeds normally.
func TestDocumentationClosureJoinsExistingNextPhase(t *testing.T) {
	runner, projDir, _ := documentationClosureFixture(t, "gap", "  - README 缺少统一入口\n", "")
	planPath := filepath.Join(projDir, "Notes", "Stage-Plan.md")
	plan, _ := os.ReadFile(planPath)
	plan = append(plan, []byte(`
### Phase 2: 后续能力
- tasks: 003（参考；权威判定按 stage 字段）
- status: planned
- 评审: （待定）
`)...)
	if err := os.WriteFile(planPath, plan, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runner.processDocumentationClosures(); got != 1 {
		t.Fatalf("created = %d, want 1", got)
	}
	updated, _ := os.ReadFile(planPath)
	if !strings.Contains(string(updated), "- tasks: 003, 002（参考；权威判定按 stage 字段）") {
		t.Fatalf("documentation task not appended to next phase:\n%s", updated)
	}
	if strings.Contains(string(updated), "### Phase 3: 项目文档交付收口") {
		t.Fatal("existing next phase must be reused")
	}
}

func TestFlipStageReviewHeldUntilDocumentationTaskLands(t *testing.T) {
	runner, projDir, _ := documentationClosureFixture(t, "gap", "  - README 缺少统一入口\n", "")
	if got := runner.processDocumentationClosures(); got != 1 {
		t.Fatalf("created = %d, want 1", got)
	}
	planPath := filepath.Join(projDir, "Notes", "Stage-Plan.md")
	before, _ := os.ReadFile(planPath)
	if runner.flipStageReviewDecision(context.Background()) {
		t.Fatal("decision must be held while documentation task is unfinished")
	}
	after, _ := os.ReadFile(planPath)
	if string(after) != string(before) {
		t.Fatal("held decision must not modify Stage-Plan")
	}

	taskPath := filepath.Join(projDir, "Tasks", "TASK-002-documentation-closure-phase-1.md")
	if err := yamlfrontmatter.Update(taskPath, map[string]any{"status": "done", "merge_status": "merged"}); err != nil {
		t.Fatal(err)
	}
	if !runner.flipStageReviewDecision(context.Background()) {
		t.Fatal("decision must proceed after documentation task lands")
	}
	plan, _ := os.ReadFile(planPath)
	if !strings.Contains(string(plan), "### Phase 1: 核心链路\n- tasks: 001\n- status: delivered") {
		t.Fatalf("phase 1 not delivered:\n%s", plan)
	}
	if !strings.Contains(string(plan), "### Phase 2: 项目文档交付收口\n- 目标:") || !strings.Contains(string(plan), "- status: in-progress") {
		t.Fatalf("documentation phase not activated:\n%s", plan)
	}
}

// AC-3: pass/not_applicable retain the existing zero-task path. Legacy
// Stage-Reviews without a gate version remain backward compatible.
func TestDocumentationClosureCrashRecoveryRepairsStagePlanAndTask(t *testing.T) {
	runner, projDir, reviewPath := documentationClosureFixture(t, "gap", "  - README 缺少统一入口\n", "")
	if got := runner.processDocumentationClosures(); got != 1 {
		t.Fatalf("created = %d, want 1", got)
	}
	batch := documentationBatchID("Phase 1", []string{"README 缺少统一入口"})
	// Simulate a crash/partial write: review marker missing, task stage wrong,
	// and Stage-Plan membership removed. Recovery must repair, not duplicate.
	if err := yamlfrontmatter.Update(reviewPath, map[string]any{"documentation_task": "", "documentation_batch": batch}); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(projDir, "Tasks", "TASK-002-documentation-closure-phase-1.md")
	if err := yamlfrontmatter.Update(taskPath, map[string]any{"stage": "P9"}); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(projDir, "Notes", "Stage-Plan.md")
	plan, _ := os.ReadFile(planPath)
	idx := strings.Index(string(plan), "\n### Phase 2: 项目文档交付收口")
	if idx < 0 {
		t.Fatal("fixture documentation phase missing")
	}
	if err := os.WriteFile(planPath, plan[:idx], 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runner.processDocumentationClosures(); got != 0 {
		t.Fatalf("recovery created = %d, want 0", got)
	}
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)
	if fm.Stage != "P2" {
		t.Fatalf("recovered task stage = %q, want P2", fm.Stage)
	}
	repaired, _ := os.ReadFile(planPath)
	if !strings.Contains(string(repaired), "### Phase 2: 项目文档交付收口") {
		t.Fatalf("Stage-Plan not repaired:\n%s", repaired)
	}
}

func TestDocumentationGateMalformedReviewFailsClosed(t *testing.T) {
	runner, projDir, reviewPath := documentationClosureFixture(t, "pass", "", "")
	if err := os.WriteFile(reviewPath, []byte("---\ndocumentation_gate_version: [\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if documentationGateAllowsDecision(projDir, reviewPath) {
		t.Fatal("malformed versioned review must fail closed")
	}
	_ = runner
}

func TestStageReviewDistributionWithoutPendingTasks(t *testing.T) {
	runner, _, _ := documentationClosureFixture(t, "gap", "  - README 缺少统一入口\n", "")
	// The documentation gate holds before any DSH dispatch. This verifies the
	// project-level review path runs even though FindGrillingTasks is empty.
	if got := runner.processGrillingConsolidation(context.Background()); got != 0 {
		t.Fatalf("held distribution dispatched = %d, want 0", got)
	}
}

func TestDocumentationGateBlocksDistributionUntilTaskLands(t *testing.T) {
	runner, projDir, reviewPath := documentationClosureFixture(t, "gap", "  - README 缺少统一入口\n", "")
	if got := runner.processDocumentationClosures(); got != 1 {
		t.Fatalf("created = %d, want 1", got)
	}
	if documentationGateAllowsDecision(projDir, reviewPath) {
		t.Fatal("unfinished documentation task must block distribution")
	}
	taskPath := filepath.Join(projDir, "Tasks", "TASK-002-documentation-closure-phase-1.md")
	if err := yamlfrontmatter.Update(taskPath, map[string]any{"status": "done", "merge_status": "merged"}); err != nil {
		t.Fatal(err)
	}
	if !documentationGateAllowsDecision(projDir, reviewPath) {
		t.Fatal("landed documentation task must open distribution")
	}
}

func TestDocumentationGatePassAndLegacyDoNotCreateTasks(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate string
	}{
		{name: "pass", gate: "pass"},
		{name: "not-applicable", gate: "not_applicable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, projDir, _ := documentationClosureFixture(t, tc.gate, "", "")
			if got := runner.processDocumentationClosures(); got != 0 {
				t.Fatalf("created = %d, want 0", got)
			}
			entries, _ := os.ReadDir(filepath.Join(projDir, "Requirements"))
			if len(entries) != 0 {
				t.Fatalf("unexpected requirements: %v", entries)
			}
		})
	}
}
