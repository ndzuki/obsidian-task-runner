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

// newDispatchFixture builds a valid task file and a Runner wired to a stub
// phaseExecutor returning the given result.
func newDispatchFixture(t *testing.T, result *ExecutionResult) (*Runner, task.ReadyTask, string) {
	t.Helper()
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := "---\nid: \"001\"\ntitle: Test\nproject: demo\nproject_id: \"001\"\nreq_doc: REQ-001.md\nassignee: default\nstatus: refining\ngeneration: 1\nplan_version: 0\n---\n# Task\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{
		Executor:            "dsh",
		ObsidianVault:       dir,
		PhaseTimeoutMinutes: map[string]int{"refining": 1},
	})
	r.logger = log.New(io.Discard, "", 0)
	r.phaseExecutor = phaseExecutorStub{result: result}
	candidate := task.ReadyTask{ID: "001", Title: "Test", Project: "demo", FilePath: taskPath, Status: "refining", Assignee: "default"}
	return r, candidate, taskPath
}

func TestRunDSHPhaseDispatchSuccess(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeSuccess})

	handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	if !handled {
		t.Fatal("success dispatch must be handled")
	}
	// Success tail clears phase error but must not flip status.
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "refining" {
		t.Fatalf("status=%q, want refining (success tail must not change status)", fm.Status)
	}
}

func TestRunDSHPhaseDispatchFailure(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeFailed, Error: "provider down"})

	handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	if !handled {
		t.Fatal("failure dispatch must be handled")
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	// refining + MODEL_FAILED → retry-then-block: handlePhaseFailure records
	// phase_error_code, not an immediate blocked status.
	if fm.PhaseErrorCode != string(ErrModelFailed) {
		t.Fatalf("phase_error_code=%q, want MODEL_FAILED", fm.PhaseErrorCode)
	}
}

func TestRunDSHPhaseDispatchInterrupted(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeInterrupted})

	handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	if !handled {
		t.Fatal("interrupted dispatch must be handled")
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.PhaseErrorCode != string(ErrPhaseInterrupted) {
		t.Fatalf("phase_error_code=%q, want PHASE_INTERRUPTED", fm.PhaseErrorCode)
	}
}

func TestRunDSHPhaseDispatchTimeoutMaps(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeTimedOut})

	r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.PhaseErrorCode != string(ErrPhaseTimeout) {
		t.Fatalf("phase_error_code=%q, want PHASE_TIMEOUT", fm.PhaseErrorCode)
	}
}

// Ensure the seam is wired: a dsh-configured runner routes through the stub
// (never spawns OMP), while an omp-configured runner ignores it.
func TestExecutorSelectionIsRespected(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\ntitle: T\nproject: demo\nproject_id: \"001\"\nreq_doc: R.md\nassignee: default\nstatus: refining\ngeneration: 1\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh", ObsidianVault: dir})
	if r.phaseExecutor == nil || r.phaseExecutor.Name() != "dsh" {
		t.Fatalf("dsh runner phaseExecutor=%v, want dsh adapter", r.phaseExecutor)
	}
	r2 := New(&config.Config{Executor: "dsh-embed", ObsidianVault: dir})
	if r2.phaseExecutor == nil || r2.phaseExecutor.Name() != "dsh-embed" {
		t.Fatalf("dsh-embed runner phaseExecutor=%v, want dsh-embed adapter", r2.phaseExecutor)
	}
	_ = context.Background()
}
