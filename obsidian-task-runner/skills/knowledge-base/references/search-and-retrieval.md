# Step 1 本地检索实现细节 与 Step 3 验证

> `knowledge-base` 的按需参考（2026-09-18 从 SKILL.md 拆出）。需要时再 `read`，不要预加载。

---

### Step 1: 本地检索
0. **先跑语义检索（本地 BM25）**：`otg kb search "<关键词>"`（vault-map 自动定位）— 输出按相关度排序的文档路径/摘要。命中 top-3 内即视为本地命中。性能说明：检索库为 SQLite 单文件，**路径以 `vault-map.json` 的 `kb_db` 为准**
（本机为 `~/.dsh/storages/otg/kb.sqlite`，vault 外）。
⚠️ **不要照抄任何写死的路径**：`~/.local/share/otg/kb.sqlite` 是**历史遗留的废弃副本**
（2026-09-17 实测该文件冻结在 09-09、只含 112 篇，缺全部新文档；照它判断会误诊成
"文档没被索引"）。核对"库是否最新"一律用 `vault-map.json` 的 `kb_db` 指向的那个文件，
并以 `kb_docs` 行数是否等于 `find References -name '*.md' ! -name 'INDEX.md' | wc -l` 为准。
其余：FTS5 BM25 倒排索引重复查询亚秒级，向量（sqlite-vec）可用时自动混合余弦，embedding 不可用自动回退纯 BM25；文档/向量按 content_hash 增量同步（`kb absorb`、merge 提取、`kb promote` 后自动），未变文档零成本；`archived/` 层默认不检索，确需时加 `--archived`。`kb index` 全量重建（迁移/模型切换后执行）。**ollama 停用不影响检索**：停掉后立即降级纯 BM25（毫秒级，不报错），重新启动后首次查询稍慢（模型加载），向量自动补齐——检索差异详见 README「检索模式与 ollama 依赖（实测）」：术语型关键词查询无感知差异，近义/口语化查询（如「链路追踪」无词面命中）依赖向量层。混合检索实现：余弦侧双路有界候选（BM25 top-N 进程内重排 + vec0 全局有界 K 保纯向量召回），配置项 `chunk_chars`/`batch_size`/`knn_candidates`/`weight` 见 README「知识库语义检索」。
0a. **交互问答可用 `otg kb ask`（可选，人类/会话入口，非自动化主路径）**：vault-map 配 `kb_chat` 后，`otg kb ask "<问题>"` 混合检索 top-k 以 `[N]` 编号拼入 prompt，由 chat 模型（如 ollama `qwen3:1.7b`）流式回答并附「参考资料」列表（实际检索结果，模型不能编造来源）。**定位边界**：ask 适合**用户提问与交互会话**（快速 grounded 回答、低 token 概览）；**agent 自动化流程（Round 1/Round 2 计划引用）仍走 Step 0 的 search + read 原文**——小模型转述有信息损耗且计划需引用原文路径，禁止用 ask 替代原文检索。配 `kb_rerank`（如 llama.cpp `bge-reranker-v2-m3`，长尾/近义查询收益最明显；后端不可用自动降级）后，`kb search` 与 `kb ask` 均先取 top-N 精排再截断。详细配置与部署见 README「检索精排」与「知识库问答」。
1. 读取 `$OBSIDIAN_VAULT/References/INDEX.md` 获取知识库目录（作为关键词检索与引用项目视图的补充）。
2. **关键词构造（多轮扩展）**：
   - 从问题/REQ 提取技术名词与实体（含中文表述）；
   - 与 INDEX `topics`/`aliases` 列匹配时**同时尝试**：同义词（k8s↔kubernetes、容器↔docker）、中英文（State Machine↔状态机）、缩写（CI↔持续集成）、主题词变体（grpc↔connect）；
   - 用「引用项目」列辅助：问题来自某项目时，优先该项目引用过的文档（已被验证的上下文）。
3. 命中后 `read` 对应文件的相关章节（不要全量加载大文件）；多候选时先读摘要行，按 verified → activity → 相关性排序深入。
4. 一轮未命中 → **迭代**：换同义词/上位词再跑 `otg kb search` 一次；仍无 → Step 2 外部搜索。
5. 若本地知识足以回答 → 直接回答，引用来源文件路径，结束。

### Step 3: 验证（条件执行）

对涉及命令、API、版本号的外部知识，优先执行轻量验证：

- CLI 工具版本：`<tool> --version` 或 `<tool> version`
- API 参数：搜索官方 pkg.go.dev / docs 确认
- 配置语法：对照官方 schema 或 example 仓库

验证失败的标注 `置信度: low`，仅作为参考。
