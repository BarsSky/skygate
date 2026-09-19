#!/usr/bin/env bash
# check_b265_derp_status_truth.sh
#
# 2026-09-19 (B265) — /admin/derp must report the TRUTH about two things:
#
#   1. STUN/UDP reachability. Pre-B265 the tile had exactly one data
#      source — `stun.counter_requests.success` from derper's
#      `GET /debug/vars` — and upstream derper gates every /debug/*
#      handler behind tsweb.AllowDebugAccess (loopback / Tailscale
#      source IP / TS_ALLOW_DEBUG_IP / TS_DEBUG_KEY_PATH only).
#      The skygate container is NEVER such a source, so the answer was
#      always `403 debug access denied` and the page rendered
#      "STUN UDP :3478 closed" (red) on a relay that was demonstrably
#      healthy (live reference VM: UDP 3478 bound,
#      stun.counter_requests.success=33709, a remote VPS measuring the
#      relay via netcheck, clients showing `relay "mow"`).
#      B265 replaces the counter read with a real RFC 5389 STUN Binding
#      Request round trip over UDP.
#
#   2. Which relay is the operator's OWN. `internal/derphealth/map.go`
#      had the mapping inverted (`d.IsOwn = isBundled == 0`), so the
#      dashboard's "Recommended DERP" banner and the main-page hero
#      selected region 901 (the `controlplane.tailscale.com` row that
#      AutoMigrateDerpRelays created from the legacy derp.external_urls
#      DERPMAP URL) instead of region 900 (the operator's own derper).
#      Live: derp_health had 901 is_own=1 108ms and 900 is_own=0 12ms.
#      Additionally the derpmap endpoint published that URL as a relay
#      node and advertised a dead `:8443` node, both named `mow-1`.
#
# Contracts (A-H source-level, I local unit tests, J live-state note).
# A check that needs live state must SKIP, never FAIL.

set -uo pipefail
# shellcheck disable=SC2016
# Find Go (git-bash / MSYS2 subshell doesn't inherit the Windows PATH).
if ! command -v go >/dev/null 2>&1; then
  for cand in '/mnt/c/Program Files/Go/bin' '/c/Program Files/Go/bin'; do
    if [ -f "$cand/go.exe" ] && "$cand/go.exe" version >/dev/null 2>&1; then
      export PATH="$cand:$PATH"
      break
    fi
  done
fi

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

STUN_GO="internal/feature/admin/derp_stun.go"
STUN_TEST="internal/feature/admin/derp_stun_b265_test.go"
DERP_GO="internal/feature/admin/derp.go"
DASH_GO="internal/feature/admin/derp_dashboard.go"
MAP_GO="internal/derphealth/map.go"
MAP_TEST="internal/derphealth/map_b265_test.go"

hdr "B265 — /admin/derp status truth (STUN round trip + own-relay selection)"

# --- A: the STUN probe module exists with the RFC 5389 essentials ---
if [ -f "$STUN_GO" ] \
   && grep -q 'func probeSTUNUDP' "$STUN_GO" \
   && grep -q 'func buildSTUNBindingRequest' "$STUN_GO" \
   && grep -q 'func parseSTUNBindingResponse' "$STUN_GO" \
   && grep -q 'stunMagicCookie uint32 = 0x2112A442' "$STUN_GO" \
   && grep -q 'stunBindingRequest uint16 = 0x0001' "$STUN_GO" \
   && grep -q 'stunBindingSuccess uint16 = 0x0101' "$STUN_GO"; then
  ok "A: derp_stun.go implements an RFC 5389 Binding Request/Response round trip"
else
  bad "A: derp_stun.go missing the STUN wire implementation (probeSTUNUDP / build / parse / magic cookie / message types)"
fi

# --- B: the STUN tile is driven by the UDP probe, not by /debug/vars ---
# The /debug/vars success>0 read must no longer be the STUN source. It
# may still set Running / the rich metrics.
if grep -q 'probeSTUNForStatus(&st, s.dbc(), derpHost, stunPort)' "$DERP_GO"; then
  ok "B: collectDerpStatus drives STUNListening via probeSTUNForStatus"
