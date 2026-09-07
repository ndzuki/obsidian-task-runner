---
adr_id: "ADR-002"
title: "Priority Assessment 异步 sidecar"
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

# ADR-002: Priority Assessment 异步 sidecar

## Status
accepted

## Context
新 REQ 可能没有人工 priority，但正式调度需要稳定排序。让 daemon 自由推断优先级会把不稳定模型输出直接写入核心状态，并可能覆盖用户决定。

## Decision
Priority Assessment 使用独立 bundled Skill 输出严格 JSON。daemon 负责 JSON 校验、三维分数重算、CAS 写回、两次失败后的 P2 fallback，以及人工 priority 覆盖保护。P0 只作为 recommendation，由用户手工确认。

## Alternatives Considered
- daemon 内规则关键词分类：难覆盖自然语言语义，维护成本高。
- 主阶段 Agent 顺带评定：占用正式阶段槽位，耦合状态流转。

## Consequences
- 模型输出不能绕过 daemon 的确定性校验。
- Priority sidecar 可独立超时和降级，不阻塞主流程。
- 安装必须包含 priority Skill。
