package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ensureGitRemote adds the project's configured git_remote as the "origin"
// remote if it does not already exist. Returns an error only when origin is
// missing and no git_remote is configured in vault-map.json.
//
// Guard: when origin already exists but points at a DIFFERENT repository than
// the project's configured git_remote, the merge refuses to proceed. This is
// the vault-fallback failure mode — a project registered without a standalone
// checkout resolves to its Vault directory, whose enclosing repository (e.g.
// the Vault's own backup repo) silently becomes the push/PR/merge target.
// Pushing there would merge project deliverables into the wrong repository.
//
// errMergeTargetMismatch marks that permanent configuration defect: hard-fail,
// never retried (matched via errors.Is, not string scanning).
var errMergeTargetMismatch = errors.New("merge target mismatch")

func ensureGitRemote(cfg *config.Config, repoDir, projectName string) error {
	if output, err := exec.Command("git", "-C", repoDir, "remote", "get-url", "origin").Output(); err == nil && len(output) > 0 {
		originURL := strings.TrimSpace(string(output))
		for _, p := range cfg.Projects {
			if p.Name == projectName && p.GitRemote != "" && !sameGitRepo(originURL, p.GitRemote) {
				return fmt.Errorf("%w: origin %q does not match git_remote %q configured in vault-map for project %q — refusing to push to the wrong repository (project resolved to a Vault fallback directory instead of a standalone checkout)", errMergeTargetMismatch, originURL, p.GitRemote, projectName)
			}
		}
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

// sameGitRepo reports whether two remote URLs reference the same repository,
// normalizing transport spellings (https://, git@…:, ssh://git@…/,
// scp-style), trailing slashes, ".git" suffixes and case:
// "git@github.com:ndzuki/demo.git" and "https://github.com/NDZUKI/demo"
// both normalize to "github.com/ndzuki/demo".
func sameGitRepo(a, b string) bool {
	return normalizeGitRepo(a) == normalizeGitRepo(b)
}

// normalizeGitRepo canonicalizes a git remote URL to "host/owner/repo"
// (lowercased). Shared by the merge-target guard (sameGitRepo) and the
// checkout promotion (gh owner/repo derivation).
func normalizeGitRepo(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	// scp-style: git@github.com:owner/repo — ":" separates host from path
	// only in scheme-less URLs where a "/" and no port follows it
	// (user@host:path). Port spellings (host:22/path, ssh://…:22/…) are
	// skipped here and lose the port below.
	hasScheme := strings.Contains(u, "://")
	if i := strings.LastIndex(u, ":"); i > 0 && !hasScheme && strings.Contains(u[i:], "/") && !isDigit(u[i+1:]) {
		u = u[:i] + "/" + u[i+1:]
	}
	if hasScheme {
		u = u[strings.Index(u, "://")+3:]
	}
	u = strings.TrimPrefix(u, "git@")
	u = strings.TrimPrefix(u, "ssh://")
	u = strings.TrimPrefix(u, "git://")
	if i := strings.Index(u, "/"); i > 0 {
		host := strings.ToLower(u[:i])
		if j := strings.LastIndex(host, ":"); j > 0 {
			host = host[:j] // drop ":port"
		}
		return host + "/" + strings.ToLower(u[i+1:])
	}
	return strings.ToLower(u)
}

func isDigit(s string) bool {
	if s == "" {
		return false
	}
	return s[0] >= '0' && s[0] <= '9'
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
	// Delivery mode: merge_mode=manual (team projects) stops at branch push;
	// the human merges through the forge UI. merge_mode=fork-merge (fork
	// development) merges the feature branch into the fork's default branch
	// locally and pushes it — the human then sends the team project a PR
	// from the fork. Both skip all gh-based remote operations.
	mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
	manualMerge := projectMergeMode(mapFile, candidate.Project) == "manual"
	forkMerge := projectMergeMode(mapFile, candidate.Project) == "fork-merge"
	// Early convergence BEFORE the REQ-hash gate: a PR that already merged
	// (legacy done tasks whose merge_status was never written back, or a
	// manual merge) finishes as done immediately. A merged PR needs no
	// re-planning no matter how stale the plan_req_hash is — sending it
	// through the hash gate would flip dozens of merged tasks into refining
	// (observed: 30+ done tasks re-refining after the done-reopen landed).
	// Manual/fork-merge projects have no PR: manual's remote-merge probe
	// (checkRemoteMergedAndComplete) owns completion; fork-merge completes
	// on the local merge into the fork default branch.
	prURL := fm.PRURL
	if prURL == "" && fm.TargetBranch != "" && !manualMerge && !forkMerge {
		prURL = findAnyPR(r.daemonCtx, repoDir, fm.TargetBranch)
	}
	if prURL != "" && !manualMerge && !forkMerge {
		state, err := prState(r.daemonCtx, repoDir, prURL)
		if err != nil {
			return err
		}
		if state == "MERGED" {
			// A merged PR converges to done only when it delivered THIS task's
			// checkpoint. A same-branch legacy PR merged in an earlier
			// generation carries old commits: converging would freeze the new
			// increment behind a fake done, and detectStaleDoneReopens reopens
			// it on the next scan — a done→refining loop (TASK-069: PR #46
			// merged 2026-07-20 for v1, the v16 checkpoint was never delivered;
			// observed loop 08-18 17:10 converge → 17:18 reopen). A stale PR
			// falls through to the normal merge path, which creates a fresh PR
			// for the current branch head.
			if fm.CheckpointCommit != "" && !prDeliversCheckpoint(r.daemonCtx, repoDir, prURL, fm.CheckpointCommit) {
				r.logger.Printf("task %s: PR %s merged but predates checkpoint %s, continuing to normal merge", candidate.ID, prURL, fm.CheckpointCommit)
				prURL = ""
			} else {
				r.logger.Printf("task %s: PR %s already merged, converging to done", candidate.ID, prURL)
				return r.completeMerge(candidate, repoDir, prURL)
			}
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
	if !manualMerge && !forkMerge {
		if _, err := exec.LookPath("gh"); err != nil {
			_ = yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "review", "merge_approved": false,
				"phase_error_code": string(ErrGitHubUnavailable), "phase_error": "gh CLI unavailable",
			})
			notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
			return fmt.Errorf("%s: gh CLI not found", ErrGitHubUnavailable)
		}
		// gh 存在但可能未登录：push 走 gh credential helper，PR create/merge
		// 也直接用 gh——未登录会阻塞全部远程步骤。先本地预检并把精确补救
		// 指引（`gh auth login`）交给用户，而不是烧光重试预算（TASK-004：
		// 5/5 次 push 重试全部失败 "could not read Username"）。预检是本地
		// 操作（读 gh config/keyring，无网络）；写回撤销授权，daemon 扫描
		// 保持安静，直到用户登录后重新批准。
		if err := checkGHAuth(r.daemonCtx); err != nil {
			_ = yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "review", "merge_approved": false,
				"phase_error_code": string(ErrGitHubUnavailable), "phase_error": err.Error(),
			})
			notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
			notify.SendTaskAction(candidate.ID, candidate.Title, "🔑", "GitHub CLI 未登录",
				"Merge 需要 GitHub 认证：请运行 `gh auth login` 完成登录，然后重新设置 merge_approved=true", r.cfg.Notifications.Desktop)
			return fmt.Errorf("%s: %v", ErrGitHubUnavailable, err)
		}
	}

	if err := ensureGitRemote(r.cfg, repoDir, candidate.Project); err != nil {
		// A merge target mismatch is a permanent configuration defect, not a
		// transient failure: retrying would re-attempt the same wrong-repo
		// push every scan. Revoke the merge authorization and surface the
		// exact origin/git_remote conflict for a human decision.
		if errors.Is(err, errMergeTargetMismatch) {
			_ = yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "review", "merge_approved": false,
				"phase_error_code": string(ErrRepoMismatch), "phase_error": err.Error(),
			})
			notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
		}
		return err
	}
	// Merge runs on the task's worktree (round2 worktrees already checkout
	// the target branch): syncMergeBranch's three-way merge must execute on
	// the target branch — running it on the main checkout while another
	// branch is checked out merges remote feature history into the wrong
	// branch and corrupts both (TASK-051/059: the main checkout sat on
	// task/067; sync merged origin/task/051 into it, a 14-file conflict, and
	// budget exhaustion then left a stale MERGE_HEAD polluting later runs).
	// The worktree is keyed by taskRunKey(filePath), the SAME key round2 and
	// audit use — a merge must reuse the round2 worktree, never look up a
	// TASK-<id> directory that does not exist (TASK-067: merge fell back to
	// the main checkout, the AI session polluted its index with another
	// task's staged files, and `git merge --abort` spun forever on
	// "Entry ... not uptodate").
	if wd, wdErr := ensureTaskWorktree(repoDir, taskRunKey(candidate.FilePath), fm.TargetBranch, r.cfg.WorktreeBase); wdErr != nil {
		// Never fall back to the main checkout: merge performs write
		// operations (sync three-way merge, checkout, commit, push). A
		// missing/misbound worktree is an environment defect (e.g. the
		// target branch is checked out by another worktree) that must be
		// surfaced, not worked around by touching the user's primary
		// working directory. The task stays review + merge_approved=true,
		// so the next scan resumes automatically once the environment is
		// fixed.
		// Debounced per task (notifyFailure, 5min window): the merge
		// retries on every scan while the environment is broken, and a
		// bare SendTaskAction would re-toast each round (TASK-067
		// notification storm).
		remedy := ""
		if occupied := worktreePathFromError(wdErr.Error()); occupied != "" {
			remedy = fmt.Sprintf("\n执行清理：git -C %s worktree remove --force %s", repoDir, occupied)
		}
		r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "🚫", "Merge 工作区不可用",
			fmt.Sprintf("任务 worktree 无法绑定分支 %s（%v）。%s", fm.TargetBranch, wdErr, remedy), failNotifyReason)
		return fmt.Errorf("merge worktree unavailable: %w", wdErr)
	} else {
		repoDir = wd
	}
	fresh := false
	// The remote feature branch may be ahead of this worktree snapshot (an
	// earlier merge attempt pushed an AI conflict fix, or a concurrent run
	// updated it), or the local history may have been rewritten by a fresh
	// implementation. Pushing without syncing then fails with a
	// non-fast-forward rejection, which the retry loop burns all 5 attempts
	// on (TASK-067: local behind remote by 1 commit, 6+ rejected pushes).
	// syncMergeBranch reconciles by ancestry: fast-forward when the remote
	// is an ancestor, three-way merge when the local is an ancestor (the
	// conflict is resolved in-place by the AI auto-fix session, same budget
	// as PR conflicts), force-push when the local rewritten history
	// supersedes a remote head that is fully absorbed into main.
	if forkMerge {
		// Fork development: no feature-branch push/sync — the deliverable
		// lands directly on the fork's default branch. The feature branch
		// exists only in the task worktree; the merge below folds it in.
		return r.forkMergeDelivery(candidate, repoDir, fm)
	}
	forcePush, err := syncMergeBranch(r.daemonCtx, repoDir, fm.TargetBranch)
	if err != nil {
		if strings.Contains(err.Error(), string(ErrGitConflict)) {
			if autoErr := r.autoResolveMergeConflict(candidate, repoDir, fm, err.Error()); autoErr != nil {
				return autoErr
			}
			// Conflict resolved and pushed: continue to PR reuse/checks with
			// the new head.
		} else {
			return err
		}
	}
	// Fail fast on a stalled network: git's default connect retry can burn
	// 2+ minutes per push when the forge is unreachable, stalling the merge
	// goroutine and every scan batch behind it. http.connectTimeout bounds
	// the TCP connect phase, http.lowSpeed* aborts once the transfer makes
	// no progress for 20s.
	var pushCmd *exec.Cmd
	var pushCancel context.CancelFunc
	if manualMerge {
		// Team projects authenticate with the checkout's own SSH/https
		// credentials — no gh credential-helper injection.
		pushCmd, pushCancel = r.mergePushCommandPlain(repoDir, fm.TargetBranch, forcePush)
	} else {
		pushCmd, pushCancel = r.mergePushCommand(repoDir, fm.TargetBranch, forcePush)
	}
	if output, err := pushCmd.CombinedOutput(); err != nil {
		pushCancel()
		// Daemon shutdown raced the push (SIGTERM kills the git child via
		// mergeCommand's context): keep the merge authorized so it resumes
		// after restart instead of stranding the task in conflict with the
		// authorization revoked (TASK-059: a shutdown-killed push burned the
		// retry budget and ended with merge_approved=false forever).
		if r.daemonCtx.Err() != nil {
			r.logger.Printf("task %s: push interrupted by daemon shutdown, merge resumes after restart", candidate.ID)
			return errConflictResolutionInterrupted
		}
		return fmt.Errorf("push feature branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	pushCancel()
	if manualMerge {
		// Push-only delivery: the branch is on the remote, the human merges
		// through the forge UI. Record the pushed head and hand the task to
		// the remote-merge probe (checkRemoteMergedAndComplete) — no PR, no
		// CI polling, no gh merge. The worktree stays intact until the merge
		// is confirmed so a re-push after conflict repair reuses it.
		approvedHead := gitCurrentHead(repoDir, fm.TargetBranch)
		if approvedHead == "" {
			return fmt.Errorf("%s: target branch head unavailable", ErrBaseCommitMismatch)
		}
		if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"merge_status": "pushed", "approved_head": approvedHead,
			"merge_approved": false,
		}); err != nil {
			return err
		}
		notify.SendTaskAction(candidate.ID, candidate.Title, "🚀", "分支已推送，请人工合并",
			fmt.Sprintf("分支 %s 已推送到项目 %s；请通过仓库 UI 发起合并（PR/merge request），远端合入后任务自动完成", fm.TargetBranch, candidate.Project), r.cfg.Notifications.Desktop)
		r.logger.Printf("task %s: manual-mode delivery: branch %s pushed at %s, waiting for human merge", candidate.ID, fm.TargetBranch, approvedHead)
		return nil
	}
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
	// authorization by merge_retry_count. The count is cleared only when the
	// merge succeeds OR a new planning round completes (fresh delivery intent
	// — see clearMergeRepairBudget); user re-approval does not re-spend the
	// budget, and replanning does not inherit the previous delivery's
	// exhaustion (TASK-067: v3 budget spent on a large rebase left v4 without
	// AI repair).
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
					// Debounced per task (notifyFailure): a failed CI fix
					// re-authorizes on the next scan (auto_merge), which
					// would re-toast every round without a window.
					r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "❌", "合并被拒绝",
						decision.Reason+"；AI 修复失败，解决后重新设置 merge_approved=true 即可重试", failNotifyReason)
					return updateErr
				}
				// Fix committed locally: push and re-evaluate checks.
				fixPush, fixPushCancel := r.mergePushCommand(repoDir, fm.TargetBranch, false)
				output, pushErr := fixPush.CombinedOutput()
				fixPushCancel()
				if pushErr != nil {
					r.logger.Printf("task %s: push CI fix failed: %v", candidate.ID, pushErr)
					updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
						"status": "review", "merge_approved": false, "merge_status": "",
						"phase_error_code": string(decision.ErrorCode),
						"phase_error":      decision.Reason + "; push CI fix failed: " + strings.TrimSpace(string(output)),
					})
					// Debounced: push-CI-fix failure also re-authorizes next
					// scan; keep the toast rate bounded.
					r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "❌", "合并被拒绝",
						decision.Reason+"；AI 修复推送失败，解决后重新设置 merge_approved=true 即可重试", failNotifyReason)
					return updateErr
				}
				approvedHead = gitCurrentHead(repoDir, fm.TargetBranch)
				if approvedHead == "" {
					return fmt.Errorf("%s: target branch head unavailable", ErrBaseCommitMismatch)
				}
			}
			updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "review", "merge_approved": false, "merge_status": "",
				"phase_error_code": string(decision.ErrorCode), "phase_error": decision.Reason +
					"; 自动修复已达上限（" + fmt.Sprint(r.cfg.MaxAutoMergeFixes) + " 次）。继续 AI 修复：清 merge_retry_count 后重设 merge_approved=true；大范围冲突/失败建议 replan（REQ 变更或 rework_resolution=replan）由 Round 2 重做，无需手动解决",
			})
			// Debounced (failNotifyBlocked — most severe tier): budget
			// exhaustion hands back to the user, but clearing the count
			// re-authorizes and re-runs the merge; without a window every
			// round re-toasts (TASK-067 notification storm).
			r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "❌", "合并被拒绝",
				decision.Reason+"；自动修复已达上限（"+fmt.Sprint(r.cfg.MaxAutoMergeFixes)+" 次）。① 继续 AI 修复：otg update-status "+candidate.ID+" merge_retry_count=0 后重设 merge_approved=true；② 大范围改动建议重出计划（rework_resolution=replan）由 Round 2 重做", failNotifyBlocked)
			return updateErr
		case mergeActionConflict:
			if err := r.autoResolveMergeConflict(candidate, repoDir, fm, decision.Reason); err != nil {
				return err
			}
			// Resolution committed and pushed: re-evaluate checks with the
			// new head. An interrupted session returns nil with review +
			// merge_approved kept, so the merge resumes after restart.
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
			return r.completeMerge(candidate, repoDir, prURL)
		default:
			return fmt.Errorf("%s: unknown merge decision", ErrInternal)
		}
	}
}

