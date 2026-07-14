#!/usr/bin/env bash
#===============================================================================
# Skygate Backup — cron installer + runner
#
# Subcommands:
#   install        Install crontab entries (daily 03:00 — calls
#                  `skygate backup-run` which reads the config from
#                  the DB and applies the admin's chosen protocol +
#                  keep_count).
#   uninstall      Remove Skygate entries from crontab.
#   status         Show installed entries + last backup result.
#   run            Run backup now (via `skygate backup-run`).
#   prune          Force rotation prune without taking a new backup
#                  (only meaningful when the destination keeps
#                  more archives than the keep_count; the in-app
#                  scheduler already prunes on each run).
#
# All subcommands honor SKYGATE_NOTIFY_ON_OK=1 to notify on every successful
# backup (default: only on failure).
#
# 2026-07-14: Этап 14 v6 — switched from calling backup.sh directly
# to `skygate backup-run`. The skygate binary is the single source of
# truth for the destination/protocol/keep_count/credentials (stored
# in global_settings). backup.sh still exists and is invoked by
# RunBackup as the actual tarball/integrity-check step, but the
# cron entry no longer has to know the destination path.
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKYGATE_BIN="${SKYGATE_BIN:-/usr/local/bin/skygate}"
NOTIFY_SH="${SCRIPT_DIR}/notify.sh"
STATUS_JSON="${SKYGATE_BACKUP_STATUS_JSON:-${HOME}/.skygate-backup-status.json}"
CRON_TAG="# skygate-backup-cron"

ACTION="${1:-status}"
HOST="$(hostname)"

usage() {
  sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

cmd="${ACTION}"
case "${cmd}" in
  -h|--help|help) usage ;;
esac

log()  { echo "[$(date +%FT%T)] $*"; }
warn() { echo "[$(date +%FT%T)] $*" >&2; }

do_install() {
  if [[ ! -x "${SKYGATE_BIN}" ]]; then
    warn "install: ${SKYGATE_BIN} not found or not executable; aborting"
    warn "  build with: cd /home/skyadmin/skygate && go build -o ${SKYGATE_BIN} ./cmd/skygate"
    exit 1
  fi
  log "install: ${SKYGATE_BIN} (config from DB; no env-var destination)"
  # Build the crontab lines. Cron runs in a near-empty env, so we set PATH.
  local cron_lines
  cron_lines=$(
    cat <<CRON
${CRON_TAG} daily (config from /admin/backup page)
0 3 * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin SKYGATE_NOTIFY_ON_OK=1 ${SKYGATE_BIN} backup-run >>${HOME}/.skygate-backup.log 2>&1
${CRON_TAG} prune (Sunday 04:00 — only needed if you keep more than keep_count archives for any reason)
0 4 * * 0 PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin find ${STATUS_JSON%/*} -maxdepth 1 -name 'skygate-full-*.tar.gz' -mtime +30 -delete 2>/dev/null
${CRON_TAG} end-marker
CRON
  )
  # Add to crontab idempotently: remove old entries first
  local cur_tmp new_tmp
  cur_tmp=$(mktemp); new_tmp=$(mktemp)
  crontab -l 2>/dev/null > "$cur_tmp" || true
  # strip existing skygate entries
  awk -v tag="${CRON_TAG}" 'index($0, tag) == 0' "$cur_tmp" > "$new_tmp"
  printf '%s\n' "$cron_lines" >> "$new_tmp"
  crontab "$new_tmp"
  rm -f "$cur_tmp" "$new_tmp"
  log "install: crontab updated"
  log "verify with: crontab -l | grep skygate"
  log "config the destination on /admin/backup (local / SMB / NFS / SFTP) and enable the master switch"
}

do_uninstall() {
  local cur_tmp new_tmp
  cur_tmp=$(mktemp); new_tmp=$(mktemp)
  crontab -l 2>/dev/null > "$cur_tmp" || true
  awk -v tag="${CRON_TAG}" 'index($0, tag) == 0' "$cur_tmp" > "$new_tmp"
  crontab "$new_tmp"
  rm -f "$cur_tmp" "$new_tmp"
  log "uninstall: skygate crontab entries removed"
}

do_run() {
  if [[ ! -x "${SKYGATE_BIN}" ]]; then
    warn "run: ${SKYGATE_BIN} not found or not executable"
    exit 1
  fi
  log "run: ${SKYGATE_BIN} backup-run"
  if SKYGATE_NOTIFY_ON_OK="${SKYGATE_NOTIFY_ON_OK:-1}" "${SKYGATE_BIN}" backup-run; then
    log "run: OK"
  else
    rc=$?
    warn "run: skygate backup-run exited ${rc}"
    exit "${rc}"
  fi
}

do_prune() {
  # Read the destination from the status JSON if available, else
  # fall back to the legacy default. We don't keep a separate
  # env var for the destination because the source of truth is
  # the DB.
  local target_dir="${SKYGATE_BACKUP_DIR:-/home/skyadmin/skygate-backups}"
  if [[ -r "${STATUS_JSON}" ]]; then
    local dest
    dest=$(grep -oE '"destination":"[^"]+"' "${STATUS_JSON}" 2>/dev/null | head -1 | sed 's/.*:"\(.*\)"/\1/' || true)
    if [[ -n "${dest}" && -d "${dest}" ]]; then
      target_dir="${dest}"
    fi
  fi
  log "prune: removing backups older than 30 days in ${target_dir}"
  find "${target_dir}" -maxdepth 1 -name 'skygate-full-*.tar.gz' -mtime +30 -print -delete 2>/dev/null
}

do_status() {
  echo "=== Skygate backup status ==="
  if [[ -r "${STATUS_JSON}" ]]; then
    echo "status.json (${STATUS_JSON}):"
    cat "${STATUS_JSON}"
  else
    echo "no ${STATUS_JSON} — never ran?"
  fi
  echo
  echo "=== crontab entries ==="
  crontab -l 2>/dev/null | grep -F "${CRON_TAG}" || echo "  (none installed)"
  echo
  echo "=== skygate binary ==="
  if [[ -x "${SKYGATE_BIN}" ]]; then
    "${SKYGATE_BIN}" version
  else
    echo "  ${SKYGATE_BIN} not found or not executable"
  fi
}

case "${cmd}" in
  install)   do_install ;;
  uninstall) do_uninstall ;;
  run)       do_run ;;
  prune)     do_prune ;;
  status)    do_status ;;
  *)         echo "unknown command: ${cmd}"; usage; exit 2 ;;
esac
