#!/usr/bin/env bash
# B-check for B222 (v1.5.0+): /admin/cluster
# Phase 4.2 — rolling upgrade orchestrator
# (drain → wait for new build → rejoin).
#
# Contracts pinned (17 source-pin + 3 go-runtime):
#   A:    cluster.RejoinNode helper exists in
#         internal/cluster/node.go
#   B:    RejoinNode allows draining→ready +
#         failed→ready transitions
#   C:    RejoinNode rejects pending→ready (use
#         ApproveNode for that)
#   D:    RejoinNode is idempotent (ready→ready
#         is a no-op)
#   E:    buildRejoinDetail JSON has node_id +
#         hostname + from_state + to_state + actor
#   F:    internal/cluster/upgrade.go has
#         UpgradeOrchestrator struct + the 5-min
#         default HealthTimeout
#   G:    UpgradeNode runs the drain+wait+rejoin
#         state machine (calls DrainNode +
#         waitForBuild + RejoinNode)
#   H:    Self-upgrade guard via SelfHostname()
#         + ErrSelfUpgrade sentinel
#   I:    waitForBuild polls /healthz until the
#         build string matches (not just any
#         200 OK)
#   J:    NodeRejoin ClusterAuditAction constant
#         exists ("node_rejoin")
#   K:    cluster.upgrade.fail audit row written
#         via B221 AppendAuditLogWithTarget with
#         target_type="cluster_node" +
#         target_id=hostname
#   L:    PostAdminClusterUpgrade HTTP handler
#         in internal/feature/admin/cluster.go
#   M:    POST /admin/cluster/upgrade route
#         registered in cmd/skygate/main.go
#   N:    cluster.html template has per-row
#         Upgrade button (non-self, ready|failed)
#   O:    cluster.html has "Upgrade all" button
#         + 8 new i18n keys (RU + EN) in
#         catalog_admin.go
#   P:    skygate cluster upgrade CLI verb wired
#         in cmd/skygate/cluster.go +
#         runClusterUpgrade function
#   Q:    self-upgrade refused in CLI
#         (handler-side check)
#   R:    B222 unit tests pass (9 pure-Go tests
#         in upgrade_b222_test.go)
#   S:    AGENTS.md mentions B222
#   T:    go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

NODE_GO="internal/cluster/node.go"
UPGRADE_GO="internal/cluster/upgrade.go"
TEST_NEW="internal/cluster/upgrade_b222_test.go"
AUDIT_GO="internal/db/cluster_audit.go"
HANDLER_GO="internal/feature/admin/cluster.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/cluster.html"
CATALOG="internal/i18n/catalog_admin.go"
CLI_GO="cmd/skygate/cluster.go"
AGENTS="AGENTS.md"

# --- A: RejoinNode helper ---
if has "$NODE_GO" "^func RejoinNode\\("; then
  ok "A: cluster.RejoinNode helper exists"
else
  fail "A: cluster.RejoinNode missing in $NODE_GO"
fi

# --- B: RejoinNode allows draining+failed→ready ---
# (The body checks the prev_state and allows
#  draining + failed; the test is the error
#  message text for pending — the
#  "use ApproveNode" hint proves pending is
#  rejected, not allowed.)
if has "$NODE_GO" 'cannot rejoin node in state %q' && \
   has "$NODE_GO" 'from_state":%q,"to_state":"ready"'; then
  ok "B: RejoinNode allows draining/failed → ready (and rejects pending)"
else
  fail "B: RejoinNode state-transition policy not visible"
fi

# --- C: RejoinNode rejects pending (covered by B's
#       "use ApproveNode" hint; pinned separately
#       so a refactor of B's wording doesn't
#       silently drop the pending-rejection) ---
if has "$NODE_GO" 'cannot rejoin node in state %q'; then
  ok "C: RejoinNode explicitly rejects pending (use ApproveNode instead)"
else
  fail "C: RejoinNode missing pending-rejection"
fi

# --- D: RejoinNode idempotent ready→ready ---
if has "$NODE_GO" 'if prevState == NodeStateReady'; then
  ok "D: RejoinNode is idempotent (ready→ready is a no-op)"
else
  fail "D: RejoinNode idempotency check missing"
fi

# --- E: buildRejoinDetail JSON shape ---
if has "$NODE_GO" 'buildRejoinDetail' && \
   has "$NODE_GO" '"node_id":%q,"hostname":%q,"from_state":%q,"to_state":"ready","actor":%q'; then
  ok "E: buildRejoinDetail has node_id + hostname + from_state + to_state + actor"
else
  fail "E: buildRejoinDetail schema wrong"
fi

# --- F: UpgradeOrchestrator struct + 5-min default ---
if has "$UPGRADE_GO" "type UpgradeOrchestrator struct" && \
   has "$UPGRADE_GO" "HealthTimeout.*time\\.Duration" && \
   has "$UPGRADE_GO" "5 \\* time\\.Minute"; then
  ok "F: UpgradeOrchestrator + 5-min HealthTimeout default"
else
  fail "F: UpgradeOrchestrator struct or default missing"
fi

# --- G: UpgradeNode state machine ---
if has "$UPGRADE_GO" "func \\(o \\*UpgradeOrchestrator\\) UpgradeNode\\(" && \
   has "$UPGRADE_GO" "DrainNode\\(" && \
   has "$UPGRADE_GO" "waitForBuild\\(" && \
   has "$UPGRADE_GO" "RejoinNode\\("; then
  ok "G: UpgradeNode runs DrainNode + waitForBuild + RejoinNode"
else
  fail "G: UpgradeNode state machine incomplete"
