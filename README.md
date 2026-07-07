# Obsidian Task Runner

让 [Claude Code](https://docs.claude.com) 自动读取 Obsidian Vault 中的需求和任务文档，理解要求后自行开发实现到代码交付。

## 这是什么

你在 Obsidian 里整理好需求和任务，保存文件后，Claude Code 自动：

1. **读需求文档** — 理解要做什么
2. **出实现计划** — 写回任务文档
3. **等你确认** — 设 `plan_approved: true` 并保存
4. **自动实现** — 写代码、跑测试、lint、提交到分支
5. **桌面通知** — 提醒你 review

不需要离开 Obsidian，不需要手动切终端 `git checkout -b`、写代码、跑测试——Claude Code 在后台自动完成。

## 工作原理

```
Obsidian Tasks/ 文件保存
        │
        ▼
  inotifywait 检测变化 (秒级触发)
        │
        ▼
  find_ready_tasks.py 发现可处理任务
        │
        ▼
  claude -p /obsidian-task-runner (headless)
        │
        ├── Round 1: 读需求 → 出计划 → 状态: plan-review → 🔔 桌面通知
        │     👤 你在 Obsidian 审阅计划，设 plan_approved: true
        │
        └── Round 2: 实现代码 → 测试/lint → 验收 → git commit → 🔔 桌面通知
```

systemd timer 每 30 分钟兜底扫描，防止 inotify 事件丢失。

## 快速开始

### 前置要求

- [Claude Code](https://docs.claude.com) CLI（`claude` 命令可用）
- Python 3.8+
- Git
- Linux + systemd
- （可选）`inotify-tools` — 实现事件触发；不装也能用定时轮询
- （可选）`libnotify` — 桌面通知

### 一键安装

```bash
git clone https://github.com/ndzuki/obsidian-task-runner.git
cd obsidian-task-runner
./install.sh
```

### 非交互安装

```bash
OBSIDIAN_VAULT=/home/you/Obsidian/Vault \
NEW_PROJECT_ROOT=/home/you/src \
./install.sh --non-interactive
```

支持的环境变量见下方[配置说明](#配置说明)。

### 创建第一个任务

```bash
cp TASK-000-template.md "$OBSIDIAN_VAULT/Tasks/TASK-001-你的任务.md"
```

编辑 YAML frontmatter：

```yaml
id: "001"
title: "实现用户登录 API"
project: "my-backend"              # vault-map.json 中的项目 key
req_doc: "Requirements/用户登录API.md"
priority: P2
assignee: claude
```

在 `Requirements/` 下写对应的需求文档。保存后如果 systemd 开着，几秒内就会自动触发；也可以手动跑：

```bash
cd /path/to/your/project
claude -p "/obsidian-task-runner"
```

## 工作流

```
你                                   Claude Code
─────────────────────────────────────────────────────
1. 复制模板创建任务
2. 写需求文档
3. 保存任务文件
                         →          4. 发现任务 (ready)
                         →          5. 读需求，出计划
                         →          6. 写回计划，status=plan-review
                         →          7. 🔔 桌面通知
8. 在 Obsidian 审阅计划
9. 设 plan_approved: true
10. 保存
                         →          11. 发现任务 (plan-review + approved)
                         →          12. 实现代码
                         →          13. 运行测试/lint
                         →          14. task-verifier 验收
                         →          15. git commit
                         →          16. status=review
                         →          17. 🔔 桌面通知
18. Review 代码，合并
19. 设 status=done
```

## 目录结构

```
obsidian-task-runner/              # skill（安装到 ~/.claude/skills/）
├── SKILL.md                       # 核心：两轮状态机指令
├── reference.md                   # 参考：状态流转、字段、故障排查
├── scripts/
│   ├── find_ready_tasks.py        # 发现可处理任务
│   ├── update_task_status.py      # 更新 YAML frontmatter
│   ├── resolve_project_path.py    # 项目名 → 本地路径
│   ├── register_project.py        # 注册新项目
│   ├── notify_on_status_change.sh # 桌面通知
│   ├── task-runner-daemon.sh      # 调度脚本
│   └── task-watcher.sh            # inotify 监听
└── config/
    └── vault-map.example.json     # 项目映射模板

agents/task-verifier.md            # 验收 subagent
TASK-000-template.md               # 新任务模板
install.sh                         # 一键安装
```

## 配置说明

### vault-map.json

安装后位于 `~/.claude/skills/obsidian-task-runner/config/vault-map.json`：

```json
{
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

- `projects` — 已有项目名 → 本地绝对路径的映射
- `new_project_root` — 新项目统一创建在哪个目录下
- `templates` — 新项目脚手架模板定义
- `notifications.desktop` — 是否弹桌面通知
- `poll_interval_minutes` — systemd timer 兜底轮询间隔

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `OBSIDIAN_VAULT` | 交互提问 | Obsidian Vault 根目录 |
| `NEW_PROJECT_ROOT` | `$HOME/src` | 新项目默认创建目录 |
| `NOTIFY_ENABLED` | `true` | 是否启用桌面通知 |
| `POLL_INTERVAL_MINUTES` | `30` | 定时轮询间隔 |
| `SYSTEMD_ENABLED` | `true` | 是否注册 systemd 服务 |
| `SKILL_INSTALL_DIR` | `~/.claude/skills/obsidian-task-runner` | skill 安装路径 |

### Task 文档字段

完整字段说明见 [`reference.md`](obsidian-task-runner/reference.md)。关键字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | enum | `ready` → `plan-review` → `implementing` → `review` → `done` |
| `plan_approved` | bool | **人工 Gate**，设 `true` 触发 Round 2 |
| `auto_approve` | bool | 跳过人工确认（新项目无效） |
| `new_project` | bool | 从零创建新项目 |
| `project` | string | vault-map.json 的项目 key |
| `req_doc` | string | 需求文档相对路径 |

## 安全边界

以下操作**不会**自动执行：

- `git push` / 创建 PR / 合并 — 保留在本地分支
- 将任务标记为 `done` — 需要人工确认后手动改
- 新项目的脚手架创建 — 永远停在 Round 1 等人工确认
- 删除文件或分支 — 只增不改

## 通知机制

两个 Gate 都会发桌面通知（需要 `notify-send`）：

- Round 1 完成 → `📋 Task #003: 计划已生成，请审阅`
- Round 2 完成 → `✅ Task #003: 代码已实现，请 review`
- 执行失败 → `❌ Task #003: 执行失败`

## 常见问题

### 任务没有被自动处理？

1. 检查 status 是 `ready` 还是 `plan-review` + `plan_approved: true`
2. 检查 `assignee` 是否包含 `claude`
3. 确认 `project` 在 vault-map.json 中有映射，或 `new_project: true`
4. `tail -f ~/.claude/logs/task-runner.log`

### 没有桌面通知？

安装 `libnotify`：`sudo pacman -S libnotify`（Arch）或 `sudo apt install libnotify-bin`（Debian/Ubuntu）。确认有 notification daemon 在运行（dunst、mako 等）。

### 如何只用手动模式？

设置 `SYSTEMD_ENABLED=false` 或跳过 systemd 步骤。手动触发：

```bash
claude -p "/obsidian-task-runner [task_id]"
```

### 查看运行状态

```bash
# systemd 服务
systemctl --user status claude-task-watcher.service
systemctl --user list-timers | grep claude-task-runner

# 日志
tail -f ~/.claude/logs/task-watcher.log
tail -f ~/.claude/logs/task-runner.log

# systemd journal
journalctl --user -u claude-task-watcher.service -n 50
```

## 贡献

MIT License。欢迎 Issue 和 PR。

## License

MIT © 2026 ndzuki and contributors
