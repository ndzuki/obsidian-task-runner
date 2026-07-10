# Obsidian Task Runner

让 [OMP (Oh My Pi)](https://github.com/ndzuki/oh-my-pi) 自动读取 Obsidian Vault 中的需求和任务文档，理解要求后自行开发实现到代码交付。

## 这是什么

你在 Obsidian 里整理好需求和任务，保存文件后，OMP 自动：

1. **发现任务** — 扫描 `Tasks/` 目录，找到 `status: ready`、`plan_approved: true` 或 `merge_approved: true` 的任务
2. **读需求文档** — 理解要做什么（支持一句话到完整结构化的任意格式）
3. **出实现计划** — Round 1 生成分步骤计划，写回任务文档
4. **等你确认** — 🔔 桌面通知提醒，在 Obsidian 中设 `plan_approved: true` 并保存
5. **自动实现** — Round 2 写代码、跑测试、lint、提交到分支
6. **需求联动** — 更新 `Requirements/` 下的需求文档后，关联任务自动重置，重新出计划
7. **等你 Review** — 🔔 桌面通知提醒 review 代码，确认后设 `merge_approved: true` 并保存
8. **自动合并** — Merge Phase 合并到主分支、`git push`、删除 feature 分支、标记 `done`

不需要离开 Obsidian，不需要手动切终端——OMP 在后台自动完成。

```
Obsidian Tasks/ 或 Requirements/ 文件保存
        │
        ▼
  inotifywait 检测变化 (秒级触发，同时监听两个目录)
        │
        ├── Requirements/ 变化 → on_req_changed.py 找关联任务 → 重置为 ready
        │
        ▼
  find_ready_tasks.py 发现可处理任务
        │
        ▼
  omp -m <model> -p "/obsidian-task-runner <task_id>"
        │
        ├── Round 1: 读需求 → 出计划 → status=plan-review → 🔔 "请审阅"
        │     👤 在 Obsidian 审阅计划，设 plan_approved: true
        │
        ├── Round 2: 🔔 "开始实现" → 写代码 → 测试/lint → 验收 → git commit → 🔔 "请 review"
        │     👤 Review 代码，设 merge_approved: true
        │
        └── Merge Phase: 🔔 "开始合并" → git merge → git push → 删除分支 → status=done → 🔔 "已完成"
              └── 冲突 → status=conflict → 🔔 "合并冲突，请手动解决"
```

systemd timer 每 30 分钟兜底扫描，防止 inotify 事件丢失。

### 前置要求

- [OMP (Oh My Pi)](https://github.com/ndzuki/oh-my-pi) CLI（`omp` 命令可用）
- Python 3.8+
- Git
- Linux + systemd
- （可选）`inotify-tools` — 事件触发；不装也能用定时轮询
- （可选）`libnotify` — 桌面通知（须配合 notification daemon：dunst / mako 等）

```bash
git clone https://github.com/ndzuki/obsidian-task-runner.git
cd obsidian-task-runner
./install.sh
```

脚本自动检测你的 shell（bash/zsh/fish），写入正确的环境变量语法。

### 非交互安装（CI / 脚本化）

```bash
OBSIDIAN_VAULT=/home/you/Obsidian/Vault \
NEW_PROJECT_ROOT=/home/you/src \
./install.sh --non-interactive
```

### 创建第一个任务

```bash
# 1. 复制任务模板
#    assignee: deepseek            # 或 gpt（使用 gpt-5.5）

3. 写需求文档（三选一，写多少都行）
cp REQ-000-template.md "$OBSIDIAN_VAULT/Requirements/用户登录API.md"
#    L1 极简：标题 + 一段话 + 完成标准
#    L2 标准：+ 功能列表 + 技术约束
#    L3 完整：+ API 规格 + 数据模型
```

保存后，几秒内自动触发。也可以手动：

```bash
cd /path/to/your/project
omp -m "deepseek/deepseek-v4-pro:xhigh" -p "/obsidian-task-runner"
```
保存后，几秒内自动触发。也可以手动：

## 工作流

### 主流程：从需求到交付

```
你                                   OMP (deepseek-v4-pro / gpt-5.5)
─────────────────────────────────────────────────────────────
1. 在 Vault/Requirements/ 创建 REQ-xxx.md
2. 保存
                         →          3. 自动创建 TASK-xxx.md（status=blocked，assignee 留空）
                         →          4. 🔔 提醒补充 project / assignee 等必填字段
5. 填写 project + assignee: deepseek 或 gpt，保存
                         →          6. 自动解除 blocked → ready
                         →          7. 根据 assignee 选择模型启动 Round 1
                         →          8. Round 1: 读需求，出计划
                         →          9. 写回计划，status=plan-review
                         →          10. 🔔 "计划已生成，请审阅"
11. 在 Obsidian 审阅计划
12. 设 plan_approved: true，保存
                         →          13. 根据 assignee 选择模型启动 Round 2
                         →          14. 🔔 "🚀 开始实现"
                         →          15. Round 2: 写代码、测试、lint
                         →          16. task-verifier 验收
                         →          17. git commit 到分支
                         →          18. status=review
                         →          19. 🔔 "代码已实现，请 review；如需自动 PR/merge，设 merge_approved: true"
20. Review 代码
21. 设 merge_approved: true，保存
                         →          22. 根据 assignee 启动对应 agent
                         →          23. 🔔 "🔀 开始合并"
                         →          24. Merge Phase: git push + gh pr create + merge
                         →          25. 删除 feature 分支
                         →          26. status=done
                         →          27. 🔔 "🎉 已完成，代码已推送"
28. 查看 MR/PR ✅
```

### 需求变更流程：任何时候更新需求文档

```
编辑 Requirements/xxx.md，保存
        │
        ▼ (秒级)
  on_req_changed.py 找到关联任务
        │
        ├── status=ready/plan-review → 直接重置为 ready
        ├── status=implementing → 标记 pending_req，等实现完自动接上
        └── status=review/done → 标记 pending_req，自动重新出计划
              │
              ▼
         Round 1 重新出计划 → 停在 plan-review → 🔔 "请审阅"
              │
         (不会自动跳到 Round 2——即使 auto_approve: true)
```

**关键行为**：
- 重新出计划**永远停在 `plan-review`**，等人工确认——不会因为"代码已经实现过了"就跳过
- `done` 状态的任务更新需求后也会被拾起，不会遗漏
- 桌面通知覆盖：计划生成、开始实现、实现完成、需求变更并入、执行失败

## 通知时间线

| 时机 | 通知 | 含义 |
|------|------|------|
| Round 1 完成 | 📋 Task #001: 计划已生成，请审阅 | 打开 Obsidian 看计划 |
| Round 2 开始 | 🚀 Task #001: 开始实现 | OMP 已启动，正在写代码 |
| Round 2 完成 | ✅ Task #001: 代码已实现，请 review | 切到分支 review 代码 |
| PR 已创建 | 📬 Task #001: PR 已创建 | 打开 GitHub 查看 Pull Request |
| Merge 开始 | 🔀 Task #001: 开始合并 | 正在合并并推送到主分支 |
| Merge 成功 | 🎉 Task #001: 已完成 | 代码已推送，分支已清理 |
| Merge 冲突 | ⚠️ Task #001: 合并冲突 | 手动解决冲突后重新设 merge_approved: true |
| 需求变更并入 | 🔄 Task #001: 需求变更已并入 | 需求文档更新了，自动重新出计划 |
| 执行失败 | ❌ Task #001: 执行失败 | 检查日志排错 |

## 监控执行进度

| 方式 | 命令 | 说明 |
|------|------|------|
| 实时日志 | `tail -f ~/.omp/logs/task-runner.log` | OMP 每一步输出 |
| 任务文件 | 在 Obsidian 中打开对应 Task | `## 实现记录` 逐步填充 |
| systemd | `journalctl --user -fu omp-task-runner` | 服务层日志 |

## 目录结构

```
obsidian-task-runner/              # skill（安装到 ~/.omp/skills/）
├── SKILL.md                       # 核心：两轮状态机指令
├── reference.md                   # 参考：状态流转、字段、故障排查
├── scripts/
│   ├── find_ready_tasks.py        # 发现可处理任务
│   ├── update_task_status.py      # 更新 YAML frontmatter
│   ├── resolve_project_path.py    # 项目名 → 本地路径
│   ├── register_project.py        # 注册新项目（原子写入 + 语法验证）
│   ├── on_req_changed.py          # 需求变更 → 关联任务自动重置
│   ├── on_req_changed.py          # 需求变更 → 关联任务自动重置
│   ├── notify_on_status_change.sh # 桌面通知
│   ├── task-runner-daemon.sh      # 调度脚本（flock 防并发）
│   └── task-watcher.sh            # inotify 双目录监听
└── config/
    └── vault-map.example.json     # 项目映射模板

agents/task-verifier.md            # 验收 subagent
TASK-000-template.md               # 新任务模板（JIRA 风格字段）
REQ-000-template.md                # 需求模板（L1/L2/L3 渐进式）
install.sh                         # 一键安装/卸载
omp-task-runner.service         # systemd 兜底轮询
omp-task-runner.timer           # systemd 定时器
omp-task-watcher.service        # systemd 事件触发
```

## 配置说明

### vault-map.json

安装后位于 `~/.omp/skills/obsidian-task-runner/config/vault-map.json`：

```json
{
  "obsidian_vault": "/home/you/Obsidian/MainVault",
  "projects": [
    {
      "name": "my-backend",
      "path": "/home/you/src/my-backend",
      "git_remote": "github.com/you/my-backend"
    },
    {
      "name": "frontend-app",
      "path": "/home/you/src/frontend-app",
      "git_remote": "github.com/you/frontend-app"
    }
  ],
  "new_project_root": "/home/you/src",
  "notifications": {
    "desktop": true
  },
  "poll_interval_minutes": 30
}
```

| 字段 | 说明 |
|------|------|
| `obsidian_vault` | Obsidian Vault 根目录（daemon 在无环境变量时的 fallback） |
| `projects` | 项目列表，每个含 `name`、`path`、`git_remote` |
| `new_project_root` | 新项目统一创建目录 |
| `templates` | 新项目脚手架模板定义 |
| `notifications.desktop` | 是否弹桌面通知 |
| `poll_interval_minutes` | systemd timer 兜底轮询间隔 |

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `OBSIDIAN_VAULT` | 交互提问 | Obsidian Vault 根目录 |
| `NEW_PROJECT_ROOT` | `$HOME/src` | 新项目默认创建目录 |
| `NOTIFY_ENABLED` | `true` | 是否启用桌面通知 |
| `POLL_INTERVAL_MINUTES` | `30` | 定时轮询间隔 |
| `SYSTEMD_ENABLED` | `true` | 是否注册 systemd 服务 |
| `SKILL_INSTALL_DIR` | `~/.omp/skills/obsidian-task-runner` | skill 安装路径 |

### Task 文档字段

完整字段说明见 [`reference.md`](obsidian-task-runner/reference.md)。关键字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | enum | `blocked`（缺必填字段或依赖阻塞）→ `ready` → `plan-review` → `implementing` → `review` → `done`；还有 `conflict`（合并冲突）、`error`（异常） |
| `plan_approved` | bool | **人工 Gate #1**，设 `true` 触发 Round 2 |
| `merge_approved` | bool | **人工 Gate #2**，设 `true` 触发自动 PR + 合并（Merge Phase）。若 `assignee: deepseek`，使用 deepseek-v4-pro 执行 `git push`、`gh pr create`、merge 和分支清理 |
| `auto_approve` | bool | 跳过 plan-review 人工确认（新项目无效） |
| `off_peak_only` | bool | Round 2 仅低峰执行（避开北京时间 9-12、14-18），降低 token 费用 |
| `switch_settings` | bool | ⛔ 已弃用。`assignee` 字段直接决定模型（`deepseek`/`gpt`），不再需要 switch_settings |
| `assignee` | string | 委派执行模型：`deepseek`（deepseek-v4-pro）、`gpt`（gpt-5.5） |
| `new_project` | bool | 从零创建新项目 |
| `project` | string | vault-map.json 的 `projects[].name` |
| `req_doc` | string | 需求文档相对路径（如 `Requirements/xxx.md`） |
| `target_branch` | string | Round 2 自动设置，Merge Phase 使用的 feature 分支名 |
| `pr_url` | string | Merge Phase 创建 PR 后自动设置 |

### 需求文档格式

支持三种层次，写多少都行：

| 层次 | 内容 | OMP 处理方式 |
|------|------|----------------|
| L1 极简 | 标题 + 一段话 + 完成标准 | 从自然语言提取功能点，推断技术栈 |
| L2 标准 | + 功能列表 + 技术约束 | 逐条映射 FR 到实现步骤 |
| L3 完整 | + API 规格 + 数据模型 | 直接生成 handler 和 struct |

模板：`REQ-000-template.md`

### 追加新需求

在已有需求文档末尾追加时，**每个新需求必须是独立的 `##` 标题 section**：

```markdown
## 新增需求：操作审计日志
- 记录所有 API 调用并持久化

## 新增需求：Swagger API 文档
- 使用 swaggo 生成 swagger.json
```

❌ 不要作为已有 section 的子项追加——AI 会当作同一需求的细节而非独立需求。保存后 watcher 自动检测变化，找到关联任务并重新出计划。

## 维护命令

```bash
# 更新 skill（git pull 后必做）
./install.sh --non-interactive

# 重启 watcher（skill 更新后）
systemctl --user restart omp-task-watcher.service

# 卸载
./install.sh --uninstall             # 保留配置和日志
./install.sh --uninstall --force      # 完全清除
```

## 日志

日志位于 `~/.omp/logs/`，超过 10MB 自动轮转，保留最近 5 个备份（`.1` ~ `.5`）。

## 安全边界

以下操作的边界：

- Round 1 / Round 2 **不会** push、创建 PR、合并或清理分支。
- `merge_approved: false` 时，Round 2 完成后停在 `review`，只提醒用户 review 代码、创建 PR 和处理 merge。
- `merge_approved: true` 是人工明确授权，Merge Phase 才会自动执行 `git push`、`gh pr create`、merge、push 默认分支和清理 feature 分支。
- `assignee: deepseek` 或 `assignee: gpt` 且 `merge_approved: true` 时，daemon 以对应模型执行上述 GitHub PR/merge 操作。
- 新项目的脚手架创建：Round 1 永远只出方案；必须人工设 `plan_approved: true` 后 Round 2 才创建文件。

## 常见问题

### 任务没有被自动处理？

1. 检查 status 是 `ready`、`blocked` 且必填字段已补齐、`plan-review` + `plan_approved: true`、还是 `review`/`conflict` + `merge_approved: true`
2. 如果 `off_peak_only: true` 且 status 为 `plan-review`，确认当前不在北京高峰时段（9-12、14-18）→ 低峰时段会自动拾起
3. 检查 `assignee` 是否为 `deepseek` 或 `gpt`
4. 确认 `project` 在 vault-map.json 的 `projects` 列表中存在，或 `new_project: true`
5. `tail -f ~/.omp/logs/task-runner.log`

### 没有桌面通知？

安装 `libnotify`：`sudo pacman -S libnotify`（Arch）或 `sudo apt install libnotify-bin`（Debian/Ubuntu）。确认 notification daemon 在运行：

```bash
pgrep -a dunst || pgrep -a mako || echo "notification daemon 未运行"
```

### 如何只用手动模式？

设置 `SYSTEMD_ENABLED=false` 或跳过 systemd 步骤。手动触发：

```bash
omp -m "deepseek/deepseek-v4-pro:xhigh" -p "/obsidian-task-runner [task_id]"
```

### 更新需求文档后任务没有刷新？

watcher 同时监听 `Tasks/` 和 `Requirements/`。更新需求文档保存后：
- 任务在 `ready`/`plan-review` → 直接重置为 `ready`
- 任务在 `implementing`/`review`/`done` → 标记 `pending_req: true`，等 daemon 拾起

确认：`tail -f ~/.omp/logs/task-watcher.log`

### 需求变更后的重新出计划会停在 plan-review 吗？

会。即使任务之前已经完成 Round 2，或者 `auto_approve: true`，**重新出计划场景下永远停在 `plan-review`**，必须人工确认 `plan_approved: true` 才进入 Round 2。这是为了防止需求变更后 AI 自作主张跳过审阅。

### 完成后更新需求文档，任务会重新处理吗？

会。`done` 状态的任务更新需求文档后会被标记 `pending_req: true`，daemon 自动拾起、重置为 `ready`、重新出计划并停在 `plan-review`。

### 如何使用不同模型执行任务？

通过任务 frontmatter 的 `assignee` 字段决定使用的 OMP 模型：

| assignee | 模型 | Round 1 | Round 2 | Merge Phase |
|----------|------|---------|---------|-------------|
| `deepseek` | deepseek-v4-pro | 出计划 | 实现代码 | 合并 |
| `gpt` | gpt-5.5 | 出计划 | 实现代码 | 合并 |

`assignee=deepseek` → 两阶段均由 deepseek-v4-pro 完成；`assignee=gpt` → 两阶段均由 gpt-5.5 完成。
轻量任务（新需求自动创建 TASK 文档、状态更新等）使用 deepseek-v4-flash。
未设置时回退到 `TASK_RUNNER_AGENT` 环境变量（默认 `deepseek`）。

> ⛔ `switch_settings` 已移除。`codex` / `claude` / `claude+human` 不再支持。

### 如何从新需求文档自动创建任务？

在 `Requirements/` 下创建 `REQ-<id>-<slug>.md` 格式的文件并保存即可。系统自动：

1. 在 `Tasks/` 下生成 `TASK-<id>-<slug>.md`（使用 deepseek-v4-flash 完成创建）
2. 从需求文档自动提取标题、摘要、验收标准、项目名、标签等
3. `assignee` 留空——你必须填写 `deepseek` 或 `gpt` 后任务才会被拾取

> 💡 文件名必须匹配 `REQ-<id>-<slug>.md`（如 `REQ-002-user-login.md`），否则只记录 warning 不自动创建。

### 查看运行状态

```bash
# systemd 服务
systemctl --user status omp-task-watcher.service
systemctl --user list-timers | grep omp-task-runner

# 实时日志
tail -f ~/.omp/logs/task-watcher.log
tail -f ~/.omp/logs/task-runner.log

# systemd journal
journalctl --user -u omp-task-watcher.service -n 50


## 贡献

MIT License。欢迎 Issue 和 PR。

## License

MIT © 2026 ndzuki and contributors
