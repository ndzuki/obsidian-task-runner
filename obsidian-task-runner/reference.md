# Obsidian Task Runner — 目标设计参考

> 完整设计规范与实现验收清单见仓库 `docs/workflow.md`（不随技能包安装）；本文定义状态、frontmatter schema、依赖引用和人工操作。
>
> 当前 Go 实现未完全满足本文；以仓库 docs/workflow.md 的实现验收清单为准。

## 1. 状态流转

```text
blocked → ready → refining ─┬─ fully_mature → planning → plan-review → implementing → review → done
                            ├─ needs input → needs-grilling → refining
                            ├─ 大型需求 → Wayfinder Map 决策地图（Grilling 焦点）
                            └─ 重复争议（grill_repeat≥2 或单任务 plan_version≥3 反复 replan）→ park → 项目级 Grilling-Decisions.md → PM 分发 → refining

决策清单（Notes/Grilling-Decisions.md）状态机：open ⇄ paused（用户主动或 REQ 更新自动激活）→ answered
- paused：项目级暂停开关——该项目的 grilling 流程任务整体暂停：不提醒、不开决策 tab、grill_continue 不重置 refining、PM 不分发/不 consolidate、parked 任务不解除；填答案也不流转
- 恢复：用户手动把清单 status 改为 open，或**关联 REQ 更新时 daemon 自动激活回 open**（用户/团队主动补充需求 = 恢复信号）——随后 consolidate 重新整理新需求与既有争议点、Grilling 对齐，任务重新进入自动化流程

needs-refining（旧版遗留）→ 自动迁移 needs-grilling → refining

refining/planning -- retry once, fail again --> blocked
implementing -- pending_req at AC boundary --> refining
implementing -- prototype FAIL → needs-grilling（带原型证据）
implementing -- 无进展完成（仍 implementing + 无 checkpoint_commit）→ 冷却自环（指数退避 10m→…→~10.7h，不重派不通知；有进展即重置）
review -- merge conflict --> conflict -- AI 预算内自动修复+自动重授权，耗尽/超规模熔断后人工 --> done
review/conflict -- 交还后 PR 被人工合并（MERGED）--> 自动收口 done（autoCloseMergedConflictPRs）
plan-review -- close_approved --> closed
review -- close_approved --> closed
closed -- [终态，不可恢复]
```

## 2. 状态定义

| 状态 | 含义 | 执行者 | 下一步 |
| ------ | ------ | -------- | -------- |
| `blocked` | 缺字段/依赖，或 refining/planning 连续失败，或 API key 不可用，或人工暂停 | daemon / 人工 | 见 §4.4（自动 unblock / resume / key 探测 / 暂停） |
| `ready` | 可开始 priority assessment + maturity gate；**`blocked_by` 上游未 done 时不调度**（依赖门禁前置，防无效重规划） | daemon | `refining` |
| `refining` | Headless 检查需求规格成熟度；**同样受依赖门禁约束** | `models.default` | `planning` / `needs-grilling` / `blocked` |
| `needs-grilling` | 需要用户交互补充规格；`grill_parked=true` 时问题已并入项目级决策清单，等 PM 分发；清单 `status=paused` 时该项目 grilling 流程整体暂停（不提醒/不分发/不 consolidate/不解除），手动改 `open` 或 REQ 更新自动激活后恢复 | Kitty + requirement-elaborator / PM 统筹 | `refining` |
| `planning` | 规格成熟，正在生成版本化计划 | TASK assignee + Round 1 Skill | `plan-review` / `blocked` / `refining` |
| `plan-review` | 具体计划已存在，等待人工批准 | 人工 | `implementing` / `closed` |
| `implementing` | 执行已批准计划；Round 2 无进展完成（仍 implementing + 无 checkpoint_commit）进入指数退避冷却（10m→…→~10.7h），冷却期不重派；checkpoint_commit 写入或状态离开 implementing 即重置 | TASK assignee + Round 2 Skill | `review` / `refining` / `needs-grilling` |
| `review` | 本地实现已提交；auto_merge=true 时先过**独立完成审计**（只读会话逐条 AC 复核原始证据，见 §4.6.10），通过后自动授权合并；人工 `merge_approved=true` 跳过审计；审计 fail 按类型转 implementing 或 needs-grilling | daemon 自动 / 人工 | `done` / `conflict` / `refining` / `implementing` / `needs-grilling` / `closed` |
| `closed` | 已关闭终止；不再流转 | 人工 | —（终态） |
| `conflict` | Merge 冲突；auto_merge 任务在 REQ 未变 + 预算未耗尽时 daemon 自动重授权重试，预算耗尽（conflict-resolve-attempted）/ 冲突规模超 `max_auto_fix_conflicts` 熔断 / 永久缺陷交还人工；**交还后 PR 被人工合并（MERGED）→ `autoCloseMergedConflictPRs` 每任务 5 分钟冷却探测自动转 done**（TASK-067：手动合完 PR 任务仍卡 conflict 阻塞下游） | daemon（AI 预算内）+ 人工 | `done` / `refining` |
| `done` | 已合并推送；breaking 变更重开（代际重置）或 additive/cosmetic 保持终态 | — | `refining`（breaking）或结束 |

## 3. 人工 Gate

| 字段 | 人工操作 | 约束 |
| ------ | ---------- | ------ |
| `plan_approved` | 审阅计划后设 true | 仅 `plan-review` 有效；`plan-review → implementing` 后**保留 true** 供 Round 2 阶段会话读取，implementing 状态不重置 |
| `merge_approved` | Merge 授权 | `pending_req=true` 时绝对无效；进入 review 时按 `auto_merge` 在**完成审计通过后**自动置 true（人工设 true 跳过审计——人工门禁优先；审计 fail 清授权）；**merge 失败回退自动重授权**（`canAutoApproveMerge`：REQ 未变 + 预算未耗尽 + 非 `GITHUB_UNAVAILABLE`/`REPO_MISMATCH` 永久缺陷，conflict 同样适用，TASK-051/059 教训）；硬失败回退（预算耗尽/REQ hash 变更/gh 缺失或**未登录**/仓库目标不匹配）置 false 需人工重设；环境性失败（网络/瞬时 GitHub 错误）自动重试期间保持 true；停机/超时中断保持 true 重启自动恢复。可自动重授权的未授权任务与已授权 merge 同走 lock-free 调度路径（`prepareBatch` 锁判定与 gate 对齐，防调度入口饿死）。**gh 未登录**：merge 前 daemon 本地预检 `gh auth status`，未登录 → 不发起任何远程操作，`phase_error` 附 `gh auth login` 指引 + 桌面通知提醒，登录后重设 `merge_approved=true` 继续 |
| `auto_merge` | 默认 true；进入 review 时自动授权合并 | 设 false 恢复人工 merge gate；仅 review 状态有效 |
| `auto_approve` | 默认 true；计划自动批准 | 缺失即 true（解析兼容 + 模板写入）；plan-review 时 daemon 自动 `plan_approved=true` 转 implementing，Grilling 是唯一人工关卡；设 false 恢复人工审计划 |
| `close_approved` | 显式关闭授权 | 仅 `plan-review`/`review` 有效；还必须同时提供合法 `closure_reason` 与非空 `closure_note`（`duplicate` 还需 `replacement_task`），否则 daemon 不转 closed |
| `rework_resolution` | 关闭前人工判定重做方向 | `replan` 转 refining（仅 `plan-review`/`review` 有效；`conflict` 需走 REQ 变更触发）；`resume` 恢复原阶段；`close` 仅在完整关闭证据下生效；空值保持等待 |
| `review_feedback` | review 阶段人工反馈摘要 | free text，`review` 状态下有效 |
| `adr_approved` | 系统自动管理 | daemon 在 plan-review→implementing 时自动设为 true；Round 2 写 ADR 后清 false |

