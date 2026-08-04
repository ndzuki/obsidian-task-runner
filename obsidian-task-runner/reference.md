# Obsidian Task Runner — 目标设计参考

> 规范流程见 [`docs/workflow.md`](../docs/workflow.md)。本文定义状态、frontmatter schema、依赖引用和人工操作。
>
> 当前 Go 实现未完全满足本文；以 workflow.md 的实现验收清单为准。

## 1. 状态流转

```text
blocked → ready → refining ─┬─ fully_mature → planning → plan-review → implementing → review → done
                            ├─ needs input → needs-grilling → refining
                            └─ 大型需求 → Wayfinder Map 决策地图（Grilling 焦点）

needs-refining（旧版遗留）→ 自动迁移 needs-grilling → refining

refining/planning -- retry once, fail again --> blocked
implementing -- pending_req at AC boundary --> refining
implementing -- prototype FAIL → needs-grilling（带原型证据）
review -- merge conflict --> conflict -- AI 自动解决一次，失败后人工重授权 --> done
plan-review -- close_approved --> closed
review -- close_approved --> closed
closed -- [终态，不可恢复]
```

## 2. 状态定义
| 状态 | 含义 | 执行者 | 下一步 |
|------|------|--------|--------|
| `blocked` | 缺字段/依赖，或 refining/planning 连续失败，或 API key 不可用，或人工暂停 | daemon / 人工 | 见 §4.4（自动 unblock / resume / key 探测 / 暂停） |
| `ready` | 可开始 priority assessment + maturity gate | daemon | `refining` |
| `refining` | Headless 检查需求规格成熟度 | `models.default` | `planning` / `needs-grilling` / `blocked` |
| `needs-grilling` | 需要用户交互补充规格 | Kitty + requirement-elaborator | `refining` |
| `planning` | 规格成熟，正在生成版本化计划 | TASK assignee + Round 1 Skill | `plan-review` / `blocked` / `refining` |
| `plan-review` | 具体计划已存在，等待人工批准 | 人工 | `implementing` / `closed` |
| `implementing` | 执行已批准计划 | TASK assignee + Round 2 Skill | `review` / `refining` / `needs-grilling` |
| `review` | 本地实现已提交；auto_merge=true 时自动授权合并，否则等待人工 | daemon 自动 / 人工 | `done` / `conflict` / `refining` / `closed` |
| `closed` | 已关闭终止；不再流转 | 人工 | —（终态） |
| `conflict` | Merge 冲突；AI 自动解决一次失败后交还人工 | daemon（AI 一次）+ 人工 | `done` / `refining` |
| `done` | 已合并推送；pending_req=false 时终态 | — | `refining` 或结束 |

## 3. 人工 Gate

| 字段 | 人工操作 | 约束 |
|------|----------|------|
| `plan_approved` | 审阅计划后设 true | 仅 `plan-review` 有效；`plan-review → implementing` 后**保留 true** 供 Round 2 OMP 读取，implementing 状态不重置 |
| `merge_approved` | Merge 授权 | `pending_req=true` 时绝对无效；进入 review 时按 `auto_merge` 自动置 true；硬失败回退（CI 拒绝/head 变更/gh 缺失）置 false 需人工重设；环境性失败（网络/瞬时 GitHub 错误）自动重试期间保持 true |
| `auto_merge` | 默认 true；进入 review 时自动授权合并 | 设 false 恢复人工 merge gate；仅 review 状态有效 |
| `close_approved` | 审阅后设 true，关闭任务 | 仅 `plan-review`/`review` 有效 |
| `rework_resolution` | 关闭前人工判定重做方向 | `replan` 转 refining；`resume` 恢复原阶段；空值保持等待 |
| `review_feedback` | review 阶段人工反馈摘要 | free text，`review` 状态下有效 |
| `adr_approved` | 系统自动管理 | daemon 在 plan-review→implementing 时自动设为 true；Round 2 写 ADR 后清 false |

## 4. Frontmatter Schema

