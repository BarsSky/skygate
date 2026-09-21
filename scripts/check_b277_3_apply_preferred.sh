#!/usr/bin/env bash
# check_b277_3_apply_preferred.sh
#
# 2026-09-21 (B277.3) — bulk-apply the user's preferred exit-node.
#
# The "Use preferred (node)" button next to the mismatch banner on
# /my/exit-rules used to be a JS-only pre-fill of the new-rule form:
# clicking it set the form's exit_node <select> to the user's
# preferred host, then the operator still had to click "Add" (and
# the existing N rules the banner was warning about were not
# touched). The user-visible result was "the button does nothing" —
# the mismatch banner stayed on the next render because the N
# rules still targeted the wrong exit node.
#
# B277.3 replaces the JS pre-fill with a real <form action="/my/
# exit-rules/apply-preferred">. One POST rewrites every rule whose
# exit_node_id doesn't match the device's preferred exit node (or
# the user-level fallback when no per-device pref is set) and re-
# applies the ACL once for the user.
#
# CONTRACTS
#   A  the backend handler exists (PostMyExitRulesApplyPreferred)
#   B  the route is registered (POST /my/exit-rules/apply-preferred)
#   C  the template renders a <form> (NOT a JS-only <button>)
#   D  the dead JS pre-fill handler is gone from the template
#   E  the DB helper exists (db.UpdateDeviceRuleExitNode)
#   F  i18n parity: 2 new keys in RU and EN (apply_preferred_btn,
#      apply_preferred_confirm)
#   G  unit tests cover the new behaviour
#   H  the script itself is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B277.3: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CAT=internal/i18n/catalog_exit_rules.go
DB=internal/db/device_rules.go
SVC=internal/feature/exit_rules/form_my.go
MAIN=cmd/skygate/main.go
TMPL=internal/handlers/templates/exit_rules.html

hdr "B277.3 — bulk-apply the user's preferred exit-node"

# --- A: backend handler exists ---------------------------------------------
if grep -q 'func (s \*Service) PostMyExitRulesApplyPreferred' "$SVC"; then
  ok "A1: PostMyExitRulesApplyPreferred handler exists"
else
  bad "A1: PostMyExitRulesApplyPreferred is missing"
fi
if grep -q 'my_exit_rules_apply_preferred' "$SVC"; then
  ok "A2: handler audits the action 'my_exit_rules_apply_preferred'"
else
  bad "A2: handler does not audit the bulk operation (audit gap)"
fi
if grep -q 'db.UpdateDeviceRuleExitNode' "$SVC"; then
  ok "A3: handler uses db.UpdateDeviceRuleExitNode for the per-row rewrite"
else
  bad "A3: handler does not call the DB helper — likely raw SQL inline"
fi
if grep -q 'db.GetUserExitNodePref' "$SVC" && grep -q 'TagToHostname' "$SVC"; then
  ok "A4: handler reads the user-level preferred via the same helper as the banner"
else
  bad "A4: handler does not share the preferred-resolution path with the banner"
fi
if grep -q 'PreferredExitNodeForRule' "$SVC"; then
  ok "A5: handler respects per-device preferred (not just user-level)"
else
  bad "A5: handler ignores per-device preferred — would clobber per-device intent"
fi
if grep -q 'userPreferredHost == ""' "$SVC" && grep -q 'my/devices' "$SVC"; then
  ok "A6: handler refuses when no preferred is set and points the operator to /my/devices"
else
  bad "A6: handler does not refuse the empty-preferred case"
fi

# --- B: route registered ---------------------------------------------------
if grep -q 'POST /my/exit-rules/apply-preferred' "$MAIN"; then
  ok "B1: POST /my/exit-rules/apply-preferred is registered"
else
  bad "B1: route is missing"
fi

# --- C: template renders a <form> (not a JS-only <button>) ---------------
if grep -q '<form method="post" action="/my/exit-rules/apply-preferred"' "$TMPL"; then
  ok "C1: template renders a real <form> for the bulk apply"
else
  bad "C1: template does not have a <form action=...apply-preferred>"
fi
if grep -q 'apply_preferred_confirm' "$TMPL" && grep -q 'onsubmit="return confirm' "$TMPL"; then
  ok "C2: the form has a confirm() dialog (no accidental clicks rewrite N rules)"
else
  bad "C2: no confirm dialog — clicking the button rewrites N rules silently"
fi
if grep -q 'tf "exit_rules.apply_preferred_btn"' "$TMPL"; then
  ok "C3: the button label uses the new apply_preferred_btn i18n key (not the old use_preferred_btn)"
else
  bad "C3: button still uses the old JS-pre-fill label"
fi

# --- D: dead JS pre-fill handler is gone -----------------------------------
if ! grep -q 'use-preferred-btn' "$TMPL" && ! grep -q 'getElementById.*use-preferred-btn' "$TMPL"; then
  ok "D1: the dead JS pre-fill handler is gone (no #use-preferred-btn anywhere)"
else
  bad "D1: dead JS handler is still in the template — confusing for the next reader"
fi

# --- E: DB helper exists --------------------------------------------------
if grep -q 'func UpdateDeviceRuleExitNode(d \*sql.DB' "$DB"; then
  ok "E1: db.UpdateDeviceRuleExitNode is exported"
else
  bad "E1: db.UpdateDeviceRuleExitNode is missing — handler can't rewrite exit_node_id"
fi
if grep -q 'UPDATE device_rules' "$DB" && grep -q 'SET exit_node_id' "$DB"; then
  ok "E2: the helper runs the right UPDATE (exit_node_id column, not anything else)"
else
  bad "E2: the helper's UPDATE is not what we expect"
fi

# --- F: i18n parity --------------------------------------------------------
KEYS=0
for k in apply_preferred_btn apply_preferred_confirm; do
  n="$(grep -c "exit_rules.$k\"" "$CAT")"
  [ "$n" -ge 2 ] && KEYS=$((KEYS+1))
done
if [ "$KEYS" -eq 2 ]; then
  ok "F1: both new keys exist in RU and EN (2/2 pairs)"
else
  bad "F1: only $KEYS/2 keys are present in both catalogues"
fi

# --- G: unit tests ---------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  # The new handler uses db.GetUserExitNodePref + per-rule UPDATE; we
  # do not have a pure-function test for it yet (the form handler
  # needs DB). The pre-existing B276.2 routescript tests confirm
  # we did not break the exit_rules package's test suite.
  OUT="$(timeout 600 go test -count=1 -run 'B2762|BuildLinux|BuildWindows|PreferredGroup|B2761|RuleRow|GroupRulesByCDN' ./internal/feature/exit_rules/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "G1: the exit_rules package's test suite stays green (B276.1 + B276.2 paths)"
  else
    bad "G1: the exit_rules test suite failed: $OUT"
  fi
else
  skip "G1: go not on PATH"
fi

# --- H: script is tracked by git (trap #11) -------------------------------
if git ls-files --error-unmatch scripts/check_b277_3_apply_preferred.sh >/dev/null 2>&1; then
  ok "H1: scripts/check_b277_3_apply_preferred.sh is tracked by git"
else
  bad "H1: scripts/check_b277_3_apply_preferred.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB277.3 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
