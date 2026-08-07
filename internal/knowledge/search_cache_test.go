package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSearchIndexCachedRoundtrip(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core", "go")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "connect-rpc.md", "connect, rpc, grpc", "Connect 轻量 RPC 框架。")

	idx1, err := BuildSearchIndexCached(vault, false)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if got := idx1.totalDocs; got != 1 {
		t.Fatalf("totalDocs = %d, want 1", got)
	}
	// Cache file must exist after first build.
	if _, err := os.Stat(filepath.Join(vault, "References", searchCacheFile)); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
	// Second build hits the cache and returns an equivalent index.
	idx2, err := BuildSearchIndexCached(vault, false)
	if err != nil {
		t.Fatalf("cached build: %v", err)
	}
	hits := idx2.Search("connect rpc", 3)
	if len(hits) == 0 || hits[0].Path != "core/go/connect-rpc.md" {
		t.Fatalf("cached search failed: %+v", hits)
	}
}

func TestBuildSearchIndexCachedInvalidates(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "alpha", "文档 A")
	if _, err := BuildSearchIndexCached(vault, false); err != nil {
		t.Fatal(err)
	}
	// Add a document: fingerprint changes → cache rebuilds.
	writeSearchDoc(t, refsDir, "b.md", "beta", "文档 B")
	idx, err := BuildSearchIndexCached(vault, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.totalDocs; got != 2 {
		t.Fatalf("totalDocs after add = %d, want 2 (cache must invalidate)", got)
	}
	if hits := idx.Search("beta", 3); len(hits) == 0 || hits[0].Path != "core/b.md" {
		t.Fatalf("new doc not searchable: %+v", hits)
	}
}

