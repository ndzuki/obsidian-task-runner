# obsidian-task-runner 架构（DSH 时代）

> 本文是当前实现的权威架构说明（2026-08 起，`refactor/dsh-architecture`）。
> 早期规划文档（`phase5-executor-migration.md` / `embed-migration-plan.md` /
> `go-rewrite-plan.md` / `refactor-architecture.md`）为历史资料，其中 OMP
> 执行器描述已被本文取代。运行时契约见 `obsidian-task-runner/SKILL.md`
> 与 `reference.md`；完整流程见 `workflow.md`。

## 1. 总览

```text
                    ┌────────────────────────── Obsidian Vault ──────────────────────────┐
                    │ Requirements/REQ-*.md   Tasks/TASK-*.md   Notes/CONTEXT.md         │
                    │ Design/（glossary · contracts · decisions · waves）                │
                    │ References/（知识库 markdown + INDEX）                              │
                    └────────────────────────────────┬───────────────────────────────────┘
                                                     │ fsnotify watcher + 每 10s scan
                                                     ▼
┌─────────────────────────── otg daemon — otg-task-watcher.service ──────────────────────────┐
│ 每轮 scan：schema 同步 → 依赖健康 → stale-done 检测 → KB 摄入/同步 →                        │
│    老化兜底恢复（autoResumeAgedBlocks，窗口可配置）→ 依赖链恢复 → 状态机派发             │
│ 状态机：ready → refining → planning → plan-review → implementing → review → merge → done  │
│ 门禁：阶段并发 / plan_files 重叠串行 / round2 空转冷却 / quota 指数退避 / 入口门禁          │
│ 交付：git worktree（task/<id>-slug）→ push → PR → CI 轮询 → 合并 → 知识提炼               │
└───────┬──────────────────────────────────────────────────────┬────────────────────────────┘
        │ 阶段执行（cfg.executor）                              │ 模型路由（免费优先）
        ▼                                                       ▼
┌───────────────────────────────────┐        ┌──────────────────────────────────────────────────┐
│ dsh-agent-server.service（默认后端） │        │ default   → deepseek_magic/gpt-5.4-mini（轻量） │
│   dsh --profile headless-agent-    │        │ deepseek  → deepseek_magic/deepseek-v4-pro（重度）│
│   server —— 常驻 RPC，127.0.0.1:8799│ ◄────► │ gpt/openai→ openai/gpt-5.6-sol（fallback/手动） │
│   dsh-embed executor：会话持久化，   │  RPC   │ ds-official→ 自费官方，仅 assignee 手动指定      │
│   支持 executor_session_id 断点续跑 │        │ gemini/claude/minimax → 显式 assignee 可选      │
└───────────────┬───────────────────┘        └──────────────────────────────────────────────────┘
                │ 或 cfg.executor="dsh"：每个阶段 spawn `dsh --profile headless`（无持久会话）
                ▼
┌────────────────────────────────────┐      ┌───────────────────────────────┐
│ 阶段 skill（~/.dsh/skills/…）       │      │ dsh-web.service（可选 Web UI）  │
│ refining / round1 / round2 / merge │      └───────────────────────────────┘
│ priority / pm / split / audit …    │
└────────────────────────────────────┘
```

## 2. 进程与服务（systemd --user）

| 单元 | 命令 | 职责 | 生命周期 |
|------|------|------|---------|
| `otg-task-watcher.service` | `otg daemon` | 扫描/状态机/派发/git 交付/知识库 | 常驻；`Restart=on-failure` |
| `dsh-agent-server.service` | `dsh --profile headless-agent-server` | 长连接阶段会话 RPC（dsh-embed 后端） | 常驻；otg 单元 `Requires+After` 它 |
| `dsh-web.service` | `dsh --profile web` | DSH Web UI | 常驻（可选） |

关键不变量：**`make deploy` 重启 watcher 时 agent-server 保持运行**——
在飞的实现/审计会话因此可持久恢复，daemon 重启不再打断阶段执行
（对应知识库 `core/daemon-stuck-task-patterns.md` 模式 7 的根修）。

## 3. 阶段执行后端（cfg.executor）

- `dsh-embed`（**默认**）：`127.0.0.1:8799` 上 agent-server 的 RPC。每阶段创建
  会话、按 phase 传 reasoningEffort；中断的会话把 `executor_session_id`
  持久化到任务 frontmatter，恢复时先 resume，失败再 fresh start
  （`internal/daemon/phase_executor.go`）。
- `dsh`：每阶段临时 spawn `dsh --profile headless`（无持久会话，一次性进程）。
- `omp`：冻结的旧执行器，仅历史兼容，默认不走。

## 4. 模型路由（免费优先）

见 `internal/config/config.go` `DefaultModels()`。daemon 按任务 `assignee`
（映射 key）选择 DSH route；未知 key 回退 `default`。**免费渠道是默认**，
`ds-official`（自费）永不自动选用——仅在 assignee 显式指定时使用。

## 5. 失败恢复层级（从快到慢）

