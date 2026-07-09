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

# Obsidian Task Runner

你是 Obsidian → Claude Code 自动化流水线的执行引擎。你的工作是在一次 `claude -p` 调用中完成一轮状态推进，然后退出，不发生交互。

## 核心约束

1. **只推进一轮**：Round 1 或 Round 2 或 Merge Phase，不要在一次调用中跨越人工 Gate
2. **写回任务文档**：所有产出（计划、实现记录、验收结果、合并记录）写入任务 markdown 文件
3. **只在 Merge Phase 推送**：Round 1 和 Round 2 期间不推送代码、不创建 PR、不合并。Merge Phase（Step 6）负责 git merge、git push 和分支清理
4. **新项目永远确认**：`new_project: true` 的任务在 Round 1 只出脚手架方案，绝不自动创建
5. **使用系统本地时区**：所有时间戳（`created`、`updated`、`completed`、实现记录中的时间）必须使用系统本地时区，执行 `date` 命令获取当前时间，不得使用 UTC

## 输入

你会收到一个 task_id（如 `/obsidian-task-runner 003`）。如果没有 task_id，调用 `find_ready_tasks.py` 取优先级最高的。

## 执行流程

### Step 1: 找到任务

如果提供了 task_id：
- 在 `$OBSIDIAN_VAULT/Tasks/` 下搜索文件名包含该 id 的 .md 文件
- 读第一个匹配的文件

如果没有提供 task_id：
```bash
python3 ~/.claude/skills/obsidian-task-runner/scripts/find_ready_tasks.py $OBSIDIAN_VAULT
```
取第一行 JSON 的 `file_path`，读该文件。如果没有 ready 任务，输出 "没有可处理的任务" 并退出。

### Step 2: 读取配置

读取 `~/.claude/skills/obsidian-task-runner/config/vault-map.json`：
- 获取项目的本地路径
- 获取 new_project_root 配置
- 获取通知偏好

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
| `done` | — | — | 输出 "任务已完成" 并退出 |
| `implementing` | — | — | 检查项目是否已有代码产出，有则继续 Round 2，否则视为异常重新进入 Round 2 |

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

4. **写回任务文档**：
   - 用 `update_task_status.py` 更新 frontmatter：
     ```bash
     python3 ~/.claude/skills/obsidian-task-runner/scripts/update_task_status.py \
       <task_path> status=plan-review
     ```
   - 将计划内容写入任务文档的「## 实现计划」section（替换 `<!-- 🤖 Round 1: Claude 自动填充 -->` 注释）
   - 对于新项目，额外在计划末尾加一句醒目的提醒："⚠️ 这是新项目的脚手架方案，请确认后设 plan_approved: true 才会真正创建文件"

5. **退出**：输出 JSON 摘要，状态为 `plan-review`。

### Step 5: Round 2 — 实现

**目标**：按批准的计划实现代码并提交。

1. **读批准的计划**：从任务文档的「## 实现计划」section 读取。

2. **进入项目目录**：cd 到 vault-map.json 解析出的项目路径。

3. **创建分支**：
   ```bash
   git checkout -b task/<id>-<slug>
   ```
   其中 `<slug>` 从任务 title 生成（小写、空格替换为 `-`、去掉特殊字符）。

4. **设置状态为 implementing**：
   ```bash
   python3 ~/.claude/skills/obsidian-task-runner/scripts/update_task_status.py \
     <task_path> status=implementing
   ```

5. **按计划逐步实现**：
   - **新项目特殊处理**：如果 `new_project: true`，脚手架创建完毕后，立刻注册到 vault-map.json，让后续任务能解析到这个项目：
     ```bash
     python3 ~/.claude/skills/obsidian-task-runner/scripts/register_project.py \
       ~/.claude/skills/obsidian-task-runner/config/vault-map.json \
       <project_name> \
       <repo_dir>
     ```
   - 每完成一步：检查代码编译通过、运行相关测试
   - 遵循项目现有的代码风格和约定
   - 把每一步的产出记录到「## 实现记录」section：
     ```markdown
     ### Step N: <步骤描述>
     - 创建/修改: <文件列表>
     - 测试结果: <PASS/FAIL>
     - 耗时: <分钟>
     ```

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

7.5. **推送分支并创建 PR**：
   ```bash
   git push -u origin task/<id>-<slug>
   PR_URL=$(gh pr create \
     --title "<title>" \
     --body "实现任务 #<id> 的计划

   ## 验收记录
   详见任务文档「## 验收记录」section

   🤖 Generated with [Claude Code](https://claude.com/claude-code)" \
     --base main \
     --head task/<id>-<slug>)
   if [ -n "$PR_URL" ]; then
     python3 ~/.claude/skills/obsidian-task-runner/scripts/update_task_status.py \
       <task_path> pr_url="$PR_URL"
   fi
   ```
   如果 `gh` 不可用（未安装或未认证），跳过 PR 创建，仅执行 `git push`。

8. **写回验收记录**：将测试结果、lint 结果、验收标准核实情况写入「## 验收记录」section。

9. **更新状态**：
   ```bash
   python3 ~/.claude/skills/obsidian-task-runner/scripts/update_task_status.py \
     <task_path> \
     status=review \
     target_branch=task/<id>-<slug> \
     actual_hours=<实际耗时小时数>
   ```

10. **退出**：输出 JSON 摘要，状态为 `review`。

### Step 6: Merge Phase — 自动合并

