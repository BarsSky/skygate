#!/bin/bash
# scripts/recover_db_corruption.sh — recover from DB corruption.
#
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — REWRITTEN for PG.
# Pre-v1.3.1 this script was the canonical "rebuild a clean SQLite
# from a corrupt one" flow: it used `sqlite3 .recover` to extract
# salvageable rows from a corrupted file, rebuilt a clean file
# from the dump, and swapped it back into the skygate-data
# volume. The whole pattern was SQLite-specific — PG has its own
# crash-safety model and a different recovery story.
#
# PG crash-safety (why this is mostly a no-op for skygate):
#   - Postgres writes to the WAL BEFORE the btree page is touched
#     (write-ahead logging). On a crash, the cluster replays the
#     WAL on startup and rolls back any uncommitted transactions.
#   - The cluster's `fsync` model is much stricter than SQLite's.
#     If the WAL write fails (disk full, read-only FS), the
#     INSERT/UPDATE returns an error to the client; the btree
#     page is NEVER touched. There is no "btree pages left
#     inconsistent" failure mode in PG the way there was in
#     SQLite (the v0.32.5 incident was a SQLite-specific bug
#     caused by SQLite's silent WAL-write-failure mode).
#   - PG has `full_page_writes=on` by default, which means the
#     WAL contains a full copy of each touched page at the time
#     of the first modification after a checkpoint. This means
#     torn-page writes (kernel-level partial page writes) are
#     caught and recovered on startup, not silently propagated.
#
# So the only realistic failure modes for the v1.3.1+ skygate
# PG-backed deployment are:
#   1. The local postgres container is down (crash, OOM, disk
#      full, host reboot). skygate's connection pool will retry
#      until PG comes back. The fix is to restart the container
#      (or the host).
#   2. The disk is full and PG has flipped to read-only mode
#      (`pg_is_in_recovery=f` but the cluster refuses writes
#      with "ERROR: could not extend file ...: No space left
#      on device"). The fix is to free space, then `ALTER
#      SYSTEM RESET default_transaction_read_only; SELECT
#      pg_reload_conf();`.
#   3. The cluster is unrecoverable (corrupted data directory,
#      wrong permissions, etc.). The fix is to restore from a
#      pg_dump backup (see scripts/restore.sh — same archive
#      layout as backup.sh produces).
#
# This script handles (1) and (2) automatically. For (3), it
# prints the exact restore command and stops. We deliberately
# do NOT auto-restore: an unexpected restore is much worse than
# a paused service.
#
# Trigger: R30 FAIL in verify-post (DB integrity check) OR a
# healthz 503 with `db:fail` in /readyz.
#
# Usage: bash scripts/recover_db_corruption.sh
#
# Operator runbook (2026-08-12, v1.3.1):
#   1. df -h / on the VM. If >85%, free space first.
#   2. sudo docker system prune -a -f
#   3. sudo rm -rf /var/backups/skygate/PRE_VACUUM_*
#   4. bash scripts/recover_db_corruption.sh
#   5. bash scripts/verify_post_deploy.sh
#      If R30 still fails: scripts/restore.sh /path/to/latest
#      .tar.gz (the backup.sh output). The restore script
#      prompts before dropping the live cluster.

set -e

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"
SSH_KEY="${SSH_KEY:-}"
for cand in \
  "$HOME/.ssh/id_ed25519" \
  "$HOME/.ssh/id_rsa" \
  "/mnt/c/Users/knaga/.ssh/id_ed25519" \
  "/c/Users/knaga/.ssh/id_ed25519"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then SSH_KEY="$cand"; break; fi
done
if [ -z "$SSH_KEY" ]; then
  echo "ERROR: no SSH key found" >&2
  exit 2
fi
SSH="ssh -i $SSH_KEY -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes $SSH_HOST"

# 2026-08-12: v1.3.1 — removed the scp of _recover_helper.sh +
# _swap_recovered.sh. The .recover + rebuild pattern was
# SQLite-specific. PG recovery uses a different toolkit (below).
# The legacy helper files are still on disk in scripts/ for
# historical reference; they are NOT invoked by this script.
#
# 2026-08-12: v1.3.1 — ADMIN_USER/ADMIN_PASS are only used for
# the post-recovery /admin/exit-rules/reapply trigger. Read
# them from .env on the remote (don't print to the operator's
# workstation).
ADMIN_USER=$($SSH 'grep ^SKYGATE_ADMIN_USER= /home/admin/skygate/.env | cut -d= -f2-')
ADMIN_PASS=$($SSH 'grep ^SKYGATE_ADMIN_PASS= /home/admin/skygate/.env | cut -d= -f2-')
ADMIN_USER="${ADMIN_USER:-admin}"

