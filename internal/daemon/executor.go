package daemon

import (
	"context"
	"time"
)

// PhaseExecutor is the execution seam between the daemon's deterministic
// control plane and the DSH agent runtime (spawn-headless and dsh-embed
// adapters). Every phase dispatch (refining/planning/round2/merge/priority/
// pm/audit/conventions) flows through one of these adapters; the daemon
// consumes only the stable ExecutionResult — never a raw process protocol.
//
// The interface is deliberately minimal: Start + Resume + Collect. Cancel and
// shutdown ride the ctx passed to Start, mirroring how the current code uses
// exec.CommandContext.
type PhaseExecutor interface {
	// Start launches one phase execution and returns immediately. The handle
	// carries the ctx-derived cancellation and the result channel.
	Start(ctx context.Context, spec PhaseSpec, snap TaskSnapshot) (ExecutionHandle, error)
	// Resume re-attaches to an in-flight execution after a daemon restart,
	// using the durable identity recorded in the task frontmatter. The resume
	// must run the CURRENT spec's phase/prompt (not the token's stored copy):
	// the persisted token only names the durable session. timeout bounds the
	// re-attached wait (the phase's own timeout); zero uses the adapter
	// default. Adapters that cannot resume return ErrResumeUnsupported.
	Resume(ctx context.Context, spec PhaseSpec, resumeToken string, timeout time.Duration) (ExecutionHandle, error)
	// Cancel aborts a wedged execution session (phase timeout — the model turn
	// is stuck in the agent-server and resume would re-attach to the same dead
	// turn forever). Best-effort: next scan's resume must then find the
	// session gone and fall back to a fresh start. Adapters without server-side
	// sessions are no-ops.
	Cancel(ctx context.Context, resumeToken string) error
	// Name returns the adapter identity (e.g. "dsh", "dsh-embed").
	Name() string
}

// ErrResumeUnsupported is returned by adapters that cannot re-attach to an
// in-flight execution (the spawn-headless adapter has no durable session id
// to resume; the dsh-embed adapter does).
var ErrResumeUnsupported = errResumeUnsupported{}

type errResumeUnsupported struct{}

func (errResumeUnsupported) Error() string { return "resume unsupported by adapter" }

// PhaseSpec is the fully data-driven description of one phase execution. It
// replaces the scattered switch that currently builds args inline in
// processBatchSequential: every phase's model, reasoning effort, tool policy,
// prompt and timeout is a field here, so adapters and tests share one shape.
type PhaseSpec struct {
	// Phase is the daemon's phase key ("refining", "planning", "round2",
	// "merge", "priority", "pm", "audit", "conventions").
	Phase string
	// Model is the provider/model identity resolved for this task's
	// assignee ("provider/model"). The DSH adapters translate it into
	// their own route form.
	Model string
	// ReasoningEffort is the phase's reasoning budget ("off"/"low"/"high"/
	// "max"); adapters map it to their native effort field.
	ReasoningEffort string
	// SkillPrompt is the slash-skill prompt the session executes
	// ("/obsidian-task-runner-round2 <task> ..."). The DSH adapter
	// translates this into a task prompt that loads the same skill.
	SkillPrompt string
	// TaskStatus is the task's frontmatter status at dispatch time
	// ("refining", "planning", "plan-review", "implementing", "review",
	// "conflict", ...). Observability-only: the dsh-embed adapter forwards
	// it to the agent-server so the agent monitor can animate its NPC per
	// real task state. Empty when the session has no task document (pm,
	// design, grilling).
	TaskStatus string
	// ToolPolicy restricts the session's tool surface. Empty means the
	// adapter default; the audit/conventions review sessions carry
	// auditToolPolicy / conventionsToolPolicy (see below).
	ToolPolicy string
	// Timeout bounds one execution. Zero uses the adapter default.
	Timeout time.Duration
	// WorkingDir is the repo/worktree the session runs in.
	WorkingDir string
	// ExtraEnv is passed to the child process (task temp env, credentials).
	ExtraEnv []string
}

// auditToolPolicy and conventionsToolPolicy are the tool-surface whitelists for
// the read-only review sessions (independent completion audit / project
// conventions baseline review). They MUST include the harness's benign
// operating tools the agent uses as standard procedure — skill (load skill
// instructions), todo_write (agent scratch todo list, never the worktree),
// job_output/job_list/job_kill (spawn & poll the background test jobs it
// runs), read_image (inspect screenshots) — or the agent-server's post-hoc
// whitelist enforcement fails EVERY session with TOOL_POLICY_VIOLATION and the
// gate can never pass. This exact failure froze both review/merge automation
// and the conventions gate on 2026-08-31: TASK-081 stuck in review through 14
// consecutive audit failures (disallowed skill/todo_write/job_output), and
// TASK-080 blocked with CONVENTIONS_REVIEW_FAILED (disallowed
// skill/todo_write/write). The worktree-mutating tools that could plant
// evidence or alter code (edit / str_replace_editor) stay excluded from both.
//
// conventions additionally allows write: its skill contract's ONLY write is
// the review artifact Notes/PROJECT-CONVENTIONS.md (the one-shot gate marker),
// which must be produced with `write` — forbidding it made the gate
// unfinishable by design.
const (
	auditToolPolicy       = "read,grep,glob,bash,skill,todo_write,job_output,job_list,job_kill,read_image"
	conventionsToolPolicy = "read,grep,glob,bash,skill,todo_write,job_output,job_list,job_kill,read_image,write"
)

