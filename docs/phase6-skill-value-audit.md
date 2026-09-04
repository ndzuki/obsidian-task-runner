# Phase 6 全 skill 价值评估报告（第二轮）

> 2026-08-19，在 P0（删 legacy-tools）+ P1/P2（描述更新/精简）之后，对 36 个
> `~/.dsh/skills` 做价值/冗余/合并/删除的完整评估。

## 1. 分组（36 个）

| 组 | skill | 数量 | 价值 |
|---|---|---|---|
| A. obsidian-task-runner 业务核心 | router + refining/round1/round2/merge/priority/conventions/pm/split/design | 10 | 🔴 高（daemon 注入） |
| B. DSH 基础设施 | auto-review-changes, config-reviewer, dsh-upgrade, writing-for-agents, wait-what, wizard, prototype | 7 | 🟢 中高 |
| C. 开发辅助 | connect-framework, diagnosing-bugs, domain-modeling, github-actions-expert, codebase-design, code-review | 6 | 🟢 中 |
| D. 业务通用 | grilling, knowledge-base, kulala-http, model-catalog, requirement-elaborator, research, project-rebaseline, project-scaffold | 8 | 🟢 中 |
| E. 测试/规格 | tdd, test-quality, to-spec, wayfinder | 4 | 🟡 部分可疑 |
| F. 交接/冲突 | handoff, resolving-merge-conflicts | 2 | 🔴 可疑 |

## 2. 删除候选（无价值 / 依赖缺失 / 被覆盖）

| skill | 行数 | 判断 | 依据 |
|---|---|---|---|
| **resolving-merge-conflicts** | 14 | 🔴 删除 | 极简 4 步通用 git 冲突指南；`obsidian-task-runner-merge`（111l，Step 0 完整冲突解决）已覆盖 daemon 场景，DSH 会话的通用冲突可由 AGENTS.md/golang 相关 skill 覆盖。14 行内容价值趋近于零 |
| **to-spec** | 75 | 🔴 删除或改造 | matt-pocock 类：依赖 `setup-matt-pocock-skills` 命令（**DSH 环境不存在**）+ issue tracker（**未配置**），输出目标不可达 |
| **wayfinder** | 128 | 🔴 删除或改造 | 同上（4 处 issue tracker + setup 引用）；与 to-spec 同为"规划"类 |

## 3. 评估候选（需用户判断）

| skill | 判断 | 依据 |
|---|---|---|
| **handoff** | 🟡 评估 | 手动会话交接（存 OS temp）；DSH 有**原生 compaction**（context 满自动压缩）+ goal 持久化，大部分场景被替代。仅"跨 agent 显式交接"场景保留价值 |

## 4. 冗余/合并评估

| 组合 | 关系 | 结论 |
|---|---|---|
| grilling ↔ requirement-elaborator | **调用关系**（elaborator 内部调 grilling 做对齐） | 不合并，互补 |
| to-spec ↔ wayfinder | 都是"规划"（对话→spec vs 大块→决策票地图），且都依赖缺失的 issue tracker | 若保留需合并为一个"规划"skill，去掉 issue tracker 依赖 |
| tdd ↔ golang-testing（~/.agents） | tdd=方法论（红绿重构），golang-testing=实践（table-driven/race/testify） | 侧重点不同，保留（tdd 是 dm=1 用户显式） |
| test-quality ↔ auto-review-changes | test-quality 被 auto-review-changes **auto-invoke** | 互补，保留 |

## 5. 优化候选

| skill | 判断 | 依据 |
|---|---|---|
| **knowledge-base** | 🟡 406l 最大 | 核心价值高（本地优先检索+沉淀），但正文偏长；可拆分"检索"与"沉淀"两块或精简示例 |

## 6. 建议操作（按优先级）

1. **删除 resolving-merge-conflicts**（14l，价值零，被覆盖）——低风险。
2. **删除 to-spec + wayfinder**（matt-pocock 依赖缺失，无法工作）——若未来接入 issue tracker 再重新引入；否则是死 skill。
3. **handoff 改造**——改为 DSH 语义（引用原生 compaction，去掉"存 OS temp"），或删除。
4. **knowledge-base 精简**——可选，价值高但可瘦身。

## 7. 删除后预期

删除 3-4 个（resolving-merge-conflicts + to-spec + wayfinder + 可能 handoff）后，
skill 数 36 → **32-33**，全部为「有明确价值、可工作」的 skill。
