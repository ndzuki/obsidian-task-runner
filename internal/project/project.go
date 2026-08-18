package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/jsonorder"
)

// registerMu serializes read-modify-write of vault-map.json so concurrent
// new-project registrations cannot race on project_id.
var registerMu sync.Mutex

// ResolveResult is the output of resolving a project name.
type ResolveResult struct {
	Status string `json:"status"` // "existing", "new", "error"
	Path   string `json:"path"`
	Error  string `json:"error,omitempty"`
}

// nextProjectID computes the next project_id (%03d, max existing + 1).
func nextProjectID(projects []any) string {
	maxID := 0
	for _, p := range projects {
		proj, ok := p.(*jsonorder.OrderedJSON)
		if !ok {
			continue
		}
		if v, ok := proj.Get("project_id"); ok {
			if s, ok := v.(string); ok {
				if n, err := strconv.Atoi(s); err == nil && n > maxID {
					maxID = n
				}
			}
		}
	}
	return fmt.Sprintf("%03d", maxID+1)
}

// GitRemoteFor derives the git remote for a new project from an existing
// entry's owner (e.g. "github.com/ndzuki/release-manager" → owner "ndzuki"),
// producing "github.com/<owner>/<name>". Empty when no existing entry exists.
func GitRemoteFor(mapFile, name string) string {
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
	owner := ""
	for _, p := range config.Projects {
		if remote := p["git_remote"]; remote != "" {
			parts := strings.Split(remote, "/")
			if len(parts) >= 2 {
				owner = parts[len(parts)-2]
				break
			}
		}
	}
	if owner == "" {
		return ""
	}
	return "github.com/" + owner + "/" + name
}

