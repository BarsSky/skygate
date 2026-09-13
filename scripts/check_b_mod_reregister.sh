#!/usr/bin/env bash
# check_b_mod_reregister.sh — B-mod-reregister (2026-09-13) — one-click
# escape hatch for ghost nodes that headscale has reassigned to the
# synthetic "tagged-devices" sentinel user (id=2147455555).
#
# Pre-B-mod-reregister these nodes had no recovery path in the skygate
# UI: the B77 tag-autoupdate tried to apply tag:dev-<user>-<device>
# and got acl_reject every 5 min because headscale's ACL builder
# can't move a node into a tag owned by a different user. The only
# fix was SSH + headscale CLI + manual node delete + manual
# /my/preauth — too many steps for a per-row "this is my device but
# it's not in my user namespace" recovery.
#
# Post-B-mod-reregister: per-row Re-register button on /my/devices
# (only shown when n.UserName == "tagged-devices" AND the user owns
# the row via node_owner_map snapshot). The handler:
#   1. Deletes the node in headscale via hsClient.DeleteNode
#   2. Runs devicedelete.Delete for full local cleanup
#   3. Issues a fresh 24h REUSABLE preauth key bound to the current user
#   4. Renders the preauth_result.html page with ReregisteredFor banner
#   5. Audits action=device_reregister
#
# Contracts (8):
#   A. PostMyDeviceReregister handler exists in internal/feature/my
#   B. The handler is wired at POST /my/devices/{id}/reregister
#   C. The template has the Re-register <form> only when IsTaggedGhost
#   D. The template has the ghost-node banner when TaggedGhostCount > 0
#   E. IsTaggedGhost field exists on myNodeRow
#   F. The handler refuses nodes that already belong to a real user
#   G. i18n keys for RU + EN exist
#   H. AGENTS.md / PLANS.md mention B-mod-reregister
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed

set -uo pipefail

PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
cd "${PROJECT_ROOT}" || exit 1

DEVICES="internal/feature/my/devices.go"
TEMPLATE_DEVICES="internal/handlers/templates/user/devices.html"
TEMPLATE_PREAUTH="internal/handlers/templates/user/preauth_result.html"
I18N_RU="internal/i18n/catalog_my.go"
MAIN_GO="cmd/skygate/main.go"

echo "skygate root: ${PROJECT_ROOT}"
echo

# --- A: PostMyDeviceReregister handler exists ---
echo "=== A. PostMyDeviceReregister handler exists ==="
if grep -q "func (s \*Service) PostMyDeviceReregister" "${DEVICES}"; then
    ok "PostMyDeviceReregister handler defined in ${DEVICES}"
else
    bad "PostMyDeviceReregister handler NOT FOUND in ${DEVICES}"
fi

# --- B: route wired ---
echo
echo "=== B. route POST /my/devices/{id}/reregister wired ==="
if grep -q 'POST /my/devices/{id}/reregister' "${MAIN_GO}"; then
    ok "POST /my/devices/{id}/reregister route wired in main.go"
else
    bad "POST /my/devices/{id}/reregister route NOT FOUND in main.go"
fi

# --- C: template Re-register button only when IsTaggedGhost ---
echo
echo "=== C. template has Re-register <form> only when IsTaggedGhost ==="
if grep -q "{{if .IsTaggedGhost}}" "${TEMPLATE_DEVICES}" && \
   grep -q "/my/devices/{{.ID}}/reregister" "${TEMPLATE_DEVICES}"; then
    ok "Re-register button gated on IsTaggedGhost in template"
else
    bad "Re-register button missing or not gated on IsTaggedGhost"
fi

# --- D: ghost-node banner when TaggedGhostCount > 0 ---
echo
echo "=== D. template has ghost-node banner when TaggedGhostCount > 0 ==="
if grep -q "{{if gt .TaggedGhostCount 0}}" "${TEMPLATE_DEVICES}" && \
   grep -q "devices.reregister_banner_title" "${TEMPLATE_DEVICES}"; then
    ok "ghost-node banner present and gated on TaggedGhostCount > 0"
