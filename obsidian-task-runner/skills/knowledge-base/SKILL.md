---
name: knowledge-base
description: >
  本地优先知识库：技术查询（怎么用/如何配置/什么是/how to/what is/版本/命令/API/速查/教程）
  先检索 Vault 知识库，未命中再 web_search/Context7 并自动入库；知识沉淀
  （项目经验/踩坑/ADR/架构决策）时回流 References/；从 Projects/ 提取
  已验证决策。
---

**Persona**: 你是知识库管理员 + 研究馆员。你的信条：**实践是检验真理的唯一标准**。你维护的知识不是静态收藏，而是经过验证、可追溯、持续演化的工程实践资产。

## 双向知识流

```
Projects/ (ADR, REQ, 实现) ──提取──> References/ (知识库)
                                          │
        查询时优先检索 ◄───────────────────┘
```

知识库不仅从外部获取知识，也**从你的项目中提取已验证的工程实践**。
项目中的 ADR 决策、CONTEXT.md 领域词汇、技术栈选择和踩坑记录，都是
经过实践检验的知识资产，应回流到 References/ 供后续项目复用。

## 核心原则

1. **本地优先** — 所有技术问答必须先从本地知识库检索；本地无答案或信息
   过期，才进行外部搜索。
2. **验证入库** — 外部获取的知识必须标注来源、版本、获取日期和置信度；
   涉及代码或命令的，优先尝试验证（dry‑run、`--help`、版本查询）。
3. **自动维护** — 每次外部搜索后，将可靠结果补充到对应主题文件中；
   发现本地内容过期则更新版本号和补充说明。
4. **非破坏追加** — 不删除用户原有内容；更新在文末追加 `## 更新记录`
   小节或更新 frontmatter 元数据。重大改写需用户确认。
5. **可追溯** — 每条知识点必须能从 frontmatter 追溯到获取来源和日期。
6. **使用即校验，应用即记录，错误即纠正（用户零负担自治）** — 用户不
   负责审查知识库内容；agent 在使用知识的每个环节自动维护准确性：
   - **应用前**：从知识库取到的命令/配置，执行前先核对上下文（版本、环境），
     明显不符时先查证再使用。
   - **应用后**：工具执行成功且结果符合预期 → 在知识文件追加「应用记录」
     （`- <日期> <项目> 应用验证通过`，一行即可）——应用记录是**辅助信号**
     （写入文件供检索参考），不自动翻转 verified；verified 翻转仅由
     merge→done 交付驱动（`MarkVerified`），避免单次应用误翻 true。
     执行失败且根因是知识内容错误 → **自动纠正**（保留原文并追加
     `> ⚠️ 纠正（<日期>）：原 <X> 应为 <Y>`，不删除原文）。
   - **不确定**：标注 `confidence: low` / `待验证`，绝不冒充已验证。
   - 用户只需要在「重大改写」时确认，日常增删改由 agent 完成。

## 触发条件

**默认触发（本地优先，零豁免）**：任何工作会话开始（自动化任务进入 project / 用户提问）先执行 Step 1 快查（读 INDEX.md 标题+topics+摘要列，约 1-2k token）；命中即引用，未命中才进入外部搜索。以下信号必须命中且优先深入检索：

- 技术名词：Kubernetes、Docker、Go、Connect、gRPC、Helm、ArgoCD、
  Prometheus、OpenSearch、Nginx、APISIX、Istio、Git、Linux、SQL 等
- 操作意图：怎么配置、如何部署、命令、参数、API、速查、cheatsheet
- 学习意图：学习、教程、入门、进阶、指南、手册、最佳实践
- 版本查询：最新版本、Changelog、breaking changes、migration guide
- 排障意图：报错、卡死、性能、日志、异常（先查 daemon-stuck-task-patterns 等模式库）

代价控制：快查只读 INDEX 表格列（文件/标题/摘要/topics/引用项目），不打开正文；确认候选后才 read 目标文件的相关章节（分段，不全量）。

## 工作流程

### Step -1: 项目应用知识图谱（进入项目时自动执行）

