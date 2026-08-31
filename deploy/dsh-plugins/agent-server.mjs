/**
 * agent-server — DSH 长驻 Agent RPC 服务（obsidian-task-runner embed 地基）。
 *
 * WHY: spawn 模式（`dsh --profile headless <task>`）每阶段启动一个短命进程，
 * 且 headless 无 `--thinking`/`--resume`，导致：推理强度无法 per-阶段传递、
 * 无 durable resume、每阶段重复启动开销。本插件暴露一个长驻 HTTP 服务，
 * Go daemon 通过 `POST /agent/run` 复用同一个 DSH agent runtime，并把
 * reasoningEffort + sessionId 作为请求参数传入。
 *
 * 实现约束：本插件位于 ~/.dsh/plugins/（不在 DSH node_modules 解析路径），
 * 因此**不 import 任何 @deepseek-ai/* 包**——与 fallback.mjs 一致，只用：
 *   - ctx.get('agents')             → agents.create/resume/list
 *   - ctx.on('agent/request', ...)  → 注入 reasoningEffort（prepend 最外层）
 *   - 直接构造消息对象（SessionId/MessageId 均为 compile-time no-op brand）
 *
 * reasoningEffort 传递：AgentOptions 只含 provider/model（buildRequest 只读
 * options.provider/model），reasoningEffort 只从 persistedConfig 或
 * agent/request waterfall 注入——本插件用后者的 prepend 监听，把 per-请求
 * 的 effort 写进 request config，完整还原 omp 的 --thinking per-阶段语义。
 *
 * config（profile cordis.patch.yml 的 agent-server 行）：
 *   port: 8799        # 监听端口
 *   host: 127.0.0.1   # 仅回环，避免暴露
 *
 * RPC 契约：
 *   GET  /health                      → 200 { ok: true }
 *   GET  /agents                      → 200 [ { sessionId, phase, task, project, taskId, status, taskStatus, elapsed, lastEventAt, seq } ]
 *        （仅活跃会话：进行中的 run + 存活中的 chat；已完成的 run 会话不出现在
 *        列表中，其数量经 x-agents-finished 响应头暴露给监控面板。
 *        lastEventAt/seq 供 daemon 超时判定：近期有事件 → timeout_active
 *        继续等；长时间无事件 → wedged 会话 cancel。
 *        taskId 为 daemon 派发时携带的精确任务标识——daemon 重启后 fresh Start
 *        前据此把同一任务上一代 daemon 残留的 working 会话 cancel 掉，保证同一
 *        任务同一时刻只有一个活跃写者（task 是从 prompt 正则推导的展示标签，
 *        只作 taskId 缺失时的回退匹配））
 *   GET  /monitor（或 /）             → agent-monitor.html 监控面板
 *   POST /agent/run  body: { task, provider, model, reasoningEffort?, sessionId?, status?, taskId?, toolPolicy? }
 *     → 200 { text, outcome, sessionId, errorCode?, error? }
 *     outcome: completed | error | timeout | context_window | quota | key_unavailable | interrupted | tool_policy_violation
 *     （error 字段承载失败详情消息；errorCode 为分类码，两者可都缺省。
 *     toolPolicy="read,grep,glob,bash" 等白名单：只读审查会话注入硬约束
 *     preamble，且事后校验白名单外的 tool/call → tool_policy_violation）
 *   POST /agent/chat body: { message, provider, model, reasoningEffort?, sessionId?, kbQuery?, project? }
 *     → 200 { text, outcome, sessionId, errorCode?, error? }（多轮交互）
 *     kbQuery 为可选的精准查询词（如任务标题）；project 为当前工作区项目名
 *     （命中 <vault>/Projects/<dir> 时注入项目上下文 CONTEXT/ADR/规范）。
 *     全新会话（sessionId 空）时服务端先做项目上下文 + KB-first 预检索并注入首条消息。
 *   POST /agent/close body: { sessionId } → 200 { ok: true }（取消交互会话）
 */
import { createServer } from "node:http"
import { randomUUID } from "node:crypto"
import { readFileSync, existsSync, statSync, readdirSync } from "node:fs"
import { execFile } from "node:child_process"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

export const name = "agent-server"
export const inject = ["agents"]

const ALLOWED_KEYS = new Set(["port", "host"])

/** 交互会话空闲回收窗口：客户端死亡（tab 被直接关闭）后不再有 /agent/close，
 * 超过该时长的 chat 会话在 /agents 枚举时惰性取消，避免僵尸条目永久驻留。 */
const CHAT_IDLE_DISPOSE_MS = 30 * 60 * 1000

/** run 会话完成后的内存保留窗口：daemon 在中断/超时后会带同一 sessionId
 * 再次 /agent/run（durable resume），保留窗口内可 re-attach 到热会话；
 * 超时后 dispose 回收 registry/事件内存（resume 仍可从持久层重新加载）。 */
const RUN_DISPOSE_MS = 10 * 60 * 1000

/** 监控面板「最近完成」计数窗口：仅统计该窗口内完成的 run 会话。 */
const RUN_FINISHED_TRACK_MS = 60 * 60 * 1000

const PLUGIN_DIR = dirname(fileURLToPath(import.meta.url))

/** 聚合最后一个 assistant text 与 turn 结果（对齐 dsh-headless 的 summarize）。 */
function summarize(events, firstSeq) {
  let started = false
  let text = ""
  let reason
  for (const event of events) {
    if (event.seq < firstSeq) continue
    if (event.type === "turn/start") {
      started = true
      continue
    }
    if (!started) continue
    if (event.type === "assistant/message") {
      const joined = event.data.message.content
        .filter((block) => block.type === "text")
        .map((block) => block.text)
        .join("")
      if (joined !== "") text = joined
    }
    if (event.type === "turn/end") reason = event.data.reason
  }
  return { text, reason }
}

/** 把 turn reason 映射为 RPC outcome 契约。 */
function mapOutcome(reason) {
  if (reason?.kind === "completed") return "completed"
  if (reason?.kind === "error") {
    const code = reason.error?.code
    if (code === "TIMEOUT") return "timeout"
    if (code === "CONTEXT_WINDOW_EXCEEDED") return "context_window"
    if (code === "QUOTA") return "quota"
    if (code === "INVALID_CREDENTIAL") return "key_unavailable"
    return "error"
  }
  if (reason?.kind === "cancelled" || reason?.kind === "interrupted") return "interrupted"
  return "error"
}

