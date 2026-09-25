#!/usr/bin/env bash
# check_b_public_ip_leak_live.sh — LIVE assertions for B-check
# Phase 7 privacy-leak detector. Requires:
#   SKYGATE_LIVE_HOST  e.g. http://192.168.13.69:8080
#   SKYGATE_LIVE_USER  e.g. skyadmin
#   SKYGATE_LIVE_PASSWORD  skygate password (use --password-file for prod)
#
# Strategy: log in as a NON-ADMIN user, request every /my/* and
# /api/v1/user/* endpoint, and assert that no other user's
# public IP appears in the response body.

set -uo pipefail

PASS=0; FAIL=0; SKIP=0
# Use `${1:-}` instead of `$*` so the function is safe to call
# with no args under `set -u`.
ok()    { echo "  PASS  ${1:-}"; PASS=$((PASS+1)); }
bad()   { echo "  FAIL  ${1:-}"; FAIL=$((FAIL+1)); }
skip()  { echo "  SKIP  ${1:-}"; SKIP=$((SKIP+1)); }

HOST="${SKYGATE_LIVE_HOST:-}"
USER="${SKYGATE_LIVE_USER:-}"
PASS="${SKYGATE_LIVE_PASSWORD:-}"
if [ -z "${HOST}" ] || [ -z "${USER}" ] || [ -z "${PASS}" ]; then
    skip "SKYGATE_LIVE_HOST / SKYGATE_LIVE_USER / SKYGATE_LIVE_PASSWORD not all set"
    exit 2
fi

# Login → get session cookie
COOKIE_JAR=$(mktemp)
trap "rm -f ${COOKIE_JAR}" EXIT

# URL-encode the password for form post
PW_ENC=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1]))" "${PASS}" 2>/dev/null || echo "${PASS}")
LOGIN_RESP=$(curl -s -i -c "${COOKIE_JAR}" -d "username=${USER}&password=${PW_ENC}" "${HOST}/login" 2>&1)
if ! echo "${LOGIN_RESP}" | grep -q "302\|303"; then
    skip "login failed for ${USER}@${HOST} — check credentials"
    exit 2
fi
SESSION=$(grep "skygate_session" "${COOKIE_JAR}" | awk '{print $7}')
if [ -z "${SESSION}" ]; then
    skip "session cookie not set after login"
    exit 2
fi
echo "  logged in as ${USER}; session=${SESSION:0:20}..."
echo

