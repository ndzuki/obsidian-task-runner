#!/bin/bash
# E2E: target state machine flow (ready→refining→plans/grilling→…)
set -euo pipefail

BIN=${OTG_BIN:-./otg}
PASS=0; FAIL=0

check() {
  local got="$1" want="$2" label="$3"
  if [ "$got" = "$want" ]; then echo "  ✅ $label"; PASS=$((PASS + 1))
  else echo "  ❌ $label: got=$got want=$want"; FAIL=$((FAIL + 1)); fi
}

unset OBSIDIAN_VAULT OMP_CMD

systemctl --user stop omp-task-watcher.service 2>/dev/null || true
sleep 1; pkill -U "$(id -u)" -f "otg daemon" 2>/dev/null || true; sleep 1

VAULT=$(mktemp -d); SKILL_DIR=$(mktemp -d); LOG_DIR=$(mktemp -d)
TESTDIR=$(mktemp -d); FAKE_OMP=$TESTDIR/fake-omp; OMP_LOG=$TESTDIR/omp.log
trap 'rm -rf "$TESTDIR" "$VAULT" "$SKILL_DIR" "$LOG_DIR"; systemctl --user start omp-task-watcher.service 2>/dev/null' EXIT

mkdir -p "$VAULT/Projects/001-lifecycle/Requirements" "$VAULT/Projects/001-lifecycle/Tasks" "$SKILL_DIR/config"

cat > "$SKILL_DIR/config/vault-map.json" <<EOF
{"obsidian_vault": "$VAULT", "omp_cmd": "$FAKE_OMP", "projects": [{"name": "lifecycle", "path": "$TESTDIR/repo"}], "models": {"default": "d"}, "notifications": {"desktop": false}}
EOF

cat > "$FAKE_OMP" <<EOF
#!/bin/bash
echo "FAKE OMP: \$*" >> ${OMP_LOG}
exit 0
EOF
chmod +x "$FAKE_OMP"

get_field() { grep "^$1:" "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" | sed "s/^$1: //" | tr -d '"'; }

run_daemon() { "$BIN" daemon --once --map-file "$SKILL_DIR/config/vault-map.json" --skill-dir "$SKILL_DIR" --log-dir "$LOG_DIR" 2>&1 || true; }

# ── Setup ──
cat > "$VAULT/Projects/001-lifecycle/Requirements/REQ-001-audit-log.md" << 'EOF'
---
id: "001"
title: 审计日志查询
domain: audit
priority: P1
project_id: "001"
depends_on: []
status: defined
---
# 审计日志查询
## 要做什么
为 release_admin 提供带筛选的审计日志查询界面。
## 完成标准
- [ ] AC-001-01 Given 筛选条件，When 查询，Then 返回分页结果。
- [ ] AC-001-02 Given 非 admin，When 访问，Then permission_denied。
EOF

cat > "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" << 'TASKEOF'
---
id: "001"
title: 审计日志查询前端
project: lifecycle
project_id: "001"
status: ready
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
plan_version: 0
assignee: default
req_doc: Projects/001-lifecycle/Requirements/REQ-001-audit-log.md
blocked_by: []
grill_done: false
grill_prev_status: ""
grill_context: ""
---
# TASK-001
TASKEOF

check "$(get_field status)" "ready" "initial status = ready"

# ═══ Phase 1: ready → refining (new: unified maturity gate) ═══
echo "=== Phase 1: ready → refining ==="
run_daemon
check "$(get_field status)" "refining" "ready → refining"

# ═══ Phase 2: refining stays (fake OMP doesn't transition, but daemon respawns) ═══
echo "=== Phase 2: refining repeat (PID recovery) ==="
run_daemon
check "$(get_field status)" "refining" "refining PID skip"

# ═══ Phase 3: needs-grilling bounce with plan_version=0 ═══
echo "=== Phase 3: implementing bounce → plan-review ==="
cat > "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" << 'TASKEOF'
---
id: "001"
title: 审计日志查询前端
project: lifecycle
project_id: "001"
status: needs-grilling
plan_approved: false
merge_approved: false
pending_req: false
plan_version: 0
assignee: default
req_doc: Projects/001-lifecycle/Requirements/REQ-001-audit-log.md
blocked_by: []
grill_prev_status: implementing
grill_done: false
grill_context: "bounced from implementing"
---
# TASK-001
TASKEOF
run_daemon
check "$(get_field status)" "plan-review" "bounce: needs-grilling→plan-review"
check "$(get_field grill_done)" "true" "bounce: grill_done set"

# ═══ Phase 4: plan-review + approved → Round 2 ═══
echo "=== Phase 4: plan-review + plan_approved → Round 2 ==="
"$BIN" update-status "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" plan_approved=true plan_version=1
"$BIN" find-ready "$VAULT" > "$TESTDIR/fr.txt" 2>/dev/null || true
if grep -q '"id":"001"' "$TESTDIR/fr.txt"; then echo "  ✅ Round 2 ready"; PASS=$((PASS + 1))
else echo "  ❌ Round 2 not ready"; FAIL=$((FAIL + 1)); fi

# ═══ Phase 5: pending_req + needs-grilling → refining ═══
echo "=== Phase 5: pending_req + needs-grilling → refining ==="
cat > "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" << 'TASKEOF'
---
id: "001"
title: 审计日志查询前端
project: lifecycle
project_id: "001"
status: needs-grilling
plan_approved: true
merge_approved: false
pending_req: true
plan_version: 1
assignee: default
req_doc: Projects/001-lifecycle/Requirements/REQ-001-audit-log.md
blocked_by: []
grill_done: false
grill_context: ""
---
# TASK-001
TASKEOF
run_daemon
check "$(get_field status)" "refining" "pending_req + needs-grilling → refining"

# ═══ Phase 6: review + pending_req → refining ═══
echo "=== Phase 6: review + pending_req → refining ==="
cat > "$VAULT/Projects/001-lifecycle/Tasks/TASK-001-lifecycle.md" << 'TASKEOF'
---
id: "001"
title: 审计日志查询前端
project: lifecycle
project_id: "001"
status: review
plan_approved: true
merge_approved: true
pending_req: true
plan_version: 1
assignee: default
req_doc: Projects/001-lifecycle/Requirements/REQ-001-audit-log.md
blocked_by: []
---
# TASK-001
TASKEOF
run_daemon
check "$(get_field status)" "refining" "review + pending_req → refining"
check "$(get_field merge_approved)" "false" "merge_approved cleared"

echo ""
echo "═══════════════════════════"
echo "Results: $PASS passed, $FAIL failed"
echo "═══════════════════════════"
[ "$FAIL" -eq 0 ]