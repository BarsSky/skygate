#!/bin/bash
# scripts/verify_backup.sh — v1.3.1: weekly auto-verify the latest
# backup file by replaying the embedded PG dump into a throwaway
# postgres:15-alpine database and confirming the table count.
#
# Cron: `0 4 * * 0` (Sundays at 04:00, off-peak). Picks the newest
# `skygate-*.tar.gz` in the configured destination, extracts the
# embedded PG dump (skygate-pg.sql from the v1.3.1+ backup layout),
# spins up a throwaway postgres container, replays the dump with
# `psql -f`, and asserts the resulting DB has the expected public
# table count.
#
# Why weekly: a healthy DB doesn't need daily checks. Weekly is
# enough to catch silent corruption (the v0.32.5 SQLite incident
# was a 2-week window) before the backup is needed for DR. The
# cron fires AFTER the daily 03:00 backup (1h later) so the
# freshest backup is the verification target.
#
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — sqlite3 → psql.
# Pre-v1.3.1 the script extracted the embedded skygate.db and ran
# `sqlite3 ... 'PRAGMA integrity_check'`. v1.3.0 removed SQLite
# entirely; v1.3.1 extracts skygate-pg.sql and replays it into a
# throwaway postgres:15-alpine container on the same docker network
# as the live PG, then asserts the table count. This is a stronger
# check than the pre-v1.3.0 one (it proves the dump is structurally
# valid AND replayable, not just "the btree is consistent at the
# time of the dump").
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
# convention is `skygate-YYYYMMDD-HHMMSS.tar.gz` (v0.33.x naming)
# OR `skygate-full-YYYYMMDD_HHMMSS.tar.gz` (v1.3.1+ naming).
# We accept both so the script works during the v0.33 → v1.3
# upgrade window where older archives may still be on disk.
LATEST=$(ls -t "$DEST"/skygate-*.tar.gz "$DEST"/skygate-full-*.tar.gz 2>/dev/null | head -1)
if [ -z "$LATEST" ]; then
    echo "verify_backup: no skygate-*.tar.gz archives in $DEST — backup hasn't run yet"
    exit 0
fi

echo "verify_backup: verifying $LATEST"

# Extract the PG dump to a temp file. The v1.3.1+ archive structure
# is `skygate-full-YYYYMMDD_HHMMSS/skygate-pg.sql`. Pre-v1.3.0 had
# `backup/skygate.db` (SQLite); v1.3.1 only ships PG dumps. We
# detect the layout from the archive contents and bail with a
# clear error if the archive is from the SQLite era.
TMPDIR=$(mktemp -d)
trap "rm -rf '$TMPDIR'" EXIT
if ! tar -xzf "$LATEST" -C "$TMPDIR" 2>&1; then
    echo "verify_backup: ERROR — tar extract failed for $LATEST"
    "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "tar extract failed" 2>/dev/null || true
    exit 1
fi

# Find the PG dump file. v1.3.1 writes it as skygate-pg.sql at the
# archive root (the backup.sh structure: skygate-full-XXX/skygate-pg.sql).
# Older v0.33.x SQLite-era archives wrote skygate.db under backup/.
# We refuse to verify the SQLite era — the operator should
# re-run backup.sh post-v1.3.1 to get a fresh PG dump.
DUMP_FILE=$(find "$TMPDIR" -name 'skygate-pg.sql' -type f | head -1)
if [ -z "$DUMP_FILE" ]; then
    # SQLite-era archive? Detect skygate.db and bail with a clear
    # error pointing to the upgrade path.
    if find "$TMPDIR" -name 'skygate.db' -type f | grep -q .; then
        echo "verify_backup: ERROR — $LATEST is a v0.33.x SQLite-era archive"
        echo "  v1.3.0+ requires PG backups. Re-run scripts/backup.sh after upgrading."
        "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "sqlite-era archive (pre-v1.3.1)" 2>/dev/null || true
        exit 1
    fi
    echo "verify_backup: ERROR — $LATEST did not contain skygate-pg.sql (and not SQLite-era either)"
    "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "missing skygate-pg.sql" 2>/dev/null || true
    exit 1
fi

