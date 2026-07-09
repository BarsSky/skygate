#!/bin/bash
# scripts/smoke.sh — end-to-end smoke test for skygate.
# Runs in ~5s. Exits 0 on PASS, 1 on FAIL.
#
# Usage:
#   bash scripts/smoke.sh [BASE_URL]   # default http://localhost:8080
#
# Reads SKYGATE_ADMIN_USER / SKYGATE_ADMIN_PASS from .env automatically.
set -u
PASS=0
FAIL=0
ok()  { echo "PASS: $1"; PASS=$((PASS+1)); }
bad() { echo "FAIL: $1"; FAIL=$((FAIL+1)); }
note(){ echo "---- $1"; }

BASE="${1:-http://localhost:8080}"
COOKIE=/tmp/smoke_ck
rm -f "$COOKIE"

# Try to load credentials from .env
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
if [ -f "$PROJECT_ROOT/.env" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$PROJECT_ROOT/.env"
  set +a
fi
USER="${SKYGATE_ADMIN_USER:-skyadmin}"
PASS_VAR="${SKYGATE_ADMIN_PASS:-}"

if [ -z "$PASS_VAR" ]; then
  bad "SKYGATE_ADMIN_PASS not set in env / .env"
  exit 1
fi

# Helper: HTTP status
status() {
  curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE" "$@"
}

# Step 1: login
note "1. login as $USER"
CODE=$(curl -s -c "$COOKIE" -o /dev/null -w "%{http_code}" -X POST \
  --data-urlencode "username=$USER" --data-urlencode "password=$PASS_VAR" \
  "$BASE/login")
[ "$CODE" = "302" ] && ok "login returned 302" || bad "login returned $CODE"

# Step 2: dashboard
note "2. /dashboard"
CODE=$(status "$BASE/dashboard")
[ "$CODE" = "200" ] && ok "/dashboard 200" || bad "/dashboard $CODE"

# Step 3: my/* pages
note "3. /my/* pages"
for path in /my/devices /my/exit-rules /my/exit-rules/help /my/tokens /my/keys /my/exit-nodes /help; do
  CODE=$(status "$BASE$path")
  [ "$CODE" = "200" ] && ok "$path 200" || bad "$path $CODE"
done

# Step 4: admin/* pages (skyadmin is admin)
note "4. /admin/* pages"
for path in /admin/users /admin/devices /admin/audit /admin/acls \
            /admin/exit-rules /admin/exit-rules/cleanup /admin/exit-rules/sync \
            /admin/exit-nodes /admin/derp /admin/backup /admin/settings \
            /admin/telegram; do
  CODE=$(status "$BASE$path")
  [ "$CODE" = "200" ] && ok "$path 200" || bad "$path $CODE"
done

# Step 5: API endpoints
note "5. API: GET /my/exit-rules/api"
RESP=$(curl -s -b "$COOKIE" "$BASE/my/exit-rules/api")
if echo "$RESP" | grep -q '"rules"'; then
  ok "/my/exit-rules/api returns JSON with 'rules'"
else
  bad "/my/exit-rules/api response unexpected: ${RESP:0:80}"
fi

# Step 6: Add a temp rule via API and verify it appears
# Use device 3 (emilia) which has 0 manual rules so we don't hit the per-device 200 limit.
note "6. API: POST /my/exit-rules/api (add smoke-test rule on emilia=3)"
RAND_VAL="198.51.100.$((RANDOM % 250 + 1))"
RESP=$(curl -s -b "$COOKIE" -X POST \
  -H "Content-Type: application/json" \
  -d "{\"rules\":[{\"device_id\":3,\"exit_node\":\"karolina\",\"target_type\":\"subnet\",\"target_value\":\"$RAND_VAL/32\",\"action\":\"accept\"}]}" \
  "$BASE/my/exit-rules/api")
# Check actually added (not just field present)
ADDED=$(echo "$RESP" | grep -oE '"added":[0-9]+' | grep -oE '[0-9]+' | head -1)
if [ -n "$ADDED" ] && [ "$ADDED" -gt 0 ]; then
  ok "POST /my/exit-rules/api added=$ADDED rules"
  # Extract first id of newly added rule
  ID=$(echo "$RESP" | grep -oE '"id":[0-9]+' | head -1 | grep -oE '[0-9]+')
  [ -n "$ID" ] && ok "got rule id=$ID"
else
  bad "POST /my/exit-rules/api did not add: ${RESP:0:200}"
fi

# Step 7: Verify the new rule is in the list
note "7. rule visible in /my/exit-rules/api"
sleep 1
RESP=$(curl -s -b "$COOKIE" "$BASE/my/exit-rules/api")
if echo "$RESP" | grep -q "$RAND_VAL"; then
  ok "rule $RAND_VAL/32 present in API response"
else
  bad "rule $RAND_VAL/32 NOT in API response"
fi

# Step 8: delete via multi-delete API
note "8. delete the smoke-test rule (cascade test included)"
if [ -n "$ID" ]; then
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE" -X POST \
    --data-raw "ids=$ID" \
    "$BASE/my/exit-rules/delete")
  [ "$CODE" = "302" ] && ok "delete via ids= returned 302" || bad "delete returned $CODE"
  sleep 1
  RESP=$(curl -s -b "$COOKIE" "$BASE/my/exit-rules/api")
  if echo "$RESP" | grep -q "$RAND_VAL"; then
    bad "rule $RAND_VAL/32 still present after delete"
  else
    ok "rule $RAND_VAL/32 removed (no orphans)"
  fi
fi

# Step 9: static assets
note "9. static assets"
for path in /favicon.ico /favicon.svg /static/css/font-awesome.min.css; do
  CODE=$(status "$BASE$path")
  [ "$CODE" = "200" ] && ok "$path 200" || bad "$path $CODE"
done

# Step 10: /admin/exit-rules/sync — run advertised-routes sync
note "10. /admin/exit-rules/sync (admin trigger)"
CODE=$(status "$BASE/admin/exit-rules/sync")
[ "$CODE" = "200" ] && ok "/admin/exit-rules/sync 200" || bad "/admin/exit-rules/sync $CODE"

# Step 11: validate HTML /my/exit-rules contains critical strings
note "11. UI sanity: /my/exit-rules contains required text"
HTML=$(curl -s -b "$COOKIE" "$BASE/my/exit-rules")
for needle in "Текущие правила" "Мои правила" "Добавить правило"; do
  if echo "$HTML" | grep -qF "$needle"; then
    ok "page contains '$needle'"
  else
    bad "page missing '$needle'"
  fi
done

# Step 12: logout
note "12. logout"
CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE" -c "$COOKIE" -X POST "$BASE/logout")
[ "$CODE" = "302" ] || [ "$CODE" = "200" ] && ok "logout returned $CODE" || bad "logout $CODE"

note "SUMMARY: $PASS pass, $FAIL fail"
[ "$FAIL" = "0" ] && exit 0 || exit 1
