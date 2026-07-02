#!/usr/bin/env bash
#===============================================================================
# Skygate Migration Script
# Migrate full stack to a new machine
# Usage:
#   On OLD machine: ./scripts/backup.sh /tmp/skygate-migration
#   Copy tar.gz to NEW machine
#   On NEW machine: ./scripts/migrate.sh skygate-full-<date>.tar.gz
#   Or from Synology: ./scripts/migrate.sh --from-synology skygate-full-<date>.tar.gz
#===============================================================================
set -euo pipefail

BACKUP_FILE="${1:-}"
SYNO_USER="${SYNO_USER:-SkyAdmin}"
SYNO_PASS="${SYNO_PASS:-}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[✓]${NC} $1"; }
err()  { echo -e "${RED}[✗]${NC} $1"; exit 1; }

if [ -z "${BACKUP_FILE}" ]; then
    echo "Usage: $0 [backup-file.tar.gz | --from-synology <filename>]"
    echo ""
    echo "Options:"
    echo "  --from-synology <file>   Download backup from Synology SMB first"
    exit 1
fi

# Download from Synology if requested
if [ "${BACKUP_FILE}" = "--from-synology" ]; then
    REMOTE_FILE="${2:-}"
    if [ -z "${REMOTE_FILE}" ]; then
        err "Usage: $0 --from-synology <filename>"
    fi
    if [ -z "${SYNO_PASS}" ]; then
        read -s -p "Synology password (SkyAdmin): " SYNO_PASS
        echo
    fi
    log "Downloading from Synology SMB..."
    smbclient "//SYNYA/home/backup/skygate/" -U "${SYNO_USER}%${SYNO_PASS}" \
        -c "get ${REMOTE_FILE} /tmp/${REMOTE_FILE}" 2>/dev/null || \
        err "Failed to download from Synology"
    BACKUP_FILE="/tmp/${REMOTE_FILE}"
    log "Downloaded: ${BACKUP_FILE}"
fi

echo "=============================================="
echo "  Skygate Migration"
echo "=============================================="

echo ""
echo "Prerequisites check:"
echo "  [ ] Docker installed and running"
echo "  [ ] Git installed"
echo "  [ ] smbclient installed (for Synology pulls)"
echo "  [ ] headscale_default network exists"
echo "  [ ] Target user: skyadmin"
echo ""
read -p "Continue? [Y/n] " CONFIRM
CONFIRM="${CONFIRM:-Y}"
if [ "${CONFIRM}" != "Y" ] && [ "${CONFIRM}" != "y" ]; then
    exit 0
fi

# Step 1: Create directories
log "Creating directories..."
mkdir -p /home/skyadmin/skygate
mkdir -p /home/skyadmin/headscale/config
mkdir -p /var/lib/derper
mkdir -p /var/lib/derpmap

# Step 2: Run restore
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -f "${SCRIPT_DIR}/restore.sh" ]; then
    bash "${SCRIPT_DIR}/restore.sh" "${BACKUP_FILE}" /home/skyadmin/skygate
else
    err "restore.sh not found in ${SCRIPT_DIR}"
fi

# Step 3: Setup Docker network if needed
log "Setting up Docker network..."
docker network inspect headscale_default >/dev/null 2>&1 || \
    docker network create headscale_default --driver bridge --subnet 172.18.0.0/16 || \
    warn "  Network already exists or failed to create"

# Step 4: Start containers
log "Starting containers..."
cd /home/skyadmin/skygate
docker compose up -d --build 2>/dev/null || \
    warn "  docker compose up failed — check docker-compose.yml"

# Step 5: Verify
sleep 5
echo ""
echo "=============================================="
echo "  Verification"
echo "=============================================="
for ep in /login /dashboard; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:8080${ep}" 2>/dev/null || echo "FAIL")
    echo "  http://localhost:8080${ep} → ${CODE}"
done

echo ""
log "Migration complete!"
echo ""
echo "Post-migration:"
echo "  1. Verify DNS: skygate.skynas.ru → $(hostname -I 2>/dev/null | awk '{print $1}')"
echo "  2. Check NPM proxy settings on 192.168.13.67"
echo "  3. Tailscale nodes should re-connect automatically"
echo "  4. Update .env secrets if needed"
