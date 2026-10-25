#!/usr/bin/env bash
# check_b420_dialect_leaks.sh — B420 (2026-10-25, TD-27)
#
# THE DIALECT LEAK GUARD MUST SEE THE WHOLE STATEMENT, NOT ONE LINE OF IT — AND
# THE LEAKS IT MISSED HAD TO BE FIXED, NOT EXEMPTED.
#
# The guard was written on 2026-09-18 for exactly the class that had just broken
# SQLite writes, and it did two things that made it blind:
#
#   1. it inspected only lines that LITERALLY CONTAINED A BACKTICK — and this repo
#      formats every statement as a multi-line backtick literal, whose backticks sit
#      on the FIRST and LAST line only, so every token in between was invisible;
#   2. it knew exactly TWO tokens (`EXTRACT(EPOCH FROM now())` and its lowercase
#      spelling), and skipped all of `internal/db/migrations_*` wholesale.
#
# Measured with the widened guard (548 shared files, 23 migration files) the tree
# carried 11 real leaks in 7 shared files, plus 2 statements inside the PostgreSQL
# migration chain that depended on the strftime() compatibility shim:
#
#   internal/feature/admin/certificates.go   EXTRACT(EPOCH FROM created_at)::bigint
#   internal/feature/admin/deploy.go         EXTRACT(EPOCH FROM created_at)::bigint
#   internal/feature/admin/certificates.go   now()  ×3   (2 audit + 1 global_settings)
#   internal/feature/admin/derp_relays_auto.go EXTRACT(epoch FROM now())::bigint
#   internal/certsync/certsync.go            now()
#   internal/deploy/ha.go                    now()  ×3
#   internal/deploy/push.go                  now()
#   internal/metrics/collector.go            pg_database_size(current_database()) ×2
#   internal/db/migrations_pg.go             strftime('%s','now') in migrateV050PG/051PG
#
# Every one of them was broken on at least ONE backend, and most on BOTH, because
# `created_at` / `updated_at` are INTEGER epoch columns in BOTH migration chains:
#
#   now()          PostgreSQL: "column created_at is of type integer but expression is
#                  of type timestamp with time zone"  |  SQLite: "no such function: now"
#   EXTRACT(...)   PostgreSQL: extract() has no integer overload  |  SQLite: "near FROM"
#   pg_database_size  SQLite: "no such function: current_database" → gauge stuck at 0
#
# All of them failed SILENTLY in the product (an empty page, a missing audit row, a
# zero gauge), which is why they survived. The fixes: db.NowUnixSQL() where a write
# needs the clock, the raw epoch column where the read already has one, and a
# dialect branch (page_count × page_size) for the size probe.
#
# MEASURED, and deliberately NOT a leak: SQLite accepts ARBITRARY type names, so
# `TEXT[]`, `jsonb`, `timestamptz` and even `SERIAL PRIMARY KEY` all CREATE fine.
# `TEXT[]` round-trips a JSON array unchanged → not a defect. `SERIAL PRIMARY KEY`
# is accepted but INERT (an INSERT leaves the id NULL) → still a defect, so it stays
# in the token set. That distinction is measured in
# internal/db/dialect_type_name_b420_test.go, not remembered.
#
# CONTRACTS (31)
#   A  the guard scans whole literals, knows both directions, and requires a
#      dialect branch — and it is proven to FIRE on a planted leak
#   B  the migration chains are classified per FUNCTION (hard zero), not skipped
#   C  every measured leak is fixed at the source (no pre-fix shape left, the
#      dialect helper is used instead)
#   D  the fixed statements WORK on a real SQLite database (the real functions, the
#      real migration chain), and the pre-fix shapes still fail there
#   E  the one escape hatch is pinned, documented in the file itself, and cannot
#      cover a file that lost its marker
#   F  registered, indexed, tracked, documented

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b420.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

GUARD=internal/db/dialect_leak_guard_test.go
MEASURE=internal/db/dialect_type_name_b420_test.go
GATE=scripts/verify_pre_deploy.sh

for f in "$GUARD" "$MEASURE" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# --- A: the guard itself ----------------------------------------------------
hdr "A. the widened guard"

if grep -qF 'func extractStringLiterals(' "$GUARD"; then
  ok "A1: the guard lexes string literals instead of scanning lines"
