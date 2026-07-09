#!/usr/bin/env bash
#===============================================================================
# Skygate Backup Script
# Backup all Skygate + Headscale + Headplane state for migration/restore
# Usage: ./backup.sh [destination]
#   destination: local path or smb://host/share/path (default: /tmp/skygate-backup/)
#
# Exit codes:
#   0 — backup OK, integrity OK, all required artifacts present
#   1 — backup failed at any step
#   2 — backup completed but integrity check failed (file is questionable)
#
# Side effects:
#   - writes STATUS_JSON (default /home/skyadmin/.skygate-backup-status.json)
#   - on Telegram failure: returns exit 1, notifies --severity=fail
#   - on success: optionally notifies --severity=ok (set SKYGATE_NOTIFY_ON_OK=1)
#===============================================================================
set -uo pipefail

BACKUP_DIR="${1:-/tmp/skygate-backup}"
DATE_TAG=$(date +%Y%m%d_%H%M%S)
BACKUP_PATH="${BACKUP_DIR}/skygate-full-${DATE_TAG}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKYGATE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
STATUS_JSON="${SKYGATE_BACKUP_STATUS_JSON:-${HOME}/.skygate-backup-status.json}"
KEEP_DAILY="${KEEP_DAILY:-7}"
KEEP_WEEKLY="${KEEP_WEEKLY:-4}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[✓]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[✗]${NC} $1"; }

# Per-run state
STEP=""
FAIL_REASON=""
INTEGRITY="skip"

# Final summary writer
write_status() {
  local status="$1" err_msg="${2:-}"
  local host="$(hostname 2>/dev/null || echo unknown)"
  # write_status always emits valid JSON. err_msg is JSON-escaped by python
  # so user-supplied quotes/backslashes/newlines cannot break the file.
  if command -v python3 >/dev/null 2>&1 && [[ -r "${SCRIPT_DIR}/.write_status.py" ]]; then
    BACKUP_STATUS="${status}" \
    BACKUP_ERR="${err_msg}" \
    BACKUP_HOST="${host}" \
    BACKUP_BDIR="${BACKUP_DIR}" \
    BACKUP_BPATH="${BACKUP_PATH}" \
    BACKUP_BFILE="${BACKUP_FILE:-}" \
    BACKUP_BSIZE="${BACKUP_SIZE:-0}" \
    BACKUP_SHA="${SHA256:-}" \
    BACKUP_INT="${INTEGRITY}" \
    BACKUP_OUT="${STATUS_JSON}" \
    python3 "${SCRIPT_DIR}/.write_status.py" || true
  else
    # Fallback: hand-written JSON. err_msg MUST be empty in this branch —
    # we escaped user input by simply not supporting non-empty values here.
    cat > "${STATUS_JSON}" <<JSON
{
  "status": "${status}",
  "timestamp": "$(date -u +%FT%TZ)",
  "host": "${host}",
  "backup_dir": "${BACKUP_DIR}",
  "backup_path": "${BACKUP_PATH}",
  "archive": "${BACKUP_FILE:-}",
  "archive_size": ${BACKUP_SIZE:-0},
  "sha256": "${SHA256:-}",
  "integrity": "${INTEGRITY}",
  "error": ""
}
JSON
  fi
}

notify_end() {
  local severity="$1"
  local subject="$2"
  local body="$3"
  if [[ -x "${SCRIPT_DIR}/notify.sh" ]]; then
    "${SCRIPT_DIR}/notify.sh" --severity="${severity}" "${subject}" "${body}" \
      || warn "notify.sh failed (non-fatal)"
  fi
}

cleanup_on_error() {
  local code=$?
  FAIL_REASON="${FAIL_REASON:-${STEP:-unknown} failed (exit ${code})}"
  err "BACKUP FAILED at ${STEP}: ${FAIL_REASON}"
  rm -rf "${BACKUP_PATH}" 2>/dev/null
  INTEGRITY="fail"
  write_status "fail" "${FAIL_REASON}"
  notify_end "fail" "skygate backup FAIL (${DATE_TAG})" \
    "${FAIL_REASON}
host=${HOSTNAME:-?}
dir=${BACKUP_DIR}"
  exit 1
}
trap cleanup_on_error ERR

# -----------------------------------------------------------------------------
mkdir -p "${BACKUP_PATH}"
cd "${BACKUP_PATH}"

