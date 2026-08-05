package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsSetsConcurrentTaskLimit(t *testing.T) {
	if got := Defaults().MaxConcurrentTasks; got != 2 {
		t.Fatalf("MaxConcurrentTasks = %d, want 2", got)
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

func TestDefaultModelsUsesDefaultAssignee(t *testing.T) {
	models := DefaultModels()
	if got := models["default"]; got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("default model = %q, want %q", got, "deepseek/deepseek-v4-flash")
	}
	if _, ok := models["flash"]; ok {
		t.Fatal("legacy flash assignee must not be present")
	}
}

func TestFallbackModelDefaultsToFlash(t *testing.T) {
	cfg := Defaults()
	if got := cfg.FallbackModelFor("gpt"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("FallbackModelFor(gpt) = %q, want %q", got, "deepseek/deepseek-v4-flash")
	}
	if got := cfg.FallbackModelFor("default"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("FallbackModelFor(default) = %q, want %q", got, "deepseek/deepseek-v4-flash")
	}
	if got := cfg.FallbackModelFor("deepseek"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("FallbackModelFor(deepseek) = %q, want %q", got, "deepseek/deepseek-v4-flash")
	}
	for _, assignee := range []string{"unknown", "gemini", "claude", "minimax"} {
		if got := cfg.FallbackModelFor(assignee); got != "" {
			t.Fatalf("FallbackModelFor(%s) = %q, want empty", assignee, got)
		}
	}
}

func TestFallbackModelsAreConfigurable(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"fallback_models": {"gpt": "deepseek/deepseek-v4-pro", "gemini": "deepseek/deepseek-v4-flash"}}`)
	if err := os.WriteFile(mapFile, data, 0644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Per-assignee override wins.
	if got := cfg.FallbackModelFor("gpt"); got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("FallbackModelFor(gpt) = %q, want %q", got, "deepseek/deepseek-v4-pro")
	}
	// New assignee key gets a fallback without code changes.
	if got := cfg.FallbackModelFor("gemini"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("FallbackModelFor(gemini) = %q, want %q", got, "deepseek/deepseek-v4-flash")
	}
	// Unlisted keys keep the built-in defaults (JSON merges into Defaults).
	if got := cfg.FallbackModelFor("default"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("FallbackModelFor(default) = %q, want %q", got, "deepseek/deepseek-v4-flash")
	}
	if got := cfg.FallbackModelFor("unknown"); got != "" {
		t.Fatalf("FallbackModelFor(unknown) = %q, want empty", got)
	}
}

func TestFallbackModelDisabledByEmptyValue(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	data := []byte(`{"fallback_models": {"gpt": ""}}`)
	if err := os.WriteFile(mapFile, data, 0644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}
	cfg, err := Load(mapFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.FallbackModelFor("gpt"); got != "" {
		t.Fatalf("FallbackModelFor(gpt) = %q, want empty (disabled)", got)
	}
}

func TestModelReferenceTracksDefaultModels(t *testing.T) {
	ref := ModelReference()
	for _, model := range DefaultModels() {
		if !strings.Contains(ref, model) {
			t.Fatalf("ModelReference() missing default model %q", model)
		}
	}
	for _, model := range DefaultFallbackModels() {
		if !strings.Contains(ref, model) {
			t.Fatalf("ModelReference() missing fallback model %q", model)
		}
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

func TestDefaultsSetsWorkflowConfiguration(t *testing.T) {
	cfg := Defaults()
	if cfg.ConfigVersion != 1 || cfg.ShutdownGraceSeconds != 30 || cfg.OffPeakTimezone != "Asia/Shanghai" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if got := cfg.PhaseTimeout("round2"); got.String() != "1h0m0s" {
		t.Fatalf("round2 timeout = %v, want 1h", got)
	}
	if len(cfg.OffPeakWindows) != 3 || cfg.StarvationWarningDays["P3"] != 14 || cfg.StarvationWarningDays["P4"] != 30 {
		t.Fatalf("workflow defaults = %+v", cfg)
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