## 4. Frontmatter Schema

### 4.1 身份与人工填写

| 字段 | 类型 | 说明 |
| ------ | ------ | ------ |
| `id` | string | 项目内唯一；不同项目可重复 |
| `title` | string | 任务标题 |
| `project` | string | vault-map project key |
| `assignee` | string | vault-map.json 顶层 `models` 的 key（如 `default`） |
| `req_doc` | string | Vault 相对规范路径，必须完整精确匹配 |
| `new_project` | bool | 新项目标记 |
| `template` | string | 新项目脚手架提示（已弃用，见 `scaffold`） |
| `scaffold` | object | 新项目脚手架意图（`kind`/`capabilities`/`preferences`/`notes`），供 Round 1 / project-scaffold 消费 |
| `blocked_by` | list | 同项目 `TASK-010`；跨项目 `project-key:TASK-010` |
| `auto_approve` | bool | 默认 true（缺失即 true）；plan-review 由 daemon 自动批准转 implementing，Grilling 是唯一人工关卡；设 false 恢复人工审计划（完整语义见上表 Gate 字段） |
| `off_peak_only` | bool | Round 2 只在北京时间低峰执行 |
| `stage` | string | 阶段归属 `P{N}`（如 `P1`）；创建 TASK 时从 REQ 继承，PM 拆分落地时写入；daemon 阶段完成检测与 auto-staging 以此为**权威判定**（见 §4.8） |

顶层配置 `default_assignee`（vault-map.json）：新 REQ 自动创建 TASK 时预写 `assignee`（`models` 的 key，如 `default` → `gateway/gpt-5.4-mini`），任务直接可调度。**空值/缺省恢复旧行为**：`assignee` 留空、任务停在 `blocked` 等人工补填（`IsReady` 要求 `assignee` 非空）。

### 4.2 Maturity Gate

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `maturity` | enum/string | `""` | `fully_mature` / `mostly_mature` / `immature` |
| `refine_version` | int | `0` | maturity gate 审计版本 |
| `refine_req_hash` | string | `""` | refining 派发前由 daemon 预写、**成功后按当前 REQ bytes 兜底重写**的完整 REQ SHA-256（会话写漏时 early-out 仍成立，防 maturity gate 空转——TASK-058） |
| `refine_retry_count` | int | `0` | refining 自动恢复次数 |
| `refine_error` | string | `""` | 最近 refining 错误 |

Refining 必须同时维护 TASK 的 `## 需求成熟度评估` section，保存六项检查证据。

### 4.3 Planning

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `plan_req_hash` | string | `""` | planning 使用的 REQ hash |
| `plan_version` | int | `0` | 每次 planning 成功 +1 |
| `planning_retry_count` | int | `0` | planning 自动恢复次数 |
| `plan_approved` | bool | `false` | Round 2 Gate |
| `checkpoint_commit` | string | `""` | pending_req 前的 WIP checkpoint |
| `round2_stall_until` | string | `""` | Round 2 无进展冷却截止（RFC3339，daemon 维护、重启不清零）；冷却期内不重派、不通知；`checkpoint_commit` 写入或状态离开 implementing 即清 |

Planning 写 plan-review 前必须复核当前 REQ hash。Hash 变化时不得写入/批准计划，返回 refining。

### 4.4 阶段恢复

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `blocked_phase` | string | `""` | `refining`、`planning` 或 `implementing` |
| `phase_error` | string | `""` | 阶段失败原因 |
| `phase_error_code` | string | `""` | 阶段失败机器可读错误码 |
| `phase_log` | string | `""` | 对应日志路径 |
| `resume_approved` | bool | `false` | 恢复授权（人工或依赖链自动） |
| `auto_resume_pending` | bool | `false` | 本次失败由自动 resume 发起（仅它计入预算） |
| `auto_resume_count` | int | `0` | 自动 resume 连续失败累计，≥2 停止自动恢复 |

Refining/planning/implementing 第一次失败自动恢复；再次失败转 blocked。阶段成功或人工 resume 后 retry count 清零。

`blocked_by` 上游处于阶段失败阻塞（`blocked_phase` 非空且错误码为 MODEL_FAILED/PHASE_TIMEOUT/PHASE_INTERRUPTED/MODEL_QUOTA_EXHAUSTED，或空错误码且上游自身无 `blocked_by` 的 legacy 阶段失败）时，daemon 自动设 `resume_approved=true` + `auto_resume_pending=true` 以解开依赖链；`auto_resume_count` 仅在这种自动恢复后再次失败时递增。人工 resume（无 pending 标记）清零计数。`REQ_MISSING` 等非瞬时错误与循环依赖永不自动恢复。

**`recoverBlockedPendingReq`（blocked + pending_req 覆盖）**：阶段失败 blocked 的任务若 REQ 已变更（`pending_req=true`），daemon 每轮 scan 自动转 `refining` 重细化——不复用旧 phase（手动 resume 会拿旧需求重新实现）。转 refining 复用 `transitionToRefining` 基底原子清 grill/plan/merge/`round2_stall_until` 残留。排除：`PREREQUISITE_SMOKE_FAILED` 门禁、`REQ_MISSING` 等非瞬时码、空错误码 + `blocked_by` 非空的入口门禁形态（三者各有专属恢复路径）。

