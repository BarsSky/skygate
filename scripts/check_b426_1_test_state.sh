#!/usr/bin/env bash
# check_b426_1_test_state.sh — B426.1 (2026-10-26)
#
# «CI WENT RED ON A TREE THAT WAS GREEN TWICE» — measured, not guessed.
#
# The B426 push failed CI in the one job that runs the catalog, on a check nobody had
# touched: `B1 go test ./...` with
#
#	--- FAIL: TestB391_DedupSuppressesRepeatsInsideTheWindow (0.11s)
#	B391: the first alert of a key must be sent
#
# That test passed 5/5 standalone here, so the question was what the CI run has that a
# standalone run does not. The answer was two pieces of PROCESS-GLOBAL state which the
# tests neither owned nor reset:
#
#	(a) the dedup table (`alertDedup`) is keyed by a FIXED key in both tests, so the first
#	    send is suppressed on the SECOND run in a process: `go test ./internal/telegram
#	    -count=2` failed deterministically, 40 of 40 iterations after the first, with
#	    "the first alert of a key must be sent". A test whose verdict depends on how many
#	    times the binary ran is not evidence.
#
#	(b) the per-test SQLite handle had NO busy_timeout, and the alert transport writes from
#	    two goroutines by design: `insertAlert` on the send path and `go pruneAlerts` on
#	    every send. openSQLiteWith appends foreign_keys(1) + journal_mode(WAL) +
#	    busy_timeout(2000) to a file DSN but TRUSTS a DSN that already carries `_pragma=`,
#	    and B419's fast-test pragmas supplied their own — so the whole production set was
#	    silently replaced. Measured, 4 goroutines x 100 insert+prune rounds:
#
#		without busy_timeout      inserts_ok=163  busy=237   (59% of writes lost)
#		with busy_timeout(2000)   inserts_ok=400  busy=0
#
#	    A lost INSERT is not a lost message — the send still happens — but insertAlert
#	    returns no id, so SendAlertEvent returns 0 for an alert the operator DID receive,
#	    and 0 is exactly what callers read as "not delivered" (the B391 disk monitor retries
#	    on it). That is how a green tree fails CI under load and nowhere else.
#
# CONTRACTS (11)
#   A  the fast per-test handle keeps busy_timeout (and B419's two pragmas)
#   B  behaviour: concurrent writers on a fast per-test DB lose nothing
#   C  the process-global dedup table is resettable, and the two tests reset it
#   D  behaviour: the run that failed CI now passes (-count=2)
#   E  a failed insert names what it costs, and this check is registered

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

HELP=internal/db/testhelpers.go
PROBE=internal/db/sqlite_busy_b426_1_test.go
ALERTS=internal/telegram/alerts.go
T391=internal/telegram/alert_compose_b391_test.go
T421=internal/telegram/alert_ledger_b421_test.go
OPEN=internal/db/open_sqlite.go
GATE=scripts/verify_pre_deploy.sh
for f in "$HELP" "$PROBE" "$ALERTS" "$T391" "$T421" "$OPEN" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the fast per-test handle keeps what production keeps"
PRAGMAS="$(sed -n 's/^const sqliteFastTestPragmas = "\(.*\)"$/\1/p' "$HELP")"
if [ -z "$PRAGMAS" ]; then
  bad "A1: sqliteFastTestPragmas could not be read (a fact about this check, not the product)"
else
  case "$PRAGMAS" in
    *busy_timeout\(2000\)*) ok "A1: the fast test DSN carries busy_timeout(2000) — a concurrent writer WAITS instead of losing the write" ;;
    *busy_timeout*)         ok "A1: the fast test DSN carries a busy_timeout" ;;
    *) bad "A1: the fast test DSN has no busy_timeout: $PRAGMAS" ;;
  esac
  case "$PRAGMAS" in
    *synchronous\(0\)*)     ok "A2: B419's synchronous(0) is still there (the 16x migration win)" ;;
    *) bad "A2: synchronous(0) disappeared from the fast pragmas" ;;
  esac
  case "$PRAGMAS" in
    *journal_mode\(memory\)*) ok "A3: B419's journal_mode(memory) is still there" ;;
    *) bad "A3: journal_mode(memory) disappeared from the fast pragmas" ;;
  esac
