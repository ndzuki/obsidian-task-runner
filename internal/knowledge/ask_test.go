package knowledge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// fakeChatServer streams an ollama-style NDJSON completion and validates
// the request shape: model, system+user messages, and a [N]-cited
// reference block in the user message.
func fakeChatServer(t *testing.T, wantModel string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Model != wantModel {
			t.Errorf("model = %q, want %q", req.Model, wantModel)
		}
		if !req.Stream {
			t.Errorf("stream = false, want true")
		}
		var userContent string
		for _, m := range req.Messages {
			if m.Role == "user" {
				userContent = m.Content
			}
		}
		if !strings.Contains(userContent, "参考资料：") || !strings.Contains(userContent, "[1]") {
			t.Errorf("user message missing cited reference block: %.120s", userContent)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, piece := range []string{"答案", "片段", "。"} {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]string{"content": piece}, "done": false,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{}, "done": true,
		})
	}))
}

func TestAskKnowledgeDBStreams(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	chatSrv := fakeChatServer(t, "qwen3:1.7b")
	defer chatSrv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})
	chat := NewChatClient(&config.KBChatConfig{Backend: "ollama", URL: chatSrv.URL, Model: "qwen3:1.7b"})

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "connect-rpc.md", "connect, rpc, grpc, protobuf",
		"Connect 是轻量 RPC 框架，一套 handler 同时支持 Connect/gRPC/gRPC-Web 三种协议。")
	dbPath := syncTestKB(t, vault, client)

	var streamed strings.Builder
	refs, err := AskKnowledgeDB(dbPath, "connect rpc 协议", AskOptions{
		Limit: 3,
		Stream: func(s string) error {
			streamed.WriteString(s)
			return nil
		},
	}, client, chat)
	if err != nil {
		t.Fatalf("AskKnowledgeDB: %v", err)
	}
	if streamed.String() != "答案片段。" {
		t.Fatalf("streamed = %q, want 答案片段。", streamed.String())
	}
	if len(refs) == 0 || refs[0].Path != "core/connect-rpc.md" {
		t.Fatalf("refs = %+v, want core/connect-rpc.md first", refs)
	}
	if refs[0].ChunkText == "" {
		t.Fatalf("ref ChunkText empty: %+v", refs[0])
	}
}

func TestAskKnowledgeDBNoRefsSkipsChat(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("chat must not be called when retrieval finds nothing")
	}))
	defer chatSrv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})
	chat := NewChatClient(&config.KBChatConfig{Backend: "ollama", URL: chatSrv.URL, Model: "qwen3:1.7b"})

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "connect-rpc.md", "connect, rpc, grpc, protobuf",
		"Connect 是轻量 RPC 框架。")
	dbPath := syncTestKB(t, vault, client)

	refs, err := AskKnowledgeDB(dbPath, "完全无关的查询词", AskOptions{Limit: 3}, client, chat)
	if err != nil {
		t.Fatalf("AskKnowledgeDB: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want empty", refs)
	}
}

func TestAskRequiresBothClients(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kb.sqlite")
	if _, err := AskKnowledgeDB(dbPath, "q", AskOptions{}, nil, nil); err == nil {
		t.Fatal("nil clients should fail")
	}
}

// TestAskKnowledgeDBReranks: with a rerank client configured, the retrieval
// fetches top-N candidates (including vector-only hits BM25 misses), the
// cross-encoder reorders them with its own scores, and the final reference
// list is trimmed back to the limit — the [N] citations follow the reranked
// order.
func TestAskKnowledgeDBReranks(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()
	// Local scorer: connect documents 0.9, everything else 0.3 — distinct
	// from both the hybrid blend and the shared fakeRerankServer scoring.
	rerankSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Documents []string `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		results := make([]map[string]any, 0, len(req.Documents))
		for i, d := range req.Documents {
			score := 0.3
			if strings.Contains(d, "connect") {
				score = 0.9
			}
			results = append(results, map[string]any{"index": i, "relevance_score": score})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer rerankSrv.Close()
	chatSrv := fakeChatServer(t, "qwen3:1.7b")
	defer chatSrv.Close()
	client := NewEmbeddingClient(&config.KBEmbeddingConfig{Backend: "ollama", URL: srv.URL, Model: "bge-m3"})
	chat := NewChatClient(&config.KBChatConfig{Backend: "ollama", URL: chatSrv.URL, Model: "qwen3:1.7b"})
	rc := NewRerankClient(&config.KBRerankConfig{Backend: "openai", URL: rerankSrv.URL, Model: "bge-reranker-v2-m3"})

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "connect-rpc.md", "connect, rpc, grpc",
		"Connect 是轻量 RPC 框架。\n\n## 协议细节\nconnect 协议与 grpc 互操作。")
	// Vector-only candidate: BM25 misses it (AND query lacks "connect" in
	// this doc), but the lowercase "rpc" token makes its vector hit — the
	// rerank candidate pool must include it via the vec0 path.
	writeSearchDoc(t, refsDir, "api-spec.md", "协议, 规范",
		"rpc 接口规范说明。\n\n## 版本\n规范版本管理。")
	dbPath := syncTestKB(t, vault, client)

	refs, err := AskKnowledgeDB(dbPath, "connect rpc 协议", AskOptions{
		Limit: 2, Rerank: rc, RerankTopN: 5,
	}, client, chat)
	if err != nil {
		t.Fatalf("AskKnowledgeDB: %v", err)
	}
	if len(refs) != 2 || refs[0].Path != "core/connect-rpc.md" {
		t.Fatalf("reranked refs = %+v, want connect-rpc first", refs)
	}
	// Scores are the cross-encoder's (0.9 / 0.3), proving the rerank stage
	// ran and replaced the hybrid blend scores.
	if refs[0].Score != 0.9 || refs[1].Score != 0.3 {
		t.Fatalf("rerank scores = %v/%v, want 0.9/0.3", refs[0].Score, refs[1].Score)
	}
}

func TestChatClientOpenAIStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	chat := NewChatClient(&config.KBChatConfig{Backend: "openai", URL: srv.URL, Model: "gpt-4o-mini"})
	answer, err := chat.Complete("sys", "user", "", nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if answer != "hi there" {
		t.Fatalf("answer = %q, want %q", answer, "hi there")
	}
}
