package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

type phaseExecutorStub struct {
	result *ExecutionResult
	err    error
}

func (e phaseExecutorStub) Name() string { return "stub" }
func (e phaseExecutorStub) Resume(context.Context, string, time.Duration) (ExecutionHandle, error) {
	return nil, ErrResumeUnsupported
}
func (e phaseExecutorStub) Start(context.Context, PhaseSpec, TaskSnapshot) (ExecutionHandle, error) {
	if e.err != nil {
		return nil, e.err
	}
	return phaseHandleStub{result: e.result}, nil
}

// resumeExecutorStub 支持 Resume，记录调用情况，用于验证 durable resume 接通
// 以及 resume 超时/终态失败后的分派策略。
type resumeExecutorStub struct {
	result        *ExecutionResult // Resume 返回的结果
	startResult   *ExecutionResult // Start（fresh start）返回的结果
	resumeCalled  bool
	startCalled   bool
	resumeTimeout time.Duration
}

func (e *resumeExecutorStub) Name() string { return "resume-stub" }
func (e *resumeExecutorStub) Resume(_ context.Context, _ string, timeout time.Duration) (ExecutionHandle, error) {
	e.resumeCalled = true
	e.resumeTimeout = timeout
	return phaseHandleStub{result: e.result}, nil
}
func (e *resumeExecutorStub) Start(context.Context, PhaseSpec, TaskSnapshot) (ExecutionHandle, error) {
	e.startCalled = true
	return phaseHandleStub{result: e.startResult}, nil
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

func TestReadExecutorSessionID(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := "---\nid: \"001\"\nexecutor_session_id: '{\"sessionId\":\"s-1\"}'\n---\n# body\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readExecutorSessionID(taskPath); got != `{"sessionId":"s-1"}` {
		t.Fatalf("readExecutorSessionID = %q", got)
	}
	if got := readExecutorSessionID(filepath.Join(dir, "missing.md")); got != "" {
		t.Fatalf("missing file should return empty, got %q", got)
	}
}

func TestRunDSHPhaseResumesWhenTokenPresent(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := "---\nid: \"001\"\nexecutor_session_id: 'tok-abc'\n---\n# body\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(&config.Config{Executor: "dsh-embed"})
	stub := &resumeExecutorStub{result: &ExecutionResult{Code: OutcomeSuccess}}
	r.phaseExecutor = stub
	r.runDSHPhase(context.Background(), PhaseSpec{Phase: "round2"}, TaskSnapshot{TaskID: "001", TaskPath: taskPath})
	if !stub.resumeCalled {
		t.Fatal("有 executor_session_id 时应走 Resume，而非 fresh Start")
	}
}

func TestRunDSHPhaseFreshStartWhenNoToken(t *testing.T) {
	r := New(&config.Config{Executor: "dsh-embed"})
	stub := &resumeExecutorStub{startResult: &ExecutionResult{Code: OutcomeSuccess}}
	r.phaseExecutor = stub
	r.runDSHPhase(context.Background(), PhaseSpec{Phase: "round2"}, TaskSnapshot{TaskID: "001"}) // 无 TaskPath
	if stub.resumeCalled {
		t.Fatal("无 executor_session_id 时应走 fresh Start，而非 Resume")
	}
	if !stub.startCalled {
		t.Fatal("无 executor_session_id 时应调用 Start")
	}
}

// TestRunDSHPhaseResumeTimeoutDoesNotFreshStart 守护 TASK-058 重复会话回归：
// resume 超时只表示 daemon 侧 HTTP 等待超时，agent-server 里的会话可能仍在
// 运行；此时 fresh start 会让两个会话并行写同一个任务文档（观测到同一任务
// 两个 planning 会话同时规划）。超时必须如实上报，由阶段重试策略接管，下轮
// scan 用同一 token 再 resume。
func TestRunDSHPhaseResumeTimeoutDoesNotFreshStart(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	content := "---\nid: \"058\"\nexecutor_session_id: 'tok-058'\n---\n# body\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(&config.Config{Executor: "dsh-embed"})
	stub := &resumeExecutorStub{
		result:      &ExecutionResult{Code: OutcomeTimedOut, Error: "agent-server request timed out"},
		startResult: &ExecutionResult{Code: OutcomeSuccess},
	}
	r.phaseExecutor = stub
	res, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "planning", Timeout: 30 * time.Minute},
		TaskSnapshot{TaskID: "058", TaskPath: taskPath})

	if !stub.resumeCalled {
		t.Fatal("有 executor_session_id 时应走 Resume")
	}
	if stub.startCalled {
		t.Fatal("resume 超时后不得 fresh start：服务器会话可能仍存活，fresh start 会形成两个并行会话写同一任务文档")
	}
	if outcome != OutcomeTimedOut || code != ErrPhaseTimeout {
		t.Fatalf("outcome/code = %q/%q, want timeout/PHASE_TIMEOUT（超时必须如实上报，而非吞掉后开新会话）", outcome, code)
	}
	if res == nil || res.Code != OutcomeTimedOut {
		t.Fatalf("result code = %v, want OutcomeTimedOut", res)
	}
	if stub.resumeTimeout != 30*time.Minute {
		t.Fatalf("Resume timeout = %v, want phase spec timeout 30m", stub.resumeTimeout)
	}
}

// TestRunDSHPhaseResumeInterruptedDoesNotFreshStart：resume 会话被 daemon 停机
// 中断时同样不得 fresh start——上报中断结果（携带 resume token），下次重启
// 继续 resume 同一会话。
func TestRunDSHPhaseResumeInterruptedDoesNotFreshStart(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	content := "---\nid: \"058\"\nexecutor_session_id: 'tok-058'\n---\n# body\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(&config.Config{Executor: "dsh-embed"})
	stub := &resumeExecutorStub{result: &ExecutionResult{Code: OutcomeInterrupted, Error: "agent-server request cancelled"}}
	r.phaseExecutor = stub
	_, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "round2"},
		TaskSnapshot{TaskID: "058", TaskPath: taskPath})

	if stub.startCalled {
		t.Fatal("resume 中断后不得 fresh start")
	}
	if outcome != OutcomeInterrupted || code != ErrPhaseInterrupted {
		t.Fatalf("outcome/code = %q/%q, want interrupted/PHASE_INTERRUPTED", outcome, code)
	}
}

// TestRunDSHPhaseResumeTerminalFailureFallsBackToFreshStart：resume 返回终态
// 失败（会话已在 agent-server 里结束）时 fresh start 是安全的——此时服务器里
// 没有存活会话，不会形成并行写。
func TestRunDSHPhaseResumeTerminalFailureFallsBackToFreshStart(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	content := "---\nid: \"058\"\nexecutor_session_id: 'tok-058'\n---\n# body\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{
		result:      &ExecutionResult{Code: OutcomeFailed, Error: `agent-server HTTP 500: session "..." not found`},
		startResult: &ExecutionResult{Code: OutcomeSuccess},
	}
	r.phaseExecutor = stub
	_, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "planning"},
		TaskSnapshot{TaskID: "058", TaskPath: taskPath})

	if !stub.resumeCalled || !stub.startCalled {
		t.Fatal("resume 终态失败（会话已死亡）应回退 fresh Start")
	}
	if outcome != OutcomeSuccess || code != "" {
		t.Fatalf("outcome/code = %q/%q, want success after fresh start", outcome, code)
	}
}
