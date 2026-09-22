#!/usr/bin/env bash
. "$(dirname "$0")/lib/db_credentials.sh"
SKYGATE_DB_PASSWORD="${SKYGATE_DB_PASSWORD:-$(skygate_db_password)}"
# launch_skigate.sh — start skygate-skygate-1 with the correct backend.
#
# 2026-09-11 (Issue follow-up): the recovery session (v1.5.4 incident)
# surfaced 3 hard-won lessons about launching skygate:
#
#   1. ALWAYS pass --env-file <file>, NOT individual -e flags. The .env
#      has 40+ vars; combining --env-file with -e flags can silently
#      drop unrelated keys (HEADSCALE_API_KEY showed empty in the
#      container despite being set in .env). The fix is to patch
#      SKYGATE_DB / SKYGATE_DB_DSN via sed in a temp copy of .env
#      and pass THAT.
#
#   2. NEVER bind-mount /home/skyadmin/skygate/data into the
#      container — the named volume `skygate-data` is the source of
#      truth (legacy from pre-v1.3.0). The recovery on 2026-09-11
#      lost data because bind-mount + docker run created an empty
#      /data/skygate.db (the volume kept its real data but the
#      container never saw it). The contract now is:
#        - skygate-data volume → /data (always)
#        - host bind-mount → /data/skygate.db (NEVER; for inspection
#          only via `docker run -v skygate-data:/data alpine ...`)
#
#   3. Always launch with `--cap-add=NET_ADMIN --cap-add=SYS_ADMIN`
#      for tailscale. Skipping these breaks the Tailscale integration
#      silently (no error, just no peers visible).
#
# Backend mode (PG vs SQLite) is selected by SKYGATE_DB in the .env
# (or by the --sqlite flag below which forces SKYGATE_DB=/data/skygate.db
# from the named volume). PG mode requires a postgres container on
# the headscale_default network (named `skygate-pg-local` by default);
# SQLite mode uses skygate-data:/data/skygate.db.
#
# Usage:
#   bash scripts/launch_skigate.sh                     # auto-detect from .env
#   bash scripts/launch_skigate.sh --sqlite            # force SQLite mode
#   bash scripts/launch_skigate.sh --pg-host 172.18.0.3 # force PG with explicit host
#   bash scripts/launch_skigate.sh --rebuild          # pull latest image + restart
#
# Companion scripts:
#   - scripts/check_b_prod_health.sh      — pre-deploy health gate
#   - scripts/check_b_backend_mode.sh     — backend detection (PG vs SQLite)
#   - scripts/migrate_pg_to_sqlite.sh      — convert PG → SQLite (TODO)
#   - scripts/migrate_sqlite_to_pg.sh      — convert SQLite → PG (TODO)
#   - skygate db-migrate                  — built-in migration (v1.5.4+)

set -euo pipefail

# ── Args ──
FORCE_SQLITE=0
FORCE_PG_HOST=""
REBUILD=0
for arg in "$@"; do
    case "$arg" in
        --sqlite)   FORCE_SQLITE=1 ;;
        --pg-host=*) FORCE_PG_HOST="${arg#--pg-host=}" ;;
        --pg-host)  FORCE_PG_HOST="${2:-}"; shift ;;
        --rebuild)  REBUILD=1 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

ENV_FILE="/home/skyadmin/skygate/.env"
CONTAINER_NAME="skygate-skygate-1"
IMAGE_NAME="skygate-skygate"
PG_CONTAINER_NAME="skygate-pg-local"

# ── 1. detect current backend ──
CURRENT_DB=$(grep -E '^SKYGATE_DB=' "$ENV_FILE" | head -1 | cut -d= -f2-)
echo "current SKYGATE_DB=$CURRENT_DB"

if [ "$FORCE_SQLITE" = "1" ]; then
    TARGET_BACKEND="sqlite"
elif [ -n "$FORCE_PG_HOST" ]; then
    TARGET_BACKEND="pg"
elif [[ "$CURRENT_DB" == postgres:* ]]; then
    TARGET_BACKEND="pg"
    PG_HOST_FROM_DSN=$(echo "$CURRENT_DB" | sed -E 's|^postgres://[^@]+@([^:/]+):.*|\1|')
else
    TARGET_BACKEND="sqlite"
fi

echo "target backend: $TARGET_BACKEND"

# ── 2. ensure PG container is running (if target=pg) ──
if [ "$TARGET_BACKEND" = "pg" ]; then
    # Determine PG host: explicit override, or use the container NAME (NOT its IP).
    #
    # 2026-09-22 (B-fix-dsn-ip-rotates): the previous code captured
    # `docker inspect ... .IPAddress` and baked it into SKYGATE_DB / SKYGATE_DB_DSN.
    # Postgres container IPs on the headscale_default bridge are NOT stable — every
    # recreate (image upgrade, `docker run` after a VM reboot, even a healthy
    # stop+start cycle on some Docker versions) hands out a fresh IP from
    # 172.18.0.0/16. The stale DSN then crashed the skygate container on every
    # restart with "DB pre-flight: <old-ip>:5432 UNREACHABLE", the
    # `--restart=on-failure:5` policy below exhausted its 5 retries after a few
    # minutes, and skygate stopped permanently. Downstream headscale then
    # crash-looped on "creating OIDC provider from issuer config: 502 Bad Gateway"
    # because its OIDC discovery target (skygate) was down.
    #
    # Using the container NAME instead of the IP lets docker's embedded DNS
    # (127.0.0.11:53) resolve `skygate-pg-local` to whatever IP the container
    # currently has. The DSN is then stable across postgres recreates.
    # Explicit `--pg-host=<ip>` still wins as an escape hatch for operators
    # who deliberately run postgres on a non-default host (e.g. an external
    # Patroni cluster at deploy/pg-ha/).
    if [ -z "$FORCE_PG_HOST" ]; then
        if ! sudo docker inspect "$PG_CONTAINER_NAME" >/dev/null 2>&1; then
            echo "ERROR: target=pg but $PG_CONTAINER_NAME container is not running." >&2
            echo "  hint: docker run -d --name $PG_CONTAINER_NAME --network headscale_default \\" >&2
            echo "         -e POSTGRES_USER=admin -e POSTGRES_PASSWORD=${SKYGATE_DB_PASSWORD} \\" >&2
            echo "         -e POSTGRES_DB=skygate_staging postgres:18-alpine" >&2
            exit 1
        fi
        PG_HOST="$PG_CONTAINER_NAME"
    else
        PG_HOST="$FORCE_PG_HOST"
    fi
    echo "PG host: $PG_HOST"
