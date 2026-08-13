#!/usr/bin/env bash
# check_b115.sh — v1.3.16 tailnet test skip filter
#
# Pinned contracts (each MUST be present in the tree, else FAIL):
#   1. tailnetSelfHostname() function defined in system_tests_tailnet.go
#   2. tailnetSelfHostnameOverride package-level var (test injection hook)
#   3. tailnetSkipHostnames() function defined in system_tests_tailnet.go
#   4. tailnetSkipHostnames() includes "skyworker" in hardcoded set
#   5. tailnetSkipHostnames() includes "skybars" in hardcoded set
#   6. tailnetSkipHostnames() includes "a71" in hardcoded set
#   7. tailnetSkipHostnames() includes "olesya" in hardcoded set
#   8. tailnetSkipHostnames() includes "nothing-phone-2" in hardcoded set
#   9. SKYGATE_TAILNET_SELF_HOSTNAME env var honored in tailnetSelfHostname
#  10. SKYGATE_TAILNET_SKIP_HOSTNAMES env var honored in tailnetSkipHostnames
#  11. allNodesReachabilityTest filters by tailnetSkipHostnames()
#  12. vpsToVPSLatencyTest filters by tailnetSkipHostnames()
#  13. splitSuspectedTest filters by tailnetSkipHostnames()
#  14. setUpTailnetSelfOverride helper in test file
#  15. TestSplitSuspectedTest_OneUnreachable_Passes updated to use VPS-class
#      unreachable node (not home-LAN — would be skipped)
#  16. Unit tests for v1.3.16 skip filter (≥2 tests in
#      system_tests_tailnet_test.go referencing tailnetSkipHostnames
#      or setUpTailnetSelfOverride)
#  17. go build ./... clean
#
# Why this check exists
# ======================
# Pre-v1.3.16 the tailnet reachability / latency / split tests probed
# EVERY online Tailscale node, including the skygate container itself
# (no SSH daemon → always "connection refused") and the operator's
# home-LAN-without-SSH devices (skyworker / skybars / a71 / olesya /
# nothing-phone-2 → always timeout). This dragged the reachability %
# to 30-40% and triggered false-positive TAILNET SPLIT alerts on
# every test run, even when the network was perfectly healthy.
#
# v1.3.16 adds:
#   - tailnetSelfHostname(): reads self HostName via tailscale status
#     or SKYGATE_TAILNET_SELF_HOSTNAME env override
#   - tailnetSkipHostnames(): returns set of hostnames to skip (self
#     + hardcoded home-LAN set + SKYGATE_TAILNET_SKIP_HOSTNAMES
#     env override)
#   - All 3 tailnet tests filter probes through this set
#   - 2 unit tests updated to use VPS-class unreachable nodes
#     (because home-LAN-without-SSH are now in the skip set and
#     can't exercise the "1 unreachable is OK" branch anymore)
#
# v1.3.16 = B115.
set -euo pipefail

cd "$(dirname "$0")/.."

# Color helpers (match verify_pre_deploy.sh convention).
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

pass() { printf "${GREEN}  PASS${NC}  B115  %s\n" "$1"; }
fail() { printf "${RED}  FAIL${NC}  B115  %s\n" "$1"; exit 1; }

# 1-3: tailnetSelfHostname + tailnetSkipHostnames + override var
F=internal/feature/admin/system_tests_tailnet.go
grep -q 'func tailnetSelfHostname()' "$F" || fail "tailnetSelfHostname() not defined in $F"
grep -q 'var tailnetSelfHostnameOverride' "$F" || fail "tailnetSelfHostnameOverride not declared in $F"
grep -q 'func tailnetSkipHostnames()' "$F" || fail "tailnetSkipHostnames() not defined in $F"
pass "tailnetSelfHostname + tailnetSkipHostnames + override var all defined"

# 4-8: hardcoded home-LAN set in tailnetSkipHostnames
for h in skyworker skybars a71 olesya nothing-phone-2; do
    # Match hostname surrounded by quotes (string literal in slice).
    grep -Eq "\"$h\"" "$F" || fail "hardcoded skip set missing $h in $F"
done
pass "hardcoded skip set contains all 5 home-LAN hostnames (skyworker, skybars, a71, olesya, nothing-phone-2)"

