package knowledge

import (
	"database/sql"
	"fmt"
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
	defer db.Close()

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
	defer rows.Close()
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
func hybridRank(db *sql.DB, hits []bm25Hit, query string, limit int, client *EmbeddingClient, weight float64) ([]SearchResult, error) {
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
	qb, err := sqlite_vec.SerializeFloat32(toFloat32(qvec))
	if err != nil {
		return rankBM25(hits, limit), nil
	}

	// KNN over every vector (full scan, like the historical in-memory walk;
	// vec0 requires an explicit LIMIT); join doc paths so no second lookup
	// is needed.
	var nChunks int
	if err := db.QueryRow(`SELECT count(*) FROM kb_chunks`).Scan(&nChunks); err != nil {
		return nil, fmt.Errorf("count chunks: %w", err)
	}
	rows, err := db.Query(`SELECT d.path, c.heading, v.distance
		FROM kb_vec v
		JOIN kb_chunks c ON c.chunk_id = v.rowid
		JOIN kb_docs d ON d.rowid = c.doc_id
		WHERE v.embedding MATCH ? AND k = ?
		ORDER BY v.distance`, qb, nChunks)
	if err != nil {
		return nil, fmt.Errorf("knn query: %w", err)
	}
	defer rows.Close()
	bestCos := make(map[string]float64)
	bestChunk := make(map[string]string)
	for rows.Next() {
		var path, heading string
		var dist float64
		if err := rows.Scan(&path, &heading, &dist); err != nil {
			return nil, err
		}
		if dist >= 1 { // cosine ≤ 0 — historical cut
			continue
		}
		if c, ok := bestCos[path]; !ok || 1-dist > c {
			bestCos[path] = 1 - dist
			bestChunk[path] = heading
		}
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
	// Metadata for every blended path — including vector-only documents that
	// never appeared in the BM25 hits (they have no bm25Hit entry).
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
				mrows.Close()
				return nil, err
			}
			meta[m.Path] = m
		}
		mrows.Close()
	}
	results := make([]SearchResult, 0, len(paths))
	for _, p := range paths {
		m := meta[p]
		results = append(results, SearchResult{
			Path: m.Path, Title: m.Title, Summary: m.Summary,
			Score: blend[p], Chunk: bestChunk[p],
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

// SearchResult is one ranked hit.
type SearchResult struct {
	Path    string
	Title   string
	Summary string
	Score   float64
	Chunk   string // best-matching section heading ("" when unknown)
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