// forkMergeDelivery merges the task's feature branch into the fork's default
// branch and pushes it — fork-development delivery: the fork belongs to the
// developer, so the merge is fully automatable; the human then sends the team
// project a PR from the fork's default branch (out-of-band, manual by design).
//
// Flow (all in the task worktree, which starts on the feature branch):
//  1. fetch origin <default> (default resolved via ls-remote --symref — forges
//     allow configuring it, never hardcode main)
//  2. checkout -B <default> origin/<default>
//  3. git merge --no-ff <feature>; conflicts go through the bounded AI
//     conflict-resolution session (merge_retry_count budget, same as PR
//     conflicts); the AI session commits the resolution, completing the merge
//  4. push origin <default> with the repository's own credentials (no gh)
//  5. done + merge_status=merged — the fork default branch is the deliverable
func (r *Runner) forkMergeDelivery(candidate task.ReadyTask, repoDir string, fm *yamlfrontmatter.Frontmatter) error {
	defaultBranch := remoteDefaultBranch(r.daemonCtx, repoDir)
	if defaultBranch == "" {
		return fmt.Errorf("%s: cannot resolve fork default branch (remote unreachable or HEAD symref missing)", ErrGitHubUnavailable)
	}
	// Fetch the default branch so the local ref is current.
	fetchCmd, fetchCancel := mergeCommand(r.daemonCtx, repoDir, "git", "-C", repoDir,
		"-c", "http.connectTimeout=15", "-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=20",
		"fetch", "origin", defaultBranch+":refs/remotes/origin/"+defaultBranch)
	out, fetchErr := fetchCmd.CombinedOutput()
	fetchCancel()
	if fetchErr != nil {
		return fmt.Errorf("fetch fork default branch: %w: %s", fetchErr, strings.TrimSpace(string(out)))
	}
	// Move the worktree onto the default branch (tracking the fetched ref).
	if out, err := exec.Command("git", "-C", repoDir, "checkout", "-B", defaultBranch, "origin/"+defaultBranch).CombinedOutput(); err != nil {
		return fmt.Errorf("checkout fork default branch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Merge the feature branch in; a conflict keeps the in-progress merge
	// state (MERGE_HEAD + markers) for the AI session, which commits the
	// resolution — completing the merge. Retry until clean or budget spent.
	for attempt := 0; ; attempt++ {
		mergeCmd, mergeCancel := mergeCommand(r.daemonCtx, repoDir, "git", "-C", repoDir,
			"-c", "http.connectTimeout=15", "-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=20",
			"merge", "--no-ff", fm.TargetBranch)
		output, mergeErr := mergeCmd.CombinedOutput()
		mergeCancel()
		if mergeErr == nil {
			break // merged (or already up to date after an AI resolution)
		}
		msg := strings.TrimSpace(string(output))
		r.logger.Printf("task %s: fork merge into %s failed: %v: %s", candidate.ID, defaultBranch, mergeErr, msg)
		if r.daemonCtx.Err() != nil {
			return errConflictResolutionInterrupted
		}
		if !strings.Contains(msg, "CONFLICT") && !strings.Contains(mergeErr.Error(), string(ErrGitConflict)) {
			return fmt.Errorf("merge into fork default branch: %w: %s", mergeErr, msg)
		}
		if fm.MergeRetryCount >= r.cfg.MaxAutoMergeFixes {
			updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
				"phase_error_code": string(ErrGitConflict),
				"phase_error":      fmt.Sprintf("fork merge conflict in %s; 自动修复已达上限（%d 次）。清 merge_retry_count 后重设 merge_approved=true 继续；连续失败建议 replan", defaultBranch, r.cfg.MaxAutoMergeFixes),
			})
			r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "⚠️", "fork 合并冲突",
				fmt.Sprintf("fork merge conflict in %s；自动修复已达上限（%d 次）。清 merge_retry_count 后重设 merge_approved=true 继续；连续失败建议 replan", defaultBranch, r.cfg.MaxAutoMergeFixes), failNotifyBlocked)
			return updateErr
		}
		fm.MergeRetryCount++
		if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"merge_retry_count": fm.MergeRetryCount,
			"merge_status":      "auto-fix-conflict",
		}); err != nil {
			return err
		}
		if err := r.resolveMergeConflict(candidate, repoDir); err != nil {
			if errors.Is(err, errConflictResolutionInterrupted) {
				r.logger.Printf("task %s: fork merge conflict resolution interrupted by daemon shutdown, merge resumes after restart", candidate.ID)
				return nil
			}
			updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
				"phase_error_code": string(ErrGitConflict),
				"phase_error":      "fork merge conflict; AI auto-resolution failed: " + err.Error(),
			})
			r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "⚠️", "fork 合并冲突",
				"fork merge conflict; AI auto-resolution failed: "+err.Error(), failNotifyReason)
			return updateErr
		}
		// AI committed the resolution (completing the merge); the loop
		// re-merges and sees "Already up to date" → clean.
	}
	// Push the fork default branch with the repository's own credentials.
	pushCmd, pushCancel := r.mergePushCommandPlain(repoDir, defaultBranch, false)
	output, pushErr := pushCmd.CombinedOutput()
	pushCancel()
	if pushErr != nil {
		if r.daemonCtx.Err() != nil {
			r.logger.Printf("task %s: fork merge push interrupted by daemon shutdown, merge resumes after restart", candidate.ID)
			return errConflictResolutionInterrupted
		}
		return fmt.Errorf("push fork default branch: %w: %s", pushErr, strings.TrimSpace(string(output)))
	}
	// Delivered: the fork default branch carries the implementation and is
	// on the remote. The human sends the team project a PR from here.
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
		"status": "done", "merge_approved": false, "pending_req": false,
		"merge_status": "merged", "completed": time.Now().Format(time.RFC3339),
		"phase_error_code": "", "phase_error": "", "merge_retry_count": 0,
	}); err != nil {
		return err
	}
	r.cleanupTaskArtifacts(candidate.FilePath, repoDir)
	notify.SendTaskAction(candidate.ID, candidate.Title, "✅", "已合入 fork 默认分支",
		fmt.Sprintf("实现已 merge 到 %s 的 %s 分支并推送；请手动向团队项目提交 PR，团队 review 后合入", candidate.Project, defaultBranch), r.cfg.Notifications.Desktop)
	r.logger.Printf("task %s: fork-merge delivery: %s merged into fork %s and pushed, awaiting manual team PR", candidate.ID, fm.TargetBranch, defaultBranch)
	r.activeTasks.Add(1)
	go func() {
		defer r.activeTasks.Add(-1)
		r.extractProjectKnowledge(candidate.Project, candidate.FilePath)
	}()
	return nil
}

