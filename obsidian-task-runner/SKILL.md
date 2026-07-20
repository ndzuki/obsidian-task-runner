---
name: obsidian-task-runner
description: >
  读取 Obsidian Vault 中的需求文档和任务文档，自动理解要求并实现代码。
  两轮状态机：Round 1 出计划、Round 2 写代码。
  支持自动发现可处理任务、解析项目路径、创建新项目脚手架、
  运行测试和 lint、提交到分支。
  支持需求类型识别（路线图/领域索引/原子需求）与 depends_on 依赖链解析，
  确保任务按正确顺序执行，阻止依赖未满足时提前开始。
  集成 skill://grilling 对齐追问和 skill://domain-modeling 共享词汇维护。
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
6. **事后总结写入领域模型**：每轮（Round 1 / Round 2 / Merge Phase）结束后，若有新发现的**领域模式、反模式（陷阱）或跨任务经验**，追加到 `$OBSIDIAN_VAULT/Projects/<project>/Notes/CONTEXT.md` 的 `## Patterns` 或 `## Anti-patterns` section。架构决策走 ADR，领域术语走 CONTEXT.md `## Language`。不复述已在 TASK 文档「实现记录」中的内容。
7. **依赖优先**：任何任务在 `depends_on` 中的前序需求对应的 TASK 未 `done` 前，不得进入 Round 2 实现阶段。Round 1 出计划可以提前执行（了解全局上下文），但计划中必须标注阻塞依赖。
8. **维护共享领域词汇**：所有计划、实现记录、代码命名必须使用项目 CONTEXT.md 中定义的术语。Round 1 和 Round 2 过程中新出现的领域概念，即时调用 `skill://domain-modeling` 更新 CONTEXT.md。架构决策满足 ADR 三条件时，主动提议创建 ADR。

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
- 领域词汇：`$OBSIDIAN_VAULT/Projects/<project>/Notes/CONTEXT.md`
- 架构决策：`$OBSIDIAN_VAULT/Projects/<project>/Notes/adr/`
- 可从 `file_path` 推导：`dirname(dirname(task_file))` = 项目目录

### Step 2.3: 加载项目领域模型（Round 1 必执行）

**目标**：加载项目共享词汇和架构决策，确保后续所有命名和设计使用一致术语。

1. **读取 CONTEXT.md**（如果存在）：
   - 路径：`$OBSIDIAN_VAULT/Projects/<project>/Notes/CONTEXT.md`
   - 将其中所有领域术语加载为当前会话的词汇表
   - 后续所有计划、实现记录、代码标识符必须使用 CONTEXT.md 中的规范术语
   - 发现代码中使用 `_Avoid_` 标注的废弃术语 → 在计划中标注为需要统一

2. **读取 ADR**（如果存在）：
   - 扫描 `$OBSIDIAN_VAULT/Projects/<project>/Notes/adr/` 下的所有 ADR
   - 理解已做出的架构决策，不重新讨论、不违反
   - 如果需求与已有 ADR 冲突 → 在计划中标注并提议讨论

3. **CONTEXT.md 不存在时的处理**：
   - 不必强求创建，但 Round 1 出计划时若产生新的领域术语，应新建 CONTEXT.md
   - 从需求文档和已有代码中提取候选术语，在计划末尾提议"建议纳入 CONTEXT.md 的术语列表"

### Step 2.5: 项目依赖分析（Round 1 & blocked 判定前必执行）

**目标**：理解项目全局结构和需求间的依赖关系，确保任务按正确顺序执行。

**此步骤必须在 Step 3 判定 `blocked` 状态之前执行**。

1. **读取项目路线图**：
   - 在 `$OBSIDIAN_VAULT/Projects/<project>/Requirements/` 下搜索 `type: requirement-roadmap` 的需求文档（如 REQ-001）
   - 解析路线图中的交付波次（Wave）、依赖链（`A -> B -> C`）和里程碑
   - 如果找不到路线图，扫描所有需求文档，从各需求的 `depends_on` 字段反向推导依赖图

