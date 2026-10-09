#!/usr/bin/env bash
# check_b370_off_tailnet_is_not_a_relay_failure.sh — B370 (2026-10-09): an
# attempt that could not reach a relay because the PORTAL had no tailnet is not
# evidence about the relay; and the IPv6 jump rung must bracket its target once.
#
# THE MEASUREMENT (reference deployment, PostgreSQL, operator-visible)
# -------------------------------------------------------------------
# The operator updated the panel and then saw, on /admin/exit-nodes:
#
#   «Ретранслятор не удалось настроить — его префиксы переданы другому»
#   karolina — последняя попытка …, префиксов было у него 196:
#   tailnet 100.64.0.2:18022 is not answering: dial tcp …: i/o timeout; …
#
# Timeline from the container log (all UTC):
#   11:18:51  /admin/update RECREATED the container
#   [init]    TS_AUTHKEY_FILE not set — Tailscale skipped (non-RF mode)
#   11:19:47  the sync pass starts
#   11:19:52  exit-node sync(karolina): local daemon unreadable → SSH transport
#   11:19:57  … 11:20:07  EVERY rung fails: the direct tailnet dial times out,
#             the node name needs the container's DNS, and both peer hops
#             (100.64.0.3:22, 100.64.0.4:22) time out too — they are tailnet
#             addresses as well. One rung died differently:
#               Bad stdio forwarding specification '[[fd7a:115c:a1e0::2]]:18022'
#   11:19:59  tailscale-autostart: boot: up (hostname=skygate-host) — the app
#             brings tailscaled up ~70 s after start
#   11:20:38  emilia's turn in the SAME pass: routes applied over the tailnet
#
# So nothing was wrong with karolina: 90 s later the same pass configured emilia
# over the tailnet, a live TCP probe of 100.64.0.2:18022 answers 5/5 in 135 ms,
# and a manual Re-sync succeeded on the FIRST rung. The panel, however, had
# already recorded `relay_apply_state:karolina = <err>`; B309/B352 then excluded
# her from the healthy set, moved her 196 prefixes to another relay and painted
# the red banner — and only B361's safety net kept the two devices pinned to her
# tag online (it withheld their pin, so their egress stayed unpinned).
#
# Two defects, two fixes:
#   1. the portal's own missing tailnet is not a fact about a relay (this is the
#      B362 lesson applied to the transport ladder) → the outcome is marked
#      NOT-EVIDENCE, the previous relay state stands (no demotion, no prefix
#      migration) and the reason is logged;
#   2. `%h` carries the BRACKETS of an IPv6 target, so `-W '[%h]:%p'` rendered
#      `[[fd7a:…]]:18022` → the spec is built from the same host/port the outer
#      ssh is given, bracketed exactly once.
#
# WHAT THIS SCRIPT PINS
#   A. the not-evidence predicate exists, is used at the ladder's failure point,
#      and the state store refuses to record such an outcome — while a real
#      failure and an empty candidate list stay evidence;
#   B. the forwarded spec brackets an IPv6 target exactly once and follows the
#      same host/port the outer ssh is given;
#   C. the behavioural halves run;
#   D. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs a tool this host lacks.
#
# Usage:  bash scripts/check_b370_off_tailnet_is_not_a_relay_failure.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

TAILNET=internal/feature/exit_rules/relay_transport_tailnet_b310.go
B309=internal/feature/exit_rules/relay_transport_b309.go
ROUTES_SYNC=internal/feature/exit_rules/sync_routes.go
ROUTES=internal/headscale/routes.go
T_TAILNET=internal/feature/exit_rules/relay_apply_not_evidence_b370_test.go
T_ROUTES=internal/headscale/routes_b370_test.go
SELF=scripts/check_b370_off_tailnet_is_not_a_relay_failure.sh

for f in "$TAILNET" "$B309" "$ROUTES_SYNC" "$ROUTES" "$T_TAILNET" "$T_ROUTES"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B370 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# =====================================================================
hdr "A. an attempt the portal could not make is not recorded as a relay failure"

if grep -q 'func AllCandidatesNeedThePortalTailnet' "$TAILNET"; then
  ok "A1: the predicate exists (every candidate — and every jump hop — needs the portal's tailnet)"
