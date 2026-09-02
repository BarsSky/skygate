#!/usr/bin/env bash
# Phase 3.6 (v1.5.0+) — skygate cluster failover-drill CLI
# subcommand (safe-test counterpart of runClusterFailover).
#
# Phase 3.6 of docs/internal/cluster-management.md. Closes
# the "operator wants to verify the failover workflow
# without committing to a real swap" gap. Pre-Phase 3.6
# the only way to test the B204 elector + Phase 3.4
# button + runClusterFailover CLI together was either
# (a) point the test at a fake cluster (no real
# verification on the production data) or (b) do a real
# swap and immediately swap back (operator fatigue,
# cluster_audit log noise). Post-Phase 3.6 the operator
# runs `skygate cluster failover-drill --target=<id>` —
# same atomic swap, but writes action='node_drill' to
# cluster_audit instead of 'node_failover', so the
# /admin/ha "Last 20 events" table can show the drill
# alongside real failovers.
#
# The contracts:
#
#   1. internal/db/cluster_drill.go exists
#   2. db.DrillClusterNode signature: func(d *sql.DB, targetID, actor, reason string) (fromID, toID string, err error)
#   3. db.DrillClusterNode uses a transaction (sql.Tx + Commit)
#   4. db.DrillClusterNode writes action='node_drill' to cluster_audit
#   5. cmd/skygate/cluster.go has runClusterFailoverDrill function
#   6. cluster CLI dispatches "failover-drill" verb
#   7. The /admin/ha "Last 20 events" query includes 'node_drill' in the WHERE clause
#   8. Build + vet + tests pass
#   9. AGENTS.md mentions Phase 3.6
#  10. verify_pre_deploy.sh has a run_check for this

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

# 1. cluster_drill.go exists
file_exists "internal/db/cluster_drill.go" \
  && check "internal/db/cluster_drill.go exists" ok \
  || check "internal/db/cluster_drill.go exists" fail

# 2. DrillClusterNode signature
file_grep "^func DrillClusterNode\(d \*sql\.DB, targetID, actor, reason string\)" "internal/db/cluster_drill.go" \
  && check "db.DrillClusterNode signature" ok \
  || check "db.DrillClusterNode signature" fail

# 3. Uses transaction
file_grep "d\.BeginTx" "internal/db/cluster_drill.go" \
  && file_grep "tx\.Commit" "internal/db/cluster_drill.go" \
  && check "db.DrillClusterNode uses a transaction" ok \
  || check "db.DrillClusterNode uses a transaction" fail

# 4. Writes action='node_drill' to cluster_audit
file_grep "INSERT INTO cluster_audit" "internal/db/cluster_drill.go" \
  && file_grep "'node_drill'" "internal/db/cluster_drill.go" \
  && check "db.DrillClusterNode writes action='node_drill' to cluster_audit" ok \
  || check "db.DrillClusterNode writes action='node_drill' to cluster_audit" fail

# 5. runClusterFailoverDrill function
file_grep "^func runClusterFailoverDrill" "cmd/skygate/cluster.go" \
  && check "cmd/skygate/cluster.go has runClusterFailoverDrill" ok \
  || check "cmd/skygate/cluster.go has runClusterFailoverDrill" fail

# 6. CLI dispatches the verb
file_grep 'case "failover-drill"' "cmd/skygate/cluster.go" \
  && check "cluster CLI dispatches 'failover-drill' verb" ok \
  || check "cluster CLI dispatches 'failover-drill' verb" fail

# 7. /admin/ha includes node_drill in the events filter
file_grep "'node_drill'" "internal/feature/admin/ha.go" \
  && check "/admin/ha Last 20 events filter includes 'node_drill'" ok \
  || check "/admin/ha Last 20 events filter includes 'node_drill'" fail

# 8. build + vet + tests
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
  if "$GO" vet ./internal/db/... ./cmd/skygate/... >/dev/null 2>&1; then
    check "go vet on db + cmd passes" ok
  else
    check "go vet on db + cmd passes" fail
  fi
  if "$GO" test ./internal/db/... -count=1 >/dev/null 2>&1; then
    check "go test on db package passes" ok
  else
    check "go test on db package passes" fail
  fi
else
  check "go binary not found (skipping build/vet/test)" fail
fi

# 9. AGENTS.md
if [ -f "AGENTS.md" ] && grep -qE "Phase 3\.6|failover-drill" "AGENTS.md"; then
  check "AGENTS.md mentions Phase 3.6 / failover-drill" ok
else
  check "AGENTS.md mentions Phase 3.6 / failover-drill" fail
fi

# 10. verify_pre_deploy.sh
if [ -f "scripts/verify_pre_deploy.sh" ] && grep -q 'run_check "Phase_3_6"' "scripts/verify_pre_deploy.sh"; then
  check "verify_pre_deploy.sh has Phase_3_6 run_check" ok
else
  check "verify_pre_deploy.sh has Phase_3_6 run_check" fail
fi

echo
echo "=== Phase 3.6: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "FAILURES:"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
exit 0