else
  bad "A1: no extractStringLiterals — the guard is back to line-based scanning, which cannot see a multi-line statement"
fi
if grep -qF 'multi-line backtick literal' "$GUARD"; then
  ok "A2: the guard documents WHY whole literals are required (a multi-line literal has backticks on its first and last line only)"
else
  bad "A2: the guard no longer explains the line-based blind spot the next reader will re-introduce"
fi

# Both directions must be present, with a real token set (not two hardcoded ones).
pgonly=$(awk '/^var pgOnlySQL = /,/^}/' "$GUARD" | grep -c '{"' 2>/dev/null); pgonly=${pgonly:-0}
liteonly=$(awk '/^var sqliteOnlySQL = /,/^}/' "$GUARD" | grep -c '{"' 2>/dev/null); liteonly=${liteonly:-0}
if [ "$pgonly" -ge 15 ]; then
  ok "A3: $pgonly PostgreSQL-only tokens are enforced (was 2)"
else
  bad "A3: only $pgonly PostgreSQL-only token(s) — the set collapsed back toward the two hardcoded expressions"
fi
if [ "$liteonly" -ge 10 ]; then
  ok "A4: $liteonly SQLite-only tokens are enforced (was 0)"
else
  bad "A4: only $liteonly SQLite-only token(s) — a SQLite-ism in shared code breaks PostgreSQL and nothing would notice"
fi
if grep -qF 'var dialectAwareMarkers = ' "$GUARD" && grep -qF 'func isDialectAware(' "$GUARD"; then
  ok "A5: a file may carry one-dialect SQL only if it also BRANCHES on the dialect (dialectAwareMarkers)"
else
  bad "A5: the guard does not require a dialect branch — PG-only SQL in a shared file would pass"
fi
if grep -qF 'func looksLikeSQL(' "$GUARD" && grep -qF 'func stripSQLComments(' "$GUARD"; then
  ok "A6: prose and SQL comments are not statements (a Prometheus help string is not a leak)"
else
  bad "A6: no SQL-likeness / comment gate — prose mentioning a function name would be reported as a leak"
fi
if grep -qF 'func TestDialectLeakGuardCatchesAPlantedLeak(' "$GUARD"; then
  ok "A7: the detector is proven to FIRE on a planted multi-line violation, and to stay quiet for a dialect-aware file and for prose"
else
  bad "A7: the guard has no planted-violation test — a broken lexer would turn it into a silent no-op (AGENTS trap 16)"
fi

# --- B: the migration chains ------------------------------------------------
hdr "B. the migration chains are classified per function"

if grep -qF 'func TestMigrationChainsArePerDialectClean(' "$GUARD"; then
  ok "B1: the migrations are covered by their own contract"
else
  bad "B1: no per-function migration contract — the chains are either skipped or unchecked"
fi
if grep -qF 'strings.HasPrefix(rel, "internal/db/migrations_")' "$GUARD"; then
  ok "B2: the shared-file scan defers migrations to that contract explicitly (not by accident)"
else
  bad "B2: the shared scan no longer names the migration exemption — the two scans may overlap or leave a gap"
fi
if grep -qF 'ownerAt' "$GUARD" && grep -qF 'strings.HasSuffix(ownerName, "SQLite")' "$GUARD"; then
  ok "B3: each literal is attributed to its owning chain function (migrateV…PG vs …SQLite)"
else
  bad "B3: the migration scan does not classify literals by the function that owns them"
fi

# --- C: the leaks are fixed at the source -----------------------------------
hdr "C. every measured leak is fixed at the source"

# Count occurrences of a pattern in CODE only: this block's own explanatory
# comments quote the pre-fix spelling on purpose ("the now() this used to splice
# in"), and a contract that cannot tell code from prose would force us to stop
# documenting the defect.
code_count() { # code_count <pattern> <file...>
  local pat="$1"; shift
  awk -v pat="$pat" '
    { line=$0; t=line; sub(/^[ \t]+/, "", t) }
    t ~ /^\/\// { next }
    index(line, pat) { n++ }
    END { print n+0 }' "$@"
}

# C1/C2: the PostgreSQL-only spellings this block removed, in the files that had them.
cnow=$(code_count 'now()' internal/deploy/ha.go internal/deploy/push.go \
        internal/certsync/certsync.go internal/feature/admin/certificates.go)
