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

## Status Routing（状态路由）
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

## Core Invariants（核心不变量）
1. MUST route initial tasks through `ready → refining`；REQ 变更按当前状态设置 pending_req 并安全回 refining。
2. Maturity Gate MUST be fully_mature to enter planning；其他进入 Grilling。
3. 需求细化 Grilling MUST return to refining after completion；实现阻塞按 grill_resolution 恢复或重规划。
4. Planning MUST succeed before plan-review is available。
5. pending_req MUST NOT be cleared until new plan succeeds。
6. MUST NOT merge when pending_req=true；绝对禁止。
7. MUST prioritize pending_req over grill_resolution=resume，过期需求不能恢复旧实现。
8. MUST atomically clear grill_done/resolution/context/prev_status after consuming Grilling results。
9. plan_approved MUST only be valid during plan-review；提前批准自动清 false。
10. MUST NOT create directories during planning for new projects；Round 2 才创建。
11. MUST NOT push during Round 1/Round 2；Merge Phase 才允许远程操作。
12. MUST use system local time for all timestamps。

## IDs & Dependencies（ID与依赖）
- 数字 ID 项目内唯一。
- 同项目依赖：`TASK-010`。
- 跨项目依赖：`project-key:TASK-010`。
- req_doc 只使用 Vault 相对规范完整路径精确匹配。

## OnReqChanged（需求变更联动）
- blocked：保持 blocked，pending_req=true。
- ready：保持 ready，pending_req=true。
- refining/planning：只设 pending_req，不中断 live phase。
- needs-grilling + active owner：只设 pending_req，不清 owner、不重开 Kitty。
- plan-review：撤销批准，转 refining。
- implementing：当前 AC 后 checkpoint → refining。
- review/conflict/done：清 Merge 授权，转 refining。
- 新自动创建 TASK：pending_req=false。

## Notifications（通知）
- `notifications.desktop` 只控制 notify-send。
- Kitty Grilling tab 始终尝试创建，不受 desktop 开关控制。
- 同一 TASK 只允许一个活跃 Grilling tab；创建前按 task ID 检查 Kitty tab/window title，并以 per-task flock + debounce 防止并发和重启重复创建。
- Kitty JSON 无法解析时不创建 tab，保留 notify-send fallback；Kitty 不可用时保持 needs-grilling 并周期重试。

## Documentation（文档）
完整规范和实现验收清单见 `docs/workflow.md`；字段参考见 `reference.md`。
