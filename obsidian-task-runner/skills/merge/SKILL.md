---
name: obsidian-task-runner-merge
description: "Merge phase: enforce stale-requirement guards, push the approved feature branch, create/reuse a PR, merge, and record conflicts. Daemon invokes this skill for one-shot AI conflict auto-resolution (local commits only; push/PR/merge stay with the daemon)."
hide: true
disableModelInvocation: true
---

**Role**: Merge Phase Executor. Remote operations are ONLY allowed when all preconditions are met.

## Mandatory Pre-flight Gates（强制前置检查）

以下门禁由 daemon 在任何远程操作（`git push` / `gh pr create` / `gh pr merge`）前自动校验。本 Skill 会话仅执行本地冲突解决，不发起任何远程操作：

1. `status` 是 `review` 或 `conflict`。
2. `merge_approved=true`。
3. `pending_req=false`。
4. 当前 REQ 完整 bytes SHA-256 等于 `plan_req_hash`。
7. **gh CLI 认证可用**：`gh auth status` 已登录（Merge Phase 的 push 经 `gh auth git-credential` 注入凭据，PR create/merge 也走 gh——统一使用 gh keyring token 身份）。gh 缺失或未登录 → daemon 拒绝远程操作。

> **命令契约**：Merge Phase 所有远程操作统一走 **gh CLI 认证通道**——`git push` 由 daemon 注入 `-c credential.helper='!gh auth git-credential'`，PR 创建/合并用 `gh pr create` / `gh pr merge`。禁止裸 `git push`（无 ambient https 凭据的机器会以 `could not read Username` 烧光重试预算，TASK-004 教训）。
>
> **gh 未登录处理**：daemon 在远程操作前本地预检 `gh auth status`（无网络）。gh 缺失或未登录 → 不发起任何远程操作，写 `status=review` + `merge_approved=false` + `phase_error_code=GITHUB_UNAVAILABLE`，`phase_error` 附 `gh auth login` 指引，并桌面通知提醒用户完成 GitHub CLI 认证；登录后重新设 `merge_approved=true` 继续。

任一失败：

- 不执行任何远程操作。
- 若 pending_req=true 或 hash 不一致，清 merge_approved 并转 refining。
- 写变更记录和错误上下文。

用户手动重新设置 merge_approved 不能绕过 pending_req 门禁。

## Merge Flow（合并流程）

### 0. Automated Repair（daemon 触发，本地模式）

PR 存在合并冲突**或 CI checks 失败**时，daemon 以本 Skill 启动 OMP 会话自动修复。每个计划/交付周期最多 **`max_auto_merge_fixes`** 次自动修复（vault-map 配置，默认 3；daemon 以 `merge_retry_count` 计数），全部失败后交还用户选择：继续 AI 修复（清计数）或 replan（见下方「预算恢复」）。

模式由 prompt 第二参数决定：
- `{task} conflicts` — 冲突解决（默认，兼容旧调用）：
  1. 加载 `skill://resolving-merge-conflicts`。
  2. **需求溯源（必须）**：读取 TASK frontmatter 的 `req_doc`，用 `grep`/`read` 定位 REQ 文档中与冲突代码对应的契约章节（输入/输出契约、错误模型、状态机、验收标准 AC）。每个冲突侧先回答「它对应哪个需求意图/AC」，再判断代码归属。
  3. 逐 hunk 解决。简单冲突（空白/import）自动处理；语义冲突**以需求契约为准**裁决（两侧各自满足哪个 AC，冲突处的最终行为必须符合 REQ 描述），读取上下文提方案；无法从 REQ 判定的分歧记录到解决 commit message 中。
  4. 运行项目测试。PASS→本地 commit 解决结果并正常退出；FAIL→保留冲突+证据，以非零退出码结束，daemon 通知用户。
- `{task} ci-fix` — CI 失败修复：
  1. 读取失败 checks（`gh pr checks` 只读查询 PR URL，其余禁止远程操作）。
  2. 在 feature 分支上修复失败测试/代码的根因（加载 `skill://diagnosing-bugs` 定位，**同样先做需求溯源**：失败测试断言的需求依据在 REQ 哪个 AC，修复必须保持契约一致），运行项目测试直到 PASS。
  3. PASS → 本地 commit（如 `fix(ci): repair failing checks for TASK-{id}`）并正常退出；无法修复 → 保留证据，非零退出。
5. **本地模式铁律：禁止 push / pr create / pr merge 及任何远程操作**（`gh pr checks` 只读查询除外）。daemon 负责 push 新 head 并重新评估 CI checks，通过后才合并。
6. **会话被 daemon 停机中断**：任务保持 `review + merge_approved=true`，重启后自动恢复合并，不会写 conflict——不要做任何收尾写回。

