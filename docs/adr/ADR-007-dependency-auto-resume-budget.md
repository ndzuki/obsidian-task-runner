---
adr_id: "007"
title: "Dependency Chain Auto-Resume with Bounded Retry Budget"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
supersedes: []
created: 2026-07-31
---

# ADR-007: Dependency Chain Auto-Resume with Bounded Retry Budget

## Context

`blocked_by` 依赖链中的上游任务因阶段失败（MODEL_FAILED/PHASE_TIMEOUT 等）阻塞时，下游任务也被卡住。需要自动解开依赖链，但必须限制自动重试次数防止死循环。

## Decision

**`resolveBlockedDependencies` + `auto_resume_count` 预算机制**。

1. **自动 resume 触发条件**：上游 `status=blocked` + `blocked_phase` 非空 + `isAutoResumableError`（MODEL_FAILED/PHASE_TIMEOUT/PHASE_INTERRUPTED/MODEL_QUOTA_EXHAUSTED 或空码）+ `resume_approved=false`。
2. **预算语义**：
   - `auto_resume_pending=true`：标记本次失败由自动 resume 发起
   - 仅 `pending=true` 时 `handlePhaseFailure` 递增 `auto_resume_count`
   - 首次失败和人工 resume 后失败**不消耗预算**（`pending=false`）
3. **上限**：`auto_resume_count ≥ 2` → 停止自动恢复 → 桌面通知用户手动 `resume_approved=true`。
4. **人工 resume 重置**：无 `auto_resume_pending` 标记 → 计数清零。

## Safety Boundaries

- 未限定引用只在**下游项目内**解析
- 跨项目引用按 vault-map key 精确匹配（目录名 → 数字前缀后缀 → frontmatter project fallback）
- 循环依赖（`dependencyCycle`）双方都不自动恢复
- `REQ_MISSING`/`VALIDATION_FAILED` 等非瞬时错误永不自动恢复
- `maxAutoResumeAttempts = 2`

## Consequences

- 依赖链自动推进，减少人工干预
- 连续失败 2 次后停止，避免无限重试
- `auto_resume_count` 和 `auto_resume_pending` 语义精确，不误计
