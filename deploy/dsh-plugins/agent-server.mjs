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
 *   GET  /kb-stats                    → 200 kbStatsSnapshot()（KB 预检索统计：
 *        累计 + 当前小时窗口的 hits/misses/empty/errs/skipped/searches/avgMs +
 *        耗时直方图 {boundaries,counts}——Agent Town 面板「📊 KB 预检索」小图
 *        每 30s 轮询此端点，F1 落地）
 *   POST /agent/run  body: { task, provider, model, reasoningEffort?, sessionId?, status?, taskId?, toolPolicy?, fallback? }
 *     → 200 { text, outcome, sessionId, errorCode?, error? }
 *     outcome: completed | error | timeout | context_window | quota | key_unavailable | interrupted | tool_policy_violation
 *     （error 字段承载失败详情消息；errorCode 为分类码，两者可都缺省。
 *     toolPolicy="read,grep,glob,bash,skill,todo_write,job_output,job_list,job_kill,read_image"（audit）
 *     或追加 ",write"（conventions，用于落盘审查产物）等白名单：只读审查会话注入
 *     硬约束 preamble，且事后校验白名单外的 tool/call → tool_policy_violation。
 *     白名单必须含 harness 常规工具（skill/todo_write/job_*），否则会话必被
 *     TOOL_POLICY_VIOLATION 卡死（TASK-080/081 2026-08-31 教训）；真正要挡的
 *     是工作区写工具 edit/str_replace_editor。）
 *   POST /agent/chat body: { message, provider, model, reasoningEffort?, sessionId?, kbQuery?, project? }
 *     → 200 { text, outcome, sessionId, errorCode?, error? }（多轮交互）
 *     kbQuery 为可选的精准查询词（如任务标题）；project 为当前工作区项目名
 *     （命中 <vault>/Projects/<dir> 时注入项目上下文 CONTEXT/ADR/规范）。
 *     全新会话（sessionId 空）时服务端先做项目上下文 + KB-first 预检索并注入首条消息。
 *   POST /agent/close body: { sessionId } → 200 { ok: true }（取消交互会话）
 */
import { createServer } from "node:http"
import { randomUUID } from "node:crypto"
import { readFileSync, existsSync, statSync, readdirSync, mkdirSync, writeFileSync } from "node:fs"
import { execFile } from "node:child_process"
import { dirname, join } from "node:path"
import { homedir } from "node:os"
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
 * 传 auditToolPolicy / conventionsToolPolicy 等允许工具白名单（含 harness
 * 常规工具 skill/todo_write/job_*——漏了它们会话必被 TOOL_POLICY_VIOLATION
 * 卡死，TASK-080/081 2026-08-31 教训）。这里做两层执行：
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

/** 预检索失败（otg 不可用 / 检索出错）的短 TTL：瞬时故障不再毒化缓存
 * 满 10 分钟——真无命中（empty）仍按满 TTL 缓存（A2）。 */
const KB_ERR_TTL_MS = 30 * 1000

/** 预检索命中缓存条数上限：带 TTL 的 LRU（超限逐最旧，不清空热数据，A3）。 */
const KB_HITS_CACHE_MAX = 128

/** 预检索子进程超时：embedding 后端不可用等场景不得卡住聊天会话。 */
const KB_SEARCH_TIMEOUT_MS = 15 * 1000

/** 预检索超时预算（B1）：
 *  - 全预算 15s：首次 / 空闲超过 keep_alive（ollama 默认 5min 卸载模型）/
 *    上次慢或超时 → 冷启动需要耐心；
 *  - 快预算 4s：上次检索 ≤3s 完成 → 嵌入后端在热路径，收紧到 4s。
 * 超时自动回退 INDEX 摘要（现有逻辑），不牺牲正确性。 */
const KB_SEARCH_FAST_TIMEOUT_MS = 4 * 1000
const KB_SEARCH_FAST_OK_MS = 3 * 1000
const KB_SEARCH_COLD_IDLE_MS = 5 * 60 * 1000

/** 预检索统计日志周期：每小时一条 hit/miss 计数（A0，daemon logWriter 收集）。 */
const KB_STATS_LOG_INTERVAL_MS = 60 * 60 * 1000

/** 耗时直方图桶边界（F1）：与 B1 阈值对齐——100/500/1000 分辨 HTTP 快慢路径，
 * 2000/4000 对应 slow 标记与快预算边界，16000 即全预算+1s 兜底。 */
const KB_DURATION_BOUNDARIES = [0, 100, 500, 1000, 2000, 4000, 16000]

/** 耗时 → 桶下标（纯函数，F1）：桶 i = [B[i], B[i+1])，末桶 = [B[last], ∞)。
 * B[0] 是 0 哨兵——保证 0ms 落入首桶。 */
function durationBucket(ms, boundaries) {
  const B = boundaries || KB_DURATION_BOUNDARIES
  for (let i = 0; i < B.length - 1; i++) {
    if (ms >= B[i] && ms < B[i + 1]) return i
  }
  return B.length - 1
}

/** 直方图累加（纯函数，F1）。 */
function durationHistNote(hist, ms) {
  const idx = durationBucket(ms, KB_DURATION_BOUNDARIES)
  hist[idx] = (hist[idx] || 0) + 1
}

/** 直方图渲染（F1）：`<100=2,100-500=5,500-1000=0,1000-2000=1,2000-4000=0,4000-16000=0,>=16000=0`。 */
function renderDurationHist(hist, boundaries) {
  const B = boundaries || KB_DURATION_BOUNDARIES
  const labels = []
  for (let i = 0; i < B.length; i++) {
    if (i === 0) labels.push(`<${B[1]}`)
    else if (i === B.length - 1) labels.push(`>=${B[i]}`)
    else labels.push(`${B[i]}-${B[i + 1]}`)
  }
  return labels.map((l, i) => `${l}=${hist[i] || 0}`).join(",")
}

let kbDigestCache = { key: "", rows: [] }
let kbHitsCache = new Map()