2. **分类需求类型**：

   读取任务关联的需求文档（`req_doc` frontmatter 字段），根据需求文档自身的 frontmatter `type` 分类：

   | 需求类型 | `type` 值 | 含义 | 处理方式 |
   |----------|-----------|------|----------|
   | 路线图 | `requirement-roadmap` | 项目总览，定义 Wave 和依赖链 | **禁止创建任务**，仅用于分析依赖 |
   | 领域索引 | `requirement-index` | 领域内原子需求列表 | **禁止创建任务**，标注"本文不直接创建实现任务" |
   | 原子需求 | 无特殊 `type` 或有 `depends_on`/`domain` | 可独立实现的需求单元 | 正常创建任务，按依赖顺序执行 |

   **重要**：如果当前处理的任务关联的需求文档是 `requirement-roadmap` 或 `requirement-index` 类型，输出错误并退出："此需求是路线图/领域索引，不应创建实现任务。请选择其下的原子需求。"

3. **解析依赖链**：

   从需求文档的 frontmatter 提取 `depends_on` 字段（数组）：
   ```yaml
   depends_on: ["039", "009"]
   ```

   对于每个依赖的 REQ-ID：
   - 在 `$OBSIDIAN_VAULT/Projects/<project>/Tasks/` 下搜索对应的 TASK 文件（文件名含该 ID）
   - 读取该 TASK 的 frontmatter `status` 字段
   - 判定依赖是否满足：

   | 依赖任务状态 | 判定 |
   |-------------|------|
   | `done` | ✅ 依赖已满足 |
   | `review` | ⚠️ 代码已实现但未合并，可出计划但 Round 2 必须等 merge 后执行 |
   | `implementing`、`plan-review`、`ready` | ❌ 依赖未完成 |
   | 不存在 | ❌ 依赖任务尚未创建 |

4. **更新任务 `blocked_by`**：

   将所有未完成的依赖 REQ-ID 映射为对应的 TASK-ID，写入任务的 `blocked_by` frontmatter：
   ```bash
   otg update-status \
     <task_path> \
     blocked_by="TASK-039,TASK-009"
   ```

   如果所有依赖都已 `done`，且 `blocked_by` 非空，清空之：
   ```bash
   otg update-status \
     <task_path> \
     blocked_by=""
   ```

5. **构建依赖上下文**（供 Round 1 计划生成使用）：

   对于每个已完成的依赖任务：
   - 读取其需求文档，提取提供的契约（proto message、接口、数据模型）
   - 读取其实现记录，了解产出的文件和关键设计决策
   - 在生成当前任务的实现计划时，显式引用这些前序产出，避免重复设计或接口不兼容

   对于未完成的依赖：
   - 记录缺失的契约/接口
   - 在计划的"前置条件"部分列出，让审阅者清楚当前任务的阻塞状态
### Step 3: 判断当前阶段

解析任务文档的 YAML frontmatter，关注 `status`、`plan_approved`、`merge_approved` 和 `adr_approved`：

**ADR 写入优先检查**（在任何状态转换之前）：
- 如果 `adr_approved: true` 且 `adr_proposed` 非空 → 执行 **ADR 写入子流程**（见下方），写入完成后清空 `adr_proposed`，然后继续正常状态判断。
- 如果 `adr_approved: true` 但 `adr_proposed` 为空 → 无 ADR 待写入，跳过。
- 如果 `adr_approved` 未设置（非 `true`）且 `adr_proposed` 非空 → **输出醒目提醒**（见下方），然后继续正常状态判断。这个提醒确保用户不会漏掉待审 ADR。

**待审 ADR 提醒**（每次调用只要 `adr_proposed` 非空且 `adr_approved` 非 `true` 就输出）：

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│  ⚠️  待审 ADR 提议（adr_approved 尚未授权）                  │
│                                                             │
│  以下 ADR 已提议但尚未写入。请审查后设 adr_approved: true：   │
│                                                             │
│  ▸ Event Sourcing for Order Write Model                     │
│  ▸ PostgreSQL for Read Model Projections                    │
│                                                             │
│  在 task frontmatter 中设置 adr_approved: true 以授权写入。  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

> 这个提醒在每次任务调用时都会出现，直到 `adr_approved: true` 被设置且 ADR 写入完成。用户无法忽略。

