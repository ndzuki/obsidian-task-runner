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
	ConfigVersion         int               `json:"config_version"`
	ObsidianVault         string            `json:"obsidian_vault"`
	NewProjectRoot        string            `json:"new_project_root"`
	Projects              []Project         `json:"projects"`
	Notifications         NotifConfig       `json:"notifications"`
	PollIntervalMin       int               `json:"poll_interval_minutes"`
	MaxConcurrentTasks    int               `json:"max_concurrent_tasks"`
	PhaseConcurrency      map[string]int    `json:"phase_concurrency"`
	PhaseTimeoutMinutes   map[string]int    `json:"phase_timeouts_minutes"`
	ShutdownGraceSeconds  int               `json:"shutdown_grace_seconds"`
	OffPeakTimezone       string            `json:"off_peak_timezone"`
	OffPeakWindows        []TimeWindow      `json:"off_peak_windows"`
	StarvationWarningDays map[string]int    `json:"starvation_warning_days"`
	Models                map[string]string `json:"models"`
	FallbackModels        map[string]string `json:"fallback_models"`
	OMPCmd                string            `json:"omp_cmd"`
	LogDir                string            `json:"log_dir,omitempty"`

	// Automation tuning (configurable, no hardcoded magic numbers).
	ScanMinIntervalSeconds   int `json:"scan_min_interval_seconds"`     // watcher scan throttle floor
	MaxAutoMergeFixes        int `json:"max_auto_merge_fixes"`          // AI repair budget per merge authorization
	CompactOversizeThresholdKB int `json:"compact_oversize_threshold_kb"` // TASK docs above this size get history folding
	GrillingConsolidationBatch int `json:"grilling_consolidation_batch"`  // PM sessions per scan
	MergePollWaitTicks       int `json:"merge_poll_wait_ticks"`         // CI polling ticks (30s each) per merge attempt
	StageMinPerPhase         int `json:"stage_min_per_phase"`           // deterministic staging: tasks per phase floor
	StageMaxPhases           int `json:"stage_max_phases"`              // deterministic staging: phase count ceiling

	// Registries
	ScaffoldRegistry map[string]ScaffoldCapability `json:"scaffold_registry,omitempty"`
	TemplateRegistry map[string]interface{}        `json:"template_registry,omitempty"`

	// Knowledge-base vector search (optional). When configured and the
	// vector index exists, otg kb search blends embedding cosine similarity
	// with BM25; otherwise BM25 alone is used (zero-dependency fallback).
	KBEmbedding *KBEmbeddingConfig `json:"kb_embedding,omitempty"`

	// Retrieval store path override (default: ~/.local/share/otg/kb.sqlite).
	// Keep it outside the vault when the vault is cloud-synced.
	KBDb string `json:"kb_db,omitempty"`

	// Skill install dir (not persisted)
	SkillInstallDir string `json:"-"`
	ConfigPath      string `json:"-"`
}

// KBEmbeddingConfig configures the local/API embedding backend for semantic
// knowledge search.
type KBEmbeddingConfig struct {
	// Backend: "ollama" (default) or "openai" (OpenAI-compatible API).
	Backend string `json:"backend,omitempty"`
	// URL for the embedding endpoint: ollama base URL ("http://127.0.0.1:11434")
	// or OpenAI-compatible base ("https://api.openai.com/v1").
	URL string `json:"url,omitempty"`
	// Model name: ollama "bge-m3", OpenAI "text-embedding-3-small" etc.
	Model string `json:"model,omitempty"`
	// APIKey for OpenAI-compatible backends (ollama needs none).
	APIKey string `json:"api_key,omitempty"`
	// Blend weight for cosine similarity vs BM25 (0.5 = equal).
	Weight float64 `json:"weight,omitempty"`
}

type TimeWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
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
		"deepseek": "deepseek/deepseek-v4-flash",
		"gpt":      "gateway/gpt-5.6-sol",
		"default":  "gateway/gpt-5.4-mini",
		"gemini":   "google/gemini-2.5-pro",
		"claude":   "anthropic/claude-sonnet-4-20250514",
		"minimax":  "minimax/minimax-m1",
	}
}

// DefaultPhaseConcurrency returns the per-phase OMP concurrency ceilings.
// Keys are phase names (refining/planning/merge/priority/pm); a missing key
// or 0 means unlimited. round2 is governed by max_concurrent_tasks (kept for
// backward compatibility). These caps bound simultaneous OMP sessions to
// protect API rate limits, token spend, and local CPU/memory.
func DefaultPhaseConcurrency() map[string]int {
	return map[string]int{
		"refining": 3,
		"planning": 2,
		"merge":    1,
		"priority": 1,
		"pm":       1,
	}
}

// ConcurrencyFor returns the configured concurrency ceiling for a phase, or
// 0 when the phase is unlimited.
func (c *Config) ConcurrencyFor(phase string) int {
	return c.PhaseConcurrency[phase]
}

// DefaultFallbackModels returns the built-in assignee → fallback model map.
// Keys are assignee names (matching `models` / TASK frontmatter); values are
// OMP model identifiers. Users may add/remove/override any key in
// vault-map.json; an empty value disables the fallback for that assignee.
func DefaultFallbackModels() map[string]string {
	return map[string]string{
		"gpt":      "deepseek/deepseek-v4-flash",
		"default":  "deepseek/deepseek-v4-flash",
		"deepseek": "deepseek/deepseek-v4-flash",
	}
}