# 2026-08-12: v1.3.1 — read DSN from .env (same parser as
# backup.sh and check_subnet_router.sh). The script needs
# host/port/user/db/password to do live queries + restart
# the cluster.
DSN=$($SSH 'grep -E "^SKYGATE_DB_DSN=" /home/admin/skygate/.env | head -1 | cut -d= -f2-')
if [ -z "$DSN" ]; then
  echo "ERROR: SKYGATE_DB_DSN is not set in /home/admin/skygate/.env" >&2
  exit 2
fi
DSN_PATH="${DSN#postgres://}"
DSN_PATH="${DSN_PATH%%\?*}"
PG_USER="${DSN_PATH%%:*}"
DSN_REST="${DSN_PATH#*:}"
PG_PASS="${DSN_REST%%@*}"
DSN_REST="${DSN_REST#*@}"
PG_HOST="${DSN_REST%%:*}"
DSN_REST="${DSN_REST#*:}"
PG_PORT="${DSN_REST%%/*}"
PG_DB="${DSN_REST#*/}"

$SSH "
set -e

echo '=== 1. Disk space check (the real cause, usually) ==='
df -h / 2>&1
DF_PCT=\$(df -P / | tail -1 | awk '{print \$5}' | tr -d '%')
if [ -n \"\$DF_PCT\" ] && [ \"\$DF_PCT\" -ge 85 ]; then
    echo
    echo '  WARNING: disk is '\${DF_PCT}'% full. PG is likely in read-only mode.'
    echo '  Free space first:'
    echo '    sudo docker system prune -a -f'
    echo '    sudo rm -rf /var/backups/skygate/PRE_VACUUM_*'
    echo
    read -p '  Press enter to continue anyway (NOT recommended)...'
fi

echo
echo '=== 2. PG container status ==='
# 2026-08-12: v1.3.1 — the local PG container name follows the
# docker compose convention <project>-<service>-1. For the
# default skygate compose the container is 'skygate-postgres-1'.
# For HA setups the cluster is external (svyatoslava Patroni),
# in which case this step is informational and step 4 runs
# against the external host. The script tries both.
if sudo docker ps --format '{{.Names}}' 2>/dev/null | grep -E 'skygate-postgres|postgres' | head -3; then
  PG_CONTAINER=\$(sudo docker ps --filter 'label=com.docker.compose.service=postgres' --format '{{.Names}}' | head -1)
  echo \"  PG container: \${PG_CONTAINER:-none (external cluster assumed)}\"
fi

echo
echo '=== 3. PG health + WAL state ==='
# Run pg_isready first (the cheapest check). Then probe the
# cluster with a SELECT 1 via throwaway postgres:15-alpine
# (matches the verify_post_deploy pattern).
PG_OK=no
if sudo docker exec skygate-postgres-1 pg_isready -U ${PG_USER} -d ${PG_DB} 2>/dev/null | grep -q 'accepting connections'; then
  PG_OK=yes
fi
if [ \"\$PG_OK\" = \"no\" ]; then
  # Try via the docker service name on the skygate-net bridge.
  if sudo docker run --rm --network headscale_default -e PGPASSWORD=${PG_PASS} postgres:15-alpine \
       psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_USER} -d ${PG_DB} -tA -c 'SELECT 1' 2>/dev/null | grep -q '^1\$'; then
    PG_OK=yes
  fi
fi
echo \"  PG accepting connections: \${PG_OK}\"

if [ \"\$PG_OK\" = \"yes\" ]; then
  echo
  echo '=== 4a. Check for read-only mode (disk-full recovery) ==='
  RO=\$(sudo docker run --rm --network headscale_default -e PGPASSWORD=${PG_PASS} postgres:15-alpine \
        psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_USER} -d ${PG_DB} -tA -c \
        'SELECT setting FROM pg_settings WHERE name = \"default_transaction_read_only\";' 2>/dev/null | tr -d '[:space:]')
  if [ \"\$RO\" = 'on' ]; then
    echo '  PG is in read-only mode (disk was full). Flipping back to read-write...'
    sudo docker run --rm --network headscale_default -e PGPASSWORD=${PG_PASS} postgres:15-alpine \\
        psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_USER} -d ${PG_DB} -c \\
        'ALTER SYSTEM RESET default_transaction_read_only; SELECT pg_reload_conf();' 2>&1 | tail -3
  else
    echo '  PG is in read-write mode (no read-only flip detected).'
  fi
  echo
  echo '=== 4b. Integrity-equivalent checks ==='
  # PG has no PRAGMA equivalent. The cluster-level equivalents are:
  #   - count of public tables (≥20 after v1.3.0 migrations)
  #   - presence of the 4 critical tables
  #   - audit_log recent count (sanity)
  sudo docker run --rm --network headscale_default -e PGPASSWORD=${PG_PASS} postgres:15-alpine \\
      psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_USER} -d ${PG_DB} -tA -c \\
      'SELECT \"public_tables=\" || count(*) FROM pg_tables WHERE schemaname='\\''public'\\'';' 2>&1
  for t in portal_users device_rules acl_snapshots audit_log; do
    EX=\$(sudo docker run --rm --network headscale_default -e PGPASSWORD=${PG_PASS} postgres:15-alpine \\
        psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_USER} -d ${PG_DB} -tA -c \\
        \"SELECT to_regclass('public.\${t}') IS NOT NULL\" 2>/dev/null | tr -d '[:space:]')
    echo \"  table \${t}: \${EX}\"
  done
  echo
  echo '=== 4c. Audit log: last 3 entries ==='
  sudo docker run --rm --network headscale_default -e PGPASSWORD=${PG_PASS} postgres:15-alpine \\
      psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_USER} -d ${PG_DB} -tA -c \\
      'SELECT id, to_char(created_at AT TIME ZONE '\\''UTC'\\'', '\\''YYYY-MM-DD HH24:MI:SS'\\''), action FROM audit_log ORDER BY id DESC LIMIT 3;' 2>&1
