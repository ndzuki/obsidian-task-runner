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

func (e phaseExecutorStub) Name() string                         { return "stub" }
func (e phaseExecutorStub) Cancel(context.Context, string) error { return nil }
func (e phaseExecutorStub) Resume(context.Context, PhaseSpec, string, time.Duration) (ExecutionHandle, error) {
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
	resumeWaitErr error            // Resume handle.Wait 返回的错误
	startResult   *ExecutionResult // Start（fresh start）返回的结果
	resumeCalled  bool
	startCalled   bool
	cancelCalled  bool
	cancelToken   string
	resumeTimeout time.Duration
	resumeSpec    PhaseSpec
}

func (e *resumeExecutorStub) Cancel(_ context.Context, token string) error {
	e.cancelCalled = true
	e.cancelToken = token
	return nil
}
func (e *resumeExecutorStub) Name() string { return "resume-stub" }
func (e *resumeExecutorStub) Resume(_ context.Context, spec PhaseSpec, _ string, timeout time.Duration) (ExecutionHandle, error) {
	e.resumeCalled = true
	e.resumeTimeout = timeout
	e.resumeSpec = spec
	return phaseHandleStub{result: e.result, waitErr: e.resumeWaitErr}, nil
}
func (e *resumeExecutorStub) Start(context.Context, PhaseSpec, TaskSnapshot) (ExecutionHandle, error) {
	e.startCalled = true
	return phaseHandleStub{result: e.startResult}, nil
}

type phaseHandleStub struct {
	result  *ExecutionResult
	waitErr error
}

func (h phaseHandleStub) Wait() (*ExecutionResult, error) { return h.result, h.waitErr }
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

// TestRunDSHPhaseResumeBusyDoesNotFreshStart 守护 TASK-058 二次观测
// （install-force 重启变种）：旧 daemon 被 SIGKILL 时其会话仍在 agent-server
// 运行；新 daemon resume 拿到 500（"already has active work"）——不得把挂接
// 失败当终态失败 fresh start，否则旧会话继续跑 + 新会话并行写同一任务文档。
// 挂接失败按可重试中断上报，下轮 scan 用同一 token 再 resume。
func TestRunDSHPhaseResumeBusyDoesNotFreshStart(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	content := "---\nid: \"058\"\nexecutor_session_id: 'tok-058'\n---\n# body\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{
		result:      &ExecutionResult{Code: OutcomeFailed, Error: `agent-server HTTP 500: {"error":"agent \"...\" already has active work"}`},
		startResult: &ExecutionResult{Code: OutcomeSuccess},
	}
	r.phaseExecutor = stub
	res, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "planning"},
		TaskSnapshot{TaskID: "058", TaskPath: taskPath})

	if !stub.resumeCalled {
		t.Fatal("有 executor_session_id 时应走 Resume")
	}
	if stub.startCalled {
		t.Fatal("resume 挂接失败（busy）不得 fresh start：旧会话仍在 agent-server 运行，fresh start 形成双会话并行写")
	}
	if outcome != OutcomeInterrupted || code != ErrPhaseInterrupted {
		t.Fatalf("outcome/code = %q/%q, want interrupted/PHASE_INTERRUPTED（可重试）", outcome, code)
	}
	if res == nil || res.ResumeToken != "tok-058" {
		t.Fatalf("resume token must be preserved for next-scan retry, got %+v", res)
	}
}

// TestRunDSHPhaseResumeUnreachableDoesNotFreshStart 守护 2026-08-25
// TASK-065 观测：resume 结果错误为「agent-server unreachable: … EOF」
// 时，会话状态未知——不得判 terminal 回退 fresh start（fresh start 又会
// 撞 connection refused → MODEL_FAILED → blocked → 状态来回变）。按可重试
// 中断上报，下轮 scan 用同一 token 再 resume。
func TestRunDSHPhaseResumeUnreachableDoesNotFreshStart(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-065.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"065\"\nexecutor_session_id: 'tok-065'\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{
		result:      &ExecutionResult{Code: OutcomeFailed, Error: `agent-server unreachable: Post "http://127.0.0.1:8799/agent/run": EOF`},
		startResult: &ExecutionResult{Code: OutcomeSuccess},
	}
	r.phaseExecutor = stub
	res, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "implementing"},
		TaskSnapshot{TaskID: "065", TaskPath: taskPath})

	if !stub.resumeCalled {
		t.Fatal("有 executor_session_id 时应走 Resume")
	}
	if stub.startCalled {
		t.Fatal("resume 服务器不可达不得 fresh start：会话状态未知，fresh start 可能形成双会话并行写")
	}
	if outcome != OutcomeInterrupted || code != ErrPhaseInterrupted {
		t.Fatalf("outcome/code = %q/%q, want interrupted/PHASE_INTERRUPTED（可重试）", outcome, code)
	}
	if res == nil || res.ResumeToken != "tok-065" {
		t.Fatalf("resume token must be preserved for next-scan retry, got %+v", res)
	}
}

// TestRunDSHPhaseResumeUnknownErrorDoesNotFreshStart：Resume RPC 本身报
// 非「会话已死」错误（unreachable 等）时同样不得 fresh start——会话状态
// 未知，下轮 scan 再试 resume。
func TestRunDSHPhaseResumeUnknownErrorDoesNotFreshStart(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"058\"\nexecutor_session_id: 'tok-058'\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{resumeWaitErr: context.DeadlineExceeded}
	r.phaseExecutor = stub
	_, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "planning"},
		TaskSnapshot{TaskID: "058", TaskPath: taskPath})

	if stub.startCalled {
		t.Fatal("resume 等待错误不得 fresh start")
	}
	if outcome != OutcomeInterrupted || code != ErrPhaseInterrupted {
		t.Fatalf("outcome/code = %q/%q, want interrupted/PHASE_INTERRUPTED", outcome, code)
	}
}

