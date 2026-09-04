package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// autoResumeAgedAfter is the fallback window for blocked tasks carrying a
// transient, auto-recoverable phase error: if such a task is still blocked
// and neither a human nor the dependency resolver has approved a resume for
// this long, the daemon re-arms it on its own. This is the safety net for
// daemon restarts/iterations that lose the in-memory recovery state (模式 7
// in core/daemon-stuck-task-patterns.md) and for leaf tasks with no
// downstream to unwind the blocked_by chain (TASK-015/065: blocks=[]).
// Bounded by the same auto_resume_count budget as the dependency resolver,
// so a persistently failing task degrades to manual resume instead of
// looping hot.
// autoResumeAgedDefaultAfter is the fallback window when the configurable
// window (cfg.AutoResumeAgedAfterHours) is unset or non-positive.
const autoResumeAgedDefaultAfter = 24 * time.Hour

// agedAutoResumeCode reports whether a blocked task's error code qualifies
// for age-based auto-resume. It reuses the transient-error whitelist
// (isAutoResumableError) plus DESIGN_SESSION_FAILED, whose observed root
// cause is transient model availability (gateway 503 mid-session) and
// whose retry is equally cheap and bounded. Entry gates
// (PREREQUISITE_SMOKE_FAILED) and human-decision blocks (REQ_MISSING,
// DOCUMENT_INVALID, API_KEY_UNAVAILABLE, …) are deliberately excluded: they
// carry their own fact/probe-based recovery and must never be reopened on
// age alone.
func agedAutoResumeCode(code string) bool {
	return isAutoResumableError(code) || code == string(ErrDesignSessionFailed)
}

// autoResumeAgedWindow returns the configurable age window
// (cfg.AutoResumeAgedAfterHours); non-positive config falls back to 24h.
func (r *Runner) autoResumeAgedWindow() time.Duration {
	if r.cfg == nil || r.cfg.AutoResumeAgedAfterHours <= 0 {
		return autoResumeAgedDefaultAfter
	}
	return time.Duration(r.cfg.AutoResumeAgedAfterHours) * time.Hour
}

// autoResumeAgedBlocks re-arms blocked tasks whose transient error outlived
// the fallback window. It only writes resume_approved=true +
// auto_resume_pending=true; the existing resume path (restoreBlockedPhase in
// the dispatch loop) performs the actual restore and dispatch on the same
// scan. The block time is read from blocked_at (stamped by
// handlePhaseFailure); legacy tasks without the stamp fall back to the
// updated timestamp so long-blocked tasks recover without a fresh 24h wait.
// Note: DESIGN_SESSION_FAILED blocks go through recoveryBlock and therefore
// never consume the auto_resume_count budget — the 24h window itself is the
// rate limiter there (one design session per window at most).
func (r *Runner) autoResumeAgedBlocks() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				continue
			}
			if fm.Status != "blocked" || fm.BlockedPhase == "" || fm.ResumeApproved {
				continue
			}
			if !agedAutoResumeCode(fm.PhaseErrorCode) {
				continue
			}
			if fm.AutoResumeCount >= maxAutoResumeAttempts {
				if _, seen := r.agedSkipLogged.LoadOrStore(path, true); !seen {
					r.logger.Printf("task %s: aged auto-resume skipped: auto-resume budget exhausted (%d), manual resume required", fm.ID, fm.AutoResumeCount)
				}
				continue
			}
			base := fm.BlockedAt
			if base == "" {
				base = fm.Updated
			}
			blockedAt, err := time.Parse(time.RFC3339, base)
			if err != nil {
				continue // unparseable timestamp: never guess
			}
			if time.Since(blockedAt) < r.autoResumeAgedWindow() {
				continue
			}
			if err := yamlfrontmatter.Update(path, map[string]interface{}{
				"resume_approved":     true,
				"auto_resume_pending": true,
			}); err != nil {
				r.logger.Printf("task %s: aged auto-resume failed: %v", fm.ID, err)
				continue
			}
			r.logger.Printf("task %s: aged auto-resume after %v blocked (code=%s, base=%s)", fm.ID, time.Since(blockedAt).Round(time.Minute), fm.PhaseErrorCode, base)
		}
	}
}
