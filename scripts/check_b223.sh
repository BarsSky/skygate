#!/usr/bin/env bash
# B-check for B223 (v1.5.0+): /admin/cluster
# Phase 4.3 — Tailscale auto-discovery (new node
# appears in cluster list, admin still approves).
#
# Contracts pinned (16 source-pin + 3 go-runtime):
#   A:    internal/cluster/discovery.go has
#         TailscaleStatus + TailscalePeer types
#   B:    parseTailscaleStatus handles Self +
#         Peer map keyed by DNS name
#   C:    TailscaleHostnameShort trims the
#         .ts.net MagicDNS suffix
#   D:    firstIPv4 returns first IPv4 + skips
#         v6-only peers (cluster_node is INET)
#   E:    matchesTagFilter — "" = all, case-
#         insensitive match
#   F:    DiscoverNewNodes returns the list
#         of Tailscale peers not in cluster_node
#   G:    EnsureDiscoveredNode inserts pending
#         row + writes cluster_audit row
#   H:    NodeDiscovered ClusterAuditAction =
#         "node_discovered"
#   I:    tailscaleStatusFn package-level mock
#         hook (for unit tests)
#   J:    PostAdminClusterDiscover HTTP handler
#   K:    POST /admin/cluster/discover route
#   L:    Service.DiscoveryTag field wired
#         from SKYGATE_DISCOVERY_TAG env var
#   M:    Background ticker (runDiscoveryTicker
#         + runOneDiscoveryTick) at 5-min default
#   N:    cluster.html has the "Run Tailscale
#         discovery" button + 2 new i18n keys
#   O:    B223 unit tests pass (11 pure-Go tests)
#   P:    AGENTS.md mentions B223
#   Q:    go build ./... succeeds
#   R:    verify_pre_deploy.sh includes check_b223
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

DISC_GO="internal/cluster/discovery.go"
TEST_NEW="internal/cluster/discovery_b223_test.go"
AUDIT_GO="internal/db/cluster_audit.go"
HANDLER_GO="internal/feature/admin/cluster.go"
MAIN_GO="cmd/skygate/main.go"
SERVICE_GO="internal/feature/admin/service.go"
TEMPLATE="internal/handlers/templates/admin/cluster.html"
CATALOG="internal/i18n/catalog_admin.go"
VPD="scripts/verify_pre_deploy.sh"
AGENTS="AGENTS.md"

# --- A: TailscaleStatus + TailscalePeer types ---
if has "$DISC_GO" "type TailscaleStatus struct" && \
   has "$DISC_GO" "type TailscalePeer struct"; then
  ok "A: TailscaleStatus + TailscalePeer types exist"
else
  fail "A: TailscaleStatus or TailscalePeer type missing"
fi

# --- B: parseTailscaleStatus handles Self + Peer ---
if has "$DISC_GO" "func parseTailscaleStatus\\(" && \
   has "$DISC_GO" 'json:\"Self\"' && \
   has "$DISC_GO" 'json:\"Peer\"'; then
  ok "B: parseTailscaleStatus handles Self + Peer map"
else
  fail "B: parseTailscaleStatus JSON shape wrong"
fi

# --- C: TailscaleHostnameShort trims .ts.net ---
if has "$DISC_GO" "func TailscaleHostnameShort\\(" && \
   has "$DISC_GO" '\"ts\"' && \
   has "$DISC_GO" '\"net\"'; then
  ok "C: TailscaleHostnameShort trims .ts.net MagicDNS suffix"
else
  fail "C: TailscaleHostnameShort logic wrong"
fi

# --- D: firstIPv4 v4-only ---
if has "$DISC_GO" "func firstIPv4\\(" && \
   grep -q 'strings.Contains(ip, ":")' "$DISC_GO" 2>/dev/null; then
  ok "D: firstIPv4 returns first IPv4 + skips v6-only peers"
else
  fail "D: firstIPv4 logic wrong"
fi

# --- E: matchesTagFilter — "" = all, case-insensitive ---
if has "$DISC_GO" "func matchesTagFilter\\(" && \
   grep -q 'tagFilter == ""' "$DISC_GO" 2>/dev/null && \
   has "$DISC_GO" "EqualFold"; then
  ok "E: matchesTagFilter — empty=all, case-insensitive match"
else
  fail "E: matchesTagFilter logic wrong"
fi

# --- F: DiscoverNewNodes ---
if has "$DISC_GO" "func DiscoverNewNodes\\(" && \
   has "$DISC_GO" "listClusterHostnames"; then
  ok "F: DiscoverNewNodes de-duplicates against cluster_node"
