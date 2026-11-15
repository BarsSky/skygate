#!/usr/bin/env bash
# ============================================================================
# check_b478_derp_probe_truth.sh — B478
#
# «DERPER SERVICE stopped» on a relay that was demonstrably serving, and a
# latency of 136 ms for a relay on the same machine.
#
# Measured 2026-11-15 on the migration host (derper and skygate on ONE box):
#
#   curl --http1.1 -H 'Upgrade: DERP'      https://derp.skynas.ru/derp → 101  (~10 ms)
#   curl --http1.1 -H 'Upgrade: websocket' https://derp.skynas.ru/derp → 200
#   curl --http2  -H 'Upgrade: DERP'       https://derp.skynas.ru/derp → 426
#
# The panel's liveness probe sent `Upgrade: websocket` and let Go negotiate HTTP/2,
# so through Caddy it never saw 101 and reported a healthy relay as dead. The same
# probe also dialled a stale address from derp.probe_host (the OLD host's public
# IP), which is where the 136 ms came from.
#
# Contracts:
#   A. the probe sends the REAL upgrade token and forces HTTP/1.1
#   B. 101 is the only healthy answer; the pinned dial address still wins
#   C. the measured difference is recorded where the format is decided
#   D. tests pin all three properties and pass
#   E. registered in the gate and in the block index
#   F. the operator can find the symptom (EN + RU, both catalogues)
#   G. the predicates discriminate the pre-B478 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b478.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

DERP=internal/feature/admin/derp.go
TEST=internal/feature/admin/derp_b478_test.go
DERPH=internal/derphealth/probe.go
DERPHTEST=internal/derphealth/probe_b478_1_test.go
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
DOC=docs/troubleshooting.md
RUDOC=docs/ru/derp-status-stopped.md
DOCIDX=docs/README.md
RUDOCIDX=docs/ru/README.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$DERP" "$TEST" "$DERPH" "$DERPHTEST" "$VERIFY" "$AGENTS" "$DOC" "$RUDOC" "$DOCIDX" "$RUDOCIDX"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# The probe body, so a token set in some other function cannot satisfy the contract.
PROBE="$SKY_TMP/probe.txt"
awk 'index($0, "func derperLivenessWebSocketProbe(") {f=1} f {print} f && /^}/ {exit}' "$DERP" > "$PROBE"
if [ ! -s "$PROBE" ]; then
  bad "A0: derperLivenessWebSocketProbe not found — the contract would verify nothing"
fi

# --- A: the real token, over HTTP/1.1 ---------------------------------------
if has "$PROBE" 'req.Header.Set("Upgrade", "DERP")'; then
  ok "A1: the probe sends the real DERP upgrade token"
else
  bad "A1: the probe sends some other upgrade token — a proxy decides the answer, not the relay"
fi
if has "$PROBE" 'tr.ForceAttemptHTTP2 = false' && has "$PROBE" 'tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}'; then
  ok "A2: the probe forces HTTP/1.1 (an HTTP/2 connection cannot carry an upgrade)"
else
  bad "A2: the probe leaves the ALPN to chance — the verdict depends on the proxy"
fi
if has "$PROBE" 'http.StatusSwitchingProtocols'; then
  ok "A3: 101 is the healthy answer (a plain 200 means no upgrade happened)"
else
  bad "A3: the probe does not distinguish an upgrade from a refusal"
fi
if has "$PROBE" 'tr.DialContext'; then
  ok "A4: the probe still honours the pinned dial address (B302)"
else
  bad "A4: the pinned dial address was lost — a container would probe its own loopback"
fi

# --- A5/A6: the explicit hint is a PIN, not a hint that may be ignored -------
# B478.1 (2026-11-15): DERPH=internal/derphealth/probe.go
if [ -f "$DERPH" ]; then
  if has "$DERPH" 'if hint != "" {' && ! has "$DERPH" 'nameResolvesToLoopback'; then
    ok "A5: an explicit probe host is dialled UNCONDITIONALLY (the loopback sniffing is gone)"
  else
    bad "A5: the explicit probe host is still conditional — a relay whose public name resolves publicly ignores the pin"
  fi
  if has "$DERPH" 'derp.probe_host' || has "$DERPH" 'ProbeHost'; then
    ok "A6: the row's own hint is the knob the resolver reads"
  else
    bad "A6: the resolver does not read the row hint"
  fi
