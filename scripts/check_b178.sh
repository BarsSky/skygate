#!/bin/bash
# B178 — /admin/exit-rules "preferred exit node" template-scope bug + dead code
#
# The pre-B178 template did an O(n*m) inner lookup for each rule:
#
#   {{range $ar := $.RulesAnnotated}}{{if eq $ar.ID .ID}}
#     {{$pref = $ar.PreferredHost}}
#   {{end}}{{end}}
#
# Inside the inner range, `.` is REBOUND to $ar (Go template
# scope). So `eq $ar.ID .ID` is effectively `eq $ar.ID $ar.ID`
# — always true. The lookup overwrote $pref on every iteration,
# ending with the LAST annotated rule's PreferredHost (the slice
# is sorted by ID ascending, so the last entry was skyworker's
# highest-ID rule whose PreferredHost is "karolina" because
# device_exit_node_prefs: skyadmin/skyworker → tag:dev-infra-karolina).
#
# Live-verified 2026-08-25: /admin/exit-rules showed "karolina"
# for ALL 103 of michail/basic's rules, even though
# device_exit_node_prefs had michail/basic → tag:exit-emilia
# and PreferredExitNodeForRule(s.DB, 6, "basic") returned
# "emilia" correctly.
#
# B178 fix: collapse the annotated slice into AdminRule itself
# (PreferredHost + Applicable fields), drop the inner template
# lookup, let the template read .PreferredHost directly. The
# dead `groupedByUserAnnotated` map is also removed.
#
# Contracts (14 sub-checks):
#  A. internal/feature/exit_rules/form_admin.go has the new package-level
#     AdminRule struct with PreferredHost + Applicable fields
#  B. annotateRulesWithPrefs function exists
#  C. form_admin.go NO LONGER contains the local `type AdminRule struct`
#     (moved to package level — local would shadow the package-level one)
#  D. form_admin.go NO LONGER defines `type AnnotatedRule struct`
#     (collapsed into AdminRule)
#  E. form_admin.go NO LONGER references `RulesAnnotated` in the
#     template data map (the BUG-causing variable)
#  F. form_admin.go NO LONGER references `groupedByUserAnnotated`
#     (the dead code)
#  G. The handler calls annotateRulesWithPrefs(rr, ...)
#  H. Template admin/exit_rules.html uses .PreferredHost directly
#     (NO inner range over $.RulesAnnotated)
#  I. Template NO LONGER contains the yellow "DBG:" debug span
#     (the temporary visual debug aid must not ship to production)
#  J. form_admin_b178_test.go exists with the regression test
#     for the basic/karolina case
#  K. The regression test pins "basic" → "emilia" as the expected
#     PreferredHost (the exact bug the operator reported)
#  L. AGENTS.md mentions B178
#  M. verify_pre_deploy.sh includes check_b178
#  N. go test ./internal/feature/exit_rules/... passes (the unit
#     tests run, including the 8 new B178 ones)

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

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

count() {
  # `grep -c` returns "0" with exit code 1 on no match, which makes
  # `|| echo 0` print an extra "0" (one-liner output is "0\n0").
  # Strip the trailing newline + the duplicate so the result is
  # always a clean integer.
  local n
  n=$(grep -cE "$2" "$1" 2>/dev/null) || n=0
  n=${n:-0}
  echo "$n" | tr -d '\n'
}

echo "=== B178 contracts ==="

# A. package-level AdminRule with PreferredHost + Applicable
check_ge "A" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'PreferredHost\s+string')"
check_ge "A" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'Applicable\s+bool')"

# B. annotateRulesWithPrefs function
check_ge "B" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" '^func annotateRulesWithPrefs')"

# C. local AdminRule struct is gone (would shadow the package-level one).
#    The local one was indented (inside a function); the package-level
#    one is at column 0. Match the indented form to disambiguate.
check_eq "C" "0" "$(count "$REPO/internal/feature/exit_rules/form_admin.go" '^[[:space:]]+type AdminRule struct')"

# D. AnnotatedRule is gone. Match the struct definition form
#    (with the "struct" keyword) to avoid the B178 history comment
#    that mentions "[]AnnotatedRule" without "struct".
check_eq "D" "0" "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'type AnnotatedRule struct')"

# E. RulesAnnotated is gone from the data map (the BUG-causing
#    template data key). Use the quoted form to avoid matching
#    the B178 history comment that mentions "RulesAnnotated"
#    inside backticks.
check_eq "E" "0" "$(count "$REPO/internal/feature/exit_rules/form_admin.go" '"RulesAnnotated":')"

