---
name: obsidian-task-runner
description: "Manual entry and reference router for the Obsidian task lifecycle. Daemon directly invokes phase skills (refining, round1, round2, merge, priority, pm, split) and drives stage-based delivery with per-phase concurrency limits and decision-list pause/reactivate. Trigger: task runner, 自动执行 Obsidian 任务, 阶段化交付, 任务并发."
---

# Obsidian Task Runner — Core Contract

本 Skill 是人工入口和流程参考。Daemon 不通过本 Skill 二次路由，而是按状态直接调用阶段 Skill。

## Required Skills

随包安装：

- `skill://obsidian-task-runner-refining`
- `skill://obsidian-task-runner-round1`
- `skill://obsidian-task-runner-round2`
- `skill://obsidian-task-runner-merge`

外部依赖：

- `skill://requirement-elaborator`
- `skill://grilling`
- `skill://domain-modeling`
- `skill://diagnosing-bugs`
- `skill://test-quality`
- `skill://knowledge-base`

## Status Routing（状态路由）
| status | 行为 |
|--------|------|
| `blocked` | 五类：① 缺字段/依赖——补齐后自动 `ready`/`plan-review`；② 阶段失败——`resume_approved=true` 后恢复 `blocked_phase`；③ `API_KEY_UNAVAILABLE`——daemon 每轮探测 key，可用即自动恢复（无需 resume）；④ 人工暂停——`blocked_phase` 非空 + `REQ_MISSING` 等非瞬时错误码，保持阻塞直到手动 resume；⑤ **前置门禁失败**（`PREREQUISITE_SMOKE_FAILED`）——round2 入口门禁（如 AC-066-17）未通过时由 round2 转 blocked，daemon 每轮按 `blocked_by` **事实**（上游 `done` 且 `phase_error_code` 空 = PR 已合入）自动恢复，无用户干预；门禁失败禁止 replan 循环 |
| `ready` | daemon 转 `refining`；priority_assessment 由 daemon 在 scan 末尾并行评估（每轮 ≤2），不阻塞调度 |
| `refining` | daemon 直接调用 refining Skill，使用 models.default；大型需求先加载 skill://wayfinder 生成 Wayfinder Map 决策地图，作为 Grilling 焦点；failed 项三分类收敛——fact 自修正 REQ、auto 采纳建议留 `auto_accepted` 审计（可推翻）、仅 dispute 进 grilling；重复争议（grill_repeat≥2）park 升级到项目级清单 |
| `needs-refining` | 旧版遗留状态；scan 拾起后自动迁移为 needs-grilling（`nextLocalTransition`），随后走正常 Grilling 路径（Kitty tab、提醒、lease） |
| `needs-grilling` | daemon 检查 owner/timeout并创建 Kitty；pending_req 优先强制 refining，否则 resume 恢复 prev status、replan 转 refining，空值继续等待；支持异步 Grilling（grill_continue）；`grill_parked=true` 时**为项目创建「决策清单」Kitty tab**（每项目一个、5min debounce、待答决策点 >0 时）——tab 内 OMP 会话逐项提问，答案写回清单后 daemon 按答案 hash 变更**自动分发**（无需手动 grill_continue）；**禁止任何自动转换**（残留 grill_resolution 不得触发 replan——TASK-066 17 轮零收敛的教训）；争议由 PM 统筹（`skill://obsidian-task-runner-pm`）汇总到 Notes/Grilling-Decisions.md |
| `planning` | daemon 直接调用 Round 1 Skill，使用 TASK assignee |
| `plan-review` | 等待 plan_approved→Round 2；或 close_approved→closed |
| `implementing` | daemon 直接调用 Round 2 Skill；高风险 Step 先跑 Prototype Gate |
| `review` / `conflict` | pending_req 优先→refining；rework=resume→implementing；rework=close→closed；否则 `review` 状态 auto_merge=true 时 daemon 自动授权合并（conflict 需人工重设 merge_approved） |
| `done` | pending_req=true 时回 refining；`merge_status != merged` 且有 PR/分支（任务 done 但 PR 从未合入）→ 自动重开 `review` 走 merge 闭环（auto_merge 自动授权）；否则终态 |
| `closed` | 无需交付终态（Bets, Not Backlogs）。closure_reason: not-bet（评估后不下注）/ already-implemented / duplicate / cancelled / wont-fix。不可自动恢复。重要需求会以新 REQ 形式回来——不维护积压。 |
## Core Invariants（核心不变量）
1. MUST route initial tasks through `ready → refining`；REQ 变更按当前状态设置 pending_req 并安全回 refining。
2. Maturity Gate MUST be fully_mature to enter planning；其他进入 Grilling。
3. 需求细化 Grilling MUST return to refining after completion；实现阻塞按 grill_resolution 恢复或重规划。
4. Planning MUST succeed before plan-review is available。
5. pending_req MUST NOT be cleared until new plan succeeds。
6. MUST NOT merge when pending_req=true；绝对禁止。
7. MUST prioritize pending_req over grill_resolution=resume，过期需求不能恢复旧实现。
8. MUST atomically clear grill_done/resolution/context/prev_status after consuming Grilling results。
9. plan_approved MUST only be valid during plan-review；提前批准自动清 false。
10. MUST NOT create directories during planning for new projects；Round 2 才创建。
11. MUST NOT push during Round 1/Round 2；Merge Phase 才允许远程操作。
12. MUST use system local time for all timestamps。
13. **MUST audit code against skill docs on every skill change** — 每次修改本 SKILL.md 或任何阶段 skill 文档时，必须同时审查 Go 代码（`internal/`、`pkg/`）是否实现了文档描述的能力。发现文档超前于代码的缺口，在计划中标注为"代码追赶项"并阻塞 plan-review。
14. Frontmatter struct (`pkg/yamlfrontmatter/frontmatter.go`) MUST declare all fields referenced in TASK frontmatter schema.
15. `IsReady()` MUST explicitly handle every status value in the status routing table.

