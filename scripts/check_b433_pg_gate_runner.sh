#!/usr/bin/env bash
# check_b433_pg_gate_runner.sh — B433 / TD-24 (2026-10-27)
#
# «ПОЛНЫЙ PG-ГЕЙТ НА ЭТАЛОННОЙ ВМ» — the gate must be runnable against the dialect the
# operator's install actually uses.
#
# TD-24's complaint was measured, not hypothetical: `verify_pre_deploy.sh` never sets
# `SKYGATE_TEST_PG_DSN`, so every VM gate run prints
#
#   PostgreSQL coverage: SQLite only — SKYGATE_TEST_PG_DSN is not set, so every
#   PG-gated test SKIPs (recipe: docs/operations.md §15)
#
# and then PASSes. Ten packages gate real tests on that variable (OpenTestPG,
# db.OpenForTest, raw os.Getenv), i.e. the SQLite dialect was verified while the
# PostgreSQL one — the one the reference install runs — was not. The recipe existed in
# docs/operations.md §15; what did not exist was a way to run it that cannot be pointed at
# the wrong database.
#
# scripts/gate_with_pg.sh is that way, and this contract pins the parts that matter:
#   * it refuses to guess (no DSN → named refusal, exit 1);
#   * it refuses a REAL database by name BEFORE connecting — the suite creates and drops
#     schemas, so `skygate_staging` must be unrepresentable;
#   * it refuses a name that does not look like a throwaway (no test/pgtest/citest/
#     scratch/gate), because "some random database" is how the guard would be bypassed;
#   * it proves the CREATE SCHEMA prerequisite and names the psql absence instead of
#     running 10 packages into a wall of confusing failures;
#   * it exports the variable for the catalog and says which database it used.
#
# CONTRACTS (13)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

RUNNER=scripts/gate_with_pg.sh
DOC=docs/operations.md
GATE=scripts/verify_pre_deploy.sh
for f in "$RUNNER" "$DOC" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the runner exists and is a bash script"
if [ -f "$RUNNER" ]; then
  ok "A1: $RUNNER exists"
else
  bad "A1: $RUNNER is missing — the PG recipe stays a paragraph nobody executes"
fi
if bash -n "$RUNNER" 2>/dev/null; then
  ok "A2: the runner is syntactically valid bash"
else
  bad "A2: the runner does not parse"
fi
if head -1 "$RUNNER" | grep -q '^#!/usr/bin/env bash'; then
  ok "A3: the runner has a bash shebang"
else
  bad "A3: the runner does not declare bash"
fi

hdr "B. it refuses to guess"
# B386: match the VARIABLE with a shell builtin (`case`), never `printf | grep -q` under
# pipefail — a host fault in that pipeline would become a verdict about the runner.
OUT="$(env -u SKYGATE_TEST_PG_DSN bash "$RUNNER" 2>&1)"; RC=$?
case "$OUT" in
  *"SKYGATE_TEST_PG_DSN is not set"*)
    if [ "$RC" -ne 0 ]; then
      ok "B1: with no DSN it exits non-zero and names the variable"
    else
      bad "B1: it named the variable but exited 0"
    fi
    ;;
  *)
    bad "B1: with no DSN it did not refuse by name (rc=$RC, out=$(printf '%s' "$OUT" | head -2 | tr '\n' ' '))"
    ;;
esac

hdr "C. a REAL database is refused before anything connects"
for real in skygate_staging postgres skygate_prod; do
  OUT="$(SKYGATE_TEST_PG_DSN="postgres://u:p@127.0.0.1:5432/$real?sslmode=disable" bash "$RUNNER" --check-only 2>&1)"; RC=$?
  case "$OUT" in
    *"refusing to run the test suite against database '$real'"*)
      if [ "$RC" -ne 0 ]; then
        ok "C1: refusing '$real' (the suite would create schemas next to real tables)"
      else
        bad "C1: '$real' was named in the refusal but the runner exited 0"
      fi
      ;;
    *)
      bad "C1: '$real' was NOT refused by name (rc=$RC, out=$(printf '%s' "$OUT" | head -2 | tr '\n' ' '))"
      ;;
  esac
