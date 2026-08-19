package daemon

import (
	"context"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// newPhaseExecutor selects the phase-dispatch backend from cfg.Executor.
// Default is the frozen OMP adapter (behavior unchanged); "dsh" opts into the
// spawn-headless DSH adapter. The seam is the Phase 5 migration point — the
// default flips to dsh only after every phase is verified on it
// (docs/phase5-executor-migration.md).
func newPhaseExecutor(cfg *config.Config) PhaseExecutor {
	if cfg.Executor == "dsh" {
		return newDSHExecutorWithProfile(cfg.DSHCmd, cfg.DSHProfile, "")
	}
	return newOMPExecutor(cfg.OMPCmd)
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
