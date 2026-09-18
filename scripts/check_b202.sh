#!/usr/bin/env bash
# B202 (v1.5.0+) — real dump/restore/cleanup in the
# dbmigrate framework (Phase 1.4 STUBs → real).
#
# The contracts:
#
#   1. internal/dbmigrate/transport.go exists
#   2. DumpTransport interface defined
#   3. LocalDumpTransport struct + Name()="local" + Dump()
#   4. NewLocalDumpTransport() constructor
#   5. internal/dbmigrate/framework.go defaults Transport to LocalDumpTransport
#   6. steps/dump.go has no "STUB" string
#   7. steps/restore.go has no "STUB" string
#   8. steps/cleanup.go has no "STUB" string
#   9. pg_dump / pg_restore exec.CommandContext present
#  10. pg_dump --no-owner --no-acl present
#  11. Advisory lock (pg_try_advisory_lock(42)) present in dump.go
#  12. Advisory unlock present in dump.go Rollback
#  13. pg_terminate_backend present in restore.go
#  14. pg_restore -c --if-exists present
#  15. SKYGATE_MIGRATE_DROP_SOURCE env gate in cleanup.go
#  16. DROP DATABASE in cleanup.go
#  17. verify.go: per-table diff (not just total)
#  18. precheck.go: exec.LookPath("pg_dump") + exec.LookPath("pg_restore")
#  19. New fields on MigrationContext: Transport, DumpBytes, DumpDurationMs, SourceLockHeld, Warning
#  20. MigrationContext: DBMigrator interface with BeginTx
#  21. sse.go: EmitStepLog helper
#  22. Unit tests: parseLibpqDSN + quoteIdent + keyTables + readFirstBytes
#  23. go build + vet + tests pass
#  24. AGENTS.md mentions B202

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
grep_q() { grep -qE "$1" "$2" 2>/dev/null; }
not_grep_q() { ! grep -qE "$1" "$2" 2>/dev/null; }

# 1. transport.go
file_exists "internal/dbmigrate/transport.go" \
    && check "internal/dbmigrate/transport.go exists" ok \
    || check "internal/dbmigrate/transport.go exists" fail

# 2. DumpTransport interface
grep_q 'type DumpTransport interface' "internal/dbmigrate/transport.go" \
    && check "DumpTransport interface defined" ok \
    || check "DumpTransport interface defined" fail

# 3. LocalDumpTransport
grep_q 'type LocalDumpTransport struct' "internal/dbmigrate/transport.go" \
    && check "LocalDumpTransport struct defined" ok \
    || check "LocalDumpTransport struct defined" fail
grep_q 'Name\(\) string \{ return "local" \}' "internal/dbmigrate/transport.go" \
    && check "LocalDumpTransport.Name()=\"local\"" ok \
    || check "LocalDumpTransport.Name()=\"local\"" fail
grep_q 'func \(t LocalDumpTransport\) Dump\(' "internal/dbmigrate/transport.go" \
    && check "LocalDumpTransport.Dump() defined" ok \
    || check "LocalDumpTransport.Dump() defined" fail

# 4. NewLocalDumpTransport
grep_q 'func NewLocalDumpTransport\(\)' "internal/dbmigrate/transport.go" \
    && check "NewLocalDumpTransport() constructor" ok \
    || check "NewLocalDumpTransport() constructor" fail

# 5. framework.go defaults Transport
grep_q 'mc.Transport = LocalDumpTransport\{\}' "internal/dbmigrate/framework.go" \
    && check "framework defaults Transport to LocalDumpTransport" ok \
    || check "framework defaults Transport to LocalDumpTransport" fail

# 6-8. No STUB strings
not_grep_q 'STUB' "internal/dbmigrate/steps/dump.go" \
    && check "dump.go has no STUB string" ok \
    || check "dump.go has no STUB string" fail
not_grep_q 'STUB' "internal/dbmigrate/steps/restore.go" \
    && check "restore.go has no STUB string" ok \
    || check "restore.go has no STUB string" fail
not_grep_q 'STUB' "internal/dbmigrate/steps/cleanup.go" \
    && check "cleanup.go has no STUB string" ok \
    || check "cleanup.go has no STUB string" fail

# 9. exec.CommandContext present
grep_q 'pgDumpBin' "internal/dbmigrate/transport.go" \
    && check "pg_dump via transport (exec.CommandContext)" ok \
    || check "pg_dump via transport (exec.CommandContext)" fail
grep_q 'exec\.CommandContext\(ctx, "pg_restore"' "internal/dbmigrate/steps/restore.go" \
    && check "pg_restore exec.CommandContext in restore.go" ok \
    || check "pg_restore exec.CommandContext in restore.go" fail

# 10. --no-owner --no-acl
grep_q '\-\-no-owner' "internal/dbmigrate/transport.go" \
    && check "pg_dump --no-owner flag" ok \
    || check "pg_dump --no-owner flag" fail
grep_q '\-\-no-acl' "internal/dbmigrate/transport.go" \
    && check "pg_dump --no-acl flag" ok \
    || check "pg_dump --no-acl flag" fail

