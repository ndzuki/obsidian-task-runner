> ⚠️ **历史规划文档**：本文件是 DSH 重构期间的规划记录，已由当前实现的
> 权威架构说明 [docs/architecture.md](architecture.md) 取代。执行器现状：
> `dsh-embed`（默认，agent-server RPC + durable resume），`dsh`（spawn），
> `omp`（冻结兼容）。阅读本文请对照 architecture.md，勿按旧内容实施。

# obsidian-task-runner × DSH 完整融合（embed）方案

> 2026-08-19。从「spawn 短命进程」迁移到「长驻 agent-server + RPC」，
> 解决三个 spawn 模式的已知限制：推理强度失效、无 durable resume、每阶段启动开销。

## 1. 背景与目标

spawn 模式（`dsh --profile headless <task>`）的问题：

| 限制 | spawn 现状 | embed 解决 |
|---|---|---|
| 推理强度 | headless 无 `--thinking`；`reasoningEffort` 无法 per-调用传 | `agentOptions.reasoningEffort` 原生字段 |
| resume | 无（进程退出即失，daemon 重启靠 frontmatter 重派发） | `agents.resume({resumeSessionId})` durable 恢复 |
| 启动开销 | 每阶段 spawn 新 Node 进程（~0.04s + 模型路由初始化） | 长驻一个 agent-server 进程复用 |

调研确认（rc.7 源码）：`agents.create({sessionId, agentOptions:{provider,model,reasoningEffort}, setup})`
与 `agents.resume({resumeSessionId})` 是 DSH 原生 API；`assertAgentOptions` 仅校验
`maxTokens`，`reasoningEffort` 透传——所以 embed 能完整还原 omp 的 `--thinking` per-阶段语义。

## 2. 架构

```
otg daemon (Go)
  │ HTTP RPC (localhost:PORT)
  ▼
dsh agent-server（长驻 Node 进程，自定义 profile headless-agent-server）
  │ ctx.agents.create/resume（Node 原生 API，含 reasoningEffort + durable sessionId）
  ▼
DSH agent runtime（模型路由 / fallback.mjs / skill 注入 / 工具）
```

daemon 生命周期：
- 启动：拉起 `dsh --profile headless-agent-server`（长驻），等待健康检查通过
- 阶段派发：HTTP `POST /agent/run`（含 reasoningEffort + sessionId）
- 停止：优雅关闭 agent-server

## 3. RPC 契约

```
POST /agent/run
  body: {
    task: string,                 // 已注入 SKILL.md 正文的任务文本
    provider: string, model: string,
    reasoningEffort: string,      // off/low/high/max（omp 语义）
    sessionId?: string            // 首次为空（新建）；resume 时传 durable id
  }
  → 200 { text, outcome, sessionId, errorCode? }
    outcome: success | failed | timeout | interrupted | quota | key_unavailable
```

## 4. 修改面

### DSH 侧（2 个新文件）

| 文件 | 内容 |
|---|---|
| `~/.dsh/plugins/agent-server.mjs` | cordis 插件：`webServer.register` 暴露 `/agent/run`；handler 内 `agents.create/resume` + `followup` + `whenIdle` + summarize session.events |
| `~/.dsh/profiles/headless-agent-server/` | profile：dsh-base + dsh-headless 基础 + webserver + agent-server 插件 + 端口配置 |

### Go 侧（3 个文件改造）

| 文件 | 改动 |
|---|---|
| `internal/daemon/dsh_executor.go` | 新增 `dshEmbedExecutor`（HTTP client）：Start 发 `/agent/run`，Wait 收结果，映射 ExecOutcome |
| `internal/daemon/daemon.go` | daemon 启动/停止时管理 agent-server 子进程（`newPhaseExecutor` 选 embed 时） |
| `internal/config/config.go` | 新增 `agentServerAddr`（默认 `127.0.0.1:8799`）+ `executor: "dsh-embed"` 取值 |

### 配置（1 处）

| 文件 | 改动 |
|---|---|
| `~/.dsh/settings.yaml` 或 vault-map | `executor: "dsh-embed"` 时启用 embed 路径 |

## 5. 分步实施（每步可独立验证）

