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

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// writeTeamVaultMap writes a vault-map.json whose projects carry the
// manual-mode team shape (project_type=team + merge_mode=manual).
func writeTeamVaultMap(t *testing.T, dir, name, path string) string {
	return writeTeamVaultMapMode(t, dir, name, path, "manual")
}

// newManualMergeFixture builds a real bare origin + working repo pair so the
// manual-mode delivery path (push, default-branch probe, ancestor check) runs
// against genuine git semantics. The working repo sits on the feature branch
// (mirroring the round2 worktree convention).
func newManualMergeFixture(t *testing.T) (repo, origin, taskPath string, runner *Runner, candidate task.ReadyTask, head string) {
	t.Helper()
	dir := t.TempDir()
	origin = filepath.Join(dir, "origin.git")
	git(t, "init", "--bare", "-b", "main", origin)
	repo = filepath.Join(dir, "repo")
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
	git(t, "-C", repo, "commit", "--allow-empty", "-m", "base")
	git(t, "-C", repo, "push", "-u", "origin", "main")
	git(t, "-C", repo, "checkout", "-b", "task/001-manual")
	git(t, "-C", repo, "commit", "--allow-empty", "-m", "feature")
	// The branch is already pushed (the manual delivery pushed it); the
	// probe tests rely on the remote state matching a delivered task.
	git(t, "-C", repo, "push", "-u", "origin", "task/001-manual")
	head = gitCurrentHead(repo, "task/001-manual")
	if head == "" {
		t.Fatal("feature head unavailable")
	}

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
	taskPath = filepath.Join(tasksDir, "TASK-001-x.md")
	content := fmt.Sprintf(`---
id: "001"
title: Team Task
project: team-app
status: review
merge_approved: true
plan_req_hash: %s
req_doc: Projects/001-team-app/Requirements/REQ-001-x.md
target_branch: task/001-manual
---
# TASK-001
`, hash)
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	skillDir := writeTeamVaultMap(t, dir, "team-app", repo)
	runner = newTestRunner(skillDir, filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	candidate = task.ReadyTask{
		ID: "001", Title: "Team Task", Project: "team-app",
		FilePath: taskPath, Status: "review", MergeApproved: true,
		TargetBranch: "task/001-manual",
	}
	return
}

func git(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// TestMergePushCommandPlainSkipsGHCredential pins the manual-mode push
// contract: no gh credential-helper injection — team forges authenticate with
// the checkout's own SSH/https credentials.
func TestMergePushCommandPlainSkipsGHCredential(t *testing.T) {
	_, _, _, runner, _, _ := newManualMergeFixture(t)
	cmd, cancel := runner.mergePushCommandPlain("/tmp/repo", "task/001", false)
	defer cancel()
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "credential.helper") {
		t.Fatalf("plain push must not inject gh credential helper: %q", joined)
	}
	for _, want := range []string{"push", "-u", "origin", "task/001"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("push args %q missing %q", joined, want)
		}
	}
}

// TestProcessMergeTaskManualModePushOnly drives the full manual delivery: the
// branch is pushed to the bare origin with the repo's own credentials, the
// task stays in review with merge_status=pushed, and no gh binary is ever
// consulted — a marker gh proves it (gh absent from PATH would also break git,
// which manual mode genuinely needs).
func TestProcessMergeTaskManualModePushOnly(t *testing.T) {
	repo, origin, taskPath, runner, candidate, _ := newManualMergeFixture(t)
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
		t.Fatal("manual mode must never invoke gh CLI")
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "review" {
		t.Fatalf("status = %q, want review (human merge pending)", fm.Status)
	}
	if fm.MergeStatus != "pushed" {
		t.Fatalf("merge_status = %q, want pushed", fm.MergeStatus)
	}
	if fm.MergeApproved {
		t.Fatal("merge_approved must reset after push (no re-push loop)")
	}
	if fm.ApprovedHead == "" {
		t.Fatal("approved_head must record the pushed head")
	}
	// The branch must actually be on the remote.
	out, err := exec.Command("git", "-C", origin, "for-each-ref", "refs/heads/task/001-manual", "--format=%(objectname)").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != fm.ApprovedHead {
		t.Fatalf("remote branch head = %q (err=%v), want %q", strings.TrimSpace(string(out)), err, fm.ApprovedHead)
	}
}

