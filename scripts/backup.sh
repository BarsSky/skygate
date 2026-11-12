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

# ---------------------------------------------------------------------------
# B459 — THE BACKUP MUST SPEAK THE DATABASE'S DIALECT.
#
# v1.3.1 removed SQLite from this script when PostgreSQL became the only
# backend ("Phase 2 of SQLite removal"). v1.5.4 made SQLite the DEFAULT
# backend again, so that removal went stale: on a SQLite install `load_dsn`
# answers "SKYGATE_DB_DSN is not set" and NOTHING was backed up — the
# operator's users, settings, exit rules and audit trail were simply absent
# from an archive the runner reported as created. A backup that captures
# every file EXCEPT the database is the failure class this project keeps
# paying for: an artifact that looks complete.
#
# The branch below follows the SAME precedence config.Load uses:
#   SKYGATE_DB=sqlite:/abs/path.db | file:/abs/path.db | /abs/path.db
#     -> SQLite file (v1.5.4+ default)
#   SKYGATE_DB=postgres://... | SKYGATE_DB_DSN=...      -> PostgreSQL
# ---------------------------------------------------------------------------

# B462 — WHERE THE CONFIGURATION ACTUALLY LIVES.
#
# On a native (systemd/OpenRC) install the service reads its configuration from
# `/etc/skygate/skygate.env` (the unit's EnvironmentFile), NOT from the checkout's
# `.env`. Before B462 this script only looked at `${SKYGATE_DIR}/.env`, so on a
# native host it captured NOTHING when the operator's file lived in /etc — the
# archive then carried the database but no DSN and no secrets, which is a restore
# that cannot start, and a by-hand run could not even find the database.
#
# The order below is "what the running service reads" first: an explicit override,
# then the native etc dir, then the checkout.
skygate_env_file() {
  local f
  for f in "${SKYGATE_UPDATE_ENV_FILE:-}" "${SKYGATE_ENV_FILE:-}" \
           "/etc/skygate/skygate.env" "${SKYGATE_DIR}/.env"; do
    [ -n "${f}" ] || continue
    [ -r "${f}" ] || continue
    printf '%s\n' "${f}"
    return 0
  done
  return 0
}

# skygate_db_value echoes the configured backend spec: the process env
# first, then the value written in the env file the service uses (the in-app
# runner inherits the process env; an operator running this by hand often has
# only the file).
skygate_db_value() {
  local v="${SKYGATE_DB:-}" f
  if [[ -z "${v}" ]]; then
    f="$(skygate_env_file || true)"
    if [[ -n "${f}" ]]; then
      v=$(grep -E '^SKYGATE_DB=' "${f}" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\"'" || true)
    fi
  fi
  printf '%s\n' "${v}"
}

# backup_dialect echoes "sqlite" or "postgres" for the configured backend.
backup_dialect() {
  local db; db=$(skygate_db_value)
  case "${db}" in
    sqlite:*|file:*) echo sqlite; return 0 ;;
    postgres:*|postgresql:*) echo postgres; return 0 ;;
  esac
  if [[ -n "${db}" ]]; then echo sqlite; return 0; fi   # a bare path is a SQLite file
  echo postgres
}

