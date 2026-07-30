#!/bin/bash
# scripts/recover_db_corruption.sh — one-off recovery for the
# skygate DB corruption discovered on 2026-07-30.
#
# What happened:
#   acl_snapshots and exit_rule_logs tables had btree-page
#   damage. PRAGMA integrity_check returned "database disk
#   image is malformed (11)". REINDEX failed, VACUUM INTO
#   produced an empty file. No backup existed at the time.
#
# This script:
#   1. Stops the skygate container (releases SQLite lock)
#   2. Backs up the corrupted DB
#   3. Recreates the broken tables empty
#   4. Restarts the container
#   5. Triggers a reapply via the admin session
#
# The data in those two tables is LOST (unavoidable). Other
# tables (audit_log, portal_users, device_rules, etc.)
# remain intact.
#
# Operator must know SKYGATE_ADMIN_USER and SKYGATE_ADMIN_PASS
# (set in /home/admin/skygate/.env).
#
# 2026-07-30: created in response to the v0.32.3 R9 SKIP
# incident. Reusable if the corruption recurs.

set -eu

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"
SSH_KEY="${SSH_KEY:-/home/knaga/.ssh/id_ed25519}"
SSH="ssh -i $SSH_KEY -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes $SSH_HOST"
ADMIN_USER=$($SSH 'grep SKYGATE_ADMIN_USER /home/admin/skygate/.env | cut -d= -f2-')
ADMIN_PASS=$($SSH 'grep SKYGATE_ADMIN_PASS /home/admin/skygate/.env | cut -d= -f2-')
ADMIN_USER=${ADMIN_USER:-admin}

echo "=== 1. Stop skygate ==="
$SSH 'cd /home/admin/skygate && sudo docker compose stop skygate' 2>&1 | tail -2

echo
echo "=== 2. Backup the corrupted DB ==="
TS=$(date -u +%Y%m%d_%H%M%S)
BACKUP_DIR="/var/backups/skygate/PRE_RECOVERY_$TS"
$SSH "sudo mkdir -p $BACKUP_DIR
sudo cp /home/admin/skygate/data/skygate.db $BACKUP_DIR/skygate.db
sudo chmod 644 $BACKUP_DIR/skygate.db
ls -la $BACKUP_DIR"
echo "  backup: $BACKUP_DIR/skygate.db"

echo
echo "=== 3. Recreate broken tables empty ==="
$SSH "sqlite3 /home/admin/skygate/data/skygate.db <<'SQL'
DROP TABLE IF EXISTS acl_snapshots;
CREATE TABLE acl_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version INTEGER NOT NULL,
    config TEXT NOT NULL,
    created_by TEXT NOT NULL,
    applied_success INTEGER DEFAULT NULL,
    error_msg TEXT DEFAULT '',
    created_at INTEGER DEFAULT (strftime('%s','now'))
);
DROP TABLE IF EXISTS exit_rule_logs;
CREATE TABLE exit_rule_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version INTEGER NOT NULL,
    action TEXT NOT NULL,
    detail TEXT DEFAULT '',
    created_at INTEGER DEFAULT (strftime('%s','now'))
);
SELECT 'acl_snapshots' as t, COUNT(*) FROM acl_snapshots
UNION ALL SELECT 'exit_rule_logs', COUNT(*) FROM exit_rule_logs;
PRAGMA integrity_check;
SQL"

echo
echo "=== 4. Restart skygate ==="
$SSH 'cd /home/admin/skygate && sudo docker compose up -d --no-deps skygate' 2>&1 | tail -2

echo
echo "=== 5. Wait for /healthz ==="
for i in $(seq 1 60); do
    if $SSH 'curl -fsS http://localhost:8080/healthz >/dev/null 2>&1'; then
        echo "  healthy after ${i}x5s"
        break
    fi
    sleep 5
done

echo
echo "=== 6. Login as admin + trigger reapply ==="
COOKIE="/tmp/_recover_ck.txt"
$SSH "rm -f $COOKIE
curl -s -c $COOKIE -X POST http://localhost:8080/login \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d 'username=$ADMIN_USER&password=$ADMIN_PASS' -o /dev/null -w 'login %{http_code}\n'
curl -s -b $COOKIE -X POST http://localhost:8080/admin/exit-rules/reapply -o /dev/null -w 'reapply %{http_code}\n'
rm -f $COOKIE"

echo
echo "=== Done. Wait 3s for the reapply goroutine, then run verify-post. ==="
echo "  Note: if R30 (integrity check) still FAILS, exit_rule_logs was"
echo "  corrupted AGAIN by the reapply. Run this script in a loop until"
echo "  R30 passes — each loop drops+recreates the table. The root cause"
echo "  of the recurring corruption is still under investigation"
echo "  (see docs/BACKLOG.md Priority 8)."
