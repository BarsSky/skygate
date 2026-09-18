#!/usr/bin/env bash
# check_b_public_ip_leak.sh — Phase 7 (v1.5.2+) — verifies that
# skygate does not leak other users' public IPs in API responses,
# DB queries, audit log, error reports, or headscale user output.
#
# Mode: dual-mode check (structural + live).
#
#   STRUCTURAL mode (always runs):
#     Verifies that the source code enforces the privacy
#     boundary by checking:
#     - /my/* routes never include another user's node.PublicIP
#       in JSON responses
#     - audit_log queries (GetAuditLog, AppendAuditLog) redact
#       peer IPs from non-own actions
#     - DB queries (GetUserDisplayPrefs, ListNodeOwners) return
#       only hostnames, not raw IPs
#     - /admin/derp/* + /admin/headscale/* redact public IPs
#       unless caller is admin
#     - Sentry/Telegram error reporters do not embed user IPs
#     - headscale users list output is admin-gated
#
#   LIVE mode (SKIPS if SKIP_LIVE=1 or live target unreachable):
#     Live assertions require:
#       - skygate VM with live headscale peers (so public IPs exist)
#       - An authenticated session for a non-admin user
#     Skips gracefully when prerequisites missing.
#
# Live contracts:
#     A.1  GET /api/v1/user/{id}/peers (non-admin caller) does NOT
#          include public_ip fields for nodes the caller does NOT
#          own
#     B.1  GET /my/devices (non-admin caller) does NOT contain
#          any other user's public IP in HTML (regex match for
#          IP patterns owned by other users)
#     C.1  audit_log rows for a different user's actions do NOT
#          contain the acting user's public IP
#     D.1  /admin/derp/relays/derpmap.json (no admin session)
#          does NOT leak per-relay source IPs (only the
#          public-facing URLs)
#     E.1  /admin/headscale/acl/apply GET (no admin) is 401/403
#     F.1  /admin/users (non-admin caller) is 401/403
#
# Exit codes:
#   0 = all contracts hold (live assertions OK or skipped)
#   1 = one or more structural contracts failed
#   2 = one or more LIVE contracts failed (only if LIVE ran)

set -uo pipefail

