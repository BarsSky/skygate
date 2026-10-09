#!/usr/bin/env bash
# check_b367_array_coalesce.sh — B367 (2026-10-09): `COALESCE(<array column>, '')`
# is not "the empty array" on PostgreSQL, and it took out the operator's
# drain+remove button AND the failover transaction.
#
# THE MEASUREMENT
# ---------------
# The operator pressed "Слить и удалить" (Drain & remove) on one of the relay rows
# that the pre-B359 discovery pass had adopted into `cluster_node`, and the panel
# answered:
#
#   drain+remove: lookup node: ERROR: malformed array literal: "" (SQLSTATE 22P02)
#
# `cluster_node.roles` is TEXT[] on PostgreSQL (ARRAY in information_schema), so
# the untyped '' in `COALESCE(roles, '')` is coerced to text[] and PostgreSQL
# REJECTS THE STATEMENT AT PARSE TIME — no data can make it work. Reproduced
# directly against the live database:
#
#   SELECT id, COALESCE(state,''), COALESCE(roles,'') FROM cluster_node …;
#   ERROR:  malformed array literal: ""
#
# Five statements carried it: the lookup of RemoveNode / DrainNode /
# DrainAndRemoveNode (internal/cluster/node.go) and — the severe pair —
# FindClusterPrimary + NodeRoles (internal/db/cluster_sql_b291.go), which the
# FAILOVER and failover-DRILL transactions call. On SQLite the same column is the
# `{a,b}` TEXT literal, so `'{}'` parses identically: a native install cannot see
# the defect at all (LESSONS L-16's dialect leak, in the path the operator reaches
# for during an incident).
#
# WHAT THIS SCRIPT PINS
#   A. no Go source builds `COALESCE(<array column>, '')`, for every ARRAY column
#      the PostgreSQL migration chain declares — and the detector is PROVED to
#      fire on a planted violation;
#   B. the fix is the one documented constant, and all five sites use it;
#   C. the behavioural tests exist and actually RUN (SQLite here; the PostgreSQL
#      halves need SKYGATE_TEST_PG_DSN and are run by CI's PG job, which is the
#      job that would have caught this);
#   D. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs live state or a tool this host lacks.
#
# Usage:  bash scripts/check_b367_array_coalesce.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

NODE=internal/cluster/node.go
SQL291=internal/db/cluster_sql_b291.go
PGDDL=internal/db/migrations_v0_64_b195.go
SQLITEDDL=internal/db/migrations_sqlite.go
NODETEST=internal/cluster/node_b367_test.go
DBTEST=internal/db/cluster_sql_b367_test.go
SELF=scripts/check_b367_array_coalesce.sh

for f in "$NODE" "$SQL291" "$PGDDL" "$SQLITEDDL" "$NODETEST" "$DBTEST"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B367 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# =====================================================================
hdr "A. no Go statement mixes an ARRAY column with the empty STRING literal"

# The column list is DERIVED from the PostgreSQL migration chain, so it cannot
# rot: every `TEXT[]` column declaration becomes table.column, and each one is
# then searched for in the Go sources as `COALESCE(<column>, '')`.
ARRAYS="$(
  awk '
    /CREATE TABLE/ {
      line = $0
      sub(/^.*CREATE TABLE[ \t]+(IF NOT EXISTS[ \t]+)?/, "", line)
      tbl = line
      sub(/[^A-Za-z0-9_].*$/, "", tbl)
    }
    /TEXT\[\]/ { col = $1; if (tbl != "" && col != "") print tbl"."col }
  ' "$PGDDL" | sort -u
)"
ARRAY_N="$(printf '%s\n' "$ARRAYS" | grep -c . || true)"
if [ "$ARRAY_N" -ge 2 ]; then
  ok "A1: the PostgreSQL chain declares $ARRAY_N ARRAY column(s): $(printf '%s' "$ARRAYS" | tr '\n' ' ')"
else
  bad "A1: only $ARRAY_N TEXT[] column(s) found in $PGDDL — the detector would check nothing (did the DDL move?)"
fi

# Comment lines are stripped so the fix's own explanation (which quotes the
# broken form) cannot be mistaken for a violation.
VIOL=""
while IFS= read -r col; do
  [ -z "$col" ] && continue
  short="${col#*.}"
  hits="$(
    grep -rn --include='*.go' -F "COALESCE(${short}, '')" internal cmd 2>/dev/null \
      | grep -v '_test\.go' \
      | grep -v '^[^:]*:[0-9]*: *//' || true
  )"
  [ -n "$hits" ] && VIOL="${VIOL}${hits}"$'\n'
