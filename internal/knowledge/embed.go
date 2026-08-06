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

// chunkVector is one embedded section of a document.
type chunkVector struct {
	Heading string    `json:"heading"` // "## 要点" etc.; "" for pre-heading content
	Vector  []float64 `json:"vector"`
}

// docVectors holds a document's chunked embeddings.
type docVectors struct {
	Title  string        `json:"title"`
	Chunks []chunkVector `json:"chunks"`
}

// vectorIndex maps document path → chunked vectors. Versioned for format
// migration: older single-vector files (path → []float64) fail to unmarshal
// and trigger a rebuild via otg kb index.
type vectorIndex map[string]*docVectors

const vectorIndexVersion = 2

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
// section, not the whole doc). Returns the number of documents embedded.
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
		chunks := chunkDocument(data)
		doc := &docVectors{Title: extractH1(data), Chunks: make([]chunkVector, 0, len(chunks))}
		for _, c := range chunks {
			vec, err := client.Embed(c.text)
			if err != nil {
				return fmt.Errorf("embed %s %q: %w", rel, c.heading, err)
			}
			doc.Chunks = append(doc.Chunks, chunkVector{Heading: c.heading, Vector: vec})
		}
		idx[filepath.ToSlash(rel)] = doc
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
	flush := func() {
		if strings.TrimSpace(current.String()) == "" {
			return
		}
		text := prefix.String() + currentHeading + "\n" + current.String()
		if len(text) > 8000 {
			text = text[:8000] // bge-m3 8192-token input ceiling guard
		}
		chunks = append(chunks, textChunk{heading: currentHeading, text: text})
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
