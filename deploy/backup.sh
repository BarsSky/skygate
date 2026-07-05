#!/usr/bin/env bash
#===============================================================================
# backup.sh — Full Skygate stack backup
# Usage: ./deploy/backup.sh [output-dir]
#   output-dir: where to write the tar.gz (default: DEPLOY_BACKUP_DIR from .env)
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Load .env
source "${SCRIPT_DIR}/lib/env.sh"

OUTPUT_DIR="${1:-${DEPLOY_BACKUP_DIR}}"
DATE_TAG=$(date +%Y%m%d_%H%M%S)
BACKUP_NAME="skygate-full-${DATE_TAG}"
BACKUP_PATH="${OUTPUT_DIR}/${BACKUP_NAME}"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'
log()  { echo -e "${GREEN}[OK]${NC} $1"; }
warn() { echo -e "${YELLOW}[!!]${NC} $1"; }
err()  { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }

echo "=============================================="
echo "  Skygate Full Backup — ${DATE_TAG}"
echo "=============================================="

rm -rf "${BACKUP_PATH}"
mkdir -p "${BACKUP_PATH}"

# ── 1. .env ────────────────────────────────────────────────────────────────
log "Backing up .env..."
if [ -f "${PROJECT_DIR}/.env" ]; then
    cp "${PROJECT_DIR}/.env" "${BACKUP_PATH}/.env"
    log "  .env → backup"
else
    warn "  .env not found at ${PROJECT_DIR}/.env"
fi

# ── 2. Skygate source (git bundle) ─────────────────────────────────────────
log "Backing up Skygate source..."
if [ -d "${PROJECT_DIR}/.git" ]; then
    git -C "${PROJECT_DIR}" bundle create "${BACKUP_PATH}/skygate-repo.bundle" --all 2>/dev/null &&         log "  Git bundle: $(du -h "${BACKUP_PATH}/skygate-repo.bundle" | cut -f1)" ||         warn "  Git bundle failed"
    git -C "${PROJECT_DIR}" log --oneline -10 > "${BACKUP_PATH}/skygate-git-log.txt" 2>/dev/null
else
    warn "  No .git directory — source not backed up"
fi

# ── 3. SQLite WAL checkpoint ───────────────────────────────────────────────
log "Running SQLite WAL checkpoints..."
source "${SCRIPT_DIR}/lib/docker.sh"
sqlite_checkpoint skygate-data skygate.db || true
sqlite_checkpoint headscale_headscale_data db.sqlite || true

# ── 4. Skygate DB ──────────────────────────────────────────────────────────
log "Backing up Skygate database..."
docker run --rm     -v skygate-data:/data     -v "${BACKUP_PATH}:/backup"     alpine sh -c "cp /data/skygate.db /backup/skygate.db 2>/dev/null && echo ok || echo fail" | grep -q ok &&     log "  skygate.db → $(du -h "${BACKUP_PATH}/skygate.db" | cut -f1)" ||     warn "  skygate.db copy failed"

# ── 5. Headscale DB ────────────────────────────────────────────────────────
log "Backing up Headscale database..."
docker run --rm     -v headscale_headscale_data:/data     -v "${BACKUP_PATH}:/backup"     alpine sh -c "cp /data/db.sqlite /backup/headscale-db.sqlite 2>/dev/null && echo ok || echo fail" | grep -q ok &&     log "  headscale-db.sqlite → $(du -h "${BACKUP_PATH}/headscale-db.sqlite" | cut -f1)" ||     warn "  headscale DB copy failed"

# ── 6. Headscale config ────────────────────────────────────────────────────
log "Backing up Headscale config..."
if [ -d "${DEPLOY_HEADSCALE_DIR}/config" ]; then
    mkdir -p "${BACKUP_PATH}/headscale-config"
    cp -r "${DEPLOY_HEADSCALE_DIR}/config/"* "${BACKUP_PATH}/headscale-config/" 2>/dev/null
    log "  Headscale config → $(ls "${BACKUP_PATH}/headscale-config/" | wc -l) files"
else
    warn "  ${DEPLOY_HEADSCALE_DIR}/config not found"
fi

# ── 7. Headplane config ────────────────────────────────────────────────────
log "Backing up Headplane config..."
HP_CONFIG="${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml"
if [ -f "${HP_CONFIG}" ]; then
    cp "${HP_CONFIG}" "${BACKUP_PATH}/headplane-config.yaml"
    log "  headplane-config.yaml → backup"
else
    warn "  headplane config not found"
fi

