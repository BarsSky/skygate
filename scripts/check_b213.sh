#!/usr/bin/env bash
# check_b213.sh — B213 (v1.5.0+) `skygate migrate` in-DB
# schema migration CLI. Pins the structural + wiring
# contracts so a future refactor that silently breaks
# the path (drops the bookkeeping, renames the
# subcommand, breaks the help text) is caught.
#
# Each `check` is one row; pass = exit 0, fail = exit 1.
# Run from the repo root:
#
#   bash scripts/check_b213.sh
#
# The script does NOT touch the DB (no live-verify
# needed for these — they're pure source / wiring
# pins). Live-verify on the agent is documented in
# AGENTS.md §B213 and exercised via
# scripts/b213_migrate_verify.sh.
set -euo pipefail

# REPO_ROOT resolution (same pattern as check_b211.sh / check_b212.sh).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ -n "${SKYGATE_REPO_ROOT:-}" ]]; then
  REPO_ROOT="$SKYGATE_REPO_ROOT"
else
  REPO_ROOT="$SCRIPT_DIR"
  while [[ "$REPO_ROOT" != "/" ]] && [[ ! -f "$REPO_ROOT/go.mod" ]]; do
    REPO_ROOT="$(dirname "$REPO_ROOT")"
  done
  if [[ ! -f "$REPO_ROOT/go.mod" ]]; then
    REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
  fi
fi
cd "$REPO_ROOT"

# Go binary resolution.
if [[ -n "${GO_BIN:-}" ]]; then
  GO="$GO_BIN"
elif command -v go >/dev/null 2>&1; then
  GO="go"
elif [[ -x "/c/Program Files/Go/bin/go.exe" ]]; then
  GO="/c/Program Files/Go/bin/go.exe"
elif [[ -x "/usr/local/go/bin/go" ]]; then
  GO="/usr/local/go/bin/go"
else
  GO="go"
fi

PASS=0
FAIL=0

check() {
  local label="$1"
  local want="$2"
  local got="$3"
  if [[ "$got" == "$want" ]]; then
    echo "[ok]   $label"
    PASS=$((PASS + 1))
  else
    echo "[FAIL] $label"
    echo "       want: $want"
    echo "       got:  $got"
    FAIL=$((FAIL + 1))
  fi
}

# A: cmd/skygate/migrate.go exists
[[ -f "$REPO_ROOT/cmd/skygate/migrate.go" ]]
check "A: cmd/skygate/migrate.go exists" "0" "$?"

# B: migrate.go has runMigrateSubcommand
grep -q "^func runMigrateSubcommand" "$REPO_ROOT/cmd/skygate/migrate.go"
check "B: runMigrateSubcommand dispatcher exists" "0" "$?"

# C: migrate.go has runMigrateUp
grep -q "^func runMigrateUp" "$REPO_ROOT/cmd/skygate/migrate.go"
check "C: runMigrateUp function exists" "0" "$?"

# D: migrate.go has runMigrateStatus
grep -q "^func runMigrateStatus" "$REPO_ROOT/cmd/skygate/migrate.go"
check "D: runMigrateStatus function exists" "0" "$?"

# E: migrate.go has runMigrateDown (stub)
grep -q "^func runMigrateDown" "$REPO_ROOT/cmd/skygate/migrate.go"
check "E: runMigrateDown function exists (stub)" "0" "$?"

# F: main.go has the "migrate" case
grep -q 'case "migrate":' "$REPO_ROOT/cmd/skygate/main.go"
check "F: case \"migrate\" in main.go switch" "0" "$?"

# G: main.go dispatches to runMigrateSubcommand
grep -q "runMigrateSubcommand(os.Args\[2:\])" "$REPO_ROOT/cmd/skygate/main.go"
check "G: main.go dispatches to runMigrateSubcommand" "0" "$?"

