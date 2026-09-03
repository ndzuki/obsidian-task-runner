// Standalone unit test for the KB-first digest helpers in agent-server.mjs.
// Run:  node deploy/dsh-plugins/agent-server.kb.test.mjs
// (Pure-function tests only — no HTTP server, no agents runtime required.)
import assert from "node:assert"
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, utimesSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { _kbTest } from "./agent-server.mjs"

const { kbVaultRoot, kbDbPath, kbIndexPath, summarizeKBIndex, deriveQuery, kbPrecomputePreamble, kbFirstPreamble, projectVaultRoot, resolveProjectDir, projectContextDigest, projectContextPreamble, normalizeQueryForCache, kbCfgFingerprint, kbHitsCacheKey, kbHitsEntryTTL, kbHitsCacheSet, isTrivialQuery, lruCacheSet, markdownSection, contextOverview, frontmatterField, adrDecisionOneLiner, adrTitles, pickSearchTimeout, noteSearchFinished, kbSearchTiming, consumedPathsFromEvents, kbHttpBase, kbHttpUrl, durationBucket, durationHistNote, renderDurationHist, kbStatsSnapshot } = _kbTest

// --- summarizeKBIndex ---
{
  const index = [
    "# References INDEX",
    "> 自动生成于 2026-08-28",
    "> 总计 3 篇",
    "",
    "## Core（平台与架构技术）",
    "",
    "| 文件 | 标题 | 摘要 | topics | activity | hits | level | updated | verified | 引用项目 |",
    "|------|------|------|--------|----------|------|-------|---------|----------|----------|",
    "| core/go/connect-rpc.md | Go Connect RPC 最佳实践 | 本地优先、连接复用 | go, rpc | high | 3 | advanced | 2026-08-01 | true | 001-a |",
    "| extended/k8s/helm.md | Helm 部署要点 | 版本钉死 | k8s, helm | normal | 1 | intermediate | 2026-07-20 | false | 002-b |",
    "| core/db/postgres.md | PostgreSQL 调优 | 索引与连接池 | db, postgres | low | 0 | beginner | 2025-12-01 | true | |",
    "",
    "## 项目引用",
    "",
    "| 项目 | 引用文档数 |",
    "|------|-----------|",
    "| 001-a | 1 |",
  ].join("\n")

  const digest = summarizeKBIndex(index)
  assert.ok(digest.includes("core/go/connect-rpc.md"), "should list core/go/connect-rpc.md")
  assert.ok(digest.includes("Go Connect RPC 最佳实践"), "should list title")
  assert.ok(digest.includes("extended/k8s/helm.md"), "should list extended doc")
  assert.ok(digest.includes("core/db/postgres.md"), "should list third doc")
  assert.ok(!digest.includes("| 文件 |"), "header row must be skipped")
  assert.ok(!digest.includes("|------|"), "separator rows must be skipped")
  console.log("PASS summarizeKBIndex")
}

// --- truncation cap ---
{
  const rows = []
  for (let i = 0; i < 100; i++) {
    rows.push(`| core/topic-${i}.md | 主题 ${i} | 摘要内容 ${i} | topic${i} | normal | 0 | beginner | 2026-08-01 | false | |`)
  }
  const index = "| 文件 | 标题 | 摘要 | topics | activity | hits | level | updated | verified | 引用项目 |\n|------|------|------|--------|----------|------|-------|---------|----------|----------|\n" + rows.join("\n")
  const digest = summarizeKBIndex(index)
  assert.ok(digest.length <= 2400, `digest too long: ${digest.length}`)
  assert.ok(digest.includes("已截断"), "truncated digest should note the cap")
  console.log("PASS summarizeKBIndex truncation")
}

// --- kbVaultRoot / kbIndexPath / kbFirstPreamble ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-kb-test-"))
  try {
    mkdirSync(join(vault, "References"), { recursive: true })
    writeFileSync(join(vault, "References", "INDEX.md"), "# References INDEX\n\n| 文件 | 标题 | 摘要 | topics | activity | hits | level | updated | verified | 引用项目 |\n|------|------|------|--------|----------|------|-------|---------|----------|----------|\n| core/x.md | X 技术 | 摘要 | x | high | 1 | advanced | 2026-08-01 | true | |\n")

    const old = process.env.OTR_KB_VAULT
    try {
      delete process.env.OTR_KB_VAULT
      assert.strictEqual(kbVaultRoot(), "", "empty env -> empty root")
      assert.strictEqual(kbIndexPath(), "", "empty root -> no index path")
      assert.strictEqual(kbFirstPreamble(), "", "no KB configured -> no preamble")

      process.env.OTR_KB_VAULT = vault
      process.env.OTR_KB_DB = "/tmp/kb.sqlite"
      assert.strictEqual(kbVaultRoot(), vault)
      assert.strictEqual(kbDbPath(), "/tmp/kb.sqlite")
      assert.ok(kbIndexPath().endsWith("References/INDEX.md"), "index path under References/INDEX.md")
      // 无命中（hits 非数组）→ 回退索引摘要。
      const pre = kbFirstPreamble("q", null)
      assert.ok(pre.startsWith("<knowledge_base>"), "preamble opens with knowledge_base block")
      assert.ok(pre.includes("core/x.md"), "preamble embeds the index digest")
      assert.ok(pre.includes("踩坑实践"), "preamble mentions pitfall sections")
      assert.ok(pre.includes("otg kb search"), "preamble instructs kb search")
      console.log("PASS kbVaultRoot/kbIndexPath/kbFirstPreamble-fallback")
    } finally {
      if (old === undefined) delete process.env.OTR_KB_VAULT
      else process.env.OTR_KB_VAULT = old
      delete process.env.OTR_KB_DB
    }
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
}

