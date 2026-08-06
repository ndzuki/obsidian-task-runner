package knowledge

import (
	"bytes"
	"crypto/sha256"
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
// when stale).
const vectorIndexFile = ".kb-vectors.json"

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

// LoadVectors reads the vector store; nil when absent or of the legacy
// single-vector format.
func LoadVectors(vaultDir string) vectorIndex {
	data, err := os.ReadFile(filepath.Join(vaultDir, "References", vectorIndexFile))
	if err != nil {
		return nil
	}
	var meta struct {
		Version int                   `json:"version"`
		Docs    map[string]*docVectors `json:"docs"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil // legacy format → rebuild
	}
	if meta.Version != vectorIndexVersion {
		return nil
	}
	return meta.Docs
}

// SaveVectors writes the vector store atomically.
func SaveVectors(vaultDir string, idx vectorIndex) error {
	meta := struct {
		Version int                    `json:"version"`
		Docs    map[string]*docVectors `json:"docs"`
	}{Version: vectorIndexVersion, Docs: idx}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return yamlfrontmatter.AtomicWrite(filepath.Join(vaultDir, "References", vectorIndexFile), data)
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
	// match) so rebuilds only re-embed what actually changed.
	prev := LoadVectors(vaultDir)
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
	if err := SaveVectors(vaultDir, idx); err != nil {
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
		text = prefix.String() + currentHeading + "\n" + text
		// bge-m3 input ceiling is 8192 tokens; Chinese text is ~1-2 tokens
		// per character, so cap each embedded chunk conservatively.
		const maxChunk = 1500
		for len(text) > maxChunk {
			// Cut at the last paragraph break within the window.
			cut := strings.LastIndex(text[:maxChunk], "\n\n")
			if cut < maxChunk/2 {
				cut = maxChunk
			}
			chunks = append(chunks, textChunk{heading: currentHeading, text: text[:cut]})
			text = text[cut:]
		}
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
