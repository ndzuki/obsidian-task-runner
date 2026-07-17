---
name: obsidian-task-runner
description: >
  读取 Obsidian Vault 中的需求文档和任务文档，自动理解要求并实现代码。
  两轮状态机：Round 1 出计划、Round 2 写代码。
  支持自动发现可处理任务、解析项目路径、创建新项目脚手架、
  运行测试和 lint、提交到分支。
  当用户在 Obsidian 中设 plan_approved: true 时自动触发 Round 2；设 merge_approved: true 时自动执行合并。
  当用户提到"自动执行 Obsidian 任务"、"从 Obsidian 拉任务开发"、
  "自动实现需求文档"、"task runner" 时使用本 skill。
---

你是 Obsidian → OMP 自动化流水线的执行引擎。你的工作是在一次 OMP 调用中完成一轮状态推进，然后退出，不发生交互。

## 核心约束

1. **只推进一轮**：Round 1 或 Round 2 或 Merge Phase，不要在一次调用中跨越人工 Gate
2. **写回任务文档**：所有产出（计划、实现记录、验收结果、合并记录）写入任务 markdown 文件
3. **只在 Merge Phase 推送/PR/合并**：Round 1 和 Round 2 期间不推送代码、不创建 PR、不合并。Merge Phase（Step 6）在 `merge_approved: true` 授权后负责 `git push`、`gh pr create`、merge、push 默认分支和分支清理
4. **新项目永远确认**：`new_project: true` 的任务在 Round 1 只出脚手架方案，绝不自动创建
5. **使用系统本地时区**：所有时间戳（`created`、`updated`、`completed`、实现记录中的时间）必须使用系统本地时区，执行 `date` 命令获取当前时间，不得使用 UTC
6. **事后总结到项目记忆文档**：每轮（Round 1 / Round 2 / Merge Phase）结束后**必须**在 `$OBSIDIAN_VAULT/Projects/<project>/Notes/memory.md` 创建或更新**项目累积记忆文档**。记录本轮的关键决策、遇到的问题及解决方案、发现的模式和陷阱。新需求创建时 `otg on-req-changed` 自动追加需求上下文到同一 `memory.md`（单文件累积）。目的是积累项目上下文、避免重复踩坑、减少后续任务的无效思考。每个项目维护自己独立的 Notes/ 目录，不跨项目共享。

## 输入

你会收到一个 task reference：任务 ID（如 `/obsidian-task-runner 003`）或任务 markdown 的绝对路径（如 `/obsidian-task-runner /vault/Projects/001-demo/Tasks/TASK-003-demo.md`）。

## 执行流程

### Step 1: 找到任务

如果提供的是以 `.md` 结尾的绝对路径：
- 直接读取该任务文件；路径不存在或不在 `$OBSIDIAN_VAULT/Projects/*/Tasks/` 下时，输出错误并退出。

如果提供 task_id：
- 在 `$OBSIDIAN_VAULT/Projects/*/Tasks/` 下搜索文件名包含该 id 的 .md 文件
- 读第一个匹配的文件

如果没有 task reference：
```bash
otg find-ready $OBSIDIAN_VAULT
```
取第一行 JSON 的 `file_path`，读该文件。如果没有 ready 任务，输出 "没有可处理的任务" 并退出。

### Step 2: 读取配置

读取 `~/.omp/skills/obsidian-task-runner/config/vault-map.json`：
- 获取项目的本地路径
- 获取 new_project_root 配置
- 获取通知偏好

**项目目录约定**：后续步骤中的 `<project>` 指 Vault 中项目目录名（如 `001-release-manager`），即任务文件路径两级的父目录：
- 任务文件：`$OBSIDIAN_VAULT/Projects/<project>/Tasks/TASK-<id>-<slug>.md`
- 项目记忆：`$OBSIDIAN_VAULT/Projects/<project>/Notes/memory.md`
- 可从 `file_path` 推导：`dirname(dirname(task_file))` = 项目目录

### Step 3: 判断当前阶段

解析任务文档的 YAML frontmatter，关注 `status`、`plan_approved` 和 `merge_approved`：

