package knowledge

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// SyncStats summarizes one incremental sync of the retrieval store.
type SyncStats struct {
	TotalDocs        int  // documents now in the store
	Added            int
	Updated          int
	Removed          int
	TotalChunks      int  // vector chunks now in the store
	VectorsRefreshed bool // at least one document was embedded
	VecSkipped       bool // embedding not configured — FTS-only sync (not an error)
	VecError         error // first embedding failure; docs/FTS sync still committed
}

// syncDoc is one scanned References/ document ready for store comparison.
type syncDoc struct {
	rel   string // path relative to References/ (slash-separated)
	layer string // first path segment: core|extended|archived|uncategorized
	data  []byte
	body  string
}

// walkRefs scans References/*.md (INDEX.md excluded, like the search index)
// and parses each document. Parse failures degrade to full-body documents —
// the same tolerance the BM25 index had.
func walkRefs(refsDir string) ([]syncDoc, error) {
	var docs []syncDoc
	err := filepath.WalkDir(refsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !isMarkdown(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(refsDir, path)
		rel = filepath.ToSlash(rel)
		_, body, _ := parseFrontmatter(data)
		if body == "" {
			body = string(data)
		}
		layer := rel
		if i := strings.IndexByte(layer, '/'); i > 0 {
			layer = layer[:i]
		}
		docs = append(docs, syncDoc{rel: rel, layer: layer, data: data, body: body})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].rel < docs[j].rel })
	return docs, nil
}

// docTokens pre-tokenizes the retrieval text exactly like the old in-memory
// index (title + summary + topics + aliases + tags + body) so unicode61
// tokenizes the space-joined output into the same term set as the custom
// bigram tokenizer.
func docTokens(d syncDoc, fm map[string]any, title, summary string) string {
	var parts []string
	parts = append(parts, title, summary)
	for _, key := range []string{"topics", "aliases", "tags"} {
		if v, ok := fm[key]; ok {
			parts = append(parts, strings.Join(toStringSlice(v), " "))
		}
	}
	parts = append(parts, d.body)
	text := strings.Join(parts, " ")
	toks := tokenize(text)
	return strings.Join(toks, " ")
}

// legacyIndexFiles are the pre-SQLite retrieval artifacts. The SQLite store
// supersedes them; once a sync proves the store usable they are removed
// (they would otherwise keep syncing through cloud-synced vaults).
var legacyIndexFiles = []string{".kb-bm25.gob", ".kb-vectors.gob", ".kb-vectors.json"}

// cleanupLegacyIndexFiles removes stale gob/JSON index files from
// References/ (best effort, silent when already gone).
func cleanupLegacyIndexFiles(refsDir string) {
	for _, name := range legacyIndexFiles {
		if err := os.Remove(filepath.Join(refsDir, name)); err == nil {
			fmt.Fprintf(os.Stderr, "knowledge: removed legacy index file %s (retrieval store is SQLite-based)\n", name)
		}
	}
}