else
  bad "A0': $DERPH not found"
fi

# --- C: the measurement is recorded at the site ------------------------------
if has "$DERP" 'Upgrade: DERP' && has "$DERP" '426'; then
  ok "C1: the measured 101/200/426 difference is recorded where the format is decided"
else
  bad "C1: nothing at the site explains why the token matters"
fi

# --- D: tests ---------------------------------------------------------------
for _t in TestB478_ProbeSendsTheRealUpgradeToken TestB478_TwoHundredIsNotHealthy TestB478_ProbeUsesThePinnedDialAddress; do
  if has "$TEST" "$_t"; then
    ok "D1: $_t exists"
  else
    bad "D1: $_t is missing"
  fi
done
for _t in TestB478_1_ExplicitHintWinsOnAPubliclyResolvingName TestB478_1_NoHintKeepsTheRowsOwnAddress TestB478_1_RowHintBeatsTheEnvironment; do
  if has "$DERPHTEST" "$_t"; then
    ok "D1b: $_t exists"
  else
    bad "D1b: $_t is missing"
  fi
done
if command -v go >/dev/null 2>&1; then
  OUT2="$(go test ./internal/derphealth/ -run TestB478_1 2>&1)"; rc2=$?
  if [ "$rc2" -eq 0 ] && case "$OUT2" in *"ok "*) true;; *) false;; esac; then
    ok "D2b: go test ./internal/derphealth/ -run TestB478_1 passes"
  else
    bad "D2b: B478.1 tests fail or did not run: $(printf '%s' "$OUT2" | tail -3)"
  fi
fi
if command -v go >/dev/null 2>&1; then
  # Trap #9: capture first, then match.
  OUT="$(go test ./internal/feature/admin/ -run TestB478 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && case "$OUT" in *"ok "*) true;; *) false;; esac; then
    ok "D2: go test ./internal/feature/admin/ -run TestB478 passes"
  else
    bad "D2: B478 tests fail or did not run: $(printf '%s' "$OUT" | tail -3)"
  fi
else
  printf '  \033[33m-\033[0m D2: SKIP — no go toolchain on PATH (%s)\n' "${BASH:-bash}"
fi

# --- E: registration --------------------------------------------------------
if has "$VERIFY" '"B478"'; then
  ok "E1: the contract is registered in the gate"
else
  bad "E1: an unregistered check never runs"
fi
if has "$AGENTS" 'B478'; then
  ok "E2: the block index carries the one-line entry"
else
  bad "E2: the block is not in the index"
fi

# --- F: the operator can find it --------------------------------------------
if has "$DOC" 'B478' && has "$DOC" 'Upgrade: DERP'; then
  ok "F1: the EN troubleshooting guide covers the false 'stopped' verdict"
else
  bad "F1: the EN guide does not cover it"
fi
if has "$RUDOC" 'DERPER SERVICE' && has "$RUDOC" '8443'; then
  ok "F2: the RU doc explains the symptom and the direct-port fix"
else
  bad "F2: the RU doc is missing"
fi
if has "$DOCIDX" 'derp-status-stopped.md' && has "$RUDOCIDX" 'derp-status-stopped.md'; then
  ok "F3: both catalogues index the RU doc"
else
  bad "F3: the RU doc is not indexed in both catalogues"
fi

# --- G: the predicates discriminate the pre-B478 shape ----------------------
cat > "$SKY_TMP/old.go" <<'EOF'
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")

	tr := &http.Transport{}
	if dialAddr != "" {
		tr.DialContext = nil
	}
EOF
discriminates=1
has "$PROBE" 'req.Header.Set("Upgrade", "DERP")' || discriminates=0
has "$PROBE" 'tr.ForceAttemptHTTP2 = false' || discriminates=0
has "$PROBE" 'tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}' || discriminates=0
for _m in '"Upgrade", "DERP"' 'ForceAttemptHTTP2 = false' 'TLSNextProto = map'; do
  has "$SKY_TMP/old.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "G1: every predicate is present now and absent from the pre-B478 shape"
else
  bad "G1: the predicates do not discriminate the pre-B478 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB478: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB478: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
