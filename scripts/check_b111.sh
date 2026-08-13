#!/usr/bin/env bash
# B111: v1.3.11 — B93 "infra owns technical nodes" completion
# (operator request 2026-08-13: "infra user будет владеть
# skygate + exit nodes (karolina sharlotta emilia
# svyatoslava) и давать публичный доступ к exit nodes
# остальным").
#
# Pins 5 contracts:
#   1. isInfraNode in internal/nodeownership/auto.go has
#      a rule for tag:exit-node (catches the operator's
#      4 VPS exit nodes regardless of current owner).
#   2. BackfillInfra uses UPDATE (not just INSERT OR IGNORE)
#      to re-attribute user-portal-owned nodes to 'infra'
#      when they match isInfraNode. This is the B93 design
#      completion — without UPDATE the exit nodes stay in
#      skyadmin/michail/svyatoslava buckets forever.
#   3. The new helper getInfraExitNodeTags is in
#      internal/acl/acl_perdevice.go and returns the
#      right tag list (skygate filtered out, sorted).
#   4. Both GenerateACLForPlane and
#      GenerateACLWithViaForPlane emit a `* →
#      tag:dev-infra-<exit>` catch-all per returned tag,
#      preserving the pre-B93 "any user can use the exit
#      nodes" behaviour.
#   5. The Go test suite has unit tests for
#      getInfraExitNodeTags (TestGetInfraExitNodeTags_*)
#      and they pass.
#
# Exit 0 = PASS, exit 1 = FAIL.

set -u

# Find go (same pattern as check_b110.sh / check_b100.sh)
find_go() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return 0
  fi
  for cand in \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files (x86)/Go/bin/go.exe" \
    "/mnt/c/ProgramFiles/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files (x86)/Go/bin/go.exe" \
    "/c/ProgramFiles/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/opt/go/bin/go" \
    "/root/go/bin/go" \
    ; do
    if [ -f "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done
  return 1
}
GO_BIN=$(find_go)
if [ -z "$GO_BIN" ]; then
  echo "B111 FAIL: go not in PATH and not found in standard install paths" >&2
  exit 1
fi
GO_DIR=$(dirname "$GO_BIN")
export PATH="$GO_DIR:$PATH"

AUTO_FILE="internal/nodeownership/auto.go"
ACL_FILE="internal/acl/acl.go"
PERDEVICE_FILE="internal/acl/acl_perdevice.go"
TEST_FILE="internal/acl/acl_perdevice_b111_test.go"

fail=0
pass() { echo "  ✓ $1"; }
err()  { echo "  ✗ $1" >&2; fail=1; }

# 1. isInfraNode has tag:exit-node rule
if [ ! -f "$AUTO_FILE" ]; then
  err "B111 FAIL: $AUTO_FILE not found"
else
  if ! perl -0777 -ne 'exit !(/isInfraNode.*?tag:exit-node/s)' "$AUTO_FILE" 2>/dev/null; then
    err "B111 FAIL: isInfraNode missing tag:exit-node rule (B111) — exit nodes (emilia/karolina/sharlotta/svyatoslava-1) won't match"
  else
    pass "isInfraNode has tag:exit-node rule"
  fi
fi

# 2. BackfillInfra uses UPDATE (not just INSERT OR IGNORE)
if [ ! -f "$AUTO_FILE" ]; then
  err "B111 FAIL: $AUTO_FILE not found"
else
  if ! grep -q "UPDATE node_owner_map" "$AUTO_FILE"; then
    err "B111 FAIL: BackfillInfra missing UPDATE node_owner_map — re-attribution from user-portal to infra won't happen"
  else
    pass "BackfillInfra uses UPDATE node_owner_map"
  fi
  # Also: should reference the 'infra' tag prefix `tag:dev-infra-`
  if ! grep -q "tag:dev-infra-" "$AUTO_FILE"; then
    err "B111 FAIL: BackfillInfra missing tag:dev-infra- generation (B111) — re-attributed nodes get the wrong tag"
  else
    pass "BackfillInfra generates tag:dev-infra- tags"
  fi
fi

# 3. getInfraExitNodeTags helper exists + has correct filter
if [ ! -f "$PERDEVICE_FILE" ]; then
  err "B111 FAIL: $PERDEVICE_FILE not found"
else
  if ! grep -q "func getInfraExitNodeTags" "$PERDEVICE_FILE"; then
    err "B111 FAIL: getInfraExitNodeTags helper not in $PERDEVICE_FILE"
  else
    pass "getInfraExitNodeTags helper exists"
  fi
  # Must filter out skygate-host-* from public-access
  if ! grep -q "tag:dev-infra-skygate" "$PERDEVICE_FILE"; then
    err "B111 FAIL: getInfraExitNodeTags doesn't filter out skygate host (B111) — skygate VM would be publicly routeable"
  else
    pass "getInfraExitNodeTags filters out skygate host"
  fi
fi

# 4. Both GenerateACL call sites emit the catch-all
if [ ! -f "$ACL_FILE" ]; then
  err "B111 FAIL: $ACL_FILE not found"
else
  # Count occurrences of getInfraExitNodeTags call (should be 2 — one per GenerateACL func)
  COUNT=$(grep -c "getInfraExitNodeTags(tagsByUser)" "$ACL_FILE" || echo 0)
  if [ "${COUNT:-0}" -lt 2 ]; then
    err "B111 FAIL: only ${COUNT:-0} call sites of getInfraExitNodeTags (expected 2 — GenerateACL + GenerateACLWithViaForPlane)"
  else
    pass "${COUNT} call sites of getInfraExitNodeTags (both GenerateACL variants covered)"
  fi
fi

# 5. Unit tests exist + pass
if [ ! -f "$TEST_FILE" ]; then
  err "B111 FAIL: $TEST_FILE not found"
else
  TEST_COUNT=$(grep -cE '^func TestGetInfraExitNodeTags_' "$TEST_FILE" || echo 0)
  if [ "${TEST_COUNT:-0}" -lt 4 ]; then
    err "B111 FAIL: only ${TEST_COUNT:-0} TestGetInfraExitNodeTags_* cases (need ≥4 for branch coverage)"
  else
    pass "${TEST_COUNT} test cases for getInfraExitNodeTags"
  fi
  # Run the tests (skip-able via B111_QUICK=1)
  if [ "${B111_QUICK:-0}" != "1" ]; then
    test_out=$("$GO_BIN" test -count=1 -run 'TestGetInfraExitNodeTags' ./internal/acl/ 2>&1)
    test_rc=$?
    if [ "$test_rc" -ne 0 ]; then
      err "B111 FAIL: getInfraExitNodeTags tests failed: ${test_out}"
    else
      pass "getInfraExitNodeTags tests pass"
    fi
  else
    echo "  (skipping go test, B111_QUICK=1)"
  fi
fi

if [ "$fail" -ne 0 ]; then
  echo "B111 FAIL"
  exit 1
fi
echo "B111 PASS: B93 infra-owns-technical-nodes completion (isInfraNode + BackfillInfra UPDATE + public-access grants)"
exit 0
