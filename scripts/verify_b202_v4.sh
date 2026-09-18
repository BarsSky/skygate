#!/bin/bash
# verify_b202_v4.sh — proper cross-database migration on the
# SAME server. Source = skygate_staging, target = a
# freshly-created skygate_staging_test2. Same PG version,
# different DB name. Proves the full dump→restore→verify
# cycle works on a real migration (not a self-migration).
set +e
COOKIE_FILE=/tmp/skygate_cookie.txt
PGPASSWORD=skygate_admin_pass
BASE="http://127.0.0.1:8080"

# Login
rm -f $COOKIE_FILE
curl -s -c $COOKIE_FILE -i -X POST \
  -d "username=skyadmin&password=t%25gVCuboZSMT07SM97kV5%40hb" \
  "$BASE/login?theme=linear" > /tmp/login_resp.txt
COOKIE=$(grep skygate_session $COOKIE_FILE | awk '{print $7}')

echo "=== 0. Create target DB (skygate_staging_test2) ==="
# DROP DATABASE cannot run in a transaction block, so use
# separate psql calls.
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "DROP DATABASE IF EXISTS skygate_staging_test2" 2>&1 | tail -1
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "CREATE DATABASE skygate_staging_test2" 2>&1 | tail -1
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging_test2 -c "SELECT 'target DB created' AS status;" 2>&1 | tail -3

echo ""
echo "=== 1. Setup: fixture table in source ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "
DROP TABLE IF EXISTS b202_v4_fixture;
CREATE TABLE b202_v4_fixture (id SERIAL PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO b202_v4_fixture (name) VALUES ('alpha'), ('beta'), ('gamma'), ('delta'), ('epsilon');
SELECT count(*) AS source_count FROM b202_v4_fixture;
" 2>&1 | grep -E "source_count|---" | head -3

echo ""
echo "=== 2. POST migration: source=skygate_staging, target=skygate_staging_test2 ==="
# 172.17.0.1 = the Docker bridge gateway (the host's
# 127.0.0.1 from inside the skygate container). The
# host's PG listens on port 5433.
curl -s -b "skygate_session=$COOKIE" -X POST \
  -d "target_host=172.17.0.1&target_port=5433&target_dbname=skygate_staging_test2&target_username=admin&target_sslmode=disable" \
  -i -o /tmp/migrate_resp.txt -w "  POST = %{http_code}\n" \
  $BASE/admin/database/migrate
LOCATION=$(grep -i '^Location:' /tmp/migrate_resp.txt | head -1 | tr -d '\r' | sed 's/^Location: //I')
echo "  Location: $LOCATION"

echo ""
echo "=== 3. Find run id and wait ==="
RUN_ID=$(PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
  "SELECT id FROM dbmigrate_run ORDER BY id DESC LIMIT 1;")
echo "  run id: $RUN_ID"
for i in $(seq 1 30); do
  STATUS=$(PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
    "SELECT status FROM dbmigrate_run WHERE id=$RUN_ID;")
  echo "  [$i] status: $STATUS"
  if [ "$STATUS" = "success" ] || [ "$STATUS" = "failed" ] || [ "$STATUS" = "rolled_back" ]; then break; fi
  sleep 5
done

echo ""
echo "=== 4. Per-step status (all 6 steps now in semantic order) ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT step_name, status, duration_ms, COALESCE(LEFT(error, 100), '') AS error FROM dbmigrate_step WHERE run_id=$RUN_ID ORDER BY ordinal;"

echo ""
echo "=== 5. Source vs target row counts (key tables) ==="
echo "  SOURCE (skygate_staging):"
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT
    (SELECT count(*) FROM portal_users) AS portal_users,
    (SELECT count(*) FROM device_rules) AS device_rules,
    (SELECT count(*) FROM preauth_keys) AS preauth_keys,
    (SELECT count(*) FROM node_owner_map) AS node_owner_map,
    (SELECT count(*) FROM b202_v4_fixture) AS b202_fixture;" 2>&1 | tail -3
echo ""
echo "  TARGET (skygate_staging_test2):"
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging_test2 -c \
  "SELECT
    (SELECT count(*) FROM portal_users) AS portal_users,
    (SELECT count(*) FROM device_rules) AS device_rules,
    (SELECT count(*) FROM preauth_keys) AS preauth_keys,
    (SELECT count(*) FROM node_owner_map) AS node_owner_map,
    (SELECT count(*) FROM b202_v4_fixture) AS b202_fixture;" 2>&1 | tail -3

echo ""
echo "=== 6. Dump file (if Flip didn't auto-delete it via Cleanup) ==="
ls -la /var/lib/skygate/migrations/$RUN_ID.dump 2>&1 | head -3

echo ""
echo "=== 7. Audit log (last 5 cluster.db.* + dbmigrate events) ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT username, action, substring(detail, 1, 60) AS detail_short FROM audit_log WHERE action LIKE 'cluster.%' OR action LIKE 'dbmigrate%' ORDER BY id DESC LIMIT 5;"

echo ""
echo "=== 8. Cleanup: drop the test target DB + source fixture ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "DROP TABLE b202_v4_fixture" 2>&1 | tail -1
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "DROP DATABASE skygate_staging_test2" 2>&1 | tail -1
