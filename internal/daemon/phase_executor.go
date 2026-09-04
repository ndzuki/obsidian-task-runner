package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// newPhaseExecutor selects the phase-dispatch backend from cfg.Executor.
//   - "dsh"       — spawn-headless DSH adapter.
//   - "dsh-embed" — long-lived agent-server RPC (per-phase reasoning effort +
//     durable resume; docs/archive/embed-migration-plan.md). The terminal form.
//
// Default is dsh-embed (config.Defaults.Executor).
func newPhaseExecutor(cfg *config.Config) PhaseExecutor {
	if cfg.Executor == "dsh" {
		return newDSHExecutorWithProfile(cfg.DSHCmd, cfg.DSHProfile, "")
	}
	e := newDSHEmbedExecutor(cfg.AgentServerAddr, "")
	e.fallback = cfg.Fallback
	return e
}

// newDesignExecutor selects the backend for global design sessions. It follows
// the same selection as newPhaseExecutor: embed (per-request reasoningEffort +
// durable resume + fallback forwarding) under the default dsh-embed executor,
// spawn only when the user explicitly pinned executor="dsh" (spawn cannot
// transmit ReasoningEffort — the effort falls to the profile default, which is
// why design was migrated to embed on 2026-09-02).
func newDesignExecutor(cfg *config.Config) PhaseExecutor {
	if cfg.Executor == "dsh" {
		return newDSHExecutorWithProfile(cfg.DSHCmd, cfg.DSHProfile, "")
	}
	e := newDSHEmbedExecutor(cfg.AgentServerAddr, "")
	e.fallback = cfg.Fallback
	return e
}

// staleSessionReconciler is implemented by executors with server-side durable
// sessions (dsh-embed). Before any fresh Start for a task, the runner cancels
// still-working sessions of a previous daemon incarnation (daemon restarts
// leave them alive inside the externally managed agent-server), so restarts
// can never accumulate concurrent writers on the same worktree. Spawn-style
// executors without server-side sessions do not implement it.
type staleSessionReconciler interface {
	CancelStaleTaskSessions(ctx context.Context, taskID string) error
}

// cancelStaleTaskSessions cancels still-working agent-server sessions labelled
// with the given task, when the executor supports it. No-op for task-less
// sessions (pm/grilling) and for executors without server-side sessions. Uses
// a short detached context: the dispatch ctx may already be near its deadline
// and the reconcile must not ride it. Errors are returned so callers abort
// the fresh Start (a failed enumeration means the writer set is unknown).
func (r *Runner) cancelStaleTaskSessions(executor PhaseExecutor, taskID string) error {
	if taskID == "" || executor == nil {
		return nil
	}
	rec, ok := executor.(staleSessionReconciler)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rec.CancelStaleTaskSessions(ctx, taskID); err != nil {
		return fmt.Errorf("cancel stale task sessions: %w", err)
	}
	return nil
}