/** 带 TTL 的 LRU 写入（kbHitsCache / projectCtxCache 共用，A3）：
 *  - 重复 key 先删再插（刷新 recency）；
 *  - 每次写入顺手清扫过期条目（ttlOf：数字或按条目取值——kbHitsCache 的
 *    err 条目 30s、hits/empty 10min，不能统一用一个 TTL 清扫），
 *    仍超上限则按 at 升序逐出最旧——
 *    替代旧实现"满上限整表清空"，多项目高频提问不再成批丢热数据。 */
function lruCacheSet(map, key, value, ttlOf, max) {
  map.delete(key)
  const now = Date.now()
  for (const [k, v] of map) {
    const ttl = typeof ttlOf === "function" ? ttlOf(v.value) : ttlOf
    if (now - v.at >= ttl) map.delete(k)
  }
  map.set(key, { at: now, value })
  while (map.size > max) {
    let oldestKey = null
    let oldestAt = Infinity
    for (const [k, v] of map) {
      if (v.at < oldestAt) { oldestAt = v.at; oldestKey = k }
    }
    if (oldestKey === null) break
    map.delete(oldestKey)
  }
}

/** 缓存条目 TTL：失败短缓存（30s），命中/空命中满 TTL（A2）。 */
function kbHitsEntryTTL(entry) {
  return entry?.kind === "err" ? KB_ERR_TTL_MS : KB_HITS_TTL_MS
}

/** 写命中缓存：条目 = {kind:"hits"|"empty"|"err", hits?}。 */
function kbHitsCacheSet(key, entry) {
  lruCacheSet(kbHitsCache, key, entry, kbHitsEntryTTL, KB_HITS_CACHE_MAX)
}

/** 读命中缓存：命中（含 err/empty 短缓存）→ 条目；未命中/过期 → null。 */
function kbHitsCacheGet(key) {
  const cached = kbHitsCache.get(key)
  if (cached === undefined) return null
  if (Date.now() - cached.at >= kbHitsEntryTTL(cached.value)) {
    kbHitsCache.delete(key)
    return null
  }
  return cached.value
}

/** 预检索命中率计量（A0）：hit/miss/empty/err/skipped 分类计数，每小时一条
 * 日志——没有它，后续缓存调优与阈值校准无从量化收益。F1：附带耗时直方图
 * （桶边界 KB_DURATION_BOUNDARIES，校准 B1 快/全预算阈值的分布证据）。 */
const kbPrecomputeStats = { hits: 0, misses: 0, empty: 0, errs: 0, skipped: 0, searchMs: 0, searchN: 0, slow: 0, hist: new Array(KB_DURATION_BOUNDARIES.length).fill(0), lastLogAt: Date.now() }

/** 进程生命周期累计（Agent Town 小图数据源，F1）：永不重置，与小时窗口并行。
 *  跨重启持久化（TASK-080 观测：小图「累计」在 daemon 每次重启后归零，
 *  面板长期挂零看起来像没更新）：totals 每 ≥30s 落盘一次 + 退出时兜底，
 *  启动时从文件恢复。文件在 ~/.local/state/dsh/（可用 OTR_KB_STATS_FILE 覆盖，
 *  测试用临时路径）。 */
const kbPrecomputeTotals = { hits: 0, misses: 0, empty: 0, errs: 0, skipped: 0, searchMs: 0, searchN: 0, hist: new Array(KB_DURATION_BOUNDARIES.length).fill(0) }

/** 持久化落盘最小间隔：统计更新频率低（每次预检索一次），30s 足够防止
 * 高频写盘，又保证崩溃最多丢 30s 数据。 */
const KB_STATS_SAVE_MIN_MS = 30 * 1000

/** 累计计数字段名（序列化/恢复共用）。 */
const KB_STATS_COUNTER_KEYS = ["hits", "misses", "empty", "errs", "skipped", "searchMs", "searchN"]

/** 默认持久化文件路径：~/.local/state/dsh/agent-server-kb-stats.json。
 *  OTR_KB_STATS_FILE 环境变量覆盖（单测/多实例隔离用）。 */
function kbStatsFileDefault() {
  const env = (process.env.OTR_KB_STATS_FILE || "").trim()
  if (env !== "") return env
  return join(homedir(), ".local", "state", "dsh", "agent-server-kb-stats.json")
}

/** totals 快照（持久化用）：只序列化累计对象，不含窗口/派生值。 */
function kbStatsTotalsSerialize(totals) {
  return JSON.stringify({
    hits: totals.hits, misses: totals.misses, empty: totals.empty, errs: totals.errs,
    skipped: totals.skipped, searchMs: totals.searchMs, searchN: totals.searchN,
    hist: Array.isArray(totals.hist) ? [...totals.hist] : [],
  })
}

/** 反序列化并校验：字段齐全且为数字、hist 为数字数组才接受；
 *  任何异常返回 null（调用方保持内存累计不变）。 */
function kbStatsTotalsDeserialize(text) {
  let t
  try { t = JSON.parse(text) } catch { return null }
  if (typeof t !== "object" || t === null) return null
  for (const k of KB_STATS_COUNTER_KEYS) {
    if (typeof t[k] !== "number" || !Number.isFinite(t[k])) return null
  }
  if (!Array.isArray(t.hist) || t.hist.some((v) => typeof v !== "number")) return null
  return {
    hits: t.hits, misses: t.misses, empty: t.empty, errs: t.errs,
    skipped: t.skipped, searchMs: t.searchMs, searchN: t.searchN, hist: [...t.hist],
  }
}

/** 读持久化文件（不存在/损坏 → null）。纯文件操作，单测传临时路径。 */
function loadPersistedTotals(file) {
  try {
    if (!existsSync(file)) return null
    return kbStatsTotalsDeserialize(readFileSync(file, "utf8"))
  } catch {
    return null
  }
}

/** 写持久化文件（自动建父目录）；返回是否成功。纯文件操作，单测传临时路径。 */
function savePersistedTotals(file, totals) {
  try {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, kbStatsTotalsSerialize(totals), "utf8")
    return true
  } catch {
    return false
  }
}

