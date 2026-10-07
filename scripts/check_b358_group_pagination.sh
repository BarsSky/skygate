#!/usr/bin/env bash
# check_b358_group_pagination.sh
#
# 2026-10-06 (B358) — the admin exit-rules page paginated by RULE ROW, so three
# devices and two users became EIGHT pages with the users and the groups smeared
# across them.
#
# OPERATOR REPORT (verbatim):
#
#   «веб-форма до сих пор странно отображает правила exit rules для администратора
#    теперь восемь страниц при том что в группе всего три устройства и два
#    пользователя но видимо из-за общего количества правил отображает в свернутом
#    виде 8 страниц при этом пользователи и группы размазаны по этим восьми
#    страницам»
#
# MEASURED on the live PostgreSQL deployment: 393 enabled `device_rules` rows,
# 2 users, 3 devices — skyadmin/device 9 `skyworker` 219 rows, skyadmin/56 `cyborg`
# 12, michail/29 `basic` 162. The handler read `?page`/`?page_size` (defaults 1/50)
# and called `db.GetAllRulesForAdminPaged`, whose SQL was
# `ORDER BY r.user_id, r.device_id, r.id LIMIT $1 OFFSET $2` — 8 pages of 50 rows,
# where the unit of a page was a ROW and the operator's question («one user's /
# one device's rules together») could not be answered on any single page.
#
# THE CHANGE. The window unit is now a GROUP: a distinct `(user_id, device_id)` pair.
#
#   * `qSelectAdminRuleGroups` windows groups (LIMIT/OFFSET on the GROUP query);
#   * `qCountAdminRuleGroups` counts the groups and the distinct users, unpaginated;
#   * `qSelectAllRulesForAdminPaged` fetches the rows of exactly the page's groups
#     through an OR-chain of `(r.user_id = $n AND r.device_id = $n+1)` pairs, so a
#     group is NEVER split across pages, however many rows it holds;
#   * `qSelectAllRulesForAdminCount` gained the missing `enabled = 1` (it counted
#     disabled rows too, so it disagreed with every other counter on the page);
#   * the default window is `db.AdminGroupsPerPage` = 20 groups, which renders the
#     live shape as ONE page;
#   * a group larger than `db.AdminOversizedGroupRows` = 150 rows starts COLLAPSED
#     (its rows are already fetched — presentation only, no second query, no new JS
#     state);
#   * RU + EN keys for the new pagination line and the collapsed-group hint.
#
# CONTRACTS
#   A. the window is a GROUP window, and the rows are fetched by group
#   B. the counters/heading come from the database, and the disabled rows are gone
#   C. a group is never split, and an oversized group renders collapsed
#   D. the new i18n keys exist in BOTH catalogues and are used by the template
#   E. the `?device=` drill-down path is untouched
#   F. bookkeeping: tracked by git, registered in the catalog, indexed in AGENTS.md,
#      and behavioural Go tests pin the window
#
# The B351 contract C3 — which pinned the row-based ORDER BY as the desired state —
# is RENEGOTIATED IN PLACE in the same commit to assert this GROUP window. The
# property it protects is unchanged («the window follows the grouping the page
# renders»); the unit it protects it at is now the group, because grouping the ORDER
# BY alone still let a 219-row device straddle a page boundary.

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B358: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

QUERIES=internal/db/queries.go
DB=internal/db/device_rules.go
FORM=internal/feature/exit_rules/form_admin.go
TPL=internal/handlers/templates/admin/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go

# line_of <pattern> <file> — 1-based line number of the first match, or 0.
line_of() { grep -n -- "$1" "$2" 2>/dev/null | head -1 | cut -d: -f1 | tr -d '[:space:]'; }

hdr "B358 — the admin exit-rules window is the GROUP (user → device), not the rule row"

# --- A: the window is a group window ------------------------------------------
A_MISS=""
# The window itself: one row per distinct (user_id, device_id) that has an ENABLED
# rule, with its display name and its full row count.
grep -q 'const qSelectAdminRuleGroups = .*GROUP BY r\.user_id, r\.device_id ORDER BY r\.user_id, r\.device_id LIMIT \$1 OFFSET \$2' "$QUERIES" \
  || A_MISS="${A_MISS}qSelectAdminRuleGroups is not a (user_id, device_id) GROUP window with LIMIT/OFFSET — the page is still cut by row"$'\n'
grep -q 'const qSelectAdminRuleGroups = .*WHERE r\.enabled = 1' "$QUERIES" \
  || A_MISS="${A_MISS}the group window does not filter enabled = 1 (disabled rows would form groups of their own)"$'\n'
