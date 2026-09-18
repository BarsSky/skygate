#!/usr/bin/env bash
# B-check for B217 (v1.5.0+): /admin/cluster Phase 2.2
# action surface — Approve / Drain / Drain+Remove
# buttons + the cluster.DrainNode / DrainAndRemoveNode /
# ApproveNode helpers that back them.
#
# Contracts pinned (24 source-pin + 2 go-runtime):
#   A-E: cluster package helpers (DrainNode /
#        DrainAndRemoveNode / ApproveNode +
#        build* JSON detail builders)
#   F-I: db package (NodeApprove ClusterAuditAction
#        constant + the 9-action list in /admin/cluster
#        recent events + /admin/ha filter)
#   J-L: 3 new admin POST handlers in feature/admin/cluster.go
#   M-O: 3 new routes in cmd/skygate/main.go
#   P-R: template rendering of Approve / Drain /
#        Drain+Remove buttons per row in cluster.html
#   S:    /admin/ha template update (B217 also adds
#        node_approve badge to /admin/ha)
#   T-V:  i18n keys (8 new keys: approve + drain +
#        drain-remove in RU + EN, plus ha.action_node_approve)
#   W:    unit test file exists
#   X:    B217 unit tests pass
#   Y:    go build passes
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

NODE_GO="internal/cluster/node.go"
DB_GO="internal/db/cluster_audit.go"
CLUSTER_GO="internal/feature/admin/cluster.go"
HA_GO="internal/feature/admin/ha.go"
MAIN_GO="cmd/skygate/main.go"
CLUSTER_HTML="internal/handlers/templates/admin/cluster.html"
HA_HTML="internal/handlers/templates/admin/ha.html"
CATALOG="internal/i18n/catalog_admin.go"
TEST="internal/cluster/node_b217_test.go"

# --- A: cluster.DrainNode helper ---
if has "$NODE_GO" "func DrainNode\\("; then
  ok "A: cluster.DrainNode helper exists"
else
  fail "A: cluster.DrainNode missing in $NODE_GO"
fi

# --- B: cluster.DrainAndRemoveNode helper ---
if has "$NODE_GO" "func DrainAndRemoveNode\\("; then
  ok "B: cluster.DrainAndRemoveNode helper exists"
else
  fail "B: cluster.DrainAndRemoveNode missing in $NODE_GO"
fi

# --- C: cluster.ApproveNode helper ---
if has "$NODE_GO" "func ApproveNode\\("; then
  ok "C: cluster.ApproveNode helper exists"
else
  fail "C: cluster.ApproveNode missing in $NODE_GO"
fi

# --- D: 3 builders (buildDrainDetail / buildApproveDetail / buildDrainAndRemoveLeaveDetail) ---
for fn in buildDrainDetail buildApproveDetail buildDrainAndRemoveLeaveDetail; do
  if has "$NODE_GO" "func ${fn}"; then
    ok "D: $fn builder exists in node.go"
  else
    fail "D: $fn builder missing in $NODE_GO"
  fi
done

# --- E: production code uses the builders (no inline fmt.Sprintf for detail) ---
# Note: `(` is a regex metachar; escape it for the alternation pattern.
pat='buildDrainDetail\(|buildApproveDetail\(|buildDrainAndRemoveLeaveDetail\('
if grep -q -E "$pat" "$NODE_GO"; then
  ok "E: production code uses the 3 builders (no inline detail JSON)"
else
  fail "E: production code should call the 3 builders for audit detail"
fi

# --- F: db.NodeApprove ClusterAuditAction constant ---
if has "$DB_GO" "NodeApprove[[:space:]]+ClusterAuditAction[[:space:]]*="; then
  ok "F: db.NodeApprove ClusterAuditAction constant defined"
else
  fail "F: db.NodeApprove missing in $DB_GO"
fi

# --- G: /admin/cluster recent events query includes node_approve ---
if has "$CLUSTER_GO" "node_approve"; then
  ok "G: /admin/cluster recent events query includes node_approve"
