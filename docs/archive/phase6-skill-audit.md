# Phase 6 skill 审计报告

> 2026-08-19，审查 37 个 `~/.dsh/skills` 中自建/迁移 skills 的 frontmatter、
> 描述质量、早期执行器残留与结构化输出契约。参考 `skill://config-reviewer` 与
> `skill://writing-for-agents` 标准。

## 1. 扫描概况

- 共 **37** 个 skill（35 个早期执行器迁移 + `dsh-upgrade` + `obsidian-task-runner-design`）。
- 全部为按需加载（DSH 无 `alwaysApply` 概念，靠 `disable-model-invocation` /
  `user-invocable` / `hide` 控制调用面）。
- 每会话固定开销：`~/.dsh/AGENTS.md`（86 行）+ skill catalog 列表（37 个
  name+description），约 **~1200 tokens**，在可接受范围。

调用面分布：
- `disable-model-invocation: true`（仅 daemon/用户显式，模型不自动加载）12 个：
  phase skills（round1/round2/merge/priority/conventions/design）+ dsh-upgrade、
  handoff、tdd、test-quality、to-spec、wayfinder。
- `hide: true`（不在目录展示）7 个：auto-review-changes + 6 个 phase skills。

## 2. 发现

### 🔴 严重（必须修复）

| # | skill | 问题 | 影响 |
|---|---|---|---|
| 1 | **legacy-tools** | 整个 skill 是 early-executor 工具集：正文引用 DSH 不存在的工具 `recipe`/`browser`/`lsp`/`ast_edit`/`rename_file`/`debug`，且引用早期执行器配置路径 `~/.dsh/agent/`、`早期执行器 --version`、版本敏感表（17.2.x） | `dm=0`（模型可加载）→ 模型加载后**尝试调用不存在的工具**，执行失败；描述的是已弃用的早期执行器工具链 |

### 🟡 警告（建议修复）

| # | skill | 问题 | 影响 |
|---|---|---|---|
| 2 | obsidian-task-runner（主） | 正文 8+9 处 `早期执行器` 机制描述（"早期执行器会话写回"、"fallback_models 重启早期执行器"、"早期执行器 exit"、"20+ 早期执行器会话"）——daemon 已默认切 dsh（5.9） | 文档过时，非执行错误；误导后续维护 |
| 3 | obsidian-task-runner-round2 / merge | 各 2-4 处早期执行器描述 | 同上 |
| 4 | knowledge-base / kulala-http / writing-for-agents / config-reviewer | 1-10 处 `早期执行器` 引用，多为示例/历史描述 | 轻微 |
| 5 | obsidian-task-runner-pm | description 极长（中文详细流程），每次触发全量加载 | description 应精简为触发词 + 一句职责 |
| 6 | project-rebaseline | description 含完整流程（停 daemon→对齐→核验→…） | description 过重 |

### 🟢 信息（供参考）

| # | 项 | 说明 |
|---|---|---|
| 7 | 结构化输出契约 | priority/audit 依赖 strict JSON，dsh headless 无 strict 输出（§5.6 已知）——extractJSON 兜底已实现，但非严格契约 |
| 8 | 单一事实源 | skills manifest + install.go + sync-早期执行器-skills 已一致（design 已入 manifest，37 个） |
| 9 | 触发词质量 | 多数 description 前置核心词 + 明确触发词，符合 writing-for-agents；个别（pm/rebaseline）过长 |

## 3. 优化方案（按优先级）

### P0 — legacy-tools 处理（唯一执行风险）

**选项 A（推荐）：删除** —— `~/.dsh/AGENTS.md` 已内置早期执行器→DSH 工具映射表
（recipe→make/just、browser→web_search、lsp/ast_edit/rename_file→grep/read+edit/write、
debug→bash 复现），legacy-tools 冗余且描述不存在的东西。删除后模型不会再被误导
调用 recipe/browser/lsp 等工具。

**选项 B：改造** —— 改为「DSH 工具纪律」精简版（只留读/写两档纪律 + 冒烟清单），
删除所有早期执行器工具引用 + 版本敏感表 + 早期执行器配置路径。

### P1 — obsidian-task-runner 系列早期执行器描述更新

把主 skill + round2 + merge 里的 `早期执行器` 机制描述改为 `dsh headless`：
- "早期执行器会话写回" → "执行会话写回"
- "fallback_models 重启早期执行器" → "fallback 链（DSH fallback.mjs）"
- "早期执行器 exit" → "执行器退出"
- 删除早期执行器 config.yml / watchEmptyStops（已废弃）相关描述

### P2 — description 精简

- obsidian-task-runner-pm、project-rebaseline 的 description 收敛为「触发词 + 一句职责」，
  详细流程留在正文。

### P3 — 结构化输出契约（随 embed 一并解决）

priority/audit 的 strict JSON 在 dsh headless 下依赖 extractJSON 兜底；embed
（方案 C）后可用 DSH 原生 structured output，届时移除兜底或保留双路径。

## 4. 建议操作顺序

1. **P0**：删除或改造 legacy-tools（消除唯一执行风险）。
2. **P1**：obsidian-task-runner 系列早期执行器描述更新（文档对齐 dsh 现实）。
3. **P2**：两个过长 description 精简。
4. **P3**：随 embed 迁移解决 strict JSON 契约。

> 注：legacy-tools 的删除/改造涉及用户工作流，需确认后再执行（本报告仅提议）。
