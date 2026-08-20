package daemon

import (
	"context"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

type phaseExecutorStub struct {
	result *ExecutionResult
	err    error
}

func (e phaseExecutorStub) Name() string { return "stub" }
func (e phaseExecutorStub) Resume(context.Context, string) (ExecutionHandle, error) {
	return nil, ErrResumeUnsupported
}
func (e phaseExecutorStub) Start(context.Context, PhaseSpec, TaskSnapshot) (ExecutionHandle, error) {
	if e.err != nil {
		return nil, e.err
	}
	return phaseHandleStub{result: e.result}, nil
}

type phaseHandleStub struct {
	result *ExecutionResult
}

func (h phaseHandleStub) Wait() (*ExecutionResult, error) { return h.result, nil }
func (phaseHandleStub) PID() int                          { return 0 }

func TestNewPhaseExecutorSelection(t *testing.T) {
	if got := newPhaseExecutor(&config.Config{Executor: "dsh", DSHCmd: "dsh", DSHProfile: "headless"}).Name(); got != "dsh" {
		t.Fatalf("dsh executor name=%q, want dsh", got)
	}
	if got := newPhaseExecutor(&config.Config{Executor: "dsh-embed", AgentServerAddr: "127.0.0.1:8799"}).Name(); got != "dsh-embed" {
		t.Fatalf("dsh-embed executor name=%q, want dsh-embed", got)
	}
}

func TestRunDSHPhaseMapsOutcome(t *testing.T) {
	tests := []struct {
		name     string
		result   *ExecutionResult
		startErr error
		wantCode ErrorCode
	}{
		{name: "success", result: &ExecutionResult{Code: OutcomeSuccess}, wantCode: ""},
		{name: "timeout", result: &ExecutionResult{Code: OutcomeTimedOut}, wantCode: ErrPhaseTimeout},
		{name: "failed", result: &ExecutionResult{Code: OutcomeFailed, Error: "boom"}, wantCode: ErrModelFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(&config.Config{Executor: "dsh"})
			r.phaseExecutor = phaseExecutorStub{result: tt.result}
			_, _, code, _ := r.runDSHPhase(context.Background(), PhaseSpec{Phase: "refining"}, TaskSnapshot{TaskID: "001"})
			if code != tt.wantCode {
				t.Fatalf("code=%q, want %q", code, tt.wantCode)
			}
		})
	}
}

func TestRunDSHPhaseStartError(t *testing.T) {
	r := New(&config.Config{Executor: "dsh"})
	r.phaseExecutor = phaseExecutorStub{err: context.DeadlineExceeded}
	_, out, code, reason := r.runDSHPhase(context.Background(), PhaseSpec{}, TaskSnapshot{})
	if out != OutcomeFailed || code != ErrModelFailed || reason == "" {
		t.Fatalf("start error mapping = (%q, %q, %q), want failed/MODEL_FAILED/non-empty", out, code, reason)
	}
}
