---
name: obsidian-task-runner
description: "Manual entry and reference router for the Obsidian task lifecycle. Daemon (otg-task-watcher.service) runs phase skills via the DSH headless agent-server (dsh-agent-server.service, default dsh-embed executor, free-channel-first model routing) and drives stage-based delivery with per-phase concurrency limits, aged auto-resume fallback, and decision-list pause/reactivate. Trigger: task runner, 自动执行 Obsidian 任务, 阶段化交付, 任务并发."
---

# Obsidian Task Runner — Core Contract

本 Skill 是人工入口和流程参考。Daemon 不通过本 Skill 二次路由，而是按状态直接调用阶段 Skill。

## Required Skills

随包安装（清单见 `skills/manifest`）：

- `skill://obsidian-task-runner-refining`
- `skill://obsidian-task-runner-round1`
- `skill://obsidian-task-runner-round2`
- `skill://obsidian-task-runner-merge`
- `skill://obsidian-task-runner-conventions`（团队项目规范审查门禁）
- `skill://obsidian-task-runner-priority`
- `skill://obsidian-task-runner-pm`
- `skill://obsidian-task-runner-split`
- `skill://obsidian-task-runner-design`

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
| -------- | ------ |
| `blocked` | 五类：① 缺字段/依赖——补齐后自动 `ready`/`plan-review`；② 阶段失败——`resume_approved=true` 后恢复 `blocked_phase`，**但可恢复错误码（MODEL_FAILED/PHASE_TIMEOUT/MODEL_QUOTA_EXHAUSTED/PHASE_INTERRUPTED 或空码）的阶段失败子集若 `pending_req=true`（REQ 已变更）则 daemon 自动转 `refining` 重细化（`recoverBlockedPendingReq`）而不复用旧 phase**——手动 resume 会拿旧需求重新实现，方向错误；③ `API_KEY_UNAVAILABLE`——daemon 每轮探测 key，可用即自动恢复（无需 resume）；④ 人工暂停——`blocked_phase` 非空 + `REQ_MISSING` 等非瞬时错误码，保持阻塞直到手动 resume；⑤ **前置门禁失败**（`PREREQUISITE_SMOKE_FAILED`）——round2 入口门禁（如 AC-066-17）未通过时由 round2 转 blocked，daemon 每轮按 `blocked_by` **事实**（上游 `done` 且 `phase_error_code` 空 = PR 已合入）自动恢复，无用户干预；门禁失败禁止 replan 循环 |
| `ready` | daemon 转 `refining`；**团队项目（`project_type: team`）首个任务先拦截过只读规范审查（`/obsidian-task-runner-conventions`，产物 `Notes/PROJECT-CONVENTIONS.md` 即一次性门禁标记，见「团队项目模式」）**；priority_assessment 由 daemon 在 scan 末尾并行评估（每轮 ≤2），不阻塞调度 |
| `refining` | daemon 直接调用 refining Skill，使用 models.default；大型需求先生成 Wayfinder Map 决策地图（内联规则见 refining 4a，无需外部 skill），作为 Grilling 焦点；failed 项三分类收敛——fact 自修正 REQ、auto 采纳建议留 `auto_accepted` 审计（可推翻）、仅 dispute 进 grilling；重复争议（grill_repeat≥2）park 升级到项目级清单 |
| `needs-refining` | 旧版遗留状态；scan 拾起后自动迁移为 needs-grilling（`nextLocalTransition`），随后走正常 Grilling 路径（Kitty tab、提醒、lease） |
| `needs-grilling` | daemon 检查 owner/timeout并创建 Kitty；pending_req 优先强制 refining，否则 resume 恢复 prev status、replan 转 refining，空值继续等待；支持异步 Grilling（grill_continue）；**清单 `status=paused` 时该项目 grilling 流程整体暂停**——不提醒、不开决策 tab、grill_continue 不重置 refining、PM 不分发/不 consolidate、parked 不解除；恢复靠用户手动改回 `open` 或关联 REQ 更新（daemon 自动激活）；`grill_parked=true` 时**为项目创建「决策清单」Kitty tab**（每项目一个、5min debounce、待答决策点 >0 时）——tab 内 **kitty-grill 光标问卷（批量）**：模型把待答决策点 + 选项 + 推荐结构化输出，用户 `j/k` 选选项、`Enter` 确认、`q` 一轮提交；提交后 **detached 子进程异步写回**（日志 `~/.dsh/logs/kitty-grill/writeback-*.log`，10 分钟超时，spawn 失败回退同步）并 **kitty 自动关闭本 tab**（不再卡「写回中」）；**写回守卫**：启动与写回前复查任务状态，已离开 needs-grilling（closed/done 等）阻止写回；问卷 prompt 以 `任务 TASK-<id>` 开头（agent-server 监控面板按第一个 TASK-xxx 打标签）；答案写回清单后 daemon 按答案 hash 变更**自动分发**（无需手动 grill_continue）；**禁止任何自动转换**（残留 grill_resolution 不得触发 replan——TASK-066 17 轮零收敛的教训）；争议由 PM 统筹（`skill://obsidian-task-runner-pm`）汇总到 Notes/Grilling-Decisions.md |
| `planning` | daemon 直接调用 Round 1 Skill，使用 TASK assignee |
| `plan-review` | auto_approve 默认 true（缺失即 true，模板已写入）→ daemon 自动 `plan_approved=true` 转 implementing；显式 `auto_approve: false` 时等待人工 `plan_approved=true`；关闭必须同时满足 `rework_resolution=close` + `close_approved=true` + 合法 `closure_reason` + 非空 `closure_note`（duplicate 还需 `replacement_task`）。**Grilling 是唯一常规人工关卡** |
| `implementing` | daemon 直接调用 Round 2 Skill；高风险 Step 先跑 Prototype Gate；**空转冷却**：会话完成后仍 implementing 且无 `checkpoint_commit`（入口门禁复验类）→ 指数退避冷却（10m→…→~10.7h 上限）不重派；有进展即重置。不会自动转 closed |
| `review` / `conflict` | pending_req 优先→refining；rework=resume→implementing（仅 `review`；`conflict` 先解合并冲突）；关闭门禁仅对 `review` 生效（conflict 不关闭——先解决合并冲突）；**auto_merge 完成审计门禁**：进入 review 且 `merge_approved=false` 时先跑独立只读审计会话（受限工具面 read/grep/bash，无写工具）逐条 AC 复核原始证据——pass 才自动授权合并；fail 按失败类型分路：`implementation`（代码/测试缺陷）带审计报告转回 implementing 自动修复（round2 加载 diagnosing-bugs 消费 `phase_error`/`audit_log`），连续 `audit.max_fixes` 次仍失败升级为 **grilling 决策**（resume→继续修复重置预算 / replan→refining）；`requirement`（AC 歧义/矛盾/不可验证）直接转 needs-grilling 决策；人工已 `merge_approved=true` 的任务跳过审计。**merge 失败回退（REQ 未变 + 预算未耗尽）同样自动重授权**（`canAutoApproveMerge`：非 `GITHUB_UNAVAILABLE`/`REPO_MISMATCH` 永久缺陷即重试），停机/超时中断保持授权重启自动恢复。AI 修复预算（`merge_retry_count`，上限 `max_auto_merge_fixes`）耗尽后交还用户：① 清计数重授权继续 AI 修复；② replan（review 设 `rework_resolution=replan`；conflict 在 REQ 追加歧义裁决保存→自动转 refining）——详见 `skill://obsidian-task-runner-merge`「预算恢复」；**冲突规模熔断**：sync 冲突文件数超 `max_auto_fix_conflicts`（默认 40）不启动 AI 直接交还，不耗预算；**人工合入自动收口**（`autoCloseMergedConflictPRs`）：交还用户的 conflict/review 任务 PR 被人工合并（MERGED）→ 每任务 5 分钟冷却探测 → 自动转 done（TASK-067 教训）；merge 失败通知走 `notifyFailure` per-task 5 分钟防抖 |
| `done` | REQ 变更按类型路由：`breaking`（含未标注，保守）→ pending_req=true 回 refining，代际重置（reopen_count+1、清 target_branch/pr_url/merge_status/completed/knowledge_extracted，新一轮交付新 PR）；`additive`（纯增量向后兼容）→ 保持 done，通知建议新建 TASK 承接；`cosmetic`（措辞/格式）→ 忽略；`merge_status != merged` 且有 PR/分支（任务 done 但 PR 从未合入）→ 自动重开 `review` 走 merge 闭环（auto_merge 自动授权）；**陈旧终态检测（`detectStaleDoneReopens`，每轮 scan，前置 `merge_status=merged`）**：done + `plan_version≥2` + `checkpoint_commit` 非空且**不是本地 origin/main 祖先**（git merge-base 先 fetch 一次、失败保守）→ 未交付增量被假终态锁死（TASK-018 教训）→ 自动按 breaking 语义重开 refining + 代际重置 + 通知；git 检查不确定（无 repo/ref 缺失）→ 保守不动；否则终态；不会自动转 closed |
| `closed` | 无需交付终态（Bets, Not Backlogs）。仅两条入口：① plan-review/review 的显式关闭门禁（用户批准 + 原因 + 备注）；② Stage-Review 用户决策 `end` 关闭**尚未开始交付**的后续阶段任务。已有计划/分支/PR/checkpoint/merge 状态或处于 planning/implementing/review/conflict 的任务会阻断整次 stage end，禁止自动关闭。closure_reason: not-bet / already-implemented / duplicate / cancelled / wont-fix。不可自动恢复 |