else
  bad "A1: AllCandidatesNeedThePortalTailnet is gone — nothing distinguishes 'the relay is broken' from 'we had no tailnet'"
fi
if grep -q '!state.Ready && AllCandidatesNeedThePortalTailnet(cands)' "$ROUTES_SYNC"; then
  ok "A2: the ladder's failure point marks such an outcome not-evidence"
else
  bad "A2: the failure point records every failure as evidence again"
fi
if grep -q 'if out.NotEvidence {' "$B309" && grep -q 'NOT recorded as a failure' "$B309"; then
  ok "A3: recordRelayApply refuses to overwrite the relay state and says why"
else
  bad "A3: the state store would demote a relay for the portal's own missing tailnet"
fi
# The predicate must not swallow real evidence: an empty candidate list is a
# configuration problem, and that IS about the relay.
if grep -q 'if len(cands) == 0 {' "$TAILNET"; then
  ok "A4: an EMPTY candidate list is not treated as 'all tailnet' (a misconfiguration stays evidence)"
else
  bad "A4: the predicate treats 'no candidates at all' as not-evidence"
fi

# =====================================================================
hdr "B. the forwarded spec brackets an IPv6 target exactly once"

if grep -q 'func ForwardSpec' "$ROUTES"; then
  ok "B1: ForwardSpec owns the -W host:port token"
else
  bad "B1: ForwardSpec is gone — the spec would go back to ssh's %h expansion"
fi
if grep -q "strings.Contains(h, \":\")" "$ROUTES" && grep -q 'h = "\[" + h + "\]"' "$ROUTES"; then
  ok "B2: it strips any existing brackets and re-brackets an IPv6 literal"
else
  bad "B2: the spec's bracketing is not normalised"
fi
if grep -v '^[[:space:]]*//' "$ROUTES" | grep -q '%h'; then
  bad "B3: a CODE line still expands ssh's %h into the forwarded spec — an IPv6 target renders [[addr]]:port and ssh refuses the rung (comments may explain the history, code may not use it)"
else
  ok "B3: no code line expands ssh's %h into the forwarded spec"
fi
if grep -q 'jumpProxyCommand(keyPath, jump, host, port)' "$ROUTES"; then
  ok "B4: the tunnel is built from the SAME host/port the outer ssh is given"
else
  bad "B4: the tunnel no longer follows the outer target"
fi

# =====================================================================
hdr "C. the behavioural halves run"

if grep -q 'func TestB370_NotEvidenceDoesNotOverwriteTheRelayState' "$T_TAILNET" \
   && grep -q 'func TestB370_ARealFailureIsStillRecorded' "$T_TAILNET"; then
  ok "C1: both directions are pinned (not-evidence keeps the state; a real failure still demotes)"
else
  bad "C1: the recordRelayApply regressions are missing"
fi
if grep -q 'func TestB370_ForwardSpecBracketsIPv6ExactlyOnce' "$T_ROUTES" \
   && grep -q 'func TestB370_JumpArgvCarriesASingleBracketedSpec' "$T_ROUTES"; then
  ok "C2: the IPv6 spec regressions are present"
else
  bad "C2: the ForwardSpec regressions are missing"
fi

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "C3: no go binary in PATH — the behavioural contracts were not run"
else
  if out="$("$GOBIN" test ./internal/feature/exit_rules/ ./internal/headscale/ -run 'B370' -count=1 2>&1)"; then
    ok "C3: the B370 contracts pass"
  else
    bad "C3: go test -run B370 failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
fi

# =====================================================================
hdr "D. tracked by git (AGENTS trap #11), registered, indexed"

UNTRACKED=""
for f in "$SELF" "$T_TAILNET" "$T_ROUTES"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || UNTRACKED="${UNTRACKED}${f} "
done
if [ -z "$UNTRACKED" ]; then
  ok "D1: every new file of this block is tracked by git"
else
  skip "D1: not yet tracked by git (the lead commits this block): ${UNTRACKED}"
fi
if grep -q 'run_check "B370"' scripts/verify_pre_deploy.sh; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B370\*\*' AGENTS.md; then
  ok "D3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "D3: AGENTS.md has no B370 bullet at line start"
fi

printf '\n\033[1mB370 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
