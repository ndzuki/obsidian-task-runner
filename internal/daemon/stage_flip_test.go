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
)

const flipPlanTemplate = `---
id: "stage-plan"
project: test
status: active
---

### Phase 1: 核心链路
- tasks: 001
- status: in-progress

### Phase 2: Web 控制台
- tasks: 002
- status: planned
`

func flipRunner(t *testing.T, vault string) *Runner {
	t.Helper()
	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	return runner
}

// TestApplyStageDecisionContinue guards the continue transition: current
// phase delivered, next phase in-progress.
func TestApplyStageDecisionContinue(t *testing.T) {
	runner := flipRunner(t, t.TempDir())
	out, flipped, summary := runner.applyStageDecision(flipPlanTemplate, "continue", "", "test")
	if !flipped {
		t.Fatal("continue must flip the plan")
	}
	if !strings.Contains(out, "### Phase 1: 核心链路\n- tasks: 001\n- status: delivered") {
		t.Fatalf("phase 1 not delivered:\n%s", out)
	}
	if !strings.Contains(out, "### Phase 2: Web 控制台\n- tasks: 002\n- status: in-progress") {
		t.Fatalf("phase 2 not in-progress:\n%s", out)
	}
	if !strings.Contains(summary, "Phase 2") {
		t.Fatalf("summary = %q, want next-phase mention", summary)
	}
}

// TestApplyStageDecisionContinueCompletesProject guards the last-phase
// continue: no next phase → Stage-Plan status completed.
func TestApplyStageDecisionContinueCompletesProject(t *testing.T) {
	runner := flipRunner(t, t.TempDir())
	out, flipped, _ := runner.applyStageDecision(flipPlanTemplate, "continue", "", "test")
	_ = flipped
	// Remove phase 2: phase 1 is the last.
	onePhase := flipPlanTemplate[:strings.Index(flipPlanTemplate, "### Phase 2")]
	out, flipped, summary := runner.applyStageDecision(onePhase, "continue", "", "test")
	if !flipped {
		t.Fatal("last-phase continue must flip")
	}
	if !strings.Contains(out, "status: completed") {
		t.Fatalf("plan not completed:\n%s", out)
	}
	if !strings.Contains(summary, "completed") {
		t.Fatalf("summary = %q, want completed", summary)
	}
}

// TestApplyStageDecisionSupplement guards supplement: continue plus a
// "- 补充:" line appended to the next phase block.
func TestApplyStageDecisionSupplement(t *testing.T) {
	runner := flipRunner(t, t.TempDir())
	out, flipped, _ := runner.applyStageDecision(flipPlanTemplate, "supplement:增加只读场景", "", "test")
	if !flipped {
		t.Fatal("supplement must flip")
	}
	if !strings.Contains(out, "- 补充: 增加只读场景") {
		t.Fatalf("supplement line missing:\n%s", out)
	}
	if !strings.Contains(out, "### Phase 2: Web 控制台\n- tasks: 002\n- status: in-progress\n- 补充: 增加只读场景") {
		t.Fatalf("supplement placement wrong:\n%s", out)
	}
}

