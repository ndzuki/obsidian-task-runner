package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
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
		output, createErr := exec.Command("gh", "pr", "create", "--head", fm.TargetBranch, "--fill").CombinedOutput()
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
	checks, err := loadMergeChecks(repoDir, prURL)
	if err != nil {
		return err
	}
	decision := evaluateMergeChecks(approvedHead, checks)
	switch decision.Action {
	case mergeActionWait:
		return nil
	case mergeActionReview:
		return yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"status": "review", "merge_approved": false, "merge_status": "",
			"phase_error_code": string(decision.ErrorCode), "phase_error": decision.Reason,
		})
	case mergeActionConflict:
		return yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"status": "conflict", "merge_approved": false,
			"phase_error_code": string(ErrGitConflict), "phase_error": decision.Reason,
		})
	case mergeActionMerge:
		if output, mergeErr := exec.Command("gh", "pr", "merge", prURL, "--merge", "--delete-branch").CombinedOutput(); mergeErr != nil {
			return fmt.Errorf("%s: merge PR: %w: %s", ErrGitHubUnavailable, mergeErr, strings.TrimSpace(string(output)))
		}
		return yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"status": "done", "merge_approved": false, "pending_req": false,
			"merge_status": "merged", "completed": time.Now().Format(time.RFC3339),
			"phase_error_code": "", "phase_error": "",
		})
	default:
		return fmt.Errorf("%s: unknown merge decision", ErrInternal)
	}
}

func gitCurrentHead(repoDir, branch string) string {
	output, err := exec.Command("git", "-C", repoDir, "rev-parse", branch).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func loadMergeChecks(repoDir, prURL string) (mergeChecks, error) {
	output, err := exec.Command("gh", "pr", "view", prURL, "--json", "headRefOid,mergeStateStatus,statusCheckRollup,url").CombinedOutput()
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
