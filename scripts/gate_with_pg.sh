#!/usr/bin/env bash
# scripts/gate_with_pg.sh — run the guarantee catalog against REAL PostgreSQL (TD-24).
#
# WHY THIS EXISTS. `verify_pre_deploy.sh` never sets `SKYGATE_TEST_PG_DSN` and cannot
# invent one, so a plain gate run prints
#
#   PostgreSQL coverage: SQLite only — SKYGATE_TEST_PG_DSN is not set, so every
#   PG-gated test SKIPs (recipe: docs/operations.md §15)
#
# and then PASSes — exercising the dialect the operator's install does NOT use (the
# reference install runs PostgreSQL). Ten packages gate real tests on that variable
# (`OpenTestPG`, `db.OpenForTest`, raw `os.Getenv`), and CI is the only place they ever
# ran. TD-24 measured that the PG half is green but that nobody ran it from the VM; this
# wrapper is the "somebody": it takes the DSN, REFUSES a database that is not obviously a
# throwaway, proves the DSN can `CREATE SCHEMA` (the prerequisite that used to fail with
# a misleading permission error), and then runs the catalog with the variable exported.
#
# THE GUARD IS THE POINT. The suite creates and drops schemas in the database named by the
# DSN. Pointed at `skygate_staging` it would happily work next to production tables, and a
# killed run would leave test schemas behind (LESSONS: interrupted PG runs used to look
# like regressions). So a DSN whose database name does not look like a scratch database is
# refused BEFORE anything connects, and the refusal says why.
#
# USAGE
#   SKYGATE_TEST_PG_DSN='postgres://user:pw@host:5432/skygate_pgtest?sslmode=disable' \
#     bash scripts/gate_with_pg.sh [--check-only] [extra ids for SKYGATE_GATE_ONLY]
#
#   --check-only   run the guards and the schema probe, print the verdict, DO NOT run the
#                  catalog (used by the contract, and useful before a long run).
#
# EXIT: 0 ok / 1 refusal / 2 cannot verify.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

CHECK_ONLY=0
ARGS=()
for a in "$@"; do
  case "$a" in
    --check-only) CHECK_ONLY=1 ;;
    *) ARGS+=("$a") ;;
  esac
done

say()  { printf '%s\n' "$*"; }
fail() { printf 'gate-with-pg: %s\n' "$*" >&2; exit 1; }

DSN="${SKYGATE_TEST_PG_DSN:-}"
if [ -z "$DSN" ]; then
  fail "SKYGATE_TEST_PG_DSN is not set. This wrapper will not guess a database: name a
  scratch one (see docs/operations.md §15), e.g.
    SKYGATE_TEST_PG_DSN='postgres://user:pw@<PG_IP>:5432/skygate_pgtest?sslmode=disable' bash $0"
fi

# --- the guard: the database name is part of the contract ---------------------------
# Extract the path component (the database) from a libpq URL or key/value DSN.
DB=""
case "$DSN" in
  postgres://*|postgresql://*)
    rest="${DSN#*://}"                 # user:pw@host:port/db?params
    hostpart="${rest#*@}"              # host:port/db?params  (user:pw may itself contain @, so use the LAST @)
    case "$rest" in
      *@*) hostpart="${rest##*@}" ;;
    esac
    dbpart="${hostpart#*/}"            # db?params | (empty)
    dbpart="${dbpart%%\?*}"            # db
    DB="$dbpart"
    ;;
  *)
    # key=value form: dbname=...
    for kv in $DSN; do
      case "$kv" in
        dbname=*) DB="${kv#dbname=}" ;;
      esac
    done
    ;;
esac
[ -n "$DB" ] || fail "cannot read a database name out of SKYGATE_TEST_PG_DSN — refusing to run
  the suite against an unnamed database (the DSN must be a URL: postgres://u:p@h:5432/<db>?…)"

case "$DB" in
  postgres|template0|template1|skygate_staging|skygate_prod|skygate|production|prod)
    fail "refusing to run the test suite against database '$DB': the PG-gated tests CREATE and
  DROP schemas, so they must run in a scratch database of their own (skygate_pgtest,
  skygate_citest, …), never next to real tables. Point SKYGATE_TEST_PG_DSN at a throwaway
  database (docs/operations.md §15)."
    ;;
esac
case "$DB" in
  *test*|*Test*|*probe*|*scratch*|*gate*) : ;;
  *)
    fail "refusing database '$DB': the name does not look like a throwaway. Include
  test/pgtest/citest/scratch/gate in the database name so this guard can tell it apart from
  a real one (docs/operations.md §15)."
    ;;
esac

say "gate-with-pg: database=$DB"

# --- the prerequisite: the DSN must be able to CREATE SCHEMA ------------------------
if ! command -v psql >/dev/null 2>&1; then
  fail "psql is not on this host, so the CREATE SCHEMA prerequisite cannot be verified.
  Install the PostgreSQL client, or run this on the machine that has it."
fi
# The probe schema must NOT start with pg_ — that prefix is reserved by PostgreSQL, and a
# probe named pg_gate_probe fails for a reason that has nothing to do with permissions
# (measured 2026-10-01, docs/operations.md §15).
PROBE="gate_probe_$$"
if ! OUT="$(psql "$DSN" -v ON_ERROR_STOP=1 -q -c "CREATE SCHEMA \"$PROBE\"" -c "DROP SCHEMA \"$PROBE\"" 2>&1)"; then
  fail "the DSN cannot create a schema — every OpenTestPG test would fail:
  $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
say "gate-with-pg: CREATE SCHEMA/DROP SCHEMA ok (probe $PROBE)"

if [ "$CHECK_ONLY" = "1" ]; then
  say "gate-with-pg: check-only — the catalog was NOT run"
  exit 0
fi

# --- the run ------------------------------------------------------------------------
say "gate-with-pg: running the catalog with SKYGATE_TEST_PG_DSN exported"
say "  (the banner must read 'PostgreSQL test coverage: ENABLED'; 'SQLite only' means the"
say "   variable did not reach the checks)"
if [ "${#ARGS[@]}" -gt 0 ]; then
  SKYGATE_GATE_ONLY="${ARGS[*]}" SKYGATE_TEST_PG_DSN="$DSN" bash scripts/verify_pre_deploy.sh
  rc=$?
else
  SKYGATE_TEST_PG_DSN="$DSN" bash scripts/verify_pre_deploy.sh
  rc=$?
fi

if [ "$rc" -ne 0 ]; then
  say "gate-with-pg: the catalog FAILED (rc=$rc) with database=$DB"
  exit "$rc"
fi
say "gate-with-pg: catalog green with PostgreSQL coverage (database=$DB)"
