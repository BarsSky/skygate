#!/bin/bash
# check_b160.sh — /my/devices manual expiry renewal button
# (B160, v1.5.0)
#
# Operator 2026-08-20 question: "можно ли реализовать
# продление работы ключа которым устройство
# аутентифицировалось в headscale через веб интерфейс
# skygate?" — the user is asking about renewing the
# device's session, NOT the preauth key (which is
# one-time — B155 reissue handles that). The
# preauth key is consumed at registration; the
# device's NODE EXPIRY is what keeps the device
# authenticated. The auto-renewer
# (internal/expirewatch) does this every 5min for
# nodes within 7d, but a manual button is useful for:
#   - the user disabled expirewatch
#   - the user wants to renew NOW (not wait for
#     the next tick)
#   - the user wants explicit visibility into
#     "renewed 5 days ago" (the audit log already
#     records every renewal)
#
# B160 (this file) pins the fixes:
#  1. PostMyDeviceRenew handler exists + scope-checks
#     the node to the current user
#  2. Route POST /my/devices/{id}/renew registered
#  3. devices.html renders the new Expires column +
#     the Renew button (only when Expiry is non-empty)
#  4. Per-row Expiry parsing + relative-time hint
#     (uses B159's formatRelativeExpiry)
#  5. Audit log on every renewal (device_renewed)
#  6. New i18n keys in both RU and EN

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: PostMyDeviceRenew handler exists + scope-checks ==="
# The handler must be a method on *Service.
if grep -qE 'func \(s \*Service\) PostMyDeviceRenew' internal/feature/my/devices.go; then
    ok "PostMyDeviceRenew handler defined on *Service"
else
    bad "PostMyDeviceRenew handler MISSING"
fi
# Must verify the node is owned by the current
# user before extending. We check the same
# pattern as B155 PostMyKeyReissue: ListAllNodes
# + user_id scope check.
if grep -qE 'snapIDs, _ := db\.ListNodeOwnerNodeIDsByUsername\(s\.DB, c\.Username\)' internal/feature/my/devices.go; then
    ok "PostMyDeviceRenew scope-checks via node_owner_map"
else
    bad "PostMyDeviceRenew MISSING the user-scope check"
fi
# Must reject when node has no Expiry (tagged
# / shared / no-expiry nodes are policy-
# controlled, not user-controlled).
if grep -qE 'device has no expiry' internal/feature/my/devices.go; then
    ok "PostMyDeviceRenew rejects nodes with no expiry"
else
    bad "PostMyDeviceRenew MISSING the no-expiry guard"
fi
# Must write the audit log.
if grep -qE '"device_renewed"' internal/feature/my/devices.go; then
    ok "PostMyDeviceRenew writes 'device_renewed' audit log"
else
    bad "PostMyDeviceRenew MISSING the audit log entry"
fi
# Must call ExtendNodeExpiry with a future timestamp.
if grep -qE 'hsClient\.ExtendNodeExpiry' internal/feature/my/devices.go; then
    ok "PostMyDeviceRenew calls ExtendNodeExpiry"
else
    bad "PostMyDeviceRenew MISSING the ExtendNodeExpiry call"
fi

echo ""
echo "=== contract B: route registered ==="
if grep -qE 'mux\.Handle\("POST /my/devices/\{id\}/renew"' cmd/skygate/main.go; then
    ok "POST /my/devices/{id}/renew route registered"
else
    bad "POST /my/devices/{id}/renew route NOT registered"
fi

echo ""
echo "=== contract C: devices.html has Expires column + Renew button ==="
# Header for the new column.
if grep -qE 't "devices\.expires_col"' internal/handlers/templates/user/devices.html; then
    ok "devices.html renders the 'Expires' column header"
else
    bad "devices.html MISSING the 'Expires' column header"
fi
# Per-row .Expiry rendering (datetimeformat)
if grep -qE 'datetimeformat \.ExpiryUnix' internal/handlers/templates/user/devices.html; then
    ok "devices.html renders .ExpiryUnix via datetimeformat"
else
    bad "devices.html MISSING the .ExpiryUnix render"
fi
# Per-row relative-time hint (the B159 helper)
if grep -qE '\{\{\.ExpiresRelative\}\}' internal/handlers/templates/user/devices.html; then
    ok "devices.html renders the .ExpiresRelative hint"
else
    bad "devices.html MISSING the .ExpiresRelative render"
