#!/usr/bin/env bash
# check_b324_oidc_guidance.sh
#
# 2026-09-25 (B324, v1.5.89) — the "OIDC is currently disabled" banner must be ACTIONABLE.
#
# OPERATOR REPORT (with a screenshot of /admin/oidc):
#
#   «про OIDC, на скрине указано что нехватает параметра в env для корректной работы однако
#    нигде не пишится пример того что в нем должно быть для того чтобы открыть данный
#    функционал»
#
# The banner named the variable (SKYGATE_OIDC_ISSUER), the file (.env) and the required action
# (restart the container) — but never the VALUE SHAPE, so an operator could not enable the
# feature without reading the source. It also pointed at a restart that, under docker, does
# not apply a changed .env at all (the container environment is frozen at creation).
#
# CONTRACTS
#   A. the RU and EN banners both carry a copy-paste env example (issuer + client id/secret +
#      redirect URIs), not just the variable name
#   B. the example states the /oidc suffix and the literal-match rule (issuer mismatch)
#   C. the banner points at the service-control page and explains restart vs recreate
#   D. the template renders the banner as HTML (safeHTML) so the example is readable
#   E. the page keeps the three env names the operator must set
#   F. i18n parity + this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B324: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CAT=internal/i18n/catalog_admin.go
TPL=internal/handlers/templates/admin/oidc_settings.html

hdr "B324 — the OIDC banner must say WHAT to put in the env"

# The RU and EN values of oidc.disabled_warn: extract the two literals in file order.
WARN_RU="$(grep -A2 '"oidc.disabled_warn"' "$CAT" | head -30 | tr -d '\r')"

for needle in 'SKYGATE_OIDC_ISSUER=https://' 'SKYGATE_OIDC_CLIENT_ID=' 'SKYGATE_OIDC_CLIENT_SECRET=' 'SKYGATE_OIDC_REDIRECT_URIS='; do
  n=$(grep -c -F -- "$needle" "$CAT")
  if [ "$n" -ge 2 ]; then
    ok "A: the example carries '$needle' in BOTH catalogues"
  else
    bad "A: '$needle' appears $n time(s) — the example must be in RU and EN"
  fi
done
if [ "$(grep -c -F 'SKYGATE_OIDC_ISSUER=https://' "$CAT")" -ge 2 ]; then
  ok "A2: the issuer example is a full URL (not a bare hostname)"
else
  bad "A2: the issuer example is missing the URL shape"
fi

if [ "$(grep -c -F '/oidc' "$CAT")" -ge 2 ]; then
  ok "B1: the example names the /oidc suffix"
else
  bad "B1: the issuer suffix is not explained"
fi
if [ "$(grep -c 'issuer mismatch' "$CAT")" -ge 2 ]; then
  ok "B2: the literal-match rule (issuer mismatch) is stated in both languages"
else
  bad "B2: the issuer-match rule is not stated"
fi

if [ "$(grep -c '/admin/service' "$CAT")" -ge 2 ]; then
  ok "C1: the banner links to the service-control page in both languages"
else
  bad "C1: the banner does not point at /admin/service"
fi
if [ "$(grep -c 'force-recreate' "$CAT")" -ge 2 ]; then
  ok "C2: the docker recreate requirement is explained (restart alone does not apply .env)"
else
  bad "C2: the restart-vs-recreate distinction is missing"
fi
if [ "$(grep -c 'EnvironmentFile' "$CAT")" -ge 2 ]; then
  ok "C3: the native path (EnvironmentFile re-read on restart) is explained"
else
  bad "C3: the native env-apply path is not explained"
fi

if grep -q 'oidc.disabled_warn" | safeHTML' "$TPL"; then
  ok "D1: the template renders the banner as HTML (the example stays readable)"
else
  bad "D1: the banner is not rendered with safeHTML — the example would show as escaped markup"
fi
if grep -q '{{t "oidc.disabled_warn"' "$TPL"; then
  ok "D2: the banner comes from the catalogue, not from hardcoded text in the template"
else
  bad "D2: the banner is not an i18n key"
fi

n=$(grep -c 'SKYGATE_OIDC_ISSUER' "$TPL")
if [ "$n" -ge 1 ]; then
  ok "E1: the page still shows which env variables the provider needs"
else
  bad "E1: the page no longer names the env variables"
fi

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F1: the i18n parity test passes (rule 10)"
  else
    bad "F1: i18n parity failed:"; printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "F1: go not on PATH — run the parity test on the VM"
fi
if git ls-files --error-unmatch scripts/check_b324_oidc_guidance.sh >/dev/null 2>&1; then
  ok "F2: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "F2: this script is NOT tracked by git"
fi

printf '\n\033[1mB324 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
