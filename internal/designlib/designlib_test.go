package designlib

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnsure(t *testing.T) {
	projectDir := t.TempDir()

	first, err := Ensure(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Root != second.Root {
		t.Fatalf("Ensure roots differ: %q vs %q", first.Root, second.Root)
	}
	for _, path := range []string{first.ContractsPath(), first.DecisionsPath(), first.WavesPath()} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("missing layout dir %s: %v", path, statErr)
		}
		if !info.IsDir() {
			t.Fatalf("layout path is not a directory: %s", path)
		}
	}
	data, err := os.ReadFile(first.GlossaryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "schema: glossary-v1") {
		t.Fatalf("unexpected glossary template:\n%s", data)
	}
}

func TestLayout_BumpRevision(t *testing.T) {
	layout, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, bumpErr := layout.BumpRevision("design-session"); bumpErr != nil {
				errs <- bumpErr
			}
		}()
	}
	wg.Wait()
	close(errs)
	for bumpErr := range errs {
		t.Fatal(bumpErr)
	}

	rev, err := layout.ReadRevision()
	if err != nil {
		t.Fatal(err)
	}
	if rev.Number != workers {
		t.Fatalf("revision=%d, want %d; concurrent bumps lost updates", rev.Number, workers)
	}
	if rev.SessionID != "design-session" || rev.UpdatedAt == "" {
		t.Fatalf("revision metadata incomplete: %+v", rev)
	}
}

func TestLayout_ReadSummary(t *testing.T) {
	layout, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(layout.ContractsPath(), "zeta.md"), "# zeta\n")
	write(filepath.Join(layout.ContractsPath(), "alpha.md"), "# alpha\n")
	write(filepath.Join(layout.DecisionsPath(), "ADR-001.md"), "# decision\n")
	write(filepath.Join(layout.WavesPath(), "wave-plan.md"), "# waves\n")
	write(layout.GlossaryPath(), "# 领域词汇表\n\nOrder = 订单\n")
	if _, err := layout.BumpRevision("s1"); err != nil {
		t.Fatal(err)
	}

	summary, err := layout.ReadSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Revision != 1 {
		t.Fatalf("revision=%d, want 1", summary.Revision)
	}
	if got := strings.Join(summary.Contracts, ","); got != "alpha.md,zeta.md" {
		t.Fatalf("contracts=%q, want sorted alpha,zeta", got)
	}
	if got := strings.Join(summary.Decisions, ","); got != "ADR-001.md" {
		t.Fatalf("decisions=%q", got)
	}
	if got := strings.Join(summary.Waves, ","); got != "wave-plan.md" {
		t.Fatalf("waves=%q", got)
	}
	if !summary.HasGlossary {
		t.Fatal("expected glossary in summary")
	}
}