| 当前状态 | plan_approved | merge_approved | 动作 |
|----------|---------------|----------------|------|
| `ready` 或无 | — | — | 走 **Grilling 通知流程**（见下方），设置 `needs-grilling`，通知用户交互式完成任务，然后退出。**不在 daemon 中生成计划。** |
| `needs-grilling` | — | — | 任务等待用户交互式 grilling。如果 status 仍是 `needs-grilling`（用户尚未完成）→ 再次通知，退出。如果已是 `plan-review` 或 `implementing`（用户已完成，交互式会话已改 status）→ 不会进入此行——由对应行处理。 |
| `plan-review` | `true` | — | 走 Round 2 |
| `plan-review` | 非 `true` | — | 输出 "等待人工批准" 并退出 |
| `review` | — | `true` | 走 Merge Phase |
| `review` | — | 非 `true` / 无 | 输出 "任务已在 review 状态，等待人工 review" 并退出 |
| `conflict` | — | `true` | 走 Merge Phase（重新尝试合并） |
| `conflict` | — | 非 `true` / 无 | 输出 "任务存在合并冲突，请解决后重新设置 merge_approved: true" 并退出 |
| `blocked` | — | — | 执行 Step 2.5 依赖分析后，若所有 `blocked_by` 依赖已 `done` 且必填字段已补齐，自动改为 `ready` 再走 Grilling 通知流程；否则输出缺失字段/未完成依赖并退出 |
| `done` | — | — | 输出 "任务已完成" 并退出 |
| `implementing` | — | — | 检查项目是否已有代码产出，有则继续 Round 2，否则视为异常重新进入 Round 2 |

### Grilling 通知流程

当 daemon 在 `ready` 状态检测到任务需要需求对齐时，**不对任务进行自动分析**。
而是写入 grilling 上下文、通知用户、然后退出。用户手动完成交互式 grilling 后，
daemon 在下次轮询中继续。

**触发条件**：`ready` 状态的任务首次进入 Round 1。

**流程**：

1. **加载上下文**（Step 2、Step 2.3、Step 2.5）：
   - 读取项目配置、CONTEXT.md、ADR、依赖链
   - 读取需求文档

2. **写入 grilling 上下文到任务文档**：
   ```bash
   otg update-status <task_path> \
     status=needs-grilling \
     grill_context="<需求标题>: <一句话描述需要对齐的关键决策点>"
   ```

   同时在任务文档的「## Grilling 上下文」section 写入详细信息：
   ```markdown
   ## Grilling 上下文

   > 此任务需要交互式需求对齐。请在终端中完成 grilling 对话。

   - **需求文档**: [[<req_doc>]]
   - **依赖上下文**: <已完成的前序任务及其产出>
   - **关键决策点**: <从需求文档中识别出的模糊点列表>
   - **建议技能**: skill://requirement-elaborator
   ```

3. **Kitty 新 tab 通知**（主要方式）：
   ```bash
   kitty @ launch --type=tab --title "Grilling <task_id>" \
     bash -c 'echo "🟡 需要对 <req_doc> 进行需求详细化（grilling 追问 + 技术规格生成）"; echo; exec omp'
   ```

   > `kitty @ launch` 在当前 Kitty 实例中创建新 tab。用户看到新 tab 后切换过去，
   > 直接与 OMP 对话完成 grilling。`allow_remote_control yes` 必须在 `kitty.conf` 中启用。

4. **桌面通知**（fallback，同时发送）：
   ```bash
   notify-send -u critical -t 10000 \
     "OTG: <task_id> 需要需求对齐" \
     "已在新 Kitty tab 中打开 OMP。说：对 <req_doc> 进行需求详细化"
   ```

5. **退出**：daemon 本轮结束。不等待用户、不阻塞。

**用户侧操作**：
1. 看到新 Kitty tab 或桌面通知
2. 切换到新 tab，OMP 已就绪
3. 对 OMP 说："对 <req_doc> 进行需求详细化"
4. OMP 加载 `requirement-elaborator` → grilling 对话 → 生成技术规格 → 写入 Obsidian
5. 用户审阅生成的计划后，在 Obsidian 中设 `plan_approved: true`
6. daemon 下次轮询看到 `plan-review` + `plan_approved: true` → 进入 Round 2

**Kitty 不可用时的降级**：
- 如果 `kitty @ ls` 失败（Kitty 未运行或 remote control 未启用）→ 仅使用 `notify-send`
- 同时在终端 stderr 输出醒目的 boxed message 作为最后手段

### Step 4: Round 1 — 出计划（仅在 grilling 完成后执行）

**重要**：Round 1 的 grilling 阶段已移至「Grilling 通知流程」。daemon 在 `ready` 状态下不生成计划，而是通知用户完成交互式 grilling。此 Step 4 仅在以下场景执行：
- 用户完成交互式 grilling 后，需求文档已包含详细技术规格，daemon 可据此直接生成实现计划（罕见）
- 或者任务通过 `auto_approve: true` + 需求文档已详细完备 跳过 grilling（未来优化）

