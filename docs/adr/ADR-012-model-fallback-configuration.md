---
adr_id: "012"
title: "Configurable Model Fallback via fallback_models Map"
status: accepted
requirement: REQ-003-obsidian-task-runner
task: TASK-003-obsidian-task-runner
created: 2026-08-05
---

# ADR-012: 模型兜底配置化（`fallback_models`）

## Context

模型兜底原先在代码中硬编码：`FallbackModel` 写死 `deepseek-v4-pro`，assignee 集合（gpt/default/deepseek）也是 switch 写死。omp 会话级 fallback（`config.yml` `retry.fallbackChains`）只覆盖 API 可重试错误（socket/timeout/429/5xx）；进程级失败（进程退出、阶段超时、token quota）需要 daemon 用不同模型重启整个 OMP 进程。项目开源后，使用者应能不改代码配置任意兜底模型与兜底对象集合。

## Decision

**vault-map.json 顶层 `fallback_models` 映射（assignee → 模型标识）作为 daemon 层兜底的唯一来源**：

- 默认 `gpt`/`default`/`deepseek` → `deepseek/deepseek-v4-flash`
- 可增删任意 key（如给 `gemini` 配兜底）、改任意模型（如切回 `deepseek/deepseek-v4-pro` 做深度推理）、置 `""` 禁用单个 assignee
- 部分配置与默认合并（`Load` 从 `Defaults()` 起步，`encoding/json` 对 map 是合并语义）
- `FallbackModelFor(assignee)` 纯 map 查找，代码零模型字符串；`ModelReference()` 表格由默认值动态生成，单一数据源无失同步路径
- 两层兜底分工：会话内 API 错误 → omp `fallbackChains`（按 role，同进程换模型重试）；进程级失败 → daemon 用 `fallback_models` 重启 OMP（全新 timeout 预算）
- `models.deepseek` 主模型同步调整为 v4-flash；需要深度推理时通过配置切回 pro

## Consequences

- 兜底模型与兜底对象集合均可配置，开源使用者零代码改动
- 去掉 daemon 层兜底不可行：进程级失败（exit 143/1、超时、quota）是会话内 fallback 无法覆盖的类别——真实运行日志 5 天内 daemon 层兜底触发 50+ 次、成功 15+ 次
- 新模型接入只需改 vault-map.json，无需改代码