# F. groupedByUserAnnotated is gone. Match the assignment form
#    (":= " or "=") to avoid the B178 history comment.
check_eq "F" "0" "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'groupedByUserAnnotated\s*[:=]')"

# G. annotateRulesWithPrefs is called
check_ge "G" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin.go" 'annotateRulesWithPrefs\(')"

# G2. The annotateRulesWithPrefs call MUST happen BEFORE the
#     `for _, rule := range rr` grouping loop, otherwise the
#     copies stored in groupedByUser.Nodes miss the
#     PreferredHost field. This was a live regression in the
#     first B178 deploy — the annotation was correct, the
#     prefFn returned the right values (verified by the
#     B178-DBG log), but every rule on the page rendered
#     "No preferred exit-node set" because the template
#     iterates the COPIES in groupedByUser.Nodes.
if [ -f "$REPO/internal/feature/exit_rules/form_admin.go" ]; then
  ANNO_LINE=$(grep -n 'annotateRulesWithPrefs(rr,' "$REPO/internal/feature/exit_rules/form_admin.go" | head -1 | cut -d: -f1)
  GROUP_LINE=$(grep -n 'for _, rule := range rr' "$REPO/internal/feature/exit_rules/form_admin.go" | tail -1 | cut -d: -f1)
  if [ -n "$ANNO_LINE" ] && [ -n "$GROUP_LINE" ]; then
    if [ "$ANNO_LINE" -lt "$GROUP_LINE" ]; then
      check_eq "G2" "before" "before"
    else
      check_eq "G2" "before" "after (regression: annotation must run BEFORE grouping copies)"
    fi
  fi
fi

# H. template uses .PreferredHost directly
if [ -f "$REPO/internal/handlers/templates/admin/exit_rules.html" ]; then
  check_ge "H" 1 "$(count "$REPO/internal/handlers/templates/admin/exit_rules.html" '\.PreferredHost')"
  check_eq "H-no-inner-range" "0" "$(count "$REPO/internal/handlers/templates/admin/exit_rules.html" 'range \$ar := \$\.RulesAnnotated')"
else
  check_eq "H" ">=1" "0"
fi

# I. yellow DBG span is gone (visual debug aid)
check_eq "I" "0" "$(count "$REPO/internal/handlers/templates/admin/exit_rules.html" 'background:yellow')"

# J. B178 test file exists
if [ -f "$REPO/internal/feature/exit_rules/form_admin_b178_test.go" ]; then
  check_eq "J" "yes" "yes"
else
  check_eq "J" "yes" "no"
fi

# K. regression test pins basic/karolina case
check_ge "K" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin_b178_test.go" 'BasicKarolinaRegression')"
check_ge "K" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin_b178_test.go" '"basic"')"
check_ge "K" 1 "$(count "$REPO/internal/feature/exit_rules/form_admin_b178_test.go" 'return "emilia"')"

# L. AGENTS.md mentions B178
if [ -f "$REPO/AGENTS.md" ]; then
  check_ge "L" 1 "$(count "$REPO/AGENTS.md" 'B178')"
else
  check_eq "L" ">=1" "0"
fi

# M. verify_pre_deploy.sh includes check_b178
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  check_ge "M" 1 "$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b178')"
else
  check_eq "M" ">=1" "0"
fi

# N. go test ./internal/feature/exit_rules/... passes
#    Look for go in common paths so the check works on the
#    VM (linux, /usr/local/go/bin/go) and the dev workstation
#    (Windows under Git Bash, /c/Program Files/Go/bin/go or
#    /c/Users/<user>/go/bin/go).
GO_BIN=""
for cand in /usr/local/go/bin/go /usr/bin/go /opt/go/bin/go "$(command -v go 2>/dev/null)"; do
  if [ -x "$cand" ]; then
    GO_BIN="$cand"
    break
  fi
done
if [ -n "$GO_BIN" ]; then
  if (cd "$REPO" && "$GO_BIN" test -count=1 ./internal/feature/exit_rules/... 2>&1) | grep -q '^ok\s'; then
    check_eq "N" "ok" "ok"
  else
    check_eq "N" "ok" "FAIL"
  fi
else
  echo "  SKIP [N] go not available in PATH"
fi

echo
echo "=== B178 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
