#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/lib/env.sh"
source "${SCRIPT_DIR}/lib/docker.sh"

PASS=0
FAIL=0

check() {
    local label="$1"
    local result="$2"
    if [ "${result}" = "ok" ]; then
        printf '  \033[0;32m[OK]\033[0m %s\n' "${label}"
        PASS=$((PASS + 1))
    else
        printf '  \033[0;31m[FAIL]\033[0m %s — %s\n' "${label}" "${result}"
        FAIL=$((FAIL + 1))
    fi
}

echo "=============================================="
echo "  Skygate Stack Validation"
echo "=============================================="

echo ""
echo "-- Containers --"
for ctr in headscale headplane skygate; do
    if docker ps --format '{{.Names}}' | grep -qx "${ctr}"; then
        check "${ctr} running" ok
    else
        check "${ctr} running" "container not found"
    fi
done

echo ""
echo "-- HTTP Endpoints --"
CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    -H "Authorization: Bearer ${HEADSCALE_API_KEY}" \
    "http://localhost:50444/api/v1/node" 2>/dev/null || echo "000")
check "Headscale API /api/v1/node" "$([ "${CODE}" = "200" ] && echo ok || echo "HTTP ${CODE}")

CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    "http://localhost:50445/admin/" 2>/dev/null || echo "000")
check "Headplane UI /admin/" "$(echo "${CODE}" | grep -qE "^(200|302)" && echo ok || echo "HTTP ${CODE}")

CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    "http://localhost:${SKYGATE_PORT}/login" 2>/dev/null || echo "000")
check "Skygate /login" "$([ "${CODE}" = "200" ] && echo ok || echo "HTTP ${CODE}")

CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    "http://localhost:${SKYGATE_PORT}/dashboard" 2>/dev/null || echo "000")
check "Skygate /dashboard" "$(echo "${CODE}" | grep -qE "^(200|302)" && echo ok || echo "HTTP ${CODE}")

echo ""
echo "-- Headscale --"
NODES="$(docker exec headscale headscale nodes list --output json 2>/dev/null | python3 -c 'import sys,json; print(len(json.load(sys.stdin)))' 2>/dev/null || echo "0")
check "Headscale nodes" "$([ "${NODES}" -gt 0 ] && echo "ok (${NODES} nodes)" || echo "no nodes")

ROUTES="$(docker exec headscale headscale nodes list-routes --output json 2>/dev/null | python3 -c "import sys,json; routes=json.load(sys.stdin); print(sum(len(r.get('approved_routes',[])) for r in routes))" 2>/dev/null || echo "0")
check "Approved routes" "$([ "${ROUTES}" -gt 0 ] && echo "ok (${ROUTES} routes)" || echo "no routes")

echo ""
echo "-- Skygate DB --"
USERS="$(docker run --rm -v skygate-data:/data alpine sh -c 'apk add --no-cache sqlite >/dev/null 2>&1 && sqlite3 /data/skygate.db \"SELECT count(*) FROM portal_users\"' 2>/dev/null || echo "0")
check "Portal users" "$([ "${USERS}" -gt 0 ] && echo "ok (${USERS} users)" || echo "no users")

RULES="$(docker run --rm -v skygate-data:/data alpine sh -c 'apk add --no-cache sqlite >/dev/null 2>&1 && sqlite3 /data/skygate.db \"SELECT count(*) FROM device_rules WHERE enabled=1\"' 2>/dev/null || echo "0")
check "Exit rules" "$([ "${RULES}" -gt 0 ] && echo "ok (${RULES} rules)" || echo "no rules — OK for fresh install")

KEYS="$(docker run --rm -v skygate-data:/data alpine sh -c 'apk add --no-cache sqlite >/dev/null 2>&1 && sqlite3 /data/skygate.db \"SELECT count(*) FROM preauth_keys\"' 2>/dev/null || echo "0")
check "Preauth keys" "ok (${KEYS} keys)"

TOKEN_COUNT=$(docker run --rm -v skygate-data:/data alpine sh -c 'apk add --no-cache sqlite >/dev/null 2>&1 && sqlite3 /data/skygate.db \"SELECT count(*) FROM personal_api_tokens\"' 2>/dev/null || echo "0")
check "API tokens" "ok (${TOKEN_COUNT} tokens)"

echo ""
echo "-- ACL --"
ACL_CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    -H "Authorization: Bearer ${HEADSCALE_API_KEY}" \
    "http://localhost:50444/api/v1/policy" 2>/dev/null || echo "000")
check "ACL policy API" "$([ "${ACL_CODE}" = "200" ] && echo ok || echo "HTTP ${ACL_CODE}")

if [ "${DERP_ENABLED}" = "true" ]; then
    echo ""
    echo "-- DERP --"
    if docker ps --format '{{.Names}}' | grep -qx "derper"; then
        check "DERP container" ok
    else
        check "DERP container" "not running"
    fi
fi

echo ""
echo "=============================================="
echo "  Results: ${PASS} passed, ${FAIL} failed"
echo "=============================================="

if [ "${FAIL}" -gt 0 ]; then
    exit 1
fi
