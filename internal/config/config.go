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
	ObsidianVault     string       `json:"obsidian_vault"`
	NewProjectRoot    string       `json:"new_project_root"`
	Projects          []Project    `json:"projects"`
	Notifications     NotifConfig  `json:"notifications"`
	PollIntervalMin   int          `json:"poll_interval_minutes"`

	// OMP model overrides
	OMPModelDeepSeek string `json:"omp_model_deepseek"`
	OMPModelGPT      string `json:"omp_model_gpt"`
	OMPModelFlash    string `json:"omp_model_flash"`
	OMPCmd           string `json:"omp_cmd"`

	// Skill install dir
	SkillInstallDir string `json:"-"`
}

// Project defines a project mapping.
type Project struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	GitRemote string `json:"git_remote"`
}

// NotifConfig holds notification settings.
type NotifConfig struct {
	Desktop bool `json:"desktop"`
}

// Defaults returns a Config with default values.
func Defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		NewProjectRoot:    filepath.Join(home, "src"),
		PollIntervalMin:   30,
		SkillInstallDir:   filepath.Join(home, ".omp", "skills", "obsidian-task-runner"),
		OMPModelDeepSeek:  "deepseek/deepseek-v4-pro:xhigh",
		OMPModelGPT:       "gateway/gpt-5.5:xhigh",
		OMPModelFlash:     "deepseek/deepseek-v4-flash",
		OMPCmd:            "omp",
		Notifications:     NotifConfig{Desktop: true},
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
			// Return defaults, no vault-map yet
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", mapPath, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", mapPath, err)
	}

	// Env overrides
	if v := os.Getenv("OBSIDIAN_VAULT"); v != "" {
		cfg.ObsidianVault = v
	}
	if v := os.Getenv("OMP_MODEL_DEEPSEEK"); v != "" {
		cfg.OMPModelDeepSeek = v
	}
	if v := os.Getenv("OMP_MODEL_GPT"); v != "" {
		cfg.OMPModelGPT = v
	}
	if v := os.Getenv("OMP_MODEL_FLASH"); v != "" {
		cfg.OMPModelFlash = v
	}
	if v := os.Getenv("OMP_CMD"); v != "" {
		cfg.OMPCmd = v
	}

	return cfg, nil
}

// ResolveProject returns the local path for a project name, or an error.
func (c *Config) ResolveProject(name string) (string, error) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p.Path, nil
		}
	}
	return "", fmt.Errorf("project %q not found in vault-map", name)
}
