#!/usr/bin/env bash
# scripts/cleanup_smoke_artifacts.sh — v1.5.0+ / post-TD-9.
#
# Periodic cleanup of the smoke.sh test artifacts that
# scripts/smoke.sh leaves behind when it fails partway through.
#
# What gets left behind
# ---------------------
# smoke.sh creates a fresh test user `smoke_mesh_<pid>` and a fresh
# mesh `smoke-mesh-<pid>` for its step-13 multi-user mesh test
# (see scripts/smoke.sh:481-512). On a successful run both get
# deleted in step 13.8 (line 638-680). But on a failing run — or
# when smoke.sh is interrupted (Ctrl-C, VM reboot, network
# drop) — the user and the mesh stay in the live DB, growing
# by 2 rows per broken run.
#
# Also: smoke.sh's step 0.0 / step 11.6 create exit_rules for
# the admin user on "device 8" as part of the rate-limit + cleanup
# assertions. Those rules have a generated random pattern in the
# rule_value column (no `smoke_` prefix). The script intentionally
# does NOT touch them — they're harder to identify and the
# operator's manual cleanup is the right path (1 DELETE per
# identified rule, not 1 mass DELETE per day).
#
# Why a daily cron and not a per-run cleanup
# ------------------------------------------
# The pre-smoke.sh path was: re-run smoke.sh whenever something
# looks wrong. That works for a fresh dev DB but on the live
# operator VM the smoke artifacts accumulate over weeks because
# the run is gated on a fresh .env + a manual login. A daily
# cron is simpler: run at 04:00 local, leave at most 24h of
# smoke artifacts in the live DB even on a stalled smoke.sh.
#
# What this script does
# --------------------
# 1. Connects to the live skygate DB via `sudo -u postgres psql`
#    (matches the operator's clear_test_dsn.sh pattern from
#    B207-fix). The DB name is hardcoded to `skygate_staging`
#    (the operator's canonical DB name — the smoke artifacts
#    go to whatever the live skygate process talks to, and the
#    live process reads SKYGATE_DB_DSN which points at
#    skygate_staging on this host).
# 2. SELECTs the candidate rows BEFORE deleting (so the log
#    shows what got removed; idempotent + auditable).
# 3. Wraps the DELETE in a single transaction (either all
#    candidates go or none; if the connection drops mid-run
#    the DB is left exactly as it was).
# 4. Filters by `created_at < NOW() - INTERVAL '24 hours'`
#    so a smoke.sh run that started 5 minutes ago is NOT
#    killed while in flight.
# 5. Logs a 1-line summary to stdout (for the systemd timer
#    journal) and a more detailed breakdown for the operator.
#
# Cron setup (operator's choice — pick one)
# ----------------------------------------
#
# Option A — systemd timer (preferred, idempotent on the host):
#
#   sudo cp deploy/systemd/skymate-cleanup-smoke.{service,timer} \
#           /etc/systemd/system/
#   sudo systemctl daemon-reload
#   sudo systemctl enable --now skymate-cleanup-smoke.timer
#
#   # verify: systemctl list-timers skymate-cleanup-smoke.timer
#
# Option B — crontab (one-liner, no systemd):
#
#   echo "0 4 * * * /home/skyadmin/skygate/scripts/cleanup_smoke_artifacts.sh >> /var/log/skygate-cleanup.log 2>&1" \
#       | sudo crontab -u skyadmin -
#
# Both call this script with the same env. The script is
# idempotent (a 2nd run the same day is a no-op — finds 0
# candidates older than 24h that haven't already been
# deleted today).
#
# Exit codes
# ----------
#   0 = success (with or without deletions)
#   1 = psql connection failed (DB is down, network issue, etc.)
#   2 = no candidates found at all (informational — usually
#       means the script is being run on a fresh DB or the
#       cleanup already happened earlier today)

set -u

PSQL="sudo -u postgres psql -d skygate_staging -t -A -v ON_ERROR_STOP=1"

# Show before/after for the audit trail.
echo "=== smoke-artifact cleanup $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
echo

# Sanity: psql must work at all. If the DB is down, fail
# loudly so the cron doesn't silently mark the run as
# "success" in systemd.
if ! $PSQL -c "SELECT 1" >/dev/null 2>&1; then
    echo "ERROR: psql connection to skygate_staging failed" >&2
    echo "  - is the postgres service running?" >&2
    echo "  - does the postgres role have access to skygate_staging?" >&2
    echo "  - is the DB name still 'skygate_staging'? (the clear_test_dsn.sh script uses the same name; check scripts/clear_test_dsn.sh:36)" >&2
    exit 1
fi

