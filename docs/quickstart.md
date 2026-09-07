# 快速上手（Quickstart）

> 5 分钟跑通：写一个需求，看它变成代码、PR 与合并。

## 1. 依赖

- Go 1.24+（从源码构建时需要）
- `git`
- `dsh` 命令，并已配置可用模型
- Linux 建议 `systemd --user`（其他平台可单次运行）
- 可选：Kitty 终端（`allow_remote_control yes`）——Grilling 交互时自动开新 tab
- 可选：`notify-send` + 通知服务——桌面提醒

## 2. 构建并安装

```bash
git clone https://github.com/ndzuki/obsidian-task-runner.git
cd obsidian-task-runner
make build
make install
```

`make install` 会把二进制复制到 `~/.local/bin/otg`，确认它在 `PATH` 中：

```bash
otg version
```

## 3. 安装 Skill、配置与守护进程

```bash
otg install \
  --vault "$HOME/Documents/Obsidian/MainVault" \
  --new-project-root "$HOME/src"
```

| 选项 | 默认值 | 作用 |
| --- | --- | --- |
| `--vault` | `~/Documents/Obsidian/MainVault` | Obsidian Vault 路径 |
| `--new-project-root` | `~/src` | 新项目 checkout 根目录 |
| `--notifications` | `true` | 桌面通知开关 |
| `--poll-interval` | `30` | systemd 兜底扫描间隔（分钟） |
| `--systemd` | `true` | 是否安装 user systemd 服务 |
| `--dry-run` | `false` | 只预览不写入 |
| `--force` | `false` | 强制覆盖安装文件（vault-map.json 里的用户配置不会丢失） |

等价环境变量：`OBSIDIAN_VAULT`、`NEW_PROJECT_ROOT`、`NOTIFY_ENABLED`、`POLL_INTERVAL_MINUTES`、`SYSTEMD_ENABLED`。

## 4. 配置项目映射

编辑 `~/.dsh/skills/obsidian-task-runner/config/vault-map.json`，最小配置：

```json
{
  "obsidian_vault": "/home/you/Documents/Obsidian/MainVault",
  "projects": [
    { "name": "my-backend", "path": "/home/you/src/my-backend" }
  ],
  "new_project_root": "/home/you/src",
  "models": {
    "default": "your-provider/your-model"
  }
}
```

完整字段（并发、超时、审计、KB、门禁等）都有代码默认值，缺省即工作。
需要调整时看 [`docs/config-reference.md`](config-reference.md) 或 `otg config show --effective`。

## 5. 确认服务状态

```bash
systemctl --user status otg-task-watcher.service
journalctl --user -u otg-task-watcher.service -n 50
curl -s http://127.0.0.1:8799/health        # agent-server 健康检查
```

`agent_server_managed`（vault-map）决定 agent-server 生命周期：

- `true`（默认）：daemon 自管子进程，健康检查以 `curl /health` 和 daemon 日志为准
- `false`：由外部 systemd 单元管理，`systemctl --user status dsh-agent-server.service` 为权威

**升级 daemon 用 `make deploy`**：一条命令完成构建（含知识库必需的 sqlite_fts5 tag）→ 全仓测试 → 安装 → 同步 skill/插件 → 补齐 vault-map 缺失的默认字段（绝不覆盖你的手工值）→ 重启 watcher。`make deploy-status` 看仓库与运行时的同步差异；`make rollback` 撤销回固定安装路径。

不想装常驻服务时，可单次扫描：

```bash
otg daemon --once
```

## 6. 第一个需求

在 Vault 的 `Projects/<project>/Requirements/` 下新建 `REQ-001-login.md`：

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

保存后，watcher 自动创建 `Projects/<project>/Tasks/TASK-001-login.md`。打开任务文件，
补齐至少两个字段：

```yaml
project: my-backend
assignee: acme
```

必填字段齐全且依赖满足后，任务从 `blocked` 自动变为 `ready`，随后进入流水线。
通常不需要手动改 `status`。

## 7. 状态与人工操作

| 状态 | 含义 | 你的操作 |
| --- | --- | --- |
| `blocked` | 缺字段或缺依赖 | 补 `project` / `assignee`，检查 `blocked_by` |
| `ready` | 就绪，等待优先级评估 | 无需操作，自动转入 `refining` |
| `refining` | 检查需求成熟度 | 无需操作；只有真争议才进 `needs-grilling` |
| `needs-grilling` | 等待你交互式对齐需求 | 在 Kitty tab 里回答问卷，提交后自动写回并关 tab |
| `planning` | 生成版本化实现计划 | 无需操作 |
| `plan-review` | 计划已生成 | `auto_approve: true`（默认）自动批准；否则审阅计划并设 `plan_approved: true` |
| `implementing` | Agent 正在改代码 | 不要同时手改同一分支 |
| `review` | 已提交，过独立完成审计后自动合并 | 无需操作，失败按通知处理 |
| `conflict` | 合并冲突（已自动尝试一次） | 手动解决并设 `merge_approved: true` |
| `done` | 已合并 | 结束；需求变更时自动回 `refining` |
| `closed` | 关闭（重复/取消/不予处理） | 终态 |

Round 1/2 只在本地建分支、改文件、提交，不会 push；进入 Merge Phase 需要
`merge_approved: true`——`auto_merge: true`（默认）时 daemon 先跑独立只读审计
（逐条 AC 复核证据），通过后自动授权。

## 8. 常用命令

| 命令 | 用途 |
| --- | --- |
| `make deploy` | daemon 升级标准路径（构建+测试+安装+同步+重启） |
| `make deploy-status` | 查看仓库 vs 运行时同步差异 |
| `make rollback` | 撤销 drop-in，回固定安装路径 |
| `otg install [--dry-run]` | 安装 Skill/配置/systemd |
| `otg daemon [--once]` | 常驻监听 / 单次扫描 |
| `otg status` | daemon 状态与运行中任务数 |
| `otg config show` | 当前配置（含来源标注） |
| `otg update-status <task> [key=value ...]` | 原子更新任务 frontmatter |
| `otg review <task>` | 任务的 review bundle |
| `otg kb search "<关键词>"` | 知识库本地检索（BM25 + 可选语义混合） |
| `otg kb ask "<问题>"` | 知识库问答（混合检索 + 生成，带引用） |
| `otg kb index` | 构建语义向量索引（配置 kb_embedding 后执行一次） |
| `otg validate-doc <path>` | 校验任意文档（TASK/REQ/ADR 自动识别） |
| `otg repair-doc <task>` | 修复损坏的 frontmatter |

## 9. Obsidian Dataview 看板

安装 Obsidian 的 Dataview 插件后，打开 Vault 根目录的 `Tasks-Dashboard.md` 即可
看到任务汇总、阶段看板、待办统计、审批与阻塞队列。Dataview 只读不写。
安装与字段说明见 [`docs/dataview.md`](dataview.md)。
