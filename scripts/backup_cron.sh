#!/usr/bin/env bash
#===============================================================================
# Skygate Backup — cron installer + runner
#
# Subcommands:
#   install        Install crontab entries (daily 03:00, weekly rotation prune
#                  at 04:00 Sunday) under the caller's crontab.
#   uninstall      Remove Skygate entries from crontab.
#   status         Show installed entries + last backup result.
#   run            Run backup now (same as ./backup.sh) and report status.
#   prune          Force rotation prune without taking a new backup.
#
# All subcommands honor SKYGATE_NOTIFY_ON_OK=1 to notify on every successful
# backup (default: only on failure).
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BACKUP_SH="${SCRIPT_DIR}/backup.sh"
NOTIFY_SH="${SCRIPT_DIR}/notify.sh"
STATUS_JSON="${SKYGATE_BACKUP_STATUS_JSON:-${HOME}/.skygate-backup-status.json}"
CRON_TAG="# skygate-backup-cron"

ACTION="${1:-status}"
HOST="$(hostname)"
TARGET_DIR="${SKYGATE_BACKUP_DIR:-/home/skyadmin/skygate-backups}"

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

cmd="${ACTION}"
case "${cmd}" in
  -h|--help|help) usage ;;
esac

log()  { echo "[$(date +%FT%T)] $*"; }
warn() { echo "[$(date +%FT%T)] $*" >&2; }

do_install() {
  mkdir -p "${TARGET_DIR}"
  log "install: target dir ${TARGET_DIR}"
  # Build the crontab lines. Cron runs in a near-empty env, so we set PATH.
  local cron_lines
  cron_lines=$(
    cat <<CRON
${CRON_TAG} daily
0 3 * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin SKYGATE_NOTIFY_ON_OK=1 ${BACKUP_SH} ${TARGET_DIR} >>${HOME}/.skygate-backup.log 2>&1
${CRON_TAG} prune (Sunday 04:00)
0 4 * * 0 PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin find ${TARGET_DIR} -maxdepth 1 -name 'skygate-full-*.tar.gz' -mtime +30 -delete 2>/dev/null
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
  mkdir -p "${TARGET_DIR}"
  log "run: ${BACKUP_SH} ${TARGET_DIR}"
  if SKYGATE_NOTIFY_ON_OK="${SKYGATE_NOTIFY_ON_OK:-1}" "${BACKUP_SH}" "${TARGET_DIR}"; then
    log "run: OK"
  else
    rc=$?
    warn "run: backup.sh exited ${rc}"
    exit "${rc}"
  fi
}

do_prune() {
  log "prune: removing backups older than 30 days in ${TARGET_DIR}"
  find "${TARGET_DIR}" -maxdepth 1 -name 'skygate-full-*.tar.gz' -mtime +30 -print -delete 2>/dev/null
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
  echo "=== latest archive in ${TARGET_DIR} ==="
  ls -la "${TARGET_DIR}"/skygate-full-*.tar.gz 2>/dev/null | tail -3 || echo "  (no archives yet)"
}

case "${cmd}" in
  install)   do_install ;;
  uninstall) do_uninstall ;;
  run)       do_run ;;
  prune)     do_prune ;;
  status)    do_status ;;
  *)         echo "unknown command: ${cmd}"; usage; exit 2 ;;
esac
