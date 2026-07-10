# Obsidian Task Runner

> **你在 Obsidian 写一份需求，剩下的全部自动完成。**
>
> 需求 → 计划 → 代码 → PR → 合并，一条龙。

---

## 核心理念

```
用户只做一件事                    系统自动完成
─────────────────────────────────────────────
写 REQ-001-xxx.md      ──→   TASK-001-xxx.md  (任务文档)
                              NOTE-001-xxx.md  (项目记忆)
                                   │
                              Round 1: 出计划
                                   │  ⏸ plan_approved
                              Round 2: 写代码
                                   │  ⏸ merge_approved
                              Merge: PR + 合并
                                   │
                              ✅ done
```

**三份文档通过 id 关联**：`REQ-001` ↔ `TASK-001` ↔ `NOTE-001`，Agent 自主维护全部。

**纯 Go 二进制**：20 个 `.go` 文件，0 行 Python/Bash，5.3MB 静态编译，零运行时依赖。

**Token 零开销**：Agent 通过文件 I/O 读写记忆，不注入系统前缀，DeepSeek 缓存命中率 100%。

---

## 快速开始

```bash
# 前置要求: omp + go + git + systemd (Linux)
git clone https://github.com/ndzuki/obsidian-task-runner.git
cd obsidian-task-runner
make build && make install       # 编译 → ~/.local/bin/otg
otg install                      # 安装 skill → ~/.omp/skills/

# 写一份需求，保存
echo '---
id: "001"
title: 用户登录 API
project: my-backend
---

## 要做什么
实现 JWT 鉴权的登录接口。

## 完成标准
- [ ] POST /api/login 返回 token
- [ ] 无效凭证返回 401' > ~/Vault/Requirements/REQ-001-login.md

# 3 秒后，TASK + NOTE 自动创建
# 在 Obsidian 中填 assignee: deepseek，保存
# daemon 自动发现 → Round 1 出计划 → 等你确认
```

### 安装后的文件布局

| 路径 | 说明 |
|------|------|
| `~/.local/bin/otg` | Go 二进制（5.3MB 静态编译） |
| `~/.omp/skills/obsidian-task-runner/` | Skill 目录（SKILL.md + config） |
| `~/.omp/skills/.../config/vault-map.json` | 项目映射 + 模型配置 |
| `~/.omp/agent/skills/obsidian-task-runner` | → 上者的 symlink（OMP 读取端） |
| `~/.omp/logs/otg-daemon.log` | 守护日志（10MB 轮转，gzip 压缩，30 天清除） |
| `~/.omp/logs/tasks/` | Agent 审计日志（按任务/阶段分文件） |
| `~/.config/systemd/user/omp-task-*` | systemd 单元文件 |
| `~/.config/nvim/snippets/markdown.lua` | Neovim snippets（`oreq`/`otask`/`onote`） |
| `~/Vault/Tasks/` | 任务文档（Agent 自动创建+更新） |
| `~/Vault/Notes/` | 项目记忆（Agent 自动创建+维护） |
| `~/Vault/Requirements/` | 需求文档（用户编写） |

## 两轮状态机

```
 ○ ready              ← 新任务 / 需求变更
 │
 ▼  Round 1           → 读需求 → 生成计划 → 写回 TASK
 ○ plan-review        → 🔔 请审阅计划
 │  ⏸ 人工 Gate #1
 ▼  Round 2           → 写代码 → 测试 → lint → git commit
 ○ review             → 🔔 请 review 代码
 │  ⏸ 人工 Gate #2
 ▼  Merge Phase       → git push → gh pr create → merge
 ○ done               → 🎉 已完成
```

两个人工 Gate 确保你不会被 AI 的代码偷袭。需求变更时自动重新出计划，**永远停在 plan-review**。

---

## 模型路由

`assignee` → OMP 模型标识，由 `vault-map.json` 的 `models` 字段解析：

| assignee | OMP 模型标识 | 说明 |
|----------|-------------|------|
| `deepseek` | `deepseek/deepseek-v4-pro:xhigh` | 默认主力 |
| `gpt` | `gateway/gpt-5.5:xhigh` | GPT 替代 |
| `flash` | `deepseek/deepseek-v4-flash` | 轻量任务 + 未知回退 |
| `gemini` | `google/gemini-2.5-pro` | 内置 |
| `claude` | `anthropic/claude-sonnet-4-20250514` | 内置 |
| `minimax` | `minimax/minimax-m1` | 内置 |
| *任意 key* | 用户自定义 | 编辑 `models` 字段扩展 |
---

