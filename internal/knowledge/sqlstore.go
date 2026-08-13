package knowledge

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

// SQLite-backed retrieval store replacing the gob caches (.kb-bm25.gob,
// .kb-vectors.gob/.json). One .sqlite file outside the vault (cloud-synced
// References/ must not carry the index); FTS5 provides BM25 ranking, vec0
// provides cosine KNN, both incrementally updatable per document — no full
// rebuilds, no fingerprint scans.
//
// Driver note: FTS5 requires mattn/go-sqlite3 built with `-tags sqlite_fts5`
// (see Makefile / CI); sqlite-vec is registered via sqlite_vec.Auto().

const (
	// kbSchemaVersion 2: kb_chunks gained the embedded chunk text (needed by
	// `kb ask` reference blocks and the rerank stage). Older stores fail
	// open with a rebuild hint — the store is derived data, so `otg kb index`
	// is the migration.
	kbSchemaVersion = "2"
	kbVecTable      = "kb_vec" // created lazily with the actual embedding dimension
)

// kbMetaKeys — single-row-ish key/value metadata.
const (
	metaSchemaVersion  = "schema_version"
	metaEmbeddingModel = "embedding_model"
	metaEmbeddingDim   = "embedding_dim"
	metaSyncedAt       = "synced_at"
)

// KBPath returns the store path: explicit override when set, otherwise the
// XDG data dir (Linux: ~/.local/share/otg/kb.sqlite). Single-vault setup:
// the default path does not vary per vault — when more than one vault exists
// on this machine, point kb_db at a distinct path per vault (the path is
// derived from HOME, not from the vault, so a wrong --map-file would
// otherwise hit the wrong vault's store).
func KBPath(vaultDir, override string) string {
	if override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "otg", "kb.sqlite")
}

// openKB opens (creating on first use) the store and ensures the schema.
// The caller must call sqlite_vec.Auto() once per process. An FTS5 probe
// runs on every open: a binary built without -tags sqlite_fts5 compiles and
// runs, but every kb command would otherwise fail later with a raw
// "no such module: fts5" (or worse, silently serve an empty index when the
// tables already exist). Probing up front names the real cause once.
func openKB(dbPath string) (*sql.DB, error) {
	db, err := openRaw(dbPath)
	if err != nil {
		return nil, err
	}
	if err := ensureSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// openKBForRebuild opens the store without the schema-version gate: a
// rebuild drops every table anyway, so a version mismatch must not block
// the recovery/migration path (a v1 store cannot otherwise be rebuilt by a
// v2 binary — the error message would demand the very command that fails).
func openKBForRebuild(dbPath string) (*sql.DB, error) {
	return openRaw(dbPath)
}

// openRaw opens the SQLite file with WAL + busy_timeout and probes FTS5 —
// shared plumbing for openKB and openKBForRebuild.
func openRaw(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", dbPath, err)
	}
	// WAL: concurrent daemon/CLI access without read locks; busy_timeout
	// serializes writers instead of failing fast.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if err := probeFTS5(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// probeFTS5 verifies the FTS5 module is available in this build by creating
// and dropping a throwaway virtual table.
// probeFTS5 verifies the FTS5 module is available in this build by creating
// and dropping a throwaway virtual table. IF NOT EXISTS makes a leftover
// probe table (from a failed DROP) self-heal on the next open instead of
// failing with "already exists".
func probeFTS5(db *sql.DB) error {
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS _kb_fts_probe USING fts5(probe)`); err != nil {
		if strings.Contains(err.Error(), "no such module") {
			return fmt.Errorf("FTS5 unavailable — otg was built without `-tags sqlite_fts5`; rebuild with `make build` / `make install-force` (see obsidian-task-runner SKILL.md 构建强制条款): %w", err)
		}
		return fmt.Errorf("FTS5 probe: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE _kb_fts_probe`); err != nil {
		return fmt.Errorf("FTS5 probe cleanup: %w", err)
	}
	return nil
}

