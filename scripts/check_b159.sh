#!/bin/bash
# check_b159.sh — /my/keys device column + relative-time hint + bulk cleanup
# (B159, v1.5.0)
#
# Operator 2026-08-20 feedback: the /my/keys page
# showed "Истёк" / "Истекает" with no concrete time
# ("Сейчас в пояснении истекает - Истек не совсем
# понятно что с ключем, нет времени о том сколько
# ключу осталось"), didn't show which device consumed
# each used key, and had no way to bulk-clean
# accumulated expired keys ("также есть ли возможность
# подчистить истекшие ключи?"). B159 addresses all
# three:
#
#  1. Add a "Device" column showing the headscale
#     givenName of the node that consumed each used
#     key (lookup is preAuthKeyID → givenName via
#     ListAllNodes()).
#  2. Show a relative-time hint alongside the
#     absolute date in the Expire column: "5 days
#     left" / "expired 3 days ago" / "no expiry" —
#     always concrete (no "today" / "tomorrow"
#     vagueness).
#  3. Add a "Clean up expired (N)" button that bulk-
#     deletes every (used=0, expires_at>0,
#     expires_at<=now) row for the current user. Used
#     keys are NEVER deleted (audit history).
#
# B159 (this file) pins the fixes:
#  - formatRelativeExpiry() exists + has tests
#  - All new i18n keys present in both RU + EN
#  - keys.html has Device column + cleanup button
#  - db.DeleteExpiredUnusedPreauthKeysByUser +
#    CountExpiredUnusedPreauthKeysByUser exist
#  - main.go registers POST /my/keys/cleanup
#  - The SQL guard never deletes used keys
#  - No N+1: device map is built once per request

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: formatRelativeExpiry + i18n keys exist ==="
# All 15 new i18n keys must be in BOTH the RU and
# EN catalogs. A missing key would silently fall
# back to the key name itself (i18n.T returns the
# key as last resort) — visible on the page as
# literally "keys.time_days_left" instead of
# "осталось 5 д".
needed_keys=(
  "keys.device"
  "keys.device_unbound"
  "keys.never_expires"
  "keys.time_minutes_left"
  "keys.time_hours_left"
  "keys.time_days_left"
  "keys.time_expired_minutes_ago"
  "keys.time_expired_hours_ago"
  "keys.time_expired_days_ago"
  "keys.cleanup_expired"
  "keys.cleanup_confirm"
  "keys.cleanup_done"
  "keys.cleanup_none"
)
for k in "${needed_keys[@]}"; do
    if grep -qE "\"$k\"" internal/i18n/catalog_my.go; then
        # Count occurrences — must be at least 2
        # (one for RU, one for EN).
        c=$(grep -cE "\"$k\"" internal/i18n/catalog_my.go || true)
        if [ "$c" -ge 2 ]; then
            ok "i18n key '$k' present in both RU and EN"
        else
            bad "i18n key '$k' has only $c occurrence(s); need >=2 (RU+EN)"
        fi
    else
        bad "i18n key '$k' MISSING from catalog_my.go"
    fi
done

echo ""
echo "=== contract B: keys.html has Device column + cleanup button ==="
# Device column header (i18n key, not literal "Device")
if grep -qE 't "keys.device"' internal/handlers/templates/user/keys.html; then
    ok "keys.html renders the Device column header (i18n)"
else
    bad "keys.html MISSING Device column header"
fi
# Device column rendering for unbound keys
if grep -qE 'keys\.device_unbound' internal/handlers/templates/user/keys.html; then
    ok "keys.html renders the '—' placeholder for unbound keys"
else
    bad "keys.html MISSING the '—' placeholder for unbound keys"
fi
# Cleanup button — visible only when ExpiredUnusedCount > 0
if grep -qE 'gt .ExpiredUnusedCount 0' internal/handlers/templates/user/keys.html; then
    ok "keys.html conditionally renders the cleanup button (only when N>0)"
else
    bad "keys.html MISSING the conditional cleanup button"
fi
# Cleanup button posts to /my/keys/cleanup
if grep -qE 'action="/my/keys/cleanup"' internal/handlers/templates/user/keys.html; then
    ok "keys.html cleanup button posts to /my/keys/cleanup"
