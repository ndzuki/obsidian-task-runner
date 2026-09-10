---
adr_id: "014"
title: "PM Documentation Closure Gate via Ordinary Tasks"
status: accepted
created: 2026-09-09
---

# ADR-014: PM 文档闭环门禁与普通任务派生

## Context

项目可能在所有功能 TASK done+merged 后被标记 completed，但 README、配置参考、部署运维或故障恢复仍不完整。问题根因是项目收尾缺少确定性文档门禁，而不是缺少一个长期独立的写作角色。

如果让 PM 在 stage-review 会话里直接补写全部技术文档，会混淆完整性责任与技术事实责任；如果新增 `documenting` 主状态，又会扩大已稳定的任务状态机和全部阶段工具的适配面。

## Decision

采用 **PM 完整性负责 + 工程 assignee 内容负责 + 普通 TASK 承载**：

- PM `stage-review` 建立「用户可见能力 → README/docs 入口」覆盖矩阵；
- Stage-Review frontmatter 写版本化门禁：
  - `documentation_gate_version: 1`
  - `documentation_gate: pass | gap | not_applicable`
  - `documentation_gaps: [...]`
  - `documentation_batch: ""`（daemon 按 stage + gaps hash 回写稳定批次身份）
  - `documentation_task: ""`（daemon 回写）
- `gap` 时 daemon 确定性创建一个项目内 REQ/TASK：
  - 优先归入下一阶段；没有下一阶段时追加「项目文档交付收口」阶段；
  - assignee 优先使用 `default_assignee`，否则复用被评审阶段已有工程 assignee；
  - TASK 走既有 `blocked → refining → planning → implementing → review → merge → done` 生命周期；
- 派生 TASK `done+merged` 前，daemon 不翻转 Stage-Plan，也不派发 stage-review distribute；
- `pass` / `not_applicable` 不创建任务；旧 Stage-Review 未声明 version 时保持原行为。

不新增 documentation 专用主状态或固定模型角色。未来若跨项目技术写作工作量稳定出现，可新增可选 assignee，但 PM 仍对完整性负责。

## Existing-project retrofit

On first scan after this capability is installed, a project with an existing `Stage-Review.md` but no `documentation_gate_version` receives a one-time `Notes/Documentation-Audit.md`. A separate `documentation-audit` PM session inventories the actual project before deciding `pass`, `gap`, or `not_applicable`. The result is copied into the legacy Stage-Review and then follows the same ordinary documentation TASK path. This avoids guessing that every historical project has the same gap and preserves existing user decisions.

## Consequences

- 项目不会因功能代码合入而带着明确文档缺口错误完成；
- 技术文档仍由掌握实现事实的工程 Agent 产出，PM 只做覆盖审计与编排；
- 复用现有任务状态机、Triage、Grilling、Round 1/2、审查和合并能力；
- Stage-Review 成为 versioned 机器契约，invalid/gap-without-list 均 fail closed；
- 派生过程需要幂等与崩溃恢复：Stage-Review 记录 task id，生成 REQ 携带来源 stage marker，重复扫描不会重复创建。
