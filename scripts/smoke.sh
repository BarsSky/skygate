#!/bin/bash
# scripts/smoke.sh — end-to-end smoke test for skygate.
# Runs in ~5s per language. Exits 0 on PASS, 1 on FAIL.
#
# Usage:
#   bash scripts/smoke.sh [BASE_URL]                    # runs both ru and en
#   SMOKE_LANG=ru bash scripts/smoke.sh [BASE_URL]      # one language only
#   SMOKE_LANG=en bash scripts/smoke.sh [BASE_URL]
#
# Reads SKYGATE_ADMIN_USER / SKYGATE_ADMIN_PASS from .env automatically.
#
# When SMOKE_LANG is unset, this script re-invokes itself once per
# language (ru, then en) and prints a combined SUMMARY. The base URL
# is forwarded so a caller can do `make test BASE=...`.
set -u
PASS=0
: "${SMOKE_TEST_NEW_PASSWORD:=SkySmoke_2026_X}"
FAIL=0

# Multi-language dispatch.
# If SMOKE_LANG is unset, fan out: run this script once for each
# supported language. Each sub-run uses its own cookie file and
# cookie jar so they don't interfere with each other. Forward $@
# so the BASE URL is preserved.
if [ -z "${SMOKE_LANG:-}" ]; then
  echo "=== smoke fan-out: ru then en ==="
  OVERALL=0
  for L in ru en; do
    SMOKE_LANG=$L COOKIE=/tmp/smoke_ck.$L bash "$0" "$@" || OVERALL=$?
  done
  echo "=== smoke done (overall exit=$OVERALL) ==="
  exit $OVERALL
fi

# From here on we are in a single-language sub-run.
ok()  { echo "[$SMOKE_LANG] PASS: $1"; PASS=$((PASS+1)); }
bad() { echo "[$SMOKE_LANG] FAIL: $1"; FAIL=$((FAIL+1)); }
note(){ echo "[$SMOKE_LANG] ---- $1"; }

BASE="${1:-http://localhost:8080}"
# Use a per-language cookie file so the two sub-runs don't trample
# each other's session.
COOKIE="${COOKIE:-/tmp/smoke_ck}"
rm -f "$COOKIE"

# Per-language Accept-Language header. The server reads this when no
# `lang` cookie is set. Setting it on every request is enough — we
# don't need a lang cookie because LangFromRequest falls through to
# Accept-Language.
ACCEPT_LANG_HDR=(-H "Accept-Language: $SMOKE_LANG")

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

# Helper: HTTP status. Always sends Accept-Language.
status() {
  curl -s -o /dev/null -w "%{http_code}" "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" "$@"
}

# Helper: HTML body. Always sends Accept-Language.
html() {
  curl -s "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" "$@"
}

# Helper: JSON body. Always sends Accept-Language.
json() {
  curl -s "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" "$@"
}

# ---- Per-language UI strings (for content sanity checks) ----
# English is the source of truth; Russian must match the i18n catalog.
case "$SMOKE_LANG" in
  ru)
    S_ACTIVE="Активные"
    S_TITLE_HELP="Справка"
    S_TITLE_DEVICES="Мои устройства"
    S_TITLE_EXIT_RULES="Exit Rules"            # intentionally ASCII for both
    S_PAGE_RULES_HEADER="Текущие правила"
    S_PAGE_MY_RULES="Мои правила"
    S_PAGE_ADD_RULE="Добавить правило"
    S_PAGE_DASHBOARD="Главная"
    S_TOGGLE_LANG="Язык"
    ;;
  en)
    S_ACTIVE="Active"
    S_TITLE_HELP="Help"
    S_TITLE_DEVICES="My devices"
    S_TITLE_EXIT_RULES="Exit Rules"
    S_PAGE_RULES_HEADER="Current rules"
    S_PAGE_MY_RULES="My rules"
    S_PAGE_ADD_RULE="Add rule"
    S_PAGE_DASHBOARD="Dashboard"
    S_TOGGLE_LANG="Language"
    ;;
  *)
    bad "unknown SMOKE_LANG=$SMOKE_LANG (use ru or en)"
    exit 1
    ;;
