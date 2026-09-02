---
name: obsidian-task-runner-round2
description: "Implementation phase: execute an approved plan AC by AC in a task worktree, checkpoint safely on pending requirement changes, and finish in review."
hide: true
disable-model-invocation: true
---

**Role**: Round 2 Implementation Engine. You execute approved plans AC by AC using Tracer Bullet in a task worktree.

## Knowledge Refs Application（按知识引用清单应用）

读取 TASK frontmatter `knowledge_refs`（Round 1 写入的引用的知识文档清单）：

1. 逐条 `read` 对应 References 文档（`{vault}/References/{ref}`），提取与当前 Step 相关的约束/已验证实践。
2. 实现过程中**显式应用**：在实现记录或代码注释中标注来源（如 `// per core/go/connect-rpc.md: ...`）。
3. 引用清单中的文档与计划 Step 冲突时，以 ADR 为准并记录冲突（走 `Implementation Blockers`）。
4. 发现清单文档过时/错误 → 按 `skill://knowledge-base` 自动纠正流程追加纠错标注。

## Pre-flight Checks（前置检查）

1. TASK `status: implementing`，`plan_approved=true`。
2. `pending_req=false` 才能开始新的 AC。
3. blocked_by 全部满足。
4. **Worktree 由 daemon 统一托管，会话禁止自建**：daemon 已在 `<仓库父目录>/.otg-worktrees/<repoHash>/TASK-<taskKey>`（或 `worktree_base` 覆盖根）为任务建好 worktree 并 checkout `target_branch`；会话只核对「当前 worktree/branch == `target_branch`」，**禁止 `git worktree add`（尤其禁止在仓库同级建 `release-manager-tNNN` 式目录）**——分支只由首次 planning 写回 `target_branch: task/{id}-{slug}` 时创建，会话自己 checkout 分支即可。发现 worktree 缺失/分支不符 → 写回错误交给 daemon 重建，不要手工建树（TASK-082/083 教训：会话自建同级 worktree 后 daemon 只能按安全边界复用，托管根外目录永不自动回收）。
5. 读取已批准计划和 checkpoint 复用策略。
6. **加载 `skill://knowledge-base`**：按计划中的技术栈检索知识库 core/ 文档，引用已验证的最佳实践和版本约束。实现过程中发现的踩坑经验，在 Commit 或 ADR 写回后追加到对应的 References 文件。

## Project Conventions Alignment（项目规范与架构约束对齐，强制）

实现前读取 `{vault}/Projects/{project}/Notes/PROJECT-CONVENTIONS.md`（存在时）——
它是该项目的基线（规范 **+ 架构约束**），**优先级高于本 Skill 与全局 AGENTS.md 的默认约定**：

- **注释语言**：项目用中文注释就用中文、英文就英文（即使全局约定是英文）——注释语言是项目规范的一部分，不是偏好。
- **代码风格**：命名、错误处理、包结构、测试风格按项目既有模式；不引入项目没有的框架/模式。
- **提交信息**：commit 语言与格式对齐项目 `git log` 习惯（feat/fix/chore/中文描述…）。
- **文档/API 文档**：新增/修改的文档按项目的文档规范与 API 文档形式。
- **架构约束强制对齐**：`## 架构约束` 节是硬约束。凡实现涉及数据模型/新字段/新表/迁移/环境配置：
  - **以项目 test/prod 实际数据库引擎为准**（如 test/prod=MySQL），不得用 dev 的 SQLite 语义实现——字段名结尾（`_at`/`_id` 等）、类型（`DATETIME(6)`/自增）、方言差异必须与 `## 架构约束` 一致；多引擎项目必须双引擎兼容并**在 test/prod 引擎上验证**（不只 dev 冒烟）。
  - 迁移按项目既有机制（AutoMigrate/alembic/手写 SQL…）落库，新迁移必须在 test/prod 引擎可执行。
  - 基线未覆盖处按常识 + 最小变更执行，保持与项目现状一致。
- **最小变更**：只按计划实现 AC；不顺手重构、不格式化项目没要求格式化的区域、不新增多余抽象——已有项目 review 认知负担优先。
- 项目没有 PROJECT-CONVENTIONS.md 时按常识 + 最小变更执行，并保持与项目现状一致。

## Environment Cleanup（环境清理与资源回收，强制）