// ModelReference returns a human-readable model reference table.
// Model identifiers are sourced from DefaultModels/DefaultFallbackModels so
// the table never drifts from the shipped defaults.
func ModelReference() string {
	d := DefaultModels()
	fb := DefaultFallbackModels()
	return fmt.Sprintf(`| key | 模型标识 | 用途 |
|----------|---------|------|
| default  | %s | refining、planning、round2 日常任务（gpt-5.4-mini，Agent 能力大幅增强） |
| deepseek | %s | deepseek assignee 主模型 |
| gpt      | %s | 高推理任务主力 |
| gemini   | %s | 可选 |
| claude   | %s | 可选 |
| minimax  | %s | 可选 |
| fallback_models | 映射（默认 gpt/default/deepseek → %s） | 各 assignee 失败兜底；可增删任意 key、改任意模型标识，置 "" 禁用 |
`, d["default"], d["deepseek"], d["gpt"], d["gemini"], d["claude"], d["minimax"], fb["gpt"])
}

// DefaultKBEmbedding returns the shipped embedding defaults (ollama, bge-m3,
// equal blend). Callers may override per field; a nil config disables vector
// search entirely.
func DefaultKBEmbedding() *KBEmbeddingConfig {
	return &KBEmbeddingConfig{
		Backend: "ollama",
		URL:     "http://127.0.0.1:11434",
		Model:   "bge-m3",
		Weight:  0.5,
	}
}

// Defaults returns a Config with default values.
func Defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		ConfigVersion:         1,
		NewProjectRoot:        filepath.Join(home, "src"),
		PollIntervalMin:       30,
		MaxConcurrentTasks:    2,
		PhaseConcurrency:      DefaultPhaseConcurrency(),
		PhaseTimeoutMinutes:   map[string]int{"priority": 5, "refining": 15, "planning": 30, "round2": 60, "merge": 15},
		ShutdownGraceSeconds:  30,
		OffPeakTimezone:       "Asia/Shanghai",
		OffPeakWindows:        []TimeWindow{{Start: "00:00", End: "09:00"}, {Start: "12:00", End: "14:00"}, {Start: "18:00", End: "24:00"}},
		StarvationWarningDays: map[string]int{"P3": 14, "P4": 30},
		ScanMinIntervalSeconds:   10,
		MaxAutoMergeFixes:        3,
		CompactOversizeThresholdKB: 60,
		GrillingConsolidationBatch: 3,
		MergePollWaitTicks:       20,
		StageMinPerPhase:         3,
		StageMaxPhases:           4,
		SkillInstallDir:          filepath.Join(home, ".omp", "skills", "obsidian-task-runner"),
		Models:                DefaultModels(),
		FallbackModels:        DefaultFallbackModels(),
		OMPCmd:                "omp",
		Notifications:         NotifConfig{Desktop: true},
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
	if cfg.PhaseConcurrency == nil {
		cfg.PhaseConcurrency = defaults.PhaseConcurrency
	} else {
		for phase, value := range defaults.PhaseConcurrency {
			if _, exists := cfg.PhaseConcurrency[phase]; !exists {
				cfg.PhaseConcurrency[phase] = value
			}
		}
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
	if cfg.FallbackModels == nil {
		cfg.FallbackModels = defaults.FallbackModels
	}
	if cfg.KBEmbedding != nil {
		d := DefaultKBEmbedding()
		if cfg.KBEmbedding.Backend == "" {
			cfg.KBEmbedding.Backend = d.Backend
		}
		if cfg.KBEmbedding.URL == "" {
			cfg.KBEmbedding.URL = d.URL
		}
		if cfg.KBEmbedding.Model == "" {
			cfg.KBEmbedding.Model = d.Model
		}
		if cfg.KBEmbedding.Weight == 0 {
			cfg.KBEmbedding.Weight = d.Weight
		}
	}
	if cfg.OMPCmd == "" {
		cfg.OMPCmd = defaults.OMPCmd
	}
	if cfg.SkillInstallDir == "" {
		cfg.SkillInstallDir = defaults.SkillInstallDir
	}
	if cfg.ScanMinIntervalSeconds <= 0 {
		cfg.ScanMinIntervalSeconds = defaults.ScanMinIntervalSeconds
	}
	if cfg.MaxAutoMergeFixes <= 0 {
		cfg.MaxAutoMergeFixes = defaults.MaxAutoMergeFixes
	}
	if cfg.CompactOversizeThresholdKB <= 0 {
		cfg.CompactOversizeThresholdKB = defaults.CompactOversizeThresholdKB
	}
	if cfg.GrillingConsolidationBatch <= 0 {
		cfg.GrillingConsolidationBatch = defaults.GrillingConsolidationBatch
	}
	if cfg.MergePollWaitTicks <= 0 {
		cfg.MergePollWaitTicks = defaults.MergePollWaitTicks
	}
	if cfg.StageMinPerPhase <= 0 {
		cfg.StageMinPerPhase = defaults.StageMinPerPhase
	}
	if cfg.StageMaxPhases <= 0 {
		cfg.StageMaxPhases = defaults.StageMaxPhases
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
	for phase, limit := range c.PhaseConcurrency {
		if limit < 0 {
			return fmt.Errorf("CONFIG_INVALID: phase_concurrency.%s must be >= 0 (0 = unlimited)", phase)
		}
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
	if defaultModel, ok := c.Models["default"]; ok && defaultModel != "" {
		return defaultModel
	}
	return DefaultModels()["default"]
}

// FallbackModelFor returns the configured fallback model for an assignee.
// Lookup is pure configuration: `fallback_models` maps assignee keys to OMP
// model identifiers. Neither the assignee set nor the model is hardcoded —
// users configure both via vault-map.json. Empty string means no fallback.
func (c *Config) FallbackModelFor(assignee string) string {
	return c.FallbackModels[assignee]
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