// SyncKnowledgeDB incrementally updates the retrieval store from
// References/: documents + FTS always; vectors when an embedding client is
// configured. Idempotent — a second run with an unchanged vault is a no-op
// (content_hash comparison, no fingerprint scan, no full rewrite). Embedding
// failures are non-fatal: docs/FTS commit, the error is surfaced in stats.
func SyncKnowledgeDB(vaultDir, dbPath string, client *EmbeddingClient) (SyncStats, error) {
	db, err := openKB(dbPath)
	if err != nil {
		return SyncStats{}, err
	}
	defer func() { _ = db.Close() }()
	refsDir := filepath.Join(vaultDir, "References")
	cleanupLegacyIndexFiles(refsDir)
	docs, err := walkRefs(refsDir)
	if err != nil {
		return SyncStats{}, fmt.Errorf("scan References/: %w", err)
	}

	stats := SyncStats{}
	// Model/dimension switching invalidates all vectors; detect before the
	// loop so every document is re-embedded (existing LoadVectorsFor
	// semantics: never mix vectors from different models).
	fullVec := false
	if client != nil {
		model, _ := metaGet(db, metaEmbeddingModel)
		if model != "" && model != client.cfg.Model {
			fullVec = true
		}
	}

	for _, d := range docs {
		fm, _, _ := parseFrontmatter(d.data)
		hash := contentHash(d.data)
		title := extractH1(d.data)
		summary := extractSummary(d.data)
		tokens := docTokens(d, fm, title, summary)
		hits := fmHits(fm)

		var rowid int64
		var oldHash string
		var oldLayer string
		err := db.QueryRow(`SELECT rowid, content_hash, layer FROM kb_docs WHERE path=?`, d.rel).Scan(&rowid, &oldHash, &oldLayer)
		switch {
		case err == sql.ErrNoRows:
			stats.Added++
			if err := upsertDoc(db, d, fm, title, summary, tokens, hash, hits); err != nil {
				return stats, fmt.Errorf("insert %s: %w", d.rel, err)
			}
		case err != nil:
			return stats, fmt.Errorf("read %s: %w", d.rel, err)
		case oldHash != hash || oldLayer != d.layer:
			stats.Updated++
			if err := upsertDoc(db, d, fm, title, summary, tokens, hash, hits); err != nil {
				return stats, fmt.Errorf("update %s: %w", d.rel, err)
			}
		}
	}

	// Removals: docs present in the store but gone from disk. Guard: an
	// empty scan must never mass-delete the store — a corpus that reads as
	// zero files is almost always a transient I/O failure (cloud-sync gap,
	// wrong vault path, unmounted drive), and wiping a healthy index over
	// that is unrecoverable without a full rebuild. Removal runs only when
	// the scan actually saw files.
	if len(docs) == 0 {
		var stored int
		if err := db.QueryRow(`SELECT count(*) FROM kb_docs`).Scan(&stored); err != nil {
			return stats, fmt.Errorf("count stored docs: %w", err)
		}
		if stored > 0 {
			fmt.Fprintf(os.Stderr, "knowledge: WARNING — References/ scan returned 0 documents while the store has %d; skipping removal (run `otg kb index` to force a rebuild)\n", stored)
		}
	} else {
		seen := make(map[string]bool, len(docs))
		for _, d := range docs {
			seen[d.rel] = true
		}
		rows, err := db.Query(`SELECT rowid, path FROM kb_docs`)
		if err != nil {
			return stats, fmt.Errorf("list store docs: %w", err)
		}
		var stale []struct {
			rowid int64
			path  string
		}
		for rows.Next() {
			var r struct {
				rowid int64
				path  string
			}
			if err := rows.Scan(&r.rowid, &r.path); err != nil {
				_ = rows.Close()
				return stats, err
			}
			if !seen[r.path] {
				stale = append(stale, r)
			}
		}
		_ = rows.Close()
		// Partial-empty guard: warn when a removal pass would delete more
		// than half the store (and the store is meaningfully sized) — a
		// corpus that reads as partially visible is likely a transient
		// cloud-sync/read gap, not a real mass deletion. Removals still run
		// (legitimate bulk deletions must work); the warning makes the
		// anomaly visible and points at `kb index`.
		if len(stale) > 0 {
			var stored int
			if err := db.QueryRow(`SELECT count(*) FROM kb_docs`).Scan(&stored); err != nil {
				return stats, fmt.Errorf("count stored docs: %w", err)
			}
			if shouldWarnRemoval(len(stale), stored) {
				fmt.Fprintf(os.Stderr, "knowledge: WARNING — removing %d of %d docs (corpus reads as partially empty; run `otg kb index` if this is unexpected)\n", len(stale), stored)
			}
		}
		for _, r := range stale {
			if err := removeDoc(db, r.rowid); err != nil {
				return stats, fmt.Errorf("remove %s: %w", r.path, err)
			}
			stats.Removed++
		}
	}

	// Vector layer: incremental per document (content_hash), full rebuild on
	// model switch. Failures degrade to FTS-only — same contract as before.
	if client != nil {
		stats.VectorsRefreshed, stats.VecError = syncVectors(db, docs, client, fullVec)
	} else {
		stats.VecSkipped = true // not configured — normal FTS-only operation
	}
	_ = metaSet(db, metaSyncedAt, time.Now().Format(time.RFC3339))

	var total, chunks int
	_ = db.QueryRow(`SELECT count(*) FROM kb_docs`).Scan(&total)
	_ = db.QueryRow(`SELECT count(*) FROM kb_chunks`).Scan(&chunks)
	stats.TotalDocs = total
	stats.TotalChunks = chunks
	return stats, nil
}