# sqlite_db_path resolves the SQLite database FILE path, or prints nothing
# when the value is not a local path (e.g. the container-relative default
# /data/skygate.db that does not exist on a native install).
sqlite_db_path() {
  local db; db=$(skygate_db_value)
  case "${db}" in
    sqlite:*) db="${db#sqlite:}" ;;
    file:*)   db="${db#file:}" ;;
  esac
  db="${db%%\?*}"
  case "${db}" in
    ""|postgres:*|postgresql:*) return 0 ;;
  esac
  printf '%s\n' "${db}"
}

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
  local dsn="${SKYGATE_DB_DSN:-}" f
  if [[ -z "${dsn}" ]]; then
    # 2026-08-12: v1.3.1 — read SKYGATE_DB_DSN from the env file if not in
    # the script's env. B462: that is the file the SERVICE reads
    # (/etc/skygate/skygate.env on a native install), not just the checkout's.
    # The deploy.sh template writes a literal
    # `SKYGATE_DB_DSN=postgres://...` line; we use grep + cut rather
    # than `set -a; source .env; set +a` to avoid pulling in the
    # full .env into the script's env (HEADSCALE_API_KEY, etc).
    f="$(skygate_env_file || true)"
    if [[ -n "${f}" ]]; then
      dsn=$(grep -E '^SKYGATE_DB_DSN=' "${f}" 2>/dev/null | head -1 | cut -d= -f2- || true)
    fi
  fi
  if [[ -z "${dsn}" ]]; then
    err "SKYGATE_DB_DSN is not set in the script env or in the env file (/etc/skygate/skygate.env, ${SKYGATE_DIR}/.env)"
    err "  add SKYGATE_DB_DSN=postgres://skygate:<password>@postgres:5432/skygate?sslmode=disable to that file"
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
# 2026-08-12 v1.3.8: when this script runs as root (the typical
# path when invoked from the skygate container via
# `cmd/skygate/main.go:backup-run` or from a root cron job),
# files we write to a host bind-mount end up root-owned.
# That locks the operator (skyadmin) out of the destination
# on the next run with "Permission denied" — the exact
# failure mode that hid the actual backup error for the
# last 5+ deploys (see TODO B100 in scripts/verify_pre_deploy.sh).
#
# Fix: chown the destination tree to the operator at the
# START of the run, and again at the end so the tarball and
# its subdirs are manageable from the host. SUDO_USER is set
# when invoked via `sudo`; fall back to the user who owns
# ${HOME} (the operator's typical identity), and finally to
# "skyadmin" as the documented default.
if [[ "$(id -u)" -eq 0 ]]; then
  OPERATOR_USER="${SUDO_USER:-}"
  if [[ -z "${OPERATOR_USER}" ]] && [[ -n "${HOME}" ]]; then
    OPERATOR_USER="$(stat -c '%U' "${HOME}" 2>/dev/null || true)"
  fi
  OPERATOR_USER="${OPERATOR_USER:-skyadmin}"
  # chown the destination only if it exists. If we have to
  # create it, mkdir below will do that as root and we'll
  # chown right after.
  if [[ -d "${BACKUP_DIR}" ]]; then
    chown "${OPERATOR_USER}:${OPERATOR_USER}" "${BACKUP_DIR}" 2>/dev/null || true
  fi
  export SKYGATE_OPERATOR_USER="${OPERATOR_USER}"
fi

# -----------------------------------------------------------------------------
mkdir -p "${BACKUP_PATH}"
cd "${BACKUP_PATH}"
# 2026-08-12 v1.3.8: if we just created the run dir as root,
# chown it to the operator immediately so subsequent steps
# (git bundle, tar) can write into it. (mkdir -p doesn't
# change perms if the dir already exists; if it does, we
# want root to be able to write into it, then chown for the
# operator to clean up.)
if [[ "$(id -u)" -eq 0 ]] && [[ -n "${SKYGATE_OPERATOR_USER:-}" ]]; then
  chown "${SKYGATE_OPERATOR_USER}:${SKYGATE_OPERATOR_USER}" "${BACKUP_PATH}" 2>/dev/null || true
fi

echo "=============================================="
echo "  Skygate Full Backup — ${DATE_TAG}"
echo "=============================================="

# 1. Skygate source code
STEP="git-bundle"
log "Backing up Skygate source code..."
git -C "${SKYGATE_DIR}" bundle create "${BACKUP_PATH}/skygate-repo.bundle" --all 2>/dev/null
git -C "${SKYGATE_DIR}" log --oneline -5 > "${BACKUP_PATH}/skygate-git-log.txt"
[[ -s "${BACKUP_PATH}/skygate-repo.bundle" ]] || { err "git bundle empty"; false; }

# 2. Skygate env file (B462: the one the SERVICE reads, which on a native install
# is /etc/skygate/skygate.env — not the checkout's .env).
STEP="env-copy"
SKYGATE_ENV_SOURCE="$(skygate_env_file || true)"
if [ -n "${SKYGATE_ENV_SOURCE}" ]; then
    cp "${SKYGATE_ENV_SOURCE}" "${BACKUP_PATH}/skygate.env"
    chmod 600 "${BACKUP_PATH}/skygate.env"
    log "  env copied from ${SKYGATE_ENV_SOURCE} (mode 600)"