grep -q 'const qSelectAdminRuleGroups = .*LEFT JOIN portal_users u ON u\.id = r\.user_id' "$QUERIES" \
  || A_MISS="${A_MISS}the group window does not name the owner (an orphaned row must still form a group, as \"?\")"$'\n'
# The total-group companion.
grep -q 'const qCountAdminRuleGroups = .*COUNT(DISTINCT user_id)' "$QUERIES" \
  || A_MISS="${A_MISS}qCountAdminRuleGroups is missing or does not count the distinct users"$'\n'
grep -q 'const qCountAdminRuleGroups = .*WHERE enabled = 1 GROUP BY user_id, device_id' "$QUERIES" \
  || A_MISS="${A_MISS}the group COUNT does not use the same enabled/grouping predicate as the window"$'\n'
# The row fetch must NOT carry a row window any more: the LIMIT/OFFSET moved onto
# the group query. A LIMIT on this statement is the regression itself.
grep -q 'const qSelectAllRulesForAdminPaged = `SELECT .*WHERE (%s) AND r\.enabled = 1 ORDER BY r\.user_id, r\.device_id, r\.id`' "$QUERIES" \
  || A_MISS="${A_MISS}qSelectAllRulesForAdminPaged is not the group-scoped row fetch (expected 'WHERE (%s) AND r.enabled = 1 ORDER BY r.user_id, r.device_id, r.id')"$'\n'
if grep -q 'qSelectAllRulesForAdminPaged = `SELECT .*LIMIT \$1 OFFSET \$2' "$QUERIES"; then
  A_MISS="${A_MISS}qSelectAllRulesForAdminPaged still windows RULE ROWS (LIMIT/OFFSET on the row query)"$'\n'
fi
# The predicate builder: an OR-chain over the pair, with the portable $N placeholders.
grep -q 'func adminRulesForGroupsQuery(' "$DB" \
  || A_MISS="${A_MISS}adminRulesForGroupsQuery does not exist — the page has no way to fetch whole groups"$'\n'
grep -q 'PlaceholderAt(total, i\*2)' "$DB" \
  || A_MISS="${A_MISS}the group predicate does not use PlaceholderAt (typed \$N by hand breaks on one dialect)"$'\n'
grep -q 'strings.Join(parts, " OR ")' "$DB" \
  || A_MISS="${A_MISS}the group predicate is not an OR-chain — a single group would fetch every row of the table"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: the window is a (user_id, device_id) GROUP window with its own COUNT, and the row fetch is scoped to exactly those groups"
else
  bad "A1: the group window is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# The ORDER of the statements inside db.GetAllRulesForAdminPaged is the contract
# that makes a group unsplittable: read the groups of THIS page first, then fetch the
# rows of those groups — one query for all of them, never one query per group.
GBODY="$(awk '/^func GetAllRulesForAdminPaged\(/,/^}/' "$DB")"
GROUPS_AT="$(printf '%s\n' "$GBODY" | grep -n 'adminRuleGroupsPage(' | head -1 | cut -d: -f1)"
ROWS_AT="$(printf '%s\n' "$GBODY" | grep -n 'adminRulesForGroupsQuery(' | head -1 | cut -d: -f1)"
ROW_QUERIES="$(printf '%s\n' "$GBODY" | grep -c 'd\.Query(' || true)"
if [ -n "$GROUPS_AT" ] && [ -n "$ROWS_AT" ] \
   && [ "${GROUPS_AT:-0}" -lt "${ROWS_AT:-0}" ] \
   && [ "${ROW_QUERIES:-0}" -eq 1 ] \
   && grep -q 'LIMIT \$1 OFFSET \$2' "$QUERIES"; then
  ok "A2: the page reads its GROUPS first and then fetches those groups' rows in ONE query (a group cannot be split, and the cost does not grow per group)"
else
  bad "A2: the group-then-rows order is not pinned (groups@$GROUPS_AT rows@$ROWS_AT row-queries=$ROW_QUERIES) — a per-group or row-window fetch reintroduces the split"
fi

# The default window: 20 groups, chosen so the measured live shape (3 groups) is one
# page. The handler must take the default from db, not from a literal of its own.
if grep -q 'AdminGroupsPerPage = 20' "$DB" && grep -q 'db\.AdminGroupsPerPage' "$FORM"; then
  ok "A3: db.AdminGroupsPerPage = 20 is the single source of the admin window, and the handler defaults through it"
