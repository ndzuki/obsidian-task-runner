package knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// fakeRerankServer returns deterministic relevance scores: documents whose
// text mentions "connect"/"rpc" score 0.9, everything else 0.1.
func fakeRerankServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		results := make([]map[string]any, 0, len(req.Documents))
		for i, d := range req.Documents {
			score := 0.1
			if strings.Contains(d, "connect") || strings.Contains(d, "rpc") {
				score = 0.9
			}
			results = append(results, map[string]any{"index": i, "relevance_score": score})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
}

func TestRerankClientOpenAI(t *testing.T) {
	srv := fakeRerankServer(t)
	defer srv.Close()
	client := NewRerankClient(&config.KBRerankConfig{Backend: "openai", URL: srv.URL, Model: "bge-reranker-v2-m3"})
	scores, err := client.Rerank("connect rpc", []string{"helm 文档", "connect rpc 文档"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 2 || scores[0] != 0.1 || scores[1] != 0.9 {
		t.Fatalf("scores = %v, want [0.1 0.9]", scores)
	}
}

func TestRerankClientLlamaCppRoute(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"index": 0, "relevance_score": 0.5}},
		})
	}))
	defer srv.Close()
	client := NewRerankClient(&config.KBRerankConfig{Backend: "llamacpp", URL: srv.URL, Model: "x"})
	if _, err := client.Rerank("q", []string{"a"}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if got != "/rerank" {
		t.Fatalf("llamacpp backend hit %q, want /rerank", got)
	}
}

func TestRerankRouteFallback(t *testing.T) {
	// /v1/rerank 404s → the client retries the native /rerank route.
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path == "/v1/rerank" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"index": 0, "relevance_score": 0.7}},
		})
	}))
	defer srv.Close()
	client := NewRerankClient(&config.KBRerankConfig{Backend: "openai", URL: srv.URL, Model: "x"})
	scores, err := client.Rerank("q", []string{"a"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(calls) != 2 || calls[0] != "/v1/rerank" || calls[1] != "/rerank" {
		t.Fatalf("calls = %v, want [/v1/rerank /rerank]", calls)
	}
	if scores[0] != 0.7 {
		t.Fatalf("scores = %v, want [0.7]", scores)
	}
}

func TestRerankResultsReorders(t *testing.T) {
	srv := fakeRerankServer(t)
	defer srv.Close()
	results := []SearchResult{
		{Path: "core/a.md", Title: "helm 文档", ChunkText: "helm chart 包管理"},
		{Path: "core/b.md", Title: "connect 文档", ChunkText: "connect rpc 协议"},
	}
	rc := NewRerankClient(&config.KBRerankConfig{Backend: "openai", URL: srv.URL, Model: "x"})
	ranked := RerankResults(results, "connect rpc", rc, 2)
	if len(ranked) != 2 || ranked[0].Path != "core/b.md" {
		t.Fatalf("reranked = %+v, want b first", ranked)
	}
	if ranked[0].Score != 0.9 {
		t.Fatalf("reranked score = %v, want 0.9", ranked[0].Score)
	}
}

func TestRerankResultsDegrades(t *testing.T) {
	// Unreachable backend → input order kept AND the limit contract holds
	// (the candidate pool is trimmed back — degradation never hands back
	// more rows than requested).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	results := []SearchResult{
		{Path: "core/a.md", Title: "a", Score: 0.9},
		{Path: "core/b.md", Title: "b", Score: 0.1},
		{Path: "core/c.md", Title: "c", Score: 0.05},
	}
	rc := NewRerankClient(&config.KBRerankConfig{Backend: "openai", URL: srv.URL, Model: "x"})
	ranked := RerankResults(results, "q", rc, 2)
	if len(ranked) != 2 || ranked[0].Path != "core/a.md" || ranked[1].Path != "core/b.md" {
		t.Fatalf("degraded order = %+v, want input order trimmed to limit", ranked)
	}
}
