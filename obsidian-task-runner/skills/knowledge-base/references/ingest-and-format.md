# Step 4 入库流程 与 知识库文件格式强制要求

> `knowledge-base` 的按需参考（2026-09-18 从 SKILL.md 拆出）。需要时再 `read`，不要预加载。

---

## 知识库文件格式 — 强制要求

所有 `References/` 下的 `.md` 文件 **必须** 以标准 YAML frontmatter 开头。
入库前和每次修改后都必须校验格式。不合规的文档视为待修复，不得跳过。

### 标准 Frontmatter（6 个必填字段 + 可选热度）

```yaml
---
topics: [keyword1, keyword2]    # 索引关键词，全小写英文，逗号分隔
level: beginner|intermediate|advanced|reference
updated: "2026-07-28"           # ISO 8601 日期（YYYY-MM-DD，勿写时间戳）
source: ""                      # 原始 URL；本地创建填 "local"
verified: true|false            # 实践验证后才可翻 true
aliases: []                     # 中文别名，方便中文搜索匹配
hits: 0                         # 可选：成功应用热度，自动维护（merge/absorb/hit 命令），勿手改
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

由 `otg kb rebuild-index` 生成：改 INDEX 一律重跑该命令（手改会在下次重建时丢失）。结构：

```markdown
# References INDEX

> 自动生成于 <YYYY-MM-DD>
> 总计 <N> 篇
> 可信度：verified <x>/<N>；活跃：high <n>；可能过期(>365d)：<n>

## 项目引用

| 项目 | 引用文档数 | 应用交付任务 |
|------|-----------|-------------|
| 001-release-manager | 24 | 57 |

## Core（平台与架构技术）

| 文件 | 标题 | 摘要 | topics | activity | hits | level | updated | verified | 引用项目 |
|------|------|------|--------|----------|------|-------|---------|----------|----------|
| core/go/connect-rpc.md | Connect-Go 参考手册 | 基于 Protobuf 的轻量 RPC 框架:一套 handler/client 同时支持… | go, golang, connect, rpc | high | 23 | reference | 2026-09-16 | true | 001-release-manager |
```

分层小节（`Core（平台与架构技术）` / `Extended（运维与工具）` …）按目录结构生成，每层一张同构表。

**摘要列**：取自正文 H1 之后的首个 `> 摘要：…` 引用行（可续多行）。缺这一行的文档，
生成的 INDEX 在该列显示 `⚠️`（摘要为空）——写完新文档先看这一列，看到 `⚠️` 就是缺摘要行。

### 更新记录格式

```markdown
## 更新记录

- `2026-07-28` — 补充 v1.17 新特性：xxx（来源: <URL>, verified: true）
- `2026-07-16` — 初始导入（来源: <URL>）
```
