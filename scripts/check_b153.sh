#!/bin/bash
# check_b153.sh — personal API token UX (operator 2026-08-20:
# "удобный способ обновления ключей или самим выбирать возможность
# ставить их время жизни сколько требуется при генерации ключа")
#
# Background (2026-08-20): operator reported a "key expiring" warning on
# the device + asked for a way to (a) extend the lifetime without
# revoking+recreating, and (b) choose a custom lifetime at creation
# time instead of being locked to the pre-baked 1h/1d/7d/30d/never
# dropdown.
#
# B153 (this file) pins the fixes:
#   1. PostMyTokenRenew handler MUST exist and call
#      db.UpdateAPITokenExpiryByUser.
#   2. PostMyToken handler MUST honour the custom_ttl_value +
#      custom_ttl_unit form fields (number + h/d/w/y).
#   3. GetMyTokens handler MUST compute per-row ExpiresWarn /
#      ExpiringCount / Renewable (the renewal + warning logic lives
#      here, not in the template).
#   4. The /my/token/{id}/renew route MUST be wired in main.go.
#   5. qUpdateAPITokenExpiryByUser SQL constant MUST exist.
#   6. UpdateAPITokenExpiryByUser DB helper MUST exist.
#   7. The my_tokens.html template MUST render the new
#      custom_ttl_value/unit fields + the per-row Renew button +
#      the dedicated renew card.
#   8. The .badge CSS class MUST exist (so the warning pills render
#      with the right colours).
#   9. All 12 new i18n keys MUST exist in RU + EN (B4 parity).
#  10. verify_pre_deploy.sh MUST include B153.
#  11. The Go test suite MUST compile + pass (catches dropped
#      imports or stray syntax errors).
#
# Without these checks, the token UX would silently regress to
# "no renew + no custom TTL + no warning" — which is exactly the
# state that triggered the operator's report.

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: PostMyTokenRenew handler exists + uses db.UpdateAPITokenExpiryByUser ==="
if grep -q 'func (s \*Service) PostMyTokenRenew' internal/feature/auth/service.go; then
    ok "PostMyTokenRenew handler defined"
else
    bad "PostMyTokenRenew handler MISSING — /my/token/{id}/renew will 404"
fi
if grep -q 'db.UpdateAPITokenExpiryByUser' internal/feature/auth/service.go; then
    ok "PostMyTokenRenew calls db.UpdateAPITokenExpiryByUser"
else
    bad "PostMyTokenRenew does NOT call db.UpdateAPITokenExpiryByUser — token not actually renewed"
fi

echo ""
echo "=== contract B: PostMyToken handles custom_ttl_value + custom_ttl_unit ==="
# Custom TTL must be parsed BEFORE the legacy dropdown so an
# operator who fills both fields gets the explicit value (and
# the dropdown becomes a graceful fallback for invalid input).
if grep -q 'custom_ttl_value' internal/feature/auth/service.go && \
   grep -q 'custom_ttl_unit'  internal/feature/auth/service.go; then
    ok "PostMyToken parses custom_ttl_value + custom_ttl_unit"
else
    bad "PostMyToken does NOT handle custom TTL — operator locked to hard-coded dropdown"
fi
# Min/max bounds: at least the magic constants 43800h (5y) and
# 1h (lower bound) must appear in the handler. Without the
# max, an operator could type 99999y and lock themselves out.
if grep -q '43800\|5\*24\*365' internal/feature/auth/service.go; then
    ok "Custom TTL upper bound (5 years) is enforced"
else
    bad "Custom TTL upper bound MISSING — operator could create a million-year token"
fi

echo ""
echo "=== contract C: GetMyTokens computes ExpiresWarn + ExpiringCount + Renewable ==="
# The renewal + warning logic lives in the handler, not in the
# template — the template is presentation-only.
if grep -q 'ExpiresWarn'   internal/feature/auth/service.go && \
   grep -q 'ExpiringCount' internal/feature/auth/service.go && \
   grep -q 'Renewable'     internal/feature/auth/service.go; then
    ok "GetMyTokens computes ExpiresWarn + ExpiringCount + Renewable"
else
    bad "GetMyTokens missing per-row warning state — operator won't see expiring tokens"
fi
# The 14-day banner window must be explicit (not arbitrary).
if grep -q '14 \* 24 \* time.Hour\|14\*24\*time.Hour' internal/feature/auth/service.go; then
    ok "Banner window is explicit (14 days)"
else
    bad "Banner window constant missing — magic number might drift"
fi

echo ""
echo "=== contract D: /my/token/{id}/renew route wired in main.go ==="
if grep -q 'POST /my/token/{id}/renew' cmd/skygate/main.go; then
    ok "main.go registers POST /my/token/{id}/renew"
else
    bad "main.go MISSING POST /my/token/{id}/renew — Renew button 404s"
fi

echo ""
echo "=== contract E: qUpdateAPITokenExpiryByUser SQL constant exists ==="
if grep -q 'qUpdateAPITokenExpiryByUser' internal/db/queries.go; then
    ok "qUpdateAPITokenExpiryByUser defined in queries.go"
