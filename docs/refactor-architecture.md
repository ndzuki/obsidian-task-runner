# Obsidian Task Runner → DeepSeek Harness：目标架构与重构方案

> 状态：审查完成，方案定稿，等待分阶段实施
> 分支：`refactor/dsh-architecture`（不直接改 main）
> 日期：2026-08-18

---

## 0. 摘要（TL;DR）

`obsidian-task-runner`（otr）当前是「Go daemon 状态机 + 外部 OMP 进程执行」的架构。审查两项目（otr 工具本身 + 被它开发的 release-manager）后，确认：

1. **otr 的 Go 调度/状态机设计是扎实的**（34.8MB 常驻、1.2% CPU、flock 原子写、82 个测试全绿）——**这部分保留，不重写成 TS**。
2. **真正的债在执行层与决策层**：
   - 执行层深度耦合 OMP CLI/日志/PID 协议，无法平滑迁到 DSH；
   - 决策层把「需求理解/设计推理」散落到 73 个任务各自的会话里，反复重规划、互相冲突——这是 release-manager 交付慢、返工多的根因。
3. **目标架构**：Go 确定性控制面 + PhaseExecutor seam（OMP/DSH 双 adapter）+ **一次性全局设计库**（单一事实源）+ DSH Web 看板插件。性能只升不降（实测 dsh headless 峰值 227MB / 启动 0.04s，优于 omp 的 830MB / 3.92s）。

---

## 1. 背景与目标

### 1.1 现状

- `otr` 把 Obsidian Vault 当需求入口，由 `otg daemon`（Go，约 2.4 万行）调度多阶段任务（refining → planning → implementing → review → merge → done），每阶段 spawn 一个外部 OMP headless 进程执行。
- release-manager 是它的旗舰案例：**67 个 REQ / 73 个 TASK / 23 个 ADR / 12.6 万行 Go 代码 / 514 commits**。

### 1.2 目标

1. **执行引擎从 OMP 平滑迁到 DeepSeek Harness**（`dsh --profile headless`，或未来 DSH 原生 agent），拿到 DSH 的模型路由/fallback、工具过滤、结构化输出、skill 集成能力。
2. **解决 release-manager 暴露的决策层问题**：需求理解从「每任务反复会话」上移到「一次性全局设计库」。
3. **性能不降反升**：并发调度继续由 Go 控制面负责（34.8MB），执行进程比 OMP 更轻。
4. **Vault 项目管理/看板以 DSH Web 插件呈现**，逐步取代打开 Obsidian。
5. 全程**非 main 分支**推进、每步可验证、失败可回滚。

---

## 2. 两项目审查结论

### 2.1 obsidian-task-runner（工具）审查

规模：147 个 Go 文件 / **4.5 万行**，其中 `internal/daemon` 约 **2.4 万行**；82 个测试文件，`go test -race -tags sqlite_fts5 -cover ./...` 全绿。

#### P0（正确性/恢复缺口）

| # | 问题 | 证据 | 影响 |
|---|---|---|---|
| P0-1 | **状态写入无 generation/fencing** | 所有写都经 `yamlfrontmatter.Update`（flock+读改写），只有互斥、无 revision/CAS；旧 OMP/DSH 会话晚到的写回可覆盖新代状态 | 挂死的旧会话写回会污染新计划字段 |
| P0-2 | **watcher 丢弃 Remove/Rename** | `watcher.go:146` 只处理 Create/Write；REQ 删除分支是运行期死代码 | REQ 被删后 TASK 不转 blocked，无感知 |
| P0-3 | **compact 折叠绕过 flock** | `compact.go` 自实现 rename，不取锁 | 与并发状态写回 lost update |

#### P1（架构/可迁移性）

