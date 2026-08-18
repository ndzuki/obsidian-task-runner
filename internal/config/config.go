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
	ConfigVersion  int    `json:"config_version"`
	ObsidianVault  string `json:"obsidian_vault"`
	NewProjectRoot string `json:"new_project_root"`
	// WorktreeBase overrides where task worktrees are created. Empty uses the
	// default: <repo parent>/.otg-worktrees/<repoHash>/TASK-<runKey>. Must be
	// an absolute path; relative paths are not expanded.
	WorktreeBase                 string            `json:"worktree_base,omitempty"`
	Projects                     []Project         `json:"projects"`
	Notifications                NotifConfig       `json:"notifications"`
	PollIntervalMin              int               `json:"poll_interval_minutes"`
	MaxConcurrentTasks           int               `json:"max_concurrent_tasks"`
	MaxConcurrentTasksPerProject int               `json:"max_concurrent_tasks_per_project"`
	PhaseConcurrency             map[string]int    `json:"phase_concurrency"`
	PhaseTimeoutMinutes          map[string]int    `json:"phase_timeouts_minutes"`
	ShutdownGraceSeconds         int               `json:"shutdown_grace_seconds"`
	OffPeakTimezone              string            `json:"off_peak_timezone"`
	OffPeakWindows               []TimeWindow      `json:"off_peak_windows"`
	StarvationWarningDays        map[string]int    `json:"starvation_warning_days"`
	Models                       map[string]string `json:"models"`
	FallbackModels               map[string]string `json:"fallback_models"`
	OMPCmd                       string            `json:"omp_cmd"`
	DefaultAssignee              string            `json:"default_assignee"`
	LogDir                       string            `json:"log_dir,omitempty"`

	// Automation tuning (configurable, no hardcoded magic numbers).
	ScanMinIntervalSeconds     int `json:"scan_min_interval_seconds"`     // watcher scan throttle floor
	MaxOverlapWaitMinutes      int `json:"max_overlap_wait_minutes"`      // plan-file overlap deferral cap before concurrent dispatch (merge conflict resolution is the fallback)
	MaxAutoMergeFixes          int `json:"max_auto_merge_fixes"`          // AI repair budget per merge authorization
	CompactOversizeThresholdKB int `json:"compact_oversize_threshold_kb"` // TASK docs above this size get history folding
	GrillingConsolidationBatch int `json:"grilling_consolidation_batch"`  // PM sessions per scan
	MaxAutoFixConflicts        int `json:"max_auto_fix_conflicts"`        // conflict-size circuit breaker: skip AI repair above N conflicting files (0 = disabled)
	UpstreamStallDays          int `json:"upstream_stall_days"`           // blocked_by upstreams idle this many days trigger a one-time warning (0 = disabled)
	MergePollWaitTicks         int `json:"merge_poll_wait_ticks"`         // CI polling ticks (30s each) per merge attempt
	StageMinPerPhase           int `json:"stage_min_per_phase"`           // deterministic staging: tasks per phase floor
	StageMaxPhases             int `json:"stage_max_phases"`              // deterministic staging: phase count ceiling

	// Knowledge-base vector search (optional). When configured and the
	// vector index exists, otg kb search blends embedding cosine similarity
	// with BM25; otherwise BM25 alone is used (zero-dependency fallback).
	KBEmbedding *KBEmbeddingConfig `json:"kb_embedding,omitempty"`

	// Knowledge-base rerank stage (optional). When configured, otg kb search
	// reranks the hybrid top-N with a cross-encoder before trimming to the
	// final limit; backend failure degrades to the hybrid order.
	KBRerank *KBRerankConfig `json:"kb_rerank,omitempty"`

	// Knowledge-base chat model for `otg kb ask` (retrieval-augmented
	// generation). Requires kb_embedding: retrieval is the R of RAG.
	KBChat *KBChatConfig `json:"kb_chat,omitempty"`

	// Retrieval store path override (default: ~/.local/share/otg/kb.sqlite).
	// Keep it outside the vault when the vault is cloud-synced.
	KBDb string `json:"kb_db,omitempty"`
	// Completion audit (independent verification before auto-merge).
	Audit *AuditConfig `json:"audit,omitempty"`

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
	// ChunkChars caps each section's embedded body at N chars (600 default).
	// Larger values capture more section semantics at proportionally higher
	// indexing cost; changing it requires `otg kb index` (full rebuild —
	// vectors are derived data).
	ChunkChars int `json:"chunk_chars,omitempty"`
	// BatchSize is the number of chunks per embedding API call (32 default;
	// the throughput win for index builds).
	BatchSize int `json:"batch_size,omitempty"`
	// KNNCandidates caps the BM25-hit documents whose chunks enter the
	// cosine candidate set (100 default). Query cost grows with this value.
	KNNCandidates int `json:"knn_candidates,omitempty"`
}

