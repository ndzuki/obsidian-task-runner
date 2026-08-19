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

| 步 | 内容 | 验证 | 可立即做 |
|---|---|---|---|
| E1 | DSH 侧 agent-server.mjs 插件 + profile | `curl POST /agent/run` 跑通一个任务，断言 reasoningEffort 生效 + sessionId 持久化 | ✅ 是 |
| E2 | Go 侧 dshEmbedExecutor（HTTP client） | 单测（httptest stub server）+ 与 E1 联调 | ✅ 是 |
| E3 | daemon 生命周期（启动/关闭 agent-server） | daemon 启停冒烟 | 依赖 E1/E2 |
| E4 | resume 路径（sessionId 持久化到 frontmatter executor_session_id） | 中断后 resume 一致 | 依赖 E2 |
| E5 | 默认切 dsh-embed + planning 复测（验证推理强度解决跑偏） | planning 真实冒烟 | 依赖 E1-E4 |

## 6. 风险与对策

1. **rc.7 agents API 稳定性**——之前 defer 的主因。E1 用最小插件验证，出错即回退 spawn（OMP/dsh spawn 分支保留）。
2. **agent-server 进程故障**——daemon 需健康检查 + 自动重启（同 systemd 语义）。
3. **并发**——agent-server 需支持并发 agent（agents service 天然多 agent；Go 侧连接池）。
4. **内存**——长驻 agent-server ≈ 单次 spawn 峰值（227MB）+ 会话残留；相比 spawn 每阶段峰值相同，但长驻不释放。可接受（对比 web 653MB）。

## 7. 结论

embed 是**最终形态**：解决推理强度 + resume + 启动开销三个 spawn 短板。
E1（agent-server 插件）+ E2（Go HTTP client）是**现在能直接做**的地基，
且不破坏现有 spawn/OMP 回退（executor 取值扩展，默认仍 dsh spawn）。
