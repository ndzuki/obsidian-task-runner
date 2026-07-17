# Obsidian Task Runner — Reference

## 状态流转

```
blocked ──补齐 project + assignee 且所有 blocked_by 依赖已 done──→ ready
                                                            │
                                                            ▼
ready ──→ needs-grilling ──→ plan-review ──plan_approved:true──→ Round 2 ──→ review
  ▲          │                      │                                │         │
  │          ▼                      │                                ▼         ▼
  │       🔔 Kitty 通知             └──未批准：等待人工确认       🔔 请 review 代码
  │       等待交互式 grilling                                             │
  │                                                                         │
  └── pending_req:true ─────────────────────────────────────────────────────┘
      需求文档更新后自动重置 status=ready，重新走 needs-grilling → plan-review

review/conflict ──merge_approved:true──→ Merge Phase
                                      │
                                      ├── git push + gh pr create + merge 成功 → done
                                      └── 冲突 / 不可合并 → conflict ──人工处理后重新设 merge_approved:true

implementing ──Round 2 阻塞──→ needs-grilling ──用户解决──→ implementing
```

## 状态详解

| 状态 | 含义 | 谁设置 | 下一步 |
|------|------|--------|--------|
| `blocked` | 缺必填字段或被依赖阻塞 | 自动创建任务 / 人工 | 补齐 `project` + `assignee` 后 daemon 自动扫描 `blocked_by` 中所有引用任务的状态；全部 `done` 则清空 `blocked_by` 并转 `ready`；有任一未完成则保持 `blocked` |
| `ready` | 新建任务，等待处理 | daemon 或人工 | daemon 触发 Grilling 通知，转为 `needs-grilling` |
| `needs-grilling` | 等待用户交互式需求对齐 | daemon | 用户在 Kitty tab 中完成 grilling 对话，OMP 生成计划后转为 `plan-review` |
| `plan-review` | 计划已生成，等待人工批准 | OMP（Round 1） | 人工审阅计划 |
| `implementing` | 正在实现代码 | OMP（Round 2 开始） | 自动进行；遇到阻塞转为 `needs-grilling` |
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
| `id` | string | ✅ | 项目内唯一任务编号；不同项目可使用相同编号。 |
| `title` | string | ✅ | 任务标题 |
| `project` | string | ✅ | vault-map.json 的项目 key |
| `new_project` | bool | | 是否从零创建新项目 |
| `template` | string | | 新项目技术栈提示（如 `go-gin-microservice`），Agent 出计划时参考，非强制 |
| `priority` | P0-P4 | | 优先级，默认 P2 |
| `due_date` | date | | 截止日期 |
| `estimated_hours` | float | | 预估工时 |
| `assignee` | string | | `models` 中的执行模型 key，例如 `default` / `deepseek` / `gpt` |
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

详见 `config/vault-map.example.json`。常用调度字段：

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `poll_interval_minutes` | int | `30` | watcher 未触发时的兜底扫描间隔。 |
| `max_concurrent_tasks` | int | `2` | 单个 daemon 同时执行的 OMP headless 上限；小于 `1` 时按 `1` 执行。等待仓库独占许可或准备 worktree 的任务不占槽位。不同仓库可并行；同一仓库的 Round 2 使用独立 worktree，可与 Round 2 或主工作区独占阶段并行。 |

修改并发上限或安装新二进制后，执行 `systemctl --user restart omp-task-watcher.service` 让常驻 daemon 重新读取配置和代码。

## Dataview 看板

安装后可在 Vault 根目录打开 `Tasks-Dashboard.md` 查看任务统计。看板依赖 Dataview 读取任务 frontmatter，不会修改任务文件；任务必须位于 `Projects/**/Tasks/`，并且有正确的 YAML frontmatter。

完整安装步骤、查询解释、目录约定和看板为空的排查顺序见 [`docs/dataview.md`](../docs/dataview.md)。

