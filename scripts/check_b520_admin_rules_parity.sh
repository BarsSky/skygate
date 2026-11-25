#!/usr/bin/env bash
# check_b520_admin_rules_parity.sh
#
# 2026-11-25 (B520) — админская страница получает те же возможности, что и
# пользовательская: выключить правило (чужое), увидеть выключенное, увидеть
# ИСТОЧНИК подсети и листать по номерам.
#
# WHY THREE THINGS AND NOT ONE: the operator's original request was «переработать
# страницы с правилами у пользователей и администратора». The user page got the
# group model (B516), the on/off switch (B517/B519) and the source card (B518). The
# admin page kept a tree with no switch and prev/next-only paging — so the same
# person, one tab over, still could not do the two things the request was about.
#
# THE THREE THINGS, and the trap in each:
#
#   * THE SWITCH. It writes ANOTHER user's row, so the owner is resolved per id
#     (db.GetRuleAdminContext, the same helper B379's delete uses) and the AUDIT row
#     names the ADMIN — writing the owner into the actor column would make the trail
#     claim the user switched off their own rule. `setRulesEnabled`'s struct carries
#     both (B520 groundwork).
#   * THE VISIBILITY. The admin list is enabled-only by construction (B358's counters
#     are), so a rule switched off HERE would vanish and could not be switched back on
#     from the page that did it — the «only delete and add» trap, one role up.
#     `?show_disabled=1` widens the same group window through four sibling statements
#     (the enabled-only ones keep their B358/B351 contracts untouched), and the flag
#     survives the action.
#   * THE PROVENANCE. The admin table printed `parent_domain` verbatim, i.e.
#     `cdn:cloudflare:discordapp.com`. `AdminRule.SourceLabel` is the same contract as
#     the user page's (B518), so the two pages cannot disagree about where a subnet
#     came from — and the numbered pager is the shared define, not a second one.
#
# CONTRACTS
#   A. the endpoint exists, is admin-gated, resolves the owner per row and audits the admin
#   B. the switched-off view exists in the data layer and survives the action
#   C. the page renders the switch, the off chip, the parsed source and the numbered pager
#   D. i18n RU+EN
#   E. bookkeeping + behavioural pins

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B520: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SVC=internal/feature/exit_rules/admin_toggle_b520.go
FORM=internal/feature/exit_rules/form_admin.go
DBQ=internal/db/queries.go
DBF=internal/db/device_rules.go
ROUTES=cmd/skygate/routes.go
TPL=internal/handlers/templates/admin/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go
TEST=internal/feature/exit_rules/admin_toggle_b520_test.go
RENDER=internal/handlers/exit_rules_admin_toggle_render_b520_test.go

hdr "B520 — the admin page can switch a rule off, see it off, and page by number"

# --- A: the endpoint, the gate, the owner, the actor ---------------------------
A_MISS=""
grep -q 'func (s \*Service) PostAdminExitRuleToggle(' "$SVC" \
  || A_MISS="${A_MISS}PostAdminExitRuleToggle is missing"$'\n'
grep -q 'mux.Handle("POST /admin/exit-rules/toggle", authMW' "$ROUTES" \
  || A_MISS="${A_MISS}the route is not registered behind authMW"$'\n'
grep -q 'if !c.IsAdmin {' "$SVC" \
  || A_MISS="${A_MISS}the handler does not re-check IsAdmin (authMW only means "logged in")"$'\n'
grep -q 'db.GetRuleAdminContext(s.dbc(), id)' "$SVC" \
  || A_MISS="${A_MISS}the owner is not resolved from the row — a form-supplied owner would be trusted"$'\n'
grep -q 'AuditID:   c.UserID' "$SVC" \
  || A_MISS="${A_MISS}the audit row is not attributed to the admin (it must not blame the owner)"$'\n'
grep -q 'Action:    "admin_rule_"' "$SVC" \
  || A_MISS="${A_MISS}the audit action does not distinguish an admin change from the user's own"$'\n'
grep -q 's.setRulesEnabled(ruleStateChange{' "$SVC" \
  || A_MISS="${A_MISS}the admin switch does not use the shared state-change tail"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: admin-only route, owner resolved per row, audit naming the admin, one shared tail"
else
  bad "A1: the admin switch is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# --- B: the switched-off view -------------------------------------------------
B_MISS=""
for c in qSelectAdminRuleGroupsAll qCountAdminRuleGroupsAll qSelectAllRulesForAdminCountAll qSelectAllRulesForAdminPagedAll; do
  grep -q "const $c = " "$DBQ" || B_MISS="${B_MISS}$c is missing — a switched-off rule would be invisible"$'\n'
done
grep -q 'func AdminPageIncludeDisabled()' "$DBF" \
  || B_MISS="${B_MISS}the window has no include-disabled option"$'\n'
grep -q 'func GetAllRulesForAdminPaged(d \*sql.DB, page, groupsPerPage int, opts ...AdminPageOption)' "$DBF" \
  || B_MISS="${B_MISS}GetAllRulesForAdminPaged does not accept the option (a variadic keeps every existing caller intact)"$'\n'
grep -q 'adminRulesForGroupsQuerySQL(groups, rowsSQL)' "$DBF" \
  || B_MISS="${B_MISS}the row fetch does not follow the chosen statement"$'\n'
grep -q 'showDisabled := strings.TrimSpace(r.URL.Query().Get("show_disabled")) == "1"' "$FORM" \
  || B_MISS="${B_MISS}the admin handler does not read ?show_disabled"$'\n'
