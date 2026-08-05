---
name: obsidian-task-runner-split
description: "需求分解器：把新项目首 REQ（用户通常把需求揉成一团）按业务边界与依赖域拆分为多个标准子需求建议，供 PM 统筹合并进 Grilling-Decisions 一次性对齐。Trigger: 新项目首个需求、大需求拆分、需求细化建议。"
---

# Split（需求分解）

> 由 PM 统筹（`obsidian-task-runner-pm` consolidate 阶段）对**新项目首个 REQ** 或**体积明显过大（>200 行）的 REQ** 调用。目标：减少新项目初期重复 grilling——一次性给出拆分建议，让用户只回答一轮。

## 输入

- REQ 文档路径（必填）
- 项目 CONTEXT（daemon 注入的 `[Project Context]` 块，含领域词汇/约束/ADR 摘要）

## 输出

拆分建议写入 `Notes/<req-file-name>-split-proposal.md`：

```markdown
# <REQ 标题> 拆分建议

> 由 daemon/PM 统筹自动生成，需用户确认或修改后生效。

## 建议拆分为 N 个子需求

| # | 子需求标题 | 范围 | 边界/排除 | 依赖 | 建议优先级 | 拆分依据（引用原文） |
|---|-----------|------|----------|------|-----------|---------------------|
| 1 | ... | ... | ... | - | P1 | "原文引用" |
| 2 | ... | ... | ... | 1 | P2 | "原文引用" |
```

## 拆分规则

1. **按业务边界与依赖域拆分**：可独立交付的功能单元、独立数据模型、独立集成点各自成子需求。
2. **不臆造用户未表达的意图**：每个子需求必须标注拆分依据（引用 REQ 原文）；原文未覆盖的部分标记为"需确认"而非擅自补充。
3. **规模控制**：建议 3-8 个子需求；过小（可 1 天完成）合并，过大（>2 周）再分。
4. **依赖拓扑**：子需求间显式标注依赖（如"2 依赖 1"），distribute 后写入 `blocked_by`。
5. **保留原 REQ**：拆分建议不修改原 REQ 文档；原 REQ 作为总纲保留（或确认后归档）。

## 技术栈建议（REQ 未声明技术栈/框架时）

**触发**：REQ 全文未提及技术栈、框架、语言、部署形态（或仅模糊提及）。

**候选推导（双源）**：

1. **过往项目推导**：读取 vault-map.json `projects` + 各项目 `Notes/adr/` 与知识库（`skill://knowledge-base` Step 1 检索）——列出已被验证的技术组合（如 `Go + Connect + PostgreSQL + Helm`），标注来自哪个项目与 `verified` 状态。
2. **社区方案**：`web_search` 查询该需求领域的当前主流成熟方案（官方文档/权威榜单为准），标注来源 URL 与成熟度（stable/mainstream/emerging）。

**输出**（并入 Grilling-Decisions 清单 `## 技术栈确认` 段）：

```markdown
## 技术栈确认

| 方案 | 来源（项目/社区） | 成熟度 | 适配度 | 备注 |
|------|------------------|--------|--------|------|
| Go + Connect RPC + PostgreSQL | release-manager（verified） | stable | 高 | 与既有 ADR 生态一致 |
| ... | web_search: <URL> | mainstream | 中 | 需新引入 |
| 技术栈: <用户填写> |
```

**规则**：
- 不替用户决定——每个候选标注来源与适配度，留「技术栈:」空位。
- 过往项目候选优先（已验证 + 与既有 ADR 生态一致）；社区方案必须可追溯（URL）。
- 用户决定后：distribute 写回 REQ 技术栈章节 + 决策沉淀（`skill://knowledge-base`）。

## 与 PM 统筹的衔接

- **consolidate**：拆分建议作为 `Notes/Grilling-Decisions.md` 清单的**第一部分**（`## 拆分确认`），与争议问题一起交用户一次性回答。
- **distribute**：用户确认/修改拆分后，按确认结果创建子 REQ 文档（`REQ-<id>-<slug>-<n>.md` 或独立编号，遵循项目 REQ 命名规范）；`OnReqChanged` 自动为每个子 REQ 生成 canonical TASK。子需求细化由各自的 refining/requirement-elaborator 流程处理，不再重复大需求级别的 grilling。

## 禁止事项

- 不直接修改原 REQ 正文（拆分确认前）。
- 不在拆分建议中编造验收标准（子 REQ 的 AC 由后续 requirement-elaborator 按用户确认的范围细化）。
- 不创建子 REQ 文件（创建是 distribute 阶段 PM 的职责）。
