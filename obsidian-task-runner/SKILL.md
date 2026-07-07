---
name: obsidian-task-runner
description: >
  读取 Obsidian Vault 中的需求文档和任务文档，自动理解要求并实现代码。
  两轮状态机：Round 1 出计划、Round 2 写代码。
  支持自动发现可处理任务、解析项目路径、创建新项目脚手架、
  运行测试和 lint、提交到分支。
  当用户在 Obsidian 中设 plan_approved: true 时自动触发下一轮。
  当用户提到"自动执行 Obsidian 任务"、"从 Obsidian 拉任务开发"、
  "自动实现需求文档"、"task runner" 时使用本 skill。
---

# Obsidian Task Runner

你是 Obsidian → Claude Code 自动化流水线的执行引擎。你的工作是在一次 `claude -p` 调用中完成一轮状态推进，然后退出，不发生交互。

## 核心约束

1. **只推进一轮**：Round 1 或 Round 2，不要在一次调用中跨越人工 Gate
2. **写回任务文档**：所有产出（计划、实现记录、验收结果）写入任务 markdown 文件
3. **不推送代码**：git commit 到分支但不 push，不创建 PR，不合并
4. **新项目永远确认**：`new_project: true` 的任务在 Round 1 只出脚手架方案，绝不自动创建

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

解析任务文档的 YAML frontmatter，关注 `status` 和 `plan_approved`：

| 当前状态 | plan_approved | 动作 |
|----------|---------------|------|
| `ready` 或无 | — | 走 Round 1 |
| `plan-review` | `true` | 走 Round 2 |
| `plan-review` | 非 `true` | 输出 "等待人工批准" 并退出 |
| `review` | — | 输出 "任务已在 review 状态，等待人工 review" 并退出 |
| `done` | — | 输出 "任务已完成" 并退出 |
| `implementing` | — | 检查项目是否已有代码产出，有则继续 Round 2，否则视为异常重新进入 Round 2 |

### Step 4: Round 1 — 出计划

**目标**：理解需求，生成可执行的实现计划。

1. **读需求文档**：根据 `req_doc` 字段读 `$OBSIDIAN_VAULT/<req_doc>`。
   - 需求文档通常是标准化的模板结构（基于 `REQ-000-template.md`），包含这些 section：
     - `## 背景 & 动机` → 理解为什么做
     - `## 功能需求` → FR-1, FR-2... 用 Given-When-Then 描述
     - `## 非功能性需求` → 性能/安全/可用性指标
     - `## 技术约束` → 语言、框架、数据库等硬性限制
     - `## 验收标准` → AC-1, AC-2... 可验证条件
     - `## API 规格` → 接口契约（如涉及）
     - `## 数据模型` → 实体定义（如涉及）
     - `## 风险 & 边界` → 已知风险、不在范围内的东西
   - 如果 `req_doc` 为空，从任务文档的「需求摘要」section 提取需求。
   - 如果需求文档缺失关键信息（如技术约束、验收标准），在生成计划时标注为「信息缺失，需要人工补充」。

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

### 特殊情况：auto_approve

如果 `auto_approve: true` 且 `new_project != true`：
- Round 1 完成后不退出，直接继续 Round 2
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

## 关键路径

所有工具脚本都在 `~/.claude/skills/obsidian-task-runner/scripts/` 下：
- `find_ready_tasks.py` — 发现可处理任务
- `update_task_status.py` — 更新 frontmatter 字段
- `resolve_project_path.py` — 项目名 → 本地路径
- `register_project.py` — 注册新项目

配置文件在 `~/.claude/skills/obsidian-task-runner/config/vault-map.json`。

## 参考文档

详细的状态流转、字段说明、故障排查见 `reference.md`。