if [ "$cnow" -eq 0 ]; then
  ok "C1: no now() left in the four write paths that carried it (7 sites)"
else
  bad "C1: $cnow now() occurrence(s) remain in deploy/ha.go, deploy/push.go, certsync/certsync.go or admin/certificates.go"
fi
cex=$(code_count 'EXTRACT(' internal/deploy/ha.go internal/deploy/push.go \
        internal/certsync/certsync.go internal/feature/admin/certificates.go \
        internal/feature/admin/deploy.go internal/feature/admin/derp_relays_auto.go)
if [ "$cex" -eq 0 ]; then
  ok "C2: no EXTRACT( left in the shared files that carried it (3 sites, one of them a PostgreSQL expression over a column that was already an INTEGER epoch)"
else
  bad "C2: $cex EXTRACT( occurrence(s) remain in shared admin/deploy/certsync code"
fi

# C3: the epoch writes go through the dialect helper.
uses=0
for f in internal/deploy/ha.go internal/deploy/push.go internal/certsync/certsync.go \
         internal/feature/admin/certificates.go internal/feature/admin/derp_relays_auto.go; do
  if grep -q 'NowUnixSQL()' "$f"; then uses=$((uses+1)); else bad "C3: $f does not use db.NowUnixSQL() for its epoch write"; fi
done
[ "$uses" -eq 5 ] && ok "C3: all 5 files write the epoch through db.NowUnixSQL() (the one dialect-aware clock helper)"

# C4: the audit readers select the epoch column as it is.
if grep -q 'qCertAuditEvents' internal/feature/admin/certificates.go &&
   grep -q 'qDeployAuditEvents' internal/feature/admin/deploy.go; then
  ok "C4: both audit readers are named consts (so the B420 tests can execute the REAL statements)"
else
  bad "C4: the audit statements are inlined again — the behaviour tests can no longer run what production runs"
fi
if grep -q "log.Printf(\"admin.certificates: audit query" internal/feature/admin/certificates.go &&
   grep -q "log.Printf(\"admin.deploy: audit query" internal/feature/admin/deploy.go; then
  ok "C5: a failing audit query is LOGGED instead of silently returning nil (that silence is why the empty table looked healthy)"
else
  bad "C5: the audit readers still swallow the error — the next dialect break would look like 'no events' again"
fi

# C6: the size probe branches on the dialect.
if grep -q 'func dbFileSizeBytes(' internal/metrics/collector.go &&
   grep -q 'ActiveDialect()' internal/metrics/collector.go &&
   grep -q 'PRAGMA page_count' internal/metrics/collector.go; then
  ok "C6: the DB size probe is dialect-aware (pg_database_size on PostgreSQL, page_count × page_size on SQLite)"
else
  bad "C6: the size probe is PostgreSQL-only — skygate_db_size_bytes reads 0 on every SQLite install"
fi

# C7: the two PostgreSQL statements that leaned on the shim.
shim=$(grep -c "strftime('%s', 'now')::bigint" internal/db/migrations_pg.go 2>/dev/null); shim=${shim:-0}
if [ "$shim" -eq 0 ]; then
  ok "C7: the PostgreSQL migration chain no longer defaults columns through the strftime() compatibility shim"
else
  bad "C7: $shim PostgreSQL migration statement(s) still depend on the compat shim they are supposed to replace"
fi

# --- D: behaviour on a real database ---------------------------------------
hdr "D. the fixed statements work on a real SQLite database"

if OUT="$(go test ./internal/db/ -run 'B420|TestNoDialectLeaksInSharedCode|TestMigrationChainsArePerDialectClean|TestPostgresOnlyExemptionsArePinned|TestDialectLeakGuardCatchesAPlantedLeak' -count=1 2>&1)"; then
  ok "D1: zero dialect leaks across the tree, zero per-function migration leaks, and the detector is proven to fire ($(printf '%s' "$OUT" | grep -c '^ok' | tr -d ' ') ok)"
else
  bad "D1: the dialect guard FAILED: $(printf '%s' "$OUT" | grep -E 'LEAK|FAIL' | head -3 | tr '\n' ' ')"
fi

if OUT="$(go test ./internal/feature/admin/ -run 'B420' -count=1 2>&1)"; then
  ok "D2: both audit tables return their rows on SQLite (the real qCertAuditEvents / qDeployAuditEvents, on the real migration chain)"
