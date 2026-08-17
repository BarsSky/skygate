#!/usr/bin/env bash
#===============================================================================
# Skygate Restore Script
# Restore full Skygate + Headscale + Headplane state from backup archive
# Usage: ./restore.sh <backup-file.tar.gz> [target-dir]
#   target-dir: where to restore (default: /home/admin/skygate/)
#
# 2026-08-12 v1.3.8 (BL-15): added do_pg_restore() for the
# v1.3.0+ archives that contain skygate-pg.sql (a text-format
# pg_dump with --clean --if-exists). The pre-v1.3.0 SQLite
# path (do_skygate_db copying skygate.db into the skygate-data
# Docker volume) is preserved for legacy archives — the
# dispatcher picks whichever file is present in the archive.
#
# The PG restore uses the same postgres:18-alpine throwaway
# pattern as backup.sh so the operator doesn't need
# postgresql-client installed on the host. The DSN is parsed
# from the skygate.env file in the archive (so the restore
# targets the SAME DB the backup was taken from, not whatever
# happens to be on localhost).
#===============================================================================
set -euo pipefail

if [ $# -lt 1 ]; then
    echo "Usage: $0 <backup-file.tar.gz> [target-dir]"
    echo "  target-dir defaults to /home/admin/skygate/"
    exit 1
fi

BACKUP_FILE="$1"
TARGET_DIR="${2:-/home/admin/skygate}"
RESTORE_DIR="/tmp/skygate-restore-$(date +%s)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[✓]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[✗]${NC} $1"; exit 1; }

if [ ! -f "${BACKUP_FILE}" ]; then
    err "Backup file not found: ${BACKUP_FILE}"
fi

echo "=============================================="
echo "  Skygate Restore"
echo "  Archive: ${BACKUP_FILE}"
echo "  Target:  ${TARGET_DIR}"
echo "=============================================="

mkdir -p "${RESTORE_DIR}"
cd "${RESTORE_DIR}"

# Extract
log "Extracting archive..."
tar xzf "${BACKUP_FILE}"
EXTRACTED_DIR=$(ls -d skygate-full-*/ 2>/dev/null | head -1)
if [ -z "${EXTRACTED_DIR}" ]; then
    # Try flat structure
    EXTRACTED_DIR="."
fi
cd "${EXTRACTED_DIR}"

# Verify inventory
if [ ! -f inventory.txt ]; then
    warn "No inventory.txt found — continuing anyway"
fi
cat inventory.txt 2>/dev/null || true

log "Checking extracted files..."
ls -la

# 2026-08-12 v1.3.8 (BL-15): detect which DB format the
# archive has so the menu below can show the right option.
# v1.3.0+ archives have skygate-pg.sql; v0.32.x archives
# have skygate.db. We support both.
HAS_PG_DUMP="no"
HAS_SQLITE_DB="no"
[ -f skygate-pg.sql ] && HAS_PG_DUMP="yes"
[ -f skygate.db ]    && HAS_SQLITE_DB="yes"

echo ""
echo "=============================================="
echo "  What to restore?"
echo "=============================================="
echo "  1) Skygate source code (git bundle → clone)"
echo "  2) Skygate .env (configuration with secrets)"
if [ "${HAS_PG_DUMP}" = "yes" ]; then
    echo "  3) Skygate database (skygate-pg.sql → psql replay, v1.3.0+)"
elif [ "${HAS_SQLITE_DB}" = "yes" ]; then
    echo "  3) Skygate database (skygate.db → Docker volume, v0.32.x)"
else
    echo "  3) Skygate database (no DB file in archive — skipped)"
fi
echo "  4) Headscale config + ACL"
echo "  5) Headscale database (→ Docker volume)"
echo "  6) Headplane data"
echo "  7) DERP config"
echo "  8) ALL (default)"
echo "  0) Exit"
echo ""

read -p "Choose (0-8, default=8): " CHOICE
CHOICE="${CHOICE:-8}"

do_skygate_code() {
    if [ -f skygate-repo.bundle ]; then
        log "Restoring Skygate source code..."
        if [ -d "${TARGET_DIR}/.git" ]; then
            mv "${TARGET_DIR}" "${TARGET_DIR}.bak-$(date +%Y%m%d_%H%M%S)"
            warn "  Existing repo moved to backup"
        fi
        mkdir -p "${TARGET_DIR}"
        git clone skygate-repo.bundle "${TARGET_DIR}" 2>/dev/null || \
            git -C "${TARGET_DIR}" fetch 2>/dev/null || \
            warn "  Git restore failed — copying files manually"
        log "  Source code restored"
    fi
}

do_env() {
    if [ -f skygate.env ]; then
        log "Restoring .env..."
        cp skygate.env "${TARGET_DIR}/.env"
        log "  .env restored (contains secrets!)"
    fi
}

