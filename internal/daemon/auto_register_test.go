package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

func newAutoRegRunner(t *testing.T, vault, skillDir string) *Runner {
	t.Helper()
	cfg := &config.Config{
		ObsidianVault:  vault,
		SkillInstallDir: skillDir,
		// Conventional checkout root; the directory does not exist, so
		// registration must fall back to the vault project dir.
		NewProjectRoot: filepath.Join(t.TempDir(), "repos"),
	}
	return &Runner{cfg: cfg, logger: log.New(io.Discard, "", 0)}
}

func readVaultMapProjects(t *testing.T, mapFile string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(mapFile)
	if err != nil {
		t.Fatalf("read vault-map: %v", err)
	}
	var parsed struct {
		Projects []map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse vault-map: %v", err)
	}
	return parsed.Projects
}

func findProject(t *testing.T, projects []map[string]string, name string) map[string]string {
	t.Helper()
	for _, p := range projects {
		if p["name"] == name {
			return p
		}
	}
	t.Fatalf("project %q not registered: %+v", name, projects)
	return nil
}

func TestEnsureProjectRegisteredAutoRegisters(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqDir := filepath.Join(vault, "Projects", "010-demo", "Requirements")
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reqDir, "REQ-001-demo.md"), []byte("# Demo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{
		"release-manager": "/tmp/release-manager",
	})
	mapFile := filepath.Join(skillDir, "config", "vault-map.json")
	// Give the existing project a git_remote so the new entry's remote is
	// derived from the established owner (github.com/ndzuki/...).
	var vaultMap map[string]any
	raw, err := os.ReadFile(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &vaultMap); err != nil {
		t.Fatal(err)
	}
	vaultMap["projects"] = []map[string]string{
		{"name": "release-manager", "path": "/tmp/release-manager", "git_remote": "github.com/ndzuki/release-manager"},
	}
	out, err := json.Marshal(vaultMap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mapFile, out, 0644); err != nil {
		t.Fatal(err)
	}

	runner := newAutoRegRunner(t, vault, skillDir)
	runner.ensureProjectRegistered("Projects/010-demo/Requirements/REQ-001-demo.md")

	projects := readVaultMapProjects(t, filepath.Join(skillDir, "config", "vault-map.json"))
	entry := findProject(t, projects, "demo")
	if entry["path"] != filepath.Join(vault, "Projects", "010-demo") {
		t.Fatalf("demo path = %q, want vault project dir", entry["path"])
	}
	// project_id is auto-assigned by RegisterProject (max existing + 1),
	// matching the semantics of the new-project registration path.
	if entry["project_id"] == "" || len(entry["project_id"]) != 3 {
		t.Fatalf("demo project_id = %q, want auto-assigned %03d", entry["project_id"], len(entry["project_id"]))
	}
	if entry["git_remote"] != "github.com/ndzuki/demo" {
		t.Fatalf("demo git_remote = %q, want github.com/ndzuki/demo", entry["git_remote"])
	}

	// Idempotent: a second call must not duplicate or fail.
	before := len(projects)
	runner.ensureProjectRegistered("Projects/010-demo/Requirements/REQ-001-demo.md")
	if got := len(readVaultMapProjects(t, filepath.Join(skillDir, "config", "vault-map.json"))); got != before {
		t.Fatalf("project count changed on re-register: %d → %d", before, got)
	}
}

func TestEnsureProjectRegisteredPrefersRepoCheckout(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "010-demo", "Requirements"), 0755); err != nil {
		t.Fatal(err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{})
	repoRoot := filepath.Join(dir, "repos")
	if err := os.MkdirAll(filepath.Join(repoRoot, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		cfg: &config.Config{
			ObsidianVault:  vault,
			SkillInstallDir: skillDir,
			NewProjectRoot: repoRoot,
		},
		logger: log.New(io.Discard, "", 0),
	}

	runner.ensureProjectRegistered("Projects/010-demo/Requirements/REQ-001-demo.md")

	projects := readVaultMapProjects(t, filepath.Join(skillDir, "config", "vault-map.json"))
	entry := findProject(t, projects, "demo")
	if entry["path"] != filepath.Join(repoRoot, "demo") {
		t.Fatalf("demo path = %q, want conventional repo checkout", entry["path"])
	}
}

func TestEnsureProjectRegisteredExistingProjectNoop(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "001-release-manager", "Requirements"), 0755); err != nil {
		t.Fatal(err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"release-manager": "/tmp/release-manager"})

	runner := newAutoRegRunner(t, vault, skillDir)
	runner.ensureProjectRegistered("Projects/001-release-manager/Requirements/REQ-001.md")

	projects := readVaultMapProjects(t, filepath.Join(skillDir, "config", "vault-map.json"))
	if len(projects) != 1 {
		t.Fatalf("registered projects = %d, want 1 (no auto-append for known projects)", len(projects))
	}
}

func TestScanOrphanReqsCreatesTaskAndRegisters(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqDir := filepath.Join(vault, "Projects", "010-demo", "Requirements")
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		t.Fatal(err)
	}
	reqContent := `---
id: ""
title: 演示
priority: p0
project_id: "010"
---

# 演示

## 要做什么

写一个五子棋小游戏的html

## 完成标准
- [ ] 验收条件 1
- [ ] 验收条件 2
`
	if err := os.WriteFile(filepath.Join(reqDir, "REQ-001-demo.md"), []byte(reqContent), 0644); err != nil {
		t.Fatal(err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{})

	runner := newAutoRegRunner(t, vault, skillDir)
	runner.scanOrphanReqs()

	// Canonical TASK created.
	taskPath := filepath.Join(vault, "Projects", "010-demo", "Tasks", "TASK-001-demo.md")
	if _, err := os.Stat(taskPath); err != nil {
		t.Fatalf("canonical TASK not created: %v", err)
	}

	// Project auto-registered.
	projects := readVaultMapProjects(t, filepath.Join(skillDir, "config", "vault-map.json"))
	findProject(t, projects, "demo")

	// Idempotent: a second scan must not rewrite the canonical TASK.
	marker := "marker — an idempotent rescan must not rewrite this"
	if err := os.WriteFile(taskPath, []byte(marker), 0644); err != nil {
		t.Fatal(err)
	}
	runner.scanOrphanReqs()
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("TASK disappeared: %v", err)
	}
	if string(data) != marker {
		t.Fatalf("TASK content rewritten by idempotent scan: %q", data)
	}
}
