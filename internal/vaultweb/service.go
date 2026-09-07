// Package vaultweb serves whitelisted read-only DTOs over an Obsidian vault for
// the DSH Web dashboard (docs/archive/refactor-architecture.md §3.7 / Phase 4). It is
// the Go control plane's read model: parsing and path safety live here, so the
// DSH web plugin renders structured data instead of re-reading the vault.
//
// Safety contract: every path is derived from directory listings; a
// client-supplied string is only ever a lookup key (project name, view id,
// artifact basename), never joined onto the vault root unchecked.
package vaultweb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/designlib"
	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// Service serves read-only dashboard DTOs over a vault directory.
type Service struct {
	vault string
	// agentServerAddr is the optional dsh agent-server host:port used to proxy
	// live agent-monitor data (/agents) into the same-origin dashboard API.
	agentServerAddr string
	// kbSearch answers in-process KB retrieval for /api/kb/search (B2):
	// consumers (agent-server / kb-preflight) call it instead of spawning
	// `otg kb search`, falling back to spawn when nil / endpoint absent.
	// The bool is `rerank`: false skips the cross-encoder (hybrid-only
	// fast path used by interactive precompute).
	kbSearch func(query string, limit int, rerank bool) ([]knowledge.SearchResult, error)
}

// New builds a Service rooted at the vault path. The agent-server proxy is
// disabled; use NewWithAgentServer when live agent monitoring is desired.
func New(vault string) *Service { return &Service{vault: vault} }

// NewWithAgentServer builds a Service that can also proxy live agent data from
// a dsh agent-server (e.g. "127.0.0.1:8799").
func NewWithAgentServer(vault, agentServerAddr string) *Service {
	return &Service{vault: vault, agentServerAddr: agentServerAddr}
}

// WithKBSearch wires the in-process KB retrieval backend (B2); nil disables
// the /api/kb/search endpoint (clients fall back to spawning otg). The
// backend receives a `rerank` flag so clients can request the hybrid-only
// fast path (cross-encoder is expensive and unnecessary for precompute).
func (s *Service) WithKBSearch(fn func(query string, limit int, rerank bool) ([]knowledge.SearchResult, error)) *Service {
	s.kbSearch = fn
	return s
}

// projectDirEntry is a directory directly under Projects/. id is the numeric
// prefix ("001"), name the suffix ("obsidian-task-runner"), dirName the full
// directory name.
type projectDirEntry struct {
	id      string
	name    string
	dirName string
}

func splitProjectName(dirName string) (id, bare string) {
	if idx := strings.IndexByte(dirName, '-'); idx > 0 {
		return dirName[:idx], dirName[idx+1:]
	}
	return "", dirName
}