// --- deriveQuery ---
{
  assert.strictEqual(
    deriveQuery("任务 TASK-066 — 一键开发环境：请详细化以下需求……"),
    "一键开发环境：请详细化以下需求……",
    "strip kitty-grill task prefix"
  )
  assert.strictEqual(deriveQuery("任务 TASK-058: 决策写回"), "决策写回")
  assert.ok(deriveQuery("正常问题：" + "很长的内容".repeat(50)).length <= 200, "query capped")
  assert.ok(deriveQuery("  展开  空白  折叠  ").includes("展开 空白 折叠"), true, "whitespace collapsed")
  console.log("PASS deriveQuery")
}

// --- kbPrecomputePreamble / kbFirstPreamble-with-hits ---
{
  const old = process.env.OTR_KB_VAULT
  try {
    process.env.OTR_KB_VAULT = "/kb"
    const hits = [
      { path: "core/go/connect-rpc.md", title: "Go Connect RPC 最佳实践", summary: "本地优先、连接复用", score: 8.5 },
      { path: "core/db/postgres.md", title: "PostgreSQL 调优", summary: "索引与连接池", score: 6.1 },
    ]
    const pre = kbFirstPreamble("go rpc", hits)
    assert.ok(pre.startsWith("<knowledge_base>"), "hits preamble opens knowledge_base")
    assert.ok(pre.includes("已按你的问题预检索"), "hits preamble labels server precompute")
    assert.ok(pre.includes("查询词：go rpc"), "hits preamble embeds query")
    assert.ok(pre.includes("core/go/connect-rpc.md"), "hits preamble lists hit path")
    assert.ok(pre.includes("core/db/postgres.md"), "hits preamble lists second hit")
    assert.ok(pre.includes("top-2"), "hits preamble counts hits")
    assert.ok(pre.includes("踩坑实践"), "hits preamble keeps pitfall rule")
    assert.ok(!pre.includes("| 文件 |"), "hits preamble must not dump index table")
    console.log("PASS kbPrecomputePreamble/kbFirstPreamble-with-hits")
  } finally {
    if (old === undefined) delete process.env.OTR_KB_VAULT
    else process.env.OTR_KB_VAULT = old
  }
}

// --- project-aware context ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-proj-test-"))
  try {
    const proj = join(vault, "Projects", "001-release-manager")
    mkdirSync(join(proj, "Notes", "adr"), { recursive: true })
    writeFileSync(join(proj, "Notes", "CONTEXT.md"), `# CONTEXT\n\n## Development Constraints\n- MySQL 为 test/prod 引擎\n- 迁移走 alembic\n\n## Anti-patterns\n- 不要用 dev SQLite 语义\n\n## Language\n- **rpc**: 统一 Connect\n`)
    writeFileSync(join(proj, "Notes", "PROJECT-CONVENTIONS.md"), "# Conventions\n中文注释\n")
    writeFileSync(join(proj, "Notes", "adr", "ADR-001-connect.md"), "# 采用 Connect RPC\n\n> 决策\n\n## Decision\n统一 Connect + protobuf\n")

    const oldVault = process.env.OTR_PROJECT_VAULT
    const oldKb = process.env.OTR_KB_VAULT
    try {
      delete process.env.OTR_PROJECT_VAULT
      delete process.env.OTR_KB_VAULT
      assert.strictEqual(projectVaultRoot(), "", "no project vault -> empty root")
      assert.strictEqual(resolveProjectDir("release-manager"), "", "no vault -> not resolved")

      process.env.OTR_PROJECT_VAULT = vault
      // 数字前缀目录：去前缀匹配
      const dir = resolveProjectDir("release-manager")
      assert.strictEqual(dir, proj, "resolveProjectDir strips numeric prefix")
      assert.strictEqual(resolveProjectDir("001-release-manager"), proj, "resolveProjectDir exact match")

      const digest = projectContextDigest(proj)
      assert.ok(digest.includes("Constraints"), "digest includes constraints")
      assert.ok(digest.includes("Anti-patterns"), "digest includes anti-patterns")
      assert.ok(digest.includes("ADR-001-connect"), "digest lists ADR")
      assert.ok(digest.includes("PROJECT-CONVENTIONS.md"), "digest notes conventions")

      const pre = projectContextPreamble("release-manager")
      assert.ok(pre.startsWith("<project_context>"), "project preamble opens")
      assert.ok(pre.includes("当前工作区项目：release-manager"), "project preamble names project")
      assert.ok(pre.includes("CONTEXT.md"), "project preamble lists CONTEXT path")
      assert.ok(pre.includes("Notes/adr"), "project preamble lists ADR dir")
      assert.ok(pre.includes("不要从零推理"), "project preamble tells agent not to reason from scratch")
      console.log("PASS project-aware context")
    } finally {
      if (oldVault === undefined) delete process.env.OTR_PROJECT_VAULT
      else process.env.OTR_PROJECT_VAULT = oldVault
      if (oldKb === undefined) delete process.env.OTR_KB_VAULT
      else process.env.OTR_KB_VAULT = oldKb
    }
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
}

