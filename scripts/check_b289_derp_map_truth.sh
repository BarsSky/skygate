#!/usr/bin/env bash
# check_b289_derp_map_truth.sh
#
# 2026-09-22 (B289) — «локальный DERP должен попадать в карту, а не молча из неё
# исчезать».
#
# LIVE REPORT (operator, VM `skygate-host`): the co-located DERP relay (region
# 900, `derp.skynas.ru:443`) never worked. Measured on the host:
#
#   * derper healthy: TCP 443 + UDP 3478 bound, TLS answers with the right
#     Let's Encrypt cert (CN=derp.skynas.ru), reachable at 192.168.13.69:443;
#   * `/admin/derp/relays/derpmap.json` answered {"Regions":[]} — the map
#     headscale fetches and merges with the public Tailscale map;
#   * the journal had the reason, three lines per fetch:
#       derpmap: skipping region=900 host=derp.skynas.ru port=443 — node
#       unreachable: dial tcp 127.0.0.1:443: connect: connection refused
#
# Root cause: the skygate CONTAINER inherits the host's /etc/hosts, where
# `derp.skynas.ru -> 127.0.0.1` (AGENTS deployment trap #2). Inside the container
# that is its own loopback, so the B265 reachability guard refused every bundled
# node and the local region vanished from the map — while the relay was fine.
# Nothing on any page said so.
#
# CONTRACTS
#   A. the guard probes every address a row is known by, and tries the
#      operator's SKYGATE_DERP_PROBE_HOST first when the name is leaked
#   B. a bundled region that publishes NO node is an ERROR with a named cause
#   C. the relays page shows what the map will contain, per row, with the reason
#   D. the regression tests exist and pass
#   E. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B289: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DASH=internal/feature/admin/derp_dashboard.go
RELAYS=internal/feature/admin/derp_relays.go
TMPL=internal/handlers/templates/admin/derp_relays.html
I18N=internal/i18n/catalog_derp.go
TEST=internal/feature/admin/derp_reach_b289_test.go

hdr "B289 — the local DERP must reach the map (or say why it did not)"

# --- A: probe every address, probe host first when the name is leaked --------
if grep -q '^func derpReachabilityCandidates(' "$DASH" && grep -q '^func hostResolvesToLoopback(' "$DASH"; then
  ok "A1: the candidate builder and the loopback detector exist"
else
  bad "A1: derpReachabilityCandidates / hostResolvesToLoopback are missing"
fi
if grep -q '^func probeDERPNodeReachableAny(' "$DASH"; then
  ok "A2: the fallback probe exists (first answering candidate wins)"
else
  bad "A2: probeDERPNodeReachableAny is missing"
fi
if grep -q 'derpReachabilityCandidates(s.dbc(), host, probeHost)' "$DASH" && \
   grep -q 'probeDERPNodeReachableAny(candidates, port' "$DASH"; then
  ok "A3: the derpmap endpoint probes through the candidate list"
else
  bad "A3: the derpmap endpoint still probes the bare hostname (the live failure)"
fi
if grep -q 'if leaked {' "$DASH" && grep -q 'add(probeHost)' "$DASH"; then
  ok "A4: the operator's probe host is tried FIRST when the relay name resolves to loopback"
else
  bad "A4: the candidate order does not special-case a leaked name"
fi
if grep -q 'answered via %s' "$DASH"; then
  ok "A5: the journal names the address that answered (and points at extra_hosts)"
else
  bad "A5: a relay reachable only via a fallback address is not reported"
fi

# --- B: a bundled region that publishes nothing is an ERROR ------------------
if grep -q 'is BUNDLED but publishes NO node' "$DASH"; then
  ok "B1: an empty bundled region is logged as an ERROR with its causes"
else
  bad "B1: the silent-empty-map class is still silent"
fi
if grep -q '^func bundledRegionIDs(' "$DASH"; then
  ok "B2: the bundled regions are identified from the DB, not guessed"
else
  bad "B2: bundledRegionIDs is missing"
fi
if grep -q 'tried %v' "$DASH"; then
  ok "B3: the skip line lists every probed address (so a DNS leak is visible)"
else
  bad "B3: the skip line does not say what was probed"
fi

# --- C: the page shows the map outcome --------------------------------------
if grep -q 'func (s \*Service) derpRelayMapStatuses()' "$RELAYS" && grep -q '"MapStatus"' "$RELAYS"; then
  ok "C1: /admin/derp/relays computes and renders the per-row map status"
else
  bad "C1: the relays page does not expose the map outcome"
fi
if grep -q '{{with .MapStatus}}' "$TMPL" && grep -q 'relays_map_skipped' "$TMPL"; then
  ok "C2: the template renders published / skipped with the probed addresses"
else
  bad "C2: the template does not render the map status"
fi
RU=$(grep -c '"derp.relays_map_title"' "$I18N" || true)
EN=$(grep -c '"derp.relays_map_skipped"' "$I18N" || true)
if [ "${RU:-0}" -ge 2 ] && [ "${EN:-0}" -ge 2 ]; then
  ok "C3: the new i18n keys exist in RU and EN"
else
  bad "C3: i18n keys missing (map_title=${RU:-0}, map_skipped=${EN:-0}; want 2 each)"
fi
if grep -q 'mapStatusTTL' "$RELAYS"; then
  ok "C4: the page probe is cached, so rendering cannot cost a timeout per dead row"
else
  bad "C4: the page probes on every render without a cache"
fi

# --- D: the regression tests -------------------------------------------------
if [ -f "$TEST" ]; then
  ok "D1: $TEST exists"
else
  bad "D1: $TEST is missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B289' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "D2: the B289 tests pass"
  else
    bad "D2: the B289 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "D2: go not on PATH — run the B289 tests on the VM"
fi

# --- E: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b289_derp_map_truth.sh >/dev/null 2>&1; then
  ok "E1: scripts/check_b289_derp_map_truth.sh is tracked by git"
else
  bad "E1: scripts/check_b289_derp_map_truth.sh is NOT tracked"
fi

printf '\n\033[1mB289 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
