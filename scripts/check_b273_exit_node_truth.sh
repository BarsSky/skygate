#!/usr/bin/env bash
# check_b273_exit_node_truth.sh
#
# 2026-09-19 (B273) — the exit-node health monitor and the admin page must agree
# with the rest of skygate about what an exit node is, and must name the reason
# when one is not usable.
#
# Live case (host `aro`, native install, one relay `exit-node-vps` @ 100.64.0.1):
#
#   /admin/exit-nodes : banner "Нет рабочих exit-узлов!" + "0/1 здоровых"
#                       state=offline, last_seen=47m ago, action="Tag as exit-node"
#   /my/exit-nodes    : the SAME node shown as "online"
#   headscale         : node online, advertises 2 routes (0.0.0.0/0 + ::/0)
#
# Both pages read headscale.NodeView, so the disagreement was inside skygate:
#
#   * `hasExitNodeTag` (internal/headscale/nodes.go, used by ListExitNodes →
#     /my/exit-nodes) says a node is an exit node if it carries tag:exit-node OR
#     its name starts with `exit-`/`exitnode` OR it advertises 0.0.0.0/0|::/0.
#   * `computeSnapshot` (internal/monitoring) said a node is an exit node ONLY if
#     it literally carried tag:exit-node, and its ladder was
#         case !online || !hasTag: state = "offline"
#     so a working, online, route-approved relay without the tag was filed as
#     "offline", `Healthy=false`, and the page told the operator that internet
#     egress was down while users were routing through it.
#
# Two more defects in the same ladder, both found while proving the above:
#   * `degraded` was computed from AvailableRoutes (ADVERTISED), never from
#     ApprovedRoutes — so a relay that advertises 0.0.0.0/0 but was never
#     approved (headscale hands no client an exit route through it) counted as
#     healthy, the exact false positive the monitor exists to prevent.
#   * the monitor wrote a snapshot row for EVERY node in the tailnet, so a
#     14-node tailnet carried 11 permanent `state=offline` rows for plain
#     laptops, and the bot's /exit_nodes_health listed every device.
#
# CONTRACTS
#   A  one definition of "exit node": the monitor uses NodeView.IsExitNode
#   B  the state ladder matches reality (unsupported → offline, no approved
#      default route → degraded, working without the tag → untagged+healthy)
#   C  non-exit nodes get no snapshot row (and stale rows are pruned)
#   D  the alert boundary is "can a client egress", not the literal state pair
#   E  /admin/exit-nodes shows the approved half and names both warning buckets
#   F  the template renders the untagged state and both warnings
#   G  i18n parity for every key the new states/banners use (RU + EN)
#   H  the bot agrees (untagged bucket + untagged counts as healthy)
#   I  Go contracts (behavioural: the aro node shape, pruning, alert boundary)
#   J  live state on this host (SKIPs when the skygate DB is unavailable)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B273: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MON=internal/monitoring/exit_node_monitor.go
MONT=internal/monitoring/exit_node_monitor_test.go
ADMIN=internal/feature/admin/exit_nodes.go
TPL=internal/handlers/templates/admin/exit_nodes.html
CATE=internal/i18n/catalog_exit_nodes.go
CATB=internal/i18n/catalog_bot.go
BOT=internal/telegram/commands_exit_node_health.go

hdr "B273 — exit-node health tells the truth (one predicate, named reasons)"

# --- A: one definition of "exit node" ----------------------------------------
if grep -q 'func (m \*ExitNodeMonitor) computeSnapshot(n headscale.NodeView, now time.Time) (db.ExitNodeHealth, bool)' "$MON"; then
  ok "A.1 computeSnapshot reports whether the node is an exit node at all"
else
  bad "A.1 computeSnapshot must return (db.ExitNodeHealth, bool)"
fi

if grep -q 'if !n.IsExitNode {' "$MON"; then
  ok "A.2 the monitor consults NodeView.IsExitNode (the shared predicate)"
else
  bad "A.2 computeSnapshot must gate on n.IsExitNode, not on a local tag test"
fi

if grep -q 'IsExitNode:      hasExitNodeTag' internal/headscale/nodes.go || \
   grep -q 'IsExitNode:' internal/headscale/nodes.go; then
  ok "A.3 NodeView.IsExitNode is still the field ListExitNodes filters on"
else
  bad "A.3 NodeView.IsExitNode missing from internal/headscale/nodes.go"
fi

if grep -q 'tag:exit-node" {' "$MON"; then
  bad "A.4 the monitor still has a literal tag:exit-node test"