每个会话退出前（含失败、超时、中断）必须完成环境清理，防止污染宿主机与后续任务：

1. **删除本任务创建的一切临时资源**：k3d 集群（`k3d cluster delete`）、docker 容器/网络（`docker rm -f`）、临时 kubeconfig/凭据、冒烟日志与构建产物。
2. **优先调用项目自带清理入口**：repo 存在 Makefile 清理目标（`make dev-down` / `dev-purge` / `k3d-clean` 等）时必须使用，禁止只杀进程不删资源。
3. **严禁触碰非本任务资源（红线）**：不得停止/删除用户常驻服务（kb-reranker、ollama-sycl、桌面/IDE 进程等）或其它任务的资源。内存/端口门禁不通过时记录阻塞并退出，禁止以停用户服务换取门禁通过。
4. **保留资源必须显式声明**：确需留给下游任务的环境，在任务文档写明下游任务 ID 与保留清单（ownership manifest）；下游任务结束时必须清理。
5. **清理证据写入阶段记录**：收尾输出 `k3d cluster list` / `docker ps` 快照与清理命令结果；清理失败必须列出残留清单（residual manifest）并如实上报，不得静默。

## Prototype Gate（高风险 Step 的前置验证）

若已批准计划包含 `## Prototype 建议` section，在执行任何标记为 `risk: high` 的 Step 之前：

1. 读取计划的 Prototype 建议，提取 PASS/FAIL 条件。
2. 加载 `skill://prototype`，在 task worktree 中创建 throwaway 原型。
3. 严格按 PASS/FAIL 条件评估原型结果。
4. **原型 PASS** → 继续 Tracer Bullet 实现该 Step，在 `## 实现记录` 中标注 `✅ Prototype validated`。
5. **原型 FAIL** → 不进入实现。写 `## Round 2 阻塞` + 结构化 grill_context，包含：
   - 原型代码路径和运行输出
   - 违反的具体 FAIL 条件
   - 建议的替代方案
   - 保存 `grill_prev_status=implementing`，转 `needs-grilling`

这样 Grilling 不再是"你觉得应该怎么设计？"而是"原型验证了 X 不可行（证据见 /path），建议采用 Y。你同意吗？"——一轮定案。

> **Gate 验证器是持久验收资产（throwaway 例外）**：当本任务的入口门禁验证器（如 `web/prototype/*-gate.ts`）同时是其他任务的验收标准时，它**不是 throwaway**——必须随本任务实现提交进分支（合入 main），以 main 版本为唯一权威。门禁恢复后重跑必须使用 main 版本（先 `git fetch` + `git merge --ff-only upstream/main`），不得依赖 worktree 里未提交的旧副本。教训（2026-08-22 TASK-058/079）：验证器留在 worktree 未提交，承接任务无法发现、恢复后 worktree 重建即丢失。

## Tracer Bullet（逐AC推进）

每条 AC 独立执行，Red/Green/Refactor 标准参照 `skill://tdd`：

1. **测试打在计划声明的 Seam**：读取当前 Step 的「测试 Seam」行——测试必须通过该层公共接口表达行为（HTTP handler / Repository 接口 / 纯函数层），禁止测内部实现、私有方法、实现耦合的 mock。**计划未声明 Seam 行（旧版本计划）时，默认取该 Step 的最高公共接口**（最接近用户/调用方的可观察层）。
2. Red：最小失败测试。
3. Green：刚好足够的实现。
4. Refactor：只在 Green 后。
5. 记录实现和测试证据。
6. **AC 完成后重新读取 TASK frontmatter。**

**计划外 Seam = 架构信号**：若某个行为无法通过计划声明的 seam 测试（必须测内部、必须新增 mock 边界、或 seam 本身放错层），不要静默绕过——按 `Implementation Blockers` 走 grilling，附证据（测什么测不到、为什么）。计划外 seam 往往意味着 Step 边界或接口设计需要修正（回归 Round 1 的 design-it-twice 对比）。

### Incremental Verification（写入即反馈）

每条 AC 的 Green 落地后**立即**运行受影响包的测试与静态检查（Go: `go vet ./...` + `go test -race ./<受影响包>`），而不是等全部 AC 完成后一次性跑——错误在产生时暴露，避免错误累积到会话尾部再集中修复（失败重派率与审计打回率的主要来源）。全部 AC 完成后再跑全量（见 Completion Checklist）。

