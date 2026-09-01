/**
 * kb-preflight — dsh web / dsh-tui 交互会话的 KB-first 预检插件。
 *
 * WHY：agent-server 的 /agent/chat 注入（KB 预检索 + 项目上下文）只覆盖
 * kitty-grill 与 Agent Town 问答；dsh web 原生聊天走 DSH 自己的会话运行时，
 * 不经过 agent-server。本插件用 DSH 原生 seam（agent/pre-step）把同一
 * 「本地优先」原则落到普通交互会话：新会话首个用户消息前注入
 *  1. 项目上下文（会话 cwd 命中 vault-map 已注册项目 / vault Projects/<dir>）：
 *     Notes/CONTEXT.md（别名容错提取）/ Notes/adr/（mtime 倒序 + status +
 *     决策一行）/ PROJECT-CONVENTIONS.md 的紧凑摘要 + 文件路径；
 *  2. KB-first 预检（非阻塞）：
 *     - 命中缓存（归一化查询词，10min TTL，失败短缓存 30s）→ 注入 top-N 命中；
 *     - 未命中 → 只注入 References/INDEX.md 摘要（毫秒级读文件），并在后台
 *       异步 spawn `otg kb search` 预热缓存（下次同域问题直接命中）——首问
 *       绝不因检索子进程/embedding 推理而变慢；
 *     - 无效查询（问候/单 token）跳过 KB 块（项目上下文仍注入）。
 *
 * 防双注入：会话事件里已出现 <knowledge_base>/<project_context> 块则跳过；
 * 同 agent 每个会话只注入一次（agent/disposed 时回收标记）。
 *
 * 安装：deploy/dsh-plugins/kb-preflight.mjs → ~/.dsh/plugins/，并在目标
 * profile 的 cordis.patch.yml 加载（web / dsh-tui；headless 自动化不加载——
 * 其会话已由 knowledge-base skill 强制先检索，agent-server 已覆盖 /agent/chat）。
 *
 * config（cordis.patch.yml 的 kb-preflight 行，均可缺省）：
 *   mapFile:   vault-map.json 路径（默认 ~/.dsh/skills/obsidian-task-runner/config/vault-map.json）
 *   vault:     KB 根（默认 mapFile.kb_vault ?? obsidian_vault；两者皆空 → KB 注入整体关闭）
 *   db:        检索库路径（默认 mapFile.kb_db ?? ~/.local/share/otg/kb.sqlite）
 *   otgPath:   otg 二进制（默认 ~/.local/bin/otg ?? "otg"）
 *   kbEnabled: 默认 true；projectEnabled: 默认 true
 */
import { readFileSync, existsSync, statSync, readdirSync } from "node:fs"
import { execFile } from "node:child_process"
import { homedir } from "node:os"
import { join } from "node:path"

export const name = "kb-preflight"
export const inject = []

const ALLOWED_KEYS = new Set(["mapFile", "vault", "db", "otgPath", "kbEnabled", "projectEnabled", "kbHttp"])

/* ────────────────────────────── 常量（对齐 agent-server） ───────────────── */
const KB_INDEX_DIGEST_MAX = 2400
const KB_HITS_TTL_MS = 10 * 60 * 1000
const KB_ERR_TTL_MS = 30 * 1000
const KB_HITS_CACHE_MAX = 128
const KB_PRECOMPUTE_LIMIT = 3
const KB_QUERY_MAX = 200
const KB_SEARCH_TIMEOUT_MS = 15 * 1000
const PROJECT_CONTEXT_MAX = 1800
const PROJECT_ADR_LIST_MAX = 8
const PROJECT_SECTION_LINES_MAX = 6
const PROJECT_CTX_TTL_MS = 10 * 60 * 1000
const PROJECT_CTX_CACHE_MAX = 64
const MAP_FINGERPRINT_TTL_MS = 30 * 1000
const DEFAULT_KB_DB = join(homedir(), ".local", "share", "otg", "kb.sqlite")
const DEFAULT_OTG = join(homedir(), ".local", "bin", "otg")
const DEFAULT_MAP_FILE = join(homedir(), ".dsh", "skills", "obsidian-task-runner", "config", "vault-map.json")

/* ────────────────────────────── 纯工具（可单测） ────────────────────────── */

function truncateStr(s, n) {
  const t = String(s)
  return t.length > n ? t.slice(0, n - 1) + "…" : t
}

