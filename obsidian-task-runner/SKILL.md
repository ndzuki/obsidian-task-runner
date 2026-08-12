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

## 构建强制条款（防坑）

**otg 构建必须带 `-tags sqlite_fts5`**（Makefile 与 CI 已内置，任何绕过 Makefile 的手动 `go build`/`go test` 必须自行追加）。知识库检索库依赖 SQLite FTS5，而 mattn/go-sqlite3 的 FTS5 是 **opt-in 编译宏**：

- 不带 tag 的 otg **能编译、能运行、能建库**，但所有 `otg kb *` 命令在打开检索库时失败：`no such module: fts5`（新版 otg 会附带构建提示）。这是**构建缺 tag**，不是环境/配置问题。
- 正确构建：`make build` / `make install-force`；判断已装二进制是否缺 tag：跑 `otg kb search "x"`，报 `no such module: fts5` 即缺 tag，重跑 `make install-force`。
- 本 repo 的 `go build`/`go test`/CI 全部走 `-tags sqlite_fts5`；新增构建入口（脚本、workflow、容器镜像）必须同样携带，否则知识库功能静默不可用。

**代码注释用英文**（本项目为开源仓库，代码注释与 commit 均遵循英文惯例，对齐 AGENTS.md 例外条款）：任何由本任务体系（refining / round1 / round2 / merge / daemon 自身开发）新增或修改的代码注释一律英文；仅函数签名、导出符号声明等结构性注释可双语。

