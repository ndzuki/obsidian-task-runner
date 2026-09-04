package vaultweb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// writableFields is the closed whitelist of frontmatter fields the web layer
// may write. Only Human-owned fields and Shared human-gate fields are writable;
// every System-owned field (status, phase_error*, merge_status, generation,
// attempt_id, plan_version, target_branch, pr_url, checkpoint_commit, ...) is
// rejected — the daemon state machine owns those (architecture doc §3.7).
var writableFields = map[string]bool{
	// Human-owned.
	"priority":      true,
	"assignee":      true,
	"title":         true,
	"off_peak_only": true,
	"auto_approve":  true,
	"auto_merge":    true,
	// Shared human-gate decisions.
	"plan_approved":   true,
	"merge_approved":  true,
	"resume_approved": true,
	"close_approved":  true,
	"adr_approved":    true,
}

// ErrNotWritable is returned when a requested field is System-owned or unknown.
var ErrNotWritable = errors.New("field not writable via web")

// taskPathFor resolves a task id to its absolute path inside a project. The id
// is a lookup key only — the path comes from a directory listing, never from
// string concatenation with client input.
func (s *Service) taskPathFor(p projectDirEntry, taskID string) (string, error) {
	tasksDir := filepath.Join(s.projectDir(p), "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return "", fmt.Errorf("read Tasks dir: %w", err)
	}
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
		if perr == nil && fm != nil && fm.ID == taskID {
			return path, nil
		}
	}
	return "", fmt.Errorf("task %q not found", taskID)
}

// UpdateTask applies a whitelisted field update behind generation fencing.
// Every key must be in writableFields; the write is rejected as a stale
// generation (task.ErrStaleGeneration) when expectedGeneration does not match
// the on-disk generation, so a stale browser tab can never overwrite a newer
// state.
func (s *Service) UpdateTask(project, taskID string, req TaskUpdateRequest) (*TaskDTO, error) {
	for k := range req.Updates {
		if !writableFields[k] {
			return nil, fmt.Errorf("%w: %q", ErrNotWritable, k)
		}
	}
	if len(req.Updates) == 0 {
		return nil, fmt.Errorf("no updates provided")
	}
	p, err := s.resolveProjectDir(project)
	if err != nil {
		return nil, err
	}
	taskPath, err := s.taskPathFor(p, taskID)
	if err != nil {
		return nil, err
	}
	store := task.TaskStore{}
	if err := store.Apply(taskPath, req.ExpectedGeneration, func(_ *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		return req.Updates, nil
	}); err != nil {
		return nil, err
	}
	// Re-read the updated task for the response.
	tasks, err := s.tasksFor(p)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].ID == taskID {
			return &tasks[i], nil
		}
	}
	return nil, fmt.Errorf("task %q not found after update", taskID)
}