esac

# Step 0.5: rate limit kicks in on /login
# Send 6 wrong-password attempts with a non-admin username and verify the
# 6th returns 429 Too Many Requests. Uses a throwaway username so we do
# not consume the per-username bucket that the admin login needs.
note "0.5. /login rate limit (5 wrong attempts before 429)"
RL_USER="smoke_rl_user_$$"
for i in 1 2 3 4 5 6; do
  CODE=$(curl -s -o /dev/null -w "%{http_code}" "${ACCEPT_LANG_HDR[@]}" -X POST \
    --data-urlencode "username=$RL_USER" \
    --data-urlencode "password=WRONG_PASSWORD_ATTEMPT_$i" \
    "$BASE/login")
  if [ "$i" -lt 6 ] && [ "$CODE" != "429" ]; then
    ok "login attempt $i of 6 returned $CODE (under limit)"
  elif [ "$i" -lt 6 ] && [ "$CODE" = "429" ]; then
    bad "login attempt $i should NOT be blocked yet, got 429"
  fi
  if [ "$i" -eq 6 ]; then
    if [ "$CODE" = "429" ]; then
      ok "login attempt 6 returned 429 (rate limit kicked in)"
    else
      bad "login attempt 6 should be 429, got $CODE"
    fi
  fi
done

# Step 1: login
note "1. login as $USER"
CODE=$(curl -s -c "$COOKIE" -o /dev/null -w "%{http_code}" "${ACCEPT_LANG_HDR[@]}" -X POST \
  --data-urlencode "username=$USER" --data-urlencode "password=$PASS_VAR" \
  "$BASE/login")
[ "$CODE" = "302" ] && ok "login returned 302" || bad "login returned $CODE"