else
  bad "B: collectDerpStatus does not call probeSTUNForStatus — the STUN tile is still gated on the 403'd /debug/vars counter"
fi
if grep -q 'j.STUN.CounterRequests.Success > 0' "$DERP_GO"; then
  bad "B2: the pre-B265 'counter_requests.success > 0 → STUNListening' read is still present in derp.go"
else
  ok "B2: the /debug/vars STUN-counter read is gone from the status path"
fi

# --- C: the 403 debug-access denial is acknowledged, not drawn as zero ---
if grep -q 'func isDebugAccessDenied' "$STUN_GO" \
   && grep -q 'DebugAccessDenied' "$DERP_GO" \
   && grep -q 'debug access denied' "$STUN_GO"; then
  ok "C: tsweb's 403 'debug access denied' body is recognised and surfaced (DebugAccessDenied)"
else
  bad "C: the 403 debug-access denial is not detected — traffic/client/stun metrics render as silent zeros"
fi

# --- D: probe order is hostname-first with an operator override ---
if grep -q 'func STUNCandidateHosts' "$STUN_GO" \
   && grep -q 'SKYGATE_DERP_PROBE_HOST' "$STUN_GO" \
   && grep -q 'SKYGATE_DERP_HOSTNAME' "$STUN_GO"; then
  ok "D: STUNCandidateHosts tries the bundled hostname, then SKYGATE_DERP_PROBE_HOST / SKYGATE_DERP_HOSTNAME"
else
  bad "D: STUN probe target resolution missing (hostname-first + operator override)"
fi
if grep -q 'func bundledDERPHostnamesFromDB' internal/feature/admin/derp_status_resolve.go; then
  ok "D2: bundledDERPHostnamesFromDB provides the per-row hostname fallback"
else
  bad "D2: bundledDERPHostnamesFromDB missing — a second bundled row's hostname is not probed"
fi

# --- E: IsOwn mapping is bundled==own and is a pure, testable helper ---
if grep -q 'func derpRelayIsOwn' "$MAP_GO" \
   && grep -q 'return isBundled == 1' "$MAP_GO" \
   && grep -q 'd.IsOwn = derpRelayIsOwn(isBundled)' "$MAP_GO"; then
  ok "E: FetchOwnDERPs marks bundled rows as own (derpRelayIsOwn: isBundled == 1)"
else
  bad "E: the IsOwn mapping is not the extracted 'isBundled == 1' helper — the pre-B265 inversion may be back"
fi
if grep -qE '^\s*d\.IsOwn\s*=\s*isBundled\s*==\s*0' "$MAP_GO"; then
  bad "E2: the inverted assignment 'd.IsOwn = isBundled == 0' is still present in map.go"
else
  ok "E2: no inverted 'd.IsOwn = isBundled == 0' assignment remains (comments explaining the old bug are fine)"
fi

# --- F: derpmap endpoint refuses map documents and dead nodes ---
if grep -q 'func isDerpMapURL' "$DASH_GO" \
   && grep -q 'func probeDERPNodeReachable' "$DASH_GO" \
   && grep -q 'isDerpMapURL(urlStr)' "$DASH_GO" \
   && grep -q 'reach.Reachable' "$DASH_GO"; then
  ok "F: derpmap.json skips derpmap-document rows and nodes that fail the reachability probe"
else
  bad "F: derpmap endpoint still publishes map documents / dead nodes as relay nodes"
fi
if grep -q 'node.Name = node.Name + "-" + strconv.Itoa(port)' "$DASH_GO"; then
  ok "F2: node names are de-duplicated inside a region (pre-B265 two bundled rows both published as mow-1)"
else
  bad "F2: duplicate node names inside a region are not disambiguated"
fi

