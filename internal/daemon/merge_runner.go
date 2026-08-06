package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ensureGitRemote adds the project's configured git_remote as the "origin"
// remote if it does not already exist. Returns an error only when origin is
// missing and no git_remote is configured in vault-map.json.
func ensureGitRemote(cfg *config.Config, repoDir, projectName string) error {
	if output, err := exec.Command("git", "-C", repoDir, "remote", "get-url", "origin").Output(); err == nil && len(output) > 0 {
		return nil
	}
	var remoteURL string
	for _, p := range cfg.Projects {
		if p.Name == projectName && p.GitRemote != "" {
			remoteURL = p.GitRemote
			break
		}
	}
	if remoteURL == "" {
		return fmt.Errorf("%s: no origin remote and no git_remote configured in vault-map for project %q", ErrGitHubUnavailable, projectName)
	}
	if !strings.Contains(remoteURL, "://") && !strings.Contains(remoteURL, "@") {
		remoteURL = fmt.Sprintf("https://%s", remoteURL)
	}
	if output, err := exec.Command("git", "-C", repoDir, "remote", "add", "origin", remoteURL).CombinedOutput(); err != nil {
		return fmt.Errorf("add origin remote: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *Runner) processMergeTask(candidate task.ReadyTask, repoDir string) error {
	// Concurrency guard: conflict auto-resolution can take minutes, so a
	// later scan round must not double-dispatch the same merge.
	if err := r.mergePIDFileGuard(candidate); err != nil {
		return err
	}

	data, err := os.ReadFile(candidate.FilePath)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return fmt.Errorf("parse task: %w", err)
	}
	// Early convergence BEFORE the REQ-hash gate: a PR that already merged
	// (legacy done tasks whose merge_status was never written back, or a
	// manual merge) finishes as done immediately. A merged PR needs no
	// re-planning no matter how stale the plan_req_hash is — sending it
	// through the hash gate would flip dozens of merged tasks into refining
	// (observed: 30+ done tasks re-refining after the done-reopen landed).
	prURL := fm.PRURL
	if prURL == "" && fm.TargetBranch != "" {
		prURL = findAnyPR(r.daemonCtx, repoDir, fm.TargetBranch)
	}
	if prURL != "" {
		state, err := prState(r.daemonCtx, repoDir, prURL)
		if err != nil {
			return err
		}
		if state == "MERGED" {
			r.logger.Printf("task %s: PR %s already merged, converging to done", candidate.ID, prURL)
			return r.completeMerge(candidate, prURL)
		}
	}

	reqPath := fm.ReqDoc
	if !filepath.IsAbs(reqPath) {
		reqPath = filepath.Join(r.cfg.ObsidianVault, reqPath)
	}
	if err := validateMergeAuthorization(mergeAuthorization{
		Status: fm.Status, MergeApproved: fm.MergeApproved, PendingReq: fm.PendingReq,
		ReqPath: reqPath, PlanReqHash: fm.PlanReqHash, TargetBranch: fm.TargetBranch,
	}); err != nil {
		updates := map[string]interface{}{
			"merge_approved":   false,
			"phase_error_code": string(ErrBaseCommitMismatch),
			"phase_error":      err.Error(),
		}
		if fm.Status == "done" {
			// done-reopen path: a stale hash means the requirement changed
			// AFTER the task was done — auto-refining would re-plan finished
			// work and pile up plan-review approvals. Keep done, surface the
			// conflict for a human decision (merge manually or re-plan).
			updates["phase_error"] = err.Error() + "; task was done — requirement changed after completion; merge manually or re-plan deliberately"
			_ = yamlfrontmatter.Update(candidate.FilePath, updates)
			notify.SendTaskAction(candidate.ID, candidate.Title, "⚠️", "已完成任务 PR 待人工合并",
				"REQ 在任务完成后变更，PR 未自动合入；请人工合并 PR 或显式重新规划", r.cfg.Notifications.Desktop)
			return err
		}
		if fm.PendingReq || strings.Contains(err.Error(), string(ErrBaseCommitMismatch)) {
			updates["status"] = "refining"
			updates["pending_req"] = true
		}
		_ = yamlfrontmatter.Update(candidate.FilePath, updates)
		return err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		_ = yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"status": "review", "merge_approved": false,
			"phase_error_code": string(ErrGitHubUnavailable), "phase_error": "gh CLI unavailable",
		})
		notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
		return fmt.Errorf("%s: gh CLI not found", ErrGitHubUnavailable)
	}

	if err := ensureGitRemote(r.cfg, repoDir, candidate.Project); err != nil {
		return err
	}
	fresh := false
	// Fail fast on a stalled network: git's default connect retry can burn
	// 2+ minutes per push when github.com is unreachable, stalling the merge
	// goroutine and every scan batch behind it. http.connectTimeout bounds
	// the TCP connect phase, http.lowSpeed* aborts once the transfer makes
	// no progress for 20s.
	pushCmd, pushCancel := mergeCommand(r.daemonCtx, repoDir, "git", "-C", repoDir,
		"-c", "http.connectTimeout=15",
		"-c", "http.lowSpeedLimit=1", "-c", "http.lowSpeedTime=20",
		"push", "-u", "origin", fm.TargetBranch)
	if output, err := pushCmd.CombinedOutput(); err != nil {
		pushCancel()
		return fmt.Errorf("push feature branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	pushCancel()
	if prURL == "" {
		// A PR may already exist on the remote even when frontmatter pr_url
		// is empty (created manually, or by an earlier run that crashed
		// before writing pr_url back): reusing it beats failing the whole
		// merge on "already exists" every scan.
		if existing := findExistingPR(r.daemonCtx, repoDir, fm.TargetBranch); existing != "" {
			r.logger.Printf("task %s: reusing existing PR %s for branch %s", candidate.ID, existing, fm.TargetBranch)
			prURL = existing
		}
	}
	if prURL == "" {
		createCmd, createCancel := mergeCommand(r.daemonCtx, repoDir, "gh", "pr", "create", "--head", fm.TargetBranch, "--fill")
		output, createErr := createCmd.CombinedOutput()
		createCancel()
		if createErr != nil {
			// gh refuses to create a duplicate PR; recover the existing one
			// (from the error output first, then a lookup) so the merge loop
			// continues instead of retrying creation forever.
			existing := prURLFromCreateError(string(output))
			if existing == "" {
				existing = findExistingPR(r.daemonCtx, repoDir, fm.TargetBranch)
			}
			if existing != "" {
				r.logger.Printf("task %s: PR for branch %s already exists, reusing %s", candidate.ID, fm.TargetBranch, existing)
				prURL = existing
			} else {
				return fmt.Errorf("%s: create PR: %w: %s", ErrGitHubUnavailable, createErr, strings.TrimSpace(string(output)))
			}
		} else {
			prURL = strings.TrimSpace(string(output))
			fresh = true
		}
	}
	if !fresh {
		// A PR that was not just created by this run (frontmatter pr_url or
		// recovered from the remote) may be closed, which would stall the
		// merge loop (gh pr merge refuses closed PRs) exactly like the
		// create-PR retry loop did. Reopen it before evaluating checks.
		if err := r.ensurePROpen(r.daemonCtx, repoDir, prURL, candidate); err != nil {
			return err
		}
	}
	approvedHead := gitCurrentHead(repoDir, fm.TargetBranch)
	if approvedHead == "" {
		return fmt.Errorf("%s: target branch head unavailable", ErrBaseCommitMismatch)
	}
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
		"pr_url": prURL, "merge_status": "checks-pending", "approved_head": approvedHead,
	}); err != nil {
		return err
	}

	// Automatic repair budget (conflicts + CI failures) is bounded per merge
	// authorization by merge_retry_count; re-approval by the user does not
	// silently re-spend the budget forever. The count is cleared only when
	// the merge succeeds or a fresh authorization starts (merge_status="").
	// CI polling budget: while checks are pending the merge waits in this
	// goroutine (30s per tick) instead of returning and depending on an
	// external watcher/timer scan to re-evaluate once CI settles. After the
	// budget the goroutine yields and the next scan re-evaluates.
	// Bounded (config merge_poll_wait_ticks × 30s, default 10min) so the
	// repo write lock held by this merge goroutine cannot stall same-repo
	// read-lock tasks for a full CI marathon.
	maxWaitTicks := r.cfg.MergePollWaitTicks
	waitTicks := 0
	for attempt := 0; ; attempt++ {
		checks, err := loadMergeChecks(r.daemonCtx, repoDir, prURL)
		if err != nil {
			return err
		}
		decision := evaluateMergeChecks(approvedHead, checks)
		switch decision.Action {
		case mergeActionWait:
			if waitTicks >= maxWaitTicks {
				return nil // budget exhausted; next scan re-evaluates
			}
			waitTicks++
			select {
			case <-r.daemonCtx.Done():
				return nil // shutdown; merge resumes after restart
			case <-time.After(30 * time.Second):
			}
			continue
		case mergeActionReview:
			// Only genuine check failures are auto-fixable. A changed approved
			// head (external push / stale approval) is a re-authorization
			// matter, not a code defect — hand it straight to the user.
			if fm.MergeRetryCount < r.cfg.MaxAutoMergeFixes && decision.ErrorCode == ErrValidationFailed {
				r.logger.Printf("task %s: CI checks failed (%s), starting AI fix (%d/%d)", candidate.ID, decision.Reason, fm.MergeRetryCount+1, r.cfg.MaxAutoMergeFixes)
				fm.MergeRetryCount++
				if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
					"merge_retry_count": fm.MergeRetryCount,
					"merge_status":      "auto-fix-ci",
				}); err != nil {
					return err
				}
				if err := r.resolveMergeChecksFailure(candidate, repoDir); err != nil {
					if errors.Is(err, errConflictResolutionInterrupted) {
						r.logger.Printf("task %s: CI fix interrupted by daemon shutdown, merge resumes after restart", candidate.ID)
						return nil
					}
					r.logger.Printf("task %s: CI auto-fix failed: %v", candidate.ID, err)
					updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
						"status": "review", "merge_approved": false, "merge_status": "",
						"phase_error_code": string(decision.ErrorCode),
						"phase_error":      decision.Reason + "; AI CI-fix failed: " + err.Error(),
					})
					notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
					notify.SendTaskAction(candidate.ID, candidate.Title, "❌", "合并被拒绝",
						decision.Reason+"；AI 修复失败，解决后重新设置 merge_approved=true 即可重试", r.cfg.Notifications.Desktop)
					return updateErr
				}
				// Fix committed locally: push and re-evaluate checks.
				fixPush, fixPushCancel := mergeCommand(r.daemonCtx, repoDir, "git", "-C", repoDir,
					"-c", "http.connectTimeout=15",
					"-c", "http.lowSpeedLimit=1", "-c", "http.lowSpeedTime=20",
					"push", "-u", "origin", fm.TargetBranch)
				output, pushErr := fixPush.CombinedOutput()
				fixPushCancel()
				if pushErr != nil {
					r.logger.Printf("task %s: push CI fix failed: %v", candidate.ID, pushErr)
					updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
						"status": "review", "merge_approved": false, "merge_status": "",
						"phase_error_code": string(decision.ErrorCode),
						"phase_error":      decision.Reason + "; push CI fix failed: " + strings.TrimSpace(string(output)),
					})
					notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
					return updateErr
				}
				approvedHead = gitCurrentHead(repoDir, fm.TargetBranch)
				if approvedHead == "" {
					return fmt.Errorf("%s: target branch head unavailable", ErrBaseCommitMismatch)
				}
				waitTicks = 0 // fresh head: give CI a full polling budget
				continue
			}
			updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "review", "merge_approved": false, "merge_status": "",
				"phase_error_code": string(decision.ErrorCode), "phase_error": decision.Reason,
			})
			notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
			notify.SendTaskAction(candidate.ID, candidate.Title, "❌", "合并被拒绝",
				decision.Reason+"；自动修复已达上限（"+fmt.Sprint(r.cfg.MaxAutoMergeFixes)+" 次），解决后重新设置 merge_approved=true 即可重试", r.cfg.Notifications.Desktop)
			return updateErr
		case mergeActionConflict:
			if fm.MergeRetryCount >= r.cfg.MaxAutoMergeFixes {
				// Auto-repair budget exhausted: hand back to the user. The
				// conflict-resolve-attempted marker tells the user AI repair
				// was tried; re-approval no longer re-spends the budget.
				updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
					"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
					"phase_error_code": string(ErrGitConflict), "phase_error": decision.Reason,
				})
				notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
				notify.SendTaskAction(candidate.ID, candidate.Title, "⚠️", "合并冲突待处理",
					decision.Reason+"；自动修复已达上限（"+fmt.Sprint(r.cfg.MaxAutoMergeFixes)+" 次），手动解决后重新设置 merge_approved=true 即可重试", r.cfg.Notifications.Desktop)
				return updateErr
			}
			r.logger.Printf("task %s: PR has merge conflicts, starting AI auto-resolution (%d/%d)", candidate.ID, fm.MergeRetryCount+1, r.cfg.MaxAutoMergeFixes)
			fm.MergeRetryCount++
			if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"merge_retry_count": fm.MergeRetryCount,
				"merge_status":      "auto-fix-conflict",
			}); err != nil {
				return err
			}
			if err := r.resolveMergeConflict(candidate, repoDir); err != nil {
				if errors.Is(err, errConflictResolutionInterrupted) {
					// Daemon shutdown aborted the AI session: keep review +
					// merge_approved so the merge resumes after restart. No
					// conflict write-back, no user notification.
					r.logger.Printf("task %s: conflict auto-resolution interrupted by daemon shutdown, merge resumes after restart", candidate.ID)
					return nil
				}
				r.logger.Printf("task %s: conflict auto-resolution failed: %v", candidate.ID, err)
				updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
					"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
					"phase_error_code": string(ErrGitConflict),
					"phase_error":      decision.Reason + "; AI auto-resolution failed: " + err.Error(),
				})
				notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
				return updateErr
			}
			// Resolution committed locally: push the new head and re-evaluate.
			resPush, resPushCancel := mergeCommand(r.daemonCtx, repoDir, "git", "-C", repoDir,
				"-c", "http.connectTimeout=15",
				"-c", "http.lowSpeedLimit=1", "-c", "http.lowSpeedTime=20",
				"push", "-u", "origin", fm.TargetBranch)
			output, pushErr := resPush.CombinedOutput()
			resPushCancel()
			if pushErr != nil {
				r.logger.Printf("task %s: push conflict resolution failed: %v", candidate.ID, pushErr)
				updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
					"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
					"phase_error_code": string(ErrGitConflict),
					"phase_error":      decision.Reason + "; push resolution failed: " + strings.TrimSpace(string(output)),
				})
				notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
				return updateErr
			}
			approvedHead = gitCurrentHead(repoDir, fm.TargetBranch)
			if approvedHead == "" {
				return fmt.Errorf("%s: target branch head unavailable", ErrBaseCommitMismatch)
			}
			if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"merge_status": "auto-fix-conflict", "approved_head": approvedHead,
			}); err != nil {
				return err
			}
			continue
		case mergeActionMerge:
			mergeCmd, mergeCancel := mergeCommand(r.daemonCtx, repoDir, "gh", "pr", "merge", prURL, "--merge", "--delete-branch")
			output, mergeErr := mergeCmd.CombinedOutput()
			mergeCancel()
			if mergeErr != nil {
				// --delete-branch can fail (e.g. the branch is checked out by
				// a task worktree) even though the PR itself merged. Treat an
				// already-merged PR as success so the task still reaches done.
				if prIsMerged(r.daemonCtx, repoDir, prURL) {
					r.logger.Printf("task %s: PR %s merged, local cleanup skipped: %v", candidate.ID, prURL, strings.TrimSpace(string(output)))
				} else {
					return fmt.Errorf("%s: merge PR: %w: %s", ErrGitHubUnavailable, mergeErr, strings.TrimSpace(string(output)))
				}
			}
			// Transition to done (shared with the already-merged convergence path).
			return r.completeMerge(candidate, prURL)
		default:
			return fmt.Errorf("%s: unknown merge decision", ErrInternal)
		}
	}
}

