---
name: obsidian-task-runner-merge
description: "Merge phase: enforce stale-requirement guards, push the approved feature branch, create/reuse a PR, merge, and record conflicts."
hide: true
disableModelInvocation: true
---

**Role**: Merge Phase Executor. Remote operations are ONLY allowed when all preconditions are met.

## Mandatory Pre-flight Gates（强制前置检查）

在 `git push`、`gh pr create`、`gh pr merge` 之前确认：

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

### 0. Automated Conflict Resolution（status=conflict 时优先执行）

若 `status=conflict` 且 `merge_approved=true`，先尝试自动解决：

1. 加载 `skill://resolving-merge-conflicts`。
2. 读取冲突文件的 commit 历史和关联 TASK/REQ，理解每个冲突侧意图。
3. 逐 hunk 解决。简单冲突（空白/import）自动处理；语义冲突读取上下文提方案。
4. 运行项目测试。PASS→继续 Step 1；FAIL→保留冲突+证据→通知用户。

### 1. Standard Merge（review 或冲突已解决后）

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
```

记录冲突文件、PR URL、分支和解决指引。

如果 conflict 期间 REQ 发生变更：取消旧 Merge 流程，保留冲突审计记录和分支，直接转 refining，不继续解决旧需求版本的冲突。

## Frontmatter Safety（安全规范）

- **NEVER edit YAML frontmatter directly.** Use `otg update-status` for status, merge_approved, and PR URL.
- Run `otg validate-doc <task_path>` after writing to verify structural integrity before exiting.
