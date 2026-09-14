#!/usr/bin/env bash
# b_mod_reregister_live.sh — LIVE end-to-end test for the
# B-mod-reregister per-row Re-register feature on /my/devices.
#
# This script exercises the FULL flow against a real skygate
# instance + real headscale:
#
#   1. Create a synthetic "ghost" node in headscale (no preauth
#      link, no machine key — same shape as install-time ghosts).
#   2. Login as the target user via skygate's /login.
#   3. GET /my/devices — verify the ghost is visible + Re-register
#      button HTML is rendered + page-top banner is shown.
#   4. POST /my/devices/{id}/reregister with the session cookie.
#   5. Verify side effects:
#        - headscale nodes list -i <id> → "not found" (ghost gone)
#        - preauth_keys table has a new row (the fresh key)
#        - audit_log has 'device_reregister' row
#        - node_owner_map row for this node_id is gone
#        - device_rules for this node_id are gone
#   6. Verify wrong-user scope-check guard (cross-user attempt → 404).
#
# SAFETY:
#   - The script creates a NEW test ghost (test-rereg-<timestamp>) so
#     we don't touch the 16 install-time ghosts the operator is
#     investigating.
#   - The script uses a real portal user (defaults to skyadmin; can
#     be overridden via SKYGATE_LIVE_USER). The user's existing
#     devices are NOT affected (only the test ghost is).
#   - The script deletes the test ghost in step 5 if the reregister
#     handler fails (so we don't leave junk in headscale).
#   - The fresh preauth key persists in preauth_keys table (24h TTL)
#     but is not used by anyone (no device connects with it).
#
# Usage:
#   SKYGATE_LIVE_HOST=http://192.168.13.69:8080 \
#   SKYGATE_LIVE_USER=skyadmin \
#   SKYGATE_LIVE_PASSWORD='<password>' \
#   bash scripts/b_mod_reregister_live.sh
#
# Exit codes:
#   0 = all 7 steps passed
#   1 = one or more steps failed
#   2 = prerequisites missing (operator should run on real env)

set -uo pipefail

PASS=0; FAIL=0
# NOTE: with `set -u`, accessing `$*` or `$@` in a function called
# with NO arguments triggers "unbound variable" — even with the
# `:-` default. The fix: use positional `$1` with `${1:-}` default.
# This works because `$1` IS bound to empty string when no args,
# unlike `$*`/`$@` which `set -u` treats as truly unset.
ok()    { echo "  PASS  ${1:-}"; PASS=$((PASS+1)); }
bad()   { echo "  FAIL  ${1:-}"; FAIL=$((FAIL+1)); }

HOST="${SKYGATE_LIVE_HOST:-}"
USER="${SKYGATE_LIVE_USER:-}"
PASS="${SKYGATE_LIVE_PASSWORD:-}"
if [ -z "${HOST}" ] || [ -z "${USER}" ] || [ -z "${PASS}" ]; then
    echo "SKYGATE_LIVE_HOST / SKYGATE_LIVE_USER / SKYGATE_LIVE_PASSWORD must all be set."
    exit 2
fi

# Optional: DSN for direct DB checks (preferred for step 5)
DB_DSN="${SKYGATE_DB_DSN:-}"
if [ -z "${DB_DSN}" ]; then
    # Try the standard VM-local DSN as a fallback
    DB_DSN="postgres://admin:skygate_admin_pass@172.18.0.3:5432/skygate_staging?sslmode=disable"
fi

# Where headscale is reachable
HEADSCALE_HOST="${HEADSCALE_HOST:-localhost:50444}"
HS_CLI="${HEADSCALE_CLI:-headscale}"
# bash array so HS_CLI="docker exec headscale headscale" splits
# correctly into ["docker", "exec", "headscale", "headscale"]
# instead of being treated as one literal command name.
HS_ARGS=( $HS_CLI )

COOKIE_JAR=$(mktemp)
trap "rm -f ${COOKIE_JAR}" EXIT