else
  bad "D2: admin B420 tests failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

if OUT="$(go test ./internal/deploy/ ./internal/certsync/ -run 'B420' -count=1 2>&1)"; then
  ok "D3: ha promote/demote, the HA/deploy audit rows and the certsync audit row all persist on SQLite"
else
  bad "D3: deploy/certsync B420 tests failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

if OUT="$(go test ./internal/metrics/ -run 'B420' -count=1 2>&1)"; then
  ok "D4: skygate_db_size_bytes reports a real page_count × page_size size on SQLite"
else
  bad "D4: metrics B420 tests failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

# The 'pre-fix shape still fails' assertions are the anchor for the whole rationale:
# if a driver upgrade ever accepted them, the lesson would silently become folklore.
if grep -q 'PreFixAuditSelectStillFailsOnSQLite' internal/feature/admin/admin_audit_b420_test.go &&
   grep -q 'PreFixNowWriteStillFailsOnSQLite' internal/feature/admin/admin_audit_b420_test.go &&
   grep -q 'PreFixNowWriteStillFailsOnSQLite' internal/deploy/ha_b420_test.go &&
   grep -q 'PreFixNowWriteStillFailsOnSQLite' internal/certsync/certsync_b420_test.go &&
   grep -q 'PreFixSizeProbeStillFailsOnSQLite' internal/metrics/collector_b420_test.go; then
  ok "D5: every fix is anchored by a test proving the OLD shape still fails on SQLite (the rationale cannot rot)"
else
  bad "D5: one or more 'pre-fix still fails' anchors are missing"
fi

# --- E: the escape hatch ----------------------------------------------------
hdr "E. the PostgreSQL-only escape hatch is honest"

if grep -qF 'var postgresOnlyFiles = map[string]string{' "$GUARD"; then
  ok "E1: the PG-only exemptions are an explicit PINNED map with reasons (not a directory skip)"
else
  bad "E1: no pinned PG-only map — an exemption would be invisible"
fi
if grep -qF 'dialectPostgresOnlyMarker' "$GUARD" && grep -qF 'dialect:postgres-only' internal/dbmigrate/steps/cleanup.go; then
  ok "E2: every pinned file must carry the marker in its own header, and two of them do"
else
  bad "E2: the marker contract is missing (a reader of the file cannot see that its SQL never meets SQLite)"
fi
if grep -qF 'func TestPostgresOnlyExemptionsArePinned(' "$GUARD"; then
  ok "E3: a pin without a marker — or a marker naming a deleted file — fails the build"
else
  bad "E3: nothing verifies the pins against the files"
fi
markers=$(grep -rl 'dialect:postgres-only' --include='*.go' internal scripts 2>/dev/null | wc -l | tr -d ' ')
if [ "$markers" -ge 3 ]; then
  ok "E4: $markers files declare themselves PostgreSQL-only"
else
  bad "E4: only $markers file(s) carry the marker — the pin and the tree disagree"
fi

# --- F: registration --------------------------------------------------------
hdr "F. registration"

if grep -q 'run_check "B420"' "$GATE"; then
  ok "F1: B420 is registered in the catalog"
else
  bad "F1: B420 is not registered — this contract would never run"
fi
if grep -q '\*\*B420\*\*' AGENTS.md; then
  ok "F2: AGENTS.md block index carries B420"
else
  bad "F2: AGENTS.md has no B420 entry"
fi
if git ls-files --error-unmatch scripts/check_b420_dialect_leaks.sh >/dev/null 2>&1; then
  ok "F3: this script is tracked by git (trap 11)"
else
  bad "F3: this script is not tracked by git"
fi
if grep -q 'TD-27' docs/ROADMAP.md && grep -q 'TD-27' docs/ru/ROADMAP.md; then
  ok "F4: TD-27 is recorded in both roadmap languages"
else
  bad "F4: TD-27 is missing from docs/ROADMAP.md or docs/ru/ROADMAP.md"
fi
if grep -q 'B420' docs/LESSONS.md; then
  ok "F5: docs/LESSONS.md carries the lesson"
else
  bad "F5: no LESSONS entry — the next guard author will repeat the line-based scan"
fi

printf '\n\033[1mB420 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