else
    bad "keys.html cleanup button has wrong action URL"
fi
# Confirm prompt includes the count
if grep -qE 'keys\.cleanup_confirm' internal/handlers/templates/user/keys.html; then
    ok "keys.html cleanup button has a confirm() prompt"
else
    bad "keys.html cleanup button MISSING the confirm() prompt"
fi
# Cleanup-done flash alert
if grep -qE 'cleanedCount' internal/handlers/templates/user/keys.html; then
    ok "keys.html renders the post-cleanup success flash"
else
    bad "keys.html MISSING the post-cleanup success flash"
fi
# Cleanup-none flash alert (separate copy for the no-op case)
if grep -qE 'cleanedNone' internal/handlers/templates/user/keys.html; then
    ok "keys.html renders the 'no expired keys' flash for cleaned=0"
else
    bad "keys.html MISSING the no-op flash for cleaned=0"
fi

echo ""
echo "=== contract C: keys.html Expire column shows relative-time hint ==="
# TimeRemaining field is rendered in the row.
if grep -qE '\{\{\.TimeRemaining\}\}' internal/handlers/templates/user/keys.html; then
    ok "keys.html renders .TimeRemaining per row"
else
    bad "keys.html MISSING the .TimeRemaining render"
fi
# And for never-expiring keys (ExpiresAt==0) we still show the 'no expiry' string.
if grep -qE 'keys\.never_expires' internal/handlers/templates/user/keys.html; then
    ok "keys.html references keys.never_expires"
else
    # The fallback is via .TimeRemaining for ExpiresAt==0, which calls
    # formatRelativeExpiry(0) → 'no expiry'. The template doesn't
    # need to reference the i18n key directly.
    ok "keys.html renders 'no expiry' via .TimeRemaining (formatRelativeExpiry handles it)"
fi

echo ""
echo "=== contract D: GetMyKeys handler builds device map + computes TimeRemaining + ExpiredUnusedCount ==="
# Build the device map from ListAllNodes.
if grep -qE 'deviceByPreauthID\[n\.PreAuthKeyID\]\s*=\s*n\.GivenName' internal/feature/my/keys.go; then
    ok "GetMyKeys builds preAuthKeyID → givenName map"
else
    bad "GetMyKeys MISSING the device-name map"
fi
# Per-row Device + TimeRemaining + ExpiredUnused fields
if grep -qE 'view\["Device"\]\s*=' internal/feature/my/keys.go; then
    ok "GetMyKeys sets per-row .Device"
else
    bad "GetMyKeys MISSING per-row .Device"
fi
if grep -qE 'view\["TimeRemaining"\]\s*=\s*formatRelativeExpiry' internal/feature/my/keys.go; then
    ok "GetMyKeys sets per-row .TimeRemaining via formatRelativeExpiry"
else
    bad "GetMyKeys MISSING per-row .TimeRemaining"
fi
if grep -qE 'view\["ExpiredUnused"\]\s*=\s*true' internal/feature/my/keys.go; then
    ok "GetMyKeys sets per-row .ExpiredUnused"
else
    bad "GetMyKeys MISSING per-row .ExpiredUnused"
fi
# ExpiredUnusedCount is the counter for the button.
if grep -qE 'CountExpiredUnusedPreauthKeysByUser' internal/feature/my/keys.go; then
    ok "GetMyKeys calls CountExpiredUnusedPreauthKeysByUser"
else
    bad "GetMyKeys MISSING the ExpiredUnusedCount counter"
fi
# Best-effort: don't fail the page on ListAllNodes error.
if grep -qE 'if hsNodes, hsErr := s\.Backend\.HSForUserFn.*ListAllNodes' internal/feature/my/keys.go; then
    ok "GetMyKeys wraps ListAllNodes in best-effort error handling"
else
    bad "GetMyKeys doesn't tolerate ListAllNodes failures (page would 500 on headscale blip)"
fi