else
  bad "A3: the 20-group default is missing or the handler hardcodes its own (the operator's 3-group tailnet must render ONE page)"
fi

# --- B: the counters are the database's ---------------------------------------
B_MISS=""
grep -q 'qSelectAllRulesForAdminCount' "$DB" \
  || B_MISS="${B_MISS}db.GetAllRulesForAdminPaged does not read the rule COUNT"$'\n'
grep -q 'adminRulePage\.TotalRules' "$FORM" \
  || B_MISS="${B_MISS}the handler does not take realTotal from the window's rule COUNT (a page window dressed as a total is the B351 bug)"$'\n'
if grep -qE 'realTotal = adminRulePage\.Total([^R]|$)' "$FORM"; then
  B_MISS="${B_MISS}the handler still reads the removed row-window Total as realTotal"$'\n'
fi
grep -q 'const qSelectAllRulesForAdminCount = `SELECT COUNT(\*) FROM device_rules WHERE enabled = 1`' "$QUERIES" \
  || B_MISS="${B_MISS}the rule COUNT still ignores enabled = 1 — it disagrees with every other counter as soon as a rule is disabled"$'\n'
grep -q '"ShownRules": len(rr)' "$FORM" \
  || B_MISS="${B_MISS}ShownRules is no longer the rendered row count"$'\n'
grep -q 'exit_rules_admin.shown_of_total' "$TPL" \
  || B_MISS="${B_MISS}the page no longer says how many of the table's rules it is showing"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: realTotal (heading + system load) is the unpaginated enabled-rule COUNT, ShownRules is the rendered rows, and the page says which is which"
else
  bad "B1: a counter is still derived from the page window:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# --- C: whole groups, oversized groups collapsed -------------------------------
C_MISS=""
grep -q 'divceil \.RulePage\.TotalGroups \.RulePage\.PageSize' "$TPL" \
  || C_MISS="${C_MISS}the page counter does not divide GROUPS by the group window"$'\n'
grep -q 'adminGroupIsOversized(dg\.Count)' "$FORM" \
  || C_MISS="${C_MISS}the handler never marks an oversized group"$'\n'
grep -q 'func adminGroupIsOversized' "$FORM" \
  || C_MISS="${C_MISS}adminGroupIsOversized does not exist"$'\n'
grep -q 'AdminOversizedGroupRows = 150' "$DB" \
  || C_MISS="${C_MISS}the oversized threshold is not 150"$'\n'
grep -q '{{if not \$devData\.Oversized}} open{{end}}' "$TPL" \
  || C_MISS="${C_MISS}the template does not render an oversized group COLLAPSED (and a normal one open)"$'\n'
grep -q 'exit_rules_admin.group_oversized_hint' "$TPL" \
  || C_MISS="${C_MISS}the collapsed group carries no hint, so it reads as an empty device"$'\n'
# Presentation only: the oversized decision must not add a query of its own.
grep -q 'for _, rule := range rr' "$FORM" \
  || C_MISS="${C_MISS}the oversized flag is not computed from the already-fetched rows"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: a group is rendered whole, an oversized one (> 150 rows) starts collapsed with a hint, and the flag costs no query"
else
  bad "C1: the oversized-group presentation is incomplete:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# --- D: i18n (RU + EN) ---------------------------------------------------------
D_MISS=""
for key in pagination_page_of_groups pagination_total_groups group_oversized_hint; do
  n=$(grep -c "\"exit_rules_admin\.$key\"" "$CAT" 2>/dev/null || echo 0)
  [ "${n:-0}" -ge 2 ] || D_MISS="${D_MISS}exit_rules_admin.$key is defined ${n} time(s), want 2 (RU + EN — the parity test compares key sets)"$'\n'
done
grep -q '"exit_rules_admin\.pagination_page_of_groups":[[:space:]]*"Страница %d из %d (по устройствам)"' "$CAT" \
  || D_MISS="${D_MISS}the RU pagination line does not name the group unit (по устройствам)"$'\n'
grep -q '"exit_rules_admin\.pagination_total_groups":[[:space:]]*"всего %d устройств / %d пользователей"' "$CAT" \
  || D_MISS="${D_MISS}the RU totals line does not carry both numbers (devices / users)"$'\n'
grep -q '"exit_rules_admin\.pagination_page_of_groups":[[:space:]]*"Page %d of %d (by device)"' "$CAT" \
  || D_MISS="${D_MISS}the EN pagination line does not name the group unit (by device)"$'\n'
grep -q '"exit_rules_admin\.pagination_total_groups":[[:space:]]*"total %d devices / %d users"' "$CAT" \
  || D_MISS="${D_MISS}the EN totals line does not carry both numbers"$'\n'