| 步 | 内容 | 验证 | 状态 |
|---|---|---|---|
| E1 | DSH 侧 agent-server.mjs 插件 + profile | `curl POST /agent/run` 跑通一个任务，断言 reasoningEffort 生效 + sessionId 持久化 | ✅ **已完成（2026-08-20）** |
| E2 | Go 侧 dshEmbedExecutor（HTTP client） | 单测（httptest stub server）+ 与 E1 联调 | ✅ **已完成（2026-08-20）** |
| E3 | daemon 生命周期（启动/关闭 agent-server） | daemon 启停冒烟 | ✅ **已完成（2026-08-20）** |
| E4 | resume 路径（sessionId 持久化到 frontmatter executor_session_id） | 中断后 resume 一致 | ⏸️ 暂缓（见下） |
| E5 | 默认切 dsh-embed + planning 复测（验证推理强度解决跑偏） | planning 真实冒烟 | ✅ **planning 复测通过（2026-08-20）** |

### E5 planning 复测记录（2026-08-20，里程碑）

用隔离 smoke-vault 的合法 TASK-001（独立 REQ-001，status=planning + maturity=
fully_mature）经 agent-server `/agent/run` 跑 round1，`reasoningEffort=high`：

- ✅ **reasoningEffort 传入**：session 日志确认 `"reasoningEffort":"high"`（embed
  的 per-request 推理强度生效，解决 spawn 模式的 §5.6 短板）。
- ✅ **planning 首次完整成功**（此前所有 spawn 冒烟均失败）：
  - `status: planning → plan-review`
  - `plan_version: 0 → 1`（计划 v1 生成）
  - `plan_req_hash` 与 refine_req_hash 一致（Step 4 门禁通过）
  - 正文含完整计划（AC-1~AC-4、风险、变更记录、成熟度评估）
- 耗时约 90s（high 推理强度下模型深度探索：读 otg 源码理解 update-status 写回机制）。

**结论**：推理强度失效是 planning 收敛失败（§5.4/§5.6）的**根因**；embed 的
`agentOptions.reasoningEffort` 完整还原 omp `--thinking` per-阶段语义，planning
在 dsh-embed 下可靠收敛。可以推进「默认切 dsh-embed + 替换 omp」。

### round2 冒烟记录（2026-08-20，dsh-embed 全阶段验证闭环）

用同一个 smoke-vault TASK-001（plan-review + plan_approved=true + plan_files=
go.mod/main.go/main_test.go）经 agent-server `/agent/run` 跑 round2，
`reasoningEffort=xhigh`（max → xhigh 映射）：

- ✅ **完整实现**：Tracer Bullet 逐 AC 实现——`go.mod`（module demo）+ `main.go`
  （`normalize` 纯函数：TrimSpace + Fields/Join 折叠空白 + `ErrEmptyInput`/
  `ErrInputTooLong` + `maxInputRunes=256`）+ `main_test.go`（table-driven）。
- ✅ **测试通过**：`go test ./...` → `ok demo`。
- ✅ **commit + 写回**：`ea72999 feat: ...`；status `implementing → review`。
- 耗时约 4 分钟（xhigh 深度实现：Pre-flight → Tracer Bullet → AC 实现 → commit）。

**结论**：dsh-embed 全阶段验证闭环——refining（spawn 时代已验证）+ priority
（spawn 已验证）+ planning（E5，high）+ **round2（xhigh，本轮）** 全部在 dsh 下
可靠工作。per-阶段推理强度（`ompPhaseThinking` → `mapDSHEffort`）完整生效，
dsh-embed 具备生产可用性。

### E3 验证记录（2026-08-20）

- config 新增 `agent_server_addr`（默认 `127.0.0.1:8799`）+ executor 取值加
  `dsh-embed`（validate/mergeDefaults/defaults 三处）。
- `newPhaseExecutor` 选 dsh-embed 返回 dshEmbedExecutor。
- Runner 加 `agentServerCmd` + `daemon_agent_server.go`：startAgentServer 拉起
  `dsh --profile headless-agent-server` + 30s 健康检查；stopAgentServer SIGTERM
  →10s→SIGKILL。Run/RunOnce 挂载（dsh-embed 时启动/收口，其他 executor no-op）。

### E4 完成状态：Resume 核心 + sessionId 持久化 ✅（2026-08-20）

- **dshEmbedExecutor.Resume 实现**：JSON token（sessionId + provider/model/
  skillPrompt/effort）解码后重新发 `/agent/run` 带 sessionId，agent-server
  恢复会话而非新建（`agents.resume`）。