else
  ok "A.4 no literal tag:exit-node test decides exit-node-ness any more"
fi

if grep -q 'case !online || !hasTag:' "$MON"; then
  bad "A.5 the pre-B273 '!online || !hasTag → offline' ladder is back"
else
  ok "A.5 the pre-B273 ladder (tag-missing ⇒ offline) is gone"
fi

# --- B: the state ladder -----------------------------------------------------
if grep -q 'func exitNodeUsable(state string) bool' "$MON" && \
   grep -q 'case "online", "untagged":' "$MON"; then
  ok "B.1 exitNodeUsable() defines usable = online | untagged"
else
  bad "B.1 exitNodeUsable() with the online|untagged case is missing"
fi

if grep -q 'state = "untagged"' "$MON"; then
  ok "B.2 the untagged state exists"
else
  bad "B.2 the monitor must have a distinct 'untagged' state"
fi

if grep -q 'for _, r := range n.ApprovedRoutes {' "$MON"; then
  ok "B.3 health keys on ApprovedRoutes, not only on what is advertised"
else
  bad "B.3 computeSnapshot must read n.ApprovedRoutes"
fi

if grep -q 'approvedV4' "$MON"; then
  ok "B.4 approvedV4 drives the degraded bucket"
else
  bad "B.4 approvedV4 missing from computeSnapshot"
fi

if grep -q 'healthy := exitNodeUsable(state)' "$MON"; then
  ok "B.5 Healthy is derived from exitNodeUsable (one source of truth)"
else
  bad "B.5 Healthy must be exitNodeUsable(state)"
fi

# --- C: no rows for plain devices -------------------------------------------
if grep -q 'db.DeleteExitNodeHealth(m.DB.Current(), n.ID)' "$MON"; then
  ok "C.1 stale snapshot rows for non-exit nodes are pruned"
else
  bad "C.1 the tick must delete the snapshot row of a non-exit node"
fi

if grep -q 'prune non-exit node' "$MON"; then
  ok "C.2 pruning is logged"
else
  bad "C.2 pruning a non-exit node must be logged"
fi

# --- D: the alert boundary --------------------------------------------------
if grep -q 'return exitNodeUsable(from) != exitNodeUsable(to)' "$MON"; then
  ok "D.1 isCalmModeAlert alerts on every usable-boundary crossing"
else
  bad "D.1 isCalmModeAlert must compare exitNodeUsable(from) with exitNodeUsable(to)"
fi

# --- E: the admin handler ---------------------------------------------------
if grep -q 'for _, r := range hn.ApprovedRoutes {' "$ADMIN"; then
  ok "E.1 the admin page reads the live approved routes"
else
  bad "E.1 AdminExitNodes must fill ApprovedV4Default from hn.ApprovedRoutes"
fi

if grep -q '"UntaggedCount":' "$ADMIN" && grep -q '"UnapprovedCount":' "$ADMIN"; then
  ok "E.2 both warning buckets are passed to the template"
else
  bad "E.2 UntaggedCount and UnapprovedCount must reach the template"
fi

if grep -q '"ScoredCount":' "$ADMIN" && grep -q '{{if and .ScoredCount (eq .HealthyCount 0)}}' "$TPL"; then
  ok "E.2b the zero-healthy banner is gated on rows that actually have a verdict"
else
  bad "E.2b the red banner must be gated on ScoredCount, not on TotalCount"
fi

if grep -q 'func hasExitNodeTagFor(tags \[\]string) bool' "$ADMIN" && \
   grep -q 'strings.EqualFold(t, "tag:exit-node")' "$ADMIN"; then
  ok "E.3 the admin's tag test is case-insensitive (matches headscale)"
else
  bad "E.3 hasExitNodeTagFor must exist and use EqualFold"
fi

if grep -q 'if hsEnriched {' "$ADMIN"; then
  ok "E.4 the live-derived overrides are skipped when headscale did not answer"
else
  bad "E.4 gate the live-derived state overrides on a successful enrichment"
fi

# --- F: the template --------------------------------------------------------
if grep -q 'eq .State "untagged"' "$TPL"; then
  ok "F.1 the state cell renders 'untagged'"
else
  bad "F.1 the template must render the untagged state"
fi

if grep -q 'exit_nodes.health.untagged_banner' "$TPL"; then
  ok "F.2 the untagged warning banner is rendered"
else
  bad "F.2 the template must render the untagged banner"
fi

if grep -q 'exit_nodes.health.unapproved_banner' "$TPL"; then
  ok "F.3 the unapproved-routes banner is rendered"
else
  bad "F.3 the template must render the unapproved banner"
