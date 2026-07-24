package daemon

import (
	"strings"
	"testing"
)

func TestBuildProjectContext_ReleaseManager(t *testing.T) {
	projectVaultDir := "/home/nd/nutstore/Vault/Projects/001-release-manager"
	reqPath := "/home/nd/nutstore/Vault/Projects/001-release-manager/Requirements/REQ-061-install-sdk-quality.md"

	ctx := BuildProjectContext(projectVaultDir, reqPath)
	if ctx == "" {
		t.Fatal("expected non-empty context for release-manager project")
	}
	// Always log the full context for inspection.
	t.Logf("\n=== Generated Context (%d bytes) ===\n%s\n=== End ===", len(ctx), ctx)

	t.Logf("context: %d bytes", len(ctx))
	t.Logf("\n%s", ctx)

	// Must contain constraints
	if !strings.Contains(ctx, "SDK-only") {
		t.Error("missing SDK-only constraint")
	}
	// Must contain domain terms
	if !strings.Contains(ctx, "Domain Terms") {
		t.Error("missing Domain Terms section")
	}
	// Must not exceed ~600 bytes (rough token budget check)
	if len(ctx) > 700 {
		t.Errorf("context too large: %d bytes (target < 700)", len(ctx))
	}
}