- **ResumeToken 编码**：dispatch 的 `ExecutionResult.ResumeToken` 从裸
  sessionId 改为 JSON（含 spec 字段，供 Resume 重建请求）。
- **sessionId 持久化**：runDSHPhaseDispatch 的 interrupted 分支写回
  `executor_session_id`；success 分支清空（`clearExecutorSessionID`）。
- 测试：Resume token 往返（编码/解码/sessionId 传递）+ 畸形 token 拒绝。

**剩余 backlog**：daemon 重启后「scan 检测 executor_session_id 非空 → Resume」
的接通（属 scan 流程改动）。round2 有 checkpoint 复用，重派发已能从断点继续，
故 resume 接通是省 token + 保持上下文，非正确性前提。

### E1 验证记录（2026-08-20，rc.8）

- `GET /health` → `{"ok":true}`；`POST /agent/run`（无 effort）→ `completed` + sessionId。
- reasoningEffort 传递链路（agent/request prepend → llm-pi-ai）实测生效：
  - 前提：settings.yaml 的模型须配 `reasoningEfforts`（THINKING_LEVELS → wire），
    否则 llm-pi-ai 报 `UNSUPPORTED_REASONING_EFFORT`（模型 `reasoning: false`）。
  - 已配 `low/medium/high/xhigh` → wire `low/medium/high/max`（对齐 omp
    `reasoningEffortMap`）。`off` 不支持（未声明），Go 侧映射为「不传 effort」。
- 实测：`low`/`high`/`xhigh` 均 `completed`；`off` 需 Go 侧跳过。
- **omp → DSH effort 映射**（E2 实现）：`off→不传`、`low→low`、`high→high`、`max→xhigh`。

## 6. 风险与对策

1. **rc.7 agents API 稳定性**——之前 defer 的主因。E1 用最小插件验证，出错即回退 spawn（OMP/dsh spawn 分支保留）。
2. **agent-server 进程故障**——daemon 需健康检查 + 自动重启（同 systemd 语义）。
3. **并发**——agent-server 需支持并发 agent（agents service 天然多 agent；Go 侧连接池）。
4. **内存**——长驻 agent-server ≈ 单次 spawn 峰值（227MB）+ 会话残留；相比 spawn 每阶段峰值相同，但长驻不释放。可接受（对比 web 653MB）。

## 8. 真实生产验证（2026-08-20）

把 dsh-embed 部署到真实 daemon（release-manager 73 任务 vault），观察真实工作：

- ✅ **接替 omp**：新 otg（dsh-embed 默认）安装 + daemon 重启，agent-server 正常
  拉起，真实任务走 dsh 派发。
- ✅ **TASK-057 真实流程**：audit 会话通过（`audit_status=passed`）→ merge 会话
  深度执行（`auto-fix-ci`，CI 修复诊断中，正常耗时）。
- ✅ **deployd 项目**：split / pm / refining 会话在派发（新项目首 REQ 拆分等）。
- 🔧 **发现并修复 agent-server 端口冲突**：daemon 重启时旧 agent-server 残留占用
  8799，新 agent-server 的 `server.listen` EADDRINUSE 失败但 dsh 进程不退出，健康
  检查连到旧实例造成假健康。修复：`server.on("error", EADDRINUSE → process.exit(1))`，
  让 daemon 健康检查失败并重试。（`~/.dsh/plugins/agent-server.mjs`，非 git）
- ⏳ 观察中：057 merge 会话（CI 修复）完成 + 写回；其他 ready 任务持续派发。

## 9. reasoning effort 审查（2026-08-20，对照官方 handbook）

参考 `sandbaseai/deepseek-harness-handbook` 的 headless-reasoning-effort 指南，
审查 otg 各阶段映射：

| 阶段 | omp --thinking | mapDSHEffort | 审查结论 |
|---|---|---|---|
| priority | off | `""`（不传） | ⚠️ DSH 无 off 选择（实测 reasoningEffort:"off" → UNSUPPORTED），不传=用 provider 默认 |
| round2 | max | `xhigh` | ✅ xhigh wire=max，对齐 omp |
| planning | high | `high` | ✅ |
| 默认（refining/merge/pm/conventions） | low | `low` | ✅ |
| audit | off | `""`（不传） | ⚠️ 同上 |
| design（全局设计） | max | `xhigh` | ✅ |
| merge（冲突解决） | high | `high` | ✅ |

