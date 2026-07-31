package daemon

import (
	"os"
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