// completeMerge transitions the task to done after a successful merge — or
// when the PR is already merged remotely (manual merge, earlier run). Used by
// both the merge action and the early convergence path.
func (r *Runner) completeMerge(candidate task.ReadyTask, prURL string) error {
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
		"status": "done", "merge_approved": false, "pending_req": false,
		"merge_status": "merged", "completed": time.Now().Format(time.RFC3339),
		"phase_error_code": "", "phase_error": "", "merge_retry_count": 0,
	}); err != nil {
		return err
	}
	notify.SendTaskAction(candidate.ID, candidate.Title, "✅", "合并成功",
		fmt.Sprintf("PR %s 已合并，任务完成", prURL), r.cfg.Notifications.Desktop)
	// Step 0: Extract this task's knowledge to the knowledge base (non-blocking)
	go r.extractProjectKnowledge(candidate.Project, candidate.FilePath)
	return nil
}

// findExistingPR returns the URL of an open PR for the target branch, or ""
// when none exists or the lookup itself fails. The caller falls back to
// gh pr create, which reports the duplicate case explicitly, so a lookup
// failure is never misread as "no PR".
func findExistingPR(parent context.Context, repoDir, targetBranch string) string {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--head", targetBranch, "--state", "open", "--json", "url,state", "--jq", ".[0].url // empty")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// findAnyPR returns the URL of any PR (open, merged or closed) for the
// target branch, or "" when none exists. Used by the early-convergence path
// before the REQ-hash gate: a merged legacy PR must converge to done even
// though it is invisible to findExistingPR (open-only).
func findAnyPR(parent context.Context, repoDir, targetBranch string) string {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--head", targetBranch, "--state", "all", "--json", "url,state", "--jq", ".[0].url // empty")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// prState reports the GitHub state (OPEN/CLOSED/MERGED) of a PR.
func prState(parent context.Context, repoDir, prURL string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "state", "--jq", ".state")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: inspect PR state: %w: %s", ErrGitHubUnavailable, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// ensurePROpen reopens a PR when it is closed, so the merge loop can act on
// it instead of stalling on gh pr merge refusing closed PRs. Open and
// merged PRs pass through unchanged. Reopening overrides an explicit GitHub
// close, so the action is surfaced to the user via notification.
func (r *Runner) ensurePROpen(parent context.Context, repoDir, prURL string, candidate task.ReadyTask) error {
	state, err := prState(parent, repoDir, prURL)
	if err != nil {
		return err
	}
	switch state {
	case "OPEN", "MERGED":
		return nil
	case "CLOSED":
		reopenCmd, reopenCancel := mergeCommand(parent, repoDir, "gh", "pr", "reopen", prURL)
		output, reopenErr := reopenCmd.CombinedOutput()
		reopenCancel()
		if reopenErr != nil {
			return fmt.Errorf("%s: reopen PR: %w: %s", ErrGitHubUnavailable, reopenErr, strings.TrimSpace(string(output)))
		}
		r.logger.Printf("task %s: reopened PR %s (state was CLOSED)", candidate.ID, prURL)
		notify.SendTaskAction(candidate.ID, candidate.Title, "🔓", "PR 已重新打开",
			fmt.Sprintf("PR %s 处于关闭状态，为继续合并已自动重新打开", prURL), r.cfg.Notifications.Desktop)
		return nil
	default:
		return fmt.Errorf("%s: unexpected PR state %q", ErrGitHubUnavailable, state)
	}
}

// prURLCreateErrorPattern matches a PR URL inside gh pr create's
// "already exists" error message, which is the recovery path when the PR is
// not open (e.g. closed) and therefore invisible to findExistingPR.
var prURLCreateErrorPattern = regexp.MustCompile(`https?://\S+/pulls?/\d+`)

// prURLFromCreateError extracts a PR URL from gh pr create failure output,
// or "" when the error does not mention an existing PR.
func prURLFromCreateError(output string) string {
	if match := prURLCreateErrorPattern.FindString(output); match != "" {
		return strings.TrimRight(match, ".,)")
	}
	return ""
}

// prIsMerged reports whether the PR has been merged on the remote, used to
// distinguish "merge succeeded but local --delete-branch cleanup failed" from
// a genuine merge failure.
func prIsMerged(parent context.Context, repoDir, prURL string) bool {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "state")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return false
	}
	return payload.State == "MERGED"
}

