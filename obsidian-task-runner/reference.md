# Obsidian Task Runner — Reference

## 状态流转

```
blocked ──补齐 project + assignee 且 blocked_by 为空──→ ready
                                                            │
                                                            ▼
ready ──→ Round 1 ──→ plan-review ──plan_approved:true──→ Round 2 ──→ review
  ▲          │                 │                                │         │
  │          ▼                 │                                ▼         ▼
  │       🔔 请审阅计划        └──未批准：等待人工确认       🔔 请 review 代码
  │                                                                         │
  └── pending_req:true ─────────────────────────────────────────────────────┘
      需求文档更新后自动重置 status=ready，重新走 Round 1 → plan-review

review/conflict ──merge_approved:true──→ Merge Phase
                                      │
                                      ├── git push + gh pr create + merge 成功 → done
                                      └── 冲突 / 不可合并 → conflict ──人工处理后重新设 merge_approved:true
```

## 状态详解

| 状态 | 含义 | 谁设置 | 下一步 |
|------|------|--------|--------|
| `blocked` | 缺必填字段或被依赖阻塞 | 自动创建任务 / 人工 | 补齐 `project` + `assignee` 且 `blocked_by` 为空后自动转 `ready`；依赖阻塞则等待依赖解决 |
| `ready` | 新建任务，等待处理 | daemon 或人工 | Round 1 自动启动 |
| `plan-review` | 计划已生成，等待人工批准 | OMP（Round 1） | 人工审阅计划 |
| `implementing` | 正在实现代码 | OMP（Round 2 开始） | 自动进行 |
| `review` | 代码已实现，等待人工 review | OMP（Round 2 完成） | 人工 review 代码，确认后设 merge_approved: true |
| `conflict` | 合并冲突，需人工解决 | OMP（Merge Phase） | 人工解决冲突，重新设 merge_approved: true 重试 |
| `done` | 已完成合并并推送 | OMP（Merge Phase 成功） | 结束 |
| `error` | 执行失败 | OMP（异常时） | 人工排查日志 |

## Task Frontmatter 字段参考

### 系统自动管理（不要手动改）

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | enum | 见上方状态流转表 |
| `plan_approved` | bool | Round 2 的钥匙，人工设为 true |
| `merge_approved` | bool | 自动 PR + 合并的钥匙，人工 review 通过后设为 true；若 `assignee: deepseek`，表示授权 deepseek-v4-pro 执行 `git push`、`gh pr create`、merge 和分支清理 |
| `created` | ISO8601 | 文件首次创建时间 |
| `updated` | ISO8601 | 最后更新时间 |
| `completed` | ISO8601 | 完成时间 |
| `actual_hours` | float | 实际耗时 |
| `pending_req` | bool | 需求文档在任务进行中/完成后有更新。daemon 拾起后自动重置为 ready 并重新出计划 |
| `target_branch` | string | Git 分支名，由 Round 2 自动设置，Merge Phase 使用 |
| `pr_url` | string | PR 链接，Merge Phase 创建/复用 PR 后自动设置 |

### 人工填写

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `id` | string | ✅ | 唯一任务编号 |
| `title` | string | ✅ | 任务标题 |
| `project` | string | ✅ | vault-map.json 的项目 key |
| `new_project` | bool | | 是否从零创建新项目 |
| `template` | string | | 新项目技术栈提示（如 `go-gin-microservice`），Agent 出计划时参考，非强制 |
| `priority` | P0-P4 | | 优先级，默认 P2 |
| `due_date` | date | | 截止日期 |
| `estimated_hours` | float | | 预估工时 |
| `assignee` | string | | 委派执行 agent：`deepseek`（deepseek-v4-pro）/ `gpt`（gpt-5.5） |
| `reviewer` | string | | 代码审查人 |
| `req_doc` | string | ✅ | Requirements/ 下的需求文档路径 |
| `component` | string | | 影响组件 |
| `tags` | list | | 标签 |
| `epic` | string | | 所属 Epic |
| `parent` | string | | 父任务 ID |
| `blocks` | list | | 阻塞哪些任务 ID |
| `blocked_by` | list | | 被哪些任务 ID 阻塞 |
| `auto_approve` | bool | | 是否跳过 plan-review gate（新项目无效） |
| `off_peak_only` | bool | | Round 2 仅低峰执行（避开北京时间 9-12、14-18），节省 token 费用 |
| `switch_settings` | bool | | ⛔ 已弃用，改由 `assignee` 字段选择 agent。OMP 已接管 model routing，此字段无实际效果 |
| `target_env` | string | | 部署环境 |

## vault-map.json 配置参考

详见 `config/vault-map.example.json`。