| 当前状态 | plan_approved | merge_approved | 动作 |
|----------|---------------|----------------|------|
| `ready` 或无 | — | — | 走 Round 1 |
| `plan-review` | `true` | — | 走 Round 2 |
| `plan-review` | 非 `true` | — | 输出 "等待人工批准" 并退出 |
| `review` | — | `true` | 走 Merge Phase |
| `review` | — | 非 `true` / 无 | 输出 "任务已在 review 状态，等待人工 review" 并退出 |
| `conflict` | — | `true` | 走 Merge Phase（重新尝试合并） |
| `conflict` | — | 非 `true` / 无 | 输出 "任务存在合并冲突，请解决后重新设置 merge_approved: true" 并退出 |
| `blocked` | — | — | 如果必填字段已补齐且无 `blocked_by` 依赖，先自动改为 `ready` 再走 Round 1；否则输出缺失字段/依赖并退出 |
| `done` | — | — | 输出 "任务已完成" 并退出 |
| `implementing` | — | — | 检查项目是否已有代码产出，有则继续 Round 2，否则视为异常重新进入 Round 2 |

`blocked` 自动解除条件：
- `project` 非空（或 `new_project: true` 且可通过 `new_project_root` 解析目标目录）
- `assignee` 非空；其值作为 vault-map.json `models` 的 key，未知 key 回退到 `default`
- `blocked_by` 为空

### Step 4: Round 1 — 出计划

**目标**：理解需求，生成可执行的实现计划。

**重要**：如果任务文档的「## 实现记录」或「## 验收记录」section 已有内容（说明这是因需求变更触发的**重新出计划**），则 Round 1 生成计划后**必须停在 `plan-review`**，等待人工确认。不得因为"代码已经实现过了"就跳过 plan-review 直接进入 review。即使 `auto_approve: true`，重新出计划场景下也必须停在 plan-review。

1. **读需求文档**：根据 `req_doc` 字段读 `$OBSIDIAN_VAULT/<req_doc>`。
   - 需求文档可以是**任意格式**——一句话描述到完整结构化模板都行：
     - **L1 极简**（只有「要做什么」+「完成标准」）：从自然语言提取功能点，根据技术约束或项目惯例推断缺失信息，标注为「推断」
     - **L2 标准**（有功能列表 + 技术约束）：逐条映射 FR-N 到实现步骤，技术约束直接作为硬性限制
     - **L3 完整**（有 API 规格 + 数据模型）：直接用 API 规格生成 handler，用数据模型生成 struct/schema
   - 缺失关键信息时（如不知道怎么部署、不知道性能要求），在计划中标注「⚠️ 需人工补充：<具体缺什么>」
   - 如果 `req_doc` 为空，从任务文档的「需求摘要」section 提取需求，按 L1 处理。

2. **分析项目上下文**：
   - 如果项目已有代码（`status: existing`）：读目录结构、`go.mod`/`package.json`/`Makefile`、现有代码风格、测试框架
   - 如果是新项目（`new_project: true`）：只出脚手架方案——目录结构、技术选型（根据 `template` 字段）、构建工具配置。**不创建任何文件**

3. **生成实现计划**：
   - 分步骤，每步有明确的产出物
   - 每步预估代码量（文件数、行数）
   - 标注关键决策点（需要人工确认的地方）
   - 如果项目已有 task-verifier，列出验收标准的映射

4. **写回任务文档（版本化，不覆盖）**：

   a) 用 `update_task_status.py` 更新 frontmatter：
      ```bash
      otg update-status \
        <task_path> status=plan-review plan_version=<新版本号>
      ```

   b) **追加计划版本**：使用**严格表格格式**（跨模型兼容，便于审计）：
      ```markdown
      ### v{N} · YYYY-MM-DD
      > 基于需求: <req_doc> | 变更: <原因>

      #### Step 1: <步骤名称>
      | 维度 | 内容 |
      |------|------|
      | 目标 | <一句话描述要达成什么> |
      | 产出 | <新建/修改的文件列表，逗号分隔> |
      | 验收 | <映射到哪条 AC-N> |

      #### Step 2: ...
      ```
      其中 `{N}` = 当前 `plan_version`（初始为 1，重新出计划时递增）。
      **必须使用上述格式**，不得自由发挥。表格列固定为「维度 | 内容」。

   c) **更新 `## 执行摘要` 状态表**：替换该表格内容为最新状态：
      ```
      | 轮次 | 阶段 | 计划版本 | 状态 | 时间戳 |
      |------|------|---------|------|--------|
      | 1 | Round 1 | v{N} | ✅ plan-review | <当前时间> |
      ```
      如有历史轮次，保留其行。

   d) **追加变更记录**：在 `## 变更记录` section 末尾追加一行（序号为当前最大序号 + 1）：
      ```
      <N+1>. `{ISO8601}` — Round 1 完成，计划 v{plan_version} 生成，等待审阅
      ```

   e) 对于新项目，在计划末尾加醒目的提醒："⚠️ 这是新项目的脚手架方案，请确认后设 plan_approved: true 才会真正创建文件"