/** 持久化状态：文件路径 + 上次落盘时间 + 启动恢复时间（快照暴露给面板）。 */
const kbStatsPersist = { file: kbStatsFileDefault(), lastSaveAt: 0, restoredAt: null }

// 启动恢复：把上次进程的累计并入本进程 totals（加性语义，重复恢复幂等——
// 文件只在本次落盘时整体重写，不会双计）。
{
  const st = loadPersistedTotals(kbStatsPersist.file)
  if (st !== null) {
    for (const k of KB_STATS_COUNTER_KEYS) kbPrecomputeTotals[k] += st[k]
    for (let i = 0; i < kbPrecomputeTotals.hist.length && i < st.hist.length; i++) kbPrecomputeTotals.hist[i] += st.hist[i]
    try { kbStatsPersist.restoredAt = statSync(kbStatsPersist.file).mtimeMs } catch { kbStatsPersist.restoredAt = Date.now() }
    console.log(`agent-server: kb-precompute totals restored from ${kbStatsPersist.file} (searches=${st.searchN})`)
  }
}

/** 立即落盘累计（searchN=0 不写：避免单测/空进程污染用户状态目录）。 */
function persistStatsNow() {
  if (kbPrecomputeTotals.searchN <= 0) return
  if (savePersistedTotals(kbStatsPersist.file, kbPrecomputeTotals)) {
    kbStatsPersist.lastSaveAt = Date.now()
  }
}

/** 节流落盘：距上次 ≥30s 才写。kbStatsNote/noteSearchFinished 都会调用。 */
function maybePersistStats() {
  if (Date.now() - kbStatsPersist.lastSaveAt >= KB_STATS_SAVE_MIN_MS) persistStatsNow()
}

// 进程退出兜底：正常退出/信号退出都尽量落盘（同步写，量小无风险）。
process.on("exit", () => { persistStatsNow() })

/** 快照供 /kb-stats（Agent Town 小图）与测试使用：累计 + 当前小时窗口。 */
function kbStatsSnapshot() {
  const t = kbPrecomputeTotals
  const w = kbPrecomputeStats
  const histOf = (hist) => ({ boundaries: KB_DURATION_BOUNDARIES, counts: [...hist] })
  const sum = (s) => ({
    hits: s.hits, misses: s.misses, empty: s.empty, errs: s.errs, skipped: s.skipped,
    searches: s.searchN, avgMs: s.searchN > 0 ? Math.round(s.searchMs / s.searchN) : 0,
    hist: histOf(s.hist),
  })
  return { totals: sum(t), window: sum(w), lastLogAt: w.lastLogAt, restored: kbStatsPersist.restoredAt }
}

function kbStatsNote(kind) {
  kbPrecomputeStats[kind] = (kbPrecomputeStats[kind] || 0) + 1
  kbPrecomputeTotals[kind] = (kbPrecomputeTotals[kind] || 0) + 1
  maybePersistStats()
  const now = Date.now()
  if (now - kbPrecomputeStats.lastLogAt >= KB_STATS_LOG_INTERVAL_MS) {
    const avgMs = kbPrecomputeStats.searchN > 0 ? Math.round(kbPrecomputeStats.searchMs / kbPrecomputeStats.searchN) : 0
    console.log(`agent-server: kb-precompute stats(hourly) hits=${kbPrecomputeStats.hits} misses=${kbPrecomputeStats.misses} empty=${kbPrecomputeStats.empty} errs=${kbPrecomputeStats.errs} skipped=${kbPrecomputeStats.skipped} avgMs=${avgMs} slow(>=2s)=${kbPrecomputeStats.slow} hist=${renderDurationHist(kbPrecomputeStats.hist, KB_DURATION_BOUNDARIES)}`)
    kbPrecomputeStats.hits = 0
    kbPrecomputeStats.misses = 0
    kbPrecomputeStats.empty = 0
    kbPrecomputeStats.errs = 0
    kbPrecomputeStats.skipped = 0
    kbPrecomputeStats.searchMs = 0
    kbPrecomputeStats.searchN = 0
    kbPrecomputeStats.slow = 0
    kbPrecomputeStats.hist = new Array(KB_DURATION_BOUNDARIES.length).fill(0)
    kbPrecomputeStats.lastLogAt = now
  }
}

/** 预检索耗时状态（B1）：驱动下一次检索的超时预算选择。 */
const kbSearchTiming = { measured: false, lastAt: 0, lastDurationMs: 0, lastTimedOut: false }

/** 超时预算选择（纯函数，B1）：全预算 15s / 快预算 4s。 */
function pickSearchTimeout(t, now) {
  if (!t.measured) return KB_SEARCH_TIMEOUT_MS
  if (now - t.lastAt > KB_SEARCH_COLD_IDLE_MS) return KB_SEARCH_TIMEOUT_MS
  if (t.lastTimedOut || t.lastDurationMs > KB_SEARCH_FAST_OK_MS) return KB_SEARCH_TIMEOUT_MS
  return KB_SEARCH_FAST_TIMEOUT_MS
}

/** 记录一次检索完成（成功或失败）：更新预算状态与耗时统计（B1+F1）。 */
function noteSearchFinished(durationMs, timedOut) {
  kbSearchTiming.measured = true
  kbSearchTiming.lastAt = Date.now()
  kbSearchTiming.lastDurationMs = durationMs
  kbSearchTiming.lastTimedOut = timedOut
  kbPrecomputeStats.searchMs += durationMs
  kbPrecomputeStats.searchN += 1
  if (durationMs >= 2000) kbPrecomputeStats.slow += 1
  durationHistNote(kbPrecomputeStats.hist, durationMs)
  kbPrecomputeTotals.searchMs += durationMs
  kbPrecomputeTotals.searchN += 1
  kbPrecomputeTotals.hist[durationBucket(durationMs, KB_DURATION_BOUNDARIES)] += 1
  maybePersistStats()
}

