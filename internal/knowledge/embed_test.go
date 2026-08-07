package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// fakeEmbeddingServer returns deterministic small vectors: token-length
// dependent so "connect rpc" and "connect" texts share direction. Supports
// both single (/api/embeddings) and batch (/api/embed) ollama endpoints.
func fakeEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string   `json:"model"`
			Prompt string   `json:"prompt"`
			Input  []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		texts := req.Input
		if len(texts) == 0 && req.Prompt != "" {
			texts = []string{req.Prompt}
		}
		vecs := make([][]float64, 0, len(texts))
		for _, text := range texts {
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
			vecs = append(vecs, vec)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/embed") {
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/embeddings") {
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": vecs[0]})
			return
		}
		data := make([]map[string]any, 0, len(vecs))
		for _, v := range vecs {
			data = append(data, map[string]any{"embedding": v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
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
