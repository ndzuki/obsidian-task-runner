// Package task provides task discovery and readiness analysis.
package task

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ReadyTask is the NDJSON output format for find-ready.
type ReadyTask struct {
	ID                       string `json:"id"`
	Title                    string `json:"title"`
	Project                  string `json:"project"`
	NewProject               bool   `json:"new_project"`
	Priority                 string `json:"priority"`
	Created                  string `json:"created,omitempty"`
	FilePath                 string `json:"file_path"`
	FileName                 string `json:"file_name"`
	Status                   string `json:"status"`
	PlanApproved             bool   `json:"plan_approved"`
	MergeApproved            bool   `json:"merge_approved"`
	CloseApproved            bool   `json:"close_approved,omitempty"`
	ReqDoc                   string `json:"req_doc"`
	Template                 string `json:"template"`
	Assignee                 string `json:"assignee"`
	AutoApprove              bool   `json:"auto_approve"`
	AutoMerge                bool   `json:"auto_merge"`
	PendingReq               bool   `json:"pending_req"`
	PhaseErrorCode           string `json:"phase_error_code,omitempty"`
	OffPeakOnly              bool   `json:"off_peak_only"`
	TargetBranch             string `json:"target_branch"`
	GrillDone                bool   `json:"grill_done"`
	GrillPrevStatus          string `json:"grill_prev_status,omitempty"`
	GrillResolution          string `json:"grill_resolution,omitempty"`
	GrillContext             string `json:"grill_context,omitempty"`
	GrillContinue            bool   `json:"grill_continue,omitempty"`
	GrillParked              bool   `json:"grill_parked,omitempty"`
	PriorityAssessmentStatus string `json:"priority_assessment_status,omitempty"`
	PlanVersion              int    `json:"plan_version,omitempty"`
	ReworkResolution         string `json:"rework_resolution,omitempty"`
	ReviewFeedback           string `json:"review_feedback,omitempty"`
	ClosureReason            string `json:"closure_reason,omitempty"`
	GrillStartedAt           string `json:"grill_started_at,omitempty"`
	GrillHeartbeatAt         string `json:"grill_heartbeat_at,omitempty"`
	GrillTimeoutMinutes      int    `json:"grill_timeout_minutes,omitempty"`
	RefineReqHash            string `json:"refine_req_hash,omitempty"`
	PlanReqHash              string `json:"plan_req_hash,omitempty"`
	Maturity                 string `json:"maturity,omitempty"`
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

// fmLookup resolves a task file path to a cached frontmatter without disk IO.
// nil means every read goes to disk (used by CLI and tests).
type fmLookup func(path string) (*yamlfrontmatter.Frontmatter, bool)

// AreBlockersDone checks whether every task referenced in blockedBy has
// status "done" by scanning the specified project's Tasks/ directory.
// References use format:
//
//	"TASK-010" — within current project
//	"project-key:TASK-010" — cross-project lookup via vault-map scan
func AreBlockersDone(vaultPath, projectName string, blockedBy []string) bool {
	return areBlockersDoneWith(vaultPath, projectName, blockedBy, nil)
}

func areBlockersDoneWith(vaultPath, projectName string, blockedBy []string, lookup fmLookup) bool {
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
	checkDirDepsWith(checkDir, projectName, remaining, lookup)
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
		checkDirDepsWith(filepath.Join(projectsDir, proj.Name(), "Tasks"), proj.Name(), remaining, lookup)
	}
	return len(remaining) == 0
}

func checkDirDeps(tasksDir, projName string, remaining map[string]bool) {
	checkDirDepsWith(tasksDir, projName, remaining, nil)
}

func checkDirDepsWith(tasksDir, projName string, remaining map[string]bool, lookup fmLookup) {
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
			if !strings.HasPrefix(name, "TASK-"+numID+"-") && name != "TASK-"+numID+".md" {
				continue
			}
			filePath := filepath.Join(tasksDir, name)
			fm, err := frontmatterFor(filePath, lookup)
			if err != nil || fm == nil {
				continue
			}
			if parts[0] == projName {
				if blockerSatisfiedWith(fm, tasksDir, projName, lookup) {
					delete(remaining, key)
				}
				break
			}
			if !dirMatches && fm.Project == parts[0] {
				if blockerSatisfiedWith(fm, tasksDir, projName, lookup) {
					delete(remaining, key)
				}
				break
			}
		}
	}
}

func blockerSatisfied(fm *yamlfrontmatter.Frontmatter, tasksDir, projectName string) bool {
	return blockerSatisfiedWith(fm, tasksDir, projectName, nil)
}

func blockerSatisfiedWith(fm *yamlfrontmatter.Frontmatter, tasksDir, projectName string, lookup fmLookup) bool {
	if fm.Status == "done" {
		return true
	}
	if fm.Status != "closed" {
		return false
	}
	switch fm.ClosureReason {
	case "already_implemented":
		return true
	case "duplicate":
		if fm.ReplacementTask == "" {
			return false
		}
		remaining := map[string]bool{projectName + ":" + strings.TrimPrefix(fm.ReplacementTask, "TASK-"): true}
		checkDirDepsWith(tasksDir, projectName, remaining, lookup)
		return len(remaining) == 0
	default:
		return false
	}
}

