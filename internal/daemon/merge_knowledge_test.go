package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestMeasureKnowledgeApplied(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core", "go")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refsDir, "connect-rpc.md"), []byte("# Connect\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	taskDir := filepath.Join(vault, "Projects", "p", "Tasks")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(taskDir, "TASK-001-a.md")
	taskContent := "---\nid: \"001\"\ntitle: T\nproject: p\nassignee: gpt\nstatus: done\nknowledge_refs:\n  - core/go/connect-rpc.md\n  - core/go/missing.md\n  - References/core/go/connect-rpc.md\n---\n"
	if err := os.WriteFile(taskPath, []byte(taskContent), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner("", "", "", 1)
	runner.cfg.ObsidianVault = vault
	if err := runner.measureKnowledgeApplied(taskPath, vault); err != nil {
		t.Fatalf("measureKnowledgeApplied: %v", err)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("reparse task: %v", err)
	}
	// connect-rpc.md exists (twice referenced, dedup by file check) → 2 of 3
	// unique-ish refs hit; duplicate ref counts separately by stat.
	if fm.KnowledgeApplied != "2/3" {
		t.Fatalf("knowledge_applied = %q, want 2/3", fm.KnowledgeApplied)
	}
}

func TestMeasureKnowledgeAppliedNoRefs(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	taskDir := filepath.Join(vault, "Projects", "p", "Tasks")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(taskDir, "TASK-002-b.md")
	content := "---\nid: \"002\"\ntitle: T2\nproject: p\nassignee: gpt\nstatus: done\n---\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := newTestRunner("", "", "", 1)
	runner.cfg.ObsidianVault = vault
	if err := runner.measureKnowledgeApplied(taskPath, vault); err != nil {
		t.Fatalf("measureKnowledgeApplied: %v", err)
	}
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)
	if fm.KnowledgeApplied != "" {
		t.Fatalf("no refs must leave knowledge_applied empty, got %q", fm.KnowledgeApplied)
	}
}
