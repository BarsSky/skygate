#!/bin/bash
# scripts/check_b237_23.sh — B237.23 (v1.5.2+)
# Fix autoupdate ON CONFLICT code/index drift (B183 vs B232
# regression). Restores 6-col ON CONFLICT in sync.go to match
# the live 6-col UNIQUE INDEX on device_rules (V068 / B232)
# and the qInsertDeviceRule contract in queries.go:416.
#
# Background:
#   V056 (B125, 2026-08-17): 6-col intent + CREATE UNIQUE INDEX
#     IF NOT EXISTS — silent no-op on upgrade DBs that had
#     the pre-V056 5-col index (CREATE matches by NAME, not
#     by column list).
#   B188.2 (2026-08-17): 6-col ON CONFLICT in qInsertDeviceRule
#     (matched V056's 6-col intent on FRESH DBs only).
#   B183 (V060, 2026-08-25): 5-col index + 5-col ON CONFLICT
#     (reverted for "first parent_domain wins" dedup logic).
#   B232 (V068, 2026-09-04): 6-col index repair (closed
#     live "/my/exit-rules POST db error" symptom from
#     B188.2's 6-col ON CONFLICT against the 5-col index)
#     — BUT did NOT update sync.go. Result: code/index
#     drift. sync.go still had 5-col ON CONFLICT (from B183),
#     index was now 6-col (from V068), every autoupdate
#     INSERT failed silently.
#   B237.23 (2026-09-07): restore 6-col ON CONFLICT in sync.go
#     (both the CDN-range INSERT and the per-IP /32 INSERT)
#     + update b188_3 test helper + update check_b183 E-post
#     to assert "5-col is GONE, 6-col is PRESENT".
#
# Why 6-col is the right design for the current implementation
# (vs B183's 5-col "first parent_domain wins"):
#   1. B184 status check looks for parent_domain = <rule's
#      marker>; with 5-col "first wins" only the first
#      parent_domain owns the /32 rows → others render
#      ⏳ orange forever (the bug we just fixed).
#   2. B237.22 UI grouping renders each parent_domain as a
#      separate <details> group; 5-col "first wins" would
#      collapse 5 distinct cdn:cloudflare:* markers into
#      1 (the first one), breaking the UI design.
#   3. Tailscale ApprovedRoutes is a SET — duplicates in
#      device_rules are deduplicated by the Tailscale client.
#   4. qInsertDeviceRule (form path) is 6-col; the autoupdate
#      was the only path using 5-col, causing form-vs-autoupdate
#      asymmetry.
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
skip() { echo "  SKIP  $1"; }

# --- A. Source contract: sync.go has 6-col ON CONFLICT (both places) ---

# A.1 CDN-range INSERT (sync.go: ~492)
A1=$(grep -cE 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value, parent_domain\) DO NOTHING' "$REPO/internal/feature/exit_rules/sync.go")
if [ "$A1" = "2" ]; then
  ok "A.1 sync.go has 2x 6-col ON CONFLICT (CDN-range + per-IP /32)"
else
  bad "A.1 sync.go 6-col ON CONFLICT count=$A1 (want 2: CDN-range + per-IP)"
fi

# A.2 Per-IP /32 INSERT (sync.go: ~587) — same 6-col target
A2=$(grep -cE 'VALUES \(\$1, \$2, \$3, .subnet., \$4, \$5, \$6, \$7\)' "$REPO/internal/feature/exit_rules/sync.go")
if [ "$A2" -ge 2 ]; then
  ok "A.2 sync.go has >= 2 subnet INSERT statements (CDN-range + per-IP)"
else
  bad "A.2 sync.go subnet INSERTs count=$A2 (want >= 2)"
fi

# A.3 Pre-B237.23 5-col target should be GONE from sync.go
A3=$(grep -cE 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value\) DO NOTHING' "$REPO/internal/feature/exit_rules/sync.go")
if [ "$A3" = "0" ]; then
  ok "A.3 sync.go has NO 5-col ON CONFLICT (B183 design reverted)"
