#!/usr/bin/env bash
# check_b329_db_backend_ui.sh
#
# 2026-09-28 (B329) — the database-management tab: which backend is in
# use, and a conversion that actually changes it.
#
# THE REPORT
# ----------
#   «Нету в панели администратора вкладки с тем какая сейчас
#    используется БД SQLite или Postgress а также на этой странице
#    кнопки миграции если администратор решит что хочет перевести все
#    данные на другой тип БД.»
#
# MEASURED, NOT INFERRED (before this block)
# ------------------------------------------
#   * /admin/database had NO sidebar entry. layout.html's Data section
#     held backup/invites/control-planes, sectionPageSet() agreed, and
#     the only in-app link was a button on /admin/cluster.
#   * The page could not name the backend: databasePageData had no
#     dialect field and admin/database.html contained no "sqlite"
#     string at all.
#   * On a SQLite install the page was BROKEN, not merely unhelpful:
#     collectDatabasePageData parsed SKYGATE_DB with parseLibpqDSN
#     (postgres:// only) and probed it with sql.Open("pgx", …), so the
#     operator saw "could not parse SKYGATE_DB_DSN" plus a failed
#     PostgreSQL probe for a perfectly healthy SQLite file.
#   * The "Migrate" card was PostgreSQL → PostgreSQL only
#     (dbmigrate/handlers.go hardcoded the postgres:// DSN; grep sqlite
#     internal/dbmigrate = 0 matches), and the one cross-backend tool
#     (skygate db-migrate → db.Convert) REFUSED a PostgreSQL source
#     ("only SQLite source supported in v1.5.4"), so the direction the
#     operator needs — PostgreSQL → SQLite — could not run at all.
#   * `skygate db-migrate --from=<dsn>` — the form the help text and
#     the release notes advertise — was rejected with "unknown flag".
#
# CONTRACTS
#   A. the page knows which backend it is on (live dialect, not a guess)
#   B. the probe speaks the live dialect (PostgreSQL AND SQLite)
#   C. the template shows the backend and stops assuming PostgreSQL
#   D. the conversion route, handler and isolation exist
#   E. the converter supports BOTH directions, transactionally
#   F. the tab exists in the sidebar, both directions
#   G. i18n: paired keys, and the stale "STUB" claim is gone
#   H. the regression tests exist and pass
#   I. this script is tracked by git (AGENTS trap #11)
#   J. the catalog registers it

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B329: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DBADMIN=internal/feature/admin/database.go
DBTPL=internal/handlers/templates/admin/database.html
OPENISO=internal/db/dialect.go
CONVERT=internal/db/convert.go
CLI=cmd/skygate/db_migrate.go
MAIN=cmd/skygate/main.go
LAYOUT=internal/handlers/templates/layout.html
HANDLERS=internal/handlers/handlers.go
RU=internal/i18n/catalog_admin.go
COMMON=internal/i18n/catalog_common.go

hdr "B329 — the database-management tab names the backend and can change it"

# region_has <file> <start-substring> <end-regex> <grep-pattern>
#
# Capture first, then match. NEVER `producer | grep -q` under pipefail:
# grep -q exits at the first match and closes the pipe, the still-writing
# producer dies with SIGPIPE (141), and pipefail turns a success into a
# failure (AGENTS trap #9 / TD-19 — this is the pattern that produced one
# rotating FAIL per gate run).
region_has() {
  local out
  out="$(awk -v start="$2" -v end="$3" \
    'index($0, start) > 0 { inside = 1 } inside { print } inside && $0 ~ end { exit }' "$1")"
  grep -q -- "$4" <<< "$out"
}

# --- A: the page knows which backend it is on --------------------------------
if grep -q 'db.ActiveDialect()' "$DBADMIN"; then
  ok "A1: the page asks the process which dialect it actually runs on"
else
  bad "A1: database.go does not call db.ActiveDialect() — the backend would be a guess"
fi
if grep -q 'CurrentKind' "$DBADMIN" && grep -q 'IsSQLite' "$DBADMIN"; then
  ok "A2: databasePageData carries the backend kind and the SQLite flag"
else
  bad "A2: databasePageData has no backend-kind field — the template cannot name the backend"
fi
for h in sqlitePathForDisplay humanFileSize humanBytes; do
  if grep -q "func $h(" "$DBADMIN"; then
    ok "A3: $h exists (SQLite installs show the file and its size)"
  else
    bad "A3: $h is missing — a SQLite install has no readable location"
  fi