// --- A1: 缓存键查询词归一化 ---
{
  assert.strictEqual(normalizeQueryForCache("如何部署 OTG？"), normalizeQueryForCache("如何部署otg"), "case+punct variants share cache key")
  assert.strictEqual(normalizeQueryForCache("Ｈｅｌｌｏ　ｋ８ｓ"), "hello k8s", "full-width → half-width + lowercase")
  assert.strictEqual(normalizeQueryForCache("go, 部署?"), "go 部署", "punctuation stripped")
  assert.strictEqual(normalizeQueryForCache(""), "", "empty → empty")
  console.log("PASS normalizeQueryForCache")
}

// --- A1+A5: 缓存键（归一化 + 配置指纹） ---
{
  const vault = "/vault"
  const db = "/db/kb.sqlite"
  const old = process.env.OTR_MAP_FILE
  try {
    delete process.env.OTR_MAP_FILE
    const k1 = kbHitsCacheKey(vault, db, "如何部署 OTG？")
    const k2 = kbHitsCacheKey(vault, db, "如何部署otg")
    assert.strictEqual(k1, k2, "normalized cache key shared")
    assert.ok(k1.startsWith(`${vault}|${db}|`), "key carries vault|db|fingerprint prefix")
    const kOther = kbHitsCacheKey(vault, "/db/other.sqlite", "如何部署otg")
    assert.notStrictEqual(k1, kOther, "different db → different key")

    // 配置指纹：embedding 配置变化 → 缓存键变化
    const mapFile = join(mkdtempSync(join(tmpdir(), "otr-map-test-")), "vault-map.json")
    writeFileSync(mapFile, JSON.stringify({ kb_embedding: { backend: "ollama", model: "bge-m3", weight: 0.5 } }))
    process.env.OTR_MAP_FILE = mapFile
    const fp1 = kbCfgFingerprint()
    assert.ok(fp1.includes("bge-m3"), "fingerprint reflects embedding config")
    const kEmbed = kbHitsCacheKey(vault, db, "部署")
    writeFileSync(mapFile, JSON.stringify({ kb_embedding: { backend: "openai", model: "text-embedding-3-large", api_key: "x", weight: 0.3 }, kb_rerank: { backend: "llama", model: "r2", top_n: 10 } }))
    const fp2 = kbCfgFingerprint()
    assert.notStrictEqual(fp1, fp2, "config change → new fingerprint")
    const kEmbed2 = kbHitsCacheKey(vault, db, "部署")
    assert.notStrictEqual(kEmbed, kEmbed2, "config change → different cache key")
    console.log("PASS kbHitsCacheKey/kbCfgFingerprint")
  } finally {
    if (old === undefined) delete process.env.OTR_MAP_FILE
    else process.env.OTR_MAP_FILE = old
  }
}

// --- A2: 失败/空命中分池 TTL ---
{
  assert.strictEqual(kbHitsEntryTTL({ kind: "err" }), 30 * 1000, "err cached 30s")
  assert.strictEqual(kbHitsEntryTTL({ kind: "empty" }), 10 * 60 * 1000, "empty cached full TTL")
  assert.strictEqual(kbHitsEntryTTL({ kind: "hits" }), 10 * 60 * 1000, "hits cached full TTL")
  assert.strictEqual(kbHitsEntryTTL(undefined), 10 * 60 * 1000, "unknown → full TTL (conservative)")
  console.log("PASS kbHitsEntryTTL")
}

// --- A3: LRU + TTL 缓存治理 ---
{
  const m = new Map()
  lruCacheSet(m, "a", 1, 60_000, 3)
  lruCacheSet(m, "b", 2, 60_000, 3)
  lruCacheSet(m, "c", 3, 60_000, 3)
  lruCacheSet(m, "d", 4, 60_000, 3) // 超限 → 逐出最旧 a
  assert.strictEqual(m.size, 3, "evicts to max")
  assert.ok(!m.has("a"), "oldest evicted")
  assert.ok(m.has("b") && m.has("c") && m.has("d"), "hot entries kept (no clear-all)")

  lruCacheSet(m, "b", 20, 60_000, 3) // 刷新 b recency
  lruCacheSet(m, "e", 5, 60_000, 3) // 超限 → 逐出最旧 c
  assert.ok(!m.has("c"), "after recency refresh, c (now oldest) evicted")
  assert.ok(m.has("b"), "refreshed entry survives")

  // 过期清扫：过期条目在写入时优先清除，不占容量。
  const m2 = new Map()
  m2.set("old1", { at: Date.now() - 61_000, value: 1 })
  m2.set("old2", { at: Date.now() - 61_000, value: 2 })
  lruCacheSet(m2, "new", 3, 60_000, 2)
  assert.strictEqual(m2.size, 1, "expired swept before eviction")
  assert.ok(m2.has("new"), "new entry present")

  // 回归：按条目各自 TTL 清扫——写 30s 的 err 条目不得误清
  // 30s~10min 之间的 hits/empty 条目（A2+A3 交互）。
  const ttlOf = (v) => v?.kind === "err" ? 30_000 : 600_000
  const m3 = new Map()
  m3.set("q-hits", { at: Date.now() - 120_000, value: { kind: "hits" } }) // 2min 前的有效命中
  lruCacheSet(m3, "q-err", { kind: "err" }, ttlOf, 4)
  assert.ok(m3.has("q-hits"), "per-entry TTL sweep keeps 2min-old hits entry (would be wrongly cleared by unified 30s sweep)")
  assert.ok(m3.has("q-err"), "err entry stored")
  console.log("PASS lruCacheSet")
}

