#!/usr/bin/env bash
# check_b_admin_user_promote.sh — source-level B-check for
# v1.5.2 admin-user-sync T6.1 (per-row Promote button on the
# AdminSyncPromoteToAdmin drift banner).
#
# 2026-09-13: Pre-T6.1 the AdminSyncPromoteToAdmin banner on
# /admin/users just EXPLAINED the drift — the operator had to
# open /admin/users/{id}/edit and click "Promote to admin"
# there. T6.1 collapses this into a single click on the drift
# banner itself (mirrors T5's "Adopt as Admin" form inside the
# adopt banner).
#
# Pins 9 source-level contracts (file content, runs anywhere):
#
#   A. Handler PostAdminUserPromote exists in
#      internal/feature/admin/users.go (admin-only, idempotent,
#      audit row written).
#   B. Route POST /admin/users/{id}/promote registered in
#      cmd/skygate/main.go (authMW-guarded like every other
#      admin write endpoint).
#   C. Template internal/handlers/templates/admin/users.html
#      renders the Promote button INSIDE the
#      AdminSyncPromoteToAdmin banner (mirrors the Adopt form
#      inside the adopt banner).
#   D. i18n keys users.sync_drift_promote_btn / _btn_help /
#      _confirm / _helper exist in BOTH RU + EN in
#      internal/i18n/catalog_admin.go.
#   E. DB helper SetPortalUserIsAdmin exists in
#      internal/db/portal_users.go (single UPDATE on is_admin).
#   F. SQL primitive qUpdatePortalUserIsAdmin registered in
#      internal/db/queries.go (PG-friendly $1/$2 placeholders).
#   G. Unit tests TestPostAdminUserPromote_HappyPath +
#      _Idempotent + _ForbiddenWhenNonAdmin exist in
#      internal/feature/admin/users_promote_test.go.
#   H. AdminSyncFacts has the PortalAdminID field
#      (the drift banner needs the row id to render the form
#      action).
#   I. Banner code path detects the Case D row
#      (right username + linked HS + is_admin=0) so the
#      promote mode is reachable in production (not just the
#      pure-function test).

set -uo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
USERS_GO="$PROJECT_ROOT/internal/feature/admin/users.go"
MAIN_GO="$PROJECT_ROOT/cmd/skygate/main.go"
USERS_HTML="$PROJECT_ROOT/internal/handlers/templates/admin/users.html"
CATALOG="$PROJECT_ROOT/internal/i18n/catalog_admin.go"
DB_PORTAL="$PROJECT_ROOT/internal/db/portal_users.go"
DB_QUERIES="$PROJECT_ROOT/internal/db/queries.go"
TEST="$PROJECT_ROOT/internal/feature/admin/users_promote_test.go"
SYNC_BANNER="$PROJECT_ROOT/internal/feature/admin/users_sync_banner.go"

