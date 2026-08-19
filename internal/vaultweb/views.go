package vaultweb

import (
	"strings"
)

// viewFunc projects one whitelisted view from a project's tasks/design library.
type viewFunc func(p projectDirEntry, s *Service) (ViewDTO, error)

// viewRegistry is the closed whitelist of view ids the dashboard may request.
// It is intentionally not a Dataview/DataviewJS runtime: each id is a fixed
// projection, so the web layer can never execute arbitrary queries or read
// arbitrary files.
var viewRegistry = map[string]viewFunc{
	"tasks-overview":        tasksOverviewView,
	"tasks-blocked":         tasksBlockedView,
	"tasks-running":         tasksRunningView,
	"design-library-status": designStatusView,
}

func tasksOverviewView(p projectDirEntry, s *Service) (ViewDTO, error) {
	tasks, err := s.tasksFor(p)
	if err != nil {
		return ViewDTO{}, err
	}
	rows := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, map[string]any{
			"id":           t.ID,
			"title":        t.Title,
			"status":       t.Status,
			"priority":     t.Priority,
			"plan_version": t.PlanVersion,
			"generation":   t.Generation,
		})
	}
	return ViewDTO{
		ViewID:  "tasks-overview",
		Project: p.name,
		Columns: []Column{
			{Key: "id", Label: "Task"},
			{Key: "title", Label: "Title"},
			{Key: "status", Label: "Status"},
			{Key: "priority", Label: "Priority"},
			{Key: "plan_version", Label: "Plan"},
			{Key: "generation", Label: "Gen"},
		},
		Rows: rows,
	}, nil
}

func tasksBlockedView(p projectDirEntry, s *Service) (ViewDTO, error) {
	tasks, err := s.tasksFor(p)
	if err != nil {
		return ViewDTO{}, err
	}
	var rows []map[string]any
	for _, t := range tasks {
		if t.Status != "blocked" {
			continue
		}
		rows = append(rows, map[string]any{
			"id":               t.ID,
			"title":            t.Title,
			"priority":         t.Priority,
			"blocked_by":       joinOrDash(t.BlockedBy),
			"phase_error_code": t.PhaseErrorCode,
		})
	}
	return ViewDTO{
		ViewID:  "tasks-blocked",
		Project: p.name,
		Columns: []Column{
			{Key: "id", Label: "Task"},
			{Key: "title", Label: "Title"},
			{Key: "priority", Label: "Priority"},
			{Key: "blocked_by", Label: "Blocked By"},
			{Key: "phase_error_code", Label: "Error"},
		},
		Rows: rows,
	}, nil
}

func tasksRunningView(p projectDirEntry, s *Service) (ViewDTO, error) {
	tasks, err := s.tasksFor(p)
	if err != nil {
		return ViewDTO{}, err
	}
	running := map[string]bool{"refining": true, "planning": true, "implementing": true, "review": true, "conflict": true}
	var rows []map[string]any
	for _, t := range tasks {
		if !running[t.Status] {
			continue
		}
		rows = append(rows, map[string]any{
			"id":           t.ID,
			"title":        t.Title,
			"status":       t.Status,
			"assignee":     t.Assignee,
			"plan_version": t.PlanVersion,
		})
	}
	return ViewDTO{
		ViewID:  "tasks-running",
		Project: p.name,
		Columns: []Column{
			{Key: "id", Label: "Task"},
			{Key: "title", Label: "Title"},
			{Key: "status", Label: "Status"},
			{Key: "assignee", Label: "Assignee"},
			{Key: "plan_version", Label: "Plan"},
		},
		Rows: rows,
	}, nil
}

func designStatusView(p projectDirEntry, s *Service) (ViewDTO, error) {
	sum, err := s.DesignSummary(p.name)
	if err != nil {
		return ViewDTO{}, err
	}
	return ViewDTO{
		ViewID:  "design-library-status",
		Project: p.name,
		Columns: []Column{
			{Key: "metric", Label: "Metric"},
			{Key: "value", Label: "Value"},
		},
		Rows: []map[string]any{
			{"metric": "revision", "value": sum.Revision},
			{"metric": "valid", "value": sum.Valid},
			{"metric": "contracts", "value": len(sum.Contracts)},
			{"metric": "decisions", "value": len(sum.Decisions)},
			{"metric": "waves", "value": len(sum.Waves)},
			{"metric": "glossary", "value": sum.HasGlossary},
		},
	}, nil
}

func joinOrDash(items []string) string {
	return strings.Join(items, ", ")
}