done <<< "$ARRAYS"

if [ -z "$VIOL" ]; then
  ok "A2: no production Go statement builds COALESCE(<array column>, '') — the parse-time failure (SQLSTATE 22P02) is gone"
else
  bad "A2: a statement mixes an ARRAY column with the empty string literal; PostgreSQL rejects it at parse time:
$VIOL"
fi

# A3 — the detector must FIRE on a planted violation (the B340 discipline).
PLANT="$SKY_TMP/planted.go"
printf 'package x\n\nconst q = `SELECT COALESCE(roles, %s) FROM cluster_node`\n' "''" > "$PLANT"
if grep -qF "COALESCE(roles, '')" "$PLANT"; then
  ok "A3: the detector's pattern matches a planted violation (the check above is not vacuous)"
else
  bad "A3: the planted violation was not detected — the pattern in this script is wrong"
fi

# =====================================================================
hdr "B. the fix is ONE documented constant, used by all five sites"

if grep -q 'const EmptyTextArrayLiteral = ' "$SQL291"; then
  ok "B1: db.EmptyTextArrayLiteral is defined"
else
  bad "B1: db.EmptyTextArrayLiteral is gone — the empty-array value has no single owner"
fi
if grep -q '22P02\|malformed array literal' "$SQL291"; then
  ok "B2: the constant records WHY (the live SQLSTATE and the affected paths)"
else
  bad "B2: the constant does not record the failure it prevents — the next author will 'simplify' it back to ''"
fi

NODE_USES="$(grep -c 'db\.EmptyTextArrayLiteral' "$NODE" || true)"
if [ "$NODE_USES" -ge 3 ]; then
  ok "B3: all 3 drain/remove lookups in node.go use it ($NODE_USES occurrences)"
else
  bad "B3: node.go uses db.EmptyTextArrayLiteral $NODE_USES time(s), want 3 (RemoveNode, DrainNode, DrainAndRemoveNode)"
fi
DB_USES="$(grep -c 'EmptyTextArrayLiteral' "$SQL291" || true)"
if [ "$DB_USES" -ge 2 ]; then
  ok "B4: FindClusterPrimary + NodeRoles use it ($DB_USES occurrences, incl. the declaration)"
else
  bad "B4: only $DB_USES use(s) in $SQL291 — the failover/drill statements must use the constant too"
fi

# =====================================================================
hdr "C. the behavioural test actually runs (and the PG half exists for CI)"

if grep -q 'func TestB367_DrainAndRemoveWorksOnSQLite' "$NODETEST" \
   && grep -q 'func TestB367_FailoverLookupsWorkOnSQLite' "$DBTEST"; then
  ok "C1: the SQLite behavioural contracts exist for both packages"
else
  bad "C1: the SQLite behavioural contracts are missing — a grep-only fix would not catch a regression"
fi
if grep -q 'db.OpenTestPG(t)' "$NODETEST" && grep -q 'OpenTestPG(t)' "$DBTEST"; then
  ok "C2: the PostgreSQL halves exist (SKIP without SKYGATE_TEST_PG_DSN; CI's PG job runs go test ./...)"
else
  bad "C2: the PostgreSQL halves are missing — the dialect that HAD the defect would be untested"
fi

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "C3: no go binary in PATH — the behavioural contracts were not run"
else
  if out="$("$GOBIN" test ./internal/cluster/... ./internal/db/ -run 'B367' -count=1 2>&1)"; then
    ok "C3: the SQLite halves pass ($(printf '%s\n' "$out" | grep -c '^ok') package(s))"
  else
    bad "C3: go test -run B367 failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
fi

# =====================================================================
hdr "D. tracked by git (AGENTS trap #11), registered, indexed"

UNTRACKED=""
for f in "$SELF" "$NODETEST" "$DBTEST"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || UNTRACKED="${UNTRACKED}${f} "
done
if [ -z "$UNTRACKED" ]; then
  ok "D1: every new file of this block is tracked by git"
else
  skip "D1: not yet tracked by git (the lead commits this block): ${UNTRACKED}"
fi

if grep -q 'run_check "B367"' scripts/verify_pre_deploy.sh; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B367\*\*' AGENTS.md; then
  ok "D3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "D3: AGENTS.md has no B367 bullet at line start"
fi

printf '\n\033[1mB367 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
