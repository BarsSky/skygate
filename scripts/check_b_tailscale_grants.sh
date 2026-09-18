#!/usr/bin/env bash
# check_b_tailscale_grants.sh — Phase 7 (v1.5.2+) — verifies that
# skygate's Tailscale ACL grants (tagOwners / per-user / per-device)
# are correctly produced AND applied in headscale.
#
# Mode: dual-mode check (structural + live).
#
#   STRUCTURAL mode (always runs):
#     Verifies that the source code contains the necessary
#     building blocks for Tailscale ACL grants:
#     - GenerateACLForPlane exists (internal/acl/acl.go)
#     - tagOwners emission loops for tag:public, tag:exit-node,
#       tag:subnet-router, tag:private (the static 4) + per-user
#       tags loop from GetPerUserDeviceTags
#     - per-device grants with via=[] from device_exit_node_prefs
#     - distinctVias loop emits per-device-pref tags via the
#       B227 audit identity (skyadmin for non-infra, infra for infra)
#     - emitTagOwner dedup helper prevents duplicate object keys
#       (the v1.3.18 hotfix for "duplicate object member name"
#       bug when per-device-prefs collide with infra tags)
#     - The base domain is read from env (SKYGATE_BASE_DOMAIN) or
#       defaults to skygate.local; tagOwners always uses
#       <username>@<baseDomain> format that headscale accepts
#
#   LIVE mode (SKIPS if SKIP_LIVE=1 or if headscale unreachable):
#     Live checks require:
#       - A running headscale reachable via headscale CLI OR REST
#       - At least 3 portal users with at least one device each
#       - At least 1 exit-node tagged "tag:exit-node"
#       - At least 1 subnet-router tagged "tag:subnet-router"
#     Without these the live assertions cannot validate the
#     end-to-end contract. Skips gracefully with a WARN.
#
# Live contracts (run only when LIVE):
#     A.1  Every user device in node_owner_map has a
#          tag:dev-<user>-<hostname> in headscale
#     A.2  Exit-node devices have tag:exit-node AND tag:dev-infra-<hostname>
#     A.3  Subnet routers have tag:subnet-router
#     A.4  Public devices (tag:public) do NOT have user-scoped tags
#     B.1  headscale nodes list --json returns the per-device tags
#     C.1  The generated policy (from /admin/headscale/acl/apply)
#          contains tagOwners entries for every user-tag pair
#     D.1  New device registration triggers B77 auto-tag within
#          5 minutes (verifies the autoupdater cron is running)
#
# Exit codes:
#   0 = all contracts hold (live assertions may be SKIPped)
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

ACL_FILE="internal/acl/acl.go"
PREAUTH_FILE="internal/deployrun/steps/generate_preauth_key.go"
ACL_APPLY_FILE="internal/feature/admin/headscale_acl.go"
EXIT_SVR_FILE="internal/db/exit_node_prefs.go"

echo "skygate root: ${PROJECT_ROOT}"
echo "mode: $( [ -z "${SKIP_LIVE:-}" ] && echo "structural + live" || echo "structural only (SKIP_LIVE=1)" )"
echo

# =====================================================================
# STRUCTURAL MODE
# =====================================================================
echo "=== STRUCTURAL mode (always runs) ==="
echo

# --- A.1: GenerateACLForPlane exists ---
echo "--- A.1 GenerateACLForPlane exists in internal/acl/acl.go ---"
if grep -q "func GenerateACLForPlane" "${ACL_FILE}" 2>/dev/null; then
    ok "GenerateACLForPlane defined"
else
    bad "GenerateACLForPlane NOT FOUND in ${ACL_FILE}"
fi

# --- A.2: tagOwners static emission blocks exist ---
echo
echo "--- A.2 tagOwners static emission blocks (tag:public / tag:exit-node / tag:subnet-router / tag:private) ---"
STATIC_TAGS=0
# The static 4 tags are written inline via sb.WriteString (not
# via the emitTagOwner helper — that's only used by the per-user
# loop below). We match any line that mentions the tag in a
# tagOwners context (the key string in the JSON object).
for tag in "tag:public" "tag:exit-node" "tag:subnet-router" "tag:private"; do
    # Match either form: emitTagOwner("tag:X", ...) OR the inline
    # sb.WriteString pattern that emits "tag:X" as a JSON key.
    if grep -q "${tag}.*baseDomain\|emitTagOwner.*${tag}" "${ACL_FILE}" 2>/dev/null; then
        STATIC_TAGS=$((STATIC_TAGS+1))
    else
        bad "missing static tagOwners block for ${tag}"
    fi
done
if [ "${STATIC_TAGS}" -ge 4 ]; then
    ok "all 4 static tagOwners blocks present (${STATIC_TAGS}/4)"
fi

# --- A.3: per-user tagOwners loop from GetPerUserDeviceTags ---
echo
echo "--- A.3 per-user tagOwners loop uses GetPerUserDeviceTags ---"
if grep -q "GetPerUserDeviceTags" "${ACL_FILE}" 2>/dev/null && \
   grep -q "tagsByUser\[dt.Username\]" "${ACL_FILE}" 2>/dev/null; then
    ok "per-user tagOwners loop is wired to GetPerUserDeviceTags"
else
    bad "per-user tagOwners loop missing the GetPerUserDeviceTags wiring"
fi

# --- A.4: per-device grants with via=[] ---
echo
echo "--- A.4 per-device grants with via=[] from device_exit_node_prefs ---"
if grep -q "viaByDevice" "${ACL_FILE}" 2>/dev/null && \
   grep -q "device_exit_node_prefs" "${ACL_FILE}" 2>/dev/null; then
    ok "per-device via-grant loop is wired to device_exit_node_prefs"
