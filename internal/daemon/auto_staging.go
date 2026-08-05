package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/stageplan"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// parseLeadingNumber reports whether s is a non-negative integer (used to
// strip project directory numeric prefixes).
func parseLeadingNumber(s string) (int, error) {
	return strconv.Atoi(s)
}

// processAutoStaging deterministically phases unstaged in-flight tasks
// directly in the daemon — no PM session (the consolidate path took hours
// of LLM rounds for release-manager and was unreliable; this runs in
// milliseconds and is idempotent). Runs before PM consolidation on every
// scan, so the PM input shrinks to genuine disputes only: tasks staged here
// disappear from FindUnstagedTasks, and the cooldown-free deterministic
// write makes new requirements self-stage on the next scan.
//
// Phase numbering continues from an existing Stage-Plan (append semantics);
// nothing is rewritten for already-staged tasks.
func (r *Runner) processAutoStaging() int {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	staged := 0
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		tasksDir := filepath.Join(projDir, "Tasks")
		if _, err := os.Stat(tasksDir); err != nil {
			continue
		}
		project := projectNameFromTasks(tasksDir)
		if project == "" {
			continue
		}
		res, err := stageplan.Apply(tasksDir, filepath.Join(projDir, "Notes"), project,
			stageplan.Options{MinTasksPerPhase: r.cfg.StageMinPerPhase, MaxPhases: r.cfg.StageMaxPhases},
			stageplan.ApplyOptions{})
		if err != nil {
			r.logger.Printf("auto-staging %s: %v", projectEntry.Name(), err)
			continue
		}
		if len(res.Phases) == 0 {
			continue // idempotent: everything already staged
		}
		r.logger.Printf("auto-staging %s: staged %d task(s) into %d phase(s)%s",
			project, res.Staged, len(res.Phases), map[bool]string{true: " (Stage-Plan.md created)", false: ""}[res.Created])
		staged += res.Staged
	}
	return staged
}

// projectNameFromTasks extracts the project name from the first task's
// frontmatter (frontmatter `project` is authoritative; falls back to the
// directory name with the numeric prefix stripped, e.g. "001-release-manager"
// → "release-manager").
func projectNameFromTasks(tasksDir string) string {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			continue
		}
		if fm.Project != "" {
			return fm.Project
		}
	}
	// Fallback: strip the numeric prefix from the directory name.
	name := filepath.Base(filepath.Dir(tasksDir))
	if idx := strings.Index(name, "-"); idx > 0 {
		if _, err := parseLeadingNumber(name[:idx]); err == nil {
			return name[idx+1:]
		}
	}
	return name
}
