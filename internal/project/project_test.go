package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProject(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")

	config := map[string]interface{}{
		"projects": []map[string]interface{}{
			{"name": "my-app", "path": filepath.Join(dir, "my-app"), "git_remote": "github.com/user/my-app"},
		},
		"new_project_root": "/home/user/src",
	}
	os.MkdirAll(filepath.Join(dir, "my-app"), 0755)
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(mapFile, data, 0644)

	t.Run("existing", func(t *testing.T) {
		r := ResolveProject(mapFile, "my-app", false)
		if r.Status != "existing" {
			t.Errorf("status = %q, want existing", r.Status)
		}
		if r.Path != filepath.Join(dir, "my-app") {
			t.Errorf("path = %q", r.Path)
		}
	})

	t.Run("new project", func(t *testing.T) {
		r := ResolveProject(mapFile, "new-app", true)
		if r.Status != "new" {
			t.Errorf("status = %q, want new", r.Status)
		}
		if r.Path != "/home/user/src/new-app" {
			t.Errorf("path = %q", r.Path)
		}
	})

	t.Run("not found", func(t *testing.T) {
		r := ResolveProject(mapFile, "unknown", false)
		if r.Status != "error" {
			t.Errorf("status = %q, want error", r.Status)
		}
	})
}

func TestRegisterProject(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")

	config := map[string]interface{}{
		"projects": []map[string]interface{}{},
		"new_project_root": dir,
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(mapFile, data, 0644)
	os.MkdirAll(filepath.Join(dir, "e2e-test"), 0755)

	t.Run("add new", func(t *testing.T) {
		err := RegisterProject(mapFile, "e2e-test", filepath.Join(dir, "e2e-test"), "", false)
		if err != nil {
			t.Fatalf("RegisterProject: %v", err)
		}

		// Verify it was added
		r := ResolveProject(mapFile, "e2e-test", false)
		if r.Status != "existing" {
			t.Errorf("after register, status = %q, want existing (path=%q)", r.Status, r.Path)
		}
		if r.Path != filepath.Join(dir, "e2e-test") {
			t.Errorf("path = %q", r.Path)
		}
	})

	t.Run("update existing", func(t *testing.T) {
		os.MkdirAll(filepath.Join(dir, "e2e-test-v2"), 0755)
		err := RegisterProject(mapFile, "e2e-test", filepath.Join(dir, "e2e-test-v2"), "git@github.com:x/y.git", false)
		if err != nil {
			t.Fatalf("RegisterProject update: %v", err)
		}

		r := ResolveProject(mapFile, "e2e-test", false)
		if r.Path != filepath.Join(dir, "e2e-test-v2") {
			t.Errorf("after update, path = %q, want %s", r.Path, filepath.Join(dir, "e2e-test-v2"))
		}
	})

	t.Run("dry run", func(t *testing.T) {
		before, _ := os.ReadFile(mapFile)
		err := RegisterProject(mapFile, "dry-test", filepath.Join(dir, "dry"), "", true)
		if err != nil {
			t.Fatalf("RegisterProject dry-run: %v", err)
		}
		after, _ := os.ReadFile(mapFile)
		if string(before) != string(after) {
			t.Error("dry-run should not modify file")
		}
	})
}

func TestMatchVaultDir(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")

	config := map[string]interface{}{
		"projects": []map[string]interface{}{
			{"name": "release-manager", "path": "/tmp/release-manager"},
			{"name": "obsidian-task-runner", "path": "/tmp/otr"},
			{"name": "simple", "path": "/tmp/simple"},
		},
		"new_project_root": "/home/user/src",
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(mapFile, data, 0644)

	tests := []struct {
		name     string
		vaultDir string
		want     string
	}{
		{"exact match", "release-manager", "release-manager"},
		{"prefix match", "001-release-manager", "release-manager"},
		{"prefix match multi-digit", "042-release-manager", "release-manager"},
		{"no prefix needed", "simple", "simple"},
		{"no match", "unknown-project", ""},
		{"prefix but no suffix match", "001-unknown", ""},
		{"numeric suffix only - no match", "001-release-manager-v2", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchVaultDir(mapFile, tt.vaultDir)
			if got != tt.want {
				t.Errorf("MatchVaultDir(%q) = %q, want %q", tt.vaultDir, got, tt.want)
			}
		})
	}

	// Test missing map file
	t.Run("missing map file", func(t *testing.T) {
		got := MatchVaultDir("/nonexistent/vault-map.json", "001-foo")
		if got != "" {
			t.Errorf("expected empty from missing file, got %q", got)
		}
	})
}

func TestExtractProjectID(t *testing.T) {
	tests := []struct {
		name    string
		dirName string
		want    string
	}{
		{"standard", "003-obsidian-task-runner", "003"},
		{"multi-digit", "042-my-project", "042"},
		{"single-digit", "1-release", "1"},
		{"no-dash", "myproject", ""},
		{"dash-but-no-digits", "abc-def", ""},
		{"all-digits", "123", "123"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractProjectID(tt.dirName)
			if got != tt.want {
				t.Errorf("ExtractProjectID(%q) = %q, want %q", tt.dirName, got, tt.want)
			}
		})
	}
}