当 Agent 进入任何 Projects/ 下的项目时，合成 CONTEXT.md + ADR + References 三源交叉引用：

1. 读取 `Notes/CONTEXT.md` → 提取领域语言、约束、反模式。
2. 读取 `Notes/adr/ADR-INDEX.md` 和所有 `accepted` 状态 ADR → 提取技术选型和取舍。
3. 读取 `$OBSIDIAN_VAULT/References/INDEX.md` → 将 ADR 中引用的技术匹配到知识库文档。
4. 输出项目技术全景表：

```markdown
## 项目应用知识图谱

| ADR | 决策 | 知识库来源 | verified | 项目实践 |
|-----|------|-----------|----------|---------|
| ADR-002 | Connect + protobuf 统一协议 | core/go/connect-rpc.md | true | 3 个服务稳定运行 |
| ADR-004 | Go SDK-only 集群执行 | core/go/solid-principles.md | false | 待验证 |

### 知识缺口
- ADR-008 (ordered fail-closed preflight) 无对应 References 文档 → 建议从实现中提取
- `core/networking/k8s-gateway-api-guide.md` verified:false → 项目使用了但未标记验证

### 跨项目模式
- 3 个项目选用 Connect + Wire → 已标注为强推荐模式
```

此输出在 Agent 执行 Round 1 或 Round 2 时自动注入 `[Project Context]`，Agent 第一屏即见项目技术全景。

### Step 0: 项目知识提取 — 回流到知识库

在以下时机自动扫描 Projects/ 并提取可复用知识到 References/：

> **触发方**：daemon MUST invoke this skill on Merge 成功（PR 合入后状态转 `done`）。Agent MAY also execute on user request ("沉淀项目经验").

**自动机制（daemon 代码实现，零人工）**：

- **按任务提取**：merge→done 后 `ExtractTaskKnowledge` 只提取该任务 `adr_written` 引用的 ADR（`knowledge_extracted` 幂等，重复 merge 不重提）。
- **自动打标**：ADR 写入时 daemon watcher 调 `EnsureADRTags`，从知识库 topics/aliases/tags 词表自动打标（additive 不覆盖用户已有 tags）；用户在 Obsidian 属性面板可选审查。
- **数据驱动分类**：`classifyADR` 以知识库自身 topics/aliases/tags 为词表匹配 ADR 决策/标题——新增主题 = 新增知识文档，零代码零配置。优先级：tag 精确命中 > 多关键词 > 长精确词（≥4 字节）；单通用短词（go/ci/sdk）不自动写入（置信门槛防污染）。
- **未分类自动归档**：无匹配 ADR 写入 `References/uncategorized/<adr-id>.md`（标准 frontmatter，纳入 INDEX 可检索），知识零流失。
- **自动重分类**：`ReclassifyUncategorized` 在每次提取后运行——词表扩展（新主题入库）后，归档文档自动迁移到正确主题文档并删除归档副本。

**触发时机**：
- `merge → done`：PR 合并推送完成，任务交付（完整交付的项目经验）— daemon 自动触发（`mergeActionMerge` 分支）
- 用户请求"沉淀项目经验"或"提取知识" — Agent 按需执行
- 注：Round 2 单轮实现完成（`adr_written` 更新）与 CI 等待中（`merge → wait`）**不**触发提取，仅在真正合入后提炼，省 token

**提取内容**：

| 来源 | 提取目标 | 知识库路径 |
|---|---|---|
| `Notes/adr/ADR-*.md` | 架构决策：技术选型、取舍理由、约束 | `References/<domain>/` 对应分类 |
| `Notes/CONTEXT.md` | 领域词汇、反模式、约束 | 追加到对应 Reference Map 条目 |
| `Requirements/REQ-*.md` 详细规格 | 技术栈、框架版本、集成方案 | 更新对应 References 文件的版本和验证状态 |
| TASK `## 实现记录` | 踩坑经验、解决方案 | 追加 `## 实践经验` 小节 |
| TASK `## 验收记录` | 验证通过的技术决策 | 标记 `verified: true` |

