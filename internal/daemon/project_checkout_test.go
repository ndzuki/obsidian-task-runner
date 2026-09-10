package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

// TestProjectIsExisting pins the "existing project" signal used by the
// conventions/architecture gate (004-deployd regression): a project counts as
// existing when its vault-map entry has a path that exists on disk — team or
// not. Registered-but-missing paths and unregistered names must NOT count, so
// the ready→refining fast path is never blocked for greenfield work.
func TestProjectIsExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing-repo")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing-repo")
	skillDir := filepath.Join(dir, "skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []map[string]string{
		{"name": "existing-app", "path": existing},
		{"name": "team-app", "path": existing, "project_type": "team"},
		{"name": "missing-app", "path": missing},
		{"name": "no-path-app"},
	}
	data, err := json.Marshal(map[string]any{"projects": entries})
	if err != nil {
		t.Fatal(err)
	}
	mapFile := filepath.Join(skillDir, "config", "vault-map.json")
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"existing-app": true,  // registered + path exists → existing
		"team-app":     true,  // team projects are existing repos too
		"missing-app":  false, // registered but path missing → not existing
		"no-path-app":  false, // registered without a path → not existing
		"unknown-app":  false, // unregistered → not existing
	} {
		if got := projectIsExisting(mapFile, name); got != want {
			t.Errorf("projectIsExisting(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNormalizeGitRepo(t *testing.T) {
	for _, tt := range []struct {
		in, want string
	}{
		{"git@github.com:ndzuki/demo", "github.com/ndzuki/demo"},
		{"git@github.com:ndzuki/demo.git", "github.com/ndzuki/demo"},
		{"https://github.com/ndzuki/demo", "github.com/ndzuki/demo"},
		{"https://github.com/ndzuki/demo.git", "github.com/ndzuki/demo"},
		{"https://github.com/ndzuki/demo/", "github.com/ndzuki/demo"},
		{"github.com/ndzuki/demo", "github.com/ndzuki/demo"},
		{"ssh://git@github.com/ndzuki/demo", "github.com/ndzuki/demo"},
		{"ssh://git@github.com:22/ndzuki/demo", "github.com/ndzuki/demo"},
		{"https://github.com/NDZUKI/Demo", "github.com/ndzuki/demo"},
		{"git@gitlab.example.com:team/app.git", "gitlab.example.com/team/app"},
	} {
		if got := normalizeGitRepo(tt.in); got != tt.want {
			t.Errorf("normalizeGitRepo(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSameGitRepoTransportSpellings(t *testing.T) {
	a := "git@github.com:ndzuki/demo.git"
	for _, b := range []string{
		"https://github.com/ndzuki/demo",
		"ssh://git@github.com/ndzuki/demo",
		"github.com/ndzuki/demo/",
	} {
		if !sameGitRepo(a, b) {
			t.Errorf("sameGitRepo(%q, %q) = false, want true", a, b)
		}
	}
	if sameGitRepo(a, "git@github.com:ndzuki/myNote.git") {
		t.Error("sameGitRepo must reject a different repository")
	}
}

func TestEnsureGitRemoteRejectsMismatchedOrigin(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	// The vault-fallback failure mode: repoDir sits inside the Vault repo,
	// whose origin is the Vault backup repo — not the project's own remote.
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "git@github.com:ndzuki/myNote.git").CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, out)
	}
	cfg := &config.Config{Projects: []config.Project{{Name: "demo", GitRemote: "github.com/ndzuki/demo"}}}
	err := ensureGitRemote(cfg, repo, "demo")
	if err == nil || !errors.Is(err, errMergeTargetMismatch) {
		t.Fatalf("mismatched origin must be rejected with the mismatch sentinel, got: %v", err)
	}
	if isMergeRetryable(err) {
		t.Fatal("repo mismatch is a permanent defect and must not be retried")
	}
}

func TestEnsureGitRemoteAcceptsMatchingOrigin(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/ndzuki/demo.git").CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, out)
	}
	cfg := &config.Config{Projects: []config.Project{{Name: "demo", GitRemote: "github.com/ndzuki/demo"}}}
	if err := ensureGitRemote(cfg, repo, "demo"); err != nil {
		t.Fatalf("matching origin rejected: %v", err)
	}
	// An origin with no configured git_remote (legacy) keeps working.
	cfgLegacy := &config.Config{Projects: []config.Project{{Name: "demo"}}}
	if err := ensureGitRemote(cfgLegacy, repo, "demo"); err != nil {
		t.Fatalf("legacy origin rejected: %v", err)
	}
}

func TestEnsureGitRemoteAddsConfiguredRemoteWhenOriginMissing(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	cfg := &config.Config{Projects: []config.Project{{Name: "demo", GitRemote: "github.com/ndzuki/demo"}}}
	if err := ensureGitRemote(cfg, repo, "demo"); err != nil {
		t.Fatalf("add origin: %v", err)
	}
	out, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("read origin: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "https://github.com/ndzuki/demo" {
		t.Fatalf("origin = %q, want https://github.com/ndzuki/demo", got)
	}
}

// writeVaultMapWithRemote writes a vault-map.json with a single project entry
// carrying a git_remote, and returns the skill install dir.
func writeVaultMapWithRemote(t *testing.T, dir, name, path, gitRemote string) string {
	t.Helper()
	skillDir := filepath.Join(dir, "skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []map[string]string{{"name": name, "path": path, "git_remote": gitRemote}}
	data, err := json.Marshal(map[string]any{"projects": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "config", "vault-map.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return skillDir
}

// TestEnsureProjectCheckoutPromotesVaultFallback covers the vault-fallback
// regression: a registered project whose path is the Vault project dir (not a
// git root) must be promoted to the conventional standalone checkout so
// worktrees and merges stop targeting the enclosing Vault repository.
func TestEnsureProjectCheckoutPromotesVaultFallback(t *testing.T) {
	dir := t.TempDir()
	// Isolate git identity from the developer's global config.
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\temail = test@example.com\n\tname = Test User\n[commit]\n\tgpgsign = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	// Vault is a real git repo; the project dir lives inside it (fallback).
	vault := createRepository(t, filepath.Join(dir, "vaultroot"))
	projectDir := filepath.Join(vault, "Projects", "010-demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(projectDir, "Requirements", "REQ-001-demo.md")
	if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reqPath, []byte("---\ntitle: 演示\n---\n\n写一个五子棋小游戏的html\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skillDir := writeVaultMapWithRemote(t, dir, "demo", projectDir, "github.com/ndzuki/demo")
	runner := newAutoRegRunner(t, vault, skillDir)
	checkoutRoot := filepath.Join(dir, "repos")
	runner.cfg.NewProjectRoot = checkoutRoot

	// Fake gh: repo view fails until a create happened, then succeeds; the
	// create branch adds origin (mirroring `gh repo create --remote origin`).
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "gh-create-called")
	ghScript := fmt.Sprintf(`#!/bin/sh
marker=%q
case "$1" in
  repo)
    case "$2" in
      view)
        [ -f "$marker" ] && exit 0 || exit 1
        ;;
      create)
        git remote add origin git@github.com:ndzuki/demo.git
        touch "$marker"
        exit 0
        ;;
    esac
    ;;
esac
exit 1
`, marker)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	candidate := task.ReadyTask{
		ID: "001", Title: "Demo", Project: "demo",
		FilePath: filepath.Join(projectDir, "Tasks", "TASK-001-demo.md"),
		ReqDoc:   "Projects/010-demo/Requirements/REQ-001-demo.md",
	}
	checkout, err := runner.ensureProjectCheckout(candidate, projectDir)
	if err != nil {
		t.Fatalf("ensureProjectCheckout: %v", err)
	}
	wantCheckout := filepath.Join(checkoutRoot, "demo")
	if checkout != wantCheckout {
		t.Fatalf("checkout = %q, want %q", checkout, wantCheckout)
	}
	if _, err := os.Stat(filepath.Join(checkout, ".git")); err != nil {
		t.Fatalf("checkout is not a git repo: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(checkout, "README.md"))
	if err != nil {
		t.Fatalf("read checkout README: %v", err)
	}
	if !strings.Contains(string(readme), "演示：写一个五子棋小游戏的html") {
		t.Fatalf("README missing distilled description: %q", readme)
	}
	// vault-map path updated to the standalone checkout.
	for _, p := range readVaultMapProjects(t, filepath.Join(skillDir, "config", "vault-map.json")) {
		if p["name"] == "demo" && p["path"] != checkout {
			t.Fatalf("demo path not updated: %q, want %q", p["path"], checkout)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gh repo create was not invoked: %v", err)
	}
	out, err := exec.Command("git", "-C", checkout, "remote", "get-url", "origin").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "git@github.com:ndzuki/demo.git" {
		t.Fatalf("origin = %q (%v), want git@github.com:ndzuki/demo.git", strings.TrimSpace(string(out)), err)
	}
	logs, err := exec.Command("git", "-C", checkout, "log", "--oneline").CombinedOutput()
	if err != nil || !strings.Contains(string(logs), "chore: initial README") {
		t.Fatalf("checkout initial commit missing: %s (%v)", logs, err)
	}

	// Idempotent: a second call reuses the checkout and does not re-create
	// the remote repo (repo view now succeeds).
	if again, err := runner.ensureProjectCheckout(candidate, projectDir); err != nil || again != checkout {
		t.Fatalf("second ensureProjectCheckout = %q, %v; want %q", again, err, checkout)
	}
	if data, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(data)) != "" {
		t.Fatalf("remote repo re-created on second call: %q (%v)", data, err)
	}
}

// TestEnsureProjectCheckoutInitializesExistingEmptyCheckout covers the
// empty-checkout regression: a project's conventional checkout may already
// exist as an
// empty, non-git directory (user created the folder before the daemon
// promoted it). The daemon must initialize it with a HEAD commit so Round 2
// worktree preparation stops looping on "not a git repository" and the agent
// monitor gets a live NPC/session for the implementing task.
func TestEnsureProjectCheckoutInitializesExistingEmptyCheckout(t *testing.T) {
	dir := t.TempDir()
	// Isolate git identity from the developer's global config.
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\temail = test@example.com\n\tname = Test User\n[commit]\n\tgpgsign = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	vault := filepath.Join(dir, "vault")
	projectDir := filepath.Join(vault, "Projects", "005-dshtui")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(projectDir, "Requirements", "REQ-001-dshtui.md")
	if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reqPath, []byte("---\ntitle: dshtui 基础底座\n---\n\nRust TUI 客户端\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skillDir := writeVaultMapWithRemote(t, dir, "dshtui", projectDir, "github.com/ndzuki/dshtui")
	runner := newAutoRegRunner(t, vault, skillDir)
	checkout := filepath.Join(runner.cfg.NewProjectRoot, "dshtui")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(checkout, ".git")); !os.IsNotExist(err) {
		t.Fatalf("test precondition failed: checkout should start non-git")
	}

	// Fake gh: repo view fails until a create happened, then succeeds.
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "gh-create-called")
	ghScript := fmt.Sprintf(`#!/bin/sh
marker=%q
case "$1" in
  repo)
    case "$2" in
      view)
        [ -f "$marker" ] && exit 0 || exit 1
        ;;
      create)
        git remote add origin git@github.com:ndzuki/dshtui.git
        touch "$marker"
        exit 0
        ;;
    esac
    ;;
esac
exit 1
`, marker)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	candidate := task.ReadyTask{
		ID: "001", Title: "dshtui", Project: "dshtui",
		FilePath: filepath.Join(projectDir, "Tasks", "TASK-001-dshtui-v01-core.md"),
		ReqDoc:   "Projects/005-dshtui/Requirements/REQ-001-dshtui.md",
	}
	got, err := runner.ensureProjectCheckout(candidate, projectDir)
	if err != nil {
		t.Fatalf("ensureProjectCheckout: %v", err)
	}
	if got != checkout {
		t.Fatalf("checkout = %q, want %q", got, checkout)
	}
	if top, err := gitTopLevel(checkout); err != nil || filepath.Clean(top) != filepath.Clean(checkout) {
		t.Fatalf("checkout is not a git root: %q (%v)", top, err)
	}
	logs, err := exec.Command("git", "-C", checkout, "log", "--oneline").CombinedOutput()
	if err != nil || !strings.Contains(string(logs), "chore: initial README") {
		t.Fatalf("checkout initial commit missing: %s (%v)", logs, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gh repo create was not invoked: %v", err)
	}

	// Idempotent: a second call reuses the git checkout and does not re-create
	// the remote (repo view now succeeds).
	if again, err := runner.ensureProjectCheckout(candidate, projectDir); err != nil || again != checkout {
		t.Fatalf("second ensureProjectCheckout = %q, %v; want %q", again, err, checkout)
	}
	if data, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(data)) != "" {
		t.Fatalf("remote repo re-created on second call: %q (%v)", data, err)
	}
}

// TestEnsureProjectCheckoutSkipsGitRoot covers the fast path: a project that
// already resolves to its own repository is never touched.
func TestEnsureProjectCheckoutSkipsGitRoot(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	skillDir := writeVaultMapWithRemote(t, dir, "demo", repo, "github.com/ndzuki/demo")
	runner := newAutoRegRunner(t, dir, skillDir)
	candidate := task.ReadyTask{ID: "001", Project: "demo", ReqDoc: "Projects/010-demo/Requirements/REQ-001.md"}
	got, err := runner.ensureProjectCheckout(candidate, repo)
	if err != nil {
		t.Fatalf("ensureProjectCheckout: %v", err)
	}
	if got != repo {
		t.Fatalf("git-root project must resolve unchanged, got %q", got)
	}
}

// TestEnsureProjectCheckoutCreatesMissingRemoteOnMerge covers the
// missing-remote regression: an existing standalone checkout may have a local
// origin pointing
// at a GitHub repo that does not exist yet. A merge-bound task must auto-create
// the GitHub repo with gh, push the local default branch, and set it as the
// remote default so the later feature-branch PR has a sane base.
func TestEnsureProjectCheckoutCreatesMissingRemoteOnMerge(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\temail = test@example.com\n\tname = Test User\n[commit]\n\tgpgsign = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	repo := createRepository(t, dir)
	// Deterministic default branch name for both local and remote.
	if out, err := exec.Command("git", "-C", repo, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("rename default branch: %v: %s", err, out)
	}
	origin := filepath.Join(dir, "origin.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v: %s", err, out)
	}

	skillDir := writeVaultMapWithRemote(t, dir, "demo", repo, "github.com/ndzuki/demo")
	runner := newAutoRegRunner(t, dir, skillDir)

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "gh-create-called")
	editMarker := filepath.Join(dir, "gh-edit-called")
	ghScript := fmt.Sprintf(`#!/bin/sh
marker=%q
editmarker=%q
origin=%q
checkout=%q
case "$1" in
  repo)
    case "$2" in
      view)
        [ -f "$marker" ] && exit 0 || exit 1
        ;;
      create)
        git -C "$checkout" remote add origin "$origin" 2>/dev/null || git -C "$checkout" remote set-url origin "$origin"
        touch "$marker"
        exit 0
        ;;
      edit)
        touch "$editmarker"
        exit 0
        ;;
    esac
    ;;
esac
exit 1
`, marker, editMarker, origin, repo)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	candidate := task.ReadyTask{
		ID: "001", Title: "Demo", Project: "demo", Status: "review",
		FilePath: filepath.Join(repo, "Tasks", "TASK-001-demo.md"),
		ReqDoc:   "Projects/010-demo/Requirements/REQ-001-demo.md",
	}
	got, err := runner.ensureProjectCheckout(candidate, repo)
	if err != nil {
		t.Fatalf("ensureProjectCheckout: %v", err)
	}
	if got != repo {
		t.Fatalf("git-root merge project must keep checkout, got %q", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gh repo create was not invoked: %v", err)
	}
	if _, err := os.Stat(editMarker); err != nil {
		t.Fatalf("gh repo edit (default-branch) was not invoked: %v", err)
	}
	if out, err := exec.Command("git", "-C", origin, "rev-parse", "--verify", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("origin/main missing after auto-create: %v: %s", err, out)
	}
	remoteURL, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").CombinedOutput()
	if err != nil || strings.TrimSpace(string(remoteURL)) != origin {
		t.Fatalf("origin = %q (%v), want %q", strings.TrimSpace(string(remoteURL)), err, origin)
	}

	// Idempotent: a second call must not re-create the remote or error.
	if again, err := runner.ensureProjectCheckout(candidate, repo); err != nil || again != repo {
		t.Fatalf("second ensureProjectCheckout = %q, %v; want %q", again, err, repo)
	}
}

// TestEnsureProjectCheckoutSkipsVaultOnlyProject covers projects without a
// git_remote: they stay vault-only by choice and keep the fallback path.
func TestEnsureProjectCheckoutSkipsVaultOnlyProject(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(vault, "Projects", "010-demo")
	skillDir := writeVaultMap(t, dir, map[string]string{"demo": projectDir})
	runner := newAutoRegRunner(t, vault, skillDir)
	candidate := task.ReadyTask{ID: "001", Project: "demo"}
	got, err := runner.ensureProjectCheckout(candidate, projectDir)
	if err != nil || got != projectDir {
		t.Fatalf("vault-only project must keep its fallback path, got %q (%v)", got, err)
	}
}

// TestEnsureRemoteDefaultBranchProbeFailureSkipsPush pins the default-branch
// probe regression: when ls-remote cannot reach the remote (network flap, auth
// error), the probe failure must be reported as errRemoteDefaultProbe and must
// NOT fall through to a blind push. A blind push would either fail with a
// confusing network error or be rejected non-fast-forward once connectivity
// returns and the branch turns out to exist.
func TestEnsureRemoteDefaultBranchProbeFailureSkipsPush(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, filepath.Join(dir, "local"))
	if out, err := exec.Command("git", "-C", repo, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("rename default branch: %v: %s", err, out)
	}
	// origin points at a path that does not exist: ls-remote fails without
	// reaching any server, like an unreachable github.com.
	missing := filepath.Join(dir, "missing", "origin.git")
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", missing).CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, out)
	}
	_, editMarker := writeFakeGhScript(t, dir)

	err := ensureRemoteDefaultBranch(repo, "ndzuki/demo")
	if !errors.Is(err, errRemoteDefaultProbe) {
		t.Fatalf("want errRemoteDefaultProbe, got: %v", err)
	}
	if _, statErr := os.Stat(editMarker); statErr == nil {
		t.Fatal("gh repo edit must not run when the probe cannot reach the remote")
	}
}

// TestEnsureRemoteDefaultBranchSkipsPushWhenRemoteHasMain: a remote main that
// is DIVERGED from local main (e.g. origin/main many commits ahead) must be
// left untouched — the probe reports "present" and no push is attempted,
// because any push would be rejected non-fast-forward.
func TestEnsureRemoteDefaultBranchSkipsPushWhenRemoteHasMain(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, filepath.Join(dir, "local"))
	if out, err := exec.Command("git", "-C", repo, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("rename default branch: %v: %s", err, out)
	}
	origin := filepath.Join(dir, "origin.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v: %s", err, out)
	}
	// Seed the remote main with an unrelated history (diverged from local).
	seed := createRepository(t, filepath.Join(dir, "seed"))
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", seed, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("seed add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seed, "commit", "-m", "remote history").CombinedOutput(); err != nil {
		t.Fatalf("seed commit: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seed, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("seed rename: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seed, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("seed add origin: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seed, "push", "-u", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("seed push: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, out)
	}
	remoteBefore, err := exec.Command("git", "-C", origin, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatalf("read remote main: %v", err)
	}
	_, editMarker := writeFakeGhScript(t, dir)

	if err := ensureRemoteDefaultBranch(repo, "ndzuki/demo"); err != nil {
		t.Fatalf("remote already has main, want nil: %v", err)
	}
	if _, err := os.Stat(editMarker); err != nil {
		t.Fatal("gh repo edit (default branch) must still run when main exists")
	}
	remoteAfter, err := exec.Command("git", "-C", origin, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatalf("read remote main after: %v", err)
	}
	if string(remoteBefore) != string(remoteAfter) {
		t.Fatal("remote main must not be touched when it already exists")
	}
}

// TestEnsureRemoteDefaultBranchRecoversWhenPushRejected: a rejected push (the
// branch appeared on the remote between probe and push — a concurrent task or
// a manual push won the race) must re-probe and treat the branch as present
// instead of failing the step.
func TestEnsureRemoteDefaultBranchRecoversWhenPushRejected(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, filepath.Join(dir, "local"))
	if out, err := exec.Command("git", "-C", repo, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("rename default branch: %v: %s", err, out)
	}
	origin := filepath.Join(dir, "origin.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, out)
	}
	// Seed an unrelated ref so the bare origin holds objects the fake push
	// can point refs/heads/main at (update-ref rejects unknown objects).
	seed := createRepository(t, filepath.Join(dir, "seed"))
	if out, err := exec.Command("git", "-C", seed, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("seed add origin: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seed, "push", "origin", "HEAD:refs/heads/other").CombinedOutput(); err != nil {
		t.Fatalf("seed push: %v: %s", err, out)
	}
	seedSha, err := exec.Command("git", "-C", seed, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake git: only intercepts `push` — it materializes refs/heads/main on
	// the remote (as if a concurrent push won) and exits 1 with a
	// non-fast-forward rejection. Everything else delegates to the real git.
	fakeGit := fmt.Sprintf(`#!/bin/sh
real=%q
origin=%q
sha=%q
if [ "$1" = "-C" ] && [ "$3" = "push" ]; then
  "$real" --git-dir="$origin" update-ref refs/heads/main "$sha"
  echo " ! [rejected]        main -> main (non-fast-forward)"
  exit 1
fi
exec "$real" "$@"
`, realGit, origin, strings.TrimSpace(string(seedSha)))
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fakeGit), 0o755); err != nil {
		t.Fatal(err)
	}
	_, editMarker := writeFakeGhScript(t, dir)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if err := ensureRemoteDefaultBranch(repo, "ndzuki/demo"); err != nil {
		t.Fatalf("re-probe must recover from a rejected push, got: %v", err)
	}
	if _, err := os.Stat(editMarker); err != nil {
		t.Fatal("gh repo edit must still run after re-probe recovery")
	}
	if out, err := exec.Command("git", "-C", origin, "rev-parse", "--verify", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("remote main missing after race: %v: %s", err, out)
	}
}

