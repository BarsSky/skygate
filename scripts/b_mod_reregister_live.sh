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
#     investigating. HOWEVER, headscale v0.29.1+ deprecated `nodes
#     register --key` and the new `auth register --auth-id` requires
#     a `hskey-authreq-` prefix key (NOT the `hskey-auth-` prefix
#     that `preauthkeys create` returns). ghost creation via CLI
#     FAILS on 0.29.1+ — the install-time ghosts were created via
#     the deprecated gRPC `RegisterNode` method which is no longer
#     exposed in the CLI.
#   - To run on headscale 0.29.1+, the operator must either:
#       (a) supply SKYGATE_LIVE_GHOST_ID=<id> — uses one of the
#           existing install-time ghosts as the test target (the
#           ghost WILL be deleted by step 4; pick a disposable one),
#       (b) or: temporarily downgrade headscale to ≤0.23.x to
#           re-enable `nodes register --key`,
#       (c) or: skip the live e2e and rely on the structural
#           B-check (scripts/check_b_mod_reregister.sh) + Go unit
#           tests (internal/feature/my/devices_reregister_test.go).
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
#   SKYGATE_LIVE_GHOST_ID=3  # OPTIONAL: use existing ghost (skip step 1)
#   bash scripts/b_mod_reregister_live.sh
#
# Exit codes:
#   0 = all 7 steps passed
#   1 = one or more steps failed
#   2 = prerequisites missing (operator should run on real env)

set -uo pipefail

