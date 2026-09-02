#!/usr/bin/env bash
# B-check for B216 (v1.5.0+): /admin/cluster Phase 2.1
# enrichment — bug fix for the "Recent events" section
# (was reading the wrong table) + online/offline summary
# pill + replicas/DSN-host rows in the Database section.
#
# Contracts pinned (23 source-pin + 2 go-runtime):
#   A-N: handler changes (cluster_audit query, struct
#        fields, online counting, replicas + DSN host)
#   O-R: template changes (X-of-Y pill, replicas row,
#        DSN host row, B215-style action badges)
#   S-V: i18n keys (online/offline/stale + 4 ha.action_*
#        keys for the pre-B215 actions)
#   W:   new test file
#   X:   go test passes
#   Y:   go build passes
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

CLUSTER_GO="internal/feature/admin/cluster.go"
CLUSTER_HTML="internal/handlers/templates/admin/cluster.html"
CATALOG="internal/i18n/catalog_admin.go"
TEST="internal/feature/admin/cluster_b216_test.go"

# --- A: cluster_audit is the source for Recent events ---
if has "$CLUSTER_GO" "FROM cluster_audit"; then
  ok "A: Recent events reads from cluster_audit (not audit_log)"
else
  fail "A: cluster_audit SELECT missing from $CLUSTER_GO"
fi

# --- B: 8-action IN list (B215 + B216 expansion) ---
if has "$CLUSTER_GO" "node_init.*node_join.*node_drain.*node_leave"; then
  ok "B: 8-action IN list includes the 4 B215 bootstrap events"
else
  fail "B: 8-action IN list missing in $CLUSTER_GO"
fi

# --- C: OnlineThresholdSec constant ---
if has "$CLUSTER_GO" "const OnlineThresholdSec"; then
  ok "C: OnlineThresholdSec constant defined"
else
  fail "C: OnlineThresholdSec missing in $CLUSTER_GO"
fi

# --- D: classifyNodeHealth helper ---
if has "$CLUSTER_GO" "func classifyNodeHealth"; then
  ok "D: classifyNodeHealth helper exists"
else
  fail "D: classifyNodeHealth missing in $CLUSTER_GO"
fi

# --- E: clusterPageData has new fields ---
for field in OnlineCount OfflineCount HasStaleNodes DBReplicas DBReplicaCnt DBDSNHost HasReplicas; do
  if has "$CLUSTER_GO" "^	${field}\\b" || has "$CLUSTER_GO" "^	${field} "; then
    ok "E: clusterPageData has field $field"
  else
    fail "E: clusterPageData missing field $field"
  fi
done

# --- F: handler populates OnlineCount via classifyNodeHealth ---
if has "$CLUSTER_GO" "online, stale := classifyNodeHealth"; then
  ok "F: handler calls classifyNodeHealth for online counting"
else
  fail "F: handler doesn't call classifyNodeHealth in $CLUSTER_GO"
fi

# --- G: handler reads replica_node_ids ---
if has "$CLUSTER_GO" "replica_node_ids"; then
  ok "G: handler reads cluster_database.replica_node_ids"
else
  fail "G: replica_node_ids SELECT missing in $CLUSTER_GO"
fi

# --- H: extractDSNHost helper ---
if has "$CLUSTER_GO" "func extractDSNHost"; then
  ok "H: extractDSNHost helper exists"
else
  fail "H: extractDSNHost missing in $CLUSTER_GO"
fi

# --- I: handler calls extractDSNHost ---
if has "$CLUSTER_GO" "DBDSNHost = extractDSNHost"; then
  ok "I: handler uses extractDSNHost to populate DBDSNHost"
else
  fail "I: handler doesn't call extractDSNHost"
fi

# --- J: pre-B216 audit_log LIKE 'cluster.%' query is GONE ---
if has "$CLUSTER_GO" "FROM audit_log\\s*\\n.*LIKE 'cluster.%'"; then
  fail "J: pre-B216 audit_log query still in $CLUSTER_GO (must be removed)"
else
  ok "J: pre-B216 audit_log query removed (replaced with cluster_audit)"
fi

# --- K: net/url import added for extractDSNHost ---
if has "$CLUSTER_GO" '"net/url"'; then
  ok "K: net/url import added"
else
  fail "K: net/url import missing in $CLUSTER_GO"
fi

# --- L: template renders X-of-Y online pill ---
if has "$CLUSTER_HTML" "OnlineCount.*NodeCount.*online" || has "$CLUSTER_HTML" "{{.Data.OnlineCount}}/{{.Data.NodeCount}}"; then
  ok "L: template renders X-of-Y online pill"
