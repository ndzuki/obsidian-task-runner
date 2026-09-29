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
	OffPeakTimezone              string            `json:"off_peak_timezone"`
	OffPeakWindows               []TimeWindow      `json:"off_peak_windows"`
	Models                       map[string]string `json:"models"`
	// Fallback controls the DSH cross-model fallback chains from vault-map.
	// When set, the daemon forwards it to the agent-server with every
	// dsh-embed /agent/run session; the agent-server attaches it to the run
	// agent (agent.fallbackConfig) and the fallback.mjs plugin uses it as the
	// per-session override. It applies only to automated daemon phases —
	// interactive dsh web / dsh-tui sessions never receive it, so the user
	// keeps full control of model selection and failures never auto-switch.
	Fallback *FallbackConfig `json:"fallback,omitempty"`
	// DSHCmd / DSHProfile drive the spawn-headless DSH adapter. DSHCmd is also
	// the binary used to launch the agent-server child. DSHProfile only
	// affects executor="dsh" (spawn path): under the default executor
	// dsh-embed every phase (design included) runs through the agent-server
	// whose profile is hardcoded `headless-agent-server`, so the field is
	// ignored there. The headless app has no per-invocation --model flag,
	// so the profile owns model routing on the spawn path.
	DSHCmd     string `json:"dsh_cmd"`
	DSHProfile string `json:"dsh_profile"`
	// AgentServerAddr is the long-lived `dsh --profile headless-agent-server`
	// address used by the dsh-embed executor (host:port; docs/embed-migration-
	// plan.md).
	AgentServerAddr string `json:"agent_server_addr"`
	// AgentServerManaged controls whether the daemon starts/stops the
	// agent-server child itself. When false, the operator is expected to run
	// `dsh --profile headless-agent-server` as an external systemd service;
	// the daemon only waits for it to become healthy and never spawns or
	// kills it.
	AgentServerManaged bool `json:"agent_server_managed"`
	// VaultWebAddr is the read-only vault dashboard HTTP API address (host:port)
	// served in-process by the daemon for the DSH web vault-dashboard plugin
	// (Phase 4). Empty disables the embedded server.
	VaultWebAddr        string `json:"vault_web_addr"`
	ReplanGateThreshold int    `json:"replan_gate_threshold"`
	// Executor selects the phase-execution backend: "dsh-embed" (default,
	// long-lived agent-server RPC with per-phase reasoningEffort) or "dsh"
	// (spawn `dsh --profile headless`). Any other value resolves to
	// dsh-embed (newPhaseExecutor). The pre-DSH executor is retired — no
	// legacy value is honored anymore.
	Executor        string `json:"executor"`
	DefaultAssignee string `json:"default_assignee"`
	LogDir          string `json:"log_dir,omitempty"`

	// Automation tuning (configurable, no hardcoded magic numbers).
	ScanMinIntervalSeconds     int `json:"scan_min_interval_seconds"`     // watcher scan throttle floor
	MaxOverlapWaitMinutes      int `json:"max_overlap_wait_minutes"`      // plan-file overlap deferral cap before concurrent dispatch (merge conflict resolution is the fallback)
	MaxAutoMergeFixes          int `json:"max_auto_merge_fixes"`          // AI repair budget per merge authorization
	CompactOversizeThresholdKB int `json:"compact_oversize_threshold_kb"` // TASK docs above this size get history folding
	GrillingConsolidationBatch int `json:"grilling_consolidation_batch"`  // PM sessions per scan
	MaxAutoFixConflicts        int `json:"max_auto_fix_conflicts"`        // conflict-size circuit breaker: skip AI repair above N conflicting files (0 or missing falls back to the default 40)
	UpstreamStallDays          int `json:"upstream_stall_days"`           // blocked_by upstreams idle this many days trigger a one-time warning (0 = disabled)
	MergePollWaitTicks         int `json:"merge_poll_wait_ticks"`         // CI polling ticks (30s each) per merge attempt
	StageMinPerPhase           int `json:"stage_min_per_phase"`           // deterministic staging: tasks per phase floor
	StageMaxPhases             int `json:"stage_max_phases"`              // deterministic staging: phase count ceiling
	AutoResumeAgedAfterHours   int `json:"auto_resume_aged_after_hours"`  // blocked-task aged auto-resume window (transient phase errors); <=0 = default 24

	// MemoryGate is the daemon-side host-memory gate for implementing/round2
	// dispatch. It activates when a task's REQ declares a floor (e.g.
	// "MemAvailable ≥ 12 GiB" / "可用内存 <12 GiB") or when MemAvailableMiB is
	// set as a global floor. Below the floor the daemon first auto-recovers
	// (stops restartable k3d staging clusters) and, if still short, escalates
	// to a project-level grilling decision instead of burning a round2 session
	// that would only discover the shortfall (a gate that is just under the
	// declared floor can loop between implementing/grilling until someone
	// manually stops a k3d cluster).
	MemoryGate MemoryGateConfig `json:"memory_gate"`

	// EnvCleanup is the daemon-side environment teardown that runs when an
	// automated task stops implementing: at a terminal merge (OnMerge) or at
	// a blocked / needs-grilling / closed dead-end (OnBlock). Implementing
	// sessions build disposable staging environments (k3d clusters, k3d
	// registries, docker networks) for smoke tests; when the session forgets
	// to tear them down the audit gate reports "in-flight residual" and the
	// merge completes (or the task blocks) with the environment still running
	// (sessions have survived a merge with several clusters plus a registry,
	// and requirement-driven blocks have left containers behind). This gate
	// deletes those leftovers, bounded by Exclude and DryRun.
	EnvCleanup *EnvCleanupConfig `json:"env_cleanup,omitempty"`

	// Completion audit (independent verification before auto-merge).
	Audit *AuditConfig `json:"audit,omitempty"`

	// Skill install dir (not persisted)
	SkillInstallDir string `json:"-"`
	ConfigPath      string `json:"-"`
}

