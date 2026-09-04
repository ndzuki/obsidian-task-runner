package vaultweb

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
)

func TestHTTPReadOnlyEndpoints(t *testing.T) {
	s := New(newTestVault(t))
	h := s.Handler()

	tests := []struct {
		name          string
		method        string
		path          string
		wantStatus    int
		wantStatusAny []int
		wantBody      string
	}{
		{name: "projects", method: "GET", path: "/api/vault/projects", wantStatus: 200, wantBody: `"task_count":3`},
		{name: "tasks", method: "GET", path: "/api/vault/projects/demo/tasks", wantStatus: 200, wantBody: `"id":"002"`},
		{name: "views list", method: "GET", path: "/api/vault/projects/demo/views", wantStatus: 200, wantBody: `"tasks-overview"`},
		{name: "view", method: "GET", path: "/api/vault/projects/demo/views/tasks-blocked", wantStatus: 200, wantBody: `"view_id":"tasks-blocked"`},
		{name: "design summary", method: "GET", path: "/api/vault/projects/demo/design", wantStatus: 200, wantBody: `"valid":true`},
		{name: "design artifact", method: "GET", path: "/api/vault/projects/demo/design/contract/order-api.md", wantStatus: 200, wantBody: "# Contract"},
		{name: "unknown view", method: "GET", path: "/api/vault/projects/demo/views/bogus", wantStatus: 404, wantBody: `"error"`},
		{name: "unknown project", method: "GET", path: "/api/vault/projects/nope/tasks", wantStatus: 404, wantBody: `"error"`},
		// ServeMux cleans ".." before routing (301/307 redirect), so traversal
		// never reaches the handler; safeBasename defends the service layer
		// (covered by TestDesignArtifact). Here we only assert it never 200s.
		{name: "traversal artifact", method: "GET", path: "/api/vault/projects/demo/design/contract/../../glossary.md", wantStatusAny: []int{301, 307}, wantBody: ""},
		{name: "post not allowed", method: "POST", path: "/api/vault/projects", wantStatus: 405, wantBody: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if tt.wantStatus != 0 && rec.Code != tt.wantStatus {
				t.Fatalf("%s %s status=%d, want %d (body=%s)", tt.method, tt.path, rec.Code, tt.wantStatus, rec.Body.String())
			}
			if len(tt.wantStatusAny) > 0 {
				ok := false
				for _, c := range tt.wantStatusAny {
					if rec.Code == c {
						ok = true
						break
					}
				}
				if !ok {
					t.Fatalf("%s %s status=%d, want one of %v (body=%s)", tt.method, tt.path, rec.Code, tt.wantStatusAny, rec.Body.String())
				}
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("%s body missing %q: %s", tt.path, tt.wantBody, rec.Body.String())
			}
		})
	}
}

func TestHTTPProjectsJSONShape(t *testing.T) {
	s := New(newTestVault(t))
	req := httptest.NewRequest("GET", "/api/vault/projects", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var projects []ProjectDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(projects) != 2 || projects[0].Name != "demo" {
		t.Fatalf("projects shape wrong: %+v", projects)
	}
}

func TestHTTPTaskUpdate(t *testing.T) {
	s := New(newTestVault(t))
	h := s.Handler()

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/api/vault/projects/demo/tasks/001", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Successful whitelisted write.
	rec := patch(`{"expected_generation":1,"updates":{"priority":"P0"}}`)
	if rec.Code != 200 {
		t.Fatalf("writable update status=%d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	// Stale generation → 409.
	rec = patch(`{"expected_generation":999,"updates":{"priority":"P0"}}`)
	if rec.Code != 409 {
		t.Fatalf("stale update status=%d, want 409", rec.Code)
	}
	// System-owned field → 403.
	rec = patch(`{"expected_generation":1,"updates":{"status":"done"}}`)
	if rec.Code != 403 {
		t.Fatalf("system field update status=%d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	// Unknown task → 404.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/vault/projects/demo/tasks/999", strings.NewReader(`{"expected_generation":1,"updates":{"priority":"P1"}}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unknown task status=%d, want 404", rec.Code)
	}
}

func TestHTTPAgentsProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Agents-Finished", "3")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"sessionId":"s1","phase":"refining","task":"TASK-001","taskStatus":"refining","status":"working","elapsed":12}]`))
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	s := NewWithAgentServer(newTestVault(t), addr)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/vault/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agents proxy status=%d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Agents-Finished"); got != "3" {
		t.Fatalf("x-agents-finished=%q, want 3", got)
	}
	if !strings.Contains(rec.Body.String(), `"sessionId":"s1"`) {
		t.Fatalf("agents body missing session data: %s", rec.Body.String())
	}

	// Without an agent-server address the endpoint is intentionally unavailable.
	rec = httptest.NewRecorder()
	New(newTestVault(t)).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/vault/agents", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("agents without server status=%d, want 503", rec.Code)
	}
}

func TestHTTPKBSearch(t *testing.T) {
	lastRerank := true
	fake := func(query string, limit int, rerank bool) ([]knowledge.SearchResult, error) {
		lastRerank = rerank
		if query == "boom" {
			return nil, errors.New("kb store broken")
		}
		return []knowledge.SearchResult{
			{Path: "core/go/connect-rpc.md", Title: "Go Connect RPC", Summary: "连接复用", Score: 0.91},
		}, nil
	}
	h := NewWithAgentServer(newTestVault(t), "").WithKBSearch(fake).Handler()

	// 正常命中：JSON 形状与 `otg kb search --json` 一致。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/kb/search?q=connect+rpc&limit=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("kb search status=%d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !lastRerank {
		t.Fatalf("default kb search rerank=false, want rerank=true")
	}
	if !strings.Contains(rec.Body.String(), `"path":"core/go/connect-rpc.md"`) {
		t.Fatalf("kb search body missing hit: %s", rec.Body.String())
	}

	// rerank=false is the fast hybrid-only path used by interactive precompute.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/kb/search?q=connect+rpc&limit=3&rerank=false", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("kb search no-rerank status=%d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if lastRerank {
		t.Fatalf("rerank=false passed as rerank=true")
	}

	// 缺 q → 400；非法 limit → 400；limit 上限 20 截断。
	for _, path := range []string{"/api/kb/search", "/api/kb/search?q=x&limit=abc", "/api/kb/search?q=x&limit=0"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", path, rec.Code)
		}
	}

	// 后端错误 → 500（客户端据此回退 spawn）。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/kb/search?q=boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("boom status=%d, want 500", rec.Code)
	}

	// 未接线（kbSearch nil）→ 501（客户端据此回退 spawn）。
	rec = httptest.NewRecorder()
	NewWithAgentServer(newTestVault(t), "").Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/kb/search?q=x", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unwired status=%d, want 501", rec.Code)
	}
}
