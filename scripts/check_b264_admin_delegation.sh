#!/usr/bin/env bash
# check_b264_admin_delegation.sh — source-level B-check for v0.72 (B264):
# admin role delegation with an immutable primary admin.
#
# WHAT B264 IS
# ------------
# /admin/users gets per-row Promote / Demote buttons, so an administrator
# can grant AND revoke the `admin` role for other portal users. The primary
# (bootstrap/root) admin — the single row carrying the V072
# `portal_users.is_primary=1` marker — stays immutable: it can never be
# demoted, deleted or renamed. A partial UNIQUE index makes "at most one
# primary" a database-level invariant, and the handlers add three refusal
# guards (primary / self / last admin) on the destructive demote path.
#
# This check is deliberately SOURCE-LEVEL plus a build/test run: it runs
# anywhere (no docker, no DB), and the DB-backed Go tests inside the
# touched packages SKIP themselves when PostgreSQL is unavailable. If the
# Go toolchain itself is missing the build/test contracts print SKIP, never
# FAIL (AGENTS.md rule 1).
#
# CONTRACTS
# ---------
#   A. V072 migration exists in BOTH chains (functions + registrations),
#      the SQLite path goes through execSQLiteDDL, the chain-end
#      assertion is bumped to 72, and the backfill reads
#      SKYGATE_ADMIN_USER.
#   B. PortalUsers schema: is_primary column DDL + the partial UNIQUE
#      index, on both dialects.
#   C. The four DB helpers + their query primitives, and is_primary is
#      threaded through User / GetAllPortalUsers.
#   D. Handlers: PostAdminUserDemote with the primary/self/last-admin
#      guards + 'admin_demote' audit; Delete/Rename refuse the primary;
#      Promote reads is_admin through the shared helper.
#   E. Routes: POST /admin/users/{id}/demote (authMW) and the retained
#      promote route; bootstrapAdmin re-asserts the marker.
#   F. Template: per-row Promote/Demote forms with confirm() and the
#      primary badge that hides destructive actions.
#   G. i18n: every B264 key is paired (appears twice: RU + EN) and the
#      parity test passes.
#   H. Touched-package unit tests exist.
#   I. go build ./... + go test for the touched packages.

set -uo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
cd "$PROJECT_ROOT" || exit 1

MIG="$PROJECT_ROOT/internal/db/migrations_v0_72_admin_primary.go"
DRV_PG="$PROJECT_ROOT/internal/db/driver_postgres.go"
DRV_SQLITE="$PROJECT_ROOT/internal/db/driver_sqlite.go"
SCHEMA_TEST="$PROJECT_ROOT/internal/db/migrations_sqlite_schema_test.go"
PORTAL="$PROJECT_ROOT/internal/db/portal_users.go"
QUERIES="$PROJECT_ROOT/internal/db/queries.go"
DBGO="$PROJECT_ROOT/internal/db/db.go"
USERS_GO="$PROJECT_ROOT/internal/feature/admin/users.go"
MAIN_GO="$PROJECT_ROOT/cmd/skygate/main.go"
USERS_HTML="$PROJECT_ROOT/internal/handlers/templates/admin/users.html"
CATALOG="$PROJECT_ROOT/internal/i18n/catalog_admin.go"
DEMOTE_TEST="$PROJECT_ROOT/internal/feature/admin/users_demote_test.go"
HELPER_TEST="$PROJECT_ROOT/internal/db/portal_users_test.go"

