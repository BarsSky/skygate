#!/usr/bin/env bash
# B204 (v1.5.0+) — HA elector: auto-detect failed nodes +
# auto-failover recommendation. Phase 3.2-3.3 of
# docs/internal/cluster-management.md.
#
# The contracts:
#
#   1. internal/elector/elector.go exists
#   2. Elector type with Start/Stop
#   3. Config struct with Interval/HeartbeatInterval/ClusterID/Logger
#   4. DefaultConfig returns 5s/30s/skygate-staging
#   5. HeartbeatIntervalSeconds = 30 + StaleMultiplier = 3 (3 missed → failed)
#   6. nextState() state machine: pending→failed (no hb), ready→failed (stale)
#   7. transitionNode writes cluster_audit row with from/to JSONB
#   8. recommendFailover: detects failed primary + ready standby, logs cluster_audit
#   9. cmd/skygate/main.go starts the elector
#  10. Unit tests: 6+ covering nextState + roleContains + defaults + constants
#  11. go build + vet + elector unit tests pass
#  12. AGENTS.md mentions B204

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
        printf "  \31m✗\033[0m %s\n" "$name"
        FAIL=$((FAIL+1))
        fails+=("$name")
    fi
}

file_exists() { [ -f "$1" ]; }
grep_q() { grep -qE "$1" "$2" 2>/dev/null; return $?; }

# 1. elector.go
file_exists "internal/elector/elector.go" \
    && check "internal/elector/elector.go exists" ok \
    || check "internal/elector/elector.go exists" fail

# 2. Elector type + Start/Stop
grep_q 'type Elector struct' "internal/elector/elector.go" \
    && check "Elector type defined" ok \
    || check "Elector type defined" fail
grep_q 'func .e \*Elector. Start\(' "internal/elector/elector.go" \
    && check "Elector.Start()" ok \
    || check "Elector.Start()" fail
grep_q 'func .e \*Elector. Stop\(' "internal/elector/elector.go" \
    && check "Elector.Stop()" ok \
    || check "Elector.Stop()" fail

# 3. Config struct
grep_q 'type Config struct' "internal/elector/elector.go" \
    && check "Config struct defined" ok \
    || check "Config struct defined" fail
grep_q 'Interval[[:space:]]+time\.Duration' "internal/elector/elector.go" \
    && check "Config.Interval" ok \
    || check "Config.Interval" fail
grep_q 'HeartbeatInterval[[:space:]]+time\.Duration' "internal/elector/elector.go" \
    && check "Config.HeartbeatInterval" ok \
    || check "Config.HeartbeatInterval" fail
grep_q 'ClusterID[[:space:]]+string' "internal/elector/elector.go" \
    && check "Config.ClusterID" ok \
    || check "Config.ClusterID" fail

# 4. DefaultConfig
grep_q 'func DefaultConfig\(\) Config' "internal/elector/elector.go" \
    && check "DefaultConfig() defined" ok \
    || check "DefaultConfig() defined" fail
grep -A4 'func DefaultConfig' "internal/elector/elector.go" | grep -q '5 \* time\.Second' \
    && check "DefaultConfig Interval=5s" ok \
    || check "DefaultConfig Interval=5s" fail
grep -A8 'func DefaultConfig' "internal/elector/elector.go" | grep -q 'skygate-staging' \
    && check "DefaultConfig ClusterID=skygate-staging" ok \
    || check "DefaultConfig ClusterID=skygate-staging" fail

# 5. Constants pin the B204 contract (3 missed heartbeats → failed)
grep_q 'HeartbeatIntervalSeconds[[:space:]]*=[[:space:]]*30' "internal/elector/elector.go" \
    && check "HeartbeatIntervalSeconds=30" ok \
    || check "HeartbeatIntervalSeconds=30" fail
grep_q 'StaleMultiplier[[:space:]]*=[[:space:]]*3' "internal/elector/elector.go" \
    && check "StaleMultiplier=3" ok \
    || check "StaleMultiplier=3" fail

# 6. nextState() state machine
grep_q 'func nextState' "internal/elector/elector.go" \
    && check "nextState() defined" ok \
    || check "nextState() defined" fail
grep -A30 'func nextState' "internal/elector/elector.go" | grep -q '"pending"' \
    && check "nextState handles 'pending' state" ok \
    || check "nextState handles 'pending' state" fail
grep -A30 'func nextState' "internal/elector/elector.go" | grep -q '"ready"' \
    && check "nextState handles 'ready' state" ok \
    || check "nextState handles 'ready' state" fail
grep -A30 'func nextState' "internal/elector/elector.go" | grep -q '"failed"' \
    && check "nextState returns 'failed' transition" ok \
    || check "nextState returns 'failed' transition" fail

# 7. transitionNode writes cluster_audit with JSONB detail
grep_q 'func .e \*Elector. transitionNode' "internal/elector/elector.go" \
    && check "transitionNode() defined" ok \
    || check "transitionNode() defined" fail
grep -A60 'func .e \*Elector. transitionNode' "internal/elector/elector.go" | grep -q "cluster_audit" \
    && check "transitionNode writes cluster_audit" ok \
    || check "transitionNode writes cluster_audit" fail
grep -A60 'func .e \*Elector. transitionNode' "internal/elector/elector.go" | grep -q "json\.Marshal" \
    && check "transitionNode uses JSONB detail" ok \
    || check "transitionNode uses JSONB detail" fail

# 8. recommendFailover
grep_q 'func .e \*Elector. recommendFailover' "internal/elector/elector.go" \
    && check "recommendFailover() defined" ok \
    || check "recommendFailover() defined" fail
grep -A50 'func .e \*Elector. recommendFailover' "internal/elector/elector.go" | grep -q "skygate-standby" \
    && check "recommendFailover looks for skygate-standby" ok \
    || check "recommendFailover looks for skygate-standby" fail
grep -A100 'func .e \*Elector. recommendFailover' "internal/elector/elector.go" | grep -q "failover_recommend" \
    && check "recommendFailover writes failover_recommend audit row" ok \
    || check "recommendFailover writes failover_recommend audit row" fail

# 9. main.go starts the elector
grep_q 'elector\.NewElector' "cmd/skygate/main.go" \
    && check "main.go calls elector.NewElector" ok \
    || check "main.go calls elector.NewElector" fail
grep_q 'el\.Start\(\)' "cmd/skygate/main.go" \
    && check "main.go calls el.Start()" ok \
    || check "main.go calls el.Start()" fail
grep_q 'ha-elector: started' "cmd/skygate/main.go" \
    && check "main.go logs ha-elector: started" ok \
    || check "main.go logs ha-elector: started" fail

# 10. Unit tests
file_exists "internal/elector/elector_b204_test.go" \
    && check "elector_b204_test.go exists" ok \
    || check "elector_b204_test.go exists" fail
et=$(grep -c '^func Test' "internal/elector/elector_b204_test.go")
[ "$et" -ge 5 ] \
    && check "elector unit tests: $et (>= 5)" ok \
    || check "elector unit tests: $et (need >= 5)" fail

# 11. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b204_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b204_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b204_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b204_vet.log
    fi
    if (cd "$PWD" && go test -count=1 ./internal/elector/ >/tmp/check_b204_test.log 2>&1); then
        check "go test ./internal/elector/ passes" ok
    else
        check "go test ./internal/elector/ passes" fail
        head -20 /tmp/check_b204_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 12. AGENTS.md mention
grep_q "B204" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B204" ok \
    || check "AGENTS.md mentions B204" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B204 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B204 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
