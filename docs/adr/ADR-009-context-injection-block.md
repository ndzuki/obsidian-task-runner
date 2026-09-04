---
adr_id: "009"
title: "Daemon Context Injection via <project_context> Block"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
created: 2026-07-31
---

# ADR-009: Daemon Context Injection via `<project_context>` Block

## Context

Round 2 OMP 需要项目上下文（CONTEXT.md 领域术语、ADR 决策、约束、反模式）来提高实现质量。需要 daemon 在调度 OMP 前自动注入上下文，避免 agent 手动读取文件消耗 token。

## Decision

**`<project_context>` 标签块追加到 skill 命令之后的 prompt 尾部**。

格式（daemon 注入，非 skill 指令）：
```text
<skill prompt>

<project_context>
## 项目上下文（daemon 自动注入，配合 skill://knowledge-base 交叉引用 References）
项目: <project-key>

<Constraints + Anti-patterns + Domain Terms + ADR 摘要>
</project_context>
```

注入内容控制在 ~600 字节/~300 token：
- **Constraints**（始终）：截断到 100 字符/条
- **Anti-patterns**（始终）：仅保留首句
- **Domain Terms**（动态选择）：按 REQ 关键词打分选 Top-N
- **ADR**（可选）：按 REQ 关键词匹配 Top-2，含 title + decision 首句

## Consequences

- `sync.Map` 缓存同项目多次调度避免重复 IO
- 注入位置在 skill 命令之后（不影响 skill 解析），带 `skill://knowledge-base` 交叉引用提示
- 旧格式 `[Project Context]` 头部改为 `<project_context>` 尾块（v0.16.8）
