// Package config provides configuration loading from vault-map.json and env vars.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds all configuration for the task runner.
type Config struct {
	ConfigVersion       int         `json:"config_version"`
	ObsidianVault       string      `json:"obsidian_vault"`
	NewProjectRoot      string      `json:"new_project_root"`
	Projects            []Project   `json:"projects"`
	Notifications       NotifConfig `json:"notifications"`
	PollIntervalMin     int         `json:"poll_interval_minutes"`
	MaxConcurrentTasks  int         `json:"max_concurrent_tasks"`
	PhaseTimeoutMinutes map[string]int `json:"phase_timeouts_minutes"`
	ShutdownGraceSeconds int        `json:"shutdown_grace_seconds"`
	OffPeakTimezone      string     `json:"off_peak_timezone"`
	OffPeakWindows       []TimeWindow `json:"off_peak_windows"`
	StarvationWarningDays map[string]int `json:"starvation_warning_days"`
	Models map[string]string `json:"models"`
	OMPCmd string `json:"omp_cmd"`
	LogDir string `json:"log_dir,omitempty"`
	SkillInstallDir string `json:"-"`
	ConfigPath      string `json:"-"`
}

type TimeWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
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
		PhaseTimeoutMinutes: map[string]int{"priority": 5, "refining": 15, "planning": 30, "round2": 60, "merge": 15},
		ShutdownGraceSeconds: 30,
		OffPeakTimezone:      "Asia/Shanghai",
		OffPeakWindows: []TimeWindow{{Start: "00:00", End: "09:00"}, {Start: "12:00", End: "14:00"}, {Start: "18:00", End: "24:00"}},
		StarvationWarningDays: map[string]int{"P3": 14, "P4": 30},
		SkillInstallDir:      filepath.Join(home, ".omp", "skills", "obsidian-task-runner"),
		Models:               DefaultModels(),
		OMPCmd:               "omp",
		Notifications:        NotifConfig{Desktop: true},
	}
}

// Load reads vault-map.json and applies env var overrides.
func Load(mapPath string) (*Config, error) {
	cfg := Defaults()
	if mapPath == "" {
		home, _ := os.UserHomeDir()
		mapPath = filepath.Join(home, ".omp", "skills", "obsidian-task-runner", "config", "vault-map.json")
	}
	cfg.ConfigPath = mapPath

	data, err := os.ReadFile(mapPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", mapPath, err)
		}
	} else if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", mapPath, err)
	}
	mergeDefaults(cfg)
	applyEnvironment(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func mergeDefaults(cfg *Config) {
	defaults := Defaults()
	if cfg.ConfigVersion == 0 {
		cfg.ConfigVersion = defaults.ConfigVersion
	}
	if cfg.PollIntervalMin == 0 {
		cfg.PollIntervalMin = defaults.PollIntervalMin
	}
	if cfg.MaxConcurrentTasks == 0 {
		cfg.MaxConcurrentTasks = defaults.MaxConcurrentTasks
	}
	if cfg.PhaseTimeoutMinutes == nil {
		cfg.PhaseTimeoutMinutes = defaults.PhaseTimeoutMinutes
	} else {
		for phase, value := range defaults.PhaseTimeoutMinutes {
			if cfg.PhaseTimeoutMinutes[phase] == 0 {
				cfg.PhaseTimeoutMinutes[phase] = value
			}
		}
	}
	if cfg.ShutdownGraceSeconds == 0 {
		cfg.ShutdownGraceSeconds = defaults.ShutdownGraceSeconds
	}
	if cfg.OffPeakTimezone == "" {
		cfg.OffPeakTimezone = defaults.OffPeakTimezone
	}
	if len(cfg.OffPeakWindows) == 0 {
		cfg.OffPeakWindows = defaults.OffPeakWindows
	}
	if cfg.StarvationWarningDays == nil {
		cfg.StarvationWarningDays = defaults.StarvationWarningDays
	}
	if cfg.Models == nil {
		cfg.Models = DefaultModels()
	}
	if cfg.OMPCmd == "" {
		cfg.OMPCmd = defaults.OMPCmd
	}
	if cfg.SkillInstallDir == "" {
		cfg.SkillInstallDir = defaults.SkillInstallDir
	}
}

func applyEnvironment(cfg *Config) {
	if value := firstNonEmptyEnv("OTG_OBSIDIAN_VAULT", "OBSIDIAN_VAULT"); value != "" {
		cfg.ObsidianVault = value
	}
	if value := firstNonEmptyEnv("OTG_OMP_CMD", "OMP_CMD"); value != "" {
		cfg.OMPCmd = value
	}
	if value := os.Getenv("OTG_MAX_CONCURRENT_TASKS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.MaxConcurrentTasks = parsed
		}
	}
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func (c *Config) Validate() error {
	if c.ConfigVersion != 1 {
		return fmt.Errorf("CONFIG_INVALID: unsupported config_version %d", c.ConfigVersion)
	}
	if c.MaxConcurrentTasks < 1 {
		return fmt.Errorf("CONFIG_INVALID: max_concurrent_tasks must be at least 1")
	}
	if c.PollIntervalMin < 1 || c.ShutdownGraceSeconds < 1 {
		return fmt.Errorf("CONFIG_INVALID: polling and shutdown values must be positive")
	}
	if _, err := time.LoadLocation(c.OffPeakTimezone); err != nil {
		return fmt.Errorf("CONFIG_INVALID: off_peak_timezone %q: %w", c.OffPeakTimezone, err)
	}
	for phase, minutes := range c.PhaseTimeoutMinutes {
		if minutes < 1 {
			return fmt.Errorf("CONFIG_INVALID: timeout for %s must be positive", phase)
		}
	}
	return nil
}

func (c *Config) PhaseTimeout(phase string) time.Duration {
	return time.Duration(c.PhaseTimeoutMinutes[phase]) * time.Minute
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