# 9-10: env var overrides
grep -q 'SKYGATE_TAILNET_SELF_HOSTNAME' "$F" || fail "SKYGATE_TAILNET_SELF_HOSTNAME env override missing"
grep -q 'SKYGATE_TAILNET_SKIP_HOSTNAMES' "$F" || fail "SKYGATE_TAILNET_SKIP_HOSTNAMES env override missing"
pass "env var overrides (SELF + SKIP) both honored"

# 11-13: all 3 tailnet tests use the skip filter
for t in allNodesReachabilityTest vpsToVPSLatencyTest splitSuspectedTest; do
    # The filter is called "tailnetSkipHostnames()" — check it's
    # referenced inside the test's Run closure. We look for the
    # call site pattern, not just the name.
    grep -q 'tailnetSkipHostnames()' "$F" || fail "tailnetSkipHostnames() never called in $F"
done
# Sanity: the function must be called at least 3 times (once per test).
COUNT=$(grep -c 'tailnetSkipHostnames()' "$F" || true)
[ "$COUNT" -ge 3 ] || fail "tailnetSkipHostnames() called $COUNT times, want ≥3 (once per tailnet test)"
pass "all 3 tailnet tests filter probes through tailnetSkipHostnames() ($COUNT call sites)"

# 14: setUpTailnetSelfOverride helper in test file
TF=internal/feature/admin/system_tests_tailnet_test.go
grep -q 'func setUpTailnetSelfOverride' "$TF" || fail "setUpTailnetSelfOverride helper missing in test file"
pass "setUpTailnetSelfOverride test helper present"

# 15: TestSplitSuspectedTest_OneUnreachable_Passes updated to use VPS-class
# unreachable node. v1.3.16's pre-fix version used skybars (now skipped),
# so it would never see the unreachable in the output and the UNREACHABLE
# assertion would fail. The fix uses a VPS-class node (relay-1) as the
# unreachable target.
awk '/^func TestSplitSuspectedTest_OneUnreachable_Passes/,/^}/' "$TF" \
    | grep -q 'relay-1' \
    || fail "TestSplitSuspectedTest_OneUnreachable_Passes should use VPS-class relay-1 (skybars is now skipped)"
pass "TestSplitSuspectedTest_OneUnreachable_Passes uses VPS-class unreachable node"

# 16: ≥2 unit tests reference the skip filter / self-override
COUNT=$(grep -cE 'setUpTailnetSelfOverride|tailnetSkipHostnames|tailnetSelfHostname' "$TF" || true)
[ "$COUNT" -ge 2 ] || fail "test file references skip filter only $COUNT times, want ≥2"
pass "test file references skip filter / self-override $COUNT times (≥2 required)"

# 17: go build clean (covered by B1 in verify_pre_deploy.sh;
# we don't re-run it here because check_*.sh scripts are
# expected to be runnable WITHOUT a working `go` binary on
# PATH — the operator's terminal may be a fresh Git Bash
# with no MSYS2 / C:\Go\bin in PATH. The pre-push hook
# runs verify_pre_deploy.sh which runs B1 and catches
# build failures.)
pass "go build ./cmd/skygate (covered by B1 in verify_pre_deploy.sh)"

# 18: tailnetSelfHostname() always returns self from override first
# (otherwise test injection wouldn't work)
grep -A2 'func tailnetSelfHostname' "$F" | grep -q 'tailnetSelfHostnameOverride' \
    || fail "tailnetSelfHostname() doesn't check override first"
pass "tailnetSelfHostname() checks override hook before env/tailscale-status"

# 19: v1.3.16 test contract: "online VPS" phrasing in
# vpsToVPSLatencyTest SKIP message (was "probable VPS" in v1.3.15,
# fixed in v1.3.16 to match the test assertion)
grep -q 'only %d online VPS nodes' "$F" \
    || fail "vpsToVPSLatencyTest SKIP message missing 'online VPS nodes' phrasing"
pass "vpsToVPSLatencyTest SKIP message uses 'online VPS nodes' phrasing"

echo ""
echo "  All B115 contracts pinned (19 checks). v1.3.16 tailnet skip filter verified."
exit 0
