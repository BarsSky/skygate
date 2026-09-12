#!/usr/bin/env bash
#===============================================================================
# backup.sh — Full Skygate stack backup (cross-platform)
# Usage: ./deploy/backup.sh [output-dir]
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
source "${SCRIPT_DIR}/lib/env.sh"
source "${SCRIPT_DIR}/lib/docker.sh"

OUTPUT_DIR="${1:-${DEPLOY_BACKUP_DIR}}"
DATE_TAG=$(date +%Y%m%d_%H%M%S)
BACKUP_NAME="skygate-full-${DATE_TAG}"
BACKUP_PATH="${OUTPUT_DIR}/${BACKUP_NAME}"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[OK]${NC} $1"; }
warn() { echo -e "${YELLOW}[!!]${NC} $1"; }

echo "=============================================="
echo "  Skygate Full Backup — ${DATE_TAG}"
echo "  OS: ${SKYGATE_OS}"
echo "=============================================="

rm -rf "${BACKUP_PATH}"; mkdir -p "${BACKUP_PATH}"

# ── 1. .env ──
log "Backing up .env..."
[ -f "${PROJECT_DIR}/.env" ] && cp "${PROJECT_DIR}/.env" "${BACKUP_PATH}/.env" && log "  .env -> backup" || warn "  .env not found"

# ── 2. Skygate source ──
log "Backing up Skygate source..."
if [ -d "${PROJECT_DIR}/.git" ]; then
    git -C "${PROJECT_DIR}" bundle create "${BACKUP_PATH}/skygate-repo.bundle" --all 2>/dev/null &&         log "  Git bundle: $(du -h "${BACKUP_PATH}/skygate-repo.bundle" | cut -f1)" || warn "  Git bundle failed"
    git -C "${PROJECT_DIR}" log --oneline -10 > "${BACKUP_PATH}/skygate-git-log.txt" 2>/dev/null
else warn "  No .git directory — source not backed up"; fi

# ── 3. SQLite WAL checkpoint ──
log "Running SQLite WAL checkpoints..."
sqlite_checkpoint skygate-data skygate.db || true
sqlite_checkpoint headscale_headscale_data db.sqlite || true

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
        ${DOCKER_CMD} run --rm -v skygate-data:/data -v "${BACKUP_PATH}:/backup" alpine sh -c "cp /data/skygate.db /backup/skygate.db 2>/dev/null" &&     log "  skygate.db -> $(du -h "${BACKUP_PATH}/skygate.db" | cut -f1)" || warn "  skygate.db copy failed (SQLite path)"
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
log "Backing up Headscale database..."
${DOCKER_CMD} run --rm -v headscale_headscale_data:/data -v "${BACKUP_PATH}:/backup" alpine sh -c "cp /data/db.sqlite /backup/headscale-db.sqlite 2>/dev/null" &&     log "  headscale-db.sqlite -> $(du -h "${BACKUP_PATH}/headscale-db.sqlite" | cut -f1)" || warn "  headscale DB copy failed"

# ── 6. Headscale config ──
log "Backing up Headscale config..."
if [ -d "${DEPLOY_HEADSCALE_DIR}/config" ]; then
    mkdir -p "${BACKUP_PATH}/headscale-config"
    cp -r "${DEPLOY_HEADSCALE_DIR}/config/"* "${BACKUP_PATH}/headscale-config/" 2>/dev/null
    log "  Headscale config -> $(ls "${BACKUP_PATH}/headscale-config/" | wc -l) files"
else warn "  config not found"; fi

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
cat > "${BACKUP_PATH}/inventory.txt" << INVEOF
Skygate Full Backup — ${DATE_TAG} (OS: ${SKYGATE_OS})
==================================
  .env . skygate-repo.bundle . skygate.db|skygate-pg.sql . headscale-db.sqlite
  headscale-config/ . headplane-config.yaml . headplane-data/
  ssh/ . skygate-image.tar . headscale-image.tar . headplane-image.tar
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
