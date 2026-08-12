#!/bin/bash
# scripts/reconcile_snapshots.sh — write an acl_snapshots row
# that matches the current live headscale policy's updatedAt.
#
# R9 (verify-post) compares:
#   - live policy's updatedAt (headscale API)
#   - last acl_snapshots row's created_at (skygate DB)
# and fails if they differ by more than 60s.
#
# R9 will diverge when:
#   1. An operator manually edits the policy via headscale API
#      (the snapshot stays at the last SetPolicy time, but
#      headscale's updatedAt advances)
#   2. skygate crashes / restarts during a reapply and only
#      the headscale PUT completed
#   3. Direct headscale DB edit (rare; for emergency)
#
# This script writes a snapshot row with created_at = the live
# policy's updatedAt, so R9 sees them aligned.
#
# Usage (from operator workstation):
#   bash scripts/reconcile_snapshots.sh
#
# This is a one-off reconciliation, NOT a normal flow. The
# normal flow is:
#   1. Operator changes exit rules in /my/exit-rules (or
#      /admin/exit-rules for cross-user)
#   2. POST handler calls GenerateACL + SetPolicy
#   3. GenerateACL writes the snapshot row before SetPolicy
#   4. Verify-post R9 sees the alignment automatically
#
# 2026-07-30: extracted from the inline fix in this session's
# operator actions. Use ONLY when the live policy was edited
# outside skygate's normal flow.
#
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — sqlite3 → psql.
# Pre-v1.3.1 the script:
#   1. `docker cp` the skygate container's /data/skygate.db out
#      (alpine has no sqlite3; copy to host first, edit there)
#   2. `sqlite3 /tmp/_db_recon_$$ "INSERT INTO acl_snapshots ..."`
#      (SQLite's readfile() loads the policy JSON from disk
#      into the INSERT)
#   3. `docker cp` the modified DB back
#   4. `docker exec rm` the container's file (clean up)
# v1.3.0 removed /data/skygate.db. v1.3.1 keeps the skygate
# container out of the loop entirely: we use a throwaway
# postgres:18-alpine on the headscale_default bridge to do the
# INSERT. The container never stops, never restarts, and
# never has its DB touched (other than the INSERT we made,
# which is committed via a single psql invocation).
#
# Why still SSH to the skygate host: the headscale API lives
# on the same network as the operator workstation (sometimes
# public-internet, sometimes VPN). The PG cluster also lives
# there, and the DSN might be the docker-compose local-PG or
# an external HA cluster. Doing the whole script remotely
# keeps the operator's workstation out of the trust path.

set -e

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"

SSH_KEY="${SSH_KEY:-}"
for cand in \
  "$HOME/.ssh/id_ed25519" \
  "$HOME/.ssh/id_rsa" \
  "/mnt/c/Users/knaga/.ssh/id_ed25519" \
  "/c/Users/knaga/.ssh/id_ed25519"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then
    SSH_KEY="$cand"
    break
  fi
done

if [ -z "$SSH_KEY" ]; then
  echo "ERROR: no SSH key found" >&2
  exit 2
fi

SSH="ssh -i $SSH_KEY -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes $SSH_HOST"
CREATED_BY="${CREATED_BY:-manual_reconcile_$(date -u +%Y%m%d_%H%M%S)}"

echo "=== reconcile_snapshots.sh ==="
echo "  ssh:    $SSH_HOST"
echo "  by:     $CREATED_BY"
echo

