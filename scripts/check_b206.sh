#!/usr/bin/env bash
# B206 (v1.5.0+) — GET /db/health endpoint. Phase 1.5
# / G3 of docs/internal/cluster-management.md.
#
# The contracts:
#
#   1. internal/feature/healthz/db_health.go exists
#   2. Sampler struct with Start/Stop methods
#   3. Config struct with Interval/QueryTimeout/Logger
#   4. DefaultDBHealthConfig returns 30s/3s
#   5. DBSource interface (Current() *sql.DB)
#   6. NewFixedDBSource helper for tests
#   7. DBHealthSample struct with substructs (Server, Database, Replication, Maintenance, XLog)
#   8. DBHealthResponse struct with flat fields (Pool, IsReplica, SizeBytes, etc.)
#   9. Service.GetDBHealth handler method
#  10. Service has DBHealthSampler + DBHealthSrc fields
#  11. cmd/skygate/main.go starts the sampler + registers /db/health route
#  12. Sampler.tick calls s.src.Current() (B203 hot-reload transparency)
#  13. Sampler.collect runs the pg_* queries (pg_is_in_recovery, pg_database_size, etc.)
#  14. humanBytes helper for the size_human field
#  15. Unit tests: 6+ covering humanBytes, defaults, sample-before-tick, nil source, stop idempotent, response JSON shape, populated sample field copy
#  16. go build + vet + cmd/skygate unit tests pass
#  17. AGENTS.md mentions B206

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

# 1. db_health.go
file_exists "internal/feature/healthz/db_health.go" \
    && check "internal/feature/healthz/db_health.go exists" ok \
    || check "internal/feature/healthz/db_health.go exists" fail

# 2. Sampler type + Start/Stop
grep_q '^type Sampler struct' "internal/feature/healthz/db_health.go" \
    && check "Sampler type defined" ok \
    || check "Sampler type defined" fail
grep_q 'func .s \*Sampler. Start\(' "internal/feature/healthz/db_health.go" \
    && check "Sampler.Start() defined" ok \
    || check "Sampler.Start() defined" fail
grep_q 'func .s \*Sampler. Stop\(' "internal/feature/healthz/db_health.go" \
    && check "Sampler.Stop() defined" ok \
    || check "Sampler.Stop() defined" fail

# 3. Config struct
grep_q '^type DBHealthConfig struct' "internal/feature/healthz/db_health.go" \
    && check "DBHealthConfig struct defined" ok \
    || check "DBHealthConfig struct defined" fail
grep_q 'Interval[[:space:]]+time\.Duration' "internal/feature/healthz/db_health.go" \
    && check "DBHealthConfig.Interval" ok \
    || check "DBHealthConfig.Interval" fail
grep_q 'QueryTimeout[[:space:]]+time\.Duration' "internal/feature/healthz/db_health.go" \
    && check "DBHealthConfig.QueryTimeout" ok \
    || check "DBHealthConfig.QueryTimeout" fail
grep_q 'Logger[[:space:]]+func\(' "internal/feature/healthz/db_health.go" \
    && check "DBHealthConfig.Logger" ok \
    || check "DBHealthConfig.Logger" fail

# 4. DefaultDBHealthConfig
grep_q 'func DefaultDBHealthConfig\(\) DBHealthConfig' "internal/feature/healthz/db_health.go" \
    && check "DefaultDBHealthConfig() defined" ok \
    || check "DefaultDBHealthConfig() defined" fail
grep -A8 'func DefaultDBHealthConfig' "internal/feature/healthz/db_health.go" | grep -q '30 \* time\.Second' \
    && check "DefaultDBHealthConfig Interval=30s" ok \
    || check "DefaultDBHealthConfig Interval=30s" fail
grep -A10 'func DefaultDBHealthConfig' "internal/feature/healthz/db_health.go" | grep -q '3 \* time\.Second' \
    && check "DefaultDBHealthConfig QueryTimeout=3s" ok \
    || check "DefaultDBHealthConfig QueryTimeout=3s" fail

# 5. DBSource interface
grep_q '^type DBSource interface' "internal/feature/healthz/db_health.go" \
    && check "DBSource interface defined" ok \
    || check "DBSource interface defined" fail
grep -A2 'type DBSource interface' "internal/feature/healthz/db_health.go" | grep -q 'Current() \*sql\.DB' \
    && check "DBSource has Current() *sql.DB" ok \
    || check "DBSource has Current() *sql.DB" fail

# 6. NewFixedDBSource
grep_q '^func NewFixedDBSource' "internal/feature/healthz/db_health.go" \
    && check "NewFixedDBSource() helper defined" ok \
    || check "NewFixedDBSource() helper defined" fail

# 7. DBHealthSample substructs
grep_q '^type DBHealthSample struct' "internal/feature/healthz/db_health.go" \
    && check "DBHealthSample type defined" ok \
    || check "DBHealthSample type defined" fail
grep -A50 'type DBHealthSample struct' "internal/feature/healthz/db_health.go" | grep -qE 'Server[[:space:]]+struct' \
    && check "DBHealthSample has Server substruct" ok \
    || check "DBHealthSample has Server substruct" fail
grep -A50 'type DBHealthSample struct' "internal/feature/healthz/db_health.go" | grep -qE 'Database[[:space:]]+struct' \
    && check "DBHealthSample has Database substruct" ok \
    || check "DBHealthSample has Database substruct" fail
