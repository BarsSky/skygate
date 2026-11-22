#!/usr/bin/env bash
# check_b512_relayless_claim_stability.sh — B512 (2026-11-22)
#
# THE WANDERING PREFIX, and the end of the "background wave" chain. Measured on the
# reference host after B511 made the noise visible:
#
#   * the auto-updater had created 94 rules in the last hour (284 in 24h), and one CDN
#     range was claimed by up to SEVEN rules with different parent domains;
#   * 16 enabled rules named NO relay at all (one user's CDN rule set), and their prefixes
#     were re-spread by the auto pass on every reconcile: `43.112.68.68/32` left sharlotta
#     for emilia with `claims=1` and NO failover bookkeeping;
#   * each such move rewrites the per-CIDR ACL pins → the policy is rewritten → headscale
#     fans a fresh netmap out to EVERY device. That is the wave a cellular client feels.
#
# The defect is a rule, not a query: a claim that names no relay means "the engine
# decides". The engine already decided — the stored owner IS that decision. Re-deciding it
# because "nobody named a relay" is a coin flip that costs every client a map update.
#
# What is pinned here:
#   A. a relay-less claim keeps a healthy existing owner (and says why in the source code)
#   B. it is stable across repeated passes (idempotent reconcile, not just once)
#   C. an UNHEALTHY owner still loses the prefix (the guard is not a latch)
#   D. an explicit claim still wins (the rules are not overruled)
#   E. registry: gate + AGENTS + release notes + docs

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

ENGINE=internal/prefixowner/prefixowner.go
TESTF=internal/prefixowner/assign_b512_test.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
TRBL=docs/troubleshooting.md

for f in "$ENGINE" "$TESTF" "$GATE" "$AGENTS" "$NOTES" "$TRBL"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B512 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# ── A. the guard exists and is where it belongs ─────────────────────────────
echo "[A] a relay-less claim does not evict a healthy owner"

case "$(cat "$ENGINE")" in
  *'if len(g.perNode) == 0 {'*)
    ok "A1: the reservation pass recognises a claim set that names no relay at all" ;;
  *)
    bad "A1: nothing distinguishes a relay-less claim from a claim for another relay" ;;
esac

# The guard must live INSIDE the reservation pass (after the owner/health checks), not in
# the auto pass — otherwise it would fight the placement of a genuinely new prefix.
GUARD_LINE="$(grep -n 'if len(g.perNode) == 0 {' "$ENGINE" | head -1 | cut -d: -f1)"
RESV_LINE="$(grep -n 'Pass 2 — the RESERVATION' "$ENGINE" | head -1 | cut -d: -f1)"
AUTO_LINE="$(grep -n 'Pass 3 — explicit' "$ENGINE" | head -1 | cut -d: -f1)"
if [ -n "$GUARD_LINE" ] && [ -n "$RESV_LINE" ] && [ -n "$AUTO_LINE" ] &&
   [ "$GUARD_LINE" -gt "$RESV_LINE" ] && [ "$GUARD_LINE" -lt "$AUTO_LINE" ]; then
  ok "A2: the guard sits inside the reservation pass (line $GUARD_LINE, between $RESV_LINE and $AUTO_LINE)"
else
  bad "A2: the guard is not in the reservation pass (guard=${GUARD_LINE:-none} reservation=${RESV_LINE:-none} auto=${AUTO_LINE:-none})"
fi

case "$(cat "$ENGINE")" in
  *'a claim that names NO relay is not a reason to move'*)
    ok "A3: the source records WHY (the measured move, and what it costs every client)" ;;
  *)
    bad "A3: the guard has no explanation — the next reader will remove it as redundant" ;;
esac

# ── B. behaviour ────────────────────────────────────────────────────────────
echo "[B] behaviour"

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/prefixowner/ -run 'TestB512' -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "B1: the B512 tests pass (keeper, stability, unhealthy owner, explicit claim)"
  else
    bad "B1: go test -run TestB512 failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
  # The guard must not have broken the assigner's existing contracts.
  OUT="$(go test ./internal/prefixowner/ -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "B2: the whole prefixowner suite is green (B275/B277/B398/B480/B484 assignments intact)"
  else
    bad "B2: go test ./internal/prefixowner/ failed: $(printf '%s' "$OUT" | tail -c 500)"
  fi
else
  skip "B1-B2: no Go toolchain on PATH"
fi

# ── C. the tests prove the two ways it must NOT latch ───────────────────────
echo "[C] the guard is not a latch"

case "$(cat "$TESTF")" in
  *'TestB512_UnhealthyOwnerStillMoves'*)
    ok "C1: a dead owner still loses the prefix (test present)" ;;
  *)
    bad "C1: no test that an unhealthy owner still moves" ;;
esac
case "$(cat "$TESTF")" in
  *'TestB512_ExplicitClaimStillBeatsTheExistingOwner'*)
    ok "C2: a rule naming a relay still wins (test present)" ;;
  *)
    bad "C2: no test that an explicit claim still decides" ;;
esac
case "$(cat "$TESTF")" in
  *'TestB512_RelaylessClaimIsStableAcrossPasses'*)
    ok "C3: stability is asserted across repeated passes, not just once" ;;
  *)
    bad "C3: nothing proves the reconcile is idempotent" ;;
esac

# ── D. registry ─────────────────────────────────────────────────────────────
echo "[D] registry"

case "$(cat "$GATE")" in
  *'run_check "B512"'*) ok "D1: registered in scripts/verify_pre_deploy.sh" ;;
  *) bad "D1: B512 is not registered in the gate" ;;
esac
case "$(cat "$AGENTS")" in
  *"**B512**"*) ok "D2: one-line entry in the AGENTS.md block index" ;;
  *) bad "D2: no AGENTS.md block-index entry" ;;
esac
case "$(cat "$NOTES")" in
  *"B512"*) ok "D3: the release notes explain the finding" ;;
  *) bad "D3: RELEASE-NOTES.md does not mention B512" ;;
esac
case "$(cat "$TRBL")" in
  *"B512"*) ok "D4: docs/troubleshooting.md carries the procedure" ;;
  *) bad "D4: no troubleshooting entry" ;;
esac

echo
echo "B512 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