**`PHASE_INTERRUPTED`（daemon 重启/停机中断）**：daemon 优雅停机时，运行中的 DSH 阶段会话收 SIGTERM 保存会话后退出（agent-server 常驻时由 `executor_session_id` 持久恢复），任务**不转 blocked**——保持原状态并写 `phase_error_code=PHASE_INTERRUPTED`（`phase_error="daemon 重启中断，等待自动恢复"`）；重启后下一轮 scan 经 `runDSHPhase` durable resume 重挂会话：**resume 成功复用结果；resume 等待超时/再中断时如实上报 PHASE_TIMEOUT / PHASE_INTERRUPTED，不得 fresh start**（daemon 侧 HTTP 超时不终止 agent-server 会话，fresh start 会造成同任务双会话并行写，TASK-058 教训）；仅会话终态失败（session not found 等）回退 fresh start；resume 超时对齐阶段 spec。阶段成功后由 `clearPhaseError` 清除标记。该错误码同时被依赖链自动恢复识别（见上）。

**人工暂停**：需要暂停任务等待外部条件（如用户完善需求）时，可设 `status=blocked` + `blocked_phase=<原状态>` + `phase_error_code=REQ_MISSING`（非自动恢复错误码），daemon 保持阻塞且不提醒；条件满足后设 `resume_approved=true` 恢复。

**缺字段/依赖 auto-unblock（2026-08-25 修正）**：补齐字段或依赖满足后自动转 `ready`（有旧计划则 `plan-review`）——**但不变量 #5 优先：`pending_req=true` 时不得清 false，且直接转 `refining`**（新 REQ 必须先被新计划吸收，禁止复用旧计划进 plan-review/实现）。`OnReqDeleted`（REQ 被删 → blocked `REQ_MISSING`）同样**保持 `pending_req` 原值**——人工恢复后必须重细化。

### 4.5 Grilling 所有权

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `grill_owner` | string | `""` | 当前交互会话 owner |
| `grill_started_at` | ISO8601 | `""` | owner 获取时间 |
| `grill_timeout_minutes` | int | `30` | 可配置 lease 超时 |
| `grill_heartbeat_at` | ISO8601 | `""` | 最近心跳时间，用于 lease 存活检测 |
| `grill_done` | bool | `false` | 规格写回完成标记 |
| `grill_context` | string/YAML | `""` | 需要对齐的问题上下文 |
| `grill_prev_status` | string | `""` | 实现阻塞前状态 |
| `grill_continue` | bool | `false` | 用户离线填答完成标记；daemon 检测到 true 时重置 refining 复验并清字段（异步 Grilling） |
| `grill_parked` | bool | `false` | 争议已并入项目级 `Notes/Grilling-Decisions.md`；parked 任务不创建 Kitty、不提醒，等 PM 分发答案。清单 `status=paused` 时该项目的 grilling 流程整体暂停（不提醒/不开 tab/不分发/不解除），用户手动改回 `open` 或关联 REQ 更新（daemon 自动激活）后恢复 |
| `grill_repeat` | int | `0` | 同一争议集连续未被回答的 refine 轮次；≥2 且 REQ hash 未变 → park 升级，不再逐任务重复追问 |
| `auto_accepted` | string | `""` | refining 自动采纳建议/事实修正的审计记录（`;` 分隔追加），用户可推翻后重跑 |
| `knowledge_extracted` | bool | `false` | 该任务 ADR + `## 踩坑记录` 已提取到知识库（`ExtractTaskKnowledge` 幂等标记）。**仅在提炼全成功时写入**（含检索库 store 同步）；失败保留 `false` → daemon 对 `done`+`merged`+未提炼任务自动重试（`recoverUnExtractedKnowledge`），**按 `knowledge_extract_retry_until` 指数退避，不再每轮 scan 无限重试**，不静默丢失。`adr_written` 的逗号串+路径前缀形态在匹配前归一化为裸 ADR id |
| `knowledge_extract_error` | string | `""` | 最近一次知识提炼失败/部分失败的原因（用户可见，含检索库同步失败）；成功后清空。失败同时触发桌面通知「知识提炼失败/部分失败（自动退避重试中）」 |
| `knowledge_extract_retry_count` | int | `0` | 知识提炼/检索库同步**连续**失败计数（daemon 维护）；首次失败 10m 后重试，逐次加倍，上限 6h。完整成功（提取 + store 同步）时清零——提取成功但同步失败不清零，连续同步失败同样加倍 |
| `knowledge_extract_retry_until` | string | `""` | 下一次允许重试知识提炼的 RFC3339 截止（daemon 维护，重启不清零）；`recoverUnExtractedKnowledge` 仅在该时点之后重试，防发版时 store 同步失败引发每轮扫描无限重试风暴 |
TASK body `## 踩坑记录`：Round 2 实现中试错换方案的负向经验（现象/失败方案/根因/成功方案/相关文档），merge 时自动提取到 References 对应文档「踩坑实践」小节，未命中归档 `References/uncategorized/`。
| `knowledge_refs` | list | `[]` | Round 1 计划实际引用的知识文档清单（相对 References/ 路径）；Round 2 按清单应用、merge 度量、verifier 校验 |
| `knowledge_applied` | string | `""` | merge 时 daemon 度量的知识引用命中统计（`hit/total`，如 `2/3`） |

| `grill_resolution` | enum/string | `""` | `resume` 直接恢复实现；`replan` 转 refining；空值保持等待 |
Daemon 和 requirement-elaborator 都必须检查 owner。读检查写过程使用 `${TMPDIR}/otg-grill-<task-path-sha256>.lock` flock 强化本机原子性。

需求细化完成使用 `grill_resolution=replan`，daemon 转 refining 复验。实现阻塞按 resolution 分流。`pending_req=true` 优先于 `resume`，必须重规划。

Daemon 成功消费后原子清 `grill_done`、`grill_resolution`、`grill_context`、`grill_prev_status`，防止重复路由。