// --- A2+A3: kbHitsCacheSet 条目形态（真缓存写入不抛错、分池可查） ---
{
  kbHitsCacheSet("__test__|db|q-err", { kind: "err" })
  kbHitsCacheSet("__test__|db|q-empty", { kind: "empty" })
  kbHitsCacheSet("__test__|db|q-hits", { kind: "hits", hits: [{ path: "core/go/x.md", title: "X" }] })
  console.log("PASS kbHitsCacheSet entry shapes")
}

// --- A4: 无效查询门禁 ---
{
  assert.strictEqual(isTrivialQuery(""), true, "empty trivial")
  assert.strictEqual(isTrivialQuery("你好"), true, "greeting trivial")
  assert.strictEqual(isTrivialQuery("hi"), true, "short trivial")
  assert.strictEqual(isTrivialQuery("谢谢！"), true, "thanks trivial")
  assert.strictEqual(isTrivialQuery("在吗"), true, "presence check trivial")
  assert.strictEqual(isTrivialQuery("k8s"), true, "single token trivial")
  assert.strictEqual(isTrivialQuery("如何部署 otg"), false, "substantive question")
  assert.strictEqual(isTrivialQuery("为什么任务卡住"), false, "substantive CJK question")
  assert.strictEqual(isTrivialQuery("部署 k8s"), false, "two tokens substantive")
  console.log("PASS isTrivialQuery")
}

// --- C1: CONTEXT.md 小节提取容错（红：当前要求标题逐字匹配，以下必失败） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-c1-test-"))
  try {
    const proj = join(vault, "Projects", "002-tolerance")
    mkdirSync(join(proj, "Notes"), { recursive: true })
    // 变体标题：小写、中文、无 Development 前缀——现状 markdownSection 逐字匹配全部漏提取。
    writeFileSync(join(proj, "Notes", "CONTEXT.md"), `# CONTEXT

## development constraints
- MySQL 为 test/prod 引擎

## 反模式
- 不要用 dev SQLite 语义

## Language
- **rpc**: 统一 Connect
`)
    const old = process.env.OTR_PROJECT_VAULT
    process.env.OTR_PROJECT_VAULT = vault
    try {
      const digest = projectContextDigest(proj)
      assert.ok(digest.includes("Constraints"), "lowercase 'development constraints' heading must be extracted")
      assert.ok(digest.includes("MySQL 为 test/prod 引擎"), "constraints body line kept")
      assert.ok(digest.includes("Anti-patterns"), "中文 '反模式' heading must be extracted")
      assert.ok(digest.includes("SQLite"), "anti-pattern body line kept")
      assert.ok(digest.includes("Language"), "'Language' section must be extracted")
      assert.ok(digest.includes("rpc"), "language body line kept")
    } finally {
      if (old === undefined) delete process.env.OTR_PROJECT_VAULT
      else process.env.OTR_PROJECT_VAULT = old
    }
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
  console.log("PASS C1 tolerant CONTEXT section extraction")
}

// --- C1: 无已知小节时回退注入 CONTEXT 概览（红：现状整块丢失） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-c1f-test-"))
  try {
    const proj = join(vault, "Projects", "003-fallback")
    mkdirSync(join(proj, "Notes"), { recursive: true })
    writeFileSync(join(proj, "Notes", "CONTEXT.md"), `# CONTEXT

- 本项目是金丝雀发布平台
- 服务边界以 API 为准

## 其他小节
- 不被提取的内容
`)
    const old = process.env.OTR_PROJECT_VAULT
    process.env.OTR_PROJECT_VAULT = vault
    try {
      const digest = projectContextDigest(proj)
      assert.ok(digest.includes("Context 概览"), "fallback overview section injected when no known sections match")
      assert.ok(digest.includes("金丝雀发布平台"), "fallback carries first body lines")
    } finally {
      if (old === undefined) delete process.env.OTR_PROJECT_VAULT
      else process.env.OTR_PROJECT_VAULT = old
    }
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
  console.log("PASS C1 CONTEXT fallback overview")
}

