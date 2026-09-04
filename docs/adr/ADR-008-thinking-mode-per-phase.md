---
adr_id: "008"
title: "Thinking Mode Per-Phase Control via --thinking"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
created: 2026-07-31
---

# ADR-008: Thinking Mode Per-Phase via `--thinking`

## Context

DeepSeek-V4 系列支持思考模式（chain-of-thought），不同任务阶段对推理深度需求不同：priority 只需快速 JSON 评估，round2 需要最深推理。需要按阶段自动传入 `--thinking` 参数。

## Decision

**阶段级 `--thinking` 映射，模型标识不含推理后缀**。

| 阶段 | thinking | 理由 |
|------|----------|------|
| priority | `medium` | 快速 JSON 评估（off/low 不够，见 Updates） |
| refining | `medium` | 对话式轻推理（low 不够、high 太贵） |
| planning | `max` | 深度思维链计划（2026-09-02 从 high 上调，见 Updates） |
| round2 | `max` | 最深推理代码质量优先 |

- 模型标识不含 `:xhigh` 等后缀，推理强度完全由 `--thinking` 控制
- fallback 到 Pro 模型时保持相同 thinking 档位
- `acme/acme-flash` 与 `acme/acme-pro` 均支持 `--thinking max`
- 本表外的阶段由 PhaseSpec 显式设定：merge=high、audit/pm/conventions=low、design=max；grilling 由 kitty-grill 单独分级（需求详细化 high、决策清单 low）

## Updates

- **2026-08（TASK-079 复盘后）**：priority/refining 从 `off`/`low` 上调 `medium`——低强度下 spec 命名推断类失误（D5 字段名 vs gate fixture）证明 low 不够，但 high 对每轮 refining 太贵。DSH 路径不声明 off 档位（`reasoningEffort:"off"` → UNSUPPORTED），medium 同时解决档位可用性问题。
- **2026-09-02**：planning 从 `high` 上调 `max`（DSH xhigh）——plan 是全任务最高杠杆产物，被每个 AC 迭代消费；plan-review 人审拦方向性错误、拦不住字段契约类细节（TASK-079），plan 缺陷在 round2 逐 AC 引爆；2-3× token 只付一次（planning 是稀有阶段），性价比最高。配套 `phase_timeouts_minutes.planning` 30→45。
- **2026-09-02（DSH 2.0 审计）**：design 会话从 spawn 适配器迁入 dsh-embed（`newDesignExecutor` 与 phaseExecutor 同后端选择）。spawn 路径不传 reasoningEffort，此前 design=max 从未生效、强度实际落在 `settings.yaml` `agent-default-model` 的 profile 默认（当时恰为 xhigh，纯属巧合）；迁 embed 后 design=max 真实生效，同时获得 durable resume 与 fallback 下发。

## Consequences

- 模型标识简洁，推理强度与模型选择解耦
- 后续新模型加入时无需在标识中编码推理参数
- `--thinking max` 增加 round2 token 消耗（约 2-3×），但代码质量显著提升