else
  echo
  echo '=== 4d. PG NOT accepting connections. Diagnose and recover. ==='
  # 2026-08-12: v1.3.1 — try a container restart first (covers
  # the common case of the container being up but in a stuck
  # state from a previous unclean shutdown).
  if sudo docker ps -a --format '{{.Names}}' 2>/dev/null | grep -q '^skygate-postgres-1\$'; then
    echo '  Restarting skygate-postgres-1...'
    sudo docker restart skygate-postgres-1 2>&1 | tail -1
    sleep 5
    if sudo docker exec skygate-postgres-1 pg_isready -U ${PG_USER} -d ${PG_DB} 2>/dev/null | grep -q 'accepting connections'; then
      echo '  PG accepting connections after restart ✓'
    else
      echo '  PG still not responding. Likely a deeper issue (data dir corruption).'
      echo
      echo '  === 5. Restore from latest backup ==='
      echo '  This is a destructive operation. The live cluster will be DROPPED'
      echo '  and the backup replayed. Do this only after backing up the current'
      echo '  state with:'
      echo '    sudo docker run --rm -v skygate-pg-data:/data -v /var/backups/skygate:/backup alpine:3.20 sh -c \"cp -a /data /backup/CRASH_\$(date +%Y%m%d_%H%M%S)\"'
      echo
      echo '  Then run:'
      echo '    bash scripts/restore.sh /var/backups/skygate/skygate-full-LATEST.tar.gz'
      echo
      echo '  See docs/disaster-recovery.md#postgresql-restore for the full runbook.'
    fi
  else
    echo '  skygate-postgres-1 container not found. Is the local-pg profile enabled?'
    echo '  Check: docker compose --profile local-pg ps'
    echo '  If using an external PG (svyatoslava Patroni), check the cluster health on the remote host.'
  fi
fi
"

echo
echo "=== 6. Wait for skygate /healthz (if skygate is up) ==="
for i in $(seq 1 60); do
    if $SSH 'curl -fsS http://localhost:8080/healthz >/dev/null 2>&1'; then
        echo "  healthy after ${i}x5s"
        break
    fi
    sleep 5
done

echo
echo "=== 7. Login + trigger reapply (rebuilds acl_snapshots) ==="
COOKIE="/tmp/_recover_ck.txt"
$SSH "rm -f $COOKIE
curl -s -c $COOKIE -X POST http://localhost:8080/login \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d 'username=$ADMIN_USER&password=$ADMIN_PASS' -o /dev/null -w 'login %{http_code}\n'
curl -s -b $COOKIE -X POST http://localhost:8080/admin/exit-rules/reapply -o /dev/null -w 'reapply %{http_code}\n'
rm -f $COOKIE"

echo
echo "=== Done. Run verify-post. ==="
echo "  bash scripts/verify_post_deploy.sh"
echo "  Expected: R30 PASS, R31 PASS, all other checks as before."
echo
echo "  If R30 still fails: scripts/restore.sh /path/to/latest.tar.gz"
echo "  (restores from a pg_dump backup — the SQLite-era .recover"
echo "  flow is gone because PG's crash-safety prevents that class"
echo "  of corruption.)"
