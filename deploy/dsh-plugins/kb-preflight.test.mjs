// Standalone unit test for kb-preflight.mjs (dsh web KB-first preflight plugin).
// Run:  node deploy/dsh-plugins/kb-preflight.test.mjs
// Pure-function tests only — no cordis runtime, no agents.
import assert from "node:assert"
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, utimesSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { _preflightTest } from "./kb-preflight.mjs"

const {
  normalizeQueryForCache, deriveQuery, isTrivialQuery, queryTokenCount,
  kbHitsCacheSet, kbHitsEntryTTL, hitsCacheKey, summarizeKBIndex, kbIndexDigest,
  firstUserTextOf, sessionHasInjectedBlock, markdownSection, frontmatterField,
  adrTitles, projectContextDigest, projectContextPreamble, resolveProjectName,
  buildKBBlock, messageFor, kbHttpUrl, resolveConfig,
} = _preflightTest

// --- 查询词归一化（A1 同语义） ---
{
  assert.strictEqual(normalizeQueryForCache("如何部署 OTG？"), normalizeQueryForCache("如何部署otg"), "case+punct variants share key")
  assert.strictEqual(queryTokenCount(normalizeQueryForCache("k8s")), 1, "single latin token count")
  assert.strictEqual(isTrivialQuery("你好"), true, "greeting trivial")
  assert.strictEqual(isTrivialQuery("为什么任务卡住"), false, "substantive not trivial")
  assert.strictEqual(deriveQuery("任务 TASK-012 — 修复登录").startsWith("修复登录"), true, "TASK prefix stripped")
  console.log("PASS query helpers")
}

// --- 缓存分池 + LRU ---
{
  assert.strictEqual(kbHitsEntryTTL({ kind: "err" }), 30 * 1000, "err short TTL")
  assert.strictEqual(kbHitsEntryTTL({ kind: "empty" }), 10 * 60 * 1000, "empty full TTL")
  kbHitsCacheSet(hitsCacheKey("/v", "/d", "", "部署 k8s"), { kind: "hits", hits: [{ path: "core/k8s/x.md", title: "X" }] })
  console.log("PASS cache entry shapes")
}

// --- INDEX 摘要 ---
{
  const index = "| 文件 | 标题 | 摘要 | topics | activity |\n|------|------|------|--------|----------|\n| core/go/connect-rpc.md | Go Connect RPC | 连接复用 | go, rpc | high |"
  const digest = summarizeKBIndex(index)
  assert.ok(digest.includes("core/go/connect-rpc.md"), "index row summarized")
  assert.ok(!digest.includes("| 文件 |"), "header skipped")
  console.log("PASS summarizeKBIndex")
}

// --- 消息文本提取与注入防重 ---
{
  const msg = (text) => ({ role: "user", content: [{ type: "text", text }] })
  assert.strictEqual(firstUserTextOf([msg("问题一")]), "问题一", "first user text extracted")
  assert.strictEqual(firstUserTextOf([{ role: "user", content: "纯字符串" }]), "纯字符串", "string content extracted")
  assert.strictEqual(firstUserTextOf([{ role: "assistant", content: [{ type: "text", text: "答" }] }, msg("问题")]), "问题", "assistant skipped")

  const events = [
    { type: "user/message", data: { message: msg("之前") } },
    { type: "user/message", data: { message: msg("<knowledge_base>\n已注入\n</knowledge_base>") } },
  ]
  assert.strictEqual(sessionHasInjectedBlock({ session: { events } }), true, "injected block detected")
  assert.strictEqual(sessionHasInjectedBlock({ session: { events: [{ type: "user/message", data: { message: msg("普通消息") } }] } }), false, "no block → false")
  assert.strictEqual(sessionHasInjectedBlock({ session: { events: [] } }), false, "empty events → false")
  console.log("PASS message extraction / dedup guard")
}