### 1. Standard Merge（daemon 执行，本会话不涉及）

本 Skill 的 OMP 会话**只执行 Step 0 本地冲突解决**。push、PR 创建/复用、CI checks 评估与 `gh pr merge` 全部由 daemon（Go 代码）在会话结束后执行：AI 解决成功并提交后，daemon push 新 head、重新评估 checks、通过后合并。本会话任何情况下都不得执行远程操作（见 Step 0 第 5 条本地模式铁律）。

## Success Write-back（成功写回）

```yaml
status: done
merge_approved: false
pending_req: false
completed: {local ISO8601}
```

写入 PR URL、默认分支、feature 分支和合并审计记录。

## Auto Re-authorization（自动恢复语义）

`auto_merge=true` 任务的 merge 授权由 daemon 自动恢复，无需人工干预：

- **merge 失败回退自动重授权**：`review`/`conflict` + `merge_approved=false` + `phase_error_code` 非空时，daemon 每轮 scan 判定 `canAutoApproveMerge`——REQ 未变（当前 hash == `plan_req_hash`）、非永久环境缺陷（`GITHUB_UNAVAILABLE`/`REPO_MISMATCH` 除外）、`merge_retry_count < max_auto_merge_fixes` → 自动写 `merge_approved=true` 重新进入 Merge Phase（TASK-051/059 教训：旧版 `BASE_COMMIT_MISMATCH` 写回后 gate 要求空 phase_error_code，导致 auto_merge 任务永久卡在 conflict）。
- **停机/超时中断**：push 被 daemon 停机中断 → 保持 `merge_approved=true`，重启后自动恢复合并，不写 conflict（与 Step 0 第 6 条同语义）。
- **保持人工门禁**：REQ hash 变更（走 OnReqChanged → refining）、gh 不可用、仓库目标不匹配、预算耗尽（`conflict-resolve-attempted`）仍交还用户。

## Sync & Push（daemon 执行）

- merge 在**任务 worktree** 上执行（round2 worktree 已 checkout `target_branch`），不在主 checkout 上 merge——主 checkout 停留其他分支时会把 remote feature 历史合入错误分支（TASK-051/059 教训）。
- sync 前自动清理残留 `MERGE_HEAD`（`git merge abort`）：上一次失败会话遗留的悬挂 merge 会污染后续 sync。
- sync 按**祖先关系**分流：remote 是 local 祖先 → 直接 push；local 是 remote 祖先 → 三路 merge（冲突交 Step 0 AI 解决）；历史分叉 → 仅当 remote 独有变更文件全部被 local 重新实现（`merge-base..remote` ⊆ `merge-base..local` 文件级覆盖）时 `--force-with-lease` push，否则拒绝并交还用户——绝不猜测覆盖 main 不知道的提交。

## Conflict Write-back（冲突写回）

```yaml
status: conflict
merge_approved: false
merge_status: conflict-resolve-attempted
```

记录冲突文件、PR URL、分支和解决指引。`merge_status=conflict-resolve-attempted` 标记 AI 自动修复已达上限（`merge_retry_count >= max_auto_merge_fixes`）。交还用户后两条出路（均无需手动解冲突）：① 清 `merge_retry_count` 后重设 `merge_approved: true` 继续 AI 修复；② 在 REQ 文档追加歧义裁决记录并保存（建议含 `> 变更类型: breaking` 行）→ daemon 自动转 refining 重审需求后重新出计划。同一计划内重复授权不重置计数（防无限自动修复循环）。

如果 conflict 期间 REQ 发生变更：取消旧 Merge 流程，保留冲突审计记录和分支，直接转 refining，不继续解决旧需求版本的冲突。

## Frontmatter Safety（安全规范）

> **预算恢复**：`merge_retry_count` 仅在 merge 成功或**新一轮 planning 完成**时清零——replan 是全新交付意图，不继承旧交付耗尽的修复预算（TASK-067 教训：v3 将 3 次预算耗在 18 文件大 rebase 上，v4 若继承则无 AI 修复能力）。同一计划内的重复授权不重置（防无限循环）。预算耗尽后用户可：① 清 `merge_retry_count` 后重设 `merge_approved=true` 继续 AI 修复；② replan——`review` 状态可设 `rework_resolution=replan`（或改 REQ 触发变更），`conflict` 状态在 REQ 文档追加歧义裁决并保存（建议含 `> 变更类型: breaking` 行）→ daemon 自动转 refining；大范围冲突/连续失败多为需求歧义，推荐 replan，无需手动解冲突。

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for status, merge_approved, and PR URL.
- Run `otg validate-doc {task_path}` after writing to verify structural integrity before exiting.
