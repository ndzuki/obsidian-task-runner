# 运行前提与运行环境注意事项

> `knowledge-base` 的按需参考（2026-09-18 从 SKILL.md 拆出）。需要时再 `read`，不要预加载。

---

## 能力边界：全部走 CLI，不依赖后台进程

`otg` 二进制位于 `~/.local/bin/otg`。以下子命令**都是普通 CLI，直接调即可**：

| 子命令 | 用途 |
|---|---|
| `otg kb search` | BM25 + 可选向量的混合检索（Step 1 主路径） |
| `otg kb ask` | 检索 top-k + 小模型 grounded 回答（交互概览用） |
| `otg kb absorb` | 把踩坑/经验写入知识库（**并自动重建 INDEX 与检索库**） |
| `otg kb hit` | 应用热度 +1 |
| `otg kb promote` | `hits ≥ 3` 的 `extended/` 文档移入 `core/` |
| `otg kb rebuild-index` | 从 frontmatter 重建 `References/INDEX.md` |
| `otg kb index` | 重建检索库（FTS + 可选向量） |
| `otg kb usage` / `gaps` | 引用关系 / 知识缺口 |

## otg daemon 与任务流水线已停用（2026-09-14）

实测 `otg status` → `daemon lock: unknown`（未运行），且无任何 otg 进程在跑。

**已不再发生**：`merge→done` 自动知识提取、`ExtractTaskKnowledge`、`EnsureADRTags`、
`classifyADR`、`ReclassifyUncategorized`、`AppendFailurePattern`、round1/round2 阶段流水线、
`find-ready` / `stage-plan` 任务编排。

**影响与替代**：
- 知识回流**全部改手动**：踩坑 → `otg kb absorb`；结论/架构决策 → 追加 References 文档；
  热度 → `otg kb hit`；升级 → `otg kb promote`。
- **直接 `write`/`edit` 改 `References/` 后不会自动重建索引** → 手动
  `otg kb rebuild-index` + `otg kb index`。（经 `absorb` 写入的会一并重建，无需额外操作。）
- `verified: true` 只能人工核对验收记录后翻转。

## 交互会话的自动注入来自 dsh 插件，不是 otg daemon

每会话首条消息前的 KB-first 注入由 **`~/.dsh/plugins/kb-preflight.mjs`**（dsh 原生
`agent/pre-step` seam）完成，**与 otg daemon 无关**：

1. **项目上下文**：会话 cwd 命中 `vault-map.json` 已注册项目（或 vault `Projects/<dir>`）时，
   注入 `Notes/CONTEXT.md` / `Notes/adr/` / `PROJECT-CONVENTIONS.md` 的紧凑摘要 + 路径；
   空项目额外注入 `<project_knowledge_duty>` 义务块（出现首批记录后自动撤下）。
2. **KB-first 预检**：命中缓存 → 注入 top-N；未命中 → 只注入 `References/INDEX.md` 摘要，
   并在后台异步 spawn `otg kb search` 预热缓存（首问不因检索子进程/embedding 推理而变慢）。

会话结束的提炼由 **`kb-distill.mjs`** 负责（调 `otg kb absorb`），同样不依赖 daemon。
两者都在 `~/.dsh/cordis.patch.yml` 中加载。

## 故障排查

- 检索报 `enable WAL: unable to open database file` → home 只读（沙箱）导致的写失败，
  **不是知识库损坏**：把 `kb.sqlite` 拷到可写路径，再用 `--db` 指过去即可正常检索。
- 库路径**一律以 `vault-map.json` 的 `kb_db` 为准**（本机 `~/.dsh/storages/otg/kb.sqlite`）。
  `~/.local/share/otg/kb.sqlite` 是历史遗留的废弃副本，勿照抄。
- 核对"库是否最新"：`kb_docs` 行数 == `find References -name '*.md' ! -name 'INDEX.md' | wc -l`。
- ⚠️ `~/.dsh/skills/obsidian-task-runner/config/vault-map.json` 是指向
  `~/.dsh/config/vault-map.json` 的**符号链接**，otg 二进制硬编码该默认候选路径——**勿删**。
