#!/usr/bin/env bash
# check_b353_transport_jump_fallback.sh
#
# 2026-10-06 (B353) — the tailnet path is not automatically a path: a peer relay
# must be able to carry the management connection.
#
# The live measurement that produced this block (reference deployment):
#
#   container: tailscale ping 100.64.0.3      → timed out
#   container: tailscale ping 100.64.0.2 / .4 → pong (129 ms / 130 ms)
#   container: emilia relay="hel";  derp28b/c/d.tailscale.com → i/o timeout
#   emilia:    derp28b/c/d.tailscale.com → OPEN, hel 13.4 ms
#   emilia:    peer skygate-host online=true relay=waw cur='' lastHS=never
#   karolina:  nc -z 100.64.0.3 22 → OPEN ; ssh root@100.64.0.3 → hon-brown.ptr.network
#   A/B:       emilia homed at waw → ping 85 ms, TCP22 OPEN, ssh OK
#              emilia homed at hel → ping timeout, TCP22 blocked, ssh timeout
#
# So both hosts were on the tailnet and the portal still had no route to a healthy
# relay, because emilia's only DERP home is a region this site's network cannot
# reach. The B310 ladder's candidates were all direct, so nothing could recover.
#
# CONTRACTS
#   A. the jump rung exists, is last, and is selected from the relay table
#   B. a jump candidate is proved by its HOP, and the argv is honest about it
#   C. the ssh argv keeps every B266 property on the direct path and refuses an
#      option-injecting hop
#   D. the B300 property (one ssh tail) survives
#   E. bookkeeping
#
# SKIPs when the Go toolchain is unavailable (never FAIL).

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B353: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

JUMP=internal/feature/exit_rules/relay_transport_jump_b353.go
LADDER=internal/feature/exit_rules/relay_transport_tailnet_b310.go
SYNC=internal/feature/exit_rules/sync.go
ROUTES=internal/headscale/routes.go
JTESTS=internal/feature/exit_rules/relay_transport_jump_b353_test.go
RTESTS=internal/headscale/routes_b353_test.go

hdr "B353 — a peer relay can carry the connection the portal cannot make"

# --- A: the rung exists, comes last, and comes from the relay table -------------
if [ -f "$JUMP" ]; then
  ok "A1: relay_transport_jump_b353.go exists"
else
  bad "A1: the jump transport file is missing"
fi

if grep -q 'RelayEndpointJump = "tailnet-jump"' "$JUMP"; then
  ok "A2: the transport kind is recorded as tailnet-jump (both ends are visible on the page)"
else
  bad "A2: the jump kind constant is missing or renamed without updating the record"
fi

if grep -q 'func selectJumpHops(' "$JUMP" && grep -q 'func appendJumpCandidates(' "$JUMP"; then
  ok "A3: the hop selection and the candidate builder are separate, testable units"
else
  bad "A3: selectJumpHops/appendJumpCandidates are missing"
fi

# The hop must never be the target itself, nor one of the target's addresses.
if grep -q 'skip\[strings.ToLower(name)\]' "$JUMP" && grep -q 'skip\[strings.ToLower(hopHost)\]' "$JUMP"; then
  ok "A4: the target (by name AND by address) is excluded as a hop"
else
  bad "A4: a relay can be asked to hop to itself — the exclusion is gone"
fi

if grep -q 'FROM exit_servers' "$JUMP" && grep -q 'relay_apply_state' "$JUMP"; then
  ok "A5: hops come from exit_servers, and provenness from the B309 apply record"
else
  bad "A5: the hop source or the proven evidence is missing"
fi

# The rung is appended AFTER the direct candidates: a healthy relay must not pay for
# a hop (probe + ssh) on every tick.
if grep -q 'appendJumpCandidates(d, hs, cfg, node, cands)' "$SYNC"; then
  ok "A6: the shared transport tail appends the jump rung"
else
  bad "A6: applyRoutesToRelay does not append jump candidates"
fi

if grep -q 'maxJumpHops' "$JUMP"; then
  ok "A7: the number of hops per unreachable target is bounded, and the truncation is logged"
else
  bad "A7: unbounded hop attempts — one broken relay would multiply the sync cost"
fi

if grep -q 'SkygateTailnetState().Ready' "$JUMP"; then
  ok "A8: a portal that is itself off the tailnet is named in the failure notes"
else
  bad "A8: the 'portal is not on the tailnet' case is no longer explained"
fi

# --- B: the hop is what gets probed -------------------------------------------
if grep -q 'func (e RelayEndpoint) ProbeEndpoint()' "$LADDER"; then
  ok "B1: RelayEndpoint knows which endpoint must answer before it is used"
else
  bad "B1: ProbeEndpoint is missing — a jump candidate would probe the target it cannot reach"
fi

if grep -q 'probe := ep.ProbeEndpoint()' "$LADDER" && grep -qE 'ProbeRelayEndpoint\(probe, relayProbeTimeout\)' "$LADDER"; then
  ok "B2: the ladder probes the candidate's probe endpoint with the bounded timeout"
else
  bad "B2: the ladder no longer probes through ProbeEndpoint (the B310 contract, renegotiated for the hop)"
fi

