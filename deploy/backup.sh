#!/usr/bin/env bash
#===============================================================================
# backup.sh — Full Skygate stack backup (cross-platform)
# Usage: ./deploy/backup.sh [output-dir]
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# B463 — this script needs the database and the paths, NOT the docker stack's
# HEADPLANE_*/HEADSCALE_SERVER_URL (which a native install's
# /etc/skygate/skygate.env does not carry). Without this the required-variable
# check in lib/env.sh exits before the script does any work on a native host.
SKYGATE_ENV_SKIP_REQUIRED=1
source "${SCRIPT_DIR}/lib/env.sh"
source "${SCRIPT_DIR}/lib/docker.sh"

OUTPUT_DIR="${1:-${DEPLOY_BACKUP_DIR}}"
DATE_TAG=$(date +%Y%m%d_%H%M%S)
BACKUP_NAME="skygate-full-${DATE_TAG}"
BACKUP_PATH="${OUTPUT_DIR}/${BACKUP_NAME}"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[OK]${NC} $1"; }
warn() { echo -e "${YELLOW}[!!]${NC} $1"; }
die()  { echo -e "${RED}[XX]${NC} $1" >&2; exit 1; }

# ── B459: the SQLite database is a FILE on a native install ──────────────────
# SKYGATE_DB may be sqlite:/abs/path, file:/abs/path or a bare path. env.sh
# defaults it to /data/skygate.db, which is a CONTAINER path that does not
# exist on a native host — so this resolver prints nothing there (and the
# docker-volume copy below is the fallback) instead of handing back a path
# that cannot be copied.
sqlite_db_path() {
    local v="${SKYGATE_DB:-}"
    case "$v" in
        sqlite:*) v="${v#sqlite:}" ;;
        file:*)   v="${v#file:}" ;;
        postgres:*|postgresql:*) return 0 ;;
    esac
    v="${v%%\?*}"
    case "$v" in
        /*) [ -f "$v" ] && printf '%s\n' "$v" ;;
    esac
}

# ── B463: where headscale keeps its SQLite database on a NATIVE install ──────
# This is a DELIBERATE third copy of the resolver in scripts/backup.sh and
# scripts/restore.sh: the three scripts are shipped and run independently and
# none sources a shared library. scripts/check_b463_deploy_backup_native.sh
# extracts the function from ALL THREE files and fails on any textual drift, so
# "duplicate" cannot become "diverged".
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

echo "=============================================="
echo "  Skygate Full Backup — ${DATE_TAG}"
echo "  OS: ${SKYGATE_OS}"
echo "=============================================="

rm -rf "${BACKUP_PATH}"; mkdir -p "${BACKUP_PATH}"

# ── 1. .env ──
# B463 — copy the env file lib/env.sh actually LOADED, which on a native install
# is /etc/skygate/skygate.env and not the checkout's. Copying `${PROJECT_DIR}/.env`
# unconditionally archived a file the service never reads (or nothing at all).
log "Backing up .env..."
ENV_SRC="${SKYGATE_ENV_FILE_USED:-${PROJECT_DIR}/.env}"
if [ -f "${ENV_SRC}" ]; then
    cp "${ENV_SRC}" "${BACKUP_PATH}/.env"
    chmod 600 "${BACKUP_PATH}/.env" 2>/dev/null || true
    log "  .env -> backup (from ${ENV_SRC})"
else
    warn "  .env not found"
fi

# ── 1b. B463 — the rest of /etc/skygate on a native install ──
# cluster-state.json carries the node identity the cluster API uses; dbs.env the
# DB credentials the helper scripts read. Losing them is not recoverable by
# re-installing.
if [ -d /etc/skygate ]; then
    mkdir -p "${BACKUP_PATH}/etc-skygate"
    for _f in cluster-state.json dbs.env; do
        [ -f "/etc/skygate/${_f}" ] && cp -p "/etc/skygate/${_f}" "${BACKUP_PATH}/etc-skygate/${_f}"
    done
    [ -n "$(ls -A "${BACKUP_PATH}/etc-skygate" 2>/dev/null)" ] \
        && log "  /etc/skygate (cluster-state.json, dbs.env)" \
        || rmdir "${BACKUP_PATH}/etc-skygate" 2>/dev/null || true
fi

# ── 2. Skygate source ──
log "Backing up Skygate source..."
if [ -d "${PROJECT_DIR}/.git" ]; then
    git -C "${PROJECT_DIR}" bundle create "${BACKUP_PATH}/skygate-repo.bundle" --all 2>/dev/null &&         log "  Git bundle: $(du -h "${BACKUP_PATH}/skygate-repo.bundle" | cut -f1)" || warn "  Git bundle failed"
    git -C "${PROJECT_DIR}" log --oneline -10 > "${BACKUP_PATH}/skygate-git-log.txt" 2>/dev/null
else warn "  No .git directory — source not backed up"; fi

# ── 3. SQLite WAL checkpoint ──
log "Running SQLite WAL checkpoints..."
if command -v docker >/dev/null 2>&1; then
    sqlite_checkpoint skygate-data skygate.db || true
    sqlite_checkpoint headscale_headscale_data db.sqlite || true
else
    log "  (docker not available — the native database is checkpointed in step 4)"
fi

# ── 4. Skygate DB ──
# 2026-09-12 (Issue #2 follow-up): backup.sh previously assumed
# SQLite at /data/skygate.db and never captured the PG case.
# When skygate is configured with SKYGATE_DB_DSN=postgres://...
# (local PG or remote via SSH-transport), /data/skygate.db is
# empty (or stale) — the actual live data is in PG, and the
# backup was silently capturing a 0-byte file.
#
# Now we branch on the env:
#   - SKYGATE_DB starts with "sqlite:" or "file:" or has no scheme
#     → SQLite (v1.5.4+ default): cp /data/skygate.db
#   - SKYGATE_DB starts with "postgres:" OR SKYGATE_DB_DSN is set:
#     → PG. Prefer SSHDumpTransport if SKYGATE_DBMIGRATE_TRANSPORT=ssh
#       (remote PG). Otherwise pg_dump locally via the skygate
#       container (or a one-shot alpine + psql if no skygate running).
#   - empty SKYGATE_DB AND empty SKYGATE_DB_DSN → use the legacy
#     /data/skygate.db copy as a last-resort fallback.
log "Backing up Skygate database..."

DB_TARGET=""
case "${SKYGATE_DB:-}" in
    sqlite:*|file:*|"")
        # v1.5.4+ default: SQLite at the bind-mounted path.
        # The legacy `cp` path also covers pre-v1.5.4 setups that
        # used SKYGATE_DB_DSN but stored a SQLite mirror at /data
        # for debug purposes — copy is idempotent and cheap.
        DB_TARGET="sqlite"
        ;;
    postgres:*|postgresql:*)
        DB_TARGET="postgres"
        ;;
    *)
        # Bare path (legacy v1.5.4 fallback when SKYGATE_DB is a
        # path like /data/skygate.db). Treat as SQLite.
        DB_TARGET="sqlite"
        ;;
esac
# If SKYGATE_DB is empty/unset but SKYGATE_DB_DSN is set, the
# legacy fallback (pre-v1.5.4) was to use the DSN as PG. Honour
# that here so we don't regress older deployments.
if [ "${SKYGATE_DB:-}" = "" ] && [ -n "${SKYGATE_DB_DSN:-}" ]; then
    DB_TARGET="postgres"
fi

case "${DB_TARGET}" in
    sqlite)
        # B459 — on a NATIVE install the database is a file on this filesystem
        # and there is no docker to copy a named volume with. Pre-B459 this
        # branch ran `docker run -v skygate-data:/data`, which failed with a
        # WARN on every native host — and the script then finished
        # "successfully" with an archive that contained every file EXCEPT the
        # database. A backup without the database is not a backup, so a failed
        # capture is now fatal instead of advisory.
        SQLITE_FILE="$(sqlite_db_path || true)"
        if [ -n "${SQLITE_FILE}" ]; then
            if command -v sqlite3 >/dev/null 2>&1; then
                sqlite3 "${SQLITE_FILE}" 'PRAGMA wal_checkpoint(TRUNCATE)' >/dev/null 2>&1 \
                    && log "  WAL checkpoint: ${SQLITE_FILE}" \
                    || warn "  WAL checkpoint failed — copying -wal/-shm as-is"
            else
                warn "  sqlite3 not installed — copying -wal/-shm instead of checkpointing"
            fi
            cp -p "${SQLITE_FILE}" "${BACKUP_PATH}/skygate.db" \
                || die "  skygate.db copy failed (${SQLITE_FILE})"
            for _sfx in -wal -shm; do
                [ -f "${SQLITE_FILE}${_sfx}" ] && cp -p "${SQLITE_FILE}${_sfx}" "${BACKUP_PATH}/skygate.db${_sfx}"
            done
            [ -s "${BACKUP_PATH}/skygate.db" ] \
                || die "  skygate.db copy is empty — refusing to write an archive without the database"
            log "  skygate.db -> $(du -h "${BACKUP_PATH}/skygate.db" | cut -f1)"
        else
            ${DOCKER_CMD} run --rm -v skygate-data:/data -v "${BACKUP_PATH}:/backup" alpine \
                sh -c "cp /data/skygate.db /backup/skygate.db 2>/dev/null" \
                || die "  skygate.db copy failed: SKYGATE_DB='${SKYGATE_DB:-}' is not a readable file on this host and the docker volume copy failed — a backup without the database is not a backup"
            log "  skygate.db -> $(du -h "${BACKUP_PATH}/skygate.db" | cut -f1)"
        fi
        ;;
    postgres)
        # PG mode: pg_dump the live DSN.
        #
        # Sub-case A: SKYGATE_DBMIGRATE_TRANSPORT=ssh + a valid
        # SSH host/user/key → dump from the remote PG host (the
        # case for B202.5 / svi→agent). Stream the dump directly
        # to BACKUP_PATH/skygate-pg.sql via ssh.
        #
        # Sub-case B: everything else → try a local pg_dump via
        # docker run (psql-client image). Use SKYGATE_DB_DSN if
        # set, otherwise fall back to the container's local PG.
        if [ "${SKYGATE_DBMIGRATE_TRANSPORT:-}" = "ssh" ] && \
           [ -n "${SKYGATE_DBMIGRATE_SSH_HOST:-}" ] && \
           [ -n "${SKYGATE_DBMIGRATE_SSH_USER:-}" ]; then
            log "  PG (ssh-transport): ${SKYGATE_DBMIGRATE_SSH_USER}@${SKYGATE_DBMIGRATE_SSH_HOST}"
            SSH_KEY="${SKYGATE_DBMIGRATE_SSH_KEY:-}"
            SSH_PORT="${SKYGATE_DBMIGRATE_SSH_PORT:-22}"
            PGDUMP_REMOTE="${SKYGATE_DBMIGRATE_SSH_PGDUMP:-pg_dump}"
            SOURCE_DSN="${SKYGATE_DB_DSN:-}"
            # Build ssh args. Use the configured key if present,
            # otherwise default to the user's id_*.
            SSH_ARGS=("-i" "${SSH_KEY}" "-p" "${SSH_PORT}" "-o" "BatchMode=yes" "-o" "StrictHostKeyChecking=accept-new")
            # Quote the DSN so special chars (e.g. '@', ':', '?')
            # don't break the remote shell.
            REMOTE_CMD="${PGDUMP_REMOTE} -Fc --no-owner --no-acl --no-comments $(printf '%q' "${SOURCE_DSN}")"
            # Run ssh -o ProxyCommand=... when SKYGATE_DBMIGRATE_SSH_KEY
            # is itself a tunnel target, otherwise ssh directly. For
            # now: always ssh directly (the host script is the
            # operator's; they configured the key path).
            if ssh "${SSH_ARGS[@]}" \
                "${SKYGATE_DBMIGRATE_SSH_USER}@${SKYGATE_DBMIGRATE_SSH_HOST}" \
                "${REMOTE_CMD}" > "${BACKUP_PATH}/skygate-pg.sql" 2>/dev/null; then
                log "  skygate-pg.sql (ssh dump) -> $(du -h "${BACKUP_PATH}/skygate-pg.sql" | cut -f1)"
            else
                warn "  ssh pg_dump failed (host unreachable or auth denied) — falling back to local pg_dump"
                # Fallback: try local pg_dump via docker psql-client
                if [ -n "${SOURCE_DSN}" ]; then
                    ${DOCKER_CMD} run --rm -v "${BACKUP_PATH}:/backup" postgres:15-alpine \
                        sh -c "pg_dump -Fc --no-owner --no-acl --no-comments \"${SOURCE_DSN}\" > /backup/skygate-pg.sql" 2>/dev/null \
                        && log "  skygate-pg.sql (local fallback) -> $(du -h "${BACKUP_PATH}/skygate-pg.sql" | cut -f1)" \
                        || warn "  local pg_dump fallback also failed"
                else
                    warn "  no SKYGATE_DB_DSN — cannot fall back to local pg_dump"
                fi
            fi
        elif [ -n "${SKYGATE_DB_DSN:-}" ]; then
            # Local PG (skygate container talks to a PG container on
            # the docker bridge — 172.17.0.1:5433 historically). Run
            # pg_dump via the postgres:15-alpine image with the DSN.
            log "  PG (local): ${SKYGATE_DB_DSN}"
            ${DOCKER_CMD} run --rm -v "${BACKUP_PATH}:/backup" postgres:15-alpine \
                sh -c "pg_dump -Fc --no-owner --no-acl --no-comments \"${SKYGATE_DB_DSN}\" > /backup/skygate-pg.sql" 2>/dev/null \
                && log "  skygate-pg.sql (local) -> $(du -h "${BACKUP_PATH}/skygate-pg.sql" | cut -f1)" \
                || warn "  local pg_dump failed"
        else
            warn "  PG mode but no SKYGATE_DB_DSN and no SSH transport — skipping DB backup"
        fi
        ;;
esac

# ── 5. Headscale DB ──
# B463 — the NATIVE file first. Pre-B463 this step was docker-volume-only, so a
# native host (where headscale keeps everything in /var/lib/headscale) shipped an
# archive without headscale's state at all — silently.
HS_DB_NATIVE="$(headscale_db_path || true)"
if [ -n "${HS_DB_NATIVE}" ]; then
    log "Backing up Headscale database (native file: ${HS_DB_NATIVE})..."
    if cp -p "${HS_DB_NATIVE}" "${BACKUP_PATH}/headscale-db.sqlite" 2>/dev/null; then
        for _sfx in -wal -shm; do
            [ -f "${HS_DB_NATIVE}${_sfx}" ] && cp -p "${HS_DB_NATIVE}${_sfx}" "${BACKUP_PATH}/headscale-db.sqlite${_sfx}"
        done
        log "  headscale-db.sqlite -> $(du -h "${BACKUP_PATH}/headscale-db.sqlite" | cut -f1)"
    else
        warn "  headscale DB copy failed (${HS_DB_NATIVE}) — headscale state is NOT in this archive"
    fi
elif command -v docker >/dev/null 2>&1; then
    log "Backing up Headscale database (docker volume)..."
    ${DOCKER_CMD} run --rm -v headscale_headscale_data:/data -v "${BACKUP_PATH}:/backup" alpine sh -c "cp /data/db.sqlite /backup/headscale-db.sqlite 2>/dev/null" &&     log "  headscale-db.sqlite -> $(du -h "${BACKUP_PATH}/headscale-db.sqlite" | cut -f1)" || warn "  headscale DB copy failed"
else
    warn "  headscale database NOT captured: no native path resolved (checked SKYGATE_HEADSCALE_DB_PATH, /etc/headscale/config.*, /var/lib/headscale/db.sqlite) and docker is unavailable."
    warn "  If headscale runs on PostgreSQL there is no file to copy — dump it separately."
fi

# ── 6. Headscale config ──
# B463 — /etc/headscale on a native install, the bind mount on a docker one.
log "Backing up Headscale config..."
HS_CONFIG_SRC=""
for _d in /etc/headscale "${DEPLOY_HEADSCALE_DIR}/config" /home/admin/headscale/config; do
    [ -d "${_d}" ] || continue
    mkdir -p "${BACKUP_PATH}/headscale-config"
    cp -r "${_d}/." "${BACKUP_PATH}/headscale-config/" 2>/dev/null || true
    HS_CONFIG_SRC="${_d}"
    break
done
if [ -n "${HS_CONFIG_SRC}" ]; then
    log "  Headscale config -> $(ls "${BACKUP_PATH}/headscale-config/" | wc -l) files (from ${HS_CONFIG_SRC})"
else
    warn "  config not found (/etc/headscale, ${DEPLOY_HEADSCALE_DIR}/config)"
fi

# ── 7. Headplane config ──
# 2026-07-14: Этап 14 v11 — Headplane is an optional module.
# When HEADPLANE_ENABLED=false, the sidecar isn't running and
# there's nothing to back up. The script still records the
# env var in the manifest so a restore on a different host
# can decide whether to redeploy the sidecar.
if [ "${HEADPLANE_ENABLED}" = "false" ]; then
    log "  Headplane skipped (HEADPLANE_ENABLED=false)"
else
    log "Backing up Headplane config..."
    HP_CONFIG="${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml"
    [ -f "${HP_CONFIG}" ] && cp "${HP_CONFIG}" "${BACKUP_PATH}/headplane-config.yaml" && log "  headplane-config.yaml" || warn "  not found"
fi

# ── 8. Headplane data ──
# 2026-07-14: Этап 14 v11 — see above; data backup also gated.
if [ "${HEADPLANE_ENABLED}" != "false" ]; then
    log "Backing up Headplane data..."
    ${DOCKER_CMD} run --rm -v headscale_headplane_data:/data -v "${BACKUP_PATH}:/backup" alpine sh -c "cp -r /data /backup/headplane-data 2>/dev/null" &&     log "  headplane data" || warn "  headplane data copy failed"
fi

# ── 9. SSH keys ──
log "Backing up SSH keys..."
mkdir -p "${BACKUP_PATH}/ssh"
for _key in skygate_sync skygate_sync.pub; do
    [ -f "${SSH_DIR}/${_key}" ] && cp "${SSH_DIR}/${_key}" "${BACKUP_PATH}/ssh/${_key}"; done
[ "$(ls -A "${BACKUP_PATH}/ssh" 2>/dev/null)" ] && log "  SSH keys" || warn "  No SSH keys"

# ── 10. DERP ──
[ "${DERP_ENABLED}" = "true" ] && {
    log "Backing up DERP config..."
    [ -f /var/lib/derper/derper.conf ] && cp /var/lib/derper/derper.conf "${BACKUP_PATH}/derper.conf" && log "  derper.conf"
    [ -f "${DEPLOY_HEADSCALE_DIR}/derpmap.json" ] && cp "${DEPLOY_HEADSCALE_DIR}/derpmap.json" "${BACKUP_PATH}/derpmap.json" && log "  derpmap.json"; }

# ── 11. Docker images ──
log "Saving Docker images..."
${DOCKER_CMD} save skygate-skygate:latest -o "${BACKUP_PATH}/skygate-image.tar" 2>/dev/null && log "  skygate-image.tar" || warn "  skygate image save failed"
${DOCKER_CMD} save headscale/headscale:0.29.1 -o "${BACKUP_PATH}/headscale-image.tar" 2>/dev/null && log "  headscale-image.tar" || warn "  headscale image save failed"
if [ "${HEADPLANE_ENABLED}" != "false" ] && [ -z "${HEADPLANE_EXTERNAL_URL}" ]; then
    ${DOCKER_CMD} save "${HEADPLANE_IMAGE}" -o "${BACKUP_PATH}/headplane-image.tar" 2>/dev/null && log "  headplane-image.tar" || warn "  headplane image save failed"
fi

# ── 12. Inventory ──
# B463 — the manifest must name what was really produced and WHERE it came from: a
# native archive carries .env from /etc/skygate/skygate.env, etc-skygate/ and a
# headscale file from a native path (or none at all), and an inventory that hides
# that is the same "artifact that looks complete" defect the rest of this block is
# about.
cat > "${BACKUP_PATH}/inventory.txt" << INVEOF
Skygate Full Backup — ${DATE_TAG} (OS: ${SKYGATE_OS})
==================================
  .env . skygate-repo.bundle . skygate.db|skygate-pg.sql . headscale-db.sqlite
  etc-skygate/ . headscale-config/ . headplane-config.yaml . headplane-data/
  ssh/ . skygate-image.tar . headscale-image.tar . headplane-image.tar
ENV_SOURCE=${ENV_SRC:-<none>}
HEADSCALE_CONFIG_SOURCE=${HS_CONFIG_SRC:-<none>}
HEADSCALE_DB_SOURCE=${HS_DB_NATIVE:-<docker volume or none>}
HEADPLANE_ENABLED=${HEADPLANE_ENABLED:-true}
HEADPLANE_IMAGE=${HEADPLANE_IMAGE:-ghcr.io/tale/headplane:0.6.3}
HEADPLANE_EXTERNAL_URL=${HEADPLANE_EXTERNAL_URL:-}
DERP_ENABLED=${DERP_ENABLED:-false}
DERP_EXTERNAL_URLS=${DERP_EXTERNAL_URLS:-}
SKYGATE_IMAGE=skygate-skygate:latest  # set by the running container; the actual tag is in .git describe
SKYGATE_DB=${SKYGATE_DB:-}
SKYGATE_DB_DSN=${SKYGATE_DB_DSN:-}
SKYGATE_DBMIGRATE_TRANSPORT=${SKYGATE_DBMIGRATE_TRANSPORT:-}
Restore: ./deploy/deploy.sh --from-path <this-directory>
INVEOF

# ── 13. Package ──
log "Creating archive..."
cd "${OUTPUT_DIR}"
tar czf "${BACKUP_NAME}.tar.gz" "${BACKUP_NAME}" 2>/dev/null
rm -rf "${BACKUP_NAME}"

ARCHIVE="${OUTPUT_DIR}/${BACKUP_NAME}.tar.gz"
echo ""; echo "=============================================="
echo "  Backup: ${ARCHIVE}"
echo "  Size:   $(du -h "${ARCHIVE}" | cut -f1)"
echo "  SHA256: $(sha256sum "${ARCHIVE}" | cut -d' ' -f1)"
echo "=============================================="