## Dataview 看板

安装后可在 Vault 根目录打开 `Tasks-Dashboard.md` 查看任务统计。看板依赖 Dataview 读取任务 frontmatter，不会修改任务文件；任务必须位于 `Projects/**/Tasks/`，并且有正确的 YAML frontmatter。

完整安装步骤、查询解释、目录约定和看板为空的排查顺序见 [`docs/dataview.md`](../docs/dataview.md)。

如果手动调整 Vault 目录层级，需要同步修改看板中的 `FROM` / `WHERE` 查询条件。执行是否成功仍以任务状态、任务记录和 daemon 日志为准，看板只是展示层。


## 故障排查

### 任务没有被自动处理

1. 检查 `assignee` **不为空且有效**——这是最常见的原因：自动创建的任务 `assignee` 为空，daemon 会跳过。填写 `deepseek` / `gpt` 后保存即触发
2. 如果 status 是 `blocked`，确认 `project` 已填写、`assignee` 有效且 `blocked_by` 为空；满足后 daemon 会自动改为 `ready`，无需手动改 status
3. 检查 status 是否为 `ready`、(`plan-review` 且 `plan_approved: true`) 或 (`review`/`conflict` 且 `merge_approved: true`)
4. 如果 `off_peak_only: true` 且 status 为 `plan-review`，确认当前不在北京高峰时段（9-12、14-18）→ 低峰时段会自动拾起
5. 确认 `project` 字段在 vault-map.json 的 projects 中存在，或 `new_project: true`
6. 看日志：`tail -f ~/.omp/logs/otg-daemon.log`

### 新建需求文档没有自动生成任务

1. 确认文件名格式为 `REQ-<id>-<slug>.md`（如 `REQ-002-user-login.md`）

2. 确认保存后 watcher 检测到了变更：`tail -f ~/.omp/logs/otg-daemon.log`
3. 如果已存在关联任务（通过 `req_doc` 匹配），不会重复创建，只会更新
4. 手动重试：`otg on-req-changed <vault> <req_file>`

### Tasks-Dashboard.md 显示为空

1. 确认 Dataview 已启用
2. 确认任务文件位于 `Projects/**/Tasks/`
3. 确认 frontmatter YAML 格式正确（两个 `---`，缩进一致）
4. 详细排查步骤见 [`docs/dataview.md`](../docs/dataview.md) 第 7 节

### systemd 服务没有启动

```bash
# 检查状态
systemctl --user status omp-task-watcher.service
systemctl --user list-timers | grep omp-task-runner

# 看 systemd 日志
journalctl --user -u omp-task-watcher.service -n 50
journalctl --user -u omp-task-runner.service -n 50
```

### notify-send 没反应

- 确认 `notify-send` 可用：`which notify-send`
- 确认 notification daemon 在运行（如 dunst, notification-daemon）
- 在 vault-map.json 中检查 `notifications.desktop` 是否为 true

### 并发任务处理

`task-runner-daemon.sh` 使用 flock 文件锁防止并发——同一时间只允许一个 daemon 实例运行。watcher 触发的新 daemon 遇到锁时会直接退出，但**当前运行的 daemon 会在处理完当前批次后自动重扫**（最多 3 轮），拾起中途被 `on_req_changed` 重置的任务。因此需求文档更新后即使 daemon 正忙，也不会丢失——最多延迟到当前 OMP 会话完成后的一轮重扫。

如果系统重启后锁文件残留，daemon 会自动覆盖（锁与文件描述符绑定，进程退出后自动释放）。

### 断点续跑

OMP agent 采用 **stateless 设计**——每次启动不依赖内部状态，通过分析文件系统理解当前进度。`make install-force` 或进程异常被杀后，任务可从中断点自动恢复：

**续跑原理**：
1. daemon 重启 → 扫描到 `status: implementing` → 重新 spawn OMP
2. OMP 读 task 文档中的实现记录、git log、项目文件
3. 判断已完成的步骤，从未完成步骤继续

**能保留**：
- 已创建的文件和代码
- git 提交历史
- 任务文档中的计划和实现记录
- 项目目录的完整状态

**不保留**：
- 上一轮 OMP 的对话/思考上下文（这恰恰是优点——上下文过大反而影响模型质量）

**为什么不做 session resume**：保存和恢复 OMP 对话状态（200K-300K tokens）比重新扫描文件系统更慢、更不可靠，且模型输出的不确定性会导致 `replay` 不一致。文件系统就是最好的 checkpoint。

**建议**：在 SKILL.md 实现步骤中加入原子进度标记（如 "Step N completed: YYYY-MM-DD HH:MM"），让断点恢复更精确。