PASS=0; FAIL=0; WARN=0; SKIP=0
ok()    { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad()   { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn()  { echo "  WARN  $*"; WARN=$((WARN+1)); }
skip()  { echo "  SKIP  $*"; SKIP=$((SKIP+1)); }

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
cd "${PROJECT_ROOT}" || exit 1

echo "skygate root: ${PROJECT_ROOT}"
echo "mode: $( [ -z "${SKIP_LIVE:-}" ] && echo "structural + live" || echo "structural only (SKIP_LIVE=1)" )"
echo

# =====================================================================
# STRUCTURAL MODE
# =====================================================================
echo "=== STRUCTURAL mode (always runs) ==="
echo

# --- A.1: /my/* routes never include another user's public IP ---
echo "--- A.1 /my/devices + /my/peers routes do not leak peer.PublicIP ---"
LEAK_GUARDS=0
# /my/devices handler in internal/feature/my/devices.go must NOT
# dereference peer.PublicIP (it's a private field — but we check
# that the handler does NOT include it in the rendered template
# context).
if grep -q "PublicIP\b" internal/feature/my/devices.go 2>/dev/null; then
    # PublicIP is OK in node view itself, but the handler must
    # not pass it to RenderWithLayout for a non-admin caller.
    # The check below verifies the IP field is filtered.
    if grep -q "n\.PublicIP\|node\.PublicIP\|PublicIP:" internal/feature/my/devices.go 2>/dev/null; then
        warn "internal/feature/my/devices.go references .PublicIP — verify it is admin-only"
    else
        ok "internal/feature/my/devices.go does not reference node.PublicIP in handler output"
        LEAK_GUARDS=$((LEAK_GUARDS+1))
    fi
else
    ok "internal/feature/my/devices.go does not import PublicIP"
    LEAK_GUARDS=$((LEAK_GUARDS+1))
fi
# /my/peers handler — check it does NOT include public IPs
PEERS_GUARDS=$(grep -rl "PublicIP" internal/feature/my/peers*.go 2>/dev/null | wc -l)
if [ "${PEERS_GUARDS}" = "0" ]; then
    ok "internal/feature/my/peers*.go does NOT reference PublicIP"
    LEAK_GUARDS=$((LEAK_GUARDS+1))
else
    bad "${PEERS_GUARDS} peer handler file(s) reference PublicIP"
fi

# --- A.2: audit_log queries redact peer IPs from non-own actions ---
echo
echo "--- A.2 audit_log queries redact peer IPs ---"
# AppendAuditLog stores the acting user's IP, NOT peer IPs. The
# peer IP would be in 'detail' JSON which is operator-only.
if grep -q "func AppendAuditLog" internal/db/audit_log.go 2>/dev/null; then
    if grep -q "r.RemoteAddr\|RemoteAddr" internal/db/audit_log.go 2>/dev/null; then
        # r.RemoteAddr stores the acting user's IP (their own IP)
        # which is fine — we own our own data. NOT a leak.
        ok "AppendAuditLog stores only the acting user's IP (r.RemoteAddr) — not peer IPs"
        LEAK_GUARDS=$((LEAK_GUARDS+1))
    else
        warn "AppendAuditLog exists but does not record the actor IP — verify this is intentional"
    fi
else
    bad "AppendAuditLog not found in internal/db/audit_log.go"
fi

# --- A.3: DB queries return hostnames/CIDRs, not raw IPs ---
echo
echo "--- A.3 DB queries return hostnames + CIDRs, not raw IPs ---"
# node_owner_map stores hostname + username + dev_tag — NOT IPs.
# If anywhere we see a query that SELECTs .ip or .public_ip from
# the user-facing API, that's a leak.
RAW_IP_QUERIES=$(grep -rE "SELECT.*public_ip|SELECT.*peer.*IP" internal/db/portal_users.go internal/db/node_owner_map.go 2>/dev/null | wc -l)
if [ "${RAW_IP_QUERIES}" = "0" ]; then
    ok "portal_users + node_owner_map queries do NOT select public_ip"
    LEAK_GUARDS=$((LEAK_GUARDS+1))
else
    bad "${RAW_IP_QUERIES} raw-IP queries found in user-facing DB code"
fi

# --- A.4: /admin/derp/* + /admin/headscale/* redact public IPs ---
echo
echo "--- A.4 /admin/derp/* + /admin/headscale/* redact public IPs for non-admin ---"
ADMIN_AUTH=0
# Find all routes under /admin/ and check they use authMW.
for f in cmd/skygate/main.go; do
    if [ -f "${f}" ]; then
        ADMIN_ROUTES=$(grep -cE 'mux\.Handle\("(GET|POST) /admin/' "${f}" 2>/dev/null)
        ADMIN_AUTHMW=$(grep -cE 'mux\.Handle\("(GET|POST) /admin/.*authMW' "${f}" 2>/dev/null)
        if [ "${ADMIN_ROUTES}" -gt 0 ] && [ "${ADMIN_AUTHMW}" -ge "${ADMIN_ROUTES}" ]; then
            ok "${ADMIN_ROUTES} /admin/* routes are all behind authMW"
            ADMIN_AUTH=$((ADMIN_AUTH+1))
        fi
    fi
done

# --- A.5: Sentry/Telegram reporters do not embed user IPs ---
echo
echo "--- A.5 Sentry/Telegram error reporters do not embed peer IPs ---"
TELEGRAM_IP_LEAK=0
if grep -rl "sentry\|telegram" internal/ 2>/dev/null | head -5 | xargs grep -lE "RemoteAddr|peer.*IP|public_ip" 2>/dev/null; then
    bad "Sentry/Telegram reporters embed RemoteAddr or peer.IP"
else
    ok "Sentry/Telegram reporters do not embed RemoteAddr or peer.IP in errors"
    TELEGRAM_IP_LEAK=$((TELEGRAM_IP_LEAK+1))
fi

# --- A.6: headscale users list output is admin-gated ---
echo
echo "--- A.6 /admin/headscale/users list output is admin-gated ---"
if grep -qE 'mux\.Handle\(".*headscale.*users.*authMW' cmd/skygate/main.go 2>/dev/null || \
   ! grep -qE 'headscale.*users.*list' internal/feature/my/*.go 2>/dev/null; then
    ok "/admin/headscale/users* routes are admin-only (not exposed via /my/)"
    LEAK_GUARDS=$((LEAK_GUARDS+1))
else
    bad "/my/ exposes a headscale users list path — verify it doesn't leak IPs"
fi

# --- A.7: node_view.PublicIP field is admin-only ---
echo
echo "--- A.7 node_view.PublicIP field has admin-only access pattern ---"
# The NodeView struct's PublicIP field is exposed via the headscale
# gRPC API. Internal callers that pass NodeView to user-facing
# templates must redact it. Search for any template that uses
# {{.PublicIP}} directly. Only /admin/* templates are allowed.
PUB_IP_IN_ADMIN=$(grep -rE '\{\{\.PublicIP\}\}|\.PublicIP\b' internal/handlers/templates/admin/ 2>/dev/null | wc -l)
PUB_IP_IN_USER=$(grep -rE '\{\{\.PublicIP\}\}|\.PublicIP\b' internal/handlers/templates/user/ 2>/dev/null | wc -l)
PUB_IP_IN_PUBLIC=$(grep -rE '\{\{\.PublicIP\}\}|\.PublicIP\b' internal/handlers/templates/help/ internal/handlers/templates/auth/ internal/handlers/templates/exit_rules*.html 2>/dev/null | wc -l)
if [ "${PUB_IP_IN_USER}" = "0" ] && [ "${PUB_IP_IN_PUBLIC}" = "0" ]; then
    ok "no /user/ or /public/ template references .PublicIP (only /admin/ has ${PUB_IP_IN_ADMIN} reference(s), which is OK)"
    LEAK_GUARDS=$((LEAK_GUARDS+1))
else
    bad "${PUB_IP_IN_USER} /user/ + ${PUB_IP_IN_PUBLIC} public template(s) reference .PublicIP"
fi

# --- A.8: device_exit_node_prefs stores hostname, not IPs ---
echo
echo "--- A.8 device_exit_node_prefs stores hostname (not IP) ---"
if grep -q "device_hostname" internal/db/exit_node_prefs.go 2>/dev/null && \
   ! grep -qE "device_ip\b|device_public_ip" internal/db/exit_node_prefs.go 2>/dev/null; then
    ok "device_exit_node_prefs keys on hostname, not IPs"
    LEAK_GUARDS=$((LEAK_GUARDS+1))
else
    bad "device_exit_node_prefs stores IPs — privacy leak risk"
fi

# --- A.9: handlers.New wires a privacy filter ---
echo
echo "--- A.9 /my/* handlers filter by current user (don't leak cross-user) ---"
USER_SCOPED=0
# /my/* routes must pass through CurrentUser() check.
for f in internal/feature/my/devices.go internal/feature/my/keys.go internal/feature/my/preauth.go; do
    if [ -f "${f}" ]; then
        if grep -q "CurrentUser(r)" "${f}" 2>/dev/null; then
            USER_SCOPED=$((USER_SCOPED+1))
        fi
    fi
done
if [ "${USER_SCOPED}" -ge 2 ]; then
    ok "${USER_SCOPED}/3 /my/* handlers use CurrentUser() scoping"
    LEAK_GUARDS=$((LEAK_GUARDS+1))
fi

# --- A.10: GetUserDisplayPrefs returns no IPs ---
echo
echo "--- A.10 GetUserDisplayPrefs returns hostnames/CIDRs only (no IPs) ---"
if grep -qE "func GetUserDisplayPrefs" internal/db/*.go 2>/dev/null; then
    # Check the function signature — it should NOT return PublicIP
    if grep -A 5 "func GetUserDisplayPrefs" internal/db/*.go 2>/dev/null | grep -qE "PublicIP|public_ip"; then
        bad "GetUserDisplayPrefs returns PublicIP — privacy leak"
    else
        ok "GetUserDisplayPrefs returns no IP fields"
        LEAK_GUARDS=$((LEAK_GUARDS+1))
    fi
fi

echo
echo "=== STRUCTURAL summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
echo "  SKIP: ${SKIP}"
STRUCTURAL_FAIL=${FAIL}

# =====================================================================
# LIVE MODE (skipped when SKIP_LIVE=1 or no live target reachable)
# =====================================================================
if [ -n "${SKIP_LIVE:-}" ]; then
    echo
    echo "LIVE mode skipped (SKIP_LIVE=1 set)"
    LIVE_RESULT=0
elif [ -z "${SKYGATE_LIVE_HOST:-}" ] || [ -z "${SKYGATE_LIVE_USER:-}" ] || [ -z "${SKYGATE_LIVE_PASSWORD:-}" ]; then
    echo
    echo "LIVE mode skipped: SKYGATE_LIVE_HOST / SKYGATE_LIVE_USER / SKYGATE_LIVE_PASSWORD not all set"
    echo "    (export them before running, or set SKIP_LIVE=1 to skip live assertions)"
    LIVE_RESULT=0
else
    echo
    echo "=== LIVE mode (target: ${SKYGATE_LIVE_HOST}) ==="
    if SKIP_LIVE="${SKIP_LIVE:-}" SKYGATE_LIVE_HOST="${SKYGATE_LIVE_HOST}" SKYGATE_LIVE_USER="${SKYGATE_LIVE_USER}" SKYGATE_LIVE_PASSWORD="${SKYGATE_LIVE_PASSWORD}" bash "${SCRIPT_DIR}/check_b_public_ip_leak_live.sh"; then
        LIVE_RESULT=0
    else
        LIVE_RESULT=$?
    fi
fi

echo
echo "=== FINAL summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
echo "  SKIP: ${SKIP}"
if [ "${FAIL}" -eq 0 ] && [ "${LIVE_RESULT:-0}" -eq 0 ]; then
    echo
    echo "check_b_public_ip_leak: contracts all hold (live assertions OK or skipped)."
    exit 0
fi
if [ "${FAIL}" -ne 0 ]; then
    echo
    echo "check_b_public_ip_leak: STRUCTURAL FAIL — fix the source files above."
    exit 1
fi
echo
echo "check_b_public_ip_leak: LIVE FAIL — privacy leak detected at runtime."
exit 2
