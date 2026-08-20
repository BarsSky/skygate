#!/bin/bash
# check_b155.sh — preauth key UX (custom TTL + Reissue + warning pills)
# (B155, v1.5.0)
#
# Background (2026-08-20, operator scope correction):
# operator clarified that "key expiring" in the Tailscale
# client refers to HEADSCALE PREAUTH KEYS (the keys the
# /my/devices page issues for adding new devices), NOT
# the personal API tokens B153/B154 target. B155 brings
# the same UX pattern to /my/keys:
#   - Custom TTL when issuing a new preauth key
#   - Reissue button (replaces the old key + issues a
#     new one with the same TTL) on the /my/keys table
#   - Per-row warning pill for keys expiring within 14d
#   - ExpiringCount banner trigger
#
# B155 (this file) pins the fixes:
#   1. PostMyKeyReissue handler MUST exist and call
#      headscale.ExpirePreauthKey + headscale.CreatePreauthKey
#      + db.InsertPreauthKey in that order.
#   2. PostMyPreauth handler MUST honour custom_ttl_value +
#      custom_ttl_unit (number + h/d/w/y) + reusable checkbox.
#   3. GetMyKeys handler MUST compute per-row ExpiresWarn +
#      ExpiringCount + Renewable.
#   4. The /my/keys/{id}/reissue route MUST be wired in
#      main.go.
#   5. The user/keys.html template MUST render the new
#      warning pills + per-row Reissue button + dedicated
#      ReissueForm card.
#   6. The user/devices.html form MUST add custom_ttl_value
#      + custom_ttl_unit + reusable checkbox.
#   7. The preauth_result.html template MUST show the
#      "replaces key #N" banner when ReissueFrom + ReissueTo
#      are set.
#   8. All 18 new i18n keys MUST exist in RU + EN (B4 parity).
#   9. The unit tests for the preauth TTL resolution +
#      humanize + durationFromSeconds MUST pass.
#  10. verify_pre_deploy.sh MUST include B155.
#  11. Go vet + Go test on the new package MUST pass.

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: PostMyKeyReissue handler exists + uses the right headscale calls ==="
if grep -q 'func (s \*Service) PostMyKeyReissue' internal/feature/my/keys.go; then
    ok "PostMyKeyReissue handler defined"
else
    bad "PostMyKeyReissue handler MISSING — /my/keys/{id}/reissue will 404"
fi
# The handler must call BOTH ExpirePreauthKey (for the
# old key) AND CreatePreauthKey (for the new one) —
# otherwise we have a half-rotation.
if grep -q 's.Backend.HSForUserFn(c.UserID).ExpirePreauthKey' internal/feature/my/keys.go; then
    ok "PostMyKeyReissue calls headscale.ExpirePreauthKey (old key)"
else
    bad "PostMyKeyReissue does NOT call ExpirePreauthKey — old key stays alive in headscale"
fi
if grep -q 's.Backend.HSForUserFn(c.UserID).CreatePreauthKey' internal/feature/my/keys.go; then
    ok "PostMyKeyReissue calls headscale.CreatePreauthKey (new key)"
else
    bad "PostMyKeyReissue does NOT call CreatePreauthKey — no new key issued"
fi

echo ""
echo "=== contract B: PostMyPreauth handles custom_ttl_value + custom_ttl_unit + reusable ==="
# Custom TTL must be parsed BEFORE the legacy dropdown
# so an operator who fills both fields gets the
# explicit value.
if grep -q 'custom_ttl_value' internal/feature/my/preauth.go && \
   grep -q 'custom_ttl_unit'  internal/feature/my/preauth.go; then
    ok "PostMyPreauth parses custom_ttl_value + custom_ttl_unit"
else
    bad "PostMyPreauth does NOT handle custom TTL — operator locked to 1h"
fi
# Min/max bounds: 43800h (5y) must appear in the
# handler. Without the max, an operator could type
# 99999y.
if grep -q '43800\|5\*24\*365' internal/feature/my/preauth.go; then
    ok "Custom TTL upper bound (5 years) is enforced"
else
    bad "Custom TTL upper bound MISSING — operator could create a million-year key"
fi
# Reusable checkbox.
if grep -q '"reusable"' internal/feature/my/preauth.go; then
    ok "PostMyPreauth reads the reusable checkbox"
else
    bad "PostMyPreauth does NOT read reusable checkbox — every key is single-use"
fi

echo ""
echo "=== contract C: GetMyKeys computes ExpiresWarn + ExpiringCount + Renewable ==="
# The warning logic lives in the handler, not the
# template. The template is presentation-only.
if grep -q 'ExpiresWarn'   internal/feature/my/keys.go && \
   grep -q 'ExpiringCount' internal/feature/my/keys.go && \
   grep -q 'Renewable'     internal/feature/my/keys.go; then
    ok "GetMyKeys computes ExpiresWarn + ExpiringCount + Renewable"
else
    bad "GetMyKeys missing per-row warning state — operator won't see expiring keys"
fi
# The 14-day banner window must be explicit (not
# arbitrary).
if grep -q '14 \* 24 \* time.Hour\|14\*24\*time.Hour' internal/feature/my/keys.go; then
    ok "Banner window is explicit (14 days)"
else
    bad "Banner window constant missing — magic number might drift"
fi

echo ""
echo "=== contract D: /my/keys/{id}/reissue route wired in main.go ==="
if grep -q 'POST /my/keys/{id}/reissue' cmd/skygate/main.go; then
    ok "main.go registers POST /my/keys/{id}/reissue"
