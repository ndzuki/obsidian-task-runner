---
name: obsidian-task-runner-round1
description: "Planning phase: generate a versioned implementation plan from a fully mature requirement, evaluate WIP checkpoint reuse, and write plan-review state."
hide: true
disable-model-invocation: true
---

**Role**: Round 1 Planner. You generate versioned implementation plans. You do NOT write code, push, or create PRs.

## 输入

- TASK `status: planning`
- daemon 按 TASK `assignee` 路由模型调用本 Skill：显式 `assignee`（非空且非 `default`）覆盖一切；默认/空 assignee 的 planning 走 `models.deepseek_magic` 免费旗舰（v4-pro，见 daemon 权威模型路由表）
- **Daemon 已将项目上下文（Constraints + Anti-patterns + Domain Terms + ADR 摘要）注入到 prompt 顶部 `[Project Context]` 块中。以此为基线；仅在需要完整决策上下文时读取 `Notes/adr/` 中的完整 ADR 文件。**
- `Notes/CONTEXT.md` 的完整术语表仅在注入摘要不覆盖所需术语时补充读取。
- **必须在出计划前加载 `skill://knowledge-base`**：执行 Step -1（项目知识图谱）合成 CONTEXT + ADR + References 三源交叉引用，输出技术全景表。计划涉及的技术栈（Go、K8s、Helm、Docker 等），检索 core/ 文档的关键约束和版本要求纳入计划。发现知识缺口（ADR 引用了未入库的技术）标注在计划的"风险"或"前序契约"中。
- **失败模式检索（防重蹈覆辙）**：Step -1 同时检索 `core/daemon-stuck-task-patterns.md`（系统级失败模式）与目标技术文档中的「踩坑实践」小节（领域级失败方案）。命中的失败模式作为计划风险输入：对应 Step 标注 `风险: medium/high`，并在 Step 目标中显式写"规避已验证失败的方案 X（来源：<References 路径>）"。这保证知识库沉淀的负向经验在规划阶段就被消费。

## Step 0: Read ADRs — MANDATORY（读取ADR）

**ADRs are the architectural constitution of the project.** You MUST understand
existing decisions before making new ones. A new plan that conflicts with an
accepted ADR without explicitly superseding it is a planning failure.

1. List all files under `Notes/adr/`.
2. Extract from each: **Title**, **Status** (`accepted` / `superseded` / `deprecated`), core **Constraints** imposed.
3. Reference relevant ADRs in the plan: `Follows ADR-001 (<decision summary>)`.
4. If an existing ADR conflicts with the current requirement → flag `⚠️ ADR Conflict` in the plan.
   You MUST propose a new ADR that supersedes the old one.

> Planning without reading ADRs = driving blindfolded.


## Step 0.5: ADR Matching Mode（ADR 免批匹配）

Before proposing new ADRs, check whether the plan's technical choices match known
patterns. A match avoids repetitive ADR proposals and lets the planner focus on
genuinely novel decisions.

### Known Pattern Extraction

1. Scan all `accepted` ADRs under `Notes/adr/` and extract their technology
   decisions: databases, frameworks, RPC protocols, auth mechanisms, deployment
   targets, etc.
2. Read `Notes/CONTEXT.md` constraints and toolchain references.
3. Build a **known patterns set** from these combined sources. Examples:
   - `Go + Connect RPC + GORM + JWT`
   - `Kubernetes + Helm + Docker`
   - `SQLite + Litestream`

### Matching Procedure

1. Compare the plan's technical choices (Step 1 Architecture Decision Detection
   triggers) against the known patterns set.
2. **All choices match a known pattern** → the project has already decided this
   architecture:
   - In the plan, write: `引用 ADR-XXX（<决策摘要>），不新增 ADR 提案`
   - Reference each relevant ADR by number and summary.
   - Do **NOT** write `adr_proposed` — no new ADR is needed.
   - The plan follows established project conventions.
3. **Any choice deviates from known patterns** (new database, new framework,
   new protocol, new auth mechanism, etc.):
   - Proceed with the full Architecture Decision Detection trigger table (Step 1).
   - Write `adr_proposed` only for the genuinely new architectural decisions.
   - Still reference existing ADRs that apply to the unchanged parts.

> **ADR 免批的核心原则**：已由项目 ADR 覆盖的技术选择，不重复提案。
> 仅在计划引入全新架构决策时才写 adr_proposed。