// --- 项目上下文（C1/C2 行为回归） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-pf-test-"))
  try {
    const proj = join(vault, "Projects", "002-tolerance")
    mkdirSync(join(proj, "Notes", "adr"), { recursive: true })
    writeFileSync(join(proj, "Notes", "CONTEXT.md"), "# CONTEXT\n\n## development constraints\n- MySQL 为 test/prod 引擎\n\n## 反模式\n- 不要用 dev SQLite 语义\n")
    const adrOld = join(proj, "Notes", "adr", "ADR-001-old.md")
    writeFileSync(adrOld, '---\nstatus: "accepted"\n---\n\n# ADR-001: 旧决策\n\n## Decision\n旧方案\n')
    const adrNew = join(proj, "Notes", "adr", "ADR-002-new.md")
    writeFileSync(adrNew, '---\nstatus: superseded\n---\n\n# ADR-002: 新决策\n\n## Decision\n新方案\n')
    const ts = Math.floor(Date.now() / 1000)
    utimesSync(adrNew, ts, ts - 10)
    utimesSync(adrOld, ts, ts - 1000)

    const digest = projectContextDigest(proj)
    assert.ok(digest.includes("Constraints") && digest.includes("MySQL"), "lowercase constraints heading tolerated")
    assert.ok(digest.includes("Anti-patterns"), "chinese anti-patterns heading tolerated")
    assert.ok(digest.indexOf("ADR-002-new") < digest.indexOf("ADR-001-old"), "ADR mtime desc order")
    assert.ok(digest.includes("superseded"), "ADR status carried")

    const pre = projectContextPreamble("release-manager", proj)
    assert.ok(pre.includes("<project_context>") && pre.includes("不要从零推理"), "preamble shapes")
    assert.strictEqual(projectContextPreamble("x", ""), "", "no dir → no preamble")
    console.log("PASS project context")
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
}

// --- 项目名解析（cwd → vault-map / Projects 目录） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-pf-resolve-"))
  try {
    const projDir = join(vault, "Projects", "001-release-manager")
    mkdirSync(join(projDir, "Notes"), { recursive: true })
    const checkout = join(tmpdir(), "release-manager-checkout")
    mkdirSync(checkout, { recursive: true })
    const mapFile = join(tmpdir(), "vault-map.json")
    writeFileSync(mapFile, JSON.stringify({ projects: [{ name: "release-manager", path: checkout }] }))
    try {
      const byPath = resolveProjectName(checkout, vault, mapFile)
      assert.strictEqual(byPath?.name, "release-manager", "checkout cwd → vault-map path match")
      assert.strictEqual(byPath?.dir, projDir, "project dir resolved to vault Projects dir")

      const bySubdir = resolveProjectName(join(checkout, "cmd"), vault, mapFile)
      assert.strictEqual(bySubdir?.name, "release-manager", "subdir of checkout → ancestor match")

      const byProjectsDir = resolveProjectName(projDir, vault, mapFile)
      assert.strictEqual(byProjectsDir?.name, "release-manager", "vault Projects dir cwd → numeric prefix strip")

      const none = resolveProjectName(join(tmpdir(), "elsewhere"), vault, mapFile)
      assert.strictEqual(none, "", "unrelated cwd → no project")
    } finally {
      rmSync(vault, { recursive: true, force: true })
      rmSync(checkout, { recursive: true, force: true })
      rmSync(mapFile, { force: true })
    }
    console.log("PASS resolveProjectName")
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
}

// --- KB 块组装（非阻塞语义：命中 → 命中块；未命中 → 摘要块；trivial → 空） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-pf-kb-"))
  try {
    mkdirSync(join(vault, "References"), { recursive: true })
    writeFileSync(join(vault, "References", "INDEX.md"),
      "| 文件 | 标题 | 摘要 | topics |\n|------|------|------|--------|\n| core/go/x.md | Go X 指南 | 速查 | go |\n")
    const hits = [{ path: "core/go/x.md", title: "Go X 指南", summary: "速查" }]
    const hitBlock = buildKBBlock("部署 go", { kind: "hits", hits }, vault)
    assert.ok(hitBlock.includes("预检索到以下经验"), "hits block injected")
    assert.ok(hitBlock.includes("core/go/x.md"), "hit listed")

    const fallbackBlock = buildKBBlock("部署 go", null, vault)
    assert.ok(fallbackBlock.includes("先查已有经验"), "fallback digest block injected")
    assert.ok(fallbackBlock.includes("Go X 指南"), "digest content present")

    assert.strictEqual(buildKBBlock("你好", null, vault), "", "trivial query → no KB block")
    assert.strictEqual(buildKBBlock("部署 go", null, ""), "", "no vault → no KB block")
    console.log("PASS buildKBBlock")
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
}

// --- 合成消息形状 ---
{
  const m = messageFor("<knowledge_base>\nX\n</knowledge_base>")
  assert.strictEqual(m.role, "user", "synthetic message role")
  assert.strictEqual(m.content[0].type, "text", "text content block")
  assert.strictEqual(m.source.kind, "plugin", "plugin source for traceability")
  assert.strictEqual(m.source.plugin, "kb-preflight", "plugin name")
  console.log("PASS messageFor")
}