done
case "$OUT" in
  *"never next to real tables"*)
    ok "C2: the refusal says WHY (schemas created next to real tables)"
    ;;
  *)
    bad "C2: the refusal does not explain the danger"
    ;;
esac

hdr "D. a name that does not look like a throwaway is refused"
OUT="$(SKYGATE_TEST_PG_DSN='postgres://u:p@127.0.0.1:5432/metrics?sslmode=disable' bash "$RUNNER" --check-only 2>&1)"; RC=$?
case "$OUT" in
  *"does not look like a throwaway"*)
    if [ "$RC" -ne 0 ]; then
      ok "D1: an unnamed-looking database is refused"
    else
      bad "D1: the refusal was printed but the runner exited 0"
    fi
    ;;
  *)
    bad "D1: a non-throwaway name was not refused (rc=$RC, out=$(printf '%s' "$OUT" | head -2 | tr '\n' ' '))"
    ;;
esac

hdr "E. a scratch DSN gets PAST the guards and stops at the prerequisite"
OUT="$(SKYGATE_TEST_PG_DSN='postgres://u:p@127.0.0.1:5432/skygate_pgtest?sslmode=disable' bash "$RUNNER" --check-only 2>&1)"; RC=$?
if [ "$RC" -eq 0 ]; then
  # psql exists and the probe succeeded against a real server.
  case "$OUT" in
    *"CREATE SCHEMA/DROP SCHEMA ok"*)
      ok "E1: with psql present the CREATE SCHEMA prerequisite is verified and reported"
      ;;
    *)
      bad "E1: the runner passed but did not report the schema probe"
      ;;
  esac
else
  # No psql, or no server: it must say WHICH prerequisite failed, not run 10 packages
  # into a wall of confusing failures.
  case "$OUT" in
    *"psql is not on this host"*|*"cannot create a schema"*)
      ok "E1: a scratch DSN reaches the prerequisite and the failure names it"
      ;;
    *)
      bad "E1: a scratch DSN failed for an unnamed reason (rc=$RC, out=$(printf '%s' "$OUT" | head -3 | tr '\n' ' '))"
      ;;
  esac
fi
case "$OUT" in
  *"database=skygate_pgtest"*)
    ok "E2: the run names the database it would use"
    ;;
  *)
    bad "E2: the runner does not say which database it targets"
    ;;
esac
case "$OUT" in
  *"CREATE SCHEMA"*|*"psql is not on this host"*)
    ok "E3: the prerequisite check runs BEFORE the catalog (no silent 25-minute failure)"
    ;;
  *)
    bad "E3: the prerequisite is not checked before the run"
    ;;
esac

hdr "F. the recipe points at the runner, and the catalog keeps the coverage line"
if grep -qF 'gate_with_pg.sh' "$DOC"; then
  ok "F1: docs/operations.md §15 names the runner (the recipe people actually read)"
else
  bad "F1: the documented recipe does not mention the runner — the gate stays manual folklore"
fi
if grep -qF 'PostgreSQL test coverage' "$GATE"; then
  ok "F2: the catalog still prints which database it exercised (B334)"
else
  bad "F2: the catalog lost its coverage banner"
fi

hdr "G. registration"
if grep -q 'run_check "B433"' "$GATE"; then
  ok "G1: B433 is registered in the catalog"
else
  bad "G1: B433 is not registered — this contract would never run"
fi
if grep -q '\*\*B433\*\*' AGENTS.md; then
  ok "G2: AGENTS.md block index carries B433"
else
  bad "G2: AGENTS.md has no B433 entry"
fi
if git ls-files --error-unmatch scripts/check_b433_pg_gate_runner.sh >/dev/null 2>&1; then
  ok "G3: this script is tracked by git (trap 11)"
else
  bad "G3: this script is not tracked by git"
fi

printf '\n\033[1mB433 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
