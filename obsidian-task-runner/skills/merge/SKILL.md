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
5. `target_branch` 存在。

任一失败：

- 不执行任何远程操作。
- 若 pending_req=true 或 hash 不一致，清 merge_approved 并转 refining。
- 写变更记录和错误上下文。

用户手动重新设置 merge_approved 不能绕过 pending_req 门禁。

## Merge Flow（合并流程）

### 0. Automated Conflict Resolution（daemon 触发，本地模式）

PR 存在合并冲突时，daemon 会以本 Skill 启动一次 OMP 会话自动解决（每个冲突事件仅一次，失败后交还用户）：

1. 加载 `skill://resolving-merge-conflicts`。
2. 读取冲突文件的 commit 历史和关联 TASK/REQ，理解每个冲突侧意图。
3. 逐 hunk 解决。简单冲突（空白/import）自动处理；语义冲突读取上下文提方案。
4. 运行项目测试。PASS→本地 commit 解决结果并正常退出；FAIL→保留冲突+证据，以非零退出码结束，daemon 通知用户。
5. **本地模式铁律：禁止 push / pr create / pr merge 及任何远程操作**。daemon 负责 push 新 head 并重新评估 CI checks，通过后才合并。
6. **会话被 daemon 停机中断**：任务保持 `review + merge_approved=true`，重启后自动恢复合并，不会写 conflict——不要做任何收尾写回。

### 1. Standard Merge（daemon 执行，本会话不涉及）

本 Skill 的 OMP 会话**只执行 Step 0 本地冲突解决**。push、PR 创建/复用、CI checks 评估与 `gh pr merge` 全部由 daemon（Go 代码）在会话结束后执行：AI 解决成功并提交后，daemon push 新 head、重新评估 checks、通过后合并。本会话任何情况下都不得执行远程操作（见 Step 0 第 5 条本地模式铁律）。

## Success Write-back（成功写回）

```yaml
status: done
merge_approved: false
pending_req: false
completed: <local ISO8601>
```

写入 PR URL、默认分支、feature 分支和合并审计记录。

## Conflict Write-back（冲突写回）

```yaml
status: conflict
merge_approved: false
merge_status: conflict-resolve-attempted
```

记录冲突文件、PR URL、分支和解决指引。`merge_status` 标记 AI 自动解决已尝试一次；用户手动解决后重设 `merge_approved: true` 重新授权，daemon 不再重复 AI 尝试。

如果 conflict 期间 REQ 发生变更：取消旧 Merge 流程，保留冲突审计记录和分支，直接转 refining，不继续解决旧需求版本的冲突。

## Frontmatter Safety（安全规范）

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for status, merge_approved, and PR URL.
- Run `otg validate-doc <task_path>` after writing to verify structural integrity before exiting.
