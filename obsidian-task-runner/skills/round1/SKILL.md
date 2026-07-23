---
name: obsidian-task-runner-round1
description: "Planning phase: generate a versioned implementation plan from a fully mature requirement, evaluate WIP checkpoint reuse, and write plan-review state."
hide: true
disableModelInvocation: true
---

你是 Planning / Round 1 计划生成器。你不实现代码、不 push、不创建 PR。

## 输入

- TASK `status: planning`
- daemon 使用 TASK `assignee` 模型调用本 Skill
- REQ 已通过 maturity gate

## Step 1: REQ 一致性

1. 读取 TASK、REQ 和完整 REQ bytes SHA-256。
2. 写入 `plan_req_hash`。
3. 读取 CONTEXT.md、ADR、depends_on 契约和项目结构。
4. 新项目只读需求/模板，不创建目录、Git repo 或脚手架文件。

## Step 2: Checkpoint 评估

若 `checkpoint_commit` 非空：

1. 读取该 commit diff。
2. 新计划逐项标注旧实现：`保留`、`修改`、`废弃`。
3. 说明理由和受影响 AC。

## Step 3: 生成计划

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

## Step 4: 提交前 hash 复核

计划写回前重新读取 REQ 并计算 hash：

- 与 `plan_req_hash` 不一致：丢弃本轮计划输出，不增加 plan_version，不清 pending_req，更新 `status=refining` 后退出。
- 一致：继续写回。

## Step 5: 版本化写回

- 每次 planning 成功，`plan_version = old + 1`。
- 在 `## 实现计划` 追加 `### vN`，不覆盖历史版本。
- 更新执行摘要和变更记录。
- 可提议 ADR；写 ADR 仍需 `adr_approved=true`。

## Step 6: Gate 更新

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
plan_version: <old+1>
pending_req: false
merge_approved: false
plan_approved: <autoApproveEligible>
planning_retry_count: 0
phase_error: ""
phase_log: ""
blocked_phase: ""
resume_approved: false

## Step 7: Frontmatter Safety

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