| # | 问题 | 影响 |
|---|---|---|
| P1-1 | **OMP 契约深耦合**：CLI 参数（`--model --thinking --tools -p`）、日志尾随、`empty-stop-handled` 字符串、SIGTERM 行为、PID 文件全被 daemon 硬编码 | 无法局部替换为 DSH，必须抽 seam |
| P1-2 | **daemon 巨类 + 状态机分散**：`daemon.go` 4503 行；状态转移散在 state_machine/on_req_changed/merge_runner/stage_flip/dep_health 多处；可调度判定在 task.go 与 daemon 双份 | 加一个字段要同步多处，已有语义漂移 |
| P1-3 | **校验失败只记日志**：`validatePhaseCompletion`/`validateChangedDocs` 失败仅 log，仍推进 | 文档损坏被固化，可能"假成功" |
| P1-4 | **merge/pm 并发门禁不可达**：`phaseGates["merge"/"pm"]` 无获取点 | 配置项是死配置 |
| P1-5 | **后台 goroutine 无结构化并发**：warmup/adoption/tail/KB sync 无统一 ctx 管理 | 泄漏/不可控 |
| P1-6 | **PID adoption 依赖 /proc（Linux-only）** | 跨平台不可用 |

#### P2（中低风险）

watcher 通道溢出、新目录不递归 watch、debounce map 无界、`Index.frontmatter` 持锁 I/O、priority 执行路径无日志尾随/空响应兜底、错误直写 stderr 与主日志割裂、`createTaskForReq` 非原子、compact 节边界朴素匹配。

#### skill 集成问题

- `wayfinder` 声明为外部依赖但**无 daemon 显式调用**、无结构化输出验证；refining 要求生成 Wayfinder Map 又禁止生成实现计划，契约冲突。
- `grill-with-docs` **未接入**（无 registry/引用/安装）。
- skills 清单存在 5/6/7/8 多源漂移；外部 skill 未完整登记或未 fail-fast。
- 确定 P0 bug：`scripts/skill-doctor:261-265` 用未定义 `$symlink`，`set -u` 下阻断 discovery symlink 创建。

### 2.2 release-manager（被开发项目）交付审查

#### 交付数据

| 指标 | 数值 | 含义 |
|---|---|---|
| REQ / TASK / ADR | 67 / 73 / 23 | 从 1 段文字膨胀到 67 份原子需求 |
| 最大 TASK | **1.4MB**（TASK-057） | 计划+实现+验收+审计全历史堆叠 |
| 最大重工 | TASK-069 **plan=17**、TASK-018 plan=7 | 一个需求规划 17 版 |
| reopen_count>0 | 6 个任务 | 交付后又被打破重开 |
| checkpoint_commit 非空 | 22 个任务 | 22 次中途打断/重规划 |
| 代码产出 | 446 Go 文件 / **12.6 万行** / git 505MB | 真实大型平台 |
| git 提交 | 514 commits，**55 条含返工/冲突关键词** | 重工是常态 |

#### 暴露的问题

1. **需求迭代成本爆炸**：67 份 REQ 是「每任务跑完整 LLM 会话 grilling」迭代出来的。需求细化本质是结构化决策，却用最贵的生成式会话去做——每次重规划重复读同样的 REQ+ADR+CONTEXT，上下文不连续导致反复推翻自己（TASK-069 plan=17、TASK-018 plan=7）。
2. **决策无单一事实源**：23 个 ADR + 67 个 REQ 分散在 73 个任务文件，每任务会话只看到局部上下文。出现 TASK-018 被「陈旧终态 metadata 卡死」（daemon 以为 done，实际代码没合入）这类跨任务不一致。
3. **契约定义不足导致返工**：早期无依赖声明的并发实现产生 57/253 冲突合并、11 次 v2/v3 返工（项目文档自记教训）。共享契约（REQ-009/010）放 Wave 0 的洞察对，但实现方式太贵。
4. **「分解」本身是对的，错在执行方式**：引入 wayfinder/grilling 的意图（结构化分解）正确，但用成了「每任务迭代的昂贵引擎」。

### 2.3 根本原因（两项目合并）

