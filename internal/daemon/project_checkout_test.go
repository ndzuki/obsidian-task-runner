package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
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

// TestEnsureProjectCheckoutPromotesVaultFallback covers the TASK-001-demo
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
// dshtui regression: a project's conventional checkout may already exist as an
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