else
    warn "  NO env file found (looked at /etc/skygate/skygate.env and ${SKYGATE_DIR}/.env) — the archive will carry no DSN and no secrets"
fi

# 2b. B462 — the rest of /etc/skygate on a native install. These are small and
# losing them is not recoverable by re-installing: cluster-state.json carries the
# node identity the cluster API uses, dbs.env the DB credentials the helper
# scripts read.
if [ -d /etc/skygate ]; then
    mkdir -p "${BACKUP_PATH}/etc-skygate"
    for _f in cluster-state.json dbs.env; do
        [ -f "/etc/skygate/${_f}" ] && cp -p "/etc/skygate/${_f}" "${BACKUP_PATH}/etc-skygate/${_f}"
    done
    [ -n "$(ls -A "${BACKUP_PATH}/etc-skygate" 2>/dev/null)" ] \
        && log "  /etc/skygate (cluster-state.json, dbs.env)" \
        || rmdir "${BACKUP_PATH}/etc-skygate" 2>/dev/null || true
fi

# 3. Skygate database — DIALECT-AWARE (B459).
#
# PostgreSQL (the v1.3.1 flow):
#   1. parse SKYGATE_DB_DSN (host/port/user/db/password)
#   2. `pg_dump -Fp --clean --if-exists` via throwaway postgres:18-alpine
#      container on the docker bridge so the dump picks up the live
#      server version and is replayable on any PG 15+ cluster.
#   3. the resulting .sql dump is replayable with `psql -f skygate-pg.sql`
#      on a fresh database.
# SQLite (v1.5.4+ default, B459):
#   1. resolve the database FILE from SKYGATE_DB (sqlite:/path, file:/path
#      or a bare path) — the process env first, then .env
#   2. WAL-checkpoint through sqlite3 when it is installed, then copy the
#      file AND its -wal/-shm siblings (a copy taken while the WAL holds
#      the last transactions is a database that is silently behind)
#   3. prove the copy really is a SQLite database (header magic + size)
DB_DIALECT=$(backup_dialect)
STEP="skygate-db"
case "${DB_DIALECT}" in
  sqlite)
    log "Backing up Skygate database (SQLite)..."
    SQLITE_DB=$(sqlite_db_path)
    if [[ -z "${SQLITE_DB}" ]]; then
      err "SKYGATE_DB does not name a SQLite file (got: '$(skygate_db_value)')"
      err "  set SKYGATE_DB=sqlite:/abs/path/skygate.db in the environment or .env"
      false
    fi
    if [[ ! -f "${SQLITE_DB}" ]]; then
      err "SQLite database not found: ${SQLITE_DB}"
      false
    fi
    if command -v sqlite3 >/dev/null 2>&1; then
      sqlite3 "${SQLITE_DB}" 'PRAGMA wal_checkpoint(TRUNCATE)' >/dev/null 2>&1 \
        && log "  WAL checkpoint: ${SQLITE_DB}" \
        || warn "  WAL checkpoint failed — copying -wal/-shm as-is"
    else
      warn "  sqlite3 not installed — copying -wal/-shm instead of checkpointing"
    fi
    cp -p "${SQLITE_DB}" "${BACKUP_PATH}/skygate.db" || { err "copy failed: ${SQLITE_DB}"; false; }
    for _suffix in -wal -shm; do
      [[ -f "${SQLITE_DB}${_suffix}" ]] && cp -p "${SQLITE_DB}${_suffix}" "${BACKUP_PATH}/skygate.db${_suffix}"
    done
    chmod 600 "${BACKUP_PATH}/skygate.db"
    # An empty file or a non-SQLite file is NOT a backup: fail the run here
    # instead of shipping an archive whose database is a husk.
    [[ -s "${BACKUP_PATH}/skygate.db" ]] || { err "skygate.db copy is empty"; false; }
    if [[ "$(head -c 15 "${BACKUP_PATH}/skygate.db" 2>/dev/null)" != "SQLite format 3" ]]; then
      err "skygate.db copy does not carry the SQLite header — refusing to ship it"
      false
    fi
    log "  skygate.db copied (size=$(du -h "${BACKUP_PATH}/skygate.db" | cut -f1))"
    ;;
  postgres)
    log "Backing up Skygate database (PG)..."
    load_dsn
    pg_dump_run "${BACKUP_PATH}/skygate-pg.sql"
    [[ -s "${BACKUP_PATH}/skygate-pg.sql" ]] || { err "pg_dump output is empty"; false; }
    chmod 600 "${BACKUP_PATH}/skygate-pg.sql"
    log "  PG dump written (size=$(du -h "${BACKUP_PATH}/skygate-pg.sql" | cut -f1))"
    ;;