// completeMerge transitions the task to done after a successful merge — or
// when the PR is already merged remotely (manual merge, earlier run). Used by
// both the merge action and the early convergence path.
func (r *Runner) completeMerge(candidate task.ReadyTask, repoDir, prURL string) error {
	// Refresh the local origin/main mirror BEFORE writing done: the merge
	// itself happened on the forge (gh pr merge), so the local ref updates
	// only on the next fetch. A scan racing that window would read the
	// freshly-done task as an undelivered increment and reopen it (TASK-018
	// 2026-08-14: reopened minutes after PR #76 merged). Failure is silent —
	// detectStaleDoneReopens re-fetches before reopening anyway.
	fetchOriginMain(r.daemonCtx, repoDir)
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
		"status": "done", "merge_approved": false, "pending_req": false,
		"merge_status": "merged", "completed": time.Now().Format(time.RFC3339),
		"phase_error_code": "", "phase_error": "", "merge_retry_count": 0,
	}); err != nil {
		return err
	}
	r.cleanupTaskArtifacts(candidate.FilePath, repoDir)
	notify.SendTaskAction(candidate.ID, candidate.Title, "✅", "合并成功",
		fmt.Sprintf("PR %s 已合并，任务完成", prURL), r.cfg.Notifications.Desktop)
	// Step 0：把本任务知识提取到知识库（非阻塞，但计入 activeTasks——
	// 优雅停机等待提取落盘而不是截断；此前被杀的提取会留下
	// knowledge_extracted=false 且无重试路径，直到补救扫描落地）。
	r.activeTasks.Add(1)
	go func() {
		defer r.activeTasks.Add(-1)
		r.extractProjectKnowledge(candidate.Project, candidate.FilePath)
	}()
	return nil
}

