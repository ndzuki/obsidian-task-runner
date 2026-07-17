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