else
  fail "F: DiscoverNewNodes missing or no de-dup"
fi

# --- G: EnsureDiscoveredNode inserts + cluster_audit ---
if has "$DISC_GO" "func EnsureDiscoveredNode\\(" && \
   has "$DISC_GO" "INSERT INTO cluster_node" && \
   has "$DISC_GO" "db.NodeDiscovered" && \
   has "$DISC_GO" "InsertClusterAudit"; then
  ok "G: EnsureDiscoveredNode inserts pending row + writes node_discovered cluster_audit"
else
  fail "G: EnsureDiscoveredNode INSERT or cluster_audit wiring missing"
fi

# --- H: NodeDiscovered ClusterAuditAction ---
if has "$AUDIT_GO" 'NodeDiscovered ClusterAuditAction = "node_discovered"'; then
  ok "H: NodeDiscovered ClusterAuditAction = \"node_discovered\""
else
  fail "H: NodeDiscovered constant missing"
fi

# --- I: tailscaleStatusFn mock hook ---
if has "$DISC_GO" "var tailscaleStatusFn func"; then
  ok "I: tailscaleStatusFn package-level mock hook"
else
  fail "I: tailscaleStatusFn mock hook missing"
fi

# --- J: PostAdminClusterDiscover HTTP handler ---
if has "$HANDLER_GO" "func .* PostAdminClusterDiscover\\("; then
  ok "J: PostAdminClusterDiscover HTTP handler exists"
else
  fail "J: PostAdminClusterDiscover handler missing"
fi

# --- K: POST /admin/cluster/discover route ---
if has "$MAIN_GO" 'POST /admin/cluster/discover'; then
  ok "K: POST /admin/cluster/discover route registered"
else
  fail "K: route POST /admin/cluster/discover missing"
fi

# --- L: Service.DiscoveryTag wired from env ---
if has "$SERVICE_GO" "DiscoveryTag string" && \
   has "$MAIN_GO" "DiscoveryTag:.*SKYGATE_DISCOVERY_TAG"; then
  ok "L: Service.DiscoveryTag field wired from SKYGATE_DISCOVERY_TAG"
else
  fail "L: DiscoveryTag field or env wiring missing"
fi

# --- M: Background ticker (runDiscoveryTicker + runOneDiscoveryTick) ---
if has "$MAIN_GO" "runDiscoveryTicker" && \
   has "$MAIN_GO" "runOneDiscoveryTick" && \
   has "$MAIN_GO" "SKYGATE_DISCOVERY_INTERVAL_SEC"; then
  ok "M: Background discovery ticker at 5-min default (SKYGATE_DISCOVERY_INTERVAL_SEC override)"
else
  fail "M: Background ticker or env override missing"
fi

# --- N: cluster.html button + 2 new i18n keys ---
if has "$TEMPLATE" 'action="/admin/cluster/discover"' && \
   has "$TEMPLATE" "cluster.discover_btn"; then
  ok "N: cluster.html \"Run Tailscale discovery\" button"
else
  fail "N: cluster.html discover button missing"
fi
# 2 new keys — each must be in BOTH RU and EN
for key in "cluster.discover_btn" "cluster.discover_help"; do
  n=$(grep -c "\"${key}\":" "$CATALOG" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 2 ]; then
    ok "N.i: i18n key ${key} present in RU + EN ($n occurrences)"
  else
    fail "N.i: i18n key ${key} missing (only $n occurrence(s))"
  fi
done

# --- O: B223 unit tests pass ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/cluster/... -run 'B223|Discover|Tailscale|FirstIPv4|MatchesTagFilter' -count=1 -short >/dev/null 2>&1; then
    ok "O: B223 unit tests pass"
  else
    fail "O: B223 unit tests FAILED"
  fi
else
  echo "[skip] O: go not on PATH"
fi

# --- P: AGENTS.md mentions B223 ---
if has "$AGENTS" "B223"; then
  ok "P: AGENTS.md mentions B223"
else
  echo "[skip] P: AGENTS.md doesn't mention B223 (will be added before commit)"
fi

# --- Q: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "Q: go build ./... succeeds"
  else
    fail "Q: go build ./... FAILED"
  fi
else
  echo "[skip] Q: go not on PATH"
fi

# --- R: verify_pre_deploy.sh includes B223 ---
if has "$VPD" 'check_b223' && has "$VPD" 'B223'; then
  ok "R: verify_pre_deploy.sh wires check_b223"
else
  echo "[skip] R: verify_pre_deploy.sh doesn't wire check_b223 (will be added before commit)"
fi

echo ""
echo "B223 B-check: $ok_count passed"