grep -q 'exit_rules_admin\.pagination_page_of_groups' "$TPL" \
  || D_MISS="${D_MISS}the template does not use the new pagination key"$'\n'
grep -q 'exit_rules_admin\.pagination_total_groups' "$TPL" \
  || D_MISS="${D_MISS}the template does not use the new totals key"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: the new pagination/totals/collapsed-group keys exist in RU and EN and are the ones the template renders"
else
  bad "D1: the new wording is not fully localized:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- E: the ?device= drill-down is untouched -----------------------------------
E_MISS=""
grep -q 'db\.GetAllRulesForAdminByDevice(s\.dbc(), deviceFilter)' "$FORM" \
  || E_MISS="${E_MISS}the drill-down no longer uses the per-device query"$'\n'
FILTER_AT="$(line_of 'db\.GetAllRulesForAdminByDevice(s\.dbc(), deviceFilter)' "$FORM")"
WINDOW_AT="$(line_of 'db\.GetAllRulesForAdminPaged(s\.dbc()' "$FORM")"
if [ "${FILTER_AT:-0}" -eq 0 ] || [ "${WINDOW_AT:-0}" -eq 0 ] || [ "${FILTER_AT:-0}" -ge "${WINDOW_AT:-0}" ]; then
  E_MISS="${E_MISS}the device-filter branch no longer runs BEFORE the group window (filter@$FILTER_AT window@$WINDOW_AT) — a filtered page could be paginated by group"$'\n'
fi
grep -q 'const qSelectAllRulesForAdminByDevice = ' "$QUERIES" \
  || E_MISS="${E_MISS}qSelectAllRulesForAdminByDevice is gone"$'\n'
if grep -q 'qSelectAllRulesForAdminByDevice = `SELECT .*LIMIT' "$QUERIES"; then
  E_MISS="${E_MISS}the per-device drill-down grew a LIMIT — one device is one whole group and must stay unpaged"$'\n'
fi
if [ -z "$E_MISS" ]; then
  ok "E1: the ?device= drill-down still takes its own unpaged path (one device = one whole group, B348/B349 preserved)"
else
  bad "E1: the device-filter path changed:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# --- F: bookkeeping + the behavioural pins -------------------------------------
F_MISS=""
git ls-files --error-unmatch scripts/check_b358_group_pagination.sh >/dev/null 2>&1 \
  || F_MISS="${F_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
grep -q 'check_b358_group_pagination.sh' scripts/verify_pre_deploy.sh \
  || F_MISS="${F_MISS}verify_pre_deploy.sh does not register B358 — the contract would never run"$'\n'
grep -q '\*\*B358\*\*' AGENTS.md \
  || F_MISS="${F_MISS}AGENTS.md's block index has no B358 entry (AGENTS rule 2)"$'\n'
grep -q 'func TestAdminRuleGroupsPaged_WholeGroupsAndDatabaseTotals_B358' internal/db/device_rules_b358_test.go 2>/dev/null \
  || F_MISS="${F_MISS}no data-layer test pins whole groups + database totals"$'\n'
grep -q 'func TestAdminRuleGroupsPaged_LiveShapeIsOnePage_B358' internal/db/device_rules_b358_test.go 2>/dev/null \
  || F_MISS="${F_MISS}no test pins the operator's case (2 users / 3 devices / 393 rules = ONE page)"$'\n'
grep -q 'func TestAdminWindowFromQuery_B358' internal/feature/exit_rules/form_admin_b358_test.go 2>/dev/null \
  || F_MISS="${F_MISS}no test pins the handler's window defaults"$'\n'
if [ -z "$F_MISS" ]; then
  ok "F1: tracked, registered, indexed, and the window is pinned by behavioural tests on both layers"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

# The B351 C3 renegotiation must have happened, in place, with its reason recorded —
# otherwise the old row-based assertion is either still there (and green, which is
# worse) or was deleted without a trace.
if grep -q 'RENEGOTIATED IN PLACE by B358' scripts/check_b351_rule_owner_and_counts.sh \
   && grep -qF 'qSelectAdminRuleGroups = .*GROUP BY r\.user_id, r\.device_id' scripts/check_b351_rule_owner_and_counts.sh; then
  ok "F2: B351 C3 was renegotiated in place to assert the GROUP window, with the reason recorded where the old assertion was"
else
  bad "F2: B351 C3 still pins the row-based window (or was removed without recording why)"
fi

printf '\n\033[1mB358 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
