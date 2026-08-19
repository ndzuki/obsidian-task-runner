package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// newPriorityFixture builds a Runner with a stub phaseExecutor returning the
// given stdout (free-text) and a task file whose priority is pending.
func newPriorityFixture(t *testing.T, stdout string, code ExecOutcome) (*Runner, task.PriorityTask) {
	t.Helper()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	taskPath := filepath.Join(vault, "Projects", "001-demo", "Tasks", "TASK-001-demo.md")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: \"001\"\ntitle: P\nproject_id: \"001\"\nproject: demo\nreq_doc: Projects/001-demo/Requirements/REQ-001-demo.md\nassignee: default\nstatus: ready\npriority_assessment_status: pending\npriority_assessment_attempts: 0\n---\n# Task\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{
		Executor:            "dsh",
		ObsidianVault:       vault,
		PhaseTimeoutMinutes: map[string]int{"priority": 5},
	})
	r.logger = log.New(io.Discard, "", 0)
	r.phaseExecutor = phaseExecutorStub{result: &ExecutionResult{Code: code, Stdout: stdout}}
	candidate := task.PriorityTask{ID: "001", Project: "demo", ReqDoc: "Projects/001-demo/Requirements/REQ-001-demo.md", FilePath: taskPath}
	return r, candidate
}

const validPriorityJSON = `{
  "priority": "P2",
  "impact": "medium",
  "urgency": "near_term",
  "workaround": "partial",
  "confidence": "high",
  "reason": "core path"
}`

func TestRunPriorityAssessmentDSHSuccess(t *testing.T) {
	// Free-text stdout with a fenced ```json block (the real DSH shape).
	stdout := "```json\n" + validPriorityJSON + "\n```\n"
	r, candidate := newPriorityFixture(t, stdout, OutcomeSuccess)

	if err := r.runPriorityAssessmentDSH(context.Background(), candidate, candidate.ReqDoc, 1); err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(candidate.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.PriorityAssessmentStatus != "completed" || fm.Priority != "P2" {
		t.Fatalf("assessment not completed: status=%q priority=%q", fm.PriorityAssessmentStatus, fm.Priority)
	}
	if fm.PriorityScore != 5 { // medium(2) + near_term(2) + partial(1)
		t.Fatalf("score=%d, want 5", fm.PriorityScore)
	}
}

func TestRunPriorityAssessmentDSHInterruptedResetsClaim(t *testing.T) {
	// OutcomeInterrupted → reset claim to pending, no failure recorded.
	r, candidate := newPriorityFixture(t, "", OutcomeInterrupted)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: daemon shutdown
	if err := r.runPriorityAssessmentDSH(ctx, candidate, candidate.ReqDoc, 1); err != nil {
		t.Fatalf("interrupted assessment must return nil: %v", err)
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(candidate.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.PriorityAssessmentStatus != "pending" {
		t.Fatalf("interrupted assessment status=%q, want pending", fm.PriorityAssessmentStatus)
	}
}

func TestRunPriorityAssessmentDSHMalformedJSON(t *testing.T) {
	// Free-text with no JSON object → recordPriorityFailure, not panic.
	r, candidate := newPriorityFixture(t, "模型拒绝输出 JSON", OutcomeSuccess)

	if err := r.runPriorityAssessmentDSH(context.Background(), candidate, candidate.ReqDoc, 1); err == nil {
		t.Fatal("malformed stdout must surface a failure")
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(candidate.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.PriorityAssessmentStatus != "pending" {
		t.Fatalf("failed assessment status=%q, want pending (retry)", fm.PriorityAssessmentStatus)
	}
}
