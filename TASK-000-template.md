---
# 🔴 必填
id: ""
title: ""
project: ""
project_id: ""  # 项目内唯一数字 ID，如 "001"
assignee: ""  # deepseek / gpt / gemini / claude / minimax / flash
req_doc: ""

# 🟡 推荐
tags: []
epic: ""
blocked_by: []
blocks: []

# 🟡 系统评分
priority: ""
priority_assessment_status: ""
priority_assessment_started_at: ""
priority_assessed_at: ""
priority_assessment_attempts: 0
priority_assessed_value: ""
priority_impact: ""
priority_urgency: ""
priority_workaround: ""
priority_score: 0
priority_confidence: 0
priority_reason: ""
priority_recommendation: ""

# 🟢 高级（按需取消注释）
# auto_approve: false
# off_peak_only: false
# new_project: false
# template: ""
# due_date: ""
# estimated_hours: 0
# component: ""
# parent: ""
# reviewer: ""
# author: ""
# target_env: staging

# 🟠 新项目脚手架（按需取消注释）
# scaffold:
#   kind: ""
#   capabilities: []
#   preferences: {}
#   notes: ""
# remote_create: false
# github_owner: ""
# repository_name: ""
# repository_visibility: private
# repository_description: ""
# repository_url: ""

# 🔵 Gate — 由你批准
plan_approved: false
auto_merge: true  # 默认自动合并：进入 review 后 daemon 自动授权 merge；设 false 恢复人工审查
merge_approved: false
adr_approved: false
resume_approved: false

# review_feedback: ""        # 审阅反馈
# rework_resolution: ""      # resume | replan | close
# close_approved: false       # 关闭 Gate
# closure_reason: ""          # already_implemented | duplicate | cancelled | wont_fix
# closure_note: ""
# replacement_task: ""        # closure_reason=duplicate

# ⚪ 系统维护 — 不要手动改
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
auto_resume_count: 0
auto_resume_pending: false
phase_error_code: ""
grill_owner: ""
grill_started_at: ""
grill_heartbeat_at: ""
grill_timeout_minutes: 30
grill_done: false
grill_resolution: ""
grill_context: ""
grill_prev_status: ""
grill_continue: ""
req_refine_count: 0
adr_proposed: ""
adr_written: []
created: ""
updated: ""
completed: ""
target_branch: ""
pr_url: ""
merge_status: ""
approved_head: ""
actual_hours: 0

# ⚪ 系统维护（新增）
task_schema_version: 1
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
<!-- 🤖 Round 1 提议；daemon 自动授权，Round 2 写入 Notes/adr/ -->

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