fi

if grep -q 'exit_nodes.health.not_approved' "$TPL"; then
  ok "F.4 the routes cell flags advertised-but-unapproved"
else
  bad "F.4 the routes cell must flag unapproved routes"
fi

# --- G: i18n parity ---------------------------------------------------------
for key in \
  exit_nodes.health.state_untagged \
  exit_nodes.health.untagged_tip \
  exit_nodes.health.untagged_banner \
  exit_nodes.health.untagged_banner_help \
  exit_nodes.health.unapproved_banner \
  exit_nodes.health.unapproved_banner_help \
  exit_nodes.health.not_approved \
  exit_nodes.health.not_approved_tip ; do
  n=$(grep -c "\"${key}\"" "$CATE" || true)
  if [ "$n" -ge 2 ]; then
    ok "G $key present in both catalogues"
  else
    bad "G $key appears $n time(s) in $CATE (want RU + EN)"
  fi
done

if [ "$(grep -c '"bot.exit_nodes_health.bucket_untagged"' "$CATB" || true)" -ge 2 ]; then
  ok "G bot bucket_untagged present in both catalogues"
else
  bad "G bot.exit_nodes_health.bucket_untagged must exist in RU and EN"
fi

# --- H: the bot -------------------------------------------------------------
if grep -q '{state: "untagged"}, {state: "online"},' "$BOT"; then
  ok "H.1 the bot has an untagged bucket"
else
  bad "H.1 the bot must group the untagged state"
fi

if grep -q 'healthyCount := len(byState\["online"\]) + len(byState\["untagged"\])' "$BOT"; then
  ok "H.2 untagged relays count as healthy in the bot header"
else
  bad "H.2 the bot's healthy count must include untagged"
fi

# --- I: Go contracts --------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  out="$(go test ./internal/monitoring/ -run 'B273|IsCalmModeAlert|DegradedTransition' 2>&1)"
  if grep -q '^ok' <<< "$out"; then
    ok "I.1 internal/monitoring behaviour tests pass (aro node shape, pruning, alert boundary)"
  else
    bad "I.1 go test ./internal/monitoring/ failed: $(printf '%s' "$out" | tail -n 5)"
  fi

  if grep -q 'TestComputeSnapshot_UntaggedIsHealthy_B273' "$MONT" && \
     grep -q 'TestTick_PrunesNonExitNodeRows_B273' "$MONT" && \
     grep -q 'TestComputeSnapshot_NotAnExitNode_IsSkipped_B273' "$MONT"; then
    ok "I.2 the three B273 regression tests are present"
  else
    bad "I.2 B273 regression tests missing from $MONT"
  fi

  i18n_out="$(go test ./internal/i18n/ -run TestCatalogsParity 2>&1)"
  if grep -q '^ok' <<< "$i18n_out"; then
    ok "I.3 i18n catalogue parity holds"
  else
    bad "I.3 TestCatalogsParity failed: $(printf '%s' "$i18n_out" | tail -n 5)"
  fi
else
  skip "I Go toolchain not on PATH"
fi

# --- J: live state ----------------------------------------------------------
DB_CANDIDATES="/home/skyadmin/skygate/data/skygate.db /var/lib/skygate/skygate.db"
LIVE_DB=""
for c in $DB_CANDIDATES; do
  if [ -r "$c" ] && command -v sqlite3 >/dev/null 2>&1; then LIVE_DB="$c"; break; fi
done
if [ -z "$LIVE_DB" ]; then
  skip "J no readable skygate SQLite DB on this host"
else
  # Every stored state must be one of the four the ladder can produce.
  bad_states="$(sqlite3 "$LIVE_DB" "SELECT node_id||'='||state FROM exit_node_health WHERE state NOT IN ('online','untagged','degraded','offline');" 2>/dev/null)"
  if [ -z "$bad_states" ]; then
    ok "J.1 every exit_node_health row carries a known state ($LIVE_DB)"
  else
    bad "J.1 unknown states in exit_node_health: $bad_states"
  fi
  # An online, route-advertising relay without the tag must NOT be offline.
  wrong="$(sqlite3 "$LIVE_DB" "SELECT node_id||' '||hostname FROM exit_node_health WHERE state='offline' AND online=1 AND advertised_routes_ok=1;" 2>/dev/null)"
  if [ -z "$wrong" ]; then
    ok "J.2 no online+advertising relay is stored as offline"
  else
    bad "J.2 pre-B273 misclassification still in the snapshot: $wrong"
  fi
fi

printf '\n\033[1mB273: %d PASS, %d FAIL, %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
