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
- REQ 已通过 maturity gate
- **Daemon 已将项目上下文（Constraints + Anti-patterns + Domain Terms + ADR 摘要）注入到 prompt 顶部 `[Project Context]` 块中。以此为基线；仅在需要完整决策上下文时读取 `Notes/adr/` 中的完整 ADR 文件。**
- `Notes/CONTEXT.md` 的完整术语表仅在注入摘要不覆盖所需术语时补充读取。

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

## Step 3: Generate Plan（生成计划）

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

高风险 Step 附 Prototype 建议。所有命名使用 CONTEXT.md 规范术语。

## Step 4: Pre-commit Hash Verification（提交前Hash复核）

计划写回前重新读取 REQ 并计算 hash：

- 与 `plan_req_hash` 不一致：丢弃本轮计划输出，不增加 plan_version，不清 pending_req，更新 `status=refining` 后退出。
- 一致：继续写回。

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
```

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
