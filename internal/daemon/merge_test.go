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

	"github.com/ndzuki/obsidian-task-runner/internal/config"
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
// a vault with an authorized review task, a git repo on the target branch
// with an origin remote, and a Runner wired to them. gh/git fakes are
// installed separately per test so each scenario controls its own remote
// behavior.
type mergeFixture struct {
	repo      string
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
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "checkout", "-b", mergeFixtureBranch).CombinedOutput(); err != nil {
		t.Fatalf("checkout branch: %v: %s", err, output)
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
	if output, err := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-m", "wip").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/x/y.git").CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, output)
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
		"esac\n" +
		"exec /usr/bin/git \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(gitScript), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	runner := newTestRunner(dir, filepath.Join(dir, "omp"), filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	return &mergeFixture{
		repo:     repo,
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
}

// TestProcessMergeTaskWithRetryStopsOnHardFailure pins that a non-retryable
// failure (already written back as merge_approved=false) exits the wrapper
// immediately without backoff retries.
func TestProcessMergeTaskWithRetryStopsOnHardFailure(t *testing.T) {
	f := newMergeFixture(t)
	// Overwrite the task's target_branch to break validation: the resulting
	// precondition failure must not be retried.
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
