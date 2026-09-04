package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsSetsConcurrentTaskLimits(t *testing.T) {
	d := Defaults()
	if d.MaxConcurrentTasks != 0 {
		t.Fatalf("MaxConcurrentTasks = %d, want 0 (no global cap by default)", d.MaxConcurrentTasks)
	}
	if d.MaxConcurrentTasksPerProject != 2 {
		t.Fatalf("MaxConcurrentTasksPerProject = %d, want 2", d.MaxConcurrentTasksPerProject)
	}
}

func TestLoadReadsConcurrentTaskLimit(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"max_concurrent_tasks": 4}`)
	if err := os.WriteFile(mapFile, data, 0644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}

	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxConcurrentTasks != 4 {
		t.Errorf("MaxConcurrentTasks = %d, want 4", cfg.MaxConcurrentTasks)
	}
}

func TestDefaultModelsHasNoBuiltInRoutes(t *testing.T) {
	models := DefaultModels()
	if len(models) != 0 {
		t.Fatalf("DefaultModels must ship no built-in routes (operator-configurable only), got %v", models)
	}
}

func TestModelFallsBackToDefault(t *testing.T) {
	cfg := &Config{Models: map[string]string{
		"default": "provider/default-model",
	}}
	if got := cfg.Model("unknown"); got != "provider/default-model" {
		t.Fatalf("Model(unknown) = %q, want %q", got, "provider/default-model")
	}
}

func TestLoadReadsConfiguredDefaultModel(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"models":{"default":"provider/default-model"}}`)
	if err := os.WriteFile(mapFile, data, 0644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}

	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Model("unknown"); got != "provider/default-model" {
		t.Fatalf("Model(unknown) = %q, want %q", got, "provider/default-model")
	}
}

