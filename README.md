# Obsidian Task Runner

> 在 Obsidian 写需求，自动生成任务、制定计划、实现代码，并在你确认后创建 PR 和合并。

Obsidian Task Runner（命令 `otg`）把 Obsidian Vault 当作轻量的需求入口，把项目代码目录当作执行目标。你只需要写需求并在两个关键节点确认，其他步骤由 OMP Agent 和守护进程完成。

## 适合谁

- 用 Obsidian 管理需求，同时希望 AI 在真实 Git 仓库中实现代码。
- 需要保留计划、实现记录、验收结果和决策记忆，方便追溯。
- 希望 AI 不能未经确认就进入下一阶段或推送代码。

> 当前版本面向 Linux + systemd + OMP。其他操作系统可以使用单次命令，但没有内置的 systemd 常驻服务。

## 工作方式：只记住两个确认点

```text
写需求 REQ-xxx.md
        │
        ▼
自动创建 TASK-xxx.md ──(填写 project + assignee)──> ready
        │
        ▼
Round 1：生成实现计划 ──> plan-review
        │                         │ 你确认 plan_approved: true
        ▼                         ▼
等待计划确认                 Round 2：实现、测试、提交
                                  │
                                  ▼
                              review
                                  │ 你确认 merge_approved: true
                                  ▼
                         Merge：push、PR、合并
                                  │
                                  ▼
                                done
```

- `plan_approved: true`：允许 Agent 按计划写代码。
- `merge_approved: true`：明确允许执行远程 Git 操作和合并。
- 需求发生变化时会重新出计划，并再次停在 `plan-review`，不会跳过确认。
- 新项目第一次只生成脚手架方案，确认计划后才创建项目文件。

## 5 分钟安装

### 1. 准备依赖

需要安装：

- Go 1.24 或更高版本（从源码构建时需要）。
- `git`。
- `omp` 命令，并已配置可用模型。
- Linux 下建议使用 `systemd --user`；桌面通知还需要 `notify-send` 和通知服务。

### 2. 构建并安装 `otg`

在仓库根目录执行：

```bash
git clone https://github.com/ndzuki/obsidian-task-runner.git
cd obsidian-task-runner
make build
make install
```

这会把二进制复制到 `~/.local/bin/otg`。确认该目录在 `PATH` 中：

```bash
otg version
```

### 3. 安装 Skill、配置文件和守护进程

`otg install` 会安装 Skill、生成 `vault-map.json`、部署任务看板，并在启用时注册 systemd 单元：

```bash
otg install \
  --vault "$HOME/Documents/Obsidian/MainVault" \
  --new-project-root "$HOME/src"
```

常用选项：

| 选项 | 默认值 | 作用 |
|------|--------|------|
| `--vault` | `~/Documents/Obsidian/MainVault` | Obsidian Vault 路径 |
| `--new-project-root` | `~/src` | 新项目创建根目录 |
| `--notifications` | `true` | 开启桌面通知 |
| `--poll-interval` | `30` | systemd 兜底扫描间隔（分钟） |
| `--systemd` | `true` | 是否安装 user systemd 服务 |
| `--dry-run` | `false` | 只预览，不写入文件 |
| `--force` | `false` | 覆盖已有安装文件；谨慎使用 |

也可以使用环境变量：`OBSIDIAN_VAULT`、`NEW_PROJECT_ROOT`、`NOTIFY_ENABLED`、`POLL_INTERVAL_MINUTES`、`SYSTEMD_ENABLED`。

### 4. 配置项目映射

编辑：

```text
~/.omp/skills/obsidian-task-runner/config/vault-map.json
```

最小配置示例：

```json
{
  "obsidian_vault": "/home/you/Documents/Obsidian/MainVault",
  "projects": [
    {
      "name": "my-backend",
      "path": "/home/you/src/my-backend"
    }
  ],
  "new_project_root": "/home/you/src",
  "models": {
    "deepseek": "deepseek/deepseek-v4-pro:xhigh",
    "gpt": "gateway/gpt-5.5:xhigh",
    "flash": "deepseek/deepseek-v4-flash"
  },
  "notifications": { "desktop": true },
  "poll_interval_minutes": 30
}
```

`project` 必须匹配 `projects[].name`。`assignee` 必须匹配 `models` 的 key；未知 key 会回退到 `flash`。完整字段见 [`obsidian-task-runner/config/vault-map.example.json`](obsidian-task-runner/config/vault-map.example.json)。

### 5. 确认服务状态

```bash
systemctl --user status omp-task-watcher.service
systemctl --user list-timers | grep omp-task-runner
journalctl --user -u omp-task-watcher.service -n 50
```

如果暂时不想安装常驻服务，可以手动运行一次扫描：

```bash
otg daemon --once
```

## 第一个需求

需求文件放在 Vault 的 `Projects/<project>/Requirements/` 下，文件名使用 `REQ-<id>-<slug>.md`，例如 `REQ-001-login.md`：

```markdown
---
id: "001"
title: 用户登录 API
project: my-backend
priority: P2
tags: [auth]
---

## 要做什么
实现 JWT 鉴权的登录接口。

## 完成标准
- [ ] POST /api/login 返回 token
- [ ] 无效凭证返回 401
```

保存后 watcher 会创建对应的 `Projects/<project>/Tasks/TASK-001-login.md`。打开任务文件，补齐至少这些字段：