// AuditConfig configures the independent completion audit for auto-merge
// review tasks: a restricted read-only execution session re-verifies each AC with
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
	// Model overrides the audit session model; empty uses the task assignee's
	// model (same model as the implementation session for verification parity).
	Model string `json:"model"`
}

type TimeWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// MemoryGateConfig drives the daemon-side host-memory gate for
// implementing/round2 dispatch (see Config.MemoryGate).
type MemoryGateConfig struct {
	// MemAvailableMiB is a global host-memory floor for round2 dispatch in
	// MiB. 0 (default) disables the global floor — the gate then only
	// activates for tasks whose REQ declares an explicit floor. Use this to
	// enforce a minimum on every implementation task regardless of REQ text.
	MemAvailableMiB int `json:"mem_available_mib"`
	// AutoRecovery stops restartable k3d staging clusters (recoverable via
	// `k3d cluster start`) to free memory before escalating. It never touches
	// user services (local inference/vector services, desktop processes) or anything
	// in Exclude.
	AutoRecovery bool `json:"auto_recovery"`
	// MaxStops caps how many clusters a single auto-recovery pass may stop.
	MaxStops int `json:"max_stops"`
	// Exclude holds name substrings never auto-stopped by recovery.
	Exclude []string `json:"exclude"`
}

