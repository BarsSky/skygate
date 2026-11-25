#!/usr/bin/env bash
# check_b516_rule_group_page.sh
#
# 2026-10-30 (B516) — «ГРУППА — ЕДИНИЦА СТРАНИЦЫ» на /my/exit-rules.
#
# OPERATOR REPORT (verbatim):
#
#   «сейчас таблица существующих правил никак не группируется визуально и те
#    правила что создавались как в группе на самом деле отображаются в разнобой.
#    Страницы не удобно перелистывать потому что необходимо опускаться в самый низ
#    таблицы и всегда переходить только на следующую … Также отдельно по подсетям
#    что добавляются вместе с доменами — когда происходит такое добавление то потом
#    не понятно к какому домену относилась та или иная подсеть»
#
# MEASURED in the code, not assumed:
#
#   * the page windowed RULE ROWS (`GetDeviceRulesForUserPaged`, 50 per page) and
#     then built EVERY group view from that window — so a group was torn across
#     pages and a group's header count was the PAGE's count. The admin page had
#     already moved its window onto groups (B358); the user page never did;
#   * the page had FOUR projections of the same rows (by-target, by-device,
#     by-label, CDN-inside-device) with different nesting, so the same rule could
#     appear in two of them;
#   * `parent_domain` — the domain a resolved row came from — was rendered as a
#     small rotating icon whose tooltip was hardcoded English, so the subnet
#     question had no answer on the page even though the DATABASE has carried the
#     answer since v0.22;
#   * pagination was prev/next only, at the BOTTOM, with JS-built hrefs.
#
# THE CHANGE (this block is the model + the page; the default flip and the on/off
# switch are separate, reviewed changes):
#
#   * ONE pure model in internal/feature/exit_rules/rules_view_b516.go turns the
#     flat rows into GROUPS under a LENS (my groups / source / device / relay),
#     windows GROUPS into pages, and cuts the page numbers — so both pages and all
#     lenses share one definition of what a group is and how big a page is;
#   * the page renders a LIST of groups and ONE group's rows behind a link, with
#     the pager ABOVE and BELOW, honest unpaged counts, and a row table that names
#     each row's SOURCE (the domain it was resolved from);
#   * the four old projections stay the default until the new page is verified
#     live and their contracts are renegotiated in their own change — a rewrite
#     that removes a working screen in the same step that adds another is how a
#     page regresses while its tests stay green.
#
# CONTRACTS
#   A. the group model exists, is pure, and merges a domain with its resolved rows
#   B. a page is a page of GROUPS, and a group's count is the whole set's count
#   C. the page renders the list / the group / the pager above AND below
#   D. every new visible string is localized RU+EN (including Go-held keys)
#   E. the old views still render when the lens is not requested (migration safety)
#   F. bookkeeping: tracked by git, registered in the catalog, indexed in AGENTS.md,
#      and the properties are pinned by behavioural Go tests

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B516: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MODEL=internal/feature/exit_rules/rules_view_b516.go
VIEW=internal/feature/exit_rules/form_my_lens_b516.go
HANDLER=internal/feature/exit_rules/form_my_rules.go
TPL=internal/handlers/templates/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go
RENDER=internal/handlers/exit_rules_groups_render_b516_test.go
MODELTEST=internal/feature/exit_rules/rules_view_b516_test.go
I18NTEST=internal/feature/exit_rules/rules_view_b516_i18n_test.go

hdr "B516 — the rules page is a page of GROUPS, not of rule rows"

# --- A: the model exists, is pure, and merges a source with its rows -----------
A_MISS=""
for fn in 'func BuildRuleGroups(' 'func WindowRuleGroups(' 'func FindRuleGroup(' 'func LensPageNumbers(' 'func RuleLensDefault(' 'func ParseRuleLens(' 'func WindowRuleGroupRows('; do
  grep -qF "$fn" "$MODEL" || A_MISS="${A_MISS}${fn%?} is missing — the page has no group model"$'\n'
