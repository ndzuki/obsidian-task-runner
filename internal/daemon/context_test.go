package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProjectContext_ReleaseManager(t *testing.T) {
	dir := t.TempDir()

	// Create a minimal CONTEXT.md with constraints, anti-patterns, and domain terms.
	notesDir := dir + "/Notes"
	if err := os.MkdirAll(notesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notesDir+"/CONTEXT.md", []byte("## Development Constraints\n- SDK-only: 运行时只使用 Go SDK\n- 技术栈: Go 1.26+\n\n## Anti-patterns\n- 禁止数据库直读写\n\n## Language\n**ReleaseDefinition**: 发布目标定义\n**ReleaseBundle**: 发布内容\n**ValuesRevision**: 配置快照\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a REQ file with keywords.
	reqPath := dir + "/REQ.md"
	if err := os.WriteFile(reqPath, []byte("# REQ\n\nReleaseDefinition 和 ValuesRevision 需要 SDK-only 约束\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := BuildProjectContext(dir, reqPath)
	if ctx == "" {
		t.Fatal("expected non-empty context")
	}
	t.Logf("\n=== Generated Context (%d bytes) ===\n%s\n=== End ===", len(ctx), ctx)

	// Must contain constraints
	if !strings.Contains(ctx, "SDK-only") {
		t.Error("missing SDK-only constraint")
	}
	// Must contain domain terms
	if !strings.Contains(ctx, "Domain Terms") {
		t.Error("missing Domain Terms section")
	}
	// Must not exceed ~600 bytes
	if len(ctx) > 700 {
		t.Errorf("context too large: %d bytes (target < 700)", len(ctx))
	}
}

func TestContextCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	notesDir := filepath.Join(dir, "Notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(notesDir, "CONTEXT.md")
	if err := os.WriteFile(contextPath, []byte("## Development Constraints\n- v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(dir, "REQ.md")
	if err := os.WriteFile(reqPath, []byte("# REQ\n\nconstraint\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx1 := BuildProjectContext(dir, reqPath)
	if !strings.Contains(ctx1, "v1") {
		t.Fatalf("initial context missing v1: %q", ctx1)
	}

	// CONTEXT.md updated but not invalidated → cache serves stale content.
	if err := os.WriteFile(contextPath, []byte("## Development Constraints\n- v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ctx2 := BuildProjectContext(dir, reqPath); strings.Contains(ctx2, "v2") {
		t.Fatal("cache should serve stale content until invalidated")
	}

	// Watcher invalidation makes the next dispatch fresh.
	invalidateProjectContext(contextPath)
	if ctx3 := BuildProjectContext(dir, reqPath); !strings.Contains(ctx3, "v2") {
		t.Fatalf("context missing v2 after invalidation: %q", ctx3)
	}
}

func TestADRCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	adrDir := filepath.Join(dir, "Notes", "adr")
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeADR := func(name, title, decision string) {
		content := fmt.Sprintf("---\nadr_id: \"001\"\ntitle: %s\nstatus: accepted\n---\n\n## Decision\n\n%s\n", title, decision)
		if err := os.WriteFile(filepath.Join(adrDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeADR("ADR-001-a.md", "First", "决策一。")

	adrs := readADRs(adrDir)
	if len(adrs) != 1 {
		t.Fatalf("want 1 ADR, got %d", len(adrs))
	}
	// New ADR without invalidation → cache stays at 1.
	writeADR("ADR-002-b.md", "Second", "决策二。")
	if got := len(readADRs(adrDir)); got != 1 {
		t.Fatalf("cache should stay at 1, got %d", got)
	}
	// Invalidation reflects the new ADR.
	invalidateProjectContext(filepath.Join(adrDir, "ADR-002-b.md"))
	if got := len(readADRs(adrDir)); got != 2 {
		t.Fatalf("want 2 ADRs after invalidation, got %d", got)
	}
}
