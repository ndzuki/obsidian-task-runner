# skills-standalone — 与任务流水线无关的日常 skill

本目录装的是**从 obsidian-task-runner 阶段 skill 里提炼出来的通用内核**。

阶段 skill（`obsidian-task-runner/skills/`）的问题是：它们的价值只能在流水线里兑现——
需要 daemon 派发、TASK frontmatter、worktree、`otg update-status` 写回才能跑起来。
一旦不跑自动化，这些方法就跟着一起被封存了。而其中真正好用的部分（先做项目基线、
动手前出带风险的 AC 计划、逐条交付并把失败场景走完、高风险接口设计两次）**跟流水线
没有任何关系**——它们是通用的工程纪律。

本目录把它们剥出来，做成**不依赖任何 daemon、TASK、vault 结构**的独立 skill，
在日常交互会话里直接用。

## 四个 skill

| skill | 来源 | 管什么 |
|---|---|---|
| `project-baseline-audit` | `obsidian-task-runner-conventions` | 只读审查一个仓库，产出「规范 + 架构约束」基线（技术栈、**分环境数据库引擎**、schema/字段命名、迁移机制、注释语言、commit 习惯）。报告者，不是顾问：零建议、零改写。 |
| `risk-aware-planning` | `obsidian-task-runner-round1` | 动手前产出可审阅的计划：先读既有决策，每条 AC 落到 Step，每个 Step 有测试 seam 和风险等级，高风险 Step 配原型与 PASS/FAIL 条件。 |
| `design-pass` | `obsidian-task-runner-design` + round1 的 Design-It-Twice | 为高风险区域做一次设计轮次：双向核对约束、出方案对比、落成 **contracts / decisions / glossary / waves** 四类档案。**接口设计协议不在此**——深度模块词汇与 Design It Twice 并行协议归 `codebase-design`，本 skill 只引用不重述。 |
| `incremental-delivery` | `obsidian-task-runner-round2` | 逐条 AC 的 tracer bullet、写入即反馈、**失败场景矩阵**、排障先看决策、阻塞必附根因、时间盒削 scope、可独立复现的交付证据。 |

协同关系：**基线 → 计划 → （高风险接口）设计两次 → 逐 AC 交付**。
四个都声明 `description`（model-invoked），所以后续 skill 能互相引用，agent 也能自己触发。

## 与既有 skill 的分工

不重复造轮子——被引用的部分留给原主：

| 关注点 | 归谁 |
|---|---|
| 深度模块词汇、deletion test、Design It Twice 并行协议 | `codebase-design` |
| 红绿重构机制、测试打在 seam、好测试的定义 | `tdd` |
| 测试是否值得保留（同义反复 / 实现耦合 / 缺覆盖） | `test-quality` |
| 规范轴 + 需求轴双轴评审 | `code-review` |
| 难缠 bug 的诊断循环 | `diagnosing-bugs` |
| 一轮一问的需求对齐 | `grilling` |
| 一次性原型代码 | `prototype` |

> **为什么引用 `tdd` / `test-quality` 的地方都自带一行版兜底**：它们是
> `disable-model-invocation: true`——**主动调用型**，按设计不进 agent 的技能目录
> （见下节「调用模式」）。agent 无法自己加载它们，所以本目录的 skill 把关键判据
> 内联了一行版，缺了它们也能独立工作。

## 安装

```bash
make install-standalone          # 装到 ~/.dsh/skills/<name>/
make install-standalone DRY_RUN=1  # 只打印会写哪些文件
```

- **显式 opt-in**：不挂在 `install` / `deploy` / `sync-docs` 上，跑不跑由你决定。
- **安全边界**：只写这 4 个名字对应的 `~/.dsh/skills/<name>/SKILL.md`，不清理、不删除；
  用户自装 skill 和其它通道的 skill 一律不碰。
- **卸载**：`rm -rf ~/.dsh/skills/<name>`。
- `~/.dsh/skills` 若被 chezmoi 纳管，装完会被自动收编进个人配置源。

## 提炼时剥掉了什么

只保留方法，剥掉一切流水线管道：

- TASK 状态机与 daemon 派发：`otg update-status` / `validate-doc` 写回、`pending_req`、
  `grill_owner`、`adr_approved`、阶段门禁、冷却与重试预算、审计回打
- worktree 托管与合并流程自动化
- 写回 schema 校验（design 的 `schema: contract-v1` 那套）

**保留并强化的是 vault 约定**：`Projects/<项目>/` 下的 `Notes/`、`Requirements/`、
`Tasks/` 是上下文与任务记录的**第一者**。技能把产物写进**既有章节**，不另造格式：

