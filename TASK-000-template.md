---
id: ""
title: ""
project: ""
project_id: ""
template: ""

# ── 状态流转（系统自动管理，不要手动改） ──
status: ready
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
plan_version: 0
created: ""
updated: ""
completed: ""

# ── 优先级 & 排期 ──
priority: P2
due_date: ""
estimated_hours: 0
actual_hours: 0

# ── 人员 & 模型委派 ──
# 🔴 必填！任意支持的 assignee key（deepseek | gpt | gemini | claude | minimax...）
# 留空则 daemon 不会拾取此任务
# 模型映射: vault-map.json → models 字段
assignee: ""
reviewer: ""

# ── 范围 & 分类 ──
req_doc: ""
component: ""
tags: []
epic: ""
parent: ""
blocks: []
blocked_by: []

# ── 环境 & 部署 ──
target_branch: ""
target_env: staging
---

# <!-- 标题 -->

## 需求摘要
<!-- 从 Requirements/<req_doc>.md 复制摘要，或简要说明 -->
<!-- 需求文档模板: REQ-000-template.md -->

## 验收标准
<!-- 可验证的验收条件，task-verifier 会按此清单核实 -->
- [ ] 
- [ ] 

---

## 执行摘要
<!-- 🤖 Agent 自动维护 — 当前状态快照 -->
| 轮次 | 阶段 | 计划版本 | 状态 | 时间戳 |
|------|------|---------|------|--------|
| 1 | Round 1 | v1 | ⏳ 待开始 | — |

---

## 实现计划
<!-- 🤖 Round 1 生成。重新出计划时追加新版本，不覆盖旧版 -->
### v1 · 2026-07-10
<!-- 初始计划 -->

---

## 实现记录
<!-- 🤖 Round 2 填充。每个执行轮次追加 dated 子节 -->
### Round 1 · 2026-07-10
<!-- 初始实现 -->

---

## 验收记录
<!-- 🤖 task-verifier 填充。每轮验收追加 dated 子节 -->
### Round 1 · 2026-07-10
<!-- 初始验收 -->

---

## 变更记录
<!-- 🤖 Agent 自动追加 — 不可变审计日志 -->
1. `2026-07-10T10:00:00+08:00` — 任务创建，等待就绪
