#!/usr/bin/env bash
# check_b523_admin_group_page.sh
#
# B523 (2026-11-25) — THE ADMIN RULES PAGE GETS THE USER PAGE'S CARD MODEL.
#
# Operator, after B516-B521 shipped: «на странице по правилам у администратора тоже бы
# применить такого же рода изменения по визуалу». The admin page was the last one still
# rendering the pre-B516 shape: a sticky toolbar over a «пользователь → устройство →
# релей → правила» tree, with its own prev/next pager. The user page had become a page
# of GROUPS with a lens selector, numbered pagers above and below, and provenance on
# every row.
#
# The block adds NO second grouping implementation: AdminRule is a presentation type
# (PreferredHost, ApprovedInHeadscale, ServedBy), so the page projects its rows onto the
# shared RuleRow and calls the SAME BuildRulesLensView the user page calls — with
# AdminRuleLensOrder() (the same four lenses plus «по пользователю», the one question
# the tree answered that a lens can answer too). Two pages, one model: they cannot
# disagree about what a group is, how many rules it holds, or which page it is on.
#
# WHY THE OWNER PIVOT EXISTS AT ALL: the admin page spans users, and the tree's value
# was «что настроил вот этот человек». Dropping the tree without that lens would have
# removed a capability while the operator was asking for a VISUAL change — the class of
# silent removal this project keeps paying for. The tree itself stays reachable:
# `?view=target|device`, exactly as the user page keeps its four pre-B516 projections.
#
# CONTRACTS
#   A  the owner lens exists in the shared model and is offered ONLY on the admin page
#   B  the admin handler decides the page in ONE place and projects onto the shared model
#   C  the template renders the card page (selector, list, both pagers, owner column,
#      group page) and NOTHING renders the tree at the same time
#   D  the new strings exist in BOTH catalogues (a key held in Go is invisible to the
#      shell ratchet that scans templates)
#   E  the Go tests run, and the shared builder is the one both pages call
#   F  bookkeeping (tracked files, registration, AGENTS entry)
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B523: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
has()  { grep -qF -- "$2" "$1" 2>/dev/null; }

MODEL=internal/feature/exit_rules/rules_view_b516.go
VIEW=internal/feature/exit_rules/form_my_lens_b516.go
ADMIN=internal/feature/exit_rules/form_admin.go
ROW=internal/feature/exit_rules/cdn_group.go
TPL=internal/handlers/templates/admin/exit_rules.html
TEST=internal/feature/exit_rules/admin_lens_b523_test.go
RTEST=internal/handlers/exit_rules_admin_lens_b523_test.go
RU=internal/i18n/catalog_exit_rules.go

hdr "B523 — the admin rules page is a page of groups"

# --- A. the owner lens, in the shared model, admin-only ---------------------------
A=""
has "$MODEL" 'LensUser RuleLens = "user"' \
  || A="${A}the shared model has no owner lens"$'\n'
has "$MODEL" 'func AdminRuleLensOrder() []RuleLens' \
  || A="${A}there is no separate admin lens order"$'\n'
has "$MODEL" 'var adminRuleLensOrder = []RuleLens{LensGroups, LensSource, LensDevice, LensUser, LensRelay}' \
  || A="${A}the admin order is not «the shared four + the owner pivot»"$'\n'
has "$MODEL" 'GroupKindUser   = "user"' \
  || A="${A}there is no owner group kind (the card would render with a destination chip)"$'\n'
has "$MODEL" 'case "user", "users", "owner", "owners":' \
  || A="${A}ParseRuleLens does not accept the owner spellings"$'\n'
has "$ROW" 'UserName string' \
  || A="${A}RuleRow has no UserName, so the owner lens cannot group"$'\n'
# The user page must NOT offer it: a control that cannot change the answer.
if has "$MODEL" 'var ruleLensOrder = []RuleLens{LensGroups, LensSource, LensDevice, LensRelay}'; then
  :
else
  A="${A}the user page's lens order changed — the owner pivot belongs to the admin page"$'\n'