// IsAutoUnblockable checks if a blocked task can be auto-promoted to ready.
// vaultPath is required to resolve blocked_by references against actual task status.
func IsAutoUnblockable(fm *yamlfrontmatter.Frontmatter, vaultPath string) bool {
	return isAutoUnblockableWith(fm, vaultPath, nil)
}

func isAutoUnblockableWith(fm *yamlfrontmatter.Frontmatter, vaultPath string, lookup fmLookup) bool {
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
		if !areBlockersDoneWith(vaultPath, fm.Project, fm.BlockedBy, lookup) {
			return false
		}
	}
	if fm.BlockedPhase != "" && !fm.ResumeApproved {
		// API_KEY_UNAVAILABLE tasks are auto-resumed by the daemon's key probe
		// (no manual resume_approved needed) — admit them for processing.
		// PHASE_INTERRUPTED tasks are self-healed the same way on restart
		// (docs/workflow.md 3.2 promises automatic re-scheduling, no manual
		// resume); legacy daemons wrote interrupted phases as blocked
		// (observed: TASK-015).
		switch fm.PhaseErrorCode {
		case PhaseErrorCodeAPIKeyUnavailable, PhaseErrorCodeInterrupted:
			return true
		}
		return false
	}
	return true
}

// IsReady checks if a task should be picked up by the daemon.
// PhaseErrorCodeAPIKeyUnavailable marks tasks blocked because the provider
// API key could not be resolved (e.g. KeePassXC/secret service locked). The
// daemon probes key availability each scan and auto-resumes these tasks
// without manual resume_approved. Single source shared with internal/daemon.
const PhaseErrorCodeAPIKeyUnavailable = "API_KEY_UNAVAILABLE"

// PhaseErrorCodeInterrupted marks tasks blocked by a daemon shutdown that
// interrupted the running phase. The daemon self-heals these on restart
// (docs/workflow.md 3.2) without manual resume_approved — legacy daemons
// wrote interrupted phases as blocked instead of keeping the phase status.
const PhaseErrorCodeInterrupted = "PHASE_INTERRUPTED"

// DoneReopensMerge reports whether a done task still owes its PR merge:
// merge_status is set but not "merged" (merge interrupted/conflicted), or a
// PR URL is recorded but the merge never completed. A cleanly merged task
// (merge_status=merged), a done task with no PR at all (legacy tasks that
// never ran the PR flow), and a bare target_branch without a PR record stay
// terminal — reopening the latter would spin legacy done tasks through the
// merge flow every scan (observed: 003/004/005/010 re-entering planning).
func DoneReopensMerge(fm *yamlfrontmatter.Frontmatter) bool {
	if fm == nil || fm.MergeStatus == "merged" {
		return false
	}
	return fm.MergeStatus != "" || fm.PRURL != ""
}

// vaultPath is used to resolve blocked_by dependencies.
func IsReady(fm *yamlfrontmatter.Frontmatter, vaultPath string) bool {
	return isReadyWith(fm, vaultPath, nil)
}

func isReadyWith(fm *yamlfrontmatter.Frontmatter, vaultPath string, lookup fmLookup) bool {
	if fm == nil || fm.Assignee == "" || fm.Status == "closed" {
		return false
	}
	if isAutoUnblockableWith(fm, vaultPath, lookup) {
		return true
	}
	if fm.ReworkResolution != "" {
		switch fm.ReworkResolution {
		case "resume":
			return fm.Status == "review"
		case "replan":
			return fm.Status == "plan-review" || fm.Status == "review"
		case "close":
			return fm.CloseApproved && (fm.Status == "plan-review" || fm.Status == "review")
		}
	}
	switch fm.Status {
	case "ready", "refining", "planning":
		// Dependency gate applies to scheduling phases too: a task whose
		// blocked_by upstreams are not done must not be dispatched into
		// refining/planning — otherwise unmerged upstreams drive endless
		// no-op replans (TASK-066 regression: 15 plan versions while
		// upstream TASK-067/065 were unmerged).
		if !isEmptyList(fm.BlockedBy) {
			if !areBlockersDoneWith(vaultPath, fm.Project, fm.BlockedBy, lookup) {
				return false
			}
		}
		return true
	case "needs-grilling":
		// Grilling clarifies requirements and does not touch code; it is
		// allowed while upstreams are pending so the discussion is not
		// blocked on delivery order.
		return true
	case "needs-refining":
		// Legacy status from an earlier daemon version. Ready so the scan
		// picks the task up and nextLocalTransition migrates it to
		// needs-grilling (the current name), which then creates the Grilling
		// tab and starts requirement alignment.
		return true
	case "implementing":
		return !fm.OffPeakOnly || OffPeakFn()
	case "plan-review":
		return fm.PlanApproved && (!fm.OffPeakOnly || OffPeakFn())
	case "review":
		// Fresh review (Round 2 completed, no failure) with auto_merge is
		// ready so the daemon auto-approves and merges without a manual gate.
		return fm.PendingReq || fm.MergeApproved || (fm.AutoMerge && fm.PhaseErrorCode == "")
	case "conflict":
		return fm.PendingReq || fm.MergeApproved
	case "done":
		// Done with an unmerged PR (merge_status != merged + PR/branch
		// exists) reopens the merge flow: the task previously stalled in
		// done while its PR sat CONFLICTING for weeks. nextLocalTransition
		// converts it to review; here it just becomes schedulable.
		return fm.PendingReq || DoneReopensMerge(fm)
	case "closed", "wayfinder":
		return false
	default:
		return false
	}
}

