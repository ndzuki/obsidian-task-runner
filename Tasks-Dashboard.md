# 任务总览

## 按项目汇总

```dataview
TABLE 
  length(rows) as "任务数",
  filter(rows, (r) => r.status = "ready").length as "就绪",
  filter(rows, (r) => r.status = "implementing").length as "实现中",
  filter(rows, (r) => r.status = "plan-review").length as "待审阅",
  filter(rows, (r) => r.status = "done").length as "已完成",
  filter(rows, (r) => r.status = "blocked").length as "阻塞"
FROM "Projects"
WHERE contains(file.folder, "Tasks")
GROUP BY regexreplace(file.folder, "Projects/([^/]+)/.*", "$1")
SORT regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") asc
```

## 待处理任务

```dataview
TABLE 
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  priority as "优先级",
  status as "状态",
  assignee as "执行者"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND status != "done" AND status != "blocked"
SORT priority asc
```

## 阻塞任务

```dataview
TABLE 
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  assignee as "执行者",
  file.mtime as "最后更新"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND status = "blocked"
SORT file.mtime desc
```

## 最近完成

```dataview
TABLE 
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