// mergeCommand builds a context-bounded exec.Cmd for the merge flow's remote
// git/gh operations: a hung network cannot stall the merge goroutine forever,
// and daemon shutdown interrupts the command instead of orphaning it. 60s is
// a generous ceiling for normal pushes (seconds) and gh API calls; the git
// push path additionally fails fast via http.connectTimeout/lowSpeed when a
// proxy sits between git and github and masks the connect phase. The caller
// must invoke the returned cancel function after the command finishes.
func mergeCommand(parent context.Context, dir, name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd, cancel
}

// isMergeRetryable reports whether a merge failure is environmental — a
// transient network or GitHub API error worth retrying with a short backoff —
// as opposed to a hard failure that already revoked the merge authorization
// (validation, CI rejection, head change, missing gh CLI) or conflicts with
// a concurrent run, where retrying would be useless or harmful.
func isMergeRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"gh CLI not found",
		"precondition:",
		string(ErrBaseCommitMismatch),
		string(ErrReqMissing),
		string(ErrGitConflict),
		string(ErrInternal),
		"merge already in progress",
		"decode PR checks",
		"unknown merge decision",
		"unexpected PR state",
	} {
		if strings.Contains(msg, marker) {
			return false
		}
	}
	// ErrGitHubUnavailable is an ErrorCode string constant, not an error
	// value, so match its rendered prefix instead of errors.Is.
	return strings.Contains(msg, string(ErrGitHubUnavailable)) ||
		strings.Contains(msg, "push feature branch")
}