## 团队项目模式（Team Projects）

对**已存在的组织仓库**（如私有 Gitea），在 vault-map.json 手动注册即可。
`git_remote` 指向你实际开发操作的仓库：直接在团队仓库上开发用 `merge_mode:
manual`；**fork 出来开发**（推荐，团队仓库只读、由你手动向团队发 PR）用
`merge_mode: fork-merge`，`git_remote` 指向你的 fork：

```json
{"name": "team-app", "path": "/work/team-app-fork",
 "git_remote": "git@gitea.internal.example.com:yourname/team-app-fork.git",
 "project_type": "team", "merge_mode": "fork-merge"}
```

- **`project_type: team`**：daemon 禁止对该项目自动建仓/自动注册/checkout 提升/`gh repo create`/`remote_create`——仓库归团队所有，操作面只读 + 推送。
- **`merge_mode: manual`**：交付停在**推分支**（`git push` 用仓库自身 SSH/https 凭据，不注入 gh credential helper）；不创建 PR、不轮询 CI、不自动合并。push 后写 `merge_status=pushed`、保持 `review` 并通知「请到仓库 UI 合并」；**daemon 每轮探测远端默认分支（`git ls-remote --symref` + fetch + `merge-base --is-ancestor`，默认分支名不硬编码）**，人工合入后自动转 `done`。完成审计（AC 证据复核）在 push 前照常执行。
- **`merge_mode: fork-merge`**（fork 开发）：自动化推进到**本地 merge 完成**——在任务 worktree 中把 feature 分支 `merge --no-ff` 进 fork 默认分支（默认分支名经 `ls-remote --symref` 解析，不硬编码 main）；**冲突由 AI 会话自动解决**（`merge_retry_count` 预算，与 PR 冲突共享）；merge 成功后用仓库自身凭据 push fork 默认分支，任务自动转 `done` 并通知**「请手动向团队项目提交 PR」**——团队侧 PR/review/合入完全人工，daemon 不接触团队仓库。失败（预算耗尽）转 conflict 交还用户，与 manual 同语义。
- **规范审查门禁**：团队项目**首个任务**在 refining 前必须通过只读规范审查（`/obsidian-task-runner-conventions`，models.default）——汇总项目的设计/代码/注释语言/API 文档/文档/提交规范到 `Notes/PROJECT-CONVENTIONS.md`（**产物文件即一次性标记**，删除可重审）；审查只总结规范、零优化建议、零代码修改。失败转 blocked（`CONVENTIONS_REVIEW_FAILED`），resume 重跑。
- **规范注入**：`PROJECT-CONVENTIONS.md` 随 `[Project Context]` 注入 refining/planning/round2/merge 修复全部会话，**优先级高于全局默认约定**——项目注释用中文就用中文、技术栈按项目既有模式、commit 按项目习惯；不引入项目没有的框架，不做计划外重构（团队 review 认知负担优先）。
- **防误重开**：`detectStaleDoneReopens` 与 done 重开 merge 对团队项目跳过（squash 合入后 checkpoint 不是 main 祖先，但 `merge_status=merged` 由远端探测/本地 merge 完成写入，是权威交付证据）。

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
16. **done 任务仅 breaking（含未标注）变更重开**；additive/cosmetic 不重开已交付终态；重开必须代际重置（reopen_count+1 + 清旧 PR/分支/merge 事实），禁止复用已 MERGED 的旧 PR（会让新交付永远合不进去）。**陈旧终态不例外**：done + `plan_version≥2` + checkpoint 非 origin/main 祖先（未交付增量）→ `detectStaleDoneReopens` 自动按 breaking 重开（TASK-018：外部写回假 done 锁死 v6 增量、下游 TASK-071 依赖门禁饿死）；git 检查不确定时保守不动。
17. **merge AI 修复预算（`merge_retry_count`）仅在 merge 成功或新一轮 planning 完成时清零**；replan 不继承旧交付耗尽；预算耗尽后 review 走 `rework_resolution=replan`、conflict 走 REQ 追加歧义裁决自动转 refining，均无需手动解冲突。
18. **Round 2 无进展完成 MUST 进入冷却**：会话结束后仍 `implementing` 且无 `checkpoint_commit`（入口门禁复验类空转）→ daemon 指数退避冷却（10m→…→~10.7h 上限）内不重派、不通知；**截止时间持久化 `round2_stall_until`（RFC3339）——daemon 重启不清零**（TASK-071 二修：纯内存冷却在频繁重启下每次重启即重派）；`checkpoint_commit` 写入或状态离开 implementing 即重置（重置点 = `recordRound2Completion` 判定：`Status != "implementing" || CheckpointCommit != ""`，同时清 frontmatter 字段）。人工派发不受冷却限制。无进展完成的 implementing 会话不发状态通知（`StatusNotify` 的 implementing 分支仅在 `phase_error_code` 非空时报「实现会话异常 + 原因」，正常完成等待门禁静默）。
19. **auto_merge 完成审计门禁**：`auto_merge: true` 的 review 任务 MUST 通过**独立只读审计**（`audit_status=passed`，受限工具面 read/grep/bash 逐条 AC 复核原始证据）才自动授权合并——实现者不能自证完成；`merge_approved=true`（人工门禁优先）或 `audit.enabled=false` 跳过。fail implementation → 转 implementing 自动修复（`AUDIT_FAILED`，round2 消费 `phase_error`/`audit_log`），连续 `audit.max_fixes`（默认 2）次升级 **grilling 决策**（resume 重置预算 / replan 回 refining）；fail requirement → 直接 needs-grilling，不消耗修复预算。会话失败保持 review + `audit_status=pending` 下一轮重试（进程级失败 2min 冷却防烧 token）；API key 不可用无冷却（key 恢复即重试）。不惩罚实现。
20. **Vault 文档写回 MUST 用结构化 Markdown**（自动化全流程与 Vault 文档强关联，读者是人）：写回 REQ/TASK/清单/评估/计划/记录时——`##`/`###` 标题分层、`| 表格 |` 对齐结构化数据（检查项/决策/AC/维度）、`- ` 分点拆长内容（每条 ≤2 句）、`> ` 引用块标注来源/裁决/时间戳、``` 代码块包裹 YAML/JSON/代码。**禁止把结论/评估/说明写成一段超长纯文本**（不分点、不换行、数十句挤一行）——读者一眼扫不到重点即写回失败，必须重写为分点/表格。
21. **知识库优先（Knowledge-First）**：任何阶段遇到工具报错、命令失败、不确定「怎么用/如何配置/版本/命令/API」、技术障碍时，**第一步先 `otg kb search "<关键词>"` 查本地知识库（`References/`）**，命中才继续（`read` 原文 + 引用路径），未命中才 web_search / Context7。**踩坑不重蹈**：调试报错先搜「现象/错误关键词」——知识库的踩坑记录（现象 + 失败方案 + 根因 + 成功方案）就是上一轮的正确答案，不要重新试错。**引用而非转述**：计划/实现引用知识库写文件路径，不凭记忆转述。

## 并发上限（Concurrency）

并发语义的权威定义（代码实现 = `internal/daemon/implementation_gate.go`；其余阶段 = `phase_gate.go`）：

- **implementing / Round 2**：`max_concurrent_tasks_per_project`（每项目上限，默认 `2`，缺失/`0` 回落默认）——N 个项目最多并行 N×2 个实现会话，一个项目的满负荷不会饿死其它项目；`max_concurrent_tasks` 为可选**全局总封顶**（`0` = 不限，默认 `0`），两上限同时生效、取更严格者。**旧配置仅含 `max_concurrent_tasks: 2` 时行为不变**（等效全局封顶 2 + 每项目 2）。
- **其它阶段**：`phase_concurrency` 按阶段限并发（默认 `refining: 3 / planning: 2 / merge: 1 / priority: 1 / pm: 1 / audit: 1`；key 置 `0` 或删除 = 不限），防止一轮 scan 同时拉起 20+ 个 dsh 会话烧 token、触发 API 限速与本地资源抢占。**实际消费点**：`refining`/`planning`/`priority`/`audit` 由 `phaseGateKey` 映射生效；`merge` 槽位当前不可达（review/conflict+merge_approved 在到达门禁段前已进入 merge 分支提前 `continue`）、`pm` 槽位无获取点（PM 由 `grilling_consolidation_batch` 默认 1 + `pmInFlight` 按目标去重约束）——这两个 key 置 `0`/调大均无效果，属代码追赶项。
- **daemon 重启存活会话**：dsh-embed 无 PID 文件，daemon 重启后按 frontmatter 状态重派发（round2 有 checkpoint 复用，从断点继续）；`executor_session_id` 持久化支持 durable resume——resume 成功复用会话；**超时/中断如实上报**（PHASE_TIMEOUT / PHASE_INTERRUPTED，不 fresh start：daemon 侧 HTTP 超时不终止 agent-server 会话，fresh start 会造成同任务双会话并行写，TASK-058 教训）；仅终态失败（session not found 等）回退 fresh start；resume 等待超时对齐阶段 spec（不再硬编码 30m）。
- 修改配置后重启 daemon 生效。

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
- **阶段顺序调度**：daemon 对 ready 任务按 **项目内 stage 升序**（数字序，`P10` 在 `P2` 后）→ priority → created 排序拾取——低阶段任务优先消耗实现容量，P1 未收敛前 P2+ 任务不抢容量；**跨项目不做 stage 比较**（各项目阶段独立，按 priority → created → project 排序），未分阶段任务排最后（当轮即被 auto-staging 归组）。阶段只用于「顺序调度 + 完成后评审」，不阻止后阶段任务提前进入 refining/planning——依赖先后由 `blocked_by` 表达（release-manager 教训：无依赖声明的并发实现产生 57/253 冲突合并与 11 次 v2/v3 返工）。
- **阶段完成**：daemon 检测某 in-progress 阶段全部 `stage` 任务 done+merged → 调 PM `stage-review`（四维评分 + 建议 → `Notes/Stage-Review.md`）；**防卡死放宽**——剩余任务全部 blocked/closed（无可推进任务）的阶段同样触发评审，PM 给出「继续等待 / 收窄 / 拆出」建议，阶段不会无限静默。
- **阶段目标自动填**：auto-staging 生成阶段块时 `- 目标:` 自动派生（阶段名 + 任务数），PM 可覆盖为可演示成果——占位不退化（P4/P5/P6 空目标教训）。
- **用户决策**：回答 Stage-Review「评审决策: continue / supplement:{建议} / end」→ daemon 分发（继续下一阶段 / 建议写入下一阶段 / 后续阶段任务 close，功能满足即结束）。
- **阶段规模**：由配置 `stage_min_per_phase`/`stage_max_phases` 控制（daemon 分组参数）；PM 仅在新需求到达时评估归入现有阶段或**建议增/拆阶段**（写清单「阶段规划确认」区，用户拍板，不塞进进行中阶段）。

## 依赖卫生与健康诊断（Daemon Health）

每轮 scan 自动执行，防"任务静默饿死/冲突延迟暴露/队列虚胖"：

- **依赖引用校验**：`blocked_by` 引用不存在的任务 → 日志 + 一次性通知（引用写错 = 依赖永不满足 = 下游永久等待且无信号）；**目标文件存在但 frontmatter 暂解析失败（dsh 会话写回瞬时窗口，如重复 YAML 键）→ 只记 deferring 日志跳过本轮，下一轮自动重查，不误报**；closed 上游按 `closure_reason` 判定：`already-implemented` 视为已交付，`duplicate` 通过 `replacement_task` 解析，均不报警/不阻塞；仅 `cancelled`/`wont-fix`/`not-bet`/空原因等无交付关闭才对非终态下游发一次性「依赖永不满足」通知；done/closed 下游的历史引用不诊断（legacy 噪音）。**上游长期未完成提醒（`upstream_stall_days`，默认 3）**：`blocked_by` 上游非终态且 `updated` 距今超阈值 → `diagNotified` 每进程一次通知（TASK-067：019/057/066/069 静默阻塞一个多月无信号，直到用户被动发现）。
- **依赖链自动恢复**（`resolveBlockedDependencies`）：**任一非终态任务**（blocked/ready/refining/planning/implementing/review 等）的 `blocked_by` 上游若为阶段失败 blocked（MODEL_FAILED/PHASE_TIMEOUT/MODEL_QUOTA_EXHAUSTED/PHASE_INTERRUPTED；空错误码且上游自身无 `blocked_by` 的 legacy 阶段失败）→ 自动 `resume_approved=true`（上限 2 次、防循环）；**空错误码 + 上游自身 `blocked_by` 非空的 blocked 是入口门禁形态**（round2 写回丢码），不自动恢复——scan 先由 `fixBlockedGateErrorCodes` 补记 `PREREQUISITE_SMOKE_FAILED` 归入门禁事实恢复分支（TASK-019 8/11：空码 blocked 被误恢复成 completed→blocked→resume 死循环，10+ 轮烧 token）。此前只扫描 blocked 下游，refining/ready 下游的阻塞上游无人解析（TASK-019 教训）。前置门禁（`PREREQUISITE_SMOKE_FAILED`）仅对 blocked 任务按事实变化恢复。
- **24h 老化兜底恢复**（`autoResumeAgedBlocks`，2026-08 新增；窗口可配置 `auto_resume_aged_after_hours`，默认 24）：`status=blocked` 且错误码可自动恢复（MODEL_FAILED/QUOTA/PHASE_TIMEOUT/PHASE_INTERRUPTED/空码，以及 DESIGN_SESSION_FAILED）且阻断超过配置窗口（基线 `blocked_at`，缺失回退 `updated`）且 `auto_resume_count < 2` → 每轮 scan 自动 `resume_approved=true`。覆盖两类依赖链覆盖不到的场景：daemon 迭代/重启丢失恢复状态、`blocks=[]` 叶子任务无下游拉起（TASK-015/065 教训）。进入 blocked 统一盖 `blocked_at` 时间戳，恢复时清空；人为决策块（REQ_MISSING/DOCUMENT_INVALID/API_KEY_UNAVAILABLE/PREREQUISITE_SMOKE_FAILED 门禁）不按年龄恢复。

- **计划文件重叠自动串行**：同 repo 并发 implementing 任务的 `plan_files`（Round 1 写回）重叠时，调度器**自动延迟派发**排序靠后的任务（按项目内 stage → priority → created），待前序任务实现会话结束（状态离开 implementing 即释放重叠，不跨 merge 生命周期）后自动继续——把合并冲突从 merge 阶段前置消除；等待受 `max_overlap_wait_minutes`（默认 720，大于 round2 空转冷却上限）约束，超限放行并发、merge 冲突走既有兜底，防上游卡死饿死下游；另发一次性通知告知已自动串行（无 `plan_files` 信息的任务跳过重叠检查，正常并发派发）。
- **项目健康诊断**：每轮输出 in-flight / stage 空 / merged-未收口 计数；超阈值（每日一次）通知——`merged 未收口 ≥5 且 in-flight ≥20` 提示跑 `project-rebaseline`；`stage 空 ≥5` 提示 `otg stage-plan init`；in-progress 阶段任务 >8 提示拆阶段。
- **任务自动收口**（D4）：`merge_status=merged` + 非 done/closed + 无 `pending_req` + `plan_version<2` 的任务自动转 `done`（PR 合入是确定性证据；pending_req 增量任务与 plan_version≥2 的增量 replan 任务不误收口）+ 通知 + Roadmap 里程碑。**反向防锁**（`detectStaleDoneReopens`）：done + `merge_status=merged` + `plan_version≥2` + checkpoint 非 origin/main 祖先 → 未交付增量被假终态锁死（TASK-018 教训）→ 自动重开 refining + 代际重置 + 通知；git 检查不确定保守不动。
- **知识提炼自动补救**（D5）：`knowledge_extracted` 标记仅在提炼**全成功**时写入（= ADR/踩坑落盘 **且** 检索库 store 同步成功；同步失败重置 marker=false）；失败写 `knowledge_extract_error` + 通知（「知识提炼失败，自动重试中」）。每轮 scan 对 `done`+`merged`+未提炼+无 `pending_req` 的任务自动重新提炼——覆盖 daemon 强杀/异常退出截断提炼 goroutine、部分失败与 store 同步失败场景（此前静默永久丢失）；提炼 goroutine 计入 `activeTasks`，优雅停机等待落地。`adr_written` 逗号串+`Notes/adr/` 前缀在匹配前归一化为裸 ADR id（否则扫描 N 提取 0）。
- **决策归档兜底**（D3）：主决策清单 >50KB 且未答 ≤3 时，daemon 确定性归档已答决策点至 `Grilling-Decisions-archive.md`（PM Step 4.5 是主路径，此为无会话兜底，主清单永不膨胀）。
- **阶段状态 daemon 翻转**（D2）：用户填「评审决策:」后，daemon 在 PM 分发**前**确定性翻转 Stage-Plan 状态机（continue→delivered+下阶段 in-progress/completed；supplement→+补充行；end→后续阶段 ended+任务 close）；PM 会话只做 REQ 标注与知识沉淀。

## Roadmap（里程碑路线图）

`Notes/Roadmap.md`：项目发展历史总览（里程碑时间线 + 当前状态），**daemon 在交付事件点确定性追加**（阶段评审触发/阶段决策/任务自动收口/决策归档，幂等按日期+标题），PM 在阶段评审/阶段化时补充语义。用户可随时查看项目走到哪、经历过什么；与 `Stage-Plan.md`（前瞻规划）互补。细化阶段产物（路线图/领域索引类 REQ）归档见 `Notes/legacy-requirements.md`。

## OnReqChanged（需求变更联动）

- blocked：保持 blocked，pending_req=true；**阶段失败子集（blocked_phase 非空 + 可恢复错误码）由 daemon 每轮 scan 自动转 refining（`recoverBlockedPendingReq`）**，排除 `PREREQUISITE_SMOKE_FAILED` 门禁、`REQ_MISSING` 等非瞬时码、空错误码 + `blocked_by` 非空的入口门禁形态。
- ready：保持 ready，pending_req=true。
- refining/planning：只设 pending_req，不中断 live phase。
- needs-grilling + active owner：只设 pending_req，不清 owner、不重开 Kitty。
- plan-review：撤销批准，转 refining。
- implementing：当前 AC 后 checkpoint → refining。
- review/conflict：清 Merge 授权，转 refining。
- done：按 REQ 变更类型路由——breaking/未标注：清 Merge 授权转 refining + 代际重置（reopen_count+1，清 target_branch/pr_url/merge_status/completed/knowledge_extracted，round2 完成后写新分支/新 PR）；additive：保持终态，通知「建议新建 TASK 承接增量或手动重开」；cosmetic：忽略。类型取 REQ 最新一条变更记录 `> 变更类型:` 行（修改者保存前写入）。
- **已吸收变更去重**：任务 `refine_req_hash` 已等于 REQ 当前内容 hash 时跳过处理——refining/PM 写回自身的审计记录不重复打回、不重复通知。**例外（TASK-018）**：任务处于陈旧终态（done + `plan_version≥2` + checkpoint 非空）时不跳过——吸收会锁死未交付增量，改走 done 分支（breaking 重开 / additive 保持终态并提示 / cosmetic 忽略）。
- 新自动创建 TASK：pending_req=false。

## Daemon 重启与中断恢复

- daemon 收到 SIGTERM（`systemctl stop`、`otg install`、重启）时优雅停机：长驻 agent-server（`dsh --profile headless-agent-server`）由 daemon 的 `stopAgentServer` SIGTERM（10 秒内未退出则 SIGKILL）；被中断的 dsh-embed 会话把 `executor_session_id` 持久化到 frontmatter（interrupted 时写回），停机期间不启动 fallback。
- 被中断的 phase **不视为失败**：任务保持原状态（`refining`/`planning`/`implementing`），写入 `phase_error_code=PHASE_INTERRUPTED` 标记；daemon 重启后下一轮 scan 自动重新调度——无 `blocked`、无手动 `resume_approved`。
- 阶段成功后自动清理 `PHASE_INTERRUPTED` 标记（`clearPhaseError`）。
- `otg install` 的 stopDaemon 阻塞等待 systemd 优雅停机完成后再安装，不与新实例竞态。
- 依赖链自动恢复（`resolveBlockedDependencies`）识别 `PHASE_INTERRUPTED` 为可恢复错误码（同 `MODEL_FAILED`/`PHASE_TIMEOUT`）。

## Notifications（通知）

- `notifications.desktop` 只控制 notify-send。
- Kitty Grilling tab 始终尝试创建，不受 desktop 开关控制。
- 同一 TASK 只允许一个活跃 Grilling tab；创建前按 task ID 检查 Kitty tab/window title，并以 per-task flock + debounce 防止并发和重启重复创建。
- Kitty JSON 无法解析时不创建 tab，保留 notify-send fallback；Kitty 不可用时保持 needs-grilling 并周期重试。
- **失败/切换通知按任务 5 分钟防抖**（`notifyFailure`）：⏰执行超时 / 💥进程异常 / 💰Token 不足 / 🔄模型切换 / ❌全部失败 / 🚫阶段失败——同级/低级别窗口内抑制，更高级别事件（❌全部失败 / 🚫阶段阻塞）升级后再发，保证终态必达（一个失败事件链最多 2 条：🔄+❌ 或 ⏰+🚫）；有 fallback 时失败原因并入切换通知单条发出。API key 故障走全局 5 分钟防抖（`notifyKeyUnavailable`）。**需求变更通知按 taskID+action 5 分钟防抖**（`notifyReqChanged`）：grilling 写回多次改写 REQ，每轮 on-req-changed 重复发同一条「需求变更」toast（TASK-058 观测：对齐后连续多条）——同一任务同一 action 窗口内只发第一条。

## Fallback Model（兜底模型）

模型兜底统一由 DSH 的 fallback.mjs 插件处理（配置在 `headless` / `headless-agent-server` 的 `cordis.patch.yml`，**不在** `~/.dsh/cordis.patch.yml`）：

- **进程内跨模型降级**：magic 免费 deepseek 失败 / 配额耗尽 → 自动切 magic 免费 openai gpt-5.6（`deepseek-v4-pro → gpt-5.6-terra` / `gpt-5.4-mini → gpt-5.6-luna`）。
- **失败码白名单**（SERVER / RATE_LIMIT / TIMEOUT / QUOTA / EMPTY_RESPONSE 等）触发切换；HTTP 5xx 也触发。
- daemon 侧无 fallback 层——OMP 时代的 `fallback_models` / `watchEmptyStops` 已随 OMP 移除。
- **不要**把 fallback 加回 home 级 `~/.dsh/cordis.patch.yml`：dsh web / dsh-tui 交互会话应失败即返回，不在免费渠道间循环切换。

## Frontmatter 字段规范

TASK frontmatter 有**规范字段序**（`pkg/yamlfrontmatter/frontmatter.go` 的 `taskFieldOrder`）：用户关注的字段（身份、priority、Gate、推荐 metadata）在前，daemon 维护字段在后。daemon 每轮 scan 自动 Normalize（补齐缺失字段 + 按规范序重排，不覆盖已有值）；模板与 snippet 必须与规范序一致，避免新任务文档被反复改写。**REQ frontmatter 同样每轮 Normalize**（`reqFieldOrder`，`syncReqSchemaDefaults`）：旧 REQ 自动补齐演进新增的稳定字段（created/updated/tags），必填身份字段（id/title/project_id）与可选决策字段（priority/stage/depends_on/project/...）**不伪造**——缺失时走系统兜底（auto-staging / priority 评估 / resolveProjectField），避免字段缺失静默导致依赖继承断裂或任务停止自动化（迭代快 + 旧文档重拾场景）。**REQ Normalize 写回仅补 frontmatter 元数据（tags/created/updated/字段序），不改需求实质——写回后同步刷新关联任务的 `refine_req_hash`/`plan_req_hash`**（仅 hash 匹配写回前字节的任务；更旧的 hash 是真实未吸收变更，保留），否则 `OnReqChanged` 把 daemon 自己的 Normalize 误判为需求变更而批量重开任务（2026-08-12：一次 backfill 重开 19 个任务，含 15 个已 done 的代际重置）。**性能**：mtime+size 短路（`normCache`）跳过未变文档（万级时后续轮仅 stat），写回去 fsync（幂等修复可重放，`Update` 事务写保持 fsync）。

- **弃用字段**：`switch_settings`（迁移专用，新代码/新文档必须用 `assignee`）、REQ 的 `domain`/`parent_req`/`task_size`（不再被解析）。模板与文档不得再写入；存量文档由 `otg migrate-tasks <path> --write` 或手工清理。
- `stage` 字段是阶段归属的**权威判定**（TASK 从 REQ 继承，PM 拆分落地时写入），与 `Notes/Stage-Plan.md` 的 `### Phase N:` 块对应。
- `stage_source`：阶段来源标记——`req`（REQ 继承，跟随 REQ stage 变更）、空（daemon 自动分组 / PM 手动分配，不跟随）。PM 手动改 TASK stage 时必须清空 `stage_source`（`otg update-status stage=... stage_source=`）。
- `plan_files`：Round 1 计划产出的将修改文件清单（repo 相对路径），daemon 用于同 repo 并行实现的文件重叠自动串行（`max_overlap_wait_minutes` 上限）。
- `reopen_count`：交付轮次（daemon 维护）——done 任务因 breaking 需求变更重开时 +1；0 = 首次交付。第二次 merge 后任务仍为 1，标识已二次交付（审计用）。**陈旧终态检测重开同样 +1**（`detectStaleDoneReopens`，TASK-018）。
- `default_assignee`（vault-map.json 顶层）：新 REQ 自动创建 TASK 时预写 `assignee` 为指定 models key（如 `"default"`），任务直接可调度；**空值/缺省**恢复旧行为（blocked 等人工补 assignee）。