### Failure Scenario Verification（失败场景验证，强制）

实现 AC 时**禁止只验证主成功路径**——"测试全绿、生产爆炸"是验收打回与返工的头号来源（大量 bug 只在某一种失败场景或某一种环境才暴露）。每条涉及**可失败路径**的 AC 必须补负向测试，按失败场景矩阵逐项过：

1. **逐项构造失败场景**（按相关类别，不必全做）：
   - 权限/认证：401/403、无权限调用、token 失效。
   - 并发/排队/竞态：信号量耗尽、槽位释放、冲突写回、重试预算耗尽。**用 `-count=3`（或更多）重跑**，单次通过不算证据——flaky 必须抓到根因修复，禁止"重试到绿"掩盖。
   - 吊销/删除/幂等：删除后重放、幂等拒绝（409）、已成功重复提交。
   - 非法输入：校验失败、空值、超长、重复。
   - 序列化/契约形状：`null` vs `[]`、字段缺失、时间戳/枚举边界。
   - 跨引擎方言：dev 用 SQLite 但 test/prod 用 MySQL 时，存储/迁移必须**在 prod 引擎实测**（如本地 MySQL 8.4 严格模式，核对 sql_mode/零日期/TEXT 默认值/字符集）。
   - 失败被容错掩盖：脚本/代码对错误"打印后继续"产生假成功——失败必须 fail-fast。
   - **恢复路径（状态机/重试类）**：对每个可失败点，补一条"失败后修正输入重跑/重试"验证，确认恢复不被旧失败状态污染——例如失败 tag 的同步失败后，改正确输入必须能正常走到成功。**只测失败本身、不测恢复路径 = 覆盖不完整。**
   - **生命周期/复用**：同名资源内容变化（同标识重跑产生新内容）、状态残留（旧失败条目跨迭代残留）、长生命周期复用（事件/TTL 过期）——状态机与清理类功能必须覆盖。
2. **测试要驱动真实实现**：优先用 httptest/真实依赖/内存版真实组件（如内存 registry server），不要只用 mock/fake——mock 发现不了"远程调用多了一次/参数没传对"这类问题。
3. **失败信息要可断言**：错误串稳定（如 `digest mismatch after push ...`），测试锁住这些串；分类逻辑（permanent/retryable）依赖子串时尤其如此。
4. 每个失败场景的测试/证据在 `## 验收记录` 标注（命令 + 预期 + 通过输出），并写入 Review Bundle 的失败场景摘要。

### Debug-First Troubleshooting（排障先开 debug 看决策，不猜）

遇到"看似没生效 / 没处理 / 像环境问题"的异常，**第一步开 debug/verbose 日志看真实决策**，再下结论——禁止靠 info 日志的启动行猜测（例如把组件日志调到 debug 后，`grep` 决策/关键事件行，能一眼看到"空配置正常等待"的 Noop 决策，而非误判为卡住）。

- 本地/测试环境：把组件日志级别调到 debug/verbose（如 operator `LOG_LEVEL=debug`），`grep` 决策/关键事件行。
- 用**决策日志**（decision made / action=X / sync succeeded version=N）而不是只看启动信息判断"有没有干活"。
- 存疑时记录证据到 `## 踩坑记录`，不要静默跳过。

### Blocked 必须附根因证据（禁止归咎环境绕开）

转 `## Round 2 阻塞` 或 `needs-grilling` 前，**阻塞必须附根因证据**（debug 日志片段、复现命令输出、代码路径），禁止把"疑似环境问题 / 看起来没处理"当作阻塞理由绕开（常见陷阱：某个正常行为——如空配置时状态机 Noop——被误判为"系统不处理"，从而绕过排查、漏掉潜伏的真实缺陷）。

- 证据要求：至少一条可复现的日志/命令输出 + 一句根因判断。
- "看起来没处理"先走 Debug-First（上一节）确认是 Noop、还是真卡住。
- 无法确认根因时，如实记录"根因未明 + 已排除 X/Y"，而不是归咎环境。

### Independent Audit Expectation（独立审计预期）