/** 查询词归一化（A1，仅用于缓存键，检索仍传原文）：
 * 全角→半角、lowercase、提取 [a-z0-9]+ 与 CJK/假名/韩文 token——与索引的
 * token 空间语义一致，"如何部署 OTG？" 与 "如何部署otg" 共享同一条缓存。 */
function normalizeQueryForCache(q) {
  let t = String(q || "")
  t = t.replace(/\u3000/g, " ")
  t = t.replace(/[\uff01-\uff5e]/g, (ch) => String.fromCharCode(ch.charCodeAt(0) - 0xfee0))
  t = t.toLowerCase()
  return (t.match(/[a-z0-9]+|[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af]+/g) || []).join(" ")
}

/** 配置指纹（A5）：embedding/rerank 配置变化 → 缓存键变化，避免旧向量命中
 * 被新配置复用。按 OTR_MAP_FILE 的 mtime/size 指纹缓存解析结果。 */
let kbCfgFingerprintCache = { key: "", fp: "" }
function kbCfgFingerprint() {
  const mapFile = (process.env.OTR_MAP_FILE || "").trim()
  if (!mapFile) return ""
  let st
  try { st = statSync(mapFile) } catch { return "" }
  const key = `${mapFile}:${st.mtimeMs}:${st.size}`
  if (kbCfgFingerprintCache.key === key) return kbCfgFingerprintCache.fp
  let fp = ""
  try {
    const cfg = JSON.parse(readFileSync(mapFile, "utf8"))
    fp = JSON.stringify({ embedding: cfg?.kb_embedding ?? null, rerank: cfg?.kb_rerank ?? null })
    if (fp.length > 160) fp = fp.slice(0, 160)
  } catch { fp = "" }
  kbCfgFingerprintCache = { key, fp }
  return fp
}

/** 命中缓存键：vault|db|配置指纹|归一化查询词（A1+A5）。 */
function kbHitsCacheKey(vault, db, q) {
  return `${vault}|${db}|${kbCfgFingerprint()}|${normalizeQueryForCache(q)}`
}

/* ────────────────────────── B2：常驻检索端点 ────────────────────────────── */

/** 常驻 KB 检索端点基址（daemon vaultweb /api/kb/search）；空 → 只走 spawn。 */
const KB_HTTP_TIMEOUT_MS = 5000

function kbHttpBase() {
  return (process.env.OTR_KB_HTTP || "").trim()
}

/** 组装检索 URL（纯函数，可单测）：base 去尾斜杠 + 编码查询词。 */
function kbHttpUrl(base, q, limit, noRerank = false) {
  const rerank = noRerank ? "&rerank=false" : ""
  return `${String(base || "").replace(/\/+$/, "")}/api/kb/search?q=${encodeURIComponent(q)}&limit=${limit}${rerank}`
}

/** HTTP 检索：成功 → 命中数组（含空数组 = 真无命中）；失败/超时/非数组 → null
 * （调用方回退 spawn）。5s 超时——daemon 的 hybrid-only 快路径约 0.25-0.35s，
 * 带 rerank 的完整路径也能在预算内完成，避免每次 miss 又 spawn 重复检索。 */
async function kbHttpSearch(base, q, limit, noRerank = false) {
  try {
    const res = await fetch(kbHttpUrl(base, q, limit, noRerank), { signal: AbortSignal.timeout(KB_HTTP_TIMEOUT_MS) })
    if (!res.ok) return null
    const hits = await res.json()
    return Array.isArray(hits) ? hits : null
  } catch {
    return null
  }
}

/** 注册项目名集合（C3）：读 OTR_MAP_FILE 的 projects[].name。
 *  - 返回 Set：map 可读 → 门禁生效（未注册目录不再注入项目上下文）；
 *  - 返回 null：map 缺失/不可解析 → 未知，保持旧行为（目录匹配即放行）。
 * 按 map 路径+mtime+size 指纹缓存解析结果。 */
let registeredNamesCache = { key: "", names: null }
function registeredProjectNames() {
  const mapFile = (process.env.OTR_MAP_FILE || "").trim()
  if (!mapFile) return null
  let st
  try { st = statSync(mapFile) } catch { return null }
  const key = `${mapFile}:${st.mtimeMs}:${st.size}`
  if (registeredNamesCache.key === key) return registeredNamesCache.names
  let names = null
  try {
    const cfg = JSON.parse(readFileSync(mapFile, "utf8"))
    if (Array.isArray(cfg?.projects)) {
      names = new Set(cfg.projects.filter((p) => p && typeof p.name === "string" && p.name !== "").map((p) => p.name))
    }
  } catch { names = null }
  registeredNamesCache = { key, names }
  return names
}

/** C3 注册门禁：map 已知且 name（或其去数字前缀形式）不在注册集 → false。 */
function projectIsRegistered(name) {
  const names = registeredProjectNames()
  if (names === null) return true // 未知 → 旧行为放行
  const idx = name.indexOf("-")
  const stripped = idx > 0 ? name.slice(idx + 1) : ""
  return names.has(name) || (stripped !== "" && names.has(stripped))
}

/** 查询词 token 数：latin token 计 1，CJK/假名/韩文逐字计。 */
function queryTokenCount(t) {
  let n = 0
  for (const tok of t.match(/[a-z0-9]+|[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af]/g) || []) {
    n += /^[a-z0-9]+$/.test(tok) && tok.length > 1 ? 1 : tok.length
  }
  return n
}

/** 无效查询门禁（A4）：token 数 < 2 或纯问候语 → 跳过 spawn 预检索，
 * 避免无谓子进程与缓存污染（项目上下文块仍照常注入）。 */
function isTrivialQuery(q) {
  const t = normalizeQueryForCache(q)
  if (!t) return true
  if (queryTokenCount(t) < 2) return true
  const joined = t.replace(/\s+/g, "")
  return /^(你好|您好|hi|hello|hey|谢谢|thanks|thankyou|ok|好的|收到|在吗)+$/.test(joined)
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
 * 产出原始行数组（供渲染/排序复用）。 */
function parseKBIndexRows(text) {
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
    const summary = body[2] || ""
    const topics = body[3] || ""
    rows.push({ path, title, summary, topics })
  }
  return rows
}