**当前默认行为**：daemon 在 `ready` 状态下走 Grilling 通知流程后退出。此 Step 4 作为 fallback 保留。
**重要**：如果任务文档的「## 实现记录」或「## 验收记录」section 已有内容（说明这是因需求变更触发的**重新出计划**），则 Round 1 生成计划后**必须停在 `plan-review`**，等待人工确认。不得因为"代码已经实现过了"就跳过 plan-review 直接进入 review。即使 `auto_approve: true`，重新出计划场景下也必须停在 plan-review。

0. **回顾依赖上下文**（Step 2.5 的产出）：
   - 列出所有已完成的前序依赖及其提供的契约/接口
   - 列出所有未完成的依赖及阻塞原因
   - 计划中必须引用前序产出，避免重复设计或契约冲突

1. **读需求文档**：根据 `req_doc` 字段读 `$OBSIDIAN_VAULT/<req_doc>`。
   - 如果 `req_doc` 为空，从任务文档的「需求摘要」section 提取需求，按 L1 处理。

2. **分析项目上下文**：
   - 如果项目已有代码（`status: existing`）：读目录结构、`go.mod`/`package.json`/`Makefile`、现有代码风格、测试框架
   - 如果是新项目（`new_project: true`）：只出脚手架方案——目录结构、技术选型（根据 `template` 字段）、构建工具配置。**不创建任何文件**

3. **生成实现计划**：
   - 首先列出「前置条件」：已完成的前序任务及其提供的契约/接口，待完成的依赖及阻塞原因
   - 分步骤，每步有明确的产出物
   - 每步预估代码量（文件数、行数）
   - 标注关键决策点（需要人工确认的地方）
   - 标注对前序产出的引用（如"复用 TASK-010 的 `RequestMetadata` message"）
   - 如果项目已有 task-verifier，列出验收标准的映射
   - **使用 CONTEXT.md 术语**：计划中所有实体、模块、接口命名必须使用 CONTEXT.md 定义的规范术语。避免使用 `_Avoid_` 列表中的废弃术语。如果 CONTEXT.md 不存在，在计划末尾附上"建议纳入 CONTEXT.md 的术语列表"

4. **写回任务文档（版本化，不覆盖）**：

   a) 用 `update_task_status.py` 更新 frontmatter：
      ```bash
      otg update-status \
        <task_path> status=plan-review plan_version=<新版本号>
   b) **追加计划版本**：使用**严格表格格式**（跨模型兼容，便于审计）：
      ```markdown
      ### v{N} · YYYY-MM-DD
      > 基于需求: <req_doc> | 变更: <原因>
      > 前置依赖: <已完成的前序 TASK-ID 列表，逗号分隔；无则为「无」>
      > 阻塞依赖: <未完成的依赖 TASK-ID 列表及阻塞原因；无则为「无」>

      #### Step 1: <步骤名称>
      | 维度 | 内容 |
      |------|------|
      | 目标 | <一句话描述要达成什么> |
      | 产出 | <新建/修改的文件列表，逗号分隔> |
      | 依赖前序 | <引用的前序 TASK 产出，如"TASK-010 的 RequestMetadata"> |
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


