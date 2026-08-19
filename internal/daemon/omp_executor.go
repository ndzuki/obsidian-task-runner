package daemon

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ompExecutor is the OMP adapter for PhaseExecutor. It freezes the current
// exec.CommandContext contract (--model/--auto-approve/-p/--thinking) so the
// daemon's behavior is unchanged while the seam is introduced. The daemon
// does NOT call this adapter yet — it exists as a behavior-preserving
// reference implementation and the migration target for processBatchSequential.
type ompExecutor struct {
	// cmd is the OMP executable (config.OMPCmd, default "omp").
	cmd string
}

// newOMPExecutor builds the OMP adapter. cmdPath empty defaults to "omp".
func newOMPExecutor(cmdPath string) *ompExecutor {
	if cmdPath == "" {
		cmdPath = "omp"
	}
	return &ompExecutor{cmd: cmdPath}
}

func (e *ompExecutor) Name() string { return "omp" }

func (e *ompExecutor) Resume(context.Context, string) (ExecutionHandle, error) {
	// The OMP adapter historically recovered via PID files in the daemon
	// (procAlive/adopt), not via a durable session token. Keep that split:
	// resume is unsupported at the executor seam; the daemon's adoption path
	// stays as-is until DSH migration introduces durable session ids.
	return nil, ErrResumeUnsupported
}

func (e *ompExecutor) Start(ctx context.Context, spec PhaseSpec, snap TaskSnapshot) (ExecutionHandle, error) {
	// ── args construction: byte-identical to daemon.go processBatchSequential ──
	// (phase→thinking mapping lives in ompPhaseThinking so both call sites
	// share one source).
	thinking := spec.ReasoningEffort
	if thinking == "" {
		thinking = ompPhaseThinking(spec.Phase)
	}
	args := []string{"--model", spec.Model, "--auto-approve", "-p", spec.SkillPrompt, "--thinking", thinking}
	if spec.ToolPolicy != "" {
		// Audit phase restricts the tool surface (read,grep,bash only).
		args = append(args, "--tools", spec.ToolPolicy)
	}

	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	cmd := exec.CommandContext(ctx, e.cmd, args...)
	cmd.Dir = spec.WorkingDir
	for _, kv := range spec.ExtraEnv {
		cmd.Env = append(os.Environ(), kv)
	}
	// Graceful shutdown: SIGTERM first (OMP persists its session), hard-kill
	// after WaitDelay.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	return &ompHandle{ctx: ctx, cancel: cancel, cmd: cmd, phase: spec.Phase}, nil
}

type ompHandle struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	phase  string
}

func (h *ompHandle) PID() int {
	if h.cmd.Process != nil {
		return h.cmd.Process.Pid
	}
	return 0
}

func (h *ompHandle) Wait() (*ExecutionResult, error) {
	defer h.cancel()
	runErr := h.cmd.Wait()
	switch {
	case runErr == nil:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeSuccess}, nil
	case h.ctx.Err() == context.DeadlineExceeded:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeTimedOut, Error: runErr.Error()}, nil
	case h.ctx.Err() != nil:
		// Daemon shutdown / explicit cancel.
		return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: runErr.Error()}, nil
	default:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: runErr.Error()}, nil
	}
}

// ompExecutor must satisfy PhaseExecutor.
var _ PhaseExecutor = (*ompExecutor)(nil)
