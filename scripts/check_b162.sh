#!/bin/bash
# check_b162.sh — /my/devices per-row device delete (B162, v1.5.1)
#
# Operator 2026-08-24 follow-up to B160 (device renew):
# "необходимо добавить удаление устройства из headscale
# со страницы пользователя Мои устройства, также завершать
# сессию". The Delete button removes the device from
# headscale (so the Tailscale client loses connectivity on
# the next netmap sync) and cleans up local state
# (node_owner_map + device_exit_node_prefs). The audit
# log captures the action.
#
# B162 (this file) pins the fixes:
#  1. PostMyDeviceDelete handler exists + scope-checks
#     the node to the current user
#  2. Route POST /my/devices/{id}/delete registered
#  3. devices.html renders the new Delete button
#     (next to Renew, with confirm dialog)
#  4. Post-delete flash (DeletedHost) rendered
#  5. Audit log on every delete (device_deleted)
#  6. New i18n keys in both RU and EN
#  7. node_owner_map + device_exit_node_prefs cleanup
#  8. B160.1-style 410 Gone handling for the
#     "node no longer exists in NodeStore" gRPC error
#  9. DeleteDeviceExitNodePref helper added to
#     internal/db/exit_node_prefs.go

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: PostMyDeviceDelete handler exists + scope-checks ==="
# The handler must be a method on *Service.
if grep -qE 'func \(s \*Service\) PostMyDeviceDelete' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete handler defined on *Service"
else
    bad "PostMyDeviceDelete handler MISSING"
fi
# Must verify the node is owned by the current
# user before deleting. We use the same dual
# check (live user_name + snapshot node_owner_map)
# as PostMyDeviceRenew.
if grep -qE 'snapIDs, _ := db\.ListNodeOwnerNodeIDsByUsername\(s\.DB, c\.Username\)' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete scope-checks via node_owner_map snapshot"
else
    bad "PostMyDeviceDelete: snapshot scope-check MISSING"
fi
if grep -qE 'n\.UserName == c\.Username' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete scope-checks via live n.UserName"
else
    bad "PostMyDeviceDelete: live scope-check MISSING"
fi
# Cross-user attempt must return 404 (defense
# against IDOR).
if grep -qE '"device not found", http\.StatusNotFound' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete returns 404 on cross-user / unknown id"
else
    bad "PostMyDeviceDelete: 404 on cross-user MISSING"
fi
# Must call hsClient.DeleteNode (the headscale
# primitive that the Renew handler parallels).
if grep -qE 'hsClient\.DeleteNode\(nodeID\)' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete calls hsClient.DeleteNode"
else
    bad "PostMyDeviceDelete: DeleteNode call MISSING"
fi

echo ""
echo "=== contract B: route registered in main.go ==="
if grep -qE 'mux\.Handle\("POST /my/devices/\{id\}/delete"' cmd/skygate/main.go; then
    ok "POST /my/devices/{id}/delete route registered"
else
    bad "POST /my/devices/{id}/delete route MISSING"
fi
# Must be behind authMW (per-user endpoint, not
# public).
if grep -qE 'POST /my/devices/\{id\}/delete", authMW' cmd/skygate/main.go; then
    ok "POST /my/devices/{id}/delete is behind authMW"
else
    bad "POST /my/devices/{id}/delete NOT behind authMW (security regression)"
fi

echo ""
echo "=== contract C: devices.html renders the Delete button + post-delete flash ==="
# Per-row Delete button (next to Renew, with
# confirm dialog). The form must use POST (not
# GET — destructive actions must never be GET).
if grep -qE 'action="/my/devices/\{\{\.ID\}\}/delete"' internal/handlers/templates/user/devices.html; then
    ok "devices.html renders a per-row Delete form"
else
    bad "devices.html: Delete form MISSING"
fi
if grep -qE 'method="post".*action="/my/devices/\{\{\.ID\}\}/delete"' internal/handlers/templates/user/devices.html; then
    ok "Delete form is POST (not GET — destructive actions must be POST)"
else
    bad "Delete form method is NOT POST (security regression — destructive GETs can be CSRF'd)"
fi
# Confirm dialog with hostname interpolation.
if grep -qE 'devices\.delete_confirm.*\.Hostname' internal/handlers/templates/user/devices.html; then
    ok "Delete button has confirm() with hostname interpolation"
else
    bad "Delete button: confirm() dialog MISSING (user might delete the wrong device)"
fi
# Post-delete flash.
if grep -qE 'DeletedHost' internal/handlers/templates/user/devices.html; then
    ok "devices.html renders the post-delete flash"
else
    bad "devices.html: post-delete flash MISSING"
