# 任务总览

> **状态缩写对照**：`blocked` 阻塞 | `ready` 就绪 | `refining` 成熟度检查 | `needs-grilling` 追问中 | `planning` 规划中 | `plan-review` 待审阅 | `implementing` 实现中 | `review` 待合并 | `conflict` 冲突 | `done` 已完成 | `closed` 已关闭 | `wayfinder` 待拆分

## 按项目汇总

```dataview
TABLE
  length(rows) as "任务数"
FROM "Projects"
WHERE contains(file.folder, "Tasks")
GROUP BY regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目"
SORT 项目 asc
```

## 按状态统计

```dataview
TABLE rows.file.link as "任务"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND status != "done" AND status != "closed"
GROUP BY status
SORT status asc
```

## 待处理任务

```dataview
TABLE
  file.link as "任务",
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  priority as "优先级",
  status as "状态",
  assignee as "执行者"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND status != "done" AND status != "closed" AND status != "blocked" AND status != "wayfinder"
SORT priority asc
```

## 等待审批

```dataview
TABLE
  file.link as "任务",
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  status as "状态",
  plan_version as "计划版本",
  priority as "优先级"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND (
  (status = "plan-review" and plan_approved != true) OR
  (status = "review" and merge_approved != true)
)
SORT status asc, priority asc
```

## 阻塞任务

```dataview
TABLE
  file.link as "任务",
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  blocked_by as "依赖",
  assignee as "执行者",
  file.mtime as "最后更新"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND status = "blocked"
SORT file.mtime desc
```

## 最近完成

```dataview
TABLE
  file.link as "任务",
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  completed as "完成时间",
  assignee as "执行者"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND status = "done"
SORT completed desc
LIMIT 10
```

## 领域上下文

```dataview
TABLE
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  file.link as "CONTEXT.md",
  file.mtime as "最后更新"
FROM "Projects"
WHERE file.name = "CONTEXT.md"
SORT file.folder asc
```

## ADR 提议状态

```dataview
TABLE
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  adr_proposed as "提议 ADR",
  adr_approved as "已授权",
  status as "任务状态"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND adr_proposed != null AND adr_approved != true AND status != "done"
SORT file.mtime desc
```

## 依赖阻塞详情

```dataview
TABLE
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  blocked_by as "等待",
  status as "状态",
  priority as "优先级"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND blocked_by != null AND status != "done"
SORT priority asc
```

## 架构决策记录

```dataview
TABLE
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  file.link as "ADR",
  file.mtime as "最后更新"
FROM "Projects"
WHERE contains(file.folder, "adr")
SORT file.folder asc, file.name asc
```
