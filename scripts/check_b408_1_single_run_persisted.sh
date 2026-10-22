#!/usr/bin/env bash
# check_b408_1_single_run_persisted.sh
#
# 2026-10-21 (B408.1) — «ПРОГНАТЬ ЭТОТ» MUST LEAVE A TRACE.
#
# WHY (measured on the operator's installation, v1.5.151 live)
#   20:41:35  skyadmin  system_tests_run  started test exit_rules.domain_prefix (run f2203374b438)
#   20:42:17  skyadmin  system_tests_run  started test exit_rules.domain_prefix (run 8be1d4df586d)
#
#   He pressed the per-row button twice. Both starts are audited. `system_tests_runs`
#   gained NOTHING, because runTestsAsync persisted only whole-catalogue runs:
#
#       persisted := false
#       if only == "" { s.PersistRun(...) }
#
#   So after the reload the row fell back to the last FULL run — three days old — and
#   the only reasonable conclusion was "the re-run does not work, the test still
#   fails". The button worked; its result was discarded.
#
# CONTRACTS
#   A. a single-test run is persisted, tagged with its scope
#   B. the row's verdict comes from the newest run that CONTAINS it
#   C. the history RATES stay comparable
#   D. pinned, registered, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/db/migrations_v0_81_system_test_only.go ] || {
  printf 'B408.1: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b4081.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

MIG=internal/db/migrations_v0_81_system_test_only.go
RUN=internal/feature/admin/system_tests_run_state_b394.go
RUNTIME=internal/feature/admin/system_tests_runtime.go
READER=internal/feature/admin/system_tests_runs.go
HANDLER=internal/feature/admin/system_tests_handlers.go
TEST=internal/feature/admin/system_tests_b394_run_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

MIG_SRC="$(src "$MIG")"; RUN_SRC="$(src "$RUN")"; RT_SRC="$(src "$RUNTIME")"
RD_SRC="$(src "$READER")"; HD_SRC="$(src "$HANDLER")"; TEST_SRC="$(src "$TEST")"

hdr "B408.1 — «прогнать этот» must leave a trace"

# ---------------------------------------------------------------------------
# A. persisted, with its scope
# ---------------------------------------------------------------------------
hdr "A — a single-test run is stored"

if has 'only_test TEXT NOT NULL DEFAULT' "$MIG_SRC"; then
  ok "A1: V081 adds system_tests_runs.only_test (both chains)"
else
  bad "A1: no scope column"
fi
if grep -q 'migrateV081PG' internal/db/driver_postgres.go 2>/dev/null && grep -q 'migrateV081SQLite' internal/db/driver_sqlite.go 2>/dev/null; then
  ok "A2: registered in BOTH migration chains (rule 9)"
else
  bad "A2: a chain is missing V081 — the native install would diverge"
fi
if has 'func (s *Service) PersistRunScoped(' "$RT_SRC"; then
  ok "A3: PersistRunScoped writes the scope"
else
  bad "A3: the scope cannot be written"
fi
if has 'PersistRunScoped(ctx, results, summary, userID, only)' "$RUN_SRC"; then
  ok "A4: the runner passes the scope of the run it just made"
else
  bad "A4: the runner does not pass its scope"
fi
# The guard is specifically "persist only when only == ''" — testsForRun's own
# `if only == ""` is a different thing and must stay. Look for the unscoped write.
if has 's.PersistRun(ctx,' "$RUN_SRC"; then
  bad "A5: the runner still calls the UNSCOPED writer — a single-test run has no scope"
else
  ok "A5: every run goes through PersistRunScoped with its scope"
fi

# ---------------------------------------------------------------------------
# B. the row reads the newest run that CONTAINS it
# ---------------------------------------------------------------------------
hdr "B — the row shows what was just measured"

if has 'func (s *Service) ListLastResultsPerTest(' "$RD_SRC"; then
  ok "B1: the reader returns the newest verdict PER TEST"
else
  bad "B1: no per-test reader — one re-run would blank the other rows"
fi
if has 'if perTest, err := s.ListLastResultsPerTest(' "$HD_SRC"; then
  ok "B2: the page uses it for LastResults"
else
  bad "B2: the page still renders the newest run only"
fi
if has 'LastResultsPerTest' "$HD_SRC"; then
  ok "B3: the page can say the verdicts come from different runs"
else
  bad "B3: the page cannot distinguish the two sources"
fi

# ---------------------------------------------------------------------------
# C. the history RATES ignore single-test runs
# ---------------------------------------------------------------------------
hdr "C — rates stay comparable"

if has "WHERE only_test = ''" "$RD_SRC"; then
  ok "C1: ListRecentRuns (the rates) excludes single-test rows"
else
  bad "C1: a re-run would enter the history rates"
fi
if has 'must not write a history row' "$TEST_SRC"; then
  bad "C2: the B394 test still pins the old contract"
else
  ok "C2: the B394 contract was renegotiated, not left contradicting the code"
fi
if has 'only_test = %q, want the test name' "$TEST_SRC"; then
  ok "C3: the test asserts the stored scope"
else
  bad "C3: the stored scope is not asserted"
fi

# ---------------------------------------------------------------------------
# D. pinned, registered, documented
# ---------------------------------------------------------------------------
hdr "D — pinned, registered, documented"

if go test -count=1 -run 'TestSingleTestRunDoesNotPolluteHistory_B394' ./internal/feature/admin/ >"$SKY_TMP/b4081.log" 2>&1; then
  ok "D1: the renegotiated test passes"
else
  bad "D1: the test fails: $(tail -3 "$SKY_TMP/b4081.log" | tr '\n' ' ')"
fi
if git ls-files --error-unmatch scripts/check_b408_1_single_run_persisted.sh >/dev/null 2>&1; then
  ok "D2: this script is tracked by git (trap #11)"
else
  bad "D2: this script is not tracked"
fi
if grep -q 'check_b408_1_single_run_persisted' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D3: registered in scripts/verify_pre_deploy.sh"
else
  bad "D3: not registered in the catalog"
fi
if grep -q '^\- \*\*B408\.1\*\*' AGENTS.md 2>/dev/null; then
  ok "D4: one-line entry in the AGENTS.md block index"
else
  bad "D4: no B408.1 entry in the AGENTS block index"
fi
if grep -q 'B408.1' docs/ROADMAP.md 2>/dev/null && grep -q 'B408.1' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "D5: both roadmaps record the block (rule 12)"
else
  bad "D5: a roadmap does not record the block"
fi

printf '\n\033[1mB408.1: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