## Status Routing（状态路由）
| status | 行为 |
|--------|------|
| `blocked` | 五类：① 缺字段/依赖——补齐后自动 `ready`/`plan-review`；② 阶段失败——`resume_approved=true` 后恢复 `blocked_phase`；③ `API_KEY_UNAVAILABLE`——daemon 每轮探测 key，可用即自动恢复（无需 resume）；④ 人工暂停——`blocked_phase` 非空 + `REQ_MISSING` 等非瞬时错误码，保持阻塞直到手动 resume；⑤ **前置门禁失败**（`PREREQUISITE_SMOKE_FAILED`）——round2 入口门禁（如 AC-066-17）未通过时由 round2 转 blocked，daemon 每轮按 `blocked_by` **事实**（上游 `done` 且 `phase_error_code` 空 = PR 已合入）自动恢复，无用户干预；门禁失败禁止 replan 循环 |
| `ready` | daemon 转 `refining`；priority_assessment 由 daemon 在 scan 末尾并行评估（每轮 ≤2），不阻塞调度 |
| `refining` | daemon 直接调用 refining Skill，使用 models.default；大型需求先加载 skill://wayfinder 生成 Wayfinder Map 决策地图，作为 Grilling 焦点；failed 项三分类收敛——fact 自修正 REQ、auto 采纳建议留 `auto_accepted` 审计（可推翻）、仅 dispute 进 grilling；重复争议（grill_repeat≥2）park 升级到项目级清单 |
| `needs-refining` | 旧版遗留状态；scan 拾起后自动迁移为 needs-grilling（`nextLocalTransition`），随后走正常 Grilling 路径（Kitty tab、提醒、lease） |
| `needs-grilling` | daemon 检查 owner/timeout并创建 Kitty；pending_req 优先强制 refining，否则 resume 恢复 prev status、replan 转 refining，空值继续等待；支持异步 Grilling（grill_continue）；`grill_parked=true` 时**为项目创建「决策清单」Kitty tab**（每项目一个、5min debounce、待答决策点 >0 时）——tab 内 OMP 会话逐项提问，答案写回清单后 daemon 按答案 hash 变更**自动分发**（无需手动 grill_continue）；**禁止任何自动转换**（残留 grill_resolution 不得触发 replan——TASK-066 17 轮零收敛的教训）；争议由 PM 统筹（`skill://obsidian-task-runner-pm`）汇总到 Notes/Grilling-Decisions.md |
| `planning` | daemon 直接调用 Round 1 Skill，使用 TASK assignee |
| `plan-review` | auto_approve 默认 true（缺失即 true，模板已写入）→ daemon 自动 `plan_approved=true` 转 implementing；显式 `auto_approve: false` 时等待人工 `plan_approved=true`；关闭必须同时满足 `rework_resolution=close` + `close_approved=true` + 合法 `closure_reason` + 非空 `closure_note`（duplicate 还需 `replacement_task`）。**Grilling 是唯一常规人工关卡** |
| `implementing` | daemon 直接调用 Round 2 Skill；高风险 Step 先跑 Prototype Gate；**空转冷却**：会话完成后仍 implementing 且无 `checkpoint_commit`（入口门禁复验类）→ 指数退避冷却（10m→…→~10.7h 上限）不重派；有进展即重置。不会自动转 closed |
| `review` / `conflict` | pending_req 优先→refining；rework=resume→implementing；关闭门禁仅对 `review` 生效（conflict 不关闭——先解决合并冲突）；否则 auto_merge=true 时 daemon 自动授权合并——**merge 失败回退（REQ 未变 + 预算未耗尽）同样自动重授权**（`canAutoApproveMerge`：非 `GITHUB_UNAVAILABLE`/`REPO_MISMATCH` 永久缺陷即重试），停机/超时中断保持授权重启自动恢复。AI 修复预算（`merge_retry_count`，上限 `max_auto_merge_fixes`）耗尽后交还用户：① 清计数重授权继续 AI 修复；② replan（review 设 `rework_resolution=replan`；conflict 在 REQ 追加歧义裁决保存→自动转 refining）——详见 `skill://obsidian-task-runner-merge`「预算恢复」 |
| `done` | REQ 变更按类型路由：`breaking`（含未标注，保守）→ pending_req=true 回 refining，代际重置（reopen_count+1、清 target_branch/pr_url/merge_status/completed/knowledge_extracted，新一轮交付新 PR）；`additive`（纯增量向后兼容）→ 保持 done，通知建议新建 TASK 承接；`cosmetic`（措辞/格式）→ 忽略；`merge_status != merged` 且有 PR/分支（任务 done 但 PR 从未合入）→ 自动重开 `review` 走 merge 闭环（auto_merge 自动授权）；否则终态；不会自动转 closed |
| `closed` | 无需交付终态（Bets, Not Backlogs）。仅两条入口：① plan-review/review 的显式关闭门禁（用户批准 + 原因 + 备注）；② Stage-Review 用户决策 `end` 关闭**尚未开始交付**的后续阶段任务。已有计划/分支/PR/checkpoint/merge 状态或处于 planning/implementing/review/conflict 的任务会阻断整次 stage end，禁止自动关闭。closure_reason: not-bet / already-implemented / duplicate / cancelled / wont-fix。不可自动恢复 |
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
16. **done 任务仅 breaking（含未标注）变更重开**；additive/cosmetic 不重开已交付终态；重开必须代际重置（reopen_count+1 + 清旧 PR/分支/merge 事实），禁止复用已 MERGED 的旧 PR（会让新交付永远合不进去）。
17. **merge AI 修复预算（`merge_retry_count`）仅在 merge 成功或新一轮 planning 完成时清零**；replan 不继承旧交付耗尽；预算耗尽后 review 走 `rework_resolution=replan`、conflict 走 REQ 追加歧义裁决自动转 refining，均无需手动解冲突。

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
- **阶段顺序调度**：daemon 对 ready 任务按 **项目内 stage 升序**（数字序，`P10` 在 `P2` 后）→ priority → created 排序拾取——低阶段任务优先消耗实现容量，P1 未收敛前 P2+ 任务不抢容量；**跨项目不做 stage 比较**（各项目阶段独立，保持创建时间公平），未分阶段任务排最后（当轮即被 auto-staging 归组）。阶段只用于「顺序调度 + 完成后评审」，不阻止后阶段任务提前进入 refining/planning——依赖先后由 `blocked_by` 表达（release-manager 教训：无依赖声明的并发实现产生 57/253 冲突合并与 11 次 v2/v3 返工）。
- **阶段完成**：daemon 检测某 in-progress 阶段全部 `stage` 任务 done+merged → 调 PM `stage-review`（四维评分 + 建议 → `Notes/Stage-Review.md`）；**防卡死放宽**——剩余任务全部 blocked/closed（无可推进任务）的阶段同样触发评审，PM 给出「继续等待 / 收窄 / 拆出」建议，阶段不会无限静默。
- **阶段目标自动填**：auto-staging 生成阶段块时 `- 目标:` 自动派生（阶段名 + 任务数），PM 可覆盖为可演示成果——占位不退化（P4/P5/P6 空目标教训）。
- **用户决策**：回答 Stage-Review「评审决策: continue / supplement:{建议} / end」→ daemon 分发（继续下一阶段 / 建议写入下一阶段 / 后续阶段任务 close，功能满足即结束）。
- **阶段规模**：由配置 `stage_min_per_phase`/`stage_max_phases` 控制（daemon 分组参数）；PM 仅在新需求到达时评估归入现有阶段或**建议增/拆阶段**（写清单「阶段规划确认」区，用户拍板，不塞进进行中阶段）。

