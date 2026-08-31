// Package daemon: completion audit — independent verification of round2
// completion before merge authorization.
//
// The implementer's session must not be the sole verifier of its own
// completion ("the bamboozle trap"): an auto_merge review task passes a
// restricted read-only execution session (read/grep/bash only, no write/edit) that
// re-verifies every AC with raw command output before the merge is
// authorized. A fail verdict routes the task back to implementing (bounded
// retries), and the audit log is persisted for the implementer to consume.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// auditSessionTimeoutFallback bounds one audit session when the config value
// is absent (mirrors the phase timeout defaulting pattern).
const auditSessionTimeoutFallback = 15 * time.Minute

// auditRetryCooldown suppresses audit session retries after a process-level
// failure (model outage, session crash) so a failing model does not burn a
// session every scan.
const auditRetryCooldown = 2 * time.Minute

// auditPromptTemplate instructs the read-only verification session. It must
// demand strict JSON and raw evidence per AC — the same contract the priority
// assessment uses, so the daemon can decode the verdict deterministically.
const auditPromptTemplate = `你是独立完成审计员（independent completion auditor）。

任务: %s
项目: %s

背景：实现会话（round2）已声明完成并进入 review。你的职责是独立复核该完成声明——
不信任实现会话内的自我检查；每条验收标准（AC）必须用你亲自运行的命令或亲自读取
的代码收集原始证据。

要求：
1. 读取 TASK 文档 frontmatter 与正文：
   - ` + "`## 验收标准`" + `（AC 列表，权威验收清单）
   - ` + "`plan_files`" + `（变更文件清单——本次实现的全部文件）
   - ` + "`knowledge_refs`" + `（知识引用——计划声称应用的知识文档，逐条核对是否落地）
   - 计划区最新版本（plan 摘要、Step 验收条件）
2. 先锁定变更范围，再逐条 AC 验证（效率约束）：
   - 用只读 git 命令（` + "`git status`" + `、` + "`git diff --name-only`" + `、` + "`git log --oneline`" + `）确认实际变更文件，与 plan_files 交叉核对；禁止任何 git 写操作
   - 测试只跑变更相关包：一次 bash 调用批量执行（如 ` + "`go test -count=1 ./pkg-a/... ./pkg-b/...`" + `，Go 自动并行包），与变更无关的包跳过
   - 全仓 ` + "`go build ./...`" + ` 跑一次保底；全仓 test 仅当 AC 明确要求时运行
3. 逐条 AC 独立验证：运行测试/命令、读取实现代码与产物，收集证据
   （测试输出、命令输出、文件路径+行号；每条 AC 证据一行，精确到文件:行号或命令输出摘要）。
4. 输出 STRICT JSON（一个对象，无 markdown 代码围栏、无额外散文）：
{"verdict":"pass|fail","failure_type":"implementation|requirement","summary":"...","ac_results":[{"ac":"AC-1","pass":true,"evidence":"..."}]}
   - verdict=pass 仅当所有 AC 均 pass 或证据充分
   - verdict=fail 时 failure_type 分类失败性质：
     * "implementation" = 代码/测试缺陷，修复方向明确（默认，拿不准就填这个）
     * "requirement" = AC 本身歧义/矛盾/无法验证，或实现与需求意图冲突需用户裁决
   - verdict=fail 时 summary 必须列出最关键失败点与建议修复方向（供 round2 会话直接使用）
5. 诚实原则：证据不足的 AC 判 fail，禁止推断为 pass。
6. 核验环境清理证据：实现会话必须已清理自建临时资源（k3d 集群、docker 容器/网络、临时凭据、冒烟日志与构建产物）并在阶段记录留下清理快照（` + "`k3d cluster list`" + `/` + "`docker ps`" + ` 输出）。存在自建资源残留、或发现会话曾停用用户常驻服务（kb-reranker、ollama-sycl、桌面进程等）换取门禁通过 → verdict=fail（failure_type=implementation）。
7. 失败场景复核（防"测试全绿、生产爆炸"）：本次实现涉及**可失败路径**时，必须复核负向场景有测试且行为正确，禁止只验证主成功路径：
   - 可失败路径清单：权限/认证拒绝（401/403）、并发/排队/竞态、重试耗尽、吊销/删除/幂等拒绝、非法输入校验、序列化/契约形状（null vs []）、跨引擎方言差异（如 dev SQLite vs prod MySQL 严格模式零日期/TEXT 默认值）、失败被容错掩盖（假成功输出）。
   - 并发/排队/竞态类测试用 ` + "`-count=3`" + `（或更多）重跑以暴露 flaky——只跑一次通过不算证据。
   - 存储/DB 相关 AC：若项目 test/prod 用生产引擎（如 MySQL），需核对 Review Bundle 含**生产引擎实测证据**（如本地 MySQL 8.4 严格模式跑过、sql_mode/字符集断言）；仅 dev SQLite 冒烟不足以判 pass。
   - **恢复路径（状态机/重试类）**：涉及"失败→恢复"的 AC（同步失败后重发、重建/重试、重试耗尽后修复），必须复核有"失败后修正输入重跑能到成功"的证据，确认不被旧失败状态污染（此类污染常在恢复路径才暴露）。
   - **阻塞证据（实现会话转 blocked/grilling 的）**：实现记录里标"疑似环境问题/未处理"的阻塞，必须有 debug 日志/复现输出佐证；无证据的归咎环境判 fail。
   - 上述任一类失败路径缺测试/证据 → verdict=fail（failure_type=implementation，summary 指出缺失的失败场景）。

工具限制：你只有只读/查询工具——read / grep / glob / bash（仅用于运行测试与查询命令）/
skill（加载技能指令）/ todo_write（维护你自己的检查清单，仅会话自身记录，非工作区文件）/
job_output·job_list·job_kill（查看与管理你自己启动的后台任务）/ read_image（查看截图）。
禁止修改任何任务/代码/配置文件、禁止任何 git 写操作、禁止 edit/write/str_replace_editor
等写工具、禁止提交。`

