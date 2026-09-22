#!/usr/bin/env bash

# Live-state check: skip (do not fail) when the docker daemon is unreachable.
. "$(dirname "$0")/lib/skip_if_no_docker.sh"
# check_b_node_owner_map_orphans.sh — verify node_owner_map integrity:
# every (node_id) row points at a real headscale user (or is
# deliberately orphaned with a valid reason).
#
# 2026-09-12 (B243 follow-up investigation): the live VM has 2 rows in
# node_owner_map with headscale_user_id=2147455555 (overflow int).
# The MAX valid headscale_user_id is 86 (int32 / int64 ceiling), so
# 2147455555 is impossible — it must be a bug where some Go integer
# (probably a sentinel / uninitialized value) overflowed. The portal
# can't filter these rows out cleanly because their headscale_user_id
# doesn't match any real headscale user, so ACL generation silently
# skips them.
#
# This B-check catches:
#   - Rows with headscale_user_id NOT in (SELECT id FROM current
#     headscale users): orphaned (the headscale user was deleted
#     but the node_owner_map row wasn't cleaned up).
#   - Rows with headscale_user_id == 0 (the "no link" sentinel
#     shouldn't be in node_owner_map at all — a node with no
#     link should be in the B77 backfill path, not in node_owner_map).
#   - Rows with headscale_user_id > 100000 or otherwise
#     impossible / overflowed.
#
# This B-check does NOT clean up — it just reports. The operator
# must decide whether to delete the row (manual DELETE) or fix the
# underlying bug that created it.
#
# Usage:
#   bash scripts/check_b_node_owner_map_orphans.sh
#
# Exit codes:
#   0 = clean
#   1 = orphans found
#   2 = DB unreachable / table missing

set -uo pipefail

CONTAINER="${SKYGATE_CONTAINER:-skygate-skygate-1}"
PG_CONTAINER="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"
HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"

for c in "$CONTAINER" "$PG_CONTAINER" "$HEADSCALE_CONTAINER"; do
    if ! sudo docker inspect "$c" >/dev/null 2>&1; then
        # B281 (2026-09-22): absent live dependency = SKIP, never FAIL
        # (AGENTS.md §1.1). The old `exit 2` was read by the catalog as a
        # failure on every CI run, where no stack exists at all.
        echo "  SKIP  $c container not running (live check — run it on the skygate host)"
        exit 0
    fi
done

# ── live headscale user IDs (string set, comma-joined for SQL) ──
HS_IDS=$(sudo docker exec "$HEADSCALE_CONTAINER" headscale users list -o json 2>/dev/null \
  | python3 -c 'import json,sys; print(",".join(str(u["id"]) for u in json.load(sys.stdin)))')
[ -n "$HS_IDS" ] || { echo "  FAIL  headscale returned no users"; exit 2; }
echo "headscale live user IDs: $HS_IDS"

# ── query node_owner_map ──
# (node_owner_map PK is node_id, NOT id — common mistake.)
SQL="SELECT node_id, headscale_user_id, username, tag FROM node_owner_map
     WHERE headscale_user_id NOT IN ($HS_IDS)
        OR headscale_user_id = 0
        OR headscale_user_id < 0
        OR headscale_user_id > 100000
     ORDER BY headscale_user_id, node_id"

ORPHAN_ROWS=$(sudo docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -F'|' -c "$SQL" 2>/dev/null)
# ORPHAN_COUNT: strip trailing whitespace (psql -At adds \n even when empty).
ORPHAN_COUNT=$(echo -n "$ORPHAN_ROWS" | grep -c '.' || true)

if [ -z "$ORPHAN_COUNT" ] || [ "$ORPHAN_COUNT" = "0" ]; then
    TOTAL=$(sudo docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
      "SELECT COUNT(*) FROM node_owner_map" 2>/dev/null)
    echo "  PASS  A: 0 orphan rows in node_owner_map (all $TOTAL rows point at live headscale users)"
    exit 0
fi

echo "  FAIL  A: $ORPHAN_COUNT orphan row(s) in node_owner_map"
echo
# Capture the first orphan's node_id for the remediation hint, then list.
FIRST_ROW=$(echo "$ORPHAN_ROWS" | grep -m1 '.')
ORPHAN_NODE_ID=$(echo "$FIRST_ROW" | awk -F'|' '{print $1}')
echo "$ORPHAN_ROWS" | head -20 | while IFS='|' read -r node_id hs_id username tag; do
    [ -z "$node_id" ] && continue
    echo "    row node_id=$node_id hs_id=$hs_id username=$username tag=$tag"
done
echo
echo "  These rows reference headscale users that don't exist (or have"
echo "  impossible IDs like 2147455555 — see T240 / overflow bug)."
echo "  The ACL generator silently skips them, so the rules attached to"
echo "  these nodes don't apply."
echo
echo "  Remediation options:"
if [ -n "$ORPHAN_NODE_ID" ]; then
    echo "    (a) DELETE the row if the node is truly dead:"
    echo "        DELETE FROM node_owner_map WHERE node_id = '$ORPHAN_NODE_ID';"
    echo "    (b) UPDATE to a valid hs_id if the operator knows which"
    echo "        headscale user owns the node:"
    echo "        UPDATE node_owner_map SET headscale_user_id = <valid_id>"
    echo "          WHERE node_id = '$ORPHAN_NODE_ID';"
    echo "    (c) Run the B77 backfill (auto-attribute by node properties):"
    echo "        restart skygate (the backfill runs at startup)"
fi
exit 1