// Merge retry parameters are package-level so tests can shorten the backoff
// without waiting real time.
var (
	mergeRetryBackoff    = 2 * time.Minute
	mergeMaxRetries      = 5
)

// processMergeTaskWithRetry runs processMergeTask and retries environmental
// failures (transient network / GitHub API errors) with a short backoff
// instead of waiting for the next scan batch, which can be stalled behind a
// long Round 2 session. Hard failures return immediately: they already wrote
// merge_approved=false and require a human decision. The retry loop is
// daemonCtx-aware so shutdown interrupts the backoff; each attempt re-reads
// the task frontmatter, so an external revocation is caught by
// validateMergeAuthorization inside processMergeTask.
func (r *Runner) processMergeTaskWithRetry(candidate task.ReadyTask, repoDir string) error {
	var lastErr error
	for attempt := 0; attempt <= mergeMaxRetries; attempt++ {
		if attempt > 0 {
			r.logger.Printf("task %s: merge retry %d/%d in %v (previous failure: %v)",
				candidate.ID, attempt, mergeMaxRetries, mergeRetryBackoff, lastErr)
			select {
			case <-r.daemonCtx.Done():
				return fmt.Errorf("merge retry interrupted by daemon shutdown: %w", lastErr)
			case <-time.After(mergeRetryBackoff):
			}
		}
		lastErr = r.processMergeTask(candidate, repoDir)
		if lastErr == nil || !isMergeRetryable(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// mergePIDFileGuard serializes merge runs per task. Returns nil when no
// other merge run is active for this task, after claiming the pid file.
func (r *Runner) mergePIDFileGuard(candidate task.ReadyTask) error {
	logDir := r.cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".omp", "logs")
	}
	taskLogDir := filepath.Join(logDir, "tasks")
	if err := os.MkdirAll(taskLogDir, 0o700); err != nil {
		return fmt.Errorf("create task log directory: %w", err)
	}
	pidFile := taskPIDFile(taskLogDir, candidate.ID, candidate.FilePath)
	if data, err := os.ReadFile(pidFile); err == nil {
		var existingPID int
		if _, scanErr := fmt.Sscanf(string(data), "%d", &existingPID); scanErr == nil && procAlive(existingPID) {
			return fmt.Errorf("merge already in progress (PID %d), skipping duplicate dispatch", existingPID)
		}
	}
	if err := os.WriteFile(pidFile, []byte(formatPIDRecord(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("write merge pid file: %w", err)
	}
	// Claim lives until the merge run (including conflict auto-resolution)
	// completes, then releases so the next scan can dispatch again.
	defer func() {
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			r.logger.Printf("task %s: remove merge pid file: %v", candidate.ID, err)
		}
	}()
	return nil
}

// errConflictResolutionInterrupted marks an AI conflict-resolution session
// aborted by daemon shutdown. Unlike a resolution failure, it must NOT
// downgrade the task to conflict: the merge stays authorized and resumes on
// the next scan after restart, mirroring runPhase's PHASE_INTERRUPTED
// semantics (interrupted phases are not treated as failures).
var errConflictResolutionInterrupted = errors.New("conflict resolution interrupted by daemon shutdown")

// mergeFixMode selects what the merge skill session should repair.
type mergeFixMode string

const (
	mergeFixConflicts mergeFixMode = "conflicts"
	mergeFixCI        mergeFixMode = "ci-fix"
)

// resolveMergeConflict runs one OMP session that loads the merge skill's
// Automated Conflict Resolution step (skill://resolving-merge-conflicts) to
// resolve PR conflicts locally. The AI may commit a resolution; the daemon
// pushes and re-evaluates checks afterwards. The session is local-only —
// push/PR/merge stay with the daemon.
func (r *Runner) resolveMergeConflict(candidate task.ReadyTask, repoDir string) error {
	return r.runMergeAISession(candidate, repoDir, mergeFixConflicts)
}

// resolveMergeChecksFailure runs one OMP session that loads the merge skill's
// CI-fix step: diagnose failed checks, fix the underlying tests/code in the
// feature branch, commit locally. The daemon pushes and re-evaluates. Local
// only, like conflict resolution.
func (r *Runner) resolveMergeChecksFailure(candidate task.ReadyTask, repoDir string) error {
	return r.runMergeAISession(candidate, repoDir, mergeFixCI)
}

// runMergeAISession is the shared executor for merge-skill repair sessions
// (conflict resolution or CI-fix). mode selects the skill step via prompt
// suffix; the session never touches the remote.
func (r *Runner) runMergeAISession(candidate task.ReadyTask, repoDir string, mode mergeFixMode) error {
	model := r.selectModel(candidate.Assignee)
	skillPrompt := fmt.Sprintf("/obsidian-task-runner-merge %s %s", candidate.FilePath, mode)
	args := []string{"--model", model, "--auto-approve", "-p", skillPrompt, "--thinking", "high"}

	logDir := r.cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".omp", "logs")
	}
	taskLogDir := filepath.Join(logDir, "tasks")
	if err := os.MkdirAll(taskLogDir, 0o700); err != nil {
		return fmt.Errorf("create task log directory: %w", err)
	}
	logSuffix := "merge-conflict"
	if mode == mergeFixCI {
		logSuffix = "merge-ci-fix"
	}
	logPath := filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s-%s-%s.log",
		candidate.ID, taskRunKey(candidate.FilePath), time.Now().Format("20060102-150405"), logSuffix))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create merge fix log: %w", err)
	}
	defer f.Close()
	header := fmt.Sprintf("# TASK-%s %s\n# model=%s phase=%s time=%s\n\n",
		candidate.ID, candidate.Title, model, logSuffix, time.Now().Format(time.RFC3339))
	if _, err := f.WriteString(header); err != nil {
		return fmt.Errorf("write merge fix log header: %w", err)
	}

	timeout := r.cfg.PhaseTimeout("merge")
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.daemonCtx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.cfg.OMPCmd, args...)
	// Graceful shutdown: SIGTERM so omp can persist its session, hard-kill
	// after WaitDelay if it does not exit.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = repoDir
	cmd.Stdout = io.MultiWriter(f, os.Stderr)
	cmd.Stderr = io.MultiWriter(f, os.Stderr)

	r.logger.Printf("task %s: merge fix session %s via OMP (model=%s, timeout=%v, log=%s)", candidate.ID, mode, model, timeout, logPath)
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return errConflictResolutionInterrupted
		}
		// Session killed by an external SIGTERM (operator kill of a session
		// that outlived a --once parent) surfaces as exit 143 or
		// signal-terminated. With no timeout/cancel pending, treat it like an
		// interrupted attempt: keep the merge authorized so it resumes on the
		// next scan instead of burning the one-shot AI resolution budget.
		// Timeout exits are excluded (ctx.Err() != nil) — those are genuine
		// resolution failures handed back to the user.
		if ctx.Err() == nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if code := exitErr.ExitCode(); code == 143 || code == -1 {
					return errConflictResolutionInterrupted
				}
			}
		}
		return fmt.Errorf("merge fix session %s failed: %w", mode, err)
	}
	return nil
}

