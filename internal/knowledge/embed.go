package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	if c == nil {
		return nil, fmt.Errorf("embedding client not configured")
	}
	switch c.cfg.Backend {
	case "", "ollama":
		return c.embedOllama(text)
	case "openai":
		return c.embedOpenAI(text)
	default:
		return nil, fmt.Errorf("unknown embedding backend %q", c.cfg.Backend)
	}
}

// embedOllama calls POST {base}/api/embeddings with {model, prompt}.
func (c *EmbeddingClient) embedOllama(text string) ([]float64, error) {
	body, err := json.Marshal(map[string]string{"model": c.cfg.Model, "prompt": text})
	if err != nil {
		return nil, err
	}
	url := c.cfg.URL + "/api/embeddings"
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embeddings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama embeddings: %s: %s", resp.Status, string(data))
	}
	var payload struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("ollama embeddings decode: %w", err)
	}
	if len(payload.Embedding) == 0 {
		return nil, fmt.Errorf("ollama embeddings: empty vector")
	}
	return payload.Embedding, nil
}

// embedOpenAI calls POST {base}/embeddings (OpenAI-compatible) with
// {model, input}.
func (c *EmbeddingClient) embedOpenAI(text string) ([]float64, error) {
	body, err := json.Marshal(map[string]string{"model": c.cfg.Model, "input": text})
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
	if len(payload.Data) == 0 || len(payload.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai embeddings: empty data")
	}
	return payload.Data[0].Embedding, nil
}

// vectorIndex maps document path → embedding vector.
type vectorIndex map[string][]float64

// LoadVectors reads the vector store; nil when absent.
func LoadVectors(vaultDir string) vectorIndex {
	data, err := os.ReadFile(filepath.Join(vaultDir, "References", vectorIndexFile))
	if err != nil {
		return nil
	}
	var idx vectorIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil
	}
	return idx
}

// SaveVectors writes the vector store atomically.
func SaveVectors(vaultDir string, idx vectorIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return yamlfrontmatter.AtomicWrite(filepath.Join(vaultDir, "References", vectorIndexFile), data)
}

// BuildVectors embeds every References document (title+summary+topics+body
// prefix) and persists the vector store. Returns the number of documents
// embedded and the first error encountered (aborts on first failure).
func BuildVectors(vaultDir string, client *EmbeddingClient) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("embedding not configured")
	}
	refsDir := filepath.Join(vaultDir, "References")
	idx := make(vectorIndex)
	embedded := 0
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
		text := embeddingText(data)
		vec, err := client.Embed(text)
		if err != nil {
			return fmt.Errorf("embed %s: %w", rel, err)
		}
		idx[filepath.ToSlash(rel)] = vec
		embedded++
		return nil
	})
	if err != nil {
		return embedded, err
	}
	if err := SaveVectors(vaultDir, idx); err != nil {
		return embedded, err
	}
	return embedded, nil
}

// embeddingText composes the text fed to the embedding model: frontmatter
// topics + title + summary + body prefix (vectors stay cheap; full bodies
// add little for 45-doc scale).
func embeddingText(data []byte) string {
	fm, body, err := parseFrontmatter(data)
	if err != nil {
		return string(data)
	}
	var b bytes.Buffer
	if fm != nil {
		if topics, ok := fm["topics"]; ok {
			b.WriteString(strings.Join(toStringSlice(topics), " "))
			b.WriteString(" ")
		}
	}
	b.WriteString(extractH1(data))
	b.WriteString(" ")
	b.WriteString(extractSummary(data))
	if len(body) > 2000 {
		body = body[:2000]
	}
	b.WriteString(" ")
	b.WriteString(body)
	return b.String()
}

func isMarkdown(path string) bool {
	return len(path) > 3 && path[len(path)-3:] == ".md" && filepath.Base(path) != "INDEX.md"
}