// TaskSnapshot is the minimal durable task state an executor needs beyond the
// spec: identity and the frontmatter path for write-back. The adapter itself
// performs the phase's frontmatter updates via the task file (same contract
// as today); the snapshot exists so Resume and result accounting can key on
// stable identity without re-parsing the file.
type TaskSnapshot struct {
	TaskID   string
	TaskPath string
	Project  string
	RepoDir  string
}

// ExecutionResult is the single, protocol-neutral outcome the daemon consumes.
// It replaces the practice of inferring success/failure from process exit
// codes plus log-text scanning (empty-stop, quota, key-unavailable).
type ExecutionResult struct {
	// Phase is the spec phase, echoed back for correlation.
	Phase string
	// Code is a stable machine-readable outcome.
	Code ExecOutcome
	// Error is the human-readable failure reason (empty on success).
	Error string
	// Stdout carries the process stdout (the DSH headless final assistant
	// message). Phases that parse a JSON output contract (priority) read this;
	// file-writing phases ignore it.
	Stdout string
	// LogPath is where the session's transcript was written.
	LogPath string
	// ResumeToken is a durable identity for re-attaching after restart
	// (empty when the adapter does not support resume).
	ResumeToken string
}

// ExecOutcome is the closed set of phase outcomes the daemon routes on.
type ExecOutcome string

const (
	OutcomeSuccess  ExecOutcome = "success"
	OutcomeFailed   ExecOutcome = "failed"
	OutcomeTimedOut ExecOutcome = "timeout"
	// OutcomeTimedOutActive reports a phase whose timeout window elapsed but
	// whose agent-server session shows RECENT activity (model still producing
	// steps/tool calls — e.g. a long real-smoke Round 2). The caller keeps
	// the durable token and re-waits on the next scan instead of cancelling
	// a working turn (TASK-065: 60m window hit while the session was
	// actively committing and running dev-up smoke).
	OutcomeTimedOutActive ExecOutcome = "timeout_active"
	OutcomeInterrupted    ExecOutcome = "interrupted" // daemon shutdown / cancel
	OutcomeQuotaExhausted ExecOutcome = "quota_exhausted"
	OutcomeKeyUnavailable ExecOutcome = "key_unavailable"
	OutcomeEmptyResponse  ExecOutcome = "empty_response"
)

// ExecutionHandle is the live handle returned by Start/Resume.
type ExecutionHandle interface {
	// Wait blocks until the execution settles and returns the result.
	// The ctx passed to Start controls cancellation.
	Wait() (*ExecutionResult, error)
	// PID returns the child process id (for the legacy PID-file adoption
	// path); 0 when the adapter does not expose one.
	PID() int
}

// phaseThinking maps a phase to its reasoning-effort budget.
// Reasoning effort by phase nature:
//   - priority / refining / design：评估/规格作者类，medium（spec 命名推断
//     类失误证明 low 不够，但每轮 high 太贵）
//   - audit / pm / merge / conventions：确定性为主，low
//   - planning：跨需求规划，max（plan 是全任务最高杠杆产物，被每个 AC
//     迭代消费；2026-09-02 从 high 上调——plan-review 人审拦方向性错误，
//     拦不住字段契约类细节（TASK-079 D5），而 plan 缺陷在 round2 逐 AC
//     引爆；2-3× token 只付一次，planning 是稀有阶段，性价比最高）
//   - round2：实现阶段，max（最复杂，需 deep reasoning 写代码）
//   - grilling 交互在 kitty-grill 单独分级（需求详细化 high、决策清单 low）
func phaseThinking(phase string) string {
	switch phase {
	case "priority", "refining", "design":
		// 规格作者与设计库修订：medium——低强度下的 spec 命名推断类失误
		// （TASK-079 D5 字段名 vs gate fixture）证明 low 不够，但 high 对
		// 每轮 refining 太贵。
		return "medium"
	case "round2", "planning":
		return "max"
	default:
		return "low"
	}
}

// mapExecOutcome maps a protocol-neutral ExecutionResult to the daemon's
// failure-code vocabulary (Phase 5 seam). Success yields an empty code and
// reason; every other outcome maps to a stable ErrorCode for handlePhaseFailure
// and a human-readable reason. dshExecutor currently distinguishes
// success/failed/timeout/interrupted only; quota/key/empty-response outcomes
// arrive once the DSH exit-code probe lands (docs/phase5-executor-migration.md
// §5.6), but the mapping is already closed over the full ExecOutcome set.
func mapExecOutcome(result *ExecutionResult) (ExecOutcome, ErrorCode, string) {
	if result == nil {
		return OutcomeFailed, ErrModelFailed, "executor returned no result"
	}
	switch result.Code {
	case OutcomeSuccess:
		return OutcomeSuccess, "", ""
	case OutcomeTimedOut:
		return OutcomeTimedOut, ErrPhaseTimeout, "phase timed out"
	case OutcomeTimedOutActive:
		return OutcomeTimedOutActive, ErrPhaseInterrupted, "phase session still active after timeout window (next scan resumes)"
	case OutcomeInterrupted:
		return OutcomeInterrupted, ErrPhaseInterrupted, "interrupted by daemon shutdown"
	case OutcomeQuotaExhausted:
		return OutcomeQuotaExhausted, ErrModelQuotaExhausted, "model quota exhausted"
	case OutcomeKeyUnavailable:
		return OutcomeKeyUnavailable, ErrAPIKeyUnavailable, "api key unavailable"
	case OutcomeEmptyResponse:
		return OutcomeEmptyResponse, ErrModelFailed, "empty model response"
	default:
		reason := result.Error
		if reason == "" {
			reason = "phase failed"
		}
		return OutcomeFailed, ErrModelFailed, reason
	}
}