// KBRerankConfig configures an optional cross-encoder rerank stage after
// hybrid retrieval — OpenAI-compatible /v1/rerank (llama.cpp server) or
// llama.cpp native /rerank. Ollama's server did not ship a rerank route as
// of 0.32.x, so this points at a separate llama-server instance.
type KBRerankConfig struct {
	// Backend: "openai" (OpenAI-compatible /v1/rerank, default) or
	// "llamacpp" (native /rerank).
	Backend string `json:"backend,omitempty"`
	// URL of the rerank server base ("http://127.0.0.1:11435").
	URL string `json:"url,omitempty"`
	// Model name on the rerank server (e.g. "bge-reranker-v2-m3").
	Model string `json:"model,omitempty"`
	// TopN rerank candidates taken from the hybrid top (20 default).
	TopN int `json:"top_n,omitempty"`
}

// KBChatConfig configures the chat model used by `otg kb ask` (retrieval-
// augmented generation over the knowledge base).
type KBChatConfig struct {
	// Backend: "ollama" (default) or "openai" (OpenAI-compatible).
	Backend string `json:"backend,omitempty"`
	// URL of the chat endpoint base ("http://127.0.0.1:11434").
	URL string `json:"url,omitempty"`
	// Model name (ollama "qwen3:1.7b", OpenAI "gpt-4o-mini" etc).
	Model string `json:"model,omitempty"`
	// Temperature for generation (0.2 default — retrieval-grounded answers
	// want low entropy).
	Temperature float64 `json:"temperature,omitempty"`
}