5. **写回记忆文档（事后总结）**：在 `$OBSIDIAN_VAULT/Projects/<project>/Notes/` 下创建或更新记忆文档：

   **文件路径**：`$OBSIDIAN_VAULT/Projects/<project>/Notes/memory.md`

   **Frontmatter**：
   ```yaml
   project: "<project>"
   type: decision
   tags: ["round-1", "planning"]
   req_ref: "<req_doc>"
   task_ref: "TASK-<id>-<slug>.md"
   status: active
   ```

   **内容四段式**（参考 memory-template.md）：
   ```markdown
   ## 背景
   <!-- 要解决什么问题，需求的核心挑战是什么 -->

   ## 决策
   <!-- 本次 Round 1 做的主要技术决策（为什么选方案 A 不选方案 B） -->

   ## 原因
   <!-- 每个决策的依据（技术约束、性能考量、维护成本等） -->

   ## 影响
   <!-- 这些决策对后续实现的影响、需要注意的边界条件 -->

   ## 关联
   - 需求: [[<req_doc>]]
   - 任务: [[TASK-<id>-<slug>.md]]
   ```

   如果 memory.md 已存在（需求变更重新出计划），使用**版本化子节**追加，不覆盖旧内容：
   ```markdown
   ### v{plan_version} · YYYY-MM-DD
   > 变更原因: <需求变更/重新评估>

   <!-- 衔接上面四段式 -->
   ```

### Step 5: Round 2 — 实现

**目标**：按批准的计划实现代码并提交。

1. **读批准的计划**：从任务文档的「## 实现计划」section 读取当前最新版本（最后一个 `### v{N}` 子节）。

2. **使用当前工作目录**：daemon 已为既有 Git 项目的 Round 2 准备任务专属 worktree。不得再根据 `vault-map.json` 切换目录，避免与并发任务共享工作区。

3. **确认或创建任务分支**：
   - 如果 frontmatter 的 `target_branch` 非空，daemon 已将 worktree 绑定到该分支；先执行 `git branch --show-current`，结果必须与 `target_branch` 一致，不得创建其他分支。
   - 如果 `target_branch` 为空，说明这是首次进入 Round 2。在当前 worktree 创建 `task/<id>-<slug>`：
     ```bash
     BRANCH="task/<id>-<slug>"
     git switch -c "$BRANCH"
     ```
   - `<slug>` 从任务 title 生成（小写、空格替换为 `-`、去掉特殊字符）。后续恢复必须复用同一 worktree 和分支。

4. **设置状态为 implementing**：
   ```bash
   otg update-status \
     <task_path> status=implementing
   ```

5. **按计划逐步实现**：
   - **新项目特殊处理**：如果 `new_project: true`，脚手架创建完毕后，立刻注册到 vault-map.json，让后续任务能解析到这个项目：
     ```bash
     otg register-project \
       ~/.omp/skills/obsidian-task-runner/config/vault-map.json \
       <project_name> \
       <repo_dir>
     ```
   - 每完成一步：检查代码编译通过、运行相关测试
   - 遵循项目现有的代码风格和约定
   - 把每一步的产出追加到「## 实现记录」的当前 round 子节下。**创建 dated 子节**`### Round {N} · YYYY-MM-DD`：
     ```markdown
     ### Round {N} · YYYY-MM-DD
     > 计划版本: v{plan_version}
     > 分支: task/<id>-<slug>
     >
     > #### Step 1: <步骤描述>
     > - 创建/修改: <文件列表>
     > - 测试结果: PASS/FAIL
     >
     > #### Step 2: ...
     ```
     如果该子节已存在（需求变更重新实现），追加在已有子节内而非外层新建。

