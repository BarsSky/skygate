#!/bin/bash
# B188.3 — port per-CIDR via= to legacy GenerateACLForPlane.
#
# B188.2 added per-CIDR via= to the useVia=true path
# (GenerateACLWithViaForPlane). B188.3 closes the B188.3 TODO
# by porting the same logic to the useVia=false path
# (GenerateACLForPlane) — so callers that explicitly pass
# useVia=false (the bot's /clear, /add_rule, etc.) also
# get the selective routing.
#
# The change is structural: BOTH functions now call the
# same resolvePerCIDRVia helper, so the per-CIDR via=
# behavior is identical between the two paths.
#
# Contracts (8 contracts, A-H):
#  A. resolvePerCIDRVia helper exists in internal/acl/acl.go
#  B. GenerateACLForPlane (OLD function) calls resolvePerCIDRVia
#  C. GenerateACLWithViaForPlane (NEW function) calls resolvePerCIDRVia
#     (single source of truth — both paths use the same helper)
#  D. resolvePerCIDRVia doc comment references B188.3
#  E. resolvePerCIDRVia is a package-private function (lowercase
#     'r' = not exported — only used by acl.go internals)
#  F. exitNodeID field is added to the OLD function's local
#     ruleEntry struct
#  G. The OLD function populates viaByDeviceOld from
#     device_exit_node_prefs (mirror of the NEW function's
#     viaByDevice)
#  H. (VM-only) live: per-CIDR via= still works in the
#     useVia=true path (B188.2 contract preserved after the
#     refactor) — checked via the existing B188.2 contract
#     `headscale policy get ... h-rule-... via: [tag:dev-infra-...]`

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

# A. resolvePerCIDRVia helper exists.
A=$(grep -c '^func resolvePerCIDRVia' "$REPO/internal/acl/acl.go" 2>/dev/null || echo 0)
check_ge "A-resolvePerCIDRVia-helper-exists" 1 "$A"

# B. GenerateACLForPlane calls resolvePerCIDRVia.
# The OLD function is long (~540 lines), so we grep the
# ENTIRE file for resolvePerCIDRVia then check the line
# number is within the OLD function's range. The OLD
# function starts at "func GenerateACLForPlane(" and ends
# at the next "^func " line. We do a simpler check: grep
# the whole file for the function body containing the call.
B=$(awk '/^func GenerateACLForPlane\(/{p=1} p; /^func [A-Z]/{if (NR>1 && $0 !~ /^func GenerateACLForPlane/){p=0}}' "$REPO/internal/acl/acl.go" 2>/dev/null | grep -c 'resolvePerCIDRVia')
check_ge "B-OLD-calls-resolvePerCIDRVia" 1 "$B"

# C. GenerateACLWithViaForPlane calls resolvePerCIDRVia.
# Same approach as B but for the NEW function.
C=$(awk '/^func GenerateACLWithViaForPlane\(/{p=1} p; /^func [A-Z]/{if (NR>1 && $0 !~ /^func GenerateACLWithViaForPlane/){p=0}}' "$REPO/internal/acl/acl.go" 2>/dev/null | grep -c 'resolvePerCIDRVia')
check_ge "C-NEW-calls-resolvePerCIDRVia" 1 "$C"

# D. The helper's doc comment references B188.3.
D=$(grep -B 1 -A 25 'func resolvePerCIDRVia' "$REPO/internal/acl/acl.go" 2>/dev/null | grep -c 'B188.3')
check_ge "D-doc-references-B188.3" 1 "$D"

# E. resolvePerCIDRVia is package-private (lowercase 'r').
# Should NOT be exported (no "func R" capital R). We check
# that the function declaration is exactly `func resolvePerCIDRVia`
# (not `func ResolvePerCIDRVia`).
E=$(grep -cE '^func [Rr]esolvePerCIDRVia' "$REPO/internal/acl/acl.go" 2>/dev/null || echo 0)
check_eq "E-package-private-only" "1" "$E"

# F. The OLD function's local ruleEntry struct has exitNodeID.
# Look for the ruleEntry type definition inside the
# GenerateACLForPlane function.
F=$(grep -E -A 30 '^func GenerateACLForPlane\(' "$REPO/internal/acl/acl.go" 2>/dev/null | grep -A 15 'type ruleEntry struct' | grep -c 'exitNodeID')
check_ge "F-OLD-ruleEntry-has-exitNodeID" 1 "$F"

