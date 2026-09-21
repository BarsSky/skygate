#!/usr/bin/env bash
# check_pagination.sh — v1.5.43 (/my/exit-rules + /admin/exit-rules)
#
# 2026-09-21 — server-side pagination. The pre-v1.5.43 handlers
# called s.getDeviceRules(c.UserID) / db.GetAllRulesForAdmin() which
# loaded every enabled rule in one round-trip. For users with
# 1500+ rules (typical for admins managing many domains across
# many devices) the per-host + CDN-grouped render is O(N²) on
# row count — 3-5s page load, >500KB HTML.
#
# CONTRACTS (A–F)
#   A  query constants carry LIMIT/OFFSET and a COUNT(*) companion
#   B  helpers clamp page bounds (1..clamp) + lastPage
#   C  /my/exit-rules handler reads ?page=&page_size= and passes RulePage
#   D  /admin/exit-rules handler does the same (unfiltered path only)
#   E  templates render pagination controls (only when Total > PageSize)
#   F  template funcs divceil / sub are registered

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'pagination: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

QUERIES=internal/db/queries.go
DB=internal/db/device_rules.go
SVC=internal/feature/exit_rules/form_my.go
ADMIN=internal/feature/exit_rules/form_admin.go
TMPL=internal/handlers/templates/exit_rules.html
ADMINTMPL=internal/handlers/templates/admin/exit_rules.html
TFUNCS=internal/handlers/templates.go

hdr "v1.5.43 — server-side pagination for /my/exit-rules + /admin/exit-rules"

# --- A: query constants carry LIMIT/OFFSET + COUNT(*) -----------------
if grep -q 'qSelectUserRulesForViewPaged' "$QUERIES" && grep -q 'LIMIT \$2 OFFSET \$3' "$QUERIES"; then
  ok "A1: qSelectUserRulesForViewPaged exists with LIMIT \$2 OFFSET \$3"
else
  bad "A1: the paged user SELECT is missing — /my/exit-rules cannot paginate"
fi

if grep -q 'qSelectUserRulesForViewCount' "$QUERIES"; then
  ok "A2: qSelectUserRulesForViewCount companion COUNT(*) exists for Total"
else
  bad "A2: the COUNT(*) companion is missing — Total=0 in template"
fi

if grep -q 'qSelectAllRulesForAdminPaged' "$QUERIES"; then
  ok "A3: qSelectAllRulesForAdminPaged exists for the cross-user admin view"
else
  bad "A3: the admin cross-user paged SELECT is missing"
fi

# --- B: helpers clamp page bounds + lastPage ------------------------------
if grep -q 'GetDeviceRulesForUserPaged' "$DB"; then
  ok "B1: db.GetDeviceRulesForUserPaged helper exists with page/pageSize clamp"
else
  bad "B1: the user-paged helper is missing"
fi

if grep -q 'pageSizeClamp\s*=' "$DB" && grep -q '500' "$DB"; then
  ok "B2: pageSizeClamp constant exists (upper bound on a single-page response)"
else
  bad "B2: pageSizeClamp is missing — operators can request unbounded page sizes"
fi

if grep -q 'GetAllRulesForAdminPaged' "$DB"; then
  ok "B3: db.GetAllRulesForAdminPaged helper exists (admin path)"
else
  bad "B3: the admin-paged helper is missing"
fi

# --- C: /my/exit-rules reads ?page=&page_size= ---------------------------
if grep -q 'GetDeviceRulesForUserPaged' "$SVC"; then
  ok "C1: form_my.go GetMyExitRules calls the paged helper"
else
  bad "C1: /my/exit-rules still uses the unpaged getDeviceRules — load is O(N)"
fi

if grep -q 'r.URL.Query().Get(\"page\")' "$SVC" && grep -q 'r.URL.Query().Get(\"page_size\")' "$SVC"; then
  ok "C2: handler reads ?page= and ?page_size= from URL"
else
  bad "C2: handler does not read the pagination query params"
fi

# --- D: /admin/exit-rules (unfiltered path only) -----------------------
if grep -q 'GetAllRulesForAdminPaged' "$ADMIN"; then
  ok "D1: form_admin.go AdminExitRules calls the paged helper (unfiltered path)"
else
  bad "D1: /admin/exit-rules (unfiltered) still loads all rules — slow on big tails"
fi

if grep -q 'deviceFilter != \"\"' "$ADMIN"; then
  # The device-filtered path intentionally stays unpaged (small
  # scope, ~50–200 rules typical) — only the cross-user path
  # needs pagination.
  ok "D2: the device-filtered drill-down stays unpaged (intentional — small scope)"
else
  bad "D2: device-filtered path is unexpectedly affected by the pagination refactor"
fi

# --- E: templates render pagination controls --------------------------
if grep -q 'RulePage' "$TMPL" && grep -q 'pagination' "$TMPL"; then
  ok "E1: /my/exit-rules template iterates .RulePage and renders pagination controls"
else
  bad "E1: /my/exit-rules template does not render pagination controls"
fi

if grep -q 'RulePage' "$ADMINTMPL" && grep -q 'admin-page-prev\|RulePage.Page' "$ADMINTMPL"; then
  ok "E2: /admin/exit-rules template iterates .RulePage and renders pagination"
else
  bad "E2: /admin/exit-rules template does not render pagination"
fi

# The controls must be HIDDEN when the dataset fits in one page —
# `gt .RulePage.Total .RulePage.PageSize` guards the block.
if grep -q 'gt .RulePage.Total .RulePage.PageSize' "$TMPL" && grep -q 'gt .RulePage.Total .RulePage.PageSize' "$ADMINTMPL"; then
  ok "E3: pagination controls are hidden when Total <= PageSize (no useless controls)"
else
  bad "E3: the conditional guard is missing — pagination renders even on a single page"
fi

# --- F: template funcs divceil / sub are registered -------------------
if grep -q '"divceil":' "$TFUNCS" && grep -q '"sub":' "$TFUNCS"; then
  ok "F1: divceil + sub template funcs are registered in the FuncMap"
else
  bad "F1: divceil/sub are missing — template execution will fail on the new pagination calls"
fi

# --- G: i18n parity for the new pagination keys -------------------------
KEYS=0
for k in prev_page next_page page_size pagination_page_of pagination_total; do
  n=$(grep -c "exit_rules.$k\"" internal/i18n/catalog_exit_rules.go)
  [ "$n" -ge 2 ] && KEYS=$((KEYS+1))
done
if [ "$KEYS" -eq 5 ]; then
  ok "G1: all 5 pagination keys exist in RU and EN (5/5 pairs)"
else
  bad "G1: only $KEYS/5 pagination keys are present in both catalogues"
fi

# --- H: the script is tracked by git (trap #11) -------------------------
if git ls-files --error-unmatch scripts/check_pagination.sh >/dev/null 2>&1; then
  ok "H1: scripts/check_pagination.sh is tracked by git"
else
  bad "H1: scripts/check_pagination.sh is NOT tracked — .gitignore can eat it silently"
fi

# --- I: unit tests pin the bounds-clamping math ------------------------
if grep -q 'func TestClampPageBounds' internal/db/pagination_b_test.go 2>/dev/null; then
  ok "I1: pagination clamp math has unit tests"
else
  bad "I1: no unit tests for clampPageBounds — a regression in the bounds goes unnoticed"
fi

printf '\n\033[1mpagination summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