## 依赖卫生与健康诊断（Daemon Health）

每轮 scan 自动执行，防"任务静默饿死/冲突延迟暴露/队列虚胖"：

- **依赖引用校验**：`blocked_by`/REQ `depends_on` 引用不存在的任务 → 日志 + 一次性通知（引用写错 = 依赖永不满足 = 下游永久等待且无信号）；**目标文件存在但 frontmatter 暂解析失败（OMP 会话写回瞬时窗口，如重复 YAML 键）→ 只记 deferring 日志跳过本轮，下一轮自动重查，不误报**；closed 上游按 `closure_reason` 判定：`already-implemented` 视为已交付，`duplicate` 通过 `replacement_task` 解析，均不报警/不阻塞；仅 `cancelled`/`wont-fix`/`not-bet`/空原因等无交付关闭才对非终态下游发一次性「依赖永不满足」通知；done/closed 下游的历史引用不诊断（legacy 噪音）。
- **依赖链自动恢复**（`resolveBlockedDependencies`）：**任一非终态任务**（blocked/ready/refining/planning/implementing/review 等）的 `blocked_by` 上游若为阶段失败 blocked（MODEL_FAILED/PHASE_TIMEOUT/PHASE_INTERRUPTED/空错误码）→ 自动 `resume_approved=true`（上限 2 次、防循环）；此前只扫描 blocked 下游，refining/ready 下游的阻塞上游无人解析（TASK-019 教训）。前置门禁（`PREREQUISITE_SMOKE_FAILED`）仅对 blocked 任务按事实变化恢复。
- **计划文件重叠预警**：同项目并发 implementing 任务的 `plan_files`（Round 1 写回）重叠 → 一次性通知——把合并冲突信号从 merge 阶段前置到调度阶段。
- **项目健康诊断**：每轮输出 in-flight / stage 空 / merged-未收口 计数；超阈值（每日一次）通知——`merged 未收口 ≥5 且 in-flight ≥20` 提示跑 `project-rebaseline`；`stage 空 ≥5` 提示 `otg stage-plan init`；in-progress 阶段任务 >8 提示拆阶段。
- **任务自动收口**（D4）：`merge_status=merged` + 非 done/closed + 无 `pending_req` 的任务自动转 `done`（PR 合入是确定性证据；pending_req 增量任务不误收口）+ 通知 + Roadmap 里程碑。
- **知识提炼自动补救**（D5）：`knowledge_extracted` 标记仅在提炼**全成功**时写入；失败写 `knowledge_extract_error` + 通知（「知识提炼失败，自动重试中」）。每轮 scan 对 `done`+`merged`+未提炼+无 `pending_req` 的任务自动重新提炼——覆盖 daemon 强杀/异常退出截断提炼 goroutine 与部分失败场景（此前静默永久丢失）；提炼 goroutine 计入 `activeTasks`，优雅停机等待落地。
- **决策归档兜底**（D3）：主决策清单 >50KB 且未答 ≤3 时，daemon 确定性归档已答决策点至 `Grilling-Decisions-archive.md`（PM Step 4.5 是主路径，此为无会话兜底，主清单永不膨胀）。
- **阶段状态 daemon 翻转**（D2）：用户填「评审决策:」后，daemon 在 PM 分发**前**确定性翻转 Stage-Plan 状态机（continue→delivered+下阶段 in-progress/completed；supplement→+补充行；end→后续阶段 ended+任务 close）；PM 会话只做 REQ 标注与知识沉淀。

