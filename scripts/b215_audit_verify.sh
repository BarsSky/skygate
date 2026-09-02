#!/usr/bin/env bash
# B215 live-verify on the agent.
#
# Strategy
# ~~~~~~~~
# The four B215 emissions live in:
#   1. node_init  → cmd/skygate/runInitBootstrap calls
#                   db.InsertClusterAudit(NodeInit) at the
#                   END of the bootstrap.
#   2. node_join  → internal/cluster.Join calls
#                   db.InsertClusterAudit(NodeJoin) on the
#                   NEW-INSERT path (not the idempotent
#                   existing-node path).
#   3. node_drain → internal/db.FailoverClusterNode calls
#                   db.InsertClusterAudit(NodeDrain) in the
#                   same Tx as the demote UPDATE.
#   4. node_leave → internal/cluster.RemoveNode calls
#                   db.InsertClusterAudit(NodeLeave) in the
#                   same Tx as the DELETE.
#
# Running these via the CLI is brittle:
#   - `skygate init`              modifies the live "agent"
#                                 cluster_node row (UpsertNode
#                                 ON CONFLICT).
#   - `skygate cluster join`      uses os.Hostname() → hits
#                                 the idempotent-existing-node
#                                 branch and does NOT emit
#                                 node_join (it only fires on
#                                 the new-INSERT path).
#   - `skygate cluster failover`  requires a state=ready
#                                 primary + state=ready
#                                 skygate-standby, but the
#                                 agent's cluster_node rows
#                                 are mostly in state=failed.
#   - direct DELETE FROM cluster_node would bypass
#     cluster.RemoveNode and skip the node_leave emission.
#
# Instead, we use a single Go helper (scripts/b215_liveverify.go,
# //go:build ignore) that calls the SAME library functions
# the CLI/handlers call, with controlled inputs and full DB
# state restoration at the end. The helper is independent per
# subcommand — you can re-run safely; each subcommand
# snapshots and restores the rows it touches.
set -euo pipefail
cd /home/skyadmin/skygate

set --
set -a
# shellcheck disable=SC1091
. /home/skyadmin/skygate/.env
set +a

GO_BIN="${GO_BIN:-/snap/go/current/bin/go}"
export GOCACHE="/tmp/go-cache"
export GOMODCACHE="/tmp/go-modcache"
mkdir -p "$GOCACHE" "$GOMODCACHE"

DB="skygate_staging"
# Use `env` so the PGPASSWORD env var prefix survives being
# assigned to a variable + invoked. The naive form
# `DSN_RUN="PGPASSWORD=... psql ..."` followed by `$DSN_RUN -c "..."`
# fails because bash tries to execute "PGPASSWORD=skygate_admin_pass"
# as a command.
DSN_RUN="env PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5433 -U admin -d $DB -tA"

HELPER_SRC="/home/skyadmin/skygate/scripts/b215_liveverify.go"
HELPER_BIN="/tmp/skygate_b215_helper"
if [[ ! -f "$HELPER_SRC" ]]; then
  echo "FATAL: $HELPER_SRC not found — was it synced?" >&2
  exit 1
fi

echo "=== Step 0: build Go helper (does NOT restart skygate) ==="
# Build the B215 helper binary. It calls the B215-emitting
# functions directly (cluster.UpsertNode, cluster.Join,
# db.FailoverClusterNode, cluster.RemoveNode) with
# controlled inputs. The helper handles snapshot/restore
# internally, so we don't need to manage state here.
$GO_BIN build -o "$HELPER_BIN" "$HELPER_SRC"
echo "  built: $HELPER_BIN"
echo ""

echo "=== Step 1: snapshot cluster_audit BEFORE B215 events ==="
echo "  pre-B215 action counts:"
$DSN_RUN -c "SELECT action, count(*) FROM cluster_audit GROUP BY action ORDER BY action"
echo ""

echo "=== Step 2: node_init (via runInitBootstrap's emission path) ==="
INIT_OUT=$($HELPER_BIN init 2>&1)
echo "$INIT_OUT" | head -3
INIT_LINE=$($DSN_RUN -c "SELECT count(*) FROM cluster_audit WHERE action='node_init'")
echo "  total node_init rows: $INIT_LINE"
echo ""

echo "=== Step 3: node_join (via cluster.Join's new-INSERT path) ==="
JOIN_OUT=$($HELPER_BIN join 2>&1)
echo "$JOIN_OUT" | head -3
JOIN_LINE=$($DSN_RUN -c "SELECT count(*) FROM cluster_audit WHERE action='node_join'")
echo "  total node_join rows: $JOIN_LINE"
echo ""

echo "=== Step 4: node_drain (via FailoverClusterNode) ==="
DRAIN_OUT=$($HELPER_BIN drain --actor="b215-liveverify" --reason="B215 verify" 2>&1)
echo "$DRAIN_OUT" | head -3
DRAIN_LINE=$($DSN_RUN -c "SELECT count(*) FROM cluster_audit WHERE action='node_drain'")
echo "  total node_drain rows: $DRAIN_LINE"
echo ""

echo "=== Step 5: node_leave (via cluster.RemoveNode) ==="
LEAVE_OUT=$($HELPER_BIN leave 2>&1)
echo "$LEAVE_OUT" | head -3
LEAVE_LINE=$($DSN_RUN -c "SELECT count(*) FROM cluster_audit WHERE action='node_leave'")
echo "  total node_leave rows: $LEAVE_LINE"
echo ""

echo "=== Step 6: final summary ==="
echo "  action counts (B215 contract: node_init, node_join, node_drain, node_leave should all be > 0):"
$DSN_RUN -c "SELECT action, count(*) FROM cluster_audit GROUP BY action ORDER BY action"
echo ""
echo "  B215 success checks (all 4 should be > 0):"
echo "    node_init  : $INIT_LINE"
echo "    node_join  : $JOIN_LINE"
echo "    node_drain : $DRAIN_LINE"
echo "    node_leave : $LEAVE_LINE"
echo ""
if [[ "$INIT_LINE" -gt 0 && "$JOIN_LINE" -gt 0 && "$DRAIN_LINE" -gt 0 && "$LEAVE_LINE" -gt 0 ]]; then
  echo "  B215 LIVE-VERIFY: PASS"
else
  echo "  B215 LIVE-VERIFY: PARTIAL (one or more counts were 0 — see above)"
fi
echo ""
echo "=== B215 live-verify DONE ==="
