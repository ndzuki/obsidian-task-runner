---
adr_id: "010"
title: "Knowledge Extraction Timing — Only at Merge→Done"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
created: 2026-07-31
---

# ADR-010: Knowledge Extraction Timing — Only at Merge→Done

## Context

`skill://knowledge-base` 的 Step 0（项目知识提取到知识库）原计划在三个时机触发：Round 2 ADR 写回后、Merge 成功（done）、CI 等待中（wait）。三轮触发导致 token 消耗过大且提取的知识常为中间态。

## Decision

**仅保留 Merge 成功（`mergeActionMerge` → `status=done`）一个触发点**。

- Round 2 完成（包括 ADR 写回）**不触发**——中间态知识质量不稳定
- CI 等待（`mergeActionWait`）**不触发**——PR 尚在检查中，可能回退
- 仅在 PR 成功合并推送后触发——此时的交付物是"已接受的知识"

daemon 调用点：`merge_runner.go` 的 `processMergeTask` → `mergeActionMerge` 分支 → `go r.extractProjectKnowledge(candidate.Project)`（异步非阻塞）。

## Consequences

- 知识库中仅有"已交付"的知识，无中间态噪音
- 大幅减少 token 消耗（每轮 Round 2 省一个 knowledge-base 调用）
- 缺点：ADR 中的架构决策在合并前不会进入知识库——若 PR 被 reject 或长时间 review，知识延迟入库。可接受——知识库是"事后沉淀"，不是实时参考。
