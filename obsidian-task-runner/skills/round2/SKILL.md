---
name: obsidian-task-runner-round2
description: "Implementation phase: execute an approved plan AC by AC in a task worktree, checkpoint safely on pending requirement changes, and finish in review."
hide: true
disableModelInvocation: true
---

**Role**: Round 2 Implementation Engine. You execute approved plans AC by AC using Tracer Bullet in a task worktree.

## Pre-flight Checks（前置检查）

1. TASK `status: implementing`，`plan_approved=true`。
2. `pending_req=false` 才能开始新的 AC。
3. blocked_by 全部满足。
4. 当前 worktree/branch 与 `target_branch` 一致；首次进入时创建 `task/\<id\>-\<slug\>`。
5. 读取已批准计划和 checkpoint 复用策略。

## Tracer Bullet（逐AC推进）

每条 AC 独立执行：

1. Red：最小失败测试。
2. Green：刚好足够的实现。
3. Refactor：只在 Green 后。
4. 记录实现和测试证据。
5. **AC 完成后重新读取 TASK frontmatter。**

不得批量写完全部测试后再实现。

## Pending Requirement Handoff（pending_req安全交接）

AC 完成后若 `pending_req=true`：

1. 不开始下一条 AC。
2. 提交：

```text
chore(task): checkpoint before requirement replan
```

3. 写入 commit SHA：

```bash
otg update-status \<task\> \
  checkpoint_commit=\<sha\> \
  status=refining \
  merge_approved=false
```

4. 保持 pending_req=true。
5. 写入变更记录，正常退出。

## Implementation Blockers（实现阻塞）

测试连续失败、计划外决策、依赖冲突或架构摩擦需要用户决策时：

1. 写 `## Round 2 阻塞` 和结构化 grill_context。
2. 保存 `grill_prev_status=implementing`，转 `needs-grilling`。
3. daemon 自动打开 Kitty。
4. Grilling 完成必须写：
   - `grill_resolution=resume`：纯实现/环境问题，daemon 直接恢复 implementing。
   - `grill_resolution=replan`：需求/设计/计划变化，设置 pending_req=true 后转 refining。
5. grill_resolution 为空时 daemon 不猜测，保持 needs-grilling。

## Completion Checklist（完成检查）

1. 全部 AC 有独立证据。
2. 运行项目全部测试（Go: `go test -race ./...`）。
3. 运行 lint。
4. 加载 `skill://test-quality`，修复 critical/important 问题。
5. 调 task-verifier 核验 AC。

### Write ADRs (BEFORE implementation — do not skip)

**ADRs are blueprints, not footnotes.** Write the decision down before writing
the code that depends on it.

> The daemon auto-sets `adr_approved=true` at the plan-review→implementing transition.

If `adr_approved=true` AND `adr_proposed` is non-empty:

1. **Before starting the first AC**, read the `adr_proposed` list.
2. For each ADR title, generate the body using the standard ADR format:

```markdown
# ADR-<id>: <title>

## Status
accepted

## Context
<Why is this decision needed? What constraints and forces are at play?>

## Decision
<What specific approach was chosen?>

## Alternatives Considered
<What other options were evaluated and why were they rejected?>

## Consequences
<What becomes easier? What becomes harder? What are the risks?>
```

3. Call `otg write-adr <project_dir> <task_id> "<title>" "<body>"`
4. Call `otg validate-adr <file_path>` to verify structural integrity.
5. Reference the ADR during implementation: in commit messages or code comments, note `See ADR-<id>`.

**After all ACs are complete:**
6. Run `otg update-status` once:
   - Append written filenames to `adr_written`
   - Clear `adr_proposed`
   - Set `adr_approved` to `false`
7. Commit ADR files together with the implementation — **ADRs and code in the same commit.**

6. 本地 commit，不 push。

成功写回：
## New Project（新项目）

只有 Round 2 可以创建项目目录、Git repo 和脚手架。创建成功后执行 `otg register-project`。

## Frontmatter Safety（安全规范）

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for checkpoint commits, grill_context, and status transitions.
- Run `otg validate-doc <task_path>` after every TASK file write to verify structural integrity.