fi
if [ -z "$A" ]; then
  ok "A1: one owner lens in the shared model, offered by the admin page only"
else
  bad "A1: the owner lens is incomplete or leaked into the user page:"
  printf '%s' "$A" | sed 's/^/       /' >&2
fi

# --- B. the handler: one decision point + a projection ---------------------------
B=""
has "$ADMIN" 'func adminRulesLensEnabled(q url.Values) bool' \
  || B="${B}the admin page has no single «which page» predicate"$'\n'
has "$ADMIN" 'func adminLensRowsFrom(rr []AdminRule) []RuleRow' \
  || B="${B}there is no AdminRule → RuleRow projection (a second grouping model would drift)"$'\n'
has "$ADMIN" 'BuildRulesLensViewFor(' \
  || B="${B}the admin page does not use the shared builder"$'\n'
has "$ADMIN" 'AdminRuleLensOrder()' \
  || B="${B}the admin page does not pass its own lens order"$'\n'
has "$ADMIN" '"AdminLens": adminLens' \
  || B="${B}the page does not hand the lens view to the template"$'\n'
# The projection must carry the fields the model reads; a dropped GroupLabel would
# render every rule as «без группы» with no error anywhere.
for f in 'GroupLabel:   a.GroupLabel' 'UserName:     a.UserName' 'Enabled:      a.Enabled'; do
  has "$ADMIN" "$f" || B="${B}the projection drops “${f%%:*}”"$'\n'
done
if [ -z "$B" ]; then
  ok "B1: one predicate decides the admin page, and its rows are projected onto the shared model"
else
  bad "B1: the admin handler does not share the model:"
  printf '%s' "$B" | sed 's/^/       /' >&2
fi

# --- C. the template -------------------------------------------------------------
C=""
for id in 'admin-rules-groups' 'admin-rules-lens-bar' 'admin-rules-pager-top' 'admin-rules-pager-bottom' \
          'admin-rules-list' 'admin-group-rows' 'admin-rules-back' 'admin-rules-flash'; do
  has "$TPL" "$id" || C="${C}the card page has no ${id}"$'\n'
done
has "$TPL" 'exit_rules_admin.col_user' || C="${C}the card page has no owner column"$'\n'
has "$TPL" '{{if .AdminLens.Mode}}' || C="${C}the card page is not gated by the lens view"$'\n'
has "$TPL" '{{if not .AdminLens.Mode}}' || C="${C}the tree is not gated by the lens view — both would render together"$'\n'
has "$TPL" '{{template "rules-pager" $d.Pager}}' || C="${C}the group page pager is not the shared numbered define"$'\n'
# The LIST pager must be the DATABASE one (B358 windows groups in SQL), not the lens
# window's: a pager built from the window's groups says «1 из 1» on a table with more
# groups than a window and makes page 2 unreachable.
has "$TPL" '{{with .AdminPager}}{{template "rules-pager" .}}{{end}}' || C="${C}the group list pages by the lens window instead of by the database (B358) — page 2 becomes unreachable"$'\n'
has "$TPL" 'exit_rules_admin.pagination_page_of_groups' || C="${C}the list pager does not print the database window arithmetic"$'\n'
has "$TPL" 'exit_rules_admin.lens_page_summary' || C="${C}the counters do not say they describe the current window"$'\n'
has "$TPL" 'action="/admin/exit-rules/toggle"' || C="${C}the group page has no per-row switch"$'\n'
# The bulk-relay form (B471) takes its inputs from the TREE rows
# (`form="bulk-relay-form"`). Rendering it on the card page would be a button that
# submits an empty selection; it must be gated WITH the tree, and the card page must
# point at the tree instead.
has "$TPL" '{{if not .AdminLens.Mode}}' || C="${C}the bulk-relay card is not gated with the tree (a dead control)"$'\n'
has "$TPL" 'admin-bulk-relay-card' || C="${C}the bulk-relay card is gone entirely — it must stay with the ?view= page"$'\n'
has "$TPL" 'admin-rules-bulk-relay-hint' || C="${C}the card page does not point at the tree for the batch move"$'\n'
has "$TPL" 'exit_rules_admin.bulk_relay_hint_link' || C="${C}the pointer to the batch move has no label key"$'\n'
if [ -z "$C" ]; then
  ok "C1: the admin template renders the group list and one group page, with the tree on the other side of the gate"