# --- A.1: /api/v1/user/{id}/peers (non-admin caller) ---
echo "--- A.1 /api/v1/user/* peers does NOT leak other-user IPs ---"
PEERS_BODY=$(curl -s -H "Cookie: skygate_session=${SESSION}" "${HOST}/api/v1/user/1/peers" 2>&1)
# Extract any IP-looking strings and assert none of them
# belong to other users (we don't have a list, so we just
# assert the response contains no peer.PublicIP fields).
PUBLIC_IP_LEAK=$(echo "${PEERS_BODY}" | python3 -c "
import sys, re
text = sys.stdin.read()
# Match anything that looks like a public IPv4
ips = re.findall(r'\b(?:\d{1,3}\.){3}\d{1,3}\b', text)
print(len(ips))
" 2>/dev/null)
if [ -z "${PUBLIC_IP_LEAK}" ]; then
    skip "python3 not available for IP regex"
    exit 2
fi
if [ "${PUBLIC_IP_LEAK}" = "0" ]; then
    ok "/api/v1/user/1/peers contains 0 IPv4 addresses"
else
    bad "/api/v1/user/1/peers contains ${PUBLIC_IP_LEAK} IPv4 address(es) — privacy leak"
fi

# --- B.1: /my/devices HTML ---
echo
echo "--- B.1 /my/devices HTML contains no IPv4 ---"
DEVICES_HTML=$(curl -s -H "Cookie: skygate_session=${SESSION}" "${HOST}/my/devices" 2>&1)
DEV_IPS=$(echo "${DEVICES_HTML}" | python3 -c "
import sys, re
text = sys.stdin.read()
ips = re.findall(r'\b(?:\d{1,3}\.){3}\d{1,3}\b', text)
print(len(ips))
" 2>/dev/null)
if [ "${DEV_IPS}" = "0" ]; then
    ok "/my/devices HTML contains 0 IPv4 addresses"
else
    bad "/my/devices HTML contains ${DEV_IPS} IPv4 address(es) — privacy leak"
fi

# --- C.1: audit_log for cross-user actions does NOT contain IPs ---
echo
echo "--- C.1 audit_log rows for cross-user actions ---"
# We can't query DB from here without pg credentials — leave this
# to manual verification OR future scripts/b_public_ip_leak_db.sh.
skip "C.1 requires direct DB access (left to manual verify or DB-side script)"

# --- D.1: /admin/derp/relays/derpmap.json without admin session ---
echo
echo "--- D.1 /admin/derp/relays/derpmap.json returns clean data without admin ---"
DERP_BODY=$(curl -s -H "Cookie: skygate_session=${SESSION}" "${HOST}/admin/derp/relays/derpmap.json" 2>&1)
DERP_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -H "Cookie: skygate_session=${SESSION}" "${HOST}/admin/derp/relays/derpmap.json" 2>&1)
if [ "${DERP_STATUS}" = "401" ] || [ "${DERP_STATUS}" = "403" ] || [ "${DERP_STATUS}" = "404" ]; then
    ok "/admin/derp/relays/derpmap.json requires auth (${DERP_STATUS})"
elif [ "${DERP_STATUS}" = "200" ]; then
    # If it IS 200 (intentionally public), verify the body doesn't
    # contain relay source IPs.
    SOURCE_IPS=$(echo "${DERP_BODY}" | python3 -c "
import sys, re
text = sys.stdin.read()
ips = re.findall(r'\b(?:\d{1,3}\.){3}\d{1,3}\b', text)
print(len(ips))
" 2>/dev/null)
    if [ "${SOURCE_IPS}" = "0" ]; then
        ok "/admin/derp/relays/derpmap.json returns 0 source IPs"
    else
        bad "/admin/derp/relays/derpmap.json leaks ${SOURCE_IPS} source IP(s)"
    fi
else
    warn "unexpected status ${DERP_STATUS} from /admin/derp/relays/derpmap.json"
fi

# --- E.1: /admin/headscale/acl/apply GET requires admin ---
echo
echo "--- E.1 /admin/headscale/acl requires admin (non-admin caller) ---"
ACL_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -H "Cookie: skygate_session=${SESSION}" "${HOST}/admin/headscale/acl" 2>&1)
if [ "${ACL_STATUS}" = "401" ] || [ "${ACL_STATUS}" = "403" ]; then
    ok "/admin/headscale/acl requires auth (${ACL_STATUS})"
else
    bad "/admin/headscale/acl returned ${ACL_STATUS} for non-admin caller"
fi

# --- F.1: /admin/users requires admin ---
echo
echo "--- F.1 /admin/users requires admin (non-admin caller) ---"
USERS_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -H "Cookie: skygate_session=${SESSION}" "${HOST}/admin/users" 2>&1)
if [ "${USERS_STATUS}" = "401" ] || [ "${USERS_STATUS}" = "403" ]; then
    ok "/admin/users requires auth (${USERS_STATUS})"
else
    bad "/admin/users returned ${USERS_STATUS} for non-admin caller"
fi

echo
echo "=== LIVE summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  SKIP: ${SKIP}"
if [ "${FAIL}" -eq 0 ]; then
    exit 0
fi
exit 1

# B322 (2026-09-25): reach the gate with a non-zero exit on a recorded FAIL.
[ "${FAIL:-0}" -eq 0 ] || exit 1