#### 4.6.1 需求变更与 Merge

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `pending_req` | bool | `false` | 新 REQ 尚未被新计划完整吸收 |
| `merge_approved` | bool | `false` | Merge Gate；pending_req=true 时绝对无效；review 进入时按 auto_merge 在**完成审计通过后**自动置 true（人工设 true 跳过审计——人工门禁优先；审计 fail 清授权） |
| `auto_merge` | bool | `true` | 进入 review 自动授权合并（先过完成审计）；false 恢复人工 gate |
| `audit_status` | string | `""` | 完成审计状态：`""`（未审计 / 失败路由后清空待重审）/ `pending`（审计中或会话失败待重试）/ `passed`（已通过）；auto_merge 任务仅 `passed` 或人工 `merge_approved=true` 可进入合并授权；merge 失败回退保持 `passed` 不重复审计 |
| `audit_fail_count` | int | `0` | 连续审计失败次数（implementation 类）；达 `audit.max_fixes`（默认 2）升级 grilling 决策时清零（resume 重置预算，防止已决策方向被旧计数立即再限流） |
| `audit_log` | string | `""` | 最近一次审计会话原始输出日志路径（`~/.dsh/logs/tasks/TASK-<id>-audit-<ts>.log`），round2 修复时消费 |
| `target_branch` | string | `""` | Round 2 分支；done 重开时清空，round2 完成后 daemon 写新分支 |
| （自愈） | — | — | round2 被 daemon 重启打断未写回 `target_branch` 时，merge 授权前 daemon 从任务 worktree 恢复 `task/{id}-*` 分支名并写回（幂等，仅 `task/` 前缀；TASK-079） |
| `pr_url` | string | `""` | PR URL；done 重开时清空，新交付创建新 PR |
| `reopen_count` | int | `0` | 交付轮次：done 任务因 breaking 需求变更重开时 +1；0 = 首次交付 |
| `merge_retry_count` | int | `0` | AI 合并修复预算（冲突/CI 失败共享，上限见 vault-map `max_auto_merge_fixes`）；仅在 merge 成功或**新一轮 planning 完成**时清零——replan 不继承旧交付耗尽（TASK-067 教训）；同一计划内重复授权不重置（防无限循环）。**冲突规模熔断（`max_auto_fix_conflicts`，默认 40）**：sync 冲突文件数超阈值不启动 AI 直接交还（不耗预算）；**`upstream_stall_days`（默认 3）**：blocked_by 上游非终态且 `updated` 超阈值 → 每日一次提醒 |

`pending_req` 仅在新 planning 成功后清 false。**done 重开代际重置**：breaking 变更（含未标注）打回 done 任务时清 `target_branch`/`pr_url`/`merge_status`/`completed` 并置 `knowledge_extracted=false`——旧 PR 已 MERGED 时 merge 流程会提前收敛为 done，不清则新交付永远合不进去（TASK-018 实测）。

**Merge 认证契约**：Merge Phase 所有远程操作统一走 **gh CLI 认证通道**——`git push` 由 daemon 注入 `-c credential.helper='!gh auth git-credential'`（`mergePushCommand`），PR 创建/复用与合并用 `gh pr create` / `gh pr merge`；禁止裸 `git push`（无 ambient https 凭据的机器会以 `could not read Username` 烧光重试预算，TASK-004 教训）。gh 缺失或未登录（`checkGHAuth` 预检）→ 拒绝远程操作，写 `status=review` + `merge_approved=false` + `phase_error_code=GITHUB_UNAVAILABLE` + `phase_error` 附 `gh auth login` 指引 + 通知。

#### 4.6.2 ADR（架构决策记录）

ADR 是项目的架构宪法。三个原则：

1. **决策前读 ADR** — Round 1 规划的第一步是读全部已有 ADR，新计划不能与已确立决策冲突。
2. **实现前写 ADR** — Round 2 在写代码之前把架构决策文档化，ADR 和代码在同一 commit 中。
3. **ADR 是活的** — 决策可以被 supersede，但必须有新 ADR 说明理由。

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `adr_proposed` | list | `[]` | Round 1 提议的 ADR 标题列表 |
| `adr_approved` | bool | `false` | daemon 在 plan-review→implementing 时自动设为 true |
| `adr_written` | list | `[]` | 已写入 `Notes/adr/` 的 ADR 文件名列表 |

ADR 生命周期：Round 1 读取已有 ADR → 检测新架构决策 → 写入 `adr_proposed` → daemon 自动授权 `adr_approved=true` → Round 2 在实现前写入 ADR 文件 → 全部 AC 完成后更新 `adr_written` 并清 `adr_proposed`/`adr_approved`。

#### 4.6.3 文档校验（CLI 命令）

| 命令 | 覆盖 |
| ------ | ------ |
| `otg validate-doc` | 自动识别 TASK/REQ/ADR，校验 frontmatter 必填字段 + body `<tag>` 扫描 |
| `otg repair-doc` | 修复 frontmatter + 自动转义 body `<tag>` → `\<tag\>` |
| `otg write-adr` | 原子写 ADR + fsync + validate |
| `otg validate-adr` | ADR frontmatter 结构校验 |

Daemon 在阶段会话成功后通过 `git diff --name-only` 扫描工作区所有 `.md` 变更，调用 `ValidateDocument` 兜底检测 CONTEXT.md、ADR 等非 TASK 文件的损坏。

#### 4.6.4 CONTEXT.md 自动维护

项目的 `Notes/CONTEXT.md` 是共享领域词汇表，由两个阶段自动维护：

- **Round 1**：计划中引入新领域术语时追加到 `## Language` 区域
- **Round 2 + ADR**：ADR 引入新架构概念时追加到 `## Language` 区域

append-only，不覆盖已有条目。daemon 的 `ensureProjectContext`（`internal/daemon/context.go`）在新项目首轮 dispatch 时创建骨架模板，refining skill 通过 `otg ensure-context-term` 即时追加新术语。

#### 4.6.5 Priority Assessment（优先级评定）

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `priority` | string | `""` | 优先级标签，人工或自动填写 |
| `priority_assessment_status` | string | `""` | `pending` / `running` / `completed` / `failed` |
| `priority_impact` | string | `""` | 影响范围描述 |
| `priority_urgency` | string | `""` | 紧急程度描述 |
| `priority_workaround` | string | `""` | 已知变通方案 |
| `priority_score` | int | `0` | 综合优先级分数 |
| `priority_confidence` | string | `"0"` | 评定置信度 0.0–1.0（数值以字符串存储） |
| `priority_reason` | string | `""` | 评定理由 |
| `priority_recommendation` | string | `""` | 推荐处理策略 |
| `priority_assessed_value` | string | `""` | 评定结果值 |
| `priority_assessed_at` | ISO8601 | `""` | 评定完成时间 |
| `priority_assessment_attempts` | int | `0` | 评定重试次数 |
| `priority_assessment_started_at` | ISO8601 | `""` | 评定开始时间 |

Priority Assessment 由 daemon 在**每轮 scan 末尾**触发（与 refining 并行，每轮 ≤2 个，不阻塞 ready→refining；API key 不可用时跳过），评定完成后写入结果字段。`priority_score` 用于调度排序。疑似 P0 只写 `priority_recommendation`，P0 必须由用户手工确认。

