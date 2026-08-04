package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/priority"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// priorityAssessmentBatchLimit bounds lightweight assessment work per scan
// independently from the implementation concurrency limit.
const priorityAssessmentBatchLimit = 2

// errAPIKeyUnavailable is returned when the key probe fails so the caller can
// skip the candidate without counting it as processed or consuming attempts.
// Public wrappers (runPriorityAssessment) propagate it — callers should treat
// it as "skip, retry next scan", not as a failure.
var errAPIKeyUnavailable = errors.New("api key unavailable")

func (r *Runner) processPriorityAssessments(ctx context.Context) int {
	// Shutdown: skip entirely. Claiming a task (writing
	// priority_assessment_status: running) and recording a failure here would
	// race the interrupted phase's PHASE_INTERRUPTED write-back on the same
	// frontmatter and could overwrite it.
	if ctx.Err() != nil {
		return 0
	}
	pending, err := task.FindPriorityTasks(r.cfg.ObsidianVault, time.Now())
	if err != nil {
		r.logger.Printf("priority scan: %v", err)
		return 0
	}
	if len(pending) > priorityAssessmentBatchLimit {
		pending = pending[:priorityAssessmentBatchLimit]
	}

	var processed int
	for _, candidate := range pending {
		if err := r.runPriorityAssessmentContext(ctx, candidate); err != nil {
			if errors.Is(err, errAPIKeyUnavailable) {
				// Key not reachable (e.g. KeePassXC locked): retry next scan.
				continue
			}
			r.logger.Printf("task %s: priority assessment: %v", candidate.ID, err)
			continue
		}
		processed++
	}
	return processed
}

func (r *Runner) runPriorityAssessment(candidate task.PriorityTask) error {
	return r.runPriorityAssessmentContext(context.Background(), candidate)
}

func (r *Runner) runPriorityAssessmentContext(parent context.Context, candidate task.PriorityTask) error {
	// API key unavailable (e.g. KeePassXC locked): skip without claiming or
	// burning attempts — the scan loop retries the assessment next round.
	if !apiKeyAvailable() {
		return errAPIKeyUnavailable
	}
	attempts := candidate.Attempts + 1
	started := time.Now().Format(time.RFC3339)
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
		"priority_assessment_status":     "running",
		"priority_assessment_attempts":   attempts,
		"priority_assessment_started_at": started,
	}); err != nil {
		return fmt.Errorf("claim assessment: %w", err)
	}

	reqPath := candidate.ReqDoc
	if !filepath.IsAbs(reqPath) {
		reqPath = filepath.Join(r.cfg.ObsidianVault, reqPath)
	}
	timeout := 5 * time.Minute
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.cfg.OMPCmd, "--model", r.cfg.Model("default"), "--auto-approve", "-p", "/obsidian-task-runner-priority "+reqPath)
	// Graceful timeout/shutdown: SIGTERM first, hard-kill after WaitDelay.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	output, runErr := cmd.Output()
	if runErr != nil {
		// Interrupted by daemon shutdown (our own SIGTERM via ctx): do not
		// record a failure — the phase-interrupt contract applies. Reset the
		// claim so the next scan retries the assessment.
		if ctx.Err() != nil {
			_ = yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"priority_assessment_status": "pending",
			})
			return nil
		}
		return r.recordPriorityFailure(candidate, attempts, fmt.Sprintf("model failed: %v", runErr))
	}
	result, decodeErr := priority.Decode(output)
	if decodeErr != nil {
		return r.recordPriorityFailure(candidate, attempts, decodeErr.Error())
	}
	return yamlfrontmatter.Update(candidate.FilePath, priorityUpdates(result, "completed"))
}

func (r *Runner) recordPriorityFailure(candidate task.PriorityTask, attempts int, reason string) error {
	if attempts < 2 {
		if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
			"priority_assessment_status": "pending",
			"phase_error_code":           "PRIORITY_ASSESSMENT_FAILED",
			"phase_error":                reason,
		}); err != nil {
			return fmt.Errorf("record priority retry: %w", err)
		}
		return fmt.Errorf("priority assessment attempt %d failed: %s", attempts, reason)
	}
	result := priority.Fallback(reason)
	updates := priorityUpdates(result, "failed")
	updates["phase_error_code"] = "PRIORITY_ASSESSMENT_FAILED"
	updates["phase_error"] = reason
	if err := yamlfrontmatter.Update(candidate.FilePath, updates); err != nil {
		return fmt.Errorf("record priority fallback: %w", err)
	}
	return nil
}

func priorityUpdates(result priority.Result, status string) map[string]interface{} {
	return map[string]interface{}{
		"priority":                       result.Priority,
		"priority_assessment_status":     status,
		"priority_assessed_at":           time.Now().Format(time.RFC3339),
		"priority_assessed_value":        result.Priority,
		"priority_impact":                result.Impact,
		"priority_urgency":               result.Urgency,
		"priority_workaround":            result.Workaround,
		"priority_score":                 result.Score,
		"priority_confidence":            result.Confidence,
		"priority_reason":                result.Reason,
		"priority_recommendation":        result.Recommendation,
		"priority_assessment_started_at": "",
		"phase_error_code":               "",
		"phase_error":                    "",
	}
}