done
if grep -q 'CurrentDSNMatch' "$DBADMIN"; then
  ok "A4: the page cross-checks the env DSN against the live dialect"
else
  bad "A4: a DSN that disagrees with the live dialect would be shown without a warning"
fi

# --- B: the probe speaks the live dialect ------------------------------------
if grep -q '^func probeLiveDatabase(' "$DBADMIN"; then
  ok "B1: probeLiveDatabase exists"
else
  bad "B1: the dialect-aware probe is missing — the page is PostgreSQL-only again"
fi
if grep -q 'sqlite_version()' "$DBADMIN" && grep -q 'server_version' "$DBADMIN"; then
  ok "B2: the probe reads a version from both backends"
else
  bad "B2: the probe does not ask each backend its own version question"
fi
if grep -q 'sqlite_master' "$DBADMIN"; then
  ok "B3: the SQLite table count reads sqlite_master"
else
  bad "B3: the table count has no SQLite branch"
fi
# The pre-B329 bug: the page decided the probe by the DSN shape and always
# used pgx. contract: collectDatabasePageData must not call the PG-only probe.
if region_has "$DBADMIN" 'func (s *Service) collectDatabasePageData' '^}' 'probeDB('; then
  bad "B4: collectDatabasePageData still calls the PostgreSQL-only probeDB — SQLite installs fail"
else
  ok "B4: collectDatabasePageData no longer probes through pgx unconditionally"
fi

# --- C: the template shows the backend ---------------------------------------
if grep -q 'db.backend_type' "$DBTPL" && grep -q 'CurrentKindLabel' "$DBTPL"; then
  ok "C1: the template renders the backend type"
else
  bad "C1: database.html does not show which backend is in use"
fi
if grep -q '{{if .Data.IsSQLite}}' "$DBTPL"; then
  ok "C2: the DSN card branches on the backend instead of assuming PostgreSQL"
else
  bad "C2: the DSN card still shows PostgreSQL-only fields on every install"
fi
if grep -q 'db.backend_file' "$DBTPL" && grep -q 'db.backend_size' "$DBTPL"; then
  ok "C3: a SQLite install gets the file path and size"
else
  bad "C3: no file/size rows for SQLite"
fi

# --- D: the conversion route, handler and isolation --------------------------
if grep -q 'POST /admin/database/convert' "$MAIN"; then
  ok "D1: POST /admin/database/convert is registered"
else
  bad "D1: the conversion route is missing — there is no button to change the backend"
fi
if grep -q 'PostAdminDatabaseConvert' "$DBADMIN"; then
  ok "D2: PostAdminDatabaseConvert exists"
else
  bad "D2: PostAdminDatabaseConvert is missing"
fi
if region_has "$DBADMIN" 'func (s *Service) PostAdminDatabaseConvert' '^}' 'IsAdmin'; then
  ok "D3: the conversion handler is admin-gated"
else
  bad "D3: the conversion handler has no admin gate"
fi
if grep -q 'db.OpenIsolated(' "$DBADMIN"; then
  ok "D4: the target is opened through db.OpenIsolated"
else
  bad "D4: the handler opens the target with the ordinary opener — that flips the process-wide dialect"
fi
if grep -q '^func OpenIsolated(' "$OPENISO"; then
  ok "D5: db.OpenIsolated exists"
else
  bad "D5: db.OpenIsolated is missing"
fi
# The whole point of OpenIsolated: it must NOT write the process-wide value.
if region_has "$OPENISO" 'func OpenIsolated(' '^}' 'SetActiveDialect'; then
  bad "D6: OpenIsolated calls SetActiveDialect — a conversion would change the dialect of every concurrent request"
else
  ok "D6: OpenIsolated never touches the process-wide active dialect"
fi
if region_has "$OPENISO" 'func OpenIsolated(' '^}' 'registerToolConnection'; then
  ok "D7: OpenIsolated still registers the per-connection backend (the migration chain reads it)"
else
  bad "D7: OpenIsolated does not register the connection — MigrateSQLite/MigratePostgres would not know their dialect"
fi
if grep -q '^func registerToolConnection(' internal/db/driver.go; then
  ok "D8: registerToolConnection exists in driver.go"
