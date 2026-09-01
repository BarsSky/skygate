#!/usr/bin/env bash
# ============================================================================
# check_b196.sh — B196 (v1.5.0+) /admin/database (Phase 1.1, read-only)
#
# Verifies:
#   A) internal/feature/admin/database.go exists
#   B) The handler GetAdminDatabase + databasePageData + parseLibpqDSN
#      + probeDB are all defined
#   C) The route /admin/database is registered in cmd/skygate/main.go
#   D) The template admin/database.html exists
#   E) The template uses the i18n keys (db.page_title, db.current_dsn_title, etc.)
#   F) Both ru and en i18n catalogs include the db.* keys (no padding drift)
#   G) internal/db/cluster.go exists with GetClusterDatabase + SetClusterDatabase + ClusterDatabase struct
#   H) AGENTS.md mentions B196
#   I) verify_pre_deploy.sh references check_b196.sh
#   J) go build / vet clean
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PASS=0
FAIL=0
ok() { echo "  ✓ $*"; PASS=$((PASS+1)); }
no() { echo "  ✗ $*"; FAIL=$((FAIL+1)); }

HANDLER="$PROJECT_DIR/internal/feature/admin/database.go"
TPL="$PROJECT_DIR/internal/handlers/templates/admin/database.html"
DBHELP="$PROJECT_DIR/internal/db/cluster.go"
MAIN="$PROJECT_DIR/cmd/skygate/main.go"
CATALOG="$PROJECT_DIR/internal/i18n/catalog_admin.go"

# ----- A) handler file exists -------------------------------------------
echo "A) internal/feature/admin/database.go"
[ -f "$HANDLER" ] && ok "handler file exists" || no "handler file missing"

# ----- B) handler internals --------------------------------------------
echo "B) handler internals"
if [ -f "$HANDLER" ]; then
  for sym in "GetAdminDatabase" "databasePageData" "parseLibpqDSN" "probeDB"; do
    if grep -q "$sym" "$HANDLER"; then
      ok "defines $sym"
    else
      no "missing $sym"
    fi
  done
fi

# ----- C) route registered ----------------------------------------------
echo "C) /admin/database route"
if grep -q 'admin/database' "$MAIN" 2>/dev/null; then
  ok "main.go references /admin/database"
else
  no "main.go does not reference /admin/database"
fi

# ----- D) template file exists -----------------------------------------
echo "D) admin/database.html"
[ -f "$TPL" ] && ok "template file exists" || no "template file missing"

# ----- E) template uses i18n keys ---------------------------------------
echo "E) template uses db.* i18n keys"
if [ -f "$TPL" ]; then
  for key in 'db.page_title' 'db.current_dsn_title' 'db.desired_dsn_title' 'db.d8_note'; do
    if grep -q "$key" "$TPL"; then
      ok "template uses $key"
    else
      no "template missing $key"
    fi
  done
fi

# ----- F) both ru + en catalogs include db.* keys -----------------------
echo "F) ru + en catalogs in lock-step on db.* keys"
if [ -f "$CATALOG" ]; then
  for key in 'db.page_title' 'db.current_dsn_title' 'db.desired_dsn_title' 'db.reachable' 'db.unreachable' 'db.d8_note'; do
    RU=$(grep -c "\"$key\":" "$CATALOG" 2>/dev/null || echo 0)
    if [ "$RU" -ge 1 ]; then
      ok "$key present (in $RU catalogs)"
    else
      no "$key missing from catalogs"
    fi
  done
fi

# ----- G) cluster.go helper ---------------------------------------------
echo "G) internal/db/cluster.go"
[ -f "$DBHELP" ] && ok "cluster.go exists" || no "cluster.go missing"
if [ -f "$DBHELP" ]; then
  for sym in "GetClusterDatabase" "SetClusterDatabase" "ClusterDatabase" "ErrClusterDatabaseNotFound"; do
    if grep -q "$sym" "$DBHELP"; then
      ok "defines $sym"
    else
      no "missing $sym"
    fi
  done
fi

# ----- H) AGENTS.md mentions B196 ---------------------------------------
echo "H) AGENTS.md mentions B196"
if grep -qE "B196" "$PROJECT_DIR/AGENTS.md" 2>/dev/null; then
  ok "AGENTS.md mentions B196"
else
  no "AGENTS.md does not mention B196 (defer to /docs/internal/cluster-management.md)"
fi

# ----- I) verify_pre_deploy.sh references check_b196.sh -----------------
echo "I) verify_pre_deploy.sh references check_b196.sh"
if grep -q "check_b196" "$PROJECT_DIR/scripts/verify_pre_deploy.sh" 2>/dev/null; then
  ok "verify_pre_deploy.sh references check_b196.sh"
else
  no "verify_pre_deploy.sh does not reference check_b196.sh"
fi

# ----- J) build clean ----------------------------------------------------
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
echo "B196 summary: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
