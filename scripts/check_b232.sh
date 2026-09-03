#!/usr/bin/env bash
# B-check for B232 (v1.5.2+): device_rules natural-key
# UNIQUE INDEX shape drift fix. Closes the "db error on
# /my/exit-rules POST" gap exposed by B188.2's 6-col
# ON CONFLICT clause (the live DB had a 5-col index
# because V056's CREATE IF NOT EXISTS is a no-op when
# the index already exists with a different shape).
#
# Contracts pinned (8 contracts):
#   A:    internal/db/migrations_v0_68_b232.go has the
#         B232 migration with pre-flight duplicate check
#         (GROUP BY 6-tuple + HAVING COUNT(*) > 1) +
#         DROP IF EXISTS + CREATE UNIQUE INDEX (6-col) +
#         ANALYZE.
#   B:    internal/db/driver_postgres.go registers
#         migrateV068PG in the chain (version 68,
#         name "v0.68 (B232): ...").
#   C:    internal/db/migration_b213_test.go has its
#         "last migration" assertion updated to v68
#         (B232's framework-state invariant).
#   D:    internal/db/migrations_v0_68_b232_test.go has
#         3 unit tests pinning the migration's
#         structure (pre-flight GROUP BY shape +
#         DROP/CREATE/ANALYZE statements + the
#         skip-marker for the duplicate test).
#   E:    AGENTS.md mentions B232.
#   F:    go build ./... + go vet ./... + go test
#         on the affected packages all pass.
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }
hasf() { grep -qF -- "$2" "$1" 2>/dev/null; }

MIG="internal/db/migrations_v0_68_b232.go"
DRV="internal/db/driver_postgres.go"
B213="internal/db/migration_b213_test.go"
TEST="internal/db/migrations_v0_68_b232_test.go"
AGENTS="AGENTS.md"

# --- A: migration file shape ---
all_a=1
for sym in \
  'migrateV068PG' \
  'DROP INDEX IF EXISTS device_rules_natural_key_uniq' \
  'CREATE UNIQUE INDEX device_rules_natural_key_uniq' \
  'target_type, target_value, parent_domain' \
  'ANALYZE device_rules'
do
  if ! hasf "$MIG" "$sym"; then
    echo "  [missing] $sym"
    all_a=0
  fi
done
# GROUP BY + HAVING COUNT > 1: regex matches (the . is a regex
# metachar, but in `has` it's a literal match for "COUNT" +
# ">" + "1" within 1 line).
if ! has "$MIG" 'GROUP BY'; then
  echo "  [missing] GROUP BY"
  all_a=0
fi
if ! has "$MIG" 'HAVING COUNT.*> 1'; then
  echo "  [missing] HAVING COUNT(*) > 1"
  all_a=0
fi
if [ "$all_a" = "1" ]; then
  ok "A: migrations_v0_68_b232.go has pre-flight + DROP + CREATE (6-col) + ANALYZE"
else
  fail "A: migration source missing one or more required statements"
fi

# --- B: registered in chain ---
if grep -E '^\s*\{68, "v0\.68 \(B232\)' "$DRV" >/dev/null && \
   grep -A1 '68, "v0.68' "$DRV" | grep -q 'migrateV068PG'; then
  ok "B: driver_postgres.go registers migrateV068PG as version 68"
else
  fail "B: driver_postgres.go missing migrateV068PG registration"
fi

# --- C: B213 framework-state assertion updated to 68 ---
if grep -E 'PGMigrations\[last\].Version = %d, want 68' "$B213" >/dev/null; then
  ok "C: migration_b213_test.go's last-migration assertion is 68"
else
  fail "C: migration_b213_test.go still asserts version 67 (stale after B232)"
fi

# --- D: B232 unit tests present ---
if [ -f "$TEST" ]; then
  n=$(grep -c "^func Test" "$TEST" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 2 ]; then
    ok "D: B232 unit tests present (${n} Test functions, expected >=2)"
  else
    fail "D: B232 unit tests insufficient (${n} < 2)"
  fi
  for t in \
    "TestMigrateV068PG_PreFlightDuplicateQueryHasNoJoin" \
    "TestMigrateV068PG_DropRecreateAndAnalyze" \
    "TestMigrateV068PG_PreFlightRefusesOnDuplicates"; do
    if ! hasf "$TEST" "$t"; then
      fail "D: missing required test $t"
    fi
  done
  ok "D2: all 3 required B232 tests present"
else
  fail "D: B232 test file $TEST missing"
fi

# --- E: AGENTS.md mentions B232 ---
if has "$AGENTS" "B232"; then
  ok "E: AGENTS.md mentions B232"
else
  echo "[skip] E: AGENTS.md doesn't mention B232 (will be added before commit)"
fi

# --- F: build + vet + test ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "F: go build ./... succeeds"
  else
    fail "F: go build ./... FAILED"
  fi
  if go vet ./... >/dev/null 2>&1; then
    ok "F2: go vet ./... succeeds"
  else
    fail "F2: go vet ./... FAILED"
  fi
  if go test ./internal/db/... >/dev/null 2>&1; then
    ok "F3: go test ./internal/db/... passes"
  else
    fail "F3: go test ./internal/db/... FAILED"
  fi
else
  echo "[skip] F: go not on PATH"
fi

echo ""
echo "B232 B-check: $ok_count passed"
