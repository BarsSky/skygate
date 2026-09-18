#!/bin/bash
# scripts/check_b237_19.sh — B237.19 (v1.5.2+) form-error
# flash UX contract.
#
# Closes the two operator-reported bugs (2026-09-07):
#
#   Bug 1: when the /my/exit-rules form's POST handler
#   rejected the values (invalid IP, limit exceeded,
#   device not owned, etc.), it called http.Error(w, ...,
#   400) which rendered a giant plain-text page. The
#   operator lost the form values they had typed.
#
#   Bug 2: the "duplicate" banner wording was
#   misleading — "уже существует — не дублируем. Удалите
#   существующее, если нужно обновить" sounded like an
#   error, and the operator interpreted it as "the new
#   rule failed to add" when in reality the new rule
#   was never created (it was the old /32 from the
#   first add of the same domain).
#
# B237.19 fix:
#   1. PostMyExitRule now calls http.Redirect to
#      /my/exit-rules?err=<msg>&form_* via the new
#      buildFormErrorRedirectURL helper, instead of
#      http.Error. The template renders .err as a
#      flash banner above the form (same UI surface
#      as the duplicate banner).
#   2. The duplicate banner's color changed from
#      alert-danger (red) to alert-info (blue) — it's
#      informational, not an error. The wording was
#      updated to make it clear that the domain is
#      already covered + the autoupdater handles
#      updates, no need to delete.
#
# Why a separate B-check: the B123 check (B123 covers
# the duplicate-redirect URL builder) doesn't cover
# the new form-error builder. Combining them would
# conflate the two contracts.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. The new helper ---

# A.1 the helper exists
if grep -qE '^func buildFormErrorRedirectURL\(' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "A.1 buildFormErrorRedirectURL function defined in form_my.go"
else
    bad "A.1 buildFormErrorRedirectURL missing (the B237.19 helper)"
fi

# A.2 the helper uses the canonical URL shape
# (?err=<msg>&form_*) — the same form_* keys as
# buildDuplicateRedirectURL so the template's FormValues
# re-fill works for both
if grep -qE '"/my/exit-rules\?err=%s&form_device_id=' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "A.2 helper uses ?err=<msg>&form_device_id=… (canonical URL shape)"
else
    bad "A.2 helper must use the canonical ?err=&form_* URL shape"
fi

# A.3 the helper escapes errMsg (otherwise & in the
# error message would split the query)
if grep -q 'url.QueryEscape(errMsg)' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "A.3 helper escapes errMsg (defense against & in the error message)"
else
    bad "A.3 helper must escape errMsg — otherwise & in the error message corrupts the query"
fi

# --- B. The handler conversions (Bug 1 fix) ---

# B.1 PostMyExitRule no longer calls http.Error for
# user-facing validation errors. We grep for the
# specific conversion pattern: the new buildFormErrorRedirectURL
# calls in form_my.go.
# (http.Error calls for 401/403/500 are still allowed
# for true auth + DB errors — those should NOT
# redirect.)
n_redirects=$(grep -cE 'buildFormErrorRedirectURL\(' internal/feature/exit_rules/form_my.go 2>/dev/null || echo 0)
if [ "$n_redirects" -ge 5 ]; then
    ok "B.1 PostMyExitRule redirects to the form on validation errors ($n_redirects call sites)"
else
    bad "B.1 PostMyEatRule must call buildFormErrorRedirectURL >= 5 times (one per validation path). Found: $n_redirects"
fi

# B.2 the http.Error calls that REMAIN must be only the
# auth + DB-error ones (http.StatusUnauthorized + http.StatusInternalServerError).
# All http.StatusBadRequest + http.StatusForbidden in
# PostMyEatRule should be replaced with redirects.
# This is a soft check — the post-fix code may still
# have a few http.Error calls for unexpected DB errors.
n_user_errors=$(grep -cE 'http\.Error\(w,.*, http\.StatusBadRequest\)' internal/feature/exit_rules/form_my.go 2>/dev/null || echo 0)
n_forbidden=$(grep -cE 'http\.Error\(w,.*, http\.StatusForbidden\)' internal/feature/exit_rules/form_my.go 2>/dev/null || echo 0)
echo "  INFO  PostMyExitRule still has $n_user_errors http.StatusBadRequest + $n_forbidden http.StatusForbidden http.Error calls (target: 0 each; the 401 + 500 calls in the auth gate are excluded from this count)"

