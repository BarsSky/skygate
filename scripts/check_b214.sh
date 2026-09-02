#!/usr/bin/env bash
# check_b214.sh — B214 (v1.5.0+) /admin/database migration
# workflow: async Run + cancel + rollback endpoints +
# UI buttons + i18n. Phase 1.4.4 (cancellation) and
# 1.4.5 (rollback UI) of docs/internal/cluster-management.md.
#
# Each `check` is one row; pass = exit 0, fail = exit 1.
# Run from the repo root:
#
#   bash scripts/check_b214.sh
set -euo pipefail

# REPO_ROOT resolution (same pattern as check_b211/b212/b213.sh).
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

# A: framework refactor: Run() registers cancel func in live-runs
grep -q "registerLiveRun(mc.RunID, runCancel)" "$REPO_ROOT/internal/dbmigrate/framework.go"
check "A: Run() registers cancel func in live-runs registry" "0" "$?"

# B: Run() checks ctx between steps for cancellation
grep -q "runCtx.Err() == context.Canceled" "$REPO_ROOT/internal/dbmigrate/framework.go"
check "B: Run() checks cancellation between steps" "0" "$?"

# C: framework exports CancelRun + IsRunLive
grep -q "^func CancelRun" "$REPO_ROOT/internal/dbmigrate/framework.go"
check "C: CancelRun exported" "0" "$?"
grep -q "^func IsRunLive" "$REPO_ROOT/internal/dbmigrate/framework.go"
check "D: IsRunLive exported" "0" "$?"

# E: framework has RunCancelled sentinel
grep -q "RunCancelled\s*MigrationStatus = \"cancelled\"" "$REPO_ROOT/internal/dbmigrate/types.go"
check "E: RunCancelled sentinel exists in types.go" "0" "$?"

# F: handler: PostAdminDatabaseMigrateCancel
grep -q "func .* PostAdminDatabaseMigrateCancel" "$REPO_ROOT/internal/dbmigrate/handlers.go"
check "F: PostAdminDatabaseMigrateCancel handler exists" "0" "$?"

# G: handler: PostAdminDatabaseMigrateRollback
grep -q "func .* PostAdminDatabaseMigrateRollback" "$REPO_ROOT/internal/dbmigrate/handlers.go"
check "G: PostAdminDatabaseMigrateRollback handler exists" "0" "$?"

# H: handler: PostAdminDatabaseMigrate is async (goroutine)
grep -q 'go func()' "$REPO_ROOT/internal/dbmigrate/handlers.go"
check "H: PostAdminDatabaseMigrate is async (goroutine)" "0" "$?"

# I: main.go wires the cancel + rollback routes
grep -q '/admin/database/migrate/{id}/cancel' "$REPO_ROOT/cmd/skygate/main.go"
check "I: cancel route wired in main.go" "0" "$?"
grep -q '/admin/database/migrate/{id}/rollback' "$REPO_ROOT/cmd/skygate/main.go"
check "J: rollback route wired in main.go" "0" "$?"

# K: admin/database.go surfaces CanCancel + CanRollback to template
grep -q "CanCancel" "$REPO_ROOT/internal/feature/admin/database.go"
check "K: CanCancel surfaced in admin/database.go" "0" "$?"
grep -q "CanRollback" "$REPO_ROOT/internal/feature/admin/database.go"
check "L: CanRollback surfaced in admin/database.go" "0" "$?"

# M: template has the cancel/rollback forms
grep -q 'action="/admin/database/migrate/{{.Data.Run.ID}}/cancel"' "$REPO_ROOT/internal/handlers/templates/admin/migrate_run.html"
check "M: template has cancel form" "0" "$?"
grep -q 'action="/admin/database/migrate/{{.Data.Run.ID}}/rollback"' "$REPO_ROOT/internal/handlers/templates/admin/migrate_run.html"
check "N: template has rollback form" "0" "$?"

# O: i18n keys present (RU + EN)
grep -q '"db.migrate_cancel_btn"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "O: i18n db.migrate_cancel_btn present" "0" "$?"
grep -q '"db.migrate_rollback_btn"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "P: i18n db.migrate_rollback_btn present" "0" "$?"
grep -q '"db.migrate_cancel_confirm"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "Q: i18n db.migrate_cancel_confirm present" "0" "$?"
grep -q '"db.migrate_rollback_confirm"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "R: i18n db.migrate_rollback_confirm present" "0" "$?"

# S: unit tests
[[ -f "$REPO_ROOT/internal/dbmigrate/b214_test.go" ]]
check "S: internal/dbmigrate/b214_test.go exists" "0" "$?"

# T: parseTargetDSNForRollback helper exists
[[ -f "$REPO_ROOT/internal/dbmigrate/dsn_parse.go" ]]
check "T: internal/dbmigrate/dsn_parse.go exists (parseTargetDSNForRollback)" "0" "$?"

# U: go test passes
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO test ./internal/dbmigrate/... -run "TestParseTargetDSNForRollback|TestLiveRunsRegistry|TestRunCancelledStatus|TestRunIDFromPath" -count=1 -short >/dev/null 2>&1; then
    check "U: go test (B214 unit tests) passes" "pass" "pass"
  else
    check "U: go test (B214 unit tests) passes" "pass" "fail"
  fi
else
  echo "[skip] U: go not on PATH — run on a host with go installed (e.g. the agent)"
fi

# V: go build works
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO build ./... >/dev/null 2>&1; then
    check "V: go build ./... succeeds" "pass" "pass"
  else
    check "V: go build ./... succeeds" "pass" "fail"
  fi
else
  echo "[skip] V: go not on PATH — run on a host with go installed"
fi

# W: skygate binary actually exposes the cancel + rollback routes
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  SKYGATE_TMP="$(mktemp -d)"
  trap 'rm -rf "$SKYGATE_TMP"' EXIT
  if $GO build -o "$SKYGATE_TMP/skygate" ./cmd/skygate 2>/dev/null; then
    # We can't easily probe the routes (they need auth) but we
    # can check the binary doesn't crash on --help.
    if "$SKYGATE_TMP/skygate" --help >/dev/null 2>&1; then
      check "W: skygate binary builds + starts" "pass" "pass"
    else
      check "W: skygate binary builds + starts" "pass" "fail"
    fi
  else
    check "W: skygate binary builds + starts" "pass" "could not build"
  fi
else
  echo "[skip] W: go not on PATH — run on a host with go installed"
fi

echo ""
echo "B214 B-check: $PASS passed, $FAIL failed"
[[ "$FAIL" == "0" ]]