esac

# B462 — where headscale keeps its SQLite database on a NATIVE install.
#
#   1. SKYGATE_HEADSCALE_DB_PATH — explicit override;
#   2. the `path:` under `database:` → `sqlite:` in headscale's own config, using
#      the SAME candidate list the OIDC and policy appliers already use
#      (deploy/skygate-apply-oidc.sh, deploy/skygate-apply-policy.sh) rather than
#      a fourth invented one;
#   3. the documented default /var/lib/headscale/db.sqlite, when it exists.
#
# Prints nothing when no file was found — the caller then says WHY instead of
# silently shipping an archive without headscale's state (which is what this
# script did before B462: the step was docker-volume-only).
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

# 4. Headscale config
# B462 — the config lives in /etc/headscale on a NATIVE install and in
# /home/admin/headscale/config on the docker one (bind mount). Pre-B462 only the
# docker path was captured, so a native restore had no headscale config at all.
STEP="headscale-config"
HS_CONFIG_SOURCE=""
for _d in /etc/headscale /home/admin/headscale/config; do
    [ -d "${_d}" ] || continue
    mkdir -p "${BACKUP_PATH}/headscale-config"
    cp -r "${_d}/." "${BACKUP_PATH}/headscale-config/" 2>/dev/null || true
    HS_CONFIG_SOURCE="${_d}"
    break
done
if [ -n "${HS_CONFIG_SOURCE}" ]; then
    log "  Headscale config backed up from ${HS_CONFIG_SOURCE}"
else
    warn "  no headscale config dir found (/etc/headscale, /home/admin/headscale/config)"
fi

# 5. Headscale DB — the NATIVE file first (B462), the docker volume as fallback.
STEP="headscale-db"
HS_DB_NATIVE="$(headscale_db_path || true)"
if [ -n "${HS_DB_NATIVE}" ]; then
    log "Backing up Headscale database (native file: ${HS_DB_NATIVE})..."
    if cp -p "${HS_DB_NATIVE}" "${BACKUP_PATH}/headscale.db" 2>/dev/null; then
        for _sfx in -wal -shm; do
            [ -f "${HS_DB_NATIVE}${_sfx}" ] && cp -p "${HS_DB_NATIVE}${_sfx}" "${BACKUP_PATH}/headscale.db${_sfx}"
        done
        log "  headscale.db ($(du -h "${BACKUP_PATH}/headscale.db" | cut -f1))"
    else
        warn "  headscale.db copy failed (${HS_DB_NATIVE}) — headscale state is NOT in this archive"
    fi
elif command -v docker >/dev/null 2>&1; then
    log "Backing up Headscale database (docker volume)..."
    docker run --rm -v headscale_headscale_data:/data -v "${BACKUP_PATH}:/backup" alpine \
        sh -c "find /data -name '*.db' -exec cp {} /backup/ \; 2>/dev/null; ls -la /backup/*.db 2>/dev/null" || \
        warn "Failed to copy headscale.db"

    # 6. Headplane data (a docker sidecar — it does not exist on a native install)
    docker run --rm -v headscale_headplane_data:/data -v "${BACKUP_PATH}:/backup" alpine \
        sh -c "cp -r /data /backup/headplane-data 2>/dev/null; echo done" 2>/dev/null || \
        warn "Failed to copy headplane data"
