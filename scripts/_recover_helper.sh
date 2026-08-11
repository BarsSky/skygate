#!/bin/sh
# Helper for recover_db_corruption.sh: .recover the corrupted DB
# and rebuild a clean one. Runs inside a throwaway alpine:3.20
# container (the skygate container has no sqlite3 binary).
set -e
apk add --no-cache sqlite 2>&1 | tail -1
cd /work
echo "--- 1. .recover on the corrupted DB ---"
sqlite3 /data/skygate.db ".recover" > /work/dump.sql 2>/work/dump.err || true
echo "dump lines: $(wc -l < /work/dump.sql)"
echo "stderr first 5 lines:"
head -5 /work/dump.err 2>/dev/null || true
echo
echo "--- 2. Filter out sqlite_sequence and rebuild ---"
# sqlite_sequence is auto-managed; .recover includes the row
# but the CREATE is invalid. Filter to safe.
grep -v "CREATE TABLE.*sqlite_sequence" /work/dump.sql > /work/dump_clean.sql
rm -f /work/clean.db
sqlite3 /work/clean.db < /work/dump_clean.sql
ls -la /work/clean.db
echo
echo "--- 3. Integrity check ---"
sqlite3 /work/clean.db "PRAGMA integrity_check;"
echo
echo "--- 4. Table counts ---"
sqlite3 /work/clean.db "SELECT 'portal_users' as t, COUNT(*) FROM portal_users UNION ALL SELECT 'audit_log', COUNT(*) FROM audit_log UNION ALL SELECT 'device_rules', COUNT(*) FROM device_rules;"
echo
echo "--- 5. Move to /work/skygate.db (the swap target) ---"
cp /work/clean.db /work/skygate.db
ls -la /work/skygate.db
