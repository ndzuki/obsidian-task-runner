#!/bin/bash
# E2E: complete task lifecycle from requirement creation through all state transitions.
# Covers the implementing-bounce-with-no-plan fix (TASK-060 regression).
# Run: OTG_BIN=./otg bash test/e2e/full-lifecycle.sh
set -euo pipefail

BIN=${OTG_BIN:-./otg}
PASS=0; FAIL=0

check() {
  local got="$1" want="$2" label="$3"
  if [ "$got" = "$want" ]; then
    echo "  ✅ $label"
    PASS=$((PASS + 1))
  else
    echo "  ❌ $label: got=$got want=$want"
    FAIL=$((FAIL + 1))
  fi
}

check_contains() {
  local haystack="$1" needle="$2" label="$3"
  if echo "$haystack" | grep -qF "$needle"; then
    echo "  ✅ $label"
    PASS=$((PASS + 1))
  else
    echo "  ❌ $label: '$needle' not found in $haystack"
    FAIL=$((FAIL + 1))
  fi
}
# Ensure env vars don't override test vault
unset OBSIDIAN_VAULT OMP_CMD


# ── Setup ──
systemctl --user stop omp-task-watcher.service 2>/dev/null || true
sleep 1; pkill -9 -f "otg daemon" 2>/dev/null || true; sleep 1
trap 'systemctl --user start omp-task-watcher.service 2>/dev/null' EXIT
# Clean up any lingering lock from previous runs
rm -f /tmp/otg-daemon.lock


VAULT=$(mktemp -d)
SKILL_DIR=$(mktemp -d)
LOG_DIR=$(mktemp -d)
FAKE_OMP=/tmp/fake-omp-lifecycle

mkdir -p "$VAULT/Projects/001-lifecycle/Requirements"
mkdir -p "$VAULT/Projects/001-lifecycle/Tasks"
mkdir -p "$SKILL_DIR/config"

cat > "$SKILL_DIR/config/vault-map.json" <<EOF
{"obsidian_vault": "$VAULT", "omp_cmd": "$FAKE_OMP", "projects": [{"name": "lifecycle", "path": "/tmp/lifecycle-repo"}], "models": {"default": "d"}}
EOF

cat > "$FAKE_OMP" <<'SCRIPT'
#!/bin/bash
echo "FAKE OMP: $*" >> /tmp/fake-omp-lifecycle.log
exit 0
SCRIPT
chmod +x "$FAKE_OMP"

run_daemon() {
  "$BIN" daemon --once \
    --map-file "$SKILL_DIR/config/vault-map.json" \
    --skill-dir "$SKILL_DIR" \
    --log-dir "$LOG_DIR" 2>&1 || true
}

get_field() {
  grep "^$1:" "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" | sed "s/^$1: //" | tr -d '"'
}

# ═══════════════════════════════════════════
# Phase 1: Create requirement + task (simulating on_req_changed)
# ═══════════════════════════════════════════

echo "=== Phase 1: Create REQ + TASK ==="

cat > "$VAULT/Projects/001-lifecycle/Requirements/REQ-001-audit-log.md" << 'EOF'
---
id: "001"
title: 审计日志查询
domain: audit
priority: P1
project_id: "001"
depends_on: []
parent_req: "006"
task_size: medium
status: defined
---
# 审计日志查询

## 目标
为 release_admin 提供带筛选的审计日志查询界面。

## 验收标准
- [ ] AC-001-01 Given 筛选条件，When 查询，Then 返回分页结果。
- [ ] AC-001-02 Given 非 admin，When 访问，Then permission_denied。
EOF

cat > "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" << 'TASKEOF'
---
id: "001"
title: 审计日志查询前端
project: lifecycle
project_id: "001"
template: ""
status: ready
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
plan_version: 0
created: "2026-07-20T12:00:00+08:00"
updated: "2026-07-20T12:00:00+08:00"
completed: ""
priority: P1
due_date: ""
estimated_hours: 0
actual_hours: 0
assignee: gpt
reviewer: ""
req_doc: Projects/001-lifecycle/Requirements/REQ-001-audit-log.md
author: ""
tags: []
epic: ""
parent: ""
blocks: []
blocked_by: []
target_branch: ""
target_env: staging
adr_approved: false
adr_proposed: []
grill_prev_status: ""
grill_done: false
grill_context: ""
---