done
# Purity: the model may not touch the database, the clock or the request.
if grep -qE 'database/sql|net/http|time\.(Now|Since)|db\.' "$MODEL"; then
  A_MISS="${A_MISS}the group model imports the DB, HTTP or the clock — its tests would stop being a fixture"$'\n'
fi
# THE live-report regression: a domain row, its resolved /32 rows and its CDN range
# set are ONE card. Both parent_domain spellings must land on the same key, and a
# plain domain rule must land there too.
grep -q 'func ruleSourceKey(' "$MODEL" \
  || A_MISS="${A_MISS}ruleSourceKey is missing — nothing resolves the provenance of a subnet"$'\n'
grep -q 'ParseCDNGroupMarker(parent)' "$MODEL" \
  || A_MISS="${A_MISS}the source key does not parse the cdn:<provider>:<domain> marker — CDN ranges would form a card of their own"$'\n'
grep -q 'return "dom:" + strings.ToLower(domain), GroupKindCDN, domain, cdn, true' "$MODEL" \
  || A_MISS="${A_MISS}a CDN marker does not share the dom:<domain> key with the domain row it came from"$'\n'
grep -q 'return "dom:" + strings.ToLower(parent), GroupKindDomain, parent, "", true' "$MODEL" \
  || A_MISS="${A_MISS}a resolved /32 does not key on its parent domain"$'\n'
grep -q 'EqualFold(strings.TrimSpace(r.TargetType), "domain")' "$MODEL" \
  || A_MISS="${A_MISS}a domain rule typed by the operator does not join the same card as its resolved rows"$'\n'
# The label is the operator's own taxonomy and must win in ITS lens (B380 applied
# to the list) — but only in that lens.
grep -q 'if lens == LensGroups {' "$MODEL" \
  || A_MISS="${A_MISS}the group lens has no label precedence branch (the operator's own names must be the first cards)"$'\n'
grep -q 'groupLabelKey(label), GroupKindLabel' "$MODEL" \
  || A_MISS="${A_MISS}the label card key is not the B380 folded key — «Игры» and «игры» would split"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: the pure group model exists and one card holds a domain, its /32s and its CDN ranges"
else
  bad "A1: the group model is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# The lens set is fixed and every lens has a label key.
A2_MISS=""
grep -q 'var ruleLensOrder = \[\]RuleLens{LensGroups, LensSource, LensDevice, LensRelay}' "$MODEL" \
  || A2_MISS="${A2_MISS}ruleLensOrder is not the four lenses (groups, source, device, relay)"$'\n'
grep -q 'func RuleLensOrder()' "$MODEL" \
  || A2_MISS="${A2_MISS}RuleLensOrder() is missing — the selector would read the package slice directly"$'\n'
grep -q 'func RuleLensDefault(' "$MODEL" \
  || A2_MISS="${A2_MISS}RuleLensDefault() is missing"$'\n'
grep -q 'return LensGroups' "$MODEL" || A2_MISS="${A2_MISS}the default never chooses the operator's own groups"$'\n'
grep -q 'return LensSource' "$MODEL" || A2_MISS="${A2_MISS}the default never falls back to the source lens"$'\n'
if [ -z "$A2_MISS" ]; then
  ok "A2: four lenses, and the page opens on the operator's own groups when they exist, else on the source"
else
  bad "A2: the lens set is incomplete:"
  printf '%s' "$A2_MISS" | sed 's/^/       /' >&2
fi

# --- B: a page is a page of GROUPS, and the counts are the whole set's ---------
B_MISS=""
# The window slices the GROUP slice — the row slice inside a group is only cut by
# WindowRuleGroupRows, i.e. behind a link, never inside the list.
grep -q 'pageGroups = groups\[from-1 : to\]' "$MODEL" \
  || B_MISS="${B_MISS}WindowRuleGroups does not window the GROUP slice (a row window is what tore a group apart)"$'\n'
