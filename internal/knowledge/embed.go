package knowledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// vectorIndexFile is the JSON vector store next to References/. Hidden from
// Obsidian's markdown views; rebuilt by `otg kb index` (or lazily by search
// when stale). Kept as a read fallback for migration.
const vectorIndexFile = ".kb-vectors.json"

// vectorIndexGobFile is the compact binary vector store (gob). JSON is ~3x
// larger and 10x slower to parse at 10k-document scale, so writes go to gob;
// LoadVectors falls back to JSON for existing deployments.
const vectorIndexGobFile = ".kb-vectors.gob"

// EmbeddingClient talks to an ollama or OpenAI-compatible embedding backend.
type EmbeddingClient struct {
	cfg    *config.KBEmbeddingConfig
	client *http.Client
}

// NewEmbeddingClient wraps the configured backend. A nil cfg returns nil —
// callers then fall back to BM25-only search.
func NewEmbeddingClient(cfg *config.KBEmbeddingConfig) *EmbeddingClient {
	if cfg == nil {
		return nil
	}
	return &EmbeddingClient{
		cfg: cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed returns the embedding vector for one text.
func (c *EmbeddingClient) Embed(text string) ([]float64, error) {
	vecs, err := c.EmbedBatch([]string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// EmbedBatch embeds multiple texts in one API call — the throughput win for
// index builds (ollama /api/embed handles arrays; batching ~8 chunks per
// call is ~8x faster than serial single-text requests).
func (c *EmbeddingClient) EmbedBatch(texts []string) ([][]float64, error) {
	if c == nil || len(texts) == 0 {
		return nil, fmt.Errorf("embedding client not configured")
	}
	switch c.cfg.Backend {
	case "", "ollama":
		return c.embedOllamaBatch(texts)
	case "openai":
		return c.embedOpenAIBatch(texts)
	default:
		return nil, fmt.Errorf("unknown embedding backend %q", c.cfg.Backend)
	}
}

// embedOllamaBatch calls POST {base}/api/embed with {model, input: [...]}.
func (c *EmbeddingClient) embedOllamaBatch(texts []string) ([][]float64, error) {
	body, err := json.Marshal(map[string]any{"model": c.cfg.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	url := c.cfg.URL + "/api/embed"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama embed: %s: %s", resp.Status, string(data))
	}
	var payload struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("ollama embed decode: %w", err)
	}
	if len(payload.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: got %d embeddings for %d inputs", len(payload.Embeddings), len(texts))
	}
	return payload.Embeddings, nil
}

// embedOpenAIBatch calls POST {base}/embeddings with {model, input: [...]}.
func (c *EmbeddingClient) embedOpenAIBatch(texts []string) ([][]float64, error) {
	body, err := json.Marshal(map[string]any{"model": c.cfg.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	url := c.cfg.URL + "/embeddings"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("openai embeddings: %s: %s", resp.Status, string(data))
	}
	var payload struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("openai embeddings decode: %w", err)
	}
	if len(payload.Data) != len(texts) {
		return nil, fmt.Errorf("openai embeddings: got %d for %d inputs", len(payload.Data), len(texts))
	}
	vecs := make([][]float64, 0, len(payload.Data))
	for _, d := range payload.Data {
		vecs = append(vecs, d.Embedding)
	}
	return vecs, nil
}

// chunkVector is one embedded section of a document.
type chunkVector struct {
	Heading string    `json:"heading"` // "## 要点" etc.; "" for pre-heading content
	Vector  []float64 `json:"vector"`
}

// docVectors holds a document's chunked embeddings.
type docVectors struct {
	Title      string        `json:"title"`
	Chunks     []chunkVector `json:"chunks"`
	SourceHash string        `json:"source_hash,omitempty"` // content sha256 prefix; unchanged docs skip re-embedding
}

// vectorIndex maps document path → chunked vectors. Versioned for format
// migration: older single-vector files (path → []float64) fail to unmarshal
// and trigger a rebuild via otg kb index.
type vectorIndex map[string]*docVectors

const vectorIndexVersion = 2

// embedBatchSize is the number of chunks per embedding API call.
const embedBatchSize = 8

// vectorStoreMeta is the shared store header in both the gob and JSON
// formats — the single decoding point for LoadVectors/VectorsModel/
// VectorStoreCorrupt so format evolution happens in one place.
type vectorStoreMeta struct {
	Version int
	Model   string
	Docs    map[string]*docVectors
}

// healthy reports whether a decoded store is usable (right version, non-nil
// docs). Legacy single-vector files decode as JSON but carry another shape
// and fail here, triggering a rebuild — same semantics as before.
func (m vectorStoreMeta) healthy() bool {
	return m.Version == vectorIndexVersion && m.Docs != nil
}

func decodeGobMeta(data []byte) (vectorStoreMeta, bool) {
	var meta vectorStoreMeta
	if gob.NewDecoder(bytes.NewReader(data)).Decode(&meta) != nil {
		return vectorStoreMeta{}, false
	}
	return meta, true
}

func decodeJSONMeta(data []byte) (vectorStoreMeta, bool) {
	var raw struct {
		Version int                    `json:"version"`
		Model   string                 `json:"model,omitempty"`
		Docs    map[string]*docVectors `json:"docs"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return vectorStoreMeta{}, false
	}
	return vectorStoreMeta{Version: raw.Version, Model: raw.Model, Docs: raw.Docs}, true
}

// loadVectorsWithModel reads the vector store once and returns both the
// index and the recorded embedding model. Nil index when absent or of the
// legacy single-vector format. Prefers the compact gob file and falls back
// to JSON so existing deployments migrate on the next SaveVectors.
func loadVectorsWithModel(vaultDir string) (vectorIndex, string) {
	refsDir := filepath.Join(vaultDir, "References")
	if data, err := os.ReadFile(filepath.Join(refsDir, vectorIndexGobFile)); err == nil {
		if meta, ok := decodeGobMeta(data); ok && meta.healthy() {
			return meta.Docs, meta.Model
		}
	}
	if data, err := os.ReadFile(filepath.Join(refsDir, vectorIndexFile)); err == nil {
		if meta, ok := decodeJSONMeta(data); ok && meta.healthy() {
			return meta.Docs, meta.Model
		}
	}
	return nil, ""
}

// VectorStoreCorrupt reports whether no vector store format is readable:
// a healthy gob OR a healthy JSON fallback means retrieval works, and only
// when every existing format fails to decode (or the version mismatches)
// does the store need a rebuild. Distinguishes "missing" (nothing to read —
// first build) from "corrupt" (needs a rebuild to heal).
func VectorStoreCorrupt(vaultDir string) bool {
	refsDir := filepath.Join(vaultDir, "References")
	found := false
	if data, err := os.ReadFile(filepath.Join(refsDir, vectorIndexGobFile)); err == nil {
		found = true
		if meta, ok := decodeGobMeta(data); ok && meta.healthy() {
			return false // healthy gob
		}
		// gob unreadable — a valid JSON fallback still serves retrieval.
	}
	if data, err := os.ReadFile(filepath.Join(refsDir, vectorIndexFile)); err == nil {
		found = true
		if meta, ok := decodeJSONMeta(data); ok && meta.healthy() {
			return false // healthy JSON fallback
		}
	}
	// Corrupt only when a store file exists but no format decodes; a
	// completely missing store is the normal first-build state.
	return found
}

// LoadVectors reads the vector store; nil when absent or of the legacy
// single-vector format.
func LoadVectors(vaultDir string) vectorIndex {
	idx, _ := loadVectorsWithModel(vaultDir)
	return idx
}

// VectorsModel returns the embedding model recorded in the vector store, or
// "" when the store predates model tracking (legacy files were always built
// with the locally configured model).
func VectorsModel(vaultDir string) string {
	_, model := loadVectorsWithModel(vaultDir)
	return model
}

// LoadVectorsFor returns the vector store only when it was built with the
// given model. A model mismatch (or legacy store with no recorded model —
// treated as "matches whatever is configured first") returns nil, forcing a
// full rebuild: vectors from different embedding models have incompatible
// dimensions and must never be mixed in cosine comparison.
func LoadVectorsFor(vaultDir, model string) vectorIndex {
	idx, stored := loadVectorsWithModel(vaultDir)
	if stored != "" && stored != model {
		return nil
	}
	return idx
}

// SaveVectors writes the vector store atomically in compact gob format,
// recording the embedding model so later model switches invalidate it.
func SaveVectors(vaultDir string, idx vectorIndex, model string) error {
	meta := struct {
		Version int
		Model   string
		Docs    map[string]*docVectors
	}{Version: vectorIndexVersion, Model: model, Docs: idx}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&meta); err != nil {
		return err
	}
	return yamlfrontmatter.AtomicWrite(filepath.Join(vaultDir, "References", vectorIndexGobFile), buf.Bytes())
}

// BuildVectors embeds every References document section-by-section
// ("## heading" chunks; pre-heading content becomes one chunk) and persists
// the vector store. Section-level vectors give precise hits on long
// documents (a query about error handling matches the error-handling
// section, not the whole doc). Embedding calls run concurrently (4 workers)
// because ollama bge-m3 takes seconds per chunk serially. Returns the number
// of documents embedded.
func BuildVectors(vaultDir string, client *EmbeddingClient) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("embedding not configured")
	}
	refsDir := filepath.Join(vaultDir, "References")
	type job struct {
		path string
		data []byte
		rel  string
	}
	var jobs []job
	err := filepath.WalkDir(refsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isMarkdown(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(refsDir, path)
		jobs = append(jobs, job{path: path, data: data, rel: filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return 0, err
	}

	idx := make(vectorIndex)
	// Incremental: reuse vectors for unchanged documents (content hash
	// match) so rebuilds only re-embed what actually changed. A stored store
	// built by a different model is treated as absent: mixing embedding
	// dimensions would corrupt cosine comparison, so it forces a full rebuild.
	prev := LoadVectorsFor(vaultDir, client.cfg.Model)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	workers := 4
	if len(jobs) < workers {
		workers = len(jobs)
	}
	jobCh := make(chan job)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				hash := contentHash(j.data)
				mu.Lock()
				if old, ok := prev[j.rel]; ok && old.SourceHash == hash {
					idx[j.rel] = old
					mu.Unlock()
					continue
				}
				mu.Unlock()
				chunks := chunkDocument(j.data)
				doc := &docVectors{Title: extractH1(j.data), SourceHash: hash, Chunks: make([]chunkVector, 0, len(chunks))}
				// Batch 8 chunks per API call — /api/embed arrays make this
				// ~8x faster than serial single-text requests.
				for start := 0; start < len(chunks); start += embedBatchSize {
					end := start + embedBatchSize
					if end > len(chunks) {
						end = len(chunks)
					}
					texts := make([]string, 0, end-start)
					for _, c := range chunks[start:end] {
						texts = append(texts, c.text)
					}
					vecs, err := client.EmbedBatch(texts)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("embed %s %q: %w", j.rel, chunks[start].heading, err)
						}
						mu.Unlock()
						return
					}
					for i, vec := range vecs {
						doc.Chunks = append(doc.Chunks, chunkVector{Heading: chunks[start+i].heading, Vector: vec})
					}
				}
				mu.Lock()
				idx[j.rel] = doc
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	if firstErr != nil {
		return 0, firstErr
	}
	if err := SaveVectors(vaultDir, idx, client.cfg.Model); err != nil {
		return 0, err
	}
	return len(idx), nil
}

// contentHash returns a short content fingerprint for incremental builds.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:8])
}

// textChunk is one embeddable section.
type textChunk struct {
	heading string
	text    string
}

// chunkDocument splits a References document into sections at "## " headings.
// Every chunk is prefixed with topics + title + summary so section vectors
// carry document context; pre-heading content becomes its own chunk.
func chunkDocument(data []byte) []textChunk {
	fm, body, err := parseFrontmatter(data)
	if err != nil {
		fm, body = nil, string(data)
	}
	var prefix strings.Builder
	if fm != nil {
		if topics, ok := fm["topics"]; ok {
			prefix.WriteString(strings.Join(toStringSlice(topics), " "))
			prefix.WriteString(" ")
		}
	}
	prefix.WriteString(extractH1(data))
	prefix.WriteString(" ")
	prefix.WriteString(extractSummary(data))
	prefix.WriteString("\n")

	var chunks []textChunk
	current := strings.Builder{}
	currentHeading := ""
	appendChunk := func(text string) {
		// CPU-only inference (~200 chars/s on bge-m3) bounds the embeddable
		// volume: embed only the section head — the first 300 chars after the
		// heading carry the section's topic sentence, which is what retrieval
		// matches. Full-body vectors would take 40+ minutes to build.
		if len(text) > 300 {
			text = text[:300]
		}
		text = prefix.String() + currentHeading + "\n" + text
		chunks = append(chunks, textChunk{heading: currentHeading, text: text})
	}
	flush := func() {
		if strings.TrimSpace(current.String()) == "" {
			return
		}
		appendChunk(current.String())
		current.Reset()
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			currentHeading = line
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	flush()
	return chunks
}

func isMarkdown(path string) bool {
	return len(path) > 3 && path[len(path)-3:] == ".md" && filepath.Base(path) != "INDEX.md"
}
