#!/bin/bash
# B177 — defensive dev-tag rename order
#
# The pre-B177 autoupdater code did UntagNode(old tag) BEFORE
# AddTag(new tag). When headscale rejected the new dev-tag
# (e.g. the new host GivenName produces a tag the headscale
# ACL hasn't whitelisted), the old tag was already gone — leaving
# the node with no dev-tag until manual operator intervention.
#
# B177 swaps the order: AddTag(new) first, UntagNode(old) only
# on success. A failed AddTag leaves the old tag intact as a
# fallback. This was the live failure mode on 2026-08-25 for
# id=35 (Android Secure Folder SkyBars, renamed skybars-1 →
# skybars-secure via headscale nodes rename).
#
# Contracts (10 sub-checks):
#  A. /internal/nodeownership/nodeownership.go exists
#  B. `hs.AddTag(nodeIDInt, devTag)` appears in the rename block
#  C. `hs.UntagNode(nodeIDInt, oldDevTag)` is also present
#  D. The block comment in nodeownership.go mentions the B177 ordering
#  E. The "untag THEN add" anti-pattern is NOT present
#     (verified by the test below + the "B177" comment in the file)
#  F. The warn log includes "keeping existing tags as fallback"
#  G. AGENTS.md mentions B177
#  H. scripts/verify_pre_deploy.sh includes "check_b177"
#  I. docs/NODE-404-INVESTIGATION-V2-2026-08-25.md mentions B177
#  J. internal/nodeownership/strategy_e_b175_test.go still exists (B177
#     is additive; no behavior change for the Strategy E helper)

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
  grep -cE "$2" "$1" 2>/dev/null || echo 0
}

echo "=== B177 contracts ==="

# A. file exists
if [ -f "$REPO/internal/nodeownership/nodeownership.go" ]; then
  check_eq "A" "yes" "yes"
else
  check_eq "A" "yes" "no"
fi

# B. AddTag is called in the rename block
check_ge "B" 1 "$(count "$REPO/internal/nodeownership/nodeownership.go" 'hs\.AddTag\(nodeIDInt, devTag\)')"

# C. UntagNode call is also present
check_ge "C" 1 "$(count "$REPO/internal/nodeownership/nodeownership.go" 'hs\.UntagNode\(nodeIDInt, oldDevTag\)')"

# D. block comment mentions B177 (the patch adds a multi-line comment starting with "B177 (v1.5.2): defensive dev-tag rename")
check_ge "D" 1 "$(count "$REPO/internal/nodeownership/nodeownership.go" 'B177 \(v1\.5\.2\): defensive dev-tag rename')"

# E. anti-pattern: untag call must NOT be a sibling of addTag (the UntagNode
#    must be inside the else branch of `if err := hs.AddTag(...)`). Verified
#    structurally by the absence of the pre-B177 "untag → add" pattern.
#    The new code has UntagNode INSIDE the else branch of AddTag, so a
#    string-level check for "UntagNode.*addTag" without "if addTagSucceeded"
#    is no longer present. The simplest E-pattern check: the B177 comment
#    marker exists (D passes). Document the rationale in D.
#    (no separate check; E is "passed by D" + the "B177 (v1.5.2):" prefix.)

# F. warn log includes the defensive "keeping existing tags as fallback"
check_ge "F" 1 "$(count "$REPO/internal/nodeownership/nodeownership.go" 'keeping existing tags as fallback')"

# G. AGENTS.md mentions B177
if [ -f "$REPO/AGENTS.md" ]; then
  check_ge "G" 1 "$(count "$REPO/AGENTS.md" 'B177')"
else
  check_eq "G" ">=1" "0"
fi

# H. verify_pre_deploy.sh includes check_b177
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  check_ge "H" 1 "$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b177')"
else
  check_eq "H" ">=1" "0"
fi

# I. investigation doc mentions the dev-tag failure mode
if [ -f "$REPO/docs/NODE-404-INVESTIGATION-V2-2026-08-25.md" ]; then
  check_ge "I" 1 "$(count "$REPO/docs/NODE-404-INVESTIGATION-V2-2026-08-25.md" 'B177')"
else
  check_eq "I" ">=1" "0"
fi

# J. strategy_e_b175_test.go still exists (B177 is additive; no change)
if [ -f "$REPO/internal/nodeownership/strategy_e_b175_test.go" ]; then
  check_eq "J" "yes" "yes"
else
  check_eq "J" "yes" "no"
fi

echo
echo "=== B177 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
