---
adr_id: "011"
title: "Blocked Auto-Unblock Guards and Failure Cooldown"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
supersedes: [ADR-004]
created: 2026-07-31
---

# ADR-011: Blocked Auto-Unblock Guards and Failure Cooldown

## Context

v0.18.4 排查发现 `processBatchSequential` 将 phase-failure blocked 任务（`blocked_phase` 非空、`resume_approved=false`）与正常依赖阻塞任务同等处理，导致每次扫描都 auto-unblock → 重新调度失败 → 再 blocked → 死循环。同时 早期执行器 被 kill 后 PID gate 无 cooldown，watcher 触发即重调，产生调度风暴。

## Decision

**三重防护**：

1. **Phase-failure 不 auto-unblock**：`blocked_phase != "" && !resume_approved` → `continue`（保持阻塞）。
2. **Defensive `blocked_phase` 补全**：`blocked` + `blocked_phase=""` + 有 phase error → 自动补 `"implementing"`（防御 早期执行器 skill 的畸形写入）。
3. **失败 cooldown**：`handlePhaseFailure` 写 blocked 后记录 `phaseFailures[taskPath] = time.Now()`。下次扫描若 `time.Since < 2min` → 跳过该任务。
4. **SIGTERM 不 fallback**：早期执行器 exit code 为负（信号终止）→ 跳过 fallback 模型重试，直接 blocked（省 token + 时间）。
5. **Grilling 通知去重**：5 分钟内同一 task ID 只发送一次提醒。

额外修复（v0.18.5）：`tryKittyTab` debounce 返回 `true`（而非 `false`）→ 桌面通知 fallback 正确抑制。

## Consequences

- 彻底消除"blocked→auto-unblock→re-dispatch→fail→blocked"死循环
- 消除 早期执行器 被杀后的重复调度风暴（027 17 次/064 6 次）
- 消除 grilling 通知每 30 秒重复触发
- 缺点：2 分钟 cooldown 是硬编码，高负载时可能过于保守——后续可改为按失败次数递增冷却
