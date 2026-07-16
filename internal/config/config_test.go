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