5. **维护领域模型（事后总结）**：

   加载 `skill://domain-modeling`，根据本轮计划产出的内容更新项目领域模型：

   a) **更新 CONTEXT.md**：
      - 如果 CONTEXT.md 不存在且计划中产生了领域特定术语 → 新建 `$OBSIDIAN_VAULT/Projects/<project>/Notes/CONTEXT.md`
      - 如果计划中引入了新的领域概念（如新的实体、新的流程名称）→ 追加到 CONTEXT.md 的 `## Language` section
      - 如果计划中统一了模糊术语（如将"工单/ticket/issue"统一为"Task"）→ 更新对应条目，将废弃术语加入 `_Avoid_`
      - 格式遵循 `skill://domain-modeling` 中「CONTEXT.md — The glossary」section 的 Format 定义

   b) **提议 ADR**（仅在满足全部三条件时）：
      - 本轮计划中有**难以逆转的架构决策**（技术选型、模块边界、集成模式）
      - 该决策**对后续开发者而言是意外的**（不记录会有人疑惑）
      - 该决策是**在多个真实备选方案中做出的权衡**
      - 满足条件 → 执行以下操作：

      1. 为每个提议的 ADR 生成 `title`（短标题）和 `slug`（小写、空格替换为 `-`）
      2. **记录到 task frontmatter**：
         ```bash
         otg update-status <task_path> adr_proposed='[{"title":"Event Sourcing for Order Write Model","slug":"event-sourcing-order-write"},{"title":"PostgreSQL for Read Model Projections","slug":"postgres-read-projections"}]'
         ```
      3. **在任务文档中写入醒目的 ADR 提议 section**（在 `## 变更记录` section 之前插入）：

         使用 Obsidian callout 格式，确保在 Obsidian 中显眼渲染：

         ```markdown
         ## ADR 提议

         > [!important]- ADR 提议（待授权）
         > 以下架构决策记录已提议但**尚未写入**。请审查后授权：
         >
         > | # | ADR 标题 | Slug |
         > |---|----------|------|
         > | 1 | Event Sourcing for Order Write Model | event-sourcing-order-write |
         > | 2 | PostgreSQL for Read Model Projections | postgres-read-projections |
         >
         > **操作**：审查完毕后，在 task frontmatter 中设置 `adr_approved: true`。
         > 下次任务调用时，agent 自动写入 `Notes/adr/` 目录。
         ```

         > ⚠️ `> [!important]` callout 在 Obsidian 中渲染为红色/橙色高亮框，用户绝对不会错过。
         > 使用 `-` 折叠标记（`[!important]-`）使其默认折叠，避免干扰日常任务查看。

      4. 在实现计划末尾也输出简要 ADR 摘要（供 plan-review 阶段参考）。
         ```markdown
         ## ADR 提议摘要

         > 详见任务文档「ADR 提议」section。

         | # | ADR 标题 | Slug |
         |---|----------|------|
         | 1 | Event Sourcing for Order Write Model | event-sourcing-order-write |
         | 2 | PostgreSQL for Read Model Projections | postgres-read-projections |
         ```

   c) **输出摘要**：在 Round 1 完成消息中附带术语变更摘要：
      ```markdown
      📝 领域模型更新：
         - CONTEXT.md: 新增术语 <N> 个，更新 <N> 个
         - ADR 提议: <N> 条（已记录到 adr_proposed），设 adr_approved: true 授权写入
      ```

### Step 5: Round 2 — 实现

**目标**：按批准的计划实现代码并提交。

**前置检查**：在开始实现前，确认计划的「阻塞依赖」列表为空。如果仍有未 `done` 的依赖，输出错误并退出："以下依赖尚未完成，无法进入 Round 2：<TASK-ID 列表>"。

