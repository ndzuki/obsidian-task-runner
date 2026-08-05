package task

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// GrillingTask is a task parked in needs-grilling, eligible for project-level
// consolidation by the PM coordinator.
type GrillingTask struct {
	ID          string
	Title       string
	Project     string
	ReqDoc      string
	FilePath    string
	GrillParked bool
	GrillRepeat int
	PlanVersion int // high replan count drives single-task consolidation
}

// FindGrillingTasks returns every task currently in needs-grilling status.
// The PM consolidation pass uses this to group tasks by shared req_doc and to
// detect repeat disputes (grill_repeat) that must escalate to a project-level
// decision list instead of re-asking the same per-task questions.
func FindGrillingTasks(vaultPath string) ([]GrillingTask, error) {
	projectsDir := filepath.Join(vaultPath, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Projects dir: %w", err)
	}

	var pending []GrillingTask
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, project.Name(), "Tasks")
		entries, readErr := os.ReadDir(tasksDir)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, readErr := readFileWithRetry(path)
			if readErr != nil {
				continue
			}
			fm, parseErr := yamlfrontmatter.Parse(data)
			if parseErr != nil || fm == nil || fm.Status != "needs-grilling" {
				continue
			}
			pending = append(pending, GrillingTask{
				ID:          fm.ID,
				Title:       fm.Title,
				Project:     fm.Project,
				ReqDoc:      fm.ReqDoc,
				FilePath:    path,
				GrillParked: fm.GrillParked,
				GrillRepeat: fm.GrillRepeat,
				PlanVersion: fm.PlanVersion,
			})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].FilePath < pending[j].FilePath })
	return pending, nil
}
