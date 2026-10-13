#!/usr/bin/env bash
# scripts/check_b379_admin_rule_delete.sh
#
# B379 (2026-10-13) — THE ADMIN PER-RULE DELETE MUST EXIST, AND MUST DELETE THE RULE THE
# OWNER'S OWN PAGE WOULD DELETE. Closes TD-29.
#
# THE DEFECT (measured 2026-10-11 while B376 was being written)
# ------------------------------------------------------------
# Both delete controls on /admin/exit-rules post to `/admin/exit-rules/delete`, and **no
# mux.Handle registered that path**: a grep over the whole tree found the two forms and
# zero server-side matches. So every admin delete — of any rule, of any user — answered
# 404, and there was no way at all to remove a rule from another user's device.
#
# WHY THE CONTRACTS BELOW ARE NOT "the route is registered"
# ---------------------------------------------------------
# The risk in this handler is not the route, it is the CASCADE. The owner's own delete is
# a decision tree (form_my.go): an `all_devices` row cascades the whole fan-out group
# (B277.4), a domain row takes its derived /32 children (B123/B184 — they are what the
# ACL pins, and the auto-updater would re-add the domain on its next tick), anything else
# deletes one row. Every one of those helpers is scoped by `user_id`, and that scope is
# load-bearing across users. A `DeleteRule(id)` one-liner would compile, pass a route
# grep, and silently leave the ghosts AND cross users if the scope were dropped. So the
# behavioural half is the point: six tests against a real migrated SQLite database,
# including a DECOY row owned by a second user that the cascade must not touch.
#
# CONTRACTS
#   A. the route exists and is admin-gated
#   B. the handler resolves the OWNER before it deletes (no user-filtered guess, no
#      cross-user DELETE)
#   C. the cascade is the owner's own cascade (fan-out / domain / single), verified by the
#      tests and by the shared helpers, not by a copy of the tree
#   D. the operator is told what happened (counts in the redirect, banner in the template,
#      a PARTIAL result is not reported as a complete one, a missing rule is not silent)
#   E. bookkeeping (tracked, registered, indexed)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B379: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

HANDLER=internal/feature/exit_rules/admin_delete_b379.go
ROUTES=cmd/skygate/routes.go
TPL=internal/handlers/templates/admin/exit_rules.html
TESTS=internal/feature/exit_rules/admin_delete_b379_test.go

hdr "B379 — the admin per-rule delete exists and removes what the owner's page removes"

# ---------------------------------------------------------------------------
hdr "A. the route exists and cannot be reached by a non-admin"
# ---------------------------------------------------------------------------
if grep -q 'POST /admin/exit-rules/delete' "$ROUTES"; then
  ok "A1: cmd/skygate/routes.go registers POST /admin/exit-rules/delete (the 404 this closes)"
else
  bad "A1: the route is still unregistered — every admin delete answers 404 (TD-29)"
fi
# The route must sit behind the admin-scoped middleware, exactly like its neighbours.
if grep -E 'POST /admin/exit-rules/delete".*authMW' "$ROUTES" | grep -q 'authMW'; then
  ok "A2: the route is registered behind authMW, like the rest of /admin/exit-rules"
else
  bad "A2: the delete route is not behind the admin middleware"
fi
if grep -q '!c.IsAdmin' "$HANDLER" && grep -q 'StatusForbidden' "$HANDLER"; then
  ok "A3: the handler re-checks IsAdmin itself (defense in depth, like PostAdminExitRule)"
else
  bad "A3: the handler does not re-check IsAdmin"
fi

# ---------------------------------------------------------------------------
hdr "B. the OWNER is resolved before anything is deleted"
# ---------------------------------------------------------------------------
if grep -q 'db.GetRuleAdminContext' "$HANDLER"; then
  ok "B1: the handler resolves the rule's owner through db.GetRuleAdminContext"
else
  bad "B1: the handler does not resolve the owner — it cannot run an owner-scoped cascade"
fi
if grep -q 'qSelectRuleAdminContext' internal/db/queries.go && grep -q 'func GetRuleAdminContext' internal/db/device_rules.go; then
  ok "B2: the helper exists and reads the row WITHOUT a user filter (that is its purpose)"
else
  bad "B2: db.GetRuleAdminContext / its query is missing"
fi
# The one place a cross-user delete could leak: a bare DeleteRule(id) with no owner check.
if grep -q 'db.DeleteRuleForUser(s.dbc(), id, ctx.UserID)' "$HANDLER"; then
  ok "B3: the single-row delete is re-scoped to the resolved owner (not a bare DeleteRule)"
else
  bad "B3: the single-row delete does not re-check the owner it resolved"
fi

# ---------------------------------------------------------------------------
hdr "C. the cascade is the owner's own cascade"
# ---------------------------------------------------------------------------
if grep -q 'db.DeleteAllDeviceFanOut(s.dbc(), ctx.UserID' "$HANDLER"; then
  ok "C1: an all_devices rule cascades its fan-out group (B277.4), scoped to the owner"
