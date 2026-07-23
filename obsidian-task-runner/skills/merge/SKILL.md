---
name: obsidian-task-runner-merge
description: "Merge phase: enforce stale-requirement guards, push the approved feature branch, create/reuse a PR, merge, and record conflicts."
hide: true
disableModelInvocation: true
---

你是 Merge Phase 执行器。只有全部前置条件满足时才允许远程操作。

## 强制前置检查

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

## 合并流程

1. 解析默认分支并 `git fetch origin`。
2. 验证 feature branch。
3. `git push -u origin <target_branch>`。
4. 使用 gh 创建或复用 PR；gh 不可用时按既定本地 merge fallback。
5. 合并成功后拉取最新默认分支并清理 feature 分支。

## 成功写回

```yaml
status: done
merge_approved: false
pending_req: false
completed: <local ISO8601>
```

写入 PR URL、默认分支、feature 分支和合并审计记录。

## 冲突写回

```yaml
status: conflict
merge_approved: false
```

记录冲突文件、PR URL、分支和解决指引。

如果 conflict 期间 REQ 发生变更：取消旧 Merge 流程，保留冲突审计记录和分支，直接转 refining，不继续解决旧需求版本的冲突。