# TASK-001: 审计日志查询前端

## 需求摘要
为 release_admin 提供审计日志查询界面。

## 验收标准
- [ ] AC-001-01 Given 筛选条件，When 查询，Then 返回分页结果。
- [ ] AC-001-02 Given 非 admin，When 访问，Then permission_denied。

## 执行摘要
| 轮次 | 阶段 | 计划版本 | 状态 | 时间戳 |
|------|------|---------|------|--------|
| 1 | — | v0 | ⏳ 待开始 | — |

## 实现计划
### v1 · PENDING

## 实现记录
### Round 1 · PENDING

## 验收记录
### Round 1 · PENDING

## 变更记录
1. `2026-07-20T12:00:00+08:00` — 任务创建，等待就绪
TASKEOF

check "$(get_field status)" "ready" "initial status = ready"

# ═══════════════════════════════════════════
# Phase 2: ready → needs-grilling (daemon picks up)
# ═══════════════════════════════════════════

echo ""
echo "=== Phase 2: ready → needs-grilling ==="

run_daemon
check "$(get_field status)" "needs-grilling" "ready → needs-grilling"
check_contains "$(get_field grill_context)" "审计日志查询前端 (Projects/001-lifecycle/Requirements/REQ-001-audit-log.md)" "grill_context set"

# ═══════════════════════════════════════════
# Phase 3: needs-grilling → reminder (no change)
# ═══════════════════════════════════════════

echo ""
echo "=== Phase 3: needs-grilling → reminder ==="

run_daemon
check "$(get_field status)" "needs-grilling" "reminder: status unchanged"
check "$(get_field grill_done)" "false" "reminder: grill_done still false"

# ═══════════════════════════════════════════
# Phase 4: plan_approved → plan-review
# ═══════════════════════════════════════════

echo ""
echo "=== Phase 4: plan_approved → plan-review ==="

"$BIN" update-status "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" plan_approved=true

run_daemon
check "$(get_field status)" "plan-review" "plan_approved → plan-review"
check "$(get_field grill_done)" "true" "grill_done set true"
check "$(get_field grill_context)" "" "grill_context cleared"
check "$(get_field grill_prev_status)" "" "grill_prev_status cleared"

# ═══════════════════════════════════════════
# Phase 5: Reset and test grill_done path
# ═══════════════════════════════════════════

echo ""
echo "=== Phase 5: grill_done-only → plan-review ==="

# Reset to needs-grilling with grill_done=true (simulating user finished grilling)
"$BIN" update-status "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" \
  status=needs-grilling plan_approved=false grill_done=true grill_context="user finished grilling"

run_daemon
check "$(get_field status)" "plan-review" "grill_done → plan-review"
check "$(get_field plan_approved)" "false" "plan_approved still false"
check "$(get_field grill_prev_status)" "" "grill_prev_status cleared"

# plan-review without plan_approved → NOT ready for Round 2
"$BIN" find-ready "$VAULT" > /tmp/find-ready-output.txt 2>/dev/null || true
if grep -q '"id":"001"' /tmp/find-ready-output.txt 2>/dev/null; then
  echo "  ❌ plan-review without plan_approved should NOT be ready"
  FAIL=$((FAIL + 1))
else
  echo "  ✅ plan-review without plan_approved correctly not ready"
  PASS=$((PASS + 1))
fi

# ═══════════════════════════════════════════
# Phase 6: plan-review + plan_approved → Round 2 ready
# ═══════════════════════════════════════════

echo ""
echo "=== Phase 6: plan-review + plan_approved → Round 2 ==="

"$BIN" update-status "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" \
  plan_approved=true plan_version=1

"$BIN" find-ready "$VAULT" > /tmp/find-ready-output.txt 2>/dev/null || true
if grep -q '"id":"001"' /tmp/find-ready-output.txt 2>/dev/null; then
  echo "  ✅ plan-review + plan_approved → Round 2 ready"
  PASS=$((PASS + 1))
else
  echo "  ❌ plan-review + plan_approved should be ready for Round 2"
  FAIL=$((FAIL + 1))
fi

# ═══════════════════════════════════════════
# Phase 7: 🆕 implementing bounce with plan_version=0
# ═══════════════════════════════════════════

echo ""
echo "=== Phase 7: 🆕 implementing bounce + plan_version=0 → plan-review ==="

