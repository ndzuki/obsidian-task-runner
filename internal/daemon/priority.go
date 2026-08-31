package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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

	// 优先级评估统一走 DSH executor（extractJSON 从 headless 输出恢复 strict
	// JSON 后 priority.Decode）。
	return r.runPriorityAssessmentDSH(ctx, candidate, reqPath, attempts)
}

// runPriorityAssessmentDSH executes the priority assessment through the DSH
// phase executor. DSH headless returns the strict JSON contract as free text
// (usually a ```json fenced block), so extractJSON isolates the object before
// priority.Decode. Interruption by daemon shutdown resets the claim for the
// next scan; other failures burn the attempt budget via recordPriorityFailure.
func (r *Runner) runPriorityAssessmentDSH(ctx context.Context, candidate task.PriorityTask, reqPath string, attempts int) error {
	spec := PhaseSpec{
		Phase:           "priority",
		Model:           r.cfg.Model("default"),
		ReasoningEffort: ompPhaseThinking("priority"),
		SkillPrompt:     "/obsidian-task-runner-priority " + reqPath,
		Timeout:         5 * time.Minute,
		WorkingDir:      filepath.Dir(reqPath),
	}
	executor := r.phaseExecutor
	if executor == nil {
		executor = newPhaseExecutor(r.cfg)
		r.phaseExecutor = executor
	}
	// 优先级评估同样写任务文档：派发前 reconcile 上一代 daemon 残留的
	// working 会话（与 audit/merge CI-fix 同一会话残留问题）。
	if err := r.cancelStaleTaskSessions(executor, candidate.ID); err != nil {
		return r.recordPriorityFailure(candidate, attempts, fmt.Sprintf("priority stale-session reconcile: %v", err))
	}
	handle, err := executor.Start(ctx, spec, TaskSnapshot{
		TaskID:   candidate.ID,
		TaskPath: candidate.FilePath,
		Project:  candidate.Project,
	})
	if err != nil {
		return r.recordPriorityFailure(candidate, attempts, fmt.Sprintf("priority start: %v", err))
	}
	result, err := handle.Wait()
	if err != nil {
		return r.recordPriorityFailure(candidate, attempts, err.Error())
	}
	if result == nil || result.Code != OutcomeSuccess {
		if ctx.Err() != nil {
			_ = yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{
				"priority_assessment_status": "pending",
			})
			return nil
		}
		reason := "priority assessment failed"
		if result != nil && result.Error != "" {
			reason = result.Error
		}
		return r.recordPriorityFailure(candidate, attempts, reason)
	}
	jsonBytes, jsonErr := extractJSON(result.Stdout)
	if jsonErr != nil {
		return r.recordPriorityFailure(candidate, attempts, jsonErr.Error())
	}
	parsed, decodeErr := priority.Decode(jsonBytes)
	if decodeErr != nil {
		return r.recordPriorityFailure(candidate, attempts, decodeErr.Error())
	}
	return yamlfrontmatter.Update(candidate.FilePath, priorityUpdates(parsed, "completed"))
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
