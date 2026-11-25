#!/usr/bin/env bash
# check_b518_source_card_actions.sh
#
# 2026-11-25 (B518) — правила домена меняются карточкой, а не «снести всё и задать
# заново».
#
# OPERATOR REPORT (verbatim): «отдельно по подсетям что добавляются вместе с
# доменами — когда происходит такое добавление то потом не понятно к какому домену
# относилась та или иная подсеть, из за чего если потребуется координально менять
# правила то придется все сносить и задавать заново, что очень не удобно».
#
# B516 answered the first half (one card holds a domain and every row resolved from
# it, in both provenance spellings). THIS block is the second half — the verbs that
# make «change this domain's routing» one action:
#
#   * «собрать группу из источника» — file the card's EXISTING rows under the
#     operator's own label. B380 could only write a label at CREATION, and a
#     re-added rule is a duplicate that changes nothing (the natural key does not
#     include the label), so filing existing rows had no verb at all;
#   * «переразрешить сейчас» — clear the per-domain resolve gate and run the SAME
#     resolver pass the scheduler runs. A second implementation of «resolve one
#     domain» would drift on exactly the parts that took blocks to get right (CDN
#     markers B409, IPv6 answers B442, service ranges B455);
#   * «удалить домен и подсети» and «запросить другой релей» reuse the existing
#     delete and relay-request endpoints, addressed to the card's WHOLE id set.
#
# THE PROPERTY THAT MATTERS: an action on a card acts on the rows the card showed.
# The page windows rows at 50, so posting the VISIBLE rows would silently act on
# 50 of 200 subnets — the half-applied action this project keeps paying for.
#
# CONTRACTS
#   A. both verbs exist, are routed behind authMW and are owner-scoped
#   B. an action carries the card's whole id set (not one page) and only the owner's rows
#   C. the page offers the verbs, hides the ones that do not apply, and renders the
#      label suggestions exactly once
#   D. every new string is localized RU+EN
#   E. the resolver is REUSED, not re-implemented, and the two callers cannot race
#   F. bookkeeping: tracked by git, registered in the catalog, indexed in AGENTS.md

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B518: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SVC=internal/feature/exit_rules/source_actions_b518.go
DBL=internal/db/device_rules_group_label_b518.go
ROUTES=cmd/skygate/routes.go
TPL=internal/handlers/templates/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go
VIEW=internal/feature/exit_rules/form_my_lens_b516.go
MODEL=internal/feature/exit_rules/rules_view_b516.go
DOMAIN=internal/feature/exit_rules/sync_domain.go
STAMP=internal/feature/exit_rules/domain_interval_b308.go
TEST=internal/feature/exit_rules/source_actions_b518_test.go
RENDER=internal/handlers/exit_rules_source_actions_render_b518_test.go

hdr "B518 — a domain card carries its own verbs"

# --- A: both verbs exist, routed, owner-scoped ---------------------------------
A_MISS=""
grep -q 'func (s \*Service) PostMyExitRuleAssignGroup(' "$SVC" \
  || A_MISS="${A_MISS}PostMyExitRuleAssignGroup is missing — filing existing rules into a group has no verb"$'\n'
grep -q 'func (s \*Service) PostMyExitRuleSourceResolve(' "$SVC" \
  || A_MISS="${A_MISS}PostMyExitRuleSourceResolve is missing — a domain cannot be re-resolved on demand"$'\n'
grep -q 'mux.Handle("POST /my/exit-rules/assign-group", authMW' "$ROUTES" \
  || A_MISS="${A_MISS}the assign-group route is not registered behind authMW"$'\n'
grep -q 'mux.Handle("POST /my/exit-rules/source-resolve", authMW' "$ROUTES" \
  || A_MISS="${A_MISS}the source-resolve route is not registered behind authMW"$'\n'
grep -q 'func SetDeviceRulesGroupLabel(' "$DBL" \
  || A_MISS="${A_MISS}db.SetDeviceRulesGroupLabel is missing"$'\n'
grep -q 'WHERE user_id = \$2 AND id IN (' "$DBL" \
  || A_MISS="${A_MISS}the label write is not owner-scoped"$'\n'
grep -q 'PlaceholdersRange(3, len(chunk)+2)' "$DBL" \
  || A_MISS="${A_MISS}the id list is not built with PlaceholdersRange (dialect trap)"$'\n'
