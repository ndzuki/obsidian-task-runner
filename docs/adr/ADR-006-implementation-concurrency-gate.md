---
adr_id: "006"
title: "Implementation Concurrency Gate with PID Adoption"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
created: 2026-07-31
---

# ADR-006: Implementation Concurrency Gate with PID Adoption

## Context

`max_concurrent_tasks` 需要限制同时执行 Round 2（implementing）的 早期执行器 实例数，但 refining/planning/priority/merge 阶段不限。实现需要处理 daemon 重启后仍在运行的遗留 Round 2 进程。

## Decision

**`implementationGate`：本地槽 + PID 采纳双机制**。

1. **本地槽**（`tryAcquireLocal`/`releaseLocal`）：当前 daemon 实例启动的 早期执行器，通过计数信号量控制并发。
2. **PID 采纳**（`adopt`/`releaseAdopted`）：daemon 重启时扫描 `/tmp/otg-pid-*` 文件，将存活进程（`procAlive` 排除 zombie）的 PID 计入 gate 活跃计数。
3. **自适应轮询**：扫描间隔从 12 次（×500ms）递增到 60 次，且改用 `findReadyTasks()` 而非原始任务列表。
4. **priority 独立批次上限** 2：不受 implementation gate 限制。

## Consequences

- 重启后遗留 早期执行器 不会被重复调度
- `procAlive` 排除 zombie（防止死进程占用槽位）
- 缺点：PID-based gate 无法区分"进程正常退出"与"被 kill"，需配合 cooldown 机制（见 ADR-011）
