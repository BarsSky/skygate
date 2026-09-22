#!/usr/bin/env bash
# check_b290_oidc_ui_enablement.sh
#
# 2026-09-22 (B290) — «OIDC должен включаться из интерфейса, без правки .env».
#
# LIVE REPORT (operator): «нет опять же удобного выставления включения OIDC —
# пока нет в env строчки нельзя никак настроить, но из описания непонятно что и
# как добавлять, и удобнее было бы иметь автономный механизм включения без правки
# в ручную env».
#
# What the code actually did (three defects behind that one complaint):
#
#   1. /admin/oidc SAVED the settings but told the operator "restart skygate
#      (/admin/update) to apply" — nothing observable changed, so the feature felt
#      absent even though a form existed;
#   2. the page rendered the ENV values, not the effective ones, so a saved row
#      was invisible on the page that had just saved it — and a DB row with
#      enabled=0 was ignored by the boot path (only the env off-switch counted);
#   3. the client_secret was stored in the clear.
#
# CONTRACTS
#   A. the effective configuration is UI-over-env, with a per-field source
#   B. saving APPLIES to the running provider (no restart), and the page shows
#      the live state next to the form
#   C. the client_secret is encrypted at rest and never echoed back
#   D. the boot path honours the UI switch (enabled=0 disables the provider)
#   E. the page explains what to put in every field (RU + EN)
#   F. the regression tests exist and pass
#   G. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B290: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

PAGE=internal/feature/admin/oidc_settings.go
SVC=internal/feature/admin/service.go
DBF=internal/db/oidc_settings_b277_5.go
MAIN=cmd/skygate/main.go
TMPL=internal/handlers/templates/admin/oidc_settings.html
I18N=internal/i18n/catalog_admin.go
TEST=internal/feature/admin/oidc_settings_b290_test.go

hdr "B290 — OIDC must be enableable from the UI"

# --- A: effective configuration, UI over env --------------------------------
if grep -q '^func (s \*Service) effectiveOIDCSettings()' "$PAGE"; then
  ok "A1: the effective-configuration resolver exists"
else
  bad "A1: effectiveOIDCSettings is missing — the page would show env values only"
fi
if grep -q 'GetOIDCSettingsDecrypted' "$PAGE" && grep -q 'row.Issuer' "$PAGE"; then
  ok "A2: the saved row is what the form renders (not the env values)"
else
  bad "A2: the page ignores the saved row"
fi
if grep -q 'Source\["issuer"\]\|set(field, strings.TrimSpace(rowVal), "ui")' "$PAGE" && grep -q '"env"' "$PAGE"; then
  ok "A3: every field carries its source (ui / env / default)"
else
  bad "A3: the page cannot say where a value came from"
fi
if grep -q 'EnvDisabled' "$PAGE" && grep -q 'OIDCEnabledEnv' "$PAGE"; then
  ok "A4: the env emergency off-switch is detected and explained"
else
  bad "A4: SKYGATE_OIDC_ENABLED is not surfaced"
fi

# --- B: saving applies immediately ------------------------------------------
if grep -q 'OIDCApplier func(' "$SVC" && grep -q 'OIDCStatusFn func(' "$SVC"; then
  ok "B1: the admin Service exposes an applier + a live-status hook"
else
  bad "B1: no applier hook — a save could only announce a restart"
fi
if grep -q 's.OIDCApplier(' "$PAGE"; then
  ok "B2: POST /admin/oidc applies the configuration to the running provider"
else
  bad "B2: the save handler does not apply anything"
fi
if grep -q 'oidcSvc.ApplyConfig(issuer, clientID, clientSecret, redirectURIs)' "$MAIN"; then
  ok "B3: main.go wires the applier to the live OIDC service"
else
  bad "B3: the applier is not wired to oidc.Service.ApplyConfig"