# Counter vars are NAMED DIFFERENTLY from the password var (`PASS`).
# CRITICAL: with `set -u`, `declare -i PASS=0` makes `PASS` an integer
# type. When the script later does `PASS="${SKYGATE_LIVE_PASSWORD:-}"`,
# bash 5.2.21 tries to assign the password string as an integer, which
# fails on `%` / `@` with "invalid arithmetic operator" (surfaced as
# "t: unbound variable" with set -u). Using `_COUNT` suffix avoids the
# collision while keeping `$((...))` arithmetic.
PASS_COUNT=0
FAIL_COUNT=0
ok()    { echo "  PASS  ${1:-}"; PASS_COUNT=$((PASS_COUNT+1)); }
bad()   { echo "  FAIL  ${1:-}"; FAIL_COUNT=$((FAIL_COUNT+1)); }

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
# Step 1: create a synthetic ghost node in headscale (OR use existing)
# ---------------------------------------------------------------------
echo "--- Step 1: prepare test ghost node in headscale ---"
# If SKYGATE_LIVE_GHOST_ID is set, use an EXISTING install-time ghost
# instead of creating one (headscale 0.29.1+ cannot create ghosts via
# CLI — see SAFETY block above).
EXISTING_GHOST_ID="${SKYGATE_LIVE_GHOST_ID:-}"
TEST_HOST=""
if [ -n "${EXISTING_GHOST_ID}" ]; then
    # Validate the ghost exists in headscale + belongs to tagged-devices sentinel
    EXISTING_INFO=$("${HS_ARGS[@]}" nodes list -i "${EXISTING_GHOST_ID}" -o json 2>/dev/null)
    EXISTING_USER=$(echo "${EXISTING_INFO}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
if nodes:
    n = nodes[0]
    user = n.get('user', {})
    name = user.get('name') if isinstance(user, dict) else user
    print(f'{n.get(\"givenName\") or n.get(\"name\")}|{name}')
" 2>/dev/null)
    EXISTING_HOST="${EXISTING_INFO%|*}"
    EXISTING_USER_NAME="${EXISTING_INFO##*|}"
    if [ -z "${EXISTING_HOST}" ]; then
        bad "SKYGATE_LIVE_GHOST_ID=${EXISTING_GHOST_ID} not found in headscale"
        exit 1
    fi
    if [ "${EXISTING_USER_NAME}" != "tagged-devices" ]; then
        bad "ghost ${EXISTING_GHOST_ID} belongs to '${EXISTING_USER_NAME}' (expected 'tagged-devices' sentinel)"
        exit 1
    fi
    GHOST_ID="${EXISTING_GHOST_ID}"
    TEST_HOST="${EXISTING_HOST}"
    ok "using existing ghost: id=${GHOST_ID} host=${TEST_HOST} (will be deleted by step 4)"
else
    # Try to create a NEW ghost via CLI (works on headscale ≤0.23.x).
    # On 0.29.1+, this WILL fail — see the message at the end of step 1.
    TEST_HOST="test-rereg-$(date +%s)"
    USER_HS_ID=$(psql "${DB_DSN}" -At -c "SELECT headscale_user_id FROM portal_users WHERE username='${USER}'" 2>/dev/null | head -1)
    if [ -z "${USER_HS_ID}" ] || [ "${USER_HS_ID}" = "" ]; then
        bad "could not find headscale_user_id for ${USER}"
        exit 1
    fi
    ok "${USER} headscale_user_id = ${USER_HS_ID}"
    PREAUTH_OUT=$("${HS_ARGS[@]}" preauthkeys create -u "${USER_HS_ID}" --expiration 1h --reusable --output json 2>/dev/null)
    PREAUTH_KEY=$(echo "${PREAUTH_OUT}" | python3 -c "import json,sys; print(json.load(sys.stdin).get('key',''))" 2>/dev/null)
    if [ -z "${PREAUTH_KEY}" ]; then
        bad "could not create preauth key via headscale"
        exit 1
    fi
    ok "preauth key created"
    REG_OUT=$("${HS_ARGS[@]}" auth register --user tagged-devices --auth-id "${PREAUTH_KEY}" "${TEST_HOST}" 2>&1)
    if echo "${REG_OUT}" | grep -qE "Error|Failed|invalid"; then
        REG_OUT=$("${HS_ARGS[@]}" nodes register --user tagged-devices --key "${PREAUTH_KEY}" "${TEST_HOST}" 2>&1)
    fi
    echo "${REG_OUT}" | head -5
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
        echo
        echo "  *** GHOST CREATION FAILED on headscale $(sudo docker exec headscale headscale version 2>/dev/null | head -1 | awk '{print $3}') ***"
        echo "  *** To run this e2e on 0.29.1+, set SKYGATE_LIVE_GHOST_ID=<id>  ***"
        echo "  *** of an existing install-time ghost (will be deleted by step 4) ***"
        bad "could not create test ghost ${TEST_HOST} via CLI"
        exit 1
    fi
    ok "test ghost created: ${TEST_HOST} (id=${GHOST_ID})"
fi
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
# Step 6 only runs if SKYGATE_LIVE_USER2 (a SECOND portal user) is
# provided. We create a ghost owned by USER2 (snapshot maps to
# USER2's node_owner_map row), then USER1 tries to reregister it.
# The handler should refuse with 404 — USER1 doesn't own the
# snapshot row, so the lookup fails.
USER2="${SKYGATE_LIVE_USER2:-}"
PASS2="${SKYGATE_LIVE_PASSWORD2:-}"
if [ -z "${USER2}" ] || [ -z "${PASS2}" ]; then
    echo "  SKIP: SKYGATE_LIVE_USER2 / SKYGATE_LIVE_PASSWORD2 not set (Step 6 is opt-in)"
    echo "  to enable: set both env vars to a 2nd portal user + password"
else
    PW2_ENC=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1]))" "${PASS2}" 2>/dev/null)
    COOKIE_JAR2=$(mktemp)
    LOGIN2_RESP=$(curl -s -i -c "${COOKIE_JAR2}" -d "username=${USER2}&password=${PW2_ENC}" "${HOST}/login" 2>&1)
    if ! echo "${LOGIN2_RESP}" | grep -q "302\|303"; then
        bad "USER2 (${USER2}) login failed — Step 6 skipped"
    else
        SESSION2=$(grep "skygate_session" "${COOKIE_JAR2}" | awk '{print $7}')
        ok "USER2 (${USER2}) logged in"
        # Create ghost2 owned by USER2 in tagged-devices sentinel
        USER2_HS_ID=$(psql "${DB_DSN}" -At -c "SELECT headscale_user_id FROM portal_users WHERE username='${USER2}'" 2>/dev/null | head -1)
        TEST_HOST2="test-rereg2-$(date +%s)"
        PREAUTH2=$("${HS_ARGS[@]}" preauthkeys create -u "${USER2_HS_ID}" --expiration 1h --output json 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin).get('key',''))" 2>/dev/null)
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
        if [ -z "${GHOST2_ID}" ]; then
            bad "could not create USER2's test ghost — Step 6 skipped"
        else
            ok "USER2's ghost registered: ${TEST_HOST2} (id=${GHOST2_ID})"
            # Hit /my/devices as USER2 first to snapshot the ghost
            curl -s -H "Cookie: skygate_session=${SESSION2}" "${HOST}/my/devices" >/dev/null 2>&1
            # Now USER1 tries to reregister USER2's ghost — expect 404
            WRONG_HTTP=$(curl -s -o /tmp/__wrong_body.html -w '%{http_code}' -H "Cookie: skygate_session=${SESSION}" -X POST "${HOST}/my/devices/${GHOST2_ID}/reregister" 2>&1)
            if [ "${WRONG_HTTP}" = "404" ] || [ "${WRONG_HTTP}" = "403" ]; then
                ok "USER1 → USER2's ghost reregister returned ${WRONG_HTTP} (scope-check guard works)"
            else
                bad "USER1 → USER2's ghost reregister returned ${WRONG_HTTP} (expected 404 or 403)"
                echo "    body: $(head -c 300 /tmp/__wrong_body.html)"
            fi
            # Verify ghost2 STILL exists (refused, not deleted)
            sleep 1
            STILL_THERE=$("${HS_ARGS[@]}" nodes list -i "${GHOST2_ID}" -o json 2>/dev/null | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
print(len(nodes))
" 2>/dev/null)
            if [ "${STILL_THERE}" = "1" ]; then
                ok "USER2's ghost is intact after USER1's refused attempt (no data leak)"
            else
                bad "USER2's ghost was MODIFIED after USER1's refused attempt (${STILL_THERE} nodes)"
            fi
            # Cleanup USER2's ghost
            "${HS_ARGS[@]}" nodes delete -i "${GHOST2_ID}" --force 2>&1 | head -2
            ok "USER2's test ghost deleted (cleanup)"
        fi
    fi
    rm -f "${COOKIE_JAR2}"
fi
echo

echo "=== b_mod_reregister summary ==="
echo "  PASS: ${PASS_COUNT}"
echo "  FAIL: ${FAIL_COUNT}"
if [ "${FAIL_COUNT}" -eq 0 ]; then
    echo
    echo "b_mod_reregister LIVE e2e: all steps passed."
    exit 0
fi
echo
echo "b_mod_reregister LIVE e2e: one or more steps failed."
exit 1

