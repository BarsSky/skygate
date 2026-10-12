#!/usr/bin/env bash
# scripts/push_gate_verdict.sh <catalog-log>
#
# B378 (2026-10-12) — the ONE local reader of the guarantee catalog's verdict.
#
# WHY THIS EXISTS
# ---------------
# `scripts/verify_pre_deploy.sh` ends on its own verdict (`exit 1` on any FAIL or
# TIMEOUT — B335). `.githooks/pre-push` used to branch on that status alone, which is
# correct but mute: the operator was told «catalog RED — push ABORTED» and then had to
# scroll a 400-line wall to find WHICH check failed. The reason is already printed by
# the catalog (`=== summary ===` + `failing: B337`); this script reads it out of the
# captured log so the hook can name it, and so the `no verdict at all` case (a crash
# before the summary — a missing line-continuation backslash, a `set -u` abort) is a
# FAILURE rather than something a human has to notice.
#
# CI's `verify-pre` job parses the same rows inline in `.github/workflows/ci.yml`
# (that is what B281 pins, and it must keep working if this script is renamed), so
# this is the LOCAL implementation, not a second source of truth: both read the
# summary block the catalog itself prints.
#
# Exit codes: 0 green (0 FAIL, 0 TIMEOUT) / 1 red / 2 usage.
set -uo pipefail

LOG="${1:-}"
if [ -z "$LOG" ] || [ ! -f "$LOG" ]; then
  printf 'push-gate: usage: push_gate_verdict.sh <catalog-log> (got: %s)\n' "${LOG:-<none>}" >&2
  exit 2
fi

# ANSI-strip first: the catalog colours every row, and a coloured `FAIL` row does not
# match `^ *FAIL *:`.
CLEAN="$(sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' "$LOG" 2>/dev/null || cat "$LOG")"

count_of() {  # $1 = label, e.g. FAIL
  printf '%s\n' "$CLEAN" \
    | grep -E "^[[:space:]]*$1[[:space:]]*:" \
    | tail -n 1 \
    | grep -oE '[0-9]+' \
    | head -n 1
}

FAILN="$(count_of 'FAIL')"
TIMEOUTN="$(count_of 'TIMEOUT')"
PASSN="$(count_of 'PASS')"

if [ -z "$FAILN" ]; then
  # No summary block: the catalog never reached its verdict. That is a failure of the
  # GATE, not a pass — pre-B322/TD-22 this is exactly how a red run looked green.
  printf 'push-gate: RED — the catalog produced NO verdict (it crashed before "=== summary ===")\n'
  printf '%s\n' "$CLEAN" | grep -aE 'CATALOG ERROR|unbound variable|command not found' | head -n 3 | sed 's/^/          /'
  exit 1
fi

printf 'push-gate: %s passed, %s failed, %s timed out\n' "${PASSN:-?}" "$FAILN" "${TIMEOUTN:-0}"

if [ "$FAILN" -gt 0 ] || [ "${TIMEOUTN:-0}" -gt 0 ]; then
  FAILING="$(printf '%s\n' "$CLEAN" | grep -aE '^[[:space:]]*failing:' | tail -n 1)"
  if [ -n "$FAILING" ]; then
    printf 'push-gate: %s\n' "$(printf '%s' "$FAILING" | sed 's/^[[:space:]]*//')"
  else
    # Older catalogs did not print the names; at least say how to find them.
    printf 'push-gate: the failing check names are above (each has its own FAIL row and its own output)\n'
  fi
  exit 1
fi

exit 0
