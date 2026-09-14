---
name: design-pass
description: "Run a design pass over a high-risk area and leave durable artifacts: contracts, decisions, a glossary, and delivery waves, with the rejected options recorded. Use for new modules, cross-service contracts, data-flow changes, storage abstractions, or when the user asks for a design doc or an approach comparison. Trigger: 全局设计, 设计文档, 契约设计, 选型, design pass."
---

**Role**: Architect。你为一个**高风险区域**做一次设计轮次，产出的是一份别人能照着实现的**契约 + 决策记录**，不是一段感想。

**接口设计的协议不在本 skill 里。** 深度模块词汇（module / interface / seam / adapter / leverage）、deletion test、以及「并行起多个子代理、各给一个激进不同的约束、再按 depth / locality / seam 对比」的 **Design It Twice 协议**，都在 `skill://codebase-design`（含 `DESIGN-IT-TWICE.md`）。**去读它、用它，不要在这里重述它。**

本 skill 管的是外围那圈：**什么时候做设计轮次、动手前要收集什么、产出落成什么档案、怎么算收口。**

## 何时用

任一成立：新模块/新组件；跨服务契约（RPC、proto message、事件类型）；数据流变化（同步→异步、直调→消息队列）；存储抽象；鉴权/信任边界变化；用户说"设计一下 / 选型 / 方案对比 / 出个设计文档"。

**何时不用**：接口只有一两个显而易见的实现方式；既有模式内的常规扩展；用户只要一个快速原型（→ `skill://prototype`）。小改动的仪式感是纯浪费——直接走 `skill://risk-aware-planning`。

**落点**：产物默认写进 vault 的项目设计目录
`<vault>/Projects/<项目>/Design/{glossary.md, contracts/, decisions/, waves/}`——
vault 是上下文第一者，设计档案必须留在那儿才被后续会话看见；用户另行指定时从用户。

## Step 0: 写探针（落盘前必做）

若产物要写到磁盘，先验证目标路径**真的可写**：

```bash
touch "$target_dir/.design-probe" && rm "$target_dir/.design-probe"
```

探针失败就**立刻停**，报 `target_unwritable: <error>`，不要把产物写到别的地方（临时目录、staging 目录都不行），也不要声称完成。

> **教训**：曾有会话把设计写到 staging 目录 + 附一句部署说明就报"完成"，验收方按约定路径去读，读到空目录，于是整条链路反复空转。**"写到了别的地方"不等于写好了。** 收尾用 `ls` 和 `grep` 确认产物真的落在约定路径，并在回复里给出绝对路径。

## Step 1: 收集约束（一次读全）

动设计之前读完：

1. **需求本身**，以及**双向**核对关联需求：上游承诺给这块什么、下游要求这块交付什么。只读一边必然写出未来会冲突的契约。
2. **既有决策**（**vault 优先**）— `<vault>/Projects/<项目>/Notes/adr/`、`Notes/CONTEXT.md`；仓库侧 `docs/adr/`、`docs/architecture.md` 作为补充。已接受的决策是**约束**，不是建议；要改就得显式 supersede。
3. **目标区域的代码证据** — 现有接口形状、调用方、数据流。设计脱离代码现状就是纸上谈兵。
4. **硬约束 vs 偏好 vs 未决歧义** 分三类列清。歧义多到无法安全设计时走 `skill://grilling` 跟用户对齐，**不要用假设填**。

## Step 2: 做接口设计

走 `skill://codebase-design` 的 Design It Twice 协议（并行多份**激进不同**的设计 → 按 depth / locality / seam 对比 → 给推荐，可以是 hybrid）。

> 强制差异化的意义：防止"三份设计其实是同一个想法的三种措辞"。若各方案的接口形状收敛，说明约束没有真正分化，重做。

**落选方案要留档** —— 它们会在未来某个约束变化时重新变得正确。

## Step 3: 落成档案

四类产物，按需取用（小设计只写其中两类是正常的）：

| 产物 | 装什么 |
|---|---|
| `glossary` | 领域术语表——这个词在这个项目里是什么意思。**减少后续会话各说各话** |
| `contracts` | 共享接口、数据结构、API、不变量。实现方照着这个写 |
| `decisions` | 架构决策与备选方案，带状态（见 Step 4） |
| `waves` | 交付波次：做什么、依赖什么、什么可以并行。**契约先于并行实现** |

约束：

- 每条结论标**来源**（代码路径+行号、ADR 编号、需求章节），无来源的不写。
- 术语表写真实的领域词，不留占位表格。
- 契约里的每个不变量都写清"违反时会怎样"——没有失败语义的契约无法被实现者验证。

## Step 4: 决策生命周期

每条决策一个状态，**不许静默改写历史**：

| 状态 | 含义 |
|---|---|
| `accepted` | 已定，实现方按它办事 |
| `proposed` | 有倾向但不安全拍板——**歧义大到无法安全设计时，就把不确定性写成一条 `proposed`**，而不是假装想清楚了 |
| `superseded` | 被新决策取代，保留原文并链接新决策 |

## 完成判据

- 每份设计有**接口形状 + 调用方用法示例 + 权衡**，不是抽象议论。
- 各设计方案的约束确实分化了（否则重做 Step 2）。
- 有明确推荐，理由落在 depth / locality / seam 三个维度上。
- 落选方案的取舍已记录。
- 关联需求**双向**核对过，残留冲突记成了 `proposed` 决策。
- 交付波次标了依赖，契约先于并行实现。
- 产物落在约定路径（已用 `ls` / `grep` 验证），回复里给了绝对路径。

**需求歧义大到无法安全设计时**：写一条 `proposed` 决策记录这个不确定性，仍然产出一个边界明确、显式受限的波次计划——不要用"等需求清楚了再说"把工作卡住，也不要用假定的答案冒充设计。
