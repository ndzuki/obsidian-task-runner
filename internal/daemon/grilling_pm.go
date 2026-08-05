package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// grillingConsolidationBatchLimit bounds PM coordinator sessions per scan:
// one heavy cross-task analysis per round, the rest waits for the next scan.
const grillingConsolidationBatchLimit = 1

// grillingDecisionListName is the project-level decision list filename.
const grillingDecisionListName = "Grilling-Decisions.md"

// processGrillingConsolidation dispatches the project-level PM coordinator
// for needs-grilling tasks that share a req_doc or carry repeat disputes.
// Runs after processBatch, once per scan, never blocking dispatched tasks
// (they run in their own goroutines).
//
// Priority order:
//  1. distribute — a project decision list was answered by the user
//     (grill_continue=true) and parked tasks are waiting for the answers.
//  2. consolidate — a group of tasks shares a req_doc with un-parked members,
//     or a single task has a repeat dispute (grill_repeat >= 2).
func (r *Runner) processGrillingConsolidation(ctx context.Context) int {
	if ctx.Err() != nil {
		return 0
	}
	pending, err := task.FindGrillingTasks(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("grilling pm scan: %v", err)
		return 0
	}
	if len(pending) == 0 {
		return 0
	}

	byProject := make(map[string][]task.GrillingTask)
	for _, t := range pending {
		byProject[t.Project] = append(byProject[t.Project], t)
	}

	// Priority 1: distribute answered decision lists.
	for _, project := range sortedProjectKeys(byProject) {
		listPath := grillingDecisionListPath(r.cfg.ObsidianVault, project)
		if listPath == "" || !grillingListAnswered(listPath) {
			continue
		}
		if !hasParked(byProject[project]) {
			continue
		}
		if err := r.runGrillingPM(ctx, "distribute", listPath); err != nil {
			if errors.Is(err, errAPIKeyUnavailable) {
				return 0 // retry next scan
			}
			r.logger.Printf("project %s: grilling pm distribute: %v", project, err)
			continue
		}
		r.logger.Printf("project %s: grilling pm distribute dispatched", project)
		return 1
	}

	// Priority 2: consolidate groups that need cross-task coordination.
	for _, project := range sortedProjectKeys(byProject) {
		group := groupByReqDoc(byProject[project])
		for _, req := range sortedProjectKeys(group) {
			members := group[req]
			if !needsConsolidation(members) {
				continue
			}
			paths := make([]string, 0, len(members))
			for _, m := range members {
				paths = append(paths, m.FilePath)
			}
			if err := r.runGrillingPM(ctx, "consolidate", paths...); err != nil {
				if errors.Is(err, errAPIKeyUnavailable) {
					return 0 // retry next scan
				}
				r.logger.Printf("grilling pm consolidate %s: %v", req, err)
				continue
			}
			r.logger.Printf("grilling pm consolidate dispatched: %s (%d tasks)", req, len(members))
			return 1
		}
	}
	return 0
}

// runGrillingPM invokes one OMP session running the PM coordinator skill.
// The session is synchronous (like priority assessments) so the scan loop
// naturally serializes PM work; dispatched task goroutines keep running.
func (r *Runner) runGrillingPM(ctx context.Context, mode string, args ...string) error {
	if !apiKeyAvailable() {
		return errAPIKeyUnavailable
	}
	timeoutMin := r.cfg.PhaseTimeoutMinutes["refining"]
	if timeoutMin <= 0 {
		timeoutMin = 15
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMin)*time.Minute)
	defer cancel()

	prompt := "/obsidian-task-runner-pm " + mode + " " + strings.Join(args, " ")
	cmd := exec.CommandContext(runCtx, r.cfg.OMPCmd,
		"--model", r.cfg.Model("default"), "--auto-approve", "-p", prompt)
	// Graceful timeout/shutdown: SIGTERM first, hard-kill after WaitDelay.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	output, runErr := cmd.Output()
	if runErr != nil {
		if ctx.Err() != nil {
			// Interrupted by daemon shutdown; retry on next scan.
			r.logger.Printf("grilling pm %s interrupted (shutdown)", mode)
			return nil
		}
		return fmt.Errorf("grilling pm %s: %w (output: %s)", mode, runErr, summarizeOutput(output))
	}
	r.logger.Printf("grilling pm %s ok: %s", mode, summarizeOutput(output))
	return nil
}

// grillingDecisionListPath resolves the project decision list path, or ""
// when the project has no vault directory.
func grillingDecisionListPath(vaultPath, project string) string {
	projDir := resolveVaultProjectDir(vaultPath, project)
	if projDir == "" {
		return ""
	}
	return filepath.Join(projDir, "Notes", grillingDecisionListName)
}

// grillingListAnswered reports whether the decision list frontmatter has
// grill_continue=true (user finished answering).
func grillingListAnswered(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return false
	}
	return fm.GrillContinue
}

// needsConsolidation reports whether a req_doc group requires a PM session:
// a shared req_doc with at least one un-parked member, or a lone task whose
// dispute repeated enough to park. Fully parked groups already have their
// questions in the decision list and must not be re-consolidated.
func needsConsolidation(members []task.GrillingTask) bool {
	if len(members) == 0 {
		return false
	}
	if len(members) == 1 {
		// Single-task consolidation: repeated identical disputes (grill_repeat)
		// OR a requirement that keeps churning replans (plan_version >= 3, e.g.
		// TASK-066's 15 no-op replans) escalate to the project-level decision
		// list so the user answers once instead of per round.
		return !members[0].GrillParked && (members[0].GrillRepeat >= 2 || members[0].PlanVersion >= 3)
	}
	for _, m := range members {
		if !m.GrillParked {
			return true
		}
	}
	return false
}

func hasParked(members []task.GrillingTask) bool {
	for _, m := range members {
		if m.GrillParked {
			return true
		}
	}
	return false
}

func groupByReqDoc(members []task.GrillingTask) map[string][]task.GrillingTask {
	group := make(map[string][]task.GrillingTask)
	for _, m := range members {
		group[m.ReqDoc] = append(group[m.ReqDoc], m)
	}
	return group
}

func sortedProjectKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func summarizeOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if len(trimmed) > 300 {
		return trimmed[:300] + "..."
	}
	return trimmed
}
