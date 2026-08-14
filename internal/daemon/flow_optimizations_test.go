package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestEvaluateMergeChecksWaitsForMergeableConvergence pins the TASK-067
// lesson: GitHub computes mergeability asynchronously after a push, so a
// SUCCESS check state with a non-MERGEABLE mergeable field must WAIT instead
// of merging immediately (the server then rejects with "not mergeable" and
// the environmental retry budget burns).
func TestEvaluateMergeChecksWaitsForMergeableConvergence(t *testing.T) {
	tests := []struct {
		name      string
		mergeable string
		state     string
		want      mergeAction
	}{
		{name: "converged merges", mergeable: "MERGEABLE", state: "SUCCESS", want: mergeActionMerge},
		{name: "computing waits", mergeable: "UNKNOWN", state: "SUCCESS", want: mergeActionWait},
		{name: "conflicting waits (computed)", mergeable: "CONFLICTING", state: "SUCCESS", want: mergeActionWait},
		{name: "empty mergeable keeps legacy merge", mergeable: "", state: "SUCCESS", want: mergeActionMerge},
		{name: "checks failure still review", mergeable: "MERGEABLE", state: "FAILURE", want: mergeActionReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateMergeChecks("abc", mergeChecks{HeadOID: "abc", State: tt.state, Mergeable: tt.mergeable})
			if got.Action != tt.want {
				t.Fatalf("evaluateMergeChecks(%q,%q) = %q, want %q", tt.state, tt.mergeable, got.Action, tt.want)
			}
		})
	}
}

