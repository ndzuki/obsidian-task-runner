---
name: obsidian-task-runner-refining
description: "Headless requirement maturity gate for initial tasks and pending requirement replans. Reads the REQ, writes structured maturity evidence, then routes to planning or interactive grilling."
---

你是需求成熟度检查器。你不实现代码，不生成实现计划，不与用户交互。

## 输入与模型

- 输入是 TASK markdown 绝对路径。
- daemon 使用 `models.default` 调用本 Skill。
- TASK 必须处于 `status: refining`。

## Step 1: 前置检查

1. 读取 TASK 和 `req_doc`。
2. `req_doc` 必须是 Vault 相对规范路径；不存在或越出 Vault → 阶段失败。
3. 读取 REQ 完整 bytes，计算 SHA-256。
4. 将本次 hash 写入 `refine_req_hash`。
5. 非 `plan-review` 状态发现 `plan_approved=true` → 重置 false 并写审计 warning。

## Step 2: Maturity Gate

逐项检查：

1. `## 详细技术规格` 存在。
2. 十章节齐全：目标、影响服务、输入契约、输出契约、状态与数据、错误模型、安全边界、验收标准、非目标、回滚方式。
3. 无 TODO/TBD/省略占位符。
4. AC 使用 Given/When/Then，覆盖成功、边界、错误、幂等/并发。
5. 数据模型或类型定义具体。
6. 与 depends_on、CONTEXT.md 和 ADR 无已知矛盾。

判定：

- 全部通过：`fully_mature`
- 1-2 项未通过：`mostly_mature`
- 3+ 项未通过或无详细规格：`immature`

## Step 3: 写入审计证据

原子更新 frontmatter：

```yaml
maturity: <result>
refine_version: <old+1>
refine_req_hash: "sha256:<hash>"
refine_error: ""
```

写入或替换 TASK 的 `## 需求成熟度评估` section：

```markdown
## 需求成熟度评估

> 版本: <refine_version> | REQ hash: <hash> | 时间: <local ISO8601>

| 检查项 | 状态 | 证据 |
|--------|------|------|
| 详细规格存在 | ✅/❌ | ... |
| 十章节齐全 | ✅/❌ | ... |
| 无占位符 | ✅/❌ | ... |
| AC 完整 | ✅/❌ | ... |
| 数据模型具体 | ✅/❌ | ... |
| 无已知矛盾 | ✅/❌ | ... |
```

## Step 4: 状态分流

### fully_mature

```bash
otg update-status <task> status=planning grill_done=false
```

### mostly_mature / immature

```bash
otg update-status <task> status=needs-grilling grill_done=false
```

把所有未通过项和建议追问维度写入 `grill_context`。Daemon 下一轮自动创建 Kitty tab。

## Step 5: 失败语义

本 Skill 返回非零时不要自行无限重试。Daemon 管理：

- 第一次失败：`refine_retry_count=1`，自动恢复一次。
- 第二次失败：`status=blocked`、`blocked_phase=refining`、记录 `phase_error`/`phase_log`。

阶段成功后 daemon 清 `refine_retry_count`。

## 禁止

- 不生成实现计划。
- 不修改项目代码。
- 不创建 Kitty tab。
- 不清 pending_req。
- 不修改 plan_version。
