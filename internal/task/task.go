// Package task provides task discovery and readiness analysis.
package task

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ReadyTask is the NDJSON output format for find-ready.
type ReadyTask struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Project         string `json:"project"`
	NewProject      bool   `json:"new_project"`
	Priority        string `json:"priority"`
	FilePath        string `json:"file_path"`
	FileName        string `json:"file_name"`
	Status          string `json:"status"`
	PlanApproved    bool   `json:"plan_approved"`
	MergeApproved   bool   `json:"merge_approved"`
	ReqDoc          string `json:"req_doc"`
	Template        string `json:"template"`
	Assignee        string `json:"assignee"`
	AutoApprove     bool   `json:"auto_approve"`
	PendingReq      bool   `json:"pending_req"`
	OffPeakOnly     bool   `json:"off_peak_only"`
	TargetBranch    string `json:"target_branch"`
	GrillDone       bool   `json:"grill_done"`
	GrillPrevStatus string `json:"grill_prev_status,omitempty"`
	GrillResolution string `json:"grill_resolution,omitempty"`
	GrillContext    string `json:"grill_context,omitempty"`
	PlanVersion     int    `json:"plan_version,omitempty"`
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
// IsValidAssignee returns true for any non-empty assignee.
// The actual model is resolved at execution time from vault-map.json's models table.
func IsValidAssignee(a string) bool {
	return a != ""
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

// AreBlockersDone checks whether every task referenced in blockedBy has
// status "done" by scanning the specified project's Tasks/ directory.
// References use format:
//
//	"TASK-010" — within current project
//	"project-key:TASK-010" — cross-project lookup via vault-map scan
func AreBlockersDone(vaultPath, projectName string, blockedBy []string) bool {
	if len(blockedBy) == 0 {
		return true
	}
	deps := make([]struct{ proj, id string }, 0, len(blockedBy))
	for _, raw := range blockedBy {
		if idx := strings.Index(raw, ":"); idx > 0 {
			deps = append(deps, struct{ proj, id string }{proj: raw[:idx], id: strings.TrimPrefix(raw[idx+1:], "TASK-")})
		} else {
			deps = append(deps, struct{ proj, id string }{proj: projectName, id: strings.TrimPrefix(raw, "TASK-")})
		}
	}
	remaining := make(map[string]bool, len(blockedBy))
	for _, d := range deps {
		remaining[d.proj+":"+d.id] = true
	}
	checkDir := filepath.Join(vaultPath, "Projects", projectName, "Tasks")
	checkDirDeps(checkDir, projectName, remaining)
	if len(remaining) == 0 {
		return true
	}
	projectsDir := filepath.Join(vaultPath, "Projects")
	projEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		return false
	}
	for _, proj := range projEntries {
		if !proj.IsDir() || len(remaining) == 0 {
			continue
		}
		checkDirDeps(filepath.Join(projectsDir, proj.Name(), "Tasks"), proj.Name(), remaining)
	}
	return len(remaining) == 0
}

func checkDirDeps(tasksDir, projName string, remaining map[string]bool) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return
	}
	// Precompute whether any remaining key matches this directory's name directly.
	dirMatches := false
	for key := range remaining {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[0] == projName {
			dirMatches = true
			break
		}
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		name := entry.Name()
		for key := range remaining {
			parts := strings.SplitN(key, ":", 2)
			if len(parts) != 2 {
				continue
			}
			numID := parts[1]
			if !strings.HasPrefix(name, "TASK-"+numID+"-") {
				continue
			}
			// Direct match: directory name equals project key.
			if parts[0] == projName {
				filePath := filepath.Join(tasksDir, name)
				data, err := readFileWithRetry(filePath)
				if err != nil {
					continue
				}
				fm, err := yamlfrontmatter.Parse(data)
				if err != nil || fm == nil {
					continue
				}
				if fm.Status == "done" {
					delete(remaining, key)
				}
				break
			}
			// Cross-project fallback: read the task's project field to match.
			if !dirMatches {
				filePath := filepath.Join(tasksDir, name)
				data, err := readFileWithRetry(filePath)
				if err != nil {
					continue
				}
				fm, err := yamlfrontmatter.Parse(data)
				if err != nil || fm == nil {
					continue
				}
				if fm.Project == parts[0] && fm.Status == "done" {
					delete(remaining, key)
					break
				}
			}
			break
		}
	}
}

