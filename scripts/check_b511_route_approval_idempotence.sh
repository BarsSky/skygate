#!/usr/bin/env bash
# check_b511_route_approval_idempotence.sh — B511 (2026-11-22)
#
# THE BACKGROUND WAVE. On the reference host, over 24 hours: 36 ACL re-applies, almost all
# of them "prefix ownership changed (inserted=N changed=M)" with N between 1 and 48, and the
# generated policy shrinking 97393 → 90585 bytes — so the writes followed REAL ownership
# moves, not phantom diffs. A diff of prefix_owner across one reconcile tick showed
# added=0 removed=0 with every owner unchanged while updated_at moved on 311 of 316 rows,
# i.e. the moves arrive in BURSTS and the counts alone cannot say which prefixes moved.
#
# Two things were wrong, and both are pinned here:
#
#   1. The APPROVAL step ran on every pass unconditionally. Approving routes is a headscale
#      state change, so re-approving an unchanged set makes headscale fan a fresh netmap out
#      to every device — invisible on Wi-Fi, felt as a stall on a cellular stream. It now
#      asks headscale what the relay already has and does nothing when it already has it.
#   2. A count is not a finding: "inserted=9" never named the nine. The reconcile now logs a
#      bounded sample (prefix → new owner, old owner) plus the healthy set.
#
# What must NOT regress: a REAL move still approves. Only "nothing changed" is skipped, and
# a relay headscale cannot answer for is treated as "unknown", never as "nothing changed".

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

HELP=internal/feature/exit_rules/route_idempotence_b511.go
TESTF=internal/feature/exit_rules/route_idempotence_b511_test.go
ROUTES=internal/feature/exit_rules/sync_routes.go
ACL=internal/feature/exit_rules/sync_acl.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
TRBL=docs/troubleshooting.md

for f in "$HELP" "$TESTF" "$ROUTES" "$ACL" "$GATE" "$AGENTS" "$NOTES" "$TRBL"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B511 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# ── A. the approval step ASKS FIRST ─────────────────────────────────────────
echo "[A] approval only when the set changed"

case "$(cat "$ROUTES")" in
  *'relayRouteState(hs, node)'*)
    ok "A1: the approval step reads what the relay already has" ;;
  *)
    bad "A1: nothing asks headscale what is already approved — every pass re-approves" ;;
esac
case "$(cat "$ROUTES")" in
  *'routesAlreadyApproved(alreadyApproved, approveRoutes)'*)
    ok "A2: the decision is made on the live approved set, not on our own bookkeeping" ;;
  *)
    bad "A2: no comparison against the live approved set" ;;
esac
case "$(cat "$ROUTES")" in
  *'approve=unchanged'*)
    ok "A3: a skipped approval says so (an invisible skip is the class this project keeps paying for)" ;;
  *)
    bad "A3: the skip is silent — nobody could tell an unchanged pass from a broken one" ;;
esac
# The guard must come BEFORE the unconditional call, or it gates nothing.
GUARD_LINE="$(grep -n 'routesAlreadyApproved(alreadyApproved, approveRoutes)' "$ROUTES" | head -1 | cut -d: -f1)"
CALL_LINE="$(grep -n 'ApproveAllRoutesWithList(node, approveRoutes)' "$ROUTES" | head -1 | cut -d: -f1)"
if [ -n "$GUARD_LINE" ] && [ -n "$CALL_LINE" ] && [ "$GUARD_LINE" -lt "$CALL_LINE" ]; then
  ok "A4: the guard is above the call (line $GUARD_LINE < $CALL_LINE) — it actually gates it"
else
  bad "A4: the guard does not precede the approval call (guard=${GUARD_LINE:-none} call=${CALL_LINE:-none})"
fi

# ── B. UNKNOWN is not "unchanged" ───────────────────────────────────────────
echo "[B] a missing answer never skips the work"

case "$(cat "$ROUTES")" in
  *'known && routesAlreadyApproved'*)
    ok "B1: the skip requires headscale to have ANSWERED (known==true)" ;;
  *)
    bad "B1: an unreadable headscale could skip an approval it should have done" ;;
esac
case "$(cat "$HELP")" in
  *'return nil, nil, false'*)
    ok "B2: relayRouteState reports unknown on error instead of an empty (== unchanged) set" ;;
  *)
    bad "B2: a read failure would look like 'no routes' and be treated as unchanged" ;;
esac
case "$(cat "$HELP")" in
  *'if len(normalizeRouteSet(want)) == 0'*)
    ok "B3: an empty target is never 'already approved' (the base routes still need approving)" ;;
  *)
    bad "B3: an empty target could read as approved and skip the base routes" ;;
esac

# ── C. a count is not a finding ─────────────────────────────────────────────
echo "[C] the reconcile NAMES what moved"

case "$(cat "$ACL")" in
  *'ownershipChangeSample(before, prefixowner.OwnerByPrefix(s.dbc()), 6)'*)
    ok "C1: the reconcile logs a bounded sample of the prefixes that moved" ;;
  *)
    bad "C1: the reconcile still reports only a count — 'inserted=9' names nothing" ;;
esac
case "$(cat "$ACL")" in
  *'healthy relays:'*)
    ok "C2: the sample carries the healthy set (a move is decided by it)" ;;
  *)
    bad "C2: the log omits the healthy set, so a move cannot be explained from one line" ;;
esac
case "$(cat "$HELP")" in
  *'func ownershipChangeSample'*'limit int'*)
    ok "C3: the sample is bounded (a 48-prefix burst must not become a 48-item line)" ;;
  *)
    bad "C3: the sample has no limit" ;;
esac

# ── D. behaviour ────────────────────────────────────────────────────────────
echo "[D] behaviour"

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ -run 'TestB511' -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "D1: the B511 unit tests pass (set semantics, conservative approval, named sample)"
  else
    bad "D1: go test -run TestB511 failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
else
  skip "D1: no Go toolchain on PATH"
fi

# ── E. registry ─────────────────────────────────────────────────────────────
echo "[E] registry"

case "$(cat "$GATE")" in
  *'run_check "B511"'*) ok "E1: registered in scripts/verify_pre_deploy.sh" ;;
  *) bad "E1: B511 is not registered in the gate" ;;
esac
case "$(cat "$AGENTS")" in
  *"**B511**"*) ok "E2: one-line entry in the AGENTS.md block index" ;;
  *) bad "E2: no AGENTS.md block-index entry" ;;
esac
case "$(cat "$NOTES")" in
  *"approve=unchanged"*|*"B511"*) ok "E3: the release notes explain the change" ;;
  *) bad "E3: RELEASE-NOTES.md does not mention B511" ;;
esac
case "$(cat "$TRBL")" in
  *"B511"*) ok "E4: docs/troubleshooting.md carries the procedure" ;;
  *) bad "E4: no troubleshooting entry for this class" ;;
esac

echo
echo "B511 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