func TestLayout_ReadSummaryEmpty(t *testing.T) {
	tests := []struct {
		name   string
		layout func(*testing.T) *Layout
	}{
		{
			name: "missing design directory",
			layout: func(t *testing.T) *Layout {
				return ForProject(t.TempDir())
			},
		},
		{
			name: "ensure skeleton with placeholder glossary",
			layout: func(t *testing.T) *Layout {
				layout, err := Ensure(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				return layout
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.layout(t).ReadSummary()
			if !errors.Is(err, ErrEmpty) {
				t.Fatalf("ReadSummary empty error=%v, want ErrEmpty", err)
			}
		})
	}
}

func TestLayout_Validate(t *testing.T) {
	layout, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(layout.GlossaryPath(), "---\nschema: glossary-v1\n---\n# Glossary\n")
	write(filepath.Join(layout.ContractsPath(), "order-api.md"), "---\nschema: contract-v1\nid: order-api\ntitle: Order API\n---\n# Contract\n")
	write(filepath.Join(layout.DecisionsPath(), "ADR-001.md"), "---\nschema: decision-v1\nid: ADR-001\ntitle: Storage\nstatus: accepted\n---\n# Decision\n")
	write(filepath.Join(layout.WavesPath(), "wave-0.md"), "---\nschema: wave-v1\nid: wave-0\ntitle: Contract first\n---\n# Wave 0\n")

	if err := layout.Validate(); err != nil {
		t.Fatalf("valid design library rejected: %v", err)
	}
}

func TestLayout_ValidateRejectsInvalidLibrary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Layout)
		want   string
	}{
		{
			name:   "placeholder skeleton",
			mutate: func(*testing.T, *Layout) {},
			want:   "contracts: no artifacts",
		},
		{
			name: "decision missing status",
			mutate: func(t *testing.T, layout *Layout) {
				writeValidDesignArtifacts(t, layout)
				if err := os.WriteFile(filepath.Join(layout.DecisionsPath(), "ADR-001.md"), []byte("---\nschema: decision-v1\nid: ADR-001\ntitle: Storage\n---\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "status is required",
		},
		{
			name: "duplicate contract id",
			mutate: func(t *testing.T, layout *Layout) {
				writeValidDesignArtifacts(t, layout)
				if err := os.WriteFile(filepath.Join(layout.ContractsPath(), "order-api-copy.md"), []byte("---\nschema: contract-v1\nid: order-api\ntitle: Copy\n---\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "duplicate id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout, err := Ensure(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, layout)
			err = layout.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate error=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func writeValidDesignArtifacts(t *testing.T, layout *Layout) {
	t.Helper()
	files := map[string]string{
		layout.GlossaryPath(): "---\nschema: glossary-v1\n---\n# Glossary\n",
		filepath.Join(layout.ContractsPath(), "order-api.md"): "---\nschema: contract-v1\nid: order-api\ntitle: Order API\n---\n",
		filepath.Join(layout.DecisionsPath(), "ADR-001.md"):   "---\nschema: decision-v1\nid: ADR-001\ntitle: Storage\nstatus: accepted\n---\n",
		filepath.Join(layout.WavesPath(), "wave-0.md"):        "---\nschema: wave-v1\nid: wave-0\ntitle: Contract first\n---\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLayout_SliceForTask(t *testing.T) {
	layout, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(layout.GlossaryPath(), "# Glossary\nOrder = 订单\n")
	write(filepath.Join(layout.WavesPath(), "wave-plan.md"), "# Wave 0\nTASK-001 first\n")
	write(filepath.Join(layout.ContractsPath(), "order-api.md"), `---
related:
  - TASK-001
---
# Order API
POST /orders
`)
	write(filepath.Join(layout.ContractsPath(), "billing-api.md"), `---
related:
  - TASK-999
---
# Billing API
POST /charges
`)
	write(filepath.Join(layout.DecisionsPath(), "ADR-order-storage.md"), "# Order Storage\nUse SQLite\n")
	write(filepath.Join(layout.DecisionsPath(), "ADR-unrelated.md"), "# Other\nDo not include\n")

	slice, err := layout.SliceForTask("TASK-001", "implement order storage and order api", 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Glossary", "Wave 0", "Order API", "Order Storage"} {
		if !strings.Contains(slice, want) {
			t.Fatalf("slice missing %q:\n%s", want, slice)
		}
	}
	for _, unwanted := range []string{"Billing API", "Do not include"} {
		if strings.Contains(slice, unwanted) {
			t.Fatalf("slice unexpectedly contains %q:\n%s", unwanted, slice)
		}
	}
}

func TestLayout_SliceForTaskCapsBytes(t *testing.T) {
	layout, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.GlossaryPath(), []byte(strings.Repeat("x", 2048)), 0o644); err != nil {
		t.Fatal(err)
	}

	const maxBytes = 128
	slice, err := layout.SliceForTask("TASK-001", "", maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	// Section header and truncation marker are metadata overhead; the source
	// body itself must be capped and the result must clearly mark truncation.
	if !strings.Contains(slice, "截断") {
		t.Fatalf("capped slice missing truncation marker: %q", slice)
	}
	if len(slice) > maxBytes+128 {
		t.Fatalf("slice len=%d exceeds cap with reasonable header overhead", len(slice))
	}
}