else
  bad "A.3 sync.go 5-col ON CONFLICT count=$A3 (want 0 — see B237.23 in AGENTS.md)"
fi

# --- B. qInsertDeviceRule in queries.go is 6-col (unchanged but pin) ---

B1=$(grep -cE 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value, parent_domain\) DO UPDATE SET id = device_rules.id' "$REPO/internal/db/queries.go")
if [ "$B1" -ge 1 ]; then
  ok "B.1 qInsertDeviceRule in queries.go still 6-col (B188.2 contract, unchanged)"
else
  bad "B.1 qInsertDeviceRule in queries.go should be 6-col (B188.2 contract)"
fi

# --- C. acl_b188_3 test helper is 6-col ---

C1=$(grep -cE 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value, parent_domain\) DO NOTHING' "$REPO/internal/acl/acl_b188_3_integration_test.go")
if [ "$C1" -ge 1 ]; then
  ok "C.1 b188_3 test helper uses 6-col ON CONFLICT (matches live index)"
else
  bad "C.1 b188_3 test helper should use 6-col ON CONFLICT"
fi

# C.2 Pre-B237.23 5-col should be GONE from test helper
C2=$(grep -cE 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value\) DO NOTHING' "$REPO/internal/acl/acl_b188_3_integration_test.go")
if [ "$C2" = "0" ]; then
  ok "C.2 b188_3 test helper has NO 5-col ON CONFLICT (B183 design reverted)"
else
  bad "C.2 b188_3 test helper 5-col ON CONFLICT count=$C2 (want 0)"
fi

# --- D. check_b183.sh has the B237.23 reverted assertion ---

D1=$(grep -cE 'E-b183-reverted' "$REPO/scripts/check_b183.sh")
if [ "$D1" -ge 1 ]; then
  ok "D.1 check_b183.sh has the B237.23 reverted-E assertion"
else
  bad "D.1 check_b183.sh should have the B237.23 reverted-E assertion"
fi

D2=$(grep -cE 'E-b183-reverted\+6col-restored|E-b23723-6col|6col-restored' "$REPO/scripts/check_b183.sh")
if [ "$D2" -ge 1 ]; then
  ok "D.2 check_b183.sh has the B237.23 6-col-restored assertion"
else
  bad "D.2 check_b183.sh should have the B237.23 6-col-restored assertion"
fi

# --- E. Live state checks (run only on the VM) ---

# E.1 Live index is 6-col (B232 / V068)
if [ -d /home/skyadmin/skygate ] && [ -f /home/skyadmin/skygate/scripts/verify_pre_deploy.sh ]; then
  E1=$(PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5433 -U admin -d skygate_staging -A -t -F'|' -c "SELECT indexdef FROM pg_indexes WHERE indexname='device_rules_natural_key_uniq'" 2>/dev/null)
  if echo "$E1" | grep -qE '\(user_id, device_id, exit_node_id, target_type, target_value, parent_domain\)'; then
    ok "E.1 live index is 6-col (matches sync.go's 6-col ON CONFLICT)"
  else
    bad "E.1 live index is NOT 6-col: $E1"
  fi

  # E.2 No pre-B237.23 5-col ON CONFLICT source in the live build
  E2=$(docker exec skygate-skygate-1 find / -name 'sync.go' 2>/dev/null | head -1)
  if [ -z "$E2" ]; then
    skip "E.2 live source path not findable (no /sync.go in container) — skip"
  else
    E2_5COL=$(docker exec skygate-skygate-1 grep -cE 'ON CONFLICT \(user_id, device_id, exit_node_id, target_type, target_value\) DO NOTHING' "$E2" 2>/dev/null)
    if [ "$E2_5COL" = "0" ]; then
      ok "E.2 live source has NO 5-col ON CONFLICT (B237.23 deployed)"
    else
      bad "E.2 live source has 5-col ON CONFLICT count=$E2_5COL (B237.23 not deployed?)"
    fi
  fi