// ensureSchema creates the store tables (idempotent) and validates the
// schema version; a version mismatch aborts so a future migration cannot
// silently run on stale tables.
func ensureSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS kb_meta (
			key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		// Corpus table: full document text + pre-tokenized retrieval fields.
		// kb_fts is standalone (no external-content coupling) so row updates
		// never corrupt the index.
		`CREATE TABLE IF NOT EXISTS kb_docs (
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
		// Standalone FTS table (no external content): the pre-tokenized
		// retrieval fields live in the index itself. External-content mode
		// was rejected because DELETE/REPLACE inside a transaction that
		// also modifies the content table can corrupt the index (FTS5
		// requires both sides consistent at all times); standalone tables
		// have no such coupling and tokens-only storage stays compact.
		`CREATE VIRTUAL TABLE IF NOT EXISTS kb_fts USING fts5(
			title, topics, aliases, tags, tokens,
			tokenize='unicode61')`,
		`CREATE TABLE IF NOT EXISTS kb_chunks (
			chunk_id INTEGER PRIMARY KEY,
			doc_id INTEGER NOT NULL REFERENCES kb_docs(rowid) ON DELETE CASCADE,
			heading TEXT NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL,
			UNIQUE(doc_id, heading))`,
		// kb_vec is created lazily with the real embedding dimension
		// (model-dependent: bge-m3 is 1024); see ensureVecTable.
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			if strings.Contains(err.Error(), "no such module: fts5") {
				// The binary was built without the opt-in FTS5 tag: it
				// compiles and runs, but every kb command hits this. Name
				// the real cause instead of surfacing a raw SQLite error.
				return fmt.Errorf("FTS5 unavailable — otg was built without `-tags sqlite_fts5`; use `make build` / `make install-force` (see obsidian-task-runner SKILL.md 构建强制条款): %w", err)
			}
			return fmt.Errorf("create schema: %w\n%s", err, s)
		}
	}
	var version string
	err := db.QueryRow(`SELECT value FROM kb_meta WHERE key=?`, metaSchemaVersion).Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		if _, err := db.Exec(`INSERT INTO kb_meta(key, value) VALUES(?, ?)`, metaSchemaVersion, kbSchemaVersion); err != nil {
			return fmt.Errorf("init schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	case version != kbSchemaVersion:
		return fmt.Errorf("store schema version %s, binary expects %s — rebuild with `otg kb index`", version, kbSchemaVersion)
	}
	return nil
}

// ensureVecTable creates kb_vec with the given embedding dimension. A
// dimension mismatch (embedding model switched) drops and recreates the
// table — vectors from different models are never mixed.
func ensureVecTable(db *sql.DB, dim int) error {
	if dim <= 0 {
		return fmt.Errorf("invalid embedding dimension %d", dim)
	}
	var stored int
	err := db.QueryRow(`SELECT value FROM kb_meta WHERE key=?`, metaEmbeddingDim).Scan(&stored)
	if err == sql.ErrNoRows {
		stored = 0
	} else if err != nil {
		return fmt.Errorf("read embedding dim: %w", err)
	}
	if stored == dim {
		// Table must exist alongside the recorded dimension (fresh DBs
		// record the dim only after creating the table).
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, kbVecTable).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	if stored != 0 {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + kbVecTable); err != nil {
			return fmt.Errorf("drop stale vector table: %w", err)
		}
		if _, err := db.Exec(`DELETE FROM kb_chunks`); err != nil {
			return fmt.Errorf("clear stale chunks: %w", err)
		}
	}
	ddl := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(
			doc_id INTEGER,
			heading TEXT,
			embedding float[%d] distance_metric=cosine)`, kbVecTable, dim)
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("create vector table: %w", err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO kb_meta(key, value) VALUES(?, ?)`, metaEmbeddingDim, fmt.Sprint(dim)); err != nil {
		return fmt.Errorf("record embedding dim: %w", err)
	}
	return nil
}

// metaGet/metaSet are small helpers for the kb_meta key/value store.
func metaGet(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM kb_meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func metaSet(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO kb_meta(key, value) VALUES(?, ?)`, key, value)
	return err
}

// Auto-register the sqlite-vec extension process-wide; subsequent database
// connections (mattn driver) get vec0 support via auto-extension.
func init() {
	sqlite_vec.Auto()
}

// VecStatus reports whether the vector layer is usable and which embedding
// model it was built with ("" when never built). "ready" means the vec0
// table exists and holds at least one chunk — the condition for hybrid
// search.
func VecStatus(dbPath string) (ready bool, model string) {
	db, err := openKB(dbPath)
	if err != nil {
		return false, ""
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, kbVecTable).Scan(&n); err != nil || n == 0 {
		return false, ""
	}
	var chunks int
	if err := db.QueryRow(`SELECT count(*) FROM kb_chunks`).Scan(&chunks); err != nil || chunks == 0 {
		return false, ""
	}
	model, _ = metaGet(db, metaEmbeddingModel)
	return true, model
}