### 4.1 身份与人工填写

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 项目内唯一；不同项目可重复 |
| `title` | string | 任务标题 |
| `project` | string | vault-map project key |
| `assignee` | string | planning/Round 2/Merge 模型 key |
| `req_doc` | string | Vault 相对规范路径，必须完整精确匹配 |
| `new_project` | bool | 新项目标记 |
| `template` | string | 新项目脚手架提示（已弃用，见 `scaffold_intent`） |
| `scaffold_intent` | string | 新项目脚手架意图描述，结构化技术栈/框架/构建/部署 |
| `blocked_by` | list | 同项目 `TASK-010`；跨项目 `project-key:TASK-010` |
| `auto_approve` | bool | 只跳过首次既有项目计划的 Plan Review |
| `off_peak_only` | bool | Round 2 只在北京时间低峰执行 |

### 4.2 Maturity Gate

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `maturity` | enum/string | `""` | `fully_mature` / `mostly_mature` / `immature` |
| `refine_version` | int | `0` | maturity gate 审计版本 |
| `refine_req_hash` | string | `""` | refining 开始时完整 REQ bytes SHA-256 |
| `refine_retry_count` | int | `0` | refining 自动恢复次数 |
| `refine_error` | string | `""` | 最近 refining 错误 |

Refining 必须同时维护 TASK 的 `## 需求成熟度评估` section，保存六项检查证据。

### 4.3 Planning

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `plan_req_hash` | string | `""` | planning 使用的 REQ hash |
| `plan_version` | int | `0` | 每次 planning 成功 +1 |
| `planning_retry_count` | int | `0` | planning 自动恢复次数 |
| `plan_approved` | bool | `false` | Round 2 Gate |
| `checkpoint_commit` | string | `""` | pending_req 前的 WIP checkpoint |

Planning 写 plan-review 前必须复核当前 REQ hash。Hash 变化时不得写入/批准计划，返回 refining。

### 4.4 阶段恢复

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `blocked_phase` | string | `""` | `refining`、`planning` 或 `implementing` |
| `phase_error` | string | `""` | 阶段失败原因 |
| `phase_error_code` | string | `""` | 阶段失败机器可读错误码 |
| `phase_log` | string | `""` | 对应日志路径 |
| `resume_approved` | bool | `false` | 恢复授权（人工或依赖链自动） |
| `auto_resume_pending` | bool | `false` | 本次失败由自动 resume 发起（仅它计入预算） |
| `auto_resume_count` | int | `0` | 自动 resume 连续失败累计，≥2 停止自动恢复 |

Refining/planning/implementing 第一次失败自动恢复；再次失败转 blocked。阶段成功或人工 resume 后 retry count 清零。

`blocked_by` 上游处于阶段失败阻塞（`blocked_phase` 非空且错误码为 MODEL_FAILED/PHASE_TIMEOUT/PHASE_INTERRUPTED/MODEL_QUOTA_EXHAUSTED 或空）时，daemon 自动设 `resume_approved=true` + `auto_resume_pending=true` 以解开依赖链；`auto_resume_count` 仅在这种自动恢复后再次失败时递增。人工 resume（无 pending 标记）清零计数。`REQ_MISSING` 等非瞬时错误与循环依赖永不自动恢复。

**`PHASE_INTERRUPTED`（daemon 重启/停机中断）**：daemon 优雅停机时，运行中的 OMP 收 SIGTERM 保存 session 后退出，任务**不转 blocked**——保持原状态并写 `phase_error_code=PHASE_INTERRUPTED`（`phase_error="daemon 重启中断，等待自动恢复"`）；重启后下一轮 scan 自动重新调度，阶段成功后由 `clearPhaseError` 清除标记。该错误码同时被依赖链自动恢复识别（见上）。

**人工暂停**：需要暂停任务等待外部条件（如用户完善需求）时，可设 `status=blocked` + `blocked_phase=<原状态>` + `phase_error_code=REQ_MISSING`（非自动恢复错误码），daemon 保持阻塞且不提醒；条件满足后设 `resume_approved=true` 恢复。

### 4.5 Grilling 所有权

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `grill_owner` | string | `""` | 当前交互会话 owner |
| `grill_started_at` | ISO8601 | `""` | owner 获取时间 |
| `grill_timeout_minutes` | int | `30` | 可配置 lease 超时 |
| `grill_heartbeat_at` | ISO8601 | `""` | 最近心跳时间，用于 lease 存活检测 |
| `grill_done` | bool | `false` | 规格写回完成标记 |
| `grill_context` | string/YAML | `""` | 需要对齐的问题上下文 |
| `grill_prev_status` | string | `""` | 实现阻塞前状态 |
| `grill_continue` | bool | `false` | 用户离线填答完成标记；daemon 检测到 true 时重置 refining 复验并清字段（异步 Grilling） |