// TestRunDSHPhaseResumeForwardsCurrentSpec：resume 必须携带当前 spec
// （相位/prompt），token 只标识会话——重启后可能以不同相位重新派发，
// 注入 token 里的旧 prompt 会在恢复会话里跑错阶段。
func TestRunDSHPhaseResumeForwardsCurrentSpec(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"058\"\nexecutor_session_id: 'tok-058'\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{result: &ExecutionResult{Code: OutcomeSuccess}}
	r.phaseExecutor = stub
	spec := PhaseSpec{Phase: "planning", SkillPrompt: "/obsidian-task-runner-round1 /task.md", Timeout: 30 * time.Minute}
	r.runDSHPhase(context.Background(), spec, TaskSnapshot{TaskID: "058", TaskPath: taskPath})

	if !stub.resumeCalled {
		t.Fatal("应走 Resume")
	}
	if stub.resumeSpec.Phase != "planning" || stub.resumeSpec.SkillPrompt != spec.SkillPrompt {
		t.Fatalf("Resume 收到 spec = %+v, want phase=planning + 当前 prompt", stub.resumeSpec)
	}
	if stub.resumeTimeout != 30*time.Minute {
		t.Fatalf("Resume timeout = %v, want spec timeout", stub.resumeTimeout)
	}
}

// TestRunDSHPhaseTimeoutCancelsWedgedSession 守护阶段超时后的会话回收：
// resume/Start 超时只表示 daemon 侧等待超时，agent-server 里的 model turn
// 可能已死锁（TASK-079 refining 观测 6.8h 挂起）——必须 Cancel 掉，否则
// 下一轮 resume 永远 re-attach 同一个死 turn。
func TestRunDSHPhaseTimeoutCancelsWedgedSession(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-079.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"079\"\nexecutor_session_id: '{\"sessionId\":\"s-1\",\"provider\":\"p\",\"model\":\"m\"}'\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{result: &ExecutionResult{Code: OutcomeTimedOut, ResumeToken: `{"sessionId":"s-1","provider":"p","model":"m"}`}}
	r.phaseExecutor = stub
	_, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "refining", Timeout: 15 * time.Minute},
		TaskSnapshot{TaskID: "079", TaskPath: taskPath})
	if outcome != OutcomeTimedOut || code != ErrPhaseTimeout {
		t.Fatalf("outcome/code = %q/%q, want timeout/PHASE_TIMEOUT", outcome, code)
	}
	if !stub.cancelCalled {
		t.Fatal("phase timeout 后必须 Cancel 服务器侧会话（否则下一轮 resume 死循环）")
	}
	if stub.cancelToken != `{"sessionId":"s-1","provider":"p","model":"m"}` {
		t.Fatalf("cancel token = %q", stub.cancelToken)
	}
}

// TestRunDSHPhaseInterruptedDoesNotCancel 守护反向语义：PHASE_INTERRUPTED
// （daemon 停机）必须保留会话供 durable resume，不得 Cancel。
func TestRunDSHPhaseInterruptedDoesNotCancel(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-079.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"079\"\nexecutor_session_id: '{\"sessionId\":\"s-1\",\"provider\":\"p\",\"model\":\"m\"}'\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{result: &ExecutionResult{Code: OutcomeInterrupted, ResumeToken: `{"sessionId":"s-1","provider":"p","model":"m"}`}}
	r.phaseExecutor = stub
	_, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "refining"},
		TaskSnapshot{TaskID: "079", TaskPath: taskPath})
	if outcome != OutcomeInterrupted || code != ErrPhaseInterrupted {
		t.Fatalf("outcome/code = %q/%q, want interrupted/PHASE_INTERRUPTED", outcome, code)
	}
	if stub.cancelCalled {
		t.Fatal("PHASE_INTERRUPTED 不得 Cancel（会话要留给 resume）")
	}
}

// TestRunDSHPhaseResumeServerEndedTurnFallsBackFresh 守护分类边界：
// resume 返回服务器确认的 turn 结束错误（"agent-server outcome error"——
// TASK-079 观测：卡死会话被 agent-server 重启清掉后，持久层的旧 turn
// 立即 error）时，会话侧已无活跃写者——必须 fresh start 收敛，否则
// interrupted-retry 会永远对着同一个死 turn 空转。
func TestRunDSHPhaseResumeServerEndedTurnFallsBackFresh(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-079.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"079\"\nexecutor_session_id: '{\"sessionId\":\"s-1\",\"provider\":\"p\",\"model\":\"m\"}'\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh-embed"})
	r.logger = log.New(io.Discard, "", 0)
	stub := &resumeExecutorStub{
		result:      &ExecutionResult{Code: OutcomeFailed, Error: "agent-server outcome error"},
		startResult: &ExecutionResult{Code: OutcomeSuccess},
	}
	r.phaseExecutor = stub
	_, outcome, code, _ := r.runDSHPhase(context.Background(),
		PhaseSpec{Phase: "refining"},
		TaskSnapshot{TaskID: "079", TaskPath: taskPath})

	if !stub.resumeCalled || !stub.startCalled {
		t.Fatal("服务器侧 turn 已结束的 resume 失败应回退 fresh Start")
	}
	if outcome != OutcomeSuccess || code != "" {
		t.Fatalf("outcome/code = %q/%q, want fresh-start success", outcome, code)
	}
}