- **执行层**：OMP 协议硬编码（P1-1）→ 无法平滑换执行引擎。
- **状态层**：无 fencing + 状态机分散（P0-1、P1-2）→ 并发写回不可靠、语义漂移。
- **决策层**：需求理解/设计推理被「每任务会话」化，无一次性全局设计、无持久设计库 → 反复重规划、跨任务冲突、返工爆炸。
- **校验层**：校验失败不阻断（P1-3）→ 损坏被固化。

---

## 3. 优化方案（合并清单，按优先级）

### 3.1 执行层：抽 PhaseExecutor seam（P1-1，最高优先）

```
otg daemon (Go) ── PhaseExecutor 接口 ──┬─ ompAdapter（行为冻结，回退）
                                        └─ dshAdapter（spawn dsh headless）
```

- 接口：`Start(ctx, PhaseSpec, TaskSnapshot, Workspace) → ExecutionHandle`；`Resume/Cancel/Wait/Collect → ExecutionResult`。
- `PhaseSpec` 数据化：`{phase, model, reasoningEffort, toolsPolicy, promptBuilder, timeout, recoveryPolicy}`——把 daemon.go 里 8 个阶段分支全部迁入 spec 工厂。
- OMP adapter 冻结现有 CLI/日志/SIGTERM 契约；DSH adapter 用 `dsh --profile headless`，未来升级到 `ctx.agents.create/resume`。
- daemon 侧只消费稳定事件（Started/Progress/Completed/Failed/Interrupted/Quota/KeyUnavailable），不再解析日志字符串。

### 3.2 状态层：fencing + 状态机收敛（P0-1、P1-2）

- 引入 `TaskStore.Apply(taskID, expectedRevision, event)` 作为唯一写入口，frontmatter 作为 durable projection。
- 任务持久化字段增加 `generation / attempt_id / executor_session_id`；阶段回写校验代际，不匹配只写审计日志。
- 合法转移集中在单一状态机（当前散在 5 处）；`IsReady` 保留为只读谓词。

### 3.3 决策层：一次性全局设计库（本方案核心创新）

> 这是对 release-manager 交付问题的最重要回应。

**原则**：需求理解/设计推理从「每任务会话」上移到「一次性全局设计会话」，产出**持久设计库**作为单一事实源。

- **全局设计会话**（v4-pro，一次性）：读第一版需求 + 项目约束 → 产出设计库：
  - 接口/契约定义（protobuf/API/数据结构）
  - 依赖图 / 交付波次（Wave 0-5，沿用现有洞察）
  - 领域词汇（CONTEXT.md 升级为设计库一部分）
  - ADR 决策（23 个 ADR 收敛进设计库，而非散落任务）
- **设计库**（`Projects/{project}/Design/` 或独立目录）：
  - `contracts/`（接口/契约，单一事实源）
  - `decisions/`（ADR，含 status/superseded 关系）
  - `waves/`（交付波次 + 依赖图）
  - `glossary.md`（领域词汇）
- **任务执行**（v4-flash，批量）：每任务只读设计库**相关切片** + 自己 REQ，不重复理解全局。
- **契约先行**：并行任务必须先定义共享契约（Wave 0 强化），实现期只对接契约，减少合并冲突。
- **粒度控制**：新增「重规划门禁」——plan_version 超阈值（如 5）自动升级为设计库修订而非单任务 replan，阻止 TASK-069 plan=17 这类空转。

### 3.4 校验层：校验失败阻断（P1-3）

- `validatePhaseCompletion`/`validateChangedDocs` 失败升级为结构化 `DOCUMENT_INVALID` phase failure，阻断推进，不再"假成功"。

### 3.5 工具侧修复（P0-2/P0-3/P1-4/P1-5/P1-6）

- watcher 增加 Remove/Rename 路由（修 P0-2）。
- compact 走 `WithLockedFrontmatter` 读改写（修 P0-3）。
- 实现或删除 merge/pm 门禁配置（修 P1-4）。
- 后台 goroutine 统一 errgroup + daemonCtx（修 P1-5）。
- DSH 迁移时用 durable session id 替代 PID adoption（修 P1-6，顺带跨平台）。

### 3.6 skill 集成