// EnvCleanupConfig drives the daemon-side environment teardown (see
// Config.EnvCleanup). It deletes disposable k3d clusters, k3d registries,
// and their leftover docker networks — resources that an implementing
// session built for smoke tests and forgot to remove. It never touches user
// services (local inference/vector services, desktop processes) or anything in
// Exclude.
type EnvCleanupConfig struct {
	// OnMerge enables teardown when a task reaches the merged/done terminal
	// state. Disable it to keep smoke environments alive for manual
	// inspection after merge.
	OnMerge bool `json:"on_merge"`
	// OnBlock enables teardown when a task stops implementing without
	// merging: blocked by a phase failure, blocked by a requirement change /
	// pending_req replan, held in needs-grilling, or closed. Implementing
	// sessions can leave k3d clusters / registries / networks behind on these
	// paths too (a requirement-driven block has left containers running).
	// Same Exclude/DryRun guards as OnMerge.
	OnBlock bool `json:"on_block"`
	// Exclude holds name substrings never deleted by the teardown (persistent
	// clusters the user wants to keep, e.g. "deployd-customer").
	Exclude []string `json:"exclude"`
	// DryRun logs and notifies what would be deleted without deleting it.
	// Useful for auditing the teardown before trusting it.
	DryRun bool `json:"dry_run"`
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

// FallbackConfig mirrors the DSH fallback.mjs plugin config (chains +
// default + fallbackOnCodes). It is the vault-map-driven source of truth for
// cross-model fallback: vault-map.json is the daemon's own config file, so
// the fallback routing lives next to the model table instead of buried in
// a home-level cordis.patch.yml.
type FallbackConfig struct {
	// Chains are ordered fallback lists keyed by a primary model. When the
	// primary (from) fails with a fallbackable code, the plugin advances down
	// its to-list in order.
	Chains []FallbackChain `json:"chains"`
	// Default is the fallback list used when no chain's from matches the
	// session's primary model. Mirrors the plugin's `default` key — omit
	// when chains already cover every routable model.
	Default []ModelRef `json:"default,omitempty"`
	// FallbackOnCodes is the failure-code whitelist that triggers fallback.
	// Empty means the plugin default (server/rate-limit/timeout/transport/
	// empty-response/stream-closed/malformed/unknown + HTTP_5xx).
	FallbackOnCodes []string `json:"fallbackOnCodes,omitempty"`
}

// FallbackChain maps a primary model to its ordered fallback candidates.
type FallbackChain struct {
	From ModelRef   `json:"from"`
	To   []ModelRef `json:"to"`
}

// ModelRef names one DSH provider/model route.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// DefaultModels returns the built-in model mappings.
//
// There are no built-in routes: every assignee key → DSH provider/model
// route is operator-provided via vault-map.json `models`. An empty or
// missing mapping means tasks wait (the daemon logs the gap) until the
// operator configures one — the project never ships operator-specific
// model/gateway preferences.
func DefaultModels() map[string]string {
	return map[string]string{}
}

// DefaultPhaseConcurrency returns the per-phase phase concurrency ceilings.
// Keys are phase names (refining/planning/merge/priority/pm/audit); only an
// explicit 0 means unlimited — a missing key is backfilled with the default
// during config merging (see mergeDefaults). round2 is governed by
// max_concurrent_tasks_per_project (per-project cap, default 2) plus
// max_concurrent_tasks (optional global total cap, 0 = unlimited). These caps
// bound simultaneous execution sessions to protect API rate limits, token spend,
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

// ModelReference returns a human-readable model reference table.
// Model identifiers are sourced from DefaultModels so the table never drifts
// from the shipped defaults.

// Defaults returns a Config with default values.
func Defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		NewProjectRoot:  filepath.Join(home, "src"),
		PollIntervalMin: 30,
		// max_concurrent_tasks = optional global cap across all projects
		// (0 = unlimited); per-project capacity is governed by
		// MaxConcurrentTasksPerProject (default 2).
		MaxConcurrentTasks:           0,
		MaxConcurrentTasksPerProject: 2,
		PhaseConcurrency:             DefaultPhaseConcurrency(),
		// round2 默认 120m：实现阶段带真实环境冒烟（k3d/镜像构建/回归），
		// 单窗口 60m 会把活跃会话误判为 wedged 而 cancel；
		// 配合 timeout_active 活动度续期，活跃会话不会被误杀。
		// planning 45m：思维链会话变长后，大 REQ 的 plan 生成需要余量
		//（30m 会在活跃会话下误判 wedged 的风险升高）。
		PhaseTimeoutMinutes:    map[string]int{"priority": 5, "refining": 15, "planning": 45, "round2": 120, "merge": 15, "design": 90},
		OffPeakTimezone:        "",
		OffPeakWindows:         nil,
		ScanMinIntervalSeconds: 10,
		// Overlap deferral cap: 12h exceeds the round2 no-progress cooldown
		// ceiling (~10.7h), so a stalled upstream stops being re-dispatched
		// before the deferred task is released to run concurrently.
		MaxOverlapWaitMinutes:      720,
		MergePollWaitTicks:         20, // 20 × 30s = 10min CI polling budget per merge attempt
		Audit:                      &AuditConfig{Enabled: true, MaxFixes: 2, TimeoutMinutes: 15},
		MaxAutoMergeFixes:          3,
		CompactOversizeThresholdKB: 60,
		MaxAutoFixConflicts:        40, // 90+ conflicting files can doom the 15min AI session
		UpstreamStallDays:          3,  // upstream idle warning (a silently stalled upstream can block for a month)
		StageMinPerPhase:           3,
		StageMaxPhases:             4,
		GrillingConsolidationBatch: 1,
		AutoResumeAgedAfterHours:   24,
		MemoryGate: MemoryGateConfig{
			// 0 = 无全局下限：仅 REQ 显式声明 "MemAvailable ≥ N GiB" 的门禁生效。
			// AutoRecovery 默认关闭：自动停 k3d 集群是有损运维决策，必须显式
			// 开启，并用 Exclude 声明永不触碰的常驻服务。
			MemAvailableMiB: 0,
			AutoRecovery:    false,
			MaxStops:        2,
			Exclude:         []string{},
		},
		// EnvCleanup 默认不启用（nil）：删除 k3d 集群/registry/网络是有损操作，
		// 开源默认不做任何自动删除；需要时在 vault-map.json 显式配置并声明
		// Exclude 白名单。
		EnvCleanup:      nil,
		SkillInstallDir: filepath.Join(home, ".dsh", "skills", "obsidian-task-runner"),
		Models:          DefaultModels(),
		DSHCmd:          "dsh",
		// DSHProfile 默认空：仅 executor=dsh（spawn 路径）使用，空值时
		// newDSHExecutorWithProfile 回退内置 "headless"。默认空保证
		// `config migrate --write` 不会把该字段写回用户文件。
		DSHProfile:          "",
		AgentServerAddr:     "127.0.0.1:8799",
		AgentServerManaged:  true,
		VaultWebAddr:        "127.0.0.1:8787",
		ReplanGateThreshold: 5,
		Executor:            "dsh-embed",
		DefaultAssignee:     "",
		Notifications:       NotifConfig{Desktop: true},
	}
}

