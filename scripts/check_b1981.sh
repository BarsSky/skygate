#!/usr/bin/env bash
# ============================================================================
# check_b1981.sh — B198.1 (v1.5.0+) DB migration UI completion
#
# Phase 1.4 of docs/internal/cluster-management.md. B198 added the
# framework; B198.1 adds the user-facing surface:
#   - /admin/database page now has a "Migrate to new host" form
#   - GET /admin/database/migrate shows recent runs list
#   - GET /admin/database/migrate/{id} shows single-run page with
#     steps + SSE for live progress
#
# Verifies:
#   A) admin/database.html has the migrate form (target_host etc.)
#   B) admin/migrate_run.html exists
#   C) dbmigrate.LoadRun is called from admin handler
#   D) dbmigrate.RunView is defined + used
#   E) Routes re-wired: GET /admin/database/migrate is now adminSvc
#   F) All db.* i18n keys (migrate_* + recent_runs + migrate_run_*) present
#   G) AGENTS.md mentions B198.1
#   H) verify_pre_deploy.sh references check_b1981.sh
#   I) go build / vet clean
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PASS=0
FAIL=0
ok() { echo "  ✓ $*"; PASS=$((PASS+1)); }
no() { echo "  ✗ $*"; FAIL=$((FAIL+1)); }

DBPAGE="$PROJECT_DIR/internal/handlers/templates/admin/database.html"
RUNPAGE="$PROJECT_DIR/internal/handlers/templates/admin/migrate_run.html"
ADMINDB="$PROJECT_DIR/internal/feature/admin/database.go"
DBMIG="$PROJECT_DIR/internal/dbmigrate/db.go"
MAIN="$PROJECT_DIR/cmd/skygate/main.go"
CATALOG="$PROJECT_DIR/internal/i18n/catalog_admin.go"

# ----- A) admin/database.html has migrate form ---------------------------
echo "A) admin/database.html has the Migrate card"
if [ -f "$DBPAGE" ]; then
  for marker in 'db.migrate_title' '/admin/database/migrate' 'target_host' 'target_port' 'target_dbname' 'target_username' 'target_sslmode' 'db.migrate_btn' 'RecentRuns'; do
    if grep -q "$marker" "$DBPAGE"; then
      ok "has $marker"
    else
      no "missing $marker"
    fi
  done
else
  no "database.html missing"
fi

# ----- B) migrate_run.html exists --------------------------------------
echo "B) admin/migrate_run.html"
if [ -f "$RUNPAGE" ]; then
  ok "migrate_run.html exists"
  for marker in 'db.migrate_run_title' 'db.migrate_steps_title' 'sse-status' 'EventSource' 'admin/database/migrate/' 'stream'; do
    if grep -q "$marker" "$RUNPAGE"; then
      ok "has $marker"
    else
      no "missing $marker"
    fi
  done
else
  no "migrate_run.html missing"
fi

# ----- C) dbmigrate.LoadRun called from admin handler ------------------
echo "C) dbmigrate.LoadRun is called from admin handler"
if grep -q "dbmigrate.LoadRun" "$ADMINDB" 2>/dev/null; then
  ok "admin/database.go calls dbmigrate.LoadRun"
else
  no "admin/database.go does not call dbmigrate.LoadRun"
fi

# ----- D) dbmigrate.RunView defined + used -----------------------------
echo "D) dbmigrate.RunView is defined and used"
if grep -q "type RunView struct" "$PROJECT_DIR/internal/dbmigrate/types.go" 2>/dev/null; then
  ok "RunView type is defined"
else
  no "RunView type not defined"
fi
if grep -q "dbmigrate.RunView" "$ADMINDB" 2>/dev/null; then
  ok "admin handler uses dbmigrate.RunView"
else
  no "admin handler does not use dbmigrate.RunView"
fi

# ----- E) Routes re-wired ----------------------------------------------
echo "E) GET /admin/database/migrate → adminSvc"
if grep -q "adminSvc.GetAdminDatabaseMigrate" "$MAIN" 2>/dev/null; then
  ok "GET /admin/database/migrate → adminSvc.GetAdminDatabaseMigrate"
else
  no "GET /admin/database/migrate is not wired to adminSvc"
fi
if grep -q "adminSvc.GetAdminDatabaseMigrateRun" "$MAIN" 2>/dev/null; then
  ok "GET /admin/database/migrate/{id} → adminSvc.GetAdminDatabaseMigrateRun"
else
  no "GET /admin/database/migrate/{id} is not wired to adminSvc"
fi

# ----- F) i18n keys in RU+EN ------------------------------------------
echo "F) i18n migrate_* keys in RU+EN"
for key in 'db.migrate_title' 'db.migrate_help' 'db.migrate_btn' 'db.migrate_confirm' 'db.migrate_steps_help' 'db.migrate_run_title' 'db.migrate_run_status' 'db.migrate_operator' 'db.migrate_source' 'db.migrate_target' 'db.migrate_error' 'db.migrate_steps_title' 'db.migrate_step_name' 'db.migrate_step_status' 'db.migrate_step_duration' 'db.migrate_step_started' 'db.migrate_stream_live' 'db.migrate_stream_offline' 'db.migrate_stream_help' 'db.recent_runs_title'; do
  if grep -q "\"$key\":" "$CATALOG" 2>/dev/null; then
    ok "$key present"
  else
    no "$key missing"
  fi
done

# ----- G) AGENTS.md ----------------------------------------------------
echo "G) AGENTS.md mentions B198.1"
if grep -qE "B198\.1" "$PROJECT_DIR/AGENTS.md" 2>/dev/null; then
  ok "AGENTS.md mentions B198.1"
else
  no "AGENTS.md does not mention B198.1"
fi

# ----- H) verify_pre_deploy.sh -----------------------------------------
echo "H) verify_pre_deploy.sh references check_b1981.sh"
if grep -q "check_b1981" "$PROJECT_DIR/scripts/verify_pre_deploy.sh" 2>/dev/null; then
  ok "verify_pre_deploy.sh references check_b1981.sh"
else
  no "verify_pre_deploy.sh does not reference check_b1981.sh"
fi

# ----- I) build clean --------------------------------------------------
echo "I) build clean"
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
echo "B198.1 summary: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