// TestEnsureRemoteDefaultBranchPushFailsDefinitively: a push that fails while
// the branch stays absent (e.g. permission denied) is a definitive error, not
// a transient probe failure.
func TestEnsureRemoteDefaultBranchPushFailsDefinitively(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, filepath.Join(dir, "local"))
	if out, err := exec.Command("git", "-C", repo, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("rename default branch: %v: %s", err, out)
	}
	origin := filepath.Join(dir, "origin.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v: %s", err, out)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Push always fails without creating the branch: definitive failure.
	fakeGit := fmt.Sprintf(`#!/bin/sh
real=%q
if [ "$1" = "-C" ] && [ "$3" = "push" ]; then
  echo "remote: Permission denied"
  exit 1
fi
exec "$real" "$@"
`, realGit)
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fakeGit), 0o755); err != nil {
		t.Fatal(err)
	}
	_, editMarker := writeFakeGhScript(t, dir)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	err = ensureRemoteDefaultBranch(repo, "ndzuki/demo")
	if err == nil {
		t.Fatal("definitive push failure must error")
	}
	if errors.Is(err, errRemoteDefaultProbe) {
		t.Fatalf("definitive push failure must not be classified as transient probe failure: %v", err)
	}
	if !strings.Contains(err.Error(), "push default branch") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(editMarker); statErr == nil {
		t.Fatal("gh repo edit must not run after a definitive push failure")
	}
}

