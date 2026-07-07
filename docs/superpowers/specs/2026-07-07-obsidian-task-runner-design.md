# Obsidian Task Runner — Design Spec

## 目标

让 Claude Code 自动读取 Obsidian 中的需求文档和任务文档，理解要求后自行开发实现到代码交付。封装为 skill，做到基本全自动发现任务并执行，`plan_approved: true` 时动态拉起 `claude -p` 执行任务。提供一键安装脚本。

## 架构

```
触发层                调度层              发现层                 执行层
inotifywait ───┐    task-runner-    find_ready_tasks.py    claude -p
(Tasks/ 变化)   ├──> daemon.sh  ───> resolve_project_  ──> /obsidian-task-runner
systemd timer ──┘    (遍历任务)       path.py               (两轮状态机)
```

两轮状态机：
- **Round 1（计划）**：读需求 → 出实现计划 → 写回 Task 文档 → status → `plan-review` → 🔔 桌面通知
- **人工 Gate**：在 Obsidian 审阅计划，设 `plan_approved: true`，保存
- **Round 2（实现）**：读批准的计划 → 实现代码 → 测试/lint → task-verifier → git commit → status → `review` → 🔔 桌面通知

`auto_approve: true` 且非新项目时，Round 1 → Round 2 同一次 `claude -p` 内完成。新项目永远停在 Round 1。

## 目录结构（最终态）

```
obsidian-task-runner/              # skill（装到 ~/.claude/skills/）
├── SKILL.md                       # 核心：两轮状态机指令（<300 行）
├── reference.md                   # 详细参考：状态流转、字段说明、故障处理
├── scripts/
│   ├── find_ready_tasks.py        # 发现可处理任务
│   ├── update_task_status.py      # 更新 YAML frontmatter
│   ├── resolve_project_path.py    # 项目名 → 本地路径
│   ├── register_project.py        # 注册新项目到 vault-map.json
│   └── notify_on_status_change.sh # 🔔 桌面通知（plan-review / review）
├── config/
│   └── vault-map.example.json     # 项目映射配置模板

task-runner-daemon.sh              # 调度脚本（skill/scripts/ 内）
task-watcher.sh                    # inotify 监听脚本（skill/scripts/ 内）

claude-task-runner.service         # systemd oneshot（兜底轮询）
claude-task-runner.timer           # systemd 定时器（30min）
claude-task-watcher.service        # systemd 常驻（事件触发）

agents/
└── task-verifier.md               # 验收 subagent

TASK-000-template.md               # 新任务模板
install.sh                         # 一键安装
README.md                          # 文档
```

## Task 文档模板字段

参考 JIRA 体系，YAML frontmatter：

```yaml
id: "003"
title: "..."
project: "user-service"           # vault-map.json 的 key
new_project: false
template: "go-gin-microservice"   # 新项目脚手架模板

# 状态流转（系统自动管理）
status: ready                     # ready → plan-review → implementing → review → done
plan_approved: false              # 👤 人工 Gate
created: 2026-07-07T14:30:00      # 自动填充
updated: 2026-07-07T14:30:00      # 自动更新
completed: ""                     # status=done 时自动填充

# 优先级 & 排期
priority: P2                      # P0-紧急 P1-高 P2-中 P3-低 P4-暂缓
due_date: "2026-07-14"
estimated_hours: 4
actual_hours: 0

# 人员 & 分工
assignee: claude
reviewer: ndzuki

# 范围 & 分类
req_doc: "Requirements/xxx.md"
component: api
tags: [api, auth]
epic: "用户系统-v2"
parent: ""
blocks: []
blocked_by: []

# 环境 & 部署
target_branch: ""
target_env: staging
```

## vault-map.json

```json
{
  "projects": {
    "user-service": {
      "path": "/home/nd/src/repos/github.com/ndzuki/user-service",
      "git_remote": "github.com/ndzuki/user-service"
    }
  },
  "new_project_root": "/home/nd/src/repos/github.com/ndzuki",
  "templates": {
    "go-gin-microservice": {
      "description": "Gin + Kustomize + kind 标准微服务",
      "scaffold_skill": "cloud-native-go",
      "structure": ["cmd/", "internal/", "api/", "deployments/"]
    }
  },
  "notifications": {
    "desktop": true,
    "sound": false
  }
}
```

## 通知机制

两个 Gate 都发桌面通知：
- Round 1 完成 → `notify-send "📋 Task #003 计划已生成，请审阅"`
- Round 2 完成 → `notify-send "✅ Task #003 代码已实现，请 review"`

