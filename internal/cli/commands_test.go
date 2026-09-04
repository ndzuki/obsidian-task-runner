package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigShowEffectiveRedactsModels(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"models":{"default":"secret-model"},"max_concurrent_tasks":2}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := newRootCommand("test")
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"config", "show", "--effective", "--redact", "--map-file", mapFile})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out.String(), "secret-model") || !strings.Contains(out.String(), "<redacted>") {
		t.Fatalf("redacted output = %s", out.String())
	}
}

// TestConfigMigrateAppendsMissingDefaultsOnly guards `config migrate --write`:
// it must APPEND missing schema fields (kb_vault, env_cleanup, …) to an
// existing vault-map.json while NEVER overwriting user-set values — the
// vault-map.json protection clause. This is what `make deploy` uses to bring
// an older config up to date with new capability fields.
func TestConfigMigrateAppendsMissingDefaultsOnly(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	// 旧版 config：无 kb_vault / env_cleanup，且 obsidian_vault/kb_db/projects 为用户自定义值。
	old := `{"obsidian_vault":"/my/vault","kb_db":"/my/custom.sqlite","projects":[{"name":"p","path":"/p"}]}`
	if err := os.WriteFile(mapFile, []byte(old), 0o644); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	cmd := newRootCommand("test")
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"config", "migrate", "--map-file", mapFile, "--write"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config migrate --write: %v", err)
	}

	data, err := os.ReadFile(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("migrated config not valid JSON: %v", err)
	}

	// 新字段已补齐（空 kb_vault 也可见，便于用户填写）。
	// 零值默认（空字符串）不落盘：kb_vault/dsh_profile 留待用户显式设置。
	if _, ok := got["kb_vault"]; ok {
		t.Fatalf("kb_vault empty default must not be written by migrate, got %v", got["kb_vault"])
	}
	if _, ok := got["dsh_profile"]; ok {
		t.Fatalf("dsh_profile empty default must not be written by migrate, got %v", got["dsh_profile"])
	}
	if _, ok := got["env_cleanup"]; ok {
		t.Fatalf("env_cleanup must be opt-in and not auto-added by migrate, got %v", got["env_cleanup"])
	}

	// 用户已有值必须原样保留。
	if got["obsidian_vault"] != "/my/vault" {
		t.Fatalf("obsidian_vault overwritten: %v", got["obsidian_vault"])
	}
	if got["kb_db"] != "/my/custom.sqlite" {
		t.Fatalf("kb_db overwritten: %v", got["kb_db"])
	}
	projs, ok := got["projects"].([]interface{})
	if !ok || len(projs) != 1 {
		t.Fatalf("projects mangled: %v", got["projects"])
	}
}

func TestStatusJSON(t *testing.T) {
	cmd := newRootCommand("test")
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"status", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, out.String())
	}
	if _, ok := payload["daemon_lock"]; !ok {
		t.Fatalf("status JSON missing daemon_lock: %v", payload)
	}
}

func TestReviewJSON(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\ntarget_branch: task/001\n---\n# Task\n"), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	cmd := newRootCommand("test")
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"review", taskPath, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), `"task_id":"001"`) || !strings.Contains(out.String(), `"target_branch":"task/001"`) {
		t.Fatalf("review JSON = %s", out.String())
	}
}