# --- G: i18n keys for the new tile states, RU+EN paired ---
for k in derp.stun_unreachable derp.stun_reflex derp.stun_reflex_help derp.debug_access_denied; do
  ru=$(grep -c "\"$k\":" internal/i18n/catalog_derp.go)
  if [ "$ru" -ge 2 ]; then
    ok "G: i18n key $k present in both catalogues"
  else
    bad "G: i18n key $k present only $ru time(s) — RU and EN must both define it"
  fi
done

# --- H: the template renders the reason and the metrics caveat ---
if grep -q 'derp.stun_unreachable' internal/handlers/templates/admin/derp.html \
   && grep -q 'derp.debug_access_denied' internal/handlers/templates/admin/derp.html \
   && grep -q 'DerpStatus.STUNRTT' internal/handlers/templates/admin/derp.html; then
  ok "H: derp.html shows the STUN RTT, the failure reason and the debug-denied caveat"
else
  bad "H: derp.html does not render the new STUN/debug states — the operator still sees a bare 'closed'"
fi

# --- I: regression tests exist and pass ---
if [ -f "$STUN_TEST" ] && [ -f "$MAP_TEST" ]; then
  n=$(grep -c '^func Test' "$STUN_TEST")
  m=$(grep -c '^func Test' "$MAP_TEST")
  if [ "$n" -ge 6 ] && [ "$m" -ge 3 ]; then
    ok "I: regression tests present (derp_stun_b265_test.go: $n, map_b265_test.go: $m)"
  else
    bad "I: too few B265 tests (stun=$n want >=6, map=$m want >=3)"
  fi
else
  bad "I: B265 test files missing ($STUN_TEST / $MAP_TEST)"
fi
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "I2: go build ./... clean"
  else
    bad "I2: go build ./... failed"
  fi
  if go test ./internal/feature/admin/ -run 'STUN|DebugAccessDenied' -count=1 >/dev/null 2>&1; then
    ok "I3: STUN wire-format + negative-path tests pass"
  else
    bad "I3: go test ./internal/feature/admin/ -run 'STUN|DebugAccessDenied' failed"
  fi
  if go test ./internal/derphealth/ -run 'DerpRelayIsOwn' -count=1 >/dev/null 2>&1; then
    ok "I4: IsOwn mapping tests pass"
  else
    bad "I4: go test ./internal/derphealth/ -run DerpRelayIsOwn failed"
  fi
else
  skip "I2-I4: go not reachable in this bash PATH"
fi

# --- J: live-state note (operator side) ---
hdr "J: live-state (operator-side, post-deploy)"
cat <<'NOTE'
  On the derper host, after deploying a build with B265:

    # 1. The tile must be green and show an RTT:
    #    open /admin/derp → "STUN UDP :3478 <rtt> ms"
    # 2. Cross-check a real STUN round trip from the skygate container's
    #    network (any host that can reach the derper on UDP 3478):
    #      python3 - <<'EOF'
    #      import socket,struct,os
    #      req=struct.pack('!HHI12s',1,0,0x2112A442,os.urandom(12))
    #      s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.settimeout(3)
    #      s.sendto(req,('derp.example.com',3478)); print(len(s.recvfrom(1024)[0]),'bytes')
    #      EOF
    #    (a STUN Binding Success Response is 32+ bytes; a timeout means the
    #     path really is blocked and the red tile is correct)
    # 3. Confirm the recommended relay is the operator's own:
    #      psql ... -c "SELECT region_id, is_own, host, latency_ms FROM derp_health ORDER BY is_own DESC, latency_ms"
    #    the bundled region (900) must be the one with is_own=1.
    # 4. Confirm the client receives no phantom region:
    #      tailscale debug derp-map | grep -A3 '"900"'
    #    (the public map has no region 901; a 901 entry pointing at
    #     controlplane.tailscale.com is the stale derp_relays row —
    #     delete/disable it and re-run "Apply to headscale")
NOTE
ok "J: live-state note printed (run manually after deploy)"

printf '\n\033[1mB265 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