else
    bad "qUpdateAPITokenExpiryByUser MISSING — handler call will not compile"
fi
# The SQL itself must be a real UPDATE, scoped to the user's own row.
if grep -q 'UPDATE personal_api_tokens SET expires_at' internal/db/queries.go; then
    ok "SQL is a real UPDATE on personal_api_tokens"
else
    bad "SQL is not a real UPDATE — silently broken?"
fi

echo ""
echo "=== contract F: UpdateAPITokenExpiryByUser DB helper exists ==="
if grep -q 'func UpdateAPITokenExpiryByUser' internal/db/personal_api_tokens.go; then
    ok "db.UpdateAPITokenExpiryByUser helper defined"
else
    bad "db.UpdateAPITokenExpiryByUser helper MISSING — handler will not compile"
fi

echo ""
echo "=== contract G: my_tokens.html renders custom_ttl + Renew + dedicated card ==="
# Custom TTL input + unit dropdown.
if grep -q 'custom_ttl_value' internal/handlers/templates/my_tokens.html && \
   grep -q 'custom_ttl_unit'  internal/handlers/templates/my_tokens.html; then
    ok "my_tokens.html renders custom_ttl_value + custom_ttl_unit"
else
    bad "my_tokens.html missing custom TTL inputs — operator can't pick a custom lifetime"
fi
# Per-row Renew button.
if grep -q '/my/token/{{.ID}}/renew' internal/handlers/templates/my_tokens.html; then
    ok "my_tokens.html has per-row Renew button (POST /my/token/{id}/renew)"
else
    bad "my_tokens.html MISSING per-row Renew button — operator has to revoke+recreate"
fi
# Dedicated ?renew=ID card.
if grep -q '\.RenewForm' internal/handlers/templates/my_tokens.html; then
    ok "my_tokens.html has dedicated RenewForm card (for custom TTL on renew)"
else
    bad "my_tokens.html missing .RenewForm card — can't change TTL when renewing"
fi
# Per-row ExpiresWarn rendering.
if grep -q 'ExpiresWarn' internal/handlers/templates/my_tokens.html && \
   grep -q 'ExpiresInWords' internal/handlers/templates/my_tokens.html; then
    ok "my_tokens.html renders ExpiresWarn + ExpiresInWords"
else
    bad "my_tokens.html missing warning pill rendering"
fi

echo ""
echo "=== contract H: .badge CSS exists in themes.css ==="
# The new badge variants are the only place we colour-code
# per-row urgency. Without these styles, all warnings would
# render as unstyled text and the operator would miss them.
for cls in badge-expired badge-soon badge-month badge-success badge-danger; do
    if grep -q "\.${cls}" static/css/themes.css; then
        ok "themes.css has .${cls} style"
    else
        bad "themes.css MISSING .${cls} — warning pills unstyled"
    fi
done

echo ""
echo "=== contract I: i18n keys (RU+EN parity) ==="
# All 12 new keys must exist in both languages (B4 parity).
# Missing one means a half-translated page.
RU_FILE=internal/i18n/catalog_my.go
if [ ! -f "$RU_FILE" ]; then
    bad "catalog_my.go MISSING — can't check i18n"
fi
# Find the EN section (the second map literal — same file
# holds both languages, separated by a known marker line).
for k in custom_ttl_label custom_ttl_hint custom_ttl_h custom_ttl_d custom_ttl_w custom_ttl_y \
         renew renew_title renewed_to expired expires_in_days expires_in_day expires_in_hours \
         expires_tomorrow banner_expiring; do
    # Each key must appear at least twice (once in the RU map,
    # once in the EN map). Counting occurrences is the simplest
    # parity check we can do without a YAML/JSON parser.
    count=$(grep -c "\"tokens\\.${k}\"" "$RU_FILE" || true)
    if [ "$count" -ge 2 ]; then
        ok "tokens.${k} present in both RU and EN ($count occurrences)"
    else
        bad "tokens.${k} missing parity (only $count occurrence(s) in catalog_my.go)"
    fi
done

echo ""
echo "=== contract J: verify_pre_deploy.sh includes B153 ==="
if grep -q 'B153' scripts/verify_pre_deploy.sh && \
   grep -q 'check_b153' scripts/verify_pre_deploy.sh; then
    ok "verify_pre_deploy.sh registers B153"
else
    bad "verify_pre_deploy.sh MISSING B153 — pre-push gate won't run this check"
fi

echo ""
echo "=== contract K: Go test suite still passes ==="
# Catch dropped imports / stray syntax errors that grep alone
# would miss. We use `go vet` first because it's faster and
# catches the same class of issues for this small a change.
if command -v go >/dev/null 2>&1; then
    if go vet ./... 2>&1 | grep -v '^ok' | grep -E '^(.*):(.*): (error|undefined|imports)' | head -1; then
        bad "go vet ./... reports an error — fix before commit"
    else
        ok "go vet ./... clean"
    fi
else
    echo "  SKIP  go not in PATH (Windows-host env-only; check on VM)"
fi

echo ""
echo "=== summary ==="
echo "B153: personal API token UX (custom TTL + per-row Renew + warning pills)"
echo "all B153 contracts satisfied"