func gitCurrentHead(repoDir, branch string) string {
	output, err := exec.Command("git", "-C", repoDir, "rev-parse", branch).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func loadMergeChecks(parent context.Context, repoDir, prURL string) (mergeChecks, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "headRefOid,mergeStateStatus,statusCheckRollup,url")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return mergeChecks{}, fmt.Errorf("%s: inspect PR checks: %w: %s", ErrGitHubUnavailable, err, strings.TrimSpace(string(output)))
	}
	var payload struct {
		HeadRefOID        string `json:"headRefOid"`
		MergeStateStatus  string `json:"mergeStateStatus"`
		URL               string `json:"url"`
		StatusCheckRollup []struct {
			Type       string `json:"__typename"`
			State      string `json:"state"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return mergeChecks{}, fmt.Errorf("decode PR checks: %w", err)
	}
	state := "SUCCESS"
	if strings.EqualFold(payload.MergeStateStatus, "DIRTY") {
		state = "CONFLICTING"
	}
	for _, check := range payload.StatusCheckRollup {
		checkState := check.State
		if check.Type == "CheckRun" {
			// CheckRun entries carry status + conclusion, not state:
			// incomplete runs are pending, completed ones resolve to their
			// conclusion. Treating them as "" would mark every CheckRun
			// pending forever.
			if check.Status != "COMPLETED" {
				checkState = "PENDING"
			} else {
				checkState = check.Conclusion
			}
		}
		switch checkState {
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
			state = "FAILURE"
			return mergeChecks{HeadOID: payload.HeadRefOID, State: state, URL: payload.URL}, nil
		case "SUCCESS", "NEUTRAL", "SKIPPED", "STALE":
			// completed or non-blocking: keep current state. STALE means the
			// run was superseded (e.g. by a head push); GitHub schedules a
			// fresh run, and head movement is caught separately by the
			// approvedHead comparison in evaluateMergeChecks.
		default:
			state = "PENDING"
		}
	}
	return mergeChecks{HeadOID: payload.HeadRefOID, State: state, URL: payload.URL}, nil
}

// taskKnowledgeRefs returns the task's knowledge_refs (relative References/
// paths) with the References/ prefix and slashes normalized.
func taskKnowledgeRefs(taskPath string) ([]string, error) {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return nil, err
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return nil, fmt.Errorf("parse task frontmatter")
	}
	refs := make([]string, 0, len(fm.KnowledgeRefs))
	for _, ref := range fm.KnowledgeRefs {
		clean := strings.TrimPrefix(strings.TrimSpace(ref), "References/")
		clean = strings.TrimPrefix(clean, "/")
		if clean != "" {
			refs = append(refs, clean)
		}
	}
	return refs, nil
}

// measureKnowledgeApplied counts how many of the task's knowledge_refs
// (planned by Round 1) exist in References/ at delivery time, records
// "hit/total" in the task frontmatter (knowledge_applied), and logs the
// result — the lifecycle's knowledge-application metric.
func (r *Runner) measureKnowledgeApplied(taskPath, vaultDir string) error {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return err
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return fmt.Errorf("parse task: %w", err)
	}
	if len(fm.KnowledgeRefs) == 0 {
		return nil
	}
	refsDir := filepath.Join(vaultDir, "References")
	hits := 0
	var missing []string
	for _, ref := range fm.KnowledgeRefs {
		clean := strings.TrimPrefix(strings.TrimSpace(ref), "References/")
		clean = strings.TrimPrefix(clean, "/")
		if clean == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(refsDir, clean)); err == nil {
			hits++
		} else {
			missing = append(missing, clean)
		}
	}
	applied := fmt.Sprintf("%d/%d", hits, len(fm.KnowledgeRefs))
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"knowledge_applied": applied}); err != nil {
		return err
	}
	if len(missing) > 0 {
		r.logger.Printf("knowledge-base applied: %s (missing: %s)", applied, strings.Join(missing, ", "))
	} else {
		r.logger.Printf("knowledge-base applied: %s (all refs found)", applied)
	}
	return nil
}

func (r *Runner) extractProjectKnowledge(projectName, taskPath string) {
	vaultDir := r.cfg.ObsidianVault

	result, err := knowledge.ExtractTaskKnowledge(vaultDir, projectName, taskPath)
	if err != nil {
		r.logger.Printf("knowledge-base Step 0 failed: project=%s task=%s error=%v", projectName, filepath.Base(taskPath), err)
		return
	}
	if result.ADRCount > 0 || result.NewRefs > 0 || result.UpdatedRefs > 0 {
		r.logger.Printf("knowledge-base extracted: project=%s adrs=%d new=%d updated=%d",
			projectName, result.ADRCount, result.NewRefs, result.UpdatedRefs)
	}
	if len(result.Unclassified) > 0 {
		r.logger.Printf("knowledge-base archived: %d ADRs under References/uncategorized/", len(result.Unclassified))
	}
	// Archived knowledge re-classifies automatically as the vocabulary grows.
	if moved, rerr := knowledge.ReclassifyUncategorized(vaultDir); rerr != nil {
		r.logger.Printf("knowledge-base reclassify failed: %v", rerr)
	} else if moved > 0 {
		r.logger.Printf("knowledge-base reclassified: %d archived docs moved to topics", moved)
	}
	// Measure knowledge application: Round 1's planned knowledge_refs are
	// checked against the knowledge base at delivery time, recorded as
	// "hit/total" on the task (knowledge_applied) and logged.
	if err := r.measureKnowledgeApplied(taskPath, vaultDir); err != nil {
		r.logger.Printf("knowledge-base applied measure failed: %v", err)
	}
	// Record the delivery on every referenced knowledge document: a merged
	// task is applied-and-verified by definition, so append the application
	// line automatically (idempotent per project+date).
	if refs, readErr := taskKnowledgeRefs(taskPath); readErr == nil {
		if added, recErr := knowledge.AppendApplicationRecord(vaultDir, projectName, refs); recErr != nil {
			r.logger.Printf("knowledge-base application record failed: %v", recErr)
		} else if added > 0 {
			r.logger.Printf("knowledge-base application recorded: %d docs for %s", added, projectName)
		}
	} else {
		r.logger.Printf("knowledge-base read refs for record failed: %v", readErr)
	}
	// Delivered project experience grows the scaffold registry: classified
	// topics without a matching capability become new capabilities.
	if len(result.Topics) > 0 {
		mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
		if err := project.RegisterScaffoldFromProject(mapFile, projectName, result.Topics); err != nil {
			r.logger.Printf("knowledge-base scaffold registry update failed: %v", err)
		} else {
			r.logger.Printf("knowledge-base scaffold registry: %d topics reviewed for %s", len(result.Topics), projectName)
		}
	}
	// The delivery passed verification and merged — the extracted ADR decisions
	// are now validated by real practice. Flip verified on touched files so the
	// knowledge base distinguishes proven content from untested references.
	if len(result.Touched) > 0 {
		if err := knowledge.MarkVerified(result.Touched); err != nil {
			r.logger.Printf("knowledge-base verified flip failed: %v", err)
		} else {
			r.logger.Printf("knowledge-base verified: %d files", len(result.Touched))
		}
	}
	if _, idxErr := knowledge.RebuildINDEX(vaultDir); idxErr != nil {
		r.logger.Printf("knowledge-base INDEX rebuild failed: %v", idxErr)
	}
}