function normalizeQueryForCache(q) {
  let t = String(q || "")
  t = t.replace(/\u3000/g, " ")
  t = t.replace(/[\uff01-\uff5e]/g, (ch) => String.fromCharCode(ch.charCodeAt(0) - 0xfee0))
  t = t.toLowerCase()
  return (t.match(/[a-z0-9]+|[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af]+/g) || []).join(" ")
}

function deriveQuery(message) {
  let t = String(message).replace(/^任务\s+TASK-[\w-]+\s*[—:-]\s*/, "")
  t = t.replace(/\s+/g, " ").trim()
  if (t.length > KB_QUERY_MAX) t = t.slice(0, KB_QUERY_MAX)
  return t
}

function queryTokenCount(t) {
  let n = 0
  for (const tok of t.match(/[a-z0-9]+|[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af]/g) || []) {
    n += /^[a-z0-9]+$/.test(tok) && tok.length > 1 ? 1 : tok.length
  }
  return n
}

function isTrivialQuery(q) {
  const t = normalizeQueryForCache(q)
  if (!t) return true
  if (queryTokenCount(t) < 2) return true
  const joined = t.replace(/\s+/g, "")
  return /^(你好|您好|hi|hello|hey|谢谢|thanks|thankyou|ok|好的|收到|在吗)+$/.test(joined)
}

/* ────────────────────────────── 命中缓存 ─────────────────────────────────── */

const kbHitsCache = new Map()

function kbHitsEntryTTL(entry) {
  return entry?.kind === "err" ? KB_ERR_TTL_MS : KB_HITS_TTL_MS
}

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

function kbHitsCacheSet(key, entry) {
  lruCacheSet(kbHitsCache, key, entry, kbHitsEntryTTL, KB_HITS_CACHE_MAX)
}

function hitsCacheKey(vault, db, q) {
  return `${vault}|${db}|${normalizeQueryForCache(q)}`
}

/** 同步读命中缓存：命中（含 err/empty 短缓存）→ 返回条目；未命中/过期 → null。 */
function hitsCacheGet(vault, db, q) {
  const key = hitsCacheKey(vault, db, q)
  const cached = kbHitsCache.get(key)
  if (cached === undefined) return null
  if (Date.now() - cached.at >= kbHitsEntryTTL(cached.value)) {
    kbHitsCache.delete(key)
    return null
  }
  return cached.value
}

/* ────────────────────────────── INDEX 摘要 ──────────────────────────────── */

let digestCache = { key: "", rows: [] }