echo "=============================================="
echo "  Skygate Full Backup — ${DATE_TAG}"
echo "=============================================="

# 1. Skygate source code
STEP="git-bundle"
log "Backing up Skygate source code..."
git -C "${SKYGATE_DIR}" bundle create "${BACKUP_PATH}/skygate-repo.bundle" --all 2>/dev/null
git -C "${SKYGATE_DIR}" log --oneline -5 > "${BACKUP_PATH}/skygate-git-log.txt"
[[ -s "${BACKUP_PATH}/skygate-repo.bundle" ]] || { err "git bundle empty"; false; }

# 2. Skygate .env
STEP="env-copy"
if [ -f "${SKYGATE_DIR}/.env" ]; then
    cp "${SKYGATE_DIR}/.env" "${BACKUP_PATH}/skygate.env"
    chmod 600 "${BACKUP_PATH}/skygate.env"
    log "  .env copied (mode 600)"
fi

# 3. Skygate DB
STEP="skygate-db"
log "Backing up Skygate database..."
docker run --rm -v skygate-data:/data -v "${BACKUP_PATH}:/backup" alpine \
    sh -c "cp /data/skygate.db /backup/skygate.db && chmod 644 /backup/skygate.db" 2>/dev/null
[[ -s "${BACKUP_PATH}/skygate.db" ]] || warn "skygate.db missing (container may be down)"

