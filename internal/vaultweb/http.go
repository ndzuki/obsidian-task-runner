package vaultweb

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"

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
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) { serveDashboard(w, dashboard) })
	mux.HandleFunc("GET /vault", func(w http.ResponseWriter, _ *http.Request) { serveDashboard(w, dashboard) })
	return mux
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
