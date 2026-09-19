# 2026-09-18: operator infrastructure values redacted to env vars before
# this script was committed. Set them in your shell, e.g.
#   VM_HOST=<the skygate host> EMILIA_PUBLIC_IP=... bash scripts\verify_b202.sh
# The ${VAR:?} form makes a missing value a hard error instead of an
# empty hostname.
#!/bin/bash
# verify_b202.sh — end-to-end B202 test.
# Runs a real migration on the skygate-pg-test container
# (the agent's local fresh PG 5433). The "source" and
# "target" are the SAME database (a self-migration = noop
# + idempotency check), so the dump + restore + verify
# all run on a known fixture and we can confirm the
# data isn't corrupted.
COOKIE="${SKYGATE_COOKIE:?set SKYGATE_COOKIE}"
BASE="http://127.0.0.1:8080"

# Test DSN (the test PG that skygate-pg-test runs on host port 5433)
TEST_DSN="postgres://admin:${SKYGATE_DB_PASSWORD}@127.0.0.1:5433/skygate_staging?sslmode=disable"
# The actual skygate runs against 172.17.0.1:5433 (Docker bridge
# to the host's PG 16). For the migration, source=target
# = the actual skygate DB.

echo "=== 0. Setup: create a fixture table + insert test rows ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "
DROP TABLE IF EXISTS b202_fixture;
CREATE TABLE b202_fixture (id SERIAL PRIMARY KEY, name TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
INSERT INTO b202_fixture (name) VALUES ('alpha'), ('beta'), ('gamma'), ('delta');
SELECT count(*) AS fixture_count FROM b202_fixture;
" 2>&1 | Select-String -Pattern "fixture_count|---|CREATE|INSERT|DROP|^\s+[0-9]" 2>&1

echo ""
echo "=== 1. Run a self-migration (source=target=the live skygate DB) ==="
# Source: the current skygate DSN (from env, which is 172.17.0.1:5433)
# Target: SAME (self-migration tests that dump+restore round-trips cleanly)
RESP=$(curl -s -b "skygate_session=$COOKIE" -X POST \
  -d "host=172.17.0.1&port=5433&dbname=skygate_staging&username=admin&sslmode=disable" \
  -i -o /tmp/migrate_resp.txt -w "%{http_code}" \
  $BASE/admin/database/migrate)
echo "  POST /admin/database/migrate = $RESP"
LOCATION=$(grep -i '^Location:' /tmp/migrate_resp.txt | head -1 | tr -d '\r')
echo "  Location: $LOCATION"

echo ""
echo "=== 2. Wait for run to finish (poll /admin/database/migrate/{id}) ==="
RUN_ID=$(echo "$LOCATION" | grep -oE 'migrate/[0-9]+' | grep -oE '[0-9]+' | head -1)
if [ -z "$RUN_ID" ]; then
  # Try a recent run from the DB
  RUN_ID=$(PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
    "SELECT id FROM dbmigrate_run ORDER BY id DESC LIMIT 1;")
fi
echo "  run id: $RUN_ID"
for i in 1 2 3 4 5 6 7 8 9 10; do
  STATUS=$(PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
    "SELECT status FROM dbmigrate_run WHERE id=$RUN_ID;")
  echo "  [$i] run status: $STATUS"
  if [ "$STATUS" = "success" ] || [ "$STATUS" = "failed" ] || [ "$STATUS" = "rolled_back" ]; then
    break
  fi
  sleep 5
done

echo ""
echo "=== 3. Per-step status from dbmigrate_step ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT step_name, status, duration_ms, error FROM dbmigrate_step WHERE run_id=$RUN_ID ORDER BY ordinal;"

echo ""
echo "=== 4. Dump file exists? ==="
DUMP_FILE=$(PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
  "SELECT id || '.dump' FROM dbmigrate_run WHERE id=$RUN_ID;" | xargs -I {} echo "/var/lib/skygate/migrations/{}")
echo "  expected: $DUMP_FILE"
if ssh skyadmin@${VM_HOST:?set VM_HOST} -- "ls -la $DUMP_FILE 2>&1" 2>&1; then
  echo "  dump file present"
else
  echo "  (dump may be cleaned up — checking migrations dir)"
  ssh skyadmin@${VM_HOST:?set VM_HOST} -- "ls -la /var/lib/skygate/migrations/ 2>&1 | head -10" 2>&1
fi

echo ""
echo "=== 5. Source row count (skygate_staging.portal_users etc) ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT
    (SELECT count(*) FROM portal_users) AS portal_users,
    (SELECT count(*) FROM device_rules) AS device_rules,
    (SELECT count(*) FROM preauth_keys) AS preauth_keys,
    (SELECT count(*) FROM node_owner_map) AS node_owner_map;"

echo ""
echo "=== 6. Verify the fixture table is intact (b202_fixture should have 4 rows) ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT count(*) AS rows_in_fixture FROM b202_fixture;"

echo ""
echo "=== 7. Audit log: cluster.db.* + dbmigrate events ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT username, action, substring(detail, 1, 80) AS detail_short FROM audit_log WHERE action LIKE 'cluster.%' OR action LIKE 'dbmigrate%' ORDER BY id DESC LIMIT 5;"

echo ""
echo "=== 8. Clean up: drop the fixture table ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "DROP TABLE b202_fixture;"