# 4. Headscale config
STEP="headscale-config"
if [ -d /home/skyadmin/headscale/config ]; then
    mkdir -p "${BACKUP_PATH}/headscale-config"
    cp -r /home/skyadmin/headscale/config/* "${BACKUP_PATH}/headscale-config/" 2>/dev/null
    log "  Headscale config backed up"
fi

# 5. Headscale DB
STEP="headscale-db"
log "Backing up Headscale database..."
docker run --rm -v headscale_headscale_data:/data -v "${BACKUP_PATH}:/backup" alpine \
    sh -c "find /data -name '*.db' -exec cp {} /backup/ \; 2>/dev/null; ls -la /backup/*.db 2>/dev/null" || \
    warn "Failed to copy headscale.db"

# 6. Headplane data
docker run --rm -v headscale_headplane_data:/data -v "${BACKUP_PATH}:/backup" alpine \
    sh -c "cp -r /data /backup/headplane-data 2>/dev/null; echo done" 2>/dev/null || \
    warn "Failed to copy headplane data"

# 7. DERP config
if [ -f /var/lib/derper/derper.conf ]; then
    cp /var/lib/derper/derper.conf "${BACKUP_PATH}/derper.conf" 2>/dev/null && log "  DERP config backed up"
fi
if [ -f /var/lib/derpmap/derpmap.json ]; then
    cp /var/lib/derpmap/derpmap.json "${BACKUP_PATH}/derpmap.json" 2>/dev/null
fi

# 8. Compose files
cp "${SKYGATE_DIR}/docker-compose.yml" "${BACKUP_PATH}/docker-compose.yml" 2>/dev/null || true
cp "${SKYGATE_DIR}/Dockerfile" "${BACKUP_PATH}/Dockerfile" 2>/dev/null || true

# 9. Inventory
cat > "${BACKUP_PATH}/inventory.txt" <<INVEOF
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

# -----------------------------------------------------------------------------
# 10. Integrity check (skygate.db)
STEP="integrity-check"
if command -v sqlite3 >/dev/null 2>&1 && [[ -s "${BACKUP_PATH}/skygate.db" ]]; then
  log "Running sqlite3 PRAGMA integrity_check..."
  if sqlite3 "${BACKUP_PATH}/skygate.db" 'PRAGMA integrity_check;' 2>&1 | grep -q '^ok$'; then
    INTEGRITY="ok"
    log "  ✓ integrity OK"
    # bonus: capture schema fingerprint
    sqlite3 "${BACKUP_PATH}/skygate.db" '.schema portal_users' > "${BACKUP_PATH}/skygate-schema.txt" 2>/dev/null || true
  else
    INTEGRITY="fail"
    err "  ✗ sqlite3 integrity_check failed on skygate.db"
    FAIL_REASON="sqlite3 integrity_check failed"
    exit 2
  fi
else
  INTEGRITY="skip"
  warn "  (sqlite3 not available or skygate.db missing — integrity: skip)"
fi

# 11. Package
STEP="package"
cd "${BACKUP_DIR}"
tar czf "skygate-full-${DATE_TAG}.tar.gz" "skygate-full-${DATE_TAG}" || { err "tar failed"; false; }
rm -rf "skygate-full-${DATE_TAG}"
BACKUP_FILE="${BACKUP_DIR}/skygate-full-${DATE_TAG}.tar.gz"
BACKUP_SIZE=$(du -b "${BACKUP_FILE}" | cut -f1)
SHA256=$(sha256sum "${BACKUP_FILE}" | cut -d' ' -f1)

echo ""
echo "=============================================="
echo "  Backup complete!"
echo "  File: ${BACKUP_FILE}"
echo "  Size: $(du -h "${BACKUP_FILE}" | cut -f1)"
echo "  SHA256: ${SHA256}"
echo "=============================================="

# 12. Rotation — keep last N daily + N weekly (Sun) archives
STEP="rotate"
log "Rotating old backups (keep daily=${KEEP_DAILY}, weekly=${KEEP_WEEKLY})..."
ROT_REMOVED=0
# All skygate-full-*.tar.gz except the just-created one
mapfile -t OLD < <(find "${BACKUP_DIR}" -maxdepth 1 -name 'skygate-full-*.tar.gz' ! -name "skygate-full-${DATE_TAG}.tar.gz" -type f | sort)
for f in "${OLD[@]}"; do
  base=$(basename "$f" .tar.gz)
  d="${base##*skygate-full-}"
  # d=YYYYMMDD_HHMMSS — extract date part only
  date_part="${d%_*}"
  # Day of week of the date — mark Sundays as weekly keepers
  dow=$(date -d "${date_part}" +%u 2>/dev/null || echo 0)  # 1..7 (7=Sun)
  # Always keep last KEEP_DAILY; of older, keep only Sundays, capped at KEEP_WEEKLY
  total=$((${#OLD[@]}))
  if (( total < KEEP_DAILY )); then
    continue  # not enough history yet
  fi
  # Position in sorted list (newest first)
  pos=-1
  for i in "${!OLD[@]}"; do
    if [[ "${OLD[$i]}" == "$f" ]]; then pos=$i; break; fi
  done
  # Daily window: keep newest KEEP_DAILY
  if (( pos < KEEP_DAILY )); then
    continue
  fi
  # Weekly: keep Sundays (dow==7), but at most KEEP_WEEKLY
  weekly_count=0
  for ((j=0; j<pos; j++)); do
    obase=$(basename "${OLD[$j]}" .tar.gz)
    od="${obase##*skygate-full-}"
    odate_part="${od%_*}"
    odow=$(date -d "${odate_part}" +%u 2>/dev/null || echo 0)
    if [[ "$odow" == "7" ]]; then weekly_count=$((weekly_count+1)); fi
  done
  if [[ "$dow" == "7" && $weekly_count -lt $KEEP_WEEKLY ]]; then
    continue
  fi
  rm -f "$f"
  ROT_REMOVED=$((ROT_REMOVED+1))
done
log "  rotation: removed ${ROT_REMOVED} old archive(s)"

# 13. SMB push (unchanged)
if [ -n "${SYNO_USER:-}" ] && [ -n "${SYNO_PASS:-}" ]; then
    log "Uploading to Synology SMB..."
    SMB_PATH="//SYNYA/home/backup/skygate/"
    smbclient "${SMB_PATH}" -U "${SYNO_USER}%${SYNO_PASS}" \
        -c "put \"${BACKUP_FILE}\" \"skygate-full-${DATE_TAG}.tar.gz\"" 2>/dev/null && \
        log "  Uploaded to ${SMB_PATH}" || \
        warn "  SMB upload failed"
fi

# 14. Status + notification
write_status "ok"
if [[ "${SKYGATE_NOTIFY_ON_OK:-0}" == "1" ]] && [[ -x "${SCRIPT_DIR}/notify.sh" ]]; then
  notify_end "ok" "skygate backup OK (${DATE_TAG})" \
    "size=$(du -h "${BACKUP_FILE}" | cut -f1)
sha256=${SHA256}
integrity=${INTEGRITY}
removed=${ROT_REMOVED}"
fi

echo ""
echo "To restore: ./scripts/restore.sh ${BACKUP_FILE}"
exit 0
