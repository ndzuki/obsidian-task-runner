---
name: obsidian-task-runner
description: "Manual entry and reference router for the Obsidian task lifecycle. Daemon directly invokes refining, round1, round2, and merge skills. Trigger: task runner, 自动执行 Obsidian 任务, 自动实现需求文档."
---

# Obsidian Task Runner — Core Contract

本 Skill 是人工入口和流程参考。Daemon 不通过本 Skill 二次路由，而是按状态直接调用阶段 Skill。

## Required Skills

随包安装：

- `skill://obsidian-task-runner-refining`
- `skill://obsidian-task-runner-round1`
- `skill://obsidian-task-runner-round2`
- `skill://obsidian-task-runner-merge`

外部依赖：

- `skill://requirement-elaborator`
- `skill://grilling`
- `skill://domain-modeling`
- `skill://diagnosing-bugs`
- `skill://test-quality`

## 状态路由

| status | 行为 |
|--------|------|
| `blocked` | 等待字段/依赖，或 `resume_approved=true` 后恢复 `blocked_phase` |
| `ready` | daemon 转 `refining` |
| `refining` | daemon 直接调用 refining Skill，使用 models.default |
| `needs-grilling` | daemon 检查 owner/timeout并创建 Kitty；pending_req 优先强制 refining，否则 resume 恢复 prev status、replan 转 refining，空值继续等待；成功路由后清 grilling 临时字段 |
| `planning` | daemon 直接调用 Round 1 Skill，使用 TASK assignee |
| `plan-review` | 等待 plan_approved；批准后进入 Round 2 |
| `implementing` | daemon 直接调用 Round 2 Skill，使用 task worktree |
| `review` / `conflict` | pending_req 优先；否则 merge_approved 后调用 Merge Skill |
| `done` | pending_req=true 时回 refining，否则终态 |

## 核心不变量

1. 初次任务走 `ready → refining`；REQ 变更按当前状态设置 pending_req 并安全回 refining。
2. Maturity Gate fully_mature 才能进入 planning；其他进入 Grilling。
3. 需求细化 Grilling 完成回 refining；实现阻塞按 grill_resolution 恢复或重规划。
4. Planning 成功后才有 plan-review。
5. pending_req 直到新计划成功才清零。
6. pending_req=true 时绝对禁止 Merge。
7. pending_req 优先于 grill_resolution=resume，过期需求不能恢复旧实现。
8. 消费 Grilling 结果后原子清 grill_done/resolution/context/prev_status。
9. plan_approved 仅 plan-review 有效；提前批准自动清 false。
10. 新项目 planning 不创建目录；Round 2 才创建。
11. Round 1/Round 2 不 push；Merge Phase 才允许远程操作。
12. 所有时间戳使用系统本地时间。

## ID 与依赖

- 数字 ID 项目内唯一。
- 同项目依赖：`TASK-010`。
- 跨项目依赖：`project-key:TASK-010`。
- req_doc 只使用 Vault 相对规范完整路径精确匹配。

## OnReqChanged

- blocked：保持 blocked，pending_req=true。
- ready：保持 ready，pending_req=true。
- refining/planning：只设 pending_req，不中断 live phase。
- needs-grilling + active owner：只设 pending_req，不清 owner、不重开 Kitty。
- plan-review：撤销批准，转 refining。
- implementing：当前 AC 后 checkpoint → refining。
- review/conflict/done：清 Merge 授权，转 refining。
- 新自动创建 TASK：pending_req=false。

## 通知

- `notifications.desktop` 只控制 notify-send。
- Kitty Grilling tab 始终尝试创建，不受 desktop 开关控制。
- Kitty 不可用时保持 needs-grilling 并周期重试。

## 文档

完整规范和实现验收清单见 `docs/workflow.md`；字段参考见 `reference.md`。
