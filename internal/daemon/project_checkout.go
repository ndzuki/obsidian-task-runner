package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

// ensureProjectCheckout promotes a project whose registered path is a Vault
// fallback directory (not a git root) to the conventional standalone checkout
// at new_project_root/<name>.
//
// Why: a project registered without a standalone checkout (Vault project dir
// fallback) resolves every phase into the enclosing Vault repository. Round 2
// worktrees branch the Vault repo and the merge flow pushes to the Vault's
// origin — silently merging project deliverables into the wrong repository
// (observed: TASK-001-demo merged into the myNote Vault repo instead of
// ndzuki/demo). Promotion restores the documented convention
// (docs/workflow.md 6.5: "path 优先 new_project_root/<name> 的约定 checkout"):
// the project gets its own repository, origin from git_remote, and the
// GitHub remote is created when missing (the registered git_remote is the
// project's declared home).
//
// Idempotent: a promoted project resolves to a git root and short-circuits.
// All failures are logged and return the fallback path — read-only phases
// (refining/planning) never block on repository availability, and the
// merge-time ensureGitRemote guard still refuses wrong-repo pushes.
func (r *Runner) ensureProjectCheckout(t task.ReadyTask, fallbackPath string) (string, error) {
	mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
	gitRemote := gitRemoteForProject(mapFile, t.Project)
	if gitRemote == "" {
		return fallbackPath, nil // vault-only project by choice
	}
	// Already a standalone checkout (or a real repo) — nothing to promote.
	if top, err := gitTopLevel(fallbackPath); err == nil && filepath.Clean(top) == filepath.Clean(fallbackPath) {
		return fallbackPath, nil
	}
	if r.cfg.NewProjectRoot == "" {
		return fallbackPath, fmt.Errorf("new_project_root is empty, cannot promote project %q to a standalone checkout", t.Project)
	}
	checkout := filepath.Join(r.cfg.NewProjectRoot, t.Project)
	if info, err := os.Stat(checkout); err == nil && info.IsDir() {
		// Checkout exists (manual creation or a previous partial promotion):
		// register the path and make sure it is a git repository.
		if err := project.RegisterProject(mapFile, t.Project, checkout, gitRemote, false); err != nil {
			r.logger.Printf("task %s: register promoted checkout path: %v", t.ID, err)
		}
		r.ensureCheckoutRemoteRepo(t, checkout, gitRemote)
		return checkout, nil
	}
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		return fallbackPath, fmt.Errorf("create checkout %s: %w", checkout, err)
	}
	if out, err := exec.Command("git", "-C", checkout, "init", "-b", "main").CombinedOutput(); err != nil {
		return fallbackPath, fmt.Errorf("git init %s: %v: %s", checkout, err, strings.TrimSpace(string(out)))
	}
	// Initial commit so worktree creation (git worktree add -b <branch>
	// <path> HEAD) has a HEAD to branch from.
	readme := fmt.Sprintf("# %s\n\n%s\n\n---\n\n> 由 obsidian-task-runner 自动创建。\n", t.Project, r.projectDescription(t))
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte(readme), 0o644); err != nil {
		return fallbackPath, fmt.Errorf("write checkout README: %w", err)
	}
	if out, err := exec.Command("git", "-C", checkout, "add", "README.md").CombinedOutput(); err != nil {
		return fallbackPath, fmt.Errorf("git add README: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("git", "-C", checkout, "commit", "-m", "chore: initial README").CombinedOutput(); err != nil {
		return fallbackPath, fmt.Errorf("git commit README: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := project.RegisterProject(mapFile, t.Project, checkout, gitRemote, false); err != nil {
		r.logger.Printf("task %s: register promoted checkout path: %v", t.ID, err)
	}
	r.ensureCheckoutRemoteRepo(t, checkout, gitRemote)
	r.logger.Printf("task %s: project %q promoted from Vault fallback dir to standalone checkout %s (git_remote %s)", t.ID, t.Project, checkout, gitRemote)
	return checkout, nil
}

// ensureCheckoutRemoteRepo makes the GitHub repository declared by git_remote
// exist and be reachable as "origin" of the checkout. Creation failures are
// logged, never fatal: the merge flow surfaces push/PR errors with their own
// phase_error, and the README-only repo is harmless if created early.
func (r *Runner) ensureCheckoutRemoteRepo(t task.ReadyTask, checkout, gitRemote string) {
	ownerRepo := normalizeGitRepo(gitRemote)
	if !strings.HasPrefix(ownerRepo, "github.com/") {
		r.logger.Printf("task %s: git_remote %q is not a github.com repo, skipping remote repo creation", t.ID, gitRemote)
		return
	}
	ownerRepo = strings.TrimPrefix(ownerRepo, "github.com/")
	url := gitRemote
	if !strings.Contains(url, "://") && !strings.Contains(url, "@") {
		url = "https://" + url
	}
	if exec.Command("gh", "repo", "view", ownerRepo).Run() == nil {
		// Repo already exists — only origin may be missing (partial setup).
		if _, rerr := exec.Command("git", "-C", checkout, "remote", "get-url", "origin").CombinedOutput(); rerr != nil {
			_ = exec.Command("git", "-C", checkout, "remote", "add", "origin", url).Run()
		}
		return
	}
	// gh ≥2.9x requires --source for --remote; the form works on older gh
	// too, so create from the checkout directory itself.
	args := []string{"repo", "create", ownerRepo, "--private", "--source", ".", "--remote", "origin"}
	if desc := r.projectDescription(t); desc != "" {
		args = append(args, "--description", desc)
	}
	ghCmd := exec.Command("gh", args...)
	ghCmd.Dir = checkout
	if out, gerr := ghCmd.CombinedOutput(); gerr != nil {
		// Race with a concurrent create or manual creation: probe again.
		if exec.Command("gh", "repo", "view", ownerRepo).Run() == nil {
			if _, rerr := exec.Command("git", "-C", checkout, "remote", "get-url", "origin").CombinedOutput(); rerr != nil {
				_ = exec.Command("git", "-C", checkout, "remote", "add", "origin", url).Run()
			}
			return
		}
		r.logger.Printf("task %s: create remote repo %s: %v: %s", t.ID, ownerRepo, gerr, strings.TrimSpace(string(out)))
		return
	}
	r.logger.Printf("task %s: created remote repo %s", t.ID, ownerRepo)
}

// projectDescription derives the one-line project description for the README
// and GitHub --description from the requirement document.
func (r *Runner) projectDescription(t task.ReadyTask) string {
	return distillRequirementDescription(r.cfg.ObsidianVault, t.ReqDoc)
}

// gitRemoteForProject returns the git_remote configured in vault-map.json for
// a project, or "" when unset. Reads the map file directly (not the config
// snapshot) so a promotion sees the latest registration.
func gitRemoteForProject(mapFile, projectName string) string {
	data, err := os.ReadFile(mapFile)
	if err != nil {
		return ""
	}
	var config struct {
		Projects []map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return ""
	}
	for _, p := range config.Projects {
		if p["name"] == projectName {
			return p["git_remote"]
		}
	}
	return ""
}