// AuditConfig configures the independent completion audit for auto-merge
// review tasks: a restricted read-only OMP session re-verifies each AC with
// raw evidence before merge authorization (implementation and verification
// run in separate sessions so the implementer cannot rubber-stamp its own
// completion).
type AuditConfig struct {
	// Enabled turns the audit gate on (default true). Disable to restore the
	// previous self-verified completion flow.
	Enabled bool `json:"enabled"`
	// MaxFixes bounds consecutive failed audits before the task is handed
	// to a grilling decision (resume resets the budget / replan routes to
	// refining) instead of looping implementing→review.
	MaxFixes int `json:"max_fixes"`
	// TimeoutMinutes bounds one audit session (default 15).
	TimeoutMinutes int `json:"timeout_minutes"`
	// Concurrency caps simultaneous audit sessions (default 1).
	Concurrency int `json:"concurrency"`
	// Model overrides the audit session model; empty uses the task assignee's
	// model (same model as the implementation session for verification parity).
	Model string `json:"model"`
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

// Project defines a project mapping.
type Project struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	GitRemote string `json:"git_remote"`
	ProjectID string `json:"project_id"`

	// ProjectType declares who owns the repository. Empty/"personal" is the
	// default: the daemon may auto-register the project, promote a Vault
	// fallback to a standalone checkout, and create the GitHub remote via
	// gh CLI. "team" marks a pre-existing organization repository (e.g. a
	// private Gitea project): the daemon must never create repos, never
	// auto-register, and never run GitHub-CLI remote operations against it.
	ProjectType string `json:"project_type"`

	// MergeMode selects the delivery path. Empty/"auto" uses the full
	// gh-CLI flow (push → PR → CI checks → merge). "manual" stops at push:
	// the branch is pushed with the repository's own credentials, the task
	// stays in review with merge_status=pushed, and a human merges through
	// the forge UI; the daemon flips the task to done once the pushed head
	// becomes an ancestor of the remote default branch. "fork-merge"
	// (fork development) merges the feature branch into the fork's default
	// branch locally (conflicts via the bounded AI session) and pushes it
	// with the repository's own credentials — the human then sends the
	// team project a PR from the fork's default branch.
	MergeMode string `json:"merge_mode"`
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
// Keys are phase names (refining/planning/merge/priority/pm/audit); a missing
// key or 0 means unlimited. round2 is governed by
// max_concurrent_tasks_per_project (per-project cap, default 2) plus
// max_concurrent_tasks (optional global total cap, 0 = unlimited). These caps
// bound simultaneous OMP sessions to protect API rate limits, token spend,
// and local CPU/memory.
func DefaultPhaseConcurrency() map[string]int {
	return map[string]int{
		"refining": 3,
		"planning": 2,
		"merge":    1,
		"priority": 1,
		"pm":       1,
		"audit":    1,
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
		Backend:       "ollama",
		URL:           "http://127.0.0.1:11434",
		Model:         "bge-m3",
		Weight:        0.5,
		ChunkChars:    600,
		BatchSize:     32,
		KNNCandidates: 100,
	}
}

// DefaultKBRerank returns the shipped rerank defaults (llama.cpp-compatible
// server on :11435). A nil config disables the rerank stage.
func DefaultKBRerank() *KBRerankConfig {
	return &KBRerankConfig{
		Backend: "openai",
		URL:     "http://127.0.0.1:11435",
		Model:   "bge-reranker-v2-m3",
		TopN:    20,
	}
}

// DefaultKBChat returns the shipped chat defaults (ollama, qwen3:1.7b —
// the smallest Qwen3 that still answers well on an Intel iGPU). A nil
// config disables `otg kb ask`.
func DefaultKBChat() *KBChatConfig {
	return &KBChatConfig{
		Backend:     "ollama",
		URL:         "http://127.0.0.1:11434",
		Model:       "qwen3:1.7b",
		Temperature: 0.2,
	}
}

// Defaults returns a Config with default values.
func Defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		ConfigVersion:   1,
		NewProjectRoot:  filepath.Join(home, "src"),
		PollIntervalMin: 30,
		// max_concurrent_tasks = optional global cap across all projects
		// (0 = unlimited); per-project capacity is governed by
		// MaxConcurrentTasksPerProject (default 2).
		MaxConcurrentTasks:           0,
		MaxConcurrentTasksPerProject: 2,
		PhaseConcurrency:             DefaultPhaseConcurrency(),
		PhaseTimeoutMinutes:          map[string]int{"priority": 5, "refining": 15, "planning": 30, "round2": 60, "merge": 15},
		ShutdownGraceSeconds:         30,
		OffPeakTimezone:              "Asia/Shanghai",
		OffPeakWindows:               []TimeWindow{{Start: "00:00", End: "09:00"}, {Start: "12:00", End: "14:00"}, {Start: "18:00", End: "24:00"}},
		StarvationWarningDays:        map[string]int{"P3": 14, "P4": 30},
		ScanMinIntervalSeconds:       10,
		// Overlap deferral cap: 12h exceeds the round2 no-progress cooldown
		// ceiling (~10.7h), so a stalled upstream stops being re-dispatched
		// before the deferred task is released to run concurrently.
		MaxOverlapWaitMinutes:      720,
		Audit:                      &AuditConfig{Enabled: true, MaxFixes: 2, TimeoutMinutes: 15, Concurrency: 1},
		MaxAutoMergeFixes:          3,
		CompactOversizeThresholdKB: 60,
		MaxAutoFixConflicts:        40, // TASK-067: 90+ conflicting files doomed the 15min AI session
		UpstreamStallDays:          3,  // upstream idle warning (TASK-067: month-long silent blockage)
		StageMinPerPhase:           3,
		StageMaxPhases:             4,
		SkillInstallDir:            filepath.Join(home, ".omp", "skills", "obsidian-task-runner"),
		Models:                     DefaultModels(),
		FallbackModels:             DefaultFallbackModels(),
		OMPCmd:                     "omp",
		DefaultAssignee:            "",
		Notifications:              NotifConfig{Desktop: true},
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
	// MaxConcurrentTasks: 0 is a valid value (no global cap) — missing and
	// explicit 0 are identical, so no fallback. Per-project capacity: 0
	// (missing or explicit) falls back to the default 2 — a per-project cap
	// of 0 has no useful meaning, unlike the global cap.
	if cfg.MaxConcurrentTasksPerProject == 0 {
		cfg.MaxConcurrentTasksPerProject = defaults.MaxConcurrentTasksPerProject
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
		if cfg.KBEmbedding.ChunkChars == 0 {
			cfg.KBEmbedding.ChunkChars = d.ChunkChars
		}
		if cfg.KBEmbedding.BatchSize == 0 {
			cfg.KBEmbedding.BatchSize = d.BatchSize
		}
		if cfg.KBEmbedding.KNNCandidates == 0 {
			cfg.KBEmbedding.KNNCandidates = d.KNNCandidates
		}
	}
	if cfg.KBRerank != nil {
		d := DefaultKBRerank()
		if cfg.KBRerank.Backend == "" {
			cfg.KBRerank.Backend = d.Backend
		}
		if cfg.KBRerank.URL == "" {
			cfg.KBRerank.URL = d.URL
		}
		if cfg.KBRerank.Model == "" {
			cfg.KBRerank.Model = d.Model
		}
		if cfg.KBRerank.TopN <= 0 {
			cfg.KBRerank.TopN = d.TopN
		}
	}
	if cfg.KBChat != nil {
		d := DefaultKBChat()
		if cfg.KBChat.Backend == "" {
			cfg.KBChat.Backend = d.Backend
		}
		if cfg.KBChat.URL == "" {
			cfg.KBChat.URL = d.URL
		}
		if cfg.KBChat.Model == "" {
			cfg.KBChat.Model = d.Model
		}
		if cfg.KBChat.Temperature == 0 {
			cfg.KBChat.Temperature = d.Temperature
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
	if cfg.MaxOverlapWaitMinutes <= 0 {
		cfg.MaxOverlapWaitMinutes = defaults.MaxOverlapWaitMinutes
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
	if cfg.MaxAutoFixConflicts == 0 {
		cfg.MaxAutoFixConflicts = defaults.MaxAutoFixConflicts
	}
	if cfg.UpstreamStallDays == 0 {
		cfg.UpstreamStallDays = defaults.UpstreamStallDays
	}
	if cfg.StageMinPerPhase <= 0 {
		cfg.StageMinPerPhase = defaults.StageMinPerPhase
	}
	if cfg.StageMaxPhases <= 0 {
		cfg.StageMaxPhases = defaults.StageMaxPhases
	}
	if cfg.Audit == nil {
		cfg.Audit = defaults.Audit
	} else {
		if cfg.Audit.MaxFixes == 0 {
			cfg.Audit.MaxFixes = defaults.Audit.MaxFixes
		}
		if cfg.Audit.TimeoutMinutes == 0 {
			cfg.Audit.TimeoutMinutes = defaults.Audit.TimeoutMinutes
		}
		if cfg.Audit.Concurrency == 0 {
			cfg.Audit.Concurrency = defaults.Audit.Concurrency
		}
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
	if value := os.Getenv("OTG_MAX_CONCURRENT_TASKS_PER_PROJECT"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.MaxConcurrentTasksPerProject = parsed
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
	if c.MaxConcurrentTasks < 0 {
		return fmt.Errorf("CONFIG_INVALID: max_concurrent_tasks must be >= 0 (0 = no global cap)")
	}
	if c.MaxConcurrentTasksPerProject < 0 {
		return fmt.Errorf("CONFIG_INVALID: max_concurrent_tasks_per_project must be >= 0 (0 = default 2)")
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