// IsAutoUnblockable checks if a blocked task can be auto-promoted to ready.
// vaultPath is required to resolve blocked_by references against actual task status.
func IsAutoUnblockable(fm *yamlfrontmatter.Frontmatter, vaultPath string) bool {
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
		if !AreBlockersDone(vaultPath, fm.Project, fm.BlockedBy) {
			return false
		}
	}
	if fm.BlockedPhase != "" && !fm.ResumeApproved {
		return false
	}
	return true
}

// IsReady checks if a task should be picked up by the daemon.
// vaultPath is used to resolve blocked_by dependencies.
func IsReady(fm *yamlfrontmatter.Frontmatter, vaultPath string) bool {
	if fm.Assignee == "" {
		return false
	}
	if IsAutoUnblockable(fm, vaultPath) {
		return true
	}
	switch fm.Status {
	case "ready", "needs-grilling", "refining", "planning":
		return true
	case "implementing":
		if fm.OffPeakOnly && !IsOffPeak() {
			return false
		}
		return true
	case "plan-review":
		if !fm.PlanApproved {
			return false
		}
		if fm.OffPeakOnly && !IsOffPeak() {
			return false
		}
		return true
	case "review", "conflict":
		if fm.PendingReq {
			return true // force refining
		}
		return fm.MergeApproved
	case "done":
		return fm.PendingReq // re-plan via refining
	}
	return fm.PendingReq
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
// FindReadyTasks scans vault's Projects/*/Tasks/ directories and returns ready tasks.
func FindReadyTasks(vaultPath string) ([]ReadyTask, error) {
	projectsDir := filepath.Join(vaultPath, "Projects")
	projEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Projects dir: %w", err)
	}

	var ready []ReadyTask
	for _, proj := range projEntries {
		if !proj.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			filePath := filepath.Join(tasksDir, entry.Name())
			data, err := readFileWithRetry(filePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: read error: %v\n", entry.Name(), err)
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				fmt.Fprintf(os.Stderr, "  %s: parse error: %v\n", entry.Name(), err)
				continue
			}
			if !IsReady(fm, vaultPath) {
				continue
			}
			if fm.Project == "" {
				continue
			}
			if fm.Status == "plan-review" && fm.PlanApproved && fm.OffPeakOnly && !IsOffPeak() {
				now := time.Now().In(time.FixedZone("CST", 8*3600))
				fmt.Fprintf(os.Stderr, "  %s (%s): Round 2 delayed by off_peak_only (CST %s, peak)\n",
					fm.ID, entry.Name(), now.Format("15:04"))
				continue
			}
			ready = append(ready, ReadyTask{
				ID: fm.ID, Title: fm.Title, Project: fm.Project,
				NewProject: fm.NewProject, Priority: fm.Priority,
				FilePath: filePath, FileName: entry.Name(),
				Status: fm.Status, PlanApproved: fm.PlanApproved,
				MergeApproved: fm.MergeApproved, ReqDoc: fm.ReqDoc,
				Template: fm.Template, Assignee: fm.Assignee,
				AutoApprove: fm.AutoApprove, PendingReq: fm.PendingReq,
				OffPeakOnly: fm.OffPeakOnly, TargetBranch: fm.TargetBranch,
				GrillDone: fm.GrillDone, GrillPrevStatus: fm.GrillPrevStatus,
				GrillResolution: fm.GrillResolution, GrillContext: fm.GrillContext,
				PlanVersion: fm.PlanVersion,
			})
		}
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

// DebugReadyTasks logs all task files and why they are not ready.
func DebugReadyTasks(vaultPath string, logger *log.Logger) {
	projectsDir := filepath.Join(vaultPath, "Projects")
	projEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, proj := range projEntries {
		if !proj.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			filePath := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(filePath)
			if err != nil {
				logger.Printf("debug: %s: read error: %v", filePath, err)
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				logger.Printf("debug: %s: parse error: %v", filePath, err)
				continue
			}
			isReady := IsReady(fm, vaultPath)
			logger.Printf("debug: %s: id=%s status=%s assignee=%q pending_req=%v plan_approved=%v merge_approved=%v isReady=%v project=%q",
				entry.Name(), fm.ID, fm.Status, fm.Assignee, fm.PendingReq, fm.PlanApproved, fm.MergeApproved, isReady, fm.Project)
		}
	}
}

// readFileWithRetry reads a file with retries to handle cloud-sync filesystems
// where WRITE events fire before the file is fully written.
func readFileWithRetry(path string) ([]byte, error) {
	const maxRetries = 5
	const retryDelay = 200 * time.Millisecond
	var lastErr error
	for i := range maxRetries {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if _, parseErr := yamlfrontmatter.Parse(data); parseErr == nil {
			return data, nil
		} else {
			lastErr = parseErr
		}
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return os.ReadFile(path)
}
