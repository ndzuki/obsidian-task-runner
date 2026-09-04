---
adr_id: "ADR-004"
title: "稳定 Error Code 与阶段恢复"
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

# ADR-004: 稳定 Error Code 与阶段恢复

## Status
accepted

## Context
daemon 若解析自然语言错误文本来决定恢复路径，会受模型、工具版本和本地化影响。不同阶段的副作用不同，统一重试或统一 blocked 都不安全。

## Decision
定义稳定 `phase_error_code` 枚举，`phase_error` 只保存人类可读摘要。恢复策略按阶段映射：Priority fallback；Refining/Planning 重试一次后 blocked；Round 2 模型 fallback 后 blocked；Merge conflict 进入 conflict，其余远程失败保持 review 并撤销授权。

## Alternatives Considered
- 解析 stderr 关键词：不稳定且难测试。
- 所有失败统一 error 状态：无法表达恢复位置和副作用。

## Consequences
- 自动化只判断 error code，通知和日志可稳定聚合。
- 新错误必须选择现有 code 或增加受测枚举。
- 阶段实现需要同步写 code、摘要和证据路径。