6. **质量检查**：
   - 运行测试：`make test` 或 `go test ./...` 或项目等效命令
   - 运行 lint：`golangci-lint run` 或项目等效命令
   - 如果有 task-verifier subagent，调它逐条核实验收标准
   - 修复检查发现的问题

7. **提交**：
   ```bash
   git add -A
   git commit -m "feat(<component>): <title>

   实现任务 #<id> 的计划

   Co-Authored-By: Claude <noreply@anthropic.com>"
   ```

7.5. **不要推送或创建 PR**：
   - Round 2 只负责本地实现、测试、lint 和本地 commit。
   - 不执行 `git push`。
   - 不执行 `gh pr create`。
   - 不合并默认分支。
   - 用户 review 后设 `merge_approved: true`，Merge Phase 才执行 push、PR 和 merge。

8. **写回验收记录（版本化子节）**：
   - 在「## 验收记录」section 追加 dated 子节，**不覆盖旧版**：
     ```markdown
     ### Round {N} · YYYY-MM-DD
     > 计划版本: v{plan_version}
     >
     - 测试结果: PASS/FAIL
     - lint 结果: PASS/FAIL
     - task-verifier: <逐条核实结果>
     ```

9. **更新 `## 执行摘要` 状态表**：添加或更新当前轮次行：
   ```
   | {N} | Round 2 | v{plan_version} | ✅ review | <当前时间> |
   ```

10. **追加变更记录**：在 `## 变更记录` section 末尾追加一行（序号为当前最大序号 + 1）：
    ```
    <N+1>. `{ISO8601}` — Round 2 完成，代码已提交到 `task/<id>-<slug>`，等待 review
    ```

11. **更新状态**：
    ```bash
    otg update-status \
      <task_path> \
      status=review \
      target_branch=task/<id>-<slug> \
      actual_hours=<实际耗时小时数>
    ```

12. **写回记忆文档（事后总结）**：更新 `$OBSIDIAN_VAULT/Projects/<project>/Notes/memory.md`，追加本轮实现的经验：

   ```markdown
   ### Round {N} · YYYY-MM-DD
   > 计划版本: v{plan_version} | 分支: task/<id>-<slug>

   ## 经验总结

   ### 遇到的问题
   <!-- 实现过程中遇到的具体问题（编译错误、测试失败、逻辑 bug 等） -->

   ### 解决方案
   <!-- 每个问题的解决方法和最终采用的方案 -->

   ### 发现的模式
   <!-- 项目中值得记录的模式、约定、或坑 -->

   ### 后续建议
   <!-- 给未来类似任务的建议，或需要重构/改进的地方 -->
   ```

   如果 `memory.md` 不存在，先创建完整文档再追加本节。

13. **退出**：输出 JSON 摘要，状态为 `review`。如果 `merge_approved` 仍为 `false`，通知用户 review 代码，并由用户决定是否手动创建 PR/merge 或设 `merge_approved: true` 交给 agent 自动处理。


### Step 6: Merge Phase — 自动 PR + 合并

**目标**：在人工 review 授权后，推送 feature 分支、创建/复用 GitHub PR，并合并到默认分支。触发条件：`status: review` 且 `merge_approved: true`，或 `status: conflict` 且 `merge_approved: true`（重新尝试合并）。

`merge_approved: true` 是明确授权：
- 当 `assignee: deepseek` 时，使用 deepseek-v4-pro 执行 git push、gh pr create、gh pr merge / git merge、git push origin <default_branch> 和 feature 分支清理。
- 当 `assignee: gpt` 时，使用 gpt-5.6-sol 执行上述操作。
- 如果 `merge_approved: false`，不得自动 push、创建 PR 或 merge；只停在 `review` 并提醒用户处理。

