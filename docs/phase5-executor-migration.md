# Phase 5 执行路径迁移：OMP → DSH

> 状态：**设计已定，实施待进行**。本文档是 daemon 执行循环从 `spawn omp`
> 切换到 `spawn dsh --profile headless` 的完整蓝图。低风险清理（skill-doctor
> omp 引用、skills manifest 单一来源）已完成；核心的执行循环切换按本文档
> 分步实施，每步可独立回滚。

## 1. 现状：OMP 执行路径的 OMP 特有逻辑

`internal/daemon/daemon.go` 的 `processBatchSequential` 内联了 ~400 行执行
代码，其中大量逻辑是 OMP 特有的，切换到 DSH 时不能简单搬运：

| 逻辑 | 位置 | DSH 对应 | 迁移动作 |
|---|---|---|---|
| `exec.CommandContext(OMPCmd, args...)` | daemon.go:3280,3477 | `dshExecutor.Start` | 替换 |
| PID 文件（check/write/cleanup/adopt） | 3266,3327,2487,2516 | durable session id（`executor_session_id`） | 移除，改用 frontmatter 会话身份 |
| `tailOMPLog`（结构化日志尾随） | 3304,4269 | DSH 输出直接落任务日志 | 移除 |
| `watchEmptyStops`（空响应检测） | 3312 | DSH fallback 插件处理 | 移除 |
| `checkAPIKeyUnavailable`/`checkTokenQuota` | 3383,3388 | 需调研 DSH headless 退出码/输出 | 待调研后替换 |
| fallback model 重试（完整循环 + stale-guard） | 3395-3500 | `~/.dsh/plugins/fallback.mjs` 跨模型降级 | 移除，交 DSH 插件 |
| `--model/--thinking/--tools` args | 3189 | `PhaseSpec{Model,ReasoningEffort,ToolPolicy}` | 数据化 |
| SIGTERM + WaitDelay | 3289 | dshExecutor 已实现同契约 | 复用 |

**保留不变**：超时、shutdown interrupt 检测（`shutdownInterrupt`）、失败码
路由框架、通知防抖、`validatePhaseDocuments` 校验门禁、compact、状态写回。

## 2. 目标：DSH 执行路径

```go
spec := PhaseSpec{
    Phase: phase, Model: model, ReasoningEffort: thinking,
    SkillPrompt: skillPrompt, ToolPolicy: toolPolicy,
    Timeout: timeout, WorkingDir: repoDir, ExtraEnv: ...,
}
handle, err := executor.Start(ctx, spec, TaskSnapshot{TaskID, TaskPath, Project, RepoDir})
result, err := handle.Wait()
// result.Code: success/failed/timeout/interrupted/quota_exhausted/key_unavailable/empty_response
```

`dshExecutor`（Phase 1 已定义，Phase 3 design session 已验证）负责
`dsh --profile headless <taskText>`。fallback、空响应、模型切换都在 DSH
进程内由 `fallback.mjs` 插件处理，daemon 侧不再需要 fallback 循环。

## 3. 切换策略：config 开关 + 渐进迁移

引入 `executor` 配置（`"omp"` 默认 / `"dsh"` 可选），`processBatchSequential`
按配置分流。默认 `omp` 保持现状零风险；`dsh` 作为 opt-in 逐阶段验证；
全部 8 阶段验证通过后默认切 `dsh`，最终删除 omp 分支。

```go
// config.go
Executor string `json:"executor"` // "omp"(default) | "dsh"
```

## 4. 分步实施（每步独立 commit + 全量 race + 逐阶段冒烟）

| 步 | 内容 | 验证 |
|---|---|---|
| 5.0 | config `executor` 开关 + 校验 + 默认值 | config 测试 |
| 5.1 | 提取 OMP 内联执行段为 `runOMPPhase`（行为不变纯重构） | 现有 daemon 测试全绿 |
| 5.2 | 新增 `runDSHPhase`：PhaseSpec 构造 + dshExecutor + ExecutionResult→ErrorCode 映射 | 单测（fake executor） |
| 5.3 | `processBatchSequential` 按 `executor` 分流；默认 omp | 双路径测试 |
| 5.4 | **逐阶段**验证 dsh 路径：refining → planning → round2 → priority（先 4 个无 git 侧效的阶段） | 每阶段一个真实 vault 冒烟 |
| 5.5 | 验证 merge/pm/audit/conventions（4 个 git/审核阶段） | 同上 |
| 5.6 | 调研并实现 DSH 退出码→ quota/key-unavailable 检测（替代日志解析） | 错误码映射测试 |
| 5.7 | 移除 OMP 特有逻辑（PID 文件、tailOMPLog、empty-stop、fallback 循环） | 删除后全量回归 |
| 5.8 | install.go 移除 omp 检查/symlink；skill 目录 ~/.omp → ~/.dsh；systemd unit 改名 | install 测试 + 全新安装冒烟 |
| 5.9 | 默认 `executor: "dsh"`；删除 omp 分支与 `OMPCmd` | 全链路 DSH 跑通 |

## 5.4 冒烟验证记录

**refining 阶段 —— 已验证 ✅（2026-08-19）**