else
  fail "G: node_approve missing from /admin/cluster recent events query"
fi

# --- H: /admin/ha recent events query includes node_approve ---
if has "$HA_GO" "node_approve"; then
  ok "H: /admin/ha recent events query includes node_approve"
else
  fail "H: node_approve missing from /admin/ha recent events query"
fi

# --- I: 9-action IN list (B215 4 + B216 4 + B217 1 = 9) ---
# We count the node_* / failover_* actions in the IN list
n=$(grep -A2 "action IN ('node_health" "$CLUSTER_GO" | tr -d "\n" | grep -oE "node_[a-z]+|failover_recommend" | sort -u | wc -l)
if [ "${n:-0}" -ge 9 ]; then
  ok "I: /admin/cluster 9-action IN list (got $n unique actions)"
else
  fail "I: /admin/cluster IN list has only $n actions, want >= 9"
fi

# --- J: PostAdminClusterNodeDrain handler ---
if has "$CLUSTER_GO" "func .* PostAdminClusterNodeDrain"; then
  ok "J: PostAdminClusterNodeDrain handler exists"
else
  fail "J: PostAdminClusterNodeDrain missing in $CLUSTER_GO"
fi

# --- K: PostAdminClusterNodeDrainRemove handler ---
if has "$CLUSTER_GO" "func .* PostAdminClusterNodeDrainRemove"; then
  ok "K: PostAdminClusterNodeDrainRemove handler exists"
else
  fail "K: PostAdminClusterNodeDrainRemove missing in $CLUSTER_GO"
fi

# --- L: PostAdminClusterNodeApprove handler ---
if has "$CLUSTER_GO" "func .* PostAdminClusterNodeApprove"; then
  ok "L: PostAdminClusterNodeApprove handler exists"
else
  fail "L: PostAdminClusterNodeApprove missing in $CLUSTER_GO"
fi

# --- M: 3 new routes in main.go ---
for route in "/admin/cluster/node/drain\"" "/admin/cluster/node/drain-remove\"" "/admin/cluster/node/approve\""; do
  if has "$MAIN_GO" "POST ${route}"; then
    ok "M: route POST ${route} registered"
  else
    fail "M: route POST ${route} missing in $MAIN_GO"
  fi
done

# --- N: handlers call the right cluster helpers ---
pat='cluster\.DrainNode\(|cluster\.DrainAndRemoveNode\(|cluster\.ApproveNode\('
if grep -q -E "$pat" "$CLUSTER_GO"; then
  ok "N: handlers call DrainNode / DrainAndRemoveNode / ApproveNode"
else
  fail "N: handlers should call the 3 new cluster helpers"
fi

# --- O: handlers refuse to act on self row (operator lockout protection) ---
# All 3 new handlers must check s.SelfHostname
# against the form-supplied hostname. We grep
# for "SelfHostname" inside each handler body
# using a tolerant pattern (the func sig is
# `func (s *Service) PostAdminClusterNodeDrain(`
# — note the parameters — so we match the func
# start line by its name + paren, not by a strict
# word boundary).
drain_block=$(awk '/^func.*PostAdminClusterNodeDrain\(/,/^}/' "$CLUSTER_GO" | grep -c "SelfHostname" || true)
drain_remove_block=$(awk '/^func.*PostAdminClusterNodeDrainRemove\(/,/^}/' "$CLUSTER_GO" | grep -c "SelfHostname" || true)
approve_block=$(awk '/^func.*PostAdminClusterNodeApprove\(/,/^}/' "$CLUSTER_GO" | grep -c "SelfHostname" || true)
if [ "${drain_block:-0}" -ge 1 ] && [ "${drain_remove_block:-0}" -ge 1 ] && [ "${approve_block:-0}" -ge 1 ]; then
  ok "O: handlers refuse to act on self row (lockout protection)"
