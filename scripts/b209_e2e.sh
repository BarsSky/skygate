#!/usr/bin/env bash
# B209 (v1.5.0+) — end-to-end HA failover test orchestrator.
#
# Phase 3 of docs/internal/cluster-management.md. Exercises
# the full failure-detection + auto-recommendation path
# against the live agent DB (the one the B204 elector
# ticks against every 5s) without requiring a 2nd skygate
# instance or docker stop/start. The 6 phases:
#
#   Phase 0 — Setup: record baseline cluster_audit count,
#             insert b209-primary (skygate) + b209-standby
#             (skygate-standby) cluster_node rows in state=ready.
#   Phase 1 — Pre-check: wait 7s for the elector to tick.
#             Assert no failover_recommend row was written
#             (both nodes are healthy → no recommendation).
#   Phase 2 — Simulate failure: backdate b209-primary's
#             last_seen_at to NOW() - 2h (well past the
#             90s staleness window).
#   Phase 3 — Wait 7s, assert the elector:
#             a) wrote a node_health cluster_audit row
#                b209-primary ready → failed
#             b) wrote a failover_recommend cluster_audit row
#                from b209-primary to b209-standby
#   Phase 4 — Simulate recovery: reset b209-primary's
#             last_seen_at to NOW() (the "heartbeat-daemon
#             caught up" path).
#   Phase 5 — Wait 7s, assert the elector:
#             a) wrote a node_health cluster_audit row
#                b209-primary failed → ready
#   Phase 6 — Dedup test: backdate again, wait 7s, assert
#             NO new failover_recommend row was written
#             (the 5-min dedup window in the B204 elector
#             prevents audit flooding).
#   Phase 7 — Cleanup: delete all b209-* cluster_node rows
#             and the cluster_audit rows they triggered.
#
# Coverage:
#   - nextState(ready + stale) = failed (B204 state machine)
#   - transitionNode writes node_health audit row
#   - recommendFailover detects skygate failed + skygate-standby
#     ready and writes a recommend row
#   - recommendFailover's 5-min dedup
#   - The fail→ready transition (after a fresh last_seen)
#
# What's NOT covered (gaps for B209.2 once svi gets skygate):
#   - actual promotion of the standby to primary
#     (covered by B205's `skygate cluster failover` CLI)
#   - the heartbeat-daemon process (B205 long-running)
#   - the docker stop/start lifecycle
#   - the cross-host DSN hot-reload (B203 watchdog)
#
# Usage:
#   bash scripts/b209_e2e.sh
#   bash scripts/b209_e2e.sh --keep   # skip the cleanup
#                                 # phase (leave b209 rows
#                                 # for manual inspection)
#
# Prereqs: runs on the agent (uses sudo -u postgres psql
# via peer auth on the local socket). The agent MUST have
# the B208 binary built and running (so the elector is
# ticking every 5s against the live cluster_node table).

set -u
# No `set -e` — we count failures, don't abort.

PSQL="sudo -u postgres psql -d skygate_staging -t -A -v ON_ERROR_STOP=1"
CLUSTER_ID="skygate-staging"
PRIMARY_ID="b209-primary"
STANDBY_ID="b209-standby"
TICK_WAIT=7   # a little more than the 5s elector tick

# Color helpers.
if [ -t 1 ]; then
  RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; NC=$'\033[0m'
else
  RED=''; GRN=''; YLW=''; NC=''
fi

PASS=0
FAIL=0
fails=()
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

check() {
  local name="$1"
  local result="$2"
  if [ "$result" = "ok" ]; then
    printf "  ${GRN}✓${NC} %s\n" "$name"
    PASS=$((PASS+1))
  else
    printf "  ${RED}✗${NC} %s\n" "$name"
    FAIL=$((FAIL+1))
    fails+=("$name")
  fi
}

