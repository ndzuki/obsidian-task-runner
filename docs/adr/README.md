# ADR 目录

> 架构决策记录（Architecture Decision Records）。每条记录「上下文 / 决定 / 取舍」，
> 是理解本项目为何如此设计的权威来源。生产实现以 `internal/` 代码为准。

| 编号 | 决策 |
| --- | --- |
| [ADR-001](ADR-001-task-lifecycle-state-machine.md) | 任务生命周期状态机：离散主状态 + 门禁字段 |
| [ADR-002](ADR-002-priority-assessment-sidecar.md) | Priority Assessment 异步 sidecar，严格 JSON + 三维重算 |
| [ADR-003](ADR-003-grilling-lease-task-path-flock.md) | Grilling 租约通过 task-path flock 写入 |
| [ADR-004](ADR-004-stable-error-codes-and-recovery.md) | 稳定 phase_error_code 枚举与阶段恢复策略 |
| [ADR-005](ADR-005-repository-concurrency-control.md) | 仓库级并发控制 |
| [ADR-006](ADR-006-implementation-concurrency-gate.md) | Implementation 并发门（本地槽 + PID 采纳） |
| [ADR-007](ADR-007-dependency-auto-resume-budget.md) | 依赖链自动恢复 + 预算封顶 |
| [ADR-008](ADR-008-thinking-mode-per-phase.md) | 阶段级 reasoningEffort 映射 |
| [ADR-009](ADR-009-context-injection-block.md) | 项目上下文注入块 |
| [ADR-010](ADR-010-knowledge-extraction-timing.md) | 知识提取时机（merge 成功后） |
| [ADR-011](ADR-011-blocked-auto-unblock-guards.md) | blocked 自动恢复护栏 |
| [ADR-012](ADR-012-model-fallback-configuration.md) | 模型兜底配置化（fallback_models） |
| [ADR-013](ADR-013-pm-grilling-consolidation.md) | PM 统筹 grilling 争议合并 |