else
  bad "C1: the admin card page is incomplete:"
  printf '%s' "$C" | sed 's/^/       /' >&2
fi

# --- D. i18n: the new strings, in BOTH catalogues --------------------------------
D=""
for k in 'exit_rules.lens_user' 'exit_rules.kind_user' 'exit_rules_admin.col_user' \
         'exit_rules_admin.col_switch' 'exit_rules_admin.groups_title'; do
  n=$(grep -c -- "\"$k\"" "$RU" 2>/dev/null || true)
  if [ "${n:-0}" -lt 2 ]; then
    D="${D}$k appears ${n:-0} time(s) in catalog_exit_rules.go, want RU+EN (2)"$'\n'
  fi
done
if [ -z "$D" ]; then
  ok "D1: every new string has an RU and an EN entry"
else
  bad "D1: a string would fall back to a raw key in one language:"
  printf '%s' "$D" | sed 's/^/       /' >&2
fi

# --- E. the tests + the shared builder -------------------------------------------
E=""
has "$VIEW" 'func BuildRulesLensViewFor(' || E="${E}there is no lens-aware builder entry point"$'\n'
has "$VIEW" 'return BuildRulesLensViewFor(path, q, rows, lensParam, groupParam, RuleLensOrder())' \
  || E="${E}the user page does not go through the same builder"$'\n'
has "$TEST" 'func TestB523_UserLensGroupsByOwner(' || E="${E}the owner grouping is not pinned"$'\n'
has "$TEST" 'func TestB523_UserLensFallbackNeverMergesOwners(' || E="${E}the id fallback is not pinned"$'\n'
has "$TEST" 'func TestB523_AdminViewHasFiveLinksAndKeepsTheQuery(' || E="${E}the admin selector/query round-trip is not pinned"$'\n'
has "$RTEST" 'func TestB523_AdminRendersTheGroupList(' || E="${E}the list render is not pinned"$'\n'
has "$RTEST" 'func TestB523_AdminGroupPageRendersTheRows(' || E="${E}the group render is not pinned"$'\n'
has "$RTEST" 'func TestB523_LegacyTreeStillRenders(' || E="${E}the ?view= escape is not pinned"$'\n'
if [ -n "$E" ]; then
  bad "E1: the shared builder or its tests are incomplete:"
  printf '%s' "$E" | sed 's/^/       /' >&2
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ ./internal/handlers/ -run 'B523' -count=1 2>&1)"
  RC=$?
  if [ -z "$E" ] && [ "$RC" -eq 0 ]; then
    ok "E1: the model and the page are pinned by tests that pass, through the SHARED builder"
  elif [ "$RC" -ne 0 ]; then
    bad "E2: the B523 tests fail (rc=$RC):"
    printf '%s\n' "$OUT" | grep -E 'FAIL|---' | head -8 | sed 's/^/       /' >&2
  fi
else
  skip "E2: no Go on PATH — the tests cannot be run here"
fi

# --- F. bookkeeping -------------------------------------------------------------
F=""
for f in "$MODEL" "$VIEW" "$ADMIN" "$ROW" "$TPL" "$TEST" "$RTEST"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 \
    || F="${F}$f is NOT tracked by git (AGENTS trap #11)"$'\n'
done
git ls-files --error-unmatch scripts/check_b523_admin_group_page.sh >/dev/null 2>&1 \
  || F="${F}this script is NOT tracked by git"$'\n'
has scripts/verify_pre_deploy.sh 'check_b523_admin_group_page.sh' \
  || F="${F}verify_pre_deploy.sh does not register B523"$'\n'
has AGENTS.md '**B523**' \
  || F="${F}AGENTS.md's block index has no B523 entry (AGENTS rule 2)"$'\n'
if [ -z "$F" ]; then
  ok "F1: tracked, registered and indexed"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB523 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
