---
# ══════════════════════════════
# 🔴 必填 — 你必须在创建任务时填写
# ══════════════════════════════
id: ""
title: ""
project: ""
assignee: ""
req_doc: ""

# ══════════════════════════════
# 🟡 推荐填写 — 按需设置
# ══════════════════════════════
priority: P2
tags: []
epic: ""
blocked_by: []

# ══════════════════════════════
# 🟢 高级选项 — 特殊场景使用
# ══════════════════════════════
# auto_approve: false       # 跳过 plan-review Gate（仅首次、既有项目生效）
# off_peak_only: false      # Round 2 仅在北京时间低峰执行（省钱）
# new_project: false        # 新项目标记（Round 1 只出脚手架，Round 2 才创建）
# template: ""              # 新项目脚手架提示（如 go-gin-microservice）
# due_date: ""
# estimated_hours: 0
# component: ""
# parent: ""
# reviewer: ""
# author: ""
# target_env: staging

# ══════════════════════════════
# 🔵 人工 Gate — 由你批准
# ══════════════════════════════
plan_approved: false       # 审阅计划后设 true → 进入 Round 2
merge_approved: false      # 审阅代码后设 true → 进入 Merge
adr_approved: false        # 授权写入 ADR 到 Notes/adr/
resume_approved: false     # 阶段失败修复后设 true → daemon 恢复

# ══════════════════════════════
# ⚪ 以下字段由系统自动维护，你不需要手动修改
# ══════════════════════════════
status: blocked
pending_req: false
maturity: ""
refine_version: 0
refine_req_hash: ""
plan_req_hash: ""
plan_version: 0
checkpoint_commit: ""
refine_retry_count: 0
refine_error: ""
planning_retry_count: 0
blocked_phase: ""
phase_error: ""
phase_log: ""
grill_owner: ""
grill_started_at: ""
grill_timeout_minutes: 30
grill_done: false
grill_resolution: ""
grill_context: ""
grill_prev_status: ""
req_refine_count: 0
created: ""
updated: ""
completed: ""
target_branch: ""
pr_url: ""
actual_hours: 0
# adr_proposed: []          # 系统填充
# adr_written: []           # 系统填充
---

# <!-- 标题 -->

## 需求摘要
<!-- 从 req_doc 提取需求摘要 -->

## 验收标准
<!-- Given/When/Then；覆盖成功、边界、错误、幂等/并发 -->
- [ ]

---

## 需求成熟度评估
<!-- 🤖 refining Skill 写入六项检查和 REQ hash -->

---

## 执行摘要
<!-- 🤖 Agent 自动维护 -->
| 轮次 | 阶段 | 计划版本 | 状态 | 时间戳 |
|------|------|---------|------|--------|
| 1 | Refining | v0 | ⏳ 待开始 | — |

---

## 实现计划
<!-- 🤖 planning/Round 1 追加版本，不覆盖历史 -->

---

## 实现记录
<!-- 🤖 Round 2 按 AC 追加证据 -->

---

## 验收记录
<!-- 🤖 task-verifier 按轮次追加 -->

---

## ADR 提议
<!-- 🤖 Planning 提议；adr_approved=true 后写入 Notes/adr/ -->

---

## Grilling 上下文
<!-- 🤖 needs-grilling 时记录未通过的 maturity 项或实现阻塞 -->

---

## Round 2 阻塞
<!-- 🤖 实现中需要用户决策时写入 -->

---

## 变更记录
<!-- 🤖 不可变审计日志 -->
1. `<local ISO8601>` — 任务创建，status=blocked