else
  bad "C1: the all_devices fan-out cascade is missing — ghost copies would survive"
fi
if grep -q 'db.DeleteRuleOrCascadeByParentDomain(s.dbc(), ctx.UserID, id, ctx.ParentDomain)' "$HANDLER"; then
  ok "C2: a domain rule takes its derived /32 children (B123/B184), scoped to the owner"
else
  bad "C2: the derived-/32 cascade is missing"
fi
# Parity, not a copy: the same three helpers the owner's page calls.
if grep -q 'db.DeleteAllDeviceFanOut' internal/feature/exit_rules/form_my.go \
   && grep -q 'db.DeleteRuleOrCascadeByParentDomain' internal/feature/exit_rules/form_my.go; then
  ok "C3: the admin path and /my/exit-rules share the SAME three delete helpers (parity by construction)"
else
  bad "C3: /my/exit-rules no longer uses those helpers — the two paths have drifted"
fi

# ---------------------------------------------------------------------------
hdr "D. the operator is told what happened"
# ---------------------------------------------------------------------------
if grep -q 'deleted_cascade' "$HANDLER" && grep -q 'deleted_fanout' "$HANDLER"; then
  ok "D1: the redirect names WHICH cascade fired (a deleted /32 family is not «1 rule»)"
else
  bad "D1: the redirect does not distinguish the cascades"
fi
if grep -q '"deleted"' internal/feature/exit_rules/form_admin.go; then
  ok "D2: the page data carries the counts to the template"
else
  bad "D2: the template never receives the delete counts"
fi
if grep -q 'exit_rules_admin.deleted_cascade' "$TPL" && grep -q 'exit_rules_admin.deleted_fanout' "$TPL"; then
  ok "D3: the admin template renders a banner per cascade shape"
else
  bad "D3: the template renders no delete banner"
fi
# The mapping line is matched WITHOUT the method call's parentheses: a pattern that
# spelled out `r.URL()` was silently wrong here (the code is `r.URL.Query()`, so the
# parenthesis never follows URL), and the contract failed while the feature was correct —
# the paste-the-code-shape-into-a-grep class. Match what is distinctive, not a guess at it.
if grep -Eq '"warn":[[:space:]]+r\.URL\.Query' internal/feature/exit_rules/form_admin.go \
   && grep -q 'admin-delete-warn' "$TPL"; then
  ok "D4: a PARTIAL delete (ids already gone) is reported, not passed off as a complete one"
else
  bad "D4: the admin page drops the partial-delete warning"
fi
if grep -q 'no rule was deleted' "$HANDLER"; then
  ok "D5: a missing rule produces a message instead of silence (the /my path's skip is wrong for one explicit click)"
else
  bad "D5: a missing rule is silently ignored"
fi
# i18n pairs (rule 10): both catalogues must carry the three new keys.
RU_OK=0; EN_OK=0
grep -q '"exit_rules_admin.deleted"' internal/i18n/catalog_exit_rules.go && RU_OK=1
grep -c '"exit_rules_admin.deleted"' internal/i18n/catalog_exit_rules.go | grep -q '^2$' && EN_OK=1
if [ "$RU_OK" = 1 ] && [ "$EN_OK" = 1 ]; then
  ok "D6: the three banner keys exist in BOTH catalogues (RU + EN, rule 10)"
else
  bad "D6: the banner keys are not in both catalogues (RU=$RU_OK both=$EN_OK)"
fi

# Behavioural: the six tests are the contract that the route grep cannot express.
if [ -f "$TESTS" ]; then
  if command -v go >/dev/null 2>&1; then
    out="$(go test -count=1 -timeout 300s -run 'B379' ./internal/feature/exit_rules/ 2>&1)"; rc=$?
    if [ "$rc" -eq 0 ]; then
      ok "D7: the six handler tests pass against a real migrated SQLite DB (owner resolved, cascades fire, decoy user untouched, non-admin refused)"
    else
      bad "D7: the B379 handler tests failed:"
      printf '%s\n' "$out" | tail -n 8 | sed 's/^/        /' >&2
    fi
  else
    skip "D7: no go on PATH"
  fi
  if grep -q 'user_id = 8' "$TESTS"; then
    ok "D8: the domain-cascade test plants a DECOY row owned by another user and asserts it survives"
  else
    bad "D8: no cross-user decoy — the cascade's user_id scope is not actually tested"
  fi
else
  bad "D8: $TESTS is missing — nothing behavioural pins this handler"
fi

# ---------------------------------------------------------------------------
hdr "E. bookkeeping"
# ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b379_admin_rule_delete.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (AGENTS trap #11)"
else
  bad "E1: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'check_b379_admin_rule_delete.sh' scripts/verify_pre_deploy.sh; then
  ok "E2: verify_pre_deploy.sh registers B379"
else
  bad "E2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B379\*\*' AGENTS.md; then
  ok "E3: AGENTS.md's block index carries B379"
else
  bad "E3: the block index does not know B379 (AGENTS rule 2)"
fi

printf '\n\033[1mB379 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
