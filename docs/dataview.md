# Dataview 配置指南

这份文档说明如何让 Obsidian 用 Dataview 显示 Obsidian Task Runner 的任务看板。Dataview 是一个“读取并查询 Markdown 元数据”的插件：它会读取 YAML frontmatter，按查询结果生成表格或列表；它不会替你修改任务文件。

## 1. 安装 Dataview

1. 打开 Obsidian 的 **设置**。
2. 进入 **社区插件**，关闭安全模式（如果 Obsidian 提示需要）。
3. 点击 **浏览**，搜索 `Dataview`。
4. 安装并启用 **Dataview**（插件作者为 `blacksmithgu`）。
5. 回到 Vault，等待几秒让 Dataview 建立索引。

官方文档：[Dataview](https://blacksmithgu.github.io/obsidian-dataview/)。

> 如果组织环境不允许社区插件，可以继续使用 `otg`；只有动态看板需要 Dataview。

## 2. 准备 Vault 目录

推荐目录结构如下：

```text
你的 Vault/
├── Tasks-Dashboard.md
└── Projects/
    └── my-backend/
        ├── Requirements/
        │   └── REQ-001-login.md
        ├── Tasks/
        │   └── TASK-001-login.md
```

运行 `otg install --vault <Vault路径>` 时，如果 Vault 根目录还没有 `Tasks-Dashboard.md`，安装器会自动部署一份。若文件已存在，安装器不会覆盖它。

手动部署时，将仓库根目录的 [`Tasks-Dashboard.md`](../Tasks-Dashboard.md) 复制到 Vault 根目录。

## 3. 检查任务 frontmatter

Dataview 只能查询它已经索引的字段。任务文件开头应有 YAML frontmatter，例如：

```yaml
---
id: "001"
title: 用户登录 API
project: my-backend
status: plan-review
priority: P2
assignee: deepseek
created: 2026-07-15T10:00:00+08:00
updated: 2026-07-15T10:30:00+08:00
---
```

注意：

- `status`、`priority`、`assignee` 等字段必须写在 `---` 之间。
- `status` 是字符串；建议使用 `blocked`、`ready`、`plan-review`、`implementing`、`review`、`conflict`、`done`。
- 日期字段建议使用 ISO 8601 格式，便于排序。
- 列表字段写成 `tags: [api, auth]` 或多行 YAML 列表。
- 任务文件放在 `Projects/**/Tasks/` 下，默认看板才会找到它。

任务 frontmatter 的完整说明见 [`obsidian-task-runner/reference.md`](../obsidian-task-runner/reference.md)。

## 4. 打开任务看板

在 Obsidian 的文件列表中打开 `Tasks-Dashboard.md`。正常情况下会看到四类内容：

1. **按项目汇总**：每个项目的任务总数，以及各状态数量。
2. **待处理任务**：排除 `done` 和 `blocked` 的任务。
3. **阻塞任务**：缺少配置或等待依赖的任务。
4. **最近完成**：最近完成的任务和执行者。

保存任务文件后，Dataview 会自动刷新查询。若没有刷新，可关闭并重新打开看板，或在命令面板执行 **Reload app without saving**。

## 5. 查询分别做了什么

看板查询使用 Dataview Query Language（DQL）的 `TABLE`、`LIST`、`FROM`、`WHERE`、`GROUP BY` 和 `SORT`：

```dataview
TABLE status, priority, assignee
FROM "Projects"
WHERE contains(file.folder, "Tasks")
SORT priority asc, file.mtime desc
```

含义：

- `FROM "Projects"`：只查询 Vault 的 `Projects` 目录。
- `contains(file.folder, "Tasks")`：只保留任务目录中的 Markdown 文件。
- `status`、`priority`、`assignee`：读取任务 frontmatter 字段。
- `file.mtime`：Dataview 提供的文件最后修改时间。
- `SORT`：按优先级和更新时间排序。

如果你改动了 Vault 目录层级，需要同时修改 `FROM` 或 `WHERE`，否则看板可能为空。

## 6. 常见定制

### 只看某个项目

在看板中增加一个查询：

```dataview
TABLE title, status, priority, assignee
FROM "Projects/my-backend/Tasks"
SORT priority asc, file.mtime desc
```

### 只看需要我确认的任务

```dataview
TABLE title, status, plan_approved, merge_approved, file.link AS "任务"
FROM "Projects"
WHERE contains(file.folder, "Tasks")
  AND (status = "plan-review" OR status = "review")
SORT file.mtime desc
```

### 只看某种执行模型

```dataview
TABLE title, status, priority, file.link AS "任务"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND assignee = "deepseek"
SORT priority asc
```


## 7. 看板为空时按顺序排查

1. 确认 Dataview 已启用，而不是只安装未启用。
2. 确认任务文件扩展名是 `.md`，且位于 `Projects/**/Tasks/`。
3. 确认 frontmatter 的两个 `---` 存在，并且 YAML 缩进正确。
4. 确认查询代码块的开头是 ````dataview`，结尾有 ````，不是普通 Markdown 代码块。
5. 在看板中临时运行最简单的查询：

   ```dataview
   LIST
   FROM "Projects"
   ```

   如果仍为空，检查 Vault 路径或 Dataview 索引；如果能列出文件，再检查任务目录筛选条件。
6. 确认 `otg install` 使用的 Vault 路径就是当前打开的 Vault：

   ```bash
   cat ~/.omp/skills/obsidian-task-runner/config/vault-map.json
   ```

7. 确认任务是否真的已生成：

   ```bash
   find <Vault路径>/Projects -path '*/Tasks/*.md' -type f
   ```

> Dataview 看到了任务，不代表 daemon 一定会执行任务。执行条件仍由 `project`、`assignee`、`status`、依赖字段和两个人工 gate 决定。

## 8. 安全与版本控制建议

- Dataview 查询不会改变 frontmatter；修改状态请在任务文件中编辑，或使用 `otg update-status`。
- 不要把 `~/.omp/skills/.../vault-map.json` 提交到公共仓库，它可能包含本机路径和模型配置。
- 可以使用 Obsidian Git 备份 Vault，但项目代码仓库仍应使用项目自己的 Git 工作流。
- 看板是展示层，不是任务执行器；daemon 日志和任务文档才是排查执行问题的依据。
