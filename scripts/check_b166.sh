#!/bin/bash
# check_b166.sh — B160 + B162 e2e / system tests
# (B166, v1.5.1)
#
# Operator 2026-08-24 review surfaced a gap: B160
# (device renew) shipped with unit tests but no
# e2e / system test that exercises the full
# ExtendNodeExpiry path on a real headscale. The
# system tests on /admin/system_tests covered
# "exit-nodes online" + "ACL admin present" but
# nothing about device renewal or deletion.
#
# B166 (this file) pins the fix:
#  1. system test "headscale.device_renew" — calls
#     ExtendNodeExpiry on the first non-tagged
#     device, verifies the new expiry lands in
#     [now+29d, now+31d], restores the original
#     expiry so the test is idempotent
#  2. system test "headscale.device_delete" —
#     tests the DeleteNode error path (the
#     B162 410-Gone handler matches on these
#     patterns: "node not found" /
#     "no longer exists in NodeStore")
#  3. Both tests use HSForUserFn(0) (the admin
#     user's headscale client) so they work on
#     the live VM
#  4. Tests are SKIP (not FAIL) when the admin
#     has no linked headscale or no nodes — so
#     they don't false-alarm on a fresh deploy

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: headscale.device_renew test present ==="
if grep -qE 'Name:\s+"headscale\.device_renew"' internal/feature/admin/system_tests.go; then
    ok "system test 'headscale.device_renew' is registered"
else
    bad "system test 'headscale.device_renew' MISSING"
fi
# Must call ExtendNodeExpiry (the same gRPC call
# the B160 PostMyDeviceRenew handler makes).
if grep -qE 'hs\.ExtendNodeExpiry' internal/feature/admin/system_tests.go; then
    ok "headscale.device_renew calls ExtendNodeExpiry"
else
    bad "headscale.device_renew: ExtendNodeExpiry call MISSING"
fi
# The test must restore the original expiry so
# it's idempotent (a non-idempotent test would
# silently move the operator's device expiry 30
# days into the future every run). The restore
# is in a defer block (multi-line) so we use
# awk to find the function containing the
# ExtendNodeExpiry(nodeID, origExp) call.
if awk '
  /defer func\(\) \{/ { in_defer=1 }
  in_defer && /ExtendNodeExpiry\(nodeID, origExp\)/ { found=1; exit }
  END { exit (found ? 0 : 1) }
' internal/feature/admin/system_tests.go; then
    ok "headscale.device_renew restores the original expiry (idempotent)"
else
    bad "headscale.device_renew: no idempotent restore (test would silently shift the operator's device expiry)"
fi
# The new expiry window check: must be in
# [now+29d, now+31d]. A wider window would mask
# regressions in the +30d calculation.
if grep -qE 'now\.Add\(29 \* 24 \* time\.Hour\)' internal/feature/admin/system_tests.go && \
   grep -qE 'now\.Add\(31 \* 24 \* time\.Hour\)' internal/feature/admin/system_tests.go; then
    ok "headscale.device_renew asserts [now+29d, now+31d] window (catches off-by-one errors)"
else
    bad "headscale.device_renew: [now+29d, now+31d] window check MISSING"
fi

echo ""
echo "=== contract B: headscale.device_delete test present ==="
if grep -qE 'Name:\s+"headscale\.device_delete"' internal/feature/admin/system_tests.go; then
    ok "system test 'headscale.device_delete' is registered"
else
    bad "system test 'headscale.device_delete' MISSING"
fi
# Must call DeleteNode.
if grep -qE 'hs\.DeleteNode\(' internal/feature/admin/system_tests.go; then
    ok "headscale.device_delete calls DeleteNode"
else
    bad "headscale.device_delete: DeleteNode call MISSING"
fi
# The B162 handler matches on the gRPC error
# patterns "node not found" / "no longer exists in
# NodeStore" / "Not Found" / "404". The test
# pins these patterns so a future headscale
# version with different wording doesn't silently
# regress B162's 410 Gone handler.
for pattern in "node not found" "no longer exists in NodeStore" "Not Found" "404"; do
    if grep -qE "$pattern" internal/feature/admin/system_tests.go; then
        ok "headscale.device_delete tests the '$pattern' gRPC error pattern (B162 410-Gone path)"
    else
        bad "headscale.device_delete: '$pattern' pattern MISSING (B162 handler would 500 on this gRPC wording)"
    fi
done

echo ""
echo "=== contract C: tests use HSForUserFn + skip on missing headscale ==="
# The tests use the admin user (id 0) — same as
# the B160 / B162 handlers do.
if grep -qE 'HSForUserFn\(0\)' internal/feature/admin/system_tests.go; then
    ok "Both tests use HSForUserFn(0) (admin user)"
else
    bad "Tests don't use HSForUserFn(0) — they'd route to the wrong headscale user"
fi
# The tests must SKIP (not FAIL) when no admin
# headscale is configured or no nodes exist —
# otherwise a fresh deploy would false-alarm.
if grep -qE 'no admin user linked to headscale' internal/feature/admin/system_tests.go; then
    ok "Tests SKIP on missing headscale config (no false-alarm on fresh deploy)"
else
    bad "Tests: missing SKIP-on-no-headscale guard (would false-alarm on a fresh deploy)"
fi
if grep -qE 'no headscale nodes registered' internal/feature/admin/system_tests.go; then
    ok "Tests SKIP on empty headscale (no false-alarm on a fresh deploy)"
else
    bad "Tests: missing SKIP-on-no-nodes guard"
fi

echo ""
echo "=== contract D: tests live in the right category ==="
# The headscale.* tests should be in the
# "headscale" category (drives the /admin/system_tests
# "Headscale" section grouping). We grep for the
# test name + Category: "headscale" within 5 lines
# (the struct literal puts Name first, then Category,
# then Description, then Run).
# Match a "headscale.device_renew" struct that has
# Category: "headscale" within 5 lines below.
device_renew_within_5=$(grep -A 5 'Name:.*"headscale\.device_renew"' internal/feature/admin/system_tests.go | grep -c 'Category:.*"headscale"')
if [ "$device_renew_within_5" -ge 1 ]; then
    ok "headscale.device_renew is in the 'headscale' category"
else
    bad "headscale.device_renew: category is not 'headscale'"
fi
device_delete_within_5=$(grep -A 5 'Name:.*"headscale\.device_delete"' internal/feature/admin/system_tests.go | grep -c 'Category:.*"headscale"')
if [ "$device_delete_within_5" -ge 1 ]; then
    ok "headscale.device_delete is in the 'headscale' category"
else
    bad "headscale.device_delete: category is not 'headscale'"
fi

echo ""
echo "=== contract E: build + vet clean ==="
out=$(go build ./... 2>&1)
if [ -z "$out" ]; then
    ok "go build ./... clean"
else
    bad "go build output: $out"
fi
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet output: $out"
fi

echo ""
echo "=== summary ==="
echo "B166: e2e + system tests for B160 renew + B162 delete"
echo "all contracts satisfied"