#### 4.6.6 Closed 终态字段

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `closure_reason` | string | `""` | 关闭原因：`not-bet`（评估后不下注）/ `already-implemented`（已实现）/ `duplicate`（重复）/ `cancelled`（取消）/ `wont-fix`（不予处理）。`already-implemented` 与 `duplicate` 满足 `blocked_by` 依赖，`cancelled` 不满足 |
| `closure_note` | string | `""` | 关闭备注 |
| `replacement_task` | string | `""` | 替代任务 ID（`project:TASK-NNN` 格式） |

`closed` 是终态，不可恢复。普通入口必须有人工关闭授权、合法原因与备注；Stage-Review `end` 仅关闭尚未开始交付的未来任务——已有计划/分支/PR/checkpoint/merge 状态或处于 planning/implementing/review/conflict 的任务会阻断整次 end，避免自动关闭正在交付的工作。`closure_reason` 提供审计追溯。

#### 4.6.7 Scaffold Intent（脚手架意图）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `scaffold` | object | `{}` | 新项目脚手架意图：`kind`（类型）、`capabilities`（能力列表）、`preferences`（键值偏好）、`notes`（自然语言说明） |

`scaffold` 结构化描述新项目技术栈、框架、构建系统和部署目标（代码 `ScaffoldIntent` 结构体）。原 `template` 字段保留向后兼容。**接线状态**：frontmatter 解析已实现；Round 1 Step 2.5 的能力校验走**知识库检索**（`otg kb search` 能力主题，注册表已废弃——`scaffold_registry`/`template_registry` 无代码消费者且自动生成噪音化，能力元数据由知识库主题承担）。

#### 4.6.8 GitHub Remote Creation（远程仓库创建）

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | -------- | ------ |
| `remote_create` | bool | `false` | 是否在 GitHub 创建远程仓库 |
| `github_owner` | string | `""` | GitHub owner（用户或组织名） |
| `repository_name` | string | `""` | 仓库名 |
| `repository_visibility` | string | `"private"` | `private` / `public` / `internal` |
| `repository_description` | string | `""` | 仓库描述 |
| `repository_url` | string | `""` | 远端仓库地址；非空时创建逻辑短路（幂等） |

`remote_create=true` 时 daemon 在 Round 2 开始前创建 GitHub 远程仓库并设置 `origin`（`ensureRemoteRepository`）：

- **命名**：`repository_name` 显式值优先；否则用项目名去掉 Vault 数字前缀（`001-release-manager` → `release-manager`）。
- **描述**：由 agent 在 Round 1 从需求自主提炼，写入 `repository_description`（项目定位 + 核心能力，≤200 字符）；daemon 侧 REQ 标题+摘要提炼为兜底；传给 `gh repo create --description` 并写入 `README.md`（含初始 commit）。
- **可见性**：默认 `private`（`repository_visibility` 可覆盖；daemon 不持续关注仓库性质）。
- **gh 版本**：`gh ≥2.9x` 要求 `--source` 才能用 `--remote`，两处创建路径（新项目/提升）均以 `--source .` 形式从仓库目录执行。
- **失败处理**：`gh repo create` 失败 → 探测 `gh repo view`（已存在则补 origin 并记录 URL 继续）；仍失败 → 任务 `blocked` + `REMOTE_PARTIAL_CREATE`（不消耗重试预算，人工 resume 幂等重试）。
- **既有项目提升路径（`ensureProjectCheckout`）**：已注册项目 path 回退 vault 目录（非 git 根）且配置 `git_remote` 时，`resolveRepo` 自动创建 `new_project_root/<name>` 独立 checkout（README 初始提交）并把 vault-map `path` 更新指向 checkout；远端仓库缺失时同样自动 `gh repo create`（private，description 从 REQ 蒸馏），无需 `remote_create=true`（git_remote 注册即声明仓库归属）。详见仓库 docs/workflow.md §6.5。

#### 4.6.9 文档校验字段

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `task_schema_version` | int | `1` | TASK frontmatter schema 版本号，用于迁移和兼容性校验 |

`otg validate-doc` 读取 `task_schema_version` 决定校验规则集。版本升级时 `otg repair-doc` 可自动迁移字段。

#### 4.6.10 完成审计（独立验证门禁）

`auto_merge: true` 的任务进入 `review` 后、合并授权前，daemon 运行**独立只读审计会话**（受限工具面 `read`/`grep`/`bash`，无写工具——实现者不能自证完成）。完整语义见仓库 docs/workflow.md §7.4；要点：

- **触发**：`status=review` + `auto_merge=true` + `merge_approved=false` + `audit_status != passed`；人工已授权或 `audit.enabled=false` 跳过。
- **会话**：assignee 模型（`audit.model` 覆盖）、`--thinking off`、超时 `audit.timeout_minutes`（默认 15）；在**任务 worktree** 内运行（与 round2 同分支），逐条 AC 复核原始证据，输出 strict JSON 判定。
- **环境清理核验（强制）**：审计同时复核实现会话的清理证据——自建临时资源（k3d 集群、docker 容器/网络、临时凭据、冒烟日志与构建产物）残留，或发现会话曾停用用户常驻服务（kb-reranker、ollama-sycl、桌面进程等）换取门禁通过 → verdict=fail（failure_type=implementation）。
- **pass** → `audit_status=passed`、清 `audit_fail_count`，继续合并授权流程。
- **fail + implementation** → `phase_error_code=AUDIT_FAILED` + 审计摘要，转 implementing 自动修复（round2 加载 `skill://diagnosing-bugs` 消费 `phase_error`/`audit_log`）；连续 `audit.max_fixes` 次 → 升级 **needs-grilling 决策**（`grill_prev_status=implementing`；resume 回 implementing 重置预算 / replan 回 refining），非 blocked。
- **fail + requirement**（AC 歧义/矛盾/不可验证）→ 直接转 needs-grilling 决策，不消耗修复预算。
- **会话失败/中断**（模型崩溃、API key 不可用、daemon 重启）→ 保持 `review` + `audit_status=pending`，下一轮 scan 重试；进程级失败 2 分钟冷却防烧 token。
- **并发**：受 `phase_concurrency["audit"]`（默认 1）限制，满员留待下一轮 scan。

### 4.7 Daemon 上下文注入