fi
if grep -qE 'devices\.delete_ok' internal/handlers/templates/user/devices.html; then
    ok "devices.html references devices.delete_ok i18n key"
else
    bad "devices.html: devices.delete_ok i18n key MISSING"
fi

echo ""
echo "=== contract D: audit log on every delete ==="
# Every successful delete (and the 410 Gone branch)
# must write a device_deleted audit row so the
# admin /audit page can correlate "user X deleted
# device Y" with any later headscale activity.
if grep -qE 'device_deleted' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete writes 'device_deleted' audit row"
else
    bad "PostMyDeviceDelete: audit log MISSING"
fi

echo ""
echo "=== contract E: i18n keys (RU + EN) ==="
needed=(
  "devices.delete"
  "devices.delete_title"
  "devices.delete_confirm"
  "devices.delete_ok"
  "devices.delete_err_404"
  "devices.delete_err_deleted"
  "devices.delete_err_failed"
)
for k in "${needed[@]}"; do
    ru_count=$(grep -cE "\"$k\"" internal/i18n/catalog_my.go 2>/dev/null || true)
    ru_count=${ru_count:-0}
    if [ "$ru_count" -ge 2 ]; then
        ok "i18n key '$k' present in both RU and EN"
    else
        bad "i18n key '$k' MISSING in catalog_my.go (found $ru_count entries — need 2 for RU+EN)"
    fi
done

echo ""
echo "=== contract F: local cleanup (node_owner_map + device_exit_node_prefs) ==="
# After headscale.DeleteNode the local snapshot
# would re-show the device on the next page load
# (the snapshot branch in GetMyDevices reads
# node_owner_map). We must clean up the local row.
if grep -qE 'db\.DeleteNodeOwnerByNodeTag\(s\.DB, idStr, ""\)' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete cleans up node_owner_map"
else
    bad "PostMyDeviceDelete: node_owner_map cleanup MISSING (row would re-appear on /my/devices)"
fi
# Per-device exit-node prefs keyed on hostname.
# They're inert for a non-existent device but
# accumulate over time if not cleaned.
if grep -qE 'db\.DeleteDeviceExitNodePref\(s\.DB, c\.UserID, strings\.ToLower\(host\)\)' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete cleans up device_exit_node_prefs"
else
    bad "PostMyDeviceDelete: device_exit_node_prefs cleanup MISSING"
fi
# The Pref-cleanup helper must exist in
# internal/db (B162 added it).
if grep -qE 'func DeleteDeviceExitNodePref' internal/db/exit_node_prefs.go; then
    ok "db.DeleteDeviceExitNodePref helper exists"
else
    bad "db.DeleteDeviceExitNodePref helper MISSING"
fi

echo ""
echo "=== contract G: 410 Gone on 'no longer exists' gRPC error ==="
# B160.1 pattern: when the local snapshot still
# references a node that headscale has already
# purged, the gRPC DeleteNode call returns
# "no longer exists in NodeStore" or "node not
# found". We treat that as 410 Gone + still
# clean up the local snapshot + return the
# i18n devices.delete_err_deleted string.
if grep -qE '"no longer exists in NodeStore"' internal/feature/my/devices.go && \
   grep -qE '"node not found"' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete detects both 'no longer exists' + 'node not found' gRPC errors"
else
    bad "PostMyDeviceDelete: 410 Gone detection MISSING (the device-deleted-mid-click race would 500)"
fi
if grep -qE 'devices\.delete_err_deleted' internal/feature/my/devices.go; then
    ok "PostMyDeviceDelete returns the i18n devices.delete_err_deleted on 410"
else
    bad "PostMyDeviceDelete: i18n for the 410 case MISSING"
fi

echo ""
echo "=== contract H: deleted-hostname plumbed into GetMyDevices data ==="
# The handler redirects with ?deleted=<host>;
# the GET handler must read it and pass it to the
# template as DeletedHost.
if grep -qE '"DeletedHost":\s+r\.URL\.Query\(\)\.Get\("deleted"\)' internal/feature/my/devices.go; then
    ok "GetMyDevices plumbs DeletedHost into the template data"
else
    bad "GetMyDevices: DeletedHost plumbing MISSING (the post-delete flash would never render)"
fi

echo ""
echo "=== contract I: build + vet clean ==="
out=$(bash -c "PATH='$PATH' go build ./..." 2>&1) || {
    # The PATH trick doesn't survive to nested
    # bash invocations from this script context;
    # fall back to the system go on PATH.
    go build ./... 2>&1
} | (read line; if [ -n "$line" ]; then echo "$line"; bad "go build output: $line"; fi)
ok "go build ./... clean"
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet output: $out"
fi

echo ""
echo "=== summary ==="
echo "B162: per-row device delete from /my/devices"
echo "all contracts satisfied"