// fetchOriginMain refreshes the local origin/main mirror so stale-terminal
// checks see freshly-merged delivery evidence. Failures are silent: the
// caller falls back to conservative behavior (never reopen on uncertainty),
// and the next scan retries. Network timeouts mirror syncMergeBranch so an
// unreachable forge cannot stall a scan behind the fetch.
func fetchOriginMain(parent context.Context, repoDir string) bool {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir,
		"-c", "http.connectTimeout=15", "-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=20",
		"fetch", "origin", "main")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
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

// prDeliversCheckpoint reports whether the merged PR's merge commit has the
// task checkpoint in its ancestry — i.e. the PR actually carried this task's
// code. Merged PRs always expose a merge commit; any lookup or ancestry
// failure resolves conservatively to false so a legacy PR can never fake a
// delivery the local repository does not confirm (TASK-069: a same-branch v1
// PR converged the v16 generation to done without delivering its checkpoint,
// and the stale-done detector reopened it — done→refining loop).
func prDeliversCheckpoint(parent context.Context, repoDir, prURL, checkpoint string) bool {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "mergeCommit", "--jq", ".mergeCommit.oid")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	mergeCommit := strings.TrimSpace(string(output))
	if mergeCommit == "" {
		return false
	}
	anc := exec.CommandContext(ctx, "git", "-C", repoDir, "merge-base", "--is-ancestor", checkpoint, mergeCommit)
	if err := anc.Run(); err != nil {
		return false
	}
	return true
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

