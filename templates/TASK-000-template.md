---
# 🔴 必填
id: ""
title: ""
project: ""
project_id: ""  # 项目内唯一数字 ID，如 "001"
# 模型选择：assignee = vault-map.json `models` 键；default/留空 = daemon 按阶段自动路由
#   ds / deepseek / deepseek_magic / ds-official — DeepSeek 系列
#   gp  — OpenAI GPT 系列（gpt/openai 为历史别名）
#   ge  — 谷歌 Gemini 系列
#   cl  — 网宿 CL（ClaudeCode 系列）
#   qw  — 阿里千问（Qwen 系列）
#   db  — 字节豆包（Seedance 系列）
assignee: ""
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
auto_merge: true  # 默认自动合并：进入 review 后先过独立完成审计（只读复核 AC 证据），通过后 daemon 自动授权 merge；设 false 恢复人工审查
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
stage: ""  # 所属交付阶段（P1/P2/...，与 Notes/Stage-Plan.md 对应）；由 REQ 继承或 PM 拆分时写入
stage_source: ""  # req=继承 REQ（跟随 REQ 变更）/ 空=auto-staging 或 PM 手动（不跟随）
plan_files: []  # 当前计划要修改的仓库内文件（daemon 按重叠串行化调度）
new_project: false

# 🟢 高级（按需取消注释）
# reviewer: ""
# author: ""
# off_peak_only: false
auto_approve: true  # 默认自动批准：grilling 后全自动（计划直接进入实现）；设 false 恢复人工审计划

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
design_replan_version: 0  # 设计库全局修订号门槛（replan gate 用）
planning_retry_count: 0
checkpoint_commit: ""
target_branch: ""
pr_url: ""
completed: ""
reopen_count: 0  # 交付轮次：breaking 需求变更重开 done 任务时 +1；0 = 首次交付
generation: 0
attempt_id: ""
executor_session_id: ""  # dsh-embed 持久会话 token（daemon 重启后 resume 用）
merge_status: ""
approved_head: ""
merge_retry_count: 0  # AI 合并修复预算（冲突/CI 失败共享；merge 成功或新一轮 planning 完成时清零）
merge_precondition_fails: 0
merge_retry_not_before: ""  # Merge 工作区人工修复冷却截止（daemon 维护；修复后可清空立即重试）
task_schema_version: 1
req_refine_count: 0  # 需求缺口循环计数：≥3 时 Agent 主动交互，全部 AC 通过后清零
round2_stall_until: ""  # Round 2 无进展冷却截止（持久化，daemon 重启不清零）
round2_stall_level: 0  # 无进展熔断计数（连续 3 轮无进展转 blocked）
audit_status: ""  # pending / passed / failed
audit_fail_count: 0
audit_log: ""
quota_backoff_level: 0
quota_backoff_until: ""
blocked_phase: ""
blocked_at: ""
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
# remote_create: false  # 在 GitHub 创建远程仓库（gh repo create）；团队项目（vault-map project_type=team）禁止开启
# github_owner: ""
# repository_name: ""
# repository_visibility: private
# repository_description: ""
# repository_url: ""

adr_proposed: []
adr_written: []
knowledge_extracted: false  # merge 后 ADR + 踩坑记录已提取到知识库（幂等）
knowledge_extract_error: ""  # 提取/同步失败摘要（daemon 维护，重试退避依据）
knowledge_extract_retry_count: 0
knowledge_extract_retry_until: ""
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