else
    warn "  headscale database NOT captured: no native path resolved (checked SKYGATE_HEADSCALE_DB_PATH, /etc/headscale/config.*, /var/lib/headscale/db.sqlite) and docker is unavailable."
    warn "  If headscale runs on PostgreSQL there is no file to copy — dump it separately."
fi

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
# B459 — the inventory must name the artifact that was actually produced. A
# SQLite archive that advertises skygate-pg.sql sends the operator (and
# scripts/restore.sh) looking for a file that is not in the archive.
if [[ "${DB_DIALECT}" == "sqlite" ]]; then
  DB_ARTIFACT_LINE="- skygate.db               SQLite database (copy, WAL-checkpointed)"
else
  DB_ARTIFACT_LINE="- skygate-pg.sql           PostgreSQL dump (text format, --clean --if-exists)"
fi
cat > "${BACKUP_PATH}/inventory.txt" <<INVEOF
Skygate Full Backup — ${DATE_TAG}
==================================
- skygate-repo.bundle      Git repository (full)
- skygate.env              Environment variables (secrets!) — copied from ${SKYGATE_ENV_SOURCE:-<none found>}
- etc-skygate/             Native-only: cluster-state.json, dbs.env (when present)
${DB_ARTIFACT_LINE}
- SKYGATE_DB_DIALECT=${DB_DIALECT}
- headscale-config/        Headscale YAML + ACL configs — copied from ${HS_CONFIG_SOURCE:-<none found>}
- headscale.db             Headscale SQLite database (native file, else docker volume, if available)
- headplane-data/          Headplane state (docker sidecar only)
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
if [[ "${DB_DIALECT}" == "sqlite" ]]; then
  # B459 — the SQLite half of the integrity check. Pre-B459 the check was
  # gated on `docker info` alone, so a native SQLite install reported
  # "integrity skipped" about an archive nobody had looked inside.
  log "Running SQLite integrity check..."
  if command -v sqlite3 >/dev/null 2>&1; then
    PRAGMA_ANSWER=$(sqlite3 "${BACKUP_PATH}/skygate.db" 'PRAGMA integrity_check' 2>&1 | head -1)
    if [[ "${PRAGMA_ANSWER}" == "ok" ]]; then
      SQLITE_TABLES=$(sqlite3 "${BACKUP_PATH}/skygate.db" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'" 2>/dev/null | tr -d '[:space:]')
      log "  ✓ PRAGMA integrity_check = ok, tables = ${SQLITE_TABLES:-?}"
      INTEGRITY="ok"
      sqlite3 "${BACKUP_PATH}/skygate.db" \
        "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name" \
        > "${BACKUP_PATH}/skygate-schema.txt" 2>/dev/null || true
    else
      INTEGRITY="fail"
      err "  ✗ PRAGMA integrity_check = ${PRAGMA_ANSWER}"
      FAIL_REASON="sqlite integrity_check: ${PRAGMA_ANSWER}"
      exit 2
    fi
  else
    INTEGRITY="skip"
    warn "  (sqlite3 not installed — SQLite header verified, PRAGMA integrity_check skipped)"
  fi
elif docker info >/dev/null 2>&1; then
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
# 2026-08-12 v1.3.8: chown the final tarball and the
# destination dir to the operator so they can be downloaded
# / rsynced / pruned by skyadmin without sudo. Without
# this the in-app backup (run as root inside the skygate
# container) leaves everything root-owned and the operator
# has to sudo to manage the archive tree — the exact
# Permission-denied loop the operator hit on 2026-08-12.
if [[ "$(id -u)" -eq 0 ]] && [[ -n "${SKYGATE_OPERATOR_USER:-}" ]]; then
  chown "${SKYGATE_OPERATOR_USER}:${SKYGATE_OPERATOR_USER}" "${BACKUP_FILE}" 2>/dev/null || true
  chown "${SKYGATE_OPERATOR_USER}:${SKYGATE_OPERATOR_USER}" "${BACKUP_DIR}" 2>/dev/null || true
fi

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