func TestLoadReadsFallbackConfig(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{
	  "models": {"default": "beta/beta-luna"},
	  "fallback": {
	    "chains": [
	      {
	        "from": {"provider": "acme", "model": "acme-pro"},
	        "to": [
	          {"provider": "acme", "model": "acme-mini"},
	          {"provider": "beta", "model": "beta-sol"}
	        ]
	      }
	    ],
	    "default": [
	      {"provider": "acme", "model": "acme-pro"},
	      {"provider": "beta", "model": "beta-terra"}
	    ],
	    "fallbackOnCodes": ["SERVER", "QUOTA", "RATE_LIMIT"]
	  }
	}`)
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}

	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Fallback == nil {
		t.Fatal("fallback must be non-nil")
	}
	if len(cfg.Fallback.Chains) != 1 {
		t.Fatalf("fallback chains = %d, want 1", len(cfg.Fallback.Chains))
	}
	chain := cfg.Fallback.Chains[0]
	if chain.From.Provider != "acme" || chain.From.Model != "acme-pro" {
		t.Errorf("chain from = %q/%q, want acme/acme-pro", chain.From.Provider, chain.From.Model)
	}
	if len(chain.To) != 2 || chain.To[1].Provider != "beta" || chain.To[1].Model != "beta-sol" {
		t.Errorf("chain to = %+v, want [acme/acme-mini beta/beta-sol]", chain.To)
	}
	// default 链必须透传（fallback.mjs 的 default 键——from 无匹配时的兜底），
	// 否则用户配了 default 会被 Go 静默丢弃（审查 P1 缺口）。
	if len(cfg.Fallback.Default) != 2 || cfg.Fallback.Default[1].Provider != "beta" || cfg.Fallback.Default[1].Model != "beta-terra" {
		t.Errorf("fallback default = %+v, want [acme/acme-pro beta/beta-terra]", cfg.Fallback.Default)
	}
	wantCodes := []string{"SERVER", "QUOTA", "RATE_LIMIT"}
	if len(cfg.Fallback.FallbackOnCodes) != len(wantCodes) {
		t.Fatalf("fallbackOnCodes = %v, want %v", cfg.Fallback.FallbackOnCodes, wantCodes)
	}
	for i, code := range wantCodes {
		if cfg.Fallback.FallbackOnCodes[i] != code {
			t.Errorf("fallbackOnCodes[%d] = %q, want %q", i, cfg.Fallback.FallbackOnCodes[i], code)
		}
	}
}

func TestDefaultsSetsWorkflowConfiguration(t *testing.T) {
	cfg := Defaults()
	if cfg.OffPeakTimezone != "" || len(cfg.OffPeakWindows) != 0 {
		t.Fatalf("off-peak must be opt-in (empty defaults), got tz=%q windows=%v", cfg.OffPeakTimezone, cfg.OffPeakWindows)
	}
	if got := cfg.PhaseTimeout("round2"); got.String() != "2h0m0s" {
		t.Fatalf("round2 timeout = %v, want 2h", got)
	}
	if cfg.MergePollWaitTicks != 20 {
		t.Fatalf("merge_poll_wait_ticks default = %d, want 20", cfg.MergePollWaitTicks)
	}
	if cfg.EnvCleanup != nil {
		t.Fatalf("env_cleanup must be opt-in (nil default), got %+v", cfg.EnvCleanup)
	}
	if cfg.MemoryGate.AutoRecovery {
		t.Fatal("memory_gate.auto_recovery must default off")
	}
	if len(cfg.MemoryGate.Exclude) != 0 {
		t.Fatalf("memory_gate.exclude must default empty, got %v", cfg.MemoryGate.Exclude)
	}
}

func TestExplicitZeroUpstreamStallDaysDisables(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"upstream_stall_days":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpstreamStallDays != 0 {
		t.Fatalf("explicit upstream_stall_days=0 must stay 0 (disable), got %d", cfg.UpstreamStallDays)
	}
}

func TestMissingUpstreamStallDaysGetsDefault(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"obsidian_vault":"/v"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpstreamStallDays != 3 {
		t.Fatalf("missing upstream_stall_days = %d, want default 3", cfg.UpstreamStallDays)
	}
}

func TestLoadAppliesOTGEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"obsidian_vault":"/file-vault","max_concurrent_tasks":2}`), 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	t.Setenv("OTG_OBSIDIAN_VAULT", "/env-vault")
	t.Setenv("OTG_MAX_CONCURRENT_TASKS", "4")

	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ObsidianVault != "/env-vault" || cfg.MaxConcurrentTasks != 4 {
		t.Fatalf("environment overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"max_concurrent_tasks":-1,"off_peak_timezone":"Not/AZone"}`), 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	if _, err := Load(mapFile); err == nil {
		t.Fatal("expected invalid configuration error")
	}
}

func TestDefaultsSetPhaseConcurrency(t *testing.T) {
	cfg := Defaults()
	if got := cfg.ConcurrencyFor("refining"); got != 3 {
		t.Fatalf("ConcurrencyFor(refining) = %d, want 3", got)
	}
	if got := cfg.ConcurrencyFor("planning"); got != 2 {
		t.Fatalf("ConcurrencyFor(planning) = %d, want 2", got)
	}
	if got := cfg.ConcurrencyFor("merge"); got != 1 {
		t.Fatalf("ConcurrencyFor(merge) = %d, want 1", got)
	}
	// Unknown phase: unlimited.
	if got := cfg.ConcurrencyFor("grilling"); got != 0 {
		t.Fatalf("ConcurrencyFor(grilling) = %d, want 0 (unlimited)", got)
	}
}

func TestLoadReadsPhaseConcurrency(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"phase_concurrency": {"refining": 1, "planning": 4}}`)
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.ConcurrencyFor("refining"); got != 1 {
		t.Errorf("ConcurrencyFor(refining) = %d, want 1", got)
	}
	if got := cfg.ConcurrencyFor("planning"); got != 4 {
		t.Errorf("ConcurrencyFor(planning) = %d, want 4", got)
	}
	// Unlisted default phases keep their defaults (merge), listed-unset stay.
	if got := cfg.ConcurrencyFor("merge"); got != 1 {
		t.Errorf("ConcurrencyFor(merge) = %d, want default 1", got)
	}
}

func TestPhaseConcurrencyZeroDisablesGate(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"phase_concurrency": {"refining": 0}}`)
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.ConcurrencyFor("refining"); got != 0 {
		t.Errorf("ConcurrencyFor(refining) = %d, want 0 (explicitly unlimited)", got)
	}
}