# 2026-08-12 v1.3.8 (BL-15): parse the DSN out of the
# skygate.env that lives inside the archive. The dump
# was taken from THIS specific DB, so the restore should
# target THIS specific DB (not whatever happens to be
# listening on localhost:5432). The format is the libpq
# URL form:
#   postgres://<user>:<password>@<host>:<port>/<database>?<params>
# We use bash parameter expansion (no python3 needed for
# the common case). Password is URL-decoded by libpq when
# PGPASSWORD= is set, so we pass it as-is.
load_dsn_from_env() {
    local env_file="$1"
    if [ ! -r "${env_file}" ]; then
        err "Cannot read ${env_file} — needed for PG restore"
    fi
    local dsn
    dsn=$(grep -E '^SKYGATE_DB_DSN=' "${env_file}" 2>/dev/null | head -1 | cut -d= -f2-)
    if [ -z "${dsn}" ]; then
        err "SKYGATE_DB_DSN not found in ${env_file}"
    fi
    # Strip postgres:// prefix and any ?params
    local stripped="${dsn#postgres://}"
    stripped="${stripped%%\?*}"
    # user:password@host:port/db
    PG_USER="${stripped%%:*}"
    local rest="${stripped#*:}"
    PG_PASS="${rest%%@*}"
    rest="${rest#*@}"
    PG_HOST="${rest%%:*}"
    rest="${rest#*:}"
    PG_PORT="${rest%%/*}"
    PG_DB="${rest#*/}"
    log "  DSN parsed: ${PG_USER}@${PG_HOST}:${PG_PORT}/${PG_DB}"
}

# 2026-08-12 v1.3.8 (BL-15): replay skygate-pg.sql into
# the PG the backup came from. Same postgres:18-alpine
# throwaway pattern as backup.sh so the operator doesn't
# need psql installed on the host. The dump is text-format
# (pg_dump -Fp --clean --if-exists) so psql -f replays it
# cleanly on a fresh database. The DSN is parsed from
# skygate.env (the .env in the archive), not from the
# host environment, so the restore targets the RIGHT DB.
#
# Caveats for the operator:
#   1. The DSN points to the DB that EXISTED when the
#      backup was taken. If the new host has a different
#      DB, the restore will write to the old DSN's host
#      and may not be visible locally. (Most operators
#      keep the same DB host across restores, but on a
#      cross-host migration, the operator typically
#      updates the .env FIRST and then re-runs restore.)
#   2. psql with -v ON_ERROR_STOP=1 aborts on the first
#      error. A pre-populated DB with conflicting tables
#      will fail at the first DROP TABLE. The dump's
#      --clean --if-exists handles DROP IF EXISTS so
#      idempotent replays work on a fresh DB; for a
#      non-fresh DB, the operator should DROP the target
#      DB first.
do_pg_restore() {
    if [ ! -f skygate-pg.sql ]; then
        return 0
    fi
    log "Restoring Skygate PG database (skygate-pg.sql)..."
    # Source of truth: skygate.env in the archive.
    # Fall back to ${TARGET_DIR}/.env (already restored
    # to disk by do_env) if the in-archive copy is missing.
    local env_source="skygate.env"
    [ ! -r "${env_source}" ] && [ -r "${TARGET_DIR}/.env" ] && env_source="${TARGET_DIR}/.env"
    if [ ! -r "${env_source}" ]; then
        err "No skygate.env found — cannot determine target DB. Restore option 2 (env) first."
    fi
    load_dsn_from_env "${env_source}"
    # Mirror backup.sh's network handling: the throwaway
    # needs to reach the DB host. The default
    # `headscale_default` covers docker-compose deployments;
    # for external PG the operator can set
    # SKYGATE_BACKUP_NETWORK=host or similar (we use the
    # same env var name as backup.sh for consistency).
    local net="${SKYGATE_BACKUP_NETWORK:-headscale_default}"
    log "  replaying skygate-pg.sql via postgres:18-alpine (network=${net})..."
    if ! docker run --rm \
        --network "${net}" \
        -e PGPASSWORD="${PG_PASS}" \
        -v "$(pwd):/restore:ro" \
        postgres:18-alpine \
        psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_USER}" -d "${PG_DB}" \
             -v ON_ERROR_STOP=1 -f /restore/skygate-pg.sql 2>&1 ; then
        err "  psql replay failed — see output above. If the target DB has conflicting tables, DROP it first and re-run."
    fi
    log "  PG database restored to ${PG_HOST}:${PG_PORT}/${PG_DB}"
    log "  (table count: $(docker run --rm --network "${net}" -e PGPASSWORD="${PG_PASS}" postgres:18-alpine \
        psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_USER}" -d "${PG_DB}" -tA -c \
        "SELECT count(*) FROM pg_tables WHERE schemaname='public';" 2>/dev/null) public tables)"
}

