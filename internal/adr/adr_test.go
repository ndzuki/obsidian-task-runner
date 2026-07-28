package adr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteValidateAndBuildIndex(t *testing.T) {
	projectDir := t.TempDir()
	path, err := Write(projectDir, "003", "状态机驱动 TASK 生命周期", `# ADR-001: 状态机驱动 TASK 生命周期

## Status
accepted

## Context
需要离散生命周期。

## Decision
使用显式状态机。

## Alternatives Considered
自由文本状态被拒绝。

## Consequences
状态可审计。
`)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Validate(path); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := BuildIndex(projectDir); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	index, err := os.ReadFile(filepath.Join(projectDir, "Notes", "adr", "ADR-INDEX.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".md")
	if !strings.Contains(string(index), stem) || !strings.Contains(string(index), "TASK-003") {
		t.Fatalf("index = %s", index)
	}
}

func TestValidateRejectsMissingDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ADR-001-invalid.md")
	if err := os.WriteFile(path, []byte("# ADR-001: Invalid\n\n## Status\naccepted\n"), 0o644); err != nil {
		t.Fatalf("write ADR: %v", err)
	}
	if err := Validate(path); err == nil {
		t.Fatal("expected invalid ADR")
	}
}