## IDs & Dependencies（ID与依赖）
- 数字 ID 项目内唯一。
- 同项目依赖：`TASK-010`。
- 跨项目依赖：`project-key:TASK-010`。
- req_doc 只使用 Vault 相对规范完整路径精确匹配。

## 阶段化交付（Stage-based Delivery）

新项目与大型需求按**阶段**交付，每阶段有可演示成果，阶段收尾由 PM 评审评分、用户决定继续/补充/结束——避免"任务永无止境、用户无体验"（release-manager 教训：76 个任务跑一个月无原型体验）。

- **Stage-Plan.md**（`Projects/{project}/Notes/`）：阶段权威定义（`### Phase N:` 块 + `- tasks:`/`- status:` 固定行格式，daemon 解析）。
- **stage 字段**：TASK/REQ frontmatter `stage: "P{N}"` 是阶段归属的**权威判定**（OnReqChanged 创建 TASK 时从 REQ 继承；PM 拆分落地时写入）。
- **自动阶段化**：daemon 每轮 scan 对未分阶段（stage 空）的进行中任务执行**确定性拓扑分组**（`processAutoStaging`，秒级幂等）——新任务/新需求自动归入新阶段，无需 LLM 会话；也可手动 `otg stage-plan init <project>`（`--force` 重建 / `--dry-run` 预览）。
- **贯穿型需求**（e2e/测试/环境/CI）：按阶段拆成**场景包**，只依赖同阶段或更早阶段交付——禁止一次性全量（TASK-066 17 轮 replan 死锁的教训）。
- **阶段完成**：daemon 检测某 in-progress 阶段全部 `stage` 任务 done+merged → 调 PM `stage-review`（四维评分 + 建议 → `Notes/Stage-Review.md`）。
- **用户决策**：回答 Stage-Review「评审决策: continue / supplement:{建议} / end」→ daemon 分发（继续下一阶段 / 建议写入下一阶段 / 后续阶段任务 close，功能满足即结束）。
- **阶段规模**：由配置 `stage_min_per_phase`/`stage_max_phases` 控制（daemon 分组参数）；PM 仅在新需求到达时评估归入现有阶段或**建议增/拆阶段**（写清单「阶段规划确认」区，用户拍板，不塞进进行中阶段）。

## Roadmap（里程碑路线图）

