# 任务总览

> 从文件路径提取项目名，按 `project_id` 聚合。Dataview 插件自动刷新。

## 按项目汇总

```dataview
TABLE 
  length(rows) AS "任务数",
  length(filter(rows, (r) => r.status = "ready")) AS "就绪",
  length(filter(rows, (r) => r.status = "implementing")) AS "实现中",
  length(filter(rows, (r) => r.status = "plan-review")) AS "待审阅",
  length(filter(rows, (r) => r.status = "review")) AS "待合并",
  length(filter(rows, (r) => r.status = "done")) AS "已完成",
  length(filter(rows, (r) => r.status = "blocked")) AS "阻塞"
FROM "Projects"
FLATTEN regexreplace(file.folder, "^Projects/(\\d+)-.*$", "$1") AS project_id
FLATTEN regexreplace(file.folder, "^Projects/[^/]+/([^/]+)/.*$", "$1") AS category
WHERE project_id AND category = "Tasks"
GROUP BY project_id
SORT project_id ASC
```

## 待处理任务（按优先级）

```dataview
TABLE 
  regexreplace(file.folder, "^Projects/([^/]+)/.*$", "$1") AS "项目",
  priority AS "优先级",
  status AS "状态",
  assignee AS "执行者",
  due_date AS "截止"
FROM "Projects/Tasks" OR "Projects"
FLATTEN regexreplace(file.folder, "^Projects/([^/]+)/.*$", "$1") AS project
WHERE contains(file.folder, "/Tasks/") AND status != "done" AND status != "blocked"
SORT priority ASC, project ASC
```

## 阻塞任务

```dataview
TABLE 
  regexreplace(file.folder, "^Projects/([^/]+)/.*$", "$1") AS "项目",
  assignee AS "执行者",
  file.mtime AS "最后更新"
FROM "Projects"
WHERE contains(file.folder, "/Tasks/") AND status = "blocked"
SORT file.mtime DESC
```

## 最近完成

```dataview
TABLE 
  regexreplace(file.folder, "^Projects/([^/]+)/.*$", "$1") AS "项目",
  completed AS "完成时间",
  assignee AS "执行者"
FROM "Projects"
WHERE contains(file.folder, "/Tasks/") AND status = "done"
SORT completed DESC
LIMIT 10
```

## 我的任务

```dataview
TABLE 
  regexreplace(file.folder, "^Projects/([^/]+)/.*$", "$1") AS "项目",
  status AS "状态",
  priority AS "优先级"
FROM "Projects"
WHERE contains(file.folder, "/Tasks/") AND assignee AND status != "done"
SORT priority ASC
```

## 项目记忆

```dataview
LIST
FROM "Projects"
WHERE file.name = "memory.md"
SORT file.folder ASC
```