# ── 8. Headplane data ──────────────────────────────────────────────────────
log "Backing up Headplane data..."
docker run --rm     -v headscale_headplane_data:/data     -v "${BACKUP_PATH}:/backup"     alpine sh -c "cp -r /data /backup/headplane-data 2>/dev/null && echo ok || echo fail" | grep -q ok &&     log "  headplane data → backup" ||     warn "  headplane data copy failed"

# ── 9. SSH keys ────────────────────────────────────────────────────────────
log "Backing up SSH keys..."
mkdir -p "${BACKUP_PATH}/ssh"
for _key in skygate_sync skygate_sync.pub; do
    if [ -f "/home/skyadmin/.ssh/${_key}" ]; then
        cp "/home/skyadmin/.ssh/${_key}" "${BACKUP_PATH}/ssh/${_key}"
    fi
done
if ls "${BACKUP_PATH}/ssh/"* >/dev/null 2>&1; then
    log "  SSH keys → $(ls "${BACKUP_PATH}/ssh/" | wc -l) files"
else
    warn "  No SSH keys found"
fi

# ── 10. DERP config ────────────────────────────────────────────────────────
if [ "${DERP_ENABLED}" = "true" ]; then
    log "Backing up DERP config..."
    if [ -f /var/lib/derper/derper.conf ]; then
        cp /var/lib/derper/derper.conf "${BACKUP_PATH}/derper.conf"
        log "  derper.conf → backup"
    fi
    if [ -f "${DEPLOY_HEADSCALE_DIR}/derpmap.json" ]; then
        cp "${DEPLOY_HEADSCALE_DIR}/derpmap.json" "${BACKUP_PATH}/derpmap.json"
        log "  derpmap.json → backup"
    fi
fi

# ── 11. Docker images ──────────────────────────────────────────────────────
log "Saving Docker images..."
docker save skygate-skygate:latest -o "${BACKUP_PATH}/skygate-image.tar" 2>/dev/null &&     log "  skygate-image.tar → $(du -h "${BACKUP_PATH}/skygate-image.tar" | cut -f1)" ||     warn "  skygate image save failed"
docker save headscale/headscale:0.29.1 -o "${BACKUP_PATH}/headscale-image.tar" 2>/dev/null &&     log "  headscale-image.tar → $(du -h "${BACKUP_PATH}/headscale-image.tar" | cut -f1)" ||     warn "  headscale image save failed"
docker save ghcr.io/tale/headplane:0.7.4 -o "${BACKUP_PATH}/headplane-image.tar" 2>/dev/null &&     log "  headplane-image.tar → $(du -h "${BACKUP_PATH}/headplane-image.tar" | cut -f1)" ||     warn "  headplane image save failed"

# ── 12. Inventory ──────────────────────────────────────────────────────────
cat > "${BACKUP_PATH}/inventory.txt" << INVEOF
Skygate Full Backup — ${DATE_TAG}
==================================
Files:
  .env                       — All secrets & configuration
  skygate-repo.bundle        — Git repository (full history)
  skygate.db                 — Skygate SQLite database
  headscale-db.sqlite        — Headscale SQLite database
  headscale-config/          — Headscale YAML config + noise key
  headplane-config.yaml      — Headplane static config
  headplane-data/            — Headplane state
  ssh/                       — SSH keys for exit node sync
  derper.conf                — DERP relay config (if enabled)
  derpmap.json               — DERP map (if enabled)
  skygate-image.tar          — Docker image
  headscale-image.tar        — Docker image
  headplane-image.tar        — Docker image
  inventory.txt              — This file

Restore:
  ./deploy/deploy.sh --from-path <this-directory>
INVEOF

# ── 13. Package ────────────────────────────────────────────────────────────
log "Creating archive..."
cd "${OUTPUT_DIR}"
tar czf "${BACKUP_NAME}.tar.gz" "${BACKUP_NAME}" 2>/dev/null
rm -rf "${BACKUP_NAME}"

ARCHIVE="${OUTPUT_DIR}/${BACKUP_NAME}.tar.gz"
SIZE=$(du -h "${ARCHIVE}" | cut -f1)
SHA256=$(sha256sum "${ARCHIVE}" | cut -d' ' -f1)

echo ""
echo "=============================================="
echo "  Backup complete!"
echo "  File:   ${ARCHIVE}"
echo "  Size:   ${SIZE}"
echo "  SHA256: ${SHA256}"
echo "=============================================="
echo ""
echo "To restore on a new machine:"
echo "  1. Copy ${ARCHIVE} to target"
echo "  2. Extract: tar xzf ${BACKUP_NAME}.tar.gz -C /tmp/"
echo "  3. Run:    ./deploy/deploy.sh --from-path /tmp/${BACKUP_NAME}"
