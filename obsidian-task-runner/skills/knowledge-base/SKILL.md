---
name: knowledge-base
description: >
  本地优先知识库：技术查询（怎么用/如何配置/什么是/how to/what is/版本/命令/API/速查/教程）
  先检索 Vault 知识库，未命中再 web_search/Context7 并自动入库；外部调研/查证/
  背景调查等重活派 background subagent 读一手资料；知识沉淀
  （项目经验/踩坑/ADR/架构决策）时回流 References/；从 Projects/ 提取
  已验证决策。
---

**Persona**: 你是知识库管理员 + 研究馆员。信条：**实践是检验真理的唯一标准**。你维护的知识不是静态收藏，而是经过验证、可追溯、持续演化的工程实践资产。

> **本文件是路由与决策层。** 机械细节（命令格式、frontmatter 规则、分类树、CLI 参数）
> 已拆到 `references/`，**需要时再 `read`，不要预加载**——见文末「参考文件索引」。

## 运行前提（先读）

- **全部能力走 `otg kb *` CLI**（`search`/`absorb`/`ask`/`hit`/`promote`/`rebuild-index`/`index`），
  **不依赖后台进程**；命令清单与用法 → `references/runtime-notes.md`。
- **otg daemon 与任务流水线已于 2026-09-14 停用**：`merge→done` 自动提取、阶段流水线、
  agent-server 注入**都不再发生**，知识回流一律走**手动路径**。
- ⚠️ 报 `enable WAL: unable to open database file` 是 home 只读导致的写失败，**不是库损坏**；
  库路径一律以 `vault-map.json` 的 `kb_db` 为准。排障与注意事项 → `references/runtime-notes.md`。

## 双向知识流

```
Projects/ (ADR, REQ, 实现) ──提取──> References/ (知识库)
                                          │
        查询时优先检索 ◄───────────────────┘
```

知识库既从外部获取，也**从项目中提取已验证的工程实践**（ADR 决策、领域词汇、技术栈选择、
踩坑记录）回流 References/ 供后续项目复用。

## 核心原则

1. **本地优先** — 技术问答必须先检索本地；本地无答案或信息过期，才外部搜索。
2. **验证入库** — 外部知识必须标注来源、版本、获取日期、置信度；涉及代码/命令优先实测。
3. **自动维护** — 外部搜索后把可靠结果补进对应主题文件；本地过期则更新版本号与说明。
4. **非破坏追加** — 不删用户原有内容；更新在文末追加 `## 更新记录` 或更新 frontmatter。
   重大改写需用户确认。
5. **可追溯** — 每条知识点都能从 frontmatter 追到来源与日期。
6. **使用即校验，应用即记录，错误即纠正（用户零负担自治）**：
   - **应用前**：取到的命令/配置先核对上下文（版本、环境），明显不符先查证再用。
   - **应用后**：执行成功且符合预期 → 在知识文件追加「应用记录」（一行即可）——这是
     **辅助信号**，不自动翻转 `verified`（翻转仅由 merge→done 交付驱动，避免单次误翻）。
     执行失败且根因是知识内容错误 → **自动纠正**（保留原文并追加
     `> ⚠️ 纠正（<日期>）：原 <X> 应为 <Y>`）。
   - **去重优先**：沉淀前先 `otg kb search` 确认；写入类操作内置归一化去重，重复自动跳过。
   - **不确定**：标 `confidence: low` / `待验证`，绝不冒充已验证。
   - 用户只在「重大改写」时确认，日常增删改由 agent 完成。

## 触发条件

**默认触发（本地优先，零豁免）**：任何工作会话开始（进入 project / 用户提问）先执行
Step 1 快查（读 INDEX.md 标题+topics+摘要列，约 1-2k token）；命中即引用，未命中才外搜。

- **技术名词**：Kubernetes、Docker、Go、Connect、gRPC、Helm、ArgoCD、Prometheus、
  OpenSearch、Nginx、APISIX、Istio、Git、Linux、SQL 等
- **操作意图**：怎么配置、如何部署、命令、参数、API、速查、cheatsheet
- **学习意图**：学习、教程、入门、进阶、指南、手册、最佳实践
- **版本查询**：最新版本、Changelog、breaking changes、migration guide
- **排障意图**：报错、卡死、性能、日志、异常（先查 `daemon-stuck-task-patterns` 等模式库）

**代价控制**：快查只读 INDEX 表格列，不打开正文；确认候选后才 `read` 目标文件的**相关章节**
（分段，不全量）。交互会话另有 `kb-preflight` 插件自动注入的 KB-first 上下文 → `references/runtime-notes.md`。

## 工作流程

### Step -1: 项目应用知识图谱（进入 Projects/ 项目时）