/** 工具政策（toolPolicy）支持：daemon 对只读审查会话（conventions/audit）
 * 传 "read,grep,glob,bash" 等允许工具白名单。这里做两层执行：
 *  1. prompt 注入：政策作为最高优先级约束块前置到任务文本；
 *  2. 事后强校验：会话事件中出现白名单外的 tool/call 即判定违规，
 *     outcome=tool_policy_violation（Go 侧映射为会话失败，禁止接受产物）。 */
function parseToolPolicy(policy) {
  if (typeof policy !== "string" || policy.trim() === "") return null
  const allowed = new Set(policy.split(",").map((t) => t.trim()).filter((t) => t !== ""))
  if (allowed.size === 0) return null
  return allowed
}

function toolPolicyPreamble(policy) {
  const tools = [...parseToolPolicy(policy)].join(", ")
  return `<tool_policy>\n本会话是受限工具会话。只允许使用以下工具：${tools}。\n` +
    `严禁调用任何写工具（edit/write/str_replace_editor 等）——调用即违规。\n` +
    `唯一允许的写入是会话契约明确指定的产物文件；其余一律只读。\n` +
    `违反本政策 = 会话失败，产出作废。\n</tool_policy>\n\n`
}

/* ---------------------------------------------------------------------------
 * 交互会话 KB-first（本地优先，零豁免的落地实现）。
 *
 * 目标：把 knowledge-base skill 的「任何工作会话开始先查本地知识库」从
 * vault 内自动化任务扩展到**所有 /agent/chat 交互会话**（grilling、未来的
 * web 聊天、临时需求解决）——即便会话不属于任何 obsidian vault 项目，也
 * 先带问题查全局共享知识库，减少思考摸索与踩坑。
 *
 * 折中方案（确定性 + 自适应）：
 *  - 服务端预检索：新会话启动时 spawn `otg kb search --json`（复用 Go 检索栈：
 *    FTS5 BM25，配了 embedding 自动混合、后端不可用自动回退 BM25），把 top-N
 *    命中（path/title/summary）注入首条消息——保证知识库**必然被消费**，
 *    不依赖模型记不记得调工具。
 *  - 模型深检索兜底：注入块同时写明「命中候选可再 read / otg kb search」，
 *    保留模型顺着对话自适应深挖的能力。
 *  - 降级：otg 不可用 / 库未建 / 检索失败 → 回退注入 References/INDEX.md 的
 *    索引摘要（仍先给到本地语料概览），再失败则整体关闭。
 *
 * 数据来源：daemon 经 OTR_KB_VAULT 传入全局共享知识库根（config KBVault，
 * 缺省回退 ObsidianVault；两者皆空时本注入整体关闭，agent-server 可独立于
 * vault 运行）。OTR_KB_DB 为检索库路径（缺省 ~/.local/share/otg/kb.sqlite）。
 * ------------------------------------------------------------------------- */

/** 注入摘要的字符上限（约 1-2k token，对齐 knowledge-base 代价控制）。 */
const KB_INDEX_DIGEST_MAX = 2400

/** 服务端预检索：每个新会话最多注入的命中条数。 */
const KB_PRECOMPUTE_LIMIT = 3

/** 服务端预检索：查询词的字符上限（从首条消息截取）。 */
const KB_QUERY_MAX = 200

/** 预检索命中缓存 TTL：同 vault+db+query 在窗口内不重复 spawn otg。 */
const KB_HITS_TTL_MS = 10 * 60 * 1000

/** 预检索命中缓存条数上限：超过后先清过期条目，仍超则整表清空，
 * 防长跑 daemon 的查询缓存无限增长（每个不同 query 是一个 key）。 */
const KB_HITS_CACHE_MAX = 128

/** 预检索子进程超时：embedding 后端不可用等场景不得卡住聊天会话。 */
const KB_SEARCH_TIMEOUT_MS = 15 * 1000

let kbDigestCache = { key: "", digest: "" }
let kbHitsCache = new Map()

/** 写命中缓存时做容量治理：先删过期条目；仍超上限则清空。 */
function kbHitsCacheSet(key, hits) {
  if (kbHitsCache.size >= KB_HITS_CACHE_MAX) {
    const now = Date.now()
    for (const [k, v] of kbHitsCache) {
      if (now - v.at >= KB_HITS_TTL_MS) kbHitsCache.delete(k)
    }
    if (kbHitsCache.size >= KB_HITS_CACHE_MAX) kbHitsCache.clear()
  }
  kbHitsCache.set(key, { at: Date.now(), hits })
}

function kbVaultRoot() {
  return (process.env.OTR_KB_VAULT || "").trim()
}

function kbDbPath() {
  return (process.env.OTR_KB_DB || "").trim()
}

function kbIndexPath() {
  const v = kbVaultRoot()
  return v ? join(v, "References", "INDEX.md") : ""
}

function truncateStr(s, n) {
  const t = String(s)
  return t.length > n ? t.slice(0, n - 1) + "…" : t
}

/** 解析 References/INDEX.md 的目录表（文件 | 标题 | 摘要 | topics | …），
 * 产出紧凑的行摘要；按字符上限截断到完整行边界。 */
function summarizeKBIndex(text) {
  const lines = String(text).split("\n")
  const rows = []
  let inTable = false
  for (const line of lines) {
    if (line.startsWith("| 文件 |")) { inTable = true; continue }
    if (!inTable) continue
    if (!line.trim().startsWith("|")) { inTable = false; continue }
    if (/^[|\s:\-]+$/.test(line.trim())) continue // 分隔行
    const cells = line.split("|").map((c) => c.trim())
    // 去掉首尾空 cell：columns = 文件 | 标题 | 摘要 | topics | activity | …
    const body = cells.slice(1, cells.length - 1)
    const path = body[0] || ""
    const title = body[1] || ""
    if (!path || !title) continue
    const summary = body[2] && body[2] !== "⚠️" ? ` — ${truncateStr(body[2], 90)}` : ""
    const topics = body[3] && body[3] !== "⚠️" ? ` [${truncateStr(body[3], 60)}]` : ""
    rows.push(`- ${path} · ${truncateStr(title, 70)}${summary}${topics}`)
  }
  let digest = rows.join("\n")
  if (digest.length > KB_INDEX_DIGEST_MAX) {
    digest = digest.slice(0, KB_INDEX_DIGEST_MAX)
    const nl = digest.lastIndexOf("\n")
    if (nl > 0) digest = digest.slice(0, nl)
    digest += "\n…（索引已截断，用 `otg kb search '<关键词>'` 检索剩余）"
  }
  return digest
}