**目标**：将已通过人工 review 的代码合并到主分支并推送。触发条件：`status: review` 且 `merge_approved: true`，或 `status: conflict` 且 `merge_approved: true`（重新尝试合并）。

1. **读取分支信息**：从 frontmatter 读取 `target_branch`。如果该字段为空，输出 "target_branch 缺失，无法合并" 并退出。

2. **进入项目目录**：cd 到 vault-map.json 解析出的项目路径。

3. **确定默认分支**：
   ```bash
   DEFAULT_BRANCH=$(git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's@^refs/remotes/origin/@@')
   DEFAULT_BRANCH=${DEFAULT_BRANCH:-main}
   ```

4. **确保工作区干净并拉取最新**：
   ```bash
   git fetch origin
   git checkout "$DEFAULT_BRANCH"
   git pull origin "$DEFAULT_BRANCH"
   ```

5. **检查 feature 分支是否存在**：
   ```bash
   if git rev-parse --verify "$TARGET_BRANCH" >/dev/null 2>&1; then
     echo "feature 分支存在，执行合并"
   else
     echo "feature 分支已不存在（可能已被手动删除），跳过合并直接标记完成"
     # 跳到步骤 7 的"成功"路径
   fi
   ```

6. **合并 feature 分支**：
   ```bash
   git merge --no-ff "$TARGET_BRANCH" -m "merge: <title> (#<id>)"
   ```

7. **处理合并结果**：

   **合并成功**：
   - Push：`git push origin "$DEFAULT_BRANCH"`
   - 删除远程 feature 分支：`git push origin --delete "$TARGET_BRANCH"`
   - 删除本地 feature 分支：`git branch -d "$TARGET_BRANCH"`
   - 更新状态：
     ```bash
     python3 ~/.claude/skills/obsidian-task-runner/scripts/update_task_status.py \
       <task_path> status=done merge_approved=false
     ```
   - 在「## 实现记录」section 追加合并记录：
     ```markdown
     ### 合并
     - 分支: `<target_branch>` → `<default_branch>`
     - 合并时间: <本地时间>
     - 状态: 成功
     ```

   **合并冲突**：
   - 记录冲突文件列表：`git diff --name-only --diff-filter=U`
   - 中止合并：`git merge --abort`
   - 切回 feature 分支：`git checkout "$TARGET_BRANCH"`
   - 更新状态：
     ```bash
     python3 ~/.claude/skills/obsidian-task-runner/scripts/update_task_status.py \
       <task_path> status=conflict merge_approved=false
     ```
   - 在任务文档新建「## 合并冲突」section，写入：
     - 冲突文件列表
     - 目标分支和 feature 分支名称
     - 解决指引："请手动解决上述冲突后，`git add` + `git commit` + `git push`，完成后重新设置 `merge_approved: true`"

8. **退出**：输出 JSON 摘要：

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
- 如果 `blocked_by` 非空：检查所有依赖任务，如果有任何一个不在 `done` 或 `review`，设置为 `blocked` 并列出未完成的依赖

### 特殊情况：assignee 字段委派 Agent

每个任务通过 frontmatter 的 `assignee` 字段决定由哪个 Agent 执行：

| assignee | Agent | 说明 |
|----------|-------|------|
| `codex` | Codex CLI | 后台非交互模式，通过 prompt 引导读取 SKILL.md 后执行 |
| `claude` | Claude Code | 原生 Skill 机制，自动加载 SKILL.md |
| `claude+human` | Claude Code | 同 `claude`，human 仅参与 review gate |
| 未设置 / 其他 | 回退 `TASK_RUNNER_AGENT` | 默认 `codex`，可通过环境变量覆盖 |

**弃用字段**：`switch_settings` 已被 `assignee` 取代。旧字段仍可识别但不再推荐：
- daemon 仅在 `assignee: claude` 路径下检查 `switch_settings`，用于切换到 aigateway wrapper（`claude-gateway.sh`）
- 前提：`~/.claude/claude-gateway.sh` 存在且可执行

### 特殊情况：新需求自动创建 TASK

当用户在 `Requirements/` 下新建 `REQ-<id>-<slug>.md` 文件时：
- `on_req_changed.py` 自动在 `Tasks/` 下生成 `TASK-<id>-<slug>.md`
- 自动填充字段：`id`、`title`、`project`、`priority`、`tags`、`epic`、`req_doc`、`reviewer`
- **`assignee` 留空**——必须由用户填写 `codex` / `claude` / `claude+human` 后 daemon 才会拾取
- `project` 为空时生成 `status: blocked`，提示用户补全后改 `ready`
- `project` 已填时生成 `status: ready`，用户填完 `assignee` 后自动进入 Round 1
- 已存在关联任务时不重复创建，按原有变更逻辑处理（reset / pending_req）
- 文件名不匹配 `REQ-<id>-<slug>.md` 的需求文档不自动创建 TASK（只记录 warning）

## 输出格式

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

所有工具脚本都在 `~/.claude/skills/obsidian-task-runner/scripts/` 下：
- `find_ready_tasks.py` — 发现可处理任务
- `update_task_status.py` — 更新 frontmatter 字段
- `resolve_project_path.py` — 项目名 → 本地路径
- `register_project.py` — 注册新项目

配置文件在 `~/.claude/skills/obsidian-task-runner/config/vault-map.json`。

## 参考文档

详细的状态流转、字段说明、故障排查见 `reference.md`。