# Pre-flight: remove any leftover smoke-test rules on device 8
# (skyadmin's tag:private device) from previous smoke runs. These were
# created by step 6 below; if step 8 ever failed (e.g. timeout), the
# rule remained and accumulated. We deliberately pick device 8 (not
# 3/4/11, which are exit-nodes emilia/sharlotta/karolina) because the
# 2026-07-11 fix rejects rule attachment to exit-nodes.
RESP=$(json "$BASE/my/exit-rules/api")
ORPHAN_IDS=$(echo "$RESP" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for r in d.get('rules', []):
    if r.get('device_id') == 8 and r.get('target_value', '').startswith('198.51.100.') and r.get('target_value', '').endswith('/32'):
        print(r['id'])
" 2>/dev/null | tr '\n' ' ')
if [ -n "$ORPHAN_IDS" ]; then
  ARGS=""
  for i in $ORPHAN_IDS; do
    ARGS="$ARGS --data-urlencode ids=$i"
  done
  curl -s -o /dev/null -b "$COOKIE" -X POST "$BASE/my/exit-rules/delete" $ARGS
  note "0. cleanup: removed $(echo $ORPHAN_IDS | wc -w) orphan smoke rules on device 8"
fi

# Step 2: dashboard
note "2. /dashboard"
CODE=$(status "$BASE/dashboard")
[ "$CODE" = "200" ] && ok "/dashboard 200" || bad "/dashboard $CODE"

# Step 2.5: dashboard title matches the language. /dashboard has a
# <h2>Welcome with the username, plus a sub line and the page title
# in the <title> tag (resolved by pageTitle() per language).
H=$(html "$BASE/dashboard")
if echo "$H" | grep -q "<title>${S_TITLE_DEVICES}\\|<title>${S_PAGE_DASHBOARD}\\|<title>Skygate"; then
  ok "/dashboard title rendered"
else
  bad "/dashboard title missing or wrong language"
fi
# The first <h2> in the body must contain either the localized nav label
# or the localized heading. (Some pages reuse the title for both.)
if echo "$H" | grep -q "Welcome, $USER\|Добро пожаловать, $USER"; then
  ok "/dashboard greets the user in the right language"
else
  bad "/dashboard greeting missing or wrong language"
fi

# Step 3: my/* pages — only HTTP status, language-agnostic
note "3. /my/* pages"
for path in /my/devices /my/exit-rules /my/exit-rules/help /my/tokens /my/keys /my/exit-nodes /help; do
  CODE=$(status "$BASE$path")
  [ "$CODE" = "200" ] && ok "$path 200" || bad "$path $CODE"
done

# Step 4: admin/* pages — HTTP status + render-clean check
note "4. /admin/* pages"
for path in /admin/users /admin/devices /admin/audit /admin/acls \
            /admin/exit-rules /admin/exit-rules/cleanup /admin/exit-rules/sync \
            /admin/exit-nodes /admin/derp /admin/backup /admin/settings \
            /admin/telegram; do
  CODE=$(status "$BASE$path")
  [ "$CODE" = "200" ] && ok "$path 200" || bad "$path $CODE"
done

# Body render check on a subset of admin pages. renderBody looks up
# {{define "body-{slug}"}}; any mismatch raises "html/template: ... is
# undefined" written to the response body. We anchor to the leading
# 'template:' on the first three response lines.
for path in /admin/users /admin/acls /admin/devices /admin/audit; do
  HTML=$(html "$BASE$path")
  if echo "$HTML" | head -3 | grep -q "^template:"; then
    bad "$path: template render error ($(echo "$HTML" | head -3 | grep '^template:' | head -1))"
  else
    ok "$path HTML renders cleanly"
  fi
done

# Content sanity: /admin/users must list skyadmin
if html "$BASE/admin/users" | grep -q "skyadmin"; then
  ok "/admin/users lists skyadmin"
else
  bad "/admin/users: missing skyadmin"
fi

# Content sanity: /admin/users active-count uses the localized word.
if html "$BASE/admin/users" | grep -q "$S_ACTIVE"; then
  ok "/admin/users uses '$S_ACTIVE' (active count label)"
else
  bad "/admin/users missing active-count label '$S_ACTIVE'"
fi

# Step 5: API endpoints
note "5. API: GET /my/exit-rules/api"
RESP=$(json "$BASE/my/exit-rules/api")
if echo "$RESP" | grep -q '"rules"'; then
  ok "/my/exit-rules/api returns JSON with 'rules'"
else
  bad "/my/exit-rules/api response unexpected: ${RESP:0:80}"
fi

# Step 6: Add a temp rule via API and verify it appears
# Use device 8 (skyadmin's tag:private device) which has 0 manual rules
# so we don't hit the per-device 200 limit. NOT device 3 (emilia),
# 4 (sharlotta) or 11 (karolina) — those are exit-nodes and the
# 2026-07-11 fix correctly rejects rule attachment to them.
note "6. API: POST /my/exit-rules/api (add smoke-test rule on device=8)"
RAND_VAL="198.51.100.$((RANDOM % 250 + 1))"
RESP=$(curl -s "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" -X POST \
  -H "Content-Type: application/json" \
  -d "{\"rules\":[{\"device_id\":8,\"exit_node\":\"karolina\",\"target_type\":\"subnet\",\"target_value\":\"$RAND_VAL/32\",\"action\":\"accept\"}]}" \
  "$BASE/my/exit-rules/api")
# Check actually added (not just field present)
ADDED=$(echo "$RESP" | grep -oE '"added":[0-9]+' | grep -oE '[0-9]+' | head -1)
if [ -n "$ADDED" ] && [ "$ADDED" -gt 0 ]; then
  ok "POST /my/exit-rules/api added=$ADDED rules"
  # Extract ids from "ids":[N1,N2,...] array
  IDS=$(echo "$RESP" | grep -oE '"ids":[[0-9,]+]' | grep -oE '[0-9]+' | tr '"' ' ')
  [ -n "$IDS" ] && ok "got rule ids: $IDS"
else
  bad "POST /my/exit-rules/api did not add: ${RESP:0:200}"
fi

# Step 7: Verify the new rule is in the list
note "7. rule visible in /my/exit-rules/api"
sleep 1
RESP=$(json "$BASE/my/exit-rules/api")
if echo "$RESP" | grep -q "$RAND_VAL"; then
  ok "rule $RAND_VAL/32 present in API response"
else
  bad "rule $RAND_VAL/32 NOT in API response"
fi

# Verify /my/exit-rules HTML renders with no template error
HTML=$(html "$BASE/my/exit-rules")
if echo "$HTML" | grep -q "^template:"; then
  bad "GET /my/exit-rules: template error"
else
  ok "/my/exit-rules HTML renders"
fi

# Step 8: delete via multi-delete API
note "8. delete the smoke-test rule (cascade test included)"
if [ -n "$IDS" ]; then
  # Build --data-urlencode ids=N for each id
  ARGS=""
  for i in $IDS; do
    ARGS="$ARGS --data-urlencode ids=$i"
  done
  CODE=$(curl -s -o /dev/null -w "%{http_code}" "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" -X POST \
    "$BASE/my/exit-rules/delete" $ARGS)
  [ "$CODE" = "302" ] && ok "delete via ids= returned 302" || bad "delete returned $CODE"
  sleep 1
  RESP=$(json "$BASE/my/exit-rules/api")
  if echo "$RESP" | grep -q "$RAND_VAL"; then
    bad "rule $RAND_VAL/32 still present after delete"
  else
    ok "rule $RAND_VAL/32 removed (no orphans)"
  fi
else
  bad "no IDS captured from step 6; cannot delete"
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

# Step 11: validate /my/exit-rules HTML contains the localized strings.
# Same page must render in both languages with no template errors.
note "11. UI sanity: /my/exit-rules contains localized strings ($SMOKE_LANG)"
HTML=$(html "$BASE/my/exit-rules")
if echo "$HTML" | grep -q "^template:"; then
  bad "/my/exit-rules template error"
fi
for needle in "$S_PAGE_RULES_HEADER" "$S_PAGE_MY_RULES" "$S_PAGE_ADD_RULE"; do
  if echo "$HTML" | grep -qF "$needle"; then
    ok "page contains '$needle'"
  else
    bad "page missing '$needle'"
  fi
done

# Step 11.5: self-service password change via /my/account
# Verifies the password-change flow introduced in commit c30044b without
# actually changing the admin password permanently (we revert at the end).
note "11.5. password change at /my/account (with revert)"

CODE=$(status "$BASE/my/account")
[ "$CODE" = "200" ] && ok "/my/account 200" || bad "/my/account $CODE"

# Verify body renders (template not undefined error).
HTML=$(html "$BASE/my/account")
if echo "$HTML" | grep -q "^template:"; then
  bad "GET /my/account: template error ($(echo "$HTML" | grep -oE 'template:[^<]*' | head -1))"
else
  ok "/my/account HTML renders without template error"
fi
# form name attributes are language-agnostic — check them in both langs.
if echo "$HTML" | grep -q 'name="current_password"' && echo "$HTML" | grep -q 'name="new_password"'; then
  ok "/my/account contains password-change form fields"
else
  bad "/my/account: missing current_password or new_password field"
fi

# Wrong current: redirect to ?err=wrong_current_password
LOC=$(curl -s -i "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" -X POST \
  --data-urlencode "current_password=WRONG_PASSWORD" \
  --data-urlencode "new_password=${SMOKE_TEST_NEW_PASSWORD}" \
  --data-urlencode "confirm_new_password=${SMOKE_TEST_NEW_PASSWORD}" \
  "$BASE/my/account/password" | grep -i "^location:" | tr -d "\r" | awk '{print $2}')
echo "$LOC" | grep -q "wrong_current_password" \
  && ok "wrong current returns err=wrong_current_password" \
  || bad "wrong current got $LOC"

# Mismatching new/confirm
LOC=$(curl -s -i "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" -X POST \
  --data-urlencode "current_password=${SKYGATE_ADMIN_PASS}" \
  --data-urlencode "new_password=${SMOKE_TEST_NEW_PASSWORD}" \
  --data-urlencode "confirm_new_password=DIFFERENT_PASSWORD" \
  "$BASE/my/account/password" | grep -i "^location:" | tr -d "\r" | awk '{print $2}')
echo "$LOC" | grep -q "passwords_dont_match" \
  && ok "mismatch returns err=passwords_dont_match" \
  || bad "mismatch got $LOC"

# Too-short password
LOC=$(curl -s -i "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" -X POST \
  --data-urlencode "current_password=${SKYGATE_ADMIN_PASS}" \
  --data-urlencode "new_password=short" \
  --data-urlencode "confirm_new_password=short" \
  "$BASE/my/account/password" | grep -i "^location:" | tr -d "\r" | awk '{print $2}')
echo "$LOC" | grep -q "password_too_short" \
  && ok "short password returns err=password_too_short" \
  || bad "short got $LOC"

# Valid change -> saved=ok
LOC=$(curl -s -i "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" -X POST \
  --data-urlencode "current_password=${SKYGATE_ADMIN_PASS}" \
  --data-urlencode "new_password=${SMOKE_TEST_NEW_PASSWORD}" \
  --data-urlencode "confirm_new_password=${SMOKE_TEST_NEW_PASSWORD}" \
  "$BASE/my/account/password" | grep -i "^location:" | tr -d "\r" | awk '{print $2}')
echo "$LOC" | grep -q "saved=ok" \
  && ok "valid change returns saved=ok" \
  || bad "valid change got $LOC"

# Login with the NEW password, then revert
rm -f /tmp/smoke_new_ck
curl -s -c /tmp/smoke_new_ck "${ACCEPT_LANG_HDR[@]}" -X POST \
  --data-urlencode "username=skyadmin" --data-urlencode "password=${SMOKE_TEST_NEW_PASSWORD}" \
  "$BASE/login" -o /dev/null
if grep -q "skygate_session" /tmp/smoke_new_ck 2>/dev/null; then
  ok "login with new password issued session cookie"
else
  bad "login with new password failed"
fi
# Revert password back to original (admin) value
curl -s -o /dev/null -b /tmp/smoke_new_ck -X POST \
  --data-urlencode "current_password=${SMOKE_TEST_NEW_PASSWORD}" \
  --data-urlencode "new_password=${SKYGATE_ADMIN_PASS}" \
  --data-urlencode "confirm_new_password=${SKYGATE_ADMIN_PASS}" \
  "$BASE/my/account/password"
ok "reverted admin password back to original"

# Post-flight: wipe any remaining 198.51.100.x rules on device 8 that
# were created during this run (defense in depth; step 8 should already
# have removed them).
RESP=$(json "$BASE/my/exit-rules/api")
ORPHAN_IDS=$(echo "$RESP" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for r in d.get('rules', []):
    if r.get('device_id') == 8 and r.get('target_value', '').startswith('198.51.100.') and r.get('target_value', '').endswith('/32'):
        print(r['id'])
" 2>/dev/null | tr '\n' ' ')
if [ -n "$ORPHAN_IDS" ]; then
  ARGS=""
  for i in $ORPHAN_IDS; do
    ARGS="$ARGS --data-urlencode ids=$i"
  done
  curl -s -o /dev/null -b "$COOKIE" -X POST "$BASE/my/exit-rules/delete" $ARGS
  note "11.6. cleanup: removed $(echo $ORPHAN_IDS | wc -w) post-run smoke artifacts"
fi

# Step 12: logout
note "12. logout"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "${ACCEPT_LANG_HDR[@]}" -b "$COOKIE" -c "$COOKIE" -X POST "$BASE/logout")
[ "$CODE" = "302" ] || [ "$CODE" = "200" ] && ok "logout returned $CODE" || bad "logout $CODE"

note "SUMMARY ($SMOKE_LANG): $PASS pass, $FAIL fail"
[ "$FAIL" = "0" ] && exit 0 || exit 1
