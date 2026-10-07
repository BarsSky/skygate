#!/usr/bin/env bash
# check_b362_expired_budget_is_not_a_relay_failure.sh — B362 (2026-10-07).
#
# WHY THIS EXISTS. v1.5.103 shipped the Telegram relay fallback (B356.1) and the
# operator applied it. The live log at 20:32 showed the ladder working as designed and
# the fallback never carrying a single request:
#
#   telegram: setMyCommands lang="" failed: … (context deadline exceeded) and no relay
#     could carry it (tried emilia, karolina, sharlotta): relay sharlotta (root@…):
#     context deadline exceeded
#   telegram: getUpdates error: … (dial tcp 149.154.166.110:443: i/o timeout) and the
#     relay fallback is unavailable: every relay is cooling down after a failure
#   …two minutes of that…
#   telegram egress: the direct path to the Bot API works again
#
# Three setMyCommands calls carry a 5-second context (their own budget), while the
# bounded direct dial alone is allowed 6 seconds — so the first call spent its whole
# budget and the relay attempts came back context deadline exceeded. The code then
# cooled EVERY relay down for RelayCooldown (2 minutes), and the 30-second getUpdates
# loop could not even TRY a relay until the direct path happened to recover. Measured
# by hand inside the same container at the same moment, the identical argv
# (`ssh -i /ssh-sync/skygate_sync … -W api.telegram.org:443 -- <relay>`) completed in
# two seconds against karolina and sharlotta: the relays were fine, OUR clock was not.
#
# THE CONTRACT: a deadline that expires on our side is not evidence about a relay. Only
# a tunnel error raised while the caller still has budget may put a relay in cooldown.
#
# Sections: A the classifier; B the call site; C the behaviour test; D the ratchets;
# E bookkeeping. House style: PASS/FAIL/SKIP, non-zero exit only on a real failure, no
# fixed /tmp path.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

PASS=0; FAIL=0; SKIP=0
ok()   { PASS=$((PASS+1)); printf '\033[32mPASS\033[0m %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf '\033[31mFAIL\033[0m %s\n' "$*"; }
skip() { SKIP=$((SKIP+1)); printf '\033[33mSKIP\033[0m %s\n' "$*"; }

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

EGRESS="internal/telegram/egress_fallback.go"
TESTFILE="internal/telegram/egress_fallback_test.go"

echo "=== A. the classifier exists and answers the right way ==="
if grep -q '^func relayFailureIsEvidence(' "$EGRESS"; then
  ok "A1: relayFailureIsEvidence is defined in $EGRESS"
else
  bad "A1: relayFailureIsEvidence is missing — nothing decides whether a failure is evidence about the RELAY"
fi

CLASSIFIER="$(awk '/^func relayFailureIsEvidence\(/,/^}/' "$EGRESS" 2>/dev/null)"
if grep -q 'context.Canceled' <<< "$CLASSIFIER"; then
  ok "A2: a CANCELLED context is not evidence about the relay"
else
  bad "A2: the classifier does not treat a cancelled context as our own failure"
fi
if grep -q 'ctx.Err() != nil' <<< "$CLASSIFIER"; then
  ok "A3: an EXPIRED caller context is not evidence about the relay"
else
  bad "A3: the classifier does not check the caller context — an expired budget would still cool the relay down"
fi
if grep -q 'return true' <<< "$CLASSIFIER"; then
  ok "A4: a tunnel error raised while the caller still has budget IS evidence (the rule is not 'never cool down')"
else
  bad "A4: the classifier never returns true — a genuinely broken relay would never be cooled down"
fi

echo
echo "=== B. the call site asks before it cools down ==="
if grep -q 'if relayFailureIsEvidence(c, areq.Context(), err)' "$EGRESS"; then
  ok "B1: the relay loop consults the classifier instead of calling noteRelayFailure unconditionally"
else
  bad "B1: the relay loop still cools every relay down on any error (the live defect)"
fi
if awk '/func \(t \*egressTransport\) RoundTrip/,/^}/' "$EGRESS" | grep -q 'noteNoRelayFailure'; then
  ok "B2: a failure that is NOT evidence is recorded without touching the cooldown"
else
  bad "B2: the not-evidence branch does not record the failure at all — the operator would lose the reason"
fi
if awk '/func \(t \*egressTransport\) RoundTrip/,/^}/' "$EGRESS" | grep -q 'break'; then
  ok "B3: the loop stops once the caller's clock is gone instead of spending the remaining relays on a dead context"
else
  skip "B3: no early break found — behaviour is still bounded, but it wastes the remaining attempts"
fi

echo
echo "=== C. the behaviour is pinned by a test, not only by this grep ==="
if grep -q '^func TestB362_ExpiredCallerBudgetDoesNotCoolTheRelayDown(' "$TESTFILE"; then
  ok "C1: the B362 test exists"
else
  bad "C1: no test pins the property — a future refactor can reintroduce the cooldown poisoning silently"
fi
if grep -q 'len(sel) != 2' "$TESTFILE" && grep -q 'len(left) != 0' "$TESTFILE"; then
  ok "C2: the test asserts BOTH directions (our expired budget keeps the relays; a refused tunnel cools one)"
else
  bad "C2: the test does not assert both directions — one half of the rule would be unpinned"
fi

echo
echo "=== D. the neighbours this could break still hold ==="
GO_BIN="$(command -v go || true)"
if [ -z "$GO_BIN" ] && [ -x "$(command -v go.exe 2>/dev/null || true)" ]; then GO_BIN="$(command -v go.exe)"; fi
if [ -n "$GO_BIN" ]; then
  OUT="$("$GO_BIN" test -count=1 -run 'TestB356_1|TestB362' ./internal/telegram/ 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "D1: the B356.1 ladder tests and the new B362 test pass together"
  else
    bad "D1: the telegram egress tests failed: $(head -3 <<< "$OUT")"
  fi
else
  skip "D1: no Go toolchain on PATH — run this check where go test works (the VM or CI)"
fi

echo
echo "=== E. bookkeeping (the catalog registration is the lead's handoff) ==="
if grep -q 'check_b362_expired_budget_is_not_a_relay_failure.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  if grep -q '\*\*B362\*\*' AGENTS.md; then
    if git ls-files --error-unmatch scripts/check_b362_expired_budget_is_not_a_relay_failure.sh >/dev/null 2>&1; then
      ok "E1: registered in the catalog, indexed in AGENTS.md, tracked by git (trap #11)"
    else
      bad "E1: this script is NOT tracked by git (AGENTS trap #11)"
    fi
  else
    bad "E1: AGENTS.md has no B362 bullet"
  fi
else
  skip "E1: not registered in verify_pre_deploy.sh yet — the lead adds the B362 line and the AGENTS.md bullet"
fi

echo
echo "=== B362 summary: $PASS passed, $FAIL failed, $SKIP skipped ==="
[ "$FAIL" -gt 0 ] && exit 1
exit 0