// --- C2: ADR 按 mtime 倒序 + status + 决策行（红：现状字典序、无 status/决策） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-c2-test-"))
  try {
    const proj = join(vault, "Projects", "004-adr")
    const adrDir = join(proj, "Notes", "adr")
    mkdirSync(adrDir, { recursive: true })

    const adrOld = join(adrDir, "ADR-001-old.md")
    writeFileSync(adrOld, `---
status: "accepted"
---

# ADR-001: 采用 Connect RPC

## Decision
统一 Connect + protobuf
`)
    const adrNew = join(adrDir, "ADR-002-new.md")
    writeFileSync(adrNew, `---
status: superseded
---

# ADR-002: 迁移 gRPC-Web

## Decision
改用 gRPC-Web 网关
`)
    const adrMid = join(adrDir, "ADR-003-mid.md")
    writeFileSync(adrMid, `---
status: proposed
---

# ADR-003: 引入缓存层

## Decision
进程内 LRU 先行
`)
    // 确定性 mtime：mid 最新 → new 中间 → old 最旧。
    const ts = Math.floor(Date.now() / 1000)
    utimesSync(adrMid, ts, ts - 100)
    utimesSync(adrNew, ts, ts - 1000)
    utimesSync(adrOld, ts, ts - 10000)

    const old = process.env.OTR_PROJECT_VAULT
    process.env.OTR_PROJECT_VAULT = vault
    try {
      const digest = projectContextDigest(proj)
      const iMid = digest.indexOf("ADR-003-mid")
      const iNew = digest.indexOf("ADR-002-new")
      const iOld = digest.indexOf("ADR-001-old")
      assert.ok(iMid >= 0 && iNew >= 0 && iOld >= 0, "all ADRs listed")
      assert.ok(iMid < iNew && iNew < iOld, "ADRs ordered by mtime desc (mid > new > old)")
      assert.ok(digest.includes("superseded"), "ADR status carried")
      assert.ok(digest.includes("accepted"), "quoted status parsed too")
      assert.ok(digest.includes("统一 Connect + protobuf"), "Decision one-liner carried")
    } finally {
      if (old === undefined) delete process.env.OTR_PROJECT_VAULT
      else process.env.OTR_PROJECT_VAULT = old
    }
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
  console.log("PASS C2 ADR mtime order + status + decision")
}

// --- C1/C2 helper 直测（回归护栏） ---
{
  // markdownSection：别名 + 大小写 + 到下一标题即停。
  const doc = `# CONTEXT\n\n## development constraints\n- 行A\n- 行B\n\n### 子小节\n- 不该出现\n\n## Anti-patterns\n- 行C\n`
  const sec = markdownSection(doc, ["development constraints", "constraints"])
  assert.ok(sec.includes("行A") && sec.includes("行B"), "alias section extracted")
  assert.ok(!sec.includes("子小节"), "sub-heading content excluded")
  assert.strictEqual(markdownSection(doc, ["不存在的节"]), "", "unknown section → empty")
  assert.ok(markdownSection(doc, ["anti-patterns", "antipattern", "反模式"]).includes("行C"), "antipattern alias group matches english heading")

  // contextOverview：跳过 H1、遇到小节标题即停。
  const overview = contextOverview("# 标题\n\n- 第一行\n- 第二行\n\n## 某个小节\n- 不该出现")
  assert.ok(overview.includes("第一行") && overview.includes("第二行"), "overview keeps body lines")
  assert.ok(!overview.includes("不该出现"), "overview stops at section heading")

  // frontmatterField：带引号 / 裸值 / 缺失。
  assert.strictEqual(frontmatterField('---\nstatus: "accepted"\n---\n# T', "status"), "accepted", "quoted value")
  assert.strictEqual(frontmatterField("---\nstatus: superseded\n---\n# T", "status"), "superseded", "bare value")
  assert.strictEqual(frontmatterField("# T\n\n正文", "status"), "", "no frontmatter → empty")

  // adrDecisionOneLiner：英文/中文 Decision、引用行跳过、缺失 → ""。
  const en = "# ADR-001\n\n## Decision\n> 引用不该被取\n\n统一 Connect + protobuf\n"
  assert.strictEqual(adrDecisionOneLiner(en), "统一 Connect + protobuf", "english Decision one-liner")
  const zh = "# ADR-002\n\n## 决策\n改用 gRPC-Web 网关\n"
  assert.strictEqual(adrDecisionOneLiner(zh), "改用 gRPC-Web 网关", "chinese 决策 one-liner")
  assert.strictEqual(adrDecisionOneLiner("# ADR-003\n\n## Context\n只有背景\n"), "", "no Decision section → empty")

  // adrTitles：直接断言 mtime 倒序 + status + 决策行格式（同 C2 行为）。
  const vault = mkdtempSync(join(tmpdir(), "otr-c2h-test-"))
  try {
    const proj = join(vault, "Projects", "005-adr-direct")
    const adrDir = join(proj, "Notes", "adr")
    mkdirSync(adrDir, { recursive: true })
    const a = join(adrDir, "ADR-010-old.md")
    writeFileSync(a, '---\nstatus: accepted\n---\n\n# ADR-010: 旧决策\n\n## Decision\n旧方案\n')
    const b = join(adrDir, "ADR-011-new.md")
    writeFileSync(b, '---\nstatus: "superseded"\n---\n\n# ADR-011: 新决策\n\n## Decision\n新方案\n')
    const ts = Math.floor(Date.now() / 1000)
    utimesSync(b, ts, ts - 10)
    utimesSync(a, ts, ts - 1000)
    const list = adrTitles(proj)
    assert.strictEqual(list.length, 2, "both ADRs listed")
    assert.ok(list[0].startsWith("ADR-011-new"), "mtime desc: newest first")
    assert.ok(list[0].includes("superseded"), "status included")
    assert.ok(list[0].includes("新方案"), "decision included")
    assert.ok(list[1].startsWith("ADR-010-old"), "oldest last")
  } finally {
    rmSync(vault, { recursive: true, force: true })
  }
  console.log("PASS C1/C2 helper direct tests")
}

