package daemon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type mergeAuthorization struct {
	Status        string
	MergeApproved bool
	PendingReq    bool
	ReqPath       string
	PlanReqHash   string
	TargetBranch  string
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
	HeadOID string
	State   string
	URL     string
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
		return mergeDecision{Action: mergeActionMerge}
	case "FAILURE", "ERROR", "CANCELLED":
		return mergeDecision{Action: mergeActionReview, ErrorCode: ErrValidationFailed, Reason: "required checks failed"}
	case "CONFLICTING":
		return mergeDecision{Action: mergeActionConflict, ErrorCode: ErrGitConflict, Reason: "PR has merge conflicts"}
	default:
		return mergeDecision{Action: mergeActionWait}
	}
}

func executeMergeCLI(repoDir, targetBranch, approvedHead, prURL string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("%s: gh CLI not found", ErrGitHubUnavailable)
	}
	if targetBranch == "" {
		return fmt.Errorf("precondition: target branch is required")
	}
	if output, err := exec.Command("git", "-C", repoDir, "push", "-u", "origin", targetBranch).CombinedOutput(); err != nil {
		return fmt.Errorf("push feature branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if prURL == "" {
		output, err := exec.Command("gh", "pr", "create", "--head", targetBranch, "--fill").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: create PR: %w: %s", ErrGitHubUnavailable, err, strings.TrimSpace(string(output)))
		}
		prURL = strings.TrimSpace(string(output))
	}
	if approvedHead == "" {
		return fmt.Errorf("precondition: approved head is required before merge")
	}
	output, err := exec.Command("gh", "pr", "merge", prURL, "--merge", "--delete-branch").CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(output)), "conflict") {
			return fmt.Errorf("%s: %s", ErrGitConflict, strings.TrimSpace(string(output)))
		}
		return fmt.Errorf("%s: merge PR: %w: %s", ErrGitHubUnavailable, err, strings.TrimSpace(string(output)))
	}
	return nil
}
