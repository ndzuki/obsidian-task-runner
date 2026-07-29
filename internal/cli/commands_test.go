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