/** 返回 References/INDEX.md 的摘要；KB 未配置或索引不可读时返回 ""。 */
function kbIndexDigest() {
  const index = kbIndexPath()
  if (!index || !existsSync(index)) return ""
  let st
  try { st = statSync(index) } catch { return "" }
  const key = `${index}:${st.mtimeMs}:${st.size}`
  if (kbDigestCache.key === key) return kbDigestCache.digest
  let text
  try { text = readFileSync(index, "utf8") } catch { return "" }
  const digest = summarizeKBIndex(text)
  kbDigestCache = { key, digest }
  return digest
}

/** 从首条会话消息派生检索查询词：去掉 kitty-grill 的「任务 TASK-xxx — 」前缀，
 * 取前 KB_QUERY_MAX 字符并折叠空白。调用方可用 /agent/chat 的 kbQuery 字段
 * 提供更精准的查询词。 */
function deriveQuery(message) {
  let t = String(message).replace(/^任务\s+TASK-[\w-]+\s*[—:-]\s*/, "")
  t = t.replace(/\s+/g, " ").trim()
  if (t.length > KB_QUERY_MAX) t = t.slice(0, KB_QUERY_MAX)
  return t
}

/** 服务端预检索：spawn `otg kb search --json` 复用 Go 检索栈。返回命中数组
 * （[{path,title,summary,score,chunk}]）或 null（KB 未配置 / otg 不可用 /
 * 检索失败——调用方回退索引摘要）。带 TTL 缓存 + 子进程超时。 */
function kbPrecompute(query) {
  const vault = kbVaultRoot()
  const q = String(query || "").trim()
  if (!vault || !q) return null

  const cacheKey = `${vault}|${kbDbPath()}|${q}`
  const cached = kbHitsCache.get(cacheKey)
  if (cached !== undefined && Date.now() - cached.at < KB_HITS_TTL_MS) {
    return cached.hits
  }

  return new Promise((resolve) => {
    const otg = (process.env.OTR_OTG_PATH || "").trim() || "otg"
    const args = ["kb", "search", "--json", "--limit", String(KB_PRECOMPUTE_LIMIT), "--vault", vault]
    if (kbDbPath()) args.push("--db", kbDbPath())
    // 带上 daemon 的 map 路径：让 otg 读到 kb_embedding/kb_rerank 配置，
    // 否则 spawn 的 otg 会读默认 vault-map.json（用户配置在别处时丢失 embedding）。
    const mapFile = (process.env.OTR_MAP_FILE || "").trim()
    if (mapFile) args.push("--map-file", mapFile)
    args.push(q)
    const child = execFile(otg, args, {
      timeout: KB_SEARCH_TIMEOUT_MS,
      maxBuffer: 4 * 1024 * 1024,
      encoding: "utf8",
    }, (err, stdout) => {
      if (err) {
        console.error(`agent-server: KB precompute failed for ${q.slice(0, 40)}: ${err?.message ?? err}`)
        kbHitsCacheSet(cacheKey, null)
        resolve(null)
        return
      }
      let hits
      try { hits = JSON.parse(stdout) } catch { hits = null }
      if (!Array.isArray(hits) || hits.length === 0) hits = null
      kbHitsCacheSet(cacheKey, hits)
      resolve(hits)
    })
    // 进程级兜底：execFile 的 timeout 只杀子进程，这里再防万一卡住不 resolve。
    setTimeout(() => { resolve(null); try { child.kill("SIGKILL") } catch { /* noop */ } }, KB_SEARCH_TIMEOUT_MS + 1000)
  })
}

/** 组装「服务端预检索命中」前置块：确定性注入 top-N 命中 + 深检索规则。 */
function kbPrecomputePreamble(query, hits) {
  const vault = kbVaultRoot()
  if (!vault || !Array.isArray(hits) || hits.length === 0) return ""
  const lines = hits.map((h) => {
    const summary = h.summary && h.summary !== "⚠️" ? ` — ${truncateStr(h.summary, 90)}` : ""
    return `- ${h.path} · ${truncateStr(h.title || "(无标题)", 70)}${summary}`
  }).join("\n")
  return `<knowledge_base>\n## 本地优先（KB-first）：已按你的问题预检索到以下经验\n` +
    `查询词：${truncateStr(query, 120)}\n知识库根：${vault}\n\n命中（top-${hits.length}）：\n${lines}\n\n规则：\n` +
    `① 先 read 命中文档 ${vault}/References/<path> 的「踩坑实践」/「约束」小节，引用时标注来源路径。\n` +
    `② 命中不足时用 \`otg kb search '<关键词>'\` 继续检索，或 read 上方文档正文。\n` +
    `③ 未命中本地库才自行推理或外部搜索；不要重复已记录的失败方案（踩坑实践 = 禁止清单）。\n` +
    `④ verified 状态以文档 frontmatter 为准，未 verified 的经验按「待验证」处理。\n</knowledge_base>\n\n`
}

/** 组装 KB-first 前置块：优先服务端预检索命中；失败回退索引摘要。 */
function kbFirstPreamble(query, hits) {
  const vault = kbVaultRoot()
  if (!vault) return ""
  if (Array.isArray(hits) && hits.length > 0) {
    return kbPrecomputePreamble(query, hits)
  }
  const digest = kbIndexDigest()
  if (!digest) return ""
  return `<knowledge_base>\n## 本地优先（KB-first）：先查已有经验再动手\n` +
    `知识库根：${vault}\n\n索引摘要（命中候选再读正文，勿全文读取）：\n${digest}\n\n规则：\n` +
    `① 解决需求/问题前，先用上方索引匹配；命中候选 → 用 \`otg kb search '<关键词>'\` 检索全局索引，\n` +
    `   并用 read 读取 ${vault}/References/<path> 的「踩坑实践」/「约束」小节。\n` +
    `② 引用知识时标注来源路径与 verified 状态；未命中本地库才自行推理或外部搜索。\n` +
    `③ 不要重复已记录的失败方案（踩坑实践 = 禁止清单）。\n</knowledge_base>\n\n`
}

/* ---------------------------------------------------------------------------
 * 项目工作区感知（project-aware）上下文注入。
 *
 * 目标：web 交互按工作区隔离时，若当前工作区对应一个已注册项目（vault-map
 * 注册、位于 <vault>/Projects/<dir>），agent 应知道该项目**自带上下文**——
 * Notes/CONTEXT.md（约束/反模式/领域术语）、Notes/adr/（架构决策）、
 * Notes/PROJECT-CONVENTIONS.md（规范 + 架构约束基线）——而不是从零推理。
 *
 * 实现：/agent/chat 携带 project 字段 → agent-server 在 <projectVault>/Projects/
 * 下按名字（精确或去数字前缀）定位项目目录 → 生成紧凑摘要注入首条消息
 * （标注各上下文文件路径，让 agent 需要时 read 全文）。带缓存。
 * ------------------------------------------------------------------------- */

