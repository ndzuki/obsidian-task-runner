package vaultweb

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPReadOnlyEndpoints(t *testing.T) {
	s := New(newTestVault(t))
	h := s.Handler()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
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
		{name: "traversal artifact", method: "GET", path: "/api/vault/projects/demo/design/contract/../../glossary.md", wantStatus: 307, wantBody: ""},
		{name: "post not allowed", method: "POST", path: "/api/vault/projects", wantStatus: 405, wantBody: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("%s %s status=%d, want %d (body=%s)", tt.method, tt.path, rec.Code, tt.wantStatus, rec.Body.String())
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
