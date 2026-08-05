---
name: obsidian-task-runner-round2
description: "Implementation phase: execute an approved plan AC by AC in a task worktree, checkpoint safely on pending requirement changes, and finish in review."
hide: true
disableModelInvocation: true
---

**Role**: Round 2 Implementation Engine. You execute approved plans AC by AC using Tracer Bullet in a task worktree.

## Knowledge Refs Application（按知识引用清单应用）

读取 TASK frontmatter `knowledge_refs`（Round 1 写入的引用的知识文档清单）：

1. 逐条 `read` 对应 References 文档（`<vault>/References/<ref>`），提取与当前 Step 相关的约束/已验证实践。
2. 实现过程中**显式应用**：在实现记录或代码注释中标注来源（如 `// per core/go/connect-rpc.md: ...`）。
3. 引用清单中的文档与计划 Step 冲突时，以 ADR 为准并记录冲突（走 `Implementation Blockers`）。
4. 发现清单文档过时/错误 → 按 `skill://knowledge-base` 自动纠正流程追加纠错标注。

## Pre-flight Checks（前置检查）

1. TASK `status: implementing`，`plan_approved=true`。
2. `pending_req=false` 才能开始新的 AC。
3. blocked_by 全部满足。
4. 当前 worktree/branch 与 `target_branch` 一致；首次进入时创建 `task/\<id\>-\<slug\>`。
5. 读取已批准计划和 checkpoint 复用策略。
6. **加载 `skill://knowledge-base`**：按计划中的技术栈检索知识库 core/ 文档，引用已验证的最佳实践和版本约束。实现过程中发现的踩坑经验，在 Commit 或 ADR 写回后追加到对应的 References 文件。

## Prototype Gate（高风险 Step 的前置验证）

若已批准计划包含 `## Prototype 建议` section，在执行任何标记为 `risk: high` 的 Step 之前：

1. 读取计划的 Prototype 建议，提取 PASS/FAIL 条件。
2. 加载 `skill://prototype`，在 task worktree 中创建 throwaway 原型。
3. 严格按 PASS/FAIL 条件评估原型结果。
4. **原型 PASS** → 继续 Tracer Bullet 实现该 Step，在 `## 实现记录` 中标注 `✅ Prototype validated`。
5. **原型 FAIL** → 不进入实现。写 `## Round 2 阻塞` + 结构化 grill_context，包含：
   - 原型代码路径和运行输出
   - 违反的具体 FAIL 条件
   - 建议的替代方案
   - 保存 `grill_prev_status=implementing`，转 `needs-grilling`

这样 Grilling 不再是"你觉得应该怎么设计？"而是"原型验证了 X 不可行（证据见 /path），建议采用 Y。你同意吗？"——一轮定案。

## Tracer Bullet（逐AC推进）

每条 AC 独立执行，Red/Green/Refactor 标准参照 `skill://tdd`：

1. Red：最小失败测试。
2. Green：刚好足够的实现。
3. Refactor：只在 Green 后。
4. 记录实现和测试证据。
5. **AC 完成后重新读取 TASK frontmatter。**


### Scope Hammering（时间盒过半自动削 scope）

若 REQ 声明了 `appetite`（small=30m / medium=2h / large=6h），在时间盒过半时执行：

1. 评估剩余 AC 的核心程度：是否 must-have？能否降级？
2. 非核心 AC 标为 `~nice-to-have`，写回 TASK 通知用户。
3. 优先交付核心 AC。nice-to-have 不阻塞 review——可在后续 cycle 中作为新 REQ 追加。

核心判断标准（From Shape Up Ch.14）：
- 没有这个功能，用户能否完成核心任务？（能 → nice-to-have）
- 这是新问题还是已有 workaround 的老问题？（老问题 → nice-to-have）
- 这个情况发生的概率？（<5% → nice-to-have）
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
2. **知识应用校验**：TASK `knowledge_refs` 中每条引用，验收记录或实现记录必须体现其应用（引用了约束/实践、或在代码/测试中落地）；未应用的 ref 在 Review Bundle 中列出并说明原因（不适用/过时/被 ADR 覆盖）。缺失应用说明视为完成检查不通过。
3. **每个 `risk: high` Step 的实现记录必须含 Prototype 证据**（`✅ Prototype validated` 或 FAIL 记录 + grill_context）；缺失则补跑 Prototype Gate，不得跳过。
3. 运行项目全部测试（Go: `go test -race ./...`）。
4. 运行 lint。
5. 加载 `skill://test-quality`，修复 critical/important 问题。
6. 加载 `skill://code-review`：Standards 轴检查代码规范+Code Smell；Spec 轴核验实现与 REQ 的 AC 是否一一对应、有无 scope creep。与 test-quality 互补——前者查测试质量，后者查代码+需求对齐。
7. 调 task-verifier 核验 AC。

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


## Review Bundle 生成

全部 AC 完成且测试通过后，生成 Review Bundle 摘要，写入 TASK：

1. **diffstat**：`git diff --stat \<target_branch\>...HEAD`
2. **测试结果**：`go test` 输出中的 PASS/FAIL 统计
3. **test-quality 摘要**：🔴/🟡/🟢 计数
4. **code-review 摘要**：Standards 轴发现数 + Spec 轴发现数
5. **风险自评**：low/medium/high
6. **Baseline 对比**（Shape Up Ch.14）：实现前用户如何解决？实现后改善了什么？一句对比帮助用户快速判断 merge 价值
7. **Scope Hammering 结果**：若有降级的 AC，列出 `~nice-to-have` 项
8. **通知摘要**：

```text
TASK-<id>: <N> files +<added>/-<deleted>, <M> tests PASS
test-quality: 🔴0/🟡0/🟢<N> | code-review: St<N> Sp<N>
appetite: <small|medium|large> | deferred: <N> AC (~nice-to-have)
baseline: <一句话改善对比>
risk: <level> → auto_merge 默认自动合并，无需人工 review（可设 auto_merge: false 恢复人工 gate）
```

成功写回：

全部 AC 完成、测试与检查通过后：

```bash
otg update-status <task> status=review
```

- `auto_merge: true`（默认）时 daemon 自动授权合并并执行 Merge Phase（push → PR → CI checks → merge），无需人工。
- `auto_merge: false` 时保持 `merge_approved: false`，等待用户 review 后手动设 `merge_approved: true`。

## New Project（新项目）

只有 Round 2 可以创建项目目录、Git repo 和脚手架。创建成功后执行 `otg register-project`。

## Frontmatter Safety（安全规范）

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for checkpoint commits, grill_context, and status transitions.
- Run `otg validate-doc <task_path>` after every TASK file write to verify structural integrity.