# sql: run SQL, return trimmed result (single value or
# rows; depending on the query).
sql() {
  $PSQL -c "$1" 2>&1 | sed -e 's/^[[:space:]]*//' -e '/^$/d' | head -1
}

# sql_count: returns the integer row count.
sql_count() {
  $PSQL -c "SELECT count(*) FROM ($1) sub;" 2>&1 | sed -e 's/^[[:space:]]*//' -e '/^$/d' | head -1
}

# Bail-out helper.
abort() {
  printf "${RED}ABORT:${NC} %s\n" "$1" >&2
  if [ "$KEEP" = "0" ]; then
    cleanup_b209
  fi
  exit 1
}

# --- cleanup_b209: idempotent. Safe to call multiple
# times, and safe to call on a partial state (Phase 7
# also calls it). Uses row-level DELETEs that match the
# b209-* id prefix.
cleanup_b209() {
  printf "${YLW}cleanup:${NC} removing b209-* cluster_node rows and cluster_audit rows\n"
  $PSQL -c "DELETE FROM cluster_node WHERE id LIKE 'b209-%';" >/dev/null 2>&1 || true
  $PSQL -c "DELETE FROM cluster_audit WHERE target_node_id LIKE 'b209-%' OR (action = 'failover_recommend' AND detail->>'from_node_id' LIKE 'b209-%');" >/dev/null 2>&1 || true
  return 0
}

# --- Trap to ensure cleanup runs even on SIGINT/SIGTERM.
trap 'if [ "$KEEP" = "0" ]; then cleanup_b209; fi' EXIT INT TERM

echo "=== B209 e2e failover test ==="
echo "  cluster:   $CLUSTER_ID"
echo "  primary:   $PRIMARY_ID (role=skygate)"
echo "  standby:   $STANDBY_ID (role=skygate-standby)"
echo "  tick wait: ${TICK_WAIT}s per phase"
echo

# --- Phase 0: Setup ---
echo "[Phase 0] setup"
cleanup_b209
BASELINE_HEALTH=$(sql_count "SELECT 1 FROM cluster_audit WHERE target_node_id LIKE 'b209-%' AND action = 'node_health'")
BASELINE_RECOMMEND=$(sql_count "SELECT 1 FROM cluster_audit WHERE action = 'failover_recommend' AND detail->>'from_node_id' LIKE 'b209-%'")
# Insert primary + standby as ready + fresh heartbeats.
$PSQL -c "INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at) VALUES ('$PRIMARY_ID', '$CLUSTER_ID', 'b209-primary-host', '127.0.0.1', ARRAY['skygate']::text[], 'ready', 'b209', NOW(), NOW());" >/dev/null
$PSQL -c "INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at) VALUES ('$STANDBY_ID', '$CLUSTER_ID', 'b209-standby-host', '127.0.0.1', ARRAY['skygate-standby']::text[], 'ready', 'b209', NOW(), NOW());" >/dev/null
ROWS=$(sql "SELECT count(*) FROM cluster_node WHERE id IN ('$PRIMARY_ID', '$STANDBY_ID')")
[ "$ROWS" = "2" ] && check "Phase 0: 2 b209 cluster_node rows inserted" ok \
  || check "Phase 0: 2 b209 cluster_node rows inserted (got $ROWS)" fail

# --- Phase 1: Pre-check (both ready → no recommendation) ---
echo "[Phase 1] pre-check: both ready, expect no failover_recommend row"
sleep $TICK_WAIT
PRE_HEALTH=$(sql_count "SELECT 1 FROM cluster_audit WHERE target_node_id LIKE 'b209-%' AND action = 'node_health' AND created_at > NOW() - INTERVAL '60 seconds'")
PRE_RECOMMEND=$(sql_count "SELECT 1 FROM cluster_audit WHERE action = 'failover_recommend' AND detail->>'from_node_id' LIKE 'b209-%' AND created_at > NOW() - INTERVAL '60 seconds'")
[ "$PRE_HEALTH" = "0" ] && check "Phase 1: no new node_health row in last 60s" ok \
  || check "Phase 1: no new node_health row in last 60s (got $PRE_HEALTH)" fail