grep -q 'rows, err := s.ownerRuleRows(c.UserID)' "$SVC" \
  || A_MISS="${A_MISS}the actions do not load the caller's rows through the shared loader"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: both verbs exist behind authMW, the label writer is owner-scoped, and every action reads the caller's own rows"
else
  bad "A1: a verb or its scope is missing:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# --- B: a card action carries the WHOLE card -----------------------------------
B_MISS=""
grep -q 'func RuleIDsOfGroup(' "$MODEL" \
  || B_MISS="${B_MISS}RuleIDsOfGroup is missing — nothing resolves a card key to its full id set"$'\n'
grep -q 'ids, found := RuleIDsOfGroup(rows, lens, r.FormValue("group"))' "$SVC" \
  || B_MISS="${B_MISS}the actions do not resolve the card through the shared model"$'\n'
grep -q 'return intersectRuleIDs(rows, ids), "selection", nil' "$SVC" \
  || B_MISS="${B_MISS}a posted id set is not intersected with the caller's rows"$'\n'
grep -q 'AllIDs \[\]int64' "$VIEW" \
  || B_MISS="${B_MISS}GroupDetailView does not carry the unpaged id set"$'\n'
grep -q 'AllIDs: allIDs,' "$VIEW" \
  || B_MISS="${B_MISS}the detail view does not fill AllIDs"$'\n'
# The template must address AllIDs, never Rows: Rows is one page.
grep -q '{{range \$id := \$d.AllIDs}}<input type="hidden" name="ids" value="{{$id}}">{{end}}' "$TPL" \
  || B_MISS="${B_MISS}the card's delete does not post the whole card (it would act on one page of rows)"$'\n'
grep -q '{{range \$id := \$d.AllIDs}}<input type="hidden" name="rule_id" value="{{$id}}">{{end}}' "$TPL" \
  || B_MISS="${B_MISS}the card's relay ask does not post the whole card"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: every card action carries the card's whole id set, resolved by the same model the page rendered"
else
  bad "B1: the action scope is wrong:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# --- C: the page offers the verbs and hides the inapplicable ones --------------
C_MISS=""
grep -q 'id="group-actions"' "$TPL" || C_MISS="${C_MISS}the card has no action bar"$'\n'
grep -q 'id="source-resolve-btn"' "$TPL" || C_MISS="${C_MISS}«переразрешить сейчас» is not rendered"$'\n'
grep -q '{{if \$d.CanResolve}}' "$TPL" \
  || C_MISS="${C_MISS}the resolve verb is not gated on the card being a SOURCE (it would answer another question on a label card)"$'\n'
grep -q 'CanResolve: g.Kind == GroupKindDomain || g.Kind == GroupKindCDN' "$VIEW" \
  || C_MISS="${C_MISS}CanResolve is not derived from the card kind"$'\n'
grep -q 'id="group-assign-btn"' "$TPL" || C_MISS="${C_MISS}«в группу» is not rendered"$'\n'
grep -q 'id="source-delete-btn"' "$TPL" || C_MISS="${C_MISS}«удалить домен и подсети» is not rendered"$'\n'
grep -q 'exit_rules.source_delete_confirm' "$TPL" \
  || C_MISS="${C_MISS}the delete does not name its blast radius before the click"$'\n'
grep -q '{{if ne \$.RelayCard.PolicyMode "deny"}}' "$TPL" \
  || C_MISS="${C_MISS}the relay ask is not gated on the policy (a control that will be refused is a lie)"$'\n'
# Exactly ONE datalist, rendered in both views: it used to live inside the old
# group view only, so the default page lost the suggestions (B517 follow-up).
N_DATALISTS=$(grep -c 'id="group-label-names"' "$TPL" 2>/dev/null || true)
[ "${N_DATALISTS:-0}" -eq 1 ] \
  || C_MISS="${C_MISS}the group-name datalist appears ${N_DATALISTS:-0} time(s), want exactly 1 outside both views"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: the card offers all four verbs, gates the two that do not always apply, and renders the label suggestions once"
else
  bad "C1: the card markup is incomplete:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# --- D: i18n -------------------------------------------------------------------