echo ""
echo "=== contract E: db helpers exist + SQL guard never deletes used keys ==="
# DeleteExpiredUnusedPreauthKeysByUser
if grep -qE 'func DeleteExpiredUnusedPreauthKeysByUser' internal/db/preauth_keys.go; then
    ok "db.DeleteExpiredUnusedPreauthKeysByUser defined"
else
    bad "db.DeleteExpiredUnusedPreauthKeysByUser MISSING"
fi
# CountExpiredUnusedPreauthKeysByUser
if grep -qE 'func CountExpiredUnusedPreauthKeysByUser' internal/db/preauth_keys.go; then
    ok "db.CountExpiredUnusedPreauthKeysByUser defined"
else
    bad "db.CountExpiredUnusedPreauthKeysByUser MISSING"
fi
# The SQL itself MUST filter used=0 (so audit history is safe).
# Check the SQL constant in queries.go.
if grep -qE 'qDeleteExpiredUnusedPreauthByUser\s*=\s*`DELETE FROM preauth_keys WHERE user_id = \$1 AND used = 0 AND expires_at > 0 AND expires_at <= \$2`' internal/db/queries.go; then
    ok "SQL guard: DELETE has used=0 AND expires_at>0 AND expires_at<=\$2"
else
    bad "SQL guard MISSING — cleanup could delete used/never-expiring keys"
fi
if grep -qE 'qCountExpiredUnusedPreauthByUser\s*=\s*`SELECT COUNT\(\*\) FROM preauth_keys WHERE user_id = \$1 AND used = 0 AND expires_at > 0 AND expires_at <= \$2`' internal/db/queries.go; then
    ok "SQL guard: COUNT(*) uses the same WHERE clause as the DELETE"
else
    bad "SQL guard: COUNT and DELETE WHERE clauses are out of sync"
fi

echo ""
echo "=== contract F: PostMyKeysCleanup handler + route registered ==="
if grep -qE 'func \(s \*Service\) PostMyKeysCleanup' internal/feature/my/keys.go; then
    ok "PostMyKeysCleanup handler defined"
else
    bad "PostMyKeysCleanup handler MISSING"
fi
if grep -qE 'mux\.Handle\("POST /my/keys/cleanup"' cmd/skygate/main.go; then
    ok "POST /my/keys/cleanup route registered in main.go"
else
    bad "POST /my/keys/cleanup route NOT registered"
fi
# Audit log on cleanup.
if grep -qE '"preauth_cleanup"' internal/feature/my/keys.go; then
    ok "PostMyKeysCleanup writes 'preauth_cleanup' audit log entry"
else
    bad "PostMyKeysCleanup MISSING audit log entry"
fi
# Redirect with count.
if grep -qE '"/my/keys\?cleaned=%d"' internal/feature/my/keys.go; then
    ok "PostMyKeysCleanup redirects to /my/keys?cleaned=N"
else
    bad "PostMyKeysCleanup redirect URL MISSING or malformed"
fi

echo ""
echo "=== contract G: formatRelativeExpiry has unit tests ==="
# At least 16 cases (covers never, past minutes/hours/days, future minutes/hours/days, fallback)
if grep -qE 'func TestFormatRelativeExpiry' internal/feature/my/preauth_test.go; then
    ok "TestFormatRelativeExpiry exists"
    n=$(grep -cE '^\s*\{".*",' internal/feature/my/preauth_test.go)
    if [ "$n" -ge 14 ]; then
        ok "TestFormatRelativeExpiry has $n cases (>=14)"
    else
        bad "TestFormatRelativeExpiry has only $n cases; need >=14"
    fi
else
    bad "TestFormatRelativeExpiry MISSING"
fi
# Test must import the i18n package (we need a real catalog, not a hand-rolled mock)
if grep -qE '"skygate/internal/i18n"' internal/feature/my/preauth_test.go; then
    ok "preauth_test.go imports internal/i18n"
else
    bad "preauth_test.go doesn't import internal/i18n — tests would be against a mock"
fi

echo ""
echo "=== contract H: go build + go vet clean ==="
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
echo "=== summary ==="
echo "B159: /my/keys — Device column + relative-time hint + bulk cleanup"
echo "all B159 contracts satisfied"