func TestBuildSearchIndexCachedSkipsArchived(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	coreDir := filepath.Join(vault, "References", "core")
	archDir := filepath.Join(vault, "References", "archived", "languages")
	for _, d := range []string{coreDir, archDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSearchDoc(t, coreDir, "go-doc.md", "golang", "Go 文档")
	writeSearchDoc(t, archDir, "rust-doc.md", "rust", "Rust 文档")

	// Default: archived layer excluded.
	idx, err := BuildSearchIndexCached(vault, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.totalDocs; got != 1 {
		t.Fatalf("skipArchived totalDocs = %d, want 1", got)
	}
	// Explicit include: both layers.
	idxAll, err := BuildSearchIndexCached(vault, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := idxAll.totalDocs; got != 2 {
		t.Fatalf("include archived totalDocs = %d, want 2", got)
	}
	// The two cache modes must not collide (fingerprint differs).
	if _, err := BuildSearchIndexCached(vault, true); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSearchIndexCached(vault, false); err != nil {
		t.Fatal(err)
	}
}

func TestVectorStoreGobRoundtrip(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := vectorIndex{
		"core/a.md": {
			Title:      "A",
			SourceHash: "abc",
			Chunks: []chunkVector{
				{Heading: "## 要点", Vector: []float64{0.1, 0.2, 0.3}},
			},
		},
	}
	if err := SaveVectors(vault, idx, "bge-m3"); err != nil {
		t.Fatalf("SaveVectors: %v", err)
	}
	if _, err := os.Stat(filepath.Join(refsDir, vectorIndexGobFile)); err != nil {
		t.Fatalf("gob file missing: %v", err)
	}
	got := LoadVectors(vault)
	if got == nil || len(got["core/a.md"].Chunks) != 1 {
		t.Fatalf("roundtrip lost data: %+v", got)
	}
	if got["core/a.md"].SourceHash != "abc" || got["core/a.md"].Chunks[0].Vector[2] != 0.3 {
		t.Fatalf("roundtrip mismatch: %+v", got["core/a.md"])
	}
	if VectorsModel(vault) != "bge-m3" {
		t.Fatalf("stored model = %q, want bge-m3", VectorsModel(vault))
	}
	// Same model → valid; different model → nil (forces rebuild).
	if LoadVectorsFor(vault, "bge-m3") == nil {
		t.Fatal("LoadVectorsFor must accept the recorded model")
	}
	if LoadVectorsFor(vault, "text-embedding-v3") != nil {
		t.Fatal("LoadVectorsFor must reject a different model")
	}
}

func TestVectorStoreCorrupt(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing → not corrupt (first build).
	if VectorStoreCorrupt(vault) {
		t.Fatal("missing store must not be corrupt")
	}
	// Valid gob → not corrupt.
	if err := SaveVectors(vault, vectorIndex{"a.md": {Title: "A", Chunks: []chunkVector{{Heading: "## x", Vector: []float64{1}}}}}, "bge-m3"); err != nil {
		t.Fatal(err)
	}
	if VectorStoreCorrupt(vault) {
		t.Fatal("valid gob must not be corrupt")
	}
	// Corrupt gob bytes → corrupt.
	if err := os.WriteFile(filepath.Join(refsDir, vectorIndexGobFile), []byte("not-gob"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !VectorStoreCorrupt(vault) {
		t.Fatal("corrupt gob must be reported corrupt")
	}
	// Gob missing, valid JSON fallback → not corrupt.
	if err := os.Remove(filepath.Join(refsDir, vectorIndexGobFile)); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":2,"docs":{"a.md":{"title":"A","chunks":[{"heading":"## x","vector":[1]}],"source_hash":"h"}}}`
	if err := os.WriteFile(filepath.Join(refsDir, vectorIndexFile), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if VectorStoreCorrupt(vault) {
		t.Fatal("valid JSON fallback must not be corrupt")
	}
	// Corrupt gob + valid JSON fallback → not corrupt (JSON still serves).
	if err := os.WriteFile(filepath.Join(refsDir, vectorIndexGobFile), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if VectorStoreCorrupt(vault) {
		t.Fatal("gob corrupt with healthy JSON fallback must not be corrupt")
	}
	// Both corrupt → corrupt.
	if err := os.WriteFile(filepath.Join(refsDir, vectorIndexFile), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !VectorStoreCorrupt(vault) {
		t.Fatal("both formats corrupt must be reported corrupt")
	}
}

func TestVectorStoreJSONFallback(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Legacy JSON file only (no gob): LoadVectors must still read it.
	legacy := `{"version":2,"docs":{"core/a.md":{"title":"A","chunks":[{"heading":"## x","vector":[1,2,3]}],"source_hash":"h"}}}`
	if err := os.WriteFile(filepath.Join(refsDir, vectorIndexFile), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadVectors(vault)
	if got == nil || len(got) != 1 || len(got["core/a.md"].Chunks[0].Vector) != 3 {
		t.Fatalf("JSON fallback failed: %+v", got)
	}
	// Legacy files carry no model: treated as valid for any configured model.
	if LoadVectorsFor(vault, "bge-m3") == nil {
		t.Fatal("legacy store without model must be accepted")
	}
}

func TestPromoteToCoreUsesHotCache(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "extended", "tools")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntopics: [probe]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\nhits: 0\n---\n# Hot\n\n> summary\n"
	if err := os.WriteFile(filepath.Join(refsDir, "hot.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Warm the cache, then bump heat twice — IncrementHits must update the
	// cache in place so PromoteToCore sees hits=2 without a rescan.
	InvalidateRefIndex(refsDir)
	if entries := loadRefIndex(refsDir); len(entries) != 1 {
		t.Fatalf("warm cache: %d entries", len(entries))
	}
	if _, err := IncrementHits(vault, []string{"extended/tools/hot.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := IncrementHits(vault, []string{"extended/tools/hot.md"}); err != nil {
		t.Fatal(err)
	}
	moved, err := PromoteToCore(vault, 2)
	if err != nil {
		t.Fatalf("PromoteToCore: %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("hot-cache promotion = %v, want 1", moved)
	}
	data, _ := os.ReadFile(filepath.Join(vault, "References", "core", "tools", "hot.md"))
	if !strings.Contains(string(data), "hits=2") {
		t.Fatal("migration note must carry the actual hits count")
	}
}
