#!/bin/bash
# check_b_issue_2.sh — admin can add exit-rules for another user's devices
# (Issue #2, lamblador/Daniil 2026-09-11)
#
# Pre-fix (operator report):
#   /admin/exit-rules was a view-only page (Re-apply + sync +
#   rollback + cleanup), with no Add form. Admin couldn't add
#   a rule for daniil's `workpc` from the admin context.
#
# Fix (commits a0c66235 + c60f0965):
#   - New PostAdminExitRule handler in internal/feature/exit_rules/form_admin.go
#     - IsAdmin gate (defense-in-depth)
#     - validates form fields (user_id, device_id, exit_node,
#       target_type, target_value, action)
#     - validates target user exists in portal_users
#     - validates device is owned by TARGET user (not admin)
#     - rejects exit-nodes (routing infra)
#     - IP/CIDR validation + DNS resolve for domain
#     - per-user / per-device / total rule limits
#     - audit row action=admin_add_exit_rule_for_user
#   - POST /admin/exit-rules route wired in cmd/skygate/main.go
#   - Add form rendered on /admin/exit-rules template
#   - 10 new i18n keys in catalog_exit_rules.go (RU + EN)
#
# This script pins the 12 contracts that prove the fix is
# in place. Run via scripts/verify_pre_deploy.sh (which
# auto-discovers check_b_*.sh + check_b_issue_*.sh).
#
# 2026-09-11: v1.5.4 — first cut, Issue #2 close-out.

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: PostAdminExitRule handler exists ==="
# The handler must be a method on *Service (not a free function)
# so the test file can mock the Backend interface.
if grep -qE 'func \(s \*Service\) PostAdminExitRule' internal/feature/exit_rules/form_admin.go; then
    ok "PostAdminExitRule handler defined on *Service"
else
    bad "PostAdminExitRule handler MISSING"
fi

echo "=== contract B: IsAdmin gate inside the handler ==="
# The handler must do an IsAdmin check even though authMW
# is in front of the route — defense-in-depth so a future
# router refactor can't bypass the admin-only contract.
if grep -qE 'if !c\.IsAdmin' internal/feature/exit_rules/form_admin.go; then
    ok "IsAdmin gate present (defense-in-depth)"
else
    bad "IsAdmin gate MISSING — defense-in-depth violated"
fi

echo "=== contract C: POST /admin/exit-rules route wired ==="
if grep -qE 'mux\.Handle\("POST /admin/exit-rules", authMW\(http\.HandlerFunc\(exitRulesSvc\.PostAdminExitRule\)\)\)' cmd/skygate/main.go; then
    ok "POST /admin/exit-rules route wired"
else
    bad "POST /admin/exit-rules route NOT wired in cmd/skygate/main.go"
fi

echo "=== contract D: ownership check uses TARGET user, not admin ==="
# The whole point of Issue #2 is that admin can add rules for
# another user's device. The handler must look up ownership
# via the TARGET username (from form user_id), NOT the admin
# session username.
if grep -qE 'db\.CountNodeOwnerByNodeUser\(s\.dbc\(\), strconv\.Itoa\(devID\), targetUserName\)' internal/feature/exit_rules/form_admin.go; then
    ok "ownership check uses targetUserName (not admin session)"
else
    bad "ownership check must use targetUserName, not admin's session"
fi

echo "=== contract E: rule is inserted with TARGET user_id ==="
# The rule's device_rules.user_id must be the TARGET user
# (not the admin session), so headscale ACL src = target.
if grep -qE 's\.insertRuleUnique\(int64\(uid\),' internal/feature/exit_rules/form_admin.go; then
    ok "insertRuleUnique called with int64(uid) = target user"
else
    bad "rule insert must use target uid, not admin's UserID"
fi

echo "=== contract F: audit row action=admin_add_exit_rule_for_user ==="
# Operator needs to see which admin added the rule on whose
# behalf. Generic PostMyExitRule's audit action would lose
# this info.
if grep -qE 'admin_add_exit_rule_for_user' internal/feature/exit_rules/form_admin.go; then
    ok "audit row uses admin_add_exit_rule_for_user action"
else
    bad "audit action must be admin_add_exit_rule_for_user (not generic)"