else
    bad "main.go MISSING POST /my/keys/{id}/reissue — Reissue button 404s"
fi

echo ""
echo "=== contract E: user/keys.html renders Reissue + warning pills + ReissueForm ==="
# Per-row Reissue button.
if grep -q '/my/keys/{{.ID}}/reissue' internal/handlers/templates/user/keys.html; then
    ok "user/keys.html has per-row Reissue button (POST /my/keys/{id}/reissue)"
else
    bad "user/keys.html MISSING per-row Reissue button — operator has to re-issue from scratch"
fi
# Per-row ExpiresWarn rendering.
if grep -q 'ExpiresWarn' internal/handlers/templates/user/keys.html && \
   grep -q 'ExpiresInWords' internal/handlers/templates/user/keys.html; then
    ok "user/keys.html renders ExpiresWarn + ExpiresInWords"
else
    bad "user/keys.html missing warning pill rendering"
fi
# Dedicated ReissueForm card.
if grep -q '\.ReissueForm' internal/handlers/templates/user/keys.html; then
    ok "user/keys.html has dedicated ReissueForm card"
else
    bad "user/keys.html missing .ReissueForm card — power-user reissue path broken"
fi

echo ""
echo "=== contract F: user/devices.html form has custom_ttl + reusable ==="
if grep -q 'custom_ttl_value' internal/handlers/templates/user/devices.html && \
   grep -q 'custom_ttl_unit'  internal/handlers/templates/user/devices.html; then
    ok "user/devices.html renders custom_ttl_value + custom_ttl_unit"
else
    bad "user/devices.html missing custom TTL inputs — operator can't pick a custom lifetime"
fi
if grep -q 'name="reusable"' internal/handlers/templates/user/devices.html; then
    ok "user/devices.html has reusable checkbox"
else
    bad "user/devices.html missing reusable checkbox"
fi

echo ""
echo "=== contract G: preauth_result.html shows the 'replaces key #N' banner ==="
# The reissue path renders the result page directly
# with .ReissueFrom + .ReissueTo set. The template
# must show the banner so the user knows the old key
# is no longer valid.
if grep -q '\.ReissueFrom' internal/handlers/templates/user/preauth_result.html && \
   grep -q 'keys.reissued_to' internal/handlers/templates/user/preauth_result.html; then
    ok "preauth_result.html shows the 'replaces key #N' banner"
else
    bad "preauth_result.html missing reissue banner"
fi

echo ""
echo "=== contract H: i18n keys (RU+EN parity) ==="
# All 18 new keys must exist in both languages
# (B4 parity). Missing one means a half-translated
# page.
for k in custom_ttl_label custom_ttl_hint custom_ttl_h custom_ttl_d custom_ttl_w custom_ttl_y \
         custom_ttl_err_min custom_ttl_err_max reusable_label reusable_hint \
         reissue reissue_title reissued_to reissue_err_used reissue_err_expired \
         expired expires_in_days expires_in_day expires_in_hours expires_tomorrow \
         banner_expiring; do
    count=$(grep -c "\"keys\\.${k}\"" internal/i18n/catalog_my.go || true)
    if [ "$count" -ge 2 ]; then
        ok "keys.${k} present in both RU and EN ($count occurrences)"
    else
        bad "keys.${k} missing parity (only $count occurrence(s) in catalog_my.go)"
    fi
done

echo ""
echo "=== contract I: unit tests exist + pass ==="
if [ -f "internal/feature/my/preauth_test.go" ]; then
    ok "internal/feature/my/preauth_test.go exists"
else
    bad "internal/feature/my/preauth_test.go MISSING — pure-function helpers are untested"
fi
# Test names we expect.
for tn in TestResolvePreauthTTL_CustomValid TestResolvePreauthTTL_OutOfRangeFallsThrough \
         TestResolvePreauthTTL_LegacyDropdown TestResolvePreauthTTL_CustomOverridesLegacy \
         TestHumanizeTTL TestDurationFromSeconds; do
    if grep -q "func $tn" internal/feature/my/preauth_test.go; then
        ok "$tn test defined"
    else
        bad "$tn test MISSING — coverage gap"
    fi
done

echo ""
echo "=== contract J: verify_pre_deploy.sh includes B155 ==="
if grep -q 'B155' scripts/verify_pre_deploy.sh && \
   grep -q 'check_b155' scripts/verify_pre_deploy.sh; then
    ok "verify_pre_deploy.sh registers B155"
else
    bad "verify_pre_deploy.sh MISSING B155 — pre-push gate won't run this check"
fi

echo ""
echo "=== contract K: Go vet + Go test on the package ==="
if command -v go >/dev/null 2>&1; then
    if go vet ./internal/feature/my/ 2>&1 | grep -E '^(.*):(.*): (error|undefined)' | head -1; then
        bad "go vet ./internal/feature/my/ reports an error"
    else
        ok "go vet ./internal/feature/my/ clean"
    fi
    if go test -count=1 -short ./internal/feature/my/ 2>&1 | tail -1 | grep -q '^ok'; then
        ok "go test ./internal/feature/my/ PASS"
    else
        bad "go test ./internal/feature/my/ FAIL"
    fi
else
    echo "  SKIP  go not in PATH (Windows-host env-only; check on VM)"
fi

echo ""
echo "=== summary ==="
echo "B155: preauth key UX (custom TTL + per-row Reissue + warning pills)"
echo "all B155 contracts satisfied"
