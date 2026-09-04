#!/bin/bash
# scripts/check_b237_7.sh — B237.7 (v1.5.2+) build-time contract:
# the preferred-exit auto-reconciler (B229) must do the right
# thing by default (LIVE / ON). Pin the contract that broke
# in 2026-09-04 (operator never flipped SKYGATE_PREFERRED_RECONCILER_LIVE
# from false→true, so cyborg+basic were without prefs for ~24h).
#
# Verifies:
#   A. Source: PreferredExitReconcilerLive() default is TRUE
#   B. Source: live=opts (false/0/no/off) are honored
#   C. Test: 8+ scenarios in reconciler_b237_7_test.go
#   D. Test: build-time tests pass (PlanDevicePrefChange + reconciler)
#   E. Build + tests
#
# Background: pre-B237.7, the default was false (DRY-RUN).
# The defensive default was a footgun: an operator who
# forgot to flip the env var ended up with manual-only
# device_exit_node_prefs (reconciler never wrote anything).
# B237.7 flips the default to TRUE so auto-reconcile
# works out of the box; operators who want dry-run
# can still set SKYGATE_PREFERRED_RECONCILER_LIVE=false.
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. Source contracts ---

# A.1 PreferredExitReconcilerLive() function exists
if grep -qE '^func PreferredExitReconcilerLive' internal/feature/exit_rules/reconciler.go 2>/dev/null; then
    ok "A.1 PreferredExitReconcilerLive() function defined"
else
    bad "A.1 PreferredExitReconcilerLive() function missing"
fi

# A.2 default-true: the new logic returns true for unset env
# (look for the pattern that returns true without checking
# the env var, OR a comment that says "default: true")
if awk '/^func PreferredExitReconcilerLive/,/^}/' internal/feature/exit_rules/reconciler.go 2>/dev/null | grep -qE 'Default: true|B237\.7.*default'; then
    ok "A.2 default is TRUE (B237.7 fix: 'Default: true' comment in source)"
else
    bad "A.2 default must be TRUE (B237.7 fix)"
fi

# A.3 explicit opt-out list: false, 0, no, off (case-insensitive)
if awk '/^func PreferredExitReconcilerLive/,/^}/' internal/feature/exit_rules/reconciler.go 2>/dev/null | grep -qE '"false".*"0".*"no".*"off"'; then
    ok "A.3 opt-out: false/0/no/off all return false (B237.7 contract)"
else
    bad "A.3 opt-out values: 'false', '0', 'no', 'off' must all be honored"
fi

# A.4 the old broken default-removed check is GONE (no
# more 'v == "true" || v == "1" || v == "yes"' without the
# opt-out prefix)
if awk '/^func PreferredExitReconcilerLive/,/^}/' internal/feature/exit_rules/reconciler.go 2>/dev/null | grep -qE 'v == "true" \|\| v == "1" \|\| v == "yes"'; then
    bad "A.4 OLD broken default-true-only check still present (must be replaced with the B237.7 opt-out prefix)"
else
    ok "A.4 old broken default-true-only check is GONE (B237.7 fix applied)"
fi

# A.5 PlanDevicePrefChange pure function still exists
if grep -qE '^func PlanDevicePrefChange' internal/feature/exit_rules/reconciler.go 2>/dev/null; then
    ok "A.5 PlanDevicePrefChange pure function preserved (B237.7 didn't touch it)"
else
    bad "A.5 PlanDevicePrefChange missing (B237.7 regression)"
fi

# --- B. Test contracts ---

# B.1 new test file exists
if [ -f internal/feature/exit_rules/reconciler_b237_7_test.go ]; then
    ok "B.1 reconciler_b237_7_test.go exists"
else
    bad "B.1 reconciler_b237_7_test.go missing (B237.7 contract test)"
fi

# B.2 8+ tests in new file
n_tests=$(grep -cE '^func Test' internal/feature/exit_rules/reconciler_b237_7_test.go 2>/dev/null || echo 0)
if [ "$n_tests" -ge "8" ]; then
    ok "B.2 B237.7 test file has $n_tests tests (>= 8 — the 8 scenarios)"
else
    bad "B.2 B237.7 test file has $n_tests tests (need >= 8)"
fi

# B.3 orphan user_id test exists (regression guard for the
# B237.7 michail case)
if grep -qE 'TestPlanDevicePrefChange_OrphanUserID' internal/feature/exit_rules/reconciler_b237_7_test.go 2>/dev/null; then
    ok "B.3 TestPlanDevicePrefChange_OrphanUserID (regression guard for the B237.7 root cause)"
else
    bad "B.3 TestPlanDevicePrefChange_OrphanUserID missing"
fi

# B.4 default-true test exists
if grep -qE 'TestPreferredExitReconcilerLive_Default(True|On)_B237_7' internal/feature/exit_rules/reconciler_b237_7_test.go 2>/dev/null; then
    ok "B.4 TestPreferredExitReconcilerLive_DefaultTrue_B237_7 (pins the flipped default)"
else
    bad "B.4 TestPreferredExitReconcilerLive_DefaultTrue_B237_7 missing"
fi

# B.5 B229 tests updated to match new default
if grep -qE 'TestPreferredExitReconcilerLive_DefaultOn_B237_7' internal/feature/exit_rules/reconciler_b229_test.go 2>/dev/null; then
    ok "B.5 B229 tests updated to match B237.7 default (the OldDefaultOff test is GONE)"
else
    bad "B.5 B229 tests NOT updated — they still expect default=false (will fail at runtime)"
fi

# --- C. Build + tests ---

# C.1 go build clean
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "C.1 go build ./... clean"
    else
        bad "C.1 go build ./... failed"
    fi
else
    echo "  SKIP  C.1 go build (no go in PATH)"
fi

# C.2 B237.7 unit tests pass
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        -run 'PlanDevicePrefChange|PreferredExitReconcilerLive' \
        ./internal/feature/exit_rules/... 2>/dev/null | grep -q '^ok'; then
        ok "C.2 B237.7 unit tests pass (PlanDevicePrefChange + PreferredExitReconcilerLive)"
    else
        bad "C.2 B237.7 unit tests failed"
    fi
else
    echo "  SKIP  C.2 B237.7 unit tests (no go in PATH)"
fi

# C.3 all exit_rules package tests pass (full coverage)
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 60s \
        ./internal/feature/exit_rules/... 2>/dev/null | grep -q '^ok'; then
        ok "C.3 full exit_rules test suite passes (B229 + B237.7 + B188.2 + B178 + B182 + B184)"
    else
        bad "C.3 full exit_rules test suite failed (regression)"
    fi
else
    echo "  SKIP  C.3 exit_rules test suite (no go in PATH)"
fi

# --- Summary ---

echo
echo "=== B237.7 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
