# Obsidian Task Runner

> 在 Obsidian 写需求，自动生成任务、制定计划、实现代码，并在你确认后创建 PR 和合并。

Obsidian Task Runner（命令 `otg`）把 Obsidian Vault 当作轻量的需求入口，把项目代码目录当作执行目标。你只需要写需求并在三个交互点确认，其他步骤由 OMP Agent 和守护进程自动完成。

## 适合谁

- 用 Obsidian 管理需求，同时希望 AI 在真实 Git 仓库中实现代码。
- 需要保留计划、实现记录、验收结果和决策记忆，方便追溯。
- 希望 AI 不能未经确认就进入下一阶段或推送代码。

> 当前版本面向 Linux + systemd + OMP。其他操作系统可以使用单次命令，但没有内置的 systemd 常驻服务。

## 工作方式：你只需要参与三次交互
```text
写需求 REQ-xxx.md
        │
        ▼
自动创建 TASK-xxx.md ──(填写 project + assignee)──> ready
        │
        ▼
refining（headless 成熟度检查）
        │
        ├── fully_mature ──> planning（生成版本化实现计划）
        │                         │
        │                         ▼
        │                    plan-review
        │                         │ 你确认 plan_approved: true
        │                         ▼
        │                    Round 2：实现、测试、提交
        │
        └── needs input ──> needs-grilling
                                  │ Kitty tab 通知，你完成对话
                                  ▼
                              refining（复验）→ planning → …

Round 2 完成后：
        │
        ▼
    review ──(你确认 merge_approved: true)──> Merge：push、PR、合并 ──> done
```

- **Grilling 对话**：AI 在 Kitty tab 中逐项追问，你确认方向。完成后自动回到成熟度检查。
- `plan_approved: true`：允许 Agent 按计划写代码。
- `merge_approved: true`：明确允许执行远程 Git 操作和合并。
## 5 分钟安装

### 1. 准备依赖

需要安装：