grep -q 'out.TotalRules += g.Count' "$MODEL" \
  || B_MISS="${B_MISS}the page summary does not sum the WHOLE group set"$'\n'
# The handler must read the rows UNPAGED for the lens path, and only there.
grep -q 'db.GetDeviceRulesForUser(s.dbc(), c.UserID)' "$HANDLER" \
  || B_MISS="${B_MISS}the handler never loads the unpaged rule set — a page of rows cannot count a group"$'\n'
grep -q 'if myRulesLensEnabled(r.URL.Query())' "$HANDLER" \
  || B_MISS="${B_MISS}the unpaged load is not gated on the lens request (the default page must keep its current cost)"$'\n'
if grep -q 'GetDeviceRulesForUserPaged(s.dbc(), c.UserID' "$VIEW"; then
  B_MISS="${B_MISS}the lens view reads the PAGED rule set — the group would be cut by the row window"$'\n'
fi
# A group's own page is a page of ROWS behind a link, with its own arithmetic.
grep -q 'func WindowRuleGroupRows(' "$MODEL" \
  || B_MISS="${B_MISS}WindowRuleGroupRows is missing — a 200-row group would render whole inside the list"$'\n'
grep -q 'pageGroup.Rules = rows' "$MODEL" \
  || B_MISS="${B_MISS}the group page does not cut its rows"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: the window unit is the GROUP, the summary counts the whole set, and rows are cut only inside one group"
else
  bad "B1: the windowing contract is broken:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# The empty/oversized edges: a stale ?group= key must fall back, and a page number
# past the end must clamp instead of rendering a blank page.
B2_MISS=""
grep -q 'if g, found := FindRuleGroup(groups, groupParam); found {' "$VIEW" \
  || B2_MISS="${B2_MISS}a stale group key is not resolved through FindRuleGroup (it would render an empty card)"$'\n'
grep -q 'page = totalPages' "$MODEL" \
  || B2_MISS="${B2_MISS}a page past the end is not clamped (the operator lands on a blank page after deleting the last group)"$'\n'
grep -q 'if total == 0 {' "$MODEL" \
  || B2_MISS="${B2_MISS}the empty case is not handled (From/To would read 1..0)"$'\n'
if [ -z "$B2_MISS" ]; then
  ok "B2: an empty set, a stale group key and a page past the end all land somewhere readable"
else
  bad "B2: the edge cases are not handled:"
  printf '%s' "$B2_MISS" | sed 's/^/       /' >&2
fi

# --- C: the page renders the list, the group, and the pager above AND below ----
C_MISS=""
grep -q '{{if \$lv.Mode}}' "$TPL" \
  || C_MISS="${C_MISS}the template has no group-page branch"$'\n'
grep -q '{{if .Rules}}{{if not .RulesLens.Mode}}' "$TPL" \
  || C_MISS="${C_MISS}the old four views are not gated against the group page — two lists of the same rules would render"$'\n'
grep -q 'id="rules-lens-bar"' "$TPL" \
  || C_MISS="${C_MISS}the grouping selector is missing"$'\n'
grep -q 'id="rules-list"' "$TPL" \
  || C_MISS="${C_MISS}the group list table is missing"$'\n'
grep -q 'id="rules-pager-top"' "$TPL" \
  || C_MISS="${C_MISS}the pager is not ABOVE the table — the operator must not have to scroll to the bottom to change page"$'\n'
grep -q 'id="rules-pager-bottom"' "$TPL" \
  || C_MISS="${C_MISS}the pager is not BELOW the table"$'\n'
grep -q '{{define "rules-pager"}}' "$TPL" \
  || C_MISS="${C_MISS}the pager is not a shared define (the two positions would drift)"$'\n'
grep -q '{{range \$p := .Pages}}' "$TPL" \
  || C_MISS="${C_MISS}the pager renders no page NUMBERS (prev/next only is the report)"$'\n'
grep -q 'aria-current="page"' "$TPL" \
  || C_MISS="${C_MISS}the current page is not marked for a screen reader"$'\n'