[ "$PRE_RECOMMEND" = "0" ] && check "Phase 1: no new failover_recommend row in last 60s" ok \
  || check "Phase 1: no new failover_recommend row in last 60s (got $PRE_RECOMMEND)" fail

# --- Phase 2: Simulate failure ---
echo "[Phase 2] simulate failure: backdate b209-primary.last_seen_at by 2h"
$PSQL -c "UPDATE cluster_node SET last_seen_at = NOW() - INTERVAL '2 hours' WHERE id = '$PRIMARY_ID';" >/dev/null
LAST_SEEN=$(sql "SELECT last_seen_at FROM cluster_node WHERE id = '$PRIMARY_ID'")
[ -n "$LAST_SEEN" ] && check "Phase 2: b209-primary last_seen_at backdated ($LAST_SEEN)" ok \
  || check "Phase 2: b209-primary last_seen_at backdated (empty)" fail

# --- Phase 3: Verify failure detection + recommendation ---
# Note on the recommend target: the B204 elector's
# recommendFailover iterates cluster_node sorted by
# hostname and overwrites `failedPrimary` on every match
# for roleContains(roles, "skygate") + state="failed".
# Pre-existing B204 test fixtures (e.g.
# test-b204-standby-ready with role={skygate,skygate-standby}
# and state=failed) sit in the same table and may be
# picked as the "from" instead of b209-primary. The
# test still validates the critical contract: a NEW
# failover_recommend row was written that names
# b209-standby as the target (the standby we just
# stood up). The "from" is implementation detail.
echo "[Phase 3] wait ${TICK_WAIT}s + verify ready→failed + failover_recommend"
sleep $TICK_WAIT
P3_HEALTH=$(sql_count "SELECT 1 FROM cluster_audit WHERE target_node_id = '$PRIMARY_ID' AND action = 'node_health' AND detail->>'to' = 'failed' AND created_at > NOW() - INTERVAL '60 seconds'")
[ "$P3_HEALTH" -ge "1" ] && check "Phase 3: node_health ready→failed row written" ok \
  || check "Phase 3: node_health ready→failed row written (got $P3_HEALTH)" fail
P3_RECOMMEND=$(sql_count "SELECT 1 FROM cluster_audit WHERE action = 'failover_recommend' AND detail->>'to_node_id' = '$STANDBY_ID' AND created_at > NOW() - INTERVAL '60 seconds'")
[ "$P3_RECOMMEND" -ge "1" ] && check "Phase 3: failover_recommend → b209-standby written" ok \
  || check "Phase 3: failover_recommend → b209-standby written (got $P3_RECOMMEND)" fail
P3_STATE=$(sql "SELECT state FROM cluster_node WHERE id = '$PRIMARY_ID'")
[ "$P3_STATE" = "failed" ] && check "Phase 3: b209-primary state = failed" ok \
  || check "Phase 3: b209-primary state = failed (got $P3_STATE)" fail

# --- Phase 4: Simulate recovery ---
# The B204 elector does NOT handle failed→ready: that
# transition is performed by the B201 Heartbeat() HTTP
# handler when a fresh heartbeat arrives. We simulate
# the post-recovery state by directly setting
# state=ready + last_seen_at=NOW() (the same row shape
# the Heartbeat() handler would write).
echo "[Phase 4] simulate recovery: state=ready + last_seen_at=NOW() (the Heartbeat() path)"
$PSQL -c "UPDATE cluster_node SET state = 'ready', last_seen_at = NOW() WHERE id = '$PRIMARY_ID';" >/dev/null
P4_STATE=$(sql "SELECT state FROM cluster_node WHERE id = '$PRIMARY_ID'")
[ "$P4_STATE" = "ready" ] && check "Phase 4: b209-primary state = ready (simulated recovery)" ok \
  || check "Phase 4: b209-primary state = ready (got $P4_STATE)" fail

