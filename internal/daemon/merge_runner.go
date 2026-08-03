package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	if output, err := exec.Command("git", "-C", repoDir, "push", "-u", "origin", fm.TargetBranch).CombinedOutput(); err != nil {
		return fmt.Errorf("push feature branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	prURL := fm.PRURL
	if prURL == "" {
		createCmd := exec.Command("gh", "pr", "create", "--head", fm.TargetBranch, "--fill")
		createCmd.Dir = repoDir
		output, createErr := createCmd.CombinedOutput()
		if createErr != nil {
			return fmt.Errorf("%s: create PR: %w: %s", ErrGitHubUnavailable, createErr, strings.TrimSpace(string(output)))
		}
		prURL = strings.TrimSpace(string(output))
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

	// One AI auto-resolution attempt per conflict event. The marker survives
	// a failed attempt, so re-approval by the user does not re-trigger it.
	conflictAttempted := fm.MergeStatus == "conflict-resolve-attempted"
	for attempt := 0; ; attempt++ {
		checks, err := loadMergeChecks(repoDir, prURL)
		if err != nil {
			return err
		}
		decision := evaluateMergeChecks(approvedHead, checks)
		switch decision.Action {
		case mergeActionWait:
			return nil
		case mergeActionReview:
			updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "review", "merge_approved": false, "merge_status": "",
				"phase_error_code": string(decision.ErrorCode), "phase_error": decision.Reason,
			})
			notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
			return updateErr
		case mergeActionConflict:
			if conflictAttempted || attempt > 0 {
				// Auto-resolution already attempted (or the resolution did not
				// clear the conflict): hand back to the user.
				updateErr := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
					"status": "conflict", "merge_approved": false, "merge_status": "conflict-resolve-attempted",
					"phase_error_code": string(ErrGitConflict), "phase_error": decision.Reason,
				})
				notify.StatusNotify(candidate.FilePath, r.cfg.Notifications.Desktop)
				return updateErr
			}
			r.logger.Printf("task %s: PR has merge conflicts, starting AI auto-resolution", candidate.ID)
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
			if output, pushErr := exec.Command("git", "-C", repoDir, "push", "-u", "origin", fm.TargetBranch).CombinedOutput(); pushErr != nil {
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
				"merge_status": "conflict-resolve-attempted", "approved_head": approvedHead,
			}); err != nil {
				return err
			}
			conflictAttempted = true
			continue
		case mergeActionMerge:
			mergeCmd := exec.Command("gh", "pr", "merge", prURL, "--merge", "--delete-branch")
			mergeCmd.Dir = repoDir
			if output, mergeErr := mergeCmd.CombinedOutput(); mergeErr != nil {
				return fmt.Errorf("%s: merge PR: %w: %s", ErrGitHubUnavailable, mergeErr, strings.TrimSpace(string(output)))
			}
			// Transition to done
			if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"status": "done", "merge_approved": false, "pending_req": false,
				"merge_status": "merged", "completed": time.Now().Format(time.RFC3339),
				"phase_error_code": "", "phase_error": "",
			}); err != nil {
				return err
			}
			// Step 0: Extract project knowledge to knowledge base (non-blocking)
			go r.extractProjectKnowledge(candidate.Project)
			return nil
		default:
			return fmt.Errorf("%s: unknown merge decision", ErrInternal)
		}
	}
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

// resolveMergeConflict runs one OMP session that loads the merge skill's
// Automated Conflict Resolution step (skill://resolving-merge-conflicts) to
// resolve PR conflicts locally. The AI may commit a resolution; the daemon
// pushes and re-evaluates checks afterwards. The session is local-only —
// push/PR/merge stay with the daemon.
func (r *Runner) resolveMergeConflict(candidate task.ReadyTask, repoDir string) error {
	model := r.selectModel(candidate.Assignee)
	skillPrompt := "/obsidian-task-runner-merge " + candidate.FilePath
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
	logPath := filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s-%s-%s.log",
		candidate.ID, taskRunKey(candidate.FilePath), time.Now().Format("20060102-150405"), "merge-conflict"))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create conflict resolution log: %w", err)
	}
	defer f.Close()
	header := fmt.Sprintf("# TASK-%s %s\n# model=%s phase=merge-conflict time=%s\n\n",
		candidate.ID, candidate.Title, model, time.Now().Format(time.RFC3339))
	if _, err := f.WriteString(header); err != nil {
		return fmt.Errorf("write conflict resolution log header: %w", err)
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

	r.logger.Printf("task %s: resolving merge conflicts via OMP (model=%s, timeout=%v, log=%s)", candidate.ID, model, timeout, logPath)
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return errConflictResolutionInterrupted
		}
		return fmt.Errorf("conflict resolution session failed: %w", err)
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

func loadMergeChecks(repoDir, prURL string) (mergeChecks, error) {
	cmd := exec.Command("gh", "pr", "view", prURL, "--json", "headRefOid,mergeStateStatus,statusCheckRollup,url")
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
			State string `json:"state"`
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
		if check.State == "FAILURE" || check.State == "ERROR" || check.State == "CANCELLED" {
			state = check.State
			break
		}
		if check.State != "SUCCESS" {
			state = "PENDING"
		}
	}
	return mergeChecks{HeadOID: payload.HeadRefOID, State: state, URL: payload.URL}, nil
}

func (r *Runner) extractProjectKnowledge(projectName string) {
	vaultDir := r.cfg.ObsidianVault

	result, err := knowledge.ExtractProjectKnowledge(vaultDir, projectName)
	if err != nil {
		r.logger.Printf("knowledge-base Step 0 failed: project=%s error=%v", projectName, err)
		return
	}
	if result.ADRCount > 0 || result.NewRefs > 0 || result.UpdatedRefs > 0 {
		r.logger.Printf("knowledge-base extracted: project=%s adrs=%d new=%d updated=%d",
			projectName, result.ADRCount, result.NewRefs, result.UpdatedRefs)
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
