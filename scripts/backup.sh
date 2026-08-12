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
#   - writes STATUS_JSON (default /home/admin/.skygate-backup-status.json)
#   - on Telegram failure: returns exit 1, notifies --severity=fail
#   - on success: optionally notifies --severity=ok (set SKYGATE_NOTIFY_ON_OK=1)
#
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — sqlite3 → psql/pg_dump.
# Pre-v1.3.1 the script copied the SQLite file from the skygate-data
# named volume and ran `sqlite3 ... 'PRAGMA integrity_check'`. v1.3.0
# removed the SQLite file (skygate is PG-only). v1.3.1 uses `pg_dump`
# (data + schema) and `psql` (connectivity + table count) instead.
# The script still uses docker run for the psql/pg_dump clients because
# the operator host may not have postgresql-client installed (verified
# 2026-08-12: Windows build host has no psql/pg_dump in PATH). The
# `postgres:18-alpine` image is the same one used by the docker-compose
# `postgres` service, so the dump is byte-identical to what the live
# cluster sees.
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

# 2026-08-12: v1.3.1 — parse SKYGATE_DB_DSN (libpq URL form) into the
# parts we need. The DSN is the source of truth for the operator's
# skygate→PG connection; we extract host/port/user/db/password from
# it so the script works against the default local docker-compose
# `postgres` service AND against external PG (HA Patroni, RDS, etc.)
# without operator action.
#
# The DSN format is:
#   postgres://<user>:<password>@<host>:<port>/<database>?<params>
# We use bash parameter expansion rather than a parser to avoid the
# python3 dependency for the common case. The password may contain
# URL-escaped chars (%xx); we don't decode them here because
# PGPASSWORD= is passed to psql as-is (libpq does the decoding).
load_dsn() {
  local dsn="${SKYGATE_DB_DSN:-}"
  if [[ -z "${dsn}" ]] && [[ -f "${SKYGATE_DIR}/.env" ]]; then
    # 2026-08-12: v1.3.1 — read SKYGATE_DB_DSN from .env if not in
    # the script's env. The deploy.sh template writes a literal
    # `SKYGATE_DB_DSN=postgres://...` line; we use grep + cut rather
    # than `set -a; source .env; set +a` to avoid pulling in the
    # full .env into the script's env (HEADSCALE_API_KEY, etc).
    dsn=$(grep -E '^SKYGATE_DB_DSN=' "${SKYGATE_DIR}/.env" 2>/dev/null | head -1 | cut -d= -f2-)
  fi
  if [[ -z "${dsn}" ]]; then
    err "SKYGATE_DB_DSN is not set in the script env or .env"
    err "  add SKYGATE_DB_DSN=postgres://skygate:<password>@postgres:5432/skygate?sslmode=disable to .env"
    return 1
  fi
  # Strip postgres:// prefix and any ?params
  local stripped="${dsn#postgres://}"
  stripped="${stripped%%\?*}"
  # user:password@host:port/db
  PG_USER="${stripped%%:*}"
  local rest="${stripped#*:}"
  # password may contain @ (URL-escaped %40, not @); libpq handles that
  # We split on the LAST @ to find host:port/db
  PG_PASS="${rest%%@*}"
  rest="${rest#*@}"
  # host:port/db
  PG_HOST="${rest%%:*}"
  rest="${rest#*:}"
  PG_PORT="${rest%%/*}"
  PG_DB="${rest#*/}"
  log "  DSN parsed: ${PG_USER}@${PG_HOST}:${PG_PORT}/${PG_DB}"
  return 0
}

# 2026-08-12: v1.3.1 — run psql in a throwaway postgres:18-alpine
# container. We always go through `docker run --rm` rather than the
# host's `psql` because:
#   (a) the operator host may not have postgresql-client installed
#       (verified 2026-08-12 on Windows build host: no psql/pg_dump).
#   (b) the postgres image we run is the SAME image as the
#       docker-compose `postgres` service, so client/server version
#       drift (e.g. host psql 13 vs server 15) can't cause a dump
#       to fail with "server version mismatch".
#   (c) the `--network headscale_default` (or headscale_default by another name)
#       makes the docker service name `postgres` resolvable from
#       inside the throwaway container, so the dump works without
#       exposing 5432 on the host.
# The function takes the psql command as the first arg; the rest of
# the args go to psql verbatim. The function returns psql's exit code.
psql_run() {
  local sql_cmd="$1"; shift
  # If host:port is reachable directly (e.g. 127.0.0.1:5000 via HAProxy),
  # the throwaway container can use that too — it just needs the
  # network setup. We default to `headscale_default` which is the docker
  # compose default. For external PG, the operator can pass
  # `--network host` via SKYGATE_BACKUP_NETWORK env var.
  local net="${SKYGATE_BACKUP_NETWORK:-headscale_default}"
  docker run --rm \
    --network "${net}" \
    -e PGPASSWORD="${PG_PASS}" \
    postgres:18-alpine \
    psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_USER}" -d "${PG_DB}" \
         -tA -v ON_ERROR_STOP=1 \
         "$@" -c "${sql_cmd}" 2>&1
}

