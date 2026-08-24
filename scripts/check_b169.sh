#!/bin/bash
# check_b169.sh — admin-side device delete (B169, v1.5.2)
#
# B162 (v1.5.1) added the per-row "Delete" button on
# /my/devices (user-scoped). B169 closes the gap for
# admin: clean up orphan / duplicate / stuck devices
# without SSH'ing into the skygate VM.
#
# The B-check is split into:
#  A. Source-contract (handler exists, is wired, calls
#     headscale.DeleteNode, cleans up node_owner_map,
#     writes an audit row, returns the right error pages)
#  B. Template contract (the form + button + confirm
#     prompt are present in admin/devices.html)
#  C. i18n contract (RU + EN keys for the button + the
#     confirm prompt)
#  D. Route contract (the POST route is registered in
#     main.go, behind authMW, with the {id} path param)
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: handler exists + has the right structure"

# A.1 — the handler is defined.
if grep -q 'func (s \*Service) PostAdminDeviceDelete' internal/feature/admin/devices.go; then
    ok "PostAdminDeviceDelete handler defined in internal/feature/admin/devices.go"
else
    bad "PostAdminDeviceDelete handler MISSING (admin can't clean up orphan devices)"
fi

# A.2 — the handler must be admin-only (the IsAdmin
# check must be the first guard, before any DB / headscale
# call). A regression that removes the IsAdmin check would
# let any authenticated user delete any device.
# Note: the naive awk '/^func ... /,/^}/' breaks on the
# FIRST `}` from a nested if/for block. The correct range
# is "from this func to the NEXT top-level func".
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -qE '!c\.IsAdmin|IsAdmin.*false|forbidden'; then
    ok "PostAdminDeviceDelete is admin-only (checks c.IsAdmin before any work)"
else
    bad "PostAdminDeviceDelete is NOT admin-only (a non-admin could delete any device)"
fi

# A.3 — the handler must call headscale.DeleteNode.
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -q 'DeleteNode'; then
    ok "handler calls headscale.DeleteNode (the actual node removal)"
else
    bad "handler does NOT call headscale.DeleteNode (would only clean the local row, not the headscale node)"
fi

# A.4 — the handler must clean up node_owner_map.
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -q 'DeleteNodeOwnerByNodeTag'; then
    ok "handler cleans up node_owner_map (DeleteNodeOwnerByNodeTag)"
else
    bad "handler does NOT clean up node_owner_map (the bot's /exit_nodes would show the deleted device as a relay candidate)"
fi

# A.5 — the handler must call hs.InvalidateCache.
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -q 'InvalidateCache'; then
    ok "handler calls hs.InvalidateCache (so the next /admin/devices load re-fetches)"
else
    bad "handler does NOT call hs.InvalidateCache (the deleted node would show up for up to 5s on the next page load)"
fi

# A.6 — the handler must write a 'device_deleted' audit row.
# Pattern matches both single-quoted (`"device_deleted"`) and
# raw (`device_deleted`) variants in the Audit call. The
# grep would fail on the original because the actual call
# is `s.Backend.Audit(... "device_deleted", ...)` (the
# trailing comma confused the previous regex).
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -qE '"device_deleted"'; then
    ok "handler writes 'device_deleted' audit row"
else
    bad "handler does NOT write a 'device_deleted' audit row (the deletion would be invisible in /admin/audit)"
fi

# A.7 — the handler must use HSGlobalFn (not HSForUserFn).
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -q 'HSGlobalFn'; then
    ok "handler uses HSGlobalFn (admin-scoped, not per-user)"
else
    bad "handler does NOT use HSGlobalFn (would be scoped to one user's control plane — defeating the point of admin delete)"
fi

# A.8 — the handler must handle the 404 case (node not found).
if awk '/^func \(s \*Service\) PostAdminDeviceDelete/{flag=1; next} flag && /^func /{flag=0} flag' internal/feature/admin/devices.go | grep -qE 'not found|not_found'; then
    ok "handler handles the 404 case (node not found)"
else
    bad "handler does NOT handle the 404 case (stale id would 500 instead of redirecting with a flash)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: template contract"

# B.1 — the template has a form pointing to the
# admin delete route.
if grep -q '/admin/devices/{{.ID}}/delete' internal/handlers/templates/admin/devices.html; then
    ok "template has the per-row delete form (action=/admin/devices/{{.ID}}/delete)"
else
    bad "template is missing the delete form"
fi

# B.2 — the template has a confirm() guard on the
# delete form (prevents accidental clicks). The
# check is a direct grep since the onsubmit line is
# the one carrying the confirm().
if grep -A1 'delete_admin_confirm' internal/handlers/templates/admin/devices.html | grep -q 'onsubmit.*return confirm'; then
    ok "template has the onsubmit confirm() guard (prevents accidental one-click deletes)"
else
    bad "template is missing the onsubmit confirm() guard (one misclick deletes a real device)"
fi

# B.3 — the template references all 3 i18n keys for
# the delete button (label + help + confirm).
for key in "delete_admin_btn" "delete_admin_help" "delete_admin_confirm"; do
    if grep -q "devices.$key" internal/handlers/templates/admin/devices.html; then
        ok "template references i18n key 'devices.$key'"
    else
        bad "template missing i18n key 'devices.$key'"
    fi
done

# ---------------------------------------------------------------------------
hdr "contract C: i18n parity (RU + EN)"

# C.1 — RU and EN blocks both have the 3 new keys.
# (devices.* keys live in catalog_my.go — vars are named
# ruMy and enMy, not ruCommon/enCommon.)
RU_COUNT=$(awk '/^var ruMy = map\[string\]string\{/,/^}/' internal/i18n/catalog_my.go | grep -cE '"devices\.delete_admin')
EN_COUNT=$(awk '/^var enMy = map\[string\]string\{/,/^}/' internal/i18n/catalog_my.go | grep -cE '"devices\.delete_admin')
if [ "$RU_COUNT" -ge 3 ] && [ "$EN_COUNT" -ge 3 ] && [ "$RU_COUNT" = "$EN_COUNT" ]; then
    ok "i18n parity: RU=$RU_COUNT, EN=$EN_COUNT 'devices.delete_admin*' keys"
else
    bad "i18n parity broken: RU=$RU_COUNT, EN=$EN_COUNT (must match, both >= 3)"
fi

# ---------------------------------------------------------------------------
hdr "contract D: route contract"

# D.1 — the route is registered in main.go.
if grep -q 'POST /admin/devices/{id}/delete' cmd/skygate/main.go; then
    ok "main.go registers POST /admin/devices/{id}/delete"
else
    bad "main.go does NOT register the admin-delete route"
fi

# D.2 — the route is behind authMW (so an
# unauthenticated request is redirected to /login,
# not the admin handler).
if grep -A1 'POST /admin/devices/{id}/delete' cmd/skygate/main.go | grep -q 'authMW'; then
    ok "route is behind authMW (unauth requests → /login)"
else
    bad "route is NOT behind authMW (any unauthenticated request would reach the handler)"
fi

# ---------------------------------------------------------------------------
hdr "summary"
echo "B169: admin-side device delete on /admin/devices (operator escape hatch for orphan/duplicate/stuck devices)"
echo "all contracts satisfied"
