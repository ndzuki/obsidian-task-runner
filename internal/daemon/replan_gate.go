package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// replanGateRequired returns true when the task has accumulated enough
// single-task replans that the next planning attempt must first revise the
// project's global Design library. A completed design revision covers the
// triggering plan version and suppresses duplicate design sessions on scans.
func replanGateRequired(t task.ReadyTask, threshold int) bool {
	return threshold > 0 && t.PlanVersion >= threshold && t.DesignReplanVersion < t.PlanVersion
}

// designGateErrorCode classifies a global-design failure for
// handlePhaseFailure. DESIGN_TARGET_UNWRITABLE is a deterministic environment
// defect (the vault Design directory cannot be written) that no retry can
// converge — it must never fall into the transient DESIGN_SESSION_FAILED
// bucket that the aged auto-resume re-arms after 24h. A daemon shutdown /
// context cancel that interrupts the design session mid-flight is a transient
// interruption (ErrPhaseInterrupted, in the auto-resume whitelist) — mapping
// it to DESIGN_SESSION_FAILED would leave the task blocked for the 24h aged
// window after every daemon restart (2026-08-25 TASK-065: user restarted the
// daemon, the in-flight replan-gate design session died with context canceled,
// and the task sat blocked awaiting the aged window). Everything else keeps
// the generic design-session code.
func designGateErrorCode(err error) ErrorCode {
	if errors.Is(err, errDesignTargetUnwritable) {
		return ErrDesignTargetUnwritable
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrPhaseInterrupted
	}
	return ErrDesignSessionFailed
}

// runReplanGate performs the design-library escalation for a planning task.
// It returns handled=true when the caller must end this scan iteration (both
// success and failure), preventing an ordinary planning session from running
// against a stale global design.
func (r *Runner) runReplanGate(ctx context.Context, t task.ReadyTask, repoDir string) (handled bool, err error) {
	if !replanGateRequired(t, r.cfg.ReplanGateThreshold) {
		return false, nil
	}
	if err := r.runGlobalDesignSession(ctx, t.Project, t.ID, t.FilePath, t.ReqDoc, repoDir); err != nil {
		return true, fmt.Errorf("replan gate for TASK-%s (plan v%d): %w", t.ID, t.PlanVersion, err)
	}
	if err := yamlfrontmatter.Update(t.FilePath, map[string]interface{}{
		"design_replan_version": t.PlanVersion,
	}); err != nil {
		return true, fmt.Errorf("record design replan version for TASK-%s: %w", t.ID, err)
	}
	return true, nil
}