如果手动调整 Vault 目录层级，需要同步修改看板中的 `FROM` / `WHERE` 查询条件。执行是否成功仍以任务状态、任务记录和 daemon 日志为准，看板只是展示层。


## 故障排查

### 任务没有被自动处理

1. 检查 `assignee` **不为空**——这是最常见的原因：自动创建的任务 `assignee` 为空，daemon 会跳过。填写 `default` / `deepseek` / `gpt` 或其他 `models` key 后保存即触发

2. 如果 status 是 `blocked`，确认 `project` 已填写、`assignee` 有效；daemon 会自动检查 `blocked_by` 中所有引用任务是否 `done`，满足后清空 `blocked_by` 并转 `ready`，无需手动改 status
3. 检查 status 是否为 `ready`、(`plan-review` 且 `plan_approved: true`) 或 (`review`/`conflict` 且 `merge_approved: true`)
4. 如果 `off_peak_only: true` 且 status 为 `plan-review`，确认当前不在北京高峰时段（9-12、14-18）→ 低峰时段会自动拾起
5. 确认 `project` 字段在 vault-map.json 的 projects 中存在，或 `new_project: true`
6. 并发任务卡住时，检查 `~/.omp/logs/tasks/` 下对应任务文件路径 hash 的 `.pid` 和审计日志；不同项目即使 `id` 相同也使用独立文件。
7. 看日志：`tail -f ~/.omp/logs/otg-daemon.log`

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

daemon 使用 flock 文件锁保证同一 Vault 只有一个调度实例。watcher 或 timer 的重复触发遇到锁会退出；当前 daemon 会在批次完成后最多重扫 3 轮，拾取执行期间新变为 ready 的任务。

`max_concurrent_tasks` 只计算已派发的 OMP headless。调度器先准备执行目录，再寻找当前可运行的任务：

- Round 2：在仓库短锁内创建或复用任务专属 worktree，然后释放锁；OMP 在 worktree 中执行。
- `target_branch` 已有值：worktree 必须绑定该分支；本地分支不存在时自动创建。已有 worktree 的分支不一致会被拒绝，防止恢复到其他任务分支。
- `target_branch: ""`：属于合法初始状态，不需要批量预填。Round 2 在任务 worktree 内创建分支，并在进入 `review` 时写回实际分支名。
- Round 1、Merge、新项目：使用主工作区，必须取得仓库独占许可；同仓库的这些阶段串行。
- 某个独占任务正在等待同仓库许可时，它不会占用 OMP 槽位。调度器继续扫描队列，后续可在 worktree 中执行的 Round 2 可以先启动。

例如同仓库队列为 `Merge A → Merge B → Round 2 C`，并发上限为 `2`：应看到 `Merge A` 和 `Round 2 C` 同时运行，`Merge B` 等待。若只看到一个 OMP，按以下顺序检查：

1. `systemctl --user status omp-task-watcher.service`：确认服务进程树中实际 OMP 数量。
2. `otg find-ready "$OBSIDIAN_VAULT"`：确认还有可执行的 Round 2；`plan-review` 必须同时满足 `plan_approved: true`。
3. `journalctl --user -u omp-task-watcher.service -n 100`：检查 worktree 准备失败、项目路径解析失败或模型启动失败。
4. `~/.omp/logs/tasks/`：检查任务审计日志和按任务路径 hash 命名的 PID 文件。
5. `git -C <repo> worktree list --porcelain`：确认每个任务 worktree 绑定到预期的 `refs/heads/task/<id>-<slug>`；处于 Round 2 初期且 `target_branch` 仍为空时，短暂 detached 属于正常状态。

6. 修改配置或更新二进制后，确认 watcher 已重启；旧进程不会自动加载新调度逻辑。

如果系统重启后锁文件残留，daemon 仍可启动；flock 与文件描述符绑定，原进程退出后锁自动释放。

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