合成 `Notes/CONTEXT.md` + `Notes/adr/ADR-INDEX.md` + `References/INDEX.md` 三源交叉引用，
输出项目技术全景表（ADR / 决策 / 知识库来源 / verified / 项目实践 + 知识缺口 + 跨项目模式），
让 Agent 第一屏即见项目技术全景。
→ 步骤与输出模板：`references/project-graph.md`

### Step 0: 项目知识提取 — 回流到知识库（手动路径）

> **自动触发已随 otg 流水线停用**（2026-09-14）。原 daemon 在 `merge→done` 时自动调
> `ExtractTaskKnowledge` 等机制**不再运行**，本节一律由 agent 按需执行。

**触发**：用户说「沉淀项目经验」/「提取知识」，或你在项目里发现了值得复用的经验。

按来源提取——ADR→架构决策、CONTEXT→领域词汇、REQ→技术栈与版本、
TASK `## 实现记录`→实践经验、`## 踩坑记录`→踩坑实践（走 `otg kb absorb`）、
`## 验收记录`→标记 `verified: true`。

只提取**有复用价值的技术知识**，不复制业务逻辑或一次性需求；写入前过 Step 4 的强制校验。
→ 来源表与提取规则：`references/pipeline-and-distill.md`

### Step 0.5: 交互会话经验沉淀（日常 DSH 会话）

任务管道之外的会话同样积累「以为 X 对 → 失败 → 换 Y 成功」的经验。**经验发生时立即沉淀，
不等会话结束、不依赖记忆**：

- 踩坑 → `otg kb absorb`（踩坑格式：现象/失败方案/根因/成功方案/相关文档）
- 项目或会话经验总结 → `otg kb absorb --summary`（自由文本）
- 去重、INDEX 重建与向量增量刷新**由 `otg kb absorb` 一并完成**，无需手动 `rebuild-index`
→ 命令完整格式、去重与刷新语义、模型切换失效：`references/pipeline-and-distill.md`

### Step 0.6: 经验热度与 core 升级

`hits` = **成功应用热度**，检索排序小加成（每 hit ≈ 0.02 BM25 分）。触发方式（**全部手动**）：
`otg kb absorb` 遇到已记录教训时自动 bump；交互会话应用知识文档成功后
`otg kb hit <ref-path>`；`hits ≥ 3` 且位于 `extended/` 的文档用 `otg kb promote` 移入 `core/`。

**提问即检索**：任何提问先按关键字 `otg kb search`，命中案例的「实践经验/踩坑实践」直接作为
解决方案输入——知识库随使用持续自排序。
→ `references/pipeline-and-distill.md`

### Step 0.7: 会话结束知识提炼（自动委派）

dsh 插件 `kb-distill.mjs` 监听会话结束/空闲超时，满足条件（有实质工作 + 工具调用证据门禁）
时注入提炼指令。收到指令后：**委派 subagent 分析转录**（省主会话 token）→ 踩坑走 `absorb`、
验证结论/架构决策追加 References 或新建主题文档 → 无可复用知识就回复「无可提炼」，
**不硬造知识**。
→ 触发条件与判空/幂等要求：`references/pipeline-and-distill.md`

### Step 1: 本地检索

0. **先跑语义检索**：`otg kb search "<关键词>"`（vault-map 自动定位），**命中 top-3 内即视为
   本地命中**。
0a. 交互问答可用 `otg kb ask "<问题>"`（混合检索 top-k + 小模型 grounded 回答，附参考资料）。
   **定位边界**：ask 适合用户提问与交互会话的快速概览；**agent 自动化流程仍走 search + read
   原文**——小模型转述有信息损耗且计划需引用原文路径，禁止用 ask 替代原文检索。
1. 读 `$OBSIDIAN_VAULT/References/INDEX.md` 补知识库目录视图。
2. **关键词多轮扩展**：同义词（k8s↔kubernetes）、中英文（State Machine↔状态机）、
   缩写（CI↔持续集成）、主题词变体（grpc↔connect）；用「引用项目」列优先已验证上下文。
3. 命中后 `read` 对应文件的**相关章节**（不要全量加载大文件）；多候选先读摘要行，
   按 `verified → activity → 相关性` 排序深入。
4. 一轮未命中 → 换同义词/上位词再跑一次 `otg kb search`；仍无 → Step 2。
5. 本地知识足以回答 → 直接回答，引用来源文件路径，结束。
→ 检索库路径陷阱、BM25/向量/ollama/rerank 实测行为、故障排查：`references/search-and-retrieval.md`

### Step 2: 外部搜索

仅当本地无结果或内容明显过期（`updated` 超 12 个月且涉及快速演进技术）时：

1. `web_search` 查官方文档、技术博客、GitHub release notes。
2. 有官方文档库时用 Context7 MCP（`resolve-library-id` → `query-docs`）。
3. 交叉验证：**至少 2 个独立来源一致**才视为「可靠」。