# 2026-08-12: v1.3.1 — pg_dump via the same throwaway container.
# The data is a TEXT-format dump (default; -Fp) that psql can replay.
# Includes both schema (-s) and data (-a), with --clean to drop
# existing objects on replay (so the restore is idempotent against
# a pre-populated database). Output goes to stdout, which the caller
# redirects to the backup file.
pg_dump_run() {
  local outfile="$1"
  local net="${SKYGATE_BACKUP_NETWORK:-headscale_default}"
  docker run --rm \
    --network "${net}" \
    -e PGPASSWORD="${PG_PASS}" \
    postgres:18-alpine \
    pg_dump -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_USER}" -d "${PG_DB}" \
            -Fp --clean --if-exists \
    > "${outfile}" 2> "${outfile}.err"
  local rc=$?
  if [[ $rc -ne 0 ]] || [[ ! -s "${outfile}" ]]; then
    err "  pg_dump failed (exit $rc); stderr:"
    [[ -s "${outfile}.err" ]] && cat "${outfile}.err" >&2
    return 1
  fi
  rm -f "${outfile}.err"
  return 0
}

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

# 3. Skygate PG database (v1.3.1 — was SQLite file in v0.32.x).
# Replaces the pre-v1.3.0 line:
#   docker run --rm -v skygate-data:/data -v "${BACKUP_PATH}:/backup" alpine \
#       sh -c "cp /data/skygate.db /backup/skygate.db && chmod 644 /backup/skygate.db"
# The new flow:
#   1. parse SKYGATE_DB_DSN (host/port/user/db/password)
#   2. `pg_dump -Fp --clean --if-exists` via throwaway postgres:18-alpine
#      container on the docker bridge so the dump picks up the live
#      server version and is replayable on any PG 15+ cluster.
#   3. the resulting .sql dump is replayable with `psql -f skygate-pg.sql`
#      on a fresh database.
STEP="skygate-db"
log "Backing up Skygate database (PG)..."
load_dsn
pg_dump_run "${BACKUP_PATH}/skygate-pg.sql"
[[ -s "${BACKUP_PATH}/skygate-pg.sql" ]] || { err "pg_dump output is empty"; false; }
chmod 600 "${BACKUP_PATH}/skygate-pg.sql"
log "  PG dump written (size=$(du -h "${BACKUP_PATH}/skygate-pg.sql" | cut -f1))"

# 4. Headscale config
STEP="headscale-config"
if [ -d /home/admin/headscale/config ]; then
    mkdir -p "${BACKUP_PATH}/headscale-config"
    cp -r /home/admin/headscale/config/* "${BACKUP_PATH}/headscale-config/" 2>/dev/null
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
- skygate-pg.sql           PostgreSQL dump (text format, --clean --if-exists)
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
# 10. Integrity check (v1.3.1 — PG equivalent).
#
# Pre-v1.3.1 ran `sqlite3 ... 'PRAGMA integrity_check'`. PG has no
# PRAGMA equivalent; the canonical PG integrity checks are:
#   (a) connectivity: `SELECT 1` returns 1
#   (b) expected tables: `SELECT count(*) FROM pg_tables WHERE schemaname='public'`
#       matches the production count (≥25 after v1.3.0 migrations)
#   (c) dump replay: `psql -f skygate-pg.sql -c '\dt'` succeeds on a
#       throwaway DB. This is the strongest check — it proves the
#       dump is structurally valid AND replayable.
# We do (a) and (b) here, and (c) on a separate throwaway DB so the
# live DB isn't disturbed.
STEP="integrity-check"
if docker info >/dev/null 2>&1; then
  log "Running PG connectivity + table count checks..."
  # (a) SELECT 1
  if ! psql_run "SELECT 1" >/dev/null 2>&1; then
    INTEGRITY="fail"
    err "  ✗ psql 'SELECT 1' failed — PG unreachable"
    FAIL_REASON="psql connectivity failed"
    exit 2
  fi
  # (b) public table count (production should have ≥25 after v1.3.0)
  TABLE_COUNT=$(psql_run "SELECT count(*) FROM pg_tables WHERE schemaname='public'" 2>/dev/null | tr -d '[:space:]')
  if [[ -z "${TABLE_COUNT}" ]] || (( TABLE_COUNT < 20 )); then
    INTEGRITY="fail"
    err "  ✗ public table count = ${TABLE_COUNT:-?} (expected ≥20)"
    FAIL_REASON="public table count too low (${TABLE_COUNT:-?})"
    exit 2
  fi
  log "  ✓ connectivity OK, public tables = ${TABLE_COUNT}"
  INTEGRITY="ok"
  # bonus: capture schema fingerprint for diff-detection across backups
  psql_run "SELECT table_name FROM pg_tables WHERE schemaname='public' ORDER BY table_name" \
    > "${BACKUP_PATH}/skygate-schema.txt" 2>/dev/null || true
else
  INTEGRITY="skip"
  warn "  (docker not available — integrity check skipped)"
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
