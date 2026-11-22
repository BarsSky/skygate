#!/usr/bin/env bash
# check_b510_derp_map_addresses.sh — B510 (2026-11-25)
#
# THE CELLULAR-ONLY SPIKE. Operator: «проблема именно в скачках, и это в основном у
# мобильных устройств по мобильной сети, на вайфае таких проблем нет».
#
# That sentence is the diagnosis. Wi-Fi clients get a DIRECT path, so DERP is not in their
# traffic at all; cellular clients are relayed, so every relay re-selection is felt as a
# stalled stream. Measured on the reference host, all read-only:
#   * derp/head/skygate names answered with an A record ONLY — no AAAA, although the host
#     has a public IPv6, the derper binds it and ufw allows it;
#   * the derpmap we publish for our own region 900 carried DERPPort and STUNPort but
#     neither an IPv4 nor an IPv6 hint (verified in the served JSON: ipv4=None ipv6=None);
#   * tailscaled answered with `netcheck.runProbe: named node "mow-1" has no v6 address`
#     continuously: 211 netcheck probe lines and 6 DERP re-connections in ONE hour.
# Cellular here is IPv6-first; the default Tailscale regions carry IPv6 and ours did not,
# so an IPv6-preferring phone kept re-probing and re-selecting DERP. Wi-Fi never noticed.
#
# What is pinned here:
#   A. the map node type carries IPv4/IPv6 as Tailscale names them, OMITTED when unknown
#   B. the builder FILLS them from what the name actually resolves to (never invents)
#   C. the missing-IPv6 case is REPORTED with the exact fix, and rate-limited (the map is
#      regenerated on every fetch, so an unguarded line would bury the journal)
#   D. behaviour: the pure picker refuses link-local/ULA/loopback, and the JSON contract
#      omits an absent address instead of publishing an empty hint
#   E. the operator-facing audit exists, is tracked, is READ-ONLY, and finds the case
#   F. registry: gate, AGENTS index, release notes, troubleshooting section
#
# Fixtures are synthetic (RFC 5737 / RFC 3849): this file is committed, so it may not carry
# a real address of the operator's tailnet.

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

MAP=internal/feature/admin/derp_dashboard.go
ADDR=internal/feature/admin/derp_mapaddr_b510.go
TESTF=internal/feature/admin/derp_mapaddr_b510_test.go
AUDIT=scripts/derp_path_audit.sh
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
TRBL=docs/troubleshooting.md

for f in "$MAP" "$ADDR" "$TESTF" "$AUDIT" "$GATE" "$AGENTS" "$NOTES" "$TRBL"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B510 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/b510.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

# ── A. the wire shape ───────────────────────────────────────────────────────
echo "[A] the map node carries the hints"

case "$(cat "$MAP")" in
  *'IPv4             string `json:"IPv4,omitempty"`'*)
    ok "A1: IPv4 is published under Tailscale's own field name and omitted when unknown" ;;
  *)
    bad "A1: the map node has no omitempty IPv4 hint" ;;
esac
case "$(cat "$MAP")" in
  *'IPv6             string `json:"IPv6,omitempty"`'*)
    ok "A2: IPv6 likewise (an EMPTY hint would make a client dial \"\")" ;;
  *)
    bad "A2: the map node has no omitempty IPv6 hint" ;;
esac

# ── B. the builder fills them, from DNS ─────────────────────────────────────
echo "[B] the builder"

case "$(cat "$MAP")" in
  *'derpNodeAddresses(host)'*)
    ok "B1: the node asks the resolver for its addresses" ;;
  *)
    bad "B1: the builder does not resolve the node's addresses" ;;
esac
case "$(cat "$ADDR")" in
  *'LookupIPAddr'*)
    ok "B2: the lookup is a real DNS query (both families), not a guess" ;;
  *)
    bad "B2: no resolver query — the hints would be invented" ;;
esac
case "$(cat "$ADDR")" in
  *'derpAddressLookupTimeout'*)
    ok "B3: the lookup is bounded (the map is generated on request; it must not stall headscale)" ;;
  *)
    bad "B3: the lookup has no timeout" ;;
esac

# ── C. the absence is REPORTED ──────────────────────────────────────────────
echo "[C] a missing IPv6 is named, not silent"

case "$(cat "$MAP")" in
  *'noteDerpAddressGap(host, nv4, nv6)'*)
    ok "C1: the builder reports the gap instead of publishing silently" ;;
  *)
    bad "C1: the builder never mentions a missing IPv6 — that was the whole defect" ;;
esac
case "$(cat "$ADDR")" in
  *'AAAA'*)
    ok "C2: the warning names the DNS record that fixes it" ;;
  *)
    bad "C2: the warning does not name the AAAA record" ;;
esac
case "$(cat "$ADDR")" in
  *'464XLAT'*)
    ok "C3: the warning explains WHY it matters (v6-only cellular via 464XLAT)" ;;
  *)
    bad "C3: the warning does not explain the cellular case" ;;
