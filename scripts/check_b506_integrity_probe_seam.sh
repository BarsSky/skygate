#!/usr/bin/env bash
# check_b506_integrity_probe_seam.sh — B506 (2026-11-20)
#
# MEASURED while running the gate for B505 on the maintainer's Windows tree:
#
#	--- FAIL: TestB466_UnmeasuredIntegrityIsNotAFailure
#	    integrity status = "ok", want unmeasured (budget was 1ns)
#
# The same test passes in CI (ubuntu), and it FAILED IDENTICALLY on a clean
# checkout of v1.5.249 — so this is not a regression, it is a test whose verdict
# depends on the host. The test's premise was "a 1 ns budget forces the integrity
# probe to time out"; on this host an in-memory `PRAGMA quick_check` answers before
# the deadline is enforced, so the probe reports "ok" and the assertion fails.
#
# Two honest options: weaken the assertion (which would stop testing the property
# that matters — "a probe that could not finish is NOT a database finding"), or let
# the test MODEL a probe that cannot finish. The second one is what a seam is for:
# the sampler gained `integrityProbe func(ctx, *sql.DB) (string, error)`, nil meaning
# the real statement, and the test injects a function that waits for the deadline.
# Production code path is unchanged; the contract is now deterministic on every host.
#
# Contracts:
#   A. the seam exists and defaults to the real `PRAGMA quick_check`
#   B. the budget path is exercised through the seam, not through a timing assumption
#   C. the product path still runs a real statement (the seam cannot silently replace it)
#   D. behaviour: the B466 tests are green repeatedly (they were flaky, so count=3)
#   E. discoverable: registered, tracked, indexed, roadmap, release notes

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

IMPL=internal/feature/healthz/db_health.go
TEST=internal/feature/healthz/db_health_b466_test.go
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
GATE=scripts/verify_pre_deploy.sh

for f in "$IMPL" "$TEST" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B506 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

IMPL_TXT="$(cat "$IMPL")"
TEST_TXT="$(cat "$TEST")"

# ── A. the seam ─────────────────────────────────────────────────────────────
case "$IMPL_TXT" in
  *'integrityProbe func(ctx context.Context, d *sql.DB) (answer string, err error)'*)
    ok "A1: the sampler carries an injectable integrity probe" ;;
  *) bad "A1: no integrityProbe seam on the Sampler" ;;
esac
case "$IMPL_TXT" in
  *'func probeIntegrityDefault(ctx context.Context, d *sql.DB) (string, error)'*)
    ok "A2: the default probe is a named function" ;;
  *) bad "A2: no probeIntegrityDefault" ;;
esac
case "$IMPL_TXT" in
  *'if probe == nil {'*) ok "A3: nil means the real statement (production path unchanged)" ;;
  *) bad "A3: the seam does not fall back to the real probe" ;;
esac
case "$IMPL_TXT" in
  *'`PRAGMA quick_check`'*) ok "A4: the real statement is still PRAGMA quick_check" ;;
  *) bad "A4: the production statement changed" ;;
esac

# ── B. the flake itself is gone ─────────────────────────────────────────────
case "$TEST_TXT" in
  *'s.integrityProbe = func(ctx context.Context'*) ok "B1: the budget test injects a probe that cannot finish" ;;
  *) bad "B1: the test still assumes a 1 ns budget forces a timeout" ;;
esac
case "$TEST_TXT" in
  *'<-ctx.Done()'*) ok "B2: the injected probe waits for the deadline instead of guessing" ;;
  *) bad "B2: the injected probe does not model a blocked probe" ;;
esac
case "$TEST_TXT" in
  *'while CI passed'*|*'passed. Modelling'*|*'B506'*)
    ok "B3: the test records why the seam exists (a future reader would delete it)" ;;
  *) bad "B3: the reason for the seam is not written down in the test" ;;
esac
# The other B466 tests must keep driving the REAL probe.
case "$(grep -c 'integrityProbe = ' "$TEST" 2>/dev/null)" in
  1) ok "B4: exactly one test replaces the probe (the others still use the real one)" ;;
  *) bad "B4: $(grep -c 'integrityProbe = ' "$TEST" 2>/dev/null) test(s) replace the probe — the measured paths would be unexercised" ;;
esac

# ── C. production still measures ────────────────────────────────────────────
case "$IMPL_TXT" in
  *'answer, err := probe(qc, db)'*) ok "C1: the probe runs under the IntegrityTimeout context" ;;
  *) bad "C1: the probe call lost its context" ;;
esac
case "$IMPL_TXT" in
  *'status = "failed"'*) ok "C2: an ANSWERED non-ok is still a finding" ;;
  *) bad "C2: the failed verdict is gone" ;;
esac
case "$IMPL_TXT" in
  *'status = "unmeasured"'*) ok "C3: an unfinished probe is still 'unmeasured', never a failure" ;;
  *) bad "C3: an unfinished probe would now read as a database error" ;;
esac

# ── D. behaviour ────────────────────────────────────────────────────────────
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/healthz/ -run 'B466' -count=3 2>&1)"
  if grep -qE '^(ok|PASS)' <<< "$OUT"; then
    ok "D1: the B466 tests are green 3× in a row (they were flaky, so one run proves nothing)"
  else
    bad "D1: go test -run B466 -count=3 failed:
$(printf '%s\n' "$OUT" | tail -20)"
  fi
else
  skip "D1: no Go toolchain on this host"
fi

# ── E. discoverable ─────────────────────────────────────────────────────────
if grep -q 'check_b506_integrity_probe_seam.sh' "$GATE"; then
  ok "E1: registered in the pre-deploy catalog"
else
  bad "E1: not registered in $GATE"
fi
if git ls-files --error-unmatch scripts/check_b506_integrity_probe_seam.sh >/dev/null 2>&1; then
  ok "E2: git tracks this script (trap #11)"
else
  bad "E2: this script is not tracked by git (trap #11)"
fi
if grep -q 'B506' "$AGENTS"; then
  ok "E3: the AGENTS block index carries its line"
else
  bad "E3: no B506 line in $AGENTS"
fi
for rd in docs/ROADMAP.md docs/ru/ROADMAP.md; do
  if grep -q 'B506' "$rd"; then
    ok "E4: $rd records the block"
  else
    bad "E4: $rd does not mention B506"
  fi
done
if grep -q 'B506' "$NOTES"; then
  ok "E5: the release notes mention it"
else
  bad "E5: no B506 mention in $NOTES"
fi

echo
echo "B506 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