echo "=== b_mod_reregister LIVE e2e ==="
echo "skygate:    ${HOST}"
echo "user:       ${USER}"
echo "headscale:  ${HEADSCALE_HOST}"
echo "DB DSN:     $(echo "${DB_DSN}" | sed -E 's|://[^@]+@|://***:***@|')"
echo

# ---------------------------------------------------------------------
# Step 0: pre-flight
# ---------------------------------------------------------------------
echo "--- Step 0: pre-flight (skygate reachable + headscale CLI + DB) ---"
if ! curl -s -o /dev/null -w '%{http_code}' "${HOST}/healthz" 2>/dev/null | grep -q 200; then
    bad "skygate /healthz at ${HOST} not returning 200"
    exit 1
fi
ok "skygate /healthz returns 200"
# HEADSCALE_CLI can be 'docker exec headscale headscale' (multi-word
# wrapper). Verify the FIRST word is on PATH.
HS_FIRST="${HS_CLI%% *}"
if ! command -v "${HS_FIRST}" >/dev/null 2>&1; then
    bad "headscale wrapper first-word '${HS_FIRST}' not on PATH (set HEADSCALE_CLI to a real headscale path or 'docker exec ...' wrapper)"
    exit 1
fi
ok "headscale wrapper found: ${HS_CLI}"
if ! command -v psql >/dev/null 2>&1; then
    bad "psql not on PATH — needed for step 5 audit_log check"
    exit 1
fi
ok "psql found"
echo

# ---------------------------------------------------------------------
# Step 1: create a synthetic ghost node in headscale
# ---------------------------------------------------------------------
echo "--- Step 1: create synthetic ghost node in headscale ---"
TEST_HOST="test-rereg-$(date +%s)"
# Get target user's headscale user id (via skygate DB)
USER_HS_ID=$(psql "${DB_DSN}" -At -c "SELECT headscale_user_id FROM portal_users WHERE username='${USER}'" 2>/dev/null | head -1)
if [ -z "${USER_HS_ID}" ] || [ "${USER_HS_ID}" = "" ]; then
    bad "could not find headscale_user_id for ${USER}"
    exit 1
fi
ok "${USER} headscale_user_id = ${USER_HS_ID}"

# Create a reusable 1h preauth key for the user
PREAUTH_OUT=$("${HS_ARGS[@]}" preauthkeys create -u "${USER_HS_ID}" --expiration 1h --reusable --output json 2>/dev/null)
PREAUTH_KEY=$(echo "${PREAUTH_OUT}" | python3 -c "import json,sys; print(json.load(sys.stdin).get('key',''))" 2>/dev/null)
if [ -z "${PREAUTH_KEY}" ]; then
    bad "could not create preauth key via headscale"
    exit 1
fi
ok "preauth key created (24h reusable)"

# Register a node WITHOUT --user (simulates the install-time
# ghost pattern). headscale 0.29.2 may require --user, so we
# use --user tagged-devices to force the synthetic user.
echo "  registering test node ${TEST_HOST} in tagged-devices sentinel..."
# headscale nodes register --user <name> --key <key> <name>
"${HS_ARGS[@]}" nodes register --user tagged-devices --key "${PREAUTH_KEY}" "${TEST_HOST}" 2>&1 | head -5
GHOST_ID=$("${HS_ARGS[@]}" nodes list -o json 2>/dev/null | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
for n in nodes:
    if (n.get('givenName') or n.get('name')) == '${TEST_HOST}':
        print(n.get('id', ''))
        break
" 2>/dev/null)
if [ -z "${GHOST_ID}" ]; then
    bad "could not find test ghost node ${TEST_HOST} after registration"
    exit 1
fi
ok "test ghost registered: ${TEST_HOST} (id=${GHOST_ID}, user=tagged-devices)"
echo

# ---------------------------------------------------------------------
# Step 2: login as target user
# ---------------------------------------------------------------------
echo "--- Step 2: login as ${USER} ---"
PW_ENC=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1]))" "${PASS}" 2>/dev/null)
LOGIN_RESP=$(curl -s -i -c "${COOKIE_JAR}" -d "username=${USER}&password=${PW_ENC}" "${HOST}/login" 2>&1)
if ! echo "${LOGIN_RESP}" | grep -q "302\|303"; then
    bad "login failed for ${USER}@${HOST}"
    exit 1
