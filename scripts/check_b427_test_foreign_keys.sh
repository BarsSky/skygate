#!/usr/bin/env bash
# check_b427_test_foreign_keys.sh — B427 (TD-35, 2026-10-26)
#
# «СХЕМА С ВНЕШНИМИ КЛЮЧАМИ, КОТОРУЮ НИКТО НЕ ПРОВЕРЯЛ» — TD-35, замерено и закрыто.
#
# B426.1 нашёл, что у per-test SQLite-хэндла пропал busy_timeout: openSQLiteWith
# добавляет foreign_keys(1) + journal_mode(WAL) + busy_timeout(2000) к файловому DSN,
# но ДОВЕРЯЕТ DSN, в котором уже есть `_pragma=`, а быстрые прагмы B419 приносили свои.
# Та же замена убрала foreign_keys(1) — а ветка `:memory:` не получала прагм ВООБЩЕ:
#
#	PRAGMA foreign_keys  на per-test хэндле  = 0
#	PRAGMA foreign_keys  на :memory:         = 0
#	PRAGMA busy_timeout  на :memory:         = 0
#
# То есть с v1.5.165 КАЖДАЯ per-test база (а их в наборе десятки) работала со схемой,
# в которой ~40 клауз `FOREIGN KEY … ON DELETE CASCADE` были линтом, а не ограничением:
# сиротскую строку можно было вставить, и дефект каскада не мог завалить тест.
#
# Включение ключей НЕ прошло молча — оно нашло настоящий дефект продукта (см. D): запись
# cluster_database кладёт в `primary_node_id` ПУСТУЮ СТРОКУ вместо NULL. Это не «primary
# ещё не выбран», это внешний ключ на узел с пустым id; PostgreSQL такое отвергает всегда,
# а SQLite — как только foreign_keys включён (в продакшене он включён всегда). Ни один
# тест не ловил этого, потому что тестовые базы ограничение не проверяли.
#
# CONTRACTS (12)
#   A  the fast per-test handle keeps foreign_keys (and the other three pragmas)
#   B  the :memory: branch carries the pragmas instead of passing the DSN through
#   C  behaviour: the four B427 tests pass, including the detector proof
#   D  the product defect the constraint exposed: NULL, not "" — and the reader agrees
#   E  registration / index / tracked

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

HELP=internal/db/testhelpers.go
OPEN=internal/db/open_sqlite.go
CLUSTER=internal/db/cluster.go
TESTS=internal/db/sqlite_foreign_keys_b427_test.go
GATE=scripts/verify_pre_deploy.sh
for f in "$HELP" "$OPEN" "$CLUSTER" "$TESTS" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the fast per-test handle keeps what production keeps"
PRAGMAS="$(sed -n 's/^const sqliteFastTestPragmas = "\(.*\)"$/\1/p' "$HELP")"
if [ -z "$PRAGMAS" ]; then
  bad "A1: sqliteFastTestPragmas could not be read (a fact about this check, not the product)"
else
  case "$PRAGMAS" in
    *foreign_keys\(1\)*) ok "A1: the fast test DSN carries foreign_keys(1) — the schema's constraints are real in tests again" ;;
    *) bad "A1: the fast test DSN has no foreign_keys(1): $PRAGMAS" ;;
  esac
  case "$PRAGMAS" in
    *busy_timeout*) ok "A2: B426.1's busy_timeout is still there" ;;
    *) bad "A2: busy_timeout disappeared from the fast pragmas" ;;
  esac
  case "$PRAGMAS" in
    *synchronous\(0\)*journal_mode\(memory\)*) ok "A3: B419's synchronous(0) + journal_mode(memory) are still there (the 16x migration win)" ;;
    *) bad "A3: the B419 pragmas disappeared from the fast handle" ;;
  esac
fi

hdr "B. the :memory: branch carries the pragmas instead of passing the DSN through"
if grep -q 'sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)&_pragma=busy_timeout(2000)")' "$OPEN"; then
  ok "B1: the :memory: DSN now installs foreign_keys(1) + busy_timeout(2000) (measured: the driver accepts query parameters on :memory: itself)"
else
  bad "B1: the :memory: branch does not install the pragmas — every test on that DSN runs without foreign keys"
fi
if grep -qF 'sql.Open("sqlite", ":memory:")' "$OPEN"; then
  bad "B2: a bare :memory: open is still present — it gets no pragmas at all"
else
  ok "B2: no bare :memory: open remains (the branch cannot silently regress to pragma-less)"
fi

hdr "C. behaviour: the constraint is enforced, and the detector can fail"
if OUT="$(go test ./internal/db/ -run 'TestB427' -count=1 2>&1)"; then
  ok "C1: foreign keys are enforced on the fast handle AND on :memory:, the detector handle without the pragma still accepts the orphan, and an empty primary_node_id stores NULL"
else
  bad "C1: the behavioural contracts failed: $(printf '%s' "$OUT" | grep -E '^(---|FAIL)' | head -4 | tr '\n' ' ')"
fi
for probe in TestB427_FastHandleEnforcesForeignKeys TestB427_MemoryHandleEnforcesForeignKeys TestB427_DetectorProvesTheDifference TestB427_SetClusterDatabaseWritesNullPrimaryNode; do
  if grep -qF "func $probe(" "$TESTS"; then
    ok "C2: $probe exists"
  else
    bad "C2: $probe is missing from the behavioural file"
  fi
done

hdr "D. the product defect the constraint exposed: NULL, not an empty string"
if grep -qF 'var primary any' "$CLUSTER" && grep -qF 'cd.ID, cd.ClusterID, primary, cd.ReplicaNodeIDs,' "$CLUSTER"; then
  ok "D1: SetClusterDatabase binds NULL for an empty primary_node_id (PostgreSQL refused the empty string all along)"
else
  bad "D1: SetClusterDatabase still writes an empty string into a foreign-key column"
fi
if grep -qF "COALESCE(primary_node_id, '')" "$CLUSTER"; then
  ok "D2: the reader still COALESCEs the nullable column back to an empty string (the two halves agree)"
else
  bad "D2: the read path no longer COALESCEs primary_node_id — the writer and reader disagree about NULL"
fi

hdr "E. registration, index, tracked"
grep -q 'run_check "B427"' "$GATE" && ok "E1: registered in the catalog" || bad "E1: not registered"
grep -q '\*\*B427\*\*' AGENTS.md && ok "E2: AGENTS.md block index carries B427" || bad "E2: no AGENTS entry"
if git ls-files --error-unmatch scripts/check_b427_test_foreign_keys.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap 11)"
else
  bad "E3: this script is not tracked by git yet — a check nobody can run is not a check"
fi

printf '\n\033[1mB427 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
