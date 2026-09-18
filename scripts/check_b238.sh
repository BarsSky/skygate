#!/usr/bin/env bash
# scripts/check_b238.sh — B238 portal_users AFTER UPDATE audit trigger.
#
# Pins the 8-contract B-check for the DB-level password rotation
# detector (catches out-of-band changes that bypass skygate's
# feature/auth.PostLogin + /password_change flow):
#
#   A. internal/db/migrations_v0_70_b238.go exists with
#      migrateV070PG(d *sql.DB) error
#   B. The migration creates CREATE OR REPLACE FUNCTION
#      portal_users_audit_trigger (idempotent)
#   C. The migration drops + recreates the trigger
#      portal_users_audit ON portal_users (idempotent)
#   D. The trigger body uses OLD.password_hash IS DISTINCT FROM
#      NEW.password_hash (skip same-value updates, e.g. is_admin
#      changes that don't touch credentials)
#   E. The trigger writes action='password_change_db' into audit_log
#      with old_prefix + new_prefix + txid in detail (operator can
#      see WHICH transaction fired the change)
#   F. internal/db/driver_postgres.go has the v0.70 B238 entry
#      pointing at migrations_v0_70_b238.go
#   G. internal/db/migrations_v0_70_b238_test.go source tests pass
#      (TestMigrateV070PG_SourceShape + TestMigrateV070PG_Registered)
#   H. go build ./... succeeds with the new migration (catches
#      syntax / type errors at the driver_postgres level)
#
# Run from the project root: `bash scripts/check_b238.sh`
#
# 2026-09-11: B238 — closes the "out-of-band password rotation
# bypasses skygate UI audit" gap (operator 2026-09-11 cost-10 bcrypt
# via external CLI rotated skyadmin without leaving a UI trace).
set -euo pipefail

# Resolve `go` binary (same logic as check_b_mod_static_embed.sh).
GO="${GO_BIN:-}"
if [ -z "$GO" ]; then
    GO=$(command -v go 2>/dev/null || true)
fi
if [ -z "$GO" ]; then
    for CAND in \
        "/c/Program Files/Go/bin/go.exe" \
        "/c/Program Files (x86)/Go/bin/go.exe" \
        "/mnt/c/Program Files/Go/bin/go.exe" \
        "/mnt/c/Program Files (x86)/Go/bin/go.exe" \
        "/usr/local/go/bin/go" \
        "/usr/bin/go" \
        "/opt/homebrew/bin/go"; do
        if [ -x "$CAND" ]; then
            GO="$CAND"
            break
        fi
    done
fi
if [ -z "$GO" ] && command -v where.exe >/dev/null 2>&1; then
    WIN_GO=$(where.exe go 2>/dev/null | head -1 | tr -d '\r\n')
    if [ -n "$WIN_GO" ]; then
        GO="$WIN_GO"
    fi
fi
if [ -z "$GO" ]; then
    echo "FATAL: go binary not found" >&2
    exit 1
fi
GO_NORM=$(echo "$GO" | tr '\\' '/')

MIGRATION_GO="internal/db/migrations_v0_70_b238.go"
MIGRATION_TEST="internal/db/migrations_v0_70_b238_test.go"
DRIVER_GO="internal/db/driver_postgres.go"

pass() { printf "  PASS %s\n" "$1"; }
fail() { printf "  FAIL %s\n" "$1"; exit 1; }

echo "=== A. $MIGRATION_GO has migrateV070PG ==="
if [ ! -f "$MIGRATION_GO" ]; then
    fail "$MIGRATION_GO missing — B238 requires v0.70 migration source file"
fi
if grep -q 'func migrateV070PG' "$MIGRATION_GO"; then
    pass "migrateV070PG function present"
else
    fail "migrateV070PG function missing"
fi

echo "=== B. CREATE OR REPLACE FUNCTION portal_users_audit_trigger (idempotent) ==="
if grep -qE 'CREATE[[:space:]]+OR[[:space:]]+REPLACE[[:space:]]+FUNCTION[[:space:]]+portal_users_audit_trigger' "$MIGRATION_GO"; then
    pass "function uses CREATE OR REPLACE (idempotent on re-run)"
else
    fail "CREATE OR REPLACE FUNCTION portal_users_audit_trigger missing"
fi

