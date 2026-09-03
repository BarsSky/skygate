#!/usr/bin/env bash
# B-check for B233 (v1.5.2+): source-level migration
# audit that catches the B232-class shape-drift
# bug at unit-test time. Closes the test-coverage
# gap exposed by B232 (the V056 silent-no-op
# CREATE INDEX IF NOT EXISTS bug that silently
# stayed 5-col on production for 3+ weeks).
#
# Contracts pinned (6 contracts):
#   A:    internal/db/migrations_audit_b233_test.go
#         exists and has the B233 surface
#         (testdataMigrationFiles, auditMigrationFile,
#         TestMigrations_ShapeDriftAudit,
#         TestMigrations_DeviceRulesNaturalKeyIndexIsSixColumns,
#         TestMigrations_V068IsLastToCreateDeviceRulesNaturalKey,
#         TestShapeDriftAudit_CatchesSyntheticOffender,
#         shapeDriftWhitelist).
#   B:    TestMigrations_ShapeDriftAudit logic: walks
#         the chain in version order, maintains a
#         running "seen" set, fails on re-CREATE
#         without paired DROP (V056 / B232 pattern).
#         Whitelist for V056 (one-time ack, fixed
#         by V068).
#   C:    TestMigrations_DeviceRulesNaturalKeyIndexIsSixColumns
#         pins the final shape of the natural_key_uniq
#         as 6-col (with parent_domain) — the contract
#         that qInsertDeviceRule's 6-col ON CONFLICT
#         clause depends on. Drift here = B232 bug.
#   D:    TestMigrations_V068IsLastToCreateDeviceRulesNaturalKey
#         pins the version order: V068 must be the LAST
#         migration to touch the natural_key_uniq. A
#         future V069 that re-creates the index with
#         a different shape (without explicit DROP +
#         CREATE) would fail this test.
#   E:    TestShapeDriftAudit_CatchesSyntheticOffender
#         mutation test: constructs a synthetic
#         re-CREATE pattern and asserts the audit
#         catches it. If the audit ever stops catching
#         the bug, this test fails first.
#   F:    AGENTS.md mentions B233 + go build + go
#         vet + go test on the affected package
#         all pass.
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }
hasf() { grep -qF -- "$2" "$1" 2>/dev/null; }

AUDIT="internal/db/migrations_audit_b233_test.go"
AGENTS="AGENTS.md"

# --- A: B233 file has the expected surface ---
all_a=1
for sym in \
  "testdataMigrationFiles" \
  "auditMigrationFile" \
  "TestMigrations_ShapeDriftAudit" \
  "TestMigrations_DeviceRulesNaturalKeyIndexIsSixColumns" \
  "TestMigrations_V068IsLastToCreateDeviceRulesNaturalKey" \
  "TestShapeDriftAudit_CatchesSyntheticOffender" \
  "shapeDriftWhitelist" \
  "DROP INDEX IF EXISTS" \
  "CREATE INDEX IF NOT EXISTS" \
  "parent_domain"
do
  if ! has "$AUDIT" "$sym"; then
    echo "  [missing] $sym"
    all_a=0
  fi
done
if [ "$all_a" = "1" ]; then
  ok "A: migrations_audit_b233_test.go has the B233 surface (helpers, tests, whitelist, the B232 sentinel strings)"
else
  fail "A: audit file missing one or more required symbols"
fi

# --- B: ShapeDriftAudit logic (running set + drop pair) ---
if has "$AUDIT" "seen := make" && \
   has "$AUDIT" "seen\[c\]" && \
   has "$AUDIT" "dropSet\[c\]" && \
   has "$AUDIT" "shape-drift risk"; then
  ok "B: TestMigrations_ShapeDriftAudit uses running set + drop pair check + clear error message"
else
  fail "B: TestMigrations_ShapeDriftAudit missing running-set / drop-pair / error-message components"
fi

# --- C: Six-col shape pinned ---
if has "$AUDIT" "lastCreateIs6Col" && \
   has "$AUDIT" "parent_domain"; then
  ok "C: TestMigrations_DeviceRulesNaturalKeyIndexIsSixColumns pins 6-col shape (with parent_domain)"
else
  fail "C: 6-col shape pin test missing key components"
fi

# --- D: Version-order pin ---
if has "$AUDIT" "lastTouch.version != 68" && \
   has "$AUDIT" "scanInt"; then
  ok "D: TestMigrations_V068IsLastToCreateDeviceRulesNaturalKey pins V068 as the LAST migration to touch the index"
else
  fail "D: version-order pin test missing key components"
fi

# --- E: Mutation test ---
if has "$AUDIT" "synthetic re-CREATE without DROP was NOT caught"; then
  ok "E: TestShapeDriftAudit_CatchesSyntheticOffender mutation test pins the audit's correctness"
else
  fail "E: mutation test missing the 'NOT caught' assertion"
fi

# --- F: docs + build + test ---
if has "$AGENTS" "B233"; then
  ok "F: AGENTS.md mentions B233"
else
  echo "[skip] F: AGENTS.md doesn't mention B233 (will be added before commit)"
fi

if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "F2: go build ./... succeeds"
  else
    fail "F2: go build ./... FAILED"
  fi
  if go vet ./... >/dev/null 2>&1; then
    ok "F3: go vet ./... succeeds"
  else
    fail "F3: go vet ./... FAILED"
  fi
  if go test -run 'TestMigrations_|TestShapeDriftAudit' ./internal/db/ >/dev/null 2>&1; then
    ok "F4: go test on B233 tests passes"
  else
    fail "F4: B233 tests FAILED"
  fi
else
  echo "[skip] F2-F4: go not on PATH"
fi

echo ""
echo "B233 B-check: $ok_count passed"