/** 解析 References/INDEX.md 目录表 → 行数组（渲染/排序复用）。 */
function parseKBIndexRows(text) {
  const lines = String(text).split("\n")
  const rows = []
  let inTable = false
  for (const line of lines) {
    if (line.startsWith("| 文件 |")) { inTable = true; continue }
    if (!inTable) continue
    if (!line.trim().startsWith("|")) { inTable = false; continue }
    if (/^[|\s:\-]+$/.test(line.trim())) continue
    const cells = line.split("|").map((c) => c.trim())
    const body = cells.slice(1, cells.length - 1)
    const path = body[0] || ""
    const title = body[1] || ""
    if (!path || !title) continue
    rows.push({ path, title, summary: body[2] || "", topics: body[3] || "" })
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

/** E1：按 token 重叠打分重排；稳定排序保证零分行保持原序殿后。 */
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

/** 行数组 → 紧凑摘要；按字符上限截断到完整行边界。 */
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

/** 返回 References/INDEX.md 的相关性摘要；不可读返回 ""。
 * 缓存按索引路径+mtime+size 存解析行；排序/渲染按查询词现做（毫秒级）。 */
function kbIndexDigest(vault, query) {
  if (!vault) return ""
  const index = join(vault, "References", "INDEX.md")
  if (!existsSync(index)) return ""
  let st
  try { st = statSync(index) } catch { return "" }
  const key = `${index}:${st.mtimeMs}:${st.size}`
  if (digestCache.key !== key) {
    let text
    try { text = readFileSync(index, "utf8") } catch { return "" }
    digestCache = { key, rows: parseKBIndexRows(text) }
  }
  return renderKBIndexDigest(rankKBIndexRows(digestCache.rows, query))
}

/* ────────────────────────────── 消息与防重 ──────────────────────────────── */

/** 从 pending messages 提取首条用户文本（content 块数组或字符串，宽松匹配）。 */
function firstUserTextOf(messages) {
  for (const m of Array.isArray(messages) ? messages : []) {
    if (m?.role !== "user") continue
    const c = m.content
    if (typeof c === "string" && c.trim() !== "") return c.trim()
    if (Array.isArray(c)) {
      for (const b of c) {
        if (b?.type === "text" && typeof b.text === "string" && b.text.trim() !== "") return b.text.trim()
      }
    }
  }
  return ""
}

/** 会话事件里是否已有本插件注入块（防 resume/多轮双注入）。 */
function sessionHasInjectedBlock(agent) {
  const events = agent?.session?.events
  if (!Array.isArray(events)) return false
  for (const ev of events) {
    const msg = ev?.data?.message
    if (!msg) continue
    const c = msg.content
    let text = ""
    if (typeof c === "string") text = c
    else if (Array.isArray(c)) text = c.map((b) => (b?.type === "text" ? b.text || "" : "")).join("")
    if (text.includes("<knowledge_base>") || text.includes("<project_context>")) return true
  }
  return false
}

/** 合成注入消息（形状对齐 kb-distill / dsh-commands 的 followup 消息）。 */
function messageFor(text) {
  return { role: "user", content: [{ type: "text", text }], source: { kind: "plugin", plugin: "kb-preflight" } }
}

/* ────────────────────────────── 项目上下文 ──────────────────────────────── */

const CONTEXT_SECTION_ALIASES = {
  constraints: ["development constraints", "constraints", "开发约束", "约束"],
  antipatterns: ["anti-patterns", "anti-pattern", "antipatterns", "antipattern", "反模式"],
  language: ["language", "语言", "领域术语", "terminology"],
}

function headingName(line) {
  const m = String(line).match(/^#+\s+(.+?)\s*$/)
  return m ? m[1].trim().toLowerCase() : ""
}

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

function contextOverview(text) {
  const lines = String(text).split("\n")
  const out = []
  let pastH1 = false
  for (const line of lines) {
    if (!pastH1) {
      if (/^#\s+/.test(line)) pastH1 = true
      continue
    }
    if (/^#/.test(line)) break
    const t = line.trim()
    if (t !== "") out.push(line)
    if (out.length >= PROJECT_SECTION_LINES_MAX) break
  }
  return out.join("\n")
}

function h1Title(text) {
  const m = String(text).match(/^#\s+(.+)$/m)
  return m ? m[1].trim() : ""
}

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

function adrDecisionOneLiner(text) {
  const lines = String(text).split("\n")
  let inSection = false
  for (const line of lines) {
    if (/^#+\s/.test(line)) {
      const name = headingName(line)
      if (name === "decision" || name === "决策") { inSection = true; continue }
      if (inSection) return ""
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

const projectCtxCache = new Map()

function projectCtxCacheSet(key, digest) {
  lruCacheSet(projectCtxCache, key, digest, PROJECT_CTX_TTL_MS, PROJECT_CTX_CACHE_MAX)
}

function projectContextDigest(projectDir) {
  const notesDir = join(projectDir, "Notes")
  const ctxPath = join(notesDir, "CONTEXT.md")
  const convPath = join(notesDir, "PROJECT-CONVENTIONS.md")
  let key = `${projectDir}`
  for (const p of [ctxPath, convPath, join(notesDir, "adr")]) {
    try { const st = statSync(p); key += `|${st.mtimeMs}:${st.size}` } catch { key += "|" }
  }
  const cached = projectCtxCache.get(key)
  if (cached !== undefined && Date.now() - cached.at < PROJECT_CTX_TTL_MS) return cached.value

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

function projectContextPreamble(projectName, projectDir) {
  if (!projectDir) return ""
  const digest = projectContextDigest(projectDir)
  if (!digest) return ""
  return `<project_context>\n## 当前工作区项目：${projectName}（${projectDir}）\n` +
    `本项目自带以下上下文（涉及项目本身的问题请先据此回答，不要从零推理）：\n\n${truncateStr(digest, PROJECT_CONTEXT_MAX)}\n\n` +
    `需要细节时用 read 读取：\n` +
    `- ${projectDir}/Notes/CONTEXT.md\n` +
    `- ${projectDir}/Notes/adr/（架构决策）\n` +
    `- ${projectDir}/Notes/PROJECT-CONVENTIONS.md（存在时，规范 + 架构约束，最高优先）\n</project_context>\n\n`
}

/* ────────────────────────────── 项目名解析（cwd） ───────────────────────── */

let mapProjectsCache = { key: "", at: 0, projects: null }

/** 注册项目列表：返回 null（map 缺失/不可解析 → 未知，旧行为放行）
 * 或数组（map 可读 → C3 门禁可用）。缓存按 map 路径+mtime+size 指纹 +
 * TTL——不同 map / 同一 map 的修改在窗口内都能正确生效。 */
function vaultMapProjects(mapFile) {
  if (!mapFile) return null
  let st
  try { st = statSync(mapFile) } catch { mapProjectsCache = { key: "", at: 0, projects: null }; return null }
  const key = `${mapFile}:${st.mtimeMs}:${st.size}`
  const now = Date.now()
  if (mapProjectsCache.key === key && now - mapProjectsCache.at < MAP_FINGERPRINT_TTL_MS) return mapProjectsCache.projects
  let projects = null
  try {
    const cfg = JSON.parse(readFileSync(mapFile, "utf8"))
    if (Array.isArray(cfg?.projects)) {
      projects = cfg.projects
        .filter((p) => p && typeof p.name === "string" && p.name !== "")
        .map((p) => ({ name: p.name, path: typeof p.path === "string" ? p.path : "" }))
    }
  } catch { projects = null }
  mapProjectsCache = { key, at: now, projects }
  return projects
}

/** 在 <vault>/Projects/ 下按项目名定位目录（精确优先，其次去数字前缀）。 */
function resolveProjectsDir(vault, name) {
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

/**
 * 从会话 cwd 解析已注册项目：
 *  1. cwd 命中 vault-map projects[].path（或为其子目录）→ 该项目（已注册）；
 *  2. cwd 位于 <vault>/Projects/<dir>（精确或去数字前缀）→ 该项目，
 *     C3：vault-map 可读时仅已注册项目放行；map 缺失/不可解析 → 旧行为放行。
 * 返回 { name, dir }（dir 为 vault Projects 目录，上下文文件所在）或 ""。
 */
function resolveProjectName(cwd, vault, mapFile) {
  const dir = String(cwd || "").replace(/\/+$/, "")
  if (!dir || !vault) return ""
  const norm = (p) => String(p || "").replace(/\/+$/, "")
  const projects = vaultMapProjects(mapFile) // null = 未知；数组 = 已知注册集
  if (projects !== null) {
    for (const p of projects) {
      const pv = norm(p.path)
      if (pv && (dir === pv || dir.startsWith(pv + "/"))) {
        const projDir = resolveProjectsDir(vault, p.name)
        return projDir ? { name: p.name, dir: projDir } : { name: p.name, dir: "" }
      }
    }
  }
  const projRoot = norm(vault) + "/Projects"
  if (dir === projRoot || dir.startsWith(projRoot + "/")) {
    const rest = dir.slice(projRoot.length + 1).split("/")[0]
    if (!rest) return ""
    const name = rest.includes("-") ? rest.slice(rest.indexOf("-") + 1) : rest
    if (projects !== null) {
      const idx = rest.indexOf("-")
      const stripped = idx > 0 ? rest.slice(idx + 1) : ""
      const registered = projects.some((p) => p.name === name || p.name === rest || (stripped !== "" && p.name === stripped))
      if (!registered) return "" // C3 门禁
    }
    const projDir = resolveProjectsDir(vault, name) || dir.slice(0, projRoot.length + 1 + rest.length)
    return { name, dir: projDir }
  }
  return ""
}

/* ────────────────────────────── KB 块组装 ───────────────────────────────── */

function kbHitsPreamble(query, hits, vault) {
  const lines = hits.slice(0, KB_PRECOMPUTE_LIMIT).map((h) => {
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

function kbDigestPreamble(query, digest, vault) {
  return `<knowledge_base>\n## 本地优先（KB-first）：先查已有经验再动手\n` +
    `查询词：${truncateStr(query, 120)}\n知识库根：${vault}\n\n索引摘要（命中候选再读正文，勿全文读取）：\n${digest}\n\n规则：\n` +
    `① 解决需求/问题前，先用上方索引匹配；命中候选 → 用 \`otg kb search '<关键词>'\` 检索全局索引，\n` +
    `   并用 read 读取 ${vault}/References/<path> 的「踩坑实践」/「约束」小节。\n` +
    `② 引用知识时标注来源路径与 verified 状态；未命中本地库才自行推理或外部搜索。\n` +
    `③ 不要重复已记录的失败方案（踩坑实践 = 禁止清单）。\n</knowledge_base>\n\n`
}

/** 组装 KB 块：命中 → 命中块；未命中 → INDEX 摘要块；trivial/无 vault → ""。 */
function buildKBBlock(query, hitsEntry, vault) {
  if (!vault) return ""
  if (isTrivialQuery(query)) return ""
  if (hitsEntry && hitsEntry.kind === "hits" && Array.isArray(hitsEntry.hits) && hitsEntry.hits.length > 0) {
    return kbHitsPreamble(query, hitsEntry.hits, vault)
  }
  const digest = kbIndexDigest(vault, query)
  if (!digest) return ""
  return kbDigestPreamble(query, digest, vault)
}

/* ────────────────────────────── 后台预热 spawn ──────────────────────────── */

/** 常驻检索端点基址（B2，默认 daemon vaultweb；配置 kbHttp: "" 关闭 HTTP 直走 spawn）。 */
const DEFAULT_KB_HTTP = "http://127.0.0.1:8787"
const KB_HTTP_TIMEOUT_MS = 1200

/** 组装检索 URL（纯函数，可单测）：base 去尾斜杠 + 编码查询词。 */
function kbHttpUrl(base, q, limit) {
  return `${String(base || "").replace(/\/+$/, "")}/api/kb/search?q=${encodeURIComponent(q)}&limit=${limit}`
}

/** HTTP 检索：成功 → 命中数组（空数组 = 真无命中）；失败/超时 → null（回退 spawn）。 */
async function kbHttpSearch(base, q, limit) {
  try {
    const res = await fetch(kbHttpUrl(base, q, limit), { signal: AbortSignal.timeout(KB_HTTP_TIMEOUT_MS) })
    if (!res.ok) return null
    const hits = await res.json()
    return Array.isArray(hits) ? hits : null
  } catch {
    return null
  }
}

/** 后台异步检索：只为预热缓存，绝不阻塞首问。B2：优先 daemon 常驻端点
 * （免 spawn/重开 SQLite），失败回退 spawn；两者都静默降级。 */
function warmSearch(config, vault, q) {
  const cacheKey = hitsCacheKey(vault, config.db, q)
  const store = (hits) => kbHitsCacheSet(cacheKey, Array.isArray(hits) && hits.length === 0 ? { kind: "empty" } : { kind: "hits", hits })
  const spawnFallback = () => {
    const otg = config.otgPath || "otg"
    const args = ["kb", "search", "--json", "--limit", String(KB_PRECOMPUTE_LIMIT), "--vault", vault]
    if (config.db) args.push("--db", config.db)
    if (config.mapFile) args.push("--map-file", config.mapFile)
    args.push(q)
    const child = execFile(otg, args, {
      timeout: KB_SEARCH_TIMEOUT_MS,
      maxBuffer: 4 * 1024 * 1024,
      encoding: "utf8",
    }, (err, stdout) => {
      if (err) {
        kbHitsCacheSet(cacheKey, { kind: "err" })
        return
      }
      let hits
      try { hits = JSON.parse(stdout) } catch { hits = null }
      if (!Array.isArray(hits) || hits.length === 0) {
        kbHitsCacheSet(cacheKey, { kind: "empty" })
        return
      }
      store(hits)
    })
    // 防兜底卡进程：超时后强杀。
    setTimeout(() => { try { child.kill("SIGKILL") } catch { /* noop */ } }, KB_SEARCH_TIMEOUT_MS + 1000)
  }
  if (config.kbHttp) {
    kbHttpSearch(config.kbHttp, q, KB_PRECOMPUTE_LIMIT).then((hits) => {
      if (hits === null) { spawnFallback(); return }
      store(hits)
    })
    return
  }
  spawnFallback()
}

/* ────────────────────────────── 配置解析 ────────────────────────────────── */

let mapConfigCache = { key: "", cfg: null }

function readMapConfig(mapFile) {
  let st
  try { st = statSync(mapFile) } catch { mapConfigCache = { key: "", cfg: null }; return null }
  const key = `${mapFile}:${st.mtimeMs}:${st.size}`
  if (mapConfigCache.key === key) return mapConfigCache.cfg
  let cfg = null
  try { cfg = JSON.parse(readFileSync(mapFile, "utf8")) } catch { cfg = null }
  mapConfigCache = { key, cfg }
  return cfg
}

function resolveConfig(raw) {
  const config = { ...(raw || {}) }
  const unknown = Object.keys(config).filter((k) => !ALLOWED_KEYS.has(k))
  if (unknown.length > 0) throw new TypeError(`${name}: unknown config key(s) ${unknown.join(", ")} — allowed: ${[...ALLOWED_KEYS].sort().join(", ")}`)
  const mapFile = config.mapFile || DEFAULT_MAP_FILE
  const mapCfg = readMapConfig(mapFile) || {}
  // B2 端点默认优先取 vault-map 的 vault_web_addr（与 daemon 同源），
  // 缺省才回落到 8787——非默认端口部署时预热仍走进程内端点。
  const defaultHttp = mapCfg.vault_web_addr ? `http://${mapCfg.vault_web_addr}` : DEFAULT_KB_HTTP
  return {
    mapFile,
    vault: config.vault || mapCfg.kb_vault || mapCfg.obsidian_vault || "",
    db: config.db || mapCfg.kb_db || DEFAULT_KB_DB,
    otgPath: config.otgPath || DEFAULT_OTG,
    kbEnabled: config.kbEnabled !== false,
    projectEnabled: config.projectEnabled !== false,
    kbHttp: config.kbHttp !== undefined ? config.kbHttp : defaultHttp,
  }
}

/* ────────────────────────────── 插件入口 ────────────────────────────────── */

export function apply(ctx, rawConfig = {}) {
  const config = resolveConfig(rawConfig)
  const injectedAgents = new Set()

  // 新会话首个用户消息前注入（非阻塞）：项目上下文（毫秒级文件读）+ KB 块
  // （缓存命中/INDEX 摘要，绝不同步 spawn）。
  ctx.on("agent/pre-step", async (payload, next) => {
    const resolved = await next()
    const agent = payload.agent
    if (agent === undefined || resolved === undefined || resolved.kind === "reject") return resolved
    const messages = Array.isArray(payload.messages) ? payload.messages : []
    const text = firstUserTextOf(messages)
    if (text === "" || injectedAgents.has(agent.id)) return resolved
    if (sessionHasInjectedBlock(agent)) { injectedAgents.add(agent.id); return resolved }

    const query = deriveQuery(text)
    let block = ""

    if (config.projectEnabled) {
      const proj = resolveProjectName(probeCwd(agent), config.vault, config.mapFile)
      if (proj?.dir) block += projectContextPreamble(proj.name, proj.dir)
    }
    if (config.kbEnabled) {
      const cached = hitsCacheGet(config.vault, config.db, query)
      block += buildKBBlock(query, cached, config.vault)
      // 未命中 → 后台预热缓存（不阻塞首问）；命中/空命中无需。
      if (cached === null && !isTrivialQuery(query) && config.vault) warmSearch(config, config.vault, query)
    }
    if (block === "") return resolved

    injectedAgents.add(agent.id)
    try {
      ctx.logger?.info?.(`kb-preflight: injected ${(block.includes("<project_context>") ? "project" : "")}${block.includes("<knowledge_base>") ? "+kb" : ""} preamble (${block.length} chars)`)
    } catch { /* logger 缺失时静默 */ }
    return { ...resolved, messages: [messageFor(block), ...messages] }
  }, { prepend: true })

  ctx.on("agent/disposed", (payload) => {
    if (payload?.agent !== undefined) injectedAgents.delete(payload.agent.id)
  })
}

/** 从 agent 对象取会话 cwd（dsh 会话 header/meta 多形态宽容探测）。 */
function probeCwd(agent) {
  const s = agent?.session
  return s?.header?.cwd || s?.meta?.cwd || s?.cwd || ""
}

/* ────────────────────────────── 测试导出 ────────────────────────────────── */
export const _preflightTest = {
  normalizeQueryForCache, deriveQuery, isTrivialQuery, queryTokenCount,
  kbHitsEntryTTL, kbHitsCacheSet, hitsCacheKey, hitsCacheGet,
  summarizeKBIndex, kbIndexDigest,
  firstUserTextOf, sessionHasInjectedBlock, messageFor,
  markdownSection, frontmatterField, adrTitles, projectContextDigest, projectContextPreamble,
  resolveProjectName, buildKBBlock, kbHttpUrl, resolveConfig,
}