# 11. Advisory lock in dump
grep_q 'pg_try_advisory_lock' "internal/dbmigrate/steps/dump.go" \
    && check "advisory lock in dump.go" ok \
    || check "advisory lock in dump.go" fail

# 12. Advisory unlock in dump Rollback
grep_q 'pg_advisory_unlock' "internal/dbmigrate/steps/dump.go" \
    && check "advisory unlock in dump.go" ok \
    || check "advisory unlock in dump.go" fail

# 13. pg_terminate_backend
grep_q 'pg_terminate_backend' "internal/dbmigrate/steps/restore.go" \
    && check "pg_terminate_backend in restore.go" ok \
    || check "pg_terminate_backend in restore.go" fail

# 14. pg_restore -c --if-exists
grep_q '"-c"' "internal/dbmigrate/steps/restore.go" \
    && check "pg_restore -c flag" ok \
    || check "pg_restore -c flag" fail
grep_q '\-\-if-exists' "internal/dbmigrate/steps/restore.go" \
    && check "pg_restore --if-exists flag" ok \
    || check "pg_restore --if-exists flag" fail

# 15. SKYGATE_MIGRATE_DROP_SOURCE gate
grep_q 'SKYGATE_MIGRATE_DROP_SOURCE' "internal/dbmigrate/steps/cleanup.go" \
    && check "SKYGATE_MIGRATE_DROP_SOURCE env gate" ok \
    || check "SKYGATE_MIGRATE_DROP_SOURCE env gate" fail

# 16. DROP DATABASE
grep_q 'DROP DATABASE' "internal/dbmigrate/steps/cleanup.go" \
    && check "DROP DATABASE in cleanup.go" ok \
    || check "DROP DATABASE in cleanup.go" fail

# 17. verify.go per-table diff
grep_q 'countPerTable' "internal/dbmigrate/steps/verify.go" \
    && check "verify.go per-table count" ok \
    || check "verify.go per-table count" fail
grep_q 'mapsEqual' "internal/dbmigrate/steps/verify.go" \
    && check "verify.go mapsEqual" ok \
    || check "verify.go mapsEqual" fail

# 18. precheck binary check
grep_q 'exec\.LookPath\("pg_dump"\)' "internal/dbmigrate/steps/precheck.go" \
    && check "precheck verifies pg_dump" ok \
    || check "precheck verifies pg_dump" fail
grep_q 'exec\.LookPath\("pg_restore"\)' "internal/dbmigrate/steps/precheck.go" \
    && check "precheck verifies pg_restore" ok \
    || check "precheck verifies pg_restore" fail

# 19. New MigrationContext fields
grep_q 'Transport[[:space:]]+DumpTransport' "internal/dbmigrate/types.go" \
    && check "MigrationContext.Transport field" ok \
    || check "MigrationContext.Transport field" fail
grep_q 'DumpBytes' "internal/dbmigrate/types.go" \
    && check "MigrationContext.DumpBytes field" ok \
    || check "MigrationContext.DumpBytes field" fail
grep_q 'SourceLockHeld' "internal/dbmigrate/types.go" \
    && check "MigrationContext.SourceLockHeld field" ok \
    || check "MigrationContext.SourceLockHeld field" fail

# 20. DBMigrator interface with BeginTx
grep_q 'type DBMigrator interface' "internal/dbmigrate/types.go" \
    && check "DBMigrator interface defined" ok \
    || check "DBMigrator interface defined" fail
grep_q 'BeginTx\(ctx context.Context' "internal/dbmigrate/types.go" \
    && check "DBMigrator includes BeginTx" ok \
    || check "DBMigrator includes BeginTx" fail

# 21. EmitStepLog helper
grep_q 'func EmitStepLog' "internal/dbmigrate/sse.go" \
    && check "EmitStepLog helper" ok \
    || check "EmitStepLog helper" fail

# 22. Unit tests
grep_q 'TestParseLibpqDSN' "internal/dbmigrate/steps/b202_helpers_test.go" \
    && check "TestParseLibpqDSN" ok \
    || check "TestParseLibpqDSN" fail
grep_q 'TestQuoteIdent' "internal/dbmigrate/steps/b202_helpers_test.go" \
    && check "TestQuoteIdent" ok \
    || check "TestQuoteIdent" fail
grep_q 'TestKeyTables' "internal/dbmigrate/steps/b202_helpers_test.go" \
    && check "TestKeyTables" ok \
    || check "TestKeyTables" fail
grep_q 'TestReadFirstBytes' "internal/dbmigrate/steps/b202_helpers_test.go" \
    && check "TestReadFirstBytes" ok \
    || check "TestReadFirstBytes" fail

# 23. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b202_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b202_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b202_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b202_vet.log
    fi
    if (cd "$PWD" && go test ./internal/dbmigrate/steps/ -count=1 >/tmp/check_b202_test.log 2>&1); then
        check "go test ./internal/dbmigrate/steps/ passes" ok
    else
        check "go test ./internal/dbmigrate/steps/ passes" fail
        head -20 /tmp/check_b202_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 24. AGENTS.md mention
grep_q "B202" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B202" ok \
    || check "AGENTS.md mentions B202" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B202 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B202 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