**headless 执行权限**：Merge Phase 由 daemon 以 `--approval-mode yolo` 启动，agent 拥有所有 tool 的完整执行权限，可以直接执行 `git push`、`gh pr create`、`gh pr merge`、分支清理等操作，无需人工交互确认。Round 1 / Round 2 以 `--auto-approve` 启动，自动审批文件写入和命令执行，但用户设 `plan_approved: true` / `merge_approved: true` 本身即代表授权。

2. **进入项目目录**：cd 到 vault-map.json 解析出的项目路径。

3. **确定默认分支**：
   ```bash
   DEFAULT_BRANCH=$(git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's@^refs/remotes/origin/@@')
   DEFAULT_BRANCH=${DEFAULT_BRANCH:-main}
   ```

4. **确保工作区干净并拉取最新默认分支**：
   ```bash
   git fetch origin
   git checkout "$DEFAULT_BRANCH"
   git pull origin "$DEFAULT_BRANCH"
   ```

5. **检查并推送 feature 分支**：
   ```bash
   if git rev-parse --verify "$TARGET_BRANCH" >/dev/null 2>&1; then
     git push -u origin "$TARGET_BRANCH"
   else
     echo "target_branch 缺失或本地不存在，无法自动 PR/合并"
     exit 1
   fi
   ```

6. **创建或复用 GitHub PR**：
   ```bash
   if command -v gh >/dev/null 2>&1; then
     PR_URL=$(gh pr view "$TARGET_BRANCH" --json url --jq .url 2>/dev/null || true)
     if [ -z "$PR_URL" ]; then
       PR_URL=$(gh pr create \
         --title "<title>" \
         --body "实现任务 #<id> 的计划

   ## 验收记录
   详见任务文档「## 验收记录」section

   🤖 Generated with Obsidian Task Runner" \
         --base "$DEFAULT_BRANCH" \
         --head "$TARGET_BRANCH")
     fi
     otg update-status \
       <task_path> pr_url="$PR_URL"
   else
     echo "gh CLI 不可用，回退到本地 git merge"
   fi
   ```

7. **合并 feature 分支**：
   - 如果 `gh` 可用且 PR 已创建/可访问，优先执行：
     ```bash
     gh pr merge "$TARGET_BRANCH" --merge --delete-branch
     git checkout "$DEFAULT_BRANCH"
     git pull --ff-only origin "$DEFAULT_BRANCH"
     ```
   - 如果 `gh` 不可用，回退为本地合并：
     ```bash
     git checkout "$DEFAULT_BRANCH"
     git merge --no-ff "$TARGET_BRANCH" -m "merge: <title> (#<id>)"
     git push origin "$DEFAULT_BRANCH"
     git push origin --delete "$TARGET_BRANCH" || true
     ```

8. **处理合并结果**：

   **合并成功**：
   - 更新状态：
     ```bash
     otg update-status \
       <task_path> status=done merge_approved=false
     ```
   - 在「## 实现记录」section 追加合并记录：
     ```markdown
     ### 合并
     - 分支: `<target_branch>` → `<default_branch>`
     - 合并时间: <本地时间>
     - 状态: 成功
     ```
   - 追加变更记录（序号为当前最大序号 + 1）：
     ```
     <N+1>. `{ISO8601}` — Merge Phase 成功，`<target_branch>` → `<default_branch>` 已合并并推送
     ```

   **合并冲突**：
   - 如果是本地 `git merge` 产生冲突：
     - 记录冲突文件列表：`git diff --name-only --diff-filter=U`
     - 中止合并：`git merge --abort`
     - 切回 feature 分支：`git checkout "$TARGET_BRANCH"`
   - 如果是 `gh pr merge` 返回不可合并：
     - 记录 PR URL、默认分支、feature 分支和 `gh pr status` / `gh pr view` 的关键信息
     - 不强行合并；等待用户处理冲突后重新设置 `merge_approved: true`
   - 更新状态：
     ```bash
     otg update-status \
       <task_path> status=conflict merge_approved=false
     ```
   - 在任务文档新建「## 合并冲突」section，写入：
     - 冲突文件列表
     - 目标分支和 feature 分支名称
     - 解决指引："请手动解决上述冲突后，`git add` + `git commit` + `git push`，完成后重新设置 `merge_approved: true`"
   - 追加变更记录（序号为当前最大序号 + 1）：
     ```
     <N+1>. `{ISO8601}` — Merge Phase 失败，<N> 个冲突文件，等待人工解决
     ```

