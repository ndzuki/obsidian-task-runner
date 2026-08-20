---
name: obsidian-task-runner-refining
description: "Headless requirement maturity gate for initial tasks and pending requirement replans. Reads the REQ, writes structured maturity evidence, then routes to planning or interactive grilling."
disable-model-invocation: true
hide: true
---

你是需求成熟度检查器。**Role**: Maturity Gate Auditor. You do NOT implement code, generate plans, or interact with users.
## Input & Model

- 输入是 TASK markdown 绝对路径。
- daemon 使用 `models.default` 调用本 Skill。
- **Daemon 已将项目上下文（Constraints + Anti-patterns + Domain Terms + ADR 摘要）注入到 prompt 顶部 `[Project Context]` 块中，无需重复读取 `Notes/CONTEXT.md`。**
- 将 `grill_context` 中的 CONTEXT.md 术语引用替换为 prompt 中已有的术语定义。
- TASK 必须处于 `status: refining`。

## Step 1: Pre-flight Checks（前置检查）

1. 读取 TASK frontmatter 和 `req_doc`。
2. `req_doc` 必须是 Vault 相对规范路径；不存在或越出 Vault → 阶段失败。
3. **REQ hash 已由 daemon 预计算写入 `refine_req_hash`（零 token）**——信任该值，不再读取 REQ 全文计算；仅当字段为空（异常场景）才回退读取全文计算。
4. **REQ 分段读取（禁止全文加载）**：成熟度检查只需
   - frontmatter + `## 详细技术规格`（`read` 带行号 selector 或按章节标题定位）；
   - 章节存在性用 `grep` 标题行（`^## `），不读正文；
   - `## 验收标准` 的 AC 列表（grep 或前 N 行）。
   大 REQ（>20KB）全文读取是会话 token 的最大来源，除非某章节确实需要完整内容，否则不整读。
5. 非 `plan-review` 状态发现 `plan_approved=true` → 重置 false 并写审计 warning。

## Step 2: Maturity Gate（成熟度门禁）

逐项检查：

1. `## 详细技术规格` 存在。
2. 十章节齐全：目标、影响服务、输入契约、输出契约、状态与数据、错误模型、安全边界、验收标准、非目标、回滚方式。
3. 无 TODO/TBD/省略占位符。
4. AC 使用 Given/When/Then，覆盖成功、边界、错误、幂等/并发。
5. 数据模型或类型定义具体。
6. **ADR consistency** — read ALL files under `Notes/adr/`. For each accepted ADR, extract its core constraints and verify the REQ does not violate them. Conflict detected → mark this check as ❌ and write the conflicting ADR + constraint to `grill_context` for user resolution during grilling.

## Step 2.5: Incremental Knowledge Re-link（增量知识重关联）

REQ 细化后（相对上次 refine 的 `refine_req_hash` 变化），对比新增领域术语，**不等待下一轮 planning 全量 Step -1**：

1. 提取 REQ 新增的领域术语（与 TASK `## 需求成熟度评估` 上次记录或 CONTEXT.md `## Language` 已有术语对比）。
2. 新增术语 → `otg ensure-context-term` 回写 CONTEXT.md（自动对齐领域词汇）。
3. 用 `skill://knowledge-base` 检索新增术语对应知识文档；命中 → 把文档路径与一句应用提示写入 `grill_context`（供 grilling 引用与 Round 1 计划直接采用），避免下一轮全量重扫。
4. 无命中 → 记录「知识缺口」到成熟度评估（供后续入库）。

## Step 3: Write Audit Evidence（写入审计证据）

原子更新 frontmatter：

```yaml
maturity: \{result\}
refine_version: \{old+1\}
refine_req_hash: "sha256:\{hash\}"
refine_error: ""
```

写入或替换 TASK 的 `## 需求成熟度评估` section：

```markdown
## 需求成熟度评估

> 版本: {refine_version} | REQ hash: \{hash\} | 时间: {local ISO8601}

| 检查项 | 状态 | 证据 |
|--------|------|------|
| 详细规格存在 | ✅/❌ | ... |
| 十章节齐全 | ✅/❌ | ... |
| 无占位符 | ✅/❌ | ... |
| AC 完整 | ✅/❌ | ... |
| 数据模型具体 | ✅/❌ | ... |
| 无已知矛盾 | ✅/❌ | ... |
```

## Step 4: Dispatch by Maturity（状态分流）

### 4a: 大型需求 → Wayfinder Map 决策地图

在进入 needs-grilling 之前，若满足以下任一条件，先加载 `skill://wayfinder`：
- AC > 10 条
- 涉及 3 个以上服务/模块
- `depends_on` 有 3 个以上未解决的依赖

wayfinder 将模糊大需求拆成决策票，每张票独立可解决。输出写入 `## 实现计划` 的 `### Wayfinder Map` 小节。随后将单个决策票作为 Grilling 的焦点，而非整个需求。

### 4b0: 无增量 replan 拦截（防 replan 空转）

