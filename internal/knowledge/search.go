package knowledge

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// searchIndex is an in-memory BM25 inverted index over References/.
// Pure Go, zero dependencies — sufficient for the current 45-document scale;
// an embedding layer can sit in front of it later (see SearchQuery).
type searchIndex struct {
	docs       []searchDoc   // parallel with postings
	postings   map[string][]int // term → doc ids
	docLen     []int         // token counts per doc
	avgDocLen  float64
	totalDocs  int
}

type searchDoc struct {
	Path     string
	Title    string
	Summary  string
	Topics   string
	Body     string
	termFreq map[string]int // precomputed at index time (BM25 tf)
}

// SearchResult is one ranked hit.
type SearchResult struct {
	Path    string
	Title   string
	Summary string
	Score   float64
}

const (
	bm25K  = 1.2
	bm25B  = 0.75
)

// BuildSearchIndex loads every References/ document (frontmatter fields +
// body) and builds the BM25 index. Rebuilding on each search is wasteful;
// callers should cache the returned index and invalidate on References
// writes (the daemon already tracks those events).
func BuildSearchIndex(vaultDir string) (*searchIndex, error) {
	refsDir := filepath.Join(vaultDir, "References")
	idx := &searchIndex{
		postings: make(map[string][]int),
	}
	err := filepath.WalkDir(refsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") || d.Name() == "INDEX.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fm, body, err := parseFrontmatter(data)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(refsDir, path)
		doc := searchDoc{
			Path:   filepath.ToSlash(rel),
			Title:  extractH1(data),
			Summary: extractSummary(data),
		}
		if fm != nil {
			if topics, ok := fm["topics"]; ok {
				doc.Topics = strings.Join(toStringSlice(topics), " ")
			}
		}
		doc.Body = body
		idx.addDoc(doc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if idx.totalDocs > 0 {
		idx.avgDocLen = float64(sum(idx.docLen)) / float64(idx.totalDocs)
	}
	return idx, nil
}

func (idx *searchIndex) addDoc(doc searchDoc) {
	docID := idx.totalDocs
	idx.docs = append(idx.docs, doc)
	text := strings.ToLower(doc.Title + " " + doc.Summary + " " + doc.Topics + " " + doc.Body)
	tokens := tokenize(text)
	idx.docLen = append(idx.docLen, len(tokens))
	freq := make(map[string]int, 32)
	for _, t := range tokens {
		freq[t]++
	}
	doc.termFreq = freq
	idx.docs[docID] = doc
	for t := range freq {
		idx.postings[t] = append(idx.postings[t], docID)
	}
	idx.totalDocs++
}

// tokenize splits text into lowercase terms: ASCII word runs and CJK
// bigrams (character pairs — good enough recall for Chinese without a
// segmenter).
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

// Search ranks documents by BM25 over the query terms. Returns up to limit
// hits with score > 0.
func (idx *searchIndex) Search(query string, limit int) []SearchResult {
	if idx == nil || idx.totalDocs == 0 || limit <= 0 {
		return nil
	}
	terms := tokenize(strings.ToLower(query))
	scores := make([]float64, idx.totalDocs)
	for _, term := range terms {
		postings, ok := idx.postings[term]
		if !ok {
			continue
		}
		df := len(postings)
		idf := log2(1 + (float64(idx.totalDocs)-float64(df)+0.5)/(float64(df)+0.5))
		for _, docID := range postings {
			tf := idx.docs[docID].termFreq[term]
			if tf == 0 {
				continue
			}
			docLen := float64(idx.docLen[docID])
			tfF := float64(tf)
			denom := tfF + bm25K*(1-bm25B+bm25B*docLen/idx.avgDocLen)
			scores[docID] += idf * tfF * (bm25K + 1) / denom
		}
	}
	return idx.rank(scores, limit)
}

// SearchHybrid blends BM25 scores with embedding cosine similarity when a
// vector store is available: each document's cosine to the query contributes
// with the configured weight, BM25 with (1-weight). Documents that only the
// vector side finds (no shared tokens) still surface — this is the semantic
// recall gain over keyword matching.
func (idx *searchIndex) SearchHybrid(query string, limit int, vectors vectorIndex, weight float64, embed func(string) ([]float64, error)) []SearchResult {
	if idx == nil || idx.totalDocs == 0 || limit <= 0 {
		return nil
	}
	bm25 := idx.Search(query, idx.totalDocs)
	bm25Scores := make(map[string]float64, len(bm25))
	for _, h := range bm25 {
		bm25Scores[h.Path] = h.Score
	}
	// Normalize BM25 scores to [0,1] for blending.
	maxBm25 := 0.0
	for _, s := range bm25Scores {
		if s > maxBm25 {
			maxBm25 = s
		}
	}
	qvec, err := embed(query)
	if err != nil || len(vectors) == 0 {
		return bm25 // embedding unavailable → pure BM25
	}
	if weight <= 0 {
		return bm25
	}
	if weight > 1 {
		weight = 1
	}
	blend := make(map[string]float64, idx.totalDocs)
	for path, vec := range vectors {
		cos := cosine(qvec, vec)
		if cos <= 0 {
			continue
		}
		bm := 0.0
		if maxBm25 > 0 {
			bm = bm25Scores[path] / maxBm25
		}
		blend[path] = weight*cos + (1-weight)*bm
	}
	// Documents only found by BM25 keep a scaled BM25 score.
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
	results := make([]SearchResult, 0, len(paths))
	for _, p := range paths {
		doc, ok := idx.docByPath(p)
		if !ok {
			continue
		}
		results = append(results, SearchResult{Path: doc.Path, Title: doc.Title, Summary: doc.Summary, Score: blend[p]})
	}
	return results
}

func (idx *searchIndex) docByPath(path string) (searchDoc, bool) {
	for _, d := range idx.docs {
		if d.Path == path {
			return d, true
		}
	}
	return searchDoc{}, false
}

// rank converts per-doc scores to sorted results.
func (idx *searchIndex) rank(scores []float64, limit int) []SearchResult {
	order := make([]int, 0, idx.totalDocs)
	for i, s := range scores {
		if s > 0 {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	if len(order) > limit {
		order = order[:limit]
	}
	results := make([]SearchResult, 0, len(order))
	for _, docID := range order {
		d := idx.docs[docID]
		results = append(results, SearchResult{
			Path:    d.Path,
			Title:   d.Title,
			Summary: d.Summary,
			Score:   scores[docID],
		})
	}
	return results
}

// cosine returns the cosine similarity between two vectors (0 for empty/zero).
func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

func sqrt(x float64) float64 {
	return math.Sqrt(x)
}

func log2(x float64) float64 {
	return math.Log2(x)
}

func sum(v []int) int {
	total := 0
	for _, n := range v {
		total += n
	}
	return total
}