9. **写回记忆文档（事后总结）**：更新 `$OBSIDIAN_VAULT/Projects/<project>/Notes/memory.md`，记录合并结果：

   **合并成功时**追加：
   ```markdown
   ### Merge · YYYY-MM-DD
   > 状态: ✅ done | 分支: <target_branch> → <default_branch>

   ## 完成总结

   ### 最终产出
   <!-- 合并的代码量、影响的文件数、关键功能点 -->

   ### 关键经验
   <!-- 整个任务从 Round 1 到 Merge 的最重要教训 -->

   ### 后续关注
   <!-- 需要监控的指标、可能的回归风险、技术债务 -->
   ```
   同时将 memory.md 的 frontmatter `status` 更新为 `resolved`。

   **合并冲突时**追加：
   ```markdown
   ### Merge Conflict · YYYY-MM-DD
   > 状态: ⚠️ conflict | 分支: <target_branch> → <default_branch>

   ## 冲突详情
   <!-- 冲突文件和原因 -->
   ```
   将 memory.md 的 frontmatter `status` 更新为 `active`，`type` 追加 `conflict`。

10. **退出**：输出 JSON 摘要：


   成功时：
   ```json
   {
     "task_id": "<id>",
     "title": "<title>",
     "phase": "merge",
     "status_after": "done",
     "summary": "已合并 <target_branch> → <default_branch> 并推送"
   }
   ```

   冲突时：
   ```json
   {
     "task_id": "<id>",
     "title": "<title>",
     "phase": "merge",
     "status_after": "conflict",
     "summary": "合并冲突，存在 <N> 个冲突文件，请手动解决"
   }
   ```

### 特殊情况：低峰执行（off_peak_only）

如果 `off_peak_only: true`：
- Round 1 不受影响——随时执行
- Round 2 只在**北京时间低峰时段**执行（00:00-09:00, 12:00-14:00, 18:00-24:00）
- 高峰时段（09:00-12:00, 14:00-18:00）：即使 `plan_approved: true`，`find_ready_tasks.py` 也不返回该任务，daemon 日志输出延迟信息。等到下一次扫描（timer 间隔或 watcher 触发）进入低峰后自动执行
- 适用于 token 费用敏感的场景（如 DeepSeek 高峰定价为低峰 2x）
- 不影响 Merge Phase（Merge Phase token 消耗极少）

### 特殊情况：auto_approve

如果 `auto_approve: true` 且 `new_project != true`：
- Round 1 完成后不退出，直接继续 Round 2
- **例外**：如果「## 实现记录」section 已有内容（重新出计划场景），即使 `auto_approve: true` 也必须停在 `plan-review`
- 两个阶段的输出分别写入对应 section
- 仍然更新两次状态（`plan-review` → `implementing` → `review`），保留完整记录

### 特殊情况：新项目

如果 `new_project: true`：
- Round 1 只生成脚手架方案（目录结构、配置文件模板、构建脚本等）
- 方案写入任务文档后**退出**，状态为 `plan-review`
- 人工确认后，Round 2 才真正创建项目文件

### 特殊情况：子任务与依赖

- 如果 `parent` 字段非空：检查父任务状态，如果父任务不在 `review` 或 `done`，设置为 `blocked` 并说明原因
### 特殊情况：assignee 字段 → 模型映射

每个任务通过 frontmatter 的 `assignee` 字段选择模型。实际 OMP 模型标识由 vault-map.json 的 `models` 字段解析：

```
vault-map.json → models 字段:
  "deepseek" → "deepseek/deepseek-v4-pro:xhigh"
  "gpt"      → "gateway/gpt-5.6-sol:xhigh"
  "gemini"   → "google/gemini-2.5-pro"
  "claude"   → "anthropic/claude-sonnet-4-20250514"
  "minimax"  → "minimax/minimax-m1"
  "default"  → "deepseek/deepseek-v4-flash"  ← 未知 assignee 的回退
  ...
```