// TestEnsureCheckoutRemoteRepoNotifySuppression pins the caller-level
// behavior: transient probe failures are logged and retried by the next scan
// without a misleading "repo init blocked" desktop notification; definitive
// failures keep notifying.
func TestEnsureCheckoutRemoteRepoNotifySuppression(t *testing.T) {
	t.Run("probe failure does not notify", func(t *testing.T) {
		dir := t.TempDir()
		repo := createRepository(t, filepath.Join(dir, "local"))
		if out, err := exec.Command("git", "-C", repo, "branch", "-M", "main").CombinedOutput(); err != nil {
			t.Fatalf("rename default branch: %v: %s", err, out)
		}
		missing := filepath.Join(dir, "missing", "origin.git")
		if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", missing).CombinedOutput(); err != nil {
			t.Fatalf("add origin: %v: %s", err, out)
		}
		writeFakeGhScript(t, dir)
		notifyLog := filepath.Join(dir, "notify.log")
		writeFakeNotifySend(t, dir, notifyLog)

		runner := &Runner{cfg: &config.Config{
			ObsidianVault: dir, SkillInstallDir: filepath.Join(dir, "skill"),
			Notifications: config.NotifConfig{Desktop: true},
		}, logger: log.New(io.Discard, "", 0)}
		candidate := task.ReadyTask{
			ID: "008", Title: "NFR", Project: "dshtui", Status: "review",
			FilePath: filepath.Join(dir, "TASK-008.md"),
		}
		runner.ensureCheckoutRemoteRepo(candidate, repo, "https://github.com/ndzuki/dshtui")

		data, err := os.ReadFile(notifyLog)
		if err != nil {
			t.Fatalf("read notify log: %v", err)
		}
		if strings.TrimSpace(string(data)) != "" {
			t.Fatalf("transient probe failure must not notify, got: %q", data)
		}
	})
	t.Run("definitive push failure notifies", func(t *testing.T) {
		dir := t.TempDir()
		repo := createRepository(t, filepath.Join(dir, "local"))
		if out, err := exec.Command("git", "-C", repo, "branch", "-M", "main").CombinedOutput(); err != nil {
			t.Fatalf("rename default branch: %v: %s", err, out)
		}
		origin := filepath.Join(dir, "origin.git")
		if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
			t.Fatalf("init bare origin: %v: %s", err, out)
		}
		if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("add origin: %v: %s", err, out)
		}
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(dir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		fakeGit := fmt.Sprintf(`#!/bin/sh
real=%q
if [ "$1" = "-C" ] && [ "$3" = "push" ]; then
  echo "remote: Permission denied"
  exit 1
fi
exec "$real" "$@"
`, realGit)
		if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fakeGit), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFakeGhScript(t, dir)
		notifyLog := filepath.Join(dir, "notify.log")
		writeFakeNotifySend(t, dir, notifyLog)
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

		runner := &Runner{cfg: &config.Config{
			ObsidianVault: dir, SkillInstallDir: filepath.Join(dir, "skill"),
			Notifications: config.NotifConfig{Desktop: true},
		}, logger: log.New(io.Discard, "", 0)}
		candidate := task.ReadyTask{
			ID: "008", Title: "NFR", Project: "dshtui", Status: "review",
			FilePath: filepath.Join(dir, "TASK-008.md"),
		}
		runner.ensureCheckoutRemoteRepo(candidate, repo, "https://github.com/ndzuki/dshtui")

		data, err := os.ReadFile(notifyLog)
		if err != nil {
			t.Fatalf("read notify log: %v", err)
		}
		if !strings.Contains(string(data), "仓库自动初始化受阻") {
			t.Fatalf("definitive failure must notify, got: %q", data)
		}
	})
}

// writeFakeGhScript installs a fake `gh` whose `repo view` always succeeds
// (repo exists) and whose `repo edit` records an invocation marker.
func writeFakeGhScript(t *testing.T, dir string) (binDir, editMarker string) {
	t.Helper()
	binDir = filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	editMarker = filepath.Join(dir, "gh-edit-called")
	script := fmt.Sprintf(`#!/bin/sh
marker=%q
case "$1" in
  repo)
    case "$2" in
      view)
        exit 0
        ;;
      edit)
        touch "$marker"
        exit 0
        ;;
    esac
    ;;
esac
exit 1
`, editMarker)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	return binDir, editMarker
}

// writeFakeNotifySend installs a fake `notify-send` that appends the title to
// a log file, so tests can observe desktop notification attempts.
func writeFakeNotifySend(t *testing.T, dir, logPath string) {
	t.Helper()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" >> %q
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(binDir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create the log so an un-notified run reads as an empty file.
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