// runDSHPhase executes one phase through the configured phaseExecutor and maps
// the stable ExecutionResult to the daemon's failure-code vocabulary. When the
// task frontmatter carries a persisted executor_session_id (an interrupted
// dsh-embed session), it resumes that session first; a failed/unsupported
// resume falls back to a fresh Start.
func (r *Runner) runDSHPhase(ctx context.Context, spec PhaseSpec, snap TaskSnapshot) (*ExecutionResult, ExecOutcome, ErrorCode, string) {
	executor := r.phaseExecutor
	if executor == nil {
		executor = newPhaseExecutor(r.cfg)
		r.phaseExecutor = executor
	}

	// durable resume：上次中断会话（executor_session_id 持久化在 frontmatter）。
	// 只有 resume 真正成功（OutcomeSuccess）才复用会话结果；会话真正终态
	// （Failed/Quota/…——agent-server 里已无存活会话）才回退 fresh start。
	// 超时/中断必须如实上报：daemon 侧 HTTP 等待超时不会终止 agent-server
	// 里的会话，此时 fresh start 会让两个会话并行写同一个任务文档
	// （TASK-058 观测：同一任务两个 planning 会话并行规划）。
	//
	// TASK-058 二次观测（install-force 重启变种）：旧 daemon 被 SIGKILL 时其
	// 会话仍在 agent-server 中运行（busy）；新 daemon resume 该会话时
	// agent-server 返回 500（"already has active work" 等），旧代码把一切
	// 非 Success 都当终态失败 → fresh start → 旧会话继续跑 + 新会话并行写。
	// 因此：只有错误文本能证明会话已死（session not found）才允许 fresh
	// start；其余挂接失败/未知错误一律按可重试处理（OutcomeInterrupted
	// 语义），下一轮 scan 用同一 token 再 resume，绝不新开会话。
	if token := readExecutorSessionID(snap.TaskPath); token != "" {
		if handle, err := executor.Resume(ctx, spec, token, spec.Timeout); err == nil {
			if result, werr := handle.Wait(); werr == nil {
				if result.Code == OutcomeSuccess {
					outcome, code, reason := mapExecOutcome(result)
					return result, outcome, code, reason
				}
				if result.Code == OutcomeTimedOutActive {
					// 超时窗口耗尽但会话近期仍有活动（模型还在推 step/工具
					// 调用——TASK-065：Round 2 真实冒烟远超 60 分钟窗口）。
					// 不 cancel、不记失败：保留 token，下一轮 scan 继续等。
					r.logger.Printf("task %s: resumed session still active after timeout window (%s) — keeping token, next scan continues waiting", snap.TaskID, spec.Phase)
					outcome, code, reason := mapExecOutcome(result)
					return result, outcome, code, reason
				}
				if result.Code == OutcomeTimedOut || result.Code == OutcomeInterrupted {
					// 超时：会话可能是死锁的模型 turn（gateway 挂起，TASK-079
					// refining 观测 6.8h）——cancel 后下一轮 resume 才能落到
					// fresh start，否则会永久 re-attach 同一个死 turn。
					// 中断：daemon 停机，会话必须保留供 resume，不 cancel。
					if result.Code == OutcomeTimedOut {
						r.cancelWedgedSession(ctx, executor, result.ResumeToken, snap.TaskID)
					}
					// 会话可能仍在 agent-server 里运行：上报真实结果，
					// 下一轮 scan 用同一 token 再 resume。
					outcome, code, reason := mapExecOutcome(result)
					return result, outcome, code, reason
				}
				if sessionBusyEvidence(result.Error) || resumeUnknownStateEvidence(result.Error) {
					// 会话明确仍在跑（busy），或服务器不可达（unreachable/EOF/
					// connection refused——会话状态未知）：都可重试中断，绝不
					// fresh start（否则两个会话并行写同一任务文档；2026-08-25
					// TASK-065 观测：agent-server unreachable: EOF 被当 terminal
					// → fresh start 撞 connection refused → MODEL_FAILED 再写
					// blocked → 状态来回变）。
					r.logger.Printf("task %s: resume attach failed (%s), session state unknown — retrying next scan (no fresh start)", snap.TaskID, result.Error)
					res := &ExecutionResult{
						Phase:       spec.Phase,
						Code:        OutcomeInterrupted,
						Error:       "resume attach failed, session state unknown: " + result.Error,
						ResumeToken: token,
					}
					outcome, code, reason := mapExecOutcome(res)
					return res, outcome, code, reason
				}
				// 其余（session gone / 服务器确认 turn 已结束，如 "agent-server
				// outcome error"）：服务器侧无活跃写者，fresh start 安全且是唯一
				// 收敛路径（TASK-079 观测：死 turn 会让 interrupted-retry 永转）。
				r.logger.Printf("task %s: resume terminal (%s), falling back to fresh start", snap.TaskID, result.Error)
			} else if !sessionGoneEvidence(werr.Error()) {
				// 传输层错误（unreachable 等）：服务器状态未知，保守重试。
				r.logger.Printf("task %s: resume wait failed (%v), transport-level — retrying next scan (no fresh start)", snap.TaskID, werr)
				res := &ExecutionResult{Phase: spec.Phase, Code: OutcomeInterrupted, Error: "resume wait failed: " + werr.Error(), ResumeToken: token}
				outcome, code, reason := mapExecOutcome(res)
				return res, outcome, code, reason
			}
			r.logger.Printf("task %s: resume not successful, falling back to fresh start", snap.TaskID)
		} else if !errors.Is(err, ErrResumeUnsupported) {
			if !sessionGoneEvidence(err.Error()) {
				// Resume RPC 本身失败（连接异常等）：会话状态未知，按可重试
				// 中断上报，下一轮 scan 再试 resume——不得 fresh start。
				r.logger.Printf("task %s: resume failed (%v), session state unknown — retrying next scan (no fresh start)", snap.TaskID, err)
				res := &ExecutionResult{Phase: spec.Phase, Code: OutcomeInterrupted, Error: "resume failed: " + err.Error(), ResumeToken: token}
				outcome, code, reason := mapExecOutcome(res)
				return res, outcome, code, reason
			}
			r.logger.Printf("task %s: resume failed (%v), falling back to fresh start", snap.TaskID, err)
		}
	}

	// Fresh start：先 reconcile 同一任务上一代 daemon 残留的 working 会话。
	// 残留会话（daemon 重启后仍跑在外部 agent-server 里）与本次新会话会并发
	// 写同一 worktree——必须先把旧会话 cancel 掉，保证同一任务同一时刻只有
	// 一个活跃写者。reconcile 失败（服务器不可达/响应异常）时中止本次派发，
	// 按可重试中断上报：宁可这轮不跑，也不冒险叠加第二个写者。
	if err := r.cancelStaleTaskSessions(executor, snap.TaskID); err != nil {
		r.logger.Printf("task %s: cancel stale sessions before fresh start failed: %v", snap.TaskID, err)
		res := &ExecutionResult{Phase: spec.Phase, Code: OutcomeInterrupted, Error: "stale-session reconcile failed: " + err.Error()}
		outcome, code, reason := mapExecOutcome(res)
		return res, outcome, code, reason
	}

	handle, err := executor.Start(ctx, spec, snap)
	if err != nil {
		return nil, OutcomeFailed, ErrModelFailed, err.Error()
	}
	result, err := handle.Wait()
	if err != nil {
		return nil, OutcomeFailed, ErrModelFailed, err.Error()
	}
	if result != nil && result.Code == OutcomeTimedOutActive {
		// 全新派发的超时窗口耗尽但会话仍活跃：不 cancel（会话在真实推进），
		// 保留 token 下一轮 scan 继续等。
		r.logger.Printf("task %s: phase session still active after timeout window (%s) — next scan continues waiting", snap.TaskID, spec.Phase)
		outcome, code, reason := mapExecOutcome(result)
		return result, outcome, code, reason
	}
	if result != nil && result.Code == OutcomeTimedOut {
		// 全新派发也超时：cancel 服务器侧会话（同样的死 turn 问题），下一轮
		// resume 找不到会话 → fresh start。
		r.cancelWedgedSession(ctx, executor, result.ResumeToken, snap.TaskID)
	}
	outcome, code, reason := mapExecOutcome(result)
	return result, outcome, code, reason
}

