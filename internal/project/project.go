package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ResolveResult is the output of resolving a project name.
type ResolveResult struct {
	Status string `json:"status"` // "existing", "new", "error"
	Path   string `json:"path"`
	Error  string `json:"error,omitempty"`
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
		Projects        []map[string]string `json:"projects"`
		NewProjectRoot  string              `json:"new_project_root"`
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

// RegisterProject adds or updates a project entry in vault-map.json.
// Uses atomic write (tmp → fsync → rename) to prevent corruption.
// Set dryRun to true to preview changes without writing.
func RegisterProject(mapFile, name, path, gitRemote string, dryRun bool) error {
	data, err := os.ReadFile(mapFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", mapFile, err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse %s: %w", mapFile, err)
	}

	projects, _ := config["projects"].([]interface{})
	if projects == nil {
		projects = []interface{}{}
	}

	// Update or append
	updated := false
	for i, p := range projects {
		proj, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if proj["name"] == name {
			projects[i] = map[string]interface{}{
				"name":       name,
				"path":       path,
				"git_remote": gitRemote,
			}
			updated = true
			break
		}
	}
	if !updated {
		projects = append(projects, map[string]interface{}{
			"name":       name,
			"path":       path,
			"git_remote": gitRemote,
		})
	}
	config["projects"] = projects

	newContent, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	newContent = append(newContent, '\n')

	if dryRun {
		fmt.Printf("[DRY RUN] Would write to %s:\n%s\n", mapFile, string(newContent))
		return nil
	}

	return atomicWriteJSON(mapFile, newContent)
}

func atomicWriteJSON(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".otg-register-")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
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