D_MISS=""
for key in source_resolve source_resolve_confirm source_resolve_ok source_delete \
           source_delete_confirm source_err group_assign_btn group_assign_hint \
           group_assign_ok group_assign_cleared relay_request_confirm_group; do
  n=$(grep -c "\"exit_rules\.$key\"[[:space:]]*:" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -eq 2 ] || D_MISS="${D_MISS}exit_rules.$key is defined ${n:-0} time(s), want exactly 2 (RU + EN)"$'\n'
done
grep -q '"exit_rules\.source_resolve":[[:space:]]*"Переразрешить сейчас"' "$CAT" \
  || D_MISS="${D_MISS}the RU resolve label is not «Переразрешить сейчас»"$'\n'
grep -q '"exit_rules\.source_delete":[[:space:]]*"Удалить домен и подсети"' "$CAT" \
  || D_MISS="${D_MISS}the RU delete label does not say the subnets go with the domain"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: every new string exists exactly twice (RU+EN), and the two destructive labels say what they do"
else
  bad "D1: the wording is not fully localized:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- E: the resolver is reused, and the two callers cannot race -----------------
E_MISS=""
grep -q 'func (s \*Service) runResolvePass()' internal/feature/exit_rules/service.go \
  || E_MISS="${E_MISS}runResolvePass is missing — the handler would call the pass directly (no seam, no test)"$'\n'
grep -q 'added, removed, rerr := s.runResolvePass()' "$SVC" \
  || E_MISS="${E_MISS}the handler does not go through the shared pass call site"$'\n'
grep -q 'var domainUpdaterMu sync.Mutex' "$DOMAIN" \
  || E_MISS="${E_MISS}the resolver pass is not serialised — the button and the tick could race"$'\n'
grep -q 'domainUpdaterMu.Lock()' "$DOMAIN" \
  || E_MISS="${E_MISS}DomainAutoUpdater does not take the resolver lock"$'\n'
grep -q 'func (s \*Service) clearDomainResolveStamp(' "$STAMP" \
  || E_MISS="${E_MISS}clearDomainResolveStamp is missing"$'\n'
grep -q 'return db.DeleteGlobalSetting(' "$STAMP" \
  || E_MISS="${E_MISS}the stamp is not actually deleted (the next pass would skip the domain)"$'\n'
grep -q 'the domain rule is switched off' "$SVC" \
  || E_MISS="${E_MISS}a switched-off domain rule is not refused with its reason (the resolver would skip it and report 0)"$'\n'
if [ -z "$E_MISS" ]; then
  ok "E1: the button runs the SAME pass as the scheduler, the two callers are serialised, and a switched-off rule is refused with its reason"
else
  bad "E1: the resolver reuse is incomplete:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# --- F: bookkeeping + the behavioural pins -------------------------------------
F_MISS=""
git ls-files --error-unmatch scripts/check_b518_source_card_actions.sh >/dev/null 2>&1 \
  || F_MISS="${F_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
for f in "$SVC" "$DBL" "$TEST" "$RENDER"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || F_MISS="${F_MISS}$f is NOT tracked by git"$'\n'
done
grep -q 'check_b518_source_card_actions.sh' scripts/verify_pre_deploy.sh \
  || F_MISS="${F_MISS}verify_pre_deploy.sh does not register B518"$'\n'
grep -q '\*\*B518\*\*' AGENTS.md \
  || F_MISS="${F_MISS}AGENTS.md's block index has no B518 entry (AGENTS rule 2)"$'\n'
for t in TestB518_AssignGroupFilesTheCardAndOnlyTheCard TestB518_AssignGroupWithEmptyLabelClears \
         TestB518_AssignGroupRefusesAForeignSelection TestB518_ResolveNowRunsThePassAndReportsIt \
         TestB518_ResolveRefusesADisabledDomainRule TestB518_CardIDsAreEveryRowUnpaged; do
  grep -q "func $t(" "$TEST" || F_MISS="${F_MISS}the test $t is missing"$'\n'
done
for t in TestB518_SourceCardOffersItsVerbs TestB518_CardActionsCarryEveryRowNotOnePage \
         TestB518_LabelCardHidesTheResolveVerb TestB518_RelayAskHiddenWhenThePolicyForbidsIt; do
  grep -q "func $t(" "$RENDER" || F_MISS="${F_MISS}the render test $t is missing"$'\n'
done
if [ -z "$F_MISS" ]; then
  ok "F1: tracked, registered, indexed, and pinned by handler, model and render tests"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB518 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