fi

# ── 3. build patched env file (sed SKYGATE_DB / SKYGATE_DB_DSN) ──
PATCHED_ENV=$(mktemp)
trap "rm -f $PATCHED_ENV" EXIT
cp "$ENV_FILE" "$PATCHED_ENV"
case "$TARGET_BACKEND" in
    pg)
        DSN="postgres://admin:${SKYGATE_DB_PASSWORD}@${PG_HOST}:5432/skygate_staging?sslmode=disable"
        sed -i "s|^SKYGATE_DB=.*|SKYGATE_DB=${DSN}|" "$PATCHED_ENV"
        sed -i "s|^SKYGATE_DB_DSN=.*|SKYGATE_DB_DSN=${DSN}|" "$PATCHED_ENV"
        ;;
    sqlite)
        # Restore the default SQLite path. The named volume
        # `skygate-data` is the source of truth.
        sed -i "s|^SKYGATE_DB=.*|SKYGATE_DB=sqlite:/data/skygate.db|" "$PATCHED_ENV"
        sed -i "s|^SKYGATE_DB_DSN=.*|SKYGATE_DB_DSN=|" "$PATCHED_ENV"
        ;;
esac

# ── 4. stop existing skygate ──
echo "stop existing $CONTAINER_NAME (if any)..."
sudo docker stop "$CONTAINER_NAME" 2>/dev/null || true
sudo docker rm "$CONTAINER_NAME" 2>/dev/null || true

# ── 5. optionally rebuild image ──
if [ "$REBUILD" = "1" ]; then
    echo "rebuilding $IMAGE_NAME..."
    sudo docker build -t "$IMAGE_NAME" /home/skyadmin/skygate
fi

# ── 6. launch ──
echo "launching $CONTAINER_NAME on $TARGET_BACKEND..."
sudo docker run -d \
    --name "$CONTAINER_NAME" \
    --hostname skygate \
    --network headscale_default \
    -p 8080:8080 \
    --restart=unless-stopped \
    --cap-add=NET_ADMIN \
    --cap-add=SYS_ADMIN \
    --env-file "$PATCHED_ENV" \
    -v /home/skyadmin/skygate/.ssh:/host-keys:ro \
    -v /var/run/docker.sock:/var/run/docker.sock:rw \
    -v skygate-data:/data \
    -v /home/skyadmin/skygate/deploy/headscale-users/headscale-bootstrap.sh:/usr/local/bin/headscale-bootstrap.sh:ro \
    -v /home/skyadmin/headscale:/home/skyadmin/headscale:rw \
    -v /home/skyadmin/skygate/data/ssh-sync:/ssh-sync:ro \
    -v /home/skyadmin/skygate/.ssh:/etc/skygate/ssh_key:ro \
    -v /home/skyadmin/skygate/:/app:rw \
    -v /home/skyadmin/skygate/deploy/headscale-users/headscale-deprovision.sh:/usr/local/bin/headscale-deprovision.sh:ro \
    -v /home/admin/headscale:/home/admin/headscale:rw \
    "$IMAGE_NAME" 2>&1

# ── 7. wait + verify ──
echo "waiting 30s for boot..."
sleep 30
echo ""
echo "=== /healthz ==="
curl -s -m 5 http://127.0.0.1:8080/healthz
echo ""
echo ""
echo "=== container status ==="
sudo docker ps --format "table {{.Names}}\t{{.Status}}" | grep -E "skygate|headscale|headplane"
echo ""
echo "=== backend ==="
case "$TARGET_BACKEND" in
    pg)
        echo "  PG: docker exec skygate-pg-local psql -U admin -d skygate_staging -c 'SELECT count(*) FROM portal_users'"
        sudo docker exec skygate-pg-local psql -U admin -d skygate_staging -c "SELECT count(*) AS portal_users FROM portal_users; SELECT count(*) AS device_rules FROM device_rules" 2>&1 | head -10
        ;;
    sqlite)
        echo "  SQLite: docker run --rm -v skygate-data:/data alpine sqlite3 /data/skygate.db 'SELECT count(*) FROM portal_users'"
        sudo docker run --rm -v skygate-data:/data alpine sh -c "apk add --no-cache sqlite >/dev/null 2>&1; sqlite3 /data/skygate.db 'SELECT count(*) AS portal_users FROM portal_users; SELECT count(*) AS device_rules FROM device_rules'" 2>&1 | head -10
        ;;
esac
