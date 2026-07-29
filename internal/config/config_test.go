package config

import (
	"os"
	"path/filepath"
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
