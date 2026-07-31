package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

func TestValidateMergeAuthorization(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "REQ-001.md")
	if err := os.WriteFile(req, []byte("# Requirement\n"), 0o644); err != nil {
		t.Fatalf("write REQ: %v", err)
	}
	hash, err := hashFile(req)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	if err := validateMergeAuthorization(mergeAuthorization{Status: "review", MergeApproved: true, ReqPath: req, PlanReqHash: hash, TargetBranch: "task/001"}); err != nil {
		t.Fatalf("valid authorization rejected: %v", err)
	}
	if err := validateMergeAuthorization(mergeAuthorization{Status: "review", MergeApproved: true, PendingReq: true, ReqPath: req, PlanReqHash: hash, TargetBranch: "task/001"}); err == nil {
		t.Fatal("pending requirement must revoke merge authorization")
	}
}

func TestEvaluateMergeChecksRevokesChangedHead(t *testing.T) {
	result := evaluateMergeChecks("abc", mergeChecks{HeadOID: "def", State: "SUCCESS"})
	if result.Action != mergeActionReview || result.ErrorCode != ErrBaseCommitMismatch {
		t.Fatalf("result = %+v", result)
	}
}

func TestMergeCommandRequiresGH(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	t.Setenv("PATH", dir)
	err := executeMergeCLI(&config.Config{}, repo, "test", "task/001", "", "")
	if err == nil || !strings.Contains(err.Error(), string(ErrGitHubUnavailable)) {
		t.Fatalf("error = %v, want GITHUB_UNAVAILABLE", err)
	}
}
