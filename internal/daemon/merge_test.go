package daemon

import (
	"context"
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

// TestPrIsMerged verifies the merged-state probe used when gh pr merge fails
// during local branch cleanup: a MERGED state must be treated as success.
func TestPrIsMerged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	for _, tt := range []struct {
		name  string
		state string
		want  bool
	}{
		{name: "merged", state: "MERGED", want: true},
		{name: "open", state: "OPEN", want: false},
		{name: "closed unmerged", state: "CLOSED", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			script := "#!/bin/sh\nprintf '%s\\n' '{\"state\":\"" + tt.state + "\"}'\n"
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake gh: %v", err)
			}
			if got := prIsMerged(context.Background(), dir, "https://github.com/x/y/pull/1"); got != tt.want {
				t.Fatalf("prIsMerged = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLoadMergeChecksCheckRunRollup verifies that CheckRun entries in
// statusCheckRollup (which carry status + conclusion, not state) are mapped
// correctly: completed+successful runs must NOT leave the PR stuck in
// PENDING, which previously made every CheckRun look non-successful.
func TestLoadMergeChecksCheckRunRollup(t *testing.T) {
	dir := t.TempDir()
	fakeGH := filepath.Join(dir, "gh")
	if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
cat <<'EOF'
{"headRefOid":"abc123","mergeStateStatus":"UNSTABLE","url":"https://github.com/x/y/pull/1","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS","name":"sdk-check"}]}
EOF
`), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	checks, err := loadMergeChecks(context.Background(), dir, "https://github.com/x/y/pull/1")
	if err != nil {
		t.Fatalf("loadMergeChecks: %v", err)
	}
	if checks.State != "SUCCESS" {
		t.Fatalf("State = %q, want SUCCESS for completed successful CheckRun", checks.State)
	}
	if checks.HeadOID != "abc123" {
		t.Fatalf("HeadOID = %q, want abc123", checks.HeadOID)
	}
}

// TestFindExistingPR verifies the open-PR lookup used before gh pr create:
// an existing PR is reused instead of failing the merge, while "no PR" and
// lookup failures fall through to creation.
func TestFindExistingPR(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	for _, tt := range []struct {
		name   string
		script string
		want   string
	}{
		{
			// gh pr list --jq '.[0].url // empty' already projects the URL
			// client-side; the fake emits gh's projected output verbatim.
			name:   "open PR found",
			script: "#!/bin/sh\nprintf '%s\\n' 'https://github.com/x/y/pull/66'\n",
			want:   "https://github.com/x/y/pull/66",
		},
		{
			name:   "no open PR",
			script: "#!/bin/sh\nexit 0\n",
			want:   "",
		},
		{
			name:   "lookup failure falls through to create",
			script: "#!/bin/sh\nexit 1\n",
			want:   "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(tt.script), 0o755); err != nil {
				t.Fatalf("write fake gh: %v", err)
			}
			if got := findExistingPR(context.Background(), dir, "task/064"); got != tt.want {
				t.Fatalf("findExistingPR = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrURLFromCreateError(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "already exists with URL",
			output: "a pull request for branch \"task/064-rollout-watch-quality\" into branch \"main\" already exists:\n" +
				"https://github.com/ndzuki/release-manager/pull/66\n",
			want: "https://github.com/ndzuki/release-manager/pull/66",
		},
		{
			name:   "unrelated failure has no URL",
			output: "fatal: could not read Username",
			want:   "",
		},
		{name: "empty output", output: "", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := prURLFromCreateError(tt.output); got != tt.want {
				t.Fatalf("prURLFromCreateError = %q, want %q", got, tt.want)
			}
		})
	}
}

// mergeFixture bundles the shared setup for merge-flow end-to-end tests:
// a vault with an authorized review task, a git repo whose PRIMARY checkout
// sits on main while the target branch lives in the task worktree (the real
// daemon layout — merge must run in the round2 worktree, never the main
// checkout, TASK-067), and a Runner wired to them. gh/git fakes are installed
// separately per test so each scenario controls its own remote behavior.
type mergeFixture struct {
	repo      string
	worktree  string
	head      string
	taskPath  string
	runner    *Runner
	candidate task.ReadyTask
}

const mergeFixtureBranch = "task/064-rollout"
const mergeFixturePR = "https://github.com/x/y/pull/66"

func newMergeFixture(t *testing.T) *mergeFixture {
	t.Helper()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqPath := filepath.Join(vault, "Projects", "001-release-manager", "Requirements", "REQ-064.md")
	if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
		t.Fatalf("create req dir: %v", err)
	}
	if err := os.WriteFile(reqPath, []byte("# REQ-064\n"), 0o644); err != nil {
		t.Fatalf("write req: %v", err)
	}
	hash, err := hashFile(reqPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	tasksDir := filepath.Join(vault, "Projects", "001-release-manager", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks dir: %v", err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-064-rollout.md")
	content := fmt.Sprintf(`---
id: "064"
title: Test Rollout
project: release-manager
status: review
merge_approved: true
plan_req_hash: %s
req_doc: Projects/001-release-manager/Requirements/REQ-064.md
target_branch: %s
---
# TASK-064
`, hash, mergeFixtureBranch)
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}

	repo := filepath.Join(dir, "repo")
	if output, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	// Isolate from the developer's global git config: a global
	// commit.gpgsign=true backed by an SSH key fails when the ssh-agent has
	// no identities loaded, which is unrelated to the merge flow under test.
	for _, args := range [][]string{
		{"-C", repo, "config", "user.email", "test@example.com"},
		{"-C", repo, "config", "user.name", "Test User"},
		{"-C", repo, "config", "commit.gpgsign", "false"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	if output, err := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-m", "base").CombinedOutput(); err != nil {
		t.Fatalf("commit base: %v: %s", err, output)
	}
	// Local bare origin (hermetic): the fixture repo must never touch the real
	// GitHub. A remote pointing at a fake https URL leaks `git ls-remote --symref
	// origin HEAD` / `fetch origin` to the network — on a machine with internet
	// git then prompts "Username for 'https://github.com':" and `make test`
	// hangs forever at the terminal (observed). A local bare origin keeps every
	// git probe offline while still resolving a real default branch (main).
	origin := filepath.Join(dir, "origin.git")
	if output, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "push", "-q", "-u", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("push main: %v: %s", err, output)
	}
	// Create the feature branch and its WIP commit, then return the primary
	// checkout to main so the task worktree can bind the feature branch.
	if output, err := exec.Command("git", "-C", repo, "checkout", "-b", mergeFixtureBranch).CombinedOutput(); err != nil {
		t.Fatalf("checkout branch: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-m", "wip").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "checkout", "main").CombinedOutput(); err != nil {
		t.Fatalf("checkout main: %v: %s", err, output)
	}
	// The task worktree is created with the same key the daemon uses, so
	// processMergeTask reuses it instead of creating a second one.
	t.Setenv("HOME", filepath.Join(dir, "home"))
	worktree, err := ensureTaskWorktree(repo, taskRunKey(taskPath), mergeFixtureBranch, "")
	if err != nil {
		t.Fatalf("ensureTaskWorktree: %v", err)
	}
	head := gitCurrentHead(repo, mergeFixtureBranch)
	if head == "" {
		t.Fatal("branch head unavailable")
	}
	// Fake git forwards everything except push (which would hit the network).
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	gitScript := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-C\" ]; then\n" +
		"  cd \"$2\" || exit 1\n" +
		"  shift 2\n" +
		"fi\n" +
		"# Skip -c key=value config pairs (fail-fast push config).\n" +
		"while [ \"$1\" = \"-c\" ]; do\n" +
		"  shift 2\n" +
		"done\n" +
		"case \"$1\" in\n" +
		"  push) exit 0 ;;\n" +
		"  fetch) exit 0 ;;\n" + // rebase pre-push fetch against a fake origin: no remote branch
		"esac\n" +
		"exec /usr/bin/git \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(gitScript), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	runner := newTestRunner(dir, filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	// Merge completion can launch an async knowledge-extraction goroutine
	// that writes under the fixture vault. Wait for it before TempDir
	// cleanup, otherwise the cleanup races the write and flakes with
	// "directory not empty" (Go 1.26 TempDir numbered subdirectories).
	t.Cleanup(func() { waitForTasksIdle(t, runner) })
	return &mergeFixture{
		repo:     repo,
		worktree: worktree,
		head:     head,
		taskPath: taskPath,
		runner:   runner,
		candidate: task.ReadyTask{
			ID: "064", Title: "Test Rollout", Project: "release-manager",
			FilePath: taskPath, Status: "review", MergeApproved: true,
			TargetBranch: mergeFixtureBranch,
		},
	}
}

// installFakeGH writes the fake gh script into the fixture's PATH dir.
// Scripts receive the original gh arguments, so the view branch must
// dispatch on $5 (the --json field list): "state" for prState probes,
// anything else for loadMergeChecks.
func (f *mergeFixture) installFakeGH(t *testing.T, script string) {
	t.Helper()
	// The fake git bin dir is the first PATH entry; reuse it for gh.
	binDir := strings.Split(os.Getenv("PATH"), ":")[0]
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

func gitRevParse(t *testing.T, repo, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", rev).CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v: %s", rev, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestProcessMergeTaskReusesExistingPR covers the recovery loop that kept
// TASK-064 stuck in review: frontmatter pr_url empty while the remote already
// has an open PR for the branch. The merge must adopt the existing PR, record
// its URL, and complete without ever calling gh pr create.
func TestProcessMergeTaskReusesExistingPR(t *testing.T) {
	f := newMergeFixture(t)
	createMarker := filepath.Join(t.TempDir(), "create-called")
	ghScript := fmt.Sprintf(`#!/bin/sh
marker=%q
case "$1" in
  pr)
    case "$2" in
      list)
        echo '%s'
        ;;
      view)
        case "$5" in
          state) echo 'OPEN' ;;
          *) echo '{"headRefOid":"%s","mergeStateStatus":"CLEAN","url":"%s","statusCheckRollup":[]}' ;;
        esac
        ;;
      create)
        : > "$marker"
        echo 'gh pr create must not be called when a PR already exists' >&2
        exit 1
        ;;
      merge)
        exit 0
        ;;
    esac ;;
esac
exit 0
`, createMarker, mergeFixturePR, f.head, mergeFixturePR)
	f.installFakeGH(t, ghScript)

	if err := f.runner.processMergeTask(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}

	fm := mustParse(t, f.taskPath)
	if fm.Status != "done" {
		t.Fatalf("status = %q, want done", fm.Status)
	}
	if fm.PRURL != mergeFixturePR {
		t.Fatalf("pr_url = %q, want existing PR URL", fm.PRURL)
	}
	if fm.MergeStatus != "merged" {
		t.Fatalf("merge_status = %q, want merged", fm.MergeStatus)
	}
	if _, err := os.Stat(createMarker); err == nil {
		t.Fatal("gh pr create was called despite an existing open PR")
	}
}

// TestProcessMergeTaskReopensClosedPR covers the recovery path for a PR that
// exists but is closed (findExistingPR only sees open PRs, so the URL comes
// from gh pr create's "already exists" error): the merge must reopen it and
// complete, instead of stalling on gh pr merge refusing a closed PR.
func TestProcessMergeTaskReopensClosedPR(t *testing.T) {
	f := newMergeFixture(t)
	reopenMarker := filepath.Join(t.TempDir(), "reopened")
	ghScript := fmt.Sprintf(`#!/bin/sh
marker=%q
case "$1" in
  pr)
    case "$2" in
      list)
        exit 0
        ;;
      create)
        echo 'a pull request for branch "task/064-rollout" into branch "main" already exists:'
        echo '%s'
        exit 1
        ;;
      view)
        case "$5" in
          state) echo 'CLOSED' ;;
          *) echo '{"headRefOid":"%s","mergeStateStatus":"CLEAN","url":"%s","statusCheckRollup":[]}' ;;
        esac
        ;;
      reopen)
        : > "$marker"
        exit 0
        ;;
      merge)
        exit 0
        ;;
    esac ;;
esac
exit 0
`, reopenMarker, mergeFixturePR, f.head, mergeFixturePR)
	f.installFakeGH(t, ghScript)

	if err := f.runner.processMergeTask(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}

	fm := mustParse(t, f.taskPath)
	if fm.Status != "done" {
		t.Fatalf("status = %q, want done", fm.Status)
	}
	if fm.PRURL != mergeFixturePR {
		t.Fatalf("pr_url = %q, want recovered PR URL", fm.PRURL)
	}
	if _, err := os.Stat(reopenMarker); err != nil {
		t.Fatalf("closed PR was not reopened: %v", err)
	}
}

// TestProcessMergeTaskReopensClosedFrontmatterPR covers the same closed-PR
// recovery when the task already carries a pr_url in its frontmatter (for
// example a PR closed manually after a previous merge attempt): the state
// check must apply to frontmatter PRs too, not just recovered ones.
func TestProcessMergeTaskReopensClosedFrontmatterPR(t *testing.T) {
	f := newMergeFixture(t)
	if err := yamlfrontmatter.Update(f.taskPath, map[string]interface{}{"pr_url": mergeFixturePR}); err != nil {
		t.Fatalf("set frontmatter pr_url: %v", err)
	}
	reopenMarker := filepath.Join(t.TempDir(), "reopened")
	ghScript := fmt.Sprintf(`#!/bin/sh
marker=%q
case "$1" in
  pr)
    case "$2" in
      list)
        echo 'gh pr list must not be called when pr_url is set' >&2
        exit 1
        ;;
      create)
        echo 'gh pr create must not be called when pr_url is set' >&2
        exit 1
        ;;
      view)
        case "$5" in
          state) echo 'CLOSED' ;;
          *) echo '{"headRefOid":"%s","mergeStateStatus":"CLEAN","url":"%s","statusCheckRollup":[]}' ;;
        esac
        ;;
      reopen)
        : > "$marker"
        exit 0
        ;;
      merge)
        exit 0
        ;;
    esac ;;
esac
exit 0
`, reopenMarker, f.head, mergeFixturePR)
	f.installFakeGH(t, ghScript)

	if err := f.runner.processMergeTask(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}

	fm := mustParse(t, f.taskPath)
	if fm.Status != "done" {
		t.Fatalf("status = %q, want done", fm.Status)
	}
	if _, err := os.Stat(reopenMarker); err != nil {
		t.Fatalf("closed frontmatter PR was not reopened: %v", err)
	}
}

func TestLoadMergeChecksCheckRunPendingAndFailed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	for _, tt := range []struct {
		name   string
		rollup string
		want   string
	}{
		{
			name:   "in-progress check run is pending",
			rollup: `[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":null,"name":"build"}]`,
			want:   "PENDING",
		},
		{
			name:   "failed check run blocks merge",
			rollup: `[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE","name":"test"}]`,
			want:   "FAILURE",
		},
		{
			name:   "legacy status context uses state",
			rollup: `[{"__typename":"StatusContext","state":"SUCCESS","context":"ci"}]`,
			want:   "SUCCESS",
		},
		{
			name:   "skipped check does not block",
			rollup: `[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SKIPPED","name":"lint"}]`,
			want:   "SUCCESS",
		},
		{
			name:   "stale superseded run does not block",
			rollup: `[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"STALE","name":"build"}]`,
			want:   "SUCCESS",
		},
		{
			name:   "startup failure blocks merge",
			rollup: `[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"STARTUP_FAILURE","name":"build"}]`,
			want:   "FAILURE",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			script := "#!/bin/sh\ncat <<'EOF'\n" +
				`{"headRefOid":"abc123","mergeStateStatus":"UNSTABLE","url":"https://github.com/x/y/pull/1","statusCheckRollup":` + tt.rollup + "}\nEOF\n"
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake gh: %v", err)
			}
			checks, err := loadMergeChecks(context.Background(), dir, "https://github.com/x/y/pull/1")
			if err != nil {
				t.Fatalf("loadMergeChecks: %v", err)
			}
			if checks.State != tt.want {
				t.Fatalf("State = %q, want %q", checks.State, tt.want)
			}
		})
	}
}

