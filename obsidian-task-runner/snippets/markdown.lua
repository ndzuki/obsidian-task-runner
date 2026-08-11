-- Obsidian Task Runner snippets
-- Usage: type trigger → <C-y> accept from blink.cmp → <Tab>/<S-Tab> jump
local ls = require('luasnip')
local s = ls.snippet
local t = ls.text_node
local i = ls.insert_node

return {
  -- =========================================================================
  -- oreq → REQ-000-template.md (需求文档 — 自由格式，L1/L2/L3 任选)
  -- =========================================================================
  s('oreq', {
    t({ '---', 'id: ""', 'title: "' }),
    i(1, '需求标题'),
    t({ '"', 'project_id: ""', 'epic: ""', '# priority: P0 仅人工设置，留空由系统自动评定', 'priority: ""', 'depends_on: []      # 依赖的 REQ 编号（daemon 自动继承到 TASK blocked_by）', 'status: defined     # index/defined/elaborated', 'stage: ""           # 所属交付阶段（创建 TASK 时继承，REQ 变更后 TASK 跟随）', '# appetite: ""              # small(30m) / medium(2h) / large(6h)', '# no_gos: []                # 明确不做的事，防 Agent 过度实现', 'created: ""', 'updated: ""', 'author: ""', 'tags: []', '---', '', '# <!-- 标题 -->' }),
    t({ '', '<!--', '用法：你想怎么写就怎么写，三种任选。', '', 'L1 极简 —— 一个标题 + 一段话', 'L2 标准 —— 功能 + 技术 + 验收标准', 'L3 完整 —— 加上 API 规格、数据模型等', '', 'OMP 会自动识别你写到了哪个层次。', '', '保存后系统自动创建 TASK。', '-->' }),
    t({ '', '---', '', '<!-- ═══════════════════════════════════════════════════ -->', '<!-- L1: 极简 —— 至少写这几行                            -->', '<!-- ═══════════════════════════════════════════════════ -->', '', '## 要做什么', '<!-- 一句话 + 一段话描述。 -->' }),
    i(2, '一句话描述要做什么'),
    t({ '', '## 完成标准', '<!-- task-verifier 会逐条核实。 -->', '- [ ] ' }),
    i(3, '验收条件 1'),
    t({ '', '- [ ] ' }),
    i(4, '验收条件 2'),
    t({ '', '', '---', '', '<!-- ═══════════════════════════════════════════════════ -->', '<!-- L2: 标准 —— 需要时展开                                 -->', '<!-- ═══════════════════════════════════════════════════ -->', '<!--', '## 背景', '', '## 功能列表', '### 功能 1: xxx', '- Given ', '- When ', '- Then ', '', '## 技术约束', '- 语言/框架: ', '- 数据库: ', '', '## 已知风险（Rabbit Holes）', '<!-- 技术未知数、未验证的假设 -->', '- ', '', '## 不在范围内（No Gos）', '<!-- 明确不做的事 -->', '- ', '', '## 验收标准', '- [ ] AC-1: ', '- [ ] AC-2: ', '-->' }),
    t({ '', '---', '', '<!-- L3: 完整 — 见模板 -->' }),
  }),

  -- =========================================================================
  -- otask → TASK-000-template.md (任务文档，字段按规范序：用户关注在前、系统维护在后)
  -- =========================================================================
  s('otask', {
    t({ '---' }),
    t({ '# 🔴 必填' }),
    t({ 'id: "' }), i(1, '001'), t({ '"' }),
    t({ 'title: "' }), i(2, '任务标题'), t({ '"' }),
    t({ 'project: "' }), i(3, 'your-project'), t({ '"' }),
    t({ 'project_id: "' }), i(31, '001'), t({ '"  # 项目内唯一数字 ID' }),
    t({ 'assignee: "' }), i(4, 'deepseek'), t({ '"  # deepseek / gpt / gemini / claude / minimax / flash' }),
    t({ 'req_doc: "' }), i(5, 'Requirements/REQ-xxx.md'), t({ '"' }),
    t({ 'status: blocked' }),
    t({ '', '# 🟡 系统评分', 'priority: ""', 'priority_assessment_status: ""', 'priority_assessment_attempts: 0', 'priority_assessment_started_at: ""', 'priority_assessed_at: ""', 'priority_assessed_value: ""', 'priority_impact: ""', 'priority_urgency: ""', 'priority_workaround: ""', 'priority_score: 0', 'priority_confidence: 0', 'priority_reason: ""', 'priority_recommendation: ""' }),
    t({ '', '# 🔵 Gate — 由你批准', 'plan_approved: false', 'auto_merge: true', 'merge_approved: false', 'adr_approved: false', 'resume_approved: false', 'close_approved: false', 'pending_req: false' }),
    t({ '', '# 🟡 推荐', 'tags: []', 'epic: ""', 'blocked_by: []   # 同项目 TASK-010；跨项目 project-key:TASK-010；引用必须真实存在（daemon 每轮校验，目标文件解析失败时 defer 不误报）', 'blocks: []', 'target_env: staging', 'stage: ""', 'stage_source: ""   # req=REQ 继承（跟随 REQ 变更）；空=daemon 分组/PM 手动（不跟随）', 'plan_files: []     # Round 1 计划将修改的文件清单（repo 相对路径），daemon 用于并发重叠预警', 'new_project: false' }),
    t({ '', '# 🟢 高级（按需取消注释）', '# due_date: ""', '# estimated_hours: 0', '# actual_hours: 0', '# component: ""', '# parent: ""', '# reviewer: ""', '# author: ""', '# template: ""', '# off_peak_only: false', '# auto_approve: true' }),
    t({ '', '# 时间戳（系统维护）', 'created: ""', 'updated: ""' }),
    t({ '', '# ⚪ 系统维护 — 不要手动改', 'maturity: ""', 'refine_version: 0', 'refine_req_hash: ""', 'refine_retry_count: 0', 'refine_error: ""', 'plan_req_hash: ""', 'plan_version: 0', 'planning_retry_count: 0', 'checkpoint_commit: ""', 'target_branch: ""', 'pr_url: ""', 'completed: ""', 'merge_status: ""', 'approved_head: ""', 'task_schema_version: 1', 'req_refine_count: 0', 'blocked_phase: ""', 'phase_error: ""', 'phase_error_code: ""', 'phase_log: ""', 'auto_resume_pending: false', 'auto_resume_count: 0', 'grill_owner: ""', 'grill_started_at: ""', 'grill_heartbeat_at: ""', 'grill_timeout_minutes: 30', 'grill_done: false', 'grill_resolution: ""', 'grill_context: ""', 'grill_continue: false', 'grill_prev_status: ""', 'grill_parked: false', 'grill_repeat: 0', 'auto_accepted: ""', 'review_feedback: ""', 'rework_resolution: ""', 'closure_reason: ""', 'closure_note: ""', 'replacement_task: ""' }),
    t({ '', '# 🟠 新项目脚手架（按需取消注释）', '# scaffold:', '#   kind: ""', '#   capabilities: []', '#   preferences: {}', '#   notes: ""', '# remote_create: false', '# github_owner: ""', '# repository_name: ""', '# repository_visibility: private', '# repository_description: ""', '# repository_url: ""' }),
    t({ '', 'adr_proposed: []', 'adr_written: []', 'knowledge_extracted: false', 'knowledge_refs: []', 'knowledge_applied: ""', '---' }),
    t({ '', '# ' }), i(6, ' <!-- 标题 -->'),
    t({ '', '## 需求摘要', '' }), i(7, '简要说明'),
    t({ '', '## 验收标准', '- [ ] ' }), i(8, '验收条件 1'),
    t({ '', '- [ ] ' }), i(9, '验收条件 2'),
    t({ '', '', '---', '', '## 需求成熟度评估', '<!-- 🤖 refining 写入 -->', '', '---', '', '## 执行摘要', '| 轮次 | 阶段 | 计划版本 | 状态 | 时间戳 |', '|------|------|---------|------|--------|', '| 1 | Refining | v0 | ⏳ 待开始 | —' }),
    t({ '', '', '---', '', '## 实现计划', '', '---', '', '## 实现记录', '', '---', '', '## 验收记录', '', '---', '', '## ADR 提议', '', '---', '', '## Grilling 上下文', '', '---', '', '## Round 2 阻塞', '', '---', '', '## 变更记录', '1. `' }), i(10, '2026-07-10T10:00:00+08:00'), t({ '` — 任务创建，status=blocked' }),
  }),

  -- =========================================================================
  -- okb → 知识沉淀（knowledge-base KB v2：标准 frontmatter 6 字段）
  -- 写入 References/<分类>/<slug>.md，入库后重建 INDEX.md
  -- =========================================================================
  s('okb', {
    t({ '---', 'topics: [' }), i(1, 'keyword1, keyword2'), t({ ']', 'level: ' }), i(2, 'beginner'), t({ '', 'updated: "' }), i(3, '2026-08-05'), t({ '"', 'source: "local"', 'verified: false', 'aliases: []', '---', '', '# ' }), i(4, '知识标题'),
    t({ '', '', '> 摘要：' }), i(5, '一句话摘要'),
    t({ '', '', '## 要点', '- ' }), i(6, '要点 1'),
    t({ '', '- ' }), i(7, '要点 2'),
    t({ '', '', '## 更新记录', '- ' }), i(8, '2026-08-05 创建'),
  }),

  -- =========================================================================
  -- oadr → ADR entry (写入 Notes/adr/NNNN-slug.md)
  -- =========================================================================
  s('oadr', {
    t({ '# ', '' }),
    i(1, 'ADR 标题'),
    t({ '', '' }),
    i(2, '1-3 句：上下文、决定、原因'),
  }),

  -- =========================================================================
  -- ogrill → Grilling context block (写入 TASK 的 Grilling 上下文 section)
  -- =========================================================================
  s('ogrill', {
    t({ '- **阻塞类型**: ', '' }),
    i(1, '测试失败 / 设计决策 / 依赖冲突 / 架构摩擦 / 需求缺口 / 代码逻辑错误'),
    t({ '', '- **当前 AC**: ', '' }),
    i(2, 'AC-N 描述'),
    t({ '', '- **问题描述**: ', '' }),
    i(3, '具体阻塞点'),
    t({ '', '- **根因分析**: ', '' }),
    i(4, '是代码问题还是需求不清晰？若是需求问题，指出 spec 中哪个 AC 或哪段描述有歧义'),
    t({ '', '- **已尝试**: ', '' }),
    i(5, '已尝试的方案和结果'),
    t({ '', '- **需要的决策**: ', '' }),
    i(6, '明确列出用户需要回答的问题'),
    t({ '', '- **建议技能**: ', '' }),
    i(7, '若为需求缺口 → skill://requirement-elaborator；若为代码逻辑错误 → skill://diagnosing-bugs'),
    t({ '', '<!-- req_refine_count 守护：≥3 时 Agent 主动交互，交互完成后自主恢复 status=implementing → count=0 -->' }),
    t({ '', '<!-- plan_version=0 守护：若从未有过计划，daemon 自动转 plan-review；直接设 plan_approved: true 即可，无需手动恢复 -->' }),
  }),

  -- =========================================================================
  -- obounce → 无计划回退恢复（plan_version=0 守护，设 plan_approved 即可）
  -- =========================================================================
  s('obounce', {
    t({ '> ⚠️ plan_version=0 守护：daemon 已自动转入 plan-review。', '> 直接设 plan_approved: true，下次轮询自动出计划 + 实现。', '', '```bash', 'otg update-status ' }),
    i(1, 'TASK-000-task.md'),
    t({ ' plan_approved=true', '```' }),
  }),

  -- =========================================================================
  -- ovalidate → otg validate-doc 诊断命令
  -- =========================================================================
  s('ovalidate', {
    t({ '```bash', 'otg validate-doc ' }),
    i(1, 'TASK-000-task.md'),
    t({ '', '```' }),
  }),

  -- =========================================================================
  -- orepair → otg repair-doc 修复命令
  -- =========================================================================
  s('orepair', {
    t({ '```bash', 'otg repair-doc ' }),
    i(1, 'TASK-000-task.md'),
    t({ '', '```' }),
  }),
}