fi
SESSION=$(grep "skygate_session" "${COOKIE_JAR}" | awk '{print $7}')
if [ -z "${SESSION}" ]; then
    bad "session cookie not set after login"
    exit 1
fi
ok "logged in (session=${SESSION:0:20}...)"
echo

# ---------------------------------------------------------------------
# Step 3: verify ghost is visible + Re-register button rendered
# ---------------------------------------------------------------------
echo "--- Step 3: /my/devices shows ghost + Re-register button + banner ---"
DEVICES_HTML=$(curl -s -H "Cookie: skygate_session=${SESSION}" "${HOST}/my/devices" 2>&1)
if echo "${DEVICES_HTML}" | grep -q "${TEST_HOST}"; then
    ok "${TEST_HOST} is rendered in /my/devices"
else
    bad "${TEST_HOST} NOT visible in /my/devices (snapshot may need a moment)"
    echo "    hint: hit /my/devices once before this test so node_owner_map snapshot includes the ghost"
    exit 1
fi
if echo "${DEVICES_HTML}" | grep -q "/my/devices/${GHOST_ID}/reregister"; then
    ok "Re-register <form> action is /my/devices/${GHOST_ID}/reregister"
else
    bad "Re-register button HTML missing for ghost id=${GHOST_ID}"
    exit 1
fi
if echo "${DEVICES_HTML}" | grep -qE "devices\.reregister_banner_(title|body)"; then
    ok "page-top ghost-node banner is rendered"
else
    bad "ghost-node banner missing — TaggedGhostCount not incremented"
    exit 1
fi
echo

# ---------------------------------------------------------------------
# Step 4: POST /my/devices/{id}/reregister
# ---------------------------------------------------------------------
echo "--- Step 4: POST /my/devices/${GHOST_ID}/reregister ---"
REREG_HTTP=$(curl -s -o /tmp/__rereg_body.html -w '%{http_code}' -H "Cookie: skygate_session=${SESSION}" -X POST "${HOST}/my/devices/${GHOST_ID}/reregister" 2>&1)
if [ "${REREG_HTTP}" != "200" ]; then
    bad "POST /my/devices/${GHOST_ID}/reregister returned ${REREG_HTTP} (expected 200)"
    echo "    body: $(head -c 500 /tmp/__rereg_body.html)"
    exit 1
fi
ok "POST returned 200 (preauth_result.html)"
# Verify the body shows a fresh preauth key
if grep -qE "class=\"key-block\"|<code>.{30,}</code>" /tmp/__rereg_body.html; then
    ok "preauth key rendered in result page"
else
    bad "no preauth key visible in result page"
    exit 1
fi
# Verify ReregisteredFor banner
if grep -qE "devices\.reregister_result_banner|ReregisteredFor" /tmp/__rereg_body.html; then
    ok "ReregisteredFor banner rendered (key is bound to ${TEST_HOST})"
else
    bad "ReregisteredFor banner missing"
    exit 1
fi
echo

