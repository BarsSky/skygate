#!/usr/bin/env bash
# check_b_backend_mode.sh — detect whether skygate is using PG or SQLite.
#
# 2026-09-11 (Issue follow-up): the recovery session surfaced that
# skygate can run in TWO backends, and operators need a one-glance
# way to tell which one is active. This script reads the live
# SKYGATE_DB env var inside the skygate container (not the host
# .env, since the env may be patched at launch time) and reports:
#   - backend: pg | sqlite | unknown
#   - target: postgres URL or sqlite path
#   - rows: live row count from the active backend
#   - data freshness: most recent audit_log timestamp
#
# Usage:
#   bash scripts/check_b_backend_mode.sh                  # default container
#   SKYGATE_CONTAINER=myskygate bash scripts/check_b_backend_mode.sh
#
# Exit codes:
#   0 = backend detected and reachable
#   1 = container not running
#   2 = backend is "unknown" (not PG or SQLite — investigate)

set -uo pipefail

CONTAINER="${SKYGATE_CONTAINER:-skygate-skygate-1}"
PG_CONTAINER="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"
VOLUME="${SKYGATE_DATA_VOLUME:-skygate-data}"

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

# ── 1. container reachable ──
# B281 (2026-09-22): no container = absent live dependency = SKIP (AGENTS §1.1),
# not FAIL. This script used to exit 1 on a CI runner that has no stack at all.
if ! sudo -n docker inspect "$CONTAINER" >/dev/null 2>&1; then
    echo "  SKIP  $CONTAINER container not running (live check — run it on the skygate host)"
    exit 0
fi
state=$(sudo -n docker inspect "$CONTAINER" --format '{{.State.Status}}' 2>&1)
if [ "$state" != "running" ]; then
    echo "  SKIP  $CONTAINER state=$state (live check — run it on the skygate host)"
    exit 0
fi
ok "$CONTAINER is running"

# ── 2. read live SKYGATE_DB from the container's env ──
LIVE_DB=$(sudo -n docker exec "$CONTAINER" sh -c 'env | grep "^SKYGATE_DB=" | head -1 | cut -d= -f2-' 2>&1)
if [ -z "$LIVE_DB" ]; then
    bad "SKYGATE_DB is empty in container env (v1.3.0+ refuses to start)"
fi

# ── 3. classify ──
BACKEND="unknown"
case "$LIVE_DB" in
    postgres://*|postgresql://*)
        BACKEND="pg"
        ;;
    sqlite:*|sqlite3:*|file:*|""|"/data/skygate.db"|"/var/lib/skygate/skygate.db")
        # bare path or sqlite: scheme or file: → SQLite
        BACKEND="sqlite"
        ;;
    /data/skygate.db|sqlite:*|file:*)
        BACKEND="sqlite"
        ;;
    *)
        BACKEND="unknown"
        ;;
esac
ok "backend detected: $BACKEND  (SKYGATE_DB=${LIVE_DB})"

# ── 4. report rows + freshness ──
echo ""
case "$BACKEND" in
    pg)
        if ! sudo -n docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
            bad "$PG_CONTAINER not running (skygate points at PG but the PG container is down)"
        fi
        ok "$PG_CONTAINER running"
        echo ""
        echo "=== PG row counts ==="
        sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -c "SELECT 'portal_users', count(*) FROM portal_users UNION ALL SELECT 'device_rules', count(*) FROM device_rules UNION ALL SELECT 'acl_snapshots', count(*) FROM acl_snapshots UNION ALL SELECT 'audit_log', count(*) FROM audit_log" 2>&1 | head -10
        echo ""
        echo "=== PG latest audit_log ==="
        sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -c "SELECT action, to_timestamp(created_at) as t FROM audit_log ORDER BY created_at DESC LIMIT 3" 2>&1 | head -10
        ;;
    sqlite)
        echo "=== SQLite file via named volume $VOLUME ==="
        sudo -n docker run --rm -v "$VOLUME":/data alpine sh -c '
            apk add --no-cache sqlite >/dev/null 2>&1
            sqlite3 /data/skygate.db "SELECT '"'"'portal_users'"'"', count(*) FROM portal_users UNION ALL SELECT '"'"'device_rules'"'"', count(*) FROM device_rules UNION ALL SELECT '"'"'acl_snapshots'"'"', count(*) FROM acl_snapshots UNION ALL SELECT '"'"'audit_log'"'"', count(*) FROM audit_log"
        ' 2>&1 | head -10
        echo ""
        echo "=== SQLite latest audit_log ==="
        sudo -n docker run --rm -v "$VOLUME":/data alpine sh -c '
            apk add --no-cache sqlite >/dev/null 2>&1
            sqlite3 /data/skygate.db "SELECT action, datetime(created_at, '"'"'unixepoch'"'"') as t FROM audit_log ORDER BY created_at DESC LIMIT 3"
        ' 2>&1 | head -10
        ;;
    unknown)
        bad "SKYGATE_DB=$LIVE_DB is neither PG nor SQLite (v1.5.4+ supports both)"
        ;;
esac