// cancelWedgedSession cancels a phase session on the agent-server after a
// phase timeout. Best-effort: cancellation failure only means the session
// stays live and the next resume re-attaches — the retry budget still bounds
// the loop. The Cancel call must not use the (already expired) dispatch ctx.
func (r *Runner) cancelWedgedSession(ctx context.Context, executor PhaseExecutor, resumeToken, taskID string) {
	if resumeToken == "" || executor == nil {
		return
	}
	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := executor.Cancel(cancelCtx, resumeToken); err != nil {
		r.logger.Printf("task %s: cancel wedged session after phase timeout: %v", taskID, err)
		return
	}
	r.logger.Printf("task %s: phase timeout — wedged session cancelled (next scan will fresh start)", taskID)
}

// sessionGoneEvidence reports whether an agent-server error string proves the
// target session no longer exists. Only this evidence may authorize a fresh
// start after a failed resume — every other failure keeps the durable token
// and retries resume on the next scan.
func sessionGoneEvidence(errText string) bool {
	lower := strings.ToLower(errText)
	hasSession := strings.Contains(lower, "session")
	hasNotFound := strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no such session") ||
		strings.Contains(lower, "does not exist")
	return hasSession && hasNotFound
}

// sessionBusyEvidence reports whether the error says the session still has
// active work ("already has active work") — the only non-timeout case where
// the server-side session is provably still running and fresh start would
// create a parallel writer.
func sessionBusyEvidence(errText string) bool {
	return strings.Contains(strings.ToLower(errText), "active work")
}

// resumeUnknownStateEvidence reports whether a resume failure says the
// agent-server itself was unreachable (EOF, connection refused, dial
// errors, no such host) rather than confirming the session is gone. The
// session's state is then UNKNOWN: fresh start is forbidden — the old turn
// may still be alive server-side and would become a parallel writer the
// moment the server returns (2026-08-25 TASK-065: unreachable: EOF 被误判
// terminal → fresh start → MODEL_FAILED → blocked → 状态来回变)。
func resumeUnknownStateEvidence(errText string) bool {
	lower := strings.ToLower(errText)
	for _, marker := range []string{
		"unreachable", "connection refused", "dial tcp", "no such host", " eof", "network is down",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// readExecutorSessionID reads the persisted durable-resume token from the task
// frontmatter. Returns "" when absent or unreadable (fresh start).
func readExecutorSessionID(taskPath string) string {
	if taskPath == "" {
		return ""
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return ""
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return ""
	}
	return fm.ExecutorSessionID
}