// 凭据走 gh CLI 认证通道——credential.helper 调用 `gh auth git-credential`，
// 与创建/合并 PR 使用同一 GitHub 身份，而不是依赖 ambient git 凭据。
// 仅通过 gh keyring（或 SSH）认证的机器没有 https credential helper：
// 裸 `git push` 到 https origin 会报 "could not read Username" 并烧光
// 全部重试预算（实例：TASK-004 卡在 review，merge 重试 1/5..5/5 全部
// syncMergeBranch brings the local worktree branch up to date with the
// remote feature branch before push. Returns forcePush=true when the local
// branch was rewritten (fresh Round 2 implementation / rebase) and the stale
// remote head's exclusive changes are all re-implemented locally — a plain
// push would be rejected as non-fast-forward and three-way-merging the stale
// head back would resurrect discarded WIP (TASK-051/059: remote head was the
// old WIP snapshot, local was the v4 re-implementation).
// Conflicts surface as ErrGitConflict with the conflict state preserved so
// the caller can route straight into auto-fix-conflict.
func syncMergeBranch(ctx context.Context, repoDir, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	// A stale in-progress merge from an earlier failed run (AI session
	// failure, budget exhaustion) must be rolled back before any new merge:
	// merging onto an unresolved MERGE_HEAD would stack conflict state and
	// pollute the branch (TASK-051/059: the main checkout carried a stale
	// MERGE_HEAD for a day, corrupting every sync that ran on it).
	if mergeInProgress(repoDir) {
		if out, err := exec.Command("git", "-C", repoDir, "merge", "--abort").CombinedOutput(); err != nil {
			return false, fmt.Errorf("abort stale merge: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	// Fail fast on a stalled network, mirroring mergePushCommand: without
	// http.connectTimeout/lowSpeed the fetch can hang the full 60s merge
	// command budget on an unreachable github.com, stalling the merge
	// goroutine (and its scan batch) behind it.
	fetchCmd, fetchCancel := mergeCommand(ctx, repoDir, "git", "-C", repoDir,
		"-c", "http.connectTimeout=15", "-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=20",
		"fetch", "origin", branch+":refs/remotes/origin/"+branch)
	out, err := fetchCmd.CombinedOutput()
	fetchCancel()
	if err != nil {
		msg := string(out)
		// No remote branch yet — first push, nothing to sync with.
		if strings.Contains(msg, "couldn't find remote ref") || strings.Contains(msg, "not our ref") {
			return false, nil
		}
		return false, fmt.Errorf("fetch merge branch: %w: %s", err, strings.TrimSpace(msg))
	}
	local := gitCurrentHead(repoDir, branch)
	remote := gitCurrentHead(repoDir, "origin/"+branch)
	if local == "" || remote == "" || local == remote {
		return false, nil
	}
	// Ancestry decides merge vs force-push:
	// - remote is an ancestor of local → local ahead, push fast-forwards.
	// - local is an ancestor of remote → remote ahead (an earlier AI-fix
	//   push), three-way merge the remote head in.
	// - neither (history rewritten by the new implementation): force-push
	//   when the stale remote head's exclusive changes are all re-implemented
	//   by the local branch (TASK-051/059: the remote WIP snapshot predates
	//   the v4 plan; three-way merging it back would resurrect discarded
	//   code). The file-level check protects genuine remote-only work (an
	//   AI-fix commit touching files the local branch never changed) — never
	//   guess about clobbering changes local does not own.
	if isAncestor(ctx, repoDir, "origin/"+branch, branch) {
		return false, nil
	}
	if isAncestor(ctx, repoDir, branch, "origin/"+branch) {
		mergeCmd, mergeCancel := mergeCommand(ctx, repoDir, "git", "-C", repoDir, "merge", "origin/"+branch)
		out, err = mergeCmd.CombinedOutput()
		mergeCancel()
		if err != nil {
			// Keep the in-progress merge conflict state (unresolved markers +
			// .git/MERGE_HEAD) for the AI auto-fix session; the caller resolves
			// it in the same processMergeTask run.
			return false, fmt.Errorf("%s: merge onto remote branch: %w: %s", ErrGitConflict, err, strings.TrimSpace(string(out)))
		}
		return false, nil
	}
	// Diverged: remote-only changes must be a subset of the local branch's
	// changes relative to the shared base, otherwise the force would drop
	// work the local implementation does not know about.
	if remoteChangesCovered(ctx, repoDir, branch, "origin/"+branch) {
		return true, nil
	}
	return false, fmt.Errorf("%s: local branch %s diverged from remote %s and the remote head carries changes the local branch does not re-implement — refusing to force-push (resolve manually or force deliberately)", ErrGitConflict, branch, remote)
}

// remoteChangesCovered reports whether every file changed exclusively on the
// remote branch (shared-base..remote) is also changed by the local branch
// (shared-base..local). Equal coverage means the local re-implementation
// supersedes the stale remote work file by file.
func remoteChangesCovered(ctx context.Context, repoDir, localBranch, remoteRef string) bool {
	mergeBase := gitMergeBase(ctx, repoDir, localBranch, remoteRef)
	if mergeBase == "" {
		return false
	}
	remoteFiles := gitDiffNames(ctx, repoDir, mergeBase+".."+remoteRef)
	localFiles := gitDiffNames(ctx, repoDir, mergeBase+".."+localBranch)
	if len(remoteFiles) == 0 {
		// No exclusive remote changes (pure history divergence, e.g. a
		// rebase with identical content): the local rewrite owns the branch.
		return true
	}
	covered := make(map[string]struct{}, len(localFiles))
	for _, f := range localFiles {
		covered[f] = struct{}{}
	}
	for _, f := range remoteFiles {
		if _, ok := covered[f]; !ok {
			return false
		}
	}
	return true
}

func gitMergeBase(ctx context.Context, repoDir, a, b string) string {
	cmd, cancel := mergeCommand(ctx, repoDir, "git", "-C", repoDir, "merge-base", a, b)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitDiffNames(ctx context.Context, repoDir, rangeSpec string) []string {
	cmd, cancel := mergeCommand(ctx, repoDir, "git", "-C", repoDir, "diff", "--name-only", rangeSpec)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	names := lines[:0]
	for _, l := range lines {
		if l != "" {
			names = append(names, l)
		}
	}
	return names
}

// mergeInProgress reports whether the repository is mid-merge — a stale
// MERGE_HEAD left by an earlier failed sync or conflict auto-fix.
func mergeInProgress(repoDir string) bool {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--git-path", "MERGE_HEAD")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoDir, path)
	}
	_, err = os.Stat(path)
	return err == nil
}

// isAncestor reports whether commit A is an ancestor of commit B
// (`git merge-base --is-ancestor A B`, exit 0 = ancestor).
func isAncestor(ctx context.Context, repoDir, a, b string) bool {
	cmd, cancel := mergeCommand(ctx, repoDir, "git", "-C", repoDir, "merge-base", "--is-ancestor", a, b)
	defer cancel()
	return cmd.Run() == nil
}

func (r *Runner) mergePushCommand(repoDir, branch string, force bool) (*exec.Cmd, context.CancelFunc) {
	args := []string{"-C", repoDir,
		"-c", "credential.helper=!gh auth git-credential",
		"-c", "http.connectTimeout=15",
		"-c", "http.lowSpeedLimit=1", "-c", "http.lowSpeedTime=20",
		"push", "-u", "origin", branch}
	if force {
		// force-with-lease: only overwrite the remote ref if it still points
		// at the head syncMergeBranch fetched — never clobber a concurrent
		// push (e.g. another daemon's AI-fix push).
		args = append(args, "--force-with-lease")
	}
	return mergeCommand(r.daemonCtx, repoDir, "git", args...)
}

// mergePushCommandPlain is the push command for team-project deliveries
// (merge_mode=manual push-only and merge_mode=fork-merge): no gh
// credential-helper injection — the ambient SSH/https credentials of the
// checkout authenticate the push. Same fail-fast network flags as the
// gh-credential variant.
func (r *Runner) mergePushCommandPlain(repoDir, branch string, force bool) (*exec.Cmd, context.CancelFunc) {
	args := []string{"-C", repoDir,
		"-c", "http.connectTimeout=15",
		"-c", "http.lowSpeedLimit=1", "-c", "http.lowSpeedTime=20",
		"push", "-u", "origin", branch}
	if force {
		args = append(args, "--force-with-lease")
	}
	return mergeCommand(r.daemonCtx, repoDir, "git", args...)
}

// checkGHAuth 验证 merge 流程所需的 gh CLI 认证（`gh auth status` 退出码 0）。
// 全部远程步骤——push（gh credential helper）、PR 创建/复用、PR 合并——
// 都依赖 gh 身份，因此未登录时无论 ambient git 凭据如何都无法合并。
// 仅本地检查（gh config + keyring，无网络），超时 15s。
func checkGHAuth(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "gh", "auth", "status").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = "gh auth status exited non-zero"
		}
		return fmt.Errorf("gh CLI not authenticated: run 'gh auth login' (%s)", detail)
	}
	return nil
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
	if errors.Is(err, errMergeTargetMismatch) {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"gh CLI not found",
		"precondition:",
		string(ErrBaseCommitMismatch),
		string(ErrReqMissing),
		string(ErrGitConflict),
		string(ErrRepoMismatch),
		string(ErrInternal),
		"merge already in progress",
		"decode PR checks",
		"gh CLI not authenticated",
		"unknown merge decision",
		"unexpected PR state",
	} {
		if strings.Contains(msg, marker) {
			return false
		}
	}
	// ErrGitHubUnavailable is an ErrorCode string constant, not an error
	// value, so match its rendered prefix instead of errors.Is. The pre-push
	// sync fetch (syncMergeBranch) fails with "fetch merge branch" on the
	// same transient network conditions as the push — it must keep the
	// backoff retry instead of falling through to the next scan.
	return strings.Contains(msg, string(ErrGitHubUnavailable)) ||
		strings.Contains(msg, "push feature branch") ||
		strings.Contains(msg, "fetch merge branch")
}