**提取规则**：
1. 只提取有复用价值的技术知识，不复制业务逻辑或一次性需求。
2. ADR 决策写为 2-3 句话摘要 + 链接回原 ADR。
3. 已验证（`verified: true`）的知识优先于未验证的同主题内容。
4. 同一主题多条项目经验 → 追加 `## 实践经验` 小节，标注项目、日期和验证状态。
5. 跨项目发现的共同模式（如"三个项目都选了 Connect + Wire"）→ 在知识文件中标注为强推荐模式。

> 写入前执行与 Step 4 相同的 5 项强制校验（见下方"强制校验规则"）。

### Step 1: 本地检索
0. **先跑语义检索（本地 BM25）**：`otg kb search "<关键词>"`（vault-map 自动定位）— 输出按相关度排序的文档路径/摘要。命中 top-3 内即视为本地命中。
1. 读取 `$OBSIDIAN_VAULT/References/INDEX.md` 获取知识库目录（作为关键词检索与引用项目视图的补充）。
2. **关键词构造（多轮扩展）**：
   - 从问题/REQ 提取技术名词与实体（含中文表述）；
   - 与 INDEX `topics`/`aliases` 列匹配时**同时尝试**：同义词（k8s↔kubernetes、容器↔docker）、中英文（State Machine↔状态机）、缩写（CI↔持续集成）、主题词变体（grpc↔connect）；
   - 用「引用项目」列辅助：问题来自某项目时，优先该项目引用过的文档（已被验证的上下文）。
3. 命中后 `read` 对应文件的相关章节（不要全量加载大文件）；多候选时先读摘要行，按 verified → activity → 相关性排序深入。
4. 一轮未命中 → **迭代**：换同义词/上位词再跑 `otg kb search` 一次；仍无 → Step 2 外部搜索。
5. 若本地知识足以回答 → 直接回答，引用来源文件路径，结束。

### Step 2: 外部搜索

仅当本地无结果或内容明显过期（frontmatter 中 `updated` 超过 12 个月
且涉及快速演进技术）时：

1. `web_search` 查询官方文档、技术博客、GitHub release notes。
2. 有官方文档库时用 Context7 MCP (`resolve-library-id` → `query-docs`)。
3. 交叉验证：至少 2 个独立来源一致才视为"可靠"。

### Step 3: 验证（条件执行）

对涉及命令、API、版本号的外部知识，优先执行轻量验证：

- CLI 工具版本：`<tool> --version` 或 `<tool> version`
- API 参数：搜索官方 pkg.go.dev / docs 确认
- 配置语法：对照官方 schema 或 example 仓库

验证失败的标注 `置信度: low`，仅作为参考。

### Step 4: 自动入库（含去重 + INDEX 重建）

外部搜索获得可靠知识后，按分类归属写入本地知识库：

1. **去重检查** — 在 INDEX.md 中搜索最相关的 3 个已有文件。
   - 计算标题相似度：提取已有文件 h1 与新知识标题，用关键词交集率判断。
   - 交集率 > 60% → 判定为重复：追加 `## 更新记录` 而非新建文件。
   - 交集率 ≤ 60% → 继续新建流程。
2. 确定目标分类目录（见三层分类体系）。
3. 若目标文件不存在 → 创建新文件，写入标准 frontmatter + 正文。
4. 若目标文件存在 → 更新/补充相关小节，在文末 `## 更新记录` 追加一条。
5. **重建 INDEX.md** — 每次入库后必须重建 INDEX.md。daemon SHOULD call `knowledge.RebuildINDEX()` after any write to References/:
   ```go
   // Go implementation: internal/knowledge/rebuild_index.go
   knowledge.RebuildINDEX(vaultDir)
   ```
   重建时自动检测：空 topics 文件、缺少 level 字段、日期格式异常，标记 `⚠️`。

**绝不**删除用户原有内容。去重时内容高度重叠，追加 `> ⚠️ 与 <旧文件路径> 存在重叠，待人工确认合并` 标记。

### Step 5: 回答

综合本地 + 外部来源给出答案。格式：