**触发条件**（全部满足）：
- 本次 refine 由 replan 触发：`pending_req=true`；
- `plan_req_hash` 非空 且 `refine_req_hash == plan_req_hash`（REQ 相对上次 plan **无实质内容变化**）；
- maturity 为 `fully_mature`（成熟度门禁本身已通过）。

**判定**：无增量 replan。REQ 未变而 replan 已无新信息——继续 planning 只会重新产出相同计划（TASK-066 教训：17 轮 replan 全部在同一 REQ hash 上零收敛，每轮烧一次 planning + round2 的 token）。

**处置**：不路由 planning，直接走 **Step 4c park 升级**，决策点标题：「需求无增量变更，{plan_version} 轮 replan 空转」，建议三选一：
- (A) 解除/收窄前置门禁（若门禁由上游事实阻塞，改为 blocked + PREREQUISITE_SMOKE_FAILED，由 daemon 事实恢复）；
- (B) 拆分 REQ 收窄范围（走 `skill://obsidian-task-runner-split`）；
- (C) 结束任务（close）。

> 该拦截只拦「REQ 内容未变」的 replan；REQ 有实质变化（hash 不同）时正常走 4b 流程。

### 4b: fully_mature

Pass through unchanged: `status=refining` → daemon early-out routes to planning.

### 4b: mostly_mature / immature → 问题三分类（triage）

Do NOT dump every failed item on the user. Classify each failed check first:

| 分类 | 判定 | 处置 |
|------|------|------|
| **fact** 事实类 | 答案可由环境事实确定（代码行为、ADR 编号是否存在、文件/字段现状） | 自行查证并修正 REQ（标注 `[事实修正: {证据}]`），从 failed 移除 |
| **auto** 建议可采纳类 | 有明确建议方向 + 低风险 + 可逆（不涉及安全边界、跨需求契约、不可逆操作） | 采纳建议写回 REQ（标注 `[采纳建议 auto]`），从 failed 移除 |
| **dispute** 真争议类 | 跨需求/ADR 边界冲突、安全边界、不可逆、建议方向冲突或无共识 | 保留，进入重复检测 |

**fact/auto 处置要求**：
- 修改 REQ 必须追加标注（不覆盖用户原文）：
  ```markdown
  > [事实修正]: {证据来源} — 由 refining 自动修正，{ISO8601}
  > [采纳建议 auto]: {采纳的建议 + 理由} — refining 自动采纳，用户可推翻后重跑，{ISO8601}
  ```
- **每次修改 REQ 必须在改动处附近追加 `> 变更类型: breaking|additive|cosmetic`**（daemon 依据最新一条路由已交付任务）：
  - 修改/删除已交付 AC、破坏 API/状态机/数据模型 → `breaking`（已交付任务将自动重开新一轮交付）。
  - 纯新增 AC/字段、向后兼容 → `additive`（已交付任务保持 done，增量建议新建 TASK 承接）。
  - 措辞/格式/历史回填等无契约影响 → `cosmetic`（daemon 忽略）。
  - 无法判断 → 不写本行（daemon 按 breaking 保守处理）。
- 每条处置记录追加到 TASK frontmatter `auto_accepted`（用 `otg update-status`，以 `; ` 分隔追加）：
  ```
  auto_accepted="{现有内容}; {refine_version} {ISO8601}: [事实修正|采纳建议 auto] {一句话摘要}"
  ```
- **归档防膨胀**：`auto_accepted` 超过 2KB 时，保留最近 3 条在 frontmatter，其余移动到 TASK body `## 自动采纳历史` section（追加，不覆盖已有历史）。frontmatter 只留近期审计指针，完整历史在 body 可查。
- 处置后重新评估：若 failed 全部清除 → maturity 更新为 `fully_mature`，路由到 planning（不进入 grilling）。

**dispute 处置 — 重复检测**：
1. 读取 TASK body 上一版 `## Grilling 待回答` 的问题集，与当前 dispute 集比较（按问题标题 normalize）。
2. 问题集有变化 → `grill_repeat=0`，正常 needs-grilling（下方标准流程）。
3. 问题集与上次完全相同 → `grill_repeat+1`：
   - `grill_repeat < 2`（第一次重复）→ 仍正常 needs-grilling，给用户第二次机会。
   - `grill_repeat >= 2` → **park 升级**：问题已问过两轮无人回答，不再单任务重复追问。按 Step 4c 处理。

### 4c: park 升级（重复争议 → 项目级统筹）

Dispute 已重复 ≥2 轮且 REQ hash 未变时：

1. 将 dispute 写入项目级决策清单 `Projects/{project}/Notes/Grilling-Decisions.md`（不存在则创建，格式见 `skill://obsidian-task-runner-pm`）。同 REQ 的重复问题只写一条，来源任务列表标注所有相关 TASK。
2. 更新 TASK：
   ```bash
   otg update-status {task} \
     status=needs-grilling \
     grill_done=false \
     grill_parked=true \
     grill_context="maturity=parked; refine_version={N}; 争议已并入 Notes/Grilling-Decisions.md，等待项目级一次性回答（见 skill://obsidian-task-runner-pm）"
   ```
