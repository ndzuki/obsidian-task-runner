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
	// Team projects are pre-existing organization repositories (e.g. private
	// Gitea): the daemon must never create a standalone checkout or a remote
	// repo for them. The registered path is authoritative.
	if projectIsTeam(mapFile, t.Project) {
		return fallbackPath, nil
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

func (r *Runner) ensureCheckoutRemoteRepo(t task.ReadyTask, checkout, gitRemote string) {
	// Team projects own their forge repository; never create one via gh.
	mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
	if projectIsTeam(mapFile, t.Project) {
		r.logger.Printf("task %s: team project %q, skipping remote repo creation", t.ID, t.Project)
		return
	}
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
	return projectEntryFromVaultMap(mapFile, projectName)["git_remote"]
}

// projectEntryFromVaultMap returns the vault-map.json entry for a project
// (name/path/git_remote/project_type/merge_mode/conventions_reviewed), or an
// empty map when the project is not registered. Reads the map file directly
// (not the config snapshot) so manual edits are honored immediately.
func projectEntryFromVaultMap(mapFile, projectName string) map[string]string {
	data, err := os.ReadFile(mapFile)
	if err != nil {
		return nil
	}
	var config struct {
		Projects []map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	for _, p := range config.Projects {
		if p["name"] == projectName {
			return p
		}
	}
	return nil
}

// projectMergeMode returns the merge_mode for a project: "manual" (push-only
// delivery, human merges through the forge UI) or "fork-merge" (fork
// development: local merge into the fork default branch, then push). "" means
// the auto GitHub flow. Unknown projects default to auto so nothing changes
// for unregistered/vault-only projects.
func projectMergeMode(mapFile, projectName string) string {
	if entry := projectEntryFromVaultMap(mapFile, projectName); entry != nil {
		return entry["merge_mode"]
	}
	return ""
}

// projectIsTeam reports whether the vault-map entry marks the project as an
// existing organization repository ("team"). Unknown projects are personal.
func projectIsTeam(mapFile, projectName string) bool {
	if entry := projectEntryFromVaultMap(mapFile, projectName); entry != nil {
		return entry["project_type"] == "team"
	}
	return false
}

// projectIsExisting reports whether the project is an EXISTING codebase rather
// than a brand-new (greenfield) scaffold: the vault-map entry exists AND its
// path is an existing directory on disk. This mirrors ResolveProject's
// "existing" semantics — it is the authoritative signal that the project has a
// real checkout to review before further development (004-deployd lesson: new
// features were developed without first reviewing the project architecture —
// dev ran SQLite while test/prod ran MySQL, and schema field-naming drift
// shipped as a bug).
//
// Team projects are a subset (they are registered existing repos). New
// projects are NOT existing until auto-registration materializes a path, so
// the ready→refining fast path is never blocked for greenfield work.
// Unregistered/unknown projects are treated as non-existing (conservative:
// never block a task we cannot even locate).
func projectIsExisting(mapFile, projectName string) bool {
	entry := projectEntryFromVaultMap(mapFile, projectName)
	if entry == nil {
		return false
	}
	path := entry["path"]
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// conventionsReviewed reports whether the project's conventions baseline
// exists — the review artifact itself is the idempotent gate marker:
// `{vault}/Projects/{proj}/Notes/PROJECT-CONVENTIONS.md`. The conventions
// review session creates it; deleting it re-arms the gate for a re-review.
// Projects whose Vault directory cannot be located are treated as reviewed
// (conservative: never block a task on an unlocatable project).
func (r *Runner) conventionsReviewed(project string) bool {
	projDir := resolveVaultProjectDir(r.cfg.ObsidianVault, project)
	if projDir == "" {
		return true
	}
	_, err := os.Stat(filepath.Join(projDir, "Notes", "PROJECT-CONVENTIONS.md"))
	return err == nil
}