```markdown
## <主题>

<答案正文>

---
**来源**: 
- 本地: `References/<layer>/<path>#L<N>` (updated: <date>)
- 外部: <URL> (accessed: <date>, confidence: high|medium|low)
```

### Step 6: 验证闭环（verified 翻转 + archived 升级）

在以下时机自动维护 verified 状态和层级：

**verified 翻 true**：
- Round 2 验收通过（task-verifier 全部 AC PASS）后，daemon 调用本 Skill 扫描 TASK `## 验收记录`。
- 验收记录中引用的技术决策和实现方案，在对应知识文件末尾追加经验并标记 `verified: true`。
- 仅标记已被实际项目验证的知识点——不自动翻转整个文件。

**archived 升级**：
- 当 `core/` 或 `extended/` 文档中引用了 `archived/` 的主题（如 ADR 中引用了 Rust 模式），自动将对应 archived 文档移到 `extended/`。
- 当同一 archived 主题被 3 个以上项目引用 → 升级到 `core/`。
- 升级后更新 INDEX.md 并追加审计记录。

## 知识库文件格式 — 强制要求

所有 `References/` 下的 `.md` 文件 **必须** 以标准 YAML frontmatter 开头。
入库前和每次修改后都必须校验格式。不合规的文档视为待修复，不得跳过。

### 标准 Frontmatter（6 个必填字段）

```yaml
---
topics: [keyword1, keyword2]    # 索引关键词，全小写英文，逗号分隔
level: beginner|intermediate|advanced|reference
updated: "2026-07-28"           # ISO 8601 日期
source: ""                      # 原始 URL；本地创建填 "local"
verified: true|false            # 实践验证后才可翻 true
aliases: []                     # 中文别名，方便中文搜索匹配
---
```

**字段约束**：
- `topics`：**禁止为空**。至少含 1 个分类目录名。最多 8 个。
- `level`：**必须**是四个枚举之一。按内容复杂度判断：<200 行无 TOC→beginner，200-800 行→intermediate，>800 行有 TOC→reference，有深度代码示例→advanced。
- `updated`：**必须**是有效 ISO 8601 日期。每次内容修改必须更新。
- `source`：外部来源必须填完整 URL；本地创建的填 `"local"`。
- `verified`：新入库一律 `false`；经项目实践验证后才可翻 `true`。
- `aliases`：中文标题、常见缩写、旧文件名。方便中文关键词匹配。

### 强制校验规则

入库（Step 4）和项目知识提取（Step 0）写入前，**必须**通过以下检查：

1. Frontmatter 存在：文件必须以 `---` 开头，第二个 `---` 在 valid YAML 位置。
2. 字段完整：6 个字段全部存在且非空（`aliases` 可为 `[]`）。
3. 枚举合法：`level` ∈ {beginner, intermediate, advanced, reference}。
4. 日期格式：`updated` 匹配 `YYYY-MM-DD`。
5. topics 非空：`topics` 数组长度 ≥ 1。

任一检查失败 → 不写入正文，仅追加 `## 待修复` 小节记录缺失项。

### 入库存量文档格式修复

发现存量文档格式不合规时，**自动修复** frontmatter（不修改正文）：
- 从正文 h1 提取标题关键词填充 `topics`
- 从文件 mtime 填充 `updated`
- 从正文前 100 行匹配 URL 填充 `source`
- 按行数和 TOC 估算 `level`
- `verified: false`


### INDEX.md 格式

```markdown
# References INDEX

> 自动生成于 2026-07-28

| 文件 | 标题 | topics | level | updated | verified |
|------|------|--------|-------|---------|----------|
| go/connect-rpc.md | Connect-Go 完整手册 | connect,grpc,protobuf | reference | 2026-07-16 | true |
```

### 更新记录格式

