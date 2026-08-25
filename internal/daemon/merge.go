package daemon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ensureGitRemote is defined in merge_runner.go.
type mergeAuthorization struct {
	Status        string
	MergeApproved bool
	PendingReq    bool
	ReqPath       string
	PlanReqHash   string
	TargetBranch  string
}

// healTargetBranch 自愈 round2 被中断后丢失的 target_branch（TASK-079）：
// 任务 worktree 已 checkout 在 round2 分支（task/{id}-slug）上，从其恢复
// 分支名并写回 frontmatter。只在 target_branch 为空且 worktree 分支带
// "task/" 前缀时生效（幂等、一次写回），避免把 main 等无关分支误写进去。
func healTargetBranch(worktreeBase, repoDir string, candidate task.ReadyTask, fm *yamlfrontmatter.Frontmatter) bool {
	if fm == nil || fm.TargetBranch != "" {
		return false
	}
	wtPath := taskWorktreePath(worktreeBase, repoDir, taskRunKey(candidate.FilePath))
	if _, err := os.Stat(wtPath); err != nil {
		return false
	}
	branch, err := gitCurrentBranch(wtPath)
	if err != nil || branch == "" || !strings.HasPrefix(branch, "task/") {
		return false
	}
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{"target_branch": branch}); err != nil {
		return false
	}
	fm.TargetBranch = branch
	return true
}

func validateMergeAuthorization(auth mergeAuthorization) error {
	if auth.Status != "review" && auth.Status != "conflict" {
		return fmt.Errorf("precondition: status %q is not mergeable", auth.Status)
	}
	if !auth.MergeApproved {
		return fmt.Errorf("precondition: merge_approved is false")
	}
	if auth.PendingReq {
		return fmt.Errorf("precondition: pending_req revokes merge authorization")
	}
	if auth.TargetBranch == "" {
		return fmt.Errorf("precondition: target_branch is required")
	}
	hash, err := hashFile(auth.ReqPath)
	if err != nil {
		return fmt.Errorf("%s: %w", ErrReqMissing, err)
	}
	if auth.PlanReqHash == "" || hash != auth.PlanReqHash {
		return fmt.Errorf("%s: REQ hash changed", ErrBaseCommitMismatch)
	}
	return nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

type mergeChecks struct {
	HeadOID   string
	State     string
	URL       string
	Mergeable string // GitHub PR mergeable: MERGEABLE / CONFLICTING / UNKNOWN (computing)
}

type mergeAction string

const (
	mergeActionWait     mergeAction = "wait"
	mergeActionMerge    mergeAction = "merge"
	mergeActionReview   mergeAction = "review"
	mergeActionConflict mergeAction = "conflict"
)

type mergeDecision struct {
	Action    mergeAction
	ErrorCode ErrorCode
	Reason    string
}

func evaluateMergeChecks(approvedHead string, checks mergeChecks) mergeDecision {
	if approvedHead == "" || checks.HeadOID != approvedHead {
		return mergeDecision{Action: mergeActionReview, ErrorCode: ErrBaseCommitMismatch, Reason: "approved head changed"}
	}
	switch strings.ToUpper(checks.State) {
	case "SUCCESS":
		// GitHub computes mergeability asynchronously: a freshly pushed head
		// can report CLEAN checks while mergeable is still UNKNOWN. Merging
		// immediately then fails server-side with "not mergeable" and burns
		// the environmental retry budget (TASK-067: push → gh pr merge
		// rejected DIRTY → 5 retries wasted). Wait for the server to
		// converge instead. An empty mergeable (gh did not return the field,
		// e.g. older gh CLI) keeps the legacy behavior: merge on SUCCESS.
		if checks.Mergeable != "" && !strings.EqualFold(checks.Mergeable, "MERGEABLE") {
			return mergeDecision{Action: mergeActionWait, Reason: "PR mergeability still computing"}
		}
		return mergeDecision{Action: mergeActionMerge}
	case "FAILURE", "ERROR", "CANCELLED":
		return mergeDecision{Action: mergeActionReview, ErrorCode: ErrValidationFailed, Reason: "required checks failed"}
	case "CONFLICTING":
		return mergeDecision{Action: mergeActionConflict, ErrorCode: ErrGitConflict, Reason: "PR has merge conflicts"}
	default:
		return mergeDecision{Action: mergeActionWait}
	}
}
