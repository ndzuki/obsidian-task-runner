package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
//     DSH adapter default set). DSH's own cross-model fallback plugin
//     (fallback.mjs) covers provider failure; there is no daemon-side fallback
//     layer — that OMP-era mechanism was removed with OMP itself.
//   - Future: replace spawn-per-phase with ctx.agents.create/resume for
//     durable, resumable sessions (Phase 3+). Until then, resume is
//     unsupported and daemon restart re-dispatches from frontmatter state,
//     same as today.
type dshExecutor struct {
	// dsh is the DSH executable (usually "dsh" on PATH via mise).
	dsh string
	// defaultProfile is the profile to boot ("headless").
	defaultProfile string
	// skillDir is where phase SKILL.md bodies live (~/.dsh/skills). The dsh
	// adapter injects the skill body directly — phase skills are marked
	// `disable-model-invocation: true` (they are daemon-invoked, not model-
	// loaded), so the DSH session cannot load them itself; this mirrors the
	// OMP daemon's behavior of injecting the skill body into the prompt.
	skillDir string
}

// newDSHExecutor builds the DSH adapter with the default headless profile.
func newDSHExecutor(dshPath string) *dshExecutor {
	return newDSHExecutorWithProfile(dshPath, "headless", "")
}

// newDSHExecutorWithProfile builds a DSH adapter with an explicit profile.
// Profiles own model routing because the headless app intentionally exposes no
// per-invocation --model flag; this keeps the design phase's v4-pro route in
// configuration rather than pretending PhaseSpec.Model is a CLI option.
func newDSHExecutorWithProfile(dshPath, profile, skillDir string) *dshExecutor {
	if dshPath == "" {
		dshPath = "dsh"
	}
	if profile == "" {
		profile = "headless"
	}
	if skillDir == "" {
		skillDir = defaultDSHSkillDir()
	}
	return &dshExecutor{dsh: dshPath, defaultProfile: profile, skillDir: skillDir}
}

// defaultDSHSkillDir resolves the DSH user-skill directory (~/.dsh/skills).
func defaultDSHSkillDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".dsh", "skills")
}

func (e *dshExecutor) Name() string { return "dsh" }

// Cancel is a no-op for the spawn adapter: its sessions are child processes
// bound to the dispatch ctx, which already carries the phase timeout.
func (e *dshExecutor) Cancel(context.Context, string) error { return nil }

func (e *dshExecutor) Resume(context.Context, PhaseSpec, string, time.Duration) (ExecutionHandle, error) {
	// DSH durable session resume lands in Phase 3 (ctx.agents.resume). For
	// now the daemon re-dispatches from frontmatter state after restart,
	// identical to today's OMP PID-adoption fallback.
	return nil, ErrResumeUnsupported
}

// dshTaskText translates the OMP slash-skill prompt into a DSH headless task.
// Phase skills carry `disable-model-invocation: true` (daemon-invoked), so the
// DSH session cannot load them from its catalog; the adapter therefore reads
// the SKILL.md body and injects it directly — the same contract the OMP daemon
// used (skill body in the prompt, target file as the argument).
func (e *dshExecutor) dshTaskText(skillPrompt string) string {
	trimmed := strings.TrimSpace(skillPrompt)
	if trimmed == "" {
		return "Complete the current task phase following the obsidian-task-runner workflow."
	}
	// Extract the leading "/skill-name" token (slash form). Prompts that are
	// not slash skills (e.g. the audit full-template) fall through unchanged.
	fields := strings.Fields(trimmed)
	skillName := ""
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		skillName = strings.TrimPrefix(fields[0], "/")
	}
	if skillName != "" && e.skillDir != "" {
		if body := readSkillBody(e.skillDir, skillName); body != "" {
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			argLine := ""
			if args != "" {
				argLine = "\n\n任务参数：" + args
			}
			return "执行 obsidian-task-runner 阶段任务。以下是该阶段的 skill 完整指令，必须严格遵循并完成本阶段，将结果写回任务文档。\n\n<skill name=\"" + skillName + "\">\n" + body + "\n</skill>" + argLine
		}
	}
	// Fallback: keep the slash form so the session can still attempt to load
	// the skill (model-invocable skills resolve this way).
	return "执行 obsidian-task-runner 阶段任务：\n\n" + trimmed + "\n\n严格遵循该 skill 的指令完成本阶段，并将结果写回任务文档。"
}

