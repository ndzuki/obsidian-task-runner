---
# 🔴 必填
id: ""
title: ""
project: ""
project_id: ""  # 项目内唯一数字 ID，如 "001"
assignee: ""  # deepseek / gpt / gemini / claude / minimax / flash
req_doc: ""
status: blocked

# 🟡 系统评分（人工可覆盖）
priority: ""
priority_assessment_status: ""  # pending / running / completed / failed
priority_assessment_attempts: 0
priority_assessment_started_at: ""
priority_assessed_at: ""
priority_assessed_value: ""
priority_impact: ""
priority_urgency: ""
priority_workaround: ""
priority_score: 0
priority_confidence: 0
priority_reason: ""
priority_recommendation: ""

# 🔵 Gate — 由你批准
plan_approved: false  # 仅 plan-review 状态有效
auto_merge: true  # 默认自动合并：进入 review 后 daemon 自动授权 merge；设 false 恢复人工审查
merge_approved: false
adr_approved: false  # daemon 自动管理，plan-review→implementing 时置 true
resume_approved: false
close_approved: false
pending_req: false

# 🟡 推荐
tags: []
epic: ""
blocked_by: []  # 同项目 TASK-010；跨项目 project-key:TASK-010
blocks: []
target_env: staging
stage: ""  # 所属交付阶段（P1/P2/...，与 Notes/Stage-Plan.md 对应）；由 REQ 继承或 PM 拆分时写入
new_project: false

# 🟢 高级（按需取消注释）
# due_date: ""
# estimated_hours: 0
# actual_hours: 0
# component: ""
# parent: ""
# reviewer: ""
# author: ""
# template: ""  # 旧脚手架提示字段，已由 scaffold 取代；保留向后兼容
# off_peak_only: false
# auto_approve: true  # 完全自主任务：首次规划自动 plan_approved，跳过人工审计划（有 ADR 提议时仍强制人工）

# 时间戳（系统维护）
created: ""
updated: ""

# ⚪ 系统维护 — 不要手动改
maturity: ""  # fully_mature / mostly_mature / immature
refine_version: 0
refine_req_hash: ""
refine_retry_count: 0
refine_error: ""
plan_req_hash: ""
plan_version: 0
planning_retry_count: 0
checkpoint_commit: ""
target_branch: ""
pr_url: ""
completed: ""
merge_status: ""
approved_head: ""
task_schema_version: 1
req_refine_count: 0  # 需求缺口循环计数：≥3 时 Agent 主动交互，全部 AC 通过后清零
blocked_phase: ""
phase_error: ""
phase_error_code: ""  # MODEL_FAILED / PHASE_TIMEOUT / PHASE_INTERRUPTED / API_KEY_UNAVAILABLE 等
phase_log: ""
auto_resume_pending: false
auto_resume_count: 0
grill_owner: ""
grill_started_at: ""
grill_heartbeat_at: ""
grill_timeout_minutes: 30
grill_done: false
grill_resolution: ""  # resume | replan | ""
grill_context: ""
grill_continue: false
grill_prev_status: ""
grill_parked: false
grill_repeat: 0
auto_accepted: ""  # refining 自动采纳建议审计记录
review_feedback: ""
rework_resolution: ""  # resume | replan | close
closure_reason: ""  # already_implemented | duplicate | cancelled | wont_fix | not-bet
closure_note: ""
replacement_task: ""  # closure_reason=duplicate

# 🟠 新项目脚手架（按需取消注释）
# scaffold:
#   kind: ""
#   capabilities: []
#   preferences: {}
#   notes: ""
# remote_create: false  # 在 GitHub 创建远程仓库（gh repo create）
# github_owner: ""
# repository_name: ""
# repository_visibility: private
# repository_description: ""
# repository_url: ""

adr_proposed: []
adr_written: []
knowledge_extracted: false  # merge 后 ADR + 踩坑记录已提取到知识库（幂等）
knowledge_refs: []  # Round 1 计划引用的知识文档（References/ 相对路径）
knowledge_applied: ""  # merge 时度量：命中/总数（如 2/3）
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

## 踩坑记录
<!-- 🤖 Round 2 每次试错换方案后追加；merge 时自动提取到知识库 References（防重蹈覆辙）。格式：
### {YYYY-MM-DD}: {现象一句话}
- 现象: {观察到的失败行为}
- 失败方案: {尝试过但不成立的方案与失败证据}
- 根因: {失败原因分析}
- 成功方案: {最终生效的方案}
- 相关文档: {knowledge_refs 里的 References 路径，可选，帮助分类归档}
-->

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