# Find candidates older than 24h. The 24h grace window means
# a smoke.sh run that started 5 minutes ago is not killed
# while in flight (the smoke run takes ~10s typical, but we
# keep 24h to be safe against VM pauses + overnight runs).
echo "--- candidates (smoke_mesh_* users + smoke-mesh-* meshes older than 24h) ---"
CANDIDATES=$($PSQL -c "
    SELECT 'user', username, to_char(created_at, 'YYYY-MM-DD HH24:MI:SS')
    FROM portal_users
    WHERE username LIKE 'smoke_mesh_%'
      AND created_at < NOW() - INTERVAL '24 hours'
    UNION ALL
    SELECT 'mesh', name, to_char(created_at, 'YYYY-MM-DD HH24:MI:SS')
    FROM meshes
    WHERE name LIKE 'smoke-mesh-%'
      AND created_at < NOW() - INTERVAL '24 hours'
    ORDER BY 1, 3;
" 2>&1) || {
    echo "ERROR: candidate SELECT failed" >&2
    echo "$CANDIDATES" >&2
    exit 1
}

if [ -z "$CANDIDATES" ]; then
    n_users=0
    n_meshes=0
    echo "(no candidates found — nothing to clean)"
    echo
    echo "=== summary: 0 users + 0 meshes deleted (DB is already clean) ==="
    exit 0
fi

n_users=$(echo "$CANDIDATES" | grep -c "^user|" || true)
n_meshes=$(echo "$CANDIDATES" | grep -c "^mesh|" || true)
echo "found $n_users users + $n_meshes meshes to delete"
echo "$CANDIDATES" | head -20
if [ "$(echo "$CANDIDATES" | wc -l)" -gt 20 ]; then
    echo "... ($(echo "$CANDIDATES" | wc -l) total)"
fi
echo

# The deletion is a single transaction. ON DELETE CASCADE on
# the meshes table handles mesh_members (the CASCADE on
# portal_users → devices, preauth_keys, exit_rules, audit
# rows is implicit via the portal_users.id foreign keys).
#
# Why BOTH deletes are in one transaction: if the user
# delete succeeds but the mesh delete fails (or vice versa)
# the script is supposed to be all-or-nothing. CASCADE on
# the schema means the user delete triggers device + rule
# + token deletes; the mesh delete triggers mesh_members
# deletes. If we ran them separately, a failure between
# the two would leave the DB in a half-cleaned state
# that's hard to recover from without re-running.
#
# We also write a single audit_log row so the operator can
# see "when did the last cleanup happen" without ssh-ing.
# The audit row uses the special "system" username (the
# skygate process normally writes audit rows with a real
# user_id; the cleanup script uses id=1 or a dedicated
# "system" user if it exists; falls back to id=0 if no
# system user is present).
$PSQL <<SQL 2>&1
BEGIN;
-- Delete meshes first (CASCADE drops mesh_members, no
-- foreign-key back to portal_users). The WHERE clause
-- mirrors the SELECT above.
DELETE FROM meshes
WHERE name LIKE 'smoke-mesh-%'
  AND created_at < NOW() - INTERVAL '24 hours';
-- Then delete users (CASCADE drops devices, preauth_keys,
-- exit_rules, audit rows for this user — but NOT the
-- audit_log row we're about to insert, because that's
-- written AFTER the user is gone).
DELETE FROM portal_users
WHERE username LIKE 'smoke_mesh_%'
  AND created_at < NOW() - INTERVAL '24 hours';
-- Audit row. We use user_id=1 (the canonical "admin" user
-- on a fresh skygate) so the audit_log row is attributable
-- to a real row, not NULL. If user_id=1 has been deleted
-- in some operator-side customization, the audit_log row
-- will be in a weird state but the cleanup still happened
-- — better to log it than to abort the whole transaction
-- because the audit row failed.
INSERT INTO audit_log (user_id, username, action, detail, created_at)
VALUES (
    COALESCE((SELECT id FROM portal_users WHERE is_admin = 1 ORDER BY id LIMIT 1), 1),
    'system_cleanup',
    'smoke_artifacts_purge',
    'deleted ${n_users} users + ${n_meshes} meshes older than 24h',
    EXTRACT(EPOCH FROM NOW())::bigint
);
COMMIT;
SQL
RC=$?

if [ $RC -ne 0 ]; then
    echo "ERROR: cleanup transaction failed (rc=$RC)" >&2
    echo "  the transaction was rolled back — DB is unchanged" >&2
    exit 1
fi

echo "=== summary: $n_users users + $n_meshes meshes deleted ==="
echo "audit_log row written (action=smoke_artifacts_purge)"
