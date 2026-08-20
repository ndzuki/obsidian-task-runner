package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// writeTeamVaultMapMode writes a vault-map.json with a team project entry
// carrying the given merge_mode.
func writeTeamVaultMapMode(t *testing.T, dir, name, path, mergeMode string) string {
	t.Helper()
	skillDir := filepath.Join(dir, "skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "config"), 0o755); err != nil {
		t.Fatalf("create skill config: %v", err)
	}
	data, err := json.Marshal(map[string]any{
		"notifications": map[string]any{"desktop": false},
		"projects": []map[string]string{
			{"name": name, "path": path, "git_remote": path + ".git",
				"project_type": "team", "merge_mode": mergeMode},
		},
	})
	if err != nil {
		t.Fatalf("marshal vault map: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "config", "vault-map.json"), data, 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	return skillDir
}

// newForkMergeFixture builds a bare origin (the fork) + working repo. main
// carries a base commit (pushed); the feature branch carries one extra commit
// and is NOT pushed — fork-merge delivery never pushes the feature branch.
// The PRIMARY checkout sits on main while the feature branch lives in the
// task worktree (the real daemon layout, TASK-067).
// Returns (repo, origin, taskPath, runner, candidate).
func newForkMergeFixture(t *testing.T, mergeMode string) (string, string, string, *Runner, task.ReadyTask) {
	t.Helper()
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	git(t, "init", "--bare", "-b", "main", origin)
	repo := filepath.Join(dir, "repo")
	git(t, "init", "-b", "main", repo)
	for _, args := range [][]string{
		{"-C", repo, "config", "user.email", "test@example.com"},
		{"-C", repo, "config", "user.name", "Test User"},
		{"-C", repo, "config", "commit.gpgsign", "false"},
		{"-C", repo, "remote", "add", "origin", origin},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", repo, "add", "base.txt")
	git(t, "-C", repo, "commit", "-m", "base")
	git(t, "-C", repo, "push", "-u", "origin", "main")
	git(t, "-C", repo, "checkout", "-b", "task/001-fork")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", repo, "add", "feature.txt")
	git(t, "-C", repo, "commit", "-m", "feature")
	// Detach the primary checkout (the task worktree binds the feature
	// branch; forkMergeDelivery must be able to check out main inside the
	// worktree, which git forbids while the primary checkout holds it).
	git(t, "-C", repo, "checkout", "--detach")
	// Release the feature branch from the primary checkout so the task
	// worktree can bind it.
	// The task worktree carries the feature branch; processMergeTask must
	// find and reuse it via the same taskRunKey the daemon uses.

	vault := filepath.Join(dir, "vault")
	reqDir := filepath.Join(vault, "Projects", "001-team-app", "Requirements")
	tasksDir := filepath.Join(vault, "Projects", "001-team-app", "Tasks")
	if err := os.MkdirAll(reqDir, 0o755); err != nil {
		t.Fatalf("create req dir: %v", err)
	}
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks dir: %v", err)
	}
	reqPath := filepath.Join(reqDir, "REQ-001-x.md")
	if err := os.WriteFile(reqPath, []byte("# REQ\n"), 0o644); err != nil {
		t.Fatalf("write req: %v", err)
	}
	hash, err := hashFile(reqPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-001-x.md")
	content := fmt.Sprintf(`---
id: "001"
title: Fork Task
project: team-app
status: review
merge_approved: true
plan_req_hash: %s
req_doc: Projects/001-team-app/Requirements/REQ-001-x.md
target_branch: task/001-fork
---
# TASK-001
`, hash)
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	// The task worktree carries the feature branch; processMergeTask must
	// find and reuse it via the same taskRunKey the daemon uses.
	t.Setenv("HOME", filepath.Join(dir, "home"))
	if _, err := ensureTaskWorktree(repo, taskRunKey(taskPath), "task/001-fork", ""); err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}

	skillDir := writeTeamVaultMapMode(t, dir, "team-app", repo, mergeMode)
	runner := newTestRunner(skillDir, filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	candidate := task.ReadyTask{
		ID: "001", Title: "Fork Task", Project: "team-app",
		FilePath: taskPath, Status: "review", MergeApproved: true,
		TargetBranch: "task/001-fork",
	}
	return repo, origin, taskPath, runner, candidate
}

// makeForkConflict rewrites base.txt on both main and the feature branch so a
// later `git merge` conflicts, and pushes the new main to the fork origin.
// The feature-side edit happens inside the task worktree (the branch lives
// there, never on the primary checkout).
func makeForkConflict(t *testing.T, worktree, repo string) {
	t.Helper()
	git(t, "-C", repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("main-change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", repo, "commit", "-am", "main change")
	git(t, "-C", repo, "push")
	// Leave the primary checkout off main so forkMergeDelivery can check
	// out main inside the task worktree.
	git(t, "-C", repo, "checkout", "--detach")
	// The feature side changes inside the task worktree, whose gitdir is
	// shared with the primary checkout (same repo), so the branch commit
	// is visible to every worktree.
	if err := os.WriteFile(filepath.Join(worktree, "base.txt"), []byte("feature-change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "commit", "-am", "feature change")
}

// TestProcessMergeTaskForkMergeHappyPath drives the fork-merge delivery: the
// feature branch is merged into the fork's default branch (main) with --no-ff
// and pushed; the task reaches done with merge_status=merged. gh CLI is never
// invoked (marker script).
func TestProcessMergeTaskForkMergeHappyPath(t *testing.T) {
	repo, origin, taskPath, runner, candidate := newForkMergeFixture(t, "fork-merge")
	ghMarker := filepath.Join(t.TempDir(), "gh-called")
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghScript := fmt.Sprintf("#!/bin/sh\n: > %q\nexit 1\n", ghMarker)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if err := runner.processMergeTask(candidate, repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}
	if _, err := os.Stat(ghMarker); err == nil {
		t.Fatal("fork-merge must never invoke gh CLI")
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "done" || fm.MergeStatus != "merged" || fm.Completed == "" {
		t.Fatalf("status=%q merge_status=%q completed=%q, want done/merged/non-empty", fm.Status, fm.MergeStatus, fm.Completed)
	}
	// The fork's main must contain the feature content (via the merge).
	out, err := exec.Command("git", "-C", origin, "show", "main:feature.txt").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "feature") {
		t.Fatalf("fork main missing feature content: %v: %s", err, out)
	}
	// The merge must be a real merge commit (--no-ff), not a fast-forward.
	out, err = exec.Command("git", "-C", origin, "log", "--merges", "--oneline", "main").CombinedOutput()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Fatalf("no merge commit on fork main: %v: %s", err, out)
	}
}

// TestForkMergeConflictAutoResolved: a conflicting feature branch goes
// through the AI conflict-resolution session (fake OMP commits the resolved
// merge), then the fork default branch is pushed and the task completes.
func TestForkMergeConflictAutoResolved(t *testing.T) {
	repo, origin, taskPath, runner, candidate := newForkMergeFixture(t, "fork-merge")
	worktree, err := ensureTaskWorktree(repo, taskRunKey(taskPath), candidate.TargetBranch, "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	makeForkConflict(t, worktree, repo)
	// Fake DSH resolves the in-progress merge by taking the feature side
	// (--theirs = the merged branch) and committing — completing the merge
	// commit, like the real AI session would after editing markers out.
	fake := filepath.Join(t.TempDir(), "fake-dsh-resolve")
	script := `#!/bin/sh
git checkout --theirs base.txt 2>/dev/null || true
git add -A
git commit --no-edit >/dev/null 2>&1
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.cfg.Executor = "dsh"
	runner.cfg.DSHCmd = fake
	runner.phaseExecutor = newDSHExecutorWithProfile(fake, "headless", "")
	// Bump the AI-fix budget so the single conflict resolution fits.
	runner.cfg.MaxAutoMergeFixes = 1

	if err := runner.processMergeTask(candidate, repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "done" || fm.MergeStatus != "merged" {
		t.Fatalf("status=%q merge_status=%q, want done/merged after AI resolution", fm.Status, fm.MergeStatus)
	}
	// The fork main must carry the feature-side change (conflict resolved
	// in the AI commit) — assert the merge result contains both files.
	out, err := exec.Command("git", "-C", origin, "show", "main:base.txt").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "feature-change" {
		t.Fatalf("fork main base.txt = %q (err=%v), want feature-change (AI-resolved)", strings.TrimSpace(string(out)), err)
	}
	out, err = exec.Command("git", "-C", origin, "show", "main:feature.txt").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "feature") {
		t.Fatalf("fork main missing feature.txt: %v: %s", err, out)
	}
}

// TestForkMergeConflictBudgetExhausted: when the AI repair budget is already
// exhausted (0), a merge conflict hands the task straight back to the human —
func TestForkMergeConflictBudgetExhausted(t *testing.T) {
	repo, _, taskPath, runner, candidate := newForkMergeFixture(t, "fork-merge")
	worktree, err := ensureTaskWorktree(repo, taskRunKey(taskPath), candidate.TargetBranch, "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	makeForkConflict(t, worktree, repo)
	runner.cfg.MaxAutoMergeFixes = 0 // no budget at all: AI session never starts
	if err := runner.processMergeTask(candidate, repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}
	data, _ := os.ReadFile(taskPath)
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "conflict" || fm.MergeStatus != "conflict-resolve-attempted" {
		t.Fatalf("status=%q merge_status=%q, want conflict/conflict-resolve-attempted", fm.Status, fm.MergeStatus)
	}
	if fm.MergeApproved {
		t.Fatal("merge_approved must be revoked after budget exhaustion")
	}
}

// TestForkMergeConflictInterruptedKeepsAuthorization: a daemon shutdown
// mid-conflict-resolution must NOT downgrade the task — the merge stays
// authorized (review + merge_approved kept) and resumes after restart,
// mirroring the errConflictResolutionInterrupted semantics of the PR flow.
func TestForkMergeConflictInterruptedKeepsAuthorization(t *testing.T) {
	repo, _, taskPath, runner, candidate := newForkMergeFixture(t, "fork-merge")
	worktree, err := ensureTaskWorktree(repo, taskRunKey(taskPath), candidate.TargetBranch, "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	makeForkConflict(t, worktree, repo)
	runner.cfg.MaxAutoMergeFixes = 1

	// Fake DSH blocks until released (like a real AI session that outlives
	// the daemon); the test cancels the daemon context to kill it.
	releaseFile := filepath.Join(t.TempDir(), "release")
	t.Setenv("RELEASE_FILE", releaseFile)
	fake := filepath.Join(t.TempDir(), "fake-dsh-block")
	script := `#!/bin/sh
while [ ! -f "$RELEASE_FILE" ]; do sleep 0.01; done
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.cfg.Executor = "dsh"
	runner.cfg.DSHCmd = fake
	runner.phaseExecutor = newDSHExecutorWithProfile(fake, "headless", "")
	ctx, cancel := context.WithCancel(context.Background())
	runner.daemonCtx = ctx

	done := make(chan error, 1)
	go func() {
		done <- runner.processMergeTask(candidate, repo)
	}()
	// Wait until the AI conflict-resolution session is in flight (the
	// auto-fix-conflict marker is written right before the session starts).
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(taskPath)
		fm, _ := yamlfrontmatter.Parse(data)
		if fm != nil && fm.MergeStatus == "auto-fix-conflict" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("AI conflict-resolution session never started (no auto-fix-conflict marker)")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Daemon shutdown: cancel the context, killing the execution session.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("interrupted merge must return nil (resumes after restart), got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("processMergeTask hung after daemon-context cancellation")
	}
	data, _ := os.ReadFile(taskPath)
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "review" {
		t.Fatalf("status = %q, want review kept (interrupted merge is not a failure)", fm.Status)
	}
	if !fm.MergeApproved {
		t.Fatal("merge_approved must survive an interrupted conflict resolution")
	}
	// The interruption marker stays — the merge resumes from it after
	// restart, and the failure marker must not be written.
	if fm.MergeStatus != "auto-fix-conflict" {
		t.Fatalf("merge_status = %q, want auto-fix-conflict kept (interrupted mid-resolution)", fm.MergeStatus)
	}
	if fm.MergeStatus == "conflict-resolve-attempted" {
		t.Fatal("interrupted resolution must not mark conflict-resolve-attempted")
	}
}
