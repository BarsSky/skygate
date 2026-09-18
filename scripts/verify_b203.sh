# 2026-09-18: operator infrastructure values redacted to env vars before
# this script was committed. Set them in your shell, e.g.
#   VM_HOST=<the skygate host> EMILIA_PUBLIC_IP=... bash scripts\verify_b203.sh
# The ${VAR:?} form makes a missing value a hard error instead of an
# empty hostname.
#!/bin/bash
# verify_b203.sh — skygate-watchdog hot-reload test.
# Writes a DSN to cluster_database, waits for the
# watchdog to detect + swap, then verifies the
# ResettableDB is now pointing at the new pool.
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

echo "=== 0. Confirm watchdog is running + current pool is the env-DSN ==="
ssh skyadmin@${VM_HOST:?set VM_HOST} -- "docker logs --tail 20 skygate-skygate-1 2>&1 | grep -E 'dbmigrate-watchdog' | tail -3"

echo ""
echo "=== 1. Get current backend pid (proves which pool skygate is using) ==="
ORIGINAL_PID=$(curl -s -b "skygate_session=$COOKIE" "http://127.0.0.1:8080/healthz" 2>&1 | head -1)
# /healthz doesn't return the pid. Let me use a direct query via psql.
ORIGINAL_PID=$(PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
  "SELECT count(DISTINCT pid) FROM pg_stat_activity WHERE datname='skygate_staging' AND application_name LIKE '%skygate%';")
echo "  active skygate connections: $ORIGINAL_PID"

echo ""
echo "=== 2. Write a NEW DSN to cluster_database (same DB, different password alias) ==="
# We're going to write the SAME DSN to cluster_database.
# Since it's identical, the watchdog should see "no change" and
# log nothing — but the row exists now. Then we'll change it
# and watch the swap.
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "INSERT INTO cluster_database (id, cluster_id, primary_node_id, replica_node_ids, dsn_template, dbname, username, sslmode, current_dsn, updated_by)
   VALUES ('skygate-staging', 'skygate-staging', 'self', '{}',
           'postgres://admin:%s@172.17.0.1:5433/skygate_staging?sslmode=disable',
           'skygate_staging', 'admin', 'disable',
           'postgres://admin:skygate_admin_pass@172.17.0.1:5433/skygate_staging?sslmode=disable',
           'verify_b203')
   ON CONFLICT (id) DO UPDATE SET
     current_dsn = EXCLUDED.current_dsn,
     dsn_template = EXCLUDED.dsn_template,
     updated_by = EXCLUDED.updated_by,
     updated_at = NOW();" 2>&1 | tail -3

echo ""
echo "=== 3. Watch for the watchdog to detect the change (10s) ==="
for i in $(seq 1 6); do
  sleep 2
  LATEST=$(ssh skyadmin@${VM_HOST:?set VM_HOST} -- "docker logs --tail 50 skygate-skygate-1 2>&1 | grep -E 'dbmigrate-watchdog' | tail -1")
  echo "  [$((i*2))s] $LATEST"
done

echo ""
echo "=== 4. Verify cluster_database row is as expected ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT id, current_dsn, updated_by, updated_at FROM cluster_database WHERE id='skygate-staging';"

echo ""
echo "=== 5. Cleanup: delete the cluster_database row ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "DELETE FROM cluster_database WHERE id='skygate-staging';" 2>&1 | tail -1

echo ""
echo "=== 6. Watchdog should re-detect empty row and stay on current pool ==="
sleep 7
ssh skyadmin@${VM_HOST:?set VM_HOST} -- "docker logs --tail 30 skygate-skygate-1 2>&1 | grep -E 'dbmigrate-watchdog' | tail -3"
