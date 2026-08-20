package daemon

import (
	"context"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
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
// the stable ExecutionResult to the daemon's failure-code vocabulary. It is the
// Phase 5 replacement for the inline OMP exec block: the caller routes
// outcome/code/reason into the existing failure/fallback/notification path
// without touching OMP-specific logging, PID files, or empty-stop watch.
func (r *Runner) runDSHPhase(ctx context.Context, spec PhaseSpec, snap TaskSnapshot) (ExecOutcome, ErrorCode, string) {
	executor := r.phaseExecutor
	if executor == nil {
		executor = newPhaseExecutor(r.cfg)
		r.phaseExecutor = executor
	}
	handle, err := executor.Start(ctx, spec, snap)
	if err != nil {
		return OutcomeFailed, ErrModelFailed, err.Error()
	}
	result, err := handle.Wait()
	if err != nil {
		return OutcomeFailed, ErrModelFailed, err.Error()
	}
	return mapExecOutcome(result)
}
