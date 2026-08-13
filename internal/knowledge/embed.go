package knowledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// EmbeddingClient talks to an ollama or OpenAI-compatible embedding backend.
type EmbeddingClient struct {
	cfg    *config.KBEmbeddingConfig
	client *http.Client
}

// batchSize returns the configured chunk batch (32 default). Index builds
// call EmbedBatch with this many texts per request — the throughput win for
// ollama /api/embed.
func (c *EmbeddingClient) batchSize() int {
	if c == nil || c.cfg == nil || c.cfg.BatchSize <= 0 {
		return 32
	}
	return c.cfg.BatchSize
}

// chunkChars returns the per-chunk body cap (600 default).
func (c *EmbeddingClient) chunkChars() int {
	if c == nil || c.cfg == nil || c.cfg.ChunkChars <= 0 {
		return 600
	}
	return c.cfg.ChunkChars
}

// knnCandidates returns the max BM25-hit documents whose chunks enter the
// cosine candidate set (100 default).
func (c *EmbeddingClient) knnCandidates() int {
	if c == nil || c.cfg == nil || c.cfg.KNNCandidates <= 0 {
		return 100
	}
	return c.cfg.KNNCandidates
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
// carry document context; pre-heading content becomes its own chunk. Body
// text beyond chunkChars chars is dropped — only the section head is
// embedded (the topic sentence carries what retrieval matches); the cap is
// configurable (kb_embedding.chunk_chars, 600 default) and bounded by the
// backend's context window.
func chunkDocument(data []byte, chunkChars int) []textChunk {
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
		if len(text) > chunkChars {
			text = text[:chunkChars]
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
