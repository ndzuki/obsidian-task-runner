# Step 0 / 0.5 / 0.6 / 0.7：知识回流、会话沉淀、热度与提炼

> `knowledge-base` 的按需参考（2026-09-18 从 SKILL.md 拆出）。需要时再 `read`，不要预加载。

---

### Step 0: 项目知识提取 — 回流到知识库（手动路径）

> **原自动触发已随 otg 流水线停用（2026-09-14）**：daemon 在 `merge→done` 时自动调用
> `ExtractTaskKnowledge` / `EnsureADRTags` / `classifyADR` / `ReclassifyUncategorized` /
> `AppendFailurePattern` 的机制**不再运行**。本节一律由 agent 按需执行。

**触发**：用户说「沉淀项目经验」/「提取知识」，或你在项目里发现了值得复用的经验。

**执行**：扫描 `Projects/<项目>/` 下的 ADR / `CONTEXT.md` / REQ / TASK 记录，按下表提取。
分类时按知识库自身的 `topics`/`aliases`/`tags` 词表匹配既有主题文档（优先级：tag 精确命中 >
多关键词 > 长精确词 ≥4 字节；单通用短词 go/ci/sdk 不写入，防污染）；无匹配则新建文档或
归档 `References/uncategorized/<id>.md`（标准 frontmatter，纳入 INDEX 可检索）。

**提取内容**：

| 来源 | 提取目标 | 知识库路径 |
|---|---|---|
| `Notes/adr/ADR-*.md` | 架构决策：技术选型、取舍理由、约束 | `References/<domain>/` 对应分类 |
| `Notes/CONTEXT.md` | 领域词汇、反模式、约束 | 追加到对应 Reference Map 条目 |
| `Requirements/REQ-*.md` 详细规格 | 技术栈、框架版本、集成方案 | 更新对应 References 文件的版本和验证状态 |
| TASK `## 实现记录` | 解决方案、实践细节 | 追加 `## 实践经验` 小节 |
| TASK `## 踩坑记录` | 失败方案+根因+成功方案（试错换方案的负向经验） | 追加对应文档「踩坑实践」小节；未命中归档 `References/uncategorized/` |
| TASK `## 验收记录` | 验证通过的技术决策 | 标记 `verified: true` |

**提取规则**：
1. 只提取有复用价值的技术知识，不复制业务逻辑或一次性需求。
2. ADR 决策写为 2-3 句话摘要 + 链接回原 ADR。
3. 已验证（`verified: true`）的知识优先于未验证的同主题内容。
4. 同一主题多条项目经验 → 追加 `## 实践经验` 小节，标注项目、日期和验证状态。
5. 跨项目发现的共同模式（如"三个项目都选了 Connect + Wire"）→ 在知识文件中标注为强推荐模式。

> 写入前执行与 Step 4 相同的 5 项强制校验（见下方"强制校验规则"）。

### Step 0.5: 交互会话经验沉淀（日常 DSH 会话）

任务管道之外的日常会话（用户直接对话、非 TASK 驱动的调试/试错）同样会积累"以为方案 X 对 → 失败 → 换 Y 成功"的经验。**经验发生时立即沉淀，不等会话结束、不依赖记忆**：

1. **试错换方案**（踩坑）→ `otg kb absorb`，stdin 传踩坑格式：
   ```bash
   otg kb absorb --project <项目名或 daily> <<'EOF'
   ### {YYYY-MM-DD}: {现象一句话}
   - 现象: {观察到的失败行为}
   - 失败方案: {尝试过但不成立的方案与失败证据}
   - 根因: {失败原因分析}
   - 成功方案: {最终生效的方案}
   - 相关文档: {References 相对路径，可选，帮助分类}
   EOF
   ```
2. **项目/会话经验总结**（自由文本）→ `otg kb absorb --summary`：
   ```bash
   otg kb absorb --project <项目名> --summary <<'EOF'
   {自由文本：技术栈验证结论、踩坑要点、最佳实践}
   EOF
   ```