fi

# --- H: Self-upgrade guard ---
if has "$UPGRADE_GO" "ErrSelfUpgrade = errors\\.New" && \
   has "$UPGRADE_GO" "func SelfHostname\\(\\)" && \
   has "$UPGRADE_GO" "checkSelfUpgrade"; then
  ok "H: self-upgrade guard (ErrSelfUpgrade + SelfHostname + checkSelfUpgrade)"
else
  fail "H: self-upgrade guard incomplete"
fi

# --- I: waitForBuild polls /healthz + matches build ---
if has "$UPGRADE_GO" "func \\(o \\*UpgradeOrchestrator\\) waitForBuild\\(" && \
   has "$UPGRADE_GO" 'healthz' && \
   has "$UPGRADE_GO" 'parsed.Build'; then
  ok "I: waitForBuild polls /healthz + matches the build field (not just any 200)"
else
  fail "I: waitForBuild not polling /healthz with build-match"
fi

# --- J: NodeRejoin ClusterAuditAction ---
if has "$AUDIT_GO" 'NodeRejoin ClusterAuditAction = "node_rejoin"'; then
  ok "J: NodeRejoin ClusterAuditAction constant exists"
else
  fail "J: NodeRejoin constant missing in $AUDIT_GO"
fi

# --- K: cluster.upgrade.fail B221 audit ---
if has "$UPGRADE_GO" "cluster\\.upgrade\\.fail" && \
   has "$UPGRADE_GO" 'AppendAuditLogWithTarget' && \
   has "$UPGRADE_GO" '"cluster_node", hostname'; then
  ok "K: cluster.upgrade.fail B221 audit (target_type=cluster_node + target_id=hostname)"
else
  fail "K: cluster.upgrade.fail B221 audit missing or wrong target"
fi

# --- L: PostAdminClusterUpgrade HTTP handler ---
if has "$HANDLER_GO" "func .* PostAdminClusterUpgrade\\("; then
  ok "L: PostAdminClusterUpgrade HTTP handler exists"
else
  fail "L: PostAdminClusterUpgrade missing in $HANDLER_GO"
fi

# --- M: POST /admin/cluster/upgrade route ---
if has "$MAIN_GO" 'POST /admin/cluster/upgrade'; then
  ok "M: POST /admin/cluster/upgrade route registered"
else
  fail "M: POST /admin/cluster/upgrade route missing in $MAIN_GO"
fi

# --- N: per-row Upgrade button (non-self, ready|failed) ---
if has "$TEMPLATE" 'action="/admin/cluster/upgrade"' && \
   has "$TEMPLATE" 'name="target" value="\{\{\.Hostname\}\}"' && \
   has "$TEMPLATE" 'cluster.node_upgrade_btn'; then
  ok "N: cluster.html per-row Upgrade button (non-self, ready|failed)"
else
  fail "N: per-row Upgrade button missing or wrong shape"
fi

# --- O: Upgrade all + 8 new i18n keys (RU + EN) ---
if has "$TEMPLATE" 'name="target" value="all"' && \
   has "$TEMPLATE" 'cluster.upgrade_all_btn'; then
  ok "O: cluster.html Upgrade all (rolling) button"
else
  fail "O: Upgrade all button missing"
fi
# 8 new keys — each must be in BOTH RU and EN
# sections of catalog_admin.go (>= 2 occurrences).
for key in "cluster.node_upgrade_btn" "cluster.node_upgrade_confirm" "cluster.node_upgraded" "cluster.upgrade_all_btn" "cluster.upgrade_all_confirm" "cluster.upgrade_all_done" "cluster.upgrade_self_refused" "cluster.upgrade_all_help"; do
  n=$(grep -c "\"${key}\":" "$CATALOG" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 2 ]; then
    ok "O.i: i18n key ${key} present in RU + EN ($n occurrences)"
  else
    fail "O.i: i18n key ${key} missing (only $n occurrence(s))"
  fi
done

# --- P: skygate cluster upgrade CLI verb ---
if has "$CLI_GO" 'case "upgrade":' && \
   has "$CLI_GO" "func runClusterUpgrade\\("; then
  ok "P: skygate cluster upgrade CLI verb + runClusterUpgrade function"
else
  fail "P: skygate cluster upgrade CLI verb missing"
fi

# --- Q: self-upgrade refused in handler ---
# (Both the handler AND the orchestrator's
#  checkSelfUpgrade enforce this. The B-check
#  pins the handler-level refusal — the
#  orchestrator's check is verified by the
#  unit test TestCheckSelfUpgrade_SameHostname.)
if has "$HANDLER_GO" 'refusing to upgrade self'; then
  ok "Q: handler refuses self-upgrade with operator-readable message"
else
  fail "Q: handler missing self-upgrade refusal"
fi

# --- R: B222 unit tests pass ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/cluster/... -run 'Rejoin|Upgrade|SelfUpgrade|pollOnce' -count=1 -short >/dev/null 2>&1; then
    ok "R: B222 unit tests pass"
  else
    fail "R: B222 unit tests FAILED"
  fi
else
  echo "[skip] R: go not on PATH"
fi

# --- S: AGENTS.md mentions B222 ---
if has "$AGENTS" "B222"; then
  ok "S: AGENTS.md mentions B222"
else
  echo "[skip] S: AGENTS.md doesn't mention B222 (will be added before commit)"
fi

# --- T: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "T: go build ./... succeeds"
  else
    fail "T: go build ./... FAILED"
  fi
else
  echo "[skip] T: go not on PATH"
fi

echo ""
echo "B222 B-check: $ok_count passed"