## Step 1: REQ Consistency（需求一致性）

1. Read TASK, REQ, and compute full REQ bytes SHA-256.
2. Write `plan_req_hash`.
3. Read CONTEXT.md, `depends_on` contracts, and project structure.
4. New projects: read requirements/templates only; do NOT create directories, git repos, or scaffold files.

### Architecture Decision Detection — MANDATORY（架构决策检测）

**Propose an ADR when ANY of these triggers fire:**

| Trigger | Description |
|---------|-------------|
| New storage/persistence mechanism | Introducing a new database, cache, or file store |
| New or changed cross-service contract | New RPC, modified proto message, new event type |
| New external dependency | New library, framework, or infrastructure service |
| Replacing or deprecating an existing pattern | Changing how an existing concern is handled |
| Cross-service data flow change | Sync → async, direct call → message queue, new data pipeline |
| Security model change | Auth mechanism, RBAC granularity, trust boundary |
| Conflict with an existing ADR | Must supersede the old decision |

**Detection procedure:**
1. Compare the plan's technical choices against ADRs read in Step 0 + CONTEXT.md patterns.
2. Check `depends_on` upstream contracts for additions or changes.
3. If ANY trigger fires → write `adr_proposed`.

```bash
otg update-status <task> adr_proposed='["ADR: <decision title>", ...]'
```

> Title the ADR for the decision itself, not the task.
> Good: `ADR: Use <technology> as the sole business database`
> Bad: `ADR: TASK-069 implementation`

## Step 1.5: Target-Area Architecture Survey（目标区域架构探索，条件触发）

**Purpose**: 计划质量的上限取决于对目标模块真实结构的理解。Step -1 知识图谱只读文档，不读代码——大型/跨模块需求在此补一次**轻量代码探索**，让 Step 边界、seam 与接口设计与实际代码结构对齐，降低 Round 2 中途「架构摩擦」转 needs-grilling 的概率。

**触发条件**（任一，否则跳过本步）：
- REQ 涉及 3 个以上模块/服务；
- 重构类需求（替换/废弃既有模式、跨服务数据流变更）；
- `depends_on` 复杂（≥3 未解决依赖）；
- 计划者对目标模块结构不熟（读码后仍无法说出模块边界与耦合点）。

**方法**（加载 `skill://improve-codebase-architecture` 借用其 Explore 方法论——**不生成 HTML 报告、不进入 grilling**，只取探索模式）：
0. **知识库预检（防重蹈覆辙）**：`otg kb search` 检索目标模块主题与相关技术栈，读取命中文档的「踩坑实践」小节与 `core/daemon-stuck-task-patterns.md`（系统级失败模式）——已记录失败的方案作为子代理走查的**验证点**（该坑在当前代码里是否仍存在），`verified: true` 的已验证架构模式作为 seam/模块边界判断参考；命中文档路径记入探索产出，纳入 Step 3.5 的 `knowledge_refs`。
1. **热区定位**：`git log --oneline -20` 走查最近变更，让活跃路径先吸引注意力（deepening 的机会在正在变的代码里）。
2. **子代理并行走查**：spawn 2-3 个只读子代理分头走查目标模块，记录摩擦点——概念需跨多少文件才能理解（Locality）、模块是否**浅**（接口与实现一样复杂）、纯函数仅为可测而抽离但真 bug 在调用方、耦合模块是否跨 seam 泄漏、哪些部分不可测/难测。**对照预检命中的失败模式逐项验证**（坑已修 / 仍存在 / 换了形态）。
3. **Deletion Test**：对任何疑似浅的模块问——删除它，复杂度是集中到更小接口还是扩散到调用方？只有「集中」才算 deepening 候选。
4. 检查 `Notes/adr/` 是否已有禁止重开的决策；候选与既有 ADR 冲突时只在摩擦真实存在时提出并标注。

**产出**：写入 `## 实现计划` 的 `### 架构探索` 小节（计划头部）：
- 目标区域现状：模块清单、seam 位置、已知耦合点（1-3 句/模块）；
- 命中的知识文档（含「踩坑实践」结论与验证结果：坑已修/仍存在/换形态）；
- deepening 候选：≤3 个（带 deletion test 结论与影响范围）；
- 对计划的影响：Step 边界是否按真实 seam 调整、哪些 Step 需要先做局部深化。

