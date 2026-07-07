#!/usr/bin/env bash
#===============================================================================
# Skygate Restore Script
# Restore full Skygate + Headscale + Headplane state from backup archive
# Usage: ./restore.sh <backup-file.tar.gz> [target-dir]
#   target-dir: where to restore (default: /home/skyadmin/skygate/)
#===============================================================================
set -euo pipefail

if [ $# -lt 1 ]; then
    echo "Usage: $0 <backup-file.tar.gz> [target-dir]"
    echo "  target-dir defaults to /home/skyadmin/skygate/"
    exit 1
fi

BACKUP_FILE="$1"
TARGET_DIR="${2:-/home/skyadmin/skygate}"
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

echo ""
echo "=============================================="
echo "  What to restore?"
echo "=============================================="
echo "  1) Skygate source code (git bundle → clone)"
echo "  2) Skygate .env (configuration with secrets)"
echo "  3) Skygate database (skygate.db → Docker volume)"
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

do_skygate_db() {
    if [ -f skygate.db ]; then
        log "Restoring Skygate database..."
        docker run --rm -v skygate-data:/data -v "$(pwd):/restore" alpine \
            sh -c "cp /restore/skygate.db /data/skygate.db && chown -R 1000:1000 /data" 2>/dev/null && \
            log "  skygate.db restored to Docker volume" || \
            warn "  DB restore failed (is container running?)"
    fi
}

do_headscale_config() {
    if [ -d headscale-config ]; then
        log "Restoring Headscale config..."
        mkdir -p /home/skyadmin/headscale/config
        cp -r headscale-config/* /home/skyadmin/headscale/config/
        log "  Headscale config restored"
        warn "  Restart headscale: sudo docker restart headscale"
    fi
}

do_headscale_db() {
    local DB_FILE
    DB_FILE=$(ls headscale*.db 2>/dev/null | head -1)
    if [ -n "${DB_FILE}" ]; then
        log "Restoring Headscale database..."
        # Copy via temporary container
        docker run --rm -v headscale_headscale_data:/data -v "$(pwd):/restore" alpine \
            sh -c "find /data -name '*.db' -exec cp /restore/${DB_FILE} {} \; 2>/dev/null" && \
            log "  Headscale DB restored" || \
            warn "  Headscale DB restore failed"
        warn "  Restart headscale: sudo docker restart headscale"
    fi
}

do_headplane() {
    if [ -d headplane-data ]; then
        log "Restoring Headplane data..."
        docker run --rm -v headscale_headplane_data:/data -v "$(pwd):/restore" alpine \
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