// auditFailureType classifies a failed audit so the daemon can route the
// task to the right recovery path: implementation defects go back to the
// automatic repair loop, requirement disputes go to a human grilling
// decision. Empty means implementation (the conservative default).
type auditFailureType string

const (
	auditFailureImplementation auditFailureType = "implementation"
	auditFailureRequirement    auditFailureType = "requirement"
)

// auditACResult is one AC verification outcome.
type auditACResult struct {
	AC       string `json:"ac"`
	Pass     bool   `json:"pass"`
	Evidence string `json:"evidence"`
}

// auditResult is the strict-JSON contract of the audit session.
type auditResult struct {
	Verdict     string          `json:"verdict"` // "pass" | "fail"
	FailureType string          `json:"failure_type"`
	Summary     string          `json:"summary"`
	ACResults   []auditACResult `json:"ac_results"`
}

// failureType returns the normalized failure classification; unknown or
// empty values default to implementation (repair loop).
func (r *auditResult) failureType() auditFailureType {
	switch auditFailureType(r.FailureType) {
	case auditFailureRequirement:
		return auditFailureRequirement
	default:
		return auditFailureImplementation
	}
}

// parseAuditResult decodes the audit session output, tolerating trailing
// noise (the session may print log lines around the JSON object).
func parseAuditResult(data []byte) (*auditResult, error) {
	trimmed := strings.TrimSpace(string(data))
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return nil, errors.New("no JSON object in audit output")
	}
	var result auditResult
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &result); err != nil {
		return nil, fmt.Errorf("decode audit JSON: %w", err)
	}
	if result.Verdict != "pass" && result.Verdict != "fail" {
		return nil, fmt.Errorf("unexpected audit verdict %q", result.Verdict)
	}
	return &result, nil
}

// auditEnabled reports whether the completion audit gate is configured on.
func (r *Runner) auditEnabled() bool {
	return r.cfg.Audit != nil && r.cfg.Audit.Enabled
}

// auditMaxFixes returns the consecutive-failure budget before a task is
// handed to a grilling decision (resume resets the budget / replan routes
// to refining) instead of looping implementing→review.
func (r *Runner) auditMaxFixes() int {
	if r.cfg.Audit != nil && r.cfg.Audit.MaxFixes > 0 {
		return r.cfg.Audit.MaxFixes
	}
	return 2
}