// TestIsMergeRetryable pins the retry classification: environmental failures
// (network / transient GitHub API errors) retry with backoff, hard failures
// that already revoked merge authorization never do.
func TestIsMergeRetryable(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "push network failure", err: fmt.Errorf("push feature branch: exit status 128: fatal: unable to access"), want: true},
		{name: "sync fetch network failure", err: fmt.Errorf("fetch merge branch: exit status 128: fatal: unable to access"), want: true},
		{name: "sync merge conflict", err: fmt.Errorf("%s: merge onto remote branch: CONFLICT (content)", ErrGitConflict), want: false},
		{name: "create PR unavailable", err: fmt.Errorf("%s: create PR: exit status 1: could not resolve host", ErrGitHubUnavailable), want: true},
		{name: "inspect PR checks unavailable", err: fmt.Errorf("%s: inspect PR checks: exit status 1: network error", ErrGitHubUnavailable), want: true},
		{name: "merge PR unavailable", err: fmt.Errorf("%s: merge PR: exit status 1: rate limited", ErrGitHubUnavailable), want: true},
		{name: "reopen PR unavailable", err: fmt.Errorf("%s: reopen PR: exit status 1: gone", ErrGitHubUnavailable), want: true},
		{name: "gh CLI missing", err: fmt.Errorf("%s: gh CLI not found", ErrGitHubUnavailable), want: false},
		{name: "status precondition", err: fmt.Errorf("precondition: status %q is not mergeable", "done"), want: false},
		{name: "pending req revocation", err: fmt.Errorf("precondition: pending_req revokes merge authorization"), want: false},
		{name: "req hash changed", err: fmt.Errorf("%s: REQ hash changed", ErrBaseCommitMismatch), want: false},
		{name: "req missing", err: fmt.Errorf("%s: requirement missing", ErrReqMissing), want: false},
		{name: "conflict handback", err: fmt.Errorf("%s: PR has conflicts", ErrGitConflict), want: false},
		{name: "merge already in progress", err: fmt.Errorf("merge already in progress (PID 123), skipping duplicate dispatch"), want: false},
		{name: "unexpected PR state", err: fmt.Errorf("%s: unexpected PR state %q", ErrGitHubUnavailable, "BOGUS"), want: false},
		{name: "checks decode failure", err: fmt.Errorf("decode PR checks: unexpected EOF"), want: false},
		{name: "internal", err: fmt.Errorf("%s: unknown merge decision", ErrInternal), want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMergeRetryable(tt.err); got != tt.want {
				t.Fatalf("isMergeRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestProcessMergeTaskWithRetryRecoversFromNetworkFailure proves the retry
// wrapper keeps retrying an environmental failure with backoff until the
// remote recovers: gh pr create fails once (network), then succeeds, and the
// merge completes and writes status=done — without waiting for a scan batch.
func TestProcessMergeTaskWithRetryRecoversFromNetworkFailure(t *testing.T) {
	f := newMergeFixture(t)
	countFile := filepath.Join(t.TempDir(), "create-count")

	ghScript := fmt.Sprintf(`#!/bin/sh
count=%q
case "$1" in
  pr)
    case "$2" in
      create)
        n=0
        [ -f "$count" ] && n=$(cat "$count")
        n=$((n+1))
        echo "$n" > "$count"
        if [ "$n" -eq 1 ]; then
          echo "fatal: unable to access 'https://github.com/': Failed to connect" >&2
          exit 1
        fi
        echo '%s'
        exit 0
        ;;
      view)
        case "$5" in
          state) echo 'OPEN' ;;
          *) echo '{"headRefOid":"%s","mergeStateStatus":"CLEAN","url":"%s","statusCheckRollup":[]}' ;;
        esac
        ;;
    esac
    ;;
esac
exit 0
`, countFile, mergeFixturePR, f.head, mergeFixturePR)
	f.installFakeGH(t, ghScript)

	// Shorten the backoff so the test does not wait real minutes.
	prev := mergeRetryBackoff
	mergeRetryBackoff = time.Millisecond
	t.Cleanup(func() { mergeRetryBackoff = prev })

	if err := f.runner.processMergeTaskWithRetry(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTaskWithRetry: %v", err)
	}

	data, err := os.ReadFile(f.taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "done" {
		t.Fatalf("status = %q, want done (merge must complete after retry)", fm.Status)
	}
	if fm.PRURL != mergeFixturePR {
		t.Fatalf("pr_url = %q, want %q", fm.PRURL, mergeFixturePR)
	}
	createCalls, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read create count: %v", err)
	}
	if got := strings.TrimSpace(string(createCalls)); got != "2" {
		t.Fatalf("gh pr create calls = %s, want 2 (first attempt failed, retry succeeded)", got)
	}
	// Merge completion launches an async knowledge-extraction goroutine that
	// writes under the fixture vault; wait for it before TempDir cleanup, or
	// the cleanup races the write and flakes with "directory not empty".
	waitForTasksIdle(t, f.runner)
}

// TestProcessMergeTaskWithRetryStopsOnHardFailure pins that a non-retryable
// failure (already written back as merge_approved=false) exits the wrapper
// immediately without backoff retries.
func TestProcessMergeTaskWithRetryStopsOnHardFailure(t *testing.T) {
	f := newMergeFixture(t)
	// The TASK-079 heal recovers a missing target_branch from a live task
	// worktree, so remove the worktree first: only then does the cleared
	// target_branch surface as a precondition failure, which must not be
	// retried.
	if output, err := exec.Command("git", "-C", f.repo, "worktree", "remove", "--force", f.worktree).CombinedOutput(); err != nil {
		t.Fatalf("remove task worktree: %v: %s", err, output)
	}
	if err := yamlfrontmatter.Update(f.taskPath, map[string]interface{}{"target_branch": ""}); err != nil {
		t.Fatalf("clear target_branch: %v", err)
	}
	prev := mergeRetryBackoff
	mergeRetryBackoff = time.Millisecond
	t.Cleanup(func() { mergeRetryBackoff = prev })

	err := f.runner.processMergeTaskWithRetry(f.candidate, f.repo)
	if err == nil {
		t.Fatal("processMergeTaskWithRetry: want error, got nil")
	}
	if isMergeRetryable(err) {
		t.Fatalf("precondition failure must not be retryable: %v", err)
	}
}

// TestProcessMergeTaskWithRetryExhaustsRetries pins the retry budget: after
// mergeMaxRetries backoff retries all fail with an environmental error, the
// wrapper returns the last error (merge_approved stays true so the next scan
// batch re-attempts) instead of retrying forever.
func TestProcessMergeTaskWithRetryExhaustsRetries(t *testing.T) {
	f := newMergeFixture(t)
	countFile := filepath.Join(t.TempDir(), "create-count")
	ghScript := fmt.Sprintf(`#!/bin/sh
count=%q
case "$1" in
  pr)
    case "$2" in
      create)
        n=0
        [ -f "$count" ] && n=$(cat "$count")
        n=$((n+1))
        echo "$n" > "$count"
        echo "fatal: unable to access 'https://github.com/': Failed to connect" >&2
        exit 1
        ;;
      view)
        case "$5" in
          state) echo 'OPEN' ;;
          *) echo '{}' ;;
        esac
        ;;
    esac
    ;;
esac
exit 0
`, countFile)
	f.installFakeGH(t, ghScript)

	prevBackoff, prevMax := mergeRetryBackoff, mergeMaxRetries
	mergeRetryBackoff = time.Millisecond
	mergeMaxRetries = 2
	t.Cleanup(func() { mergeRetryBackoff, mergeMaxRetries = prevBackoff, prevMax })

	err := f.runner.processMergeTaskWithRetry(f.candidate, f.repo)
	if err == nil {
		t.Fatal("processMergeTaskWithRetry: want error after retry budget exhausted, got nil")
	}
	if !isMergeRetryable(err) {
		t.Fatalf("last error must stay environmental/retryable: %v", err)
	}
	calls, readErr := os.ReadFile(countFile)
	if readErr != nil {
		t.Fatalf("read create count: %v", readErr)
	}
	if got := strings.TrimSpace(string(calls)); got != "3" {
		t.Fatalf("gh pr create calls = %s, want 3 (initial + %d retries)", got, mergeMaxRetries)
	}
	// merge_approved must survive the exhaustion: the next scan re-attempts.
	data, err := os.ReadFile(f.taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if !fm.MergeApproved {
		t.Fatal("merge_approved must stay true after environmental retry exhaustion")
	}
}

// TestCompleteMergeRefreshesCheckpointFromApprovedHead guards the TASK-065
// 2026-08-28 reopen: completeMerge writes done with the checkpoint_commit
// carried forward to approved_head (the head that actually merged). Round 2's
// checkpoint may be a commit a later rebase dropped; leaving it behind makes
// detectStaleDoneReopens misread the freshly-merged task as an undelivered
// increment and reopen it seconds after merge.
func TestCompleteMergeRefreshesCheckpointFromApprovedHead(t *testing.T) {
	f := newMergeFixture(t)
	const oldCkpt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const approved = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := yamlfrontmatter.Update(f.taskPath, map[string]interface{}{
		"checkpoint_commit": oldCkpt,
		"approved_head":     approved,
	}); err != nil {
		t.Fatalf("set frontmatter: %v", err)
	}
	if err := f.runner.completeMerge(f.candidate, f.repo, mergeFixturePR); err != nil {
		t.Fatalf("completeMerge: %v", err)
	}
	raw, err := os.ReadFile(f.taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		t.Fatalf("parse: %v", err)
	}
	if fm.Status != "done" || fm.MergeStatus != "merged" {
		t.Fatalf("status=%q merge_status=%q, want done/merged", fm.Status, fm.MergeStatus)
	}
	if fm.CheckpointCommit != approved {
		t.Fatalf("checkpoint_commit = %q, want approved_head %q carried forward", fm.CheckpointCommit, approved)
	}
}

// TestCompleteMergeKeepsCheckpointWithoutApprovedHead guards the conservative
// fallback: legacy tasks with no approved_head keep their checkpoint untouched
// (no worse than before — the stale-done check owns reopen semantics).
func TestCompleteMergeKeepsCheckpointWithoutApprovedHead(t *testing.T) {
	f := newMergeFixture(t)
	const oldCkpt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := yamlfrontmatter.Update(f.taskPath, map[string]interface{}{
		"checkpoint_commit": oldCkpt,
		"approved_head":     "",
	}); err != nil {
		t.Fatalf("set frontmatter: %v", err)
	}
	if err := f.runner.completeMerge(f.candidate, f.repo, mergeFixturePR); err != nil {
		t.Fatalf("completeMerge: %v", err)
	}
	raw, err := os.ReadFile(f.taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		t.Fatalf("parse: %v", err)
	}
	if fm.Status != "done" || fm.MergeStatus != "merged" {
		t.Fatalf("status=%q merge_status=%q, want done/merged", fm.Status, fm.MergeStatus)
	}
	if fm.CheckpointCommit != oldCkpt {
		t.Fatalf("checkpoint_commit = %q, want unchanged %q", fm.CheckpointCommit, oldCkpt)
	}
}

// TestProcessMergeTaskAlreadyMergedConverges proves the early convergence
// path: when frontmatter pr_url points at an already-merged PR (manual merge,
// or a run that crashed after merging), the merge writes done with a single
// gh pr view call — no push, no checks, no gh pr merge — so the task is not
// left lingering in review while the daemon re-runs the full merge flow.
func TestProcessMergeTaskAlreadyMergedConverges(t *testing.T) {
	f := newMergeFixture(t)
	if err := yamlfrontmatter.Update(f.taskPath, map[string]interface{}{"pr_url": mergeFixturePR}); err != nil {
		t.Fatalf("set frontmatter pr_url: %v", err)
	}
	// Replace the fake git with a marker variant to prove push is skipped.
	binDir := strings.Split(os.Getenv("PATH"), ":")[0]
	pushMarker := filepath.Join(t.TempDir(), "push-called")
	gitScript := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-C\" ]; then cd \"$2\" || exit 1; shift 2; fi\n" +
		"while [ \"$1\" = \"-c\" ]; do shift 2; done\n" +
		"case \"$1\" in\n" +
		"  push) touch %q; exit 0 ;;\n" +
		"esac\n" +
		"exec /usr/bin/git \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fmt.Sprintf(gitScript, pushMarker)), 0o755); err != nil {
		t.Fatalf("write marker git: %v", err)
	}
	f.installFakeGH(t, `#!/bin/sh
case "$1" in
  pr)
    case "$2" in
      view)
        case "$5" in
          state) echo 'MERGED' ;;
          *) echo '{}' ;;
        esac
        ;;
    esac
    ;;
esac
exit 0
`)

	if err := f.runner.processMergeTask(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}
	data, err := os.ReadFile(f.taskPath)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.Status != "done" || fm.MergeStatus != "merged" {
		t.Fatalf("status = %q merge_status = %q, want done/merged", fm.Status, fm.MergeStatus)
	}
	if _, err := os.Stat(pushMarker); !os.IsNotExist(err) {
		t.Fatal("git push must not run when the PR is already merged")
	}
}

// TestProcessMergeTaskStaleLegacyPRDoesNotConverge guards the done→refining
// loop of TASK-069: a merged PR for the same branch from an EARLIER
// generation (v1, merged before this task's checkpoint existed) must not
// converge the task to done. The early-convergence path verifies the PR's
// merge commit contains the checkpoint; when it does not, the merge falls
// through to the normal path and creates a fresh PR for the current head.
func TestProcessMergeTaskStaleLegacyPRDoesNotConverge(t *testing.T) {
	f := newMergeFixture(t)
	wip := gitRevParse(t, f.repo, mergeFixtureBranch)
	base := gitRevParse(t, f.repo, mergeFixtureBranch+"~1")
	if err := yamlfrontmatter.Update(f.taskPath, map[string]interface{}{
		"pr_url": mergeFixturePR, "checkpoint_commit": wip,
	}); err != nil {
		t.Fatalf("set frontmatter: %v", err)
	}
	createMarker := filepath.Join(t.TempDir(), "create-called")
	ghScript := fmt.Sprintf(`#!/bin/sh
marker=%q
case "$1" in
  pr)
    case "$2" in
      list) : ;;
      view)
        case "$5" in
          state) echo 'MERGED' ;;
          mergeCommit) echo '%s' ;;
          *) echo '{"headRefOid":"%s","mergeStateStatus":"CLEAN","url":"%s","statusCheckRollup":[]}' ;;
        esac
        ;;
      create)
        : > "$marker"
        echo '%s'
        ;;
      merge) exit 0 ;;
    esac ;;
esac
exit 0
`, createMarker, base, f.head, mergeFixturePR, mergeFixturePR)
	f.installFakeGH(t, ghScript)

	if err := f.runner.processMergeTask(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}
	fm := mustParse(t, f.taskPath)
	if fm.Status != "done" || fm.MergeStatus != "merged" {
		t.Fatalf("status = %q merge_status = %q, want done/merged", fm.Status, fm.MergeStatus)
	}
	if _, err := os.Stat(createMarker); err != nil {
		t.Fatal("stale legacy PR must not converge; a fresh PR must be created")
	}
}

// TestProcessMergeTaskMergedPRWithCheckpointConverges pins the positive side
// of the checkpoint gate: when the merged PR's merge commit actually contains
// the task checkpoint, convergence to done stays fast (no push, no create).
func TestProcessMergeTaskMergedPRWithCheckpointConverges(t *testing.T) {
	f := newMergeFixture(t)
	wip := gitRevParse(t, f.repo, mergeFixtureBranch)
	if err := yamlfrontmatter.Update(f.taskPath, map[string]interface{}{
		"pr_url": mergeFixturePR, "checkpoint_commit": wip,
	}); err != nil {
		t.Fatalf("set frontmatter: %v", err)
	}
	binDir := strings.Split(os.Getenv("PATH"), ":")[0]
	pushMarker := filepath.Join(t.TempDir(), "push-called")
	gitScript := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-C\" ]; then cd \"$2\" || exit 1; shift 2; fi\n" +
		"while [ \"$1\" = \"-c\" ]; do shift 2; done\n" +
		"case \"$1\" in\n" +
		"  push) touch %q; exit 0 ;;\n" +
		"esac\n" +
		"exec /usr/bin/git \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fmt.Sprintf(gitScript, pushMarker)), 0o755); err != nil {
		t.Fatalf("write marker git: %v", err)
	}
	f.installFakeGH(t, fmt.Sprintf(`#!/bin/sh
case "$1" in
  pr)
    case "$2" in
      view)
        case "$5" in
          state) echo 'MERGED' ;;
          mergeCommit) echo '%s' ;;
          *) echo '{}' ;;
        esac
        ;;
    esac ;;
esac
exit 0
`, wip))

	if err := f.runner.processMergeTask(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}
	fm := mustParse(t, f.taskPath)
	if fm.Status != "done" || fm.MergeStatus != "merged" {
		t.Fatalf("status = %q merge_status = %q, want done/merged", fm.Status, fm.MergeStatus)
	}
	if _, err := os.Stat(pushMarker); !os.IsNotExist(err) {
		t.Fatal("git push must not run when the merged PR contains the checkpoint")
	}
}

// TestMergePushUsesGHCredential 钉住 push 凭据契约：merge 流程的 git push
// 必须经 gh CLI credential helper（`gh auth git-credential`）认证——与
// 创建/合并 PR 使用同一身份。仅通过 gh keyring/SSH 认证的机器没有
// ambient https 凭据，裸 push 会烧光重试预算（TASK-004：5/5 次重试
// 全部失败 "could not read Username"）。
func TestMergePushUsesGHCredential(t *testing.T) {
	f := newMergeFixture(t)
	argsMarker := filepath.Join(t.TempDir(), "git-args")
	// 记录每次 git 调用，非 push 调用转发给真实 git。fixture 的 fake git
	// 必须替换，才能观察 push 参数。
	gitScript := "#!/bin/sh\n" +
		"echo \"$@\" >> " + argsMarker + "\n" +
		"if [ \"$1\" = \"-C\" ]; then\n" +
		"  cd \"$2\" || exit 1\n" +
		"  shift 2\n" +
		"fi\n" +
		"while [ \"$1\" = \"-c\" ]; do\n" +
		"  shift 2\n" +
		"done\n" +
		"case \"$1\" in\n" +
		"  push) exit 0 ;;\n" +
		"  fetch) exit 0 ;;\n" + // rebase pre-push fetch against a fake origin: no remote branch
		"esac\n" +
		"exec /usr/bin/git \"$@\"\n"
	binDir := strings.Split(os.Getenv("PATH"), ":")[0]
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(gitScript), 0o755); err != nil {
		t.Fatalf("write recording fake git: %v", err)
	}
	ghScript := fmt.Sprintf(`#!/bin/sh
case "$1" in
  pr)
    case "$2" in
      list)
        echo ''
        ;;
      view)
        case "$5" in
          state) echo 'OPEN' ;;
          *) echo '{"headRefOid":"%s","mergeStateStatus":"CLEAN","url":"%s","statusCheckRollup":[]}' ;;
        esac
        ;;
      create)
        echo '%s'
        ;;
      merge)
        exit 0
        ;;
    esac ;;
esac
exit 0
`, f.head, mergeFixturePR, mergeFixturePR)
	f.installFakeGH(t, ghScript)

	if err := f.runner.processMergeTask(f.candidate, f.repo); err != nil {
		t.Fatalf("processMergeTask: %v", err)
	}
	args, err := os.ReadFile(argsMarker)
	if err != nil {
		t.Fatalf("read git args marker: %v", err)
	}
	if !strings.Contains(string(args), "credential.helper=!gh auth git-credential") {
		t.Fatalf("git invocations = %q, want push authenticated via gh credential helper", string(args))
	}
}

// TestSyncMergeBranch guards the pre-push sync: a local branch behind its
// remote counterpart must be merged so the push is a fast-forward (TASK-067:
// non-fast-forward rejections burned all 5 retries); a rewritten local
// history whose stale remote head is absorbed into main force-pushes
// (TASK-051/059: remote held the old WIP snapshot while local carried the
// v4 re-implementation); a stale MERGE_HEAD from a failed run is aborted
// before any new merge. First-push (no remote branch) and already-in-sync
// cases are no-ops; conflicting merges surface as ErrGitConflict with the
// conflict state preserved for the AI auto-fix session.
func TestSyncMergeBranch(t *testing.T) {
	git := func(dir string, args ...string) string {
		t.Helper()
		args = append([]string{"-C", dir}, args...)
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	if out, err := exec.Command("git", "init", "--bare", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "init", work).CombinedOutput(); err != nil {
		t.Fatalf("init work: %v: %s", err, out)
	}
	git(work, "config", "user.email", "t@t")
	git(work, "config", "user.name", "t")
	git(work, "config", "commit.gpgsign", "false")
	git(work, "remote", "add", "origin", origin)
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) {
		t.Helper()
		git(work, "add", "-A")
		git(work, "commit", "-m", msg)
	}
	ctx := context.Background()

	// First push: no remote branch yet — sync is a no-op, no force.
	writeFile("a.txt", "a\n")
	commit("base")
	git(work, "branch", "-M", "task/foo")
	git(work, "push", "-u", "origin", "task/foo")
	force, err := syncMergeBranch(ctx, work, "task/foo")
	if err != nil {
		t.Fatalf("first-push sync: %v", err)
	}
	if force {
		t.Fatal("first push must not force")
	}

	// Remote ahead of local (local is an ancestor): three-way merge brings
	// the remote head in, no force.
	writeFile("b.txt", "b\n")
	commit("local change")
	git(work, "push", "origin", "task/foo")
	other := filepath.Join(dir, "other")
	if out, err := exec.Command("git", "clone", origin, other).CombinedOutput(); err != nil {
		t.Fatalf("clone other: %v: %s", err, out)
	}
	git(other, "config", "user.email", "o@o")
	git(other, "config", "user.name", "o")
	git(other, "config", "commit.gpgsign", "false")
	git(other, "checkout", "-b", "task/foo", "origin/task/foo")
	if err := os.WriteFile(filepath.Join(other, "a.txt"), []byte("a\nremote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(other, "add", "-A")
	git(other, "commit", "-m", "remote change")
	git(other, "push", "origin", "task/foo")
	// Local worktree is now one commit behind the remote.
	force, err = syncMergeBranch(ctx, work, "task/foo")
	if err != nil {
		t.Fatalf("sync onto remote: %v", err)
	}
	if force {
		t.Fatal("merge-onto-remote sync must not force")
	}
	if got := git(work, "log", "--oneline", "-5"); !strings.Contains(got, "remote change") {
		t.Fatalf("remote commit not in local history after sync:\n%s", got)
	}
	if got := git(work, "log", "--oneline", "-5"); !strings.Contains(got, "local change") {
		t.Fatalf("local commit not in local history after sync:\n%s", got)
	}
	git(work, "push", "origin", "task/foo")

	// Conflicting / diverged history scenarios on a separate branch.
	git(work, "checkout", "-b", "task/conflict")
	if err := os.WriteFile(filepath.Join(work, "c.txt"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-m", "conflict base")
	git(work, "push", "-u", "origin", "task/conflict")
	// Local edits the line but does not push yet.
	if err := os.WriteFile(filepath.Join(work, "c.txt"), []byte("same\nlocal-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-m", "local same line")
	// Remote diverges on the same line from the shared base.
	git(other, "fetch", "origin")
	git(other, "checkout", "-b", "task/conflict", "origin/task/conflict")
	if err := os.WriteFile(filepath.Join(other, "c.txt"), []byte("same\nremote-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(other, "add", "-A")
	git(other, "commit", "-m", "remote same line")
	git(other, "push", "origin", "task/conflict")
	// Diverged history where the local branch re-implements every file the
	// remote head changed exclusively → the local rewrite supersedes the
	// stale remote work, force-push.
	force, err = syncMergeBranch(ctx, work, "task/conflict")
	if err != nil {
		t.Fatalf("diverged-covered sync: %v", err)
	}
	if !force {
		t.Fatal("diverged history with remote changes re-implemented locally must force")
	}

	// A stale in-progress merge (left by a failed AI session) is aborted by
	// the next sync before any new merge runs. Manufacture the state by
	// attempting the conflicting merge directly.
	if out, err := exec.Command("git", "-C", work, "merge", "origin/task/conflict").CombinedOutput(); err == nil {
		t.Fatalf("expected conflicting merge, got success: %s", out)
	}
	if !mergeInProgress(work) {
		t.Fatal("mergeInProgress must report the in-progress merge")
	}
	// Sync a different, in-sync branch: the stale MERGE_HEAD must be
	// aborted first and the sync must proceed cleanly.
	force, err = syncMergeBranch(ctx, work, "task/foo")
	if err != nil {
		t.Fatalf("sync with stale MERGE_HEAD: %v", err)
	}
	if force {
		t.Fatal("clean sync must not force")
	}
	if mergeInProgress(work) {
		t.Fatal("stale MERGE_HEAD not cleared by sync")
	}

	// Rewritten history: the stale remote head's exclusive file changes are
	// re-implemented by the local branch → force-push.
	baseSHA := git(work, "rev-parse", "HEAD~4")
	git(work, "checkout", "-b", "task/rewrite", baseSHA)
	if err := os.WriteFile(filepath.Join(work, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-m", "stale wip")
	git(work, "push", "-u", "origin", "task/rewrite")
	// Fresh implementation rewrites the branch from the same base: it
	// re-implements wip.txt (the stale remote's only exclusive change) and
	// adds its own files, so the stale head is fully superseded.
	git(work, "reset", "--hard", baseSHA)
	if err := os.WriteFile(filepath.Join(work, "wip.txt"), []byte("wip\nv4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "v4.txt"), []byte("v4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-m", "v4 rewrite")
	force, err = syncMergeBranch(ctx, work, "task/rewrite")
	if err != nil {
		t.Fatalf("rewritten-history sync: %v", err)
	}
	if !force {
		t.Fatal("rewritten history with remote changes re-implemented locally must force")
	}

	// Guard: when the remote head touches a file the local rewrite never
	// changed, the force is refused — never drop unknown remote work.
	git(work, "checkout", "-b", "task/guard", baseSHA)
	if err := os.WriteFile(filepath.Join(work, "remote-only.txt"), []byte("r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-m", "remote-only work")
	git(work, "push", "-u", "origin", "task/guard")
	git(work, "reset", "--hard", baseSHA)
	if err := os.WriteFile(filepath.Join(work, "local.txt"), []byte("l\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "-A")
	git(work, "commit", "-m", "local rewrite")
	force, err = syncMergeBranch(ctx, work, "task/guard")
	if err == nil || !strings.Contains(err.Error(), string(ErrGitConflict)) {
		t.Fatalf("uncovered remote work sync = %v, want ErrGitConflict", err)
	}
	if force {
		t.Fatal("uncovered remote work must not force")
	}
}

// TestMergeRejectsLoggedOutGH 钉住认证预检：gh 已安装但未登录时，merge
// 不得发起任何远程操作——撤销授权、写 review + phase_error（附精确补救
// `gh auth login`）、通知用户，而不是在凭据提示上烧光重试预算。
func TestMergeRejectsLoggedOutGH(t *testing.T) {
	f := newMergeFixture(t)
	// gh 二进制存在但 `gh auth status` 失败（未登录）；任何 PR 操作
	// 都不应被触达。
	f.installFakeGH(t, `#!/bin/sh
case "$1" in
  auth)
    case "$2" in
      status) echo 'not logged in' >&2; exit 1 ;;
    esac ;;
esac
echo 'gh pr/list/view/create/merge must not run without authentication' >&2
exit 1
`)

	err := f.runner.processMergeTask(f.candidate, f.repo)
	if err == nil {
		t.Fatal("processMergeTask: want error for logged-out gh")
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("error = %v, want gh auth login guidance", err)
	}
	fm := mustParse(t, f.taskPath)
	if fm.Status != "review" || fm.MergeApproved {
		t.Fatalf("status = %q merge_approved = %v, want review/false", fm.Status, fm.MergeApproved)
	}
	if fm.PhaseErrorCode != string(ErrGitHubUnavailable) {
		t.Fatalf("phase_error_code = %q, want %q", fm.PhaseErrorCode, ErrGitHubUnavailable)
	}
	if !strings.Contains(fm.PhaseError, "gh auth login") {
		t.Fatalf("phase_error = %q, want gh auth login guidance", fm.PhaseError)
	}
}
