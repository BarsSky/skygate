#!/usr/bin/env bash
# check_b519_selection_actions.sh
#
# 2026-11-25 (B519) — выделить несколько строк и сделать с ними одно действие.
#
# THE REGRESSION THIS CLOSES: the pre-B516 page had checkboxes and a multi-delete
# bar; the group page (the default since B517) had neither, so «tick these twelve
# subnets and switch them off together» became impossible — the only tools were the
# per-row switch and the whole-card verbs. A rework that takes a capability away
# without saying so is the class of regression this project keeps a catalog for.
#
# HOW IT IS BUILT: ONE form (`id="rules-selection"`), checkboxes in the table bound
# to it by the HTML `form` attribute (a form may not wrap table rows, and a second
# copy of the checkbox set is how the two copies drift), one submit button per verb
# (`name="op"`), and «удалить» overriding the action with `formaction` so it posts
# to the endpoint that OWNS the delete cascade. No JavaScript is needed for the
# action; the inline script only keeps the counter and the select-all box honest.
#
# The three ops reuse the SHARED writers — `db.SetDeviceRulesGroupLabel` (B518) and
# `setRulesEnabled` (the B517 tail: audit + one ACL decision + route re-sync). A
# second copy of that tail is how the delete path once ended up with an audit row
# and no apply.
#
# CONTRACTS
#   A. the handler exists, is routed behind authMW, and is owner-scoped
#   B. every op goes through a SHARED writer — the ACL tail is not duplicated
#   C. the bar renders one form, all four verbs, bound checkboxes, counter, banner
#   D. i18n RU+EN for the new strings, and the reused ones still exist
#   E. delete is NOT re-implemented here (it posts to the existing endpoint)
#   F. bookkeeping: tracked, registered, indexed, pinned by tests

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B519: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SVC=internal/feature/exit_rules/selection_b519.go
TOGGLE=internal/feature/exit_rules/toggle_b517.go
# B528 step 5 (v1.5.274): the route table is a set of per-feature registries now, so
# read the PACKAGE surface — a row bound to routes.go turns red on a pure move.
. scripts/lib/gosurface.sh
gosurface ROUTES cmd/skygate/*.go || { echo "B519: cannot read the cmd/skygate surface" >&2; exit 2; }
TPL=internal/handlers/templates/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go
TEST=internal/feature/exit_rules/selection_b519_test.go
RENDER=internal/handlers/exit_rules_selection_render_b519_test.go

hdr "B519 — a selection of rows can be switched, filed or deleted together"

# --- A: the handler exists and is owner-scoped ---------------------------------
A_MISS=""
grep -q 'func (s \*Service) PostMyExitRuleSelection(' "$SVC" \
  || A_MISS="${A_MISS}PostMyExitRuleSelection is missing — the bar would post nowhere"$'\n'
grep -q 'mux.Handle("POST /my/exit-rules/selection", authMW' "$ROUTES" \
  || A_MISS="${A_MISS}the selection route is not registered behind authMW"$'\n'
grep -q 'rows, err := s.ownerRuleRows(c.UserID)' "$SVC" \
  || A_MISS="${A_MISS}the handler does not load the caller's own rows"$'\n'
grep -q 'ids, _, aerr := s.cardActionIDs(r, rows)' "$SVC" \
  || A_MISS="${A_MISS}the posted ids are not intersected with the caller's rows"$'\n'
grep -q 'if len(ids) == 0 {' "$SVC" \
  || A_MISS="${A_MISS}an empty selection is not refused (the operator gets no feedback)"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: the selection endpoint is routed behind authMW and operates only on the caller's own rows"
else
  bad "A1: the handler is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# --- B: shared writers, no second ACL tail -------------------------------------
B_MISS=""
grep -q 'db.SetDeviceRulesGroupLabel(s.dbc(), c.UserID, intIDs, label)' "$SVC" \
  || B_MISS="${B_MISS}op=group does not use the B518 label writer"$'\n'
# RENEGOTIATED by B520 (2026-11-25), in place: the tail now takes a ruleStateChange
# STRUCT rather than five positionals, because the admin page switches ANOTHER
# user's rule — the row written belongs to the owner while the audit row must name
# the ADMIN. The property is unchanged («the selection goes through the one shared
# tail»); only the call shape moved.
grep -q 's.setRulesEnabled(ruleStateChange{' "$SVC" \
  || B_MISS="${B_MISS}op=disable/enable do not use the shared state-change tail"$'\n'
grep -q 'func (s \*Service) setRulesEnabled(' "$TOGGLE" \
  || B_MISS="${B_MISS}setRulesEnabled is missing — the tail has no single home"$'\n'
# The tail must exist ONCE: a second applyACLIfDrifted in the selection file is the
# duplicated-tail defect, not a shortcut.
N_APPLY=$(grep -c 'applyACLIfDrifted(' "$SVC" 2>/dev/null || true)
[ "${N_APPLY:-0}" -eq 0 ] \
  || B_MISS="${B_MISS}the selection handler duplicates the ACL tail (${N_APPLY} applyACLIfDrifted call(s)) — it must go through setRulesEnabled"$'\n'
N_AUDIT=$(grep -c 'Backend.Audit' "$TOGGLE" 2>/dev/null || true)
[ "${N_AUDIT:-0}" -ge 1 ] \
  || B_MISS="${B_MISS}the shared tail does not write an audit row"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: every op goes through a shared writer and the ACL tail exists exactly once"
else
  bad "B1: the writers are duplicated or missing:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# --- C: the bar renders ---------------------------------------------------------
C_MISS=""
grep -q 'id="rules-selection" method="post" action="/my/exit-rules/selection"' "$TPL" \
  || C_MISS="${C_MISS}the selection form is not rendered"$'\n'
grep -q 'id="rules-selection-bar"' "$TPL" || C_MISS="${C_MISS}the bar is missing"$'\n'
for op in group disable enable; do
  grep -q "name=\"op\" value=\"$op\"" "$TPL" \
    || C_MISS="${C_MISS}the «$op» button is missing (a verb the operator cannot press)"$'\n'
done
grep -q 'formaction="/my/exit-rules/delete"' "$TPL" \
  || C_MISS="${C_MISS}the delete button does not override the action (it would post to the selection endpoint, which has no delete)"$'\n'
grep -q 'class="rule-check" name="ids" value="{{$r.ID}}"' "$TPL" \
  || C_MISS="${C_MISS}the row table has no checkbox"$'\n'
grep -q 'form="rules-selection" aria-label="{{$r.TargetValue}}"' "$TPL" \
  || C_MISS="${C_MISS}the checkbox is not bound to the selection form (it would never be submitted)"$'\n'
grep -q '<th class="col-check"></th>' "$TPL" \
  || C_MISS="${C_MISS}the table header has no checkbox column (the columns shift)"$'\n'
grep -q 'id="rules-select-all"' "$TPL" || C_MISS="${C_MISS}select-all is missing"$'\n'
grep -q 'id="rules-sel-count"' "$TPL" || C_MISS="${C_MISS}the counter is missing"$'\n'
grep -q 'window.skyRulesSelAll' "$TPL" || C_MISS="${C_MISS}select-all has no handler"$'\n'
grep -q 'id="selection-banner"' "$TPL" || C_MISS="${C_MISS}the outcome banner is missing"$'\n'
# The script must scope itself to THIS form: the page can also render a legacy view
# with its own .rule-check set behind ?view=.
grep -q "input.rule-check\[form=\"rules-selection\"\]" "$TPL" \
  || C_MISS="${C_MISS}the counter is not scoped to the selection form (it would count rows of the legacy view)"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: one form carries all four verbs, every row is bound to it, and the counter is scoped"
else
  bad "C1: the bar is incomplete:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# --- D: i18n -------------------------------------------------------------------
D_MISS=""
for key in selection_off selection_on selection_ok_off selection_ok_on; do
  n=$(grep -c "\"exit_rules\.$key\"[[:space:]]*:" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -eq 2 ] || D_MISS="${D_MISS}exit_rules.$key is defined ${n:-0} time(s), want exactly 2 (RU + EN)"$'\n'
done
# The bar reuses the strings the old page already had; a rework must not drop them.
for key in select_all selected delete_selected confirm_multi_delete; do
  grep -q "\"exit_rules\.$key\"[[:space:]]*:" "$CAT" \
    || D_MISS="${D_MISS}the reused key exit_rules.$key disappeared from the catalogue"$'\n'
done
grep -q '"exit_rules\.selection_off":[[:space:]]*"Выключить выбранные"' "$CAT" \
  || D_MISS="${D_MISS}the RU selection_off value is not «Выключить выбранные»"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: the new strings exist exactly twice (RU+EN) and the reused ones are still there"
else
  bad "D1: the wording is not complete:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- E: delete is not re-implemented -------------------------------------------
E_MISS=""
for helper in DeleteRuleOrCascadeByParentDomain DeleteAllDeviceFanOut DeleteRuleForUser; do
  if grep -q "$helper" "$SVC"; then
    E_MISS="${E_MISS}the selection handler re-implements the delete cascade ($helper) — a second copy is how one of them loses it"$'\n'
  fi
done
grep -q 'formaction="/my/exit-rules/delete"' "$TPL" \
  || E_MISS="${E_MISS}there is no path from the selection to the existing delete endpoint"$'\n'
if [ -z "$E_MISS" ]; then
  ok "E1: delete goes to the endpoint that owns the cascade; nothing is re-implemented"
else
  bad "E1: the delete path is duplicated:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# --- F: bookkeeping + behavioural pins -----------------------------------------
F_MISS=""
git ls-files --error-unmatch scripts/check_b519_selection_actions.sh >/dev/null 2>&1 \
  || F_MISS="${F_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
for f in "$SVC" "$TEST" "$RENDER"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || F_MISS="${F_MISS}$f is NOT tracked by git"$'\n'
done
grep -q 'check_b519_selection_actions.sh' scripts/verify_pre_deploy.sh \
  || F_MISS="${F_MISS}verify_pre_deploy.sh does not register B519"$'\n'
grep -q '\*\*B519\*\*' AGENTS.md \
  || F_MISS="${F_MISS}AGENTS.md's block index has no B519 entry (AGENTS rule 2)"$'\n'
for t in TestB519_DisableSelectionSwitchesOnlyTheTickedRows TestB519_EnableSelection \
         TestB519_GroupSelectionFilesOnlyTheTickedRows TestB519_ForeignIdsAreIgnoredButTheRestStillApplies \
         TestB519_EmptyAndUnknownAreRefused; do
  grep -q "func $t(" "$TEST" || F_MISS="${F_MISS}the test $t is missing"$'\n'
done
for t in TestB519_SelectionBarCarriesEveryVerb TestB519_EveryRowHasABoundCheckbox TestB519_SelectionBannerRenders; do
  grep -q "func $t(" "$RENDER" || F_MISS="${F_MISS}the render test $t is missing"$'\n'
done
if [ -z "$F_MISS" ]; then
  ok "F1: tracked, registered, indexed, and pinned by handler and render tests"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB519 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