## Documentation（文档）

完整规范和实现验收清单见仓库 `docs/workflow.md`（开发者文档，不随技能包安装）；字段参考见 `reference.md`。

## 知识库 KB v2 格式规范（References/）

知识库文件格式的完整规范（frontmatter 6 字段、摘要前置、目录强制、要点化、噪音零容忍、verified 语义、交互经验归类规则、分类体系）见 `skill://knowledge-base` 的「知识库文件格式 — 强制要求」「分类体系」与「交互经验归类规则」章节——本文件不重复定义，仅在本 Skill 检索/入库时遵循该规范。

## 知识库检索与问答（能力入口）

- **自动化主路径（agent）**：`otg kb search`（BM25 + 可选 embedding 混合，语义命中优先）→ `read` 原文 → 引用路径；未命中才 web_search/Context7。检索链路与 skill 指令见 `skill://knowledge-base` Step 1（§12 知识流细节见仓库 `docs/workflow.md`）。
- **交互问答（人类/会话入口）**：`otg kb ask "<问题>"`（vault-map 配 `kb_chat`）——混合检索 + chat 流式生成，附确定性「参考资料」列表；`kb_rerank` 可选 cross-encoder 精排。**定位边界**：ask 用于用户提问与交互会话，agent 计划引用禁止用 ask 替代原文检索（转述有信息损耗）。
- **配置**：`kb_embedding`（后端/模型/混合权重/chunk 截断/批量/KNN 候选）、`kb_rerank`（精排）、`kb_chat`（生成）——字段说明与部署见 README「知识库语义检索」「检索精排」「知识库问答」及 `obsidian-task-runner/config/vault-map.example.json`。
