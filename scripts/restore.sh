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
    echo "  3) Skygate database (skygate.db → native SKYGATE_DB path, else Docker volume)"
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

# B462 — the env must go where the SERVICE reads it.
#
# On a native install that is /etc/skygate/skygate.env (the unit's EnvironmentFile).
# Writing it into the checkout's `.env` — the pre-B462 behaviour — restores nothing
# for the running service, so a "successful" restore left skygate pointing at the
# old database.
skygate_env_target() {
    local f
    for f in "${SKYGATE_UPDATE_ENV_FILE:-}" "${SKYGATE_ENV_FILE:-}" /etc/skygate/skygate.env; do
        [ -n "${f}" ] || continue
        [ -d "$(dirname "${f}")" ] || continue
        printf '%s\n' "${f}"
        return 0
    done
    printf '%s\n' "${TARGET_DIR}/.env"
}

do_env() {
    if [ -f skygate.env ]; then
        local dest
        dest="$(skygate_env_target)"
        if [ -f "${dest}" ]; then
            cp -p "${dest}" "${dest}.pre-restore-$(date +%Y%m%d_%H%M%S)" 2>/dev/null || true
        fi
        mkdir -p "$(dirname "${dest}")" 2>/dev/null || true
        cp skygate.env "${dest}" || err "  cannot write the env file to ${dest}"
        chmod 600 "${dest}" 2>/dev/null || true
        log "  env restored to ${dest} (contains secrets!)"
        if [ "${dest}" != "${TARGET_DIR}/.env" ]; then
            warn "  that is the RUNTIME config file — restart skygate so the new values are used"
        fi
    fi

    # B462 — the native-only files. cluster-state.json carries the node identity the
    # cluster API uses; losing it on a restore means re-joining the node by hand.
    if [ -d etc-skygate ]; then
        local _f
        for _f in cluster-state.json dbs.env; do
            [ -f "etc-skygate/${_f}" ] || continue
            sudo mkdir -p /etc/skygate 2>/dev/null || true
            if [ -f "/etc/skygate/${_f}" ]; then
                sudo cp -p "/etc/skygate/${_f}" "/etc/skygate/${_f}.pre-restore-$(date +%Y%m%d_%H%M%S)" 2>/dev/null || true
            fi
            if sudo cp -p "etc-skygate/${_f}" "/etc/skygate/${_f}" 2>/dev/null; then
                log "  /etc/skygate/${_f} restored"
            else
                warn "  could not write /etc/skygate/${_f} (needs root) — copy it by hand from etc-skygate/"
            fi
        done
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
# B461 — THE RESTORE MUST SPEAK THE DATABASE'S DIALECT TOO.
#
# B459 gave scripts/backup.sh a SQLite branch, so a native SQLite install can now
# produce an archive whose database is `skygate.db` — and that same script tells
# the operator to run `./scripts/restore.sh <archive>` as the next step. The
# restore half was still docker-only: `do_skygate_db()` copied the file into the
# docker named volume `skygate-data` and called it "v0.32.x legacy", so on a
# native install (no docker, the database at an absolute path) the documented
# command could not put the database back at all. A backup you cannot restore is
# not a backup.
#
# sqlite_target_path resolves the database FILE the install uses. The SOURCES are
# walked explicitly — the process env, then the restored target .env, then the
# archive's own skygate.env — because an earlier version of this function tried to
# guess "value vs file path" from the SHAPE of the string (`/*` meant "a value"),
# and `${TARGET_DIR}/.env` is an absolute path that does not exist: it was taken as
# the database path and the restore wrote `skygate.db` OVER it. Caught by the B461
# behavioural leg (E6), which is why that leg exists.
#
# Prints nothing when the install is not SQLite or the value is not a local
# absolute path.
sqlite_target_path() {
    local v="" f
    if [ -n "${SKYGATE_DB:-}" ]; then
        v="${SKYGATE_DB}"
    else
        for f in "${TARGET_DIR:-}/.env" "skygate.env"; do
            [ -n "${f}" ] || continue
            [ -f "${f}" ] || continue
            v=$(grep -E '^SKYGATE_DB=' "${f}" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\"'" || true)
            [ -n "${v}" ] && break
        done
    fi
    case "${v}" in
        sqlite:*) v="${v#sqlite:}" ;;
        file:*)   v="${v#file:}" ;;
        postgres:*|postgresql:*|"") return 0 ;;
    esac
    v="${v%%\?*}"
    case "${v}" in
        /*) printf '%s\n' "${v}" ;;
    esac
}

# do_skygate_db_sqlite puts `skygate.db` back where the install expects it.
#
# The archive file is validated BEFORE anything is touched: copying an empty or
# non-SQLite file over a live database is worse than not restoring at all.
do_skygate_db_sqlite() {
    [ -s skygate.db ] || err "  skygate.db in the archive is empty — refusing to touch the live database"
    if [ "$(head -c 15 skygate.db 2>/dev/null)" != "SQLite format 3" ]; then
        err "  skygate.db in the archive does not carry the SQLite header — refusing to restore it"
    fi

    local target backup
    target="$(sqlite_target_path || true)"
    if [ -n "${target}" ]; then
        # A directory here means the resolver handed back something that cannot be
        # a database file — refuse rather than write into it.
        [ -d "${target}" ] && err "  the resolved SKYGATE_DB path is a directory (${target}) — refusing to restore into it"
        log "Restoring Skygate SQLite database (native file: ${target})..."
        if [ -f "${target}" ]; then
            backup="${target}.pre-restore-$(date +%Y%m%d_%H%M%S)"
            cp -p "${target}" "${backup}" \
                && log "  previous database kept at ${backup}" \
                || err "  cannot back up the live database (${target}) — refusing to overwrite it"
        fi
        mkdir -p "$(dirname "${target}")"
        cp -p skygate.db "${target}" || err "  copy to ${target} failed"
        # The WAL/SHM of the OLD database must not survive next to the NEW file:
        # SQLite would replay a log that belongs to different data.
        rm -f "${target}-wal" "${target}-shm"
        [ -s "${target}" ] || err "  ${target} is empty after the copy"
        chmod 600 "${target}" 2>/dev/null || true
        log "  skygate.db restored to ${target}"
        warn "  Restart skygate so it opens the restored database"
        return 0
    fi

    # No resolvable native path: this is a docker install, so the named volume is
    # the target — the pre-B461 behaviour.
    log "Restoring Skygate SQLite database (docker volume skygate-data)..."
    docker run --rm -v skygate-data:/data -v "$(pwd):/restore" alpine \
        sh -c "cp /restore/skygate.db /data/skygate.db && chown -R 1000:1000 /data" 2>/dev/null \
        && log "  skygate.db restored to the Docker volume" \
        || err "  the database was NOT restored: no native SKYGATE_DB path was resolvable and the docker volume copy failed. Place skygate.db at the path in SKYGATE_DB (or start the containers) and re-run."
}

do_skygate_db() {
    if [ -f skygate-pg.sql ]; then
        do_pg_restore
    elif [ -f skygate.db ]; then
        do_skygate_db_sqlite
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
        #
        # B462 — WHICH directory: /etc/headscale on a native install (the
        # documented location of headscale's config), /home/admin/headscale/config
        # for the docker bind mount. Pre-B462 only the docker path was restored, so
        # a native restore put nothing where headscale reads.
        local dest=""
        if [ -d /etc/headscale ]; then
            dest=/etc/headscale
        elif [ -d /home/admin/headscale/config ]; then
            dest=/home/admin/headscale/config
        else
            dest=/etc/headscale
            warn "  neither /etc/headscale nor /home/admin/headscale/config exists — creating the native path"
        fi
        sudo mkdir -p "${dest}"
        if [ -n "$(ls -A "${dest}" 2>/dev/null)" ]; then
            sudo cp -r "${dest}" "${dest}.pre-restore-$(date +%Y%m%d_%H%M%S)" 2>/dev/null || true
        fi
        sudo cp -r headscale-config/. "${dest}/"
        log "  Headscale config restored to ${dest}"
        warn "  Restart headscale so it reads the restored config"
    fi
}

# B462 — where headscale keeps its SQLite database on a NATIVE install. This is a
# DELIBERATE duplicate of the resolver in scripts/backup.sh: the two scripts are
# shipped and run independently on operator hosts, and neither sources a shared
# lib today. The B462 contract extracts this function from BOTH files and fails on
# any textual drift, so "duplicate" cannot become "diverged".
#
#   1. SKYGATE_HEADSCALE_DB_PATH — explicit override;
#   2. the `path:` under `database:` → `sqlite:` in headscale's own config, using
#      the SAME candidate list the OIDC and policy appliers use;
#   3. the documented default /var/lib/headscale/db.sqlite, when it exists.
headscale_db_path() {
  local v="${SKYGATE_HEADSCALE_DB_PATH:-}" cfg
  if [ -n "${v}" ]; then
    [ -f "${v}" ] && printf '%s\n' "${v}"
    return 0
  fi
  for cfg in "${SKYGATE_HEADSCALE_CONFIG:-}" \
             /etc/headscale/config.yaml /etc/headscale/config.yml \
             /etc/headscale/config.hujson /var/lib/headscale/config.yaml; do
    [ -r "${cfg}" ] || continue
    v=$(awk '
      /^[[:space:]]*database:[[:space:]]*$/ { indb = 1; next }
      indb && /^[^[:space:]]/             { indb = 0 }
      indb && /sqlite:[[:space:]]*$/      { insql = 1; next }
      indb && /sqlite:[[:space:]]*[^[:space:]]/ {
        line = $0
        sub(/.*sqlite:[[:space:]]*/, "", line)
        gsub(/["\047]/, "", line)
        if (line != "") { print line; exit }
        insql = 1; next
      }
      insql && /^[[:space:]]*path:[[:space:]]*/ {
        line = $0
        sub(/^[[:space:]]*path:[[:space:]]*/, "", line)
        gsub(/["\047]/, "", line)
        print line
        exit
      }
    ' "${cfg}" 2>/dev/null || true)
    [ -n "${v}" ] && break
  done
  if [ -z "${v}" ] && [ -f /var/lib/headscale/db.sqlite ]; then
    v=/var/lib/headscale/db.sqlite
  fi
  [ -n "${v}" ] && [ -f "${v}" ] && printf '%s\n' "${v}"
  return 0
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
        # B462 — put it back where headscale reads it. A native install keeps the
        # file at the `path:` in its own config (default /var/lib/headscale/db.sqlite);
        # the pre-B462 code could only write into the docker named volume, so a
        # native restore silently left the OLD headscale database in place.
        local hs_target
        hs_target="$(headscale_db_path || true)"
        if [ -n "${hs_target}" ]; then
            log "Restoring Headscale database (native file: ${hs_target})..."
            [ -f "${hs_target}" ] && sudo cp -p "${hs_target}" \
                "${hs_target}.pre-restore-$(date +%Y%m%d_%H%M%S)" 2>/dev/null || true
            sudo cp -p "${DB_FILE}" "${hs_target}" \
                && log "  Headscale DB restored to ${hs_target}" \
                || err "  cannot write ${hs_target} — the headscale database was NOT restored"
            # A surviving -wal/-shm belongs to the OLD database.
            sudo rm -f "${hs_target}-wal" "${hs_target}-shm"
            warn "  Restart headscale so it opens the restored database"
            return 0
        fi
        log "Restoring Headscale database (docker volume)..."
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
