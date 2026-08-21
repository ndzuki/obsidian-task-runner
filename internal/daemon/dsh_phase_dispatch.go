package daemon

import (
	"fmt"
	"os"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// runDSHPhaseDispatch executes one phase on the DSH backend and routes the
// stable outcome into the shared success/failure paths. It returns handled=true
// when the phase was fully processed (success, failure, or interruption), so
// the caller records the processed task and continues — the inline OMP path is
// untouched while cfg.Executor stays "omp".
//
// Phase 5 seam (docs/phase5-executor-migration.md §5.3): the dsh branch is a
// deliberate mirror of the OMP success tail (validate→compact→round2 notify→
// clear) without executor-specific logging, PID files, empty-stop watch, or the
// daemon-side fallback loop — those live in the DSH fallback plugin.
func (r *Runner) runDSHPhaseDispatch(t task.ReadyTask, taskPath, repoDir, phase, model, skillPrompt, logPath string) bool {
	timeout := r.cfg.PhaseTimeout(phase)
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	spec := PhaseSpec{
		Phase:           phase,
		Model:           model,
		ReasoningEffort: ompPhaseThinking(phase),
		SkillPrompt:     skillPrompt,
		Timeout:         timeout,
		WorkingDir:      repoDir,
	}
	snap := TaskSnapshot{TaskID: t.ID, TaskPath: taskPath, Project: t.Project, RepoDir: repoDir}

	result, outcome, code, reason := r.runDSHPhase(r.daemonCtx, spec, snap)

	switch outcome {
	case OutcomeSuccess:
		if err := r.validatePhaseDocuments(taskPath, repoDir, t.ID, t.Title, t.Status, phase, logPath); err != nil {
			r.logger.Printf("task %s: phase documents invalid after %s: %v", t.ID, phase, err)
			r.handlePhaseFailure(taskPath, t.ID, t.Title, t.Status, phase, ErrDocumentInvalid, err.Error(), logPath)
			notify.SendTaskAction(t.ID, t.Title, "📄", "阶段产物文档损坏",
				"任务文档或阶段产物损坏且无法自动修复，任务已阻断；修复后 resume_approved=true 恢复", r.cfg.Notifications.Desktop)
			return true
		}
		r.logger.Printf("task %s: completed via DSH", t.ID)
		// 会话完成：清空持久化的 resume token（无未完成会话可恢复）。
		r.clearExecutorSessionID(taskPath)
		r.clearQuotaBackoff(taskPath)
		r.compactPlanHistory(taskPath, phase)
		if phase == "round2" {
			r.recordRound2Completion(taskPath, t.ID)
			_, stalled := r.round2StallActive(taskPath)
			if !stalled {
				if _, statErr := os.Stat(taskPath); statErr == nil {
					notify.StatusNotify(taskPath, r.cfg.Notifications.Desktop)
				}
			}
		} else if _, statErr := os.Stat(taskPath); statErr == nil {
			notify.StatusNotify(taskPath, r.cfg.Notifications.Desktop)
		}
		r.clearPhaseRetry(taskPath, phase)
		r.clearPhaseError(taskPath, t.ID)
		r.clearMergeRepairBudget(taskPath, phase)
		return true

	case OutcomeInterrupted:
		r.logger.Printf("task %s: DSH interrupted by daemon shutdown, status=%s kept for auto-resume", t.ID, t.Status)
		updates := map[string]interface{}{
			"phase_error_code": string(ErrPhaseInterrupted),
			"phase_error":      "daemon 重启中断，等待自动恢复",
			"phase_log":        logPath,
		}
		// 持久化 resume token：daemon 重启后可用它恢复未完成的会话。
		if result != nil && result.ResumeToken != "" {
			updates["executor_session_id"] = result.ResumeToken
		}
		if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
			r.logger.Printf("task %s: record interruption: %v", t.ID, err)
		}
		return true

	default:
		r.logger.Printf("task %s: DSH failed (%s): %s", t.ID, code, reason)
		r.handlePhaseFailure(taskPath, t.ID, t.Title, t.Status, phase, code, reason, logPath)
		desc := fmt.Sprintf("%s 阶段失败（%s）：%s", phase, code, reason)
		if isFreeModelRoute(model) && (code == ErrModelQuotaExhausted || code == ErrModelFailed) {
			desc = fmt.Sprintf("%s 阶段失败：免费模型渠道不可用或额度耗尽（deepseek_magic / openai gpt-5.6）——请把任务文档 assignee 改为 ds-official 后 resume_approved=true 恢复", phase)
		}
		r.notifyFailure(taskPath, t.ID, t.Title, "💥", "DSH 阶段失败", desc, failNotifyReason)
		return true
	}
}

// clearExecutorSessionID clears the persisted resume token after a phase
// completes, so a later scan does not mistake the finished session for an
// in-flight one to resume.
func (r *Runner) clearExecutorSessionID(taskPath string) {
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"executor_session_id": ""}); err != nil {
		r.logger.Printf("clear executor_session_id: %v", err)
	}
}

// clearQuotaBackoff resets the free-tier quota backoff after a phase succeeds,
// so the next failure starts a fresh 2m→4m→… ladder.
func (r *Runner) clearQuotaBackoff(taskPath string) {
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"quota_backoff_level": 0,
		"quota_backoff_until": "",
	}); err != nil {
		r.logger.Printf("clear quota backoff: %v", err)
	}
}
