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
	ConfigVersion      int         `json:"config_version"`
	ObsidianVault      string      `json:"obsidian_vault"`
	NewProjectRoot     string      `json:"new_project_root"`
	Projects           []Project   `json:"projects"`
	Notifications      NotifConfig `json:"notifications"`
	PollIntervalMin    int         `json:"poll_interval_minutes"`
	MaxConcurrentTasks int         `json:"max_concurrent_tasks"`

	// Models maps assignee keys to OMP model identifiers.
	Models map[string]string `json:"models"`

	OMPCmd string `json:"omp_cmd"`
	LogDir string `json:"log_dir,omitempty"`

	// Phase timeouts (minutes)
	PhaseTimeoutsMinutes map[string]int `json:"phase_timeouts_minutes,omitempty"`
	ShutdownGraceSeconds int            `json:"shutdown_grace_seconds"`

	// Off-peak schedule
	OffPeakTimezone string          `json:"off_peak_timezone,omitempty"`
	OffPeakWindows  []OffPeakWindow `json:"off_peak_windows,omitempty"`

	// Starvation warnings
	StarvationWarningDays map[string]int `json:"starvation_warning_days,omitempty"`

	// Registries
	ScaffoldRegistry map[string]ScaffoldCapability `json:"scaffold_registry,omitempty"`
	TemplateRegistry map[string]interface{}         `json:"template_registry,omitempty"`

	// Skill install dir (not persisted)
	SkillInstallDir string `json:"-"`
}

// OffPeakWindow defines a time window for off-peak scheduling.
type OffPeakWindow struct {
	Start string `json:"start"` // "00:00"
	End   string `json:"end"`   // "09:00"
}

// ScaffoldCapability defines a registered scaffold capability.
type ScaffoldCapability struct {
	Description string   `json:"description"`
	Aliases     []string `json:"aliases,omitempty"`
	Conflicts   []string `json:"conflicts,omitempty"`
	Requires    []string `json:"requires,omitempty"`
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
		"gpt":      "gateway/gpt-5.6-sol:xhigh",
		"default":  "deepseek/deepseek-v4-flash",
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
| gpt      | gateway/gpt-5.6-sol:xhigh |
| default  | deepseek/deepseek-v4-flash |
| gemini   | google/gemini-2.5-pro |
| claude   | anthropic/claude-sonnet-4-20250514 |
| minimax  | minimax/minimax-m1 |

通过 vault-map.json 的 models 字段扩展或覆盖。`
}

// Defaults returns a Config with default values.
func Defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		ConfigVersion:        1,
		NewProjectRoot:       filepath.Join(home, "src"),
		PollIntervalMin:      30,
		MaxConcurrentTasks:   2,
		ShutdownGraceSeconds: 30,
		OffPeakTimezone:      "Asia/Shanghai",
		OffPeakWindows: []OffPeakWindow{
			{Start: "00:00", End: "09:00"},
			{Start: "12:00", End: "14:00"},
			{Start: "18:00", End: "24:00"},
		},
		StarvationWarningDays: map[string]int{"P3": 14, "P4": 30},
		PhaseTimeoutsMinutes: map[string]int{
			"priority": 5,
			"refining": 15,
			"planning": 30,
			"round2":   60,
			"merge":    15,
		},
		SkillInstallDir: filepath.Join(home, ".omp", "skills", "obsidian-task-runner"),
		LogDir:          filepath.Join(home, ".omp", "logs"),
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
// Falls back to the "default" model if the assignee is unknown.
func (c *Config) Model(assignee string) string {
	if m, ok := c.Models[assignee]; ok && m != "" {
		return m
	}
	// Fallback to default
	if defaultModel, ok := c.Models["default"]; ok {
		return defaultModel
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