- 修 `skill-doctor` `$symlink` bug。
- 统一 skills 清单单一来源（manifest + Makefile + install.go 读同一文件）。
- `wayfinder`：接成 DSH 结构化输出 skill（v4-pro 一次性产出设计库），替代"每任务生成 Wayfinder Map"。
- `grill-with-docs`：作为可选项接入 grilling（前提：grilling 只在真争议时触发，减少会话数）。

### 3.7 Vault Web 展示插件（独立交付线）

**交付形态（Phase 4，用户确认）**：`otg web serve`（Go）提供零构建单文件 SPA
看板 + 只读 JSON API；DSH Web 通过 `/vault` slash command（`~/.dsh/plugins/vault.mjs`）
作为轻量入口指向看板。DSH 原生 client 插件（npm 包 + `dsh.client` 元数据 + build）
推迟到 rc 稳定后升级为内嵌 iframe。

- **后端白名单视图 DTO**：固定 viewId 查询（tasks-overview/blocked/running/
  design-library-status），禁止任意 DQL（安全）。
- **页面**：`/`（SPA）→ 项目导航、按状态分列看板、视图表格、设计库 KPI+产物、
  任务详情抽屉。哈希路由 `#/projects/:name`。
- **读写分层**：`writableFields` 白名单——Human-owned（priority/assignee/due_date/
  title/off_peak_only/auto_approve/auto_merge）与 Shared gate（plan/merge/resume/
  close/adr_approved）可写；System-owned（`status`/`phase_error*`/`merge_status`/
  `generation`/`attempt_id`/`plan_version` 等）绝不由 Web 改。
- **fencing**：写操作走 `task.TaskStore.Apply` 代际 CAS，`expected_generation`
  不匹配返回 409 且不落盘。
- **安全**：路径一律从目录列举推导（客户端字符串仅作查找键）；`safeBasename`
  拒绝分隔符/`..`；HTTP 层由 ServeMux 清洗 `..`；不读 secrets。

---

## 4. 目标架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                      DSH Web（插件层，独立交付线）                     │
│  Vault 看板插件（白名单 viewId DTO + 读写分层 + 安全边界）            │
│  context7 MCP / omp-commands / kb-distill / fallback（已有）        │
└───────────────┬─────────────────────────────────────────────────────┘
                │
┌───────────────▼─────────────────────────────────────────────────────┐
│                     otg daemon（Go 控制面，保留）                    │
│  状态机（单一事实源 + fencing）│并发上限│fsnotify│flock 原子写│Git/worktree│
│  kb.sqlite（知识库检索）│设计库读写                                 │
└───────────────┬─────────────────────────────────────────────────────┘
                │ PhaseExecutor seam
        ┌───────┴────────┐
        │ ompAdapter     │ dshAdapter
        │（回退，冻结）   │（dsh --profile headless / 未来 ctx.agents）
        └───────┬────────┘
                │ spawn 每阶段执行进程（227MB 峰值，结束即退）