// ResolveProject resolves a project name to a local path using vault-map.json.
// Returns status: "existing" (found + dir exists), "new" (new_project with root), "error".
func ResolveProject(mapFile, projectName string, isNew bool) ResolveResult {
	result := ResolveResult{Status: "error"}

	data, err := os.ReadFile(mapFile)
	if err != nil {
		result.Error = fmt.Sprintf("vault-map.json not found at %s", mapFile)
		return result
	}

	var config struct {
		Projects       []map[string]string `json:"projects"`
		NewProjectRoot string              `json:"new_project_root"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		result.Error = fmt.Sprintf("failed to parse %s: %v", mapFile, err)
		return result
	}

	// Check existing projects
	for _, proj := range config.Projects {
		if proj["name"] == projectName {
			path := proj["path"]
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				result.Status = "existing"
				result.Path = path
				return result
			}
			result.Error = fmt.Sprintf("project %q path %q does not exist on disk", projectName, path)
			return result
		}
	}

	// New project
	if isNew {
		if config.NewProjectRoot == "" {
			result.Error = "new_project_root is not set in vault-map.json"
			return result
		}
		newPath := filepath.Join(config.NewProjectRoot, projectName)
		result.Status = "new"
		result.Path = newPath
		return result
	}

	result.Error = fmt.Sprintf("project %q not found in vault-map.json", projectName)
	return result
}

// ExtractProjectID extracts the numeric project ID from a Vault directory name.
// Format: "NNN-project-name" → "NNN", e.g., "003-obsidian-task-runner" → "003".
// Returns empty string if no numeric prefix found.
func ExtractProjectID(dirName string) string {
	for i, c := range dirName {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 {
			return dirName[:i]
		}
		return ""
	}
	return dirName // all digits, e.g., "123"
}

// MatchVaultDir tries to match a Vault project directory name to a vault-map project key.
// The Vault directory uses format "<id>-<name>" (e.g., "001-release-manager") while
// the vault-map key is typically just "<name>" (e.g., "release-manager").
// Matching order: exact match → strip numeric prefix → no match.
// Returns the matched vault-map project name, or "" if no match found.
func MatchVaultDir(mapFile, vaultDir string) string {
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

	// 1. Exact match
	for _, proj := range config.Projects {
		if proj["name"] == vaultDir {
			return vaultDir
		}
	}

	// 2. Strip numeric prefix (e.g., "001-release-manager" → "release-manager")
	// The prefix is one or more digits followed by a hyphen.
	for i, c := range vaultDir {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 {
			suffix := vaultDir[i+1:]
			for _, proj := range config.Projects {
				if proj["name"] == suffix {
					return proj["name"]
				}
			}
		}
		break
	}

	return ""
}

// RegisterProject adds or updates a project entry in vault-map.json while
// preserving the hand-curated top-level field order (see jsonorder).
// project_id is preserved on update and auto-assigned (%03d, max+1) on append.
// Missing top-level defaults are backfilled; the result is format-validated
// before the atomic write. Set dryRun to preview without writing.
func RegisterProject(mapFile, name, path, gitRemote string, dryRun bool) error {
	registerMu.Lock()
	defer registerMu.Unlock()
	data, err := os.ReadFile(mapFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", mapFile, err)
	}
	obj, err := jsonorder.Parse(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", mapFile, err)
	}

	projects, _ := obj.Get("projects")
	list, _ := projects.([]any)
	if list == nil {
		list = []any{}
	}

	// Update or append
	updated := false
	for i, p := range list {
		proj, ok := p.(*jsonorder.OrderedJSON)
		if !ok {
			continue
		}
		if projectNameOf(proj) == name {
			entry := &jsonorder.OrderedJSON{}
			entry.Set("name", name)
			entry.Set("path", path)
			entry.Set("git_remote", gitRemote)
			// Preserve the existing project_id on update.
			if id, ok := proj.Get("project_id"); ok {
				entry.Set("project_id", id)
			}
			// Preserve hand-curated team/merge settings on update: daemon
			// rewrites (promotion path, new-project registration) must never
			// clobber manual vault-map edits.
			for _, field := range []string{"project_type", "merge_mode"} {
				if v, ok := proj.Get(field); ok {
					entry.Set(field, v)
				}
			}
			list[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		entry := &jsonorder.OrderedJSON{}
		entry.Set("name", name)
		entry.Set("path", path)
		entry.Set("git_remote", gitRemote)
		entry.Set("project_id", nextProjectID(list))
		list = append(list, entry)
	}
	obj.Set("projects", list)
	return writeVaultMap(mapFile, obj, dryRun)
}

// ErrProjectNotFound is returned by UnregisterProject when the project is not
// registered in vault-map.json.
var ErrProjectNotFound = errors.New("project not found in vault-map")

// UnregisterProject removes a project entry from vault-map.json, preserving
// field order, and returns the removed entry's path. Returns ErrProjectNotFound
// (and does not modify the file) when the project is not registered. The caller
// should use the returned path to clean up the project's worktrees before the
// entry disappears — after removal the daemon no longer iterates the project
// and cannot reclaim them.
func UnregisterProject(mapFile, name string) (string, error) {
	registerMu.Lock()
	defer registerMu.Unlock()
	data, err := os.ReadFile(mapFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", mapFile, err)
	}
	obj, err := jsonorder.Parse(data)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", mapFile, err)
	}
	projects, _ := obj.Get("projects")
	list, _ := projects.([]any)
	if list == nil {
		return "", ErrProjectNotFound
	}
	var kept []any
	var removedPath string
	found := false
	for _, p := range list {
		proj, ok := p.(*jsonorder.OrderedJSON)
		if ok && projectNameOf(proj) == name {
			found = true
			if v, ok := proj.Get("path"); ok {
				if s, ok := v.(string); ok {
					removedPath = s
				}
			}
			continue // drop this entry
		}
		kept = append(kept, p)
	}
	if !found {
		return "", ErrProjectNotFound
	}
	obj.Set("projects", kept)
	if err := writeVaultMap(mapFile, obj, false); err != nil {
		return "", err
	}
	return removedPath, nil
}

// projectNameOf returns the "name" field of an ordered project entry.
func projectNameOf(proj *jsonorder.OrderedJSON) string {
	if v, ok := proj.Get("name"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// writeVaultMap backfills missing top-level defaults, marshals preserving
// field order, format-validates, and atomically writes (unless dryRun).
func writeVaultMap(mapFile string, obj *jsonorder.OrderedJSON, dryRun bool) error {
	ensureVaultMapDefaults(obj)
	content, err := jsonorder.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	content = append(content, '\n')
	if !json.Valid(content) {
		return fmt.Errorf("validation failed: produced invalid JSON")
	}
	if dryRun {
		fmt.Printf("[DRY RUN] Would write to %s:\n%s\n", mapFile, string(content))
		return nil
	}
	return jsonorder.AtomicWrite(mapFile, content)
}

// ensureVaultMapDefaults appends top-level fields that are missing from the
// file with their shipped defaults (new features add config fields over time;
// daemon maintenance keeps the file complete without clobbering user values).
// The defaults are parsed through the ordered decoder, so backfilled fields
// keep the Config struct's declaration order — including nested objects —
// instead of JSON-map alphabetical order. "projects" is pinned last: it is
// the most frequently hand-edited section (appending a new project), so
// daemon writes must not bury it under backfilled fields.
func ensureVaultMapDefaults(obj *jsonorder.OrderedJSON) {
	defs, err := json.Marshal(config.Defaults())
	if err != nil {
		return
	}
	defObj, err := jsonorder.Parse(defs)
	if err != nil {
		return
	}
	var projects any
	if v, ok := obj.Get("projects"); ok {
		projects = v
		obj.Delete("projects")
	}
	for _, f := range defObj.Fields() {
		if f.Key == "projects" {
			continue // handled below: pinned last, never backfilled as null
		}
		if _, ok := obj.Get(f.Key); ok {
			continue
		}
		obj.Set(f.Key, f.Value)
	}
	if projects != nil {
		obj.Set("projects", projects)
	}
}
