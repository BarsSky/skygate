#!/usr/bin/env bash
# check_b517_rule_toggle.sh
#
# 2026-11-25 (B517) — включение и выключение правила (и группы) вместо «только
# удалить или добавить».
#
# OPERATOR REPORT (verbatim): «также не хватает возможности выключения и включения
# правила, сейчас можно только удалить или добавить».
#
# MEASURED in the code, not assumed: `device_rules.enabled` has existed since v0.20
# and EVERY consumer already honours it — the ACL entry query is `WHERE enabled = 1`,
# the advertised-route sync, the DNS auto-updater, the preferred-exit reconciler, the
# prefix claims and every quota counter filter on it too. The panel simply had no
# WRITER, so switching a rule off meant destroying it — and with it its parent_domain,
# its group label and its place in the prefix assignment.
#
# THE CHANGE:
#
#   * `db.SetDeviceRulesEnabled` — one owner-scoped UPDATE (`WHERE user_id = $2`), the
#     security boundary of the whole feature;
#   * one endpoint, `POST /my/exit-rules/toggle`, with three scopes: a single rule
#     (which cascades to a domain's resolved rows and to an «all my devices» rule's
#     copies), a whole GROUP recomputed from the same model the page rendered, and an
#     explicit id list for the multi-select bar;
#   * the ACL tail every other rule mutation already uses (`applyACLIfDrifted` +
#     advertised-route re-sync on a real apply), an audit row and an `exit_rule_logs`
#     line under its own `disable`/`enable` actions;
#   * the page: a switch per row, one per card (with the blast radius in the confirm),
#     and the outcome in the URL so a refresh cannot resubmit;
#   * the GROUP page is now the default and `?view=` keeps the pre-B516 projections.
#
# CONTRACTS
#   A. the writer exists, is owner-scoped and chunked for SQLite's parameter limit
#   B. the three toggle shapes are decided in ONE pure function and pinned by tests
#   C. the page renders both switches, the banners, and stops lying in the old views
#   D. every new string is localized RU+EN (page keys and the alert key)
#   E. safety: an exact return path, ids intersected with the caller's rows, one ACL
#      decision and a route re-sync after a real apply
#   F. bookkeeping: tracked by git, registered in the catalog, indexed in AGENTS.md

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B517: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DB=internal/db/device_rules_enabled_b517.go
SVC=internal/feature/exit_rules/toggle_b517.go
# B528 step 5 (v1.5.274): the route table is a set of per-feature registries now, so
# read the PACKAGE surface — a row bound to routes.go turns red on a pure move.
. scripts/lib/gosurface.sh
gosurface ROUTES cmd/skygate/*.go || { echo "B517: cannot read the cmd/skygate surface" >&2; exit 2; }
TPL=internal/handlers/templates/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go
BOT=internal/i18n/catalog_bot.go
LENS=internal/feature/exit_rules/form_my_lens_b516.go
TEST=internal/feature/exit_rules/toggle_b517_test.go
RENDER=internal/handlers/exit_rules_toggle_render_b517_test.go

hdr "B517 — a rule can be switched off without being deleted"

# --- A: the writer exists, is owner-scoped and chunked -------------------------
A_MISS=""
for fn in 'func SetDeviceRulesEnabled(' 'const deviceRulesEnabledChunk = 400'; do
  grep -qF "$fn" "$DB" || A_MISS="${A_MISS}${fn%?} is missing — there is still no writer for the column"$'\n'
done
grep -q 'WHERE user_id = \$2 AND id IN (' "$DB" \
  || A_MISS="${A_MISS}the UPDATE is not owner-scoped — one crafted form could switch another account's rule"$'\n'
grep -q 'PlaceholdersRange(3, len(chunk)+2)' "$DB" \
  || A_MISS="${A_MISS}the id list is not built with PlaceholdersRange (a hand-typed \$N list breaks on one dialect)"$'\n'
if grep -q 'enabled = ' "$DB" && grep -qE 'SET enabled = ?\?' "$DB"; then
  A_MISS="${A_MISS}the UPDATE binds enabled with a ? placeholder (PostgreSQL syntax error)"$'\n'
fi
grep -q 'mux.Handle("POST /my/exit-rules/toggle", authMW' "$ROUTES" \
  || A_MISS="${A_MISS}the toggle route is not registered behind authMW"$'\n'
grep -q 'func (s \*Service) PostMyExitRuleToggle(' "$SVC" \
  || A_MISS="${A_MISS}PostMyExitRuleToggle does not exist"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: db.SetDeviceRulesEnabled is owner-scoped, dialect-safe and chunked, and the route is behind authMW"
else
  bad "A1: the writer is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# --- B: the three toggle shapes, decided in one pure function -----------------
B_MISS=""
grep -q 'func toggleTargetIDs(rows \[\]RuleRow, id int64) (\[\]int64, string)' "$SVC" \
  || B_MISS="${B_MISS}toggleTargetIDs is missing — nothing decides what 'this rule' means at the edges"$'\n'
grep -q 'if target.AllDevices {' "$SVC" \
  || B_MISS="${B_MISS}the all-devices shape is not handled (the fan-out pass would resurrect an enabled copy)"$'\n'
grep -q 'return ids, "fanout"' "$SVC" \
  || B_MISS="${B_MISS}the fan-out shape is not reported to the audit trail"$'\n'
grep -q 'if strings.EqualFold(strings.TrimSpace(target.TargetType), "domain") {' "$SVC" \
  || B_MISS="${B_MISS}the domain shape is not handled — a domain row could switch off while its /32s kept routing"$'\n'
grep -q 'if ruleParentDomainMatches(r.ParentDomain, domain) {' "$SVC" \
  || B_MISS="${B_MISS}the domain cascade does not walk parent_domain"$'\n'
grep -q 'func ruleParentDomainMatches(' "$SVC" \
  || B_MISS="${B_MISS}ruleParentDomainMatches is missing — the two provenance spellings would diverge"$'\n'
grep -q 'if _, plain, ok := ParseCDNGroupMarker(parent); ok {' "$SVC" \
  || B_MISS="${B_MISS}the cascade does not understand the cdn:<provider>:<domain> spelling"$'\n'
grep -q 'return \[\]int64{target.ID}, "rule"' "$SVC" \
  || B_MISS="${B_MISS}the single-rule shape is not handled"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: one pure function decides the three shapes (single / domain cascade / all-devices fan-out), both provenance spellings included"
else
  bad "B1: the toggle shapes are incomplete:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# The group scope must be recomputed from the SAME model the page rendered.
B2_MISS=""
grep -q 'groups := BuildRuleGroups(ruleRows, lens)' "$SVC" \
  || B2_MISS="${B2_MISS}the group scope does not rebuild the groups from the shared model"$'\n'
grep -q 'g, found := FindRuleGroup(groups, r.FormValue("group"))' "$SVC" \
  || B2_MISS="${B2_MISS}the group scope does not resolve the posted key through FindRuleGroup"$'\n'
grep -q 'if !found {' "$SVC" \
  || B2_MISS="${B2_MISS}a stale group key is not refused"$'\n'
if [ -z "$B2_MISS" ]; then
  ok "B2: 'switch this card' acts on exactly the rows the card rendered (same model, same key, stale key refused)"
else
  bad "B2: the group scope is not tied to the rendered page:"
  printf '%s' "$B2_MISS" | sed 's/^/       /' >&2
fi

# --- C: the page renders both switches, and the old views stop lying ----------
C_MISS=""
grep -q 'action="/my/exit-rules/toggle"' "$TPL" \
  || C_MISS="${C_MISS}the page has no toggle form"$'\n'
grep -q 'name="scope" value="group"' "$TPL" \
  || C_MISS="${C_MISS}the card offers no group switch"$'\n'
grep -q 'name="scope" value="rule"' "$TPL" \
  || C_MISS="${C_MISS}the row table offers no per-rule switch"$'\n'
grep -q 'id="toggle-banner"' "$TPL" \
  || C_MISS="${C_MISS}the success banner is missing — the operator would not learn what changed"$'\n'
grep -q 'id="toggle-error"' "$TPL" \
  || C_MISS="${C_MISS}the error banner is missing"$'\n'
grep -q 'exit_rules.toggle_confirm_group' "$TPL" \
  || C_MISS="${C_MISS}the group switch does not name the blast radius before the click"$'\n'
grep -q 'name="enabled" value="{{if \$r.Enabled}}0{{else}}1{{end}}"' "$TPL" \
  || C_MISS="${C_MISS}a row's switch does not offer the OPPOSITE state (it would be a no-op)"$'\n'
# The legacy projections must not present a switched-off rule as active.
N_CHIPS=$(grep -c '{{if not .Enabled}}' "$TPL" 2>/dev/null || true)
[ "${N_CHIPS:-0}" -ge 5 ] \
  || C_MISS="${C_MISS}the legacy row tables carry ${N_CHIPS:-0} disabled chips, want at least 5 — a switched-off rule would look active there"$'\n'
# B517 flipped the default: the group page renders unless ?view= asks otherwise.
grep -q 'return strings.TrimSpace(q.Get("view")) == ""' "$LENS" \
  || C_MISS="${C_MISS}myRulesLensEnabled is not the single default switch (the group page must be the default, ?view= the escape hatch)"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: per-row and per-card switches render, both banners exist, the legacy tables mark disabled rows, and the group page is the default"
else
  bad "C1: the page does not offer the verb:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# --- D: i18n, RU + EN ---------------------------------------------------------
D_MISS=""
for key in toggle_off toggle_on toggle_off_title toggle_on_title toggle_group_off \
           toggle_group_on toggle_confirm toggle_confirm_group toggle_ok_off \
           toggle_ok_on toggle_err; do
  n=$(grep -c "\"exit_rules\.$key\"[[:space:]]*:" "$CAT" 2>/dev/null || true)
  [ "${n:-0}" -eq 2 ] || D_MISS="${D_MISS}exit_rules.$key is defined ${n:-0} time(s), want exactly 2 (RU + EN)"$'\n'
done
n=$(grep -c '"alert.acl.apply_failed_toggle"[[:space:]]*:' "$BOT" 2>/dev/null || true)
[ "${n:-0}" -eq 2 ] || D_MISS="${D_MISS}alert.acl.apply_failed_toggle is defined ${n:-0} time(s), want 2 (the operator is told when a toggle left the policy stale)"$'\n'
grep -q '"exit_rules\.toggle_off":[[:space:]]*"Выключить"' "$CAT" \
  || D_MISS="${D_MISS}the RU toggle_off value is not «Выключить»"$'\n'
grep -q '"exit_rules\.toggle_ok_off":[[:space:]]*"Выключено правил: %d — они перестали действовать"' "$CAT" \
  || D_MISS="${D_MISS}the RU success line does not say the rules stopped acting (the difference from deleting)"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: every new string exists exactly twice (RU+EN), including the alert the operator receives when the policy goes stale"
else
  bad "D1: the wording is not fully localized:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- E: the safety rails -------------------------------------------------------
E_MISS=""
# The return address is an INPUT: it must be the rules page exactly.
# RENEGOTIATED by B520 (2026-11-25), in place: the same exact-path rule now lives in
# safePageBack(raw, want), because the ADMIN page needs the identical validation
# against its own path. The property is unchanged — a return address must BE the
# page, not merely start with it — and safeRulesBack still pins the my-page path it
# delegates with.
grep -q 'u.Path != want' "$SVC" \
  || E_MISS="${E_MISS}the return path is not an exact match — /my/exit-rules/../../admin starts with the right prefix"$'\n'
grep -q 'targetIDs = intersectRuleIDs(ruleRows, targetIDs)' "$SVC" \
  || E_MISS="${E_MISS}the ids are not intersected with the caller's rows (a stale or crafted id would reach the UPDATE)"$'\n'
grep -q 'func intersectRuleIDs(' "$SVC" || E_MISS="${E_MISS}intersectRuleIDs is missing"$'\n'
# One ACL decision, the shared tail, and the data plane after it.
grep -q 's.applyACLIfDrifted("user-rule-toggle", detail)' "$SVC" \
  || E_MISS="${E_MISS}the toggle does not use the shared ACL drift decision"$'\n'
grep -q 'if s.SyncRoutes != nil {' "$SVC" \
  || E_MISS="${E_MISS}the advertised routes are not re-synced after a real apply (a released prefix would stay announced)"$'\n'
grep -q 'db.ExitRuleActionDisable' "$SVC" && grep -q 'db.ExitRuleActionEnable' "$SVC" \
  || E_MISS="${E_MISS}the exit_rule_logs action does not distinguish disable from enable"$'\n'
# RENEGOTIATED by B520 (2026-11-25), in place: the audit action is now built from
# `ch.Action` + the verb, because the ADMIN page (B520) performs the same change on
# another user's rule and must not write the owner's name into the actor column.
# The property is unchanged — the audit row distinguishes the direction — and the
# user-page call sites still spell the action as `exit_rule_`.
grep -q 'ch.Action+verb' "$SVC" \
  || E_MISS="${E_MISS}no audit row distinguishes the direction of the change"$'\n'
grep -q 'Action:    "exit_rule_"' "$SVC" \
  || E_MISS="${E_MISS}the user-page toggle no longer attributes the audit row as exit_rule_<verb>"$'\n'
# R6: no raw http.Error with the parser's text in the new handler.
if grep -q 'http.Error(w, err.Error()' "$SVC"; then
  E_MISS="${E_MISS}the handler answers with http.Error(err.Error()) — the R6 error-leak guard counts that as a regression"$'\n'
fi
if [ -z "$E_MISS" ]; then
  ok "E1: exact return path, owner-intersected ids, one ACL decision, routes re-synced, audit direction recorded, no leaked error text"
else
  bad "E1: a safety rail is missing:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# --- F: bookkeeping + the behavioural pins -------------------------------------
F_MISS=""
git ls-files --error-unmatch scripts/check_b517_rule_toggle.sh >/dev/null 2>&1 \
  || F_MISS="${F_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
for f in "$DB" "$SVC" "$TEST" "$RENDER"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || F_MISS="${F_MISS}$f is NOT tracked by git"$'\n'
done
grep -q 'check_b517_rule_toggle.sh' scripts/verify_pre_deploy.sh \
  || F_MISS="${F_MISS}verify_pre_deploy.sh does not register B517 — the contract would never run"$'\n'
grep -q '\*\*B517\*\*' AGENTS.md \
  || F_MISS="${F_MISS}AGENTS.md's block index has no B517 entry (AGENTS rule 2)"$'\n'
for t in TestB517_DomainCascadeSwitchesTheResolvedRows TestB517_FanOutCopiesSwitchTogether \
         TestB517_GroupScopeActsOnTheRenderedCard TestB517_ForeignRuleIsUnreachable \
         TestB517_ReEnableBringsTheRuleBack TestB517_BackURLIsValidated; do
  grep -q "func $t(" "$TEST" || F_MISS="${F_MISS}the handler/model test $t is missing"$'\n'
done
for t in TestB517_ListOffersTheGroupSwitch TestB517_DetailOffersTheRowSwitchAndTheGroupSwitch \
         TestB517_OutcomeBannerRenders; do
  grep -q "func $t(" "$RENDER" || F_MISS="${F_MISS}the render test $t is missing"$'\n'
done
if [ -z "$F_MISS" ]; then
  ok "F1: tracked, registered, indexed, and pinned by handler, model and render tests"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB517 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