> 成本控制：探索只读不写；产出 ≤300 字；子代理数量 ≤3。小需求（触发条件不满足）绝不执行本步。
> 版本化：`### 架构探索` 为计划头部固定小节，**每次 planning（含 replan）由本步重写**，不留存旧版探索结论（旧结论随 `### vN` 历史保留）。

## Step 1.8: Project Conventions Alignment（项目规范与架构约束对齐，强制）

读取 `{vault}/Projects/{project}/Notes/PROJECT-CONVENTIONS.md`（存在时）——该项目的
基线（设计/代码/注释语言/API 文档/文档/提交规范 **+ 架构约束**），**优先级高于本
Skill 与全局 AGENTS.md 默认约定**：

1. 计划中每个涉及代码/文档的 Step，其实现约定（注释语言、代码风格、文档格式、commit 语言）必须声明遵循项目规范；项目规范与全局默认冲突时以项目规范为准。
2. **架构约束强制对齐**：`## 架构约束` 节中的数据库分环境、schema/字段命名、迁移机制是硬约束——计划中任何涉及数据模型/新字段/新表的 Step 必须：
   - **以项目 test/prod 实际使用的数据库引擎为准设计 schema**（如 test/prod=MySQL），不得假设 dev 用的 SQLite 语义（字段名结尾 `_at`、`DATETIME`、自增等）；涉及多引擎时声明「双引擎兼容」。
   - 字段名结尾/命名按项目既有 schema 规范，引用 `## 架构约束` 中的代表例（路径+行号）。
   - 迁移方式按项目既有机制（`gorm AutoMigrate` / `alembic` / 手写 SQL…），新迁移必须能在 test/prod 引擎上执行。
   - 发现计划假设与基线冲突（如计划按 SQLite 建表而基线写 test/prod=MySQL）→ 标 `⚠️ 架构约束冲突` 并按架构决策检测处理（引用基线为上下文）。
3. 技术选型**优先项目已有技术栈**：Step 引入的新依赖/新框架必须与项目既有模式一致，否则按架构决策检测触发 ADR（引用项目现状为上下文）。
4. 计划不得包含项目规范之外的重构/优化类 Step（已有项目 review 认知负担优先）；已有规范未覆盖处按最小变更原则。
5. 新项目（无此文件）不适用本步，照常生成计划。

## Step 1.9: Environment Cleanup Planning（环境清理计划，强制）

计划中任何 Step 若会创建集群/容器/临时文件（k3d、docker、冒烟日志、kubeconfig/凭据等），**计划末尾必须包含对应清理 Step**（调用项目清理目标如 `make dev-down`/`dev-purge`/`k3d-clean`）。计划中还必须显式声明：

1. 会话退出前删除本任务创建的一切临时资源，清理证据写入阶段记录（`k3d cluster list`/`docker ps` 快照）。
2. 严禁停止/删除用户常驻服务（本地推理/向量检索常驻服务、桌面/IDE 进程等）或其它任务资源；资源门禁不通过时记录阻塞，禁止以停用户服务换取门禁。
3. 确需留给下游任务的环境，写明下游任务 ID 与保留清单，由下游任务结束时清理。

## Step 2: Checkpoint Assessment（Checkpoint 评估）

若 `checkpoint_commit` 非空：

1. 读取该 commit diff。
2. 新计划逐项标注旧实现：`保留`、`修改`、`废弃`。
3. 说明理由和受影响 AC。

## Step 2.5: Scaffold Intent（新项目脚手架意图）

新项目（`new_project=true`）时读取 TASK frontmatter `scaffold` 对象（`kind`/`capabilities`/`preferences`/`notes`）：

1. 解析意图：技术栈/框架/构建/部署目标（如 `kind: go-microservice`、`capabilities: [connect-rpc, github-actions]`）。
2. **能力校验走知识库检索**（`otg kb search`）：对每个 `capabilities` 检索对应主题（`kb search "<能力名>"`），确认能力存在性与相关实践（主题文档的实践经验/踩坑小节）；主题文档中标注的冲突/替代关系（如「与 X 冲突」「优先 Y」）在计划中标注需用户确认。注册表已废弃（scaffold_registry 无代码消费者且噪音化，能力元数据由知识库主题承担）。
3. `scaffold` 为空 → 走 split 技术栈建议流程（PM 统筹）。
4. **新项目且 `remote_create=true`**：从 REQ 提炼一句话仓库描述（项目定位 + 核心能力，≤200 字符），写入 frontmatter `repository_description`——daemon 创建 GitHub 仓库时用作 `--description` 与 `README.md` 内容。提炼规则：标题 + 需求摘要精华，不堆砌细节。