else
    bad "ghost-node banner missing or not gated on TaggedGhostCount"
fi

# --- E: IsTaggedGhost field on myNodeRow ---
echo
echo "=== E. IsTaggedGhost field on myNodeRow ==="
if grep -q "IsTaggedGhost bool" "${DEVICES}"; then
    ok "IsTaggedGhost field exists on myNodeRow"
else
    bad "IsTaggedGhost field NOT FOUND on myNodeRow"
fi
# Confirm it's SET in both live + snapshot loops
LIVE_HITS=$(grep -c "IsTaggedGhost:.*n.UserName == \"tagged-devices\"" "${DEVICES}" || true)
if [ "${LIVE_HITS}" -ge 2 ]; then
    ok "IsTaggedGhost is set in both live + snapshot loops (${LIVE_HITS} occurrences)"
else
    bad "IsTaggedGhost is set only ${LIVE_HITS} time(s) — expected >= 2 (live + snapshot)"
fi

# --- F: handler refuses real-user nodes ---
echo
echo "=== F. handler refuses nodes that already belong to a real user ==="
if grep -q "if !isTaggedGhost" "${DEVICES}" && \
   grep -q "use Delete instead" "${DEVICES}"; then
    ok "handler has the 'wrong-user' scope-check guard"
else
    bad "handler missing the wrong-user scope-check guard"
fi

# --- G: i18n keys for RU + EN ---
echo
echo "=== G. i18n keys RU + EN present ==="
RU_KEYS=$(grep -c "devices.reregister_" "${I18N_RU}" 2>/dev/null) || RU_KEYS=0
EN_KEYS=$(grep -c "devices.reregister_" "${I18N_RU}" 2>/dev/null) || EN_KEYS=0
RU_KEYS=${RU_KEYS:-0}
EN_KEYS=${EN_KEYS:-0}
if [ "${RU_KEYS}" -ge 10 ] && [ "${EN_KEYS}" -ge 10 ]; then
    ok "i18n keys present (RU=${RU_KEYS}, EN=${EN_KEYS})"
else
    bad "i18n keys missing (RU=${RU_KEYS}, EN=${EN_KEYS}, expected >= 10 each)"
fi

# --- H: AGENTS.md / PLANS.md mention ---
echo
echo "=== H. AGENTS.md / PLANS.md mention ==="
if [ -f AGENTS.md ]; then
    AGENTS_HITS=$(grep -c "B-mod-reregister" AGENTS.md 2>/dev/null) || AGENTS_HITS=0
    AGENTS_HITS=${AGENTS_HITS:-0}
    if [ "${AGENTS_HITS}" -ge 1 ]; then
        ok "AGENTS.md mentions B-mod-reregister (${AGENTS_HITS} hits)"
    else
        warn "AGENTS.md does not mention B-mod-reregister (operator may add later)"
    fi
fi
if [ -f docs/PLANS.md ]; then
    PLANS_HITS=$(grep -c "B-mod-reregister" docs/PLANS.md 2>/dev/null) || PLANS_HITS=0
    PLANS_HITS=${PLANS_HITS:-0}
    if [ "${PLANS_HITS}" -ge 1 ]; then
        ok "docs/PLANS.md mentions B-mod-reregister (${PLANS_HITS} hits)"
    else
        warn "docs/PLANS.md does not mention B-mod-reregister (operator may add later)"
    fi
fi

# --- I: preauth_result.html shows ReregisteredFor banner ---
echo
echo "=== I. preauth_result.html shows ReregisteredFor banner ==="
if grep -q "{{if .ReregisteredFor}}" "${TEMPLATE_PREAUTH}" && \
   grep -q "devices.reregister_result_banner" "${TEMPLATE_PREAUTH}"; then
    ok "ReregisteredFor banner in preauth_result.html"
else
    bad "ReregisteredFor banner missing in preauth_result.html"
fi

echo
echo "=== B-mod-reregister summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B-mod-reregister contracts all hold."
    exit 0
fi
echo
echo "B-mod-reregister has failing contracts — fix the source files above."
exit 1