if grep -q 'the peer relay ' "$LADDER"; then
  ok "B3: a hop that does not answer is reported as the PEER relay, not as the target"
else
  bad "B3: a failed hop would be reported as a failed target — different fix, same message"
fi

if grep -q 'via ' "$LADDER" && grep -q 'func (e RelayEndpoint) Label()' "$LADDER"; then
  ok "B4: the label names both ends of a hop path"
else
  bad "B4: a jump label names only one end"
fi

# --- C: the argv --------------------------------------------------------------
ARGV_MARK='func buildSetAdvertisedRoutesArgv('
if grep -q "$ARGV_MARK" "$ROUTES"; then
  ok "C1: the ssh argv is composed in ONE pure function"
else
  bad "C1: buildSetAdvertisedRoutesArgv is missing — the argv cannot be tested without spawning ssh"
fi
if grep -q '"ProxyCommand=none"' "$ROUTES" && grep -q '"ProxyCommand="+jumpProxyCommand(keyPath, jump)' "$ROUTES"; then
  ok "C2: ProxyCommand=none for a direct connection, an explicit ssh -W tunnel for a hop"
else
  bad "C2: the argv lost one of the two forms"
fi
# The obvious `-J` form was TRIED and does not work from the container: OpenSSH builds
# the implicit jump connection as `ssh -l <user> -W '[%h]:%p' <hop>` with neither the
# identity nor the host-key policy, so it answers "Permission denied
# (publickey,password)". A future editor must not "simplify" it back to -J.
if grep -qF 'Permission denied (publickey,password)' "$ROUTES" && grep -qF "W '[%h]:%p'" "$ROUTES"; then
  ok "C3: the measured reason -J cannot be used here is recorded next to the explicit form"
else
  bad "C3: the file does not record why the hop is an explicit ProxyCommand"
fi
if grep -q 'jumpProxyCommandAllowed' "$ROUTES" && grep -q 'refusing the ssh jump hop' "$ROUTES"; then
  ok "C4: a key path that cannot be quoted into the ProxyCommand refuses the jump instead of silently going direct"
else
  bad "C4: an unquotable key path would silently drop the hop (or break the shell string)"
fi
if grep -q 'refusing unsafe ssh jump hop' "$ROUTES" && grep -q 'IsSafeSSHTarget(jumpTarget)' "$ROUTES"; then
  ok "C5: the hop passes the same shape check as ssh_target (B266 class)"
else
  bad "C5: the hop reaches the argv unvalidated"
fi

# --- D: the B300 property survives --------------------------------------------
ssh_total=$(( $(grep -cE '\.SetAdvertisedRoutes\(' "$SYNC" || true) + $(grep -cE '\.SetAdvertisedRoutes\(' "$LADDER" || true) ))
if [ "$ssh_total" -eq 1 ]; then
  ok "D1: exactly ONE SetAdvertisedRoutes call site (B300 property preserved with the optional hop)"
else
  bad "D1: SetAdvertisedRoutes is called from $ssh_total place(s), want 1"
fi

# --- E: the tests actually run -------------------------------------------------
if command -v go >/dev/null 2>&1; then
  if [ -f "$JTESTS" ] && [ -f "$RTESTS" ]; then
    ok "E1: both halves have tests (hop selection + argv)"
  else
    bad "E1: missing test file(s): $JTESTS / $RTESTS"
  fi
  OUT="$(go test ./internal/feature/exit_rules/ -run 'TestB353' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "E2: the hop-selection tests pass"
  else
    bad "E2: B353 tests in exit_rules failed: $(tail -3 <<< "$OUT" | tr '\n' ' ')"
  fi
  OUT2="$(go test ./internal/headscale/ -run 'TestB353' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT2"; then
    ok "E3: the argv tests pass"
  else
    bad "E3: B353 tests in headscale failed: $(tail -3 <<< "$OUT2" | tr '\n' ' ')"
  fi
  # The live case must stay documented where the decision is made: the next reader
  # has to know WHY a hop exists at all.
  if grep -q 'DERP' "$JUMP" && grep -q '100.64.0.3' "$JUMP"; then
    ok "E4: the measured live case is recorded in the transport file"
  else
    bad "E4: the live evidence (DERP home unreachable / the two relays) is not documented"
  fi
else
  skip "E1-E4: no Go toolchain — the B353 test contracts were not run"
fi

# --- F: the check itself cannot be silently dropped ----------------------------
# AGENTS trap #11: `test -f` proves nothing about what was committed, and a check
# nobody runs is the same as no check.
if git ls-files --error-unmatch scripts/check_b353_transport_jump_fallback.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git (a new script can be eaten silently)"
else
  bad "F1: scripts/check_b353_transport_jump_fallback.sh is NOT tracked by git"
fi
if grep -q 'check_b353_transport_jump_fallback.sh' scripts/verify_pre_deploy.sh; then
  ok "F2: verify_pre_deploy.sh registers B353"
else
  bad "F2: the gate does not run this contract"
fi
if grep -q '^- \*\*B353\*\*' AGENTS.md; then
  ok "F3: AGENTS.md's block index carries B353"
else
  bad "F3: the block index does not know B353"
fi

hdr "B353 summary"
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