/** 查询词 → 排序 token：latin 单词 + CJK 连续段切 bigram（与索引 token 空间一致）。 */
function rankQueryTokens(q) {
  const t = normalizeQueryForCache(q)
  const toks = []
  for (const w of t.match(/[a-z0-9]+/g) || []) toks.push(w)
  for (const cjk of t.match(/[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af]+/g) || []) {
    if (cjk.length === 1) toks.push(cjk)
    else for (let i = 0; i + 1 < cjk.length; i++) toks.push(cjk.slice(i, i + 2))
  }
  return toks
}

/** E1：按查询词与每行 path/title/summary/topics 的 token 重叠打分重排。
 * 稳定排序（Node ≥12）保证零分行保持原序殿后；无查询词返回原序。 */
function rankKBIndexRows(rows, query) {
  const toks = rankQueryTokens(query)
  if (!Array.isArray(rows) || toks.length === 0) return rows || []
  return rows
    .map((r) => {
      const hay = `${r.path} ${r.title} ${r.summary} ${r.topics}`.toLowerCase()
      let score = 0
      for (const t of toks) if (hay.includes(t)) score++
      return { r, score }
    })
    .sort((a, b) => b.score - a.score)
    .map((x) => x.r)
}

/** 行数组 → 紧凑摘要字符串；按字符上限截断到完整行边界。 */
function renderKBIndexDigest(rows) {
  const lines = []
  for (const r of rows || []) {
    const summary = r.summary && r.summary !== "⚠️" ? ` — ${truncateStr(r.summary, 90)}` : ""
    const topics = r.topics && r.topics !== "⚠️" ? ` [${truncateStr(r.topics, 60)}]` : ""
    lines.push(`- ${r.path} · ${truncateStr(r.title, 70)}${summary}${topics}`)
  }
  let digest = lines.join("\n")
  if (digest.length > KB_INDEX_DIGEST_MAX) {
    digest = digest.slice(0, KB_INDEX_DIGEST_MAX)
    const nl = digest.lastIndexOf("\n")
    if (nl > 0) digest = digest.slice(0, nl)
    digest += "\n…（索引已截断，用 `otg kb search '<关键词>'` 检索剩余）"
  }
  return digest
}

/** 解析 + 按查询词相关性排序 + 渲染（E1）。无 query 时保持文件原序。 */
function summarizeKBIndex(text, query) {
  return renderKBIndexDigest(rankKBIndexRows(parseKBIndexRows(text), query))
}

/** 返回 References/INDEX.md 的相关性摘要；KB 未配置或索引不可读时返回 ""。
 * 缓存按索引路径+mtime+size 指纹存解析后的行（排序/渲染按每次查询词现做，
 * 毫秒级）。 */