`Notes/Roadmap.md`：项目发展历史总览（里程碑时间线 + 当前状态），PM 在阶段化/阶段评审时自动维护。用户可随时查看项目走到哪、经历过什么；与 `Stage-Plan.md`（前瞻规划）互补。细化阶段产物（路线图/领域索引类 REQ）归档见 `Notes/legacy-requirements.md`。

## OnReqChanged（需求变更联动）
- blocked：保持 blocked，pending_req=true。
- ready：保持 ready，pending_req=true。
- refining/planning：只设 pending_req，不中断 live phase。
- needs-grilling + active owner：只设 pending_req，不清 owner、不重开 Kitty。
- plan-review：撤销批准，转 refining。
- implementing：当前 AC 后 checkpoint → refining。
- review/conflict/done：清 Merge 授权，转 refining。
- 新自动创建 TASK：pending_req=false。

## Daemon 重启与中断恢复

- daemon 收到 SIGTERM（`systemctl stop`、`otg install`、重启）时优雅停机：运行中的 OMP 先收 SIGTERM 保存 session（30 秒内未退出则强制终止），停机期间不启动 fallback。
- 被中断的 phase **不视为失败**：任务保持原状态（`refining`/`planning`/`implementing`），写入 `phase_error_code=PHASE_INTERRUPTED` 标记；daemon 重启后下一轮 scan 自动重新调度——无 `blocked`、无手动 `resume_approved`。
- 阶段成功后自动清理 `PHASE_INTERRUPTED` 标记（`clearPhaseError`）。
- `otg install` 的 stopDaemon 阻塞等待 systemd 优雅停机完成后再安装，不与新实例竞态。
- 依赖链自动恢复（`resolveBlockedDependencies`）识别 `PHASE_INTERRUPTED` 为可恢复错误码（同 `MODEL_FAILED`/`PHASE_TIMEOUT`）。

## Notifications（通知）
- `notifications.desktop` 只控制 notify-send。
- Kitty Grilling tab 始终尝试创建，不受 desktop 开关控制。
- 同一 TASK 只允许一个活跃 Grilling tab；创建前按 task ID 检查 Kitty tab/window title，并以 per-task flock + debounce 防止并发和重启重复创建。
- Kitty JSON 无法解析时不创建 tab，保留 notify-send fallback；Kitty 不可用时保持 needs-grilling 并周期重试。

## Fallback Model（兜底模型）

进程级失败（OMP exit / 阶段超时 / quota）由 daemon 按 vault-map.json 顶层 `fallback_models` 重启 OMP：key 为 assignee（对应 `models` 的 key），value 为任意 OMP 模型标识；可增删任意 key、置 `""` 禁用单个 assignee 的兜底。会话内 API 错误兜底由 omp `config.yml` 的 `retry.fallbackChains`（按 role）承担——daemon 层兜底只覆盖进程级失败。见 ADR-012。

## Frontmatter 字段规范

TASK frontmatter 有**规范字段序**（`pkg/yamlfrontmatter/frontmatter.go` 的 `taskFieldOrder`）：用户关注的字段（身份、priority、Gate、推荐 metadata）在前，daemon 维护字段在后。daemon 每轮 scan 自动 Normalize（补齐缺失字段 + 按规范序重排，不覆盖已有值）；模板与 snippet 必须与规范序一致，避免新任务文档被反复改写。

- **弃用字段**：`switch_settings`（迁移专用，新代码/新文档必须用 `assignee`）、REQ 的 `domain`/`parent_req`/`task_size`（不再被解析）。模板与文档不得再写入；存量文档由 `otg migrate-tasks <path> --write` 或手工清理。
- `stage` 字段是阶段归属的**权威判定**（TASK 从 REQ 继承，PM 拆分落地时写入），与 `Notes/Stage-Plan.md` 的 `### Phase N:` 块对应。

## Documentation（文档）
完整规范和实现验收清单见 `docs/workflow.md`；字段参考见 `reference.md`。

## 知识库 KB v2 格式规范（References/）

知识库文件格式的完整规范（frontmatter 6 字段、摘要前置、目录强制、要点化、噪音零容忍、verified 语义、交互经验归类规则、分类体系）见 `skill://knowledge-base` 的「知识库文件格式 — 强制要求」「分类体系」与「交互经验归类规则」章节——本文件不重复定义，仅在本 Skill 检索/入库时遵循该规范。
