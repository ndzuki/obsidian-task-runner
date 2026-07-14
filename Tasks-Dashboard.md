# 任务总览

## 项目任务数

```dataviewjs
const tasks = dv.pages('"Projects"').where(p => p.file.folder.includes("/Tasks/"));
dv.table(
  ["项目", "任务数", "就绪", "实现中", "待审阅", "已完成", "阻塞"],
  dv.array(tasks)
    .groupBy(p => p.file.folder.split("/")[1])
    .map(g => [
      g.key,
      g.rows.length,
      g.rows.filter(r => r.status === "ready").length,
      g.rows.filter(r => r.status === "implementing").length,
      g.rows.filter(r => r.status === "plan-review").length,
      g.rows.filter(r => r.status === "done").length,
      g.rows.filter(r => r.status === "blocked").length,
    ])
    .sort(g => g[0])
);
```

## 待处理任务

```dataviewjs
const all = dv.pages('"Projects"').where(p => p.file.folder.includes("/Tasks/") && p.status !== "done" && p.status !== "blocked");
dv.table(
  ["项目", "标题", "优先级", "状态", "执行者"],
  all.sort(p => p.priority).map(p => [
    p.file.folder.split("/")[1],
    p.file.link,
    p.priority,
    p.status,
    p.assignee
  ])
);
```

## 阻塞任务

```dataviewjs
const blocked = dv.pages('"Projects"').where(p => p.file.folder.includes("/Tasks/") && p.status === "blocked");
dv.table(
  ["项目", "标题", "执行者", "最后更新"],
  blocked.sort(p => p.file.mtime, "desc").map(p => [
    p.file.folder.split("/")[1],
    p.file.link,
    p.assignee,
    p.file.mtime
  ])
);
```

## 最近完成

```dataviewjs
const done = dv.pages('"Projects"').where(p => p.file.folder.includes("/Tasks/") && p.status === "done");
dv.table(
  ["项目", "标题", "完成时间", "执行者"],
  done.sort(p => p.completed, "desc").limit(10).map(p => [
    p.file.folder.split("/")[1],
    p.file.link,
    p.completed,
    p.assignee
  ])
);
```

## 项目记忆

```dataviewjs
const mem = dv.pages('"Projects"').where(p => p.file.name === "memory.md");
dv.list(mem.sort(p => p.file.folder).map(p => p.file.link));
```