Daemon 在调度 DSH 阶段会话执行 `refining`、`planning`、`implementing`、`plan-review` 阶段与 **merge 冲突/CI 修复会话**时，从 Vault `Notes/CONTEXT.md` 提取精简 bundle，以 `<project_context>` 标签块追加到 skill 命令之后的 prompt 尾部，并附 `skill://knowledge-base` 交叉引用提示。merge 会话注入目的：AI 按需求意图裁决语义冲突（结合 Merge Skill 的强制需求溯源），而非纯代码结构判断。

注入格式（技能命令后追加）：

```text
<skill prompt>

<project_context>
## 项目上下文（daemon 自动注入，配合 skill://knowledge-base 交叉引用 References）
项目: <project-key>

<Constraints + Anti-patterns + Domain Terms + ADR 摘要>
</project_context>
```

注入内容（控制在 ~750 字节 / ~300 token）：

- **Constraints**（始终注入）：`## Development Constraints` 节，截断到 100 字符/条
- **Anti-patterns**（始终注入）：`## Anti-patterns` 节，仅保留首句
- **Domain Terms**（动态选择）：`## Language` 节中按 REQ 关键词打分选 Top-N，无命中时 fallback 到核心术语（数量由 `dynamicTermCount` 按预算动态分配 1–6 个）；长定义截断到 80 字符
- **ADR**（可选）：`Notes/adr/*.md` 中按 REQ 关键词匹配 Top-2，含 title + 一句 decision

术语数量由 `dynamicTermCount` 按剩余 token 预算动态分配。同一项目多次调度缓存 CONTEXT.md 内容（`sync.Map`），避免重复 IO。

### 4.8 阶段化交付（Stage-based Delivery）

新项目与大型需求按**阶段**交付：每阶段有可演示成果，阶段收尾由 PM 评审评分、用户决定继续/补充/结束——避免"任务永无止境、用户无体验"（release-manager 教训：76 个任务跑一个月无原型体验）。

**核心文件与字段**：

| 对象 | 说明 |
| ------ | ------ |
| `Notes/Stage-Plan.md` | 阶段权威定义。固定格式：`### Phase N: {名称}` 块 + `- 目标:` / `- tasks:` / `- status:` 行（daemon 解析契约；只由 `stageplan` 包写入，PM/agent 不得自行追加阶段块） |
| `Notes/Stage-Review.md` | PM 阶段评审产出：四维评分（完成度/质量/一致性/用户可体验性）+ 建议 + 「评审决策: continue / supplement:{建议} / end」 |
| `stage` 字段（TASK/REQ frontmatter） | 阶段归属**权威判定**（`P{N}`）。创建 TASK 时从 REQ 继承；PM 拆分落地时写入；daemon 阶段完成检测按字段聚合，不依赖 Stage-Plan 的 tasks 列表（字段跟随任务移动，永不过期） |
| `Notes/Roadmap.md` | 项目发展历史总览（回顾性，与 Stage-Plan 前瞻性互补），daemon 在交付事件点确定性追加（阶段评审/阶段决策/收口/归档），PM 补充语义 |

**确定性自动分组（`processAutoStaging`）**：daemon 每轮 scan 对未分阶段（stage 空）的进行中任务执行确定性拓扑分层 → 合并为阶段（`stage_min_per_phase`/`stage_max_phases` 配置控制）→ 写 Stage-Plan 骨架 + 批量写入 stage 字段。秒级、幂等、增量追加（编号接续），无需 LLM 会话。手动触发：`otg stage-plan init <project>`（`--force` 重建 / `--dry-run` 预览）。

**贯穿型需求**（e2e/测试/环境/CI）：按阶段拆成**场景包**，只依赖同阶段或更早阶段交付——禁止一次性全量（TASK-066 17 轮 replan 死锁的教训）。

**阶段完成与评审**：daemon 检测某 in-progress 阶段全部任务 done+merged（`merge_status=merged`，stale PR 不算）→ 调 PM `stage-review` 评分写 Stage-Review.md → 用户填「评审决策:」→ daemon 检测到后调 PM `distribute`：`continue`（下一阶段 in-progress）/ `supplement:{建议}`（追加到下一阶段）/ `end`（后续阶段任务 close，功能满足即结束，不维护积压）。

**PM 语义层职责**（机械分组已由 daemon 承担）：补充阶段目标描述、按用户意图调整阶段边界（`stage-plan init --force` 或改 stage 字段）、新需求到达时评估归入现有阶段或建议追加新阶段（写清单「阶段规划确认」区，用户拍板）。

## 5. 需求变更行为

### implementing

Round 2 每完成一条 AC 后重新读取 TASK。若 pending_req=true：

1. 不开始下一条 AC。
2. 提交 `chore(task): checkpoint before requirement replan`。
3. 写 `checkpoint_commit`。
4. 转 `refining`，保持 pending_req=true。

### Grilling 阻塞分流

- 需求细化、计划外设计决策、架构假设变化：`grill_resolution=replan`。
- 纯代码逻辑错误、环境问题且无需修改规格/计划：`grill_resolution=resume`。
- resume 恢复 `grill_prev_status`；replan 保持 pending_req=true 并转 refining。

### OnReqChanged 状态规则

- blocked：保持 blocked，pending_req=true；阶段失败子集（可恢复错误码 + blocked_phase 非空）由 daemon 自动转 refining（`recoverBlockedPendingReq`），不复用旧 phase。
- ready：保持 ready，pending_req=true。
- refining/planning：仅 pending_req=true，不改 live phase。
- needs-grilling + active owner：不中断会话，只设 pending_req=true。
- plan-review：撤销 plan approval，转 refining。
- implementing：当前 AC 后 checkpoint → refining。
- review/conflict：清 merge approval，直接 refining（未合并交付必须在吸收变更后合入）。
- done：按 REQ 最新变更记录 `> 变更类型:` 路由——`breaking`/未标注：清 merge approval 转 refining + 代际重置（reopen_count+1、清 target_branch/pr_url/merge_status/completed/knowledge_extracted）；`additive`：保持 done 终态 + 通知（建议新建 TASK 承接增量或手动重开）；`cosmetic`：忽略。
- **已吸收去重**：任务 `refine_req_hash` == REQ 当前 hash 时跳过（refining/PM 写回自身记录不重复触发）。
- 新自动创建 TASK：pending_req=false。

### review / conflict / done

直接清 merge_approved 并转 refining。禁止合并已知基于过期需求的实现。

## 6. ID 与依赖解析

- TASK/REQ 数字 ID 项目内唯一。
- `TASK-010` 只在当前项目解析。
- `release-manager:TASK-010` 通过 vault-map project key 精确定位跨项目依赖。
- `req_doc` 使用 `Projects/<project>/Requirements/REQ-...md`，只做规范完整路径匹配；禁止 basename fallback。

