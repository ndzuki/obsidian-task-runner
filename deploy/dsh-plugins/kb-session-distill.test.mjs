// Standalone unit test for the KB session-distill extension (D2 pre-filter).
// Run:  node deploy/dsh-plugins/kb-session-distill.test.mjs
// Node ≥23.6 type-stripping loads the .ts directly; the extension must not
// register anything at import time (default export only wraps pi.on).
import assert from "node:assert"

const mod = await import("../../.omp/extensions/kb-session-distill.ts")
const { shouldDistill, countUserMessages, hasWorkEvidence } = mod

const DISTILL_MARK = "com.otg.kb-distilled"
const MARKED = { type: "custom", customType: DISTILL_MARK }

const user = (n) => ({ type: "message", message: { role: "user", content: `第${n}条消息` } })
const userTop = (n) => ({ role: "user", content: `第${n}条消息` })
const assistant = (text) => ({ type: "message", message: { role: "assistant", content: [{ type: "text", text }] } })
const toolCall = (name) => ({ type: "tool/call", data: { name } })
const assistantToolUse = (name) => ({ type: "message", message: { role: "assistant", content: [{ type: "tool_use", name, input: {} }] } })

// 补齐到 ≥12 条目：避免"长度过短"先于目标条件命中，让每条断言都钉住真实语义。
const pad = (arr) => [...arr, ...Array.from({ length: Math.max(0, 12 - arr.length) }, (_, i) => assistant(`历史消息${i}`))]

// --- countUserMessages ---
assert.strictEqual(countUserMessages([user(1), user(2), user(3)]), 3, "wrapped user messages counted")
assert.strictEqual(countUserMessages([userTop(1), userTop(2)]), 2, "top-level role user counted")

// --- hasWorkEvidence ---
assert.strictEqual(hasWorkEvidence([user(1), assistant("收到")]), false, "chat-only session → no work evidence")
assert.strictEqual(hasWorkEvidence([user(1), toolCall("bash")]), true, "tool/call event → evidence")
assert.strictEqual(hasWorkEvidence([user(1), assistantToolUse("read")]), true, "tool_use content block → evidence")

// --- shouldDistill ---
assert.strictEqual(shouldDistill([user(1), assistant("a")]), false, "too short branch")
{
  const branch = pad([user(1), user(2), user(3), assistant("纯聊天，无工具调用"), assistant("再见")])
  assert.strictEqual(shouldDistill(branch), false, "3 user msgs but no work evidence → skip (D2)")
}
{
  const branch = pad([user(1), user(2), user(3), toolCall("bash"), assistant("完成")])
  assert.strictEqual(shouldDistill(branch), true, "3 user msgs + tool call → trigger")
}
{
  const branch = pad([user(1), user(2), user(3), toolCall("bash"), MARKED])
  assert.strictEqual(shouldDistill(branch), false, "already distilled mark → skip (re-trigger guard)")
}
{
  const branch = pad([user(1), user(2), assistantToolUse("read"), assistant("ok")])
  assert.strictEqual(shouldDistill(branch), false, "2 user msgs + tool → below user threshold")
}
{
  // 自定义阈值：小分支也能精确判定（opts 透传）。
  const small = [user(1), user(2), user(3), toolCall("bash")]
  assert.strictEqual(shouldDistill(small, { minBranchLen: 4, minUserMsgs: 3 }), true, "opts thresholds respected")
}

console.log("kb-session-distill D2 tests: all passed")
