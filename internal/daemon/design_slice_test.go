package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/designlib"
)

func TestDesignSliceForTask(t *testing.T) {
	runner, _, projectDir := newDesignTestRunner(t)

	// Empty design library → empty slice.
	if got := runner.designSliceForTask("demo", "001", "REQ-001.md"); got != "" {
		t.Fatalf("empty design library should yield empty slice, got %q", got)
	}

	// Populate the design library plus a task-id-only matched contract.
	if err := writeValidDesignLibrary(t, projectDir); err != nil {
		t.Fatal(err)
	}
	layout := designlib.ForProject(projectDir)
	if err := os.WriteFile(filepath.Join(layout.ContractsPath(), "billing-api.md"), []byte(`---
schema: contract-v1
id: billing-api
title: Billing API
related:
  - TASK-001
---
# Billing API
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A REQ that mentions only "order"; billing is matched by task id alone.
	reqRel := filepath.Join("Projects", "001-demo", "Requirements", "REQ-001.md")
	reqAbs := filepath.Join(runner.cfg.ObsidianVault, reqRel)
	if err := os.MkdirAll(filepath.Dir(reqAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reqAbs, []byte("implement order api"), 0o644); err != nil {
		t.Fatal(err)
	}

	slice := runner.designSliceForTask("demo", "001", reqRel)
	for _, want := range []string{"Glossary", "Wave 0", "Order API", "Billing API"} {
		if !strings.Contains(slice, want) {
			t.Fatalf("slice missing %q:\n%s", want, slice)
		}
	}
	// ADR-001 (Storage) has neither related nor keyword overlap → excluded.
	if strings.Contains(slice, "Storage") {
		t.Fatalf("slice unexpectedly contains unrelated ADR:\n%s", slice)
	}
}

func TestInjectDesignLibrarySlice(t *testing.T) {
	got := injectDesignLibrarySlice("/skill task.md", "## contract")
	for _, want := range []string{"/skill task.md", "<design_library>", "## contract", "</design_library>", "设计库切片"} {
		if !strings.Contains(got, want) {
			t.Fatalf("injected prompt missing %q:\n%s", want, got)
		}
	}
}