// auditTimeout returns the per-session timeout from config or the fallback.
func (r *Runner) auditTimeout() time.Duration {
	if r.cfg.Audit != nil && r.cfg.Audit.TimeoutMinutes > 0 {
		return time.Duration(r.cfg.Audit.TimeoutMinutes) * time.Minute
	}
	return auditSessionTimeoutFallback
}

// processReviewAudit enforces the completion audit gate for auto-merge review
// tasks: verification must run in a separate read-only session before merge
// authorization. Returns handled=true when the task was consumed by the audit
// path (session ran and the verdict was routed); the caller must not dispatch
// the task further this scan. Returns handled=false when the task needs no
// audit (disabled, not auto-merge, human-authorized, or already passed) and
// should proceed through normal merge authorization.
func (r *Runner) processReviewAudit(t task.ReadyTask, repoDir string) (bool, error) {
	if !r.auditEnabled() || t.Status != "review" || !t.AutoMerge {
		return false, nil
	}
	if t.MergeApproved || t.AuditStatus == "passed" {
		return false, nil
	}
	// Bounded simultaneous audit sessions (default 1).
	if gate := r.phaseGates["audit"]; gate != nil {
		if ok, _ := gate.tryAcquire(); !ok {
			r.logger.Printf("task %s: audit gate full, deferring to next scan", t.ID)
			return true, nil
		}
		defer gate.release()
	}
	// Process-level failure cooldown: a crashing model must not burn a
	// session every scan.
	if ts, ok := r.auditRetries.Load(t.FilePath); ok {
		if time.Since(ts.(time.Time)) < auditRetryCooldown {
			return true, nil
		}
		r.auditRetries.Delete(t.FilePath)
	}
	// Claim pending before the session: an interrupted run is retried on the
	// next scan (and after daemon restarts) without re-running round2.
	if err := yamlfrontmatter.Update(t.FilePath, map[string]interface{}{"audit_status": "pending"}); err != nil {
		return true, fmt.Errorf("claim audit: %w", err)
	}

	result, output, err := r.runAuditSession(r.daemonCtx, t, repoDir)
	logPath := r.writeAuditLog(t.ID, output)
	if err != nil {
		if errors.Is(err, errAPIKeyUnavailable) {
			// Key unavailable: skip silently, retry next scan.
			return true, nil
		}
		// Session failure or interruption: keep the task in review with
		// audit_status=pending so the next scan retries the audit; never
		// punish the implementation for an environment problem.
		r.auditRetries.Store(t.FilePath, time.Now())
		_ = yamlfrontmatter.Update(t.FilePath, map[string]interface{}{
			"audit_status": "pending",
			"audit_log":    logPath,
		})
		r.logger.Printf("task %s: audit session failed (status kept review): %v", t.ID, err)
		return true, nil
	}

	if result.Verdict == "pass" {
		if err := yamlfrontmatter.Update(t.FilePath, map[string]interface{}{
			"audit_status":     "passed",
			"audit_fail_count": 0,
			"audit_log":        logPath,
		}); err != nil {
			return true, fmt.Errorf("record audit pass: %w", err)
		}
		r.logger.Printf("task %s: audit passed (%d ACs verified), proceeding to merge authorization", t.ID, len(result.ACResults))
		return false, nil
	}

	// Verdict fail. Route by failure classification:
	//  - requirement dispute → human grilling decision. grill_context
	//    carries the audit report; the user's resume restores implementing
	//    with a fresh budget, replan routes to refining.
	//  - implementation defect → automatic repair loop: back to
	//    implementing with the audit report as phase_error (the round2
	//    session consumes it and fixes); when the consecutive-failure
	//    budget is spent, escalate to a grilling decision instead of a
	//    hard block — the user picks a direction, then the chain resumes
	//    automatically. Grilling is the regular human gate; blocking would
	//    strand the task outside the automation chain.
	failCount := t.AuditFailCount + 1
	maxFixes := r.auditMaxFixes()
	reason := "独立审计未通过：" + firstLine(result.Summary)
	grillNeeded := result.failureType() == auditFailureRequirement || failCount >= maxFixes
	if grillNeeded {
		grillContext := buildAuditContext(result)
		if result.failureType() == auditFailureRequirement {
			grillContext = "审计判定为需求争议（实现与验收标准理解不一致或 AC 无法验证）。请决策：\n" +
				"1. resume → 按审计意见修正实现（回 implementing 自动修复）\n" +
				"2. replan → 需求/验收标准需要调整（回 refining）\n\n" + grillContext
		} else {
			grillContext = fmt.Sprintf("实现已连续 %d 次未通过独立审计（预算 %d）。请决策方向：\n"+
				"1. resume → 继续自动修复（重置预算）\n"+
				"2. replan → 需求/计划需要调整\n\n", failCount, maxFixes) + grillContext
		}
		updates := map[string]interface{}{
			"status":            "needs-grilling",
			"grill_prev_status": "implementing",
			"grill_context":     grillContext,
			"grill_done":        false,
			"grill_resolution":  "",
			"grill_parked":      false,
			"phase_error_code":  string(ErrAuditFailed),
			"phase_error":       reason,
			// The grilling decision is the new safety valve; resume starts a
			// fresh budget so a decided direction is not immediately
			// re-throttled by stale counters.
			"audit_fail_count": 0,
			"audit_status":     "",
			"audit_log":        logPath,
			"merge_approved":   false,
		}
		if err := yamlfrontmatter.Update(t.FilePath, updates); err != nil {
			return true, fmt.Errorf("record audit grilling handoff: %w", err)
		}
		notify.SendTaskAction(t.ID, t.Title, "🤔", "审计存疑", reason, r.cfg.Notifications.Desktop)
		r.logger.Printf("task %s: audit failed (type=%s, %d/%d), handed to grilling decision", t.ID, result.failureType(), failCount, maxFixes)
		return true, nil
	}
	updates := map[string]interface{}{
		"status":           "implementing",
		"phase_error_code": string(ErrAuditFailed),
		"phase_error":      reason,
		"audit_fail_count": failCount,
		"audit_status":     "",
		"audit_log":        logPath,
		"merge_approved":   false,
	}
	if err := yamlfrontmatter.Update(t.FilePath, updates); err != nil {
		return true, fmt.Errorf("record audit fail: %w", err)
	}
	r.logger.Printf("task %s: audit failed (type=%s), routed back to implementing for repair (attempt %d/%d)", t.ID, result.failureType(), failCount, maxFixes)
	return true, nil
}

