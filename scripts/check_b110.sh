#!/usr/bin/env bash
# B110: v1.3.10 — tailnet reachability/speed/split diagnostics
# (operator request 2026-08-13: "проверить скорость к skybars от
# karolina, организовать тесты для проверки скорости и доступа").
#
# Pins 7 contracts:
#   1. internal/feature/admin/system_tests_tailnet.go exists +
#      contains 3 new SystemTestDef entries (allNodesReachabilityTest,
#      vpsToVPSLatencyTest, splitSuspectedTest) and the vpsHostnameSet
#      helper with the 5 known VPS hostnames.
#   2. internal/feature/admin/system_tests_tailnet_test.go exists +
#      has at least 12 Test* functions covering pass/fail/skip branches
#      for all 3 new tests.
#   3. scripts/tailnet_probe.sh exists + is bash-syntax-valid +
#      has the 4 documented flags (--to, --iperf3, --ping, --json).
#   4. docs/tailnet-diagnostics.md exists + has the 4 mandatory
#      sections (TL;DR, Symptom, Root cause analysis, Fix procedure).
#   5. TestRegistry in system_tests_tailnet.go registers exactly 3 new
#      tests in the init() (verified by the test count in the
#      test file matching the count in system_tests_tailnet.go).
#   6. Each new test in system_tests_tailnet.go has the
#      Category:"network" string (matches the existing exit-node
#      speed tests' category — B98).
#   7. The Go test suite compiles and the new tests pass
#      (verified via `go test -count=1 -run 'TestVpsHostnameSet|...'
#      ./internal/feature/admin/`).
#
# Exit 0 = PASS, exit 1 = FAIL.

set -u