// TestCheckRemoteMergedAndComplete probes the manual-mode completion signal:
// no merge → false; once the branch lands on the remote default branch
// (simulating a human squash/merge through the forge UI), the task flips to
// done with merge_status=merged and knowledge extraction armed.
func TestCheckRemoteMergedAndComplete(t *testing.T) {
	repo, origin, taskPath, runner, candidate, head := newManualMergeFixture(t)
	// Seed the task with the pushed-state the delivery path wrote.
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"merge_status": "pushed", "approved_head": head, "merge_approved": false,
	}); err != nil {
		t.Fatalf("seed pushed state: %v", err)
	}

	merged, err := runner.checkRemoteMergedAndComplete(candidate, repo)
	if err != nil {
		t.Fatalf("probe before merge: %v", err)
	}
	if merged {
		t.Fatal("probe reported merged before the human merged")
	}

	// Simulate the human merging the branch into main through the forge UI.
	tmp := t.TempDir()
	git(t, "clone", "-q", origin, filepath.Join(tmp, "wt"))
	wt := filepath.Join(tmp, "wt")
	git(t, "-C", wt, "fetch", "origin", "+refs/heads/*:refs/remotes/origin/*")
	git(t, "-C", wt, "checkout", "main")
	git(t, "-C", wt, "merge", "--ff-only", "origin/task/001-manual")
	git(t, "-C", wt, "push")

	merged, err = runner.checkRemoteMergedAndComplete(candidate, repo)
	if err != nil {
		t.Fatalf("probe after merge: %v", err)
	}
	if !merged {
		t.Fatal("probe must report merged after the head reached origin/main")
	}
	data, _ := os.ReadFile(taskPath)
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "done" || fm.MergeStatus != "merged" || fm.Completed == "" {
		t.Fatalf("after merge: status=%q merge_status=%q completed=%q, want done/merged/non-empty", fm.Status, fm.MergeStatus, fm.Completed)
	}
}

func TestRemoteDefaultBranchHonorsNonMain(t *testing.T) {
	repo, origin, _, _, _, _ := newManualMergeFixture(t)
	// Rename the only branch to master: a forge-configured default branch
	// other than main (a HEAD symref to a non-existent branch would make
	// ls-remote return nothing at all).
	git(t, "-C", origin, "branch", "-m", "main", "master")
	got := remoteDefaultBranch(context.Background(), repo)
	if got != "master" {
		t.Fatalf("remoteDefaultBranch = %q, want master", got)
	}
}

// TestCanAutoApproveMergeSkipsPushed prevents the re-push loop: a pushed
// manual delivery must never be re-authorized (the remote-merge probe owns
// completion).
func TestCanAutoApproveMergeSkipsPushed(t *testing.T) {
	base := task.ReadyTask{
		Status: "review", AutoMerge: true, MergeStatus: "pushed",
		PlanReqHash: "sha256:abc", PhaseErrorCode: "",
	}
	if canAutoApproveMerge(base, "sha256:abc", 3) {
		t.Fatal("pushed manual delivery must not be auto re-approved")
	}
	base.MergeStatus = ""
	if !canAutoApproveMerge(base, "sha256:abc", 3) {
		t.Fatal("non-pushed review with clean state must auto-approve")
	}
}

func TestDetectStaleDoneReopensSkipsTeamProject(t *testing.T) {
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
	git(t, "-C", repo, "commit", "--allow-empty", "-m", "base")
	// A real non-ancestor checkpoint: a feature commit that never lands on
	// origin/main. The team skip must keep the task done; without the team
	// marker the same shape must reopen.
	git(t, "-C", repo, "checkout", "-b", "feature")
	git(t, "-C", repo, "commit", "--allow-empty", "-m", "feature")
	featureHead := gitCurrentHead(repo, "feature")
	if featureHead == "" {
		t.Fatal("feature head unavailable")
	}
	git(t, "-C", repo, "checkout", "main")
	skillDir := writeTeamVaultMap(t, dir, "team-app", repo)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-team-app", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-001-x.md")
	content := fmt.Sprintf(`---
id: "001"
title: T
project: team-app
status: done
merge_status: merged
plan_version: 2
checkpoint_commit: %s
reopen_count: 0
pending_req: false
knowledge_extracted: true
---
# T
`, featureHead)
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	runner := newTestRunner(skillDir, filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	if reopened := runner.detectStaleDoneReopens(); reopened != 0 {
		t.Fatalf("team project stale-done reopens = %d, want 0", reopened)
	}
	// The same shape WITHOUT project_type=team must reopen (sanity: the gate
	// is the team marker, not the task shape).
	plainDir := filepath.Join(dir, "plain")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(skillDir, "config", "vault-map.json"))
	var vm map[string]any
	if err := json.Unmarshal(raw, &vm); err != nil {
		t.Fatal(err)
	}
	proj := vm["projects"].([]any)[0].(map[string]any)
	delete(proj, "project_type")
	delete(proj, "merge_mode")
	plainMap, _ := json.Marshal(vm)
	if err := os.WriteFile(filepath.Join(skillDir, "config", "vault-map.json"), plainMap, 0o644); err != nil {
		t.Fatal(err)
	}
	runner2 := newTestRunner(skillDir, filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner2.cfg.ObsidianVault = vault
	// The project dir name must resolve; repo has no origin/main so the
	// checkpoint check is inconclusive and stays conservative — this probe
	// just verifies the team skip is the deciding factor by checking the
	// project is NOT skipped (i.e. the loop reaches the ancestry check).
	// With a genuine non-ancestor + origin/main present, the plain variant
	// reopens; simulate by pushing main so checkpointAncestor can decide.
	git(t, "-C", repo, "remote", "add", "origin", filepath.Join(dir, "plain-origin.git"))
	git(t, "init", "--bare", "-b", "main", filepath.Join(dir, "plain-origin.git"))
	git(t, "-C", repo, "push", "-u", "origin", "main")
	if reopened := runner2.detectStaleDoneReopens(); reopened != 1 {
		t.Fatalf("plain project stale-done reopens = %d, want 1", reopened)
	}
}

