package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// dshExecutor is the DeepSeek Harness adapter for PhaseExecutor. It spawns
// `dsh --profile headless` per phase — the minimal, drop-in migration path
// that keeps the Go control plane untouched while replacing the OMP engine.
//
// Migration notes (target architecture docs/refactor-architecture.md §4):
//   - The skill prompt is translated into a DSH task text that loads the same
//     slash skill. The DSH session's own skill routing (dsh-tool-skill +
//     skill catalog) resolves it; no daemon-side prompt string assembly.
//   - The model is passed as a provider/model identity resolved by the
//     adapter from the task assignee (vault-map models), matching DSH's
//     provider/model route form.
//   - Reasoning effort maps to DSH's effort enum (off/low/high/max → the
//     DSH adapter default set). DSH's own cross-model fallback plugin covers
//     provider failure; the daemon-side fallback_models path is retained as a
//     second layer until the DSH route is fully trusted.
//   - Future: replace spawn-per-phase with ctx.agents.create/resume for
//     durable, resumable sessions (Phase 3+). Until then, resume is
//     unsupported and daemon restart re-dispatches from frontmatter state,
//     same as today.
type dshExecutor struct {
	// dsh is the DSH executable (usually "dsh" on PATH via mise).
	dsh string
	// defaultProfile is the profile to boot ("headless").
	defaultProfile string
}

// newDSHExecutor builds the DSH adapter with the default headless profile.
func newDSHExecutor(dshPath string) *dshExecutor {
	return newDSHExecutorWithProfile(dshPath, "headless")
}

// newDSHExecutorWithProfile builds a DSH adapter with an explicit profile.
// Profiles own model routing because the headless app intentionally exposes no
// per-invocation --model flag; this keeps the design phase's v4-pro route in
// configuration rather than pretending PhaseSpec.Model is a CLI option.
func newDSHExecutorWithProfile(dshPath, profile string) *dshExecutor {
	if dshPath == "" {
		dshPath = "dsh"
	}
	if profile == "" {
		profile = "headless"
	}
	return &dshExecutor{dsh: dshPath, defaultProfile: profile}
}

func (e *dshExecutor) Name() string { return "dsh" }

func (e *dshExecutor) Resume(context.Context, string) (ExecutionHandle, error) {
	// DSH durable session resume lands in Phase 3 (ctx.agents.resume). For
	// now the daemon re-dispatches from frontmatter state after restart,
	// identical to today's OMP PID-adoption fallback.
	return nil, ErrResumeUnsupported
}

// dshTaskText translates the OMP slash-skill prompt into a DSH headless task.
// The task asks the DSH session to load the same skill and act on the same
// target file, preserving the phase contract.
func dshTaskText(skillPrompt string) string {
	trimmed := strings.TrimSpace(skillPrompt)
	if trimmed == "" {
		return "Complete the current task phase following the obsidian-task-runner workflow."
	}
	// The OMP slash prompt is "/skill <args>". DSH sessions resolve skills by
	// name from their catalog; keep the slash form so the model recognizes
	// the skill and loads it, and append an explicit instruction to follow it.
	return "执行 obsidian-task-runner 阶段任务：\n\n" + trimmed + "\n\n严格遵循该 skill 的指令完成本阶段，并将结果写回任务文档。"
}

func (e *dshExecutor) Start(ctx context.Context, spec PhaseSpec, snap TaskSnapshot) (ExecutionHandle, error) {
	// DSH headless takes the task as a positional argument.
	taskText := dshTaskText(spec.SkillPrompt)
	args := []string{"--profile", e.defaultProfile, taskText}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	cmd := exec.CommandContext(ctx, e.dsh, args...)
	cmd.Dir = spec.WorkingDir
	for _, kv := range spec.ExtraEnv {
		cmd.Env = append(os.Environ(), kv)
	}
	// Graceful shutdown: SIGTERM first, hard-kill after WaitDelay (mirrors
	// the OMP adapter's contract).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("dsh start: %w", err)
	}
	return &dshHandle{ctx: ctx, cancel: cancel, cmd: cmd, phase: spec.Phase}, nil
}

type dshHandle struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	phase  string
}

func (h *dshHandle) PID() int {
	if h.cmd.Process != nil {
		return h.cmd.Process.Pid
	}
	return 0
}

func (h *dshHandle) Wait() (*ExecutionResult, error) {
	defer h.cancel()
	runErr := h.cmd.Wait()
	switch {
	case runErr == nil:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeSuccess}, nil
	case h.ctx.Err() == context.DeadlineExceeded:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeTimedOut, Error: runErr.Error()}, nil
	case h.ctx.Err() != nil:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: runErr.Error()}, nil
	default:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: runErr.Error()}, nil
	}
}

// dshExecutor must satisfy PhaseExecutor.
var _ PhaseExecutor = (*dshExecutor)(nil)