else
  bad "D8: registerToolConnection is missing"
fi
if grep -q 'func countTargetTables(' "$DBADMIN" && grep -q 'already contains' "$DBADMIN"; then
  ok "D9: an occupied target is refused before anything is opened"
else
  bad "D9: no emptiness pre-check — a conversion could clear a populated target"
fi
if grep -q 'convertReportView' "$DBADMIN" && grep -q 'db.convert_report_title' "$DBTPL"; then
  ok "D10: the per-table report is rendered on the page"
else
  bad "D10: the conversion result is not rendered"
fi

# --- E: the converter supports BOTH directions -------------------------------
if grep -q '"convert: only SQLite source supported' "$CONVERT"; then
  bad "E1: convert.go still refuses a PostgreSQL source — PostgreSQL → SQLite cannot run"
else
  ok "E1: no PostgreSQL-source refusal remains (both directions are accepted)"
fi
if grep -q 'information_schema.tables' "$CONVERT"; then
  ok "E2: the converter can list PostgreSQL tables"
else
  bad "E2: no PostgreSQL table listing — a PG source cannot be read"
fi
if grep -q 'pg_get_serial_sequence' "$CONVERT"; then
  ok "E3: PostgreSQL identity sequences are advanced after the copy"
else
  bad "E3: sequences are not reset — the first INSERT after a conversion collides with a copied id"
fi
if grep -q 'ApplyMigrations(toDB, toD.Kind)' "$CONVERT"; then
  ok "E4: the target schema comes from the target's own migration chain"
else
  bad "E4: the target schema is not built from the target's chain — indexes and triggers would be missing"
fi
if grep -q 'BeginTx(' "$CONVERT"; then
  ok "E5: the copy runs inside a transaction"
else
  bad "E5: the copy is not transactional — a failure leaves a partially filled target"
fi
if grep -q 'func topoOrder(' "$CONVERT" && grep -q 'FOREIGN KEY cycle' "$CONVERT"; then
  ok "E6: a real topological sort orders the tables parents-first and names a cycle"
else
  bad "E6: the table order is not a real topological sort"
fi
if region_has "$CONVERT" 'func ConvertWithReport(' '^}' 'TargetRows'; then
  ok "E7: row counts are read back from the target (the verification the old doc comment only claimed)"
else
  bad "E7: no row-count verification after the copy"
fi
# The CLI form the help text advertises.
if grep -q 'splitFlagValue' "$CLI"; then
  ok "E8: the CLI accepts --from=<dsn> as well as --from <dsn>"
else
  bad "E8: the CLI parser still rejects the --from=<dsn> form its own help documents"
fi

# --- F: the tab exists in the sidebar, both directions -----------------------
if grep -q 'href="/admin/database"' "$LAYOUT"; then
  ok "F1: layout.html links /admin/database"
else
  bad "F1: the sidebar still has no database tab"
fi
if region_has "$HANDLERS" '"InSectionData": {' '^[[:space:]]*},$' '"admin/database"'; then
  ok "F2: sectionPageSet() puts /admin/database in the Data section (the section auto-opens)"
else
  bad "F2: admin/database is absent from sectionPageSet — the sidebar would not open its own section"
fi
if grep -q 'return "nav.database"' "$HANDLERS"; then
  ok "F3: pageLabel() names the new tab (the breadcrumb is not empty)"
else
  bad "F3: pageLabel() has no admin/database case"
fi
if grep -q 'case "admin/database.html"' "$HANDLERS"; then
  ok "F4: pageTitle() maps the template to its title"
else
  bad "F4: pageTitle() has no admin/database.html case"
fi

# --- G: i18n, paired, and the stale STUB claim is gone -----------------------
if grep -q '"nav.database"' "$COMMON"; then
  n=$(grep -c '"nav.database"' "$COMMON")
  if [ "$n" -ge 2 ]; then
    ok "G1: nav.database is defined in both catalogues"
  else
    bad "G1: nav.database is defined $n time(s) — RU and EN must both have it"
  fi
else
  bad "G1: nav.database is missing"
