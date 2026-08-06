package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ensureRemoteRepository creates the GitHub remote for a new project when the
// task opts in via remote_create=true and no repository_url exists yet.
// Steps: git init (if needed) → gh repo create → record repository_url.
// Failure is surfaced to the caller for blocking; the task keeps its state so
// a later manual fix (or resume) can re-run idempotently (repository_url
// non-empty short-circuits).
func (r *Runner) ensureRemoteRepository(taskPath, repoDir string) error {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return fmt.Errorf("parse task: %w", err)
	}
	if !fm.RemoteCreate || fm.RepositoryURL != "" {
		return nil
	}
	if repoDir == "" {
		return fmt.Errorf("repo dir empty")
	}
	if _, err := os.Stat(repoDir); err != nil {
		return fmt.Errorf("repo dir %s: %w", repoDir, err)
	}

	owner := fm.GitHubOwner
	if owner == "" {
		owner = githubOwnerFromVaultMap(filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json"))
	}
	if owner == "" {
		return fmt.Errorf("github_owner not set and no existing vault-map remote to derive owner from")
	}
	// Repository name uses the project name without the numeric vault prefix
	// ("001-release-manager" → "release-manager"); explicit RepositoryName wins.
	name := fm.RepositoryName
	if name == "" {
		name = stripProjectPrefix(filepath.Base(repoDir))
	}
	visibility := fm.RepositoryVisibility
	if visibility == "" {
		visibility = "private"
	}
	// Description: agent-distilled repository_description (Round 1 writes it
	// from the requirement) wins; daemon-side REQ distillation is a fallback.
	description := fm.RepositoryDescription
	if description == "" {
		description = distillRequirementDescription(r.cfg.ObsidianVault, fm.ReqDoc)
	}

	// Ensure a git repository exists before adding a remote.
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		if out, gerr := exec.Command("git", "-C", repoDir, "init").CombinedOutput(); gerr != nil {
			return fmt.Errorf("git init: %v: %s", gerr, strings.TrimSpace(string(out)))
		}
	}
	// README with the distilled description, committed on the default branch.
	readmePath := filepath.Join(repoDir, "README.md")
	readme := fmt.Sprintf("# %s\n\n%s\n\n---\n\n> 由 obsidian-task-runner 自动创建。\n", name, description)
	if err := os.WriteFile(readmePath, []byte(readme), 0o644); err != nil {
		return fmt.Errorf("write README: %w", err)
	}
	if out, gerr := exec.Command("git", "-C", repoDir, "add", "README.md").CombinedOutput(); gerr != nil {
		return fmt.Errorf("git add README: %v: %s", gerr, strings.TrimSpace(string(out)))
	}
	if out, gerr := exec.Command("git", "-C", repoDir, "commit", "-m", "chore: initial README", "--allow-empty").CombinedOutput(); gerr != nil {
		return fmt.Errorf("git commit README: %v: %s", gerr, strings.TrimSpace(string(out)))
	}

	// gh ≥2.9x rejects --remote without --source; the source form works on
	// older gh too, so create from repoDir itself.
	args := []string{"repo", "create", owner + "/" + name, "--" + visibility, "--source", ".", "--remote", "origin"}
	if description != "" {
		args = append(args, "--description", description)
	}
	ghCmd := exec.Command("gh", args...)
	ghCmd.Dir = repoDir
	out, gerr := ghCmd.CombinedOutput()
	url := "https://github.com/" + owner + "/" + name
	if gerr != nil {
		// Partial failure: the remote may already exist (interrupted between
		// create and frontmatter write, or a prior manual create). Probe with
		// gh repo view; if present, record the URL and continue so a manual
		// resume does not loop on "already exists".
		if exec.Command("gh", "repo", "view", owner+"/"+name).Run() == nil {
			// Ensure origin points at the repo (may be missing if interrupted
			// between create and remote add); otherwise the merge push stage
			// would have no remote to push to.
			if _, rerr := exec.Command("git", "-C", repoDir, "remote", "get-url", "origin").CombinedOutput(); rerr != nil {
				_ = exec.Command("git", "-C", repoDir, "remote", "add", "origin", url).Run()
			}
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"repository_url": url}); err != nil {
				return fmt.Errorf("record repository_url: %w", err)
			}
			return nil
		}
		return fmt.Errorf("gh repo create: %v: %s", gerr, strings.TrimSpace(string(out)))
	}
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"repository_url": url}); err != nil {
		return fmt.Errorf("record repository_url: %w", err)
	}
	return nil
}

// stripProjectPrefix removes the numeric vault prefix ("001-release-manager" →
// "release-manager"); returns the input unchanged when no prefix is present.
func stripProjectPrefix(name string) string {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i > 0 && i < len(name) && name[i] == '-' {
		return name[i+1:]
	}
	return name
}

// distillRequirementDescription derives a one-line project description from
// the requirement doc: frontmatter title + first non-empty summary line,
// truncated to 200 runes for the GitHub API.
func distillRequirementDescription(vaultDir, reqDoc string) string {
	if reqDoc == "" {
		return ""
	}
	reqPath := filepath.Join(vaultDir, reqDoc)
	fm, body, err := parseTaskDoc(reqPath)
	if err != nil || fm == nil {
		return ""
	}
	text := strings.TrimSpace(fm.Title)
	if summary := firstSummaryLine(body); summary != "" {
		if text != "" {
			text += "："
		}
		text += summary
	}
	runes := []rune(text)
	if len(runes) > 200 {
		text = string(runes[:200])
	}
	return text
}

// parseTaskDoc reads a markdown document and returns its parsed frontmatter
// plus the body after the closing frontmatter delimiter.
func parseTaskDoc(path string) (*yamlfrontmatter.Frontmatter, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return fm, "", err
	}
	content := string(data)
	rest := content
	if idx := strings.Index(content, "\n---"); idx >= 0 {
		rest = content[idx+4:]
	}
	if end := strings.Index(rest, "\n---"); end >= 0 {
		rest = rest[end+4:]
	}
	return fm, rest, nil
}

// firstSummaryLine returns the first non-empty body line (skipping headings)
// as the distilled description seed.
func firstSummaryLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "---") {
			continue
		}
		return t
	}
	return ""
}

// githubOwnerFromVaultMap derives the GitHub owner from the first existing
// project's git_remote (e.g. "github.com/ndzuki/x" → "ndzuki").
func githubOwnerFromVaultMap(mapFile string) string {
	data, err := os.ReadFile(mapFile)
	if err != nil {
		return ""
	}
	var cfg struct {
		Projects []map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	for _, p := range cfg.Projects {
		remote := p["git_remote"]
		parts := strings.Split(remote, "/")
		if len(parts) >= 2 && parts[len(parts)-2] != "" {
			return parts[len(parts)-2]
		}
	}
	return ""
}
