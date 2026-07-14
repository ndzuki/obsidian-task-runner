// Package config provides configuration loading from vault-map.json and env vars.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all configuration for the task runner.
type Config struct {
	ObsidianVault   string      `json:"obsidian_vault"`
	NewProjectRoot  string      `json:"new_project_root"`
	Projects        []Project   `json:"projects"`
	Notifications   NotifConfig `json:"notifications"`
	PollIntervalMin int         `json:"poll_interval_minutes"`

	// Models maps assignee keys to OMP model identifiers.
	Models map[string]string `json:"models"`

	OMPCmd string `json:"omp_cmd"`
	LogDir string `json:"log_dir,omitempty"`

	// Skill install dir (not persisted)
	SkillInstallDir string `json:"-"`
}

// Project defines a project mapping.
type Project struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	GitRemote string `json:"git_remote"`
	ProjectID string `json:"project_id"`
}

// NotifConfig holds notification settings.
type NotifConfig struct {
	Desktop bool `json:"desktop"`
}

// DefaultModels returns the built-in model mappings.
func DefaultModels() map[string]string {
	return map[string]string{
		"deepseek": "deepseek/deepseek-v4-pro:xhigh",
		"gpt":      "gateway/gpt-5.5:xhigh",
		"flash":    "deepseek/deepseek-v4-flash",
		"gemini":   "google/gemini-2.5-pro",
		"claude":   "anthropic/claude-sonnet-4-20250514",
		"minimax":  "minimax/minimax-m1",
	}
}

// ModelReference returns a human-readable model reference table.
func ModelReference() string {
	return `| assignee | 模型标识 |
|----------|---------|
| deepseek | deepseek/deepseek-v4-pro:xhigh |
| gpt      | gateway/gpt-5.5:xhigh |
| flash    | deepseek/deepseek-v4-flash |
| gemini   | google/gemini-2.5-pro |
| claude   | anthropic/claude-sonnet-4-20250514 |
| minimax  | minimax/minimax-m1 |

通过 vault-map.json 的 models 字段扩展或覆盖。`
}

// Defaults returns a Config with default values.
func Defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		NewProjectRoot:  filepath.Join(home, "src"),
		PollIntervalMin: 30,
		SkillInstallDir: filepath.Join(home, ".omp", "skills", "obsidian-task-runner"),
		Models:          DefaultModels(),
		OMPCmd:          "omp",
		Notifications:   NotifConfig{Desktop: true},
	}
}

// Load reads vault-map.json and applies env var overrides.
func Load(mapPath string) (*Config, error) {
	cfg := Defaults()

	if mapPath == "" {
		home, _ := os.UserHomeDir()
		mapPath = filepath.Join(home, ".omp", "skills", "obsidian-task-runner", "config", "vault-map.json")
	}

	data, err := os.ReadFile(mapPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", mapPath, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", mapPath, err)
	}

	// Ensure models is never nil
	if cfg.Models == nil {
		cfg.Models = DefaultModels()
	}

	// Env overrides
	if v := os.Getenv("OBSIDIAN_VAULT"); v != "" {
		cfg.ObsidianVault = v
	}
	if v := os.Getenv("OMP_CMD"); v != "" {
		cfg.OMPCmd = v
	}

	return cfg, nil
}

// Model returns the OMP model identifier for an assignee key.
// Falls back to the "flash" model if the assignee is unknown.
func (c *Config) Model(assignee string) string {
	if m, ok := c.Models[assignee]; ok && m != "" {
		return m
	}
	// Fallback to flash
	if flash, ok := c.Models["flash"]; ok {
		return flash
	}
	return "deepseek/deepseek-v4-flash"
}

// FallbackModel returns the fallback model for an assignee.
// If the assignee is "gpt", falls back to "deepseek".
// Returns empty string if no fallback is configured.
func (c *Config) FallbackModel(assignee string) string {
	if assignee == "gpt" {
		if m, ok := c.Models["deepseek"]; ok && m != "" {
			return m
		}
		return "deepseek/deepseek-v4-pro:xhigh"
	}
	return ""
}

// ResolveProject returns the local path for a project name.
func (c *Config) ResolveProject(name string) (string, error) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p.Path, nil
		}
	}
	return "", fmt.Errorf("project %q not found in vault-map", name)
}
