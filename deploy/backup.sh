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
log "Backing up Skygate database..."
${DOCKER_CMD} run --rm -v skygate-data:/data -v "${BACKUP_PATH}:/backup" alpine sh -c "cp /data/skygate.db /backup/skygate.db 2>/dev/null" &&     log "  skygate.db -> $(du -h "${BACKUP_PATH}/skygate.db" | cut -f1)" || warn "  skygate.db copy failed"

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
${DOCKER_CMD} save "${HEADPLANE_IMAGE}" -o "${BACKUP_PATH}/headplane-image.tar" 2>/dev/null && log "  headplane-image.tar" || warn "  headplane image save failed"

# ── 12. Inventory ──
cat > "${BACKUP_PATH}/inventory.txt" << INVEOF
Skygate Full Backup — ${DATE_TAG} (OS: ${SKYGATE_OS})
==================================
  .env . skygate-repo.bundle . skygate.db . headscale-db.sqlite
  headscale-config/ . headplane-config.yaml . headplane-data/
  ssh/ . skygate-image.tar . headscale-image.tar . headplane-image.tar
HEADPLANE_ENABLED=${HEADPLANE_ENABLED:-true}
HEADPLANE_IMAGE=${HEADPLANE_IMAGE:-ghcr.io/tale/headplane:0.6.3}
SKYGATE_IMAGE=skygate-skygate:latest  # set by the running container; the actual tag is in .git describe
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
