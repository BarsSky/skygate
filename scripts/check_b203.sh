#!/usr/bin/env bash
# B203 (v1.5.0+) — skygate-watchdog for cluster_database
# hot-reload (Phase 3.1 of cluster-management.md).
#
# The contracts:
#
#   1. internal/db/swapdb.go exists (ResettableDB type)
#   2. NewResettableDB constructor
#   3. ResettableDB embeds *sql.DB (so it satisfies *sql.DB)
#   4. Reset() method exists
#   5. Current() method returns the current pool
#   6. Close() method (one-shot, sets cur=nil)
#   7. Reset(nil) is a no-op
#   8. Reset swaps the embedded *sql.DB so promoted methods
#      see the new pool
#   9. Old pool Close is called in a goroutine
#  10. Methods return ErrConnDone when cur is nil
#  11. sqlDBShim interface covers all *sql.DB methods
#  12. RLock on every read (Exec, Query, QueryRow, BeginTx, Ping, Conn, Stats, Set*)
#  13. RLock is wait-free for readers (200 concurrent readers + 1 writer)
#  14. internal/watchdog/dbswap.go exists
#  15. DBSwap type with Start/Stop
#  16. Config struct with Interval/PingTimeout/Logger
#  17. DefaultConfig returns 5s/3s
#  18. tick() reads cluster_database, compares DSN, opens new pool, pings, calls Reset
#  19. redactDSN strips the password for logs
#  20. cmd/skygate/main.go starts the watchdog
#  21. main.go wraps app.DB in NewResettableDB
#  22. Unit tests: 7+ ResettableDB tests, 5+ watchdog tests
#  23. go build + vet + tests pass
#  24. AGENTS.md mentions B203

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
grep_q() { grep -qE "$1" "$2" 2>/dev/null; return $?; }

# 1. swapdb.go
file_exists "internal/db/swapdb.go" \
    && check "internal/db/swapdb.go exists" ok \
    || check "internal/db/swapdb.go exists" fail

# 2. NewResettableDB
grep_q '^func NewResettableDB' "internal/db/swapdb.go" \
    && check "NewResettableDB() defined" ok \
    || check "NewResettableDB() defined" fail

# 3. Embedding
grep_q 'type ResettableDB struct' "internal/db/swapdb.go" \
    && check "ResettableDB type defined" ok \
    || check "ResettableDB type defined" fail
grep_q '\*sql\.DB' "internal/db/swapdb.go" \
    && check "ResettableDB embeds *sql.DB" ok \
    || check "ResettableDB embeds *sql.DB" fail

# 4. Reset
grep_q 'func \(r \*ResettableDB\) Reset\(' "internal/db/swapdb.go" \
    && check "Reset() method" ok \
    || check "Reset() method" fail

# 5. Current
grep_q 'func \(r \*ResettableDB\) Current\(' "internal/db/swapdb.go" \
    && check "Current() method" ok \
    || check "Current() method" fail

# 6. Close
grep_q 'func \(r \*ResettableDB\) Close\(' "internal/db/swapdb.go" \
    && check "Close() method" ok \
    || check "Close() method" fail

# 7. Reset(nil) no-op
grep -A3 'func (r \*ResettableDB) Reset' "internal/db/swapdb.go" | grep -q 'newDB == nil' \
    && check "Reset(nil) is no-op" ok \
    || check "Reset(nil) is no-op" fail

# 8. Reset re-points embedded
grep -A20 'func (r \*ResettableDB) Reset' "internal/db/swapdb.go" | grep -q 'r\.DB = newDB' \
    && check "Reset re-points embedded *sql.DB" ok \
    || check "Reset re-points embedded *sql.DB" fail

# 9. Close in goroutine
grep -A20 'func (r \*ResettableDB) Reset' "internal/db/swapdb.go" | grep -q 'go func' \
    && check "Old pool Close in goroutine" ok \
    || check "Old pool Close in goroutine" fail

# 10. ErrConnDone
grep_q 'sql\.ErrConnDone' "internal/db/swapdb.go" \
    && check "Returns ErrConnDone when cur is nil" ok \
    || check "Returns ErrConnDone when cur is nil" fail

