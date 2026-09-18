#!/usr/bin/env bash
# check_b98.sh — exit-node speed/availability system tests
# (operator's "необходимо также добавить в тесты системы
# тестирование по скорости доступа exit nodes", 2026-08-12).
#
# Pins that:
#   1. system_tests_exit_node_speed.go exists and defines
#      both new test defs (in Category="network" so B40
#      coverage still holds).
#   2. probeExitNodeConnectOverride is a package-private
#      var so the test can inject a fake probe (no real
#      network in `go test ./...`).
#   3. system_tests_exit_node_speed_test.go exists and
#      has the expected test count (>= 15 cases).
#   4. The Go tests pass under `go test -count=1`.
#   5. The B40 category coverage is preserved (network
#      tests + db + headscale) by the 2 new network tests.

set -e
cd "$(cd "$(dirname "$0")/.." && pwd)"

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
  for cand in \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files (x86)/Go/bin/go.exe" \
    "/mnt/c/ProgramFiles/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files (x86)/Go/bin/go.exe" \
    "/c/ProgramFiles/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/opt/go/bin/go" \
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
  echo "B98 FAIL: go not in PATH and not found in standard install paths" >&2
  exit 1
fi

# 1. The two new test defs exist with the right shape.
grep -qF 'Name:        "exit_nodes.tcp_connect_speed"' internal/feature/admin/system_tests_exit_node_speed.go || {
  echo "FAIL: exit_nodes.tcp_connect_speed not registered in system_tests_exit_node_speed.go"
  exit 1
}
grep -qF 'Name:        "exit_nodes.availability_summary"' internal/feature/admin/system_tests_exit_node_speed.go || {
  echo "FAIL: exit_nodes.availability_summary not registered in system_tests_exit_node_speed.go"
  exit 1
}
grep -qF 'Category:    "network"' internal/feature/admin/system_tests_exit_node_speed.go || {
  echo "FAIL: new tests not in 'network' category (breaks B40 category coverage)"
  exit 1
}

# 2. The probe override hook exists.
grep -qF 'probeExitNodeConnectOverride' internal/feature/admin/system_tests_exit_node_speed.go || {
  echo "FAIL: probeExitNodeConnectOverride not declared (tests can't inject fake probe)"
  exit 1
}

# 3. The test file exists and has the expected test count.
test -f internal/feature/admin/system_tests_exit_node_speed_test.go || {
  echo "FAIL: system_tests_exit_node_speed_test.go not found"
  exit 1
}
test_count=$(grep -cE '^func Test' internal/feature/admin/system_tests_exit_node_speed_test.go || echo 0)
if [ "$test_count" -lt 15 ]; then
  echo "FAIL: expected >=15 test functions, found $test_count"
  exit 1
fi

# 4. The Go tests actually pass.
test_output=$(cd internal/feature/admin && "$GO_BIN" test -count=1 -run 'TestExitNodeSpeed|TestExitNodes|TestTailscaleIPFromNode|TestFormatLatencyMs|TestProbeExitNodeConnect_' 2>&1)
if ! echo "$test_output" | grep -qE '^ok\s+skygate/internal/feature/admin'; then
  echo "FAIL: Go tests for exit-node speed/availability did not pass"
  echo "$test_output" | tail -5
  exit 1
fi

# 5. B40 category coverage is preserved (regression guard).
grep -qE 'Name:\s+"net\.' internal/feature/admin/system_tests.go || {
  echo "FAIL: B40 network category no longer covered"
  exit 1
}
grep -qE 'Name:\s+"db\.' internal/feature/admin/system_tests.go || {
  echo "FAIL: B40 db category no longer covered"
  exit 1
}
grep -qE 'Name:\s+"headscale\.' internal/feature/admin/system_tests.go || {
  echo "FAIL: B40 headscale category no longer covered"
  exit 1
}

echo "PASS: B98 (exit-node speed/availability system tests: $test_count Go tests, 2 new defs, B40 categories preserved)"
exit 0
