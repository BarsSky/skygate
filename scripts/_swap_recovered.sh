#!/bin/sh
# Helper for recover_db_corruption.sh: swap the clean DB into the
# skygate-data volume, fix ownership, verify integrity.
set -e
apk add --no-cache sqlite >/dev/null 2>&1
cp /work/skygate.db /data/skygate.db
chown 1000:1000 /data/skygate.db
chmod 644 /data/skygate.db
ls -la /data/skygate.db
echo "---"
sqlite3 /data/skygate.db "PRAGMA integrity_check;"