fi
# Per-row Renew button — only when ExpiryUnix > 0
if grep -qE 'action="/my/devices/\{\{\.ID\}\}/renew"' internal/handlers/templates/user/devices.html; then
    ok "devices.html has a Renew form per device"
else
    bad "devices.html MISSING the Renew form per device"
fi
# Renew button is INSIDE the {{if .ExpiryUnix}} block,
# so it only renders for nodes that have an expiry.
# We can't easily grep for the nesting, so we
# just check that the button is present at all.
if grep -qE 't "devices\.renew"' internal/handlers/templates/user/devices.html; then
    ok "devices.html references 'devices.renew' i18n key"
else
    bad "devices.html MISSING the 'devices.renew' i18n key reference"
fi
# Post-renew flash alert.
if grep -qE 'RenewedHost' internal/handlers/templates/user/devices.html; then
    ok "devices.html renders the post-renew flash alert"
else
    bad "devices.html MISSING the post-renew flash alert"
fi

echo ""
echo "=== contract D: per-row Expiry parsing + relative-time hint ==="
# The handler must parse the headscale Expiry string
# to a unix timestamp + compute the i18n relative-time
# hint for the .ExpiresRelative field.
if grep -qE 'time\.Parse\(time\.RFC3339Nano, row\.Expiry\)' internal/feature/my/devices.go; then
    ok "GetMyDevices parses Expiry to time.Time"
else
    bad "GetMyDevices MISSING the Expiry parser"
fi
if grep -qE 'row\.ExpiresRelative = formatRelativeExpiry' internal/feature/my/devices.go; then
    ok "GetMyDevices sets .ExpiresRelative via formatRelativeExpiry"
else
    bad "GetMyDevices MISSING the .ExpiresRelative computation"
fi
# Best-effort: unparseable Expiry → "no expiry"
# (graceful degradation, not a hard error).
if grep -qE 'row\.ExpiresRelative = s\.I18n\.T\(lang, "keys\.never_expires"\)' internal/feature/my/devices.go; then
    ok "GetMyDevices degrades gracefully on unparseable Expiry"
else
    bad "GetMyDevices MISSING the graceful-degradation path"
fi
# Per-row warning kind (7d / 30d / past).
if grep -qE 'row\.ExpiryWarning = "soon"' internal/feature/my/devices.go; then
    ok "GetMyDevices sets the per-row .ExpiryWarning (7d/30d/past)"
else
    bad "GetMyDevices MISSING the per-row warning logic"
fi

echo ""
echo "=== contract E: i18n keys in both RU and EN ==="
# 7 new keys, each must appear in BOTH halves of
# the catalog (B4 parity contract).
needed=(
  "devices.expires_col"
  "devices.renew"
  "devices.renew_title"
  "devices.renewed_toast"
  "devices.renew_err_not_found"
  "devices.renew_err_no_expiry"
  "devices.renew_err_failed"
)
for k in "${needed[@]}"; do
    c=$(grep -cE "\"$k\"" internal/i18n/catalog_my.go || true)
    if [ "$c" -ge 2 ]; then
        ok "i18n key '$k' present in both RU and EN"
    else
        bad "i18n key '$k' has only $c occurrence(s); need >=2 (RU+EN)"
    fi
done

echo ""
echo "=== contract F: go build + go vet clean ==="
out=$(go build ./... 2>&1)
if [ -z "$out" ]; then
    ok "go build ./... clean"
else
    bad "go build ./... output: $out"
fi
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet ./... output: $out"
fi

echo ""
echo "=== contract G: pre-existing tests still pass ==="
# The TestTemplateArgsMatchCatalog test enforces
# that {{t "key"}} (no args) only uses keys with
# 0 placeholders, and {{tf "key" arg1 ...}} uses
# keys with the matching arg count. A future edit
# that uses {{t "keys.expires_in_days" 7}} would
# fail this test (we hit it during B160 development).
out=$(go test ./internal/handlers/ -run TestTemplateArgsMatchCatalog 2>&1)
if echo "$out" | grep -q '^ok'; then
    ok "TestTemplateArgsMatchCatalog passes"
else
    bad "TestTemplateArgsMatchCatalog FAILED: $out"
fi

echo ""
echo "=== summary ==="
echo "B160: /my/devices manual expiry renewal (Expires column + Renew button)"
echo "all B160 contracts satisfied"