| 产物 | 落点 |
|---|---|
| 规范 + 架构约束基线 | `Notes/PROJECT-CONVENTIONS.md` |
| 架构决策 | `Notes/decisions/ADR-*.md` |
| 实现计划 | `Tasks/TASK-*.md` → `## 实现计划` → `### vN · <日期>` |
| 逐条验收结论 | 同上 → `## 验收记录` → `### Round N · <日期>` |
| 试错换方案的负向经验 | 同上 → `## 踩坑记录` → `### <日期>: <现象>` |
| 设计档案 | `Projects/<项目>/Design/{glossary.md,contracts/,decisions/,waves/}` |
| 可复用技术知识 | 已退役（原 `<vault>/References/`）|

这样新记录与历史上已有的 110 个 TASK **可比、可连续检索**——记录形态的断裂比少写几个字贵得多。

保留的是**判断力**：什么算证据、哪里会翻车、什么情况下必须停下来问人。

---

## 自动化通道：三种，别混淆

"自动"在 DSH 里有三条互相独立的通道。选错通道是常见的浪费——**每次都必须在场的东西
不该交给模型判断，需要判断力的东西也不该硬编码注入**。

| 通道 | 谁决定触发 | 适合放什么 | 本机现状 |
|---|---|---|---|
| **插件 seam**（`agent/pre-step`、会话生命周期） | 确定性，与模型判断无关 | 每次都必须有的上下文 | `kb-preflight`（会话内自动注入 vault 上下文 + KB 预检）、`kb-distill`（会话结束自动沉淀）、`dsh-commands`（注册斜杠命令） |
| **skill description**（model-invoked） | agent 按场景自己判断 | 需要判断"该不该展开"的方法论 | 本目录 3 个 + `grilling` / `diagnosing-bugs` / `code-review` 等 |
| **显式调用**（user-invoked） | 人 | 低频，或你想自己控制时机 | `tdd` / `test-quality` / `handoff` / `dsh-upgrade` / `model-catalog` |

**关键点**：vault 上下文的自动注入**不经过 daemon**——`kb-preflight` 挂在 DSH 原生
`agent/pre-step` seam 上，读的就是 `Notes/CONTEXT.md`、`Notes/decisions/`、
**`Notes/PROJECT-CONVENTIONS.md`（其注释写明"最高优先"）**。所以 daemon 归档后
"进项目就自动有上下文"**照旧成立**；而 `project-baseline-audit` 的产物正好落在这个
注入点上——审计一次，之后每个会话自动受益。

## 调用模式怎么设（实测的 DSH 语义）

DSH 的 skill 加载器**只认两个调用字段**：

```js
modelInvocable: disableModelInvocation !== true
userInvocable:  userInvocable !== false
```

| 模式 | frontmatter | 效果 | 每轮上下文开销 |
|---|---|---|---|
| **自动（按场景）** | 省略（或 `disable-model-invocation: false`） | description 进 agent 目录，场景命中即自动加载 | 描述**常驻** |
| **主动（显式）** | `disable-model-invocation: true` | 不进 agent 目录；人用 `/name` 或点名调用 | **0** |
| 仅 agent | `user-invocable: false` | 人不直接调，供其它 skill 引用 | 描述常驻 |

> **实测结论**：`alwaysApply` 在整个 DSH 包中出现 **0 次**，`hide` 也不被 skill 加载器
> 读取——**两者都是空操作**。多个既有 skill 带着 `alwaysApply: false` / `hide: true`，
> 属于无效配置：无害，但别指望它起作用。

**分配原则**：

- **错过代价高 + 场景可辨认 → 自动。** 进陌生仓库该做基线、非平凡改动前该出计划、
  报错该先建复现——漏掉的代价远大于描述常驻的开销。
- **低频 / 需人拍板 / 想自己控制时机 → 主动。** `tdd`（写测试时才要）、
  `dsh-upgrade`（升级 DSH 时才要）、`handoff`（交接时才要），零常驻开销。
- **每次都必须有 → 别用 skill，用插件 seam。** skill 靠模型判断，一定会漏。

### 本目录四个 skill 的模式

全部**自动**（省略字段）——各自对应一个明确场景，漏掉的代价高：

| skill | 场景 |
|---|---|
| `project-baseline-audit` | 首次在某仓库开工 / 栈或数据库大改后 |
| `risk-aware-planning` | 非平凡改动动手前 |
| `design-pass` | 高风险接口设计、选型、要设计文档 |
| `incremental-delivery` | 按已批准计划逐条实现时 |

要改成主动调用，在 frontmatter 加一行 `disable-model-invocation: true` 即可
（`~/.dsh/skills/<name>/SKILL.md` 与 chezmoi 源**两处同改**，否则会被反向收编回滚）。