| 层 | 触发 | 行为 |
|----|------|------|
| 阶段内重试 | refining/planning 首次失败 | 记 `refine_retry_count`/`planning_retry_count`，下一轮 scan 自动重试一次 |
| **活动会话续期（2026-08 TASK-065）** | 阶段 HTTP 等待超过 phase timeout，但 agent-server 会话近期仍有事件（模型还在推 step/工具调用） | 判 `timeout_active`：不 cancel、不计失败、不转 blocked；保留 `executor_session_id`，下一轮 scan 继续等待同一会话。只有长时间无事件的会话才按 wedged cancel |
| quota 退避 | `MODEL_QUOTA_EXHAUSTED` | `quota_backoff_until` 指数退避（2m→4m→…→4h），到期前不重派；重启不清零 |
| API key 探测 | `API_KEY_UNAVAILABLE` | 每轮 scan 探测，恢复即自动 resume |
| 重启中断自愈 | `PHASE_INTERRUPTED` | 重启后自动重派（daemon 优雅停机路径） |
| 依赖链拉起 | 上游 blocked 且错误可自动恢复 | 下游任务通过 `blocked_by` 自动 `resume_approved=true`，预算 ≤2 次 |
| 入口门禁事实恢复 | `PREREQUISITE_SMOKE_FAILED` | 全部上游 done 且 phase_error 清空才放行（不按时间恢复） |
| **老化兜底（2026-08 新增）** | `status=blocked` + 可自动恢复错误 + 阻断超过窗口 + 预算未耗尽 | `autoResumeAgedBlocks()` 每轮 scan 自动 `resume_approved=true`；窗口 = `auto_resume_aged_after_hours`（默认 24h）；基线时间取 `blocked_at`（缺失回退 `updated`）；覆盖 MODEL_FAILED/QUOTA/TIMEOUT/INTERRUPTED/空码 与 DESIGN_SESSION_FAILED；预算 `auto_resume_count ≥ maxAutoResumeAttempts(2)` 则转人工 |

进入 blocked 的写点统一盖 `blocked_at` 时间戳；恢复（`restoreBlockedPhase`）清空。
设计动机：daemon 迭代/重启丢内存状态、叶子任务（`blocks: []`）无下游拉起等
场景，阻断超过一天即视为环境性而非人为决策，自动再试（对应
`internal/daemon/aged_auto_resume.go`）。

## 6. 一个任务的完整旅程（数据流）

1. watcher 发现 `REQ-xxx.md` → 生成 `TASK-xxx.md`（status=blocked→ready，补 project/assignee）。
2. scan 派发 `refining`（DSH 会话）→ fully_mature → `planning`（plan_version≥阈值时先过 replan gate 全局设计会话）。
3. `plan-review` →（auto_approve 默认 true）→ `implementing`：建 worktree → round2 会话 → checkpoint commit → `review`。
4. 独立审计会话逐条复核 AC → `auto_merge` 授权 → push/PR/CI/merge → `done` → 知识提炼入库（SQLite FTS5 + 向量）。
5. 任何阶段失败按 §5 层级恢复；人为决策块（REQ_MISSING/DOCUMENT_INVALID 等）不自动恢复。

### 全局设计会话（replan gate）契约（2026-08-24 TASK-065 修复后）

- 会话 `WorkingDir` = Vault 的 `Design/` 目录（workspace-write 沙箱范围即制品树）；
  `repo_dir` 作为只读证据路径经 prompt 传入，不再把仓库当工作目录。
- daemon 派发前对 `Design/` 做写探针（秒级失败，不烧 10-90 分钟会话）；探针失败
  → `DESIGN_TARGET_UNWRITABLE`（确定性环境缺陷，不做 24h 老化盲重试）。
- 会话成功后 daemon 校验**真实** `Design/` 四类制品；无效时回退校验并导入
  `repo/.design-stage/Projects/<proj>/Design`（旧契约会话的暂存产物，daemon
  侧有 Vault 写权限）——导入后仍无效才报 `DESIGN_SESSION_FAILED`。
- replan gate 的设计会话**不持 repo 锁**（只读仓库、只写 Vault），避免
  长会话饿死同 repo 其他任务的 worktree 准备（`repo busy` 风暴）。
- PM consolidate 的 prompt 注入 `<dependency_context>`（blocked_by/blocks/
  depends_on/设计库清单），跨 REQ 契约冲突与「设计库为空会被 gate 拦截」
  在 grilling 之前进入决策清单。

## 7. 运维速查

```bash
make build            # go build -tags sqlite_fts5（知识库必需）
make deploy           # 重建二进制 + 单测 + 同步 skill/插件 + 写 drop-in override + 重启 watcher（agent-server 存活）+ 条件重启 agent-server；install-force 为其别名
systemctl --user status otg-task-watcher dsh-agent-server dsh-web
tail -f ~/.dsh/logs/otg-daemon.log              # daemon 主日志
ls ~/.dsh/logs/tasks/                           # 每任务阶段日志/审计日志
ls ~/.dsh/sessions/                             # DSH 会话持久化（zstd jsonl，按 workdir）
```

## 8. 关键目录

| 路径 | 内容 |
|------|------|
| `~/.dsh/skills/obsidian-task-runner/` | 运行时 skill 包（SKILL.md/reference.md/skills/，`sync-docs` 同步） |
| `~/.dsh/skills/…`（顶层独立 skill） | refining/round1/round2/merge/priority/pm/split/knowledge-base/kulala-http |
| `~/.dsh/config/` | 配置（vault-map.json 在 skill 包 `config/` 下） |
| `~/.config/systemd/user/` | 三个 user 单元 |
| `<repo>/.otg-worktrees/` 或 `~/.otg-worktrees/` | 任务 worktree（按配置） |
