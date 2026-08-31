package knowledge

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// matchQuery builds an FTS5 MATCH expression from the query, pre-tokenized
// the same way documents are indexed: every term quoted and AND-joined so
// user input can never inject FTS5 syntax (terms are [a-z0-9]+ or CJK
// bigrams — no quotes/operators survive tokenize). Empty result means the
// query has no searchable terms.
func matchQuery(query string) string {
	toks := tokenize(query)
	if len(toks) == 0 {
		return ""
	}
	quoted := make([]string, len(toks))
	for i, t := range toks {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " AND ")
}

// bm25Hit is one FTS result row with the fields the ranking logic needs.
type bm25Hit struct {
	Path    string
	Title   string
	Summary string
	Topics  string
	Score   float64
}

// SearchKnowledgeDB ranks documents for a query: FTS5 BM25 always, plus
// embedding cosine KNN blended with the historical formula when an embedding
// client is configured. archived/ is excluded when skipArchived is set
// (BM25 side only — the vector side never excluded it, matching the previous
// implementation). Returns hits with score > 0, topic-cluster deduplicated
// on the BM25-only path (historical rank semantics).
func SearchKnowledgeDB(dbPath, query string, limit int, skipArchived bool, client *EmbeddingClient, weight float64) ([]SearchResult, error) {
	db, err := openKB(dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	// Fresh or empty store: nothing to search (caller prompts `kb index`).
	var total int
	if err := db.QueryRow(`SELECT count(*) FROM kb_docs`).Scan(&total); err != nil {
		return nil, fmt.Errorf("count docs: %w", err)
	}
	if total == 0 || limit <= 0 {
		return nil, nil
	}

	match := matchQuery(query)
	if match == "" {
		return nil, nil
	}
	hits, err := bm25Search(db, match, skipArchived, total)
	if err != nil {
		return nil, err
	}
	if client == nil || weight <= 0 {
		return rankBM25(hits, limit), nil
	}
	if weight > 1 {
		weight = 1
	}
	return hybridRank(db, hits, query, limit, client, weight)
}

// bm25Search runs the FTS5 BM25 query over the whole corpus (ranked by
// score + hits boost), returning every hit with a positive score. FTS5's
// bm25() is negative-more-relevant; negating restores the historical
// positive-score contract the CLI and the blend formula expect.
func bm25Search(db *sql.DB, match string, skipArchived bool, limit int) ([]bm25Hit, error) {
	q := `SELECT d.path, d.title, d.summary, d.topics, -bm25(kb_fts) AS score
		FROM kb_fts JOIN kb_docs d ON d.rowid = kb_fts.rowid
		WHERE kb_fts MATCH ?`
	args := []any{match}
	if skipArchived {
		q += ` AND d.layer <> 'archived'`
	}
	q += ` ORDER BY -bm25(kb_fts) + d.hits * 0.02 DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var hits []bm25Hit
	for rows.Next() {
		var h bm25Hit
		if err := rows.Scan(&h.Path, &h.Title, &h.Summary, &h.Topics, &h.Score); err != nil {
			return nil, err
		}
		if h.Score > 0 {
			hits = append(hits, h)
		}
	}
	return hits, rows.Err()
}

// rankBM25 reproduces the historical rank(): sort by score + hits boost,
// drop documents sharing ≥2 topics with an already-kept cluster.
func rankBM25(hits []bm25Hit, limit int) []SearchResult {
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	var results []SearchResult
	var keptTopics [][]string
	for _, h := range hits {
		if topicOverlap(h.Topics, keptTopics) {
			continue
		}
		results = append(results, SearchResult{
			Path: h.Path, Title: h.Title, Summary: h.Summary, Score: h.Score,
		})
		keptTopics = append(keptTopics, strings.Fields(h.Topics))
		if len(results) >= limit {
			break
		}
	}
	return results
}

// hybridRank blends BM25 scores with embedding cosine — the exact historical
// SearchHybrid formula (cosine × weight + normalized BM25 × (1-weight);
// BM25-only documents keep a scaled score; the best chunk heading is kept).
//
// Two bounded candidate paths feed the cosine side:
//  1. BM25 top-N chunks scored in-process (N = knn_candidates, 100 default).
//     vec0's MATCH has no filter push-down (KNN always scans the full
//     table), so reading only the candidates' blobs and scoring them here
//     is cheaper once the corpus outgrows the candidate set.
//  2. A global vec0 KNN capped at k = max(limit×3, 30) rows — this keeps
//     the historical vector-only recall (documents BM25 never matched can
//     still surface, e.g. 「状态机」 → state-machine) with bounded rows.
func hybridRank(db *sql.DB, hits []bm25Hit, query string, limit int, client *EmbeddingClient, weight float64) ([]SearchResult, error) {
	// BM25 top-N candidates — the in-process cosine set is bounded.
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	if n := client.knnCandidates(); len(hits) > n {
		hits = hits[:n]
	}
	bm25Scores := make(map[string]float64, len(hits))
	maxBm25 := 0.0
	for _, h := range hits {
		bm25Scores[h.Path] = h.Score
		if h.Score > maxBm25 {
			maxBm25 = h.Score
		}
	}
	qvec, err := client.Embed(query)
	if err != nil {
		return rankBM25(hits, limit), nil // embedding unavailable → pure BM25
	}
	qn := normalize(qvec)
	if qn == nil {
		return rankBM25(hits, limit), nil // zero vector — no semantic signal
	}

	// bestCos tracks the strongest cosine per path across both paths.
	bestCos := make(map[string]float64)
	bestChunk := make(map[string]string)
	bestText := make(map[string]string)
	merge := func(path, heading, text string, cos float64) {
		if cos <= 0 { // cosine ≤ 0 — historical cut
			return
		}
		prev, ok := bestCos[path]
		// Tie-break on equal cosine: a named section beats the preamble
		// chunk (empty heading — low information density).
		if !ok || cos > prev || (cos == prev && heading != "" && bestChunk[path] == "") {
			bestCos[path] = cos
			bestChunk[path] = heading
			bestText[path] = text
		}
	}

	// Path 1: in-process cosine over BM25 candidates' chunks.
	if len(hits) > 0 {
		ph := strings.Repeat("?,", len(hits))
		ph = ph[:len(ph)-1]
		args := make([]any, len(hits))
		for i, h := range hits {
			args[i] = h.Path
		}
		rows, err := db.Query(`SELECT d.path, c.heading, c.text, v.embedding
			FROM kb_vec v
			JOIN kb_chunks c ON c.chunk_id = v.rowid
			JOIN kb_docs d ON d.rowid = c.doc_id
			WHERE d.path IN (`+ph+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("candidate vectors: %w", err)
		}
		for rows.Next() {
			var path, heading, text string
			var blob []byte
			if err := rows.Scan(&path, &heading, &text, &blob); err != nil {
				_ = rows.Close()
				return nil, err
			}
			merge(path, heading, text, cosine(qn, decodeFloat32(blob)))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}

	// Path 2: bounded global KNN — vector-only recall with capped rows.
	qb, err := sqlite_vec.SerializeFloat32(toFloat32(qvec))
	if err != nil {
		return rankBM25(hits, limit), nil
	}
	k := limit * 3
	if k < 30 {
		k = 30
	}
	rows, err := db.Query(`SELECT d.path, c.heading, c.text, COALESCE(v.distance, 1) AS distance
		FROM kb_vec v
		JOIN kb_chunks c ON c.chunk_id = v.rowid
		JOIN kb_docs d ON d.rowid = c.doc_id
		WHERE v.embedding MATCH ? AND k = ?
		ORDER BY v.distance`, qb, k)
	if err != nil {
		return nil, fmt.Errorf("knn query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var path, heading, text string
		var dist float64
		if err := rows.Scan(&path, &heading, &text, &dist); err != nil {
			return nil, err
		}
		merge(path, heading, text, 1-dist)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	blend := make(map[string]float64, len(bm25Scores)+len(bestCos))
	for path, cos := range bestCos {
		bm := 0.0
		if maxBm25 > 0 {
			bm = bm25Scores[path] / maxBm25
		}
		blend[path] = weight*cos + (1-weight)*bm
	}
	for path, s := range bm25Scores {
		if _, ok := blend[path]; !ok && maxBm25 > 0 {
			blend[path] = (1 - weight) * s / maxBm25
		}
	}
	paths := make([]string, 0, len(blend))
	for p := range blend {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(a, b int) bool { return blend[paths[a]] > blend[paths[b]] })
	if len(paths) > limit {
		paths = paths[:limit]
	}
	// Metadata for every blended path — including vector-only documents
	// that never appeared in the BM25 hits (they have no bm25Hit entry).
	meta := make(map[string]bm25Hit, len(paths))
	if len(paths) > 0 {
		ph := strings.Repeat("?,", len(paths))
		ph = ph[:len(ph)-1]
		args := make([]any, len(paths))
		for i, p := range paths {
			args[i] = p
		}
		mrows, err := db.Query(`SELECT path, title, summary FROM kb_docs WHERE path IN (`+ph+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("doc metadata: %w", err)
		}
		for mrows.Next() {
			var m bm25Hit
			if err := mrows.Scan(&m.Path, &m.Title, &m.Summary); err != nil {
				_ = mrows.Close()
				return nil, err
			}
			meta[m.Path] = m
		}
		_ = mrows.Close()
	}
	results := make([]SearchResult, 0, len(paths))
	for _, p := range paths {
		m := meta[p]
		results = append(results, SearchResult{
			Path: m.Path, Title: m.Title, Summary: m.Summary,
			Score: blend[p], Chunk: bestChunk[p], ChunkText: bestText[p],
		})
	}
	return results, nil
}

func toFloat32(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(f)
	}
	return out
}

// normalize returns the L2-normalized copy of v, or nil when v is the
// zero vector (no semantic signal — callers fall back to BM25 ranking).
func normalize(v []float64) []float64 {
	var norm float64
	for _, f := range v {
		norm += f * f
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return nil
	}
	out := make([]float64, len(v))
	for i, f := range v {
		out[i] = f / norm
	}
	return out
}

// cosine returns the cosine similarity between a normalized query vector
// and a raw chunk vector (normalized on the fly — vectors are stored
// unnormalized).
func cosine(qn []float64, v []float32) float64 {
	var dot, norm float64
	n := len(qn)
	if len(v) < n {
		n = len(v)
	}
	for i := range n {
		f := float64(v[i])
		dot += f * qn[i]
		norm += f * f
	}
	if norm == 0 {
		return 0
	}
	return dot / math.Sqrt(norm)
}

// decodeFloat32 decodes a sqlite-vec float32 BLOB (SerializeFloat32 output:
// a raw little-endian float32 array).
func decodeFloat32(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// SearchResult is one ranked hit. JSON tags are lowercase so machine
// consumers (e.g. agent-server's interactive KB-first precompute running
// `otg kb search --json`) get stable field names; the human CLI output and
// `kb ask` reference blocks read the Go fields directly, unaffected by tags.
type SearchResult struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
	Score   float64 `json:"score"`
	Chunk   string  `json:"chunk,omitempty"` // best-matching section heading ("" when unknown)
	// ChunkText is the embedded chunk text of the best-matching section
	// ("" on the BM25-only path) — used by `kb ask` reference blocks and
	// the rerank stage.
	ChunkText string `json:"chunkText,omitempty"`
}

// tokenize splits text into lowercase terms: ASCII word runs and CJK
// bigrams (character pairs — good enough recall for Chinese without a
// segmenter). Both indexing (docTokens) and querying (matchQuery) use it,
// keeping the FTS5 term space identical to the historical in-memory index.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	runes := []rune(text)
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			j := i
			for j < len(runes) && (runes[j] >= 'a' && runes[j] <= 'z' || runes[j] >= '0' && runes[j] <= '9') {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		case unicode.Is(unicode.Han, r):
			// CJK bigrams; a trailing lone Han char becomes a unigram.
			if i+1 < len(runes) && unicode.Is(unicode.Han, runes[i+1]) {
				tokens = append(tokens, string(runes[i:i+2]))
				i += 2
			} else {
				tokens = append(tokens, string(runes[i:i+1]))
				i++
			}
		default:
			i++
		}
	}
	return tokens
}

// topicOverlap reports whether the document's topics share ≥2 terms with any
// kept cluster (semantic noise dedup — historical rank semantics).
func topicOverlap(topics string, kept [][]string) bool {
	fields := strings.Fields(topics)
	if len(fields) == 0 {
		return false
	}
	for _, cluster := range kept {
		shared := 0
		for _, f := range fields {
			for _, c := range cluster {
				if f == c {
					shared++
					break
				}
			}
		}
		if shared >= 2 {
			return true
		}
	}
	return false
}