## Step 3: Generate Plan（生成计划）

涉及新模块或接口设计的 Step，按 `skill://codebase-design` 的深度模块原则：
- 接口是否简洁（≤3 方法）但背后隐藏足够复杂度（Depth > 1）？
- Seam 是否放在调用方不需关心的位置（Locality）？
- 删除该模块，复杂度是消失还是扩散（Deletion Test）？

**Design It Twice（高风险接口 Step 强制）**：若 Step 声明 `risk: high` 且涉及接口设计（新模块、跨服务契约、数据流变更、存储抽象），必须用 `skill://codebase-design` 的 DESIGN-IT-TWICE 模式产出方案对比，而非直接写第一个想法：

1. **并行 3 个子代理**，各用一个激进不同的设计约束：
   - Agent 1: "Minimize the interface — 1–3 个入口点，最大化每个入口的 leverage"
   - Agent 2: "Maximise flexibility — 支持多用例与扩展"
   - Agent 3: "Optimise for the most common caller — 默认场景零摩擦"
2. 每个子代理输出：接口形状（类型/方法/参数 + 不变量、顺序、错误模式）、调用方用法示例、seam 背后隐藏什么、依赖策略、权衡（leverage 高/薄处）。
3. 顺序呈现 → 按 **depth / locality / seam placement** 对比 → 给出推荐（可 hybrid），连同落选方案的取舍写入计划 Step 的「设计对比」小节。

> 第一个想法通常不是最好的（Ousterhout）。接口返工是 Round 2 阻塞的常见来源——设计两次的成本远低于实现期返工。非高风险接口 Step 仍按上方深度模块原则检查即可。

每个 Step 使用固定表格：

```markdown
#### Step N: <名称>
| 维度 | 内容 |
|------|------|
| 目标 | ... |
| 产出 | ... |
| 测试 Seam | 本 Step 的测试打在哪层公共接口（如 HTTP handler、Repository 接口、纯函数层）；计划外 seam 在 Round 2 视为架构信号走实现阻塞 |
| Step 依赖 | ... |
| 前序契约 | ... |
| Checkpoint 处理 | 保留/修改/废弃/N/A |
| 验收 | AC-N |
| 风险 | low/medium/high |
```

高风险 Step **必须**附带 Prototype 建议，用于数据驱动 Grilling。格式：

```markdown
## Prototype 建议

#### Step N: <名称>（risk: high）
| 维度 | 内容 |
|------|------|
| 验证目标 | 验证 <假设 X> 在 <场景 Y> 下是否可行 |
| PASS 条件 | <可观测的确定性结果>，如"单次查询<10ms"或"proto 编译通过" |
| FAIL 条件 | <触发 Grilling 的条件>，如"需要新增依赖或 API 不兼容" |
| 原型范围 | <最小可运行代码，不含测试/持久化> |
| 预计耗时 | <10 分钟以内> |
| 失败后分流 | needs-grilling + grill_context 附带原型证据 |
```

**设计意图**：原型在 plan-review 阶段仍只是建议。用户 `plan_approved=true` 后，Round 2 在执行该高风险 Step **之前**先运行原型。PASS → 跳过 Grilling 直接实现；FAIL → 带原型证据进入 Grilling，用户看到的是数据而非猜想。这样可将多轮"猜测型 Grilling"压缩为一轮"证据型 Grilling"。

## Step 3.5: Knowledge References Write-back（知识引用写回）

Step -1 知识图谱与 core/ 检索命中的知识文档，**必须写入 TASK frontmatter `knowledge_refs`**（相对 References/ 的路径列表，如 `core/go/connect-rpc.md`）：

- 只记录**计划实际引用**的文档（Step 目标/前序契约中体现的知识约束），不堆砌检索到的全部结果。
- 写回方式：`otg update-status <task> knowledge_refs=<comma-separated>`（或等价 frontmatter 更新）。
- 目的：形成跨会话引用链——Round 2 按清单应用、merge 时 daemon 度量 `knowledge_applied`（hit/total）、task-verifier 校验 AC 证据引用。