# 11. sqlDBShim
grep_q 'type sqlDBShim interface' "internal/db/swapdb.go" \
    && check "sqlDBShim interface defined" ok \
    || check "sqlDBShim interface defined" fail
grep_q 'var _ sqlDBShim' "internal/db/swapdb.go" \
    && check "Compile-time assertion *ResettableDB satisfies sqlDBShim" ok \
    || check "Compile-time assertion *ResettableDB satisfies sqlDBShim" fail

# 12. RLock on reads (count overrides)
override_count=$(grep -c 'r.mu.RLock()' "internal/db/swapdb.go")
[ "$override_count" -ge 8 ] \
    && check "RLock used in $override_count override methods" ok \
    || check "RLock used in only $override_count overrides (need >= 8)" fail

# 13. Concurrent readers test
grep_q 'TestResettableDB_ConcurrentReadersSafe' "internal/db/swapdb_b203_test.go" \
    && check "TestResettableDB_ConcurrentReadersSafe exists" ok \
    || check "TestResettableDB_ConcurrentReadersSafe exists" fail

# 14. watchdog/dbswap.go
file_exists "internal/watchdog/dbswap.go" \
    && check "internal/watchdog/dbswap.go exists" ok \
    || check "internal/watchdog/dbswap.go exists" fail

# 15. DBSwap type + Start/Stop
grep_q 'type DBSwap struct' "internal/watchdog/dbswap.go" \
    && check "DBSwap type defined" ok \
    || check "DBSwap type defined" fail
grep_q 'func \(w \*DBSwap\) Start\(' "internal/watchdog/dbswap.go" \
    && check "DBSwap.Start()" ok \
    || check "DBSwap.Start()" fail
grep_q 'func \(w \*DBSwap\) Stop\(' "internal/watchdog/dbswap.go" \
    && check "DBSwap.Stop()" ok \
    || check "DBSwap.Stop()" fail

# 16. Config struct
grep_q 'type Config struct' "internal/watchdog/dbswap.go" \
    && check "Config struct defined" ok \
    || check "Config struct defined" fail
grep_q 'Interval[[:space:]]+time\.Duration' "internal/watchdog/dbswap.go" \
    && check "Config.Interval" ok \
    || check "Config.Interval" fail
grep_q 'PingTimeout[[:space:]]+time\.Duration' "internal/watchdog/dbswap.go" \
    && check "Config.PingTimeout" ok \
    || check "Config.PingTimeout" fail

# 17. DefaultConfig
grep_q 'func DefaultConfig\(\) Config' "internal/watchdog/dbswap.go" \
    && check "DefaultConfig() defined" ok \
    || check "DefaultConfig() defined" fail
grep -A4 'func DefaultConfig' "internal/watchdog/dbswap.go" | grep -q '5 \* time\.Second' \
    && check "DefaultConfig Interval=5s" ok \
    || check "DefaultConfig Interval=5s" fail

# 18. tick() with Reset (search the whole file — tick() is long)
grep_q 'migrator\.Reset' "internal/watchdog/dbswap.go" \
    && check "tick() calls migrator.Reset on DSN change" ok \
    || check "tick() calls migrator.Reset on DSN change" fail
grep_q 'PingContext' "internal/watchdog/dbswap.go" \
    && check "tick() pings new pool before swap" ok \
    || check "tick() pings new pool before swap" fail

# 19. redactDSN
grep_q 'func redactDSN' "internal/watchdog/dbswap.go" \
    && check "redactDSN defined" ok \
    || check "redactDSN defined" fail

# 20. main.go starts watchdog
grep_q 'wd\.Start\(\)' "cmd/skygate/main.go" \
    && check "main.go calls wd.Start()" ok \
    || check "main.go calls wd.Start()" fail

# 21. main.go wraps DB
grep_q 'db\.NewResettableDB' "cmd/skygate/main.go" \
    && check "main.go wraps app.DB in NewResettableDB" ok \
    || check "main.go wraps app.DB in NewResettableDB" fail

