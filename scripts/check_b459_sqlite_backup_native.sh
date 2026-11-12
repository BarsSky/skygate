#!/usr/bin/env bash
# ============================================================================
# check_b459_sqlite_backup_native.sh — B459
#
# THE BACKUP MUST SPEAK THE DATABASE'S DIALECT.
#
# Measured 2026-10-29 on the standby host (native install, no docker, SQLite
# backend — the v1.5.4+ default): `scripts/backup.sh` — the script
# internal/backup/runner.go invokes for the in-app "Run now" button — had NO
# SQLite path at all. Step 3 called load_dsn(), which answers
# "SKYGATE_DB_DSN is not set in the script env or .env" on a SQLite install,
# so the run failed and nothing was exported; and the pre-B459 flow could not
# even reach the copy because v1.3.1 had deleted the SQLite branch
# ("Phase 2 of SQLite removal") — a removal v1.5.4 made stale by making
# SQLite the default backend again.
#
# What the contracts below pin:
#   A. the dialect dispatcher exists (backup_dialect / sqlite_db_path)
#   B. the SQLite branch copies the FILE, not a docker named volume
#   C. a missing/empty/non-SQLite copy FAILS the run instead of shipping an
#      archive whose database is a husk
#   D. WAL handling: checkpoint when sqlite3 exists, copy -wal/-shm otherwise
#   E. the PostgreSQL flow is unchanged
#   F. the integrity check has a SQLite branch and is no longer reachable
#      only through `docker info`
#   G. the inventory names the artifact that was actually produced
#   H. BEHAVIOURAL: the two helpers are extracted from the real script and
#      executed against 7 inputs (sqlite:, file:, bare path, postgres://,
#      empty+no .env, empty+.env fallback, .env with quotes)
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b459.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

SCRIPT="scripts/backup.sh"
PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }

if [ ! -f "$SCRIPT" ]; then
  echo "FAIL: $SCRIPT not found (run from the repository root)" >&2
  exit 1
fi

has() { case "$(cat "$SCRIPT")" in *"$1"*) return 0;; *) return 1;; esac; }

echo "=== B459: SQLite backup on a native install ==="

# --- A: the dispatcher ---
for fn in "skygate_db_value()" "backup_dialect()" "sqlite_db_path()"; do
  if has "$fn"; then ok "A: $fn defined"; else bad "A: $fn missing from $SCRIPT"; fi
done
if has 'DB_DIALECT=$(backup_dialect)'; then
  ok "A2: the database step dispatches on DB_DIALECT"
else
  bad "A2: the database step does not dispatch on the dialect"
fi

# --- B: the SQLite branch copies the file ---
if has 'cp -p "${SQLITE_DB}" "${BACKUP_PATH}/skygate.db"'; then
  ok "B1: SQLite branch copies the resolved FILE"
else
  bad "B1: SQLite branch does not copy the resolved file"
fi
if has 'docker run --rm -v skygate-data:/data'; then
  bad "B2: the dead docker-volume copy is still present"
else
  ok "B2: no docker-volume copy of the SQLite database"
fi

# --- C: a husk must fail the run, not ship ---
if has 'skygate.db copy is empty'; then
  ok "C1: an empty copy fails the run"
else
  bad "C1: an empty copy is not detected"
fi
if has 'SQLite format 3'; then
  ok "C2: the SQLite header magic is verified on the copy"
else
  bad "C2: the SQLite header magic is not verified"
fi
if has 'SQLite database not found'; then
  ok "C3: a missing database file fails the run"
else
  bad "C3: a missing database file is not reported"
fi

# --- D: WAL handling ---
if has "PRAGMA wal_checkpoint(TRUNCATE)"; then
  ok "D1: WAL checkpoint is attempted through sqlite3"
else
  bad "D1: no WAL checkpoint in the SQLite branch"
fi
for sfx in -wal -shm; do
  case "$sfx" in "-wal") _n="WAL" ;; *) _n="SHM" ;; esac
  if has 'for _suffix in -wal -shm' && has '"${SQLITE_DB}${_suffix}"' && has '"${BACKUP_PATH}/skygate.db${_suffix}"'; then
    ok "D2: the ${_n} sibling is carried with the copy"
  else
    bad "D2: the ${_n} sibling is not carried with the copy"
  fi
done

# --- E: PostgreSQL unchanged ---
if has 'load_dsn' && has 'pg_dump_run "${BACKUP_PATH}/skygate-pg.sql"'; then
  ok "E1: the PostgreSQL flow is intact"