`auto_merge: true` 的任务在进入 review 后，daemon 会启动独立只读审计会话（`read`/`grep`/`bash`，无写工具）逐条 AC 复核原始证据。实现会话必须保证验收记录可被独立复现：每条 AC 标注可复现的命令（测试/curl/示例调用）与预期输出，而非仅写"已实现"。审计失败会带审计报告打回 implementing（见 daemon 文档 audit 配置）。

### Audit Failure Repair（审计失败修复）

`auto_merge: true` 的任务在 review 被独立审计打回 `implementing` 时（`phase_error_code=AUDIT_FAILED`）：

1. **加载 `skill://diagnosing-bugs`**，先读 `phase_error`（审计摘要）与 `audit_log` 字段指向的审计会话日志（`~/.dsh/logs/tasks/TASK-*-audit-*.log`）——审计已给出失败 AC 与原始证据，直接以此为根因起点，不要从零排查。
2. 按审计报告的失败 AC 逐条修复；修复后运行该 AC 对应的复现命令，确认证据与审计预期一致。
3. 修复完成走正常完成检查转 review，daemon 会重新审计（新会话、新证据）。`audit_fail_count` 由 daemon 维护，无需处理。
4. 若修复后仍无法满足 AC——怀疑 AC 本身歧义/矛盾/无法验证，不要反复空转：如实转 `needs-grilling`（grill_context 附审计报告与你的证据），daemon 审计路径也会将 requirement 类失败自动转 grilling 决策。

### Scope Hammering（时间盒过半自动削 scope）

若 REQ 声明了 `appetite`（small=30m / medium=2h / large=6h），在时间盒过半时执行：

1. 评估剩余 AC 的核心程度：是否 must-have？能否降级？
2. 非核心 AC 标为 `~nice-to-have`，写回 TASK 通知用户。
3. 优先交付核心 AC。nice-to-have 不阻塞 review——可在后续 cycle 中作为新 REQ 追加。

核心判断标准（From Shape Up Ch.14）：
- 没有这个功能，用户能否完成核心任务？（能 → nice-to-have）
- 这是新问题还是已有 workaround 的老问题？（老问题 → nice-to-have）
- 这个情况发生的概率？（<5% → nice-to-have）
## Prerequisite Gate（前置门禁，AC-066-17 式入口门禁）

计划中声明「入口门禁」（如 AC-066-17：上游 PR 合入、依赖任务 done、环境可运行）且门禁未通过时：

1. **禁止 replan 循环**：不写 `grill_resolution=replan`。门禁失败是**上游事实问题**，不是需求/计划问题——replan 不改变任何上游事实（TASK-066 教训：17 轮 replan 在同一 REQ hash 上零收敛，每轮烧一次 xhigh round2 的 token）。
2. 写 `## Round 2 阻塞`（证据：上游 PR 状态实测、依赖任务状态、环境复测结论），然后转 **blocked**：
   ```bash
   otg update-status {task} \
     status=blocked \
     blocked_phase=implementing \
     phase_error_code=PREREQUISITE_SMOKE_FAILED \
     phase_error="{上游事实证据摘要，一句话}" \
     resume_approved=false
   ```
3. 恢复由 daemon 自动完成：每轮 scan 检查 `blocked_by` 依赖**事实**（上游 `status=done` 且 `phase_error_code` 为空 = 上游 PR 已合入；**已熔断过的任务**还要求上游 `merge_status=merged`），事实变化后自动 `resume_approved=true`，无需用户干预、无需重新 grilling。
4. 恢复后**先同步 upstream/main 再重跑门禁**：
   ```bash
   git fetch upstream main
   # 若工作树有未提交的旧门禁 fixture（throwaway），用 main 版本覆盖/移除后再同步
   # （fixture 已随承接任务合入 main，以 main 版本为准，不在 worktree 手工维护）：
   git merge --ff-only upstream/main   # 冲突按项目 ADR 处理
   ```
   然后重跑门禁；**通过才进入 Step 2–9**。未通过则保持 implementing 记录证据（进入 daemon 无进展冷却，见下），不写 blocked、不 replan。

