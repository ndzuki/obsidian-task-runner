---
adr_id: "ADR-001"
title: "状态机驱动 TASK 生命周期"
project: "obsidian-task-runner"
project_id: "003"
status: "accepted"
decision_scope: "project"
created: "2026-07-28"
updated: "2026-07-28"
requirements:
  - "REQ-003"
tasks:
  - "TASK-003"
---

# ADR-001: 状态机驱动 TASK 生命周期

## Status
accepted

## Context
TASK 需要覆盖需求成熟度、Grilling、双人工 Gate、返工、冲突和无需交付终态。依赖布尔组合或自由文本状态会产生非法组合，daemon 也无法可靠恢复。

## Decision
使用离散主状态 `blocked → ready → refining → needs-grilling|planning → plan-review → implementing → review → done|conflict|closed`。状态迁移由 daemon 和阶段 Skill 按稳定前置条件执行；`pending_req`、`plan_approved`、`merge_approved` 是门禁，不替代主状态。

## Alternatives Considered
- 只使用 `ready/implementing/done` 加大量布尔字段：非法组合过多，恢复策略不可证明。
- 由 Agent 自由改写状态：缺少机器可校验的迁移契约。

## Consequences
- 调度和恢复可通过表驱动测试验证。
- 新增状态必须同步更新 readiness、通知和审计。
- 状态机更严格，旧 TASK 需要兼容读取和显式迁移。