# Simulate: task was manually set to implementing (plan_version=0!),
# hit architecture friction, bounced to needs-grilling
cat > "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" << 'TASKEOF'
---
id: "001"
title: 审计日志查询前端
project: lifecycle
project_id: "001"
template: ""
status: needs-grilling
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
plan_version: 0
created: "2026-07-20T12:00:00+08:00"
updated: "2026-07-20T18:00:00+08:00"
completed: ""
priority: P1
due_date: ""
estimated_hours: 0
actual_hours: 0
assignee: gpt
reviewer: ""
req_doc: Projects/001-lifecycle/Requirements/REQ-001-audit-log.md
author: ""
tags: []
epic: ""
parent: ""
blocks: []
blocked_by: []
target_branch: task/001-lifecycle
target_env: staging
adr_approved: false
adr_proposed: []
grill_prev_status: implementing
grill_done: false
grill_context: "implementation blocked — no plan, worktree detached"
---

# TASK-001: 审计日志查询前端

## 需求摘要
为 release_admin 提供审计日志查询界面。

## Round 2 阻塞
> ⚠️ 实现前置条件不成立。

- **阻塞类型**: 架构摩擦 / 缺失实现计划
- **当前 AC**: 尚未开始
- **问题描述**: plan_version=0，worktree 落后
TASKEOF

check "$(get_field status)" "needs-grilling" "bounce: status = needs-grilling"
check "$(get_field grill_prev_status)" "implementing" "bounce: grill_prev_status = implementing"
check "$(get_field plan_version)" "0" "bounce: plan_version = 0"
check "$(get_field grill_done)" "false" "bounce: grill_done = false"

run_daemon

# ⚠️ CRITICAL: should auto-transition to plan-review, NOT stay in needs-grilling!
check "$(get_field status)" "plan-review" "🆕 bounce: auto → plan-review"
check "$(get_field grill_done)" "true" "🆕 bounce: grill_done set true"
check "$(get_field grill_context)" "" "🆕 bounce: grill_context cleared"
check "$(get_field grill_prev_status)" "" "🆕 bounce: grill_prev_status cleared"

# Next: set plan_approved and verify Round 2 ready
"$BIN" update-status "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" \
  plan_approved=true plan_version=1

"$BIN" find-ready "$VAULT" > /tmp/find-ready-output.txt 2>/dev/null || true
if grep -q '"id":"001"' /tmp/find-ready-output.txt 2>/dev/null; then
  echo "  ✅ after bounce recovery: Round 2 ready"
  PASS=$((PASS + 1))
else
  echo "  ❌ after bounce recovery: should be Round 2 ready"
  FAIL=$((FAIL + 1))
fi

# ═══════════════════════════════════════════
# Phase 8: pending_req + needs-grilling → reset
# ═══════════════════════════════════════════

echo ""
echo "=== Phase 8: pending_req + needs-grilling → reset ==="

cat > "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" << 'TASKEOF'
---
id: "001"
title: 审计日志查询前端
project: lifecycle
project_id: "001"
template: ""
status: needs-grilling
plan_approved: true
merge_approved: false
pending_req: true
off_peak_only: false
plan_version: 1
created: "2026-07-20T12:00:00+08:00"
updated: "2026-07-20T18:00:00+08:00"
completed: ""
priority: P1
assignee: gpt
req_doc: Projects/001-lifecycle/Requirements/REQ-001-audit-log.md
grill_prev_status: ""
grill_done: true
grill_context: "stale"
---
# TASK-001
TASKEOF

run_daemon
check "$(get_field status)" "ready" "pending_req → reset to ready"
check "$(get_field pending_req)" "false" "pending_req cleared"
check "$(get_field plan_approved)" "false" "plan_approved reset"
check "$(get_field grill_done)" "false" "grill_done reset"

# ═══════════════════════════════════════════
# Summary
# ═══════════════════════════════════════════

echo ""
echo "═══════════════════════════════════"
echo "Full Lifecycle E2E Results"
echo "═══════════════════════════════════"
echo "Passed: $PASS"
echo "Failed: $FAIL"
echo "═══════════════════════════════════"

if [ "$FAIL" -gt 0 ]; then
  echo "❌ SOME TESTS FAILED"
  exit 1
else
  echo "✅ ALL TESTS PASSED"
fi
