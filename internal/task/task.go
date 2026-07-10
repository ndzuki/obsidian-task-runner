// Package task provides task discovery and readiness analysis.
package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ReadyTask is the NDJSON output format for find-ready.
type ReadyTask struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Project        string `json:"project"`
	NewProject     bool   `json:"new_project"`
	Priority       string `json:"priority"`
	FilePath       string `json:"file_path"`
	FileName       string `json:"file_name"`
	Status         string `json:"status"`
	PlanApproved   bool   `json:"plan_approved"`
	MergeApproved  bool   `json:"merge_approved"`
	ReqDoc         string `json:"req_doc"`
	Template       string `json:"template"`
	Assignee       string `json:"assignee"`
	AutoApprove    bool   `json:"auto_approve"`
	PendingReq     bool   `json:"pending_req"`
	OffPeakOnly    bool   `json:"off_peak_only"`
}

// priorityOrder maps P0-P4 to sortable int.
func priorityOrder(p string) int {
	switch p {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	case "P4":
		return 4
	default:
		return 2
	}
}

// IsValidAssignee returns true for supported assignees.
func IsValidAssignee(a string) bool {
	switch a {
	case "deepseek", "gpt":
		return true
	}
	return false
}

// isEmptyList returns true if the value is nil or an empty slice.
func isEmptyList(v interface{}) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case []interface{}:
		return len(val) == 0
	case []string:
		return len(val) == 0
	}
	return false
}

// IsAutoUnblockable checks if a blocked task can be auto-promoted to ready.
func IsAutoUnblockable(fm *yamlfrontmatter.Frontmatter) bool {
	if fm.Status != "blocked" {
		return false
	}
	if fm.Project == "" {
		return false
	}
	if !IsValidAssignee(fm.Assignee) {
		return false
	}
	if !isEmptyList(fm.BlockedBy) {
		return false
	}
	return true
}

// IsReady checks if a task should be picked up by the daemon.
func IsReady(fm *yamlfrontmatter.Frontmatter) bool {
	if fm.Assignee == "" {
		return false
	}
	if IsAutoUnblockable(fm) {
		return true
	}
	switch fm.Status {
	case "ready":
		return true
	case "plan-review":
		return fm.PlanApproved
	case "review", "conflict":
		return fm.MergeApproved
	}
	if fm.PendingReq {
		return true
	}
	return false
}

// IsOffPeak returns true during Beijing off-peak hours (cheaper DeepSeek pricing).
// Peak: 09:00-12:00 and 14:00-18:00 CST (UTC+8).
func IsOffPeak() bool {
	cst := time.FixedZone("CST", 8*3600)
	now := time.Now().In(cst)
	h := now.Hour()
	if 9 <= h && h < 12 {
		return false
	}
	if 14 <= h && h < 18 {
		return false
	}
	return true
}

// FindReadyTasks scans the vault's Tasks/ directory and returns ready tasks.
func FindReadyTasks(vaultPath string) ([]ReadyTask, error) {
	tasksDir := filepath.Join(vaultPath, "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Tasks dir: %w", err)
	}

	var ready []ReadyTask
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		filePath := filepath.Join(tasksDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			continue
		}
		if !IsReady(fm) {
			continue
		}
		if fm.Project == "" {
			continue // skip templates
		}

		// Defer off-peak-only Round 2 tasks during peak
		if fm.Status == "plan-review" && fm.PlanApproved && fm.OffPeakOnly && !IsOffPeak() {
			now := time.Now().In(time.FixedZone("CST", 8*3600))
			fmt.Fprintf(os.Stderr, "  %s (%s): Round 2 delayed by off_peak_only (CST %s, peak)\n",
				fm.ID, entry.Name(), now.Format("15:04"))
			continue
		}

		ready = append(ready, ReadyTask{
			ID:            fm.ID,
			Title:         fm.Title,
			Project:       fm.Project,
			NewProject:    fm.NewProject,
			Priority:      fm.Priority,
			FilePath:      filePath,
			FileName:      entry.Name(),
			Status:        fm.Status,
			PlanApproved:  fm.PlanApproved,
			MergeApproved: fm.MergeApproved,
			ReqDoc:        fm.ReqDoc,
			Template:      fm.Template,
			Assignee:      fm.Assignee,
			AutoApprove:   fm.AutoApprove,
			PendingReq:    fm.PendingReq,
			OffPeakOnly:   fm.OffPeakOnly,
		})
	}

	sort.Slice(ready, func(i, j int) bool {
		pi := priorityOrder(ready[i].Priority)
		pj := priorityOrder(ready[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return ready[i].ID < ready[j].ID
	})

	return ready, nil
}

// PrintReadyTasks outputs tasks as NDJSON to stdout.
func PrintReadyTasks(tasks []ReadyTask) {
	for _, t := range tasks {
		data, _ := json.Marshal(t)
		fmt.Println(string(data))
	}
}