## 7. 通知

`notifications.desktop` 只控制 `notify-send`：

- false：关闭动作、提醒和最终状态的系统桌面通知。
- Kitty tab 不受该字段控制，Grilling 时始终尝试创建。
- 同一 TASK 只允许一个活跃 Grilling tab。Daemon 创建前解析 `kitty @ ls`，按 `Grilling <task-id>` 检查所有 tab/window title；任务标题变化或 Unicode JSON 转义不会触发第二个 tab。
- per-task 文件锁和每次尝试前写入的 5 分钟 debounce 时间戳防止并发扫描或 daemon 重启重复创建。
- **提交后异步写回 + 自动关 tab**：kitty-grill 提交答案后 spawn detached 子进程（`--writeback` 模式）重新挂接 session 完成写回（日志 `~/.dsh/logs/kitty-grill/writeback-*.log`，10 分钟超时），主进程 `kitty @ close-window --match id:$KITTY_WINDOW_ID` 关闭本 tab；spawn 失败回退有界同步写回。**写回守卫**：启动与写回前复查任务 status，已离开 needs-grilling 阻止写回并提示。问卷 prompt 以 `任务 TASK-<id>` 开头（监控面板按第一个 TASK-xxx 打标签）。
- **需求变更通知按 taskID+action 5 分钟防抖**（`notifyReqChanged`）：grilling 写回多次改写 REQ 时，同一任务同一 action 只发第一条「需求变更」toast。
- Kitty 状态 JSON 无法解析时不会创建 tab，并回退到桌面通知；后续扫描继续重试。
- Kitty 不可用：保持 needs-grilling，写日志并周期重试，不转 blocked，不启动普通终端。

## 8. Daemon 与并发

Daemon 锁：`${TMPDIR}/otg-daemon-<vault-path-sha256>.lock`。

- 同一 Vault watcher/timer 互斥。
- 不同 Vault 可并行。
- refining 不需要仓库（但 `resolveRepo` 会对 vault 回退且配置 `git_remote` 的项目做一次性的独立 checkout 提升与远端仓库补建，见仓库 docs/workflow.md §6.5）。
- 既有项目 planning 使用主工作区独占锁；Merge 无仓库锁（push/merge 在主 checkout 上执行，与 worktree 阶段会话隔离，避免被 planning/refining 读锁长期阻塞）。
- Round 2 使用任务专属 worktree。
- 新项目 planning 不创建目录；Round 2 才创建并 register-project。
- 每轮 scan 自动 Normalize 全部任务 frontmatter：缺失的 schema 字段按默认值补齐（不覆盖已有值，必填字段不补）、字段顺序按规范序维护（用户关注在前、系统维护在后，未知字段保持相对顺序置尾）；写前/写后均做 Parse 校验，损坏文档拒绝改写；补齐后校验必填完整性并记录诊断。`otg migrate-tasks <path> --write` 手动执行同一逻辑。**REQ frontmatter 同样每轮 Normalize**（`NormalizeReqFrontmatter` / `syncReqSchemaDefaults`）：稳定字段（created/updated/tags）回填，必填身份与可选决策字段不伪造——旧 REQ 被重拾（done 任务 breaking 重开）时字段演进不静默断链。
- **异步调度**：`processBatch` 只调度（dispatch）不等待——每个任务在独立 `runTask` goroutine 中执行，完成后释放仓库锁并 `requestScan()` 触发下一轮 scan（scan-gate coalesce，任务批量完成只多一轮）。一个长 Round 2（最长 1h）不再冻结 scan 循环：plan-review transition、merge 重试、REQ 变更等全部实时响应。`--once`（systemd timer）保持同步等待语义（dispatch 后等任务归零）。shutdown 时 `activeTasks` 计数等待在跑任务落盘（PHASE_INTERRUPTED 写回）后退出。
- **阶段并发上限（`phase_concurrency`）**：`max_concurrent_tasks_per_project`（每项目 implementing 上限，默认 2）与 `max_concurrent_tasks`（可选全局总封顶，0=不限）只限制 implementing。其它启动 DSH 阶段会话的阶段由 vault-map.json 顶层 `phase_concurrency` 按阶段限并发（默认 `refining: 3, planning: 2, merge: 1, priority: 1, pm: 1, audit: 1`）——防止一轮 scan 同时拉起 20+ 个 DSH 阶段会话造成 token 快速消耗、API 限速与本地资源抢占。调度循环非阻塞 tryAcquire：上限满的任务留在 pending，等其它任务完成（runTask → requestScan）后下一轮自动调度。key 置 `0`/删除 = 不限；`round2` 由两个 implementing 上限控制。修改后重启 daemon 生效。**消费点（2026-08-25 全部接线）**：`refining`/`planning`/`priority`/`audit` 由 `phaseGateKey` 门禁段生效；`merge` 在 merge 分支（review/conflict + merge_approved 提前 `continue` 处）获取/释放；`pm` 在 `runGrillingPM`（distribute/consolidate/stage-review 唯一派发点）获取并跨会话生命周期持有，满时 `errPMGateFull` 下一轮 scan 重试。

### 8.1 Thinking Mode

daemon 按阶段注入 `--thinking`，flash 与 pro 模型均支持：

| 阶段 | thinking |
|------|----------|
| priority | `off` |
| refining | `low` |
| planning | `high` |
| round2 | `max` |
| audit（完成审计会话） | `off` |

模型标识不含 `:xhigh` 等推理后缀，强度完全由 `--thinking` 控制。

### 8.2 自动 resume 预算

`resolveBlockedDependencies` 每次扫描自动解开 `blocked_by` 依赖链，预算规则：

- `auto_resume_pending=true` 仅标记自动 resume 发起的尝试；`handlePhaseFailure` 只在 pending 时递增 `auto_resume_count`。
- 首次失败不计数；人工 resume（无 pending）清零计数。
- count ≥ 2：停止自动恢复，通知用户手动修复后设 `resume_approved=true`。
- 循环依赖与 `REQ_MISSING`/`VALIDATION_FAILED` 等非瞬时错误不自动恢复。

## 9. Skill 安装

Installer 随包安装 8 个顶层 Skill（真实文件，非 symlink，清单见 `skills/manifest`）：refining、round1、round2、merge、**conventions**（**已有项目**基线审查门禁：规范 + 架构约束）、priority、pm、split。子 Skill 同时写入 `skills/` 子目录供 daemon 直读。