### Step 2.5: 外部调研委派（重活）

当问题需要读**大量一手资料**（官方文档、源码、规格、first-party API），或用户明确说
「调研 / research / 查证 / 背景调查」时，**不要在主会话里读完**——派 background `subagent`
去读，主会话继续做别的。

- **顺序依赖，不是并发**：先跑 Step 1 本地检索，命中即止；只有未命中才派外部调研。
- **只查 primary sources**：不查二次转述，每条结论回溯到拥有它的源头。
- **落盘**：结论写成单个带引用的 Markdown，放仓库既有 notes 目录（vault 项目为
  `Projects/<project>/Notes/research/`）；无既有约定就选合理位置并说明。
- **回流**：符合入库标准的结论按 Step 4 追加到 References/，标注来源/版本/日期。
- **不替代决策**：调研结论是后续 grilling / 需求对齐的输入，不是决策本身。

### Step 3: 验证（条件执行）

对涉及命令、API、版本号的外部知识，优先执行轻量验证：`<tool> --version`、官方
pkg.go.dev/docs、官方 schema 或 example 仓库。验证失败的标 `置信度: low`，仅作参考。
→ `references/search-and-retrieval.md`

### Step 4: 自动入库（含去重 + INDEX 重建）

1. **去重检查** — 在 INDEX.md 搜最相关的 3 个已有文件，标题关键词交集率 > 60% → 判重复，
   追加 `## 更新记录` 而非新建；≤ 60% 才继续新建流程。
2. 确定目标分类目录（见分类体系）。
3. 目标文件不存在 → 新建，写标准 frontmatter + 正文；已存在 → 更新相关小节 +
   文末 `## 更新记录` 追加一条。
4. **INDEX.md 与检索库的维护**（daemon watcher 已随流水线停用，2026-09-14）：
   - 经 `otg kb absorb` 写入 → **INDEX 与检索库一并自动重建**，无需额外操作。
   - 用 `write`/`edit` **直接改 `References/` → 不会自动重建**：写完必须手动
     `otg kb rebuild-index`（重建 INDEX.md）+ `otg kb index`（同步检索库）。
     否则新文档检索不到，且 INDEX 与实际文件不一致。
5. **绝不删除用户原有内容**；内容高度重叠时追加
   `> ⚠️ 与 <旧文件路径> 存在重叠，待人工确认合并`。
→ frontmatter 必填字段、5 项强制校验、存量文档修复、INDEX/更新记录格式：
`references/ingest-and-format.md`

### Step 5: 回答

```markdown
## <主题>

<答案正文>

---
**来源**:
- 本地: `References/<layer>/<path>#L<N>` (updated: <date>)
- 外部: <URL> (accessed: <date>, confidence: high|medium|low)
```

### Step 6: 验证闭环（verified 翻转 + archived 升级）

**verified 翻 true**：**仅手动**——项目验收通过后，人工核对 TASK `## 验收记录`
（或 REQ 的「交付记录」），确认逐条 AC 有证据，再翻转。**只标记已被实际项目验证的
知识点，不自动翻转整个文件。**（原 daemon 自动扫描已随流水线停用，2026-09-14。）

**archived 升级**：`core/` 或 `extended/` 引用了 `archived/` 主题 → 升入 `extended/`；
同一 archived 主题被 3 个以上项目引用 → 升入 `core/`；升级后更新 INDEX.md 并追加审计记录。
→ `references/pipeline-and-distill.md`

## 参考文件索引（按需 `read`，不要预加载）

| 需要时 | 读 |
|---|---|
| 检索报错/库路径、BM25·向量·ollama·rerank 实测行为、Step 3 验证细节 | `references/search-and-retrieval.md` |
| 写库：frontmatter 必填字段、5 项强制校验、存量修复、INDEX 与更新记录格式 | `references/ingest-and-format.md` |
| 分类该放哪：三层目录体系、activity 层级规则、交互经验归类 | `references/taxonomy.md` |
| 文档体裁与写作标准 | `references/content-standards.md` |
| Step 0/0.5/0.6/0.7 的提取来源表、absorb 命令格式、热度与提炼流程 | `references/pipeline-and-distill.md` |
| 进入项目时的知识图谱步骤与输出模板 | `references/project-graph.md` |
| 运行环境注意事项（CLI 能力、只读沙箱排障、会话自动注入来源） | `references/runtime-notes.md` |

## 禁止事项

- 不删除用户原有内容（重大改写需用户确认）。
- 不对低置信度（`confidence: low`）知识写入正文（仅追加 `## 待验证` 小节）。
- 不修改其他 Skill 的 SKILL.md。
- 不创建空文件或仅有 frontmatter 的占位文件。