// --- B1: 预检索超时预算选择（红：pickSearchTimeout 尚未实现） ---
{
  assert.strictEqual(pickSearchTimeout({ measured: false, lastAt: 0, lastDurationMs: 0, lastTimedOut: false }, Date.now()), 15000, "first search gets full budget (cold)")
  const now = Date.now()
  const fast = { measured: true, lastAt: now - 1000, lastDurationMs: 500, lastTimedOut: false }
  assert.strictEqual(pickSearchTimeout(fast, now), 4000, "recent fast search → fast budget")
  const slow = { measured: true, lastAt: now - 1000, lastDurationMs: 3500, lastTimedOut: false }
  assert.strictEqual(pickSearchTimeout(slow, now), 15000, "recent slow-but-completed search → full budget")
  const timedOut = { measured: true, lastAt: now - 1000, lastDurationMs: 100, lastTimedOut: true }
  assert.strictEqual(pickSearchTimeout(timedOut, now), 15000, "last timed out → full budget")
  const idleCold = { measured: true, lastAt: now - 6 * 60 * 1000, lastDurationMs: 500, lastTimedOut: false }
  assert.strictEqual(pickSearchTimeout(idleCold, now), 15000, "idle beyond keep_alive → assume model unloaded, full budget")
  const borderline = { measured: true, lastAt: now - 1000, lastDurationMs: 3000, lastTimedOut: false }
  assert.strictEqual(pickSearchTimeout(borderline, now), 4000, "duration at fast-ok boundary → fast budget")
  console.log("PASS pickSearchTimeout")
}

// --- B1: noteSearchFinished 直测（P2-c）：状态写入 + 与预算选择联动 ---
{
  const before = { ...kbSearchTiming }
  noteSearchFinished(2500, false)
  assert.strictEqual(kbSearchTiming.measured, true, "measured flag set")
  assert.ok(kbSearchTiming.lastAt >= before.lastAt, "lastAt advanced")
  assert.strictEqual(kbSearchTiming.lastDurationMs, 2500, "duration recorded")
  assert.strictEqual(kbSearchTiming.lastTimedOut, false, "timedOut recorded")

  noteSearchFinished(500, false)
  assert.strictEqual(pickSearchTimeout(kbSearchTiming, Date.now()), 4000, "recent fast completion → fast budget")

  noteSearchFinished(100, true)
  assert.strictEqual(pickSearchTimeout(kbSearchTiming, Date.now()), 15000, "last timed out → full budget")

  noteSearchFinished(500, false) // 复位为快状态，避免影响后续无关测试
  console.log("PASS noteSearchFinished linkage")
}

// --- D1: 注入→消费率判定（红：consumedPathsFromEvents 尚未实现） ---
{
  const base = 10
  const ev = (seq, type, name, input) => ({ seq, type, data: { name, input } })
  const events = [
    ev(base - 1, "tool/call", "read", { path: "core/go/before.md" }), // firstSeq 之前 → 不计
    ev(base + 1, "tool/call", "read", { path: "core/go/connect-rpc.md" }), // 命中 → 计
    ev(base + 2, "tool/call", "read", { path: "core/go/connect-rpc.md" }), // 重复 → 去重
    ev(base + 3, "tool/call", "grep", { pattern: "x", path: "extended/k8s/helm.md" }), // grep 命中 → 计
    ev(base + 4, "tool/call", "bash", { cmd: "otg kb search 部署" }), // bash 未触路径 → 不计
    ev(base + 5, "tool/call", "read", { path: "core/db/postgres.md" }), // 未注入路径 → 不计
    ev(base + 6, "assistant/message", { message: { content: [{ type: "text", text: "已读 connect-rpc" }] } }), // 非工具事件 → 不计
  ]
  const consumed = consumedPathsFromEvents(events, base, ["core/go/connect-rpc.md", "extended/k8s/helm.md"])
  assert.deepStrictEqual(consumed.sort(), ["core/go/connect-rpc.md", "extended/k8s/helm.md"], "only injected paths consumed via read-family tools counted, deduped, seq-gated")
  assert.deepStrictEqual(consumedPathsFromEvents([], 0, ["x"]), [], "no events → empty")
  assert.deepStrictEqual(consumedPathsFromEvents(events, base, []), [], "no injected paths → empty")
  // 事件形态宽容：input 缺失时用整个 data 序列化匹配
  const loose = [{ seq: base + 1, type: "tool/call", data: { name: "read", path: "core/go/connect-rpc.md" } }]
  assert.deepStrictEqual(consumedPathsFromEvents(loose, base, ["core/go/connect-rpc.md"]), ["core/go/connect-rpc.md"], "loose event shape still matched")
  console.log("PASS consumedPathsFromEvents")
}

// --- E1: INDEX 摘要按查询词相关性排序（agent-server fallback 同款） ---
{
  const rows = []
  for (let i = 1; i <= 40; i++) {
    rows.push(`| core/filler/filler-${String(i).padStart(2, "0")}.md | 填充文档${i} ${"x".repeat(90)} | 无关摘要 ${"y".repeat(70)} | filler |`)
  }
  rows.push("| core/go/connect-rpc.md | Go Connect RPC 实践 | 连接复用与流式 | go, rpc |")
  const index = "| 文件 | 标题 | 摘要 | topics | activity |\n|------|------|------|--------|----------|\n" + rows.join("\n")

  const ranked = summarizeKBIndex(index, "connect rpc")
  assert.ok(ranked.includes("connect-rpc"), "relevant row pulled into head by query")
  assert.ok(ranked.indexOf("connect-rpc") < ranked.indexOf("填充文档"), "relevant row ordered before zero-score fillers")
  console.log("PASS E1 ranked index digest (agent-server)")
}

