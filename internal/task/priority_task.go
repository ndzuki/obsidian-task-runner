package task

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

type PriorityTask struct {
	ID        string
	Title     string
	Project   string
	ReqDoc    string
	FilePath  string
	Attempts  int
	Takeover  bool
}

func FindPriorityTasks(vaultPath string, now time.Time) ([]PriorityTask, error) {
	projectsDir := filepath.Join(vaultPath, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Projects dir: %w", err)
	}

	var pending []PriorityTask
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
			if parseErr != nil || fm == nil || fm.Priority != "" {
				continue
			}

			takeover := false
			switch fm.PriorityAssessmentStatus {
			case "pending":
			case "running":
				started, parseTimeErr := time.Parse(time.RFC3339, fm.PriorityAssessmentStartedAt)
				if parseTimeErr != nil || now.Sub(started) < 10*time.Minute {
					continue
				}
				takeover = true
			default:
				continue
			}
			pending = append(pending, PriorityTask{
				ID: fm.ID, Title: fm.Title, Project: fm.Project, ReqDoc: fm.ReqDoc,
				FilePath: path, Attempts: fm.PriorityAssessmentAttempts, Takeover: takeover,
			})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].FilePath < pending[j].FilePath })
	return pending, nil
}