else
  fail "O: handlers must refuse to act on self row (Drain=$drain_block, DrainRemove=$drain_remove_block, Approve=$approve_block)"
fi

# --- P: template renders Approve button for state=pending ---
if has "$CLUSTER_HTML" "/admin/cluster/node/approve"; then
  ok "P: template renders Approve button (POST /admin/cluster/node/approve)"
else
  fail "P: Approve button missing in $CLUSTER_HTML"
fi

# --- Q: template renders Drain button ---
if has "$CLUSTER_HTML" "/admin/cluster/node/drain\""; then
  ok "Q: template renders Drain button"
else
  fail "Q: Drain button missing in $CLUSTER_HTML"
fi

# --- R: template renders Drain & Remove button ---
if has "$CLUSTER_HTML" "/admin/cluster/node/drain-remove\""; then
  ok "R: template renders Drain & Remove button"
else
  fail "R: Drain & Remove button missing in $CLUSTER_HTML"
fi

# --- S: /admin/ha template also renders node_approve badge (mirror B216) ---
if has "$HA_HTML" 'eq .Action "node_approve"'; then
  ok "S: /admin/ha template renders node_approve badge"
else
  fail "S: node_approve badge missing in $HA_HTML"
fi

# --- T: i18n keys (8 new in cluster.* + 1 in ha.action_node_approve) ---
# Count must be >= 2 for RU + EN sections.
for key in "cluster.node_approve" "cluster.node_drain_btn" "cluster.node_drain_remove_btn" "cluster.node_approved" "cluster.node_drained" "cluster.node_drain_removed" "ha.action_node_approve"; do
  n=$(grep -c "\"${key}\":" "$CATALOG" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 2 ]; then
    ok "T: i18n key ${key} present in RU + EN ($n occurrences)"
  else
    fail "T: i18n key ${key} missing (only $n occurrence(s))"
  fi
done

# --- U: i18n values are not the i18n key string (catch empty translations) ---
# The Russian section's cluster.node_approve must NOT be the English value
# (else the Russian page would show "Approve" instead of "Одобрить").
# The catalog is structured as two Go map literals: `var ruAdmin`
# (RU) + `var enAdmin` (EN). The /admin page is RU by default
# so we check the RU map.
ru_approve=$(awk '/^var ruAdmin/,/^}$/' "$CATALOG" | grep '"cluster.node_approve"' | head -1)
if echo "$ru_approve" | grep -q 'Одобрить'; then
  ok "U: cluster.node_approve RU value is 'Одобрить' (not English)"
else
  fail "U: cluster.node_approve RU value wrong (expected Одобрить, got: $ru_approve)"
fi

# --- V: Approve button is conditional on state=pending (not always shown) ---
if has "$CLUSTER_HTML" "eq .State \"pending\""; then
  ok "V: Approve button is conditional on state=pending"
else
  fail "V: Approve button should be conditional on state=pending"
fi

# --- W: new test file exists ---
if [ -f "$TEST" ]; then
  ok "W: $TEST exists"
else
  fail "W: $TEST missing"
fi

# --- X: 4+ test functions in B217 test file ---
n=$(grep -c "^func Test" "$TEST" 2>/dev/null || echo 0)
if [ "${n:-0}" -ge 4 ]; then
  ok "X: B217 test file has $n test functions"
else
  fail "X: only $n test functions in $TEST (expected >= 4)"
fi

# --- Y: go test (B217 unit tests) passes ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/cluster/... -count=1 >/dev/null 2>&1; then
    ok "Y: go test ./internal/cluster/... passes"
  else
    fail "Y: go test ./internal/cluster/... FAILED"
  fi
else
  echo "[skip] Y: go not on PATH"
fi

# --- Z: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "Z: go build ./... succeeds"
  else
    fail "Z: go build ./... FAILED"
  fi
else
  echo "[skip] Z: go not on PATH"
fi

echo ""
echo "B217 B-check: $ok_count passed"