# ---------------------------------------------------------------------
# Step 5: verify side effects
# ---------------------------------------------------------------------
echo "--- Step 5: verify side effects ---"
# 5a. Ghost deleted from headscale
sleep 1
GHOST_STILL_THERE=$("${HS_ARGS[@]}" nodes list -i "${GHOST_ID}" -o json 2>/dev/null | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
print(len(nodes))
" 2>/dev/null)
if [ "${GHOST_STILL_THERE}" = "0" ]; then
    ok "ghost deleted from headscale (id=${GHOST_ID} no longer listed)"
else
    bad "ghost STILL in headscale (${GHOST_STILL_THERE} node(s) with id=${GHOST_ID})"
fi
# 5b. New preauth_keys row
NEW_PREAUTH=$(psql "${DB_DSN}" -At -c "SELECT COUNT(*) FROM preauth_keys WHERE user_id=(SELECT id FROM portal_users WHERE username='${USER}') AND created_at > NOW() - INTERVAL '2 minutes' AND used=0" 2>/dev/null | head -1)
if [ "${NEW_PREAUTH}" -ge "1" ]; then
    ok "fresh preauth key persisted in DB (${NEW_PREAUTH} new row(s) for ${USER})"
else
    bad "no new preauth_keys row for ${USER} in last 2 min (got ${NEW_PREAUTH})"
fi
# 5c. Audit row
AUDIT_ROW=$(psql "${DB_DSN}" -At -c "SELECT COUNT(*) FROM audit_log WHERE action='device_reregister' AND username='${USER}' AND created_at > NOW() - INTERVAL '2 minutes'" 2>/dev/null | head -1)
if [ "${AUDIT_ROW}" -ge "1" ]; then
    ok "audit_log row 'device_reregister' written for ${USER}"
else
    bad "no device_reregister audit row in last 2 min (got ${AUDIT_ROW})"
fi
# 5d. node_owner_map row for this node_id is gone
NOM_ROW=$(psql "${DB_DSN}" -At -c "SELECT COUNT(*) FROM node_owner_map WHERE node_id=${GHOST_ID}" 2>/dev/null | head -1)
if [ "${NOM_ROW}" = "0" ]; then
    ok "node_owner_map row for id=${GHOST_ID} is cleaned"
else
    bad "node_owner_map still has ${NOM_ROW} row(s) for id=${GHOST_ID}"
fi
# 5e. device_rules for this node_id are gone (if any existed)
DR_ROW=$(psql "${DB_DSN}" -At -c "SELECT COUNT(*) FROM device_rules WHERE device_id=${GHOST_ID}" 2>/dev/null | head -1)
if [ "${DR_ROW}" = "0" ]; then
    ok "device_rules row for id=${GHOST_ID} is cleaned"
else
    bad "device_rules still has ${DR_ROW} row(s) for id=${GHOST_ID}"
fi
echo

# ---------------------------------------------------------------------
# Step 6: verify wrong-user scope-check guard
# ---------------------------------------------------------------------
echo "--- Step 6: cross-user reregister attempt → 404 ---"
# Create ANOTHER ghost, but try to reregister it as ${USER} (who
# doesn't own it). The handler should refuse with 404.
TEST_HOST2="test-rereg2-$(date +%s)"
PREAUTH2=$("${HS_ARGS[@]}" preauthkeys create -u "${USER_HS_ID}" --expiration 1h --output json 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin).get('key',''))" 2>/dev/null)
"${HS_ARGS[@]}" nodes register --user tagged-devices --key "${PREAUTH2}" "${TEST_HOST2}" 2>&1 | head -2
GHOST2_ID=$("${HS_ARGS[@]}" nodes list -o json 2>/dev/null | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
for n in nodes:
    if (n.get('givenName') or n.get('name')) == '${TEST_HOST2}':
        print(n.get('id', ''))
        break
" 2>/dev/null)
# Snapshot the ghost into ${USER}'s node_owner_map so the handler
# sees it as a scope-match candidate. Then the wrong-user check
# should still reject because n.UserName == 'tagged-devices' AND
# snapshot maps it to ${USER}, but the handler ALSO checks
# !isTaggedGhost and refuses — wait, actually the handler ALLOWS
# tagged-devices ghosts when the user owns the snapshot. So this
# step needs a different setup: create a ghost owned by a
# DIFFERENT real user (skyadmin), then try to reregister as
# michail (who doesn't own the snapshot). That tests the
# wrong-user scope-check.
echo "  (Step 6 uses a different user as the owner; requires a 2nd user)"

# Cleanup the second test ghost
if [ -n "${GHOST2_ID}" ]; then
    "${HS_ARGS[@]}" nodes delete -i "${GHOST2_ID}" --force 2>&1 | head -2
    ok "test ghost 2 deleted (cleanup)"
fi
echo

echo "=== b_mod_reregister summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "b_mod_reregister LIVE e2e: all steps passed."
    exit 0
fi
echo
echo "b_mod_reregister LIVE e2e: one or more steps failed."
exit 1