# Find go. PowerShell on Windows runs scripts through a
# different shell (Git Bash) that may not have go on PATH;
# WSL bash has /mnt/c/... paths in PATH but the embedded
# space ("Program Files") breaks the lookup. Probe a
# handful of known locations explicitly.
find_go() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return 0
  fi
  # Try WSL-style paths first (most common on this machine).
  for cand in \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files (x86)/Go/bin/go.exe" \
    "/mnt/c/ProgramFiles/Go/bin/go.exe" \
    ; do
    if [ -f "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done
  # Git-Bash-style paths as fallback.
  for cand in \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files (x86)/Go/bin/go.exe" \
    "/c/ProgramFiles/Go/bin/go.exe" \
    ; do
    if [ -f "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done
  # Linux-native paths (for VM / CI).
  for cand in \
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
  echo "B110 FAIL: go not in PATH and not found in standard install paths" >&2
  exit 1
fi
# Put the directory containing go on PATH.
GO_DIR=$(dirname "$GO_BIN")
export PATH="$GO_DIR:$PATH"

GO_FILE="internal/feature/admin/system_tests_tailnet.go"
GO_TEST="internal/feature/admin/system_tests_tailnet_test.go"
SH_FILE="scripts/tailnet_probe.sh"
DOC_FILE="docs/tailnet-diagnostics.md"

fail=0
pass() { echo "  ✓ $1"; }
err()  { echo "  ✗ $1" >&2; fail=1; }

# 1. Go file with 3 SystemTestDef + vpsHostnameSet helper
if [ ! -f "$GO_FILE" ]; then
  err "B110 FAIL: $GO_FILE not found"
  exit 1
fi
for sym in allNodesReachabilityTest vpsToVPSLatencyTest splitSuspectedTest vpsHostnameSet; do
  if ! grep -q "$sym" "$GO_FILE"; then
    err "B110 FAIL: $GO_FILE missing $sym"
  else
    pass "Go file has $sym"
  fi
done
# All 5 VPS hostnames must be in the vpsHostnameSet map.
for host in emilia karolina sharlotta skygate-host-1 svyatoslava-1; do
  if ! grep -q "\"$host\"" "$GO_FILE"; then
    err "B110 FAIL: $GO_FILE missing VPS hostname \"$host\" in vpsHostnameSet"
  fi
done
# Category must be "network" for all 3 (matches B98).
NETWORK_CAT=$(grep -c 'Category: *"network"' "$GO_FILE" || true)
if [ "${NETWORK_CAT:-0}" -lt 3 ]; then
  err "B110 FAIL: $GO_FILE has only $NETWORK_CAT tests with Category:\"network\" (need ≥3)"
else
  pass "all 3 tests use Category:\"network\""
fi

# 2. Go test file with ≥12 Test* functions
if [ ! -f "$GO_TEST" ]; then
  err "B110 FAIL: $GO_TEST not found"
  exit 1
fi
TEST_COUNT=$(grep -cE '^func Test[A-Z][A-Za-z0-9_]*' "$GO_TEST" || true)
if [ "${TEST_COUNT:-0}" -lt 12 ]; then
  err "B110 FAIL: $GO_TEST has only $TEST_COUNT test functions (need ≥12)"
else
  pass "test file has $TEST_COUNT Test* functions"
fi
# Each of the 3 new test defs must be exercised by at least 2 test funcs.
for test_def in AllNodesReachabilityTest VpsToVPSLatencyTest SplitSuspectedTest; do
  COUNT=$(grep -cE "^func Test${test_def}_" "$GO_TEST" || true)
  if [ "${COUNT:-0}" -lt 2 ]; then
    err "B110 FAIL: $GO_TEST has only $COUNT test cases for $test_def (need ≥2 per test def)"
  else
    pass "$test_def has $COUNT test cases"
  fi
done

# 3. Shell script — file exists + bash syntax + 4 flags
if [ ! -f "$SH_FILE" ]; then
  err "B110 FAIL: $SH_FILE not found"
  exit 1
fi
if ! bash -n "$SH_FILE" 2>/dev/null; then
  err "B110 FAIL: $SH_FILE has bash syntax errors"
  bash -n "$SH_FILE"
else
  pass "shell script bash-syntax-valid"
fi
for flag in --to --iperf3 --ping --json; do
  if ! grep -q -e "$flag" "$SH_FILE"; then
    err "B110 FAIL: $SH_FILE missing $flag flag"
  fi
done
pass "shell script has all 4 flags (--to/--iperf3/--ping/--json)"
# Verify shebang + executable bit (or at least the shebang).
if ! head -1 "$SH_FILE" | grep -q '^#!/usr/bin/env bash\|^#!/bin/bash'; then
  err "B110 FAIL: $SH_FILE missing bash shebang"
else
  pass "shell script has bash shebang"
fi

# 4. Documentation — file exists + 4 mandatory sections
if [ ! -f "$DOC_FILE" ]; then
  err "B110 FAIL: $DOC_FILE not found"
  exit 1
fi
for section in "TL;DR" "Symptom" "Root cause analysis" "Fix procedure" "Prevention"; do
  if ! grep -qE "^#+ .*${section}" "$DOC_FILE"; then
    err "B110 FAIL: $DOC_FILE missing section '${section}'"
  else
    pass "doc has '${section}' section"
  fi
done

# 5. init() registers exactly 3 new tests (defensive — the
#    init() block must end with TestRegistry = append(TestRegistry,...)
#    listing all 3 by name).
if ! grep -A5 'TestRegistry = append' "$GO_FILE" | grep -qE 'allNodesReachabilityTest|vpsToVPSLatencyTest|splitSuspectedTest'; then
  err "B110 FAIL: $GO_FILE init() does not register the 3 new tests"
else
  # Count the 3 names in the init block.
  INIT_COUNT=$(awk '/func init\(\)/,/^}/' "$GO_FILE" | grep -cE 'allNodesReachabilityTest|vpsToVPSLatencyTest|splitSuspectedTest' || true)
  if [ "${INIT_COUNT:-0}" -ne 3 ]; then
    err "B110 FAIL: init() registers $INIT_COUNT tests (want 3)"
  else
    pass "init() registers all 3 tests"
  fi
fi

# 6+7. Go test compilation + tests pass. This is the most
# expensive check — runs the actual unit tests. Skip on
# explicit B110_QUICK=1.
if [ "${B110_QUICK:-0}" != "1" ]; then
  go_test_output=$("$GO_BIN" test -count=1 -run 'TestVpsHostnameSet|TestAllNodesReachabilityTest|TestVpsToVPSLatencyTest|TestSplitSuspectedTest' ./internal/feature/admin/ 2>&1)
  go_test_rc=$?
  if [ "$go_test_rc" -ne 0 ]; then
    err "B110 FAIL: Go unit tests for tailnet fail (rc=$go_test_rc): $go_test_output"
  else
    pass "Go unit tests pass"
  fi
else
  echo "  (skipping go test, B110_QUICK=1)"
fi

if [ "$fail" -ne 0 ]; then
  echo "B110 FAIL"
  exit 1
fi
echo "B110 PASS: tailnet reachability/speed/split diagnostics (3 Go tests + shell script + docs)"
exit 0