// TestRemoteCreateRejectedForTeamProject: remote_create on a team project is
// a configuration error — creating a GitHub repo for an organization's
// existing repository would fork the project into the wrong forge.
func TestRemoteCreateRejectedForTeamProject(t *testing.T) {
	repo, _, taskPath, runner, _, _ := newManualMergeFixture(t)
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"remote_create": true, "github_owner": "me",
	}); err != nil {
		t.Fatalf("seed remote_create: %v", err)
	}
	err := runner.ensureRemoteRepository(taskPath, repo)
	if err == nil || !strings.Contains(err.Error(), "not supported for team project") {
		t.Fatalf("err = %v, want team-project rejection", err)
	}
}

// TestConventionsGateDispatchesReviewAndIdempotency: a team project's first
// task dispatches the conventions review session before refining; once
// Notes/PROJECT-CONVENTIONS.md exists the gate releases and refining runs.
func TestConventionsGateDispatchesReviewAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	git(t, "init", "-b", "main", repo)
	skillDir := writeTeamVaultMap(t, dir, "team-app", repo)
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	argsDir := filepath.Join(dir, "args")
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)
	t.Setenv("ARGS_DIR", argsDir)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-team-app", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-001-x.md")
	content := `---
id: "001"
title: T
project: team-app
status: ready
assignee: default
req_doc: Projects/001-team-app/Requirements/REQ-001-x.md
---
# T
`
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault

	done := runBatch(runner, []task.ReadyTask{{
		ID: "001", Title: "T", Project: "team-app", FilePath: taskPath,
		Status: "ready", Assignee: "default",
	}})
	waitForStartCount(t, startDir, 1)
	waitForArgsFile(t, argsDir)
	releaseBarrier(t, releaseFile)
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	args, err := readSingleArgsFile(t, argsDir)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(args, "/obsidian-task-runner-conventions") {
		t.Fatalf("first team task must dispatch conventions review, got: %s", args)
	}

	// Gate release: create the artifact → the next run goes to refining.
	notesDir := filepath.Join(vault, "Projects", "001-team-app", "Notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "PROJECT-CONVENTIONS.md"), []byte("---\ntitle: conv\n---\n\n# Conventions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	startDir2 := filepath.Join(dir, "starts2")
	argsDir2 := filepath.Join(dir, "args2")
	t.Setenv("START_DIR", startDir2)
	t.Setenv("ARGS_DIR", argsDir2)
	// The first round's runTask triggers a background scan that may hold the
	// repo lock when this batch starts (and may dispatch refining itself);
	// either way the refining session appears in argsDir2. Do not assert the
	// batch's processed count — the scan owns the eventual dispatch.
	done2 := runBatch(runner, []task.ReadyTask{{
		ID: "001", Title: "T", Project: "team-app", FilePath: taskPath,
		Status: "ready", Assignee: "default",
	}})
	waitForArgsFile(t, argsDir2)
	releaseBarrier(t, releaseFile)
	_ = waitForBatch(t, done2)
	args2, err := readSingleArgsFile(t, argsDir2)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if strings.Contains(args2, "/obsidian-task-runner-conventions") {
		t.Fatalf("gate must release after artifact exists, got: %s", args2)
	}
}

// readSingleArgsFile returns the concatenated content of the first file in
// dir (the barrier OMP writes one argv-capture file per invocation, named by
// PID).
func readSingleArgsFile(t *testing.T, dir string) (string, error) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no args file in %s", dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	return string(data), err
}

// TestCheckRemoteMergedAndCompleteIgnoresTransientFetchError: a fetch failure
// (network/credential hiccup) must degrade to "not merged yet" — never revoke
// anything or fail the probe hard.
func TestCheckRemoteMergedAndCompleteIgnoresTransientFetchError(t *testing.T) {
	repo, _, taskPath, runner, candidate, head := newManualMergeFixture(t)
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"merge_status": "pushed", "approved_head": head, "merge_approved": false,
	}); err != nil {
		t.Fatalf("seed pushed state: %v", err)
	}
	// Break the remote so the probe cannot reach it.
	if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(repo, ".git-moved")); err != nil {
		t.Fatal(err)
	}
	merged, err := runner.checkRemoteMergedAndComplete(candidate, repo)
	if err != nil {
		t.Fatalf("probe with broken repo must not hard-fail: %v", err)
	}
	if merged {
		t.Fatal("broken repo must not report merged")
	}
}