fi

echo "=== contract G: pure helper validateAdminRuleForm exists ==="
# Pure validator (no DB) so the test file can pin the
# required-field logic without spinning up sqlmock.
if grep -qE 'func validateAdminRuleForm' internal/feature/exit_rules/form_admin.go; then
    ok "validateAdminRuleForm helper exists"
else
    bad "validateAdminRuleForm helper MISSING"
fi

echo "=== contract H: pure helper buildAdminExitRuleRedirectURL exists ==="
# B237.19 mirror — carries user_id + form_* + errMsg so the
# form re-renders with the user's typed values preserved.
if grep -qE 'func buildAdminExitRuleRedirectURL' internal/feature/exit_rules/form_admin.go; then
    ok "buildAdminExitRuleRedirectURL helper exists"
else
    bad "buildAdminExitRuleRedirectURL helper MISSING"
fi

echo "=== contract I: Add form rendered on /admin/exit-rules ==="
# Template must have the form post-to-/admin/exit-rules + the
# 10 new i18n keys used (each at least once in the template).
TPL=internal/handlers/templates/admin/exit_rules.html
if grep -qE 'method="post" action="/admin/exit-rules"' "$TPL"; then
    ok "Add form posts to /admin/exit-rules"
else
    bad "Add form POST action /admin/exit-rules MISSING in $TPL"
fi
for key in add_form_title add_form_user_id add_form_device_id add_form_exit_node add_form_target_type add_form_target_value add_form_action add_form_submit add_form_help add_form_applied; do
    if ! grep -qE "exit_rules_admin\.${key}" "$TPL"; then
        bad "template missing i18n key exit_rules_admin.${key}"
    fi
done
ok "all 10 i18n keys referenced in template"

echo "=== contract J: i18n keys present in BOTH RU and EN ==="
# RU + EN parity — same key set on both sides. The
# catalog_exit_rules.go file has 2 maps (ruExitRules +
# enExitRules). Both must have the new keys.
RU_FILE=internal/i18n/catalog_exit_rules.go
for key in add_form_title add_form_user_id add_form_device_id add_form_exit_node add_form_target_type add_form_target_value add_form_action add_form_submit add_form_help add_form_applied; do
    # Count occurrences — must be exactly 2 (one in RU map + one in EN map)
    count=$(grep -cE "\"exit_rules_admin\.${key}\"" "$RU_FILE" || true)
    if [ "$count" != "2" ]; then
        bad "i18n key exit_rules_admin.${key} appears ${count} times (want 2 — RU + EN)"
    fi
done
ok "all 10 i18n keys present in both RU + EN maps"

echo "=== contract K: test file pins the pure helpers ==="
# Test file must pin buildAdminExitRuleRedirectURL +
# validateAdminRuleForm. If a future refactor renames or
# removes these helpers, this contract catches it.
TEST=internal/feature/exit_rules/form_admin_b2_test.go
for fn in TestBuildAdminExitRuleRedirectURL_BasicShape TestBuildAdminExitRuleRedirectURL_SpecialChars TestValidateAdminRuleForm_RequiredFields TestValidateAdminRuleForm_TargetTypeAllowed TestPostAdminExitRule_IsNotMyPath; do
    if ! grep -qE "^func ${fn}" "$TEST"; then
        bad "test ${fn} MISSING in $TEST"
    fi
done
ok "all 5 regression tests present"

echo "=== contract L: AGENTS.md / install-dry-run-report mention Issue #2 ==="
# Closure documentation. The issue close-out commits
# (a0c66235 + c60f0965) reference gh issue #2 directly in
# their commit messages — verify the commit-message contract.
# Use a tmp file (avoids pipefail + grep -q + if interactions
# where the pipeline can short-circuit before grep finishes).
TMP_LOG=$(mktemp)
git log --oneline -20 > "$TMP_LOG"
if grep -qiE 'Issue #2|admin.*exit.rule.*another user' "$TMP_LOG"; then
    ok "commit log mentions Issue #2 closure"
else
    bad "no recent commit references Issue #2 — close-out commit missing"
fi
rm -f "$TMP_LOG"

echo ""
echo "All 12 contracts PASS. Issue #2 closure verified."