# Replay the dump into a throwaway postgres:15-alpine container.
# This is the strongest integrity check: a syntactically-valid
# dump with a btree-consistent table layout would still fail this
# if any CREATE/INSERT/GRANT references a missing column, type, or
# role. docker run --rm + the network the live PG is on (default:
# skygate-net; override via SKYGATE_VERIFY_BACKUP_NETWORK for HA
# setups where the throwaway must reach an external PG host — but
# in that case, the throwaway just runs `psql` directly via the
# host's path, not the throwaway container).
NET="${SKYGATE_VERIFY_BACKUP_NETWORK:-skygate-net}"
THROWAWAY_NAME="skygate-verify-$$-$(date +%s)"
cleanup_container() {
  docker rm -f "$THROWAWAY_NAME" 2>/dev/null || true
  rm -rf "$TMPDIR"
}
trap cleanup_container EXIT

# Boot a fresh postgres with the SAME image the live cluster uses
# (postgres:15-alpine). The dump's `--clean --if-exists` header
# drops existing objects, so this DB starts clean.
docker run --rm -d \
  --name "$THROWAWAY_NAME" \
  --network "${NET}" \
  -e POSTGRES_USER=verify \
  -e POSTGRES_PASSWORD=verify \
  -e POSTGRES_DB=verify \
  postgres:15-alpine >/dev/null

# Wait for postgres to accept connections (up to 30s).
WAITED=0
until docker exec "$THROWAWAY_NAME" pg_isready -U verify -d verify >/dev/null 2>&1; do
  sleep 1
  WAITED=$((WAITED+1))
  if [ "$WAITED" -ge 30 ]; then
    echo "verify_backup: ERROR — throwaway postgres didn't come up in 30s"
    "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "throwaway PG not ready" 2>/dev/null || true
    exit 1
  fi
done

# Replay the dump. We pipe via docker exec + psql so we don't need
# a local psql binary (matches the v1.3.1 backup.sh pattern).
if ! docker exec -i "$THROWAWAY_NAME" psql -U verify -d verify -v ON_ERROR_STOP=1 < "$DUMP_FILE" >/dev/null 2>"$TMPDIR/replay.err"; then
  echo "verify_backup: FAIL — dump replay failed (would not restore cleanly)"
  echo "  --- replay stderr (first 30 lines) ---"
  head -30 "$TMPDIR/replay.err" >&2 || true
  echo "  ----------------------------------------"
  ERR_MSG=$(head -1 "$TMPDIR/replay.err" 2>/dev/null | head -c 200)
  "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "replay failed: ${ERR_MSG}" 2>/dev/null || true
  exit 1
fi

# Assert the table count matches production (≥20 public tables
# after v1.3.0 migrations; the v0.33.x baseline was 18-20).
TABLE_COUNT=$(docker exec "$THROWAWAY_NAME" psql -U verify -d verify -tA -c \
  "SELECT count(*) FROM pg_tables WHERE schemaname='public'" 2>/dev/null | tr -d '[:space:]')
if [ -z "$TABLE_COUNT" ] || [ "$TABLE_COUNT" -lt 20 ]; then
  echo "verify_backup: FAIL — replayed DB has $TABLE_COUNT tables (expected ≥20)"
  "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "table count too low ($TABLE_COUNT)" 2>/dev/null || true
  exit 1
fi

# Sanity-check: a few critical tables are present (defends against
# silent dump corruption that still passes the count check).
for table in portal_users device_rules acl_snapshots applied_migrations; do
  if ! docker exec "$THROWAWAY_NAME" psql -U verify -d verify -tA -c \
      "SELECT to_regclass('public.${table}') IS NOT NULL" 2>/dev/null | grep -q '^t$'; then
    echo "verify_backup: FAIL — replayed DB is missing critical table 'public.${table}'"
    "$SKYGATE_DIR/skygate" backup-verify-fail "$LATEST" "missing table: ${table}" 2>/dev/null || true
    exit 1
  fi
done

echo "verify_backup: OK — $LATEST is healthy (replay OK, ${TABLE_COUNT} tables, critical tables present)"
"$SKYGATE_DIR/skygate" backup-verify-ok "$LATEST" 2>/dev/null || true
exit 0
