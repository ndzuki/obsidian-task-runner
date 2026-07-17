#!/bin/bash
# E2E test for grilling notification flow
# Run: OBSIDIAN_VAULT= OMP_CMD= bash test/e2e/grilling-flow.sh

set -euo pipefail

# ── Setup ──
unset OBSIDIAN_VAULT OMP_CMD
VAULT=$(mktemp -d)
SKILL_DIR=$(mktemp -d)
BIN=${OTG_BIN:-./otg}
FAKE_OMP=/tmp/fake-omp-for-test

mkdir -p "$SKILL_DIR/config" "$VAULT/Projects/test-project/Tasks"

cat > "$SKILL_DIR/config/vault-map.json" <<EOF
{"obsidian_vault": "$VAULT", "omp_cmd": "$FAKE_OMP", "projects": [{"name": "test-project", "path": "$VAULT"}], "models": {"default": "d"}}
EOF

cat > "$FAKE_OMP" <<'SCRIPT'
#!/bin/bash
echo "$(date) ARGS=$@" >> /tmp/otg-e2e-omp.log
exit 0
SCRIPT
chmod +x "$FAKE_OMP"

PASS=0; FAIL=0
check() {
  local got="$1" want="$2" label="$3"
  if [ "$got" = "$want" ]; then
    PASS=$((PASS+1)); echo "  ✅ $label"
  else
    FAIL=$((FAIL+1)); echo "  ❌ $label (got='$got', want='$want')"
  fi
}

# stop real daemon during test
systemctl --user stop omp-task-watcher.service 2>/dev/null || true
sleep 1; pkill -9 -f "otg daemon" 2>/dev/null || true; sleep 1
trap 'systemctl --user start omp-task-watcher.service 2>/dev/null' EXIT

clean_omp_log() { echo -n "" > /tmp/otg-e2e-omp.log; }

# ── Test 1: ready → needs-grilling ──
echo "Test 1: ready → needs-grilling"

cat > "$VAULT/Projects/test-project/Tasks/TASK-001.md" <<'EOF'
---
id: "001"
title: "用户登录模块"
project: "test-project"
assignee: "default"
status: ready
req_doc: "Requirements/REQ-001-login.md"
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
blocked_by: []
---
EOF

clean_omp_log
rm -rf /tmp/otg-e2e-logs && mkdir -p /tmp/otg-e2e-logs

$BIN daemon --once \
  --map-file "$SKILL_DIR/config/vault-map.json" \
  --skill-dir "$SKILL_DIR" \
  --log-dir /tmp/otg-e2e-logs 2>&1

check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-001.md" | sed 's/status: //')" \
      "needs-grilling" "status → needs-grilling"
check "$(grep '^grill_context:' "$VAULT/Projects/test-project/Tasks/TASK-001.md" | sed 's/grill_context: //')" \
      "用户登录模块 (Requirements/REQ-001-login.md)" "grill_context set"
check "$(wc -l < /tmp/otg-e2e-omp.log 2>/dev/null || echo 0)" \
      "0" "OMP not spawned"

# ── Test 2: needs-grilling → reminder, no OMP, no status change ──
echo "Test 2: needs-grilling → reminder only"

clean_omp_log
$BIN daemon --once \
  --map-file "$SKILL_DIR/config/vault-map.json" \
  --skill-dir "$SKILL_DIR" \
  --log-dir /tmp/otg-e2e-logs 2>&1

check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-001.md" | sed 's/status: //')" \
      "needs-grilling" "status unchanged"
check "$(wc -l < /tmp/otg-e2e-omp.log 2>/dev/null || echo 0)" \
      "0" "OMP not spawned"

# ── Test 3: pending_req + needs-grilling → reset to ready ──
echo "Test 3: pending_req + needs-grilling → reset to ready"

cat > "$VAULT/Projects/test-project/Tasks/TASK-001.md" <<'EOF'
---
id: "001"
title: "用户登录模块"
project: "test-project"
assignee: "default"
status: needs-grilling
req_doc: "Requirements/REQ-001-login.md"
plan_approved: false
merge_approved: false
pending_req: true
off_peak_only: false
blocked_by: []
---
EOF

$BIN daemon --once \
  --map-file "$SKILL_DIR/config/vault-map.json" \
  --skill-dir "$SKILL_DIR" \
  --log-dir /tmp/otg-e2e-logs 2>&1

check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-001.md" | sed 's/status: //')" \
      "ready" "status → ready"
check "$(grep '^pending_req:' "$VAULT/Projects/test-project/Tasks/TASK-001.md" | sed 's/pending_req: //')" \
      "false" "pending_req cleared"

# ── Test 4: blocked → unblock → ready → grilling (2 scans) ──
echo "Test 4: blocked → unblock → grilling"

cat > "$VAULT/Projects/test-project/Tasks/TASK-003.md" <<'EOF'
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
EOF

clean_omp_log

# Scan 1: unblock
$BIN daemon --once \
  --map-file "$SKILL_DIR/config/vault-map.json" \
  --skill-dir "$SKILL_DIR" \
  --log-dir /tmp/otg-e2e-logs 2>&1
check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-003.md" | sed 's/status: //')" \
      "ready" "scan1: blocked → ready"

# Scan 2: grilling
clean_omp_log
$BIN daemon --once \
  --map-file "$SKILL_DIR/config/vault-map.json" \
  --skill-dir "$SKILL_DIR" \
  --log-dir /tmp/otg-e2e-logs 2>&1
check "$(grep '^status:' "$VAULT/Projects/test-project/Tasks/TASK-003.md" | sed 's/status: //')" \
      "needs-grilling" "scan2: ready → needs-grilling"
check "$(wc -l < /tmp/otg-e2e-omp.log 2>/dev/null || echo 0)" \
      "0" "scan2: OMP not spawned"

# ── Summary ──
echo ""
echo "═══════════════════════"
echo "Results: $PASS passed, $FAIL failed"
echo "═══════════════════════"
[ "$FAIL" -eq 0 ]
