#!/usr/bin/env bash
# ============================================================================
# check_b197.sh — B197 (v1.5.0+) /admin/database Phase 1.2 (Test + Edit)
#
# Verifies:
#   A) PostAdminDatabaseTest + PostAdminDatabaseEdit handlers exist
#   B) databasePageData has Form* fields for pre-fill
#   C) Routes /admin/database/test + /admin/database/edit registered
#   D) Template admin/database.html has the test+edit form
#   E) i18n db.test_edit_title + db.test_btn + db.save_btn + db.test_help
#      + db.edit_help + db.edit_confirm + db.port are present in RU+EN
#   F) Audit log row written on Edit (cluster.db.edit action)
#   G) db.SetClusterDatabase is called from the Edit handler
#   H) AGENTS.md mentions B197
#   I) verify_pre_deploy.sh references check_b197.sh
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
MAIN="$PROJECT_DIR/cmd/skygate/main.go"
CATALOG="$PROJECT_DIR/internal/i18n/catalog_admin.go"
DBHELP="$PROJECT_DIR/internal/db/cluster.go"

# ----- A) handlers exist ------------------------------------------------
echo "A) Phase 1.2 handlers"
if [ -f "$HANDLER" ]; then
  for sym in "PostAdminDatabaseTest" "PostAdminDatabaseEdit"; do
    if grep -q "func.*$sym" "$HANDLER"; then
      ok "defines $sym"
    else
      no "missing $sym"
    fi
  done
else
  no "database.go missing"
fi

# ----- B) Form* pre-fill fields ----------------------------------------
echo "B) databasePageData Form* fields"
if [ -f "$HANDLER" ]; then
  for f in "FormHost" "FormPort" "FormDBName" "FormUsername" "FormSSLMode"; do
    if grep -q "$f" "$HANDLER"; then
      ok "has $f"
    else
      no "missing $f"
    fi
  done
fi

# ----- C) routes registered ---------------------------------------------
echo "C) POST routes"
for route in 'POST /admin/database/test' 'POST /admin/database/edit'; do
  if grep -q "$route" "$MAIN" 2>/dev/null; then
    ok "main.go has $route"
  else
    no "main.go missing $route"
  fi
done

# ----- D) template form fields ------------------------------------------
echo "D) template admin/database.html has form"
if [ -f "$TPL" ]; then
  for marker in '/admin/database/test' '/admin/database/edit' 'db.test_btn' 'db.save_btn' 'db.test_edit_title'; do
    if grep -q "$marker" "$TPL"; then
      ok "template has $marker"
    else
      no "template missing $marker"
    fi
  done
fi

# ----- E) i18n keys present in RU+EN ------------------------------------
echo "E) i18n db.* keys in RU+EN"
if [ -f "$CATALOG" ]; then
  for key in 'db.test_edit_title' 'db.test_btn' 'db.save_btn' 'db.test_help' 'db.edit_help' 'db.edit_confirm' 'db.port'; do
    if grep -q "\"$key\":" "$CATALOG"; then
      ok "$key present"
    else
      no "$key missing"
    fi
  done
fi

# ----- F) audit row on Edit --------------------------------------------
echo "F) audit log row on Edit"
if grep -q 'cluster.db.edit' "$HANDLER" 2>/dev/null; then
  ok "Edit writes cluster.db.edit to audit_log"
else
  no "Edit does not write audit row"
fi

# ----- G) db.SetClusterDatabase called ----------------------------------
echo "G) db.SetClusterDatabase called from Edit"
if grep -q "db.SetClusterDatabase" "$HANDLER" 2>/dev/null; then
  ok "Edit calls db.SetClusterDatabase"
else
  no "Edit does not call db.SetClusterDatabase"
fi

# ----- H) AGENTS.md mentions B197 --------------------------------------
echo "H) AGENTS.md mentions B197"
if grep -qE "B197" "$PROJECT_DIR/AGENTS.md" 2>/dev/null; then
  ok "AGENTS.md mentions B197"
else
  no "AGENTS.md does not mention B197"
fi

# ----- I) verify_pre_deploy.sh references check_b197.sh -----------------
echo "I) verify_pre_deploy.sh references check_b197.sh"
if grep -q "check_b197" "$PROJECT_DIR/scripts/verify_pre_deploy.sh" 2>/dev/null; then
  ok "verify_pre_deploy.sh references check_b197.sh"
else
  no "verify_pre_deploy.sh does not reference check_b197.sh"
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
echo "B197 summary: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
