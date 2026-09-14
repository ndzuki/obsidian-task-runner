---
name: risk-aware-planning
description: "Turn a non-trivial change into a reviewable plan before writing code: read existing decisions first, then give each step a test seam, an acceptance criterion, and a risk level, with high-risk steps earning a prototype and PASS/FAIL conditions. Use when a change spans several modules, refactors an existing pattern, or changes a data model or cross-service contract. Trigger: 出计划, 怎么做, 方案, implementation plan."
---

**Role**: Planner。你产出**可审阅、分步、带风险和验收标准**的计划。本阶段不写业务代码、不提交、不建分支。

**为什么不让 agent 直接动手**：返工的最大来源不是实现写错，而是**计划没被看过就开工**——接口方向错了，写得越快越贵；约束漏了一条（"test/prod 其实是 MySQL"），全量测试全绿也会在上线时炸。

## Step 0: 先读既有决策（强制）

**既有决策是这个项目的架构宪法。** 新计划若与一条已接受的决策冲突，而没有显式声明取代它，就是计划失败。

1. 找决策目录（**vault 优先**）：`<vault>/Projects/<项目>/Notes/adr/`、`Notes/CONTEXT.md`；仓库侧 `docs/adr/`、`docs/architecture.md` 作为补充（`glob` 兜一遍）。
2. 每条提取：**标题**、**状态**（accepted/superseded/deprecated）、它施加的**硬约束**。
3. 计划中引用：`遵循 ADR-001（<决策摘要>）`。
4. 冲突 → 在计划里标 `⚠️ ADR 冲突`，**必须**提出一条取代它的新决策。
5. 项目没有决策目录 → 显式写一句"本项目无 ADR，本次以代码现状为约束基线"，然后继续。不要沉默跳过。

> 不读 ADR 就出计划 = 蒙着眼睛开车。

## Step 0.5: 命中既有模式就别重复提案

先判断本次的技术选型是不是项目已经决定过的（数据库、框架、RPC、鉴权、部署目标……）：

- **全部命中既有模式** → 计划里写 `引用 ADR-XXX（<摘要>），不新增决策提案`。
- **有偏离**（新数据库、新框架、新协议、新鉴权机制）→ 走 Step 1 的决策检测，**只为真正新增的部分**提案，其余照旧引用。

## Step 1: 需求一致性与验收标准

1. 把需求读成一组可验证的**验收标准（AC）**。写不出可观察的 AC，说明需求还没想清 → 走 `skill://grilling` 跟用户对齐，别用假设填空。
2. 读 vault 的 `<vault>/Projects/<项目>/Notes/PROJECT-CONVENTIONS.md`（若存在，由 `skill://project-baseline-audit` 产出）——**它的规范与架构约束优先级高于本 skill 和任何全局默认约定**。
3. 列 `depends_on`：本次改动的前置契约是什么，谁在等这个改动。

### 架构决策检测（强制）

**任一触发即提议一条新决策**：

| 触发 | 说明 |
|---|---|
| 新的存储/持久化 | 引入新数据库、缓存、文件存储 |
| 新增或变更跨服务契约 | 新 RPC、改 proto message、新事件类型 |
| 新外部依赖 | 新库、新框架、新基础设施 |
| 替换或废弃既有模式 | 改变某个既有关注点的处理方式 |
| 跨服务数据流变化 | 同步→异步、直调→消息队列、新数据管道 |
| 安全模型变化 | 鉴权机制、RBAC 粒度、信任边界 |
| 与既有 ADR 冲突 | 必须显式取代旧决策 |

决策**标题写决策本身，不写任务**：好 `ADR: 用 <技术> 作为唯一业务数据库`；坏 `ADR: 实现用户导出功能`。

## Step 1.5: 目标区域代码走查（条件触发）

计划质量的上限取决于对目标模块**真实结构**的理解。文档读不出模块边界时，补一次轻量代码探索，让 Step 边界和 seam 与实际代码对齐，降低中途"架构摩擦"。

**触发**（任一）：涉及 3 个以上模块/服务；重构类需求；依赖复杂（≥3 未解决前置）；读了代码仍说不清模块边界与耦合点。

**方法**（只读，产出 ≤300 字）：

1. **热区定位** — `git log --oneline -20` 走查最近变更；deepening 的机会在**正在变**的代码里。
2. **并行只读走查** — 起 2-3 个只读子代理分头看目标模块，记录摩擦点：理解一个概念要跨多少文件（Locality）、模块是否**浅**（接口和实现一样复杂）、纯函数是否只为可测而抽离、耦合是否跨 seam 泄漏、哪部分不可测。
3. **删除测试（Deletion Test）** — 删掉这个疑似"浅"的模块，复杂度是**集中到更小的接口**还是**扩散到调用方**？只有「集中」才值得深化，否则别动。
4. 检查是否已有禁止重开该设计的决策。

**产出**：计划头部 `### 架构探索` — 现状（模块/seam/耦合点，1-3 句/模块）、deletion test 结论（≤3 个候选）、对计划的影响（哪些 Step 边界要按真实 seam 调整）。**每次重出计划都重写本节，不留旧版。**

