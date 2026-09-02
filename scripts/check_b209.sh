#!/usr/bin/env bash
# B209 (v1.5.0+) — end-to-end HA failover test orchestrator.
#
# Phase 3 of docs/internal/cluster-management.md. The
# B204 elector has been ticking every 5s since v1.5.0
# shipped, but no automated test exercised the full
# failure-detection + auto-recommendation + recovery +
# dedup cycle against a live cluster_node table. The
# B209 e2e script (scripts/b209_e2e.sh) does exactly
# that: 7 phases against the agent DB.
#
# B209 also adds a Now() hook to the elector's Config
# so the e2e can fast-forward through the 90s staleness
# window in unit tests (no real-time sleeping). The hook
# is opt-in (nil-safe: NewElector restores nil to
# time.Now) and the existing B204 unit tests still pass
# against the new Config shape.
#
# The contracts:
#
#   1. scripts/b209_e2e.sh exists + bash -n passes
#   2. e2e has 7 phases (0..7) in order
#   3. e2e has a cleanup path that removes b209-* rows
#      from cluster_node + cluster_audit (idempotent,
#      safe on partial state)
#   4. e2e exit code is 0 on full pass, 1 on any fail
#   5. e2e prints pass/fail counts at the end
#   6. elector Config has Now func() time.Time field
#   7. DefaultConfig populates Now = time.Now
#   8. NewElector restores nil Now to time.Now
#   9. evaluate() reads e.now() (not time.Now())
#  10. elector_b204_test.go has TestDefaultConfig_NowSet
#      + TestNewElector_NowFallback + TestElector_NowUsesFakeClock
#      + TestNextState_AtFakeClockBoundary
#  11. unit tests cover fake-clock 90s boundary
#      (89s stays ready, 90s stays ready strict-<,
#      91s goes to failed)
#  12. build + vet + elector unit tests pass
#  13. AGENTS.md mentions B209
#  14. verify_pre_deploy.sh has a B209 run_check

set -u

if [ -n "${SKYGATE_PROJECT_DIR:-}" ]; then
  cd "$SKYGATE_PROJECT_DIR"
else
  cd "$(dirname "$0")/.."
fi

PASS=0
FAIL=0
fails=()

check() {
  local name="$1"
  local result="$2"
  if [ "$result" = "ok" ]; then
    printf "  \033[32m✓\033[0m %s\n" "$name"
    PASS=$((PASS+1))
  else
    printf "  \033[31m✗\033[0m %s\n" "$name"
    FAIL=$((FAIL+1))
    fails+=("$name")
  fi
}

file_exists() { [ -f "$1" ]; }
file_grep() { grep -qE "$1" "$2" 2>/dev/null; return $?; }
file_grep_specific() { grep -F "$1" "$2" >/dev/null 2>&1; return $?; }

# 1. e2e script exists
file_exists "scripts/b209_e2e.sh" \
  && check "scripts/b209_e2e.sh exists" ok \
  || check "scripts/b209_e2e.sh exists" fail

# 2. e2e is bash-valid
if [ -f "scripts/b209_e2e.sh" ]; then
  if bash -n scripts/b209_e2e.sh 2>/dev/null; then
    check "scripts/b209_e2e.sh bash -n passes" ok
  else
    check "scripts/b209_e2e.sh bash -n passes" fail
  fi
fi

# 3. e2e has all 7 phases (0..7)
if [ -f "scripts/b209_e2e.sh" ]; then
  PHASES_OK=1
  for p in 0 1 2 3 4 5 6 7; do
    if ! grep -qE "\\[Phase $p\\]" scripts/b209_e2e.sh; then
      PHASES_OK=0
      break
    fi
  done
  [ "$PHASES_OK" = "1" ] && check "e2e has all 8 phases (0..7)" ok \
    || check "e2e has all 8 phases (0..7)" fail
fi

# 4. e2e has cleanup function + trap on EXIT
if [ -f "scripts/b209_e2e.sh" ]; then
  file_grep "^cleanup_b209\(\)" scripts/b209_e2e.sh \
    && file_grep "trap .*EXIT .*INT .*TERM" scripts/b209_e2e.sh \
    && check "e2e has cleanup_b209() + EXIT trap" ok \
    || check "e2e has cleanup_b209() + EXIT trap" fail
fi