## Step 3.6: Plan Files Write-back（计划文件清单写回）

计划产出的**将修改文件清单**必须写入 TASK frontmatter `plan_files`（repo 相对路径，逗号分隔）：

- 只列**计划实际要改写的文件**（新增/修改/删除），不含只读参考文件；估算不清时给保守集合（宁多勿漏——daemon 用它做同项目并行实现的重叠预警）。
- 写回方式：`otg update-status <task> plan_files=internal/foo.go,web/src/bar.ts`（空计划或纯文档任务写 `plan_files=` 清空）。
- 目的：daemon 检测**同项目并发 implementing 任务的文件重叠**并一次性通知——把合并冲突信号从 merge 阶段前置到调度阶段（release-manager 教训：253 commits 中 57 个冲突解决类合并，Vue 壳层/路由/proto 是重灾区）。

## Step 4: Pre-commit Hash Verification（提交前Hash复核）

计划写回前校验 REQ hash——**hash 由 daemon 预计算写入 `refine_req_hash`（零 token），不要读取 REQ 全文重新计算**：

- 读取 TASK frontmatter 的 `refine_req_hash`；为空（异常）才回退读全文计算。
- 与 `plan_req_hash` 不一致：丢弃本轮计划输出，不增加 plan_version，不清 pending_req，更新 `status=refining` 后退出。
- 一致：继续写回。

**REQ 读取规范**（全流程适用）：读 frontmatter + `## 详细技术规格`（章节定位/行号 selector）+ `## 验收标准`（grep AC 列表）；章节存在性用标题 grep；禁止全文加载大 REQ（>20KB）。

## Step 5: Versioned Write-back（版本化写回）

- 每次 planning 成功，`plan_version = old + 1`。
- 在 `## 实现计划` 追加 `### vN`，不覆盖历史版本。
- 更新执行摘要和变更记录。
- 可提议 ADR（写 `adr_proposed`）；`adr_approved` 由 daemon 在 plan-review→implementing 过渡时自动置为 `hasADRProposal(adr_proposed)`——`adr_proposed` 非空即自动 true，无需人工批准（见 Step 6 ADR 护栏）。

## Step 6: Gate Update（Gate更新）

`auto_approve` 默认 true（frontmatter 缺失即视为 true；模板已写入 `auto_approve: true`）：计划批准由 daemon 统一接管——scan 时 plan-review 直接转 implementing 并通知，**Round 1 不计算批准资格**。仅当任务显式 `auto_approve: false` 时，计划停在 plan-review 等待人工 `plan_approved=true`。

原子更新：

```yaml
status: plan-review
plan_version: \<old+1\>
pending_req: false
merge_approved: false
plan_approved: false # daemon 按 auto_approve（默认 true）决定是否自动批准
planning_retry_count: 0
phase_error: ""
phase_log: ""
blocked_phase: ""
resume_approved: false
```

> **ADR 护栏**：daemon 在 plan-review→implementing 过渡时自动置 `adr_approved = hasADRProposal(adr_proposed)`——`adr_proposed` 非空即 true，Round 2 照常开始；空（`""`/`[]`/`null`）即 false。架构决策无需人工批准步骤（Round 2 按 `adr_approved=true` + `adr_proposed` 写 ADR，见 round2「Write ADRs」）。
> `adr_proposed` 的空值形态以 `""` 或 `[]` 为准——两者均视为空（无架构决策）。

新项目与 replan 同样适用：`new_project` 仅影响目录创建时机（Round 2 才创建），不阻断自动批准。

## Step 7: Frontmatter Safety（安全规范）

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for every field update.
- After writing the task, run `otg validate-doc <task_path>` to verify structural integrity.

新项目与 replan 的写回同样 `plan_approved=false`（批准统一由 daemon 按 auto_approve 决定，见 Step 6）。

## 失败语义

Daemon 管理：第一次失败自动恢复；第二次失败转 blocked：

```yaml
blocked_phase: planning
phase_error: "..."
phase_log: "..."
resume_approved: false
```

人工 resume 后 planning_retry_count 清零。

- **NEVER edit YAML frontmatter directly.** All frontmatter mutations MUST use `otg update-status`. Run `otg validate-doc <task_path>` before exiting.
