---
name: obsidian-task-runner
description: "已归档（2026-09-14）：otg daemon 与阶段流水线已停用，本 skill 不提供任何任务编排能力。目录保留仅为承载 config/vault-map.json —— project-context 插件按固定路径读取它，用于每个会话自动注入**项目上下文**（项目文档/ADR/术语/需求清单）。"
disable-model-invocation: true
---

# 已归档 — 请勿调用

本 skill 于 2026-09-14 随项目归档停用。**不要用它编排任何任务。**

## 为什么目录还在

`config/vault-map.json` 是 `~/.dsh/plugins/project-context.mjs` 的默认配置来源
（其 `DEFAULT_MAP_FILE` 写死了这个路径）。它提供 **vault 根路径、项目根、
已注册项目清单**，供每个交互会话自动注入 `<project_context>`（项目文档/ADR/术语/需求清单）。

> **删掉本目录 = 自动注入静默失效。** 归档拆除脚本保留 `config/`，
> 只把本文件替换成这份墓碑，让 skill 退出 agent 目录。

归档后的交互式日常能力见仓库 `skills-standalone/`：
`project-baseline-audit` / `risk-aware-planning` / `design-pass` / `incremental-delivery`。
