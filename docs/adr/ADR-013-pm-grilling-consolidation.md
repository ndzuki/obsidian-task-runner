---
adr_id: "013"
title: "PM Grilling Consolidation with Parked Disputes"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
created: 2026-08-05
---

# ADR-013: PM Grilling 统筹与争议搁置

## Context

多个任务共享同一 REQ 组时，grilling 会对相同决策点重复追问，反复打断用户。真争议若逐任务处理会陷入重复循环（历史任务复盘：35+ 轮）。需要项目级统筹机制，让用户一次性回答全部争议点。

## Decision

**争议搁置（`grill_parked`）+ 项目级决策清单（`Notes/Grilling-Decisions.md`）+ PM 统筹两阶段（`processGrillingConsolidation`）**：

- consolidate：合并共享 REQ 组的重复问题；fact 类自动修正 REQ、有明确建议的低风险项自动采纳（`auto_accepted` 审计，可推翻重跑）；真争议汇总为清单
- distribute：清单回答后分发写回各 REQ，任务重置 refining 复验
- parked 任务不受 `pending_req` 抢先规则影响（state_machine 守卫），避免重开争议循环
- 新增 `obsidian-task-runner-pm` skill；frontmatter 新增 `grill_parked`/`grill_repeat`/`auto_accepted` 字段

## Consequences

- 用户 grilling 打断次数显著下降，一次回答覆盖全部争议
- 每轮 scan 最多处理 1 个 consolidate（不抢占实现槽位）
- 需维护 `Grilling-Decisions.md` 与 REQ 的双向一致性（distribute 幂等写回）
