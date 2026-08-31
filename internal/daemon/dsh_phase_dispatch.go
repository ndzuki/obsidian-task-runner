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
		TaskStatus:      t.Status,
		Timeout:         timeout,
		WorkingDir:      repoDir,
	}
	// 只读基线审查会话（conventions）在 daemon 层限制工具面，与完成审计
	// 会话（audit）对齐：白名单外（edit/str_replace_editor 等写源码工具）
	// 一律违规。与 audit 的唯一差别是 conventions 允许 write——技能契约的
	// 唯一写入就是审查产物 Notes/PROJECT-CONVENTIONS.md（一次性门禁标记），
	// 该产物必须用 write 落盘；禁止 write 会让门禁永远无法完成
	// （TASK-080 2026-08-31 CONVENTIONS_REVIEW_FAILED：disallowed write）。
	// 审查本身仍是零代码修改。
	if phase == "conventions" {
		spec.ToolPolicy = conventionsToolPolicy
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
		// Refining success re-stamps refine_req_hash with the REQ bytes as
		// they stand AFTER the session's own write-back. The pre-dispatch
		// ensureReqHash stamped the pre-session bytes; a session that rewrites
		// the REQ (decision write-back, structural cleanup) changes the hash,
		// and a stale hash keeps failing the refining early-out
		// (refine_req_hash == current REQ) on every scan — the task then
		// re-runs the maturity gate forever instead of routing to planning
		// (TASK-058 observed after TASK-079 merged). The skill also writes
		// the hash itself, but the daemon must not trust the session here.
		if phase == "refining" {
			r.ensureReqHash(taskPath, t.ReqDoc)
		}
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

	case OutcomeTimedOutActive:
		// 超时窗口耗尽但会话仍活跃（近期有 step/工具事件——真实长任务的
		// Round 2，如 TASK-065 的 dev-up 冒烟）：不 cancel、不计失败、
		// 不转 blocked。保留 resume token，下一轮 scan 继续等待同一会话。
		r.logger.Printf("task %s: DSH %s still running (session active past timeout window) — next scan resumes", t.ID, phase)
		if result != nil && result.ResumeToken != "" {
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"executor_session_id": result.ResumeToken,
			}); err != nil {
				r.logger.Printf("task %s: persist resume token after active timeout: %v", t.ID, err)
			}
		}
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
		// 超时/断连类失败（OutcomeTimedOut/OutcomeFailed 携带 ResumeToken）：
		// 会话可能仍在 agent-server 中运行，持久化 token 让下一轮 scan resume
		// 同一会话。此前该路径丢 token → fresh start → 旧会话继续跑 + 新会话
		// 并行写同一任务文档（监控面板因此出现「多个 agent 都在工作」）。
		if result != nil && result.ResumeToken != "" {
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"executor_session_id": result.ResumeToken,
			}); err != nil {
				r.logger.Printf("task %s: persist resume token after failure: %v", t.ID, err)
			}
		}
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
