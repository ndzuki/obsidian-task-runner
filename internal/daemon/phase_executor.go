package daemon

import (
	"context"
	"errors"
	"os"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// newPhaseExecutor selects the phase-dispatch backend from cfg.Executor.
//   - "dsh"       — spawn-headless DSH adapter.
//   - "dsh-embed" — long-lived agent-server RPC (per-phase reasoning effort +
//     durable resume; docs/embed-migration-plan.md). The terminal form.
//
// Default is dsh-embed (config.Defaults.Executor).
func newPhaseExecutor(cfg *config.Config) PhaseExecutor {
	if cfg.Executor == "dsh" {
		return newDSHExecutorWithProfile(cfg.DSHCmd, cfg.DSHProfile, "")
	}
	return newDSHEmbedExecutor(cfg.AgentServerAddr, "")
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
	// 只有 resume 真正成功（OutcomeSuccess）才复用会话结果；否则（session not
	// found——中断发生在 agent-server create 之前、或 agent-server 重启丢会话）
	// 一律回退 fresh start，避免把「会话已失效」误判为阶段失败（MODEL_FAILED）。
	if token := readExecutorSessionID(snap.TaskPath); token != "" {
		if handle, err := executor.Resume(ctx, token); err == nil {
			if result, err := handle.Wait(); err == nil && result.Code == OutcomeSuccess {
				outcome, code, reason := mapExecOutcome(result)
				return result, outcome, code, reason
			}
			r.logger.Printf("task %s: resume not successful, falling back to fresh start", snap.TaskID)
		} else if !errors.Is(err, ErrResumeUnsupported) {
			r.logger.Printf("task %s: resume failed (%v), falling back to fresh start", snap.TaskID, err)
		}
	}

	handle, err := executor.Start(ctx, spec, snap)
	if err != nil {
		return nil, OutcomeFailed, ErrModelFailed, err.Error()
	}
	result, err := handle.Wait()
	if err != nil {
		return nil, OutcomeFailed, ErrModelFailed, err.Error()
	}
	outcome, code, reason := mapExecOutcome(result)
	return result, outcome, code, reason
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