**官方要点**（对照后确认）：
- DSH headless 无 `--thinking` flag——embed 用 `agentOptions.reasoningEffort`
  （request/selection 层）是**正确**的 per-请求方案。
- `reasoningEfforts` 是**声明**（模型支持的 levels），`reasoning` 是 provider
  默认，`reasoningEffort` 是 selection——三层语义已正确区分。
- **off 不是可选择 effort**：实测 `off: null` 声明后仍 UNSUPPORTED（llm-pi-ai
  的 thinkingLevelMap 不含 off）。off（关闭推理）只能通过 provider `reasoning:
  false` 或不传（omission 用默认）实现。当前 `mapDSHEffort(off)=""` 是**合理
  近似**（不传用默认），但语义是「用默认」而非「关闭推理」——priority/audit
  若需真正关闭推理省 token，需 provider 级 `reasoning` 配置（后续 backlog）。

## 10. systemd 服务名 + 日志路径去 omp 化（2026-08-20）

- **systemd**：停用 + 禁用 + 删除 `omp-task-runner.service/timer` 与
  `omp-task-watcher.service`（旧名，5.8 已改 install.go 的 unit 名为 otg-*，
  但运行时旧 unit 仍在）。daemon 重启后发现 `omp-task-runner.service` 被
  systemd 自动拉起（activating），与手动 dsh-embed daemon 冲突——已彻底清除。
- **日志路径**：`~/.omp/logs` → `~/.dsh/logs`（daemon/merge/audit/cli 共 7 处）。
- **注释**：`~/.omp/get-api-key.sh` 历史引用移除。
- 代码层 `~/.omp` 引用清零。

## 11. grilling 交互平替 + effort 分级（2026-08-20）

### grilling 交互（kitty tab 光标问卷）

omp 的 grilling 交互（`exec "omp" <prompt>` 逐问 TUI）替换为自研 kitty-grill：

- **agent-server `/agent/chat`**：多轮交互端点（sessionId 命中 liveAgents 复用
  同一 agent 保持上下文，否则 create/resume），与 `/agent/run` 共用 acquireAgent。
- **kitty-grill**（`cmd/kitty-grill/`）：Bubble Tea 光标问卷——模型输出结构化
  JSON 问卷（decisions[]: id/question/options/recommended/reason），用户
  `j/k` 选选项、`Enter` 确认、`h/l` 切题、`q` 一轮提交；ANSI/lipgloss 渲染
  （进度 + 光标 ❯ + ⭐推荐 + 推荐理由），runewidth 按显示宽度换行（中文/emoji
  各占 2 列）。
- **预取注入**：kitty-grill 读需求文档全文 + 扫描 ADR-\d+ 引用读对应 ADR，
  作为上下文注入 prompt——勘察从 10-20 轮 read 降到 0 轮，省 60-70% 时间。
- **prompt-env**：决策清单 prompt 经环境变量传（避免 bash 反引号转义）。
- daemon 集成：tryKittyTab（需求详细化）/TryKittyDecisionTab（决策清单）从
  `exec omp` 改为 `exec kitty-grill`；ompExecPath → grillExecPath。
- **提交后异步写回 + 自动关 tab**（2026-08-22，TASK-058 对齐体验修复）：
  回答提交后 spawn detached 子进程（setsid，`--writeback` 模式）重新挂接
  session 完成写回（日志 `~/.dsh/logs/kitty-grill/writeback-*.log`，写回请求
  10 分钟超时），主进程 `kitty @ close-window --match id:$KITTY_WINDOW_ID`
  关闭本 tab——消除「卡在写回中 / tab 不自动关闭」；spawn 失败回退有界同步
  写回，答案不丢失。
- **写回守卫**：启动与写回前复查任务 status，已离开 needs-grilling
  （closed/done 等）阻止写回（防僵尸 tab 写回已归档需求）。
- **prompt 任务 ID 前置**：问卷 prompt 以 `任务 TASK-<id>` 开头，agent-server
  监控面板按第一个 `TASK-xxx` 打标签时命中真实任务（观测：REQ 正文引用其他
  任务时标签被误标，如 TASK-005 问卷误标 TASK-058）。

### reasoning effort 分级

参考官方 headless-reasoning-effort 设计审查，按阶段性质分级（模型声明
low/medium/high/xhigh 四档）：