else
  skip "E.1-E.2 not on VM (no /home/skyadmin/skygate) — skipped"
fi

# --- F. Build + vet + staticcheck + unit tests ---

if ! command -v go >/dev/null 2>&1; then
  skip "F.1 go not available in PATH"
  skip "F.2 go vet (requires go)"
  skip "F.3 staticcheck (requires go)"
  skip "F.4 cdn_group unit tests (requires go)"
  skip "F.5 acl unit tests (requires go)"
else
  F1=$(go build ./... 2>&1)
  if [ -z "$F1" ]; then
    ok "F.1 go build ./... clean"
  else
    bad "F.1 go build failed: $F1"
  fi

  F2=$(go vet ./internal/feature/exit_rules/... ./internal/acl/... 2>&1)
  if [ -z "$F2" ]; then
    ok "F.2 go vet clean (exit_rules + acl)"
  else
    bad "F.2 go vet failed: $F2"
  fi

  if ! command -v staticcheck >/dev/null 2>&1; then
    skip "F.3 staticcheck not available in PATH"
  else
    F3=$(staticcheck ./internal/feature/exit_rules/... ./internal/acl/... 2>&1)
    if [ -z "$F3" ]; then
      ok "F.3 staticcheck clean (exit_rules + acl)"
    else
      bad "F.3 staticcheck failed: $F3"
    fi
  fi

  # F.4 Unit tests for cdn_group + acl + b188.3 integration
  F4=$(go test -short -count=1 -run 'TestGroupRulesByCDN|TestGroupAdminRulesByCDN|TestIsCDNGroupMarker|TestParseCDNGroupMarker' ./internal/feature/exit_rules/... 2>&1)
  if echo "$F4" | grep -qE 'FAIL|--- FAIL'; then
    bad "F.4 cdn_group unit tests failed: $F4"
  else
    ok "F.4 cdn_group unit tests pass"
  fi

  F5=$(go test -short -count=1 ./internal/acl/... 2>&1)
  if echo "$F5" | grep -qE 'FAIL|--- FAIL'; then
    bad "F.5 acl unit tests failed"
  else
    ok "F.5 acl unit tests pass (including b188_3 test with 6-col ON CONFLICT)"
  fi
fi

# --- G. Registration in AGENTS.md + PLANS.md ---

G1=$(grep -cE 'B237\.23' "$REPO/AGENTS.md")
if [ "$G1" -ge 1 ]; then
  ok "G.1 AGENTS.md mentions B237.23 ($G1 hits)"
else
  bad "G.1 AGENTS.md must mention B237.23"
fi

G2=$(grep -cE 'B237\.23' "$REPO/docs/PLANS.md")
if [ "$G2" -ge 1 ]; then
  ok "G.2 docs/PLANS.md mentions B237.23 ($G2 hits)"
else
  bad "G.2 docs/PLANS.md must mention B237.23"
fi

G3=$(grep -cE 'check_b237_23' "$REPO/scripts/verify_pre_deploy.sh")
if [ "$G3" -ge 1 ]; then
  ok "G.3 verify_pre_deploy.sh includes check_b237_23"
else
  bad "G.3 verify_pre_deploy.sh must include check_b237_23"
fi

# --- H. check_b183.sh still passes with the B237.23 reverted assertion ---

H1=$(bash "$REPO/scripts/check_b183.sh" 2>&1)
H1_PASS=$(echo "$H1" | grep -cE '^\s*PASS')
H1_FAIL=$(echo "$H1" | grep -cE '^\s*FAIL')
if [ "$H1_FAIL" = "0" ] && [ "$H1_PASS" -ge 9 ]; then
  ok "H.1 check_b183.sh: $H1_PASS passed, $H1_FAIL failed (B237.23 contract in place)"
else
  bad "H.1 check_b183.sh: $H1_PASS passed, $H1_FAIL failed"
fi

echo ""
echo "  B237.23 (autoupdate ON CONFLICT code/index drift fix): $PASS PASS, $FAIL FAIL"
exit $FAIL
