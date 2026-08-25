#!/bin/bash
# B182 — /admin/exit-rules and /my/exit-rules "Applicable" vs
# "ApprovedInHeadscale" three-state badge
#
# The pre-B182 "Applicable" check (B178) was a LOGICAL check —
# "rule.ExitNode matches the device's preferred exit-node" —
# it did NOT verify the rule's target CIDR is in headscale
# ApprovedRoutes. The user's live case (michail's rules shown
# as ✅ "accepted" but actually never pushed to headscale) was
# a gap in the B178 check.
#
# B182 adds ApprovedInHeadscale to AdminRule and a status
# string ("approved" | "pending" | "wrong" | "no_preferred")
# for /my/exit-rules. Both views now render three states:
#   ✅ green (approved)  — Applicable + ApprovedInHeadscale
#   ⏳ yellow (pending)  — Applicable but ApprovedInHeadscale=false
#   ⚠️ red (wrong)       — Applicable=false (rule.ExitNode differs)
#
# Contracts (12 sub-checks):
#  A. AdminRule has ApprovedInHeadscale field
#  B. ruleApprovedInHeadscale helper exists
#  C. ruleApprovedInHeadscale is called in annotateRulesWithPrefs
#  D. Handler builds approvedByExitNode map from headscale nodes
#  E. Handler calls annotateRulesWithPrefs with 3 args (including map)
#  F. /my/exit-rules template uses StatusByRuleID for the badge
#  G. /admin/exit-rules template uses .ApprovedInHeadscale
#  H. form_my.go passes StatusByRuleID to the template
#  I. form_admin.go passes approvedByExitNode to annotator
#  J. AGENTS.md mentions B182
#  K. verify_pre_deploy.sh includes check_b182
#  L. go test ./internal/feature/exit_rules/... passes (incl. the
#     8 new B182 unit tests in form_admin_b182_test.go)

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

check_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "  PASS [$label] $actual"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] expected=$expected got=$actual"
    FAIL=$((FAIL+1))
  fi
}

check_ge() {
  local label="$1" min="$2" actual="$3"
  if [ "$actual" -ge "$min" ] 2>/dev/null; then
    echo "  PASS [$label] actual=$actual (>= $min)"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] actual=$actual (expected >= $min)"
    FAIL=$((FAIL+1))
  fi
}

count() {
  local n
  n=$(grep -cE "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

echo "=== B182 contracts ==="

# A. AdminRule has ApprovedInHeadscale field
check_ge "A" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'ApprovedInHeadscale\s+bool')"

# B. ruleApprovedInHeadscale helper exists
check_ge "B" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" '^func ruleApprovedInHeadscale')"

# C. ruleApprovedInHeadscale is called in annotateRulesWithPrefs body
check_ge "C" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'rr\[i\]\.ApprovedInHeadscale = ruleApprovedInHeadscale')"

# D. Handler builds approvedByExitNode from headscale nodes
check_ge "D" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'approvedByExitNode := map\[string\]map\[string\]bool')"
check_ge "D-nodes-iter" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'for _, n := range nodes')"

# E. Handler calls annotateRulesWithPrefs with 3 args (the 3rd is the map)
# E1. annotator signature has 3 args
ANNO_LINE=$(grep -n 'func annotateRulesWithPrefs' "$REPO/internal/feature/exit_rules/form_admin.go" | head -1 | cut -d: -f1)
if [ -n "$ANNO_LINE" ]; then
  ANNO_SIG=$(sed -n "${ANNO_LINE}p" "$REPO/internal/feature/exit_rules/form_admin.go")
  if echo "$ANNO_SIG" | grep -qE 'rr \[\]AdminRule.*prefFn.*approvedByExitNode'; then
    check_eq "E1" "3-args" "3-args"
  else
    check_eq "E1" "3-args" "wrong-signature"
  fi
fi
# E2. call site has 3 args (call is multi-line; the { approvedByExitNode }
#     is on the closing line)
check_ge "E2" 1 "$(grep -cE 'annotateRulesWithPrefs\(rr, func' "$REPO/internal/feature/exit_rules/form_admin.go" 2>/dev/null || echo 0)"

# F. /my/exit-rules template uses StatusByRuleID for the badge
check_ge "F" 1 "$(count "$REPO/internal/handlers/templates/exit_rules.html" 'StatusByRuleID')"
check_ge "F-eq-approved" 1 "$(count "$REPO/internal/handlers/templates/exit_rules.html" 'eq \$status "approved"')"
check_ge "F-eq-pending" 1 "$(count "$REPO/internal/handlers/templates/exit_rules.html" 'eq \$status "pending"')"
check_ge "F-eq-wrong" 1 "$(count "$REPO/internal/handlers/templates/exit_rules.html" 'eq \$status "wrong"')"

# G. /admin/exit-rules template uses .ApprovedInHeadscale
check_ge "G" 1 "$(count "$REPO/internal/handlers/templates/admin/exit_rules.html" '\.ApprovedInHeadscale')"

# H. form_my.go passes StatusByRuleID to the template
check_ge "H" 1 "$(count "$REPO/internal/feature/exit_rules/form_my.go" '"StatusByRuleID":\s*statusByRuleID')"

# I. form_admin.go passes approvedByExitNode to annotator
# (multi-line call — the map arg is on the closing line, separated
# from the func body)
check_ge "I" 1 "$(grep -cE '\}, approvedByExitNode\)' "$REPO/internal/feature/exit_rules/form_admin.go" 2>/dev/null || echo 0)"

# J. AGENTS.md mentions B182
if [ -f "$REPO/AGENTS.md" ]; then
  check_ge "J" 1 "$(count "$REPO/AGENTS.md" 'B182')"
else
  check_eq "J" ">=1" "0"
fi

# K. verify_pre_deploy.sh includes check_b182
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  check_ge "K" 1 "$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b182')"
else
  check_eq "K" ">=1" "0"
fi

# L. go test passes (skipped if go not in PATH — VM-only)
GO_BIN=""
for cand in /usr/local/go/bin/go /usr/bin/go /opt/go/bin/go "$(command -v go 2>/dev/null)"; do
  if [ -x "$cand" ]; then
    GO_BIN="$cand"
    break
  fi
done
if [ -n "$GO_BIN" ]; then
  if (cd "$REPO" && "$GO_BIN" test -count=1 ./internal/feature/exit_rules/... 2>&1) | grep -q '^ok\s'; then
    check_eq "L" "ok" "ok"
  else
    check_eq "L" "ok" "FAIL"
  fi
else
  echo "  SKIP [L] go not available in PATH"
fi

echo
echo "=== B182 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