if grep -q 'href="#"' "$TPL"; then
  # The OLD pagination block is expected to keep its js-built hrefs until it is
  # retired; the NEW pager must not, so this is checked as "the new define has no
  # href=#" rather than "the file has none".
  if awk '/\{\{define "rules-pager"\}\}/,/^\{\{end\}\}$/' "$TPL" | grep -q 'href="#"'; then
    C_MISS="${C_MISS}the new pager still builds its hrefs in JavaScript (a no-JS reader gets a dead link)"$'\n'
  fi
fi
grep -q 'exit_rules.rows_title' "$TPL" \
  || C_MISS="${C_MISS}the group page has no row table"$'\n'
grep -q 'exit_rules.col_source' "$TPL" \
  || C_MISS="${C_MISS}the row table does not name each row's SOURCE — the subnet question would stay unanswered"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: list, one group, and a numbered pager above AND below, all server-rendered"
else
  bad "C1: the page does not render the group view:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# The source chip must never print a raw CDN marker: that is a storage format, and
# the OLD admin table printed it verbatim.
C2_MISS=""
grep -q 'func (r RuleRow) SourceLabel() string' "$MODEL" \
  || C2_MISS="${C2_MISS}RuleRow.SourceLabel() is missing"$'\n'
grep -q 'if _, domain, ok := ParseCDNGroupMarker(r.ParentDomain); ok {' "$MODEL" \
  || C2_MISS="${C2_MISS}SourceLabel does not strip the cdn:<provider>: prefix"$'\n'
if grep -q 'cdn:{{' "$TPL"; then
  C2_MISS="${C2_MISS}the template prints a raw cdn: marker"$'\n'
fi
if [ -z "$C2_MISS" ]; then
  ok "C2: the provenance chip shows the domain, never the cdn: storage marker"
else
  bad "C2: the provenance chip is not human-readable:"
  printf '%s' "$C2_MISS" | sed 's/^/       /' >&2
fi

# --- D: i18n, including the keys held in Go ------------------------------------
D_MISS=""
for key in lens_label lens_groups lens_source lens_device lens_relay lens_empty \
           lens_summary lens_summary_off kind_label kind_domain kind_cdn kind_device \
           kind_relay kind_target group_open group_back group_subnets group_devices \
           group_off_all group_off_some group_all_devices group_cdn group_relay \
           group_relay_auto pager_group_range pager_row_range pager_first pager_last \
           pager_prev pager_next pager_per pager_page_of col_source source_manual \
           row_on row_off rows_title; do
  n=$(grep -c "\"exit_rules\.$key\"[[:space:]]*:" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -eq 2 ] || D_MISS="${D_MISS}exit_rules.$key is defined ${n:-0} time(s), want exactly 2 (RU + EN — a third copy shadows one silently)"$'\n'
done
# The chips are rendered from GO constants, so the ratchet that scans templates
# cannot see them: the keys must resolve, and a Cyrillic check keeps the RU side
# Russian. Both live in the Go test below; here we assert the keys it walks exist.
grep -q 'func RuleKindKeys()' "$MODEL" || D_MISS="${D_MISS}RuleKindKeys() is missing — the kind chips have no key list to test"$'\n'
grep -q 'func RuleLensKeys()' "$MODEL" || D_MISS="${D_MISS}RuleLensKeys() is missing"$'\n'
grep -q 'func groupKindKey(kind string) string' "$MODEL" || D_MISS="${D_MISS}groupKindKey is missing"$'\n'
grep -q 'func lensLabelKey(l RuleLens) string' "$MODEL" || D_MISS="${D_MISS}lensLabelKey is missing"$'\n'
# The template must use the key from the model, not a literal it invented.
grep -q '{{t \$g.KindKey}}' "$TPL" || D_MISS="${D_MISS}the group card does not render its kind through KindKey"$'\n'
grep -q '{{t \$l.LabelKey}}' "$TPL" || D_MISS="${D_MISS}the lens selector does not render its label through LabelKey"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: every new key exists exactly twice (RU+EN), and the Go-held chip keys are enumerable for the i18n test"
else
  bad "D1: the new wording is not fully localized:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# RU must not carry English: sampled on the three chips a card cannot avoid.