/** 项目上下文注入的字符上限。 */
const PROJECT_CONTEXT_MAX = 1800

/** 每个 ADR 标题最多列出条数（保持摘要紧凑）。 */
const PROJECT_ADR_LIST_MAX = 8

/** 每个 CONTEXT 小节最多保留的行数。 */
const PROJECT_SECTION_LINES_MAX = 6

/** 项目上下文缓存 TTL：超过后强制重读（ADR/CONTEXT 内容变更也能生效，
 * 而不只依赖 mtime 指纹——mtime 对"内容改但 mtime 未变"不敏感）。 */
const PROJECT_CTX_TTL_MS = 10 * 60 * 1000

/** 项目上下文缓存条数上限：超过先清过期、仍超则整表清空（防长跑泄漏）。 */
const PROJECT_CTX_CACHE_MAX = 64

let projectCtxCache = new Map()

/** 写项目上下文缓存时做容量治理（同 kbHitsCache 策略）。 */
function projectCtxCacheSet(key, digest) {
  if (projectCtxCache.size >= PROJECT_CTX_CACHE_MAX) {
    const now = Date.now()
    for (const [k, v] of projectCtxCache) {
      if (now - v.at >= PROJECT_CTX_TTL_MS) projectCtxCache.delete(k)
    }
    if (projectCtxCache.size >= PROJECT_CTX_CACHE_MAX) projectCtxCache.clear()
  }
  projectCtxCache.set(key, { at: Date.now(), digest })
}

function projectVaultRoot() {
  return (process.env.OTR_PROJECT_VAULT || process.env.OTR_KB_VAULT || "").trim()
}

/** 在 <vault>/Projects/ 下按项目名定位目录：精确匹配优先，其次去数字前缀
 * （"magic-models-manager" 匹配 "002-magic-models-manager"）。返回 "" 未找到。 */
function resolveProjectDir(project) {
  const vault = projectVaultRoot()
  const name = String(project || "").trim()
  if (!vault || !name) return ""
  const projectsDir = join(vault, "Projects")
  let entries
  try { entries = readdirSync(projectsDir, { withFileTypes: true }) } catch { return "" }
  for (const e of entries) {
    if (!e.isDirectory()) continue
    if (e.name === name) return join(projectsDir, e.name)
    const idx = e.name.indexOf("-")
    if (idx > 0 && e.name.slice(idx + 1) === name) return join(projectsDir, e.name)
  }
  return ""
}

/** 提取 markdown 文件中指定 ## 小节的正文前 N 行（找不到返回 ""）。 */
function markdownSection(text, heading) {
  const lines = String(text).split("\n")
  let inSection = false
  const out = []
  for (const line of lines) {
    if (line.startsWith(`## ${heading}`)) { inSection = true; continue }
    if (inSection) {
      if (line.startsWith("## ")) break
      const t = line.trim()
      if (t !== "") out.push(line)
      if (out.length >= PROJECT_SECTION_LINES_MAX) break
    }
  }
  return out.join("\n")
}