# 5. e2e removes b209-* rows in cleanup
if [ -f "scripts/b209_e2e.sh" ]; then
  file_grep "DELETE FROM cluster_node WHERE id LIKE 'b209-%'" scripts/b209_e2e.sh \
    && file_grep "DELETE FROM cluster_audit WHERE target_node_id LIKE 'b209-%'" scripts/b209_e2e.sh \
    && check "e2e cleanup removes b209-* rows from both tables" ok \
    || check "e2e cleanup removes b209-* rows from both tables" fail
fi

# 6. e2e exits 0 on pass, 1 on fail
if [ -f "scripts/b209_e2e.sh" ]; then
  file_grep "exit 0" scripts/b209_e2e.sh \
    && file_grep "exit 1" scripts/b209_e2e.sh \
    && check "e2e exits 0 on pass + 1 on fail" ok \
    || check "e2e exits 0 on pass + 1 on fail" fail
fi

# 7. e2e prints pass/fail summary
if [ -f "scripts/b209_e2e.sh" ]; then
  file_grep "B209 e2e:.*pass.*fail" scripts/b209_e2e.sh \
    && check "e2e prints pass/fail summary" ok \
    || check "e2e prints pass/fail summary" fail
fi

# 8. elector Config has Now field
file_grep "Now[[:space:]]+func\(\) time\.Time" "internal/elector/elector.go" \
  && check "elector Config.Now field declared" ok \
  || check "elector Config.Now field declared" fail

# 9. DefaultConfig populates Now
file_grep "Now:[[:space:]]+time\.Now," "internal/elector/elector.go" \
  && check "DefaultConfig sets Now = time.Now" ok \
  || check "DefaultConfig sets Now = time.Now" fail

# 10. NewElector restores nil Now
file_grep "cfg\.Now = time\.Now" "internal/elector/elector.go" \
  && check "NewElector restores nil Now" ok \
  || check "NewElector restores nil Now" fail

# 11. evaluate() uses e.now() (not raw time.Now)
file_grep "now := e\.now\(\)\.UTC\(\)" "internal/elector/elector.go" \
  && check "evaluate() reads e.now()" ok \
  || check "evaluate() reads e.now()" fail

# 12. helper e.now() defined
file_grep "^func \(e \*Elector\) now\(\) time\.Time" "internal/elector/elector.go" \
  && check "e.now() helper exists" ok \
  || check "e.now() helper exists" fail

# 13. unit tests: 4 new test functions
if [ -f "internal/elector/elector_b204_test.go" ]; then
  NEW_TESTS_OK=1
  for fn in TestDefaultConfig_NowSet TestNewElector_NowFallback TestElector_NowUsesFakeClock TestNextState_AtFakeClockBoundary; do
    if ! grep -q "^func $fn" "internal/elector/elector_b204_test.go"; then
      NEW_TESTS_OK=0
      break
    fi
  done
  [ "$NEW_TESTS_OK" = "1" ] && check "4 new fake-clock unit tests present" ok \
    || check "4 new fake-clock unit tests present" fail
fi

# 14. build + vet + elector unit tests pass
GO=""
if command -v go >/dev/null 2>&1; then
  GO="go"
else
  for cand in \
    "C:/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go"; do
    [ -x "$cand" ] && GO="$cand" && break
  done
fi
if [ -n "$GO" ]; then
  if "$GO" build ./... >/dev/null 2>&1; then
    check "go build ./... passes" ok
  else
    check "go build ./... passes" fail
  fi
  if "$GO" vet ./internal/elector/... >/dev/null 2>&1; then
    check "go vet ./internal/elector/... passes" ok
  else
    check "go vet ./internal/elector/... passes" fail
  fi
  if "$GO" test ./internal/elector/... -count=1 >/dev/null 2>&1; then
    check "go test ./internal/elector/... passes" ok
  else
    check "go test ./internal/elector/... passes" fail
  fi
else
  check "go binary not found (skipping build/vet/test)" fail
fi

# 15. AGENTS.md mentions B209
if [ -f "AGENTS.md" ]; then
  if grep -qE "B209" "AGENTS.md"; then
    check "AGENTS.md mentions B209" ok
  else
    check "AGENTS.md mentions B209" fail
  fi
else
  check "AGENTS.md mentions B209" fail
fi

# 16. verify_pre_deploy.sh has B209 run_check
if [ -f "scripts/verify_pre_deploy.sh" ]; then
  if grep -q 'run_check "B209"' "scripts/verify_pre_deploy.sh"; then
    check "verify_pre_deploy.sh has B209 run_check" ok
  else
    check "verify_pre_deploy.sh has B209 run_check" fail
  fi
else
  check "verify_pre_deploy.sh has B209 run_check" fail
fi

echo
echo "=== B209: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "FAILURES:"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
exit 0