// Load reads vault-map.json and applies env var overrides.
func Load(mapPath string) (*Config, error) {
	cfg := Defaults()
	if mapPath == "" {
		home, _ := os.UserHomeDir()
		mapPath = filepath.Join(home, ".dsh", "skills", "obsidian-task-runner", "config", "vault-map.json")
	}
	cfg.ConfigPath = mapPath

	data, err := os.ReadFile(mapPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", mapPath, err)
		}
		mergeDefaults(cfg, map[string]bool{})
	} else {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", mapPath, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", mapPath, err)
		}
		present := make(map[string]bool, len(raw))
		for k := range raw {
			present[k] = true
		}
		mergeDefaults(cfg, present)
	}
	applyEnvironment(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// mergeDefaults fills defaults for fields the operator did not set. `present`
// holds the top-level keys that appeared in the raw vault-map.json, so an
// explicit 0 can disable a feature whose default is non-zero (upstream_stall_days).
func mergeDefaults(cfg *Config, present map[string]bool) {
	defaults := Defaults()
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
	if cfg.OffPeakTimezone == "" {
		cfg.OffPeakTimezone = defaults.OffPeakTimezone
	}
	if len(cfg.OffPeakWindows) == 0 {
		cfg.OffPeakWindows = defaults.OffPeakWindows
	}
	if cfg.Models == nil {
		cfg.Models = DefaultModels()
	}
	if cfg.DSHCmd == "" {
		cfg.DSHCmd = defaults.DSHCmd
	}
	if cfg.DSHProfile == "" {
		cfg.DSHProfile = defaults.DSHProfile
	}
	if cfg.AgentServerAddr == "" {
		cfg.AgentServerAddr = defaults.AgentServerAddr
	}
	if cfg.ReplanGateThreshold == 0 {
		cfg.ReplanGateThreshold = defaults.ReplanGateThreshold
	}
	if cfg.Executor == "" {
		cfg.Executor = defaults.Executor
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
	// Explicit upstream_stall_days=0 means "disable the idle warning" (the
	// field documents 0 = disabled). Only backfill when the key is absent.
	if !present["upstream_stall_days"] && cfg.UpstreamStallDays <= 0 {
		cfg.UpstreamStallDays = defaults.UpstreamStallDays
	}
	if cfg.StageMinPerPhase <= 0 {
		cfg.StageMinPerPhase = defaults.StageMinPerPhase
	}
	if cfg.StageMaxPhases <= 0 {
		cfg.StageMaxPhases = defaults.StageMaxPhases
	}
	if cfg.AutoResumeAgedAfterHours <= 0 {
		cfg.AutoResumeAgedAfterHours = defaults.AutoResumeAgedAfterHours
	}
	if cfg.MemoryGate.MaxStops == 0 {
		cfg.MemoryGate.MaxStops = defaults.MemoryGate.MaxStops
	}
	// EnvCleanup is opt-in (nil default): an explicit block keeps its own
	// Exclude verbatim — code never injects personal service names.
	if cfg.Audit == nil {
		cfg.Audit = defaults.Audit
	} else {
		if cfg.Audit.MaxFixes == 0 {
			cfg.Audit.MaxFixes = defaults.Audit.MaxFixes
		}
		if cfg.Audit.TimeoutMinutes == 0 {
			cfg.Audit.TimeoutMinutes = defaults.Audit.TimeoutMinutes
		}
	}
}

func applyEnvironment(cfg *Config) {
	if value := firstNonEmptyEnv("OTG_OBSIDIAN_VAULT", "OBSIDIAN_VAULT"); value != "" {
		cfg.ObsidianVault = value
	}
	if value := os.Getenv("OTG_DSH_CMD"); value != "" {
		cfg.DSHCmd = value
	}
	if value := os.Getenv("OTG_DSH_PROFILE"); value != "" {
		cfg.DSHProfile = value
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
	if c.MaxConcurrentTasks < 0 {
		return fmt.Errorf("CONFIG_INVALID: max_concurrent_tasks must be >= 0 (0 = no global cap)")
	}
	if c.MaxConcurrentTasksPerProject < 0 {
		return fmt.Errorf("CONFIG_INVALID: max_concurrent_tasks_per_project must be >= 0 (0 = default 2)")
	}
	if c.ReplanGateThreshold < 0 {
		return fmt.Errorf("CONFIG_INVALID: replan_gate_threshold must be >= 0 (0 = disabled)")
	}
	if c.Executor != "dsh" && c.Executor != "dsh-embed" {
		return fmt.Errorf("CONFIG_INVALID: executor must be \"dsh\" or \"dsh-embed\", got %q", c.Executor)
	}
	for phase, limit := range c.PhaseConcurrency {
		if limit < 0 {
			return fmt.Errorf("CONFIG_INVALID: phase_concurrency.%s must be >= 0 (0 = unlimited)", phase)
		}
	}
	if c.PollIntervalMin < 1 {
		return fmt.Errorf("CONFIG_INVALID: poll_interval_minutes must be positive")
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

// Model returns the model identifier for an assignee key.
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

// ResolveProject returns the local path for a project name.
func (c *Config) ResolveProject(name string) (string, error) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p.Path, nil
		}
	}
	return "", fmt.Errorf("project %q not found in vault-map", name)
}
