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
 *   GET  /agents                      → 200 [ { sessionId, phase, task, status, elapsed } ]
 *   GET  /monitor（或 /）             → agent-monitor.html 监控面板
 *   POST /agent/run  body: { task, provider, model, reasoningEffort?, sessionId? }
 *     → 200 { text, outcome, sessionId, errorCode? }
 *     outcome: completed | error | timeout | context_window | quota | key_unavailable | interrupted
 *   POST /agent/chat body: { message, provider, model, reasoningEffort?, sessionId? }
 *     → 200 { text, outcome, sessionId, errorCode? }（多轮交互）
 *   POST /agent/close body: { sessionId } → 200 { ok: true }（取消交互会话）
 */
import { createServer } from "node:http"
import { randomUUID } from "node:crypto"
import { readFileSync, existsSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

export const name = "agent-server"
export const inject = ["agents"]

const ALLOWED_KEYS = new Set(["port", "host"])

/** 交互会话空闲回收窗口：客户端死亡（tab 被直接关闭）后不再有 /agent/close，
 * 超过该时长的 chat 会话在 /agents 枚举时惰性取消，避免僵尸条目永久驻留。 */
const CHAT_IDLE_DISPOSE_MS = 30 * 60 * 1000

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
  return { phase, task }
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

  // agent 销毁时回收状态。
  ctx.on("agent/disposed", (payload) => {
    const agent = payload?.agent
    if (agent !== undefined) {
      effortByAgent.delete(agent.id)
      const sid = String(agent.session?.id)
      liveAgents.delete(sid)
      chatLastAt.delete(sid)
      closedChat.delete(sid)
    }
  })

  /** 获取（create/resume）一个 agent，并注入本会话的 reasoningEffort。 */
  async function acquireAgent(payload) {
    const provider = payload.provider
    const model = payload.model
    if (typeof provider !== "string" || typeof model !== "string") throw new TypeError("provider/model must be strings")

    const agentOptions = { provider, model }
    const setup = undefined

    let agent
    if (payload.sessionId !== undefined && payload.sessionId !== "") {
      agent = await agents.resume({
        resumeSessionId: payload.sessionId,
        agentOptions,
        setup,
      })
    } else {
      const created = await agents.create({
        sessionId: `session-${randomUUID()}`,
        meta: { cwd: process.cwd() },
        agentOptions,
        setup,
      })
      agent = created.agent
    }

    if (payload.reasoningEffort !== undefined && payload.reasoningEffort !== "") {
      effortByAgent.set(agent.id, payload.reasoningEffort)
    }
    return agent
  }

  /** 驱动一个 agent 到 quiescence 并收集结果。 */
  async function runAgent(payload) {
    const task = payload.task
    if (typeof task !== "string" || task.length === 0) throw new TypeError("task must be a non-empty string")
    const agent = await acquireAgent(payload)

    await agent.whenIdle()
    const firstSeq = agent.session.seq
    agent.followup(userMessage(task))
    await agent.whenIdle()
    const outcome = summarize(agent.session.events, firstSeq)
    return {
      text: outcome.text,
      outcome: mapOutcome(outcome.reason),
      sessionId: String(agent.session.id),
      errorCode: outcome.reason?.kind === "error" ? outcome.reason.error?.code : undefined,
    }
  }

  /** 交互式一问一答：sessionId 命中 liveAgents 时复用同一 agent（多轮上下文
   *  延续）；否则 create 新 agent 并缓存。返回本轮模型回复与 sessionId。 */
  async function runChat(payload) {
    const message = payload.message
    if (typeof message !== "string" || message.length === 0) throw new TypeError("message must be a non-empty string")

    const sid = payload.sessionId
    let agent
    if (sid !== undefined && sid !== "") {
      agent = liveAgents.get(sid)
      if (agent === undefined) {
        // 会话不在本进程（daemon 重启后）：resume 恢复。
        agent = await acquireAgent(payload)
        liveAgents.set(String(agent.session.id), agent)
      }
    } else {
      agent = await acquireAgent(payload)
      liveAgents.set(String(agent.session.id), agent)
    }

    const sessionKey = String(agent.session.id)
    chatLastAt.set(sessionKey, Date.now())
    closedChat.delete(sessionKey)

    await agent.whenIdle()
    const firstSeq = agent.session.seq
    agent.followup(userMessage(message))
    await agent.whenIdle()
    chatLastAt.set(sessionKey, Date.now())
    const outcome = summarize(agent.session.events, firstSeq)
    return {
      text: outcome.text,
      outcome: mapOutcome(outcome.reason),
      sessionId: sessionKey,
      errorCode: outcome.reason?.kind === "error" ? outcome.reason.error?.code : undefined,
    }
  }

  /** 取消一个交互会话（客户端退出后调用，或僵尸惰性回收）。 */
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
    closedChat.add(sessionKey)
  }

  /** 监控面板：列出 live agents（run + chat），chat 僵尸惰性回收。 */
  function listAgents() {
    const now = Date.now()
    for (const [sid, lastAt] of chatLastAt) {
      if (now - lastAt > CHAT_IDLE_DISPOSE_MS) closeChat(sid)
    }
    const out = []
    for (const agent of agents.list()) {
      const sid = String(agent.session?.id ?? agent.id)
      if (closedChat.has(sid)) continue
      const text = firstUserText(agent.session)
      const { phase, task } = labelFromText(text)
      out.push({
        sessionId: sid,
        phase,
        task,
        status: agent.status === "idle" ? "idle" : "working",
        elapsed: Math.max(0, Math.floor((now - sessionCreatedAtMs(agent.session)) / 1000)),
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
      res.writeHead(200, { "content-type": "application/json" })
      res.end(JSON.stringify(listAgents()))
      return
    }
    if (req.method === "GET" && (req.url === "/monitor" || req.url === "/")) {
      serveMonitor(res)
      return
    }
    if (req.method !== "POST" ||
      (req.url !== "/agent/run" && req.url !== "/agent/chat" && req.url !== "/agent/close")) {
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
