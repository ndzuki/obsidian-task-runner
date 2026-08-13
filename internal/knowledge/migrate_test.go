package knowledge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// TestRebuildMigratesOlderSchema: a store built by an older binary (schema
// version 1) must be rebuildable by the current binary — the version gate
// on normal opens would otherwise deadlock the migration path the error
// message itself recommends (`rebuild with otg kb index`). The vector
// layer (embed + chunk text) must come out of the migration intact.
func TestRebuildMigratesOlderSchema(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "connect-rpc.md", "connect, rpc",
		"Connect 是轻量 RPC 框架。\n\n## 协议细节\nconnect 协议与 grpc 互操作。")
	dbPath := filepath.Join(dir, "kb.sqlite")

	// Simulate an old store: create the v1 schema by hand (kb_chunks
	// without the text column) and stamp schema_version=1.
	db, err := openRaw(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`CREATE TABLE kb_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO kb_meta(key, value) VALUES('schema_version', '1')`,
		`CREATE TABLE kb_docs (
			rowid INTEGER PRIMARY KEY,
			path TEXT NOT NULL UNIQUE,
			layer TEXT NOT NULL,
			title TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			topics TEXT NOT NULL DEFAULT '',
			aliases TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL,
			tokens TEXT NOT NULL,
			hits INTEGER NOT NULL DEFAULT 0,
			content_hash TEXT NOT NULL,
			updated_at TEXT NOT NULL)`,
		`CREATE VIRTUAL TABLE kb_fts USING fts5(
			title, topics, aliases, tags, tokens,
			tokenize='unicode61')`,
		// v1 kb_chunks: no text column.
		`CREATE TABLE kb_chunks (
			chunk_id INTEGER PRIMARY KEY,
			doc_id INTEGER NOT NULL REFERENCES kb_docs(rowid) ON DELETE CASCADE,
			heading TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			UNIQUE(doc_id, heading))`,
	} {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			t.Fatalf("seed v1 schema: %v", err)
		}
	}
	db.Close()

	// A normal open refuses the stale store…
	if _, err := openKB(dbPath); err == nil {
		t.Fatal("openKB should reject schema v1")
	}
	// …but rebuild migrates it: same vault, fresh v2 store with vectors.
	stats, err := RebuildKnowledgeDB(vault, dbPath, client)
	if err != nil {
		t.Fatalf("RebuildKnowledgeDB: %v", err)
	}
	if stats.TotalDocs != 1 {
		t.Fatalf("TotalDocs = %d, want 1", stats.TotalDocs)
	}
	db, err = openKB(dbPath)
	if err != nil {
		t.Fatalf("openKB after rebuild: %v", err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT value FROM kb_meta WHERE key=?`, metaSchemaVersion).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != kbSchemaVersion {
		t.Fatalf("schema after rebuild = %s, want %s", version, kbSchemaVersion)
	}
	// Vector layer migrated: embedding model recorded, chunk text stored.
	ready, model := VecStatus(dbPath)
	if !ready || model != "bge-m3" {
		t.Fatalf("VecStatus after rebuild = %v,%q want true,bge-m3", ready, model)
	}
	var withText int
	if err := db.QueryRow(`SELECT count(*) FROM kb_chunks WHERE text != ''`).Scan(&withText); err != nil {
		t.Fatal(err)
	}
	if withText == 0 {
		t.Fatal("no chunk text after rebuild — vector migration incomplete")
	}
}