// buildAuditContext compresses a failed audit into the compact multi-line
// form used in grill_context: the summary plus every failed AC with its
// evidence lead.
func buildAuditContext(result *auditResult) string {
	var b strings.Builder
	b.WriteString("审计报告（独立只读会话）：")
	if s := firstLine(result.Summary); s != "" {
		b.WriteString("\n- 摘要: " + s)
	}
	for _, ac := range result.ACResults {
		if !ac.Pass {
			b.WriteString("\n- " + ac.AC + " FAIL: " + firstLine(ac.Evidence))
		}
	}
	return b.String()
}

// runAuditSession executes one restricted read-only verification session and
// returns the decoded verdict plus the raw session output. The session gets
// only read/grep/bash — no write/edit tools — so it cannot modify the
// worktree or plant evidence.
func (r *Runner) runAuditSession(parent context.Context, t task.ReadyTask, repoDir string) (*auditResult, string, error) {
	if !apiKeyAvailable() {
		return nil, "", errAPIKeyUnavailable
	}
	model := r.selectModel(t.Assignee, "audit")
	if r.cfg.Audit != nil && r.cfg.Audit.Model != "" {
		model = r.cfg.Audit.Model
	}
	// Run the audit in the task worktree (same key round2 uses): the
	// implementation lives on the feature branch checked out there, while the
	// main checkout may sit on another branch — verifying repoDir would test
	// the wrong code state (TASK-051/059: merges on the wrong branch corrupt
	// history). Worktree failure degrades to repoDir with a warning; the
	// audit then verifies what it can and the result is still evidence.
	workDir := repoDir
	if t.TargetBranch != "" {
		if wd, wdErr := ensureTaskWorktree(repoDir, taskRunKey(t.FilePath), t.TargetBranch, r.cfg.WorktreeBase); wdErr != nil {
			r.logger.Printf("task %s: audit worktree unavailable (%v), falling back to main checkout", t.ID, wdErr)
		} else {
			workDir = wd
		}
	}
	prompt := fmt.Sprintf(auditPromptTemplate, t.FilePath, t.Project)
	ctx, cancel := context.WithTimeout(parent, r.auditTimeout())
	defer cancel()

	// 审计会话统一走 DSH executor（只读验证 + STRICT-JSON 输出）。
	return r.runAuditSessionDSH(ctx, t, repoDir, workDir, model, prompt)
}

