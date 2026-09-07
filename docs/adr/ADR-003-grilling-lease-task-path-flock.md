---
adr_id: "ADR-003"
title: "Grilling Lease 通过 task-path flock"
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

# ADR-003: Grilling Lease 通过 task-path flock

## Status
accepted

## Context
Grilling 由 daemon、交互式 Agent 和用户共同更新 TASK。仅用 `grill_owner` 字符串无法防止并发获取、心跳覆盖和超时抢占竞争。

## Decision
以 canonical TASK 路径 SHA-256 对应的 flock 作为写入 seam。租约获取、续租、超时判断、抢占和释放均在锁内重读最新 frontmatter 后执行 CAS；超时基于 `grill_heartbeat_at`，无 heartbeat 时回退 `grill_started_at`。

## Alternatives Considered
- 仅依赖进程内 mutex：跨 daemon/Agent 进程无效。
- 仅用 started_at 超时：长时间有效交互会被错误抢占。

## Consequences
- 单 TASK 同时只有一个有效 Grilling owner。
- 所有 TASK 系统写入必须复用同一 task-path flock。
- flock 仅适用于受信任单用户本地文件系统。
