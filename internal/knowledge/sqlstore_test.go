package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// syncTestKB builds a temp vault store and syncs it once.
func syncTestKB(t *testing.T, vault string, client *EmbeddingClient) string {
	t.Helper()
	dbPath := filepath.Join(filepath.Dir(vault), "kb.sqlite")
	stats, err := SyncKnowledgeDB(vault, dbPath, client)
	if err != nil {
		t.Fatalf("SyncKnowledgeDB: %v", err)
	}
	if stats.TotalDocs == 0 {
		t.Fatal("sync produced an empty store")
	}
	return dbPath
}

func TestSyncIdempotent(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core", "go")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "connect-rpc.md", "connect, rpc, grpc", "Connect 轻量 RPC 框架。")
	dbPath := syncTestKB(t, vault, nil)

	// A second sync with an unchanged vault is a no-op (content_hash).
	stats, err := SyncKnowledgeDB(vault, dbPath, nil)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if stats.Added != 0 || stats.Updated != 0 || stats.Removed != 0 {
		t.Fatalf("idempotent sync changed the store: %+v", stats)
	}
	hits, err := SearchKnowledgeDB(dbPath, "connect rpc", 3, true, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "core/go/connect-rpc.md" {
		t.Fatalf("search failed after sync: %+v", hits)
	}
}

func TestSyncIncrementalAddAndRemove(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "alpha", "文档 A")
	dbPath := syncTestKB(t, vault, nil)

	// Add a document → next sync picks it up (no fingerprint rebuild).
	writeSearchDoc(t, refsDir, "b.md", "beta", "文档 B")
	stats, err := SyncKnowledgeDB(vault, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 1 || stats.TotalDocs != 2 {
		t.Fatalf("after add: %+v", stats)
	}
	hits, _ := SearchKnowledgeDB(dbPath, "beta", 3, true, nil, 0)
	if len(hits) == 0 || hits[0].Path != "core/b.md" {
		t.Fatalf("new doc not searchable: %+v", hits)
	}

	// Remove the document → store shrinks, no stale hits.
	if err := os.Remove(filepath.Join(refsDir, "a.md")); err != nil {
		t.Fatal(err)
	}
	stats, err = SyncKnowledgeDB(vault, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Removed != 1 || stats.TotalDocs != 1 {
		t.Fatalf("after remove: %+v", stats)
	}
	if hits, _ := SearchKnowledgeDB(dbPath, "alpha", 3, true, nil, 0); len(hits) != 0 {
		t.Fatalf("removed doc still searchable: %+v", hits)
	}
}

func TestSearchSkipsArchived(t *testing.T) {
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
	dbPath := syncTestKB(t, vault, nil)

	// Default: archived layer excluded.
	hits, err := SearchKnowledgeDB(dbPath, "文档", 3, true, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "core/go-doc.md" {
		t.Fatalf("skipArchived hits = %+v, want only core", hits)
	}
	// Explicit include: both layers.
	hits, err = SearchKnowledgeDB(dbPath, "文档", 3, false, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("include archived hits = %+v, want 2", hits)
	}
}

// TestSyncVectorsIncremental verifies the embedding layer: first sync embeds
// everything, an unchanged resync embeds nothing, a changed document
// re-embeds only itself.
func TestSyncVectorsIncremental(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "go, connect", "a 内容")
	writeSearchDoc(t, refsDir, "b.md", "helm, chart", "b 内容")
	dbPath := syncTestKB(t, vault, client)

	ready, model := VecStatus(dbPath)
	if !ready || model != "bge-m3" {
		t.Fatalf("VecStatus = %v,%q want true,bge-m3", ready, model)
	}

	// Unchanged resync: nothing re-embedded, vectors stay.
	stats, err := SyncKnowledgeDB(vault, dbPath, client)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added+stats.Updated != 0 {
		t.Fatalf("unchanged resync modified docs: %+v", stats)
	}
	before, _, _ := chunkCount(t, dbPath)

	// Change one doc → its chunks re-embed (helm→connect flips component 0).
	writeSearchDoc(t, refsDir, "b.md", "helm, chart, connect", "b 内容更新")
	stats, err = SyncKnowledgeDB(vault, dbPath, client)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 1 {
		t.Fatalf("updated = %d, want 1", stats.Updated)
	}
	after, _, _ := chunkCount(t, dbPath)
	if after != before {
		t.Fatalf("chunk count changed %d → %d", before, after)
	}
	// Hybrid search must see the new vector: query "connect" (component 0)
	// should rank b.md above a.md via vector-only evidence.
	hits, err := SearchKnowledgeDB(dbPath, "connect", 5, true, client, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hits {
		if h.Path == "core/b.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("updated doc lost from vector search: %+v", hits)
	}
}

func chunkCount(t *testing.T, dbPath string) (int, int, error) {
	t.Helper()
	db, err := openKB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var chunks int
	if err := db.QueryRow(`SELECT count(*) FROM kb_chunks`).Scan(&chunks); err != nil {
		return 0, 0, err
	}
	var docs int
	if err := db.QueryRow(`SELECT count(*) FROM kb_docs`).Scan(&docs); err != nil {
		return 0, 0, err
	}
	return chunks, docs, nil
}

// TestSearchHybridVectorOnlyHit: a query with zero lexical overlap still
// surfaces a document via the vector layer (semantic recall), and the best
// chunk heading is reported.
func TestSearchHybridVectorOnlyHit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float64, 0, len(req.Input))
		for _, text := range req.Input {
			vec := []float64{0, 0, 0}
			if strings.Contains(text, "zircon") || strings.Contains(text, "状态机") {
				vec[0] = 1
			}
			vecs = append(vecs, vec)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	defer srv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "state-machine.md", "workflow, state",
		"任务生命周期状态机：主状态互斥、迁移前置条件。\n\n## 要点\n状态机迁移规则细节。")
	dbPath := syncTestKB(t, vault, client)

	// BM25 alone finds nothing ("zircon mesh" shares no tokens).
	hits, err := SearchKnowledgeDB(dbPath, "zircon mesh", 3, true, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("BM25 should not match zircon mesh, got %+v", hits)
	}
	// Hybrid surfaces the doc via vectors and reports the chunk heading.
	hits, err = SearchKnowledgeDB(dbPath, "zircon mesh", 3, true, client, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "core/state-machine.md" {
		t.Fatalf("hybrid should surface vector-only hit, got %+v", hits)
	}
	if hits[0].Chunk == "" {
		t.Fatalf("chunk heading missing: %+v", hits[0])
	}
}

func TestMatchQueryEscapes(t *testing.T) {
	// User input cannot inject FTS5 syntax: operators and quotes are
	// stripped by tokenize, every term is double-quoted.
	q := matchQuery(`"hello" AND evil OR NEAR`)
	if q != `"hello" AND "and" AND "evil" AND "or" AND "near"` {
		t.Fatalf("escaped query = %q", q)
	}
	// Pure punctuation → no searchable terms.
	if q := matchQuery(`!@#$%^&*()`); q != "" {
		t.Fatalf("punctuation query = %q, want empty", q)
	}
}

func TestVecModelSwitchRebuilds(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	clientA := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})
	clientB := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "other-1024"})

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "go, connect", "a 内容")
	dbPath := syncTestKB(t, vault, clientA)
	if _, model := VecStatus(dbPath); model != "bge-m3" {
		t.Fatalf("model = %q, want bge-m3", model)
	}

	// Switch model → full vector rebuild under the new model name.
	stats, err := SyncKnowledgeDB(vault, dbPath, clientB)
	if err != nil {
		t.Fatal(err)
	}
	if stats.VecError != nil {
		t.Fatalf("vec error: %v", stats.VecError)
	}
	if _, model := VecStatus(dbPath); model != "other-1024" {
		t.Fatalf("model after switch = %q, want other-1024", model)
	}
	if chunks, _, _ := chunkCount(t, dbPath); chunks == 0 {
		t.Fatal("vectors lost after model switch")
	}
}