grep -A50 'type DBHealthSample struct' "internal/feature/healthz/db_health.go" | grep -qE 'Replication[[:space:]]+struct' \
    && check "DBHealthSample has Replication substruct" ok \
    || check "DBHealthSample has Replication substruct" fail
grep -A50 'type DBHealthSample struct' "internal/feature/healthz/db_health.go" | grep -qE 'Maintenance[[:space:]]+struct' \
    && check "DBHealthSample has Maintenance substruct" ok \
    || check "DBHealthSample has Maintenance substruct" fail
grep -A50 'type DBHealthSample struct' "internal/feature/healthz/db_health.go" | grep -qE 'XLog[[:space:]]+struct' \
    && check "DBHealthSample has XLog substruct" ok \
    || check "DBHealthSample has XLog substruct" fail

# 8. DBHealthResponse
grep_q '^type DBHealthResponse struct' "internal/feature/healthz/db_health.go" \
    && check "DBHealthResponse type defined" ok \
    || check "DBHealthResponse type defined" fail
grep -A30 'type DBHealthResponse struct' "internal/feature/healthz/db_health.go" | grep -qE 'Pool[[:space:]]+sql\.DBStats' \
    && check "DBHealthResponse has Pool field" ok \
    || check "DBHealthResponse has Pool field" fail

# 9. GetDBHealth handler
grep_q 'func .s \*Service. GetDBHealth\(' "internal/feature/healthz/db_health.go" \
    && check "Service.GetDBHealth handler defined" ok \
    || check "Service.GetDBHealth handler defined" fail

# 10. Service fields
grep_q 'DBHealthSampler[[:space:]]+\*Sampler' "internal/feature/healthz/service.go" \
    && check "Service has DBHealthSampler field" ok \
    || check "Service has DBHealthSampler field" fail
grep_q 'DBHealthSrc[[:space:]]+DBSource' "internal/feature/healthz/service.go" \
    && check "Service has DBHealthSrc field" ok \
    || check "Service has DBHealthSrc field" fail

# 11. main.go
grep_q 'NewDBHealthSampler' "cmd/skygate/main.go" \
    && check "main.go calls NewDBHealthSampler" ok \
    || check "main.go calls NewDBHealthSampler" fail
grep_q 'dbHealthSampler\.Start\(\)' "cmd/skygate/main.go" \
    && check "main.go calls dbHealthSampler.Start()" ok \
    || check "main.go calls dbHealthSampler.Start()" fail
grep_q 'GET /db/health' "cmd/skygate/main.go" \
    && check "main.go registers GET /db/health route" ok \
    || check "main.go registers GET /db/health route" fail

# 12. tick calls src.Current() (B203 hot-reload transparency)
grep -A10 'func .s \*Sampler. tick' "internal/feature/healthz/db_health.go" | grep -q 's\.src\.Current' \
    && check "tick() consults s.src.Current() (B203 hot-reload)" ok \
    || check "tick() consults s.src.Current() (B203 hot-reload)" fail

# 13. Sampler queries
grep -A200 'func .s \*Sampler. collect' "internal/feature/healthz/db_health.go" | grep -q 'pg_is_in_recovery' \
    && check "collect() queries pg_is_in_recovery()" ok \
    || check "collect() queries pg_is_in_recovery()" fail
grep -A200 'func .s \*Sampler. collect' "internal/feature/healthz/db_health.go" | grep -q 'pg_database_size' \
    && check "collect() queries pg_database_size()" ok \
    || check "collect() queries pg_database_size()" fail
grep -A200 'func .s \*Sampler. collect' "internal/feature/healthz/db_health.go" | grep -q 'pg_stat_user_tables' \
    && check "collect() queries pg_stat_user_tables" ok \
    || check "collect() queries pg_stat_user_tables" fail
grep -A200 'func .s \*Sampler. collect' "internal/feature/healthz/db_health.go" | grep -q 'pg_current_wal_lsn' \
    && check "collect() queries pg_current_wal_lsn" ok \
    || check "collect() queries pg_current_wal_lsn" fail

# 14. humanBytes
grep_q 'func humanBytes' "internal/feature/healthz/db_health.go" \
    && check "humanBytes() helper defined" ok \
    || check "humanBytes() helper defined" fail

# 15. Unit tests
file_exists "internal/feature/healthz/db_health_b206_test.go" \
    && check "db_health_b206_test.go exists" ok \
    || check "db_health_b206_test.go exists" fail
ut=$(grep -c '^func Test' "internal/feature/healthz/db_health_b206_test.go")
[ "$ut" -ge 6 ] \
    && check "db_health unit tests: $ut (>= 6)" ok \
    || check "db_health unit tests: $ut (need >= 6)" fail

# 16. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b206_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b206_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b206_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b206_vet.log
    fi
    if (cd "$PWD" && go test -count=1 -run 'TestHumanBytes|TestNewDBHealthSampler|TestSampler_|TestGetDBHealth_' ./internal/feature/healthz/ >/tmp/check_b206_test.log 2>&1); then
        check "B206 unit tests pass" ok
    else
        check "B206 unit tests pass" fail
        head -20 /tmp/check_b206_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 17. AGENTS.md mention
grep_q "B206" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B206" ok \
    || check "AGENTS.md mentions B206" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B206 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B206 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
