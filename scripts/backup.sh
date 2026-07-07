#!/usr/bin/env bash
#===============================================================================
# Skygate Backup Script
# Backup all Skygate + Headscale + Headplane state for migration/restore
# Usage: ./backup.sh [destination]
#   destination: local path or smb://host/share/path (default: /tmp/skygate-backup/)
#===============================================================================
set -euo pipefail

BACKUP_DIR="${1:-/tmp/skygate-backup}"
DATE_TAG=$(date +%Y%m%d_%H%M%S)
BACKUP_PATH="${BACKUP_DIR}/skygate-full-${DATE_TAG}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKYGATE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SSHPASS_FILE="/tmp/skygate-bootstrap-pass.txt"

# Colors
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[✓]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[✗]${NC} $1"; exit 1; }

mkdir -p "${BACKUP_PATH}"
cd "${BACKUP_PATH}"

echo "=============================================="
echo "  Skygate Full Backup — ${DATE_TAG}"
echo "=============================================="

# 1. Skygate source code (via git)
log "Backing up Skygate source code..."
if [ -d "${SKYGATE_DIR}/.git" ]; then
    git -C "${SKYGATE_DIR}" bundle create "${BACKUP_PATH}/skygate-repo.bundle" --all 2>/dev/null
    log "  Git bundle: skygate-repo.bundle"
    git -C "${SKYGATE_DIR}" log --oneline -5 > "${BACKUP_PATH}/skygate-git-log.txt"
fi

# 2. Skygate .env (contains secrets)
if [ -f "${SKYGATE_DIR}/.env" ]; then
    cp "${SKYGATE_DIR}/.env" "${BACKUP_PATH}/skygate.env"
    log "  .env copied"
fi

# 3. Skygate DB (from Docker volume)
log "Backing up Skygate database..."
docker run --rm -v skygate-data:/data -v "${BACKUP_PATH}:/backup" alpine \
    sh -c "cp /data/skygate.db /backup/skygate.db && chmod 644 /backup/skygate.db" 2>/dev/null || \
    warn "  Failed to copy skygate.db (container may not be running)"

# 4. Headscale config
if [ -d /home/skyadmin/headscale/config ]; then
    mkdir -p "${BACKUP_PATH}/headscale-config"
    cp -r /home/skyadmin/headscale/config/* "${BACKUP_PATH}/headscale-config/" 2>/dev/null
    log "  Headscale config backed up"
fi

# 5. Headscale DB (from Docker volume)
log "Backing up Headscale database..."
docker run --rm -v headscale_headscale_data:/data -v "${BACKUP_PATH}:/backup" alpine \
    sh -c "find /data -name '*.db' -exec cp {} /backup/ \; 2>/dev/null; ls -la /backup/*.db 2>/dev/null" || \
    warn "  Failed to copy headscale.db"

# 6. Headplane data
docker run --rm -v headscale_headplane_data:/data -v "${BACKUP_PATH}:/backup" alpine \
    sh -c "cp -r /data /backup/headplane-data 2>/dev/null; echo done" 2>/dev/null || \
    warn "  Failed to copy headplane data"

# 7. DERP config
if [ -f /var/lib/derper/derper.conf ]; then
    cp /var/lib/derper/derper.conf "${BACKUP_PATH}/derper.conf" 2>/dev/null && log "  DERP config backed up"
fi
if [ -f /var/lib/derpmap/derpmap.json ]; then
    cp /var/lib/derpmap/derpmap.json "${BACKUP_PATH}/derpmap.json" 2>/dev/null
fi

# 8. Docker compose files
cp "${SKYGATE_DIR}/docker-compose.yml" "${BACKUP_PATH}/docker-compose.yml" 2>/dev/null || true
cp "${SKYGATE_DIR}/Dockerfile" "${BACKUP_PATH}/Dockerfile" 2>/dev/null || true

# 9. NPM backup (if credentials available)
# npm credentials not stored — skip

# 10. Create inventory
cat > "${BACKUP_PATH}/inventory.txt" << INVEOF
Skygate Full Backup — ${DATE_TAG}
==================================
- skygate-repo.bundle      Git repository (full)
- skygate.env              Environment variables (secrets!)
- skygate.db               SQLite database
- headscale-config/        Headscale YAML + ACL configs
- headscale.db             Headscale SQLite database (if available)
- headplane-data/          Headplane state
- derper.conf              DERP relay config
- derpmap.json             DERP map config
- docker-compose.yml       Skygate compose file
- Dockerfile               Skygate Dockerfile
- skygate-git-log.txt      Recent commits
INVEOF
log "Inventory created"

# Package
cd "${BACKUP_DIR}"
tar czf "skygate-full-${DATE_TAG}.tar.gz" "skygate-full-${DATE_TAG}" 2>/dev/null
rm -rf "skygate-full-${DATE_TAG}"
BACKUP_FILE="${BACKUP_DIR}/skygate-full-${DATE_TAG}.tar.gz"
BACKUP_SIZE=$(du -h "${BACKUP_FILE}" | cut -f1)
SHA256=$(sha256sum "${BACKUP_FILE}" | cut -d' ' -f1)

echo ""
echo "=============================================="
echo "  Backup complete!"
echo "  File: ${BACKUP_FILE}"
echo "  Size: ${BACKUP_SIZE}"
echo "  SHA256: ${SHA256}"
echo "=============================================="

# Optional: upload to Synology SMB
if [ -n "${SYNO_USER:-}" ] && [ -n "${SYNO_PASS:-}" ]; then
    log "Uploading to Synology SMB..."
    SMB_PATH="//SYNYA/home/backup/skygate/"
    smbclient "${SMB_PATH}" -U "${SYNO_USER}%${SYNO_PASS}" \
        -c "put \"${BACKUP_FILE}\" \"skygate-full-${DATE_TAG}.tar.gz\"" 2>/dev/null && \
        log "  Uploaded to ${SMB_PATH}" || \
        warn "  SMB upload failed (check credentials)"
fi

echo ""
echo "To restore: ./scripts/restore.sh ${BACKUP_FILE}"