## Step 2: 生成计划

每个 Step 用固定表格：

```markdown
#### Step N: <名称>
| 维度 | 内容 |
|------|------|
| 目标 | 这个 Step 做完，什么变得可观察 |
| 产出 | 具体文件/接口/文档 |
| 测试 Seam | 本 Step 的测试打在哪层公共接口（HTTP handler / Repository 接口 / 纯函数层）。写不出 = 还没想清被测契约 |
| Step 依赖 | 依赖哪个 Step / 哪个前置契约 |
| 前序契约 | 上游承诺给它什么 |
| 验收 | AC-N（来自 Step 1） |
| 风险 | low / medium / high（不确定性 × 影响面） |
```

涉及新模块或接口设计的 Step，按深度模块原则自检：接口是否简洁（≤3 方法）但背后隐藏足够复杂度？seam 是否放在调用方不需要关心的位置？删掉它复杂度是消失还是扩散？

**风险评级用二因子**：不确定性 × 影响面。不是为了凑一栏，是为了决定下一步要不要做原型。

## Step 3: 高风险 Step → 设计两次 + 原型

`risk: high` **且涉及接口设计**（新模块、跨服务契约、数据流变更、存储抽象）→ 走 `skill://design-pass`（它调用 `skill://codebase-design` 的 Design It Twice 协议）产出方案对比后再写计划，**不要**直接用第一个想法。

> 第一个想法通常不是最好的（Ousterhout）。接口返工是中途阻塞的常见来源——设计两次的成本远低于实现期返工。

`risk: high` 的 Step 还必须附 **Prototype 建议**，用来把"猜测型追问"压缩成"证据型追问"：

```markdown
## Prototype 建议

#### Step N: <名称>（risk: high）
| 维度 | 内容 |
|------|------|
| 验证目标 | 验证 <假设 X> 在 <场景 Y> 下是否可行 |
| PASS 条件 | <可观测的确定性结果>，如"单次查询 <10ms"或"proto 编译通过" |
| FAIL 条件 | <触发追问的条件>，如"需要新增依赖或 API 不兼容" |
| 原型范围 | <最小可运行代码，不含测试/持久化> |
| 预计耗时 | <10 分钟以内> |
| 失败后怎么办 | 带原型证据回到 `skill://grilling`，用户看到的是数据不是猜想 |
```

## Step 4: 环境与清理计划（强制）

计划中任何 Step 若会创建集群/容器/临时文件/凭据（k3d、docker、冒烟日志、kubeconfig），**计划末尾必须包含对应的清理 Step**，并显式声明：

1. 会话退出前删除本次创建的一切临时资源，清理证据（`k3d cluster list` / `docker ps` 快照）写进记录。
2. 不停止/删除用户常驻服务与他人资源（本地推理、向量检索、桌面/IDE 进程）。资源门禁不通过时记录阻塞，**不用停用户服务换门禁**。
3. 确需留给下游的环境，写明下游任务/负责人与保留清单，由下游清理。

## Step 5: 输出与版本化

- **落点是 vault 的任务记录**：计划写进 TASK 文件
  `<vault>/Projects/<项目>/Tasks/TASK-<id>-<slug>.md` 的 `## 实现计划` 小节。
- **追加而不覆盖**：每次重出追加 `### vN · <YYYY-MM-DD>`，历史版本保留；同步把
  frontmatter 的 `plan_version` 更新为 N、`updated` 刷新为当前时间。
- **没有 CLI 写回通道**（daemon 已停用）：frontmatter 直接编辑，但只动必要的键，
  并保持既有字段命名（`id/title/project/status/priority/stage/plan_version/plan_files/
  pr_url/merge_status/updated`）不变——记录必须和历史上的 TASK 可比。
- TASK 不存在时**先与用户确认**：新建一个（沿用既有 frontmatter 字段与 `## 实现计划`
  结构），还是把计划写到别处；不擅自造任务记录。
- 版本化的是**计划**，不是决策记录；被取代的决策要显式标 superseded，不静默改写。
- `## 执行摘要` 给一段摘要：改什么、为什么、风险最高在哪、需要用户拍板的点有几个。
- 验收标准写在同一 TASK 的 `## 验收标准`（或 REQ 的 `## 验收标准`），每个 Step 引用其 AC 编号。
- 计划里**引用的知识来源要标路径**（`Notes/adr/ADR-003`、`References/core/go/connect-rpc.md`），让实现阶段能按文献办事。

## 完成判据

- 每条 AC 都能在某个 Step 的「验收」列找到落点；没有落点的 AC 要么补 Step，要么删。
- 每个 Step 都有测试 Seam 和风险等级；`risk: high` 的都有 Prototype 建议。
- 既有决策冲突项已显式标注（或明确写出"无 ADR"）。
- 清理 Step 已就位（若计划会创建临时资源）。
- 计划已落到 vault 的 TASK `## 实现计划`（`### vN`），`plan_version` 与 `updated` 已同步。
- 需要用户拍板的点**集中列出**，不散落在正文里等人自己发现。