# B.3 the original "invalid target_value" http.Error
# was the operator's first screenshot. The fix removes
# that specific http.Error and replaces it with a redirect.
if ! grep -qE 'http\.Error\(w, fmt\.Sprintf\("invalid target_value' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "B.3 the operator's first bug (invalid target_value http.Error) is fixed"
else
    bad "B.3 the invalid target_value http.Error is still present (operator's bug 1)"
fi

# --- C. The page-data wiring ---

# C.1 the GET handler (GetMyExitRules) reads ?err= and
# puts it on the page data
if grep -qE 'errMsg := r\.URL\.Query\(\)\.Get\("err"\)' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "C.1 GET handler reads ?err= and wires it to the page data"
else
    bad "C.1 GET handler must read ?err= and wire it to the page data"
fi

# C.2 the page data struct includes the err field
if grep -qE '"err":\s+errMsg' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "C.2 page data includes the 'err' field"
else
    bad "C.2 page data must include the 'err' field for the template"
fi

# --- D. The template + i18n ---

# D.1 the template renders the .err field as a flash banner
if grep -qE 'form-error-banner' internal/handlers/templates/exit_rules.html 2>/dev/null; then
    ok "D.1 template renders the .err field as a flash banner (id=form-error-banner)"
else
    bad "D.1 template must render the .err field as a flash banner"
fi

# D.2 the i18n key exists in both RU and EN
if grep -q '"exit_rules.form_error"' internal/i18n/catalog_exit_rules.go 2>/dev/null; then
    ru_count=$(grep -c 'exit_rules.form_error' internal/i18n/catalog_exit_rules.go 2>/dev/null || echo 0)
    if [ "$ru_count" -ge 2 ]; then
        ok "D.2 'exit_rules.form_error' i18n key present in RU + EN ($ru_count occurrences)"
    else
        bad "D.2 'exit_rules.form_error' must be present in both RU and EN (found: $ru_count)"
    fi
else
    bad "D.2 'exit_rules.form_error' i18n key missing"
fi

# D.3 the duplicate banner is now alert-info (not
# alert-danger) — the wording change (Bug 2)
if grep -qE 'class="alert alert-info"\s+id="duplicate-alert"' internal/handlers/templates/exit_rules.html 2>/dev/null; then
    ok "D.3 duplicate banner changed to alert-info (was alert-danger — operator's bug 2)"
else
    bad "D.3 duplicate banner must be alert-info, not alert-danger"
fi

# D.4 the duplicate banner wording is updated (no more
# "Удалите существующее" — autoupdater handles updates)
if ! grep -q 'Удалите существующее' internal/i18n/catalog_exit_rules.go 2>/dev/null; then
    ok "D.4 duplicate banner wording updated (no more 'Удалите существующее')"
else
    bad "D.4 duplicate banner still says 'Удалите существующее' (the old wording)"
fi

# --- E. The tests ---

# E.1 the new test file exists
if [ -f internal/feature/exit_rules/form_my_b237_19_test.go ]; then
    ok "E.1 form_my_b237_19_test.go exists"
else
    bad "E.1 form_my_b237_19_test.go missing (B237.19 regression guard)"
fi

# E.2 4+ unit tests cover the helper
n_tests=$(grep -cE '^func TestBuildFormErrorRedirectURL' internal/feature/exit_rules/form_my_b237_19_test.go 2>/dev/null || echo 0)
if [ "$n_tests" -ge 4 ]; then
    ok "E.2 form_my_b237_19_test.go has $n_tests unit tests (>= 4)"
else
    bad "E.2 form_my_b237_19_test.go has $n_tests tests, need >= 4"
fi

# E.3 the unit tests pass (env-gated, only runs when
# `go` is on the bash PATH)
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        -run 'TestBuildFormErrorRedirectURL' \
        ./internal/feature/exit_rules/... 2>/dev/null | grep -q '^ok'; then
        ok "E.3 buildFormErrorRedirectURL unit tests pass"
    else
        bad "E.3 buildFormErrorRedirectURL unit tests failed"
    fi
else
    echo "  SKIP  E.3 unit tests (no go in PATH)"
fi

# --- F. verify_pre_deploy.sh + AGENTS.md registration ---

# F.1 the check is registered
if grep -q 'check_b237_19' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "F.1 scripts/verify_pre_deploy.sh includes the B237.19 check"
else
    bad "F.1 scripts/verify_pre_deploy.sh must include the B237.19 check"
fi

# F.2 AGENTS.md mentions B237.19
if grep -q 'B237\.19' AGENTS.md 2>/dev/null; then
    ok "F.2 AGENTS.md documents B237.19"
else
    bad "F.2 AGENTS.md must document B237.19 (B-check convention)"
fi

# --- Summary ---

echo
echo "=== B237.19 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