PASS=0; FAIL=0; WARN=0; SKIP=0
pass() { echo "  PASS  $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }
skip() { echo "  SKIP  $*"; SKIP=$((SKIP+1)); }

# Locate the Go toolchain. The contracts that need it print SKIP (never
# FAIL) when it is genuinely absent — AGENTS.md rule 1. The extra probes
# cover the two common non-PATH installs so a dev workstation can run this
# check too: /usr/local/go (Linux tarball) and the Windows default (reached
# through WSL interop, which is how the gate's bash sees go.exe).
GO_BIN=""
if command -v go >/dev/null 2>&1; then
  GO_BIN="go"
elif [ -x "/usr/local/go/bin/go" ]; then
  GO_BIN="/usr/local/go/bin/go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
  GO_BIN="/mnt/c/Program Files/Go/bin/go.exe"
fi

echo "skygate root: $PROJECT_ROOT"
echo

echo "=== A. V072 migration in BOTH chains ==="
if [ -f "$MIG" ]; then
  pass "migrations_v0_72_admin_primary.go exists"
else
  fail "migrations_v0_72_admin_primary.go missing"
fi
grep -qE '^func migrateV072PG\(' "$MIG" && pass "  └ migrateV072PG defined" || fail "  └ migrateV072PG missing"
grep -qE '^func migrateV072SQLite\(' "$MIG" && pass "  └ migrateV072SQLite defined" || fail "  └ migrateV072SQLite missing"
grep -qE '\{72,.*migrateV072PG\}' "$DRV_PG" \
  && pass "  └ PG chain registers V072 (driver_postgres.go)" \
  || fail "  └ PG chain does NOT register V072"
grep -qE '\{72,.*migrateV072SQLite\}' "$DRV_SQLITE" \
  && pass "  └ SQLite chain registers V072 (driver_sqlite.go)" \
  || fail "  └ SQLite chain does NOT register V072"
grep -qE 'execSQLiteDDL\(' "$MIG" \
  && pass "  └ SQLite DDL goes through execSQLiteDDL (the single chokepoint)" \
  || fail "  └ SQLite DDL does not use execSQLiteDDL"
# 2026-09-20 (B275 contract renegotiation): this used to grep for the literal
# `maxV != 72`. That made EVERY later migration break B264 — v0.73 (prefix_owner)
# moved the SQLite chain head to 73 and this contract failed even though nothing
# about admin delegation had changed. The number belongs in ONE place
# (migrations_sqlite_schema_test.go, which is the actual parity guard); here we
# only assert that the chain head is pinned there at all.
grep -qE 'maxV != [0-9]+' "$SCHEMA_TEST" \
  && pass "  └ SQLite chain-end assertion present (head pinned in migrations_sqlite_schema_test.go)" \
  || fail "  └ no maxV chain-end assertion in migrations_sqlite_schema_test.go"
grep -qE 'SKYGATE_ADMIN_USER' "$MIG" \
  && pass "  └ backfill reads SKYGATE_ADMIN_USER (default admin)" \
  || fail "  └ backfill does not reference SKYGATE_ADMIN_USER"

echo
echo "=== B. is_primary column + partial UNIQUE index (both dialects) ==="
COL_DDL=$(grep -cE 'ADD COLUMN IF NOT EXISTS is_primary INTEGER NOT NULL DEFAULT 0' "$MIG")
if [ "$COL_DDL" -ge 2 ]; then
  pass "is_primary column DDL present in both migrateV072PG and migrateV072SQLite ($COL_DDL)"
else
  fail "is_primary column DDL declared $COL_DDL time(s); want 2 (PG + SQLite)"
fi
IDX=$(grep -cE 'CREATE UNIQUE INDEX IF NOT EXISTS portal_users_one_primary_uniq ON portal_users\(is_primary\) WHERE is_primary = 1' "$MIG")
if [ "$IDX" -ge 1 ]; then
  pass "partial UNIQUE index portal_users_one_primary_uniq declared"
else
  fail "partial UNIQUE index portal_users_one_primary_uniq missing"
fi
grep -qE 'SELECT COUNT\(\*\) FROM portal_users WHERE is_primary = 1' "$MIG" \
  && pass "  └ fallback/dedupe logic keeps exactly one primary" \
  || fail "  └ no single-primary fallback logic"

echo
echo "=== C. DB helpers + is_primary threading ==="
for fn in SetPortalUserPrimary IsPortalPrimaryAdmin GetPortalIsAdminByID CountPortalAdmins; do
  grep -qE "^func $fn\(" "$PORTAL" && pass "  └ $fn defined in portal_users.go" || fail "  └ $fn missing"
done
grep -qE 'qSelectPortalIsPrimaryByID' "$QUERIES" && pass "  └ qSelectPortalIsPrimaryByID (is_primary) in queries.go" || fail "  └ qSelectPortalIsPrimaryByID missing"
grep -qE 'qSelectPortalIsAdminByID' "$QUERIES" && pass "  └ qSelectPortalIsAdminByID (is_admin) in queries.go" || fail "  └ qSelectPortalIsAdminByID missing"
grep -qE 'qCountPortalAdmins\s*=\s*`SELECT COUNT\(\*\) FROM portal_users WHERE is_admin = 1`' "$QUERIES" \
  && pass "  └ qCountPortalAdmins uses PG-friendly literal SQL" \
  || fail "  └ qCountPortalAdmins missing or wrong"
grep -qE 'qUpdatePortalUserIsPrimary\s*=\s*`UPDATE portal_users SET is_primary = \$1 WHERE id = \$2`' "$QUERIES" \
  && pass "  └ qUpdatePortalUserIsPrimary uses \$1/\$2 placeholders" \
  || fail "  └ qUpdatePortalUserIsPrimary missing or non-PG placeholders"
grep -qE 'is_primary FROM portal_users ORDER BY id' "$QUERIES" \
  && pass "  └ qSelectAllPortalUsers projects is_primary" \
  || fail "  └ qSelectAllPortalUsers does not project is_primary"
grep -qE 'u\.IsPrimary = primaryI == 1' "$PORTAL" \
  && pass "  └ GetAllPortalUsers scans is_primary into User.IsPrimary" \
  || fail "  └ GetAllPortalUsers does not scan is_primary"
grep -qE 'IsPrimary\s+bool' "$DBGO" \
  && pass "  └ User.IsPrimary field exists" \
  || fail "  └ User.IsPrimary field missing"

echo
echo "=== D. handlers (demote + guards) ==="
grep -qE 'func \(s \*Service\) PostAdminUserDemote\(' "$USERS_GO" \
  && pass "PostAdminUserDemote exists" \
  || fail "PostAdminUserDemote missing"
grep -qE '"admin_demote"' "$USERS_GO" \
  && pass "  └ writes 'admin_demote' audit row" \
  || fail "  └ no 'admin_demote' audit row"
grep -qE 'db\.IsPortalPrimaryAdmin\(s\.dbc\(\), id\)' "$USERS_GO" \
  && pass "  └ primary guard (IsPortalPrimaryAdmin)" \
  || fail "  └ primary guard missing"
grep -qE 'users\.err_primary_immutable' "$USERS_GO" \
  && pass "  └ primary refusal is localized (users.err_primary_immutable)" \
  || fail "  └ primary refusal key missing"
grep -qE 'id == c\.UserID' "$USERS_GO" \
  && pass "  └ self-demotion guard (id == c.UserID)" \
  || fail "  └ self-demotion guard missing"
grep -qE 'db\.CountPortalAdmins\(s\.dbc\(\)\)' "$USERS_GO" \
  && pass "  └ last-admin guard (CountPortalAdmins)" \
  || fail "  └ last-admin guard missing"
grep -qE 'already_user=' "$USERS_GO" \
  && pass "  └ idempotent no-op flash for an already-non-admin target" \
  || fail "  └ idempotent already_user flash missing"
grep -qE 'db\.GetPortalIsAdminByID\(' "$USERS_GO" \
  && pass "  └ promote/demote share db.GetPortalIsAdminByID" \
  || fail "  └ GetPortalIsAdminByID not used by the handlers"
# Delete + Rename must each consult the primary guard.
PRIMARY_GUARD_CALLS=$(grep -cE 'db\.IsPortalPrimaryAdmin\(s\.dbc\(\), id\)' "$USERS_GO")
if [ "$PRIMARY_GUARD_CALLS" -ge 3 ]; then
  pass "  └ primary guard applied to demote + delete + rename ($PRIMARY_GUARD_CALLS sites)"
else
  fail "  └ primary guard applied at $PRIMARY_GUARD_CALLS site(s); want demote + delete + rename"
fi

echo
echo "=== E. routes + bootstrap self-heal ==="
grep -qE 'POST /admin/users/\{id\}/demote.*authMW|authMW.*PostAdminUserDemote' "$MAIN_GO" \
  && pass "POST /admin/users/{id}/demote registered behind authMW" \
  || fail "POST /admin/users/{id}/demote route missing or not behind authMW"
grep -qE 'POST /admin/users/\{id\}/promote' "$MAIN_GO" \
  && pass "  └ promote route retained (per-row menu + drift banner)" \
  || fail "  └ promote route was removed"
grep -qE 'is_admin = 1, is_primary = 1 WHERE username = \$1' "$MAIN_GO" \
  && pass "  └ bootstrapAdmin re-asserts is_admin=1 + is_primary=1 on the exists branch" \
  || fail "  └ bootstrapAdmin does not re-assert the primary marker"
grep -qE 'is_primary = 0 WHERE is_primary = 1 AND username <> \$1' "$MAIN_GO" \
  && pass "  └ bootstrapAdmin clears a stale primary before claiming the marker" \
  || fail "  └ bootstrapAdmin does not clear a stale primary (UNIQUE index would trip)"

echo
echo "=== F. template (per-row forms + primary badge) ==="
grep -qF '/admin/users/{{.ID}}/demote' "$USERS_HTML" \
  && pass "per-row Demote form posts to /admin/users/{{.ID}}/demote" \
  || fail "per-row Demote form missing"
grep -qF '/admin/users/{{.ID}}/promote' "$USERS_HTML" \
  && pass "per-row Promote form posts to /admin/users/{{.ID}}/promote" \
  || fail "per-row Promote form missing"
grep -qE 'users\.demote_confirm' "$USERS_HTML" \
  && pass "  └ Demote is confirm()-guarded" \
  || fail "  └ Demote confirm() missing"
grep -qE 'users\.promote_confirm' "$USERS_HTML" \
  && pass "  └ Promote is confirm()-guarded" \
  || fail "  └ Promote confirm() missing"
grep -qE '\{\{if \.IsPrimary\}\}' "$USERS_HTML" \
  && pass "  └ primary row renders the immutable badge branch" \
  || fail "  └ no {{if .IsPrimary}} branch in the template"
grep -qE 'users\.primary_tag' "$USERS_HTML" \
  && pass "  └ visible primary badge (users.primary_tag)" \
  || fail "  └ primary badge missing"
grep -qE 'users\.primary_help' "$USERS_HTML" \
  && pass "  └ primary explanation rendered for the immutable row" \
  || fail "  └ primary explanation missing"
# The destructive forms must sit INSIDE the not-primary branch: the
# template renders `{{if .IsPrimary}}<badge>{{else}}<promote|demote|rename>
# {{end}}`, so assert that an `{{else}}` occurs between the
# `{{if .IsPrimary}}` opener and the demote form action.
DEMOTE_SEG=$(awk '/\{\{if \.IsPrimary\}\}/{f=1} f{print} /\/admin\/users\/\{\{\.ID\}\}\/demote/{if(f) exit}' "$USERS_HTML")
HAS_ELSE=0
printf '%s' "$DEMOTE_SEG" | grep -q '{{else}}' && HAS_ELSE=1
HAS_DEMOTE=0
printf '%s' "$DEMOTE_SEG" | grep -qF '/admin/users/{{.ID}}/demote' && HAS_DEMOTE=1
if [ "$HAS_ELSE" -eq 1 ] && [ "$HAS_DEMOTE" -eq 1 ]; then
  pass "  └ per-row role forms live in the non-primary branch (hidden for primary)"
else
  fail "  └ per-row role forms are not guarded by the primary branch (else=$HAS_ELSE demote=$HAS_DEMOTE)"
fi

echo
echo "=== G. i18n keys paired RU + EN ==="
MISSING=0
UNPAIRED=0
for k in promote_btn demote_btn promote_confirm demote_confirm primary_tag primary_help err_primary_immutable err_last_admin err_cannot_demote_self already_user_flash; do
  n=$(grep -c "\"users.$k\"" "$CATALOG")
  if [ "$n" -eq 0 ]; then
    fail "  └ users.$k missing from catalog_admin.go"
    MISSING=1
  elif [ "$n" -ne 2 ]; then
    fail "  └ users.$k appears $n time(s); want 2 (RU + EN pair)"
    UNPAIRED=1
  fi
done
if [ "$MISSING" -eq 0 ] && [ "$UNPAIRED" -eq 0 ]; then
  pass "all 10 B264 keys present exactly twice (RU + EN)"
fi
if [ -n "$GO_BIN" ]; then
  OUT="$("$GO_BIN" test ./internal/i18n/ -run TestCatalogsParity 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    pass "  └ go test ./internal/i18n/ -run TestCatalogsParity"
  else
    fail "  └ TestCatalogsParity FAILED (print the missing key)"
    echo "$OUT" | tail -5 | sed 's/^/        /'
  fi
else
  skip "  └ go toolchain not on PATH — i18n parity test not run here"
fi

echo
echo "=== H. unit tests exist ==="
if [ -f "$DEMOTE_TEST" ]; then
  pass "users_demote_test.go exists"
  for fn in TestPostAdminUserDemote_HappyPath TestPostAdminUserDemote_RefusesPrimary TestPostAdminUserDemote_RefusesSelf TestPostAdminUserDemote_RefusesLastAdmin TestPostAdminDeleteUser_RefusesPrimary TestPostAdminUserRename_RefusesPrimary TestPostAdminUserPromote_PerRowArbitraryUser; do
    grep -qE "^func $fn\(" "$DEMOTE_TEST" && pass "  └ $fn" || fail "  └ $fn missing"
  done
else
  fail "users_demote_test.go missing"
fi
for fn in TestSetPortalUserPrimary TestIsPortalPrimaryAdmin_MissingRow TestPortalUsersOnePrimaryIndex TestGetPortalIsAdminByID TestCountPortalAdmins; do
  grep -qE "^func $fn\(" "$HELPER_TEST" && pass "  └ $fn (portal_users_test.go)" || fail "  └ $fn missing"
done

echo
echo "=== I. build + touched-package tests ==="
if [ -n "$GO_BIN" ]; then
  OUT="$("$GO_BIN" build ./... 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    pass "go build ./... clean"
  else
    fail "go build ./... FAILED"
    echo "$OUT" | tail -10 | sed 's/^/        /'
  fi
  # Capture first, match second — never pipe a long producer into grep -q
  # under pipefail (TD-19): a still-writing `go test` dies with SIGPIPE and
  # the pipeline reports failure even though the command succeeded.
  OUT="$("$GO_BIN" test ./internal/db/... ./internal/feature/admin/... ./internal/i18n/... ./internal/handlers/... 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    pass "go test ./internal/db/... ./internal/feature/admin/... ./internal/i18n/... ./internal/handlers/..."
    # Surface how many DB-backed tests SKIPped (no PG on this host).
    SKIPPED=$(printf '%s\n' "$OUT" | grep -c '^--- SKIP' || true)
    [ "$SKIPPED" -gt 0 ] && echo "        ($SKIPPED tests SKIPped — usually the PG-only ones)"
  else
    fail "go test on the touched packages FAILED"
    echo "$OUT" | grep -E '^(FAIL|---. FAIL)' | head -20 | sed 's/^/        /'
    echo "$OUT" | tail -10 | sed 's/^/        /'
  fi
else
  skip "go toolchain not on PATH — build/test contracts not run here"
fi

echo
echo "=== B264 summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  WARN: $WARN"
echo "  SKIP: $SKIP"

if [ "$FAIL" -gt 0 ]; then
  echo
  echo "B264 (admin role delegation + immutable primary) B-check FAILED."
  exit 1
fi
echo
echo "B264 (admin role delegation + immutable primary) B-check all contracts hold."
exit 0
