#!/usr/bin/env bash
# ============================================================================
# check_b198.sh — B198 (v1.5.0+) DB migration workflow (Phase 1.4)
#
# Verifies:
#   A) internal/dbmigrate/ package exists with 5+ core files
#   B) 6 step files in steps/ (precheck, dump, restore, verify, flip, cleanup)
#   C) Each step implements the DeployStep interface
#   D) The framework has a Run() entry point with rollback chain
#   E) DB migration V065 adds dbmigrate_run + dbmigrate_step tables
#   F) The SSE broker has Subscribe + emit methods
#   G) Routes /admin/database/migrate* registered in main.go
#   H) AGENTS.md mentions B198
#   I) verify_pre_deploy.sh references check_b198.sh
#   J) go build / vet clean
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PASS=0
FAIL=0
ok() { echo "  ✓ $*"; PASS=$((PASS+1)); }
no() { echo "  ✗ $*"; FAIL=$((FAIL+1)); }

PKG="$PROJECT_DIR/internal/dbmigrate"
STEPS="$PROJECT_DIR/internal/dbmigrate/steps"
MIGRATION="$PROJECT_DIR/internal/db/migrations_v0_65_b198.go"
MAIN="$PROJECT_DIR/cmd/skygate/main.go"

# ----- A) package + core files -----------------------------------------
echo "A) internal/dbmigrate/ package + core files"
if [ -d "$PKG" ]; then
  ok "internal/dbmigrate/ directory exists"
else
  no "internal/dbmigrate/ directory missing"
fi
for f in types.go framework.go registry.go sse.go handlers.go; do
  if [ -f "$PKG/$f" ]; then
    ok "internal/dbmigrate/$f exists"
  else
    no "internal/dbmigrate/$f missing"
  fi
done

# ----- B) 6 step files ---------------------------------------------------
echo "B) internal/dbmigrate/steps/ — 6 steps"
if [ -d "$STEPS" ]; then
  ok "steps/ directory exists"
else
  no "steps/ directory missing"
fi
for step in precheck dump restore verify flip cleanup; do
  if [ -f "$STEPS/$step.go" ]; then
    ok "$step.go exists"
  else
    no "$step.go missing"
  fi
done

# ----- C) each step implements DeployStep -------------------------------
echo "C) each step implements DeployStep (Name + Run + Rollback)"
for step in precheck dump restore verify flip cleanup; do
  if [ -f "$STEPS/$step.go" ]; then
    if grep -q "func .* Name() string" "$STEPS/$step.go" \
       && grep -q "func .* Run(" "$STEPS/$step.go" \
       && grep -q "func .* Rollback(" "$STEPS/$step.go"; then
      ok "$step implements DeployStep"
    else
      no "$step does NOT implement DeployStep"
    fi
  fi
done

# ----- D) framework Run + rollback --------------------------------------
echo "D) Framework.Run + rollback chain"
if grep -q "func Run(" "$PKG/framework.go" \
   && grep -q "func rollback(" "$PKG/framework.go" \
   && grep -q "RegisterStep" "$PKG/registry.go"; then
  ok "Run / rollback / RegisterStep present"
else
  no "framework missing key functions"
fi

# ----- E) DB migration V065 -------------------------------------------
echo "E) DB migration V065 (dbmigrate_run + dbmigrate_step)"
if [ -f "$MIGRATION" ]; then
  ok "migration file exists"
  if grep -q "CREATE TABLE IF NOT EXISTS dbmigrate_run" "$MIGRATION" \
     && grep -q "CREATE TABLE IF NOT EXISTS dbmigrate_step" "$MIGRATION"; then
    ok "creates both tables"
  else
    no "migration does not create both tables"
  fi
  if grep -q "migrateV065PG" "$PROJECT_DIR/internal/db/driver_postgres.go"; then
    ok "registered in driver_postgres.go"
  else
    no "not registered in driver_postgres.go"
  fi
else
  no "migration file missing"
fi

# ----- F) SSE broker ----------------------------------------------------
echo "F) SSE broker"
if grep -q "func Subscribe" "$PKG/sse.go" \
   && grep -q "func emit" "$PKG/sse.go" \
   && grep -q "StreamHandler" "$PKG/sse.go"; then
  ok "SSE broker has Subscribe + emit + StreamHandler"
else
  no "SSE broker missing"
fi

# ----- G) routes -------------------------------------------------------
echo "G) /admin/database/migrate* routes"
for r in 'GET /admin/database/migrate' 'POST /admin/database/migrate' 'GET /admin/database/migrate/{id}/stream' 'GET /admin/database/migrate/{id}'; do
  if grep -q "$r" "$MAIN" 2>/dev/null; then
    ok "main.go has $r"
  else
    no "main.go missing $r"
  fi
done

# ----- H) AGENTS.md ----------------------------------------------------
echo "H) AGENTS.md mentions B198"
if grep -qE "B198" "$PROJECT_DIR/AGENTS.md" 2>/dev/null; then
  ok "AGENTS.md mentions B198"
else
  no "AGENTS.md does not mention B198"
fi

# ----- I) verify_pre_deploy.sh -----------------------------------------
echo "I) verify_pre_deploy.sh references check_b198.sh"
if grep -q "check_b198" "$PROJECT_DIR/scripts/verify_pre_deploy.sh" 2>/dev/null; then
  ok "verify_pre_deploy.sh references check_b198.sh"
else
  no "verify_pre_deploy.sh does not reference check_b198.sh"
fi

# ----- J) build clean ---------------------------------------------------
echo "J) build clean"
if command -v go >/dev/null 2>&1; then
  if (cd "$PROJECT_DIR" && go build ./... >/dev/null 2>&1); then
    ok "go build ./... clean"
  else
    no "go build ./... failed"
  fi
  if (cd "$PROJECT_DIR" && go vet ./... >/dev/null 2>&1); then
    ok "go vet ./... clean"
  else
    no "go vet ./... failed"
  fi
else
  echo "  -- go not in PATH, skipping build/vet"
fi

echo
echo "B198 summary: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