// --- C3: vault-map 注册门禁（红：resolveProjectDir 尚无注册校验） ---
{
  const vault = mkdtempSync(join(tmpdir(), "otr-c3-test-"))
  const mapFile = join(tmpdir(), "otr-c3-vault-map.json")
  try {
    mkdirSync(join(vault, "Projects", "001-alpha", "Notes"), { recursive: true })
    mkdirSync(join(vault, "Projects", "beta-unregistered", "Notes"), { recursive: true })
    const oldVault = process.env.OTR_PROJECT_VAULT
    const oldMap = process.env.OTR_MAP_FILE
    try {
      process.env.OTR_PROJECT_VAULT = vault
      delete process.env.OTR_MAP_FILE
      assert.ok(resolveProjectDir("beta-unregistered") !== "", "no map → legacy dir match allowed")

      writeFileSync(mapFile, JSON.stringify({ projects: [{ name: "alpha" }] }))
      process.env.OTR_MAP_FILE = mapFile
      assert.ok(resolveProjectDir("alpha") !== "", "registered name still resolves")
      assert.strictEqual(resolveProjectDir("beta-unregistered"), "", "unregistered dir gated out (map present)")
      assert.ok(resolveProjectDir("001-alpha") !== "", "numeric-prefixed name of registered project still resolves")
    } finally {
      if (oldVault === undefined) delete process.env.OTR_PROJECT_VAULT
      else process.env.OTR_PROJECT_VAULT = oldVault
      if (oldMap === undefined) delete process.env.OTR_MAP_FILE
      else process.env.OTR_MAP_FILE = oldMap
    }
  } finally {
    rmSync(vault, { recursive: true, force: true })
    rmSync(mapFile, { force: true })
  }
  console.log("PASS C3 registration gate")
}

// --- B2: 常驻检索端点 URL 组装 ---
{
  assert.strictEqual(kbHttpUrl("http://127.0.0.1:8787", "部署 k8s", 3),
    "http://127.0.0.1:8787/api/kb/search?q=" + encodeURIComponent("部署 k8s") + "&limit=3", "url assembled with encoded query")
  assert.strictEqual(kbHttpUrl("http://127.0.0.1:8787/", "x", 1),
    "http://127.0.0.1:8787/api/kb/search?q=x&limit=1", "trailing slash normalized")
  const old = process.env.OTR_KB_HTTP
  try {
    delete process.env.OTR_KB_HTTP
    assert.strictEqual(kbHttpBase(), "", "no env → empty base (spawn only)")
    process.env.OTR_KB_HTTP = "http://127.0.0.1:8787"
    assert.strictEqual(kbHttpBase(), "http://127.0.0.1:8787", "env base read")
  } finally {
    if (old === undefined) delete process.env.OTR_KB_HTTP
    else process.env.OTR_KB_HTTP = old
  }
  console.log("PASS kbHttpUrl/kbHttpBase")
}

// --- F1: 耗时直方图桶边界 + 记录/渲染（红：durationBucket 尚未实现） ---
{
  const B = [0, 100, 500, 1000, 2000, 4000, 16000] // 7 桶：<100,100-500,500-1k,1-2k,2-4k,4-16k,>=16k
  assert.strictEqual(durationBucket(0, B), 0, "0ms → bucket0")
  assert.strictEqual(durationBucket(99, B), 0, "99ms → bucket0")
  assert.strictEqual(durationBucket(100, B), 1, "100ms → bucket1")
  assert.strictEqual(durationBucket(499, B), 1, "499ms → bucket1")
  assert.strictEqual(durationBucket(500, B), 2, "500ms → bucket2")
  assert.strictEqual(durationBucket(1000, B), 3, "1000ms → bucket3")
  assert.strictEqual(durationBucket(1999, B), 3, "1999ms → bucket3")
  assert.strictEqual(durationBucket(2000, B), 4, "2000ms → bucket4")
  assert.strictEqual(durationBucket(3999, B), 4, "3999ms → bucket4")
  assert.strictEqual(durationBucket(4000, B), 5, "4000ms → bucket5")
  assert.strictEqual(durationBucket(15999, B), 5, "15999ms → bucket5")
  assert.strictEqual(durationBucket(16000, B), 6, "16000ms → bucket6")
  assert.strictEqual(durationBucket(60000, B), 6, "60s → bucket6")

  const hist = new Array(B.length).fill(0)
  durationHistNote(hist, 50)
  durationHistNote(hist, 300)
  durationHistNote(hist, 300)
  assert.deepStrictEqual(hist, [1, 2, 0, 0, 0, 0, 0], "histogram accumulates into buckets")
  const rendered = renderDurationHist(hist, B)
  assert.ok(rendered.includes("<100=1"), "render includes bucket0")
  assert.ok(rendered.includes("100-500=2"), "render includes bucket1")
  console.log("PASS duration histogram")
}