# 22. Unit tests
rt=$(grep -c '^func TestResettableDB_' "internal/db/swapdb_b203_test.go")
[ "$rt" -ge 5 ] \
    && check "ResettableDB unit tests: $rt (>= 5)" ok \
    || check "ResettableDB unit tests: $rt (need >= 5)" fail
wt=$(grep -c '^func Test' "internal/watchdog/dbswap_b203_test.go")
[ "$wt" -ge 4 ] \
    && check "watchdog unit tests: $wt (>= 4)" ok \
    || check "watchdog unit tests: $wt (need >= 4)" fail

# 23. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b203_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b203_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b203_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b203_vet.log
    fi
    if (cd "$PWD" && go test -run 'TestResettableDB_|TestStringArray_|TestGetClusterDatabase_|TestDBSwap|TestRedactDSN|TestDefaultConfig|TestNewDBSwap|TestClusterDatabaseRow_|TestBackendPID_' ./internal/db/ ./internal/watchdog/ -count=1 >/tmp/check_b203_test.log 2>&1); then
        check "B203 unit tests (swapdb + watchdog + StringArray + GetClusterDatabase) pass" ok
    else
        check "B203 unit tests (swapdb + watchdog + StringArray + GetClusterDatabase) pass" fail
        head -30 /tmp/check_b203_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 24. AGENTS.md mention
grep_q "B203" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B203" ok \
    || check "AGENTS.md mentions B203" fail

# 25. GetClusterDatabase NULL handling (B203 live bug — watchdog
# Scan error on primary_node_id = NULL). The COALESCE on the
# SELECT statement makes NULL read as "" instead of crashing.
grep_q 'COALESCE\(primary_node_id' "internal/db/cluster.go" \
    && check "GetClusterDatabase COALESCE on primary_node_id (NULL safety)" ok \
    || check "GetClusterDatabase COALESCE on primary_node_id (NULL safety)" fail

# 26. B203 unit tests for GetClusterDatabase NULL regression
b203_cluster_tests=$(grep -c '^func TestGetClusterDatabase_' "internal/db/cluster_b203_test.go")
[ "$b203_cluster_tests" -ge 3 ] \
    && check "GetClusterDatabase NULL regression tests: $b203_cluster_tests (>= 3)" ok \
    || check "GetClusterDatabase NULL regression tests: $b203_cluster_tests (need >= 3)" fail

# 27. StringArray type for PG TEXT[] columns (B203.1 stealth fix
# discovered after COALESCE: pgx stdlib returns TEXT[] as a literal
# string, so we need sql.Scanner + driver.Valuer).
file_exists "internal/db/array.go" \
    && check "internal/db/array.go exists" ok \
    || check "internal/db/array.go exists" fail
grep_q 'type StringArray \[\]string' "internal/db/array.go" \
    && check "StringArray is []string with sql.Scanner" ok \
    || check "StringArray is []string with sql.Scanner" fail
grep_q 'func .s \*StringArray. Scan' "internal/db/array.go" \
    && check "StringArray.Scan parses PG array literal" ok \
    || check "StringArray.Scan parses PG array literal" fail
grep_q 'func .s StringArray. Value' "internal/db/array.go" \
    && check "StringArray.Value serialises to PG array literal" ok \
    || check "StringArray.Value serialises to PG array literal" fail
grep -A2 'func needsQuoting' "internal/db/array.go" | grep -qF '\' \
    && check "needsQuoting includes backslash" ok \
    || check "needsQuoting includes backslash" fail

# 28. ClusterDatabase.ReplicaNodeIDs uses StringArray (not []string)
grep_q 'ReplicaNodeIDs[[:space:]]+StringArray' "internal/db/cluster.go" \
    && check "ClusterDatabase.ReplicaNodeIDs is StringArray" ok \
    || check "ClusterDatabase.ReplicaNodeIDs is StringArray" fail

# 29. StringArray unit tests
sa_tests=$(grep -c '^func TestStringArray_' "internal/db/array_b203_test.go")
[ "$sa_tests" -ge 4 ] \
    && check "StringArray unit tests: $sa_tests (>= 4)" ok \
    || check "StringArray unit tests: $sa_tests (need >= 4)" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B203 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B203 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