grep -q 'db.AdminPageIncludeDisabled()' "$FORM" \
  || B_MISS="${B_MISS}the handler does not pass the option to the window"$'\n'
grep -q 'kv\["show_disabled"\] = "1"' "$SVC" \
  || B_MISS="${B_MISS}the action does not preserve the view (the row the operator just switched off would disappear)"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: the switched-off view exists as sibling statements, is opt-in, and survives the action"
else
  bad "B1: the switched-off view is incomplete:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# --- C: what the page renders -------------------------------------------------
C_MISS=""
grep -q 'action="/admin/exit-rules/toggle"' "$TPL" \
  || C_MISS="${C_MISS}the admin rows have no switch form"$'\n'
grep -q 'name="enabled" value="{{if .Enabled}}0{{else}}1{{end}}"' "$TPL" \
  || C_MISS="${C_MISS}a row's switch does not offer the OPPOSITE state"$'\n'
grep -q '{{t "exit_rules.row_off"}}</span>{{end}}' "$TPL" \
  || C_MISS="${C_MISS}a switched-off row carries no chip — it would read as active"$'\n'
grep -q '{{.SourceLabel}}' "$TPL" \
  || C_MISS="${C_MISS}the parent column still prints the raw parent_domain"$'\n'
if grep -q '{{.ParentDomain}}' "$TPL"; then
  C_MISS="${C_MISS}the raw parent_domain is still rendered somewhere (it is a storage format)"$'\n'
fi
grep -q 'func (r AdminRule) SourceLabel() string' "$FORM" \
  || C_MISS="${C_MISS}AdminRule.SourceLabel is missing"$'\n'
grep -q '{{with .AdminPager}}{{template "rules-pager" .}}{{end}}' "$TPL" \
  || C_MISS="${C_MISS}the admin page does not render the numbered pager"$'\n'
grep -q 'id="admin-show-disabled"' "$TPL" \
  || C_MISS="${C_MISS}there is no control to reach the switched-off view"$'\n'
grep -q 'id="admin-toggle-banner"' "$TPL" \
  || C_MISS="${C_MISS}the outcome banner is missing"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: switch, off chip, parsed source and the shared numbered pager are on the admin page"
else
  bad "C1: the admin page is incomplete:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# The shared pager lives in its own file (B520): the admin body renders it, and a
# render harness that parses one file must still find the define.
if [ -f internal/handlers/templates/rules_pager.html ] \
   && grep -q '{{define "rules-pager"}}' internal/handlers/templates/rules_pager.html \
   && ! grep -q '{{define "rules-pager"}}' "$TPL"; then
  ok "C2: the pager is one shared define in its own template file (both pages include it)"
else
  bad "C2: the pager define is not a single shared file — the two pages would drift"
fi

# --- D: i18n -------------------------------------------------------------------
D_MISS=""
for key in show_disabled only_enabled; do
  n=$(grep -c "\"exit_rules_admin\.$key\"[[:space:]]*:" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -eq 2 ] || D_MISS="${D_MISS}exit_rules_admin.$key is defined ${n:-0} time(s), want exactly 2 (RU + EN)"$'\n'
done
# The banner reuses the user page's wording; a rework must not drop it.
for key in toggle_ok_off toggle_ok_on toggle_off_title toggle_on_title toggle_confirm row_off source_err; do
  grep -q "\"exit_rules\.$key\"[[:space:]]*:" "$CAT" \
    || D_MISS="${D_MISS}the reused key exit_rules.$key is gone — the admin banner would print a raw key"$'\n'
done
grep -q '"exit_rules_admin\.show_disabled":[[:space:]]*"Показать выключенные"' "$CAT" \
  || D_MISS="${D_MISS}the RU filter label is not «Показать выключенные»"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: every new string exists twice (RU+EN) and the reused wording is intact"
else
  bad "D1: the wording is not complete:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- E: bookkeeping + behavioural pins -----------------------------------------
E_MISS=""
git ls-files --error-unmatch scripts/check_b520_admin_rules_parity.sh >/dev/null 2>&1 \
  || E_MISS="${E_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
for f in "$SVC" "$TEST" "$RENDER" internal/handlers/templates/rules_pager.html; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || E_MISS="${E_MISS}$f is NOT tracked by git"$'\n'
done
grep -q 'check_b520_admin_rules_parity.sh' scripts/verify_pre_deploy.sh \
  || E_MISS="${E_MISS}verify_pre_deploy.sh does not register B520"$'\n'
grep -q '\*\*B520\*\*' AGENTS.md \
  || E_MISS="${E_MISS}AGENTS.md's block index has no B520 entry (AGENTS rule 2)"$'\n'
for t in TestB520_AdminSwitchesAnotherUsersRuleOff TestB520_SwitchedOffViewSurvivesTheAction \
         TestB520_NonAdminIsRefused TestB520_UnknownRuleAndBadFormsAreRefused \
         TestB520_ReturnPathIsValidated; do
  grep -q "func $t(" "$TEST" || E_MISS="${E_MISS}the test $t is missing"$'\n'
done
for t in TestB520_AdminRowOffersTheSwitchAndTheOffChip TestB520_SourceColumnShowsTheDomainNotTheMarker \
         TestB520_ShowDisabledLinkAndBanner TestB520_NumberedPagerIsOnTheAdminPage; do
  grep -q "func $t(" "$RENDER" || E_MISS="${E_MISS}the render test $t is missing"$'\n'
done
if [ -z "$E_MISS" ]; then
  ok "E1: tracked, registered, indexed, and pinned by handler and render tests"
else
  bad "E1: the block bookkeeping is incomplete:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB520 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