3. **去重由命令保证**：相同（归一化）标题或失败方案已在目标文档 → 输出 `duplicates: N` 并跳过，不重复追加——同一教训被多个会话/任务重复记录不会膨胀索引和 token 消耗。
4. **索引与向量刷新**：`otg kb absorb` 与 `otg kb promote` 会**自动**重建 INDEX.md 并增量刷新 embedding 向量（未变文档跳过，<1 秒；embedding 后端不可用时仅告警，BM25 检索不受影响）——记录即检索。`otg kb hit` 只改热度计数，不触发向量刷新（hits 不参与 embedding）。⚠️ **daemon watcher 已停用**：用 `write`/`edit` **直接改 `References/`** 不会自动重建，必须手动 `otg kb rebuild-index` + `otg kb index`。
5. **模型切换自动失效**：向量库记录 embedding 模型；切换后端/模型（本地 ollama ↔ 云 OpenAI 兼容）后旧向量视为无效，`kb search` 回退 BM25 并提示重跑 `otg kb index`——不同模型向量维度不兼容，绝不混用。

### Step 0.6: 经验热度与 core 升级

知识文档 frontmatter 的 `hits` 是**成功应用热度**——每次成功应用 +1，检索排序获得小加成（每个 hit ≈ 0.02 BM25 分），让高频复用经验排在冷门匹配之前：

| 触发 | 机制 |
|------|------|
| `otg kb absorb` 遇到已记录教训（duplicate） | 自动 bump——同一教训反复出现本身就是热度信号 |
| 交互会话应用知识文档成功后 | `otg kb hit <ref-path>` 手动 bump |

> 原「任务 merge 命中 `knowledge_refs` → daemon 自动 bump」已随流水线停用。

**core 升级**：`hits ≥ 3` 且位于 `extended/` 的文档用 **`otg kb promote`** 移入 `core/`（同子目录）——经验复用热度达标即进入核心检索层，配合 core → extended → archived 的逐级检索让高热度经验最先被找到。目标路径已存在同名文档时不自动合并（跳过并保留 extended 原档）。

**提问即检索**：任何用户提问/需求（含日常交互会话），先按关键字 `otg kb search` 检索知识库，命中案例的「实践经验/踩坑实践」小节直接作为解决方案输入；应用成功后按上表提升热度——知识库随使用持续自排序。

### Step 0.7: 会话结束知识提炼（自动委派）

交互会话结束（用户 Ctrl+D / `session_stop` 事件）时，**若会话含可复用经验，自动提炼入库**——把"一次性对话"变成"可检索资产"，同一经验不被下次会话重新踩：

**触发**：
- **自动**：dsh 插件 `kb-distill.mjs`（`~/.dsh/plugins/`，由独立工作区维护、非本仓库部署）监听会话结束/空闲超时钩子，满足条件（会话有实质工作 + 达到长度阈值，含**工具调用证据门禁**：`tool/call` 事件 / `tool_use`·`tool_call`·`tool_result` 内容块）时注入提炼指令。**每个有实质工作的主会话都触发一次**（不再做「同日一次」的跨会话去重——多轮 `/new` 会话各自沉淀，重复内容由 `otg kb absorb` 内置归一化去重兜底）。「有实质工作」含**工具调用证据门禁**（2026-09-02 D2）：会话出现工具调用（`tool/call` 事件 / `tool_use`·`tool_call`·`tool_result` 内容块）才触发提炼，纯聊天的多消息会话不再白跑一轮 subagent 判空；误判方向保守（探针只匹配结构化字段中的工具名，不因闲聊文本触发，漏知识不可接受）。
- **手动**：用户说"提炼本次会话"/"沉淀经验"时立即执行；会话中途经验显著时也可即时执行（不必等结束）。

**执行流程（收到提炼指令后）**：
1. **委派 subagent 分析**（推荐，省主会话 token）：`task` 委派 scout/task 读取会话转录（`history://<id>` 或会话文件），提取：踩坑（现象/失败方案/根因/成功方案）、验证结论（实测数据）、架构决策（选型与取舍）。
2. **入库**：踩坑经验 → `otg kb absorb`（踩坑格式，内置归一化去重）；验证结论/架构决策 → 追加对应 References 文档「实践经验」小节或新建主题文档（标准 frontmatter）。⚠️ 直接 `write`/`edit` 写入的需手动 `otg kb rebuild-index` + `otg kb index`；走 `absorb` 的会自动重建。
3. **判空**：无可复用知识 → 回复「无可提炼」并结束，不硬造知识。
4. **幂等**：absorb 对重复标题/失败方案自动跳过——同一教训多会话记录不膨胀索引。

**提炼质量要求**：只收可复用技术知识（含失败方案与根因），不复制业务琐事；实测数据标注日期与语料规模；`verified` 仅实践验证后翻 true。