# Get live policy
# 2026-08-12: v1.3.1 — the SSH remote block no longer touches
# the skygate container's filesystem (no docker cp). It only
# touches the PG cluster (via SKYGATE_DB_DSN). The flow is:
#   1. GET /api/v1/policy from headscale (unchanged)
#   2. parse updatedAt → epoch (unchanged)
#   3. INSERT into acl_snapshots via `docker run --rm postgres:18-alpine psql ...`
#      (the throwaway container joins headscale_default, sees the
#      postgres service by its docker name, runs the INSERT)
#   4. print the last 3 rows so the operator can eyeball
#      the alignment with the headscale updatedAt from step 1
echo "[1/3] GET current live policy from headscale + INSERT into acl_snapshots"
$SSH <<REMOTE_EOF
set -e
API_KEY=\$(grep '^HEADSCALE_API_KEY=' /home/admin/skygate/.env | cut -d= -f2-)
POLICY_JSON=\$(curl -fsS -H "Authorization: Bearer \$API_KEY" http://localhost:50444/api/v1/policy)
UPDATED_AT=\$(echo "\$POLICY_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin)['updatedAt'])")
POLICY_BODY=\$(echo "\$POLICY_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin)['policy'])")
echo "  updatedAt: \$UPDATED_AT"
# updatedAt is an ISO 8601 timestamp; PG accepts it directly as
# a TIMESTAMPTZ literal (the format 'YYYY-MM-DDTHH:MM:SSZ' parses
# cleanly). No epoch conversion needed — PG keeps the value as a
# real timestamp with timezone (was INTEGER unix epoch in SQLite).
echo "\$POLICY_BODY" > /tmp/_policy_recon.json

# Build the psql command via a throwaway postgres:18-alpine
# container. The DSN is the same SKYGATE_DB_DSN the live skygate
# uses; the throwaway container joins the same docker network so
# the docker service name `postgres` resolves (or, for HA setups,
# the DSN points at the external cluster's host:port).
DSN=\$(grep -E '^SKYGATE_DB_DSN=' /home/admin/skygate/.env | head -1 | cut -d= -f2-)
if [ -z "\$DSN" ]; then
  echo "ERROR: SKYGATE_DB_DSN is not set in /home/admin/skygate/.env" >&2
  exit 2
fi
DSN_PATH="\${DSN#postgres://}"
DSN_PATH="\${DSN_PATH%%\?*}"
PG_USER="\${DSN_PATH%%:*}"
DSN_REST="\${DSN_PATH#*:}"
PG_PASS="\${DSN_REST%%@*}"
DSN_REST="\${DSN_REST#*@}"
PG_HOST="\${DSN_REST%%:*}"
DSN_REST="\${DSN_REST#*:}"
PG_PORT="\${DSN_REST%%/*}"
PG_DB="\${DSN_REST#*/}"

# 2026-08-12: v1.3.1 — INSERT via a single psql invocation.
# We pass the policy JSON via stdin (heredoc on the docker
# exec) so we don't have to ship a multi-MB string through
# an env var or a CLI arg. The INSERT uses TIMESTAMPTZ literal
# (no epoch math).
docker run --rm -i \
  --network headscale_default \
  -e PGPASSWORD="\$PG_PASS" \
  postgres:18-alpine \
  psql -h "\$PG_HOST" -p "\$PG_PORT" -U "\$PG_USER" -d "\$PG_DB" \
       -tA -v ON_ERROR_STOP=1 <<SQL
WITH next_ver AS (
  SELECT COALESCE(MAX(version), 0) + 1 AS v FROM acl_snapshots
),
inserted AS (
  INSERT INTO acl_snapshots (version, config, created_by, applied_success, error_msg, created_at)
  SELECT v, pg_read_binary_file('/dev/stdin')::jsonb, '$CREATED_BY', 1, '', '$UPDATED_AT'::timestamptz
    FROM next_ver
  RETURNING id, version
)
SELECT 'inserted id=' || id || ' version=' || version FROM inserted;
SQL < /tmp/_policy_recon.json

# Print the last 3 rows so the operator can confirm alignment.
docker run --rm -i \
  --network headscale_default \
  -e PGPASSWORD="\$PG_PASS" \
  postgres:18-alpine \
  psql -h "\$PG_HOST" -p "\$PG_PORT" -U "\$PG_USER" -d "\$PG_DB" \
       -tA -v ON_ERROR_STOP=1 <<SQL
SELECT id, version,
       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') AS created,
       applied_success,
       substr(created_by, 1, 40) AS by
  FROM acl_snapshots
 ORDER BY id DESC LIMIT 3;
SQL

rm -f /tmp/_policy_recon.json
echo "[2/3] DB updated"
REMOTE_EOF

echo "[3/3] Done. Run 'make verify-post' to confirm R9 PASS"