else
  bad "E1: the PostgreSQL flow was disturbed"
fi

# --- F: integrity check ---
if has "PRAGMA integrity_check"; then
  ok "F1: the integrity check has a SQLite branch"
else
  bad "F1: no PRAGMA integrity_check"
fi
if has 'if [[ "${DB_DIALECT}" == "sqlite" ]]; then' && has 'elif docker info'; then
  ok "F2: the integrity check is dialect-first, not docker-gated"
else
  bad "F2: the integrity check still starts from docker availability"
fi

# --- G: inventory ---
if has 'SKYGATE_DB_DIALECT=${DB_DIALECT}'; then
  ok "G1: the inventory records the dialect"
else
  bad "G1: the inventory does not record the dialect"
fi

# --- H: BEHAVIOURAL — run the real helpers on real inputs ---
extract_fn() {
  awk -v name="$1" '
    index($0, name "() {") == 1 { on = 1 }
    on { print }
    on && /^}$/ { exit }
  ' "$SCRIPT"
}

{
  # B462 made skygate_db_value depend on skygate_env_file (the env file the SERVICE
  # reads, which on a native install is /etc/skygate/skygate.env). The behavioural
  # legs below exercise the real call chain, so the dependency has to be extracted
  # too — otherwise the .env-fallback cases fail for a missing FUNCTION rather than
  # for a wrong answer.
  extract_fn skygate_env_file
  extract_fn skygate_db_value
  extract_fn backup_dialect
  extract_fn sqlite_db_path
} > "$SKY_TMP/helpers.sh"

if [ -s "$SKY_TMP/helpers.sh" ] && bash -n "$SKY_TMP/helpers.sh" 2>/dev/null; then
  ok "H0: the helpers extract cleanly and parse"
else
  bad "H0: the helpers could not be extracted"
fi

mkdir -p "$SKY_TMP/nodotenv" "$SKY_TMP/withdotenv" "$SKY_TMP/quoted"
printf 'SKYGATE_DB=sqlite:/srv/skygate/skygate.db\n' > "$SKY_TMP/withdotenv/.env"
printf 'SKYGATE_DB="sqlite:/quoted/path.db"\n'      > "$SKY_TMP/quoted/.env"