// runAuditSessionDSH executes the read-only verification through the DSH
// phase executor. DSH headless returns the STRICT-JSON verdict as free text;
// parseAuditResult already isolates the object from surrounding prose/fences.
// auditToolPolicy is carried in the spec; exact enforcement on the DSH side is
// verified in the audit smoke pass (docs/phase5-executor-migration.md §5.5).
func (r *Runner) runAuditSessionDSH(ctx context.Context, t task.ReadyTask, repoDir, workDir, model, prompt string) (*auditResult, string, error) {
	spec := PhaseSpec{
		Phase:           "audit",
		Model:           model,
		ReasoningEffort: "low", // off 不是 DSH 声明的级别；low 是最省的显式推理
		SkillPrompt:     prompt,
		TaskStatus:      t.Status,
		ToolPolicy:      auditToolPolicy,
		Timeout:         r.auditTimeout(),
		WorkingDir:      workDir,
	}
	executor := r.phaseExecutor
	if executor == nil {
		executor = newPhaseExecutor(r.cfg)
		r.phaseExecutor = executor
	}
	// 审计会话同样先 reconcile 上一代 daemon 残留的 working 会话：daemon 重启
	// 时旧审计会话仍在外部 agent-server 跑，fresh Start 会形成两个会话并发
	// 审计同一 worktree（会话残留 Bug 报告观测的 3 个并行 CI-fix/审计会话）。
	if err := r.cancelStaleTaskSessions(executor, t.ID); err != nil {
		return nil, "", fmt.Errorf("audit stale-session reconcile: %w", err)
	}
	handle, err := executor.Start(ctx, spec, TaskSnapshot{TaskID: t.ID, TaskPath: t.FilePath, Project: t.Project, RepoDir: repoDir})
	if err != nil {
		return nil, "", fmt.Errorf("audit start: %w", err)
	}
	result, err := handle.Wait()
	if err != nil {
		return nil, "", fmt.Errorf("audit wait: %w", err)
	}
	if result == nil || result.Code != OutcomeSuccess {
		if ctx.Err() != nil {
			return nil, "", fmt.Errorf("audit session interrupted")
		}
		reason := "audit session failed"
		if result != nil && result.Error != "" {
			reason = result.Error
		}
		return nil, "", errors.New(reason)
	}
	parsed, decodeErr := parseAuditResult([]byte(result.Stdout))
	if decodeErr != nil {
		return nil, "", fmt.Errorf("parse audit result: %w", decodeErr)
	}
	return parsed, result.Stdout, nil
}

// writeAuditLog persists the raw audit session output under the task log
// directory so the round2 implementer (and the user) can read the evidence
// trail. Returns the log path ("" when no output or write failed).
func (r *Runner) writeAuditLog(taskID string, output string) string {
	if output == "" {
		return ""
	}
	logDir := r.cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".dsh", "logs")
	}
	taskLogDir := filepath.Join(logDir, "tasks")
	if err := os.MkdirAll(taskLogDir, 0o700); err != nil {
		r.logger.Printf("task %s: create audit log dir: %v", taskID, err)
		return ""
	}
	ts := time.Now().Format("20060102-150405")
	logPath := filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-audit-%s.log", taskID, ts))
	if err := os.WriteFile(logPath, []byte(output), 0o600); err != nil {
		r.logger.Printf("task %s: write audit log: %v", taskID, err)
		return ""
	}
	return logPath
}

// firstLine trims a multi-line summary to its first non-empty line for use in
// the compact phase_error field.
func firstLine(s string) string {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return s
}