┌───────────────▼─────────────────────────────────────────────────────┐
│                      DeepSeek Harness 执行面                        │
│  模型路由/fallback（跨模型插件）│工具过滤（审计 read/grep/bash）      │
│  结构化输出（审计 JSON 契约）│第三方 skill（wayfinder/grill-with-docs）│
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│                     设计库（一次性全局设计，单一事实源）              │
│  contracts/（接口/契约）│decisions/（ADR）│waves/（依赖+波次）      │
│  glossary.md（领域词汇）                                             │
│  生产：v4-pro 全局设计会话（一次性）                                  │
│  消费：v4-flash 任务执行会话（只读相关切片）                          │
└─────────────────────────────────────────────────────────────────────┘
```

### 关键性能数据（实测）

| 指标 | otg daemon | omp headless | dsh headless | otg web serve（Vault 看板） |
|---|---|---|---|---|
| 常驻/峰值内存 | 34.8MB | 830MB | **227MB** | **≈21–23MB**（50+100 次请求后 GC 稳定，无泄漏） |
| 纯启动 | 0.01s | 3.92s | **0.04s** | — |
| 完整调用（含推理） | — | 22.3s | **7.4s** | — |

并发上限继续由 Go 控制面（phase_concurrency / max_concurrent_tasks）决定；真正瓶颈是 LLM API 速率/token 预算，与语言无关。

Vault 看板内存验收（Phase 4d）：`otg web serve` 作为独立 Go 进程稳定 RSS ≈ 22MB，
150 次混合只读请求后无单调增长；这验证了「Go 控制面解析 Vault + DSH 只拿 DTO」
的设计——若让 DSH Node 全量解析 Markdown/AST 会放大到 +50–150MB（详见 §3.7）。

---

## 5. 分阶段迁移路线

| 阶段 | 内容 | 验证门禁 | 分支 |
|---|---|---|---|
| **Phase 0** | 本文档 + 基线测试固化 | `make test` 全绿 | refactor/dsh-architecture |
| **Phase 1** | PhaseExecutor seam + ompAdapter（行为冻结）+ dshAdapter（spawn headless） | 现有 daemon 测试全绿 + 新增 adapter contract 测试（Start/Resume/Cancel/Collect） | 同上 |
| **Phase 2** | TaskStore.Apply + generation/attempt fencing；watcher Remove/Rename；compact 走锁；校验失败阻断 | fencing 测试（旧会话晚到写回被拒）+ P0 修复测试 | 同上 |
| **Phase 3** | 设计库（contracts/decisions/waves/glossary）+ v4-pro 全局设计会话 + 重规划门禁 | 设计库 schema 测试 + 全局设计会话产物验证 | 同上 |
| **Phase 4** | Vault Web 看板插件（白名单 viewId DTO + 读写分层） | DSH Web 启动 + 视图数据正确性 + 安全测试 | 独立插件仓库或本仓库 dsh.client 包 |
| **Phase 5** | 移除 omp 依赖（install 检查、fallback、日志尾随、skill-doctor） | 全链路用 dsh headless 跑通 | 同上 |
| **Phase 6（最后）** | 审查从 omp 迁移到 DSH 的所有 skill，评估描述是否需要优化并给出方案（含 phase skills、knowledge-base、grilling、wayfinder、codebase-design、omp-tools 等；优化方向：去除 omp 专属工具引用、对齐 DSH 触发词、skill 描述单一事实源、结构化输出契约） | skill 描述清单审查报告 + 优化方案；改动后 `dsh-upgrade-check --full` + skill catalog 校验 | 同上 |

每阶段独立可验证、可回滚；先 Phase 1（seam）再决策层（Phase 3）——seam 是地基，设计库是价值。Phase 6（skill 审查）按用户要求排到最后做。

---

## 6. 持续测试验证

- **回归基线**：现有 82 个测试文件（daemon 47 / task 7 / knowledge 12 等）保持全绿。
- **新增测试**：
  - Phase 1：PhaseExecutor contract 测试（各状态 + 超时/中断/quota）。
  - Phase 2：fencing 测试（旧会话晚到写回被拒、kill/restart 后 resume 一致）、watcher 删除事件、compact 并发。
  - Phase 3：设计库 schema/引用完整性、重规划门禁触发。
  - Phase 4：视图 DTO 正确性、路径逃逸/绝对路径泄露/任意 DQL 拒绝。
- **每阶段门禁**：`go test -race -tags sqlite_fts5 -cover ./...` + 新增阶段测试 + `dsh-upgrade-check --full`。

---

## 7. 参考文件索引

- otr 设计规范：`docs/workflow.md`（1334 行状态机/验收清单）、`obsidian-task-runner/reference.md`、`docs/dataview.md`、`docs/go-rewrite-plan.md`
- otr 代码：`internal/daemon`（2.4 万行）、`internal/task`、`internal/knowledge`、`pkg/yamlfrontmatter`
- 交付实例：`~/myNote/Projects/001-release-manager/`（67 REQ/73 TASK/23 ADR）、`~/release-manager/`（12.6 万行 Go）
- 相关审查：本文档第 2 节汇总自 5 个领域只读审查 + 实测数据