behaviour() {
  local desc="$1" want_dialect="$2" want_path="$3" skydb="$4" skydir="$5"
  local got_dialect got_path
  got_dialect=$(cd "$SKY_TMP" && SKYGATE_DIR="$skydir" SKYGATE_DB="$skydb" bash -c '
    . "$1/helpers.sh"
    backup_dialect' _ "$SKY_TMP" 2>/dev/null)
  got_path=$(cd "$SKY_TMP" && SKYGATE_DIR="$skydir" SKYGATE_DB="$skydb" bash -c '
    . "$1/helpers.sh"
    sqlite_db_path' _ "$SKY_TMP" 2>/dev/null)
  if [ "$got_dialect" = "$want_dialect" ] && [ "$got_path" = "$want_path" ]; then
    ok "H: $desc -> dialect=$got_dialect path='${got_path}'"
  else
    bad "H: $desc -> expected dialect=$want_dialect path='${want_path}', got dialect='${got_dialect}' path='${got_path}'"
  fi
}

behaviour "SKYGATE_DB=sqlite:/abs/db"      sqlite   "/srv/db.db"      "sqlite:/srv/db.db"        "$SKY_TMP/nodotenv"
behaviour "SKYGATE_DB=file:/abs/db"        sqlite   "/srv/db.db"      "file:/srv/db.db"          "$SKY_TMP/nodotenv"
behaviour "SKYGATE_DB=bare absolute path"  sqlite   "/srv/db.db"      "/srv/db.db"               "$SKY_TMP/nodotenv"
behaviour "SKYGATE_DB=postgres://..."      postgres ""                "postgres://u:p@h:5432/d"  "$SKY_TMP/nodotenv"
behaviour "empty + no .env -> postgres"    postgres ""                ""                         "$SKY_TMP/nodotenv"
behaviour "empty + .env fallback"          sqlite   "/srv/skygate/skygate.db" ""                "$SKY_TMP/withdotenv"
behaviour "quoted .env value"              sqlite   "/quoted/path.db" ""                       "$SKY_TMP/quoted"

# --- I: the CLI family must open the database through the DIALECT-AWARE opener ---
# Measured live on the SQLite stand: `skygate oidc-export`, `skygate init status`
# and every `skygate cluster …` verb died with
#   cannot parse `sqlite:/…/skygate.db`: failed to parse as keyword/value
# because they called db.OpenDSN, which is PostgreSQL-only. Invisible in the
# gate: it only reproduces on a non-PostgreSQL install.
echo ""
echo "=== B459: the CLI must speak the dialect too ==="

OPENDSN_SITES=$(grep -rn 'db\.OpenDSN(cfg\.DBDSN)' cmd/skygate/*.go 2>/dev/null | wc -l | tr -d '[:space:]')
if [ "$OPENDSN_SITES" = "0" ]; then
  ok "I1: no CLI subcommand calls db.OpenDSN(cfg.DBDSN) (PostgreSQL-only opener)"
else
  bad "I1: ${OPENDSN_SITES} CLI site(s) still call the PostgreSQL-only opener"
fi

if [ -f cmd/skygate/cli_db_b459.go ] && grep -q 'db.OpenWithDialect(cfg.DBDSN)' cmd/skygate/cli_db_b459.go; then
  ok "I2: openCLIDB routes through db.OpenWithDialect"
else
  bad "I2: openCLIDB is missing or does not use the dialect-aware opener"
fi

if grep -q 'case "--env":' cmd/skygate/oidc_export_b304.go; then
  ok "I3: oidc-export accepts --env (RELEASE-NOTES documented it, the parser rejected it)"
else
  bad "I3: oidc-export still rejects --env"
fi
if grep -q 'case "--from-env":' cmd/skygate/oidc_export_b304.go; then
  ok "I4: oidc-export has the explicit --from-env opt-out"
else
  bad "I4: oidc-export has no --from-env opt-out"
fi
if grep -q -- '--from-env' cmd/skygate/oidc_export_b304.go && \
   awk '/func oidcExportHelp/,/^}$/' cmd/skygate/oidc_export_b304.go | grep -q -- '--from-env'; then
  ok "I5: the help text lists --from-env"
else
  bad "I5: the help text does not list --from-env"
fi
if grep -q 'refusing to print env values' cmd/skygate/oidc_export_b304.go; then
  ok "I6: an unreadable database is an ERROR, not a warning plus env values"
else
  bad "I6: an unreadable database still degrades silently"
fi

# --- J: script-side dialect SQL, and the wildcard tagOwner refusal ---
echo ""
echo "=== B459: scripts and the imported-ACL wildcard ==="

# Three deploy scripts write their audit row through psql (PostgreSQL) but used
# SQLite's strftime(). The PG chain installs a strftime shim, so the row landed —
# but the shim IGNORES its format argument and always returns the epoch, so an
# ISO-shaped field received a number, and the INSERT depended on a shim the
# project is retiring (B420).
for f in scripts/bootstrap_standby.sh deploy/scripts/setup-skygate-public.sh deploy/scripts/create-standby-preauth.sh; do
  if [ ! -f "$f" ]; then
    bad "J1: $f is missing"
    continue
  fi
  sql_calls=$(grep -cE "strftime\('[%sY-]" "$f" 2>/dev/null || true); sql_calls=${sql_calls:-0}
  if [ "$sql_calls" = "0" ] && grep -q 'EXTRACT(EPOCH FROM now())::bigint' "$f" \
     && grep -q "to_char(now() AT TIME ZONE 'UTC'" "$f"; then
    ok "J1: $(basename "$f") uses native PostgreSQL time expressions"
  else
    bad "J1: $(basename "$f") still calls SQLite strftime() or lost the ISO timestamp"
  fi
done

if grep -q 'func wildcardTagOwnerProblem' internal/feature/admin/acl_import.go \
   && grep -q 'wildcardTagOwnerProblem(shape)' internal/feature/admin/acl_import.go; then
  ok "J2: validateImportedACL refuses a wildcard in tagOwners"
else
  bad "J2: validateImportedACL still accepts wildcards that headscale 0.29 rejects"
fi

J3_COUNT=$(grep -c '^func TestValidateImportedACL_.*_B459' internal/feature/admin/acl_import_b459_test.go 2>/dev/null || true)
J3_COUNT=${J3_COUNT:-0}
if [ "$J3_COUNT" -ge 5 ]; then
  ok "J3: ${J3_COUNT} behavioural tests cover the wildcard rule (accept + refuse + determinism)"
else
  bad "J3: expected >=5 B459 tests on the ACL import validation, found ${J3_COUNT}"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB459: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB459: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL
# — an unconditional `exit 0` after printing a summary is the defect that made 40+
# contracts decorative.
[ "$FAIL" -eq 0 ]