> **陈旧 upstream frontmatter 陷阱（TASK-071 教训）**：上游任务 frontmatter 显示 `done`+`merged` 但实际 PR 从未合入（TASK-018：旧 PR #16 标记 merged，v6 工作从未 push）时，转 blocked 会被 daemon 的 `prereqDepsSatisfied`（仅看 frontmatter）**每轮误恢复** → blocked→implementing→复验→blocked 循环。因此门禁 FAIL 时**按计划批准路径保持 `implementing` 不转 blocked**——daemon 无进展冷却接管：无进展完成（仍 implementing + 无 `checkpoint_commit`）进入指数退避冷却（10m→…→~10.7h），不每轮重派、不烧 token；冷却截止时间持久化到 frontmatter `round2_stall_until`，daemon 重启不清零（TASK-071 二修：纯内存冷却在频繁重启下每轮重启即重派）。**无进展熔断（daemon 自动）**：连续 3 轮无进展（`round2_stall_level` 累计，跨重启持久）后 daemon 停止派发并转 `blocked` + `PREREQUISITE_SMOKE_FAILED`，此后恢复要求更严格（上游全部 done+clean **且 merge_status=merged**，防谎报循环）。恢复由事实变化（承接任务真实合入）或人工 `/obsidian-task-runner-round2` 驱动。无进展完成的 implementing 会话不发「未正常结束」通知（通知仅在真实阶段错误时携带错误原因；正常完成等待门禁的会话静默进入冷却）。

## Pending Requirement Handoff（pending_req安全交接）

AC 完成后若 `pending_req=true`：

1. 不开始下一条 AC。
2. 提交：

```text
chore(task): checkpoint before requirement replan
```

3. 写入 commit SHA：

```bash
otg update-status \{task\} \
  checkpoint_commit=\{sha\} \
  status=refining \
  merge_approved=false
```

4. 保持 pending_req=true。
5. 写入变更记录，正常退出。

## Implementation Blockers（实现阻塞）

测试连续失败、计划外决策、依赖冲突或架构摩擦需要用户决策时：

1. 写 `## Round 2 阻塞` 和结构化 grill_context。
2. 保存 `grill_prev_status=implementing`，转 `needs-grilling`。
3. daemon 自动打开 Kitty。
4. Grilling 完成必须写：
   - `grill_resolution=resume`：纯实现/环境问题，daemon 直接恢复 implementing。
   - `grill_resolution=replan`：需求/设计/计划变化，设置 pending_req=true 后转 refining。
5. grill_resolution 为空时 daemon 不猜测，保持 needs-grilling。

## Pitfall Recording（踩坑记录）

**只要实现过程中"以为方案 X 对 → 失败 → 换 Y 才成功"，就必须在 TASK `## 踩坑记录` 追加一条**（模板见 TASK-000-template.md）：

```markdown
### {YYYY-MM-DD}: {现象一句话}
- 现象: {观察到的失败行为}
- 失败方案: {尝试过但不成立的方案与失败证据}
- 根因: {失败原因分析}
- 成功方案: {最终生效的方案}
- 相关文档: {knowledge_refs 里的 References 路径，可选}
```

- 成功方案本身可另写 ADR；**踩坑记录专记失败路径**——ADR 决策不含"试过 X 不行"。
- `相关文档` 帮助 merge 时分类归档到正确知识文档；省略时按内容自动分类。
- merge→done 时 daemon 自动提取到 References（`ExtractTaskKnowledge`，与 ADR 提取同批、`knowledge_extracted` 幂等），未命中自动归档 `References/uncategorized/`——负向经验不丢失。

## Completion Checklist（完成检查）

1. 全部 AC 有独立证据。
2. **踩坑记录校验**：实现过程中发生过试错换方案（`失败方案` → `成功方案`）的，`## 踩坑记录` 必须存在对应条目；确实零试错的，在 Review Bundle 显式声明"无踩坑"。缺失且未声明视为完成检查不通过。
3. **知识应用校验**：TASK `knowledge_refs` 中每条引用，验收记录或实现记录必须体现其应用（引用了约束/实践、或在代码/测试中落地）；未应用的 ref 在 Review Bundle 中列出并说明原因（不适用/过时/被 ADR 覆盖）。缺失应用说明视为完成检查不通过。
4. **每个 `risk: high` Step 的实现记录必须含 Prototype 证据**（`✅ Prototype validated` 或 FAIL 记录 + grill_context）；缺失则补跑 Prototype Gate，不得跳过。
5. 运行项目全部测试（Go: `go test -race ./...`）。
6. 运行 lint。
7. 加载 `skill://test-quality`，修复 critical/important 问题。
8. 加载 `skill://code-review`：Standards 轴检查代码规范+Code Smell；Spec 轴核验实现与 REQ 的 AC 是否一一对应、有无 scope creep。与 test-quality 互补——前者查测试质量，后者查代码+需求对齐。
9. **失败场景校验**：对照上方「失败场景矩阵」，确认每条可失败路径有负向测试且证据已写入验收记录；并发/竞态类用 `-count=3+` 复跑通过；跨引擎项目（dev SQLite / prod MySQL 等）有 prod 引擎实测证据。缺失且未补 → 完成检查不通过。
10. 调 task-verifier 核验 AC。

