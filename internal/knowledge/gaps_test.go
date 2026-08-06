package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScanKnowledgeGaps verifies the ADR → References coverage scan: ADRs
// whose decisions match a knowledge document are not gaps; unmatched ones are.
func TestScanKnowledgeGaps(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	adrDir := filepath.Join(vault, "Projects", "demo", "Notes", "adr")
	refsDir := filepath.Join(vault, "References", "core", "go")
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Knowledge document with topics connect/rpc.
	refDoc := "---\ntopics: [connect, rpc, grpc]\nlevel: reference\nupdated: \"2026-08-03\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# Connect\n\n> summary\n"
	if err := os.WriteFile(filepath.Join(refsDir, "connect-rpc.md"), []byte(refDoc), 0o644); err != nil {
		t.Fatal(err)
	}

	// ADR with connect topics → covered.
	covered := "---\nid: \"001\"\ntitle: Use Connect RPC\nstatus: accepted\n---\n# Use Connect RPC\n\n决定采用 Connect + protobuf 统一协议。\n"
	if err := os.WriteFile(filepath.Join(adrDir, "ADR-001-connect.md"), []byte(covered), 0o644); err != nil {
		t.Fatal(err)
	}
	// ADR with an exotic stack → gap.
	gap := "---\nid: \"002\"\ntitle: Zircon Mesh Integration\nstatus: accepted\n---\n# Zircon Mesh Integration\n\n决定引入 Zircon 服务网格。\n"
	if err := os.WriteFile(filepath.Join(adrDir, "ADR-002-zircon.md"), []byte(gap), 0o644); err != nil {
		t.Fatal(err)
	}

	gaps, err := ScanKnowledgeGaps(vault, "demo")
	if err != nil {
		t.Fatalf("ScanKnowledgeGaps: %v", err)
	}
	if len(gaps) != 1 || gaps[0].ADR != "ADR-002-zircon.md" {
		t.Fatalf("gaps = %+v, want only ADR-002-zircon.md", gaps)
	}
}
