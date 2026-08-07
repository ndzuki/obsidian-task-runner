# 任务总览

> **状态缩写对照**：<span class="sts-blocked">blocked</span> 阻塞 | <span class="sts-ready">ready</span> 就绪 | <span class="sts-refining">refining</span> 成熟度检查 | <span class="sts-needs-grilling">needs-grilling</span> 追问中 | <span class="sts-planning">planning</span> 规划中 | <span class="sts-plan-review">plan-review</span> 待审阅 | <span class="sts-implementing">implementing</span> 实现中 | <span class="sts-review">review</span> 待合并 | <span class="sts-conflict">conflict</span> 冲突 | <span class="sts-done">done</span> 已完成 | <span class="sts-closed">closed</span> 已关闭 | <span class="sts-wayfinder">wayfinder</span> 待拆分

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

## Stage 看板

```dataview
TABLE
  length(rows) as "任务数",
  length(filter(rows.status, (x) => x = "done")) as "已完成",
  length(filter(rows.status, (x) => x != "done")) as "进行中"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND stage != null AND stage != "" AND status != "closed"
GROUP BY stage as "阶段"
SORT number(replace(stage, "P", "")) asc
```

## 任务概览

```dataview
TABLE length(rows) as "数量"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND (
  (status != "done" AND status != "closed" AND status != "blocked" AND status != "wayfinder") OR
  (status = "done" AND completed != null AND date(completed) > date(today) - dur(7 days))
)
GROUP BY choice(status = "done", "最近完成(7天)", "待处理") as "统计项"
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

## 需求变更待处理

```dataview
TABLE
  file.link as "任务",
  regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目",
  status as "状态",
  priority as "优先级",
  file.mtime as "最后更新"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND pending_req = true AND status != "done" AND status != "closed"
SORT file.mtime desc
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

## 知识库汇总

```dataview
TABLE
  length(rows) as "任务数",
  sum(map(nonnull(rows.knowledge_refs), (x) => length(x))) as "引用知识库次数",
  length(unique(flat(nonnull(rows.knowledge_refs)))) as "引用文档数",
  length(flat(nonnull(rows.adr_written))) as "创新 ADR 数",
  length(filter(rows.knowledge_applied, (x) => x != null and x != "")) as "知识应用任务数"
FROM "Projects"
WHERE contains(file.folder, "Tasks")
GROUP BY regexreplace(file.folder, "Projects/([^/]+)/.*", "$1") as "项目"
SORT 项目 asc
```

## ADR 提议状态

```dataview
TABLE
  length(rows) as "待授权 ADR 提议任务数"
FROM "Projects"
WHERE contains(file.folder, "Tasks") AND length(adr_proposed) > 0 AND adr_approved != true AND status != "done"
GROUP BY "ADR 提议" as "统计项"
```