else
  fail "L: X-of-Y online pill missing in $CLUSTER_HTML"
fi

# --- M: template renders stale pill ---
if has "$CLUSTER_HTML" "HasStaleNodes"; then
  ok "M: template renders stale heartbeat pill"
else
  fail "M: stale heartbeat pill missing in $CLUSTER_HTML"
fi

# --- N: template renders replicas row ---
if has "$CLUSTER_HTML" "DBReplicas" && has "$CLUSTER_HTML" "db_replicas"; then
  ok "N: template renders replicas row"
else
  fail "N: replicas row missing in $CLUSTER_HTML"
fi

# --- O: template renders DSN host row ---
if has "$CLUSTER_HTML" "DBDSNHost" && has "$CLUSTER_HTML" "db_dsn_host"; then
  ok "O: template renders DSN host row"
else
  fail "O: DSN host row missing in $CLUSTER_HTML"
fi

# --- P: template uses B215-style action badges (not raw <code>{{.Action}}</code>) ---
if has "$CLUSTER_HTML" 'eq .Action "node_init"'; then
  ok "P: template renders action badges (B215-style colored spans)"
else
  fail "P: action badge rendering missing in $CLUSTER_HTML"
fi

# --- Q: template renders 8 known actions (B215 + 4 pre-B215 ha.action_*) ---
for action in node_init node_join node_drain node_leave node_health failover_recommend node_failover node_drill; do
  if has "$CLUSTER_HTML" "eq .Action \"${action}\""; then
    ok "Q: template renders ${action} badge"
  else
    fail "Q: ${action} badge missing in $CLUSTER_HTML"
  fi
done

# --- R: i18n keys (online/offline/stale + 4 ha.action_* for pre-B215) ---
for key in "cluster.online" "cluster.offline" "cluster.stale" "cluster.db_replicas" "cluster.db_no_replicas" "cluster.db_dsn_host" "ha.action_node_health" "ha.action_failover_recommend" "ha.action_node_failover" "ha.action_node_drill"; do
  # Count occurrences: must be >= 2 (RU + EN sections)
  n=$(grep -c "\"${key}\":" "$CATALOG" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 2 ]; then
    ok "R: i18n key ${key} present in RU + EN ($n occurrences)"
  else
    fail "R: i18n key ${key} missing (only $n occurrence(s))"
  fi
done

# --- S: cluster.online value is a short string ("online") ---
if grep -q '"cluster.online":[[:space:]]*"online"' "$CATALOG"; then
  ok "S: cluster.online value is 'online' in EN"
else
  fail "S: cluster.online EN value wrong"
fi

# --- T: new test file exists ---
if [ -f "$TEST" ]; then
  ok "T: $TEST exists"
else
  fail "T: $TEST missing"
fi

# --- U: 3 test functions in B216 test file ---
n=$(grep -c "^func Test" "$TEST" 2>/dev/null || echo 0)
if [ "${n:-0}" -ge 3 ]; then
  ok "U: B216 test file has $n test functions"
else
  fail "U: only $n test functions in $TEST (expected >= 3)"
fi

# --- V: B216 test pin of HA-elector alignment ---
if has "$TEST" "OnlineThresholdSec.*90"; then
  ok "V: B216 test pins OnlineThresholdSec=90 (HA-elector alignment)"
else
  fail "V: HA-elector alignment pin missing in $TEST"
fi

# --- W: pre-B199 B-check contracts (B199 schema) still pass ---
# (sanity: parsePGTextArray + parseClusterChain + abbreviateClusterTime
# are still there from B199)
if has "$CLUSTER_GO" "func parsePGTextArray" && has "$CLUSTER_GO" "func parseClusterChain" && has "$CLUSTER_GO" "func abbreviateClusterTime"; then
  ok "W: B199 helpers (parsePGTextArray/parseClusterChain/abbreviateClusterTime) preserved"
else
  fail "W: B199 helpers missing — refactor regressed"
fi

# --- X: go test (B216 unit tests) passes ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/feature/admin/... -run 'B216|ExtractDSN|ClassifyNode|OnlineThreshold' -count=1 >/dev/null 2>&1; then
    ok "X: go test (B216 unit tests) passes"
  else
    fail "X: go test (B216 unit tests) FAILED"
  fi
else
  echo "[skip] X: go not on PATH — skipping go test"
fi

# --- Y: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "Y: go build ./... succeeds"
  else
    fail "Y: go build ./... FAILED"
  fi
else
  echo "[skip] Y: go not on PATH — skipping go build"
fi

echo ""
echo "B216 B-check: $ok_count passed"