| `grill_resolution` | enum/string | `""` | `resume` 直接恢复实现；`replan` 转 refining；空值保持等待 |
Daemon 和 requirement-elaborator 都必须检查 owner。读检查写过程使用 `${TMPDIR}/otg-grill-<task-path-sha256>.lock` flock 强化本机原子性。

需求细化完成使用 `grill_resolution=replan`，daemon 转 refining 复验。实现阻塞按 resolution 分流。`pending_req=true` 优先于 `resume`，必须重规划。

Daemon 成功消费后原子清 `grill_done`、`grill_resolution`、`grill_context`、`grill_prev_status`，防止重复路由。

#### 4.6.1 需求变更与 Merge

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `pending_req` | bool | `false` | 新 REQ 尚未被新计划完整吸收 |
| `merge_approved` | bool | `false` | Merge Gate；pending_req=true 时绝对无效；review 进入时按 auto_merge 自动置 true |
| `auto_merge` | bool | `true` | 进入 review 自动授权合并；false 恢复人工 gate |
| `target_branch` | string | `""` | Round 2 分支 |
| `pr_url` | string | `""` | PR URL |

`pending_req` 仅在新 planning 成功后清 false。

#### 4.6.2 ADR（架构决策记录）

ADR 是项目的架构宪法。三个原则：
1. **决策前读 ADR** — Round 1 规划的第一步是读全部已有 ADR，新计划不能与已确立决策冲突。
2. **实现前写 ADR** — Round 2 在写代码之前把架构决策文档化，ADR 和代码在同一 commit 中。
3. **ADR 是活的** — 决策可以被 supersede，但必须有新 ADR 说明理由。

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `adr_proposed` | list | `[]` | Round 1 提议的 ADR 标题列表 |
| `adr_approved` | bool | `false` | daemon 在 plan-review→implementing 时自动设为 true |
| `adr_written` | list | `[]` | 已写入 `Notes/adr/` 的 ADR 文件名列表 |

ADR 生命周期：Round 1 读取已有 ADR → 检测新架构决策 → 写入 `adr_proposed` → daemon 自动授权 `adr_approved=true` → Round 2 在实现前写入 ADR 文件 → 全部 AC 完成后更新 `adr_written` 并清 `adr_proposed`/`adr_approved`。
#### 4.6.3 文档校验（CLI 命令）

| 命令 | 覆盖 |
|------|------|
| `otg validate-doc` | 自动识别 TASK/REQ/ADR，校验 frontmatter 必填字段 + body `<tag>` 扫描 |
| `otg repair-doc` | 修复 frontmatter + 自动转义 body `<tag>` → `\<tag\>` |
| `otg write-adr` | 原子写 ADR + fsync + validate |
| `otg validate-adr` | ADR frontmatter 结构校验 |

Daemon 在 OMP 成功后通过 `git diff --name-only` 扫描工作区所有 `.md` 变更，调用 `ValidateDocument` 兜底检测 CONTEXT.md、ADR 等非 TASK 文件的损坏。

#### 4.6.4 CONTEXT.md 自动维护

项目的 `Notes/CONTEXT.md` 是共享领域词汇表，由两个阶段自动维护：

- **Round 1**：计划中引入新领域术语时追加到 `## Language` 区域
- **Round 2 + ADR**：ADR 引入新架构概念时追加到 `## Language` 区域

append-only，不覆盖已有条目。`pipeline.EnsureContextMD` 在项目初始化时创建骨架模板。

#### 4.6.5 Priority Assessment（优先级评定）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `priority` | string | `""` | 优先级标签，人工或自动填写 |
| `priority_assessment_status` | string | `""` | `pending` / `running` / `completed` / `failed` |
| `priority_impact` | string | `""` | 影响范围描述 |
| `priority_urgency` | string | `""` | 紧急程度描述 |
| `priority_workaround` | string | `""` | 已知变通方案 |
| `priority_score` | int | `0` | 综合优先级分数 |
| `priority_confidence` | float | `0.0` | 评定置信度 0.0–1.0 |
| `priority_reason` | string | `""` | 评定理由 |
| `priority_recommendation` | string | `""` | 推荐处理策略 |
| `priority_assessed_value` | string | `""` | 评定结果值 |
| `priority_assessed_at` | ISO8601 | `""` | 评定完成时间 |
| `priority_assessment_attempts` | int | `0` | 评定重试次数 |
| `priority_assessment_started_at` | ISO8601 | `""` | 评定开始时间 |