// --- E1: INDEX 摘要按查询词相关性排序 ---
{
  const rows = []
  for (let i = 1; i <= 40; i++) {
    rows.push(`| core/filler/filler-${String(i).padStart(2, "0")}.md | 填充文档${i} ${"x".repeat(90)} | 与主题无关的摘要 ${"y".repeat(70)} | filler |`)
  }
  rows.push("| core/k8s/deploy-rollout.md | K8s 部署回滚手册 | 部署 rollout 的踩坑与回滚步骤 | k8s, 部署 |")
  const index = "| 文件 | 标题 | 摘要 | topics | activity |\n|------|------|------|--------|----------|\n" + rows.join("\n")

  const unranked = summarizeKBIndex(index)
  assert.ok(!unranked.includes("deploy-rollout"), "unranked digest excludes far-tail relevant row (beyond 2400 cap)")

  const ranked = summarizeKBIndex(index, "部署")
  assert.ok(ranked.includes("deploy-rollout"), "ranked digest pulls far-tail relevant row into head")
  assert.ok(ranked.indexOf("deploy-rollout") < ranked.indexOf("填充文档"), "relevant row ordered before zero-score fillers")

  // 多 token 查询：命中 2 词的排在最前。
  const multi = summarizeKBIndex(index, "部署 回滚")
  assert.ok(multi.includes("deploy-rollout"), "multi-token query still pulls relevant row")
  console.log("PASS E1 ranked index digest")
}

// --- C3: 项目解析注册门禁（红：branch 2 尚无注册校验） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-pf-c3-"))
  const mapFile = join(tmpdir(), "otr-pf-c3-map.json")
  try {
    mkdirSync(join(vault, "Projects", "001-alpha", "Notes"), { recursive: true })
    mkdirSync(join(vault, "Projects", "beta-unregistered", "Notes"), { recursive: true })
    writeFileSync(mapFile, JSON.stringify({ projects: [{ name: "alpha" }] }))

    assert.strictEqual(resolveProjectName(join(vault, "Projects", "001-alpha"), vault, mapFile)?.name, "alpha", "registered dir resolves")
    assert.strictEqual(resolveProjectName(join(vault, "Projects", "beta-unregistered"), vault, mapFile), "", "unregistered dir gated out (map present)")

    const noMap = join(tmpdir(), "otr-pf-c3-nomap.json")
    assert.ok(resolveProjectName(join(vault, "Projects", "beta-unregistered"), vault, noMap) !== "", "map missing → legacy dir match allowed")
    rmSync(noMap, { force: true })
    console.log("PASS C3 project resolution gate")
  } finally {
    rmSync(vault, { recursive: true, force: true })
    rmSync(mapFile, { force: true })
  }
}

// --- B2: 常驻检索端点 URL 组装 ---
{
  assert.strictEqual(kbHttpUrl("http://127.0.0.1:8787", "部署 k8s", 3),
    "http://127.0.0.1:8787/api/kb/search?q=" + encodeURIComponent("部署 k8s") + "&limit=3", "url assembled with encoded query")
  assert.strictEqual(kbHttpUrl("http://127.0.0.1:8787/", "x", 1),
    "http://127.0.0.1:8787/api/kb/search?q=x&limit=1", "trailing slash normalized")
  assert.strictEqual(kbHttpUrl("http://127.0.0.1:8787", "x", 3, true),
    "http://127.0.0.1:8787/api/kb/search?q=x&limit=3&rerank=false", "no-rerank URL assembled")
  console.log("PASS kbHttpUrl")
}

// --- 审查修复：kbHttp 默认端点从 vault-map vault_web_addr 推导 ---
{
  const mapDir = mkdtempSync(join(tmpdir(), "otr-pf-cfg-"))
  try {
    const mapFile = join(mapDir, "vault-map.json")
    writeFileSync(mapFile, JSON.stringify({ vault_web_addr: "127.0.0.1:9999" }))
    const cfg = resolveConfig({ mapFile })
    assert.strictEqual(cfg.kbHttp, "http://127.0.0.1:9999", "kbHttp derived from map vault_web_addr")
    assert.strictEqual(resolveConfig({ mapFile, kbHttp: "" }).kbHttp, "", "explicit kbHttp override wins (disable HTTP)")
    assert.strictEqual(resolveConfig({ mapFile, kbHttp: "http://x:1" }).kbHttp, "http://x:1", "explicit kbHttp override wins (custom)")
    console.log("PASS kbHttp config resolution")
  } finally {
    rmSync(mapDir, { recursive: true, force: true })
  }
}

console.log("kb-preflight tests: all passed")