// --- Agent Town 小图：kbStatsSnapshot 快照形状（红：尚未实现） ---
{
  const s = kbStatsSnapshot()
  assert.strictEqual(typeof s.totals.hits, "number", "totals.hits numeric")
  assert.strictEqual(typeof s.totals.misses, "number", "totals.misses numeric")
  assert.strictEqual(typeof s.totals.avgMs, "number", "totals.avgMs numeric")
  assert.ok(Array.isArray(s.totals.hist.boundaries) && s.totals.hist.boundaries.length === 7, "hist boundaries exposed")
  assert.ok(Array.isArray(s.totals.hist.counts) && s.totals.hist.counts.length === 7, "hist counts exposed")
  assert.ok(Array.isArray(s.window.hist.counts) && s.window.hist.counts.length === 7, "window hist counts exposed")
  assert.strictEqual(typeof s.lastLogAt, "number", "lastLogAt numeric")
  console.log("PASS kbStatsSnapshot shape")
}

// --- KB 统计持久化（红：尚未实现——重启归零修复） ---
{
  const { kbStatsTotalsSerialize, kbStatsTotalsDeserialize, loadPersistedTotals, savePersistedTotals, kbStatsFileDefault } = _kbTest
  const totals = { hits: 3, misses: 9, empty: 2, errs: 1, skipped: 4, searchMs: 12345, searchN: 12, hist: [1, 2, 3, 4, 5, 6, 7] }
  assert.deepStrictEqual(kbStatsTotalsDeserialize(kbStatsTotalsSerialize(totals)), totals, "serialize/deserialize round-trip")
  assert.strictEqual(kbStatsTotalsDeserialize("not json at all"), null, "corrupt text → null")
  assert.strictEqual(kbStatsTotalsDeserialize('{"hits":1}'), null, "missing fields → null")
  assert.strictEqual(kbStatsTotalsDeserialize('{"hits":"x","misses":1,"empty":1,"errs":1,"skipped":1,"searchMs":1,"searchN":1,"hist":[1]}'), null, "non-numeric field → null")
  assert.strictEqual(loadPersistedTotals(join(tmpdir(), "no-such-kb-stats.json")), null, "missing file → null")
  const dir = mkdtempSync(join(tmpdir(), "kbstats-"))
  const file = join(dir, "agent-server-kb-stats.json")
  assert.strictEqual(savePersistedTotals(file, totals), true, "save succeeds (creates parent dir)")
  assert.deepStrictEqual(loadPersistedTotals(file), totals, "file round-trip")
  writeFileSync(file, "garbage")
  assert.strictEqual(loadPersistedTotals(file), null, "corrupt file → null")
  assert.strictEqual(typeof kbStatsFileDefault(), "string", "default stats file path is a string")
  rmSync(dir, { recursive: true, force: true })
  console.log("PASS kb stats persistence")
}

// --- Agent Town 会话标签：session.events 兼容 shim（alpha.4+ 移除 .events） ---
{
  const { sessionEvents, firstUserText, labelFromText, sessionCreatedAtMs } = _kbTest

  // 旧版：session.events 直接存在。
  const legacy = { events: [{ type: "user/message", seq: 1, time: 100, data: { content: [{ type: "text", text: "TASK-001 legacy" }] } }] }
  assert.deepStrictEqual(sessionEvents(legacy), legacy.events, "legacy .events returned as-is")

  // 新版：session.events 缺失，需 ownEvents() / snapshotEvents() 读取。
  const snapEvents = [{ type: "agent/inbox/spliced", seq: 2, time: 200, data: { inserted: [{ role: "user", content: [{ type: "text", text: "执行 /obsidian-task-runner-round2 .../Projects/005-dshtui/Tasks/TASK-001-dshtui-v01-core.md" }] }] } }]
  const modern = { ownEvents: () => snapEvents }
  assert.deepStrictEqual(sessionEvents(modern), snapEvents, "ownEvents() fallback used when .events is absent")
  const modernSnapshotOnly = { snapshotEvents: () => snapEvents }
  assert.deepStrictEqual(sessionEvents(modernSnapshotOnly), snapEvents, "snapshotEvents() fallback used when ownEvents is absent")
  assert.deepStrictEqual(sessionEvents({}), [], "no events/own/snapshot -> empty array")

  // firstUserText 在两种事件形态下都能提取。
  assert.strictEqual(firstUserText(modern), "执行 /obsidian-task-runner-round2 .../Projects/005-dshtui/Tasks/TASK-001-dshtui-v01-core.md", "firstUserText reads agent/inbox/spliced via ownEvents")
  assert.strictEqual(firstUserText(legacy), "TASK-001 legacy", "firstUserText reads user/message via .events")

  // labelFromText 从任务 prompt 提取展示字段。
  const label = labelFromText("执行 obsidian-task-runner 阶段任务：\n\n/obsidian-task-runner-round2 /home/user/src/repos/github.com/ndzuki/myNote/Projects/005-dshtui/Tasks/TASK-001-dshtui-v01-core.md")
  assert.strictEqual(label.phase, "round2", "phase from skill")
  assert.strictEqual(label.task, "TASK-001-dshtui-v01-core", "task from TASK path")
  assert.strictEqual(label.project, "005-dshtui", "project from Projects/<dir>/Tasks/")
  assert.strictEqual(labelFromText("").phase, "session", "empty text -> session phase")

  // sessionCreatedAtMs 缺 createdAt 时从 ownEvents 首事件取时间。
  const created = sessionCreatedAtMs({ ownEvents: () => [{ type: "user/message", time: 123456 }] })
  assert.strictEqual(created, 123456, "createdAt falls back to first own event time")
  console.log("PASS sessionEvents/firstUserText/labelFromText/sessionCreatedAtMs")
}

console.log("agent-server KB-first tests: all passed")
