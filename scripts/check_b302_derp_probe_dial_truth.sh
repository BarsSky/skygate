#!/usr/bin/env bash
# check_b302_derp_probe_dial_truth.sh
#
# 2026-09-23 (B302) — every /admin/derp probe must dial the ADDRESS that is
# reachable from here and speak the HOSTNAME.
#
# Live on the agent VM, /admin/derp rendered from inside the skygate container:
#
#   DERPER.SERVICE  stopped            <- FALSE: derper had been up 39 hours
#   DERP SOCKET     :443 TCP listening <- TRUE  (this probe already pinned the address)
#   STUN UDP        :3478 closed       <- TRUE  (see below)
#   VERSION         v1.70.0 go
#
# /etc/hosts inside the container maps the relay's own public name to 127.0.0.1
# (AGENTS deployment trap #2), so `derperLivenessWebSocketProbe` — the probe that
# decides `Running` whenever /debug/vars is 403, which is the normal hardened
# deployment — connected to the container's OWN loopback and failed, while the
# neighbouring probes (httpGetVia, B289.1) succeeded because they dial the pinned
# address. A liveness probe that dials a different address than its neighbours is
# not a liveness check, it is a second opinion.
#
# Verified live with the same shape as the page uses:
#   host -> derp.skynas.ru:443 (SNI set)  =>  HTTP/1.1 101 Switching Protocols
#   host -> 127.0.0.1:443     (SNI set)  =>  HTTP/1.1 101 Switching Protocols
# so the relay was up the whole time.
#
# The STUN tile, by contrast, told the truth: derper binds *:3478 (v6 table) and
# answers NO Binding Request — not from the host, not from the container, not even
# on loopback, over IPv4 or IPv6 — while its own help lists -stun/-stun-port 3478
# and its log says "STUN server listening on [::]:3478". That is a derper-side
# defect, so the tile stays red; what this block adds is that the tile now NAMES
# what it probed, so a UDP filter can be told apart from a relay that never answers.
#
# CONTRACTS
#   A. the WebSocket liveness probe pins the dial address and keeps the hostname
#      for Host/SNI, and its call site passes the same address the neighbours use
#   B. a failed STUN probe names every candidate it tried
#   C. the regression tests exist and pass (an unresolvable URL host plus a pinned
#      address must still reach the relay, and a closed port must still fail)
#   D. the other probes keep their pinning, and the STUN candidates still include
#      the panel-settable probe host (B296)
#   E. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B302: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DERP=internal/feature/admin/derp.go
STUN=internal/feature/admin/derp_stun.go
TEST=internal/feature/admin/derp_probe_dial_b302_test.go

hdr "B302 — every /admin/derp probe dials the reachable ADDRESS and speaks the HOSTNAME"

# --- A: the liveness probe pins the address -----------------------------------
if grep -q 'func derperLivenessWebSocketProbe(rawURL, dialAddr string, timeout time.Duration) (bool, error)' "$DERP"; then
  ok "A1: the WebSocket liveness probe takes a dial address"
else
  bad "A1: the WS probe still dials whatever the URL resolves to"
fi
if grep -q 'target := net.JoinHostPort(dialAddr, port)' "$DERP" \
   && grep -q 'tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {' "$DERP"; then
  ok "A2: the probe pins its TCP dial to that address (the same shape httpGetVia uses)"
else
  bad "A2: the pinned dial is missing"
fi
if grep -q 'derperLivenessWebSocketProbe(derpURL, dialAddr, 3\*time.Second)' "$DERP"; then
  ok "A3: the call site passes the SAME address the neighbouring probes use"
else
  bad "A3: the call site does not pass the resolved address — the probe would disagree with its neighbours again"
fi
if grep -q 'req.Host = hostnameOnly' "$DERP" && grep -q 'ServerName:         hostnameOnly' "$DERP"; then
  ok "A4: the hostname is still what goes into Host and TLS SNI (derper resolves its cert by SNI)"
else
  bad "A4: the Host/SNI wiring changed"
fi
if grep -q 'B302' "$DERP"; then
  ok "A5: the live failure that motivates the pin is documented at the function"
else
  bad "A5: the reason is undocumented"
fi

# --- B: a failed STUN probe names its candidates ------------------------------
if grep -q '(probed %s)' "$STUN" && grep -q 'strings.Join(candidates, ", ")' "$STUN"; then
  ok "B1: a red STUN tile names every candidate it probed"
else
  bad "B1: the STUN tile cannot say what it tried — a UDP filter is indistinguishable from a silent relay"
fi

# --- C: the regression tests ---------------------------------------------------
if [ -f "$TEST" ] \
   && grep -q 'TestWebSocketProbe_DialsThePinnedAddress_B302' "$TEST" \
   && grep -q 'TestWebSocketProbe_StillFailsWhenNothingListens_B302' "$TEST"; then
  ok "C1: the positive and negative halves of the fix are pinned as tests"
else
  bad "C1: the B302 regression tests are missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B302' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "C2: the B302 tests pass"
  else
    bad "C2: the B302 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "C2: go not on PATH — run the B302 tests on the VM"
fi

# --- D: the neighbours are still consistent ------------------------------------
if grep -c 'target := net.JoinHostPort(dialAddr, port)' "$DERP" | grep -q '^2$'; then
  ok "D1: BOTH pinned probes (status GET + liveness WS) resolve their address the same way"
else
  bad "D1: the two pinned probes no longer share the same dial rule"
fi
if grep -q 'derpcfg.DialHost(db)' "$STUN"; then
  ok "D2: the STUN candidate list still includes the panel-settable probe host (B296)"
else
  bad "D2: the operator's probe host is no longer a STUN candidate"
fi
if grep -q 'hostResolvesToLoopback' internal/feature/admin/derp_dashboard.go \
   && grep -q 'derpReachabilityCandidates' internal/feature/admin/derp_dashboard.go; then
  ok "D3: the loopback-leak machinery from B289/B289.1 is still in place"
else
  bad "D3: the reachability-candidate machinery disappeared"
fi

# --- E: git --------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b302_derp_probe_dial_truth.sh >/dev/null 2>&1; then
  ok "E1: scripts/check_b302_derp_probe_dial_truth.sh is tracked by git"
else
  bad "E1: scripts/check_b302_derp_probe_dial_truth.sh is NOT tracked"
fi

printf '\n\033[1mB302 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
