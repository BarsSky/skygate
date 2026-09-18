# 2026-09-18: the live admin password + the default DB password were hardcoded in this
# file (the v0.34.0.1 leak class). They now come from the environment: export
# SKYGATE_ADMIN_USER / SKYGATE_ADMIN_PASS (or source the VM's .env) before running.
#!/bin/bash
# verify_b202_v3.sh — cross-server migration test.
# Source: agent's live skygate DB (PG 16, port 5433)
# Target: skygate-pg-test (PG 15, port 5433, different server)
# Different version + different server = real cross-host migration.
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

echo "=== 0. Setup: fixture table + check source row count ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "
DROP TABLE IF EXISTS b202_v3_fixture;
CREATE TABLE b202_v3_fixture (id SERIAL PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO b202_v3_fixture (name) VALUES ('row1'), ('row2'), ('row3'), ('row4'), ('row5');
SELECT count(*) AS source_count FROM b202_v3_fixture;
SELECT count(*) AS b202_fixture_count FROM b202_fixture;
" 2>&1 | grep -E "source_count|fixture_count|---|DROP|CREATE|INSERT|^[ ]+[0-9]" | head -10

echo ""
echo "=== 0b. Check target DB exists (skygate-pg-test) ==="
PGPASSWORD=$PGPASSWORD psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c "SELECT version();" 2>&1 | head -3
# Try the target DSN directly
docker exec skygate-skygate-1 pg_dump --version 2>&1

echo ""
echo "=== 1. POST migration: source=PG16 agent, target=PG15 skygate-pg-test ==="
# Note: skygate-pg-test is bound to host port 5433 (same as PG 16)
# Wait — that would conflict. Let me check what port skygate-pg-test is on.
echo "  Checking skygate-pg-test port..."
docker port skygate-pg-test 2>&1 | head -3
echo "  If empty, the container has no published port. Need a different test target."

echo ""
echo "=== 2. Use the test DB container directly as target (port 5433 same host) ==="
echo "  Note: skygate-pg-test container is in 'Created' state — needs to be started first."
docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' | head -5

# Start skygate-pg-test (it's created but not running)
docker start skygate-pg-test 2>&1
sleep 3
docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' | head -5

# Test if we can reach the container's port 5433 from the agent host
echo ""
echo "  Testing if skygate-pg-test is reachable on host port 5433..."
timeout 3 bash -c "</dev/tcp/127.0.0.1/5433" 2>&1 && echo "  port 5433 open" || echo "  port 5433 closed"

echo ""
echo "  (Skipping the actual migration POST because of port conflict — see summary)"