- Go 1.24 或更高版本（从源码构建时需要）。
- `git`。
- `omp` 命令，并已配置可用模型。
- Linux 下建议使用 `systemd --user`。
- **推荐**：Kitty 终端（`allow_remote_control yes`）用于 Grilling 通知时自动创建新 tab。同一 TASK 只会保留一个活跃 Grilling tab；daemon 会跨 Kitty 窗口按任务 ID 去重，任务标题变化或 daemon 重启不会重复创建。
- 桌面通知还需要 `notify-send` 和通知服务。`kitty @ ls` 失败时 daemon 使用本次尝试写入的 5 分钟 debounce 阻止重复创建；Kitty JSON 无法解析时也不会冒险创建 tab，而是使用桌面通知 fallback 并等待后续扫描重试。


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
| `--force` | `false` | 强制覆盖安装文件；`vault-map.json` 中的用户配置（项目映射、模型）不会丢失 |

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
    "gpt": "gateway/gpt-5.6-sol:xhigh",
    "default": "deepseek/deepseek-v4-flash"
  },
  "notifications": { "desktop": true },
  "poll_interval_minutes": 30,
  "max_concurrent_tasks": 2
}
```

`project` 必须匹配 `projects[].name`。`assignee` 必须匹配 `models` 的 key；未知 key 会回退到 `default`。完整字段见 [`obsidian-task-runner/config/vault-map.example.json`](obsidian-task-runner/config/vault-map.example.json)。

### 并发任务

`max_concurrent_tasks` 是 daemon 同时运行的 **OMP headless 进程**上限，默认 `2`；设为小于 `1` 的值时按 `1` 执行。等待仓库独占许可或准备 worktree 的任务不占用该额度。调高该值会同时增加模型请求、token 消耗和本机 CPU/内存占用。

- **不同项目仓库**：只要有空闲 OMP 槽位即可并行执行。
- **同一仓库的 Round 2**：daemon 先在仓库短锁内创建或复用 `~/.omp/worktrees/` 下的任务专属 Git worktree，再释放仓库锁；实际 OMP 在独立 worktree 中运行，可与同仓库其他 Round 2 并行。
- **任务分支绑定**：如果 TASK frontmatter 已有 `target_branch`，daemon 创建或复用 worktree 时会绑定并校验该分支；若分支不存在则通过 `git worktree add -b <target_branch>` 创建。已有 worktree 分支不匹配时拒绝执行，避免代码写入错误分支。
- **空分支字段兼容**：尚未进入 Round 2 的任务可以保留 `target_branch: ""`。daemon 先提供任务专属 worktree，agent 在其中创建 `task/<id>-<slug>`；Round 2 完成后把实际分支写回 `target_branch`。
- **Planning / Refining 阶段**：既不需要仓库，也不持锁。可与其他阶段并行执行。
- **避免队头阻塞**：等待同仓库独占许可的任务保留在调度队列中，不占 OMP 槽位。排在其后的、已经有独立 worktree 的 Round 2 可以填补空闲槽位。例如 `Merge A → Merge B → Round 2 C` 且上限为 `2` 时，实际先并行运行 `Merge A + Round 2 C`，`Merge B` 等待 `Merge A` 完成。
- **安全边界**：Round 2 可与同仓库 Merge 并行，是因为它使用独立 worktree；多个 Merge 或新项目任务仍不会同时修改主工作区。Planning / Refining 阶段不使用仓库。
- **任务身份与恢复**：运行去重、PID 文件和审计日志基于任务文件路径，而非单独的 `id`；不同项目可安全使用相同任务编号。

修改 `max_concurrent_tasks` 或安装新调度器二进制后，常驻 watcher daemon 需要重启才能生效；`otg daemon --once` 会在每次启动时读取配置。

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
| `ready` | 已就绪 | daemon 自动转入 `refining` |
| `refining` | 正在 headless 检查需求成熟度 | 无需操作；成熟后自动进入 planning 或 needs-grilling |
| `needs-grilling` | 等待你交互式对话对齐需求或解决阻塞 | 在 Kitty 新 tab 中与 OMP 对话，完成后自动恢复 |
| `planning` | 正在生成版本化实现计划 | 无需操作；成功后进入 plan-review |
| `plan-review` | 计划已生成 | 审阅计划 + ADR 提议，确认后设 `plan_approved: true` |
| `implementing` | Agent 正在改代码 | 不要同时手改同一分支；可能卡住回到 `needs-grilling` |
| `review` | 本地实现已提交 | 审阅代码和验收记录，确认后设 `merge_approved: true` |
| `conflict` | 合并遇到冲突 | 手动解决并重新授权合并 |
| `done` | 已合并完成 | 任务结束 |

Round 1 和 Round 2 只在本地创建分支、改文件和提交，不会 push。只有 `merge_approved: true` 才会进入 Merge Phase。Round 2 遇到阻塞时会暂停为 `needs-grilling`，等待你交互式解决问题后自动恢复。

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
| `otg update-status <task> [key=value ...]` | 原子更新任务 frontmatter |
| `otg validate-doc <path>` | 校验任意文档（自动识别 TASK/REQ/ADR）+ body tag 扫描 |
| `otg repair-doc <task>` | 修复损坏的 frontmatter + body tag 自动转义 |
| `otg resolve-path <map> <project>` | 查询项目本地路径 |
| `otg register-project <map> <name> <dir>` | 注册项目映射 |
| `otg version` | 查看版本 |
| `otg write-adr <dir> <id> <title> <content>` | 原子写 ADR 文件 + 校验 |
| `otg validate-adr <path>` | 校验 ADR frontmatter 结构 |

## 文件在哪里

| 路径 | 内容 |
|------|------|
| `~/.local/bin/otg` | Go 二进制 |
| `~/.omp/skills/obsidian-task-runner/` | Agent Skill、参考文档和配置 |
| `~/.omp/skills/obsidian-task-runner/config/vault-map.json` | Vault 与项目映射、模型映射 |
| `~/.omp/logs/` | daemon 和任务审计日志 |
| `~/Vault/Projects/<project>/Requirements/` | 你编写的需求 |
| `~/Vault/Projects/<project>/Tasks/` | Agent 自动创建和更新的任务 |

## 故障排查

1. **没有生成 TASK**：确认文件名是 `REQ-<id>-<slug>.md`，并查看 `~/.omp/logs/otg-daemon.log`。
2. **TASK 一直是 `blocked`**：检查 `project` 是否存在于 `vault-map.json`，`assignee` 是否填写且是有效 model key，`blocked_by` 是否为空。
3. **没有自动执行**：查看 `systemctl --user status` 和 `~/.omp/logs/otg-daemon.log`；也可运行 `otg daemon --once` 验证配置。
4. **计划或代码没有继续**：确认对应 gate 字段已设为 `true`，保存任务文件后等待下一次扫描。
5. **看板为空**：确认已安装并启用 Dataview，查询来源目录是 `Projects`，任务位于 `Projects/<project>/Tasks/`，然后在 Obsidian 中重新加载索引。
6. **任务 frontmatter 损坏（"parse error"）**：运行 `otg validate-doc <task>` 诊断（现在会同时检查必填字段），`otg repair-doc <task>` 修复（可恢复块标量、列表，并将损坏的双引号标量转为块标量）。修复后 `validate-doc` 应输出 `frontmatter OK`。
7. **需要重新安装 Skill**：先执行 `otg install --dry-run`，确认路径无误后再执行 `otg install --force`。用户的 `vault-map.json`（项目映射、模型配置）不会被覆盖。

更多状态字段、需求变更、断点续跑和冲突处理说明见 [`obsidian-task-runner/reference.md`](obsidian-task-runner/reference.md)。架构时序图见 [`docs/workflow.md`](docs/workflow.md)。

## 文档索引

- [`docs/dataview.md`](docs/dataview.md)：Dataview 安装和看板配置（推荐先读）。
- [`obsidian-task-runner/reference.md`](obsidian-task-runner/reference.md)：状态、字段、故障排查参考。
- [`obsidian-task-runner/SKILL.md`](obsidian-task-runner/SKILL.md)：Agent 执行规则。
- [`docs/workflow.md`](docs/workflow.md)：架构和完整业务流程。
- [`REQ-000-template.md`](REQ-000-template.md)：需求模板。
- [`TASK-000-template.md`](TASK-000-template.md)：任务模板。

## License

MIT © 2026 ndzuki and contributors