Priority Assessment 由 daemon 在 refining 阶段触发，评定完成后写入结果字段。`priority_score` 用于调度排序。

#### 4.6.6 Closed 终态字段

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `closure_reason` | string | `""` | 关闭原因：`completed` / `rejected` / `duplicate` / `wont_fix` |
| `closure_note` | string | `""` | 关闭备注 |
| `replacement_task` | string | `""` | 替代任务 ID（`project:TASK-NNN` 格式） |

`closed` 是终态，不可恢复。`closure_reason` 提供审计追溯。

#### 4.6.7 Scaffold Intent（脚手架意图）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `scaffold_intent` | string | `""` | 新项目脚手架意图描述，替代原 `template` 自由文本字段 |

`scaffold_intent` 结构化描述新项目技术栈、框架、构建系统和部署目标，供 `project-scaffold` Skill 消费。原 `template` 字段保留向后兼容但优先使用 `scaffold_intent`。

#### 4.6.8 GitHub Remote Creation（远程仓库创建）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `remote_create` | bool | `false` | 是否在 GitHub 创建远程仓库 |
| `github_owner` | string | `""` | GitHub owner（用户或组织名） |
| `repository_name` | string | `""` | 仓库名 |
| `repository_visibility` | string | `"private"` | `private` / `public` / `internal` |
| `repository_description` | string | `""` | 仓库描述 |

`remote_create=true` 时 daemon 在 Round 2 开始前通过 GitHub API 创建远程仓库并设置 `origin`。

#### 4.6.9 文档校验字段

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `task_schema_version` | int | `1` | TASK frontmatter schema 版本号，用于迁移和兼容性校验 |

`otg validate-doc` 读取 `task_schema_version` 决定校验规则集。版本升级时 `otg repair-doc` 可自动迁移字段。

### 4.7 Daemon 上下文注入

Daemon 在调度 OMP 执行 `refining`、`planning`、`implementing`、`plan-review` 阶段时，从 Vault `Notes/CONTEXT.md` 提取精简 bundle，以 `<project_context>` 标签块追加到 skill 命令之后的 prompt 尾部，并附 `skill://knowledge-base` 交叉引用提示。

注入格式（技能命令后追加）：

```text
<skill prompt>

<project_context>
## 项目上下文（daemon 自动注入，配合 skill://knowledge-base 交叉引用 References）
项目: <project-key>

<Constraints + Anti-patterns + Domain Terms + ADR 摘要>
</project_context>
```

注入内容（控制在 ~600 字节 / ~300 token）：

- **Constraints**（始终注入）：`## Development Constraints` 节，截断到 100 字符/条
- **Anti-patterns**（始终注入）：`## Anti-patterns` 节，仅保留首句
- **Domain Terms**（动态选择）：`## Language` 节中按 REQ 关键词打分选 Top-N，无命中时 fallback 到前 4 个核心术语；长定义截断到 80 字符
- **ADR**（可选）：`Notes/adr/*.md` 中按 REQ 关键词匹配 Top-2，含 title + 一句 decision

术语数量由 `dynamicTermCount` 按剩余 token 预算动态分配。同一项目多次调度缓存 CONTEXT.md 内容（`sync.Map`），避免重复 IO。

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

- blocked：保持 blocked，pending_req=true。
- ready：保持 ready，pending_req=true。
- refining/planning：仅 pending_req=true，不改 live phase。
- needs-grilling + active owner：不中断会话，只设 pending_req=true。
- plan-review：撤销 plan approval，转 refining。
- implementing：当前 AC 后 checkpoint → refining。
- review/conflict/done：清 merge approval，直接 refining。
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
- Kitty 状态 JSON 无法解析时不会创建 tab，并回退到桌面通知；后续扫描继续重试。
- Kitty 不可用：保持 needs-grilling，写日志并周期重试，不转 blocked，不启动普通终端。

## 8. Daemon 与并发

Daemon 锁：`${TMPDIR}/otg-daemon-<vault-path-sha256>.lock`。

