# Obsidian Task Runner — Reference

## 状态流转

```
           ┌──────────────────────────────────┐
           │          人工 Gate                │
           │   设 plan_approved: true + 保存   │
           └──────────────────────────────────┘
                            ▲
                            │
ready ──→ Round 1 ──→ plan-review ──→ Round 2 ──→ review ──→ done
  ▲        │                  ▲            │            │
  │        ▼                  │            ▼            │
  │     🔔 桌面通知           │         🔔 桌面通知      │
  │   "请审阅计划"            │      "请 review 代码"    │
  │                           │                          │
  └── pending_req: true ──────┘                          │
  (需求文档更新后自动触发，                                 │
   停在 plan-review 等确认，                                │
   即使任务已完成也适用)                                     │
                                                          │
  ←── 可在任意状态下更新需求文档 ←──────────────────────────┘
```

## 状态详解

| 状态 | 含义 | 谁设置 | 下一步 |
|------|------|--------|--------|
| `ready` | 新建任务，等待处理 | 人工（模板默认） | Round 1 自动启动 |
| `plan-review` | 计划已生成，等待人工批准 | Claude（Round 1） | 人工审阅计划 |
| `implementing` | 正在实现代码 | Claude（Round 2 开始） | 自动进行 |
| `review` | 代码已实现，等待人工 review | Claude（Round 2 完成） | 人工 review 代码 |
| `done` | 已完成合并 | 人工 | 结束 |
| `error` | 执行失败 | Claude（异常时） | 人工排查日志 |
| `blocked` | 被其他任务阻塞 | 人工 | 等待依赖解决 |

## Task Frontmatter 字段参考

### 系统自动管理（不要手动改）

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | enum | 见上方状态流转表 |
| `plan_approved` | bool | Round 2 的钥匙，人工设为 true |
| `created` | ISO8601 | 文件首次创建时间 |
| `updated` | ISO8601 | 最后更新时间 |
| `completed` | ISO8601 | 完成时间 |
| `actual_hours` | float | 实际耗时 |
| `pending_req` | bool | 需求文档在任务进行中/完成后有更新。daemon 拾起后自动重置为 ready 并重新出计划 |
| `target_branch` | string | Git 分支名 |

### 人工填写

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `id` | string | ✅ | 唯一任务编号 |
| `title` | string | ✅ | 任务标题 |
| `project` | string | ✅ | vault-map.json 的项目 key |
| `new_project` | bool | | 是否从零创建新项目 |
| `template` | string | | 新项目脚手架模板名 |
| `priority` | P0-P4 | | 优先级，默认 P2 |
| `due_date` | date | | 截止日期 |
| `estimated_hours` | float | | 预估工时 |
| `assignee` | string | | claude / human / claude+human |
| `reviewer` | string | | 代码审查人 |
| `req_doc` | string | ✅ | Requirements/ 下的需求文档路径 |
| `component` | string | | 影响组件 |
| `tags` | list | | 标签 |
| `epic` | string | | 所属 Epic |
| `parent` | string | | 父任务 ID |
| `blocks` | list | | 阻塞哪些任务 ID |
| `blocked_by` | list | | 被哪些任务 ID 阻塞 |
| `auto_approve` | bool | | 是否跳过 plan-review gate（新项目无效） |
| `target_env` | string | | 部署环境 |

## vault-map.json 配置参考

详见 `config/vault-map.example.json`。

## 故障排查

### 任务没有被自动处理

1. 检查 status 是否为 `ready` 或 (`plan-review` 且 `plan_approved: true`)
2. 检查 assignee 是否为 `claude` 或 `claude+human`
3. 确认 `project` 字段在 vault-map.json 的 projects 中存在，或 `new_project: true`
4. 看日志：`tail -f ~/.claude/logs/task-runner.log`

### systemd 服务没有启动

```bash
# 检查状态
systemctl --user status claude-task-watcher.service
systemctl --user list-timers | grep claude-task-runner

# 看 systemd 日志
journalctl --user -u claude-task-watcher.service -n 50
journalctl --user -u claude-task-runner.service -n 50
```

### notify-send 没反应

- 确认 `notify-send` 可用：`which notify-send`
- 确认 notification daemon 在运行（如 dunst, notification-daemon）
- 在 vault-map.json 中检查 `notifications.desktop` 是否为 true

### 并发任务处理

`task-runner-daemon.sh` 使用 flock 文件锁防止并发——同一时间只允许一个 daemon 实例运行。watcher 触发的新 daemon 遇到锁时会直接退出，但**当前运行的 daemon 会在处理完当前批次后自动重扫**（最多 3 轮），拾起中途被 `on_req_changed` 重置的任务。因此需求文档更新后即使 daemon 正忙，也不会丢失——最多延迟到当前 Claude session 完成后的一轮重扫。

如果系统重启后锁文件残留，daemon 会自动覆盖（锁与文件描述符绑定，进程退出后自动释放）。
