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
	ID            string `json:"id"`
	Title         string `json:"title"`
	Project       string `json:"project"`
	NewProject    bool   `json:"new_project"`
	Priority      string `json:"priority"`
	FilePath      string `json:"file_path"`
	FileName      string `json:"file_name"`
	Status        string `json:"status"`
	PlanApproved  bool   `json:"plan_approved"`
	MergeApproved bool   `json:"merge_approved"`
	ReqDoc        string `json:"req_doc"`
	Template      string `json:"template"`
	Assignee      string `json:"assignee"`
	AutoApprove   bool   `json:"auto_approve"`
	PendingReq    bool   `json:"pending_req"`
	OffPeakOnly   bool   `json:"off_peak_only"`
	TargetBranch  string `json:"target_branch"`
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
// status "done" by scanning the vault's Projects/*/Tasks/ directories.
func AreBlockersDone(vaultPath string, blockedBy []string) bool {
	if len(blockedBy) == 0 {
		return true
	}
	projectsDir := filepath.Join(vaultPath, "Projects")
	projEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		return false
	}
	remaining := make(map[string]bool, len(blockedBy))
	for _, id := range blockedBy {
		remaining[id] = true
	}
	for _, proj := range projEntries {
		if !proj.IsDir() || len(remaining) == 0 {
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
			// Extract task ID from filename like TASK-039-something.md
			name := entry.Name()
			for id := range remaining {
				if strings.Contains(name, id) {
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
						delete(remaining, id)
					}
					break
				}
			}
		}
	}
	return len(remaining) == 0
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
		if !AreBlockersDone(vaultPath, fm.BlockedBy) {
			return false
		}
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
	case "ready", "needs-grilling":
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
	for i := 0; i < maxRetries; i++ {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		// Verify it looks like a valid frontmatter file (starts with ---)
		if len(data) >= 3 && string(data[:3]) == "---" {
			// Verify frontmatter closes
			rest := data[3:]
			endIdx := findFrontmatterEnd(rest)
			if endIdx > 0 {
				return data, nil
			}
		}
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}
	// Last attempt: return raw data even if incomplete
	return os.ReadFile(path)
}

func findFrontmatterEnd(data []byte) int {
	lines := splitLines(data)
	for _, line := range lines {
		if string(line) == "---" {
			return 1
		}
	}
	return 0
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
