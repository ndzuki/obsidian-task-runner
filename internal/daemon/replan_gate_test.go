package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestReplanGateRequired(t *testing.T) {
	tests := []struct {
		name      string
		task      task.ReadyTask
		threshold int
		want      bool
	}{
		{name: "disabled", task: task.ReadyTask{PlanVersion: 99}, threshold: 0, want: false},
		{name: "below threshold", task: task.ReadyTask{PlanVersion: 4}, threshold: 5, want: false},
		{name: "threshold reached", task: task.ReadyTask{PlanVersion: 5}, threshold: 5, want: true},
		{name: "already revised", task: task.ReadyTask{PlanVersion: 7, DesignReplanVersion: 7}, threshold: 5, want: false},
		{name: "newer plan after revision", task: task.ReadyTask{PlanVersion: 8, DesignReplanVersion: 7}, threshold: 5, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replanGateRequired(tt.task, tt.threshold); got != tt.want {
				t.Fatalf("replanGateRequired()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunReplanGateSuccessRecordsVersion(t *testing.T) {
	runner, _, projectDir := newDesignTestRunner(t)
	fake := &fakeDesignExecutor{
		result: &ExecutionResult{Phase: "design", Code: OutcomeSuccess, ResumeToken: "design-1"},
		write:  func(TaskSnapshot) error { return writeValidDesignLibrary(t, projectDir) },
	}
	runner.designExecutor = fake
	runner.cfg.ReplanGateThreshold = 5
	taskPath := filepath.Join(t.TempDir(), "TASK-001.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\ntitle: Replan test\nproject_id: \"001\"\nproject: demo\nreq_doc: REQ-001.md\nassignee: default\nstatus: planning\nplan_version: 5\n---\n# task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := task.ReadyTask{ID: "001", Project: "demo", FilePath: taskPath, ReqDoc: "REQ-001.md", Status: "planning", PlanVersion: 5}

	handled, err := runner.runReplanGate(context.Background(), candidate, projectDir)
	if !handled || err != nil {
		t.Fatalf("runReplanGate handled=%v err=%v, want true/nil", handled, err)
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.DesignReplanVersion != 5 {
		t.Fatalf("design_replan_version=%d, want 5", fm.DesignReplanVersion)
	}
	if got := fake.spec.Phase; got != "design" {
		t.Fatalf("design executor phase=%q, want design", got)
	}
	// The next scan sees the durable marker and does not run another design.
	candidate.DesignReplanVersion = 5
	handled, err = runner.runReplanGate(context.Background(), candidate, projectDir)
	if handled || err != nil {
		t.Fatalf("second run handled=%v err=%v, want false/nil", handled, err)
	}
}

func TestRunReplanGateFailureDoesNotRecordVersion(t *testing.T) {
	runner, _, projectDir := newDesignTestRunner(t)
	runner.cfg.ReplanGateThreshold = 5
	runner.designExecutor = &fakeDesignExecutor{
		result: &ExecutionResult{Phase: "design", Code: OutcomeFailed, Error: "design provider unavailable"},
	}
	taskPath := filepath.Join(t.TempDir(), "TASK-001.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\ntitle: Replan test\nproject_id: \"001\"\nproject: demo\nreq_doc: REQ-001.md\nassignee: default\nstatus: planning\nplan_version: 5\n---\n# task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := task.ReadyTask{ID: "001", Project: "demo", FilePath: taskPath, Status: "planning", PlanVersion: 5}

	handled, err := runner.runReplanGate(context.Background(), candidate, projectDir)
	if !handled || err == nil {
		t.Fatalf("runReplanGate handled=%v err=%v, want true/non-nil", handled, err)
	}
	if !errors.Is(err, os.ErrNotExist) && err.Error() == "" {
		t.Fatal("expected a descriptive gate failure")
	}
	fm, parseErr := yamlfrontmatter.ParseTaskDocument(taskPath)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if fm.DesignReplanVersion != 0 {
		t.Fatalf("failed gate recorded design_replan_version=%d, want 0", fm.DesignReplanVersion)
	}
}

func TestReplanGateDefaultThreshold(t *testing.T) {
	cfg := config.Defaults()
	if cfg.ReplanGateThreshold != 5 {
		t.Fatalf("default replan gate threshold=%d, want 5", cfg.ReplanGateThreshold)
	}
}
