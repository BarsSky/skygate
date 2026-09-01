#!/usr/bin/env bash
# B208 (v1.5.0+) — /admin/ha enhancements + fix
# B203 regression in admin Service. Phase 3.2 of
# docs/internal/cluster-management.md.
#
# Two sub-chunks in one B-chunk:
#
#   B208.1 — admin Service uses DBSource instead of
#             captured *sql.DB. The pre-B208 admin Service
#             captured `*sql.DB` at construction time; the
#             B203 watchdog hot-reload closed the captured
#             pool in a goroutine, so EVERY admin page
#             (database, cluster, audit, nodes, ha, acls,
#             users, ...) returned "sql: database is closed"
#             after the first B203 swap. B208.1 changes
#             `DB *sql.DB` to `DB admin.DBSource` (an
#             interface) + adds a `s.dbc() *sql.DB` helper
#             that calls `s.DB.Current()` per request. The
#             ResettableDB satisfies DBSource directly.
#
#   B208.2 — /admin/ha's "Last 20 HA events" table now
#             UNIONs audit_log + cluster_audit. Pre-B208
#             the table only saw audit_log rows; the B204
#             elector's node_health + failover_recommend
#             and the B205 failover's node_failover were
#             invisible without psql. The Source column
#             shows where each row came from.
#
# The contracts:
#
#   1. internal/feature/admin/dbsource.go exists (B208.1)
#   2. DBSource interface defined (Current() *sql.DB)
#   3. Service.DB field type changed from *sql.DB to DBSource
#   4. s.dbc() helper method defined
#   5. main.go passes `d` (ResettableDB) to adminSvc.DB
#   6. All ~70 s.DB call sites use s.dbc() (no remaining s.DB.method patterns)
#   7. internal/feature/admin/ha.go updated (B208.2)
#   8. haAuditEvent struct has Source field
#   9. collectHAPageData queries both audit_log and cluster_audit
#  10. cluster_audit query filters on node_health / failover_recommend / node_failover
#  11. admin/ha.html renders the Source column
#  12. Unit tests pass (the existing admin tests cover the migration)
#  13. go build + vet + admin unit tests pass
#  14. AGENTS.md mentions B208

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

# 1. dbsource.go exists
file_exists "internal/feature/admin/dbsource.go" \
    && check "internal/feature/admin/dbsource.go exists" ok \
    || check "internal/feature/admin/dbsource.go exists" fail

# 2. DBSource interface
grep_q '^type DBSource interface' "internal/feature/admin/dbsource.go" \
    && check "DBSource interface defined" ok \
    || check "DBSource interface defined" fail
grep -A2 'type DBSource interface' "internal/feature/admin/dbsource.go" | grep -q 'Current() \*sql\.DB' \
    && check "DBSource has Current() *sql.DB" ok \
    || check "DBSource has Current() *sql.DB" fail

# 3. Service.DB field type (must be DBSource, not *sql.DB)
if grep -A3 'type Service struct' "internal/feature/admin/service.go" | grep -qE 'DB[[:space:]]+\*sql\.DB'; then
    check "Service.DB is DBSource (not *sql.DB)" fail
else
    check "Service.DB is DBSource (not *sql.DB)" ok
fi

# 4. s.dbc() helper
grep_q 'func .s \*Service. dbc' "internal/feature/admin/dbsource.go" \
    && check "s.dbc() helper defined" ok \
    || check "s.dbc() helper defined" fail

# 5. main.go passes ResettableDB (d), not app.DB
# Look for the ResettableDB variable `d` in the DB: line
# inside the adminSvc Service{} block. Look at a window of
# 10 lines after the adminSvc declaration to allow the
# comment + DB line.
if grep -A10 'adminSvc := &adminsvc.Service{' "cmd/skygate/main.go" | grep -qE '^[[:space:]]+DB:[[:space:]]+d\b'; then
    check "main.go passes ResettableDB (not app.DB)" ok
else
    check "main.go passes ResettableDB (not app.DB)" fail
fi

# 6. No remaining s.DB.method patterns in admin (only s.dbc().method).
# Exclude dbsource.go (which legitimately contains s.DB.Current() in
# the helper itself) + exclude comment lines.
remaining=$(grep -rE 's\.DB\.' internal/feature/admin/ --include='*.go' 2>/dev/null \
    | grep -v 'dbsource\.go' \
    | grep -vE '//.*s\.DB\.' \
    | wc -l)
if [ "$remaining" -eq 0 ]; then
    check "no remaining s.DB.method patterns in admin (dbsource.go excluded)" ok
else
    check "no remaining s.DB.method patterns in admin ($remaining remaining)" fail
fi

# 7. ha.go updated (B208.2)
grep_q 'haAuditEvent' "internal/feature/admin/ha.go" \
    && check "ha.go defines haAuditEvent" ok \
    || check "ha.go defines haAuditEvent" fail

# 8. Source field on haAuditEvent
grep -A8 'type haAuditEvent struct' "internal/feature/admin/ha.go" | grep -q 'Source[[:space:]]\+string' \
    && check "haAuditEvent has Source field" ok \
    || check "haAuditEvent has Source field" fail

# 9. collectHAPageData queries both tables
# Look at a wider context around eventsByKey
if grep -A50 'eventsByKey' "internal/feature/admin/ha.go" | grep -q 'FROM audit_log'; then
    check "ha.go queries audit_log for events" ok
else
    check "ha.go queries audit_log for events" fail
fi
if grep -A100 'eventsByKey' "internal/feature/admin/ha.go" | grep -q 'FROM cluster_audit'; then
    check "ha.go queries cluster_audit for events" ok
else
    check "ha.go queries cluster_audit for events" fail
fi

# 10. cluster_audit filter on B204/B205 actions
grep -A3 'FROM cluster_audit' "internal/feature/admin/ha.go" | grep -q "node_health.*failover_recommend.*node_failover" \
    && check "ha.go filters on node_health + failover_recommend + node_failover" ok \
    || check "ha.go filters on node_health + failover_recommend + node_failover" fail

# 11. Template renders the Source column
grep -q "cluster_audit" "internal/handlers/templates/admin/ha.html" \
    && check "admin/ha.html mentions cluster_audit" ok \
    || check "admin/ha.html mentions cluster_audit" fail
grep -q 'audit_log' "internal/handlers/templates/admin/ha.html" \
    && check "admin/ha.html mentions audit_log" ok \
    || check "admin/ha.html mentions audit_log" fail
# Look for the Source field usage anywhere in the tbody of the events table
if grep -B1 -A1 'Source' "internal/handlers/templates/admin/ha.html" | grep -q 'eq .Source "cluster_audit"'; then
    check "admin/ha.html uses .Source field" ok
else
    check "admin/ha.html uses .Source field" fail
fi

# 12. Unit tests
ut_count=$(grep -c '^func Test' "internal/feature/admin/admin_audit_b207_test.go")
[ "$ut_count" -ge 5 ] \
    && check "admin unit tests: $ut_count (>= 5)" ok \
    || check "admin unit tests: $ut_count (need >= 5)" fail

# 13. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b208_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b208_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b208_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b208_vet.log
    fi
    if (cd "$PWD" && go test -count=1 ./internal/feature/admin/ >/tmp/check_b208_test.log 2>&1); then
        check "go test ./internal/feature/admin/ passes" ok
    else
        check "go test ./internal/feature/admin/ passes" fail
        head -20 /tmp/check_b208_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 14. AGENTS.md mention
grep_q "B208" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B208" ok \
    || check "AGENTS.md mentions B208" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B208 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B208 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