fi
missing=""
for k in db.backend_title db.backend_type db.backend_file db.backend_size \
         db.backend_version db.backend_tables db.backend_help \
         db.backend_dsn_mismatch db.backend_dsn_mismatch_help \
         db.convert_title db.convert_help db.convert_target_kind \
         db.convert_target_path db.convert_pg_legend db.convert_password \
         db.convert_dry_run db.convert_btn db.convert_confirm \
         db.convert_steps_help db.convert_report_title \
         db.convert_report_verified db.convert_report_unverified \
         db.convert_col_table db.convert_col_source db.convert_col_target \
         db.convert_col_copied db.convert_col_note db.convert_dropped_columns \
         db.convert_from_to db.convert_copied_total db.convert_after_help; do
  c=$(grep -c "\"$k\":" "$RU" || true)
  if [ "${c:-0}" -lt 2 ]; then
    missing="$missing $k($c)"
  fi
done
if [ -z "$missing" ]; then
  ok "G2: every new db.backend_* / db.convert_* key exists in RU and EN"
else
  bad "G2: keys missing from one catalogue:$missing"
fi
# The UI used to tell the operator that dump/restore/cleanup were STUBs
# long after B202 made them real.
if grep -q 'STUB' "$RU"; then
  hits=$(grep -n 'STUB' "$RU" | head -5)
  bad "G3: the catalogues still claim a STUB (B202 shipped the real steps): $hits"
else
  ok "G3: the stale STUB claim is gone from the migration help text"
fi

# --- H: the regression tests exist and pass ----------------------------------
for tf in internal/db/convert_test.go internal/db/open_isolated_test.go \
          internal/db/convert_cross_pg_test.go internal/db/schema_parity_pg_test.go \
          cmd/skygate/db_migrate_test.go; do
  if [ -f "$tf" ]; then
    ok "H1: $tf exists"
  else
    bad "H1: $tf is missing"
  fi
done
# The PostgreSQL legs need a live server and SKIP without one (AGENTS rule 1).
# CI's test-pg job exports SKYGATE_TEST_PG_DSN for ./internal/db/... and runs
# them for real; this contract must not require a database to pass.
if grep -q 'SKYGATE_TEST_PG_DSN' internal/db/convert_cross_pg_test.go &&
   grep -q 'SKYGATE_TEST_PG_DSN' internal/db/schema_parity_pg_test.go; then
  ok "H1b: the cross-dialect tests are gated on SKYGATE_TEST_PG_DSN (SKIP, never FAIL, without a server)"
else
  bad "H1b: a cross-dialect test does not gate on SKYGATE_TEST_PG_DSN — it would fail on a host without PostgreSQL"
fi
if grep -q 'TestSchemaParity' internal/db/schema_parity_pg_test.go; then
  ok "H1c: the TD-17 schema-parity contract exists (the ROADMAP lists it as OPEN)"
else
  bad "H1c: no schema-parity test — the two chains are still compared by grep only"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ -run 'TestConvert|TestCoerceValue|TestTopoOrder|TestSharedColumns|TestTypeBase|TestOpenIsolated|TestDialectFor|TestRegisterBackendWrites|TestSchemaParity' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "H2: the converter + isolation + parity tests pass (the PostgreSQL legs SKIP here)"
  else
    bad "H2: the converter + isolation + parity tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./cmd/skygate/ -run 'TestParseDBMigrateArgs' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "H3: the db-migrate CLI parser tests pass (including the --from= form)"
  else
    bad "H3: the CLI parser tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "H2-H3: go not on PATH — run the B329 tests on the VM"
fi

# --- I: tracked by git (trap #11) --------------------------------------------
if git ls-files --error-unmatch scripts/check_b329_db_backend_ui.sh >/dev/null 2>&1; then
  ok "I1: scripts/check_b329_db_backend_ui.sh is tracked by git"
else
  bad "I1: scripts/check_b329_db_backend_ui.sh is NOT tracked — .gitignore can eat it silently"
fi

# --- J: registered in the catalog --------------------------------------------
if grep -q 'check_b329_db_backend_ui.sh' scripts/verify_pre_deploy.sh; then
  ok "J1: verify_pre_deploy.sh registers B329"
else
  bad "J1: verify_pre_deploy.sh does not register B329 — the contract would never run"
fi

# --- K: the block is in the index (AGENTS rule 2) ----------------------------
if grep -q 'B329' AGENTS.md; then
  ok "K1: AGENTS.md's block index carries B329"
else
  bad "K1: AGENTS.md has no B329 entry — the block index is the authoritative one-liner"
fi

printf '\n\033[1mB329 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