**`vault-map.json` 保护**：`otg install --force` 不会覆盖用户的项目映射和模型配置。安装前备份 `config/vault-map.json`，拷贝后恢复。`generateVaultMap` 对已有文件只追加缺失的默认字段，不覆盖已设置的 `projects`、`models` 等用户值。

**知识库字段（`kb_db` / `kb_embedding` / `kb_rerank` / `kb_chat`）**：`kb_db` 覆盖检索库路径（默认 `~/.local/share/otg/kb.sqlite`，多 vault 机器必须为每个 vault 独立配置）；`kb_embedding` 启用语义混合检索（`backend`/`url`/`model`/`api_key`/`weight`/`chunk_chars`/`batch_size`/`knn_candidates`，缺省则纯 BM25）；`kb_rerank` 可选 cross-encoder 精排（`backend`/`url`/`model`/`top_n`，后端不可用自动降级）；`kb_chat` 启用 `otg kb ask` 问答生成（`backend`/`url`/`model`/`temperature`）。字段含义与部署示例见 README「知识库语义检索」「检索精排」「知识库问答」与 `obsidian-task-runner/config/vault-map.example.json`。


**团队项目字段（`projects[].project_type` / `projects[].merge_mode`）**：`project_type: team` 标记已存在的组织仓库（如私有 Gitea）——daemon 禁止自动建仓/自动注册/checkout 提升/`gh repo create`/`remote_create`，仓库归团队所有。`merge_mode` 三选一：缺省/`auto`（个人项目 gh 全自动）、`manual`（直接在团队仓库上开发：推分支 → `merge_status=pushed` → 人工在仓库 UI 合并 → daemon 远端探测自动 done）、`fork-merge`（fork 开发，`git_remote` 指向自己的 fork：本地 merge 进 fork 默认分支（冲突 AI 解决）→ push → done → 用户手动向团队项目发 PR）。两字段均由用户手工填写，daemon 注册/更新时**保留**（不覆盖）。**team 终态保护（2026-08-25 补全）**：`detectStaleDoneReopens` 与 done→review 自动重开（`DoneReopensMerge`）对 team 均跳过——done+merged 是权威交付证据，forge 生命周期完全人工；用户手动置 done 而 `merge_status=pushed`/残留 PR URL 的 team 任务不会再被拽回 review。

**已有项目基线门禁（所有已存在项目，非仅团队）**：任何**已注册且存在 checkout** 的项目（`projectIsExisting`，含团队项目）首个任务在 refining 前必须先过只读基线审查（`/obsidian-task-runner-conventions`）——产物 `Notes/PROJECT-CONVENTIONS.md`（**规范 + 架构约束**：技术栈、数据库分环境、schema/字段命名、迁移机制）即一次性门禁标记。004-deployd 教训：普通已有项目（非团队）此前不过门禁，开发用 SQLite、测试/生产用 MySQL，字段名结尾不一致导致返工——现在统一拦截。失败转 blocked（`CONVENTIONS_REVIEW_FAILED`），resume 重跑。**只读工具面由 daemon 层硬限制**（2026-08-25 起）：`ToolPolicy="read,grep,glob,bash"` 随 embed RPC 下发，agent-server 注入硬约束 preamble 并对白名单外 `tool/call` 判 `tool_policy_violation` 失败。详见 docs/workflow.md §6.5.1 与 §8.2。

**vault-map 自主维护（daemon）**：

- **新项目自动注册**：Round 2 首次调度 `new_project=true` 任务时自动写入 `projects` 条目——`name`/`path` 按解析结果，`git_remote` 从既有项目推断 owner（`github.com/<owner>/<name>`），`project_id` 自动分配（既有最大值 +1，`%03d`），并播种 `Notes/CONTEXT.md` 骨架。
- **保序写入**：所有 daemon 维护写回（注册、默认补齐）保留用户手排的顶层字段顺序（`jsonorder` 保序解析/序列化），不按字母序重排；缺失字段按 Config 声明序追加到文件末尾，且 **`projects` 恒置末尾**（追加新项目是最频繁的手工编辑——`generateVaultMap` 新建与 `ensureVaultMapDefaults` 补齐都遵守该布局；用户可自由重排其他字段，重跑 `otg install`/默认补齐不会打乱）。
- **缺失字段自动补齐**：写入前按 `config.Defaults()` 补齐缺失顶层字段（新功能字段自动出现，不覆盖已有值）。
- **脚手架能力（已废弃）**：`scaffold_registry`/`template_registry` 已从配置与代码移除（无消费者、自动生成噪音化）——Round 1 能力校验与 PM 技术栈写回均走知识库检索（能力主题文档承担描述/冲突元数据）；存量 registries.json 文件不再被读取，可手动删除。

**Skill 清单**：installer 安装 core、refining、round1、round2、merge、priority、pm、**split**（需求分解：大 REQ → 3-8 子需求建议，PM 统筹并入 Grilling-Decisions 一次性对齐）。

外部依赖缺失必须 fail-fast：requirement-elaborator、grilling、domain-modeling、diagnosing-bugs、test-quality。

## 10. 故障排查

1. `otg find-ready <vault>`：检查 daemon 是否会拾取任务。
2. `tail -f ~/.dsh/logs/otg-daemon.log`：检查状态分派、锁和重试。
3. `~/.dsh/logs/tasks/`：检查阶段日志（DSH 会话记录在 `~/.dsh/sessions/`）。
4. blocked 阶段失败：检查 `blocked_phase`、`phase_error`、`phase_log`；修复后设 `resume_approved=true`。自动 resume 预算耗尽（`auto_resume_count>=2`）时会收到 🧩 桌面通知。
5. Grilling 卡住：检查 `grill_owner`、`grill_started_at`、timeout 和 Kitty 日志。
6. 安装后执行 `skill-doctor check`，必须返回 0。

## 11. Skill Writing Convention（Skill 编写规范）

All skill documents MUST follow these conventions to maximize AI agent comprehension:

1. **Step headers**: bilingual format `## Step N: English Title（中文标题）`. The English verb is the action primitive agents are trained on.
2. **Mandatory actions**: prefix with RFC 2119 keywords **MUST** / **MUST NOT** / **SHOULD**.
3. **Persona line**: first non-header line MUST start with `**Role**: <English role name>. <one-line constraint>.`
4. **Trigger conditions**: use English tables with `Trigger | Description` columns.
5. **Commands and field names**: always in English (bash blocks, `otg update-status`, frontmatter keys).
6. **Explanatory body**: Chinese is acceptable for context and nuance.
7. **Prohibitions**: separate section, each item prefixed with `MUST NOT`.