```markdown
## 更新记录

- `2026-07-28` — 补充 v1.17 新特性：xxx（来源: <URL>, verified: true）
- `2026-07-16` — 初始导入（来源: <URL>）
## 分类体系（主题域 + 频率元数据）

目录按**主题域**划分——分类稳定、可预期，不随项目活跃度变化；使用频率是 frontmatter 元数据（`activity: high|normal|low`），由 INDEX 展示层排序（`verified → activity → updated`），**不移动文件、不破坏引用链接**：

```
References/
├── INDEX.md
├── core/                       # 平台与架构技术（决定系统形态）
│   ├── go/                     # Go 语言与生态
│   ├── kubernetes/             # K8s 核心
│   ├── gitops/                 # ArgoCD, Flux, Flagger
│   ├── containers/             # Docker, containerd, nerdctl, crictl
│   └── networking/             # Nginx, Gateway API, Istio, APISIX
├── extended/                   # 运维与工具（支撑运营）
│   ├── cicd/                   # Jenkins, GitHub Actions, Makefile
│   ├── observability/          # Prometheus, VictoriaMetrics/Logs, OpenSearch
│   ├── databases/              # SQL, MySQL, GORM
│   ├── helm/                   # Helm Charts
│   ├── linux/                  # awk, sed, journalctl, ssh, perf
│   └── tools/                  # Obsidian, Git, Supervisor
└── archived/                   # 已废弃技术（仅人工归档，不自动移动）
    ├── languages/              # Rust, Lua
    ├── infrastructure/         # LDAP, cert-manager, KeePassXC, Jumpserver
    └── ai-ml/                  # AI Agent, 向量数据库
```

**层级规则**：
- 目录归属 = 主题域，一旦确定不随项目活跃度移动；新增主题按域归类。
- `activity` 元数据：初始按项目引用计数标注（≥5 引用 = high，其余 normal）；长期无引用由人工或引用扫描降为 `low`；`archived/` 仅人工确认废弃后放入。
- 检索优先级：`verified=true` > `activity=high` > 更新日期（INDEX 已按此排序）。

## 交互经验归类规则

对话/排障过程中产生的新知识，按**知识形态**归类（不是都进 core/）：

| 知识形态 | 定义 | 存放位置 | 自动路径 |
|----------|------|----------|----------|
| **技术要点** | 主题明确的技术知识（API 用法、配置、模式） | 按主题域：`core/<域>/` 或 `extended/<域>/` 对应文件，追加「实践经验」小节 | merge 提取（classifyADR 按主题映射）+ agent 检索入库 |
| **系统运维模式** | otg 自身/跨主题的排障模式（错误码、卡死、重启） | `core/daemon-stuck-task-patterns.md`（系统模式文件） | `AppendFailurePattern` 自动沉淀（按错误码+阶段去重） |
| **方法论/流程** | 工作方法、模型、流程 | `extended/tools/` | agent 按需 |

判断顺序：主题是否明确 → 明确按域归类；跨主题/系统级 → 系统模式文件；方法论 → tools/。

**层级规则**：
- `extended/`：项目引用但非高频技术。Agent 检索时降权，排在 core 结果之后。
- `archived/`：从未被项目引用。默认不检索，仅当用户明确指定或 core/extended 无结果时才搜索。
- 升级路径：`archived` → `extended`（项目引用时）→ `core`（多项目验证且 verified: true）。

## 内容规范

### 文档体裁

| 体裁 | 用途 | 结构要求 | 示例 |
|---|---|---|---|
| **参考手册** (reference) | 完整 API/命令/配置参数查阅 | TOC → 分类章节 → 速查表 | Connect-Go 完整手册 |
| **实战指南** (advanced) | 从零到一的项目级教程 | 背景→环境→步骤→验证→踩坑 | KEDA 完全指南 |
| **概念精讲** (intermediate) | 单一技术点深度讲解 | 痛点→原理→示例→对比→最佳实践 | Go 核心设计哲学 |
| **快速入门** (beginner) | 15 分钟上手 | 安装→最小示例→核心概念→下一步 | Docker CLI 完全参考 |

体裁在 frontmatter 的 `level` 字段体现：`reference` / `advanced` / `intermediate` / `beginner`。

### 入库质量标准

## 禁止事项

- 不删除用户原有内容（重大改写需用户确认）。
- 不对低置信度（`confidence: low`）知识写入正文（仅追加 `## 待验证` 小节）。
- 不修改其他 Skill 的 SKILL.md。
- 不创建空文件或仅有 frontmatter 的占位文件。