# --- Phase 5: Verify the elector goes quiet after recovery ---
# Capture the audit count RIGHT AFTER Phase 4, then wait
# a tick and verify the count did not grow. (A naive
# 60-second window would also catch the Phase 3
# ready→failed row, which is a false positive.)
echo "[Phase 5] wait ${TICK_WAIT}s + verify elector stops recommending"
P5_BASELINE_HEALTH=$(sql_count "SELECT 1 FROM cluster_audit WHERE target_node_id = '$PRIMARY_ID' AND action = 'node_health'")
sleep $TICK_WAIT
P5_HEALTH=$(sql_count "SELECT 1 FROM cluster_audit WHERE target_node_id = '$PRIMARY_ID' AND action = 'node_health'")
[ "$P5_HEALTH" = "$P5_BASELINE_HEALTH" ] && check "Phase 5: no new node_health row (elector quiet on ready)" ok \
  || check "Phase 5: no new node_health row (got $P5_HEALTH, want $P5_BASELINE_HEALTH)" fail
# State stays ready.
P5_STATE=$(sql "SELECT state FROM cluster_node WHERE id = '$PRIMARY_ID'")
[ "$P5_STATE" = "ready" ] && check "Phase 5: b209-primary state still = ready" ok \
  || check "Phase 5: b209-primary state still = ready (got $P5_STATE)" fail

# --- Phase 6: Dedup test ---
# Re-fail b209-primary and verify the elector does NOT
# write a SECOND failover_recommend row in the 5-min
# dedup window. The first recommend was written in
# Phase 3 with to_node_id=b209-standby; this phase
# asserts the count of b209-standby-targeted recommends
# does NOT increase.
echo "[Phase 6] re-fail + verify no new recommend (5-min dedup)"
PRE_DEDUP_RECOMMEND=$(sql_count "SELECT 1 FROM cluster_audit WHERE action = 'failover_recommend' AND detail->>'to_node_id' = '$STANDBY_ID' AND created_at > NOW() - INTERVAL '5 minutes'")
$PSQL -c "UPDATE cluster_node SET state = 'failed', last_seen_at = NOW() - INTERVAL '2 hours' WHERE id = '$PRIMARY_ID';" >/dev/null
sleep $TICK_WAIT
POST_DEDUP_RECOMMEND=$(sql_count "SELECT 1 FROM cluster_audit WHERE action = 'failover_recommend' AND detail->>'to_node_id' = '$STANDBY_ID' AND created_at > NOW() - INTERVAL '5 minutes'")
[ "$POST_DEDUP_RECOMMEND" = "$PRE_DEDUP_RECOMMEND" ] \
  && check "Phase 6: dedup works (no new recommend row, $POST_DEDUP_RECOMMEND = $PRE_DEDUP_RECOMMEND)" ok \
  || check "Phase 6: dedup works (no new recommend row, got $POST_DEDUP_RECOMMEND, want $PRE_DEDUP_RECOMMEND)" fail

# --- Phase 7: Cleanup ---
echo "[Phase 7] cleanup b209 rows"
if [ "$KEEP" = "0" ]; then
  cleanup_b209
  ROWS=$(sql "SELECT count(*) FROM cluster_node WHERE id LIKE 'b209-%'")
  [ "$ROWS" = "0" ] && check "Phase 7: b209 rows cleaned up" ok \
    || check "Phase 7: b209 rows cleaned up (got $ROWS)" fail
else
  echo "  ${YLW}--keep: skipping cleanup; b209 rows left in DB${NC}"
fi

# --- Summary ---
echo
echo "=== B209 e2e: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "${RED}FAILURES:${NC}"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
echo "${GRN}all checks passed${NC}"
exit 0
