#!/usr/bin/env bash
# check_b419_fast_test_db.sh — B419 (2026-10-25, TD-30)
#
# A PER-TEST DATABASE MUST NOT PAY FOR DURABILITY IT DOES NOT NEED.
#
# TD-30 recorded `go test ./internal/feature/exit_rules/` at **214 s** on the reference
# VM and blamed the per-test migration chain ("every test migrates a fresh SQLite DB").
# Measured on 2026-10-25 instead of assumed:
#
#	one full SQLite migration chain, default pragmas   536-880 ms
#	synchronous(0) alone                               561 ms
#	journal_mode(memory) alone                        1151 ms
#	BOTH                                               53 ms      ← 16x
#
# The cost is fsync: the chain is hundreds of tiny DDL commits, so neither pragma helps
# alone (each commit still fsyncs once) and together they remove both fsyncs. A throwaway
# test database does not need durability, and the SCHEMA still comes from MigrateSQLite —
# the same chain production runs, not a hand-written one.
#
# Effect measured on this workstation: `internal/feature/exit_rules` 72 s → 8.6 s.
#
# CONTRACTS (11)
#   A  the helper exists, carries BOTH pragmas, and is documented for tests only
#   B  no per-test database opens the DEFAULT way any more (a ratchet at zero), and the
#      schema still comes from the real migration chain
#   C  the fast handle is used only from tests — never from production code
#   D  the behavioural guards pass (the pragmas are live; the fast handle is never SLOWER,
#      measured in the same test binary so a slow host cannot decide it)
#   E  registered, indexed, tracked

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b419.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

HELPER=internal/db/testhelpers.go
GUARD=internal/db/fastdb_b419_test.go
GATE=scripts/verify_pre_deploy.sh

for f in "$HELPER" "$GUARD" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# --- A: the helper ---------------------------------------------------------
hdr "A. the fast handle"

if grep -qF 'func OpenFastSQLiteForTest(' "$HELPER"; then
  ok "A1: OpenFastSQLiteForTest exists"
else
  bad "A1: no OpenFastSQLiteForTest helper"
fi
missing=0
for p in 'synchronous(0)' 'journal_mode(memory)'; do
  if ! grep -qF "$p" "$HELPER"; then
    bad "A2: the helper does not set $p — measured, NEITHER pragma helps alone (561 ms / 1151 ms vs 53 ms together)"
    missing=1
  fi
done
[ "$missing" -eq 0 ] && ok "A2: both pragmas are set (the combination is what buys the 16x)"
if grep -qiE 'only from tests|for tests|test_helpers' "$HELPER"; then
  ok "A3: the helper is documented as test-only (production keeps OpenWithDialect, where durability IS the point)"
else
  bad "A3: the helper does not say it is test-only — the next reader will use it for the database"
fi

# --- B: the ratchet --------------------------------------------------------
hdr "B. no per-test database pays for durability it does not need"

raw=$(grep -rl 'OpenWithDialect("sqlite:" + t\.TempDir()' --include='*_test.go' . 2>/dev/null | wc -l | tr -d ' ')
if [ "$raw" -eq 0 ]; then
  ok "B1: zero per-test databases open through the default path (ratchet at 0)"
else
  bad "B1: $raw test file(s) still open a per-test database through the default path — each one pays ~0.5 s of chain time in fsync; use db.OpenFastSQLiteForTest"
fi
uses=$(grep -rl 'OpenFastSQLiteForTest(' --include='*_test.go' . 2>/dev/null | wc -l | tr -d ' ')
if [ "$uses" -ge 10 ]; then
  ok "B2: $uses test files use the fast handle"
else
  bad "B2: only $uses test file(s) use the fast handle — the migration covered 26+"
fi
# The schema must still come from the real chain: a hand-written schema in a test would
# make the speed meaningless and the coverage fake.
mig=$(grep -rh 'MigrateSQLite(' --include='*_test.go' internal | wc -l | tr -d ' ')
if [ "$mig" -ge 10 ]; then
  ok "B3: the tests still migrate through the REAL chain ($mig MigrateSQLite calls)"
else
  bad "B3: only $mig MigrateSQLite call(s) in tests — the schema may have been hand-written"
fi

# --- C: test-only ----------------------------------------------------------
hdr "C. the fast handle never reaches production code"

prod=$(grep -rl 'OpenFastSQLiteForTest(' --include='*.go' internal cmd 2>/dev/null \
        | grep -v '_test\.go$' | grep -v '^internal/db/testhelpers.go$' | wc -l | tr -d ' ')
if [ "$prod" -eq 0 ]; then
  ok "C1: no non-test file calls the fast handle"
else
  bad "C1: $prod production file(s) call OpenFastSQLiteForTest — durability is not optional there"
fi

# --- D: the behavioural guards --------------------------------------------
hdr "D. the guards"

if OUT="$(go test ./internal/db/ -run B419 -count=1 2>&1)"; then
  ok "D1: the fast handle carries both pragmas AND is not slower than the default one (same-binary measurement)"
else
  bad "D1: go test -run B419 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi

# --- E: registration -------------------------------------------------------
hdr "E. registration"

if grep -q 'run_check "B419"' "$GATE"; then
  ok "E1: B419 is registered in the catalog"
else
  bad "E1: B419 is not registered — this contract would never run"
fi
if grep -q '\*\*B419\*\*' AGENTS.md; then
  ok "E2: AGENTS.md block index carries B419"
else
  bad "E2: AGENTS.md has no B419 entry"
fi
if git ls-files --error-unmatch scripts/check_b419_fast_test_db.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap 11)"
else
  bad "E3: this script is not tracked by git"
fi

printf '\n\033[1mB419 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