echo "=== C. DROP TRIGGER IF EXISTS + CREATE TRIGGER portal_users_audit ==="
if grep -q 'DROP[[:space:]]\+TRIGGER[[:space:]]\+IF[[:space:]]\+EXISTS[[:space:]]\+portal_users_audit[[:space:]]\+ON[[:space:]]\+portal_users' "$MIGRATION_GO" \
   && grep -q 'CREATE[[:space:]]\+TRIGGER[[:space:]]\+portal_users_audit' "$MIGRATION_GO" \
   && grep -q 'AFTER[[:space:]]\+UPDATE[[:space:]]\+ON[[:space:]]\+portal_users' "$MIGRATION_GO" \
   && grep -q 'FOR[[:space:]]\+EACH[[:space:]]\+ROW' "$MIGRATION_GO" \
   && grep -q 'EXECUTE[[:space:]]\+FUNCTION[[:space:]]\+portal_users_audit_trigger()' "$MIGRATION_GO"; then
    pass "DROP IF EXISTS + CREATE TRIGGER + AFTER UPDATE FOR EACH ROW (idempotent)"
else
    fail "trigger DDL fragment missing (DROP IF EXISTS / CREATE TRIGGER / AFTER UPDATE / FOR EACH ROW / EXECUTE FUNCTION)"
fi

echo "=== D. Trigger body: OLD.password_hash IS DISTINCT FROM NEW.password_hash ==="
if grep -q 'OLD.password_hash[[:space:]]\+IS[[:space:]]\+DISTINCT[[:space:]]\+FROM[[:space:]]\+NEW.password_hash' "$MIGRATION_GO"; then
    pass "skip-same-value guard present (non-password UPDATEs don't fire)"
else
    fail "OLD IS DISTINCT FROM NEW guard missing — every UPDATE would fire the trigger"
fi

echo "=== E. Trigger writes action='password_change_db' + old_prefix + new_prefix + txid ==="
if grep -q "'password_change_db'" "$MIGRATION_GO" \
   && grep -q 'old_prefix=' "$MIGRATION_GO" \
   && grep -q 'new_prefix=' "$MIGRATION_GO" \
   && grep -q 'txid=' "$MIGRATION_GO" \
   && grep -q 'pg_current_xact_id' "$MIGRATION_GO"; then
    pass "action + 3-part detail (old_prefix / new_prefix / txid) + pg_current_xact_id"
else
    fail "trigger body missing password_change_db action or 3-part detail (old_prefix / new_prefix / txid)"
fi

echo "=== F. $DRIVER_GO has v0.70 B238 entry pointing at the right source file ==="
if grep -q 'migrateV070PG' "$DRIVER_GO"; then
    if grep -q 'migrations_v0_70_b238.go' "$DRIVER_GO"; then
        if grep -q '70, "v0.70 (B238): portal_users AFTER UPDATE audit trigger' "$DRIVER_GO"; then
            pass "registered in pgMigrations slice with v0.70 B238 label"
        else
            fail "v0.70 entry doesn't carry the (B238) label"
        fi
    else
        fail "v0.70 entry doesn't reference migrations_v0_70_b238.go"
    fi
else
    fail "migrateV070PG not in $DRIVER_GO — migration not registered in dispatch"
fi

echo "=== G. $MIGRATION_TEST source tests pass ==="
if [ ! -f "$MIGRATION_TEST" ]; then
    fail "$MIGRATION_TEST missing — B238 requires source-level test"
fi
LOG_G=$(mktemp -t check_b238_g.XXXXXX 2>/dev/null || mktemp)
# Run go test WITHOUT piping into grep -q (which would SIGPIPE the test
# mid-stream on the first match and abort before the second test runs).
# Capture full output, then check both PASS lines against the captured log.
if "$GO_NORM" test ./internal/db/ -run 'TestMigrateV070PG' -count=1 -v >"$LOG_G" 2>&1; then
    if grep -q '^--- PASS: TestMigrateV070PG_SourceShape' "$LOG_G"; then
        if grep -q '^--- PASS: TestMigrateV070PG_Registered' "$LOG_G"; then
            N=$(grep -c '^--- PASS' "$LOG_G" || true)
            pass "TestMigrateV070PG_SourceShape + TestMigrateV070PG_Registered PASS ($N tests)"
        else
            fail "TestMigrateV070PG_Registered did not pass — see $LOG_G"
        fi
    else
        fail "TestMigrateV070PG_SourceShape did not pass — see $LOG_G"
    fi
else
    fail "go test ./internal/db/ failed — see $LOG_G"
fi
rm -f "$LOG_G"

echo "=== H. go build ./... clean (catches driver_postgres wiring errors) ==="
LOG_H=$(mktemp -t check_b238_h.XXXXXX 2>/dev/null || mktemp)
if "$GO_NORM" build ./... 2>&1 | tee "$LOG_H"; then
    pass "go build ./... clean"
else
    fail "go build ./... failed — see $LOG_H"
fi
rm -f "$LOG_H"

echo
echo "PASS B238 (8/8 contracts)"