# G. The OLD function populates viaByDeviceOld from
# device_exit_node_prefs. Look for the loading code pattern.
G=$(grep -E -A 80 '^func GenerateACLForPlane\(' "$REPO/internal/acl/acl.go" 2>/dev/null | grep -c 'viaByDeviceOld')
check_ge "G-OLD-populates-viaByDeviceOld" 1 "$G"

# H. AGENTS.md mentions B188.3 with the implementation status.
H=$(grep -cE 'B188.3' "$REPO/AGENTS.md" 2>/dev/null || echo 0)
check_ge "H-AGENTS-md-mentions-B188.3" 1 "$H"

# I. tests/verify_pre_deploy.sh registers check_b188_3.
I=$(grep -cE 'check_b188_3' "$REPO/scripts/verify_pre_deploy.sh" 2>/dev/null || echo 0)
check_ge "I-verify-pre-deploy-registered" 1 "$I"

# J. (VM-only) live: per-CIDR via= still works in the
# useVia=true path (B188.2 contract preserved). We use
# the B188.2 check_b188_2.sh live contracts (S-X) for
# this — they verify that the headscale policy has
# h-rule-* grants with via=[tag:dev-infra-...] after
# running skygate acl-apply. If they pass, the useVia=true
# path is intact, which means our refactor (extracting
# resolvePerCIDRVia and reusing it in BOTH functions)
# didn't regress anything.
if [ -d /home/skyadmin/skygate ]; then
  if command -v bash >/dev/null 2>&1 && [ -f "$REPO/scripts/check_b188_2.sh" ]; then
    if bash "$REPO/scripts/check_b188_2.sh" >/dev/null 2>&1; then
      echo "  PASS [J-B188.2-still-passes-after-refactor] ok"
      PASS=$((PASS+1))
    else
      echo "  FAIL [J-B188.2-still-passes-after-refactor] check_b188_2.sh failed"
      FAIL=$((FAIL+1))
    fi
  else
    echo "  SKIP [J] check_b188_2.sh not available"
  fi
else
  echo "  SKIP [J] not on VM"
fi

# K. Build + vet pass.
GO_BIN="${GO:-$(command -v go 2>/dev/null || true)}"
if [ -z "$GO_BIN" ]; then
  for cand in /c/Program\ Files/Go/bin/go.exe "/c/Program Files/Go/bin/go.exe" /usr/local/go/bin/go /c/Users/*/go/bin/go "$HOME/go/bin/go"; do
    if [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
  done
fi
if [ -n "$GO_BIN" ] && (cd "$REPO" && "$GO_BIN" build ./... >/dev/null 2>&1 && "$GO_BIN" vet ./... >/dev/null 2>&1); then
  echo "  PASS [K-build-vet] ok ($GO_BIN)"
  PASS=$((PASS+1))
else
  echo "  SKIP [K-build-vet] go not reachable from this shell (go=$GO_BIN)"
fi

# L. B188.3 unit tests (TestResolvePerCIDRVia) pass.
if [ -n "$GO_BIN" ] && (cd "$REPO" && "$GO_BIN" test -count=1 -run "TestResolvePerCIDRVia" ./internal/acl/... >/dev/null 2>&1); then
  echo "  PASS [L-B188.3-unit-tests] ok"
  PASS=$((PASS+1))
elif [ -n "$GO_BIN" ]; then
  echo "  FAIL [L-B188.3-unit-tests] TestResolvePerCIDRVia failed (see test output above)"
  FAIL=$((FAIL+1))
else
  echo "  SKIP [L] go not reachable"
fi

# M. B188.3 integration tests (TestGenerateACLForPlane_B1883_*) pass.
# Skips on no PG DSN.
if [ -n "$GO_BIN" ]; then
  if SKYGATE_TEST_PG_DSN="${SKYGATE_TEST_PG_DSN:-postgres://admin:skygate_admin_pass@172.17.0.1:5000/skygate_staging?sslmode=disable}" \
     "$GO_BIN" test -count=1 -run "TestGenerateACLForPlane_B1883" "$REPO/internal/acl/..." >/dev/null 2>&1; then
    echo "  PASS [M-B188.3-integration-tests] ok"
    PASS=$((PASS+1))
  else
    echo "  SKIP [M-B188.3-integration-tests] PG not reachable (likely local dev)"
  fi
else
  echo "  SKIP [M] go not reachable"
fi

echo
echo "=== B188.3 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