func TestLoadRejectsNegativePhaseConcurrency(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"phase_concurrency": {"refining": -2}}`)
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	if _, err := Load(mapFile); err == nil {
		t.Fatal("expected invalid configuration error for negative phase_concurrency")
	}
}

func TestLoadDefaultsPerProjectConcurrency(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	// Legacy config without the per-project key: per-project falls back to 2,
	// and the legacy global value is preserved as-is.
	if err := os.WriteFile(mapFile, []byte(`{"max_concurrent_tasks": 2}`), 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxConcurrentTasks != 2 {
		t.Errorf("MaxConcurrentTasks = %d, want 2 (legacy value preserved)", cfg.MaxConcurrentTasks)
	}
	if cfg.MaxConcurrentTasksPerProject != 2 {
		t.Errorf("MaxConcurrentTasksPerProject = %d, want default 2", cfg.MaxConcurrentTasksPerProject)
	}
}

func TestLoadReadsPerProjectConcurrency(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"max_concurrent_tasks": 0, "max_concurrent_tasks_per_project": 4}`)
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxConcurrentTasks != 0 {
		t.Errorf("MaxConcurrentTasks = %d, want 0 (no global cap)", cfg.MaxConcurrentTasks)
	}
	if cfg.MaxConcurrentTasksPerProject != 4 {
		t.Errorf("MaxConcurrentTasksPerProject = %d, want 4", cfg.MaxConcurrentTasksPerProject)
	}
}

func TestLoadAppliesPerProjectEnvOverride(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"max_concurrent_tasks": 0}`), 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	t.Setenv("OTG_MAX_CONCURRENT_TASKS_PER_PROJECT", "6")
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxConcurrentTasksPerProject != 6 {
		t.Fatalf("MaxConcurrentTasksPerProject = %d, want 6 (env override)", cfg.MaxConcurrentTasksPerProject)
	}
}

func TestLoadRejectsNegativePerProjectConcurrency(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"max_concurrent_tasks_per_project": -1}`), 0o644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	if _, err := Load(mapFile); err == nil {
		t.Fatal("expected invalid configuration error for negative per-project concurrency")
	}
}

func TestExecutorDefaultAndValidation(t *testing.T) {
	if got := Defaults().Executor; got != "dsh-embed" {
		t.Fatalf("default executor=%q, want dsh-embed", got)
	}
	// Invalid executor rejected.
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"executor":"bogus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(mapFile); err == nil {
		t.Fatal("invalid executor must be rejected")
	}
	// dsh executor accepted.
	if err := os.WriteFile(mapFile, []byte(`{"executor":"dsh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Executor != "dsh" {
		t.Fatalf("executor=%q, want dsh", cfg.Executor)
	}
	// dsh-embed executor accepted (embed migration seam).
	if err := os.WriteFile(mapFile, []byte(`{"executor":"dsh-embed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Executor != "dsh-embed" {
		t.Fatalf("executor=%q, want dsh-embed", cfg2.Executor)
	}
	if cfg2.AgentServerAddr != "127.0.0.1:8799" {
		t.Fatalf("agent_server_addr=%q, want default 127.0.0.1:8799", cfg2.AgentServerAddr)
	}
	if !cfg2.AgentServerManaged {
		t.Fatal("agent_server_managed defaults to true")
	}
	// External systemd-managed agent-server can opt out of daemon child management.
	if err := os.WriteFile(mapFile, []byte(`{"executor":"dsh-embed","agent_server_managed":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg3, err := Load(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg3.AgentServerManaged {
		t.Fatal("agent_server_managed=false must be honored")
	}
}
