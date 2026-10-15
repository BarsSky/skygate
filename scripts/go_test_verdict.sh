#!/usr/bin/env bash
# scripts/go_test_verdict.sh — read `go test` output and say WHAT went wrong.
#
# B378 (2026-10-12) — the local gate must distinguish "a test hung" from "an
# assertion failed", and must name the hanging test.
#
# WHY THIS EXISTS (measured, not theorised)
# -----------------------------------------
# During the v1.5.117 checked push, `B132` contract G ran
#   go test -count=1 ./internal/feature/exit_rules/ ./internal/feature/admin/ ./internal/i18n/
# with NO explicit -timeout, so Go's own 10-minute default applied. The package
# `internal/feature/exit_rules` was killed at **601.705s** and the check reported only
# the last lines of output — which happened to be two informational log lines:
#
#   FAIL  go test FAILED: 2026/10/13 07:27:44 all-devices: propagated 1 rule row(s) …
#   FAIL  skygate/internal/feature/exit_rules 601.705s
#
# Nothing in that message says the run TIMED OUT, nothing names the test, and the
# number 601.705 s had to be recognised by hand as «Go's default budget». Re-running
# the same tree reproduced it, and `-timeout 120s` produced the dump that identified
# the real cause:
#
#   panic: test timed out after 2m0s
#     running tests:
#       TestB2761_MarkGroupKeepsTheIntent (7s)
#   … skygate/internal/db.addColumnIfMissingSQLite → ExecContext → modernc.org/sqlite
#     → _winSync → syscall.FlushFileBuffers          <-- blocked in a Windows fsync
#
# i.e. a HOST filesystem stall inside a test's SQLite migration (the same test passes
# in 2.6 s on the same host minutes later; the three packages together take ~283 s on a
# quiet machine and the whole suite is fsync-heavy). That is a host condition, but the
# MESSAGE was a product-shaped lie: «go test FAILED» about a suite that never asserted
# anything wrong. A gate that cannot tell a hang from a wrong assertion sends the
# operator to the wrong place — the class B268/B327/B335 all paid for.
#
# USAGE
#   out="$(go test ... 2>&1)"; rc=$?
#   verdict="$(printf '%s' "$out" | bash scripts/go_test_verdict.sh)"; vrc=$?
# The verdict is one actionable line; the caller decides what to do with rc:
#   0 = the run is green
#   1 = an assertion failed (the line names the first failing test)
#   2 = the run HUNG (the line names the test and the blocking frame)
#   3 = no verdict could be read (the output has neither ok nor FAIL — e.g. a build error)
set -uo pipefail

OUT="$(cat)"

verdict_line() {
  # The first assertion failure, if any.
  printf '%s\n' "$OUT" | grep -aE '^--- FAIL|^[[:space:]]+--- FAIL' | head -n 1 | sed 's/^[[:space:]]*//'
}

if grep -qaE 'panic: test timed out' <<< "$OUT"; then
  BUDGET="$(printf '%s\n' "$OUT" | grep -aoE 'test timed out after [0-9a-zA-Z.]+' | head -n 1)"
  # Go prints the tests that were running at the alarm — that is the answer to «which
  # test hangs», and it is the one thing the old message never carried.
  RUNNING="$(printf '%s\n' "$OUT" | awk '/running tests:/{flag=1;next} flag&&/^\t/{gsub(/^[ \t]+/,"");print;next} flag{exit}' | head -n 3 | tr '\n' ' ')"
  # The most actionable blocking frame: OUR code if the stack reaches it (that is the
  # line an operator can act on), otherwise the first frame outside the runtime
  # machinery — on the measured incident that is modernc.org/sqlite's `_winSync`.
  FRAME="$(printf '%s\n' "$OUT" | grep -aE '^[a-zA-Z0-9_./-]+\.[A-Za-z0-9_(]' | grep -aE '^skygate/' | head -n 1 | sed 's/[[:space:]]*$//')"
  [ -z "$FRAME" ] && FRAME="$(printf '%s\n' "$OUT" | grep -aE '^[a-zA-Z0-9_./-]+\.[A-Za-z0-9_(]' | grep -avE '^(testing|runtime|main|syscall|time|os)\.' | head -n 1 | sed 's/[[:space:]]*$//')"
  printf 'HUNG: %s — the test never finished (this is NOT a failed assertion)\n' "${BUDGET:-go test timed out}"
  [ -n "$RUNNING" ] && printf 'HUNG: running test(s) at the alarm: %s\n' "$RUNNING"
  [ -n "$FRAME" ] && printf 'HUNG: first blocking frame: %s\n' "$FRAME"
  # B382: the budget is a number the caller CHOSE (B132 passes an explicit
  # -timeout since v1.5.121), so a hang is no longer only Go's implicit default.
  printf 'HUNG: that budget is the caller-supplied -timeout (B132: SKYGATE_GO_TEST_TIMEOUT, default 10m per package)\n'
  printf 'HUNG: re-run with -v and a short -timeout to see it live; a host filesystem stall (FlushFileBuffers under modernc.org/sqlite) is the measured cause on Windows\n'
  exit 2
fi

if ( set +o pipefail; printf '%s\n' "$OUT" | grep -qaE '^ok[[:space:]]|^ok$' ); then
  printf 'OK: every package reported ok\n'
  exit 0
fi

FAILED="$(verdict_line)"
if grep -qaE '^FAIL|^--- FAIL' <<< "$OUT"; then
  printf 'FAILED: %s\n' "${FAILED:-go test reported FAIL (no "--- FAIL" line: read the output above)}"
  exit 1
fi

printf 'NO VERDICT: the output carries neither "ok" nor "FAIL" — a build or setup error, not a test result\n'
printf '%s\n' "$OUT" | grep -aE 'cannot|undefined|error|no such' | head -n 3 | sed 's/^/    /'
exit 3
