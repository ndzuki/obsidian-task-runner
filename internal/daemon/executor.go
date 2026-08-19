package daemon

import (
	"context"
	"time"
)

// PhaseExecutor is the execution seam between the daemon's deterministic
// control plane and an external agent process (OMP today, DSH after
// migration). Every phase dispatch (refining/planning/round2/merge/priority/
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
	// using the durable identity recorded in the task frontmatter. Adapters
	// that cannot resume return ErrResumeUnsupported.
	Resume(ctx context.Context, resumeToken string) (ExecutionHandle, error)
	// Name returns the adapter identity (e.g. "omp", "dsh").
	Name() string
}

// ErrResumeUnsupported is returned by adapters that cannot re-attach to an
// in-flight execution (the OMP adapter historically used PID files; the DSH
// adapter will use durable session ids).
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
	// assignee (OMP form: "gateway/gpt-5.4-mini"). The DSH adapter will
	// translate this into its own route form.
	Model string
	// ReasoningEffort mirrors the OMP --thinking flag ("off"/"low"/"high"/
	// "max"). The DSH adapter maps it to its own effort enum.
	ReasoningEffort string
	// SkillPrompt is the slash-skill prompt OMP executes ("/obsidian-task-
	// runner-round2 <task> ..."). The DSH adapter translates this into a
	// task prompt that loads the same skill.
	SkillPrompt string
	// ToolPolicy restricts the session's tool surface. Empty means the
	// adapter default; the audit phase uses "read,grep,bash".
	ToolPolicy string
	// Timeout bounds one execution. Zero uses the adapter default.
	Timeout time.Duration
	// WorkingDir is the repo/worktree the session runs in.
	WorkingDir string
	// ExtraEnv is passed to the child process (task temp env, credentials).
	ExtraEnv []string
}

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
// It replaces the current practice of inferring success/failure from OMP exit
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
	OutcomeSuccess        ExecOutcome = "success"
	OutcomeFailed         ExecOutcome = "failed"
	OutcomeTimedOut       ExecOutcome = "timeout"
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

// ompPhaseThinking mirrors the daemon's current phase→--thinking mapping.
func ompPhaseThinking(phase string) string {
	switch phase {
	case "priority":
		return "off"
	case "round2":
		return "max"
	case "planning":
		return "high"
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