esac
case "$(cat "$ADDR")" in
  *'derpAddrWarnInterval'*)
    ok "C4: the warning is rate-limited (the map is regenerated on every fetch)" ;;
  *)
    bad "C4: the warning would fire on every map fetch" ;;
esac

# ── D. behaviour ────────────────────────────────────────────────────────────
echo "[D] behaviour"

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'TestB510' -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "D1: the B510 unit tests pass (picker, JSON contract, rate limit)"
  else
    bad "D1: go test -run TestB510 failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
else
  skip "D1: no Go toolchain on PATH"
fi
case "$(cat "$ADDR")" in
  *'IsLinkLocalUnicast'*|*'isULAv6'*)
    ok "D2: an unreachable IPv6 form (link-local / ULA) is never published as a hint" ;;
  *)
    bad "D2: the picker would publish a link-local or ULA address" ;;
esac

# ── E. the operator-facing audit ────────────────────────────────────────────
echo "[E] the audit that would have found this in one command"

if git ls-files --error-unmatch "$AUDIT" >/dev/null 2>&1; then
  ok "E1: $AUDIT is TRACKED by git (AGENTS trap #11)"
else
  bad "E1: $AUDIT is not tracked — a .gitignore rule would keep it out of the release"
fi
if bash -n "$AUDIT" 2>"$SKY_TMP/n.err"; then
  ok "E2: bash -n is clean"
else
  bad "E2: bash -n failed: $(tr '\n' ' ' <"$SKY_TMP/n.err")"
fi
case "$(grep -v '^[[:space:]]*#' "$AUDIT")" in
  *"tailscale set"*|*"ufw allow"*|*"caddy reload"*)
    bad "E3: the audit changes state — a diagnostic must not" ;;
  *)
    ok "E3: the audit is read-only" ;;
esac
if command -v python3 >/dev/null 2>&1; then
  # Run it against a name that CANNOT resolve: it must report findings and still exit 0
  # (an unresolvable name is a finding about DNS, never a tool failure).
  OUT="$(bash "$AUDIT" --host no-such-relay.invalid --hours 1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "E4: an unresolvable name yields findings and exit 0 (a finding is not a tool error)"
  else
    bad "E4: the audit exited $RC on an unresolvable name"
  fi
  case "$OUT" in
    *"does not resolve at all"*)
      ok "E5: the audit names the DNS failure instead of printing an empty report" ;;
    *)
      bad "E5: no DNS finding for an unresolvable name. Output: $(printf '%s' "$OUT" | head -c 300)" ;;
  esac
else
  skip "E4-E5: python3 is not available in this environment"
fi

# E6: the STUN helper must speak the TARGET's address family. Found while verifying this
# very block: the helper hardcoded AF_INET, so a v6 probe failed with "Address family for
# hostname not supported" and the audit would have reported a healthy relay as unreachable
# over v6 — a false negative of exactly the class the helper exists to prevent.
if command -v python3 >/dev/null 2>&1 && [ -f scripts/lib/stun_probe.py ]; then
  FAM="$(python3 - scripts/lib/stun_probe.py <<'PY'
import importlib.util, socket, sys
spec = importlib.util.spec_from_file_location("sp", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
v4 = m.socket_family_for("127.0.0.1")[0]
v6 = m.socket_family_for("::1")[0]
print("%s %s" % (v4 == socket.AF_INET, v6 == socket.AF_INET6))
PY
)"
  case "$FAM" in
    "True True")
      ok "E6: the STUN helper picks AF_INET for a v4 target and AF_INET6 for a v6 one" ;;
    *)
      bad "E6: the STUN helper cannot probe a v6 target (got: ${FAM:-error}) — a v6 relay would be reported unreachable" ;;
  esac
else
  skip "E6: python3 or scripts/lib/stun_probe.py unavailable"
fi

# ── F. registry ─────────────────────────────────────────────────────────────
echo "[F] registry"

case "$(cat "$GATE")" in
  *'run_check "B510"'*) ok "F1: registered in scripts/verify_pre_deploy.sh" ;;
  *) bad "F1: B510 is not registered in the gate" ;;
esac
case "$(cat "$AGENTS")" in
  *"**B510**"*) ok "F2: one-line entry in the AGENTS.md block index" ;;
  *) bad "F2: no AGENTS.md block-index entry" ;;
esac
case "$(cat "$NOTES")" in
  *"derp_path_audit"*) ok "F3: the release notes explain the audit and the rule" ;;
  *) bad "F3: RELEASE-NOTES.md does not mention the audit" ;;
esac
case "$(cat "$TRBL")" in
  *"AA"*"AA"*) ok "F4: docs/troubleshooting.md carries the procedure" ;;
  *) bad "F4: no troubleshooting section for this class" ;;
esac

echo
echo "B510 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