0. **验证依赖已解除**：读取任务 `blocked_by` 字段，确认所有依赖 TASK 的 `status` 均为 `done`。若有任何非 `done` 依赖，输出错误并退出。
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
   - **Tracer Bullet 约束**（强制红-绿循环）：
     1. **一条验收标准 = 一条示踪弹。** 对计划的每一步，按 AC 逐条推进。绝不批量写所有测试再批量写所有代码（anti-pattern: horizontal slicing）。
     2. **Red**：针对当前 AC 先写一个最小失败测试 → `go test -race -run <TestName>` 确认失败。
     3. **Green**：写刚好足够的代码使测试通过 → 再次 `go test -race -run <TestName>` 确认通过。
     4. **Refactor**（仅在绿后）：如果代码需要整理，在测试绿了之后做。红时绝不重构。
     5. **下一条 AC**：只有当前 AC 的测试绿了，才进入下一条 AC。每条 AC 都是一颗完整的示踪弹——从测试到实现到验证。
     6. **测试只测公共接口**（seam）：测试名描述行为而非实现（"用户可以用有效购物车结账"而非"CheckoutHandler.Execute 返回 200"）。内部重命名不应破坏测试。
     7. **期望值来自独立来源**：测试中的期望值必须是字面量或 spec 中明确定义的值——绝不从被测代码反向推导（tautological test）。

   - **Round 2 暂停条件**（以下任一触发时，暂停并向用户求助）：

     | 触发条件 | 自主尝试上限 | 行为 |
     |----------|-------------|------|
     | **测试连续失败** | 同一 AC 的测试修复尝试 ≤ 3 次 | 第 4 次仍失败 → 暂停。设置 `needs-grilling`，写入阻塞上下文，Kitty 通知用户。 |
     | **计划外设计决策** | 0 次（立即暂停） | 实现过程中发现计划未覆盖的模糊点（如"审批驳回后回退到哪一步？"）。不自行决定——暂停，写入问题上下文。 |
     | **依赖冲突** | 尝试 1 个替代方案 | 如果计划的库/版本不可用，尝试 1 个替代。仍失败 → 暂停。 |
     | **架构摩擦** | 0 次（立即暂停） | 发现代码库现状与计划假设不一致（如需要修改的模块已被其他任务大幅重构）。暂停，写入差异描述。 |
     | **Tracer Bullet 无法穿透** | ≤ 3 次重构 | 测试写不出来（无法找到正确的 seam）→ 暂停。这本身是一个重要信号——代码架构阻止了测试。 |

   - **暂停流程**（复用 Grilling 通知流程）：
     1. 在任务文档的「## Round 2 阻塞」section 写入：
        ```markdown
        ## Round 2 阻塞

        > ⚠️ 实现过程中遇到需要你决策的问题。

        - **阻塞类型**: <测试失败 / 设计决策 / 依赖冲突 / 架构摩擦>
        - **当前 AC**: <AC-N 描述>
        - **问题描述**: <具体阻塞点>
        - **已尝试**: <已尝试的方案和结果>
        - **需要的决策**: <明确列出用户需要回答的问题>
        ```
     2. **保存暂停前状态**：
        ```bash
        otg update-status <task_path> \
          status=needs-grilling \
          grill_prev_status=implementing
        ```
        > `grill_prev_status` 记录暂停来源——恢复时用。
        > `target_branch` 保留不变（Round 2 的 worktree）。
     3. 触发 Kitty 通知（同 Grilling 通知流程）
     4. daemon 退出

   - **恢复流程**（用户完成交互式 grilling 后）：

     **用户侧**（在交互式 OMP 会话中）：
     ```bash
     otg update-status <task_path> \
       status=implementing \
       grill_done=true \
       grill_context=""
     ```
     > `status=implementing` 即 `grill_prev_status` 的保存值（Round 2 暂停时写入 `grill_prev_status=implementing`，恢复时原值写回）。
     > 标记 grilling 完成（`grill_done=true`），清空阻塞上下文（`grill_context=""`）。
     > `plan_approved` 和 `target_branch` 保持原值不变。

     > **plan_version=0 守护**：如果任务从 `implementing` 弹回但 `plan_version=0`（从未有过有效计划），
     > daemon 不会等待 grilling，而是**自动转入 `plan-review`** 先出计划。
     > 此时用户应直接设 `plan_approved: true` 让 Round 2 生成计划并实现，
     > 而非按上述恢复流程设 `status=implementing`（无计划无法实现）。

     **daemon 侧**（下次轮询）：
     1. Step 3 看到 `status: implementing` → 走 Round 2（无 `plan_approved` 检查，因为 Round 2 的 gate 早已通过）
     2. `git checkout <target_branch>` → 回到 Round 2 worktree
     3. 从「## Round 2 阻塞」section 读取阻塞点上下文
     4. 从「## 实现记录」找到最后一个已完成的 AC
     5. 继续下一个 AC 的 Tracer Bullet 循环
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


12. **退出**：输出 JSON 摘要，状态为 `review`。如果 `merge_approved` 仍为 `false`，通知用户 review 代码，并由用户决定是否手动创建 PR/merge 或设 `merge_approved: true` 交给 agent 自动处理。


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


9. **退出**：输出 JSON 摘要：


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

### 特殊情况：新需求自动创建 TASK

当用户在 `Projects/<project>/Requirements/` 下新建 `REQ-<id>-<slug>.md` 文件时：
- `otg on-req-changed` 自动在**同一项目**的 `Tasks/` 下生成 `TASK-<id>-<slug>.md`（id 为项目内自增）
- 三者通过 id 和 frontmatter 双向关联（`req_ref` ↔ `task_ref`）
- 自动填充字段：`id`、`title`、`project`、`priority`、`tags`、`epic`、`req_doc`、`reviewer`
- **`assignee` 留空**，且新 TASK 默认 `status: blocked`
- 用户补齐必填字段并保存后，daemon 自动解除 `blocked` → `ready`
- 文件名不匹配 `REQ-<id>-<slug>.md` 的需求文档不自动创建（只记录 warning）
- **向后兼容**：Vault 根目录 `Requirements/REQ-xxx.md`（旧结构）仍按原行为创建项目目录 `{id}-{slug}`

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