隔离 vault（`~/.dsh/tmp/smoke-vault`，含完整十章节 REQ + `status: refining` TASK），
spawn `dsh --profile headless`（deepseek-v4-pro）执行 refining skill：

- ✅ skill 正确加载并遵循 `obsidian-task-runner-refining` 指令
- ✅ 读取 TASK/REQ、预计算 `refine_req_hash`（零 token 回退）
- ✅ 成熟度门禁 6/6 全通过（`fully_mature`）
- ✅ 写回 frontmatter：`maturity/refine_version=1/refine_req_hash/refine_error`
- ✅ body `## 需求成熟度评估` 结构化证据表生成
- ✅ 退出码 0，输出为结构化执行摘要

**关键依赖确认**：skill 内部调用 `otg update-status` / `otg validate-doc`，
依赖 `otg` 在 PATH（`/home/user/go/bin/otg`，dsh headless 继承 daemon 环境，可达）。

**其余阶段**：
- **priority —— 已验证 ✅（2026-08-19）**：dsh headless 输出 ```json fenced block，
  经 extractJSON 提取 + priority.Decode 完整解析（`runPriorityAssessmentDSH` 分流
  已单测：成功写回 score、中断重置 claim、畸形 stdout 失败）。skill 冒烟（spawn
  `dsh --profile headless "/obsidian-task-runner-priority <REQ>"`）确认输出形状。
- **planning —— 冒烟未通过 ⚠️（2026-08-19）**：
  - 修复 1（已提交 63bf9b0）：phase skills 带 `disable-model-invocation: true`
    被 DSH 从模型目录排除，dsh 会话无法加载 → dshExecutor 改为直接注入
    `~/.dsh/skills/<skill>/SKILL.md` 正文（对齐 omp「daemon 注入正文」机制）。
  - 残留问题：session 日志显示模型执行了 180 bash + 23 read + 14 step 工具调用，
    但**跑偏**（读真实 vault 文件 + 仓库代码，生成与 smoke TASK 无关的 plan_files），
    且**未写回**任务文件。疑为空 smoke vault（无 ADR/CONTEXT）+ 空 git repo 使
    模型缺乏聚焦上下文而到处探索；真实 vault（有完整结构）下需重新验证。
  - **未污染真实数据**（已验证真实 vault 5 分钟内无 .md 修改）。
- round2 涉及 git worktree 需额外前置；merge/pm/audit/conventions 有专属 runner，
  已做 dsh 分流（5b45d2b）但未真实冒烟。

## 5.5 模型路由（2026-08-19，用户确认）

- **fallback 链**（`~/.dsh/cordis.patch.yml`，fallback.mjs）：
  - `magic/deepseek-v4-pro` → 官方 `deepseek-official/deepseek-v4-pro`
  - `magic/gpt-5.4-mini`（=flash）→ 官方 `deepseek-official/deepseek-v4-flash`
  - 即 magic 免费模型无响应时兜底官方 DeepSeek 直连（web 与 dsh headless 统一由
    fallback.mjs 完成）。
- **settings.yaml**：新增 `deepseek-official` provider（官方 API，模型 id 小写
  `deepseek-v4-pro`/`deepseek-v4-flash`，与 `/models` 实测一致）；`agent-default-model`
  同步为 `deepseek-official/deepseek-v4-flash`（与运行时/web 一致）。
- **DSH 已知 bug**：provider 名含 `deepseek` 前缀（`deepseek-official`）时，
  `agent-default-model` 设为 magic 会 NO_ADAPTER（llm-pi-ai 注册冲突，已实测定位）；
  默认模型保持官方，magic 作为会话可选模型（fallback from 目标）。
- **otg 侧**（vault-map.json / DefaultModels / DefaultFallbackModels）：gpt 主模型
  `gateway/deepseek-v4-pro`（免费），fallback `deepseek/deepseek-v4-pro`（官方）。

## 5. 关键风险与对策

1. **DSH headless 失败语义未知**：quota/key-unavailable 如何从 `dsh` 进程
   反映（退出码 vs 输出）需在 5.6 实测，不可假设与 omp 相同。
2. **fallback 语义变化**：daemon 侧 fallback（换模型 + stale-guard）vs DSH
   插件 fallback（`agent/request-error` → retry）。迁移后 stale-guard 仍须
   保留（防旧会话写回错误阶段）。
3. **中断/恢复**：DSH 无 PID 文件；daemon 重启后靠 frontmatter 状态重新
   派发（`phase_error_code=PHASE_INTERRUPTED` 自动恢复），与现状一致。
4. **8 阶段差异**：merge 阶段用 git CLI 而非 LLM 会话（`merge_runner.go`），
   pm/audit/conventions 各有专属 runner，不在 `processBatchSequential` 主
   循环——逐阶段排查，勿一刀切。

## 6. 完成判据

- `executor: "dsh"` 下 8 阶段全链路冒烟通过；
- `OMPCmd`、`FallbackModels`、`tailOMPLog`、PID 文件、omp 日志解析全部移除；
- `otg install` 不再检查/安装 omp；skills 落 `~/.dsh/skills`；
- `make test` 全绿 + 真实 vault 端到端（release-manager 73 任务）无回归。