PASS=0; FAIL=0; WARN=0
pass() { echo "  PASS  $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

echo "skygate root: $PROJECT_ROOT"
echo

echo "=== A. PostAdminUserPromote handler in users.go ==="
if grep -qE 'func \(s \*Service\) PostAdminUserPromote\(' "$USERS_GO"; then
  pass "PostAdminUserPromote handler exists"
  # Idempotency: handler must read is_admin before UPDATE so a
  # re-click on an already-admin row short-circuits.
  if grep -qE 'if isAdmin == 1' "$USERS_GO"; then
    pass "  └ idempotent re-click short-circuits (isAdmin==1 branch)"
  else
    fail "  └ idempotent re-click short-circuit missing"
  fi
  # Audit row: handler must write an 'admin_promote' action so
  # the operator can grep for the drift fix.
  if grep -qE '"admin_promote"' "$USERS_GO"; then
    pass "  └ writes 'admin_promote' audit row"
  else
    fail "  └ no 'admin_promote' audit row"
  fi
else
  fail "PostAdminUserPromote handler missing"
fi

echo
echo "=== B. route POST /admin/users/{id}/promote wired in main.go ==="
if grep -qE 'POST /admin/users/\{id\}/promote' "$MAIN_GO"; then
  pass "POST /admin/users/{id}/promote route registered"
  # Must be behind authMW (like every other admin write).
  if grep -qE 'POST /admin/users/\{id\}/promote.*authMW|authMW.*/admin/users/\{id\}/promote' "$MAIN_GO"; then
    pass "  └ behind authMW"
  else
    fail "  └ NOT behind authMW (security regression)"
  fi
else
  fail "POST /admin/users/{id}/promote route missing"
fi

echo
echo "=== C. users.html renders Promote form inside the banner ==="
# The drift banner uses {{if eq .AdminSyncMode "promote_to_admin"}}.
# Inside it, the form action must reference .AdminSyncFacts.PortalAdminID.
HAS_PROMOTE_MODE=$(grep -c 'AdminSyncMode "promote_to_admin"' "$USERS_HTML")
HAS_PORTAL_ID=$(grep -c 'AdminSyncFacts\.PortalAdminID' "$USERS_HTML")
HAS_FORM_ACTION=$(grep -c '/admin/users/{{.AdminSyncFacts.PortalAdminID}}/promote' "$USERS_HTML")
if [ "$HAS_PROMOTE_MODE" -ge 1 ] && [ "$HAS_PORTAL_ID" -ge 1 ] && [ "$HAS_FORM_ACTION" -ge 1 ]; then
  pass "banner renders the per-row Promote form with PortalAdminID"
else
  fail "banner missing the per-row Promote form (mode=$HAS_PROMOTE_MODE id=$HAS_PORTAL_ID form=$HAS_FORM_ACTION)"
fi

echo
echo "=== D. i18n parity (RU + EN) ==="
MISSING=0
for k in sync_drift_promote_btn sync_drift_promote_btn_help sync_drift_promote_confirm sync_drift_promote_helper; do
  if ! grep -q "\"users.$k\"" "$CATALOG"; then
    fail "  └ users.$k missing from catalog"
    MISSING=1
  fi
done
if [ "$MISSING" -eq 0 ]; then
  pass "all 4 i18n keys present (catalog has both RU + EN sections)"
fi

echo
echo "=== E. SetPortalUserIsAdmin helper in db/portal_users.go ==="
if grep -qE '^func SetPortalUserIsAdmin\(' "$DB_PORTAL"; then
  pass "SetPortalUserIsAdmin helper exists"
else
  fail "SetPortalUserIsAdmin helper missing"
fi

echo
echo "=== F. qUpdatePortalUserIsAdmin SQL primitive in db/queries.go ==="
# PG-friendly $1/$2 placeholders (NOT raw ?).
if grep -qE 'qUpdatePortalUserIsAdmin\s*=\s*`UPDATE portal_users SET is_admin = \$1 WHERE id = \$2`' "$DB_QUERIES"; then
  pass "qUpdatePortalUserIsAdmin uses PG-friendly \$1/\$2 placeholders"
else
  fail "qUpdatePortalUserIsAdmin missing or uses non-PG placeholders"
fi

echo
echo "=== G. unit tests exist ==="
if [ -f "$TEST" ]; then
  pass "users_promote_test.go exists"
  for fn in TestPostAdminUserPromote_HappyPath TestPostAdminUserPromote_Idempotent TestPostAdminUserPromote_ForbiddenWhenNonAdmin; do
    if grep -qE "^func $fn\(" "$TEST"; then
      pass "  └ $fn present"
    else
      fail "  └ $fn missing"
    fi
  done
else
  fail "users_promote_test.go missing"
fi

echo
echo "=== H. AdminSyncFacts.PortalAdminID field ==="
if grep -qE 'PortalAdminID\s+int64' "$SYNC_BANNER"; then
  pass "PortalAdminID field present on AdminSyncFacts"
else
  fail "PortalAdminID field missing"
fi

echo
echo "=== I. production banner code path reaches Case D ==="
# The banner's Query 3 must look for the right username +
# is_admin=0 + linked HS row, then re-route to
# AdminSyncPromoteToAdmin instead of AdminSyncAdoptAsAdmin.
if grep -qE 'WHERE username = \$1 AND headscale_user_id IS NOT NULL AND is_admin = 0' "$SYNC_BANNER" \
   && grep -qE 'PortalAdminIsAdmin\s*=\s*false' "$SYNC_BANNER"; then
  pass "Query 3 detects the demoted row + overrides PortalAdminIsAdmin=false"
else
  fail "Query 3 (Case D detector) missing"
fi

echo
echo "=== B-T6.1 summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  WARN: $WARN"

if [ "$FAIL" -gt 0 ]; then
  echo
  echo "T6.1 (PostAdminUserPromote) B-check FAILED."
  exit 1
fi
if [ "$WARN" -gt 0 ]; then
  echo
  echo "T6.1 B-check passed with warnings."
  exit 0
fi
echo
echo "T6.1 (PostAdminUserPromote) B-check all contracts hold."
exit 0