else
    bad "per-device via-grant loop missing the device_exit_node_prefs wiring"
fi

# --- A.5: emitTagOwner dedup helper ---
echo
echo "--- A.5 emitTagOwner dedup helper prevents duplicate object keys ---"
if grep -q "emittedTagOwners" "${ACL_FILE}" 2>/dev/null && \
   grep -q "if emittedTagOwners\[" "${ACL_FILE}" 2>/dev/null; then
    ok "emitTagOwner dedup helper present (v1.3.18 hotfix)"
else
    bad "emitTagOwner dedup helper missing"
fi

# --- A.6: tagOwner struct + sort + emit loop ---
echo
echo "--- A.6 tagOwner struct + sort + emit loop ---"
if grep -q "type tagOwner struct" "${ACL_FILE}" 2>/dev/null && \
   grep -q "sort.Slice(tagOwners" "${ACL_FILE}" 2>/dev/null; then
    ok "tagOwner struct + sort.Slice stable-diff loop"
else
    bad "tagOwner struct / sort missing"
fi

# --- A.7: GetPerUserDeviceTags helper exists in db ---
echo
echo "--- A.7 GetPerUserDeviceTags helper exists in internal/db ---"
if grep -rn "func GetPerUserDeviceTags" internal/db 2>/dev/null; then
    ok "GetPerUserDeviceTags helper defined"
else
    bad "GetPerUserDeviceTags helper missing"
fi

# --- A.8: device_exit_node_prefs table has via_enabled column ---
echo
echo "--- A.8 device_exit_node_prefs has via_enabled + pref columns ---"
if grep -q "via_enabled" internal/db/migrate* 2>/dev/null || \
   grep -q "via_enabled" "${EXIT_SVR_FILE}" 2>/dev/null; then
    ok "device_exit_node_prefs has via_enabled"
else
    bad "device_exit_node_prefs missing via_enabled"
fi

# --- A.9: per-device-pref tags go through tagOwners (B227/B188 hotfix) ---
echo
echo "--- A.9 per-device-pref tags are emitted via augmentedTagsByUser ---"
if grep -q "augmentedTagsByUser" "${ACL_FILE}" 2>/dev/null; then
    ok "augmentedTagsByUser loop present (v1.3.18 hotfix for 'src=tag not found')"
else
    bad "augmentedTagsByUser loop missing"
fi

# --- A.10: preauth step has --user flag (root cause: install-time ghost nodes) ---
echo
echo "--- A.10 generate_preauth_key passes --user (B-mod-reregister root-cause guard) ---"
if grep -q "userID" "${PREAUTH_FILE}" 2>/dev/null && \
   grep -q "preauthkeys" "${PREAUTH_FILE}" 2>/dev/null; then
    ok "generate_preauth_key passes user_id to headscale (no ghost nodes)"
else
    warn "verify generate_preauth_key signature passes --user (root cause guard for B-mod-reregister)"
fi

# --- A.11: ACL apply routes are wired in main.go ---
echo
echo "--- A.11 /admin/headscale/acl/* apply routes wired ---"
APPLY_ROUTES=0
# The actual routes are /admin/acls/import/apply (POST) and
# /admin/headscale/acl/add (POST). Both must be wired.
for route in "/admin/acls/import/apply" "/admin/headscale/acl/add" "/admin/headscale/acl/remove"; do
    if grep -q "POST ${route}" cmd/skygate/main.go 2>/dev/null; then
        APPLY_ROUTES=$((APPLY_ROUTES+1))
    else
        bad "POST ${route} route not registered in main.go"
    fi
done
if [ "${APPLY_ROUTES}" -ge 3 ]; then
    ok "all 3 ACL apply routes registered (${APPLY_ROUTES}/3)"
fi

# --- A.12: B77 autoupdater has the cron entry (the runtime tag-applier) ---
echo
echo "--- A.12 B77 autoupdater cron entry exists ---"
if grep -rn "StartAutoBackfill\|BackfillInfra\|RunAutoBackfill" internal/nodeownership/ 2>/dev/null; then
    ok "B77 autoupdater has at least one of: StartAutoBackfill / BackfillInfra / RunAutoBackfill"
else
    bad "B77 autoupdater cron entry missing"
fi

echo
echo "=== STRUCTURAL summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
echo "  SKIP: ${SKIP}"
STRUCTURAL_FAIL=${FAIL}

# =====================================================================
# LIVE MODE (skipped when SKIP_LIVE=1 OR when no headscale reachable)
# =====================================================================
if [ -n "${SKIP_LIVE:-}" ]; then
    echo
    echo "LIVE mode skipped (SKIP_LIVE=1 set)"
    LIVE_RESULT=0
elif ! command -v headscale >/dev/null 2>&1 && [ -z "${HEADSCALE_CLI:-}" ]; then
    echo
    echo "LIVE mode skipped: headscale CLI not on PATH and HEADSCALE_CLI not set"
    LIVE_RESULT=0
else
    echo
    echo "=== LIVE mode ==="
    # Run the live assertions in a sub-script so a SKIP doesn't break
    # the structural summary.
    if HEADSCALE_CLI="${HEADSCALE_CLI:-}" bash "${SCRIPT_DIR}/check_b_tailscale_grants_live.sh"; then
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
    echo "check_b_tailscale_grants: contracts all hold (live assertions OK or skipped)."
    exit 0
fi
if [ "${FAIL}" -ne 0 ]; then
    echo
    echo "check_b_tailscale_grants: STRUCTURAL FAIL — fix the source files above."
    exit 1
fi
echo
echo "check_b_tailscale_grants: LIVE FAIL — the runtime grants don't match the contract."
exit 2
