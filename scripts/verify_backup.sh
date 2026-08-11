#!/bin/bash
# scripts/verify_backup.sh — v0.33.1.42 B2: weekly auto-verify
# the latest backup file via `sqlite3 ... "PRAGMA integrity_check"`.
#
# Cron: `0 4 * * 0` (Sundays at 04:00, off-peak). Picks the
# newest `skygate-*.tar.gz` in the configured destination,
# extracts the embedded SQLite DB to a temp file, runs the
# integrity check, sends a Telegram alert on failure.
#
# Why weekly: a healthy DB doesn't need daily checks. Weekly
# is enough to catch silent corruption (the v0.32.5 incident
# showed a 2-week window of recurring corruption) before the
# backup is needed for DR. The cron fires AFTER the daily
# 03:00 backup (1h later) so the freshest backup is the
# verification target.
#
# The script reads the backup destination from the SAME
# `global_settings` table the skygate UI uses (via
# `skygate backup-show-config` subcommand), so the cron and
# the in-app scheduler always target the same place. If
# destination is empty (backup feature not configured), the
# script is a no-op.

set -e

# Source the env for the Telegram bot token + chat id (same
# env the in-app Notifier uses). Fall back to the DB if the
# env is empty (the Notifier does this too).
SKYGATE_DIR="${SKYGATE_DIR:-/home/skyadmin/skygate}"
DEST="$("$SKYGATE_DIR/skygate" backup-show-config 2>/dev/null | grep '^destination=' | cut -d= -f2)"
if [ -z "$DEST" ]; then
    echo "verify_backup: backup.destination is empty in global_settings — nothing to verify"
    exit 0
fi

# Find the newest archive. The deploy/backup.sh naming
# convention is `skygate-YYYYMMDD-HHMMSS.tar.gz`.
LATEST=$(ls -t "$DEST"/skygate-*.tar.gz 2>/dev/null | head -1)
if [ -z "$LATEST" ]; then
    echo "verify_backup: no skygate-*.tar.gz archives in $DEST — backup hasn't run yet"
    exit 0
fi

echo "verify_backup: verifying $LATEST"

# Extract the SQLite DB to a temp file. The archive structure
# is `backup/skygate.db` (set by deploy/backup.sh). Use
# mktemp + a cleanup trap so we don't leak files on failure.
TMPDIR=$(mktemp -d)
trap "rm -rf '$TMPDIR'" EXIT
if ! tar -xzf "$LATEST" -C "$TMPDIR" 2>&1; then
    echo "verify_backup: ERROR — tar extract failed for $LATEST"
    "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "tar extract failed" 2>/dev/null || true
    exit 1
fi

DB_FILE="$TMPDIR/backup/skygate.db"
if [ ! -f "$DB_FILE" ]; then
    echo "verify_backup: ERROR — $LATEST did not contain backup/skygate.db"
    "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "missing backup/skygate.db" 2>/dev/null || true
    exit 1
fi

# Run PRAGMA integrity_check. Returns "ok" on a healthy DB
# or a list of errors on a corrupt one. alpine image has
# sqlite3; if missing, install on demand.
if ! command -v sqlite3 >/dev/null 2>&1; then
    apk add --no-cache sqlite 2>/dev/null || true
fi
RESULT=$(sqlite3 "$DB_FILE" "PRAGMA integrity_check;" 2>&1)
RC=$?

if [ "$RC" -ne 0 ] || [ "$RESULT" != "ok" ]; then
    echo "verify_backup: FAIL — integrity_check returned: $RESULT"
    "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "$RESULT" 2>/dev/null || true
    exit 1
fi

echo "verify_backup: OK — $LATEST is healthy (integrity_check=ok)"
"$SKYGATE_DIR/skygate" backup-verify-ok "$LATEST" 2>/dev/null || true
exit 0
