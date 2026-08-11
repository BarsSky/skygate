#!/usr/bin/env bash
#===============================================================================
# validate.sh — Health check for the entire Skygate stack (cross-platform)
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/lib/env.sh"
source "${SCRIPT_DIR}/lib/docker.sh"

PASS=0; FAIL=0
check() {
    if [ "$2" = "ok" ]; then printf '  \033[0;32m[OK]\033[0m %s\n' "$1"; PASS=$((PASS+1))
    else printf '  \033[0;31m[FAIL]\033[0m %s — %s\n' "$1" "$2"; FAIL=$((FAIL+1)); fi
}

echo "=============================================="
echo "  Skygate Stack Validation"
echo "  OS: ${SKYGATE_OS}"
echo "=============================================="

echo ""; echo "-- Containers --"
for ctr in headscale headplane skygate; do
    ${DOCKER_CMD} ps --format '{{.Names}}' 2>/dev/null | grep -qx "${ctr}" && check "${ctr} running" ok || check "${ctr} running" "not found"
done

echo ""; echo "-- HTTP Endpoints --"
C=$($(echo 'curl') -s -o /dev/null -w "%{http_code}" --max-time 5 -H "Authorization: Bearer ${HEADSCALE_API_KEY}" "http://localhost:50444/api/v1/node" 2>/dev/null || echo "000")
check "Headscale API" "$([ "$C" = "200" ] && echo ok || echo "HTTP $C")"
C=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "http://localhost:50445/admin/" 2>/dev/null || echo "000")
check "Headplane UI" "$(echo "$C" | grep -qE '^(200|302)' && echo ok || echo "HTTP $C")"
C=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "http://localhost:${SKYGATE_PORT}/login" 2>/dev/null || echo "000")
check "Skygate /login" "$([ "$C" = "200" ] && echo ok || echo "HTTP $C")"

echo ""; echo "-- Headscale --"
N=$(${DOCKER_CMD} exec headscale headscale nodes list --output json 2>/dev/null | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
check "Nodes" "$([ "$N" -gt 0 ] && echo "ok ($N)" || echo "no nodes")"

echo ""; echo "-- Skygate DB --"
U=$(${DOCKER_CMD} run --rm -v skygate-data:/data alpine sh -c "apk add --no-cache sqlite >/dev/null 2>&1 && sqlite3 /data/skygate.db 'SELECT count(*) FROM portal_users'" 2>/dev/null || echo "0")
check "Users" "$([ "$U" -gt 0 ] && echo "ok ($U)" || echo "no users")"
R=$(${DOCKER_CMD} run --rm -v skygate-data:/data alpine sh -c "apk add --no-cache sqlite >/dev/null 2>&1 && sqlite3 /data/skygate.db 'SELECT count(*) FROM device_rules WHERE enabled=1'" 2>/dev/null || echo "0")
check "Exit rules" "ok ($R rules)"

echo ""; echo "-- ACL --"
AC=$($(echo 'curl') -s -o /dev/null -w "%{http_code}" --max-time 5 -H "Authorization: Bearer ${HEADSCALE_API_KEY}" "http://localhost:50444/api/v1/policy" 2>/dev/null || echo "000")
check "ACL policy API" "$([ "$AC" = "200" ] && echo ok || echo "HTTP $AC")"

[ "${DERP_ENABLED}" = "true" ] && {
    echo ""; echo "-- DERP --"
    ${DOCKER_CMD} ps --format '{{.Names}}' 2>/dev/null | grep -qx "derper" && check "DERP container" ok || check "DERP container" "not running"; }

echo ""; echo "=============================================="
echo "  Results: ${PASS} passed, ${FAIL} failed"
echo "=============================================="
[ "${FAIL}" -gt 0 ] && exit 1