- 同一 Vault watcher/timer 互斥。
- 不同 Vault 可并行。
- refining 不需要仓库。
- 既有项目 planning 使用主工作区独占锁；Merge 无仓库锁（push/merge 在主 checkout 上执行，与 worktree OMP 隔离，避免被 planning/refining 读锁长期阻塞）。
- Round 2 使用任务专属 worktree。
- 新项目 planning 不创建目录；Round 2 才创建并 register-project。
- 每轮 scan 自动 Normalize 全部任务 frontmatter：缺失的 schema 字段按默认值补齐（不覆盖已有值，必填字段不补）、字段顺序按规范序维护（用户关注在前、系统维护在后，未知字段保持相对顺序置尾）；写前/写后均做 Parse 校验，损坏文档拒绝改写；补齐后校验必填完整性并记录诊断。`otg migrate-tasks <path> --write` 手动执行同一逻辑。
- **异步调度**：`processBatch` 只调度（dispatch）不等待——每个任务在独立 `runTask` goroutine 中执行，完成后释放仓库锁并 `requestScan()` 触发下一轮 scan（scan-gate coalesce，任务批量完成只多一轮）。一个长 Round 2（最长 1h）不再冻结 scan 循环：plan-review transition、merge 重试、REQ 变更等全部实时响应。`--once`（systemd timer）保持同步等待语义（dispatch 后等任务归零）。shutdown 时 `activeTasks` 计数等待在跑任务落盘（PHASE_INTERRUPTED 写回）后退出。

### 8.1 Thinking Mode

daemon 按阶段注入 `--thinking`，flash 与 pro 模型均支持：

| 阶段 | thinking |
|------|----------|
| priority | `off` |
| refining | `low` |
| planning | `high` |
| round2 | `max` |

模型标识不含 `:xhigh` 等推理后缀，强度完全由 `--thinking` 控制。

### 8.2 自动 resume 预算

`resolveBlockedDependencies` 每次扫描自动解开 `blocked_by` 依赖链，预算规则：

- `auto_resume_pending=true` 仅标记自动 resume 发起的尝试；`handlePhaseFailure` 只在 pending 时递增 `auto_resume_count`。
- 首次失败不计数；人工 resume（无 pending）清零计数。
- count ≥ 2：停止自动恢复，通知用户手动修复后设 `resume_approved=true`。
- 循环依赖与 `REQ_MISSING`/`VALIDATION_FAILED` 等非瞬时错误不自动恢复。

## 9. Skill 安装

Installer 随包安装 6 个顶层 Skill（真实文件，非 symlink）：core、refining、round1、round2、merge、priority。子 Skill 同时写入 `skills/` 子目录供 daemon 直读。

**`vault-map.json` 保护**：`otg install --force` 不会覆盖用户的项目映射和模型配置。安装前备份 `config/vault-map.json`，拷贝后恢复。`generateVaultMap` 对已有文件只追加缺失的默认字段，不覆盖已设置的 `projects`、`models` 等用户值。

外部依赖缺失必须 fail-fast：requirement-elaborator、grilling、domain-modeling、diagnosing-bugs、test-quality。

## 10. 故障排查

1. `otg find-ready <vault>`：检查 daemon 是否会拾取任务。
2. `tail -f ~/.omp/logs/otg-daemon.log`：检查状态分派、锁和重试。
3. `~/.omp/logs/tasks/`：检查阶段日志/PID。
4. blocked 阶段失败：检查 `blocked_phase`、`phase_error`、`phase_log`；修复后设 `resume_approved=true`。自动 resume 预算耗尽（`auto_resume_count>=2`）时会收到 🧩 桌面通知。
5. Grilling 卡住：检查 `grill_owner`、`grill_started_at`、timeout 和 Kitty 日志。
6. 安装后执行 `skill-doctor check`，必须返回 0。

## 11. Skill Writing Convention（Skill 编写规范）

All OMP skill documents MUST follow these conventions to maximize AI agent comprehension:

1. **Step headers**: bilingual format `## Step N: English Title（中文标题）`. The English verb is the action primitive agents are trained on.
2. **Mandatory actions**: prefix with RFC 2119 keywords **MUST** / **MUST NOT** / **SHOULD**.
3. **Persona line**: first non-header line MUST start with `**Role**: <English role name>. <one-line constraint>.`
4. **Trigger conditions**: use English tables with `Trigger | Description` columns.
5. **Commands and field names**: always in English (bash blocks, `otg update-status`, frontmatter keys).
6. **Explanatory body**: Chinese is acceptable for context and nuance.
7. **Prohibitions**: separate section, each item prefixed with `MUST NOT`.