## Roadmap（里程碑路线图）

`Notes/Roadmap.md`：项目发展历史总览（里程碑时间线 + 当前状态），**daemon 在交付事件点确定性追加**（阶段评审触发/阶段决策/任务自动收口/决策归档，幂等按日期+标题），PM 在阶段评审/阶段化时补充语义。用户可随时查看项目走到哪、经历过什么；与 `Stage-Plan.md`（前瞻规划）互补。细化阶段产物（路线图/领域索引类 REQ）归档见 `Notes/legacy-requirements.md`。

## OnReqChanged（需求变更联动）
- blocked：保持 blocked，pending_req=true。
- ready：保持 ready，pending_req=true。
- refining/planning：只设 pending_req，不中断 live phase。
- needs-grilling + active owner：只设 pending_req，不清 owner、不重开 Kitty。
- plan-review：撤销批准，转 refining。
- implementing：当前 AC 后 checkpoint → refining。
- review/conflict：清 Merge 授权，转 refining。
- done：按 REQ 变更类型路由——breaking/未标注：清 Merge 授权转 refining + 代际重置（reopen_count+1，清 target_branch/pr_url/merge_status/completed/knowledge_extracted，round2 完成后写新分支/新 PR）；additive：保持终态，通知「建议新建 TASK 承接增量或手动重开」；cosmetic：忽略。类型取 REQ 最新一条变更记录 `> 变更类型:` 行（修改者保存前写入）。
- **已吸收变更去重**：任务 `refine_req_hash` 已等于 REQ 当前内容 hash 时跳过处理——refining/PM 写回自身的审计记录不重复打回、不重复通知。
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
- `stage_source`：阶段来源标记——`req`（REQ 继承，跟随 REQ stage 变更）、空（daemon 自动分组 / PM 手动分配，不跟随）。PM 手动改 TASK stage 时必须清空 `stage_source`（`otg update-status stage=... stage_source=`）。
- `plan_files`：Round 1 计划产出的将修改文件清单（repo 相对路径），daemon 用于同项目并行实现的文件重叠预警。
- `reopen_count`：交付轮次（daemon 维护）——done 任务因 breaking 需求变更重开时 +1；0 = 首次交付。第二次 merge 后任务仍为 1，标识已二次交付（审计用）。
- `default_assignee`（vault-map.json 顶层）：新 REQ 自动创建 TASK 时预写 `assignee` 为指定 models key（如 `"default"`），任务直接可调度；**空值/缺省**恢复旧行为（blocked 等人工补 assignee）。

## Documentation（文档）
完整规范和实现验收清单见 `docs/workflow.md`；字段参考见 `reference.md`。

## 知识库 KB v2 格式规范（References/）

知识库文件格式的完整规范（frontmatter 6 字段、摘要前置、目录强制、要点化、噪音零容忍、verified 语义、交互经验归类规则、分类体系）见 `skill://knowledge-base` 的「知识库文件格式 — 强制要求」「分类体系」与「交互经验归类规则」章节——本文件不重复定义，仅在本 Skill 检索/入库时遵循该规范。
