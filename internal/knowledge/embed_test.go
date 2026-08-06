package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// fakeEmbeddingServer returns deterministic small vectors: token-length
// dependent so "connect rpc" and "connect" texts share direction.
func fakeEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Input  string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		text := req.Prompt
		if text == "" {
			text = req.Input
		}
		// Vector: dimension 3, each component counts keyword presence.
		vec := []float64{0, 0, 0}
		if strings.Contains(text, "connect") || strings.Contains(text, "rpc") {
			vec[0] = 1
		}
		if strings.Contains(text, "helm") || strings.Contains(text, "chart") {
			vec[1] = 1
		}
		if strings.Contains(text, "日志") || strings.Contains(text, "journal") {
			vec[2] = 1
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/embeddings") {
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": vec})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": vec}}})
	}))
	return srv
}

func TestEmbeddingClientOllama(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})
	vec, err := client.Embed("connect rpc 协议")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if vec[0] != 1 || vec[1] != 0 || vec[2] != 0 {
		t.Fatalf("vector = %v, want [1 0 0]", vec)
	}
}

func TestEmbeddingClientOpenAI(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "openai", URL: srv.URL + "/v1", Model: "text-embedding-3-small", APIKey: "k"})
	vec, err := client.Embed("helm chart")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if vec[1] != 1 {
		t.Fatalf("vector = %v, want [0 1 0]", vec)
	}
}

func TestSearchHybridSurfacesVectorOnlyHit(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Document with NO shared tokens with the query ("zircon mesh" — the doc
	// is Chinese-only, so BM25 finds no overlap).
	writeSearchDoc(t, refsDir, "state-machine.md", "workflow, state",
		"任务生命周期状态机：主状态互斥、迁移前置条件。")
	idx, err := BuildSearchIndex(vault)
	if err != nil {
		t.Fatalf("BuildSearchIndex: %v", err)
	}
	if hits := idx.Search("zircon mesh", 3); len(hits) != 0 {
		t.Fatalf("BM25 should not match zircon mesh, got %+v", hits)
	}
	// Vectors: doc vector component 0; the fake embeds "zircon" to the same
	// component — semantic (not lexical) similarity.
	vectors := vectorIndex{"core/state-machine.md": []float64{1, 0, 0}}
	embed := func(text string) ([]float64, error) {
		if strings.Contains(text, "zircon") || strings.Contains(text, "状态机") {
			return []float64{1, 0, 0}, nil
		}
		return []float64{0, 0, 0}, nil
	}
	hybrid := idx.SearchHybrid("zircon mesh", 3, vectors, 1.0, embed)
	if len(hybrid) == 0 || hybrid[0].Path != "core/state-machine.md" {
		t.Fatalf("hybrid should surface vector-only hit, got %+v", hybrid)
	}
	// Embedding failure → graceful BM25 fallback (no hits for this query).
	hybrid = idx.SearchHybrid("zircon mesh", 3, vectors, 1.0, func(string) ([]float64, error) {
		return nil, os.ErrNotExist
	})
	if len(hybrid) != 0 {
		t.Fatalf("fallback should return BM25-only (empty here), got %+v", hybrid)
	}
}
