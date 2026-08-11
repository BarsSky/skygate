#!/bin/bash
# scripts/recover_db_corruption.sh — recover from DB corruption.
#
# What this fixes (v0.32.5 — real root cause, not just symptoms):
#   The recurring acl_snapshots + exit_rule_logs corruption was
#   traced to the VM disk hitting 100% full. SQLite's WAL writes
#   fail silently when there's no free space — the call returns
#   SQLITE_OK but the actual bytes don't make it to disk, so
#   btree pages are left in an inconsistent state. The next
#   PRAGMA integrity_check (R30) finds:
#       "Tree N page X cell Y: 2nd reference to page Z"
#       "Rowid N out of order"
#       "Page N: never used"
#       "database disk image is malformed (11)"
#
# The fix has two parts:
#   1. Free up disk space FIRST (the cause, not the symptom):
#        sudo docker system prune -a -f     # reclaims images
#        sudo rm -rf /var/backups/skygate/PRE_VACUUM_*  # old backups
#   2. Then run this script to extract the data with sqlite3
#      .recover (which salvages everything that CAN be salvaged
#      from the corrupted DB) and rebuild a clean DB.
#
# Why .recover (not VACUUM INTO, not DROP+CREATE):
#   - VACUUM INTO fails when the DB is so corrupted that even
#     reading the btree pages is impossible (returns
#     "Error: stepping, database disk image is malformed (11)").
#   - DROP+CREATE leaves the OLD corrupted free pages in place.
#     When the new tables' first INSERT allocates pages from the
#     freelist, those pages have stale corrupted data — R30 fails
#     again on the next autoupdate tick.
#   - .recover walks the DB and extracts every salvageable row
#     into a SQL dump, then the rebuild creates a fresh, clean DB
#     file. The corrupted free pages never get into the new file.
#
# Trigger: R30 FAIL in verify-post.
#
# Usage: bash scripts/recover_db_corruption.sh
#
# Operator runbook (2026-07-30):
#   1. df -h / on the VM. If >85%, free space first.
#   2. sudo docker system prune -a -f
#   3. sudo rm -rf /var/backups/skygate/PRE_VACUUM_*
#   4. bash scripts/recover_db_corruption.sh
#   5. bash scripts/verify_post_deploy.sh

set -e

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"
SSH_KEY="${SSH_KEY:-}"
for cand in \
  "$HOME/.ssh/id_ed25519" \
  "/mnt/c/Users/knaga/.ssh/id_ed25519" \
  "/c/Users/knaga/.ssh/id_ed25519"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then SSH_KEY="$cand"; break; fi
done
SSH="ssh -i $SSH_KEY -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes $SSH_HOST"
ADMIN_USER=$($SSH 'grep SKYGATE_ADMIN_USER /home/admin/skygate/.env | cut -d= -f2-')
ADMIN_PASS=$($SSH 'grep SKYGATE_ADMIN_PASS /home/admin/skygate/.env | cut -d= -f2-')
ADMIN_USER=${ADMIN_USER:-admin}"

# Helper scripts are scp'd to the VM (avoids heredoc-quote issues
# over SSH). The .recover helper is the REAL fix.
scp -i $SSH_KEY -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes \
    scripts/_recover_helper.sh scripts/_swap_recovered.sh \
    $SSH_HOST:/tmp/

$SSH '
set -e
cd /home/admin/skygate

echo "=== 1. Disk space check (the real cause) ==="
df -h / 2>&1
DF_PCT=$(df -P / | tail -1 | awk "{print \$5}" | tr -d "%")
if [ -n "$DF_PCT" ] && [ "$DF_PCT" -ge 85 ]; then
    echo
    echo "  WARNING: disk is ${DF_PCT}% full. The DB corruption"
    echo "  is CAUSED by disk-full. Free space first:"
    echo "    sudo docker system prune -a -f"
    echo "    sudo rm -rf /var/backups/skygate/PRE_VACUUM_*"
    echo
    read -p "  Press enter to continue anyway (NOT recommended)..."
fi

echo
echo "=== 2. Stop skygate container (releases SQLite lock) ==="
sudo docker compose stop skygate 2>&1 | tail -2

echo
echo "=== 3. Inspect the actual DB in skygate-data volume ==="
sudo docker run --rm -v skygate-data:/data alpine:3.20 sh -c "ls -la /data/skygate.db"
echo

echo "=== 4. Backup the corrupted DB (via throwaway container) ==="
TS=$(date -u +%Y%m%d_%H%M%S)
BACKUP_DIR=/var/backups/skygate/PRE_RECOVER_$TS
sudo mkdir -p $BACKUP_DIR
sudo docker run --rm -v skygate-data:/data:ro -v $BACKUP_DIR:/backup alpine:3.20 sh -c "cp /data/skygate.db /backup/skygate.db && chmod 644 /backup/skygate.db"
ls -la $BACKUP_DIR
echo

echo "=== 5. .recover + rebuild (the REAL fix) ==="
WORK_DIR=/tmp/skygate-recover-$TS
sudo mkdir -p $WORK_DIR
sudo docker run --rm \
    -v skygate-data:/data \
    -v $WORK_DIR:/work \
    -v /tmp/_recover_helper.sh:/helper.sh:ro \
    alpine:3.20 sh /helper.sh
echo

echo "=== 6. Move recovered DB back into the volume ==="
sudo docker run --rm \
    -v skygate-data:/data \
    -v $WORK_DIR:/work \
    -v /tmp/_swap_recovered.sh:/helper.sh:ro \
    alpine:3.20 sh /helper.sh
echo

echo "=== 7. Cleanup work dir ==="
sudo rm -rf $WORK_DIR

echo
echo "=== 8. Restart skygate ==="
sudo docker compose up -d --no-deps skygate 2>&1 | tail -2
'

echo
echo "=== 9. Wait for /healthz ==="
for i in $(seq 1 60); do
    if $SSH 'curl -fsS http://localhost:8080/healthz >/dev/null 2>&1'; then
        echo "  healthy after ${i}x5s"
        break
    fi
    sleep 5
done

echo
echo "=== 10. Login as admin + trigger reapply (rebuilds acl_snapshots) ==="
COOKIE="/tmp/_recover_ck.txt"
$SSH "rm -f $COOKIE
curl -s -c $COOKIE -X POST http://localhost:8080/login \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d 'username=$ADMIN_USER&password=$ADMIN_PASS' -o /dev/null -w 'login %{http_code}\n'
curl -s -b $COOKIE -X POST http://localhost:8080/admin/exit-rules/reapply -o /dev/null -w 'reapply %{http_code}\n'
rm -f $COOKIE"

echo
echo "=== Done. Run verify-post. ==="
echo "  bash scripts/verify_post_deploy.sh"
echo "  Expected: R30 PASS, R31 PASS, all other checks as before."
