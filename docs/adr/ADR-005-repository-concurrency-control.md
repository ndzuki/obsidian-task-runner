---
adr_id: "ADR-005"
title: "仓库级并发控制"
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

# ADR-005: 仓库级并发控制

## Status
accepted

## Context
同一仓库的 Planning、Round 2 和 Merge 访问模式不同。全局串行会浪费 OMP 槽位；完全无锁会让主工作区写操作和读操作竞争，甚至破坏 worktree/branch 状态。

## Decision
每个仓库使用 `sync.RWMutex`：Refining/Planning 获取共享读锁；Merge 获取独占写锁；Round 2 仅在隔离 task worktree 运行，不持有 repo lock。等待 repo lock 不占 OMP 槽位；同一 TASK 通过 canonical task-path hash 进程内去重。

## Alternatives Considered
- 全局 mutex：不同仓库和共享读阶段无法并行。
- 所有阶段无锁：主仓库 Git 状态存在竞争和死锁风险。

## Consequences
- 同仓库多个 Planning 可并行，Merge 保持独占。
- worktree 创建仍短暂串行，Round 2 执行期无主仓库锁。
- 锁顺序固定为仓库锁前置准备、TASK 写入使用独立 flock，禁止反向嵌套。