// readSkillBody returns the SKILL.md body for a skill name, or "" when the
// skill directory does not carry it.
func readSkillBody(skillDir, skillName string) string {
	path := filepath.Join(skillDir, skillName, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// toolPolicyPrompt renders the hard tool-surface constraint for spawn-mode
// sessions. The agent-server enforces the same policy at its RPC layer for
// embed sessions; this preamble is the spawn-path equivalent.
func toolPolicyPrompt(policy string) string {
	return "<tool_policy>\n本会话是受限工具会话。只允许使用以下工具：" + policy + "。\n" +
		"严禁调用任何写工具（edit/write/str_replace_editor/skill 写类工具等）——调用即违规。\n" +
		"唯一允许的写入是会话契约明确指定的产物文件；其余一律只读。\n" +
		"违反本政策 = 会话失败，产出作废。\n</tool_policy>\n\n"
}

func (e *dshExecutor) Start(ctx context.Context, spec PhaseSpec, snap TaskSnapshot) (ExecutionHandle, error) {
	// DSH headless takes the task as a positional argument.
	taskText := e.dshTaskText(spec.SkillPrompt)
	if spec.ToolPolicy != "" {
		// Spawn 路径没有 agent-server 的 RPC 层工具约束：把政策作为
		// 会话最高优先级硬约束注入 prompt（read-only 审查会话如
		// conventions/audit 不得调用写工具）。
		taskText = toolPolicyPrompt(spec.ToolPolicy) + taskText
	}
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
	// Capture stderr and stdout into temp *os.Files (not pipes): dsh headless
	// writes `dsh: <code>: <message>` on stderr and the final assistant message
	// on stdout; its exit code is only 0/1, so the failure class and any JSON
	// output contract must be recovered from the streams
	// (docs/phase5-executor-migration.md §5.6). *os.File avoids the pipe that
	// a bytes.Buffer would introduce — a killed child keeps a pipe's write end
	// open and hangs Wait() on timeout.
	stderrFile, err := os.CreateTemp("", "dsh-stderr-*.log")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dsh stderr temp: %w", err)
	}
	stdoutFile, err := os.CreateTemp("", "dsh-stdout-*.log")
	if err != nil {
		cancel()
		_ = stderrFile.Close()
		_ = os.Remove(stderrFile.Name())
		return nil, fmt.Errorf("dsh stdout temp: %w", err)
	}
	cmd.Stderr = stderrFile
	cmd.Stdout = stdoutFile
	// Graceful shutdown: SIGTERM first, hard-kill after WaitDelay (mirrors
	// the OMP adapter's contract).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		cancel()
		_ = stderrFile.Close()
		_ = os.Remove(stderrFile.Name())
		_ = stdoutFile.Close()
		_ = os.Remove(stdoutFile.Name())
		return nil, fmt.Errorf("dsh start: %w", err)
	}
	return &dshHandle{ctx: ctx, cancel: cancel, cmd: cmd, phase: spec.Phase, stderrFile: stderrFile, stdoutFile: stdoutFile}, nil
}

type dshHandle struct {
	ctx        context.Context
	cancel     context.CancelFunc
	cmd        *exec.Cmd
	phase      string
	stderrFile *os.File
	stdoutFile *os.File
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

	// Read the captured streams and release the temp files regardless of
	// outcome.
	stderrText := ""
	stdoutText := ""
	if h.stderrFile != nil {
		if data, rerr := os.ReadFile(h.stderrFile.Name()); rerr == nil {
			stderrText = string(data)
		}
		_ = h.stderrFile.Close()
		_ = os.Remove(h.stderrFile.Name())
	}
	if h.stdoutFile != nil {
		if data, rerr := os.ReadFile(h.stdoutFile.Name()); rerr == nil {
			stdoutText = string(data)
		}
		_ = h.stdoutFile.Close()
		_ = os.Remove(h.stdoutFile.Name())
	}

	switch {
	case runErr == nil:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeSuccess, Stdout: stdoutText}, nil
	case h.ctx.Err() == context.DeadlineExceeded:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeTimedOut, Error: runErr.Error(), Stdout: stdoutText}, nil
	case h.ctx.Err() != nil:
		return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: runErr.Error(), Stdout: stdoutText}, nil
	default:
		// dsh exit code is 0/1 only; recover the failure class from the
		// `dsh: <code>: <message>` stderr line.
		res := dshFailureResult(h.phase, stderrText, runErr)
		res.Stdout = stdoutText
		return res, nil
	}
}

// dshFailureResult maps a failed dsh headless run to an ExecutionResult,
// classifying the stderr `dsh: <code>: <message>` line into the closed
// ExecOutcome set. Unknown/absent codes fall back to OutcomeFailed with the
// stderr tail preserved as the error reason.
func dshFailureResult(phase, stderrText string, runErr error) *ExecutionResult {
	code, message := parseDshErrorLine(stderrText)
	reason := message
	if reason == "" {
		reason = runErr.Error()
	}
	switch code {
	case "QUOTA":
		return &ExecutionResult{Phase: phase, Code: OutcomeQuotaExhausted, Error: reason}
	case "EMPTY_RESPONSE":
		return &ExecutionResult{Phase: phase, Code: OutcomeEmptyResponse, Error: reason}
	case "INVALID_CREDENTIAL":
		return &ExecutionResult{Phase: phase, Code: OutcomeKeyUnavailable, Error: reason}
	default:
		return &ExecutionResult{Phase: phase, Code: OutcomeFailed, Error: reason}
	}
}

// parseDshErrorLine extracts the machine code and message from a `dsh: <code>:
// <message>` stderr line (e.g. `dsh: QUOTA: quota exceeded`). Returns ("", "")
// when no such line exists.
func parseDshErrorLine(stderrText string) (code, message string) {
	for _, line := range strings.Split(stderrText, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "dsh: ") {
			continue
		}
		rest := strings.TrimPrefix(line, "dsh: ")
		// <code>: <message>
		if idx := strings.IndexByte(rest, ':'); idx > 0 {
			return strings.TrimSpace(rest[:idx]), strings.TrimSpace(rest[idx+1:])
		}
		// Plain `dsh: <message>` (direct-driver failure, no code).
		return "", rest
	}
	return "", ""
}

// dshExecutor must satisfy PhaseExecutor.
var _ PhaseExecutor = (*dshExecutor)(nil)