| 阶段 | effort | 性质 |
| --- | --- | --- |
| priority | medium | 评估判断 |
| refining/conventions/audit/pm | low | 对话/整理 |
| planning | high | 深度规划 |
| round2 | max（xhigh） | 最深实现 |
| merge | high | 冲突解决 |
| design | max | 全局设计 |
| grilling 需求详细化 | high | 理解需求 + 技术判断 |
| grilling 决策清单 | low | 信息整理 |

### 配置与持久化

- agent-default-model 修复为 `deepseek_magic/deepseek-v4-pro`（magic 免费优先），
  fallback 只在免费渠道间切换：`deepseek_magic → openai gpt-5.6`（能力映射），
  `ds-official` 官方付费渠道仅由任务 `assignee=ds-official` 显式启用。
- `omp-commands` 插件改名 `dsh-commands`。
- daemon 持久化为 systemd user service `otg-task-watcher.service`（真实环境，
  完整 PATH 含 mise shims；不硬编码 KITTY_LISTEN_ON，kittyLaunchEnv 动态扫描
  /tmp/kitty-*）。

### E4 durable resume 完整闭环（2026-08-20 实测补齐）

真实生产验证（deployd TASK-001）暴露两个 durable resume 边界，已修复：

1. **中断瞬间 sessionId 持久化**：原 sessionId 由 agent-server 内部生成，中断
   （/agent/run 响应未返回）时 daemon 拿不到 sessionId → executor_session_id 空
   → resume 退化 fresh start。修复：`dshEmbedExecutor.Start` 用 crypto/rand
   **预分配 sessionId**，Wait/doRequest 的 interrupted 分支用它编码 ResumeToken。

2. **session not found 回退**：agent-server 把「sessionId 非空」一律当 resume，
   daemon 预分配的 sessionId（create 未完成 / agent-server 重启丢会话）报
   `session not found` → daemon 误判 MODEL_FAILED 卡 blocked。修复两处：
   - agent-server（`~/.dsh/plugins/agent-server.mjs`）acquireAgent：resume 失败
     → 用预分配 sessionId **create**（而非报错）；
   - Go `runDSHPhase`：resume 只有 `OutcomeSuccess` 才复用结果，否则回退
     fresh start（不再把「会话已失效」当阶段失败）。

实测：TASK-001 resume → 不再 MODEL_FAILED → implementing 正常重派发 round2。

## 12. Agent 并发监控面板（2026-08-20）

token 消费无实感 → 加 headless agent 并发监控（dsh web 可见）：

- **agent-server `/agents`**：遍历 liveAgents（活跃 agent 会话），返回 JSON 摘要
  （sessionId / phase / task / status / elapsed）。phase 从 task 文本的
  `obsidian-task-runner-XXX` 提取，task 从 `TASK-\d+` 提取，status 从最近
  session 事件（tool/call → 工具名、reasoning → thinking、text → writing）。
- **agent-server `/monitor`**：单文件 HTML 面板（零构建 SPA），轮询 /agents
  每 2 秒，DiceBear Bottts SVG 机器人（seed=sessionId，每 agent 一个独特角色）
  + Emoji 状态 + CSS 动画（呼吸/脉冲/弹跳）。
- **vault.mjs 加 `/agents` 命令**：dsh web 对话里输入 `/agents` 打开监控面板。
- 资源占用：面板单文件 ~5KB + 每活跃 agent 一个 ~1KB SVG（懒加载）+ 几行 CSS，
  无前端框架、无大图、无重动画循环；轮询只在面板打开时进行。

## 13. systemd 独立管理 dsh web / agent-server（2026-08-21）

headless-agent-server 不再由 otg daemon 作为子进程拉起，改为独立 user
systemd 服务；dsh web 也纳入 systemd：

- `~/.config/systemd/user/dsh-agent-server.service`：`dsh --profile headless-agent-server`，
  `Restart=always`。
- `~/.config/systemd/user/dsh-web.service`：`dsh --profile web`，`Restart=always`。
- `~/.config/systemd/user/otg-task-watcher.service`：增加
  `After=dsh-agent-server.service` + `Requires=dsh-agent-server.service`。
- `vault-map.json` 增加 `agent_server_managed: false`：daemon 不再自行
  start/stop agent-server，只等待外部服务健康检查通过。
- `otg install-systemd` / `make install-force` 会同时生成并启用这三个 unit。
