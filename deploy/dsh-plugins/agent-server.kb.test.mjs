// Standalone unit test for the KB-first digest helpers in agent-server.mjs.
// Run:  node deploy/dsh-plugins/agent-server.kb.test.mjs
// (Pure-function tests only — no HTTP server, no agents runtime required.)
import assert from "node:assert"
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { _kbTest } from "./agent-server.mjs"

const { kbVaultRoot, kbDbPath, kbIndexPath, summarizeKBIndex, deriveQuery, kbPrecomputePreamble, kbFirstPreamble, projectVaultRoot, resolveProjectDir, projectContextDigest, projectContextPreamble } = _kbTest

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

console.log("agent-server KB-first tests: all passed")
