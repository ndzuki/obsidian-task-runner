package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// TestUpdateRoadmapCreateAndAppend guards the deterministic milestone log:
// first event creates the document with a template, later events append, and
// the same (date, title) event never duplicates.
func TestUpdateRoadmapCreateAndAppend(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, "Notes", "Roadmap.md")

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)

	// First event creates the file.
	runner.updateRoadmap("test", "任务自动收口", "3 个任务自动收口为 done")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("roadmap not created: %v", err)
	}
	if !strings.Contains(string(data), "任务自动收口") || !strings.Contains(string(data), "3 个任务自动收口为 done") {
		t.Fatalf("milestone missing:\n%s", data)
	}

	// Second event with a different title appends.
	runner.updateRoadmap("test", "决策归档", "5 条已答决策自动归档")
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "决策归档") {
		t.Fatalf("second milestone missing:\n%s", data)
	}

	// Idempotent: same title again must not duplicate.
	before := string(data)
	runner.updateRoadmap("test", "决策归档", "5 条已答决策自动归档")
	after, _ := os.ReadFile(path)
	if string(after) != before {
		t.Fatalf("duplicate milestone appended:\n%s", after)
	}
}

// TestUpdateRoadmapProjectScoped guards project isolation: two projects get
// separate Roadmap files.
func TestUpdateRoadmapProjectScoped(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "001-a", "Notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "002-b", "Notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)

	runner.updateRoadmap("a", "阶段评审触发", "Phase 1 评审已触发")
	runner.updateRoadmap("b", "任务收口", "1 个任务收口")

	pa := filepath.Join(vault, "Projects", "001-a", "Notes", "Roadmap.md")
	pb := filepath.Join(vault, "Projects", "002-b", "Notes", "Roadmap.md")
	da, _ := os.ReadFile(pa)
	db, _ := os.ReadFile(pb)
	if !strings.Contains(string(da), "阶段评审触发") || strings.Contains(string(da), "任务收口") {
		t.Fatalf("project a roadmap polluted:\n%s", da)
	}
	if !strings.Contains(string(db), "任务收口") || strings.Contains(string(db), "阶段评审触发") {
		t.Fatalf("project b roadmap polluted:\n%s", db)
	}
}