### Write ADRs (BEFORE implementation — do not skip)

**ADRs are blueprints, not footnotes.** Write the decision down before writing
the code that depends on it.

> The daemon auto-sets `adr_approved=true` at the plan-review→implementing transition.

If `adr_approved=true` AND `adr_proposed` is non-empty:

1. **Before starting the first AC**, read the `adr_proposed` list.
2. For each ADR title, generate the body using the standard ADR format:

```markdown
# ADR-{id}: {title}

## Status
accepted

## Context
{Why is this decision needed? What constraints and forces are at play?}

## Decision
{What specific approach was chosen?}

## Alternatives Considered
{What other options were evaluated and why were they rejected?}

## Consequences
{What becomes easier? What becomes harder? What are the risks?}
```

3. Call `otg write-adr {project_dir} {task_id} "{title}" "{body}"`
4. Call `otg validate-adr {file_path}` to verify structural integrity.
5. Reference the ADR during implementation: in commit messages or code comments, note `See ADR-{id}`.

**After all ACs are complete:**
6. Run `otg update-status` once:
   - Append written filenames to `adr_written`
   - Clear `adr_proposed`
   - Set `adr_approved` to `false`
7. Commit ADR files together with the implementation — **ADRs and code in the same commit.**

6. 本地 commit，不 push。


## Review Bundle 生成

全部 AC 完成且测试通过后，生成 Review Bundle 摘要，写入 TASK：

1. **diffstat**：`git diff --stat \{target_branch\}...HEAD`
2. **测试结果**：`go test` 输出中的 PASS/FAIL 统计
3. **test-quality 摘要**：🔴/🟡/🟢 计数
4. **code-review 摘要**：Standards 轴发现数 + Spec 轴发现数
5. **风险自评**：low/medium/high
6. **Baseline 对比**（Shape Up Ch.14）：实现前用户如何解决？实现后改善了什么？一句对比帮助用户快速判断 merge 价值
7. **Scope Hammering 结果**：若有降级的 AC，列出 `~nice-to-have` 项
8. **失败场景摘要**：可失败路径负向测试清单（类别 × 命令 × 通过），跨引擎项目附 prod 引擎实测证据一句话；缺失项显式列出
9. **通知摘要**：

```text
TASK-{id}: {N} files +{added}/-{deleted}, {M} tests PASS
test-quality: 🔴0/🟡0/🟢{N} | code-review: St{N} Sp{N}
appetite: {small|medium|large} | deferred: {N} AC (~nice-to-have)
baseline: {一句话改善对比}
risk: {level} → auto_merge 默认自动合并，无需人工 review（可设 auto_merge: false 恢复人工 gate）
```

成功写回：

全部 AC 完成、测试与检查通过后：

```bash
otg update-status {task} status=review req_refine_count=0
```

- `req_refine_count` 清零规则（CONTEXT.md 强制）：全部 AC 通过后清零——新一轮交付的需求缺口计数从 0 开始。
- `auto_merge: true`（默认）时 daemon 自动授权合并并执行 Merge Phase（push → PR → CI checks → merge），无需人工。
- `auto_merge: false` 时保持 `merge_approved: false`，等待用户 review 后手动设 `merge_approved: true`。

## New Project（新项目）

只有 Round 2 可以创建项目目录、Git repo 和脚手架。项目注册由 daemon 自动完成（`ensureProjectRegistered`，按 vault-map `new_project_root`/既有 `git_remote` 推断写入 projects 条目），无需手工执行任何 register 命令——Round 2 只需在 checkout 创建后正常退出，daemon 下一轮 scan 即自动注册。

## Frontmatter Safety（安全规范）

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for checkpoint commits, grill_context, and status transitions.
- Run `otg validate-doc {task_path}` after every TASK file write to verify structural integrity.