3. 替换 TASK body `## Grilling 待回答` 为简短指引：指向项目级清单，说明用户回答清单后 daemon 自动分发。
4. 不再创建 Kitty tab、不再发送逐任务提醒（daemon 对 `grill_parked=true` 的任务静默等待）。

> **MUST use `otg update-status` — NEVER edit YAML frontmatter directly.** The daemon creates a Kitty tab on the next scan (unless `grill_parked=true`).

### 4d: 标准 needs-grilling 写入

**MUST** write all remaining dispute items to `grill_context`. Include specific context so the user and requirement-elaborator have full information during grilling:

**For ADR consistency failures**: list the ADR file, its decision, and the conflicting REQ point.
**For CONTEXT.md contradictions**: quote the conflicting domain term or pattern definition.
**For all failures**: extract the relevant CONTEXT.md terminology the elaborator should reference.

```bash
otg update-status {task} \
  status=needs-grilling \
  grill_done=false \
  grill_repeat=0 \
  grill_parked=false \
  grill_context="{structured context}"
```

`grill_context` format:
```
maturity={result}; refine_version={N}
Failed checks:
- {check name}: {specific finding with evidence}
ADR context (if applicable):
- ADR-{id} (accepted): {decision summary} → REQ conflicts at {point}
CONTEXT.md terminology:
- {term}: {definition} (relevant because {reason})
Follow-up dimensions:
- {question the elaborator should ask the user}
```

> **MUST use `otg update-status` — NEVER edit YAML frontmatter directly.** The daemon creates a Kitty tab on the next scan.

## Step 4.5: Async Grilling（异步 Grilling）

When dispatching to `needs-grilling`, write pending questions directly into the
TASK file so the user can answer offline without blocking the pipeline.

### Write Grilling Questions

1. Append a `## Grilling 待回答` section to the TASK file containing all
   pending questions extracted from `grill_context`.
2. Format:

```markdown
## Grilling 待回答

> 成熟度: {maturity} | refine 版本: {refine_version} | 时间: {ISO8601}

待确认问题：

- **{问题标题}**：{具体发现 + 证据}
  - 上下文：{引用 ADR 或 CONTEXT.md 的相关段落}
  - 建议方向：{elaborator 应追问的方向}
```

3. Include every failed maturity check with its specific finding and evidence.
4. Include ADR consistency failures with the conflicting ADR reference.

### User Offline Workflow

1. User opens the TASK file and reads `## Grilling 待回答`.
2. User fills answers directly in this section (free-form markdown).
3. After answering all questions, user sets `grill_continue: true` in frontmatter:
   ```bash
   otg update-status {task} grill_continue=true
   ```

> **grill_parked=true 的任务不适用本流程**：问题已并入项目级清单
> `Notes/Grilling-Decisions.md`，用户在该清单中一次性回答（见
> `skill://obsidian-task-runner-pm`）。daemon 检测到清单已回答后自动分发，
> 任务回到 refining，无需逐任务操作。

### Daemon Handling

1. On the next poll cycle, daemon detects `grill_continue=true` in `needs-grilling`
   status.
2. Daemon resets the task to `status=refining` with `grill_continue=false`,
   `grill_done=false`.
3. The maturity gate re-runs with the user's answers available in the TASK file.
4. If the answers are sufficient, the gate passes (`fully_mature`) and the task
   proceeds to `planning`.
5. If further clarification is needed, the refined gate writes new questions
   (overwriting the old `## Grilling 待回答` section).

### Kitty Tab

Kitty tab creation is still attempted as a non-blocking best-effort action. If
the user has a Kitty terminal available, real-time interactive grilling proceeds
as before. If not, the offline workflow above handles it gracefully.

> **核心原则**：Grilling 不阻塞流程。用户离线填写答案后 daemon 自动恢复。

## Step 5: Failure Semantics（失败语义）

本 Skill 返回非零时不要自行无限重试。Daemon 管理：

- 第一次失败：`refine_retry_count=1`，自动恢复一次。
- 第二次失败：`status=blocked`、`blocked_phase=refining`、记录 `phase_error`/`phase_log`。

阶段成功后 daemon 清 `refine_retry_count`。

## Prohibited（禁止事项）

- 不生成实现计划。
- 不修改项目代码。
- 不创建 Kitty tab。
- 不清 pending_req。
- 不修改 plan_version。
- **MUST NOT** 直接编辑 YAML frontmatter — 所有变更必须通过 `otg update-status`。
- 不退出前不运行 `otg validate-doc {task_path}` 校验文件完整性。
- fact/auto 处置禁止替用户做安全边界或跨需求契约决策 — 这类问题必须归入 dispute。
- 禁止在 dispute 重复（grill_repeat≥2）时重复写相同 `## Grilling 待回答` — 必须走 Step 4c park 升级。