/** 提取 markdown 首行 H1 标题。 */
function h1Title(text) {
  const m = String(text).match(/^#\s+(.+)$/m)
  return m ? m[1].trim() : ""
}

/** 读取项目 Notes/adr/ 下 ADR 标题清单（取前 PROJECT_ADR_LIST_MAX 个）。 */
function adrTitles(projectDir) {
  const adrDir = join(projectDir, "Notes", "adr")
  let entries
  try { entries = readdirSync(adrDir) } catch { return [] }
  const titles = []
  for (const name of entries.sort()) {
    if (!name.endsWith(".md") || name === "ADR-INDEX.md" || name === "ADR-COVERAGE.md") continue
    try {
      const t = h1Title(readFileSync(join(adrDir, name), "utf8"))
      if (t) titles.push(`${name.replace(/\.md$/, "")}: ${t}`)
    } catch { /* skip unreadable */ }
    if (titles.length >= PROJECT_ADR_LIST_MAX) break
  }
  return titles
}

/** 生成项目上下文摘要块；缓存按 projectDir + 相关文件 mtime/size 做指纹，
 * 且叠加 TTL（PROJECT_CTX_TTL_MS）强制刷新——ADR/CONTEXT 内容变更也能生效。 */
function projectContextDigest(projectDir) {
  const notesDir = join(projectDir, "Notes")
  const ctxPath = join(notesDir, "CONTEXT.md")
  const convPath = join(notesDir, "PROJECT-CONVENTIONS.md")
  let key = `${projectDir}`
  for (const p of [ctxPath, convPath, join(notesDir, "adr")]) {
    try { const st = statSync(p); key += `|${st.mtimeMs}:${st.size}` } catch { key += "|" }
  }
  const cached = projectCtxCache.get(key)
  if (cached !== undefined && Date.now() - cached.at < PROJECT_CTX_TTL_MS) {
    return cached.digest
  }

  const parts = []
  let ctx = ""
  try { ctx = readFileSync(ctxPath, "utf8") } catch { /* no CONTEXT.md */ }
  if (ctx.trim() !== "") {
    const constraints = markdownSection(ctx, "Development Constraints")
    const anti = markdownSection(ctx, "Anti-patterns")
    if (constraints) parts.push(`## Constraints\n${constraints}`)
    if (anti) parts.push(`## Anti-patterns\n${anti}`)
  }
  const adrs = adrTitles(projectDir)
  if (adrs.length > 0) parts.push(`## ADRs（${adrs.length} 篇，需要时 read 全文）\n- ${adrs.join("\n- ")}`)
  if (existsSync(convPath)) {
    parts.push(`## Project Conventions（存在，规范 + 架构约束基线，最高优先）\n- ${convPath}`)
  }
  if (parts.length === 0) { projectCtxCacheSet(key, ""); return "" }
  const digest = parts.join("\n\n")
  projectCtxCacheSet(key, digest)
  return digest
}

/** 组装项目上下文前置块；项目未注册/无上下文时返回 ""。 */
function projectContextPreamble(project) {
  const projectDir = resolveProjectDir(project)
  if (!projectDir) return ""
  const digest = projectContextDigest(projectDir)
  if (!digest) return ""
  return `<project_context>\n## 当前工作区项目：${project}（${projectDir}）\n` +
    `本项目自带以下上下文（涉及项目本身的问题请先据此回答，不要从零推理）：\n\n${truncateStr(digest, PROJECT_CONTEXT_MAX)}\n\n` +
    `需要细节时用 read 读取：\n` +
    `- ${projectDir}/Notes/CONTEXT.md\n` +
    `- ${projectDir}/Notes/adr/（架构决策）\n` +
    `- ${projectDir}/Notes/PROJECT-CONVENTIONS.md（存在时，规范 + 架构约束，最高优先）\n</project_context>\n\n`
}

/** 仅测试导出：纯函数摘要在独立 node 脚本中可验证（不影响插件装载）。 */
export const _kbTest = { kbVaultRoot, kbDbPath, kbIndexPath, summarizeKBIndex, deriveQuery, kbPrecomputePreamble, kbFirstPreamble, projectVaultRoot, resolveProjectDir, projectContextDigest, projectContextPreamble }

function toolPolicyViolations(agent, firstSeq, policy) {
  const allowed = parseToolPolicy(policy)
  if (allowed === null) return []
  const violations = new Set()
  for (const event of agent.session.events) {
    if (event.seq < firstSeq) continue
    if (event.type !== "tool/call") continue
    const name = event.data?.name
    if (typeof name === "string" && name !== "" && !allowed.has(name)) violations.add(name)
  }
  return [...violations]
}

/** 提取 turn 失败详情（code + message）：客户端只拿到 errorCode/error 两个
 * 字段，看不到 reason 原文；不把 message 带出去时，kitty-grill 的写回日志
 * 与桌面提醒只剩「agent-server outcome error: 」空原因，失败完全不可诊断
 * （观测：TASK-058 决策写回 3 连败，无任何错误信息可查）。 */
function errorDetail(reason) {
  if (reason?.kind !== "error") return { errorCode: undefined, error: undefined }
  const e = reason.error
  const code = typeof e?.code === "string" ? e.code : undefined
  let message = undefined
  if (typeof e?.message === "string" && e.message !== "") message = e.message
  else if (typeof e === "string" && e !== "") message = e
  return { errorCode: code, error: message ?? code }
}

/** 读取 JSON 请求体。 */
async function readJson(req) {
  let body = ""
  for await (const chunk of req) body += chunk
  if (body === "") return {}
  try {
    return JSON.parse(body)
  } catch {
    throw new TypeError("request body must be JSON")
  }
}

/** 构造一条 user 消息（对齐 dsh-llm 的 createUserMessage，但不 import 该包）。 */
function userMessage(text) {
  return {
    role: "user",
    id: randomUUID(),
    content: [{ type: "text", text }],
    source: { kind: "user" },
  }
}

/** 提取会话首条用户文本（含 inbox/spliced 与 user/message 两种事件形态）。 */
function firstUserText(session) {
  const events = session?.events ?? []
  for (const event of events) {
    let blocks = []
    if (event.type === "agent/inbox/spliced") {
      const inserted = event.data?.inserted ?? []
      for (const item of inserted) {
        if (item.role === "user") blocks = item.content ?? []
        if (blocks.length > 0) break
      }
    } else if (event.type === "user/message") {
      blocks = event.data?.content ?? event.data?.message?.content ?? []
    }
    const text = blocks
      .filter((block) => block?.type === "text" && typeof block.text === "string")
      .map((block) => block.text)
      .join("\n")
      .trim()
    if (text !== "") return text
  }
  return ""
}

/** 从首条用户文本推导监控面板的 phase/task 标签。 */
function labelFromText(text) {
  let phase = "session"
  const skill = text.match(/\/obsidian-task-runner-([a-z0-9-]+)/)
  if (skill) phase = skill[1]
  else if (text.includes("需求详细化") || text.includes("requirement-elaborator")) phase = "grilling"
  else if (text.includes("决策清单")) phase = "grilling"

  let task = ""
  const m = text.match(/TASK-\d{3}(?:-[A-Za-z0-9-]+)?/)
  if (m) task = m[0]
  else {
    const line = text.split("\n").map((s) => s.trim()).find((s) => s.length > 0) ?? ""
    task = line.length > 48 ? line.slice(0, 45) + "…" : line
  }
  let project = ""
  const pm = text.match(/Projects\/([^/]+)\/Tasks\//)
  if (pm) project = pm[1]
  return { phase, task, project }
}

/** 会话创建时间（ms）；缺省回退首事件时间。 */
function sessionCreatedAtMs(session) {
  if (typeof session?.createdAt === "number") return session.createdAt
  const first = session?.events?.[0]
  if (typeof first?.time === "number") return first.time
  return Date.now()
}

export function apply(ctx, config = {}) {
  if (typeof config !== "object" || config === null || Array.isArray(config)) {
    throw new TypeError(`${name}: config must be an object`)
  }
  const unknown = Object.keys(config).filter((key) => !ALLOWED_KEYS.has(key))
  if (unknown.length > 0) {
    throw new TypeError(`${name}: unknown config key(s) ${unknown.join(", ")} — allowed: ${[...ALLOWED_KEYS].sort().join(", ")}`)
  }
  const port = config.port ?? 8799
  const host = config.host ?? "127.0.0.1"
  const agents = ctx.get("agents")

  /** agent.id -> 本会话期望的 reasoningEffort（agent/request 时注入）。 */
  const effortByAgent = new Map()

  /** sessionId -> live agent（/agent/chat 多轮交互复用同一实例，不 resume）。 */
  const liveAgents = new Map()

  /** chat sessionId -> 最近活跃时间（/agent/close 与僵尸回收用）。 */
  const chatLastAt = new Map()

  /** 已显式关闭（或惰性回收）的 chat sessionId：/agents 不再展示。 */
  const closedChat = new Set()

  /** run sessionId -> 完成时间（ms）：已完成的 run 会话从 /agents 隐藏，
   * 并在 RUN_DISPOSE_MS 后 dispose 回收；条目保留 RUN_FINISHED_TRACK_MS
   * 供「最近完成」计数，随后在 /agents 枚举时惰性剪除。 */
  const finishedRuns = new Map()

  /** sessionId -> dispose 闭包（agents.create/resume 返回的生命周期 teardown）。 */
  const disposeBySession = new Map()

  /** sessionId -> 任务 frontmatter 状态（daemon 经 /agent/run 的 status 字段
   * 传入，供监控面板按真实任务状态播放 NPC 动画：refining / planning /
   * plan-review / implementing / review / conflict ...）。 */
  const taskStatusBySession = new Map()

  /** sessionId -> 精确任务标识（daemon 经 /agent/run 的 taskId 字段传入）。
   * daemon 重启后 fresh Start 前会按 taskId 把上一代 daemon 残留的 working
   * 会话 cancel 掉（会话残留 Bug 报告：daemon 重启累积并发会话写同一
   * worktree）。task 展示标签由 prompt 正则推导，仅作 taskId 缺失时的回退。 */
  const taskIdBySession = new Map()

  // agent/request（prepend 最外层）：注入 per-请求 reasoningEffort。AgentOptions
  // 不含 reasoningEffort，只能经此 waterfall 注入（对齐 fallback.mjs 的做法）。
  ctx.on("agent/request", async (payload, next) => {
    const resolved = await next()
    const agent = payload.agent
    if (agent === undefined || resolved === undefined) return resolved
    const effort = effortByAgent.get(agent.id)
    if (effort === undefined) return resolved
    const { reasoningEffort: _drop, ...rest } = resolved
    return { ...rest, reasoningEffort: effort }
  }, { prepend: true })

  // agent 销毁时回收状态（finishedRuns 保留：它同时承载「最近完成」计数）。
  ctx.on("agent/disposed", (payload) => {
    const agent = payload?.agent
    if (agent !== undefined) {
      effortByAgent.delete(agent.id)
      const sid = String(agent.session?.id)
      liveAgents.delete(sid)
      chatLastAt.delete(sid)
      closedChat.delete(sid)
      disposeBySession.delete(sid)
      taskStatusBySession.delete(sid)
      taskIdBySession.delete(sid)
    }
  })

  /** session not found 类错误：会话确实不存在（不同于 busy/未知错误）。 */
  function isSessionNotFound(err) {
    const msg = String(err?.message ?? err).toLowerCase()
    const hasSession = msg.includes("session")
    const hasNotFound = msg.includes("not found") || msg.includes("no such session") || msg.includes("does not exist")
    return hasSession && hasNotFound
  }

  /**
   * 获取（create/resume）一个 agent，并注入本会话的 reasoningEffort。
   *
   * lenientCreate 语义分叉：
   * - /agent/run（daemon 阶段派发）：Go daemon 为每次 fresh Start 预生成
   *   sessionId（中断时用它持久化 resume token）。因此 sessionId 非空不代表
   *   「resume」——先按 resume 尝试，session not found 时按同一 id 创建新会话
   *   （观测：agent-server 重启后 pm 分发全部 500 session not found）。
   * - /agent/chat（kitty-grill 交互）：sessionId 必须严格 resume——找不到即报
   *   错，kitty-grill 依赖该信号降级 fresh fallback（writebackContext）。
   */
  async function acquireAgent(payload, lenientCreate) {
    const provider = payload.provider
    const model = payload.model
    if (typeof provider !== "string" || typeof model !== "string") throw new TypeError("provider/model must be strings")

    const agentOptions = { provider, model }
    const setup = undefined

    let agent
    let dispose = undefined
    if (payload.sessionId !== undefined && payload.sessionId !== "") {
      // 会话仍存活在本进程（daemon 中断/超时后 re-attach 的主路径）：直接复用
      // 实例。绝不走 agents.resume —— 持久层对 live 会话抛 "cannot prepare
      // session while it is live"，会导致 resume 永远挂接失败。
      const live = typeof agents.get === "function" ? agents.get(payload.sessionId) : undefined
      if (live !== undefined) {
        agent = live
      } else {
        try {
          // agents.resume 返回 published handle { agent, dispose }，取 .agent
          // （曾直接赋值导致 resume 成功后 agent.whenIdle is not a function）。
          const resumed = await agents.resume({
            resumeSessionId: payload.sessionId,
            agentOptions,
            setup,
          })
          agent = resumed.agent
          dispose = resumed.dispose
        } catch (err) {
          if (!lenientCreate || !isSessionNotFound(err)) throw err
          console.error(`agent-server: resume(${payload.sessionId}) not found — creating fresh session with that id (lenient /agent/run)`)
          const created = await agents.create({
            sessionId: payload.sessionId,
            meta: { cwd: process.cwd() },
            agentOptions,
            setup,
          })
          agent = created.agent
          dispose = created.dispose
        }
      }
    } else {
      const created = await agents.create({
        sessionId: `session-${randomUUID()}`,
        meta: { cwd: process.cwd() },
        agentOptions,
        setup,
      })
      agent = created.agent
      dispose = created.dispose
    }

    if (dispose !== undefined) disposeBySession.set(String(agent.session.id), dispose)
    if (payload.reasoningEffort !== undefined && payload.reasoningEffort !== "") {
      effortByAgent.set(agent.id, payload.reasoningEffort)
    }
    return agent
  }

  /** 判断 inbox 里是否已有一份内容相同的待投递 user 消息（re-attach 时
   *  原请求的消息可能还在队列里：跳过重复投递，只等它跑完并收集结果）。 */
  function pendingSameTask(agent, task) {
    const text = task.trim()
    for (const message of agent.inbox?.nextTurn ?? []) {
      const joined = (message.content ?? [])
        .filter((block) => block?.type === "text" && typeof block.text === "string")
        .map((block) => block.text)
        .join("\n")
        .trim()
      if (joined !== "" && joined === text) return true
    }
    return false
  }

  /** 完成一个 run 会话：进入 finishedRuns（面板隐藏 + TTL dispose）。 */
  function finishRun(sessionKey) {
    finishedRuns.set(sessionKey, Date.now())
    scheduleRunDispose(sessionKey)
  }

  /** TTL 后 dispose 已完成的 run 会话，回收 registry 与事件内存。 */
  function scheduleRunDispose(sessionKey) {
    const timer = setTimeout(() => {
      const finishedAt = finishedRuns.get(sessionKey)
      if (finishedAt === undefined) return // 已 re-attach 复活
      if (Date.now() - finishedAt < RUN_DISPOSE_MS) return
      const agent = typeof agents.get === "function" ? agents.get(sessionKey) : undefined
      if (agent === undefined) return
      if (agent.status !== "idle") return // 又开工：交给下一次完成时的调度
      const dispose = disposeBySession.get(sessionKey)
      if (dispose === undefined) return
      disposeBySession.delete(sessionKey)
      try {
        dispose()
      } catch (err) {
        console.error(`agent-server: dispose finished run ${sessionKey} failed: ${err?.message ?? err}`)
      }
    }, RUN_DISPOSE_MS)
    timer.unref?.()
  }

  /** 驱动一个 agent 到 quiescence 并收集结果。 */
  async function runAgent(payload) {
    let task = payload.task
    if (typeof task !== "string" || task.length === 0) throw new TypeError("task must be a non-empty string")
    const policy = parseToolPolicy(payload.toolPolicy)
    if (policy !== null) {
      // 只读审查会话：政策前置为最高优先级约束（hard preamble）。
      task = toolPolicyPreamble(payload.toolPolicy) + task
    }
    const agent = await acquireAgent(payload, true)
    const sessionKey = String(agent.session.id)
    finishedRuns.delete(sessionKey) // re-attach：会话复活，重新计入活跃
    // 监控面板按任务真实状态播放 NPC 动画（phase 只有 skill 名，区分不了
    // plan-review 与 implementing——两者都跑 round2）。
    if (typeof payload.status === "string" && payload.status !== "") {
      taskStatusBySession.set(sessionKey, payload.status)
    }
    // 精确任务标识：daemon 重启后据此 reconcile 上一代残留的 working 会话。
    if (typeof payload.taskId === "string" && payload.taskId !== "") {
      taskIdBySession.set(sessionKey, payload.taskId)
    }

    try {
      await agent.whenIdle()
      const firstSeq = agent.session.seq
      if (!pendingSameTask(agent, task)) agent.followup(userMessage(task))
      await agent.whenIdle()
      // 工具政策事后强校验：白名单外的 tool/call 直接判会话违规失败，
      // 客户端不得接受任何产物（防只读审查会话写源码后蒙混过关）。
      if (policy !== null) {
        const violations = toolPolicyViolations(agent, firstSeq, payload.toolPolicy)
        if (violations.length > 0) {
          console.error(`agent-server: run ${sessionKey} tool-policy violation: ${violations.join(", ")}`)
          return {
            text: "",
            outcome: "tool_policy_violation",
            sessionId: sessionKey,
            errorCode: "TOOL_POLICY_VIOLATION",
            error: `tool policy violation: disallowed tool calls [${violations.join(", ")}] (allowed: ${payload.toolPolicy})`,
          }
        }
      }
      const outcome = summarize(agent.session.events, firstSeq)
      const detail = errorDetail(outcome.reason)
      return {
        text: outcome.text,
        outcome: mapOutcome(outcome.reason),
        sessionId: sessionKey,
        errorCode: detail.errorCode,
        error: detail.error,
      }
    } finally {
      finishRun(sessionKey)
    }
  }

  /** 交互式一问一答：sessionId 命中 liveAgents 时复用同一 agent（多轮上下文
   *  延续）；否则 create 新 agent 并缓存。返回本轮模型回复与 sessionId。 */
  async function runChat(payload) {
    const message = payload.message
    if (typeof message !== "string" || message.length === 0) throw new TypeError("message must be a non-empty string")

    const sid = payload.sessionId
    // 全新会话（无 sessionId）才注入 KB-first 前置块；resume 的会话上下文
    // 已含该块，避免每轮重复膨胀。
    const freshSession = sid === undefined || sid === ""
    let agent
    if (sid !== undefined && sid !== "") {
      agent = liveAgents.get(sid)
      if (agent === undefined) {
        // 会话不在本进程（daemon 重启后）：严格 resume——session not found
        // 必须报错，kitty-grill 依赖该信号降级 fresh fallback。
        agent = await acquireAgent(payload, false)
        liveAgents.set(String(agent.session.id), agent)
      }
    } else {
      agent = await acquireAgent(payload, false)
      liveAgents.set(String(agent.session.id), agent)
    }

    const sessionKey = String(agent.session.id)
    chatLastAt.set(sessionKey, Date.now())
    closedChat.delete(sessionKey)
    finishedRuns.delete(sessionKey) // 已完成的 run 会话被 chat 复用：恢复活跃展示

    await agent.whenIdle()
    const firstSeq = agent.session.seq
    // 全新交互会话注入两块（均可独立关闭）：
    //  1. 项目工作区上下文（/agent/chat 的 project 字段，命中已注册项目时）——
    //     让 agent 知道本项目自带 CONTEXT.md/ADR/PROJECT-CONVENTIONS 可查，
    //     涉及项目本身的问题不用从零推理。
    //  2. KB-first：服务端预检索全局知识库命中（kbQuery/消息派生查询词）。
    let kbBlock = ""
    if (freshSession) {
      const project = (typeof payload.project === "string" ? payload.project : "").trim()
      const projectBlock = project ? projectContextPreamble(project) : ""
      const query = (typeof payload.kbQuery === "string" && payload.kbQuery.trim() !== "")
        ? payload.kbQuery.trim()
        : deriveQuery(message)
      const hits = await kbPrecompute(query)
      kbBlock = projectBlock + kbFirstPreamble(query, hits)
    }
    agent.followup(userMessage(kbBlock + message))
    await agent.whenIdle()
    chatLastAt.set(sessionKey, Date.now())
    const outcome = summarize(agent.session.events, firstSeq)
    const detail = errorDetail(outcome.reason)
    return {
      text: outcome.text,
      outcome: mapOutcome(outcome.reason),
      sessionId: sessionKey,
      errorCode: detail.errorCode,
      error: detail.error,
    }
  }

  /** 取消并销毁一个交互会话（客户端退出后调用，或僵尸惰性回收）。 */
  function closeChat(sessionKey) {
    const agent = liveAgents.get(sessionKey)
    if (agent === undefined) return
    try {
      agent.cancel({ kind: "cancelled" })
    } catch (err) {
      console.error(`agent-server: closeChat(${sessionKey}) cancel failed: ${err?.message ?? err}`)
    }
    liveAgents.delete(sessionKey)
    chatLastAt.delete(sessionKey)
    effortByAgent.delete(agent.id)
    taskStatusBySession.delete(sessionKey)
    taskIdBySession.delete(sessionKey)
    closedChat.add(sessionKey)
    const dispose = disposeBySession.get(sessionKey)
    disposeBySession.delete(sessionKey)
    if (dispose !== undefined) {
      try {
        dispose()
      } catch (err) {
        console.error(`agent-server: closeChat(${sessionKey}) dispose failed: ${err?.message ?? err}`)
      }
    }
  }

  /** 取消并销毁一个 run 会话（daemon 阶段超时后调用）：中止当前 model turn
   *  并从 live registry dispose——下次 /agent/run 携同 sessionId 时按
   *  session not found 走 lenient create（fresh start）。没有它，卡死的
   *  turn（gateway 挂起，TASK-079 refining 观测 6.8h）会被反复 re-attach。 */
  function cancelRun(sessionKey) {
    const agent = typeof agents.get === "function" ? agents.get(sessionKey) : undefined
    if (agent === undefined) return false
    try {
      agent.cancel({ kind: "cancelled" })
    } catch (err) {
      console.error(`agent-server: cancelRun(${sessionKey}) cancel failed: ${err?.message ?? err}`)
    }
    finishedRuns.delete(sessionKey)
    effortByAgent.delete(agent.id)
    taskStatusBySession.delete(sessionKey)
    taskIdBySession.delete(sessionKey)
    const dispose = disposeBySession.get(sessionKey)
    disposeBySession.delete(sessionKey)
    if (dispose !== undefined) {
      try {
        dispose()
      } catch (err) {
        console.error(`agent-server: cancelRun(${sessionKey}) dispose failed: ${err?.message ?? err}`)
      }
    }
    return true
  }

  /** 最近完成窗口内已结束 run 的数量（x-agents-finished）。finishedRuns
   *  本体不再按该窗口剪除——只要会话仍在 registry 里，就必须持续隐藏，
   *  否则已完成任务会在监控面板「复活」（观测：TASK-015 审计会话在 merge
   *  后仍留在面板）。 */
  function countRecentFinished(now = Date.now()) {
    let n = 0
    for (const finishedAt of finishedRuns.values()) {
      if (now - finishedAt <= RUN_FINISHED_TRACK_MS) n++
    }
    return n
  }

  /** 监控面板：列出 live agents（run + chat），chat 僵尸惰性回收。
   *  已完成的 run 会话不在列表中——面板只反映「当前真实并发」；其数量经
   *  finishedRuns 最近一小时窗口计数暴露（x-agents-finished）。 */
  function listAgents() {
    const now = Date.now()
    for (const [sid, lastAt] of chatLastAt) {
      if (now - lastAt > CHAT_IDLE_DISPOSE_MS) closeChat(sid)
    }
    const live = agents.list()
    const liveSids = new Set(live.map((agent) => String(agent.session?.id ?? agent.id)))
    // finishedRuns 是「已完成 run」的隐藏标记。只有当底层会话真的从
    // registry 消失（dispose 成功）后才剪除标记；会话仍在 registry 里时
    // 保留标记，避免超过 1h 窗口后 idle 的 run 会话重新出现在 /agents。
    for (const sid of [...finishedRuns.keys()]) {
      if (!liveSids.has(sid)) finishedRuns.delete(sid)
    }
    const out = []
    for (const agent of live) {
      const sid = String(agent.session?.id ?? agent.id)
      if (closedChat.has(sid)) continue
      if (finishedRuns.has(sid)) continue
      const text = firstUserText(agent.session)
      const { phase, task, project } = labelFromText(text)
      // 最近事件时间：daemon 侧超时判定用——有近期活动 = turn 仍在推进
      // （timeout_active，继续等）；长时间无事件 = wedged（cancel）。
      // 事件携带 epoch-ms `time` 字段；回退到会话创建时间。
      const events = Array.isArray(agent.session?.events) ? agent.session.events : []
      let lastEventAt = 0
      for (let i = events.length - 1; i >= 0; i--) {
        const t = events[i]?.time
        if (typeof t === "number" && t > 0) {
          lastEventAt = t
          break
        }
      }
      if (lastEventAt === 0) lastEventAt = sessionCreatedAtMs(agent.session)
      out.push({
        sessionId: sid,
        phase,
        task,
        project,
        taskId: taskIdBySession.get(sid) ?? "",
        status: agent.status === "idle" ? "idle" : "working",
        taskStatus: taskStatusBySession.get(sid) ?? "",
        elapsed: Math.max(0, Math.floor((now - sessionCreatedAtMs(agent.session)) / 1000)),
        lastEventAt,
        seq: Number(agent.session?.seq ?? 0),
      })
    }
    return out
  }

  /** 服务 agent-monitor.html（与插件同目录）。 */
  function serveMonitor(res) {
    const htmlPath = join(PLUGIN_DIR, "agent-monitor.html")
    if (!existsSync(htmlPath)) {
      res.writeHead(404, { "content-type": "application/json" })
      res.end(JSON.stringify({ error: "agent-monitor.html not found" }))
      return
    }
    res.writeHead(200, { "content-type": "text/html; charset=utf-8" })
    res.end(readFileSync(htmlPath))
  }

  const server = createServer(async (req, res) => {
    if (req.method === "GET" && (req.url === "/health" || req.url === "/healthz")) {
      res.writeHead(200, { "content-type": "application/json" })
      res.end(JSON.stringify({ ok: true }))
      return
    }
    if (req.method === "GET" && req.url === "/agents") {
      res.writeHead(200, {
        "content-type": "application/json",
        "x-agents-finished": String(countRecentFinished()),
      })
      res.end(JSON.stringify(listAgents()))
      return
    }
    if (req.method === "GET" && (req.url === "/monitor" || req.url === "/")) {
      serveMonitor(res)
      return
    }
    if (req.method !== "POST" ||
      (req.url !== "/agent/run" && req.url !== "/agent/chat" &&
        req.url !== "/agent/close" && req.url !== "/agent/cancel")) {
      res.writeHead(404, { "content-type": "application/json" })
      res.end(JSON.stringify({ error: "not found" }))
      return
    }
    try {
      const payload = await readJson(req)
      let result
      if (req.url === "/agent/close") {
        const sid = payload.sessionId
        if (typeof sid !== "string" || sid === "") throw new TypeError("sessionId must be a non-empty string")
        closeChat(sid)
        result = { ok: true }
      } else if (req.url === "/agent/cancel") {
        const sid = payload.sessionId
        if (typeof sid !== "string" || sid === "") throw new TypeError("sessionId must be a non-empty string")
        // run 会话优先；run 会话不存在时退化为 closeChat（同一取消语义）。
        const cancelled = cancelRun(sid)
        if (!cancelled) closeChat(sid)
        result = { ok: true, cancelled }
      } else {
        result = req.url === "/agent/chat" ? await runChat(payload) : await runAgent(payload)
      }
      res.writeHead(200, { "content-type": "application/json" })
      res.end(JSON.stringify(result))
    } catch (err) {
      res.writeHead(err instanceof TypeError ? 400 : 500, { "content-type": "application/json" })
      res.end(JSON.stringify({ error: String(err?.message ?? err) }))
    }
  })

  server.on("error", (err) => {
    // 端口被占用（上一个 agent-server 残留或并发 daemon）：退出而非静默挂起，
    // 否则 dsh 进程不退出，daemon 的健康检查会连到旧实例造成假健康。
    if (err.code === "EADDRINUSE") {
      console.error(`agent-server: ${host}:${port} already in use — another agent-server or stale process holds the port`)
      process.exit(1)
    }
    throw err
  })
  server.listen(port, host)
  ctx.on("dispose", () => {
    server.close()
  })
}