## `otg` 命令

| 命令 | 功能 |
|------|------|
| `otg install` | 一键安装（skill + systemd + OMP symlink） |
| `otg daemon` | 常驻守护（fsnotify 监听 + 30min 定时兜底） |
| `otg daemon --once` | 单次扫描 |
| `otg find-ready <vault>` | 列出就绪任务（NDJSON） |
| `otg on-req-changed <vault> <req>` | 需求变更 → 自动创建 TASK + NOTE |
| `otg update-status <task> key=val` | 原子更新 frontmatter |
| `otg resolve-path <map> <project>` | 项目名 → 路径 |
| `otg register-project <map> <name> <dir>` | 注册新项目到 vault-map |
| `otg version` | 版本 |

---

## 通知时间线

| 时机 | 通知 |
|------|------|
| 计划生成 | 📋 Task #001: 计划已生成，请审阅 |
| 开始实现 | 🚀 Task #001: 开始实现 |
| 代码完成 | ✅ Task #001: 代码已实现，请 review |
| PR 创建 | 📬 Task #001: PR 已创建 |
| 开始合并 | 🔀 Task #001: 开始合并 |
| 合并完成 | 🎉 Task #001: 已完成 |
| 合并冲突 | ⚠️ Task #001: 合并冲突 |
| 需求变更 | 🔄 Task #001: 需求变更已并入 |

---

## 项目结构

```
obsidian-task-runner/
├── cmd/otg/                    # Go 入口
├── internal/
│   ├── cli/                    # 8 个子命令
│   ├── daemon/                 # 常驻守护 + OMP 调度
│   ├── watch/                  # fsnotify 监听
│   ├── notify/                 # 桌面通知
│   ├── task/                   # 任务发现 + 需求变更 + 自动创建
│   ├── project/                # vault-map 读写
│   ├── logutil/                # 日志轮转 + gzip
│   └── install/                # 安装逻辑
├── pkg/yamlfrontmatter/        # YAML 解析 + 原子更新
├── obsidian-task-runner/       # skill 文件（安装到 ~/.omp/skills/）
│   ├── SKILL.md                # Agent 执行指令
│   ├── reference.md            # 状态流转参考
│   └── config/
├── NOTE-000-template.md        # 项目记忆模板
├── TASK-000-template.md        # 任务模板
├── REQ-000-template.md         # 需求模板（L1/L2/L3）
├── .github/workflows/          # CI + E2E
└── Makefile
```

---

## 项目记忆 (Notes/)

Agent 自主维护 `Notes/` 目录，记录技术决策、编码模式、已知坑位。

```
REQ-001-login.md  ──→  TASK-001-login.md  ──→  NOTE-001-login.md
                              │                      │
                         Round 1 决策           decision: JWT 方案
                         Round 2 发现           bug: bcrypt cost 问题
                              │                      │
                         状态联动               status: active → resolved
```

每条 note 通过 frontmatter 双向关联需求与任务，支持 `superseded` 链追溯决策演化。

> **用户不需要写 note** — Agent 在 Round 1/2 自动创建和维护，你只需要审计。

---

## 配置

`~/.omp/skills/obsidian-task-runner/config/vault-map.json`：

```json
{
  "obsidian_vault": "/home/you/Vault",
  "projects": [
    { "name": "my-backend", "path": "/home/you/src/my-backend" }
  ],
  "new_project_root": "/home/you/src",
  "notifications": { "desktop": true },
  "poll_interval_minutes": 30
}
```

---

## 文档索引

| 文档 | 内容 |
|------|------|
| [SKILL.md](obsidian-task-runner/SKILL.md) | Agent 执行的完整指令 |
| [reference.md](obsidian-task-runner/reference.md) | 状态流转、字段参考、故障排查 |
| [workflow.md](docs/workflow.md) | 架构图、时序图、Mermaid 流程图 |
| [go-rewrite-plan.md](docs/go-rewrite-plan.md) | Bash/Python → Go 迁移方案 |
| [NOTE-000-template.md](NOTE-000-template.md) | 项目记忆模板 |
| [TASK-000-template.md](TASK-000-template.md) | 任务文档模板 |
| [REQ-000-template.md](REQ-000-template.md) | 需求文档模板 |

## License

MIT © 2026 ndzuki and contributors