// Merge retry parameters are package-level so tests can shorten the backoff
// without waiting real time.
var (
	mergeRetryBackoff = 2 * time.Minute
	mergeMaxRetries   = 5
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
		logDir = filepath.Join(home, ".dsh", "logs")
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

// autoResolveMergeConflict runs the bounded AI conflict-repair cycle shared
// by PR conflicts and pre-push sync (merge) conflicts: budget check →
// auto-fix-conflict marker → AI session → push resolved head. Returns nil
// when the conflict is resolved and pushed (caller continues), or an error
func (r *Runner) autoResolveMergeConflict(candidate task.ReadyTask, repoDir string, fm *yamlfrontmatter.Frontmatter, reason string) error {
	if fm.MergeRetryCount >= r.cfg.MaxAutoMergeFixes {
		// Auto-repair budget exhausted: hand back to the user. The
		// conflict-resolve-attempted marker tells the user AI repair
		// was tried; re-approval no longer re-spends the budget.
		updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
			"phase_error_code": string(ErrGitConflict), "phase_error": reason +
				"; 自动修复已达上限（" + fmt.Sprint(r.cfg.MaxAutoMergeFixes) + " 次）。继续 AI 修复：清 merge_retry_count 后重设 merge_approved=true；连续修复失败多为需求歧义——在 REQ 文档中追加歧义裁决记录并保存（可含变更类型行：> 变更类型: breaking），daemon 自动转 refining 重审需求后重新出计划，无需手动解决",
		})
		// Debounced (failNotifyBlocked): the user may clear merge_retry_count
		// to continue AI repair, which re-authorizes and re-runs the merge —
		// a bare toast would re-fire every round (TASK-067 storm).
		r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "⚠️", "合并冲突待处理",
			reason+"；自动修复已达上限（"+fmt.Sprint(r.cfg.MaxAutoMergeFixes)+" 次）。① 继续 AI 修复：otg update-status "+candidate.ID+" merge_retry_count=0 后重设 merge_approved=true；② 连续失败多为需求歧义：在 REQ 文档中追加歧义裁决并保存（建议含变更类型行：> 变更类型: breaking），daemon 自动转 refining 重出计划——仅需修改需求文档，无需手动改任务状态", failNotifyBlocked)
		return updateErr
	}
	// Conflict-size circuit breaker: an AI session cannot realistically
	// resolve a very large conflict set inside its bounded timeout, and the
	// attempt only burns the repair budget and session time before failing
	// (TASK-067: 90+ conflicting files → 15min session timeout exit 143;
	// ~22 files resolved in 5min). When the local worktree carries more
	// conflicting files than the configured threshold, hand the task
	// straight back to the user instead of starting a doomed session.
	// Only the pre-push sync path has staged conflicts in the worktree; the
	// PR-conflict path (GitHub-side DIRTY) reports 0 and passes through.
	if r.cfg.MaxAutoFixConflicts > 0 {
		if n := countUnmergedFiles(repoDir); n > r.cfg.MaxAutoFixConflicts {
			updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
				"phase_error_code": string(ErrGitConflict), "phase_error": fmt.Sprintf(
					"%s; 冲突规模 %d 文件超过自动修复阈值 %d——AI 会话无法在预算内解决，请人工处理或 replan（REQ 追加歧义裁决并保存，daemon 自动转 refining 重出计划）",
					reason, n, r.cfg.MaxAutoFixConflicts),
			})
			r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "⚠️", "合并冲突规模过大",
				fmt.Sprintf("冲突 %d 文件超阈值 %d，跳过 AI 自动修复。replan：REQ 追加歧义裁决并保存；或人工解决后重设 merge_approved=true", n, r.cfg.MaxAutoFixConflicts), failNotifyBlocked)
			return updateErr
		}
	}
	r.logger.Printf("task %s: merge conflicts (%s), starting AI auto-resolution (%d/%d)", candidate.ID, reason, fm.MergeRetryCount+1, r.cfg.MaxAutoMergeFixes)
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
			"phase_error":      reason + "; AI auto-resolution failed: " + err.Error(),
		})
		r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "⚠️", "合并冲突解决失败",
			reason+"; AI auto-resolution failed: "+err.Error(), failNotifyReason)
		return updateErr
	}
	// Resolution committed locally: push the new head. Team projects use
	// the checkout's own credentials (no gh credential helper).
	var resPush *exec.Cmd
	var resPushCancel context.CancelFunc
	if projectMergeMode(filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json"), candidate.Project) == "manual" {
		resPush, resPushCancel = r.mergePushCommandPlain(repoDir, fm.TargetBranch, false)
	} else {
		resPush, resPushCancel = r.mergePushCommand(repoDir, fm.TargetBranch, false)
	}
	output, pushErr := resPush.CombinedOutput()
	resPushCancel()
	if pushErr != nil {
		if r.daemonCtx.Err() != nil {
			r.logger.Printf("task %s: conflict resolution push interrupted by daemon shutdown, merge resumes after restart", candidate.ID)
			return errConflictResolutionInterrupted
		}
		updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
			"phase_error_code": string(ErrGitConflict),
			"phase_error":      "conflict resolution committed but push failed: " + strings.TrimSpace(string(output)),
		})
		r.notifyFailure(candidate.FilePath, candidate.ID, candidate.Title, "⚠️", "冲突解决推送失败",
			"conflict resolution committed but push failed: "+strings.TrimSpace(string(output)), failNotifyReason)
		return updateErr
	}
	return nil
}