fi
if grep -q '^func (s \*Service) ApplyConfig(' internal/oidc/service.go && grep -q 'cfgMu' internal/oidc/service.go; then
  ok "B4: the OIDC service supports a lock-guarded runtime reconfiguration"
else
  bad "B4: ApplyConfig / cfgMu missing in internal/oidc"
fi
if grep -q 'oidc.live_title' "$TMPL" && grep -q 'LiveIssuer' "$TMPL"; then
  ok "B5: the page shows what the RUNNING provider holds"
else
  bad "B5: the page does not show the live provider state"
fi
if grep -q 'OIDC settings saved. Restart skygate' "$PAGE"; then
  bad "B6: the handler still tells the operator to restart"
elif grep -q 'сохранены и применены' "$PAGE"; then
  ok "B6: the save path reports an applied change, not a restart"
else
  bad "B6: no 'applied' message in the save path"
fi

# --- C: the secret is encrypted at rest --------------------------------------
if grep -q 'oidcSecretEncPrefix' "$DBF" && grep -q '^func SaveOIDCSettingsEncrypted(' "$DBF" && grep -q '^func GetOIDCSettingsDecrypted(' "$DBF"; then
  ok "C1: the storage layer encrypts the secret and marks it (enc:v1:)"
else
  bad "C1: the secret is still stored in the clear"
fi
if grep -q 'db.SaveOIDCSettingsEncrypted(s.dbc(), settings, s.SecretKeyHex)' "$PAGE"; then
  ok "C2: the form saves through the encrypting path"
else
  bad "C2: the form bypasses the encryption"
fi
if grep -q 'legacy plaintext row' "$DBF"; then
  ok "C3: a pre-B290 plaintext row still reads (no migration trap)"
else
  bad "C3: legacy plaintext rows are not handled"
fi

# --- D: boot honours the UI switch ------------------------------------------
if grep -q 'oidc_settings.enabled=0' "$MAIN"; then
  ok "D1: a DB row with enabled=0 disables the provider at boot"
else
  bad "D1: the boot path ignores the UI switch"
fi
if grep -q 'GetOIDCSettingsDecrypted(app.DB.Current(), app.SecretKeyHex)' "$MAIN"; then
  ok "D2: boot reads the (decrypted) DB row"
else
  bad "D2: boot does not read the encrypted row"
fi
if grep -q 'effectiveIssuer = ""' "$MAIN"; then
  ok "D3: 'disabled' is expressed as an empty issuer, so the routes stay mounted and can be re-enabled live"
else
  bad "D3: disable still nils the service (unrepresentable in the UI and a nil-deref risk)"
fi

# --- E: the page explains every field ---------------------------------------
for key in oidc.form_what_to_fill oidc.form_help_issuer oidc.form_help_client oidc.form_help_redirect oidc.live_on oidc.live_off; do
  n=$(grep -c "\"$key\"" "$I18N" || true)
  if [ "${n:-0}" -ge 2 ]; then
    ok "E1: $key exists in RU and EN"
  else
    bad "E1: $key missing (found ${n:-0}, want 2)"
  fi
done

# --- F: the regression tests -------------------------------------------------
if [ -f "$TEST" ]; then
  ok "F1: $TEST exists"
else
  bad "F1: $TEST is missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B290' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F2: the B290 tests pass"
  else
    bad "F2: the B290 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/oidc/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F3: the oidc package still passes (the accessor refactor is behaviour-preserving)"
  else
    bad "F3: the oidc package tests failed:"
    printf '%s\n' "$OUT" | tail -12 | sed 's/^/       /' >&2
  fi
else
  skip "F2-F3: go not on PATH — run the B290 tests on the VM"
fi

# --- G: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b290_oidc_ui_enablement.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b290_oidc_ui_enablement.sh is tracked by git"
else
  bad "G1: scripts/check_b290_oidc_ui_enablement.sh is NOT tracked"
fi

printf '\n\033[1mB290 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