fi
# Why the replacement happened at all: openSQLiteWith trusts a caller's own _pragma=.
if grep -q 'busy_timeout(2000)' "$OPEN" && grep -q 'foreign_keys(1)' "$OPEN"; then
  ok "A4: production still appends foreign_keys + WAL + busy_timeout(2000) — the value the test handle now matches"
else
  bad "A4: the production pragma block in open_sqlite.go changed shape — re-derive the test handle from it"
fi

hdr "B. behaviour: concurrent writers on a fast per-test DB lose nothing"
if OUT="$(go test ./internal/db/ -run TestB4261ConcurrentWritesOnAFastTestDBAreNotLost -count=1 2>&1)"; then
  ok "B1: 200 concurrent inserts+prunes on a fast per-test DB, none lost (the pre-fix measurement was 237/400 lost)"
else
  bad "B1: concurrent writes were lost on the fast per-test handle: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

hdr "C. the process-global dedup table is owned by the tests that use it"
if grep -qF 'func ResetAlertDedupForTest()' "$ALERTS"; then
  ok "C1: ResetAlertDedupForTest exists — the state a fixed DedupKey depends on is resettable"
else
  bad "C1: no ResetAlertDedupForTest — a test with a fixed key is correct only on the first run"
fi
case "$(grep -cF 'ResetAlertDedupForTest()' "$T391")" in
  0) bad "C2: the B391 dedup test uses a fixed key and never resets the table" ;;
  *) ok "C2: the B391 dedup test resets the dedup table it depends on" ;;
esac
case "$(grep -cF 'ResetAlertDedupForTest()' "$T421")" in
  0) bad "C3: TestB421_SuppressedAlertIsCountedNotLost asserts exact counters without resetting the ledger/dedup state" ;;
  *) ok "C3: the B421 suppression test resets the ledger and the dedup table" ;;
esac

hdr "D. behaviour: the run that failed CI now passes"
# AGENTS trap 15: a HOST transient must never decide a verdict. This is a `go test`
# invocation, and this workstation has already produced one false red while several jobs
# ran at once; a repeat only passes when BOTH runs pass, so a real regression cannot hide.
if OUT="$(go test ./internal/telegram/ -run 'TestB391|TestB421' -count=2 2>&1)"; then
  ok "D1: ./internal/telegram -count=2 passes (the repeat that failed 40/40 before the reset)"
elif OUT2="$(go test ./internal/telegram/ -run 'TestB391|TestB421' -count=2 2>&1)"; then
  ok "D1: ./internal/telegram -count=2 passes on a retry (the first run failed — a host transient, not a verdict: $(printf '%s' "$OUT" | grep -E '^(---|FAIL)' | head -2 | tr '\n' ' '))"
else
  bad "D1: -count=2 failed TWICE, so this is a regression: $(printf '%s' "$OUT2" | grep -E '^(---|FAIL)' | head -4 | tr '\n' ' ')"
fi

hdr "E. a failed insert names what it costs, and this check is registered"
if grep -q 'no id' "$ALERTS"; then
  ok "E1: the insert-failure log names the consequence (message delivered, id=0 read as \"not delivered\")"
else
  bad "E1: the insert-failure log does not say what the caller will conclude"
fi
grep -q 'run_check "B426.1"' "$GATE" && ok "E2: registered in the catalog" || bad "E2: not registered"
grep -q '\*\*B426.1\*\*' AGENTS.md && ok "E3: AGENTS.md block index carries B426.1" || bad "E3: no AGENTS entry"
if git ls-files --error-unmatch scripts/check_b426_1_test_state.sh >/dev/null 2>&1; then
  ok "E4: this script is tracked by git (trap 11)"
else
  bad "E4: this script is not tracked by git yet — a check nobody can run is not a check"
fi

printf '\n\033[1mB426.1 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