// countUnmergedFiles reports how many files are in a conflicted state in the
// given worktree (git diff --diff-filter=U). Returns 0 when the repo is not
// mid-conflict or the command fails — callers use it as a circuit-breaker
// input, and a failed count must never block the normal repair path.
func countUnmergedFiles(repoDir string) int {
	if repoDir == "" {
		return 0
	}
	output, err := exec.Command("git", "-C", repoDir, "diff", "--name-only", "--diff-filter=U").CombinedOutput()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line != "" {
			n++
		}
	}
	return n
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

// resolveMergeConflict runs one execution session that loads the merge skill's
// Automated Conflict Resolution step (skill://resolving-merge-conflicts) to
// resolve PR conflicts locally. The AI may commit a resolution; the daemon
// pushes and re-evaluates checks afterwards. The session is local-only —
// push/PR/merge stay with the daemon.
func (r *Runner) resolveMergeConflict(candidate task.ReadyTask, repoDir string) error {
	return r.runMergeAISession(candidate, repoDir, mergeFixConflicts)
}

// resolveMergeChecksFailure runs one execution session that loads the merge skill's
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
	// Inject the same project context (constraints / domain terms / ADRs)
	// that refining/planning sessions get: conflict and CI-fix resolution
	// must reason from the requirement's intent, not raw code structure
	// (TASK-067: 18-file semantic merges failed 3 repair rounds because the
	// session lacked the domain/ADR picture).
	if projDir := resolveVaultProjectDir(r.cfg.ObsidianVault, candidate.Project); projDir != "" {
		reqPath := filepath.Join(r.cfg.ObsidianVault, candidate.ReqDoc)
		if ctx := BuildProjectContext(projDir, reqPath); ctx != "" {
			skillPrompt = fmt.Sprintf("%s\n\n<project_context>\n## 项目上下文（daemon 自动注入，配合 skill://knowledge-base 交叉引用 References）\n项目: %s\n\n%s\n</project_context>", skillPrompt, candidate.Project, ctx)
			r.logger.Printf("task %s: merge fix session %s: injected project context (%d bytes)", candidate.ID, mode, len(ctx))
		}
	}
	logDir := r.cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".dsh", "logs")
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
	defer func() { _ = f.Close() }()
	provider, dshModel := mapDSHModel(model)
	header := fmt.Sprintf("# TASK-%s %s\n# model=%s/%s phase=%s time=%s\n\n",
		candidate.ID, candidate.Title, provider, dshModel, logSuffix, time.Now().Format(time.RFC3339))
	if _, err := f.WriteString(header); err != nil {
		return fmt.Errorf("write merge fix log header: %w", err)
	}

	timeout := r.cfg.PhaseTimeout("merge")
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.daemonCtx, timeout)
	defer cancel()

	// 合并冲突/CI 修复会话统一走 DSH executor。
	return r.runMergeAISessionDSH(ctx, candidate, repoDir, mode, model, skillPrompt, timeout)
}

// runMergeAISessionDSH executes a merge/CI-fix AI session through the DSH
// phase executor. The session writes files and git state itself (no stdout
// contract to parse); interruption semantics mirror the OMP path — an
// OutcomeInterrupted or cancelled ctx keeps the merge authorized for resume.
func (r *Runner) runMergeAISessionDSH(ctx context.Context, candidate task.ReadyTask, repoDir string, mode mergeFixMode, model, skillPrompt string, timeout time.Duration) error {
	spec := PhaseSpec{
		Phase:           "merge",
		Model:           model,
		ReasoningEffort: "high",
		SkillPrompt:     skillPrompt,
		Timeout:         timeout,
		WorkingDir:      repoDir,
	}
	executor := r.phaseExecutor
	if executor == nil {
		executor = newPhaseExecutor(r.cfg)
		r.phaseExecutor = executor
	}
	handle, err := executor.Start(ctx, spec, TaskSnapshot{
		TaskID:   candidate.ID,
		TaskPath: candidate.FilePath,
		Project:  candidate.Project,
		RepoDir:  repoDir,
	})
	if err != nil {
		return fmt.Errorf("merge fix session %s start: %w", mode, err)
	}
	result, err := handle.Wait()
	if err != nil {
		return fmt.Errorf("merge fix session %s wait: %w", mode, err)
	}
	if result != nil && result.Code == OutcomeInterrupted {
		return errConflictResolutionInterrupted
	}
	if result == nil || result.Code != OutcomeSuccess {
		if ctx.Err() != nil {
			return errConflictResolutionInterrupted
		}
		reason := "merge fix session failed"
		if result != nil && result.Error != "" {
			reason = result.Error
		}
		return fmt.Errorf("merge fix session %s failed: %s", mode, reason)
	}
	r.logger.Printf("task %s: merge fix session %s via DSH ok", candidate.ID, mode)
	return nil
}

func gitCurrentHead(repoDir, branch string) string {
	output, err := exec.Command("git", "-C", repoDir, "rev-parse", branch).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// remoteDefaultBranch resolves the remote's default branch name via
// `git ls-remote --symref origin HEAD` ("refs/heads/main" etc.), or "" when
// the lookup fails. Team forges (Gitea/GitLab) allow configuring the default
// branch name, so hardcoding "main" would miss merges into "master".
func remoteDefaultBranch(parent context.Context, repoDir string) string {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "ls-remote", "--symref", "origin", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		// Format: "ref: refs/heads/main\tHEAD"
		if strings.HasPrefix(line, "ref:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return strings.TrimPrefix(fields[1], "refs/heads/")
			}
		}
	}
	return ""
}

// checkRemoteMergedAndComplete reports whether the pushed head
// (approved_head) has been merged into the remote default branch — the
// manual-mode completion signal: the human merged the branch through the
// forge UI. Returns (true, nil) after flipping the task to done; (false, nil)
// when the branch is not merged yet; an error only for hard failures (no
// approved_head, unreadable repo). Network hiccups degrade to (false, nil):
// the next scan re-probes — a transient fetch failure must never revoke
// anything or spam notifications.
func (r *Runner) checkRemoteMergedAndComplete(candidate task.ReadyTask, repoDir string) (bool, error) {
	data, err := os.ReadFile(candidate.FilePath)
	if err != nil {
		return false, fmt.Errorf("read task: %w", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false, fmt.Errorf("parse task: %w", err)
	}
	if fm.ApprovedHead == "" {
		return false, fmt.Errorf("%s: approved_head empty for pushed branch", ErrBaseCommitMismatch)
	}
	defaultBranch := remoteDefaultBranch(r.daemonCtx, repoDir)
	if defaultBranch == "" {
		return false, nil // remote unreachable or no HEAD symref — retry next scan
	}
	fetchCmd, fetchCancel := mergeCommand(r.daemonCtx, repoDir, "git", "-C", repoDir,
		"-c", "http.connectTimeout=15", "-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=20",
		"fetch", "origin", defaultBranch+":refs/remotes/origin/"+defaultBranch)
	out, fetchErr := fetchCmd.CombinedOutput()
	fetchCancel()
	if fetchErr != nil {
		// Transient network/credential failure: keep waiting, retry next scan.
		r.logger.Printf("task %s: fetch default branch %s for merge probe: %v", candidate.ID, defaultBranch, strings.TrimSpace(string(out)))
		return false, nil
	}
	cmd := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", fm.ApprovedHead, "refs/remotes/origin/"+defaultBranch)
	if err := cmd.Run(); err != nil {
		return false, nil // not an ancestor yet — the human has not merged
	}
	// Merged: flip to done like completeMerge, but with the manual-mode
	// wording (no PR URL).
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
		"status": "done", "merge_approved": false, "pending_req": false,
		"merge_status": "merged", "completed": time.Now().Format(time.RFC3339),
		"phase_error_code": "", "phase_error": "", "merge_retry_count": 0,
	}); err != nil {
		return false, err
	}
	r.cleanupTaskArtifacts(candidate.FilePath, repoDir)
	notify.SendTaskAction(candidate.ID, candidate.Title, "✅", "远端已合入，任务完成",
		fmt.Sprintf("分支 %s 已由人工合入 %s 的 %s 分支", fm.TargetBranch, candidate.Project, defaultBranch), r.cfg.Notifications.Desktop)
	r.logger.Printf("task %s: manual-mode delivery confirmed: head %s merged into origin/%s", candidate.ID, fm.ApprovedHead, defaultBranch)
	r.activeTasks.Add(1)
	go func() {
		defer r.activeTasks.Add(-1)
		r.extractProjectKnowledge(candidate.Project, candidate.FilePath)
	}()
	return true, nil
}