D2_MISS=""
if ! grep -q '"exit_rules\.kind_domain":[[:space:]]*"домен"' "$CAT"; then
  D2_MISS="${D2_MISS}the RU kind_domain value is not «домен»"$'\n'
fi
if ! grep -q '"exit_rules\.lens_source":[[:space:]]*"По источнику"' "$CAT"; then
  D2_MISS="${D2_MISS}the RU lens_source value is not «По источнику»"$'\n'
fi
if ! grep -q '"exit_rules\.pager_per":[[:space:]]*"на страницу"' "$CAT"; then
  D2_MISS="${D2_MISS}the RU pager_per value is not «на страницу»"$'\n'
fi
if [ -z "$D2_MISS" ]; then
  ok "D2: the RU catalogue carries Russian for the chips and the pager, not English"
else
  bad "D2: an RU value is not translated:"
  printf '%s' "$D2_MISS" | sed 's/^/       /' >&2
fi

# --- E: migration safety — the old page is untouched without the lens ----------
E_MISS=""
grep -q 'myRulesLensEnabled' "$HANDLER" \
  || E_MISS="${E_MISS}the handler has no switch for the new page"$'\n'
# The old four views' own markers must still be in the template (they are what the
# existing contracts assert) — the new block ADDS, it does not replace yet.
for marker in 'id="view-target-link"' 'id="view-device-link"' 'id="view-group-link"' 'id="rules-view"'; do
  grep -qF "$marker" "$TPL" || E_MISS="${E_MISS}the old view marker $marker disappeared — the default page would break before its contracts are renegotiated"$'\n'
done
if [ -z "$E_MISS" ]; then
  ok "E1: the new page is opt-in (?lens= / ?group=) and the current page still renders unchanged"
else
  bad "E1: the migration safety net is missing:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# --- F: bookkeeping + the behavioural pins -------------------------------------
F_MISS=""
git ls-files --error-unmatch scripts/check_b516_rule_group_page.sh >/dev/null 2>&1 \
  || F_MISS="${F_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
for f in "$MODEL" "$VIEW" "$RENDER" "$MODELTEST" "$I18NTEST"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 \
    || F_MISS="${F_MISS}$f is NOT tracked by git"$'\n'
done
grep -q 'check_b516_rule_group_page.sh' scripts/verify_pre_deploy.sh \
  || F_MISS="${F_MISS}verify_pre_deploy.sh does not register B516 — the contract would never run"$'\n'
grep -q '\*\*B516\*\*' AGENTS.md \
  || F_MISS="${F_MISS}AGENTS.md's block index has no B516 entry (AGENTS rule 2)"$'\n'
for t in TestB516_DomainAndItsResolvedRowsAreOneGroup TestB516_PageIsAPageOfGroups \
         TestB516_GroupCountIsTheDatabaseCount TestB516_DeterministicOrder TestB516_PageNumbers; do
  grep -q "func $t(" "$MODELTEST" || F_MISS="${F_MISS}the model test $t is missing"$'\n'
done
for t in TestB516_GroupPageRendersTheList TestB516_GroupPageRendersOneGroup \
         TestB516_StaleGroupKeyFallsBack TestB516_OldViewsStillRenderWithoutTheLensParam; do
  grep -q "func $t(" "$RENDER" || F_MISS="${F_MISS}the render test $t is missing"$'\n'
done
grep -q 'func TestB516_KindAndLensKeysResolveInBothLanguages(' "$I18NTEST" \
  || F_MISS="${F_MISS}no test asserts the Go-held chip keys resolve in both catalogues"$'\n'
if [ -z "$F_MISS" ]; then
  ok "F1: tracked, registered, indexed, and pinned by model, render and i18n tests"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB516 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