// TestAutoResolveMergeConflictCircuitBreakerSkipsHugeConflicts pins the
// TASK-067 lesson: a conflict set far beyond what an AI session can resolve
// inside its bounded timeout must not burn the repair budget — it hands the
// task straight back to the user with conflict-resolve-attempted.
func TestAutoResolveMergeConflictCircuitBreakerSkipsHugeConflicts(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	git(t, "init", "-b", "main", repo)
	for _, args := range [][]string{
		{"-C", repo, "config", "user.email", "t@e.com"},
		{"-C", repo, "config", "user.name", "T"},
		{"-C", repo, "config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	// base.txt committed identically on both sides, then diverged in the
	// SAME line so the merge truly conflicts (different-line changes on a
	// single-line file auto-merge, which does not exercise the breaker).
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", repo, "add", "base.txt")
	git(t, "-C", repo, "commit", "-m", "base")
	git(t, "-C", repo, "checkout", "-b", "feat")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", repo, "commit", "-am", "feat")
	git(t, "-C", repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", repo, "commit", "-am", "main")
	// Six more genuinely conflicted files, so the total exceeds the
	// threshold of 5.
	for i := 1; i <= 6; i++ {
		file := fmt.Sprintf("conflict-%d.txt", i)
		if err := os.WriteFile(filepath.Join(repo, file), []byte("line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, "-C", repo, "add", file)
		git(t, "-C", repo, "commit", "-m", "base-"+file)
	}
	git(t, "-C", repo, "checkout", "feat")
	for i := 1; i <= 6; i++ {
		file := fmt.Sprintf("conflict-%d.txt", i)
		// The file exists on main but not on feat: write the diverging
		// version and add it explicitly (commit -am ignores untracked).
		if err := os.WriteFile(filepath.Join(repo, file), []byte("feat\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, "-C", repo, "add", file)
		git(t, "-C", repo, "commit", "-m", "feat-"+file)
	}
	git(t, "-C", repo, "checkout", "main")
	if out, err := exec.Command("git", "-C", repo, "merge", "--no-edit", "feat").CombinedOutput(); err == nil {
		t.Fatalf("merge feat must conflict, got: %s", out)
	}
	if n := countUnmergedFiles(repo); n != 7 {
		t.Fatalf("countUnmergedFiles = %d, want 7 (7 files with same-line divergence)", n)
	}

	taskPath := filepath.Join(dir, "TASK-067.md")
	content := "---\nid: \"067\"\ntitle: Op\nstatus: review\nmerge_approved: true\nmerge_retry_count: 0\n---\n# TASK-067\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse([]byte(content))
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}

	runner := newTestRunner(t.TempDir(), filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner.cfg.MaxAutoFixConflicts = 5 // 7 conflicted files exceed the breaker
	candidate := task.ReadyTask{ID: "067", Title: "Op", FilePath: taskPath, Status: "review", MergeApproved: true}

	if err := runner.autoResolveMergeConflict(candidate, repo, fm, "PR has merge conflicts"); err != nil {
		t.Fatalf("autoResolveMergeConflict: %v", err)
	}
	if got := readFrontmatterField(t, taskPath, "status"); got != "conflict" {
		t.Fatalf("status = %q, want conflict (circuit breaker must skip AI session)", got)
	}
	if got := readFrontmatterField(t, taskPath, "merge_status"); got != "conflict-resolve-attempted" {
		t.Fatalf("merge_status = %q, want conflict-resolve-attempted", got)
	}
	if got := readFrontmatterField(t, taskPath, "merge_retry_count"); got != "0" {
		t.Fatalf("merge_retry_count = %q, want 0 (budget must NOT be spent)", got)
	}
}

// writeConflictFixtureVault builds a vault + registered repo so
// autoCloseMergedConflictPRs can resolve the project checkout.
func writeConflictFixtureVault(t *testing.T, dir, repo string) (vault, skillDir string) {
	t.Helper()
	vault = filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir = filepath.Join(dir, "skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"notifications": map[string]any{"desktop": false},
		"projects": []map[string]string{
			{"name": "001-test", "path": repo, "git_remote": repo + ".git"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "config", "vault-map.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return vault, skillDir
}

// TestAutoCloseMergedConflictPRs pins the TASK-067 manual-merge closure: a
// conflict task (budget exhausted, handed back) whose PR was merged manually
// on the forge converges to done automatically instead of blocking
// downstream blocked_by chains forever.
func TestAutoCloseMergedConflictPRs(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	git(t, "init", "--bare", "-b", "main", origin)
	repo := filepath.Join(dir, "repo")
	git(t, "init", "-b", "main", repo)
	for _, args := range [][]string{
		{"-C", repo, "config", "user.email", "t@e.com"},
		{"-C", repo, "config", "user.name", "T"},
		{"-C", repo, "config", "commit.gpgsign", "false"},
		{"-C", repo, "remote", "add", "origin", origin},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	git(t, "-C", repo, "commit", "--allow-empty", "-m", "base")
	git(t, "-C", repo, "push", "-u", "origin", "main")

	vault, skillDir := writeConflictFixtureVault(t, dir, repo)
	taskPath := filepath.Join(vault, "Projects", "001-test", "Tasks", "TASK-067-x.md")
	content := `---
id: "067"
title: Op
project: 001-test
status: conflict
auto_merge: true
merge_approved: false
merge_retry_count: 3
merge_status: conflict-resolve-attempted
target_branch: task/067-operation-creation-workflow
---
# TASK-067
`
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake gh: findAnyPR returns the PR URL, prState reports MERGED (the
	// human merged it through the forge UI).
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghScript := `#!/bin/sh
case "$1" in
  pr)
    case "$2" in
      list) echo 'https://github.com/x/y/pull/51' ;;
      view) echo 'MERGED' ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	runner := newTestRunner(skillDir, filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	runner.cfg.MaxAutoMergeFixes = 3
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.daemonCtx = ctx

	if n := runner.autoCloseMergedConflictPRs(); n != 1 {
		t.Fatalf("auto-closed = %d, want 1", n)
	}
	if got := readFrontmatterField(t, taskPath, "status"); got != "done" {
		t.Fatalf("status = %q, want done", got)
	}
	if got := readFrontmatterField(t, taskPath, "merge_status"); got != "merged" {
		t.Fatalf("merge_status = %q, want merged", got)
	}
	// Idempotent: a second run closes nothing.
	if n := runner.autoCloseMergedConflictPRs(); n != 0 {
		t.Fatalf("second run auto-closed = %d, want 0 (idempotent)", n)
	}

	// Non-merged PRs stay untouched.
	notMerged := filepath.Join(vault, "Projects", "001-test", "Tasks", "TASK-068-x.md")
	notMergedContent := strings.Replace(content, "id: \"067\"", "id: \"068\"", 1)
	notMergedContent = strings.Replace(notMergedContent, "TASK-067", "TASK-068", 1)
	notMergedContent = strings.Replace(notMergedContent, "target_branch: task/067-operation-creation-workflow", "target_branch: task/068-other", 1)
	if err := os.WriteFile(notMerged, []byte(notMergedContent), 0o644); err != nil {
		t.Fatal(err)
	}
	ghOpenScript := `#!/bin/sh
case "$1" in
  pr)
    case "$2" in
      list) echo 'https://github.com/x/y/pull/52' ;;
      view) echo 'OPEN' ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghOpenScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// The MERGED task is now done, so the probe is a no-op for both tasks.
	if n := runner.autoCloseMergedConflictPRs(); n != 0 {
		t.Fatalf("open PR must not close: auto-closed = %d", n)
	}
	if got := readFrontmatterField(t, notMerged, "status"); got != "conflict" {
		t.Fatalf("open-PR task status = %q, want conflict kept", got)
	}
}

// TestValidateDependencyRefsWarnsStaleUpstream pins the upstream-starvation
// visibility: a non-terminal upstream that has not advanced for longer than
// the threshold triggers a one-time diag notification instead of silently
// blocking its downstream (TASK-067: 019/057/066/069 waited a month+).
func TestValidateDependencyRefsWarnsStaleUpstream(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	upstream := "---\nid: \"019\"\ntitle: Upstream\nstatus: conflict\nupdated: \"" + stale + "\"\n---\n# T\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-019-x.md"), []byte(upstream), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := "---\nid: \"057\"\ntitle: Downstream\nstatus: blocked\nblocked_by:\n  - \"019\"\nupdated: \"" + time.Now().Format(time.RFC3339) + "\"\n---\n# T\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-057-x.md"), []byte(downstream), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault, UpstreamStallDays: 3})
	runner.logger = log.New(io.Discard, "", 0)
	runner.validateDependencyRefs()
	if _, ok := runner.diagNotifyAt.Load("001-test|blocked_by_stale|019"); !ok {
		t.Fatal("stale-upstream warning key must be recorded after the first scan")
	}
	// Idempotent: second scan must not re-notify (key already present).
	runner.validateDependencyRefs()
}
