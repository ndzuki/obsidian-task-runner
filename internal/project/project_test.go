package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.MkdirAll(filepath.Join(dir, "my-app"), 0755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(mapFile, data, 0644); err != nil {
		t.Fatalf("write map file: %v", err)
	}

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

func TestRegisterProjectAutoProjectID(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	config := map[string]interface{}{
		"projects": []map[string]interface{}{
			{"name": "alpha", "path": "/x/alpha", "git_remote": "github.com/ndzuki/alpha", "project_id": "001"},
			{"name": "beta", "path": "/x/beta", "git_remote": "github.com/ndzuki/beta", "project_id": "005"},
		},
		"new_project_root": dir,
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Append: next id = max+1.
	if err := RegisterProject(mapFile, "gamma", "/x/gamma", "github.com/ndzuki/gamma", false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(mapFile)
	var got struct {
		Projects []map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	last := got.Projects[len(got.Projects)-1]
	if last["project_id"] != "006" {
		t.Fatalf("auto project_id = %q, want 006", last["project_id"])
	}

	// Update: project_id preserved (not clobbered by the overwrite).
	if err := RegisterProject(mapFile, "alpha", "/x/alpha-v2", "github.com/ndzuki/alpha", false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(mapFile)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Projects {
		if p["name"] == "alpha" && p["project_id"] != "001" {
			t.Fatalf("update must preserve project_id, got %q", p["project_id"])
		}
	}
}

func TestGitRemoteFor(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	config := map[string]interface{}{
		"projects": []map[string]interface{}{
			{"name": "alpha", "path": "/x/alpha", "git_remote": "github.com/ndzuki/alpha", "project_id": "001"},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GitRemoteFor(mapFile, "gamma"); got != "github.com/ndzuki/gamma" {
		t.Fatalf("GitRemoteFor = %q, want github.com/ndzuki/gamma", got)
	}
}

func TestRegisterScaffoldFromProject(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	config := map[string]interface{}{
		"projects": []interface{}{},
		"scaffold_registry": map[string]interface{}{
			"connect-rpc": map[string]interface{}{
				"aliases":     []interface{}{"connect", "grpc"},
				"description": "Connect RPC framework",
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// connect already registered (by alias) → skipped; otel is new → added.
	if err := RegisterScaffoldFromProject(mapFile, "gamma", []string{"connect", "otel"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(mapFile)
	var got struct {
		Registry map[string]map[string]interface{} `json:"scaffold_registry"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Registry["otel"]; !ok {
		t.Fatal("otel capability must be auto-added")
	}
	if _, ok := got.Registry["connect-rpc"]; !ok {
		t.Fatal("existing capability must be preserved")
	}
	if _, ok := got.Registry["connect"]; ok {
		t.Fatal("registered alias must not create a duplicate capability")
	}
	// No new topics → no write.
	before, _ := os.ReadFile(mapFile)
	if err := RegisterScaffoldFromProject(mapFile, "gamma", []string{"connect"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(mapFile)
	if string(before) != string(after) {
		t.Fatal("no-op registration must not rewrite the file")
	}
}

func TestMaintenancePreservesFieldOrder(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	// Hand-curated order: config_version first, projects last.
	curated := `{
  "config_version": 1,
  "new_project_root": "/x",
  "models": {"default": "deepseek-v4-flash"},
  "projects": [
    {
      "git_remote": "github.com/ndzuki/alpha",
      "name": "alpha",
      "path": "/x/alpha",
      "project_id": "001"
    }
  ],
  "scaffold_registry": {}
}
`
	if err := os.WriteFile(mapFile, []byte(curated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Register a new project: top-level order and existing entry field order
	// must survive; new entry fields keep their canonical order.
	if err := RegisterProject(mapFile, "beta", "/x/beta", "github.com/ndzuki/beta", false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(mapFile)
	s := string(raw)
	wantOrder := []string{"config_version", "new_project_root", "models", "projects", "scaffold_registry"}
	last := -1
	for _, key := range wantOrder {
		idx := strings.Index(s, `"`+key+`"`)
		if idx < 0 {
			t.Fatalf("key %s missing after maintenance", key)
		}
		if idx < last {
			t.Fatalf("field order changed: %s moved before an earlier field", key)
		}
		last = idx
	}
	// project_id auto-assigned for the appended entry.
	var parsed struct {
		Projects []map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Projects[1]["project_id"] != "002" {
		t.Fatalf("appended project_id = %q, want 002", parsed.Projects[1]["project_id"])
	}
}

func TestRegisterProject(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")

	config := map[string]interface{}{
		"projects":         []map[string]interface{}{},
		"new_project_root": dir,
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(mapFile, data, 0644); err != nil {
		t.Fatalf("write map file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "e2e-test"), 0755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}

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
		if err := os.MkdirAll(filepath.Join(dir, "e2e-test-v2"), 0755); err != nil {
			t.Fatalf("create update directory: %v", err)
		}
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
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(mapFile, data, 0644); err != nil {
		t.Fatalf("write map file: %v", err)
	}

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