实现：`task-runner-daemon.sh` 在 `claude -p` 结束后调 Python 脚本检查当前状态，对应状态发通知。

## task-verifier subagent

- 对照 Task 文档的验收标准清单逐条核实
- 运行测试 + lint
- 支持两种调用方式：skill 内部自动调用 + 独立 `/task-verifier` 调用

## 开源设计

### LICENSE
MIT License。skill 本身是 Claude Code 指令集 + 辅助脚本，MIT 最宽松、适配最广。

### 安装方式

**方式 1：交互式安装**
```bash
./install.sh
```
脚本逐个询问：Obsidian Vault 路径、new_project_root、是否启用桌面通知、定时轮询间隔。

**方式 2：非交互安装（CI / 脚本化部署）**
```bash
OBSIDIAN_VAULT=/home/me/vault \
NEW_PROJECT_ROOT=/home/me/src \
NOTIFY_ENABLED=true \
POLL_INTERVAL_MINUTES=30 \
./install.sh --non-interactive
```
所有配置项支持环境变量覆盖，跳过交互式提问。`--non-interactive` 模式下任何缺失的必要配置直接报错退出（而非fallback默认值）。

环境变量清单：
| 变量 | 默认值 | 说明 |
|------|--------|------|
| `OBSIDIAN_VAULT` | 交互提问 | Obsidian Vault 根目录 |
| `NEW_PROJECT_ROOT` | `$HOME/src` | 新项目统一创建目录 |
| `NOTIFY_ENABLED` | `true` | 是否启用桌面通知 |
| `POLL_INTERVAL_MINUTES` | `30` | 定时兜底轮询间隔 |
| `SKILL_DIR` | `$HOME/.claude/skills/obsidian-task-runner` | skill 安装路径 |
| `SYSTEMD_ENABLED` | `true` | 是否注册 systemd 单元 |

### README.md 结构

面向新用户的完整文档，按这个结构组织：

```
# Obsidian Task Runner
一句话定位：让 Claude Code 自动执行 Obsidian 中的开发任务。

## 是什么
- 解决的问题：Obsidian 里写好需求和任务后，手动切到终端开发太割裂
- 怎么做：inotify 监听 Tasks/ 变化 → 自动拉起 Claude Code 实现

## 快速开始
- 前置要求
- ./install.sh 一行安装
- 手动安装（展开详情）

## 工作流
- 创建任务（复制模板 → 填字段 → 放 Tasks/）
- systemd 自动触发，或在终端手动跑 /obsidian-task-runner
- 两轮状态机图示

## 配置说明
- vault-map.json 每个字段的含义
- Task frontmatter 字段说明
- 环境变量列表

## 通知机制
## 安全边界（什么不会自动做）
## 常见问题
## 贡献指南
## License
```

### install.sh

一键安装需要处理：
1. 依赖检查（claude, python3, git, inotifywait）
2. 安装 skill 到 `~/.claude/skills/obsidian-task-runner/`
3. 安装 task-verifier 到 `~/.claude/agents/`
4. 生成 vault-map.json（不覆盖已有）
5. 设置 OBSIDIAN_VAULT 环境变量（交互式 or 环境变量）
6. 创建 Tasks/ 和 Requirements/ 目录
7. 注册 systemd timer + watcher（可通过 SYSTEMD_ENABLED=false 跳过）
8. 探测 claude/go 路径填入 systemd PATH
9. 支持 `--non-interactive` 模式通过环境变量全量配置

## 实现任务清单

1. 创建 `LICENSE`（MIT）
2. 创建 `obsidian-task-runner/SKILL.md`
3. 创建 `obsidian-task-runner/reference.md`
4. 创建 `obsidian-task-runner/scripts/find_ready_tasks.py`
5. 创建 `obsidian-task-runner/scripts/update_task_status.py`
6. 创建 `obsidian-task-runner/scripts/resolve_project_path.py`
7. 创建 `obsidian-task-runner/scripts/register_project.py`
8. 创建 `obsidian-task-runner/scripts/notify_on_status_change.sh`
9. 创建 `obsidian-task-runner/config/vault-map.example.json`
10. 创建 `agents/task-verifier.md`
11. 创建 `TASK-000-template.md`
12. 迁移现有 `.sh` 到 `obsidian-task-runner/scripts/`
13. 重写 `install.sh`：新目录结构 + `--non-interactive` 模式 + 环境变量支持
14. 更新 systemd unit 文件中的路径引用
15. 重写 `README.md`：面向新用户的开源项目文档
16. 创建 `.gitignore`
17. Code review 现有脚本并修复问题