# H: help text mentions "migrate [verb]"
grep -q "migrate \[verb\]" "$REPO_ROOT/cmd/skygate/main.go"
check "H: help text mentions 'migrate [verb]'" "0" "$?"

# I: driver_postgres.go has MigrationEntry struct
grep -q "^type MigrationEntry struct" "$REPO_ROOT/internal/db/driver_postgres.go"
check "I: MigrationEntry struct exists" "0" "$?"

# J: driver_postgres.go has pgMigrations slice
grep -q "^var pgMigrations" "$REPO_ROOT/internal/db/driver_postgres.go"
check "J: pgMigrations slice exists" "0" "$?"

# K: driver_postgres.go has PGMigrations() public getter
grep -q "^func PGMigrations()" "$REPO_ROOT/internal/db/driver_postgres.go"
check "K: PGMigrations() public getter exists" "0" "$?"

# L: MigratePostgres records each applied migration
grep -q "RecordMigrationApplied" "$REPO_ROOT/internal/db/driver_postgres.go"
check "L: MigratePostgres records each applied migration" "0" "$?"

# M: migration_tracking.go has ON CONFLICT DO NOTHING
grep -q "ON CONFLICT (version) DO NOTHING" "$REPO_ROOT/internal/db/migration_tracking.go"
check "M: RecordMigrationApplied is idempotent (ON CONFLICT)" "0" "$?"

# N: unit tests for PGMigrations
[[ -f "$REPO_ROOT/internal/db/migration_b213_test.go" ]]
check "N: internal/db/migration_b213_test.go exists" "0" "$?"

# O: unit tests for the CLI helpers
[[ -f "$REPO_ROOT/cmd/skygate/migrate_b213_test.go" ]]
check "O: cmd/skygate/migrate_b213_test.go exists" "0" "$?"

# P: pre-B213 migrate-only is preserved (backward compat)
grep -q 'case "migrate-only":' "$REPO_ROOT/cmd/skygate/main.go"
check "P: pre-B213 migrate-only preserved (backward compat)" "0" "$?"

# Q: go test passes (no DB required for B213 unit tests)
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO test ./internal/db/ ./cmd/skygate/ -run "TestPGMigrations|TestMigrationEntry|TestRecordMigrationApplied|TestRunMigrateSubcommand|TestStartsWithDash|TestMigrateSubcommand|TestMigrateDown" -count=1 -short >/dev/null 2>&1; then
    check "Q: go test (B213 unit tests) passes" "pass" "pass"
  else
    check "Q: go test (B213 unit tests) passes" "pass" "fail"
  fi
else
  echo "[skip] Q: go not on PATH — run on a host with go installed (e.g. the agent)"
fi

# R: go build works
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO build ./... >/dev/null 2>&1; then
    check "R: go build ./... succeeds" "pass" "pass"
  else
    check "R: go build ./... succeeds" "pass" "fail"
  fi
else
  echo "[skip] R: go not on PATH — run on a host with go installed"
fi

# S: skygate binary actually exposes the migrate subcommand
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  SKYGATE_TMP="$(mktemp -d)"
  trap 'rm -rf "$SKYGATE_TMP"' EXIT
  if $GO build -o "$SKYGATE_TMP/skygate" ./cmd/skygate 2>/dev/null; then
    MIGRATE_HELP="$("$SKYGATE_TMP/skygate" migrate --help 2>&1 | head -1)"
    if [[ "$MIGRATE_HELP" == "skygate migrate <verb> [args]" ]]; then
      check "S: skygate migrate --help prints usage" "match" "match"
    else
      check "S: skygate migrate --help prints usage" "match" "mismatch: $MIGRATE_HELP"
    fi
  else
    check "S: skygate migrate --help prints usage" "match" "could not build"
  fi
else
  echo "[skip] S: go not on PATH — run on a host with go installed"
fi

echo ""
echo "B213 B-check: $PASS passed, $FAIL failed"
[[ "$FAIL" == "0" ]]