// projects lists Projects/ subdirectories. Never traverses outside the vault.
func (s *Service) projects() ([]projectDirEntry, error) {
	entries, err := os.ReadDir(filepath.Join(s.vault, "Projects"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Projects dir: %w", err)
	}
	var out []projectDirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, bare := splitProjectName(e.Name())
		out = append(out, projectDirEntry{id: id, name: bare, dirName: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dirName < out[j].dirName })
	return out, nil
}

// resolveProjectDir matches a client-supplied name to a Projects/ subdirectory
// (full dir name or bare suffix). Returns an error when no directory matches —
// the name is never used as a raw path component.
func (s *Service) resolveProjectDir(name string) (projectDirEntry, error) {
	projects, err := s.projects()
	if err != nil {
		return projectDirEntry{}, err
	}
	for _, p := range projects {
		if name == p.dirName || name == p.name {
			return p, nil
		}
	}
	return projectDirEntry{}, fmt.Errorf("project %q not found", name)
}

func (s *Service) projectDir(p projectDirEntry) string {
	return filepath.Join(s.vault, "Projects", p.dirName)
}

// tasksFor lists every task in a project regardless of status (dashboard shows
// blocked/done too, unlike task.Index.Scan which returns ready tasks only).
func (s *Service) tasksFor(p projectDirEntry) ([]TaskDTO, error) {
	tasksDir := filepath.Join(s.projectDir(p), "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Tasks dir: %w", err)
	}
	out := make([]TaskDTO, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		path := filepath.Join(tasksDir, e.Name())
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		fm, perr := yamlfrontmatter.Parse(data)
		if perr != nil || fm == nil {
			continue
		}
		out = append(out, TaskDTO{
			ID:             fm.ID,
			Title:          fm.Title,
			Project:        fm.Project,
			Status:         fm.Status,
			Priority:       fm.Priority,
			Assignee:       fm.Assignee,
			PlanVersion:    fm.PlanVersion,
			Generation:     fm.Generation,
			Stage:          fm.Stage,
			BlockedBy:      fm.BlockedBy,
			PhaseErrorCode: fm.PhaseErrorCode,
			Updated:        fm.Updated,
			ReqDoc:         fm.ReqDoc,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Projects returns the navigation-sidebar summary for every project.
func (s *Service) Projects() ([]ProjectDTO, error) {
	projects, err := s.projects()
	if err != nil {
		return nil, err
	}
	out := make([]ProjectDTO, 0, len(projects))
	for _, p := range projects {
		tasks, terr := s.tasksFor(p)
		if terr != nil {
			return nil, terr
		}
		dto := ProjectDTO{
			ID:        p.id,
			Name:      p.name,
			DirName:   p.dirName,
			TaskCount: len(tasks),
			ByStatus:  map[string]int{},
		}
		for _, t := range tasks {
			dto.ByStatus[t.Status]++
		}
		out = append(out, dto)
	}
	return out, nil
}

// Tasks returns every task in a project (any status).
func (s *Service) Tasks(project string) ([]TaskDTO, error) {
	p, err := s.resolveProjectDir(project)
	if err != nil {
		return nil, err
	}
	return s.tasksFor(p)
}

// Views returns the whitelisted view ids, sorted.
func (s *Service) Views() []string {
	out := make([]string, 0, len(viewRegistry))
	for id := range viewRegistry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// View projects a whitelisted view. Unknown view ids are rejected; the id is a
// registry lookup key, never an expression or a path.
func (s *Service) View(project, viewID string) (ViewDTO, error) {
	fn, ok := viewRegistry[viewID]
	if !ok {
		return ViewDTO{}, fmt.Errorf("unknown view %q", viewID)
	}
	p, err := s.resolveProjectDir(project)
	if err != nil {
		return ViewDTO{}, err
	}
	return fn(p, s)
}

// DesignSummary returns the design-library inventory with its validation
// verdict. An empty library is a valid empty summary (Valid=false), not an
// error, so the dashboard can render a "run a design session" state.
func (s *Service) DesignSummary(project string) (DesignSummaryDTO, error) {
	p, err := s.resolveProjectDir(project)
	if err != nil {
		return DesignSummaryDTO{}, err
	}
	layout := designlib.ForProject(s.projectDir(p))
	sum, err := layout.ReadSummary()
	if err != nil {
		if errors.Is(err, designlib.ErrEmpty) {
			return DesignSummaryDTO{Valid: false}, nil
		}
		return DesignSummaryDTO{}, err
	}
	dto := DesignSummaryDTO{
		Revision:    sum.Revision,
		Contracts:   sum.Contracts,
		Decisions:   sum.Decisions,
		Waves:       sum.Waves,
		HasGlossary: sum.HasGlossary,
	}
	dto.Valid = layout.Validate() == nil
	return dto, nil
}

// DesignArtifact returns one design-library document. kind is a whitelist
// (contract/decision/wave/glossary); name must be a plain basename inside the
// artifact's directory — traversal is impossible.
func (s *Service) DesignArtifact(project, kind, name string) (string, error) {
	p, err := s.resolveProjectDir(project)
	if err != nil {
		return "", err
	}
	layout := designlib.ForProject(s.projectDir(p))
	var path string
	switch kind {
	case "contract":
		path = filepath.Join(layout.ContractsPath(), name)
	case "decision":
		path = filepath.Join(layout.DecisionsPath(), name)
	case "wave":
		path = filepath.Join(layout.WavesPath(), name)
	case "glossary":
		path = layout.GlossaryPath()
	default:
		return "", fmt.Errorf("unknown artifact kind %q", kind)
	}
	if kind != "glossary" && !safeBasename(name) {
		return "", fmt.Errorf("invalid artifact name %q", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read artifact %s/%s: %w", kind, name, err)
	}
	return string(data), nil
}

// safeBasename reports whether name is a single path component that cannot
// escape its directory (no separators, not "." or "..").
func safeBasename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return filepath.Base(name) == name && !strings.ContainsAny(name, `/\`)
}