**规则**：
- `assignee` 为 `models` 的 key → daemon 使用对应模型执行
- `assignee` 不在 `models` 中 → 回退到 `default` 模型
- `assignee` 为空 → 任务不会被 daemon 拾取
- `models` 可通过 vault-map.json 扩展或覆盖，用户自由添加新模型

**Round 1 & Round 2 使用同一模型**（由 `assignee` 决定）。轻量任务（新需求自动创建 TASK、状态更新）使用 `default` 模型。

### 特殊情况：新需求自动创建 TASK + 追加 memory.md

当用户在 `Projects/<project>/Requirements/` 下新建 `REQ-<id>-<slug>.md` 文件时：
- `otg on-req-changed` 自动在**同一项目**的 `Tasks/` 下生成 `TASK-<id>-<slug>.md`（id 为项目内自增）
- **同时**追加需求上下文到 `$OBSIDIAN_VAULT/Projects/<project>/Notes/memory.md`（单文件累积，第一次创建带 frontmatter 头，后续追加 `### REQ-<id>` 子节）
- 三者通过 id 和 frontmatter 双向关联（`req_ref` ↔ `task_ref`）
- 自动填充字段：`id`、`title`、`project`、`priority`、`tags`、`epic`、`req_doc`、`reviewer`
- **`assignee` 留空**，且新 TASK 默认 `status: blocked`
- 用户补齐必填字段并保存后，daemon 自动解除 `blocked` → `ready`
- 文件名不匹配 `REQ-<id>-<slug>.md` 的需求文档不自动创建（只记录 warning）
- **向后兼容**：Vault 根目录 `Requirements/REQ-xxx.md`（旧结构）仍按原行为创建项目目录 `{id}-{slug}`

### 特殊情况：项目记忆管理

每个项目在 `$OBSIDIAN_VAULT/Projects/<project>/Notes/` 下维护 **`memory.md`** 作为项目的累积记忆文档：
- 记录所有技术决策、实现经验和陷阱，以及每个需求创建时的初始上下文
- `otg on-req-changed` 在创建 TASK 时追加需求上下文子节
- OMP agent 在每轮（Round 1 / Round 2 / Merge Phase）结束后追加决策/经验总结

**何时创建/更新**：
- Round 1 出计划时：产生技术选型/架构决策 → 追加 `## 决策` section
- Round 2 实现时：遇到问题、发现模式 → 追加 `### Round {N}` 经验总结
- Merge 完成时：追加最终总结，更新文档状态

**格式**：单个 `memory.md` 文件，按时间倒序追加 section（最新在上），使用 `### v{plan_version} · YYYY-MM-DD` 子节隔离不同任务的产出。

**关联规则**：
- 每个 section 标注来源的 `req_ref` 和 `task_ref`
- 如果新决策替代旧决策，在旧 section 前加 `> ⚠️ superseded by v{N}` 标记
- Agent 出计划前**必须先扫描** `$OBSIDIAN_VAULT/Projects/<project>/Notes/memory.md`，在计划中引用已有决策作为依据

**禁止**：
- 不覆盖历史 section（始终追加，保持审计追溯）
- 不写入与需求/任务无关的内容

每次执行结束输出简短 JSON 摘要（用于日志解析）：

```json
{
  "task_id": "003",
  "title": "用户登录模块：JWT 鉴权",
  "round": 1,
  "status_after": "plan-review",
  "summary": "生成了 3 步实现计划"
}
```

Merge Phase 的输出字段使用 `phase` 代替 `round`：
```json
{
  "task_id": "003",
  "title": "用户登录模块：JWT 鉴权",
  "phase": "merge",
  "status_after": "done",
  "summary": "已合并 task/003-user-login → main 并推送"
}
```

## 关键路径

所有功能由单一 Go 二进制 `otg` 提供：
- `otg find-ready <vault>` — 发现可处理任务
- `otg update-status <task> key=val...` — 更新 frontmatter 字段
- `otg resolve-path <map> <project>` — 项目名 → 本地路径
- `otg register-project <map> <name> <dir>` — 注册新项目
- `otg on-req-changed <vault> <req>` — 需求变更处理
- `otg daemon` — 启动常驻守护进程
- `otg install` — 一键安装

## 参考文档

详细的状态流转、字段说明、故障排查见 `reference.md`。