// deleteChunkRows removes a document's chunk metadata and their vec0 rows
// (vec0 only supports rowid-addressed deletes). Used by upsertDoc (changed
// docs must not keep stale vectors) and syncVectors (re-embed path).
func deleteChunkRows(tx *sql.Tx, docID int64) error {
	ids, err := tx.Query(`SELECT chunk_id FROM kb_chunks WHERE doc_id=?`, docID)
	if err != nil {
		return err
	}
	var chunkIDs []int64
	for ids.Next() {
		var id int64
		if err := ids.Scan(&id); err != nil {
			_ = ids.Close()
			return err
		}
		chunkIDs = append(chunkIDs, id)
	}
	_ = ids.Close()
	for _, id := range chunkIDs {
		if _, err := tx.Exec(`DELETE FROM kb_vec WHERE rowid=?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM kb_chunks WHERE doc_id=?`, docID); err != nil {
		return err
	}
	return nil
}

// upsertDoc writes one document row + its FTS index rows inside a
// transaction. FTS rows are upserted via INSERT OR REPLACE on the standalone
// FTS table (no external-content coupling — delete-then-insert is not
// needed and would re-trigger the content-table consistency hazard).
func upsertDoc(db *sql.DB, d syncDoc, fm map[string]any, title, summary, tokens, hash string, hits int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var rowid int64
	err = tx.QueryRow(`SELECT rowid FROM kb_docs WHERE path=?`, d.rel).Scan(&rowid)
	switch {
	case err == sql.ErrNoRows:
		res, err := tx.Exec(`INSERT INTO kb_docs(path, layer, title, summary, topics, aliases, tags, body, tokens, hits, content_hash, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			d.rel, d.layer, title, summary,
			strField(fm, "topics"), strField(fm, "aliases"), strField(fm, "tags"),
			d.body, tokens, hits, hash, time.Now().Format(time.RFC3339))
		if err != nil {
			return err
		}
		rowid, _ = res.LastInsertId()
	case err != nil:
		return err
	default:
		if _, err := tx.Exec(`UPDATE kb_docs SET layer=?, title=?, summary=?, topics=?, aliases=?, tags=?, body=?, tokens=?, hits=?, content_hash=?, updated_at=?
			WHERE rowid=?`,
			d.layer, title, summary,
			strField(fm, "topics"), strField(fm, "aliases"), strField(fm, "tags"),
			d.body, tokens, hits, hash, time.Now().Format(time.RFC3339), rowid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO kb_fts(rowid, title, topics, aliases, tags, tokens) VALUES(?,?,?,?,?,?)`,
		rowid, title, strField(fm, "topics"), strField(fm, "aliases"), strField(fm, "tags"), tokens); err != nil {
		return err
	}
	// Chunks (vectors) are refreshed by syncVectors; a changed document must
	// not keep stale chunk rows — dropping them here also marks the vectors
	// as needing a rebuild (syncVectors checks kb_chunks existence).
	if err := deleteChunkRows(tx, rowid); err != nil {
		return err
	}
	return tx.Commit()
}

// shouldWarnRemoval decides whether a removal pass triggers the
// partial-empty warning: the store must be meaningfully sized (≥3 docs) and
// more than half of it is being removed at once. Tiny stores (1-2 docs)
// legitimately shrink to nothing — warning there would be noise.
func shouldWarnRemoval(stale, stored int) bool {
	return stored >= 3 && stale > stored/2
}

// removeDoc deletes a document and every dependent row (FTS row, chunks,
// vectors) in one transaction.
func removeDoc(db *sql.DB, rowid int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM kb_fts WHERE rowid=?`, rowid); err != nil {
		return err
	}
	if err := deleteChunkRows(tx, rowid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM kb_docs WHERE rowid=?`, rowid); err != nil {
		return err
	}
	return tx.Commit()
}

// strField joins a frontmatter array field with spaces ("" when absent).
func strField(fm map[string]any, key string) string {
	if fm == nil {
		return ""
	}
	v, ok := fm[key]
	if !ok {
		return ""
	}
	return strings.Join(toStringSlice(v), " ")
}

// fmHits reads the frontmatter `hits` counter (0 when absent).
func fmHits(fm map[string]any) int {
	switch vv := fm["hits"].(type) {
	case int:
		return vv
	case float64:
		return int(vv)
	}
	return 0
}

// syncVectors embeds changed documents' chunks and writes kb_vec rows.
// fullVec forces every document to re-embed (model switch). Embedding errors
// abort the vector pass but never roll back the docs/FTS sync; the first
// error is returned for the caller to surface.
func syncVectors(db *sql.DB, docs []syncDoc, client *EmbeddingClient, fullVec bool) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("embedding not configured")
	}
	if fullVec {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + kbVecTable); err != nil {
			return false, fmt.Errorf("drop vector table on model switch: %w", err)
		}
		if _, err := db.Exec(`DELETE FROM kb_chunks`); err != nil {
			return false, fmt.Errorf("clear chunks on model switch: %w", err)
		}
	}

	var firstErr error
	embedded := 0
	for _, d := range docs {
		fm, _, _ := parseFrontmatter(d.data)
		var rowid int64
		if err := db.QueryRow(`SELECT rowid FROM kb_docs WHERE path=?`, d.rel).Scan(&rowid); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("doc %s not in store: %w", d.rel, err)
			}
			continue
		}
		if !fullVec {
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM kb_chunks WHERE doc_id=?`, rowid).Scan(&n); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if n > 0 {
				continue // vectors already current for this document
			}
		}
		chunks := chunkDocument(d.data, client.chunkChars())
		vecRows, dim, err := embedChunks(client, fm, d, chunks)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("embed %s: %w", d.rel, err)
			}
			continue
		}
		if len(vecRows) == 0 {
			continue
		}
		if err := ensureVecTable(db, dim); err != nil {
			return embedded > 0, err
		}
		tx, err := db.Begin()
		if err != nil {
			return embedded > 0, err
		}
		if err := deleteChunkRows(tx, rowid); err != nil {
			_ = tx.Rollback()
			return embedded > 0, err
		}
		bad := false
		for _, v := range vecRows {
			chunkID, err := insertVecRow(tx, rowid, v)
			if err != nil {
				bad = true
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			_ = chunkID
		}
		if bad {
			_ = tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return embedded > 0, err
		}
		embedded++
	}
	if firstErr == nil {
		_ = metaSet(db, metaEmbeddingModel, client.cfg.Model)
	}
	return embedded > 0, firstErr
}

// vecRow is one chunk embedding ready for insertion. vector is the
// serialized float32 BLOB produced by sqlite_vec.SerializeFloat32.
type vecRow struct {
	heading string
	text    string
	vector  []byte
	hash    string
}

// embedChunks embeds a document's chunks in batches and returns serializable
// rows plus the vector dimension (from the first vector — all chunks of one
// document share the backend's dimension).
func embedChunks(client *EmbeddingClient, fm map[string]any, d syncDoc, chunks []textChunk) ([]vecRow, int, error) {
	if len(chunks) == 0 {
		return nil, 0, nil
	}
	var rows []vecRow
	dim := 0
	batch := client.batchSize()
	for start := 0; start < len(chunks); start += batch {
		end := start + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			texts = append(texts, c.text)
		}
		vecs, err := client.EmbedBatch(texts)
		if err != nil {
			return nil, 0, err
		}
		for i, vec := range vecs {
			if dim == 0 {
				dim = len(vec)
			}
			f32 := make([]float32, len(vec))
			for j, f := range vec {
				f32[j] = float32(f)
			}
			b, err := sqlite_vec.SerializeFloat32(f32)
			if err != nil {
				return nil, 0, err
			}
			rows = append(rows, vecRow{heading: chunks[start+i].heading, text: chunks[start+i].text, vector: b, hash: contentHash([]byte(chunks[start+i].text))})
		}
	}
	return rows, dim, nil
}

// insertVecRow writes one vector + chunk metadata row inside tx. The vec0
// rowid mirrors kb_chunks.chunk_id so deletes stay addressable. The chunk
// text is stored alongside so `kb ask` / rerank can build reference blocks
// without re-reading the vault.
func insertVecRow(tx *sql.Tx, docID int64, v vecRow) (int64, error) {
	res, err := tx.Exec(`INSERT INTO kb_chunks(doc_id, heading, text, content_hash) VALUES(?,?,?,?)`,
		docID, v.heading, v.text, v.hash)
	if err != nil {
		return 0, err
	}
	chunkID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO kb_vec(rowid, doc_id, heading, embedding) VALUES(?,?,?,?)`,
		chunkID, docID, v.heading, v.vector); err != nil {
		return 0, err
	}
	return chunkID, nil
}

// RebuildKnowledgeDB drops every store table and resyncs from scratch —
// the `otg kb index` semantic. The store is derived data, so a rebuild is
// always safe and lossless for the vault itself. All drops run in one
// transaction: a mid-rebuild failure rolls back instead of leaving a
// half-deleted store (which would read as an empty knowledge base).
// The schema-version gate is bypassed on purpose: rebuild is also the
// migration path when a newer binary meets an older store (schema v1 → v2),
// and the drops make the version check moot.
func RebuildKnowledgeDB(vaultDir, dbPath string, client *EmbeddingClient) (SyncStats, error) {
	db, err := openKBForRebuild(dbPath)
	if err != nil {
		return SyncStats{}, err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		return SyncStats{}, err
	}
	for _, tbl := range []string{kbVecTable, "kb_chunks", "kb_fts", "kb_docs", "kb_meta"} {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + tbl); err != nil {
			_ = tx.Rollback()
			return SyncStats{}, fmt.Errorf("drop %s: %w", tbl, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SyncStats{}, fmt.Errorf("commit rebuild drops: %w", err)
	}
	return SyncKnowledgeDB(vaultDir, dbPath, client)
}