function kbIndexDigest(query) {
  const index = kbIndexPath()
  if (!index || !existsSync(index)) return ""
  let st
  try { st = statSync(index) } catch { return "" }
  const key = `${index}:${st.mtimeMs}:${st.size}`
  if (kbDigestCache.key !== key) {
    let text
    try { text = readFileSync(index, "utf8") } catch { return "" }
    kbDigestCache = { key, rows: parseKBIndexRows(text) }
  }
  const digest = renderKBIndexDigest(rankKBIndexRows(kbDigestCache.rows, query))
  if (!digest) return ""
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

/** 新会话首问可立即使用的缓存命中：只读缓存，绝不同步检索。
 * 返回命中数组或 null（无缓存/空/错误缓存一律回退 INDEX 摘要）。 */
function kbCachedHits(query) {
  const vault = kbVaultRoot()
  const q = String(query || "").trim()
  if (!vault || !q) return null
  const cached = kbHitsCacheGet(kbHitsCacheKey(vault, kbDbPath(), q))
  if (!cached) return null
  return cached.kind === "hits" && Array.isArray(cached.hits) ? cached.hits : null
}

/** 后台预热预检索（非阻塞）：默认走 hybrid-only 快路径（--no-rerank /
 * rerank=false），避免首问被 reranker 拖慢，也避免 daemon 端超过老 1.2s
 * HTTP 超时而重复 spawn。命中后写缓存，下次同域问题直接命中。
 * 缓存键含配置指纹 + 归一化查询词（A1/A5）；失败短缓存 30s（A2）；命中率计量（A0）。 */
function warmKbPrecompute(query) {
  const vault = kbVaultRoot()
  const q = String(query || "").trim()
  if (!vault || !q) return

  const cacheKey = kbHitsCacheKey(vault, kbDbPath(), q)
  const cached = kbHitsCacheGet(cacheKey)
  if (cached) {
    kbStatsNote("hits")
    return
  }

  kbStatsNote("misses")
  const startedAt = Date.now()
  const budget = pickSearchTimeout(kbSearchTiming, startedAt)
  let settled = false
  const finish = (durationMs, timedOut) => {
    if (settled) return
    settled = true
    noteSearchFinished(durationMs, timedOut)
  }
  /** 检索结果落缓存：数组（含空）= 正常结果。 */
  const storeHits = (hits, durationMs) => {
    finish(durationMs, false)
    if (!Array.isArray(hits) || hits.length === 0) {
      kbStatsNote("empty")
      kbHitsCacheSet(cacheKey, { kind: "empty" })
      return
    }
    kbHitsCacheSet(cacheKey, { kind: "hits", hits })
  }
  /** spawn 兜底（B2）：常驻端点不可用/未配置时的既有路径。 */
  const spawnSearch = () => {
    const otg = (process.env.OTR_OTG_PATH || "").trim() || "otg"
    const args = ["kb", "search", "--json", "--no-rerank", "--limit", String(KB_PRECOMPUTE_LIMIT), "--vault", vault]
    if (kbDbPath()) args.push("--db", kbDbPath())
    // 带上 daemon 的 map 路径：让 otg 读到 kb_embedding/kb_rerank 配置，
    // 否则 spawn 的 otg 会读默认 vault-map.json（用户配置在别处时丢失 embedding）。
    const mapFile = (process.env.OTR_MAP_FILE || "").trim()
    if (mapFile) args.push("--map-file", mapFile)
    args.push(q)
    const child = execFile(otg, args, {
      timeout: budget,
      maxBuffer: 4 * 1024 * 1024,
      encoding: "utf8",
    }, (err, stdout) => {
      if (err) {
        console.error(`agent-server: KB precompute failed for ${q.slice(0, 40)}: ${err?.message ?? err}`)
        // execFile 超时 kill（killed/signal）才算 timedOut；ENOENT 等立即失败不算。
        const timedOut = Boolean(err && (err.killed || err.code === "ETIMEDOUT"))
        finish(Date.now() - startedAt, timedOut)
        kbStatsNote("errs")
        kbHitsCacheSet(cacheKey, { kind: "err" })
        return
      }
      let hits
      try { hits = JSON.parse(stdout) } catch { hits = null }
      storeHits(hits, Date.now() - startedAt)
    })
    // 进程级兜底：execFile 的 timeout 只杀子进程，这里再防万一卡住不 finish。
    // 兜底触发 = 预算耗尽，记为 timedOut（下次回全预算）。
    setTimeout(() => {
      finish(Date.now() - startedAt, true)
      try { child.kill("SIGKILL") } catch { /* noop */ }
    }, budget + 1000)
  }
  // B2：优先走 daemon 常驻端点（免 spawn/重开 SQLite），失败回退 spawn。
  const httpBase = kbHttpBase()
  if (httpBase) {
    kbHttpSearch(httpBase, q, KB_PRECOMPUTE_LIMIT, true).then((hits) => {
      if (hits === null) { spawnSearch(); return }
      storeHits(hits, Date.now() - startedAt)
    }).catch(() => { spawnSearch() })
  } else {
    spawnSearch()
  }
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

/** 注入→消费率判定（D1，纯函数）：统计 firstSeq 之后、read 族工具
 * （read/view/grep/glob/cat）调用输入里出现过的注入路径——会话首轮对
 * KB-first 命中"真的读了没有"的客观证据（模型记得调用工具才算消费）。
 * 事件形态宽容：input 缺失时用整个 data 序列化匹配。 */
function consumedPathsFromEvents(events, firstSeq, injectedPaths) {
  const paths = (injectedPaths || []).filter((p) => typeof p === "string" && p !== "")
  if (!Array.isArray(events) || events.length === 0 || paths.length === 0) return []
  const remaining = new Set(paths)
  const consumed = []
  for (const ev of events) {
    if (!ev || typeof ev.seq !== "number" || ev.seq < firstSeq) continue
    if (ev.type !== "tool/call") continue
    const name = String(ev.data?.name || "").toLowerCase()
    if (!/^(read|view|grep|glob|cat)$/.test(name)) continue
    const payload = JSON.stringify(ev.data?.input ?? ev.data?.args ?? ev.data ?? {})
    for (const p of [...remaining]) {
      if (payload.includes(p)) {
        consumed.push(p)
        remaining.delete(p)
      }
    }
  }
  return consumed
}

/** 组装 KB-first 前置块：优先服务端预检索命中；失败回退索引摘要。 */
function kbFirstPreamble(query, hits) {
  const vault = kbVaultRoot()
  if (!vault) return ""
  if (Array.isArray(hits) && hits.length > 0) {
    return kbPrecomputePreamble(query, hits)
  }
  const digest = kbIndexDigest(query)
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

/** 项目上下文缓存条数上限：带 TTL 的 LRU（超限逐最旧，防长跑泄漏，A3）。 */
const PROJECT_CTX_CACHE_MAX = 64

let projectCtxCache = new Map()

/** 写项目上下文缓存时做容量治理（lruCacheSet，同 kbHitsCache 策略）。 */
function projectCtxCacheSet(key, digest) {
  lruCacheSet(projectCtxCache, key, digest, PROJECT_CTX_TTL_MS, PROJECT_CTX_CACHE_MAX)
}

function projectVaultRoot() {
  return (process.env.OTR_PROJECT_VAULT || process.env.OTR_KB_VAULT || "").trim()
}

/** 在 <vault>/Projects/ 下按项目名定位目录：精确匹配优先，其次去数字前缀
 * （"magic-models-manager" 匹配 "002-magic-models-manager"）。
 * C3：vault-map 可读时仅已注册项目放行（README「命中已注册项目」语义）；
 * map 缺失/不可解析 → 旧行为（目录匹配即放行）。返回 "" 未找到/未注册。 */
function resolveProjectDir(project) {
  const vault = projectVaultRoot()
  const name = String(project || "").trim()
  if (!vault || !name) return ""
  if (!projectIsRegistered(name)) return ""
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

/** 小节标题别名（C1）：CONTEXT.md 标题带变体（大小写/中文/无 Development
 * 前缀）也能命中——旧实现 `## ${heading}` 逐字匹配，标题差一字就整块漏注。
 * 匹配规则：`## ` 前缀后去空白 lowercase 后与任一别名精确相等。 */
const CONTEXT_SECTION_ALIASES = {
  constraints: ["development constraints", "constraints", "开发约束", "约束"],
  antipatterns: ["anti-patterns", "anti-pattern", "antipatterns", "antipattern", "反模式"],
  language: ["language", "语言", "领域术语", "terminology"],
}

/** 提取标题名（`##`/`###` 任意层级 → 去掉 # 与首尾空白，lowercase）。 */
function headingName(line) {
  const m = String(line).match(/^#+\s+(.+?)\s*$/)
  return m ? m[1].trim().toLowerCase() : ""
}

/** 提取 markdown 文件中与任一别名匹配的小节正文前 N 行（找不到返回 ""）。
 * headingAliases 传小写别名数组；遇到下一级标题即停。 */
function markdownSection(text, headingAliases) {
  const lines = String(text).split("\n")
  const want = new Set(headingAliases.map((a) => a.toLowerCase()))
  let inSection = false
  const out = []
  for (const line of lines) {
    if (/^#+\s/.test(line)) {
      if (inSection) break
      if (want.has(headingName(line))) inSection = true
      continue
    }
    if (inSection) {
      const t = line.trim()
      if (t !== "") out.push(line)
      if (out.length >= PROJECT_SECTION_LINES_MAX) break
    }
  }
  return out.join("\n")
}

/** CONTEXT.md 无已知小节时回退（C1）：H1 之后的前 N 行非空、非标题正文，
 * 保证 CONTEXT 内容永不整块丢失。 */
function contextOverview(text) {
  const lines = String(text).split("\n")
  const out = []
  let pastH1 = false
  for (const line of lines) {
    if (!pastH1) {
      if (/^#\s+/.test(line)) { pastH1 = true }
      continue
    }
    if (/^#/.test(line)) break // 遇到任意小节标题即停
    const t = line.trim()
    if (t !== "") out.push(line)
    if (out.length >= PROJECT_SECTION_LINES_MAX) break
  }
  return out.join("\n")
}

/** 提取 markdown 首行 H1 标题。 */
function h1Title(text) {
  const m = String(text).match(/^#\s+(.+)$/m)
  return m ? m[1].trim() : ""
}

/** 解析 markdown frontmatter 的单值字段（如 status）：返回字符串或 ""。
 * 兼容带引号与裸值（`status: "accepted"` / `status: superseded`）。 */
function frontmatterField(text, field) {
  const lines = String(text).split("\n")
  if (lines[0]?.trim() !== "---") return ""
  for (let i = 1; i < lines.length; i++) {
    if (lines[i].trim() === "---") return ""
    const m = lines[i].match(/^([a-zA-Z_]+):\s*(.*)$/)
    if (!m) continue
    if (m[1].toLowerCase() !== field.toLowerCase()) continue
    let v = m[2].trim()
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) v = v.slice(1, -1)
    return v
  }
  return ""
}

/** 提取 ADR 决策一行（C2）：`## Decision` / `## 决策` 小节的首个非空、
 * 非引用行（去列表符，截断 80 字符）；无 Decision 小节返回 ""。 */
function adrDecisionOneLiner(text) {
  const lines = String(text).split("\n")
  let inSection = false
  for (const line of lines) {
    if (/^#+\s/.test(line)) {
      const name = headingName(line)
      if (name === "decision" || name === "决策") { inSection = true; continue }
      if (inSection) return "" // 下一小节开始仍未取到 → 无
      continue
    }
    if (inSection) {
      const t = line.trim()
      if (t === "" || t.startsWith(">")) continue
      return truncateStr(t.replace(/^[-*]\s+/, ""), 80)
    }
  }
  return ""
}

/** 读取项目 Notes/adr/ 下 ADR 摘要清单（C2，取前 PROJECT_ADR_LIST_MAX 条）：
 *  - 按 mtime 倒序（新决策优先，替代旧字典序——ADR-010 不再排在 ADR-008 前、
 *    旧决策不再挤掉新决策）；
 *  - 每条 = `ID: 标题（status，缺失省略） — 决策一行`。 */
function adrTitles(projectDir) {
  const adrDir = join(projectDir, "Notes", "adr")
  let entries
  try { entries = readdirSync(adrDir) } catch { return [] }
  const files = []
  for (const name of entries) {
    if (!name.endsWith(".md") || name === "ADR-INDEX.md" || name === "ADR-COVERAGE.md") continue
    let st
    try { st = statSync(join(adrDir, name)) } catch { continue }
    files.push({ name, mtimeMs: st.mtimeMs })
  }
  files.sort((a, b) => b.mtimeMs - a.mtimeMs || a.name.localeCompare(b.name))
  const titles = []
  for (const f of files) {
    let text
    try { text = readFileSync(join(adrDir, f.name), "utf8") } catch { continue }
    const t = h1Title(text)
    if (!t) continue
    const status = frontmatterField(text, "status")
    const decision = adrDecisionOneLiner(text)
    let line = `${f.name.replace(/\.md$/, "")}: ${t}`
    if (status) line += `（${status}）`
    if (decision) line += ` — ${decision}`
    titles.push(line)
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
    return cached.value
  }

  const parts = []
  let ctx = ""
  try { ctx = readFileSync(ctxPath, "utf8") } catch { /* no CONTEXT.md */ }
  if (ctx.trim() !== "") {
    const constraints = markdownSection(ctx, CONTEXT_SECTION_ALIASES.constraints)
    const anti = markdownSection(ctx, CONTEXT_SECTION_ALIASES.antipatterns)
    const lang = markdownSection(ctx, CONTEXT_SECTION_ALIASES.language)
    if (constraints) parts.push(`## Constraints\n${constraints}`)
    if (anti) parts.push(`## Anti-patterns\n${anti}`)
    if (lang) parts.push(`## Language / 术语\n${lang}`)
    if (!constraints && !anti && !lang) {
      const overview = contextOverview(ctx)
      if (overview) parts.push(`## Context 概览\n${overview}`)
    }
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
export const _kbTest = { kbVaultRoot, kbDbPath, kbIndexPath, summarizeKBIndex, deriveQuery, kbPrecomputePreamble, kbFirstPreamble, kbCachedHits, kbHitsCacheGet, projectVaultRoot, resolveProjectDir, projectContextDigest, projectContextPreamble, normalizeQueryForCache, kbCfgFingerprint, kbHitsCacheKey, kbHitsEntryTTL, kbHitsCacheSet, isTrivialQuery, lruCacheSet, markdownSection, contextOverview, frontmatterField, adrDecisionOneLiner, adrTitles, pickSearchTimeout, noteSearchFinished, kbSearchTiming, consumedPathsFromEvents, registeredProjectNames, projectIsRegistered, kbHttpBase, kbHttpUrl, durationBucket, durationHistNote, renderDurationHist, kbStatsSnapshot, kbStatsFileDefault, kbStatsTotalsSerialize, kbStatsTotalsDeserialize, loadPersistedTotals, savePersistedTotals, sessionEvents, firstUserText, labelFromText, sessionCreatedAtMs, subagentDescriptor }

function toolPolicyViolations(agent, firstSeq, policy) {
  const allowed = parseToolPolicy(policy)
  if (allowed === null) return []
  const violations = new Set()
  for (const event of sessionEvents(agent.session)) {
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

/**
 * 跨版本获取会话事件数组（兼容 shim）。
 *
 * rc.2 及更早：`session.events` 直接是只读数组；alpha.4+ 移除 `.events`，
 * 改为 `snapshotEvents()` / `ownEvents()` 按需读取。双向兼容写法：
 * 优先 `.events`，回退 `ownEvents()`（子会话只取自身事件，不继承父会话），
 * 再回退 `snapshotEvents()`，都不存在给空数组。
 * 参见 `~/.dsh/plugins/kb-distill.mjs` 的 `sessionEvents` 同名函数。
 */
function sessionEvents(session) {
  if (session == null) return []
  if (Array.isArray(session.events)) return session.events
  if (typeof session.ownEvents === "function") return session.ownEvents()
  if (typeof session.snapshotEvents === "function") return session.snapshotEvents()
  return []
}

/** 提取会话首条用户文本（含 inbox/spliced 与 user/message 两种事件形态）。 */
function firstUserText(session) {
  const events = sessionEvents(session)
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

/** 提取子会话创建描述（subagent/descriptor 的 label/model 等）。 */
function subagentDescriptor(session) {
  const events = sessionEvents(session)
  for (const event of events) {
    if (event?.type !== "subagent/descriptor" || event?.data == null) continue
    const d = event.data
    return {
      label: typeof d.label === "string" ? d.label : "",
      mode: typeof d.mode === "string" ? d.mode : "",
      provider: typeof d.agentProvider === "string" ? d.agentProvider : "",
      model: typeof d.agentModel === "string" ? d.agentModel : "",
    }
  }
  return null
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
  const first = sessionEvents(session)[0]
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
    // vault-map fallback 动态下发：daemon 在 /agent/run 携带 fallback 链，挂到
    // agent 上供 fallback.mjs 以 agent.fallbackConfig 覆盖静态配置（headless-
    // agent-server profile 的 cordis.patch.yml 只加载动态配置、无静态链）。只对
    // run（自动化阶段）生效；/agent/chat 交互会话不设置 → 永不自动切模型，
    // 由用户在会话里自己选模型（dsh web / dsh-tui 交互不受 vault-map 影响）。
    if (payload.fallback !== undefined && payload.fallback !== null) {
      agent.fallbackConfig = payload.fallback
    }
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
      const outcome = summarize(sessionEvents(agent.session), firstSeq)
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
    let hits = null
    if (freshSession) {
      const project = (typeof payload.project === "string" ? payload.project : "").trim()
      const projectBlock = project ? projectContextPreamble(project) : ""
      const query = (typeof payload.kbQuery === "string" && payload.kbQuery.trim() !== "")
        ? payload.kbQuery.trim()
        : deriveQuery(message)
      // 无效/问候类查询跳过预检索（A4）：不 spawn otg、不注入 INDEX 摘要，
      // 避免无谓子进程与 token 开销；项目上下文块不受影响。
      const trivial = isTrivialQuery(query)
      if (trivial) kbStatsNote("skipped")
      // 非阻塞 KB-first：首问先用缓存命中，未命中则立即注入毫秒级 INDEX
      // 摘要，后台异步预热完整检索（hybrid-only，绕开 reranker）。
      hits = trivial ? null : kbCachedHits(query)
      if (!trivial) warmKbPrecompute(query)
      kbBlock = projectBlock + (trivial ? "" : kbFirstPreamble(query, hits))
    }
    agent.followup(userMessage(kbBlock + message))
    await agent.whenIdle()
    // 注入→消费率（D1）：本会话首轮注入的 top-3 命中，被 read 族工具
    // 实际读到的条数——KB-first 对交互问答真实增益的可观测证据。
    const injectedPaths = Array.isArray(hits) ? hits.map((h) => (typeof h?.path === "string" ? h.path : "")).filter((p) => p !== "") : []
    if (injectedPaths.length > 0) {
      const consumed = consumedPathsFromEvents(sessionEvents(agent.session), firstSeq, injectedPaths)
      console.log(`agent-server: kb-injected injected=${injectedPaths.length} consumed=${consumed.length}${consumed.length > 0 ? " [" + consumed.map((p) => p.split("/").pop()).join(",") + "]" : ""}`)
    }
    chatLastAt.set(sessionKey, Date.now())
    const outcome = summarize(sessionEvents(agent.session), firstSeq)
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
      const desc = subagentDescriptor(agent.session)
      const header = agent.session?.header ?? {}
      // 最近事件时间：daemon 侧超时判定用——有近期活动 = turn 仍在推进
      // （timeout_active，继续等）；长时间无事件 = wedged（cancel）。
      // 事件携带 epoch-ms `time` 字段；回退到会话创建时间。
      const events = sessionEvents(agent.session)
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
        label: desc?.label ?? "",
        kind: header.origin === "subagent" ? "subagent" : (taskIdBySession.has(sid) ? "task" : "session"),
        parentSessionId: typeof header.parentSession === "string" ? header.parentSession : "",
        delegationDepth: typeof header.delegationDepth === "number" ? header.delegationDepth : 0,
        provider: desc?.provider ?? "",
        model: desc?.model ?? "",
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
    if (req.method === "GET" && req.url === "/kb-stats") {
      res.writeHead(200, { "content-type": "application/json" })
      res.end(JSON.stringify(kbStatsSnapshot()))
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
