# 2026-09-18: the live admin password + the default DB password were hardcoded in this
# file (the v0.34.0.1 leak class). They now come from the environment: export
# SKYGATE_ADMIN_USER / SKYGATE_ADMIN_PASS (or source the VM's .env) before running.
#!/bin/bash
set +e
COOKIE_FILE=/tmp/skygate_cookie.txt
PGPASSWORD=<db-password>
BASE="http://127.0.0.1:8080"
PSQL="psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging"

# Login and save cookie
rm -f $COOKIE_FILE
curl -s -c $COOKIE_FILE -i -X POST \
  -d "username=${SKYGATE_ADMIN_USER:-skyadmin}" \
  --data-urlencode "password=${SKYGATE_ADMIN_PASS:-}" \
  "$BASE/login?theme=linear" > /tmp/login_resp.txt
COOKIE=$(grep skygate_session $COOKIE_FILE | awk '{print $7}')
echo "cookie: $COOKIE"

echo "=== 0. Setup: fixture table ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "
DROP TABLE IF EXISTS b202_fixture;
CREATE TABLE b202_fixture (id SERIAL PRIMARY KEY, name TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
INSERT INTO b202_fixture (name) VALUES ('alpha'), ('beta'), ('gamma'), ('delta');
SELECT count(*) AS fixture_count FROM b202_fixture;
" 2>&1 | tail -5

echo ""
echo "=== 1. POST self-migration ==="
curl -s -b "skygate_session=$COOKIE" -X POST \
  -d "target_host=172.17.0.1&target_port=5433&target_dbname=skygate_staging&target_username=admin&target_sslmode=disable" \
  -i -o /tmp/migrate_resp.txt -w "  POST = %{http_code}\n" \
  $BASE/admin/database/migrate
LOCATION=$(grep -i '^Location:' /tmp/migrate_resp.txt | head -1 | tr -d '\r')
echo "  Location: $LOCATION"

echo ""
echo "=== 2. Find run id and wait ==="
RUN_ID=$(PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
  "SELECT id FROM dbmigrate_run ORDER BY id DESC LIMIT 1;")
echo "  run id: $RUN_ID"
for i in $(seq 1 12); do
  STATUS=$(PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
    "SELECT status FROM dbmigrate_run WHERE id=$RUN_ID;")
  echo "  [$i] status: $STATUS"
  if [ "$STATUS" = "success" ] || [ "$STATUS" = "failed" ] || [ "$STATUS" = "rolled_back" ]; then break; fi
  sleep 5
done

echo ""
echo "=== 3. Per-step status (all 6 steps) ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT step_name, status, duration_ms, COALESCE(LEFT(error, 80), '') AS error FROM dbmigrate_step WHERE run_id=$RUN_ID ORDER BY ordinal;"

echo ""
echo "=== 4. Dump file ==="
DUMP_FILE="/var/lib/skygate/migrations/$RUN_ID.dump"
ls -la $DUMP_FILE 2>&1
if [ -f $DUMP_FILE ]; then
  file $DUMP_FILE
  head -c 4 $DUMP_FILE | xxd
fi

echo ""
echo "=== 5. Source row counts (key tables) ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT
    (SELECT count(*) FROM portal_users) AS portal_users,
    (SELECT count(*) FROM device_rules) AS device_rules,
    (SELECT count(*) FROM preauth_keys) AS preauth_keys,
    (SELECT count(*) FROM node_owner_map) AS node_owner_map;"

echo ""
echo "=== 6. Fixture table intact? ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT count(*) AS rows FROM b202_fixture;"

echo ""
echo "=== 7. Audit log (cluster.db.* + dbmigrate events) ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT username, action, substring(detail, 1, 60) AS detail_short FROM audit_log WHERE action LIKE 'cluster.%' OR action LIKE 'dbmigrate%' ORDER BY id DESC LIMIT 8;"

echo ""
echo "=== 8. Cleanup fixture ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "DROP TABLE b202_fixture;" 2>&1 | tail -1