func loadMergeChecks(parent context.Context, repoDir, prURL string) (mergeChecks, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", prURL, "--json", "headRefOid,mergeStateStatus,mergeable,statusCheckRollup,url")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return mergeChecks{}, fmt.Errorf("%s: inspect PR checks: %w: %s", ErrGitHubUnavailable, err, strings.TrimSpace(string(output)))
	}
	var payload struct {
		HeadRefOID        string `json:"headRefOid"`
		MergeStateStatus  string `json:"mergeStateStatus"`
		Mergeable         string `json:"mergeable"`
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
			return mergeChecks{HeadOID: payload.HeadRefOID, State: state, URL: payload.URL, Mergeable: payload.Mergeable}, nil
		case "SUCCESS", "NEUTRAL", "SKIPPED", "STALE":
			// completed or non-blocking: keep current state. STALE means the
			// run was superseded (e.g. by a head push); GitHub schedules a
			// fresh run, and head movement is caught separately by the
			// approvedHead comparison in evaluateMergeChecks.
		default:
			state = "PENDING"
		}
	}
	return mergeChecks{HeadOID: payload.HeadRefOID, State: state, URL: payload.URL, Mergeable: payload.Mergeable}, nil
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
		// 硬失败（任务不可读/不可解析）：记录到任务上让丢失可见，
		// 通知用户，保持 knowledge_extracted=false——补救扫描下一轮
		// 重试，而不是静默丢弃教训。
		msg := fmt.Sprintf("knowledge extraction failed: %v", err)
		_ = yamlfrontmatter.Update(taskPath, map[string]interface{}{"knowledge_extract_error": msg})
		r.logger.Printf("knowledge-base Step 0 failed: project=%s task=%s error=%v", projectName, filepath.Base(taskPath), err)
		if data, rerr := os.ReadFile(taskPath); rerr == nil {
			if fm, perr := yamlfrontmatter.Parse(data); perr == nil && fm != nil && !r.diagNotified("kbextract|"+fm.ID) {
				notify.SendTaskAction(fm.ID, fm.Title, "📚", "知识提炼失败（自动重试中）",
					"任务知识未入库："+msg+"。daemon 每轮扫描自动重试，无需手动操作。", r.cfg.Notifications.Desktop)
			}
		}
		return
	}
	if len(result.Errors) > 0 {
		// 部分失败：部分 ADR/踩坑已提取，其余出错。marker 保持 false，
		// 下一轮 scan 重试剩余部分。
		msg := "knowledge extraction partial failure: " + strings.Join(result.Errors, "; ")
		_ = yamlfrontmatter.Update(taskPath, map[string]interface{}{"knowledge_extract_error": msg})
		if data, rerr := os.ReadFile(taskPath); rerr == nil {
			if fm, perr := yamlfrontmatter.Parse(data); perr == nil && fm != nil && !r.diagNotified("kbextract|"+fm.ID) {
				notify.SendTaskAction(fm.ID, fm.Title, "📚", "知识提炼部分失败（自动重试中）",
					"部分任务知识未入库："+strings.Join(result.Errors, "; ")+"。daemon 每轮扫描自动重试。", r.cfg.Notifications.Desktop)
			}
		}
	} else {
		_ = yamlfrontmatter.Update(taskPath, map[string]interface{}{"knowledge_extract_error": ""})
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
	// High-heat extended/ documents join the core retrieval layer so reused
	// experience is found first (core → extended → archived cascade).
	if promoted, perr := knowledge.PromoteToCore(vaultDir, 3); perr != nil {
		r.logger.Printf("knowledge-base core promotion failed: %v", perr)
	} else if len(promoted) > 0 {
		r.logger.Printf("knowledge-base promoted to core: %d docs", len(promoted))
		for _, m := range promoted {
			r.logger.Printf("knowledge-base promote: %s", m)
		}
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
	// Sync the retrieval store incrementally so newly extracted lessons
	// participate in retrieval immediately. Non-blocking: embedding outages
	// degrade to FTS-only, never fail the merge. A store-sync failure is a
	// REAL 提炼收入 failure though: the marker is reset to false and the
	// error recorded so the done+merged recovery scan re-runs the whole
	// idempotent pipeline next round instead of leaving a stale retrieval
	// store behind a true marker (2026-08-21 TASK-001: sync failed with
	// "database is locked" and only a log line was left).
	stats, serr := r.syncKnowledgeStore()
	if serr != nil {
		r.logger.Printf("knowledge-base store sync failed: %v", serr)
		syncMsg := "knowledge store sync failed: " + serr.Error()
		if len(result.Errors) > 0 {
			syncMsg = strings.Join(result.Errors, "; ") + "; " + syncMsg
		}
		if uerr := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"knowledge_extracted":     false,
			"knowledge_extract_error": syncMsg,
		}); uerr != nil {
			r.logger.Printf("knowledge-base store sync failure marker update failed: %v", uerr)
		}
		if data, rerr := os.ReadFile(taskPath); rerr == nil {
			if fm, perr := yamlfrontmatter.Parse(data); perr == nil && fm != nil && !r.diagNotified("kbextract|"+fm.ID) {
				notify.SendTaskAction(fm.ID, fm.Title, "📚", "知识库同步失败（自动重试中）",
					"任务知识未完整收入检索库："+serr.Error()+"。daemon 每轮扫描自动重试，无需手动操作。", r.cfg.Notifications.Desktop)
			}
		}
	} else {
		r.logger.Printf("knowledge-base store synced: %d docs (+%d -%d), %d chunks",
			stats.TotalDocs, stats.Added, stats.Removed, stats.TotalChunks)
		if stats.VecError != nil {
			r.logger.Printf("knowledge-base vector refresh failed: %v", stats.VecError)
		}
	}
}