// TestApplyStageDecisionEnd guards end: current delivered, later phases
// ended, later-phase tasks closed.
func TestApplyStageDecisionEnd(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTask := func(id, stage, status string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nstatus: " + status + "\nstage: \"" + stage + "\"\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ph1 := writeTask("001", "P1", "done")
	ph2 := writeTask("002", "P2", "ready")

	runner := flipRunner(t, vault)
	out, flipped, summary := runner.applyStageDecision(flipPlanTemplate, "end", projDir, "test")
	if !flipped {
		t.Fatal("end must flip")
	}
	if !strings.Contains(out, "### Phase 2: Web 控制台\n- tasks: 002\n- status: ended") {
		t.Fatalf("phase 2 not ended:\n%s", out)
	}
	if !strings.Contains(summary, "ended") {
		t.Fatalf("summary = %q, want ended", summary)
	}
	data, _ := os.ReadFile(ph1)
	if !strings.Contains(string(data), "status: done") {
		t.Fatalf("phase-1 task must stay done, got:\n%s", data)
	}
	data, _ = os.ReadFile(ph2)
	if !strings.Contains(string(data), "status: closed") || !strings.Contains(string(data), "closure_reason: cancelled") {
		t.Fatalf("phase-2 task not closed:\n%s", data)
	}
}

func TestApplyStageDecisionEndRejectsActiveLaterTask(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(tasksDir, "TASK-002-active.md")
	if err := os.WriteFile(active, []byte("---\nid: \"002\"\nstatus: implementing\nstage: \"P2\"\nplan_version: 1\ntarget_branch: task/002-active\n---\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := flipRunner(t, vault)
	out, flipped, summary := runner.applyStageDecision(flipPlanTemplate, "end", projDir, "test")
	if flipped || out != flipPlanTemplate {
		t.Fatalf("end with active later task must not flip: flipped=%v summary=%q", flipped, summary)
	}
	if !strings.Contains(summary, "TASK-002 (implementing)") {
		t.Fatalf("summary = %q, want active task evidence", summary)
	}
	data, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "status: closed") {
		t.Fatalf("active task must not be closed:\n%s", data)
	}
}

// TestApplyStageDecisionUnknownNoOp guards invalid decisions: nothing flips.
func TestApplyStageDecisionUnknownNoOp(t *testing.T) {
	runner := flipRunner(t, t.TempDir())
	out, flipped, _ := runner.applyStageDecision(flipPlanTemplate, "maybe", "", "test")
	if flipped || out != flipPlanTemplate {
		t.Fatal("unknown decision must not flip")
	}
}

// TestFlipStageReviewDecisionIdempotent guards the flip retry loop at the
// Runner level: the same decision never re-applies (a failed PM session
// re-runs the flip without advancing the state machine further), and a
// revised decision after the state machine reached a terminal shape no-ops.
func TestFlipStageReviewDecisionIdempotent(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	notesDir := filepath.Join(projDir, "Notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "Stage-Plan.md"), []byte(flipPlanTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-002-ready.md"), []byte("---\nid: \"002\"\nstatus: ready\nstage: \"P2\"\n---\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReview := func(decision string) {
		t.Helper()
		content := "---\nid: \"stage-review\"\nproject: test\nstatus: open\ngrill_continue: true\n---\n# Stage Review\n\n## 评审决策\n- 评审决策: " + decision + "\n"
		if err := os.WriteFile(filepath.Join(notesDir, "Stage-Review.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	readPlan := func() string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(notesDir, "Stage-Plan.md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	runner := flipRunner(t, vault)
	ctx := context.Background()

	// "end" on the reviewed phase: Phase 1 delivered, later phases ended.
	writeReview("end")
	if !runner.flipStageReviewDecision(ctx) {
		t.Fatal("first flip must apply")
	}
	plan := readPlan()
	if !strings.Contains(plan, "### Phase 1: 核心链路\n- tasks: 001\n- status: delivered") {
		t.Fatalf("phase 1 not delivered:\n%s", plan)
	}
	if !strings.Contains(plan, "### Phase 2: Web 控制台\n- tasks: 002\n- status: ended") {
		t.Fatalf("phase 2 not ended:\n%s", plan)
	}

	// Re-run with the SAME decision: must no-op (nothing double-flips).
	if runner.flipStageReviewDecision(ctx) {
		t.Fatal("re-run of the same decision must no-op")
	}
	if got := readPlan(); got != plan {
		t.Fatal("re-run must not modify the plan")
	}

	// Revised decision after terminal shape: no in-progress phase → no-op.
	writeReview("continue")
	if runner.flipStageReviewDecision(ctx) {
		t.Fatal("revised decision with no in-progress phase must no-op")
	}
	if got := readPlan(); got != plan {
		t.Fatal("revised decision must not modify the plan")
	}
}