// OffPeakFn is the off-peak evaluator used by readiness checks. The daemon
// sets it from vault-map off_peak_windows/off_peak_timezone at startup;
// defaults to the legacy fixed Beijing window so tests and standalone uses
// keep working.
var OffPeakFn = IsOffPeak

// IsOffPeak returns true during Beijing off-peak hours (cheaper DeepSeek pricing).
// Peak: 09:00-12:00 and 14:00-18:00 CST (UTC+8).
func IsOffPeak() bool {
	return IsOffPeakWith(nil, "")
}

// IsOffPeakWith evaluates off-peak from configured windows in the configured
// timezone; nil/empty falls back to the legacy fixed Beijing window
// (00-09, 12-14, 18-24 CST).
func IsOffPeakWith(windows []config.TimeWindow, tz string) bool {
	if len(windows) == 0 {
		windows = []config.TimeWindow{
			{Start: "00:00", End: "09:00"},
			{Start: "12:00", End: "14:00"},
			{Start: "18:00", End: "24:00"},
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	hm := now.Hour()*60 + now.Minute()
	for _, w := range windows {
		start, ok1 := parseHM(w.Start)
		end, ok2 := parseHM(w.End)
		if !ok1 || !ok2 {
			continue
		}
		if start < end && start <= hm && hm < end {
			return true
		}
		if start > end && (hm >= start || hm < end) { // crosses midnight
			return true
		}
	}
	return false
}

// parseHM parses "HH:MM" into minutes since midnight.
func parseHM(s string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// FindReadyTaskForFile reads and checks a single task file for readiness.
// Returns a populated ReadyTask if the task is ready, or nil, nil if the file
// is not a ready task (no frontmatter, not ready, or no project). Returns an
// error only for I/O failures reading the file.
func FindReadyTaskForFile(vaultPath, changedFile string) (*ReadyTask, error) {
	data, err := readFileWithRetry(changedFile)
	if err != nil {
		return nil, fmt.Errorf("read task file %s: %w", changedFile, err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return nil, nil
	}
	if !IsReady(fm, vaultPath) {
		return nil, nil
	}
	if fm.Project == "" {
		return nil, nil
	}
	rt := &ReadyTask{
		ID:                       fm.ID,
		Title:                    fm.Title,
		Project:                  fm.Project,
		NewProject:               fm.NewProject,
		Priority:                 fm.Priority,
		FilePath:                 changedFile,
		FileName:                 filepath.Base(changedFile),
		Status:                   fm.Status,
		PlanApproved:             fm.PlanApproved,
		MergeApproved:            fm.MergeApproved,
		ReqDoc:                   fm.ReqDoc,
		Template:                 fm.Template,
		Assignee:                 fm.Assignee,
		AutoApprove:              fm.AutoApprove,
		AutoMerge:                fm.AutoMerge,
		PendingReq:               fm.PendingReq,
		PhaseErrorCode:           fm.PhaseErrorCode,
		OffPeakOnly:              fm.OffPeakOnly,
		TargetBranch:             fm.TargetBranch,
		GrillDone:                fm.GrillDone,
		GrillPrevStatus:          fm.GrillPrevStatus,
		GrillResolution:          fm.GrillResolution,
		GrillContext:             fm.GrillContext,
		GrillContinue:            fm.GrillContinue,
		GrillParked:              fm.GrillParked,
		PlanVersion:              fm.PlanVersion,
		PriorityAssessmentStatus: fm.PriorityAssessmentStatus,
		GrillHeartbeatAt:         fm.GrillHeartbeatAt,
		GrillTimeoutMinutes:      fm.GrillTimeoutMinutes,
		RefineReqHash:            fm.RefineReqHash,
		PlanReqHash:              fm.PlanReqHash,
		Maturity:                 fm.Maturity,
	}
	return rt, nil
}

// FindReadyTasks scans vault's Projects/*/Tasks/ directories and returns ready tasks.
// FindReadyTasks scans vault's Projects/*/Tasks/ directories and returns ready
// tasks. It delegates to a fresh Index, so CLI/tests get the same behavior as
// the daemon's persistent-index scans.
func FindReadyTasks(vaultPath string) ([]ReadyTask, error) {
	return NewIndex().Scan(vaultPath)
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