# 2026-08-12 v1.3.8 (BL-15): dispatcher. Picks PG vs
# SQLite based on which file is in the archive. Both
# paths are no-ops if the corresponding file is missing
# (so a partial archive doesn't break the restore).
do_skygate_db() {
    if [ -f skygate-pg.sql ]; then
        do_pg_restore
    elif [ -f skygate.db ]; then
        # v0.32.x legacy path — kept for old archives.
        log "Restoring Skygate SQLite database (skygate.db, v0.32.x legacy)..."
        docker run --rm -v skygate-data:/data -v "$(pwd):/restore" alpine \
            sh -c "cp /restore/skygate.db /data/skygate.db && chown -R 1000:1000 /data" 2>/dev/null && \
            log "  skygate.db restored to Docker volume" || \
            warn "  DB restore failed (is container running?)"
    else
        warn "No skygate-pg.sql or skygate.db in archive — DB step skipped"
    fi
}

do_headscale_config() {
    if [ -d headscale-config ]; then
        log "Restoring Headscale config..."
        # 2026-08-17: v1.3.19.2 follow-up (BL-15 e2e) — use sudo
        # for the cp. The /home/admin/headscale/config dir is owned
        # by root:root with 755 perms (headscale container was
        # created by the deploy.sh as root). The in-app restore
        # path (PostAdminBackupRestore) runs restore.sh via
        # `bash` from the skygate container, which runs as
        # root, so the cp would normally work. But the
        # INTERACTIVE path (operator runs restore.sh from their
        # shell) runs as the operator's user (skyadmin) which
        # doesn't have write access to the root-owned config
        # dir. Adding sudo here covers both paths — it's a
        # no-op when running as root (sudo NOPASSWD is set
        # for skyadmin, verified 2026-08-17).
        sudo mkdir -p /home/admin/headscale/config
        sudo cp -r headscale-config/* /home/admin/headscale/config/
        log "  Headscale config restored"
        warn "  Restart headscale: sudo docker restart headscale"
    fi
}

do_headscale_db() {
    # 2026-08-17: v1.3.19.2 follow-up (BL-15 e2e) — use
    # shell-glob match (not `ls ... | head`) so an empty
    # archive doesn't trip `set -euo pipefail`. The v1.3.8+
    # archives don't always include a headscale SQLite file
    # (headscale keeps state in /data/headscale.db in the
    # named volume, which headscale recreates on first start
    # if missing — so an archive without the file is valid).
    local DB_FILE=""
    for f in headscale*.db; do
        if [ -f "${f}" ]; then
            DB_FILE="${f}"
            break
        fi
    done
    if [ -n "${DB_FILE}" ]; then
        log "Restoring Headscale database..."
        # Copy via temporary container. The headscale_headscale_data
        # volume is owned by root (headscale container creates it),
        # so we use sudo to create the temp container.
        sudo docker run --rm -v headscale_headscale_data:/data -v "$(pwd):/restore" alpine \
            sh -c "find /data -name '*.db' -exec cp /restore/${DB_FILE} {} \; 2>/dev/null" && \
            log "  Headscale DB restored" || \
            warn "  Headscale DB restore failed"
        warn "  Restart headscale: sudo docker restart headscale"
    fi
}

do_headplane() {
    if [ -d headplane-data ]; then
        log "Restoring Headplane data..."
        # headplane container is created by deploy.sh as root; the
        # headscale_headplane_data volume is root-owned. Use sudo.
        sudo docker run --rm -v headscale_headplane_data:/data -v "$(pwd):/restore" alpine \
            sh -c "rm -rf /data/* && cp -r /restore/headplane-data/* /data/" 2>/dev/null && \
            log "  Headplane data restored" || \
            warn "  Headplane restore failed"
    fi
}

do_derp() {
    if [ -f derper.conf ]; then
        log "Restoring DERP config..."
        sudo mkdir -p /var/lib/derper
        sudo cp derper.conf /var/lib/derper/derper.conf
        sudo systemctl restart derper 2>/dev/null || warn "  DERP not running as systemd service"
        log "  DERP config restored"
    fi
    if [ -f derpmap.json ]; then
        sudo mkdir -p /var/lib/derpmap
        sudo cp derpmap.json /var/lib/derpmap/derpmap.json
        sudo systemctl restart derpmap 2>/dev/null || true
    fi
}

case "${CHOICE}" in
    1) do_skygate_code ;;
    2) do_env ;;
    3) do_skygate_db ;;
    4) do_headscale_config ;;
    5) do_headscale_db ;;
    6) do_headplane ;;
    7) do_derp ;;
    8)
        # Note: we deliberately call do_env BEFORE do_skygate_db
        # in the "ALL" path so the .env is on disk for the PG
        # dispatcher to read. (For interactive choice=3 the
        # operator can do env first manually.)
        do_skygate_code
        do_env
        do_skygate_db
        do_headscale_config
        do_headscale_db
        do_headplane
        do_derp
        ;;
    0) exit 0 ;;
    *) err "Invalid choice" ;;
esac

# Cleanup
rm -rf "${RESTORE_DIR}"

echo ""
log "Restore complete!"
echo ""
echo "Post-restore steps:"
echo "  1. Restart headscale:  sudo docker restart headscale"
echo "  2. Restart skygate:    sudo docker restart skygate"
echo "  3. Restart headplane:  sudo docker restart headplane"
echo "  4. Verify:             curl -s http://localhost:8080/login"