```yaml
project: my-backend
assignee: deepseek
```

通常不需要手动修改 `status`。当必填字段齐全且没有未完成依赖时，`blocked` 会自动变成 `ready`。

> 旧版 Vault 根目录的 `Requirements/REQ-xxx.md` 仍可使用；新项目推荐使用项目目录结构。

## Obsidian Dataview 看板

安装 Dataview 后，打开 Vault 根目录的 `Tasks-Dashboard.md`，即可查看任务汇总、待处理任务、阻塞任务和最近完成记录。

Dataview 的安装、字段格式、查询解释和常见问题见：[`docs/dataview.md`](docs/dataview.md)。

如果安装命令没有部署看板，可以手动复制仓库中的 [`Tasks-Dashboard.md`](Tasks-Dashboard.md) 到 Vault 根目录。Dataview 只负责读取和展示，不会替你修改任务状态。

## 状态与人工操作

| 状态 | 含义 | 你的操作 |
|------|------|----------|
| `blocked` | 缺少项目、执行者或依赖未完成 | 补齐 `project`、`assignee`，检查 `blocked_by` |
| `ready` | 可以生成计划 | 等待 daemon 执行 |
| `plan-review` | 计划已生成 | 审阅计划，确认后设 `plan_approved: true` |
| `implementing` | Agent 正在改代码 | 不要同时手改同一分支 |
| `review` | 本地实现已提交 | 审阅代码和验收记录，确认后设 `merge_approved: true` |
| `conflict` | 合并遇到冲突 | 手动解决并重新授权合并 |
| `done` | 已合并完成 | 任务结束 |
| `error` | 执行失败 | 查看日志和任务记录 |

Round 1 和 Round 2 只在本地创建分支、改文件和提交，不会 push。只有 `merge_approved: true` 才会进入 Merge Phase。

## 常用命令

| 命令 | 用途 |
|------|------|
| `otg install` | 安装 Skill、配置和 systemd |
| `otg install --dry-run` | 预览安装动作 |
| `otg daemon` | 常驻监听 Vault 并处理任务 |
| `otg daemon --once` | 扫描一次后退出 |
| `otg daemon --map-file <path>` | 使用指定的 `vault-map.json` |
| `otg find-ready <vault>` | 输出可执行任务（NDJSON） |
| `otg on-req-changed <vault> <req>` | 手动处理需求变化 |
| `otg update-status <task> key=value` | 原子更新任务 frontmatter |
| `otg resolve-path <map> <project>` | 查询项目本地路径 |
| `otg register-project <map> <name> <dir>` | 注册项目映射 |
| `otg version` | 查看版本 |

## 文件在哪里

| 路径 | 内容 |
|------|------|
| `~/.local/bin/otg` | Go 二进制 |
| `~/.omp/skills/obsidian-task-runner/` | Agent Skill、参考文档和配置 |
| `~/.omp/skills/obsidian-task-runner/config/vault-map.json` | Vault 与项目映射、模型映射 |
| `~/.omp/logs/` | daemon 和任务审计日志 |
| `~/Vault/Projects/<project>/Requirements/` | 你编写的需求 |
| `~/Vault/Projects/<project>/Tasks/` | Agent 自动创建和更新的任务 |
| `~/Vault/Projects/<project>/Notes/memory.md` | 项目累积记忆 |

## 故障排查

1. **没有生成 TASK**：确认文件名是 `REQ-<id>-<slug>.md`，并查看 `~/.omp/logs/otg-daemon.log`。
2. **TASK 一直是 `blocked`**：检查 `project` 是否存在于 `vault-map.json`，`assignee` 是否填写且是有效 model key，`blocked_by` 是否为空。
3. **没有自动执行**：查看 `systemctl --user status` 和 `~/.omp/logs/otg-daemon.log`；也可运行 `otg daemon --once` 验证配置。
4. **计划或代码没有继续**：确认对应 gate 字段已设为 `true`，保存任务文件后等待下一次扫描。
5. **看板为空**：确认已安装并启用 Dataview，查询来源目录是 `Projects`，任务位于 `Projects/<project>/Tasks/`，然后在 Obsidian 中重新加载索引。
6. **需要重新安装 Skill**：先执行 `otg install --dry-run`，确认路径无误后再执行 `otg install --force`。

更多状态字段、需求变更、断点续跑和冲突处理说明见 [`obsidian-task-runner/reference.md`](obsidian-task-runner/reference.md)。架构时序图见 [`docs/workflow.md`](docs/workflow.md)。

## 文档索引

- [`docs/dataview.md`](docs/dataview.md)：Dataview 安装和看板配置（推荐先读）。
- [`obsidian-task-runner/reference.md`](obsidian-task-runner/reference.md)：状态、字段、故障排查参考。
- [`obsidian-task-runner/SKILL.md`](obsidian-task-runner/SKILL.md)：Agent 执行规则。
- [`docs/workflow.md`](docs/workflow.md)：架构和完整业务流程。
- [`REQ-000-template.md`](REQ-000-template.md)：需求模板。
- [`TASK-000-template.md`](TASK-000-template.md)：任务模板。
- [`memory-template.md`](memory-template.md)：项目记忆模板。

## License

MIT © 2026 ndzuki and contributors
