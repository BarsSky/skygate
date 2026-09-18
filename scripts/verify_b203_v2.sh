# 2026-09-18: operator infrastructure values redacted to env vars before
# this script was committed. Set them in your shell, e.g.
#   VM_HOST=<the skygate host> EMILIA_PUBLIC_IP=... bash scripts\verify_b203_v2.sh
# The ${VAR:?} form makes a missing value a hard error instead of an
# empty hostname.
#!/bin/bash
# verify_b203_v2.sh — skygate-watchdog hot-reload test.
# Runs all SSH once via a single heredoc to avoid the
# nested-SSH issue.
set +e
COOKIE_FILE=/tmp/skygate_cookie.txt
PGPASSWORD=<db-password>
BASE="http://127.0.0.1:8080"

# Login
rm -f $COOKIE_FILE
curl -s -c $COOKIE_FILE -i -X POST \
  -d "username=${SKYGATE_ADMIN_USER:-skyadmin}" \
  --data-urlencode "password=${SKYGATE_ADMIN_PASS:-}" \
  "$BASE/login?theme=linear" > /tmp/login_resp.txt
COOKIE=$(grep skygate_session $COOKIE_FILE | awk '{print $7}')

echo "=== 0. Confirm watchdog is running ==="
ssh skyadmin@${VM_HOST:?set VM_HOST} -- "docker logs --tail 50 skygate-skygate-1 2>&1 | grep -E 'dbmigrate-watchdog' | tail -3"

echo ""
echo "=== 1. Insert cluster_database row with a valid node ref ==="
# Use 'tagged-devices' (the existing synthetic node from B175)
# OR use NULL for primary_node_id. The schema says it can be NULL.
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "INSERT INTO cluster_database (id, cluster_id, primary_node_id, replica_node_ids, dsn_template, dbname, username, sslmode, current_dsn, updated_by)
   VALUES ('skygate-staging', 'skygate-staging', NULL, '{}',
           'postgres://admin:%s@172.17.0.1:5433/skygate_staging?sslmode=disable',
           'skygate_staging', 'admin', 'disable',
           'postgres://admin:<db-password>@172.17.0.1:5433/skygate_staging?sslmode=disable',
           'verify_b203')
   ON CONFLICT (id) DO UPDATE SET
     current_dsn = EXCLUDED.current_dsn,
     dsn_template = EXCLUDED.dsn_template,
     updated_by = EXCLUDED.updated_by,
     updated_at = NOW();" 2>&1 | tail -3

echo ""
echo "=== 2. Single SSH: wait 12s, then dump logs ==="
ssh skyadmin@${VM_HOST:?set VM_HOST} -- "sleep 12 && docker logs --tail 30 skygate-skygate-1 2>&1 | grep -E 'dbmigrate-watchdog' | tail -8" 2>&1

echo ""
echo "=== 3. Verify cluster_database row ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT id, current_dsn, updated_by, updated_at FROM cluster_database WHERE id='skygate-staging';"

echo ""
echo "=== 4. Cleanup: delete the row ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "DELETE FROM cluster_database WHERE id='skygate-staging';" 2>&1 | tail -1

echo ""
echo "=== 5. Single SSH: wait 8s, then dump logs (watchdog should log 'not found' again) ==="
ssh skyadmin@${VM_HOST:?set VM_HOST} -- "sleep 8 && docker logs --tail 30 skygate-skygate-1 2>&1 | grep -E 'dbmigrate-watchdog' | tail -5" 2>&1
