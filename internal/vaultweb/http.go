package vaultweb

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

//go:embed dashboard.html
var dashboardFS embed.FS

// Handler returns the vault dashboard HTTP API plus the zero-build SPA. All
// data routes are GET; writes arrive in Phase 4c behind TaskStore.Apply
// fencing. The SPA is served at / and /vault from the embedded single file.
func (s *Service) Handler() http.Handler {
	dashboard, err := fs.ReadFile(dashboardFS, "dashboard.html")
	if err != nil {
		panic("vaultweb: embedded dashboard.html missing: " + err.Error())
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/vault/projects", s.handleProjects)
	mux.HandleFunc("GET /api/vault/projects/{project}/tasks", s.handleTasks)
	mux.HandleFunc("GET /api/vault/projects/{project}/views", s.handleViews)
	mux.HandleFunc("GET /api/vault/projects/{project}/views/{view}", s.handleView)
	mux.HandleFunc("GET /api/vault/projects/{project}/design", s.handleDesignSummary)
	mux.HandleFunc("GET /api/vault/projects/{project}/design/{kind}/{name}", s.handleDesignArtifact)
	mux.HandleFunc("PATCH /api/vault/projects/{project}/tasks/{taskID}", s.handleTaskUpdate)
	mux.HandleFunc("GET /api/vault/agents", s.handleAgents)
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) { serveDashboard(w, dashboard) })
	mux.HandleFunc("GET /vault", func(w http.ResponseWriter, _ *http.Request) { serveDashboard(w, dashboard) })
	return cors(mux)
}

// cors 为回环监控页开放跨源读写。
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func serveDashboard(w http.ResponseWriter, dashboard []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(dashboard)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Service) handleProjects(w http.ResponseWriter, _ *http.Request) {
	projects, err := s.Projects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

// handleAgents proxies the live agent-server /agents summary so the dsh-web
// dashboard can observe agents from the same origin. If no agent-server is
// configured, the endpoint reports 503 instead of leaking a cross-origin call.
func (s *Service) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.agentServerAddr == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("agent-server not configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.agentServerAddr+"/agents", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	if v := resp.Header.Get("X-Agents-Finished"); v != "" {
		w.Header().Set("X-Agents-Finished", v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Service) handleTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Tasks(r.PathValue("project"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Service) handleViews(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Views())
}

func (s *Service) handleView(w http.ResponseWriter, r *http.Request) {
	view, err := s.View(r.PathValue("project"), r.PathValue("view"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Service) handleDesignSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.DesignSummary(r.PathValue("project"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Service) handleDesignArtifact(w http.ResponseWriter, r *http.Request) {
	content, err := s.DesignArtifact(r.PathValue("project"), r.PathValue("kind"), r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

func (s *Service) handleTaskUpdate(w http.ResponseWriter, r *http.Request) {
	var req TaskUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.UpdateTask(r.PathValue("project"), r.PathValue("taskID"), req)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotWritable):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, task.ErrStaleGeneration):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusNotFound, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