func TestLegacyIndexFilesRemoved(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "alpha", "文档 A")
	// Plant stale gob/JSON artifacts as a pre-SQLite vault would have.
	for _, name := range legacyIndexFiles {
		if err := os.WriteFile(filepath.Join(vault, "References", name), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	syncTestKB(t, vault, nil)
	for _, name := range legacyIndexFiles {
		if _, err := os.Stat(filepath.Join(vault, "References", name)); err == nil {
			t.Fatalf("legacy file %s still present", name)
		}
	}
}

func TestRebuildKnowledgeDB(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "alpha", "文档 A")
	dbPath := filepath.Join(dir, "kb.sqlite")
	if _, err := SyncKnowledgeDB(vault, dbPath, nil); err != nil {
		t.Fatal(err)
	}

	// Change one doc, drop another: a rebuild must reflect the new corpus
	// with no stale rows.
	writeSearchDoc(t, refsDir, "b.md", "beta", "文档 B")
	writeSearchDoc(t, refsDir, "a.md", "alpha", "文档 A 更新")
	if err := os.Remove(filepath.Join(refsDir, "b.md")); err != nil {
		t.Fatal(err)
	}
	stats, err := RebuildKnowledgeDB(vault, dbPath, nil)
	if err != nil {
		t.Fatalf("RebuildKnowledgeDB: %v", err)
	}
	if stats.TotalDocs != 1 {
		t.Fatalf("TotalDocs = %d, want 1", stats.TotalDocs)
	}
	if stats.Removed != 0 { // fresh rebuild: nothing to remove
		t.Fatalf("Removed = %d, want 0", stats.Removed)
	}
	hits, err := SearchKnowledgeDB(dbPath, "alpha", 3, true, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "core/a.md" {
		t.Fatalf("rebuilt doc not searchable: %+v", hits)
	}
	if hits, _ := SearchKnowledgeDB(dbPath, "beta", 3, true, nil, 0); len(hits) != 0 {
		t.Fatalf("dropped doc still searchable after rebuild: %+v", hits)
	}
}

func TestSyncEmptyScanPreservesStore(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "alpha", "文档 A")
	dbPath := filepath.Join(dir, "kb.sqlite")
	if _, err := SyncKnowledgeDB(vault, dbPath, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate a transient read failure: the References/ tree disappears
	// (unmounted drive, cloud-sync gap). A sync must NOT wipe the store.
	if err := os.RemoveAll(filepath.Join(vault, "References")); err != nil {
		t.Fatal(err)
	}
	stats, err := SyncKnowledgeDB(vault, dbPath, nil)
	if err != nil {
		t.Fatalf("sync with empty scan: %v", err)
	}
	if stats.Removed != 0 {
		t.Fatalf("empty scan removed %d docs — store must be preserved", stats.Removed)
	}
	hits, err := SearchKnowledgeDB(dbPath, "alpha", 3, true, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "core/a.md" {
		t.Fatalf("stored doc lost after empty scan: %+v", hits)
	}
}

func TestSyncPartialRemovalStillWorks(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "a.md", "alpha", "文档 A")
	writeSearchDoc(t, refsDir, "b.md", "beta", "文档 B")
	writeSearchDoc(t, refsDir, "c.md", "gamma", "文档 C")
	dbPath := filepath.Join(dir, "kb.sqlite")
	if _, err := SyncKnowledgeDB(vault, dbPath, nil); err != nil {
		t.Fatal(err)
	}
	// Legit bulk deletion: two of three docs removed. The partial-empty
	// guard warns but must not block the removal.
	if err := os.Remove(filepath.Join(refsDir, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(refsDir, "c.md")); err != nil {
		t.Fatal(err)
	}
	stats, err := SyncKnowledgeDB(vault, dbPath, nil)
	if err != nil {
		t.Fatalf("sync after partial removal: %v", err)
	}
	if stats.Removed != 2 || stats.TotalDocs != 1 {
		t.Fatalf("Removed=%d TotalDocs=%d, want 2/1", stats.Removed, stats.TotalDocs)
	}
}

func TestRemovalWarningThresholds(t *testing.T) {
	cases := []struct {
		name   string
		stale  int
		stored int
		want   bool
	}{
		{"single-doc store fully removed", 1, 1, false}, // tiny store: no noise
		{"two-doc store half removed", 1, 2, false},     // tiny store: no noise
		{"three-doc store two removed", 2, 3, true},     // bulk removal on real store
		{"three-doc store one removed", 1, 3, false},    // normal deletion
		{"empty store", 0, 0, false},                    // nothing to compare
		{"ten-doc store nine removed", 9, 10, true},     // near-wipe
		{"ten-doc store five removed", 5, 10, false},    // exactly half: not > half
		{"ten-doc store six removed", 6, 10, true},      // just over half
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldWarnRemoval(tc.stale, tc.stored); got != tc.want {
				t.Fatalf("shouldWarnRemoval(%d, %d) = %v, want %v", tc.stale, tc.stored, got, tc.want)
			}
		})
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
	if _, err := IncrementHits(vault, "", []string{"extended/tools/hot.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := IncrementHits(vault, "", []string{"extended/tools/hot.md"}); err != nil {
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

func TestKbDSN(t *testing.T) {
	got := kbDSN("/tmp/x/kb.sqlite")
	for _, want := range []string{"_journal_mode=WAL", "_busy_timeout=5000", "_foreign_keys=on", "_txlock=immediate"} {
		if !strings.Contains(got, want) {
			t.Fatalf("kbDSN = %q, missing %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "/tmp/x/kb.sqlite?") {
		t.Fatalf("kbDSN must keep the path and append query params, got %q", got)
	}
	// An existing query string is preserved, params appended.
	got2 := kbDSN("file:kb.sqlite?mode=rwc")
	if !strings.HasPrefix(got2, "file:kb.sqlite?mode=rwc&") || !strings.Contains(got2, "_busy_timeout=5000") {
		t.Fatalf("kbDSN with query = %q", got2)
	}
}

// TestOpenRawSingleConnBusyTimeout 钉住 "database is locked" 的根因：
// PRAGMA 只作用于执行它的那一条连接，池内后续新开连接没有 busy_timeout。
// 现在参数通过 DSN 下发给每条连接，且池收敛为单连接，进程内同步互不争写。
func TestOpenRawSingleConnBusyTimeout(t *testing.T) {
	db, err := openRaw(filepath.Join(t.TempDir(), "kb.sqlite"))
	if err != nil {
		t.Fatalf("openRaw: %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1 (single in-process writer)", got)
	}
	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}
}

// TestConcurrentSyncNoLock 并发 sync 共享同一 store：每条连接都带
// busy_timeout，写入串行化而不是报 "database is locked"。
func TestConcurrentSyncNoLock(t *testing.T) {
	vault := t.TempDir()
	refsDir := filepath.Join(vault, "References", "core", "tools")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		content := "---\ntopics: [concurrency, sync]\nlevel: reference\nupdated: \"2026-08-21\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# " + name + "\n\n并发写入正文。\n"
		if err := os.WriteFile(filepath.Join(refsDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(t.TempDir(), "kb.sqlite")
	const workers = 4
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := SyncKnowledgeDB(vault, dbPath, nil)
			errs <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent sync failed: %v", err)
		}
	}
}
