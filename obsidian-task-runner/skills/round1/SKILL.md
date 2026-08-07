---
name: obsidian-task-runner-round1
description: "Planning phase: generate a versioned implementation plan from a fully mature requirement, evaluate WIP checkpoint reuse, and write plan-review state."
hide: true
disableModelInvocation: true
---

**Role**: Round 1 Planner. You generate versioned implementation plans. You do NOT write code, push, or create PRs.

## 输入

- TASK `status: planning`
- daemon 使用 TASK `assignee` 模型调用本 Skill
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
## Step 2: Checkpoint Assessment（Checkpoint 评估）

若 `checkpoint_commit` 非空：

1. 读取该 commit diff。
2. 新计划逐项标注旧实现：`保留`、`修改`、`废弃`。
3. 说明理由和受影响 AC。

## Step 2.5: Scaffold Intent（新项目脚手架意图）

新项目（`new_project=true`）时读取 TASK frontmatter `scaffold` 对象（`kind`/`capabilities`/`preferences`/`notes`）：

1. 解析意图：技术栈/框架/构建/部署目标（如 `kind: go-microservice`、`capabilities: [connect-rpc, github-actions]`）。
2. 对照 vault-map `scaffold_registry`（能力描述/别名/冲突）校验能力组合——冲突能力（如 `connect-rpc` 与 `http-api`）在计划中标注需用户确认。
3. 对照 `template_registry`：`capabilities` 匹配的模板（`default_capabilities`）作为脚手架方案基线写入计划 Step 1（新项目首个 Step 常为脚手架搭建）。
4. `scaffold` 为空 → 走 split 技术栈建议流程（PM 统筹）。

5. **新项目且 `remote_create=true`**：从 REQ 提炼一句话仓库描述（项目定位 + 核心能力，≤200 字符），写入 frontmatter `repository_description`——daemon 创建 GitHub 仓库时用作 `--description` 与 `README.md` 内容。提炼规则：标题 + 需求摘要精华，不堆砌细节。

## Step 3: Generate Plan（生成计划）

涉及新模块或接口设计的 Step，按 `skill://codebase-design` 的深度模块原则：
- 接口是否简洁（≤3 方法）但背后隐藏足够复杂度（Depth > 1）？
- Seam 是否放在调用方不需关心的位置（Locality）？
- 删除该模块，复杂度是消失还是扩散（Deletion Test）？

每个 Step 使用固定表格：

```markdown
#### Step N: <名称>
| 维度 | 内容 |
|------|------|
| 目标 | ... |
| 产出 | ... |
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
- 可提议 ADR；写 ADR 仍需 `adr_approved=true`。

## Step 6: Gate Update（Gate更新）

先计算 autoApproveEligible：

```text
auto_approve=true
AND plan_version before this run == 0
AND new_project=false
AND pending_req=false
AND adr_proposed 为空（无架构决策）
```

> **ADR 护栏**：有 ADR 提议的任务即使 `auto_approve=true` 也必须 `plan_approved=false`——
> 架构决策（ADR 提议列表）随计划一起人工审阅，审过计划才算看过决策。
> "完全自主任务" = 无 ADR 提议 + 上述条件全部满足。
> `adr_proposed` 的空值形态以 `""` 或 `[]` 为准——两者均视为空（无架构决策）。

原子更新：

```yaml
status: plan-review
plan_version: \<old+1\>
pending_req: false
merge_approved: false
plan_approved: \<autoApproveEligible\>
planning_retry_count: 0
phase_error: ""
phase_log: ""
blocked_phase: ""
resume_approved: false
```

若 `autoApproveEligible=true`（自动批准），在 TASK 变更记录追加一行，标注来源以便事后区分自动/人工批准：

```
<N+1>. {ISO8601} — plan_approved 自动批准（auto_approve，首规划且无 ADR 提议）
```

## Step 7: Frontmatter Safety（安全规范）

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for every field update.
- After writing the task, run `otg validate-doc <task_path>` to verify structural integrity.

新项目和所有 replan 必须 `plan_approved=false`。

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
