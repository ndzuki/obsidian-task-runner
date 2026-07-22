#!/bin/bash
# E2E: grilling notification flow under new state machine (ready→refining)
set -euo pipefail

unset OBSIDIAN_VAULT OMP_CMD
VAULT=$(mktemp -d); SKILL_DIR=$(mktemp -d); TESTDIR=$(mktemp -d)
BIN=${OTG_BIN:-./otg}; FAKE_OMP=$TESTDIR/fake-omp; OMP_LOG=$TESTDIR/omp.log; LOG_DIR=$TESTDIR/logs
trap 'rm -rf "$TESTDIR" "$VAULT" "$SKILL_DIR"; systemctl --user start omp-task-watcher.service 2>/dev/null' EXIT

mkdir -p "$SKILL_DIR/config" "$VAULT/Projects/test-project/Tasks"
cat > "$SKILL_DIR/config/vault-map.json" <<EOF
{"obsidian_vault": "$VAULT", "omp_cmd": "$FAKE_OMP", "projects": [{"name": "test-project", "path": "$VAULT"}], "models": {"default": "d"}, "notifications": {"desktop": false}}
EOF
cat > "$FAKE_OMP" <<EOF
#!/bin/bash
echo "\$(date) ARGS=\$@" >> ${OMP_LOG}
exit 0
EOF
chmod +x "$FAKE_OMP"

PASS=0; FAIL=0
check() {
  local got="$1" want="$2" label="$3"
  if [ "$got" = "$want" ]; then PASS=$((PASS+1)); echo "  ✅ $label"
  else FAIL=$((FAIL+1)); echo "  ❌ $label (got='$got', want='$want')"; fi
}

systemctl --user stop omp-task-watcher.service 2>/dev/null || true
sleep 1; pkill -U "$(id -u)" -f "otg daemon" 2>/dev/null || true; sleep 1

clean_omp_log() { echo -n "" > "$OMP_LOG"; }

# ── Test 1: ready → refining ──
echo "Test 1: ready → refining"
cat > "$VAULT/Projects/test-project/Tasks/TASK-001.md" <<'TEOF'
---
id: "001"
title: "用户登录模块"
project: "test-project"
assignee: "default"
status: ready
req_doc: ""
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
blocked_by: []
---
TEOF
clean_omp_log; rm -rf "$LOG_DIR" && mkdir -p "$LOG_DIR"
$BIN daemon --once --map-file "$SKILL_DIR/config/vault-map.json" --skill-dir "$SKILL_DIR" --log-dir "$LOG_DIR" 2>&1
check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-001.md" | sed 's/status: //')" "refining" "status → refining"

# ── Test 2: blocked → ready → refining (single scan, 3 rounds) ──
echo "Test 2: blocked → ready → refining"
cat > "$VAULT/Projects/test-project/Tasks/TASK-003.md" <<'TEOF'
---
id: "003"
title: "依赖解除测试"
project: "test-project"
assignee: "default"
status: blocked
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
blocked_by: []
---
TEOF
clean_omp_log
$BIN daemon --once --map-file "$SKILL_DIR/config/vault-map.json" --skill-dir "$SKILL_DIR" --log-dir "$LOG_DIR" 2>&1
check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-003.md" | sed 's/status: //')" "refining" "blocked → ready → refining"

# ── Test 3: needs-grilling + grill_done + replan → refining ──
echo "Test 3: needs-grilling + grill_done + replan → refining"
cat > "$VAULT/Projects/test-project/Tasks/TASK-004.md" <<'TEOF'
---
id: "004"
title: "Grilling完成"
project: "test-project"
assignee: "default"
status: needs-grilling
req_doc: ""
plan_approved: false
merge_approved: false
pending_req: false
grill_done: true
grill_resolution: replan
grill_context: "done"
blocked_by: []
---
TEOF
$BIN daemon --once --map-file "$SKILL_DIR/config/vault-map.json" --skill-dir "$SKILL_DIR" --log-dir "$LOG_DIR" 2>&1
check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-004.md" | sed 's/status: //')" "refining" "grill_done + replan → refining"

echo ""
echo "═══════════════════════"
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]