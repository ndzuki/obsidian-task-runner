package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// registerMu serializes read-modify-write of vault-map.json registries so
// concurrent new-project registrations cannot race on project_id or
// scaffold/template entries.
var registerMu sync.Mutex

// ResolveResult is the output of resolving a project name.
type ResolveResult struct {
	Status string `json:"status"` // "existing", "new", "error"
	Path   string `json:"path"`
	Error  string `json:"error,omitempty"`
}

// RegisterScaffoldFromProject grows scaffold_registry from delivered project
// experience: each classified topic that has no matching capability (by key or
// alias) is appended as a new capability, so the registry accumulates proven
// patterns without manual maintenance. Matching topics are left untouched.
// Caller passes classifyADR's matched topics (extraction result).
func RegisterScaffoldFromProject(mapFile, projectName string, topics []string) error {
	if len(topics) == 0 {
		return nil
	}
	registerMu.Lock()
	defer registerMu.Unlock()

	data, err := os.ReadFile(mapFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", mapFile, err)
	}
	obj, err := parseOrderedJSON(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", mapFile, err)
	}

	registry, _ := obj.get("scaffold_registry")
	reg, _ := registry.(*orderedJSON)
	if reg == nil {
		reg = &orderedJSON{}
	}
	changed := false
	// Lowercased key+alias set for matching.
	known := make(map[string]bool)
	for _, f := range reg.fields {
		known[strings.ToLower(f.key)] = true
		if cap, ok := f.value.(*orderedJSON); ok {
			if aliases, ok := cap.get("aliases"); ok {
				if arr, ok := aliases.([]any); ok {
					for _, a := range arr {
						known[strings.ToLower(fmt.Sprint(a))] = true
					}
				}
			}
		}
	}
	// Deterministic order for idempotent-ish writes.
	sorted := append([]string{}, topics...)
	sort.Strings(sorted)
	for _, topic := range sorted {
		key := strings.ToLower(strings.TrimSpace(topic))
		if key == "" || known[key] {
			continue
		}
		cap := &orderedJSON{}
		cap.set("aliases", []any{topic})
		cap.set("conflicts", []any{})
		cap.set("description", "Auto-derived from "+projectName+" (project experience)")
		cap.set("requires", []any{})
		reg.set(key, cap)
		known[key] = true
		changed = true
	}
	if !changed {
		return nil
	}
	obj.set("scaffold_registry", reg)
	return writeVaultMap(mapFile, obj, false)
}

// nextProjectID computes the next project_id (%03d, max existing + 1).
func nextProjectID(projects []any) string {
	maxID := 0
	for _, p := range projects {
		proj, ok := p.(*orderedJSON)
		if !ok {
			continue
		}
		if v, ok := proj.get("project_id"); ok {
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
// preserving the hand-curated top-level field order (see orderedJSON).
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
	obj, err := parseOrderedJSON(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", mapFile, err)
	}

	projects, _ := obj.get("projects")
	list, _ := projects.([]any)
	if list == nil {
		list = []any{}
	}

	// Update or append
	updated := false
	for i, p := range list {
		proj, ok := p.(*orderedJSON)
		if !ok {
			continue
		}
		if projectNameOf(proj) == name {
			entry := &orderedJSON{}
			entry.set("name", name)
			entry.set("path", path)
			entry.set("git_remote", gitRemote)
			// Preserve the existing project_id on update.
			if id, ok := proj.get("project_id"); ok {
				entry.set("project_id", id)
			}
			list[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		entry := &orderedJSON{}
		entry.set("name", name)
		entry.set("path", path)
		entry.set("git_remote", gitRemote)
		entry.set("project_id", nextProjectID(list))
		list = append(list, entry)
	}
	obj.set("projects", list)
	return writeVaultMap(mapFile, obj, dryRun)
}

// projectNameOf returns the "name" field of an ordered project entry.
func projectNameOf(proj *orderedJSON) string {
	if v, ok := proj.get("name"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// writeVaultMap backfills missing top-level defaults, marshals preserving
// field order, format-validates, and atomically writes (unless dryRun).
func writeVaultMap(mapFile string, obj *orderedJSON, dryRun bool) error {
	ensureVaultMapDefaults(obj)
	content, err := marshalOrderedJSON(obj)
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
	return atomicWriteJSON(mapFile, content)
}

// ensureVaultMapDefaults appends top-level fields that are missing from the
// file with their shipped defaults (new features add config fields over time;
// daemon maintenance keeps the file complete without clobbering user values).
// The defaults are parsed through the ordered decoder, so backfilled fields
// keep the Config struct's declaration order — including nested objects —
// instead of JSON-map alphabetical order.
func ensureVaultMapDefaults(obj *orderedJSON) {
	defs, err := json.Marshal(config.Defaults())
	if err != nil {
		return
	}
	defObj, err := parseOrderedJSON(defs)
	if err != nil {
		return
	}
	for _, f := range defObj.fields {
		if _, ok := obj.get(f.key); ok {
			continue
		}
		obj.set(f.key, f.value)
	}
}

func atomicWriteJSON(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".otg-register-")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(data); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			return fmt.Errorf("write temp: %w (close: %v)", err, closeErr)
		}
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			return fmt.Errorf("fsync: %w (close: %v)", err, closeErr)
		}
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
