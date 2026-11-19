#!/usr/bin/env bash
# B500 (2026-11-19) — «ПОРТЫ ОТКРЫТЫ?» И «УСТРОЙСТВО ДОХОДИТ?» — ДВЕ ПРОВЕРКИ, А НЕ ДВЕ ДОГАДКИ.
#
# The operator asked two things after the olesya investigation: «по устройству делаем
# проверки» and «посмотри может сама svyatoslava не открыла порты нужные». Both were
# answerable only by hand, and the hand answer split one symptom into two causes:
#
#   * ufw allowed 443/tcp, 3478/udp and 8443/tcp but NOT 443/udp, although Caddy listens
#     on udp/443 (HTTP/3 — the same port DERP-over-QUIC uses). The product's own installer
#     did not plan that port either, so a rebuilt host recreates the gap silently:
#     browsers and phones fall back to TCP and nobody notices the slower path.
#   * STUN never answered — not from the internet, not from 127.0.0.1 — while the socket
#     was bound and ufw accepted the packets (the accept rule's counter was growing).
#     «The port is open» and «the service answers» are different facts, and only the
#     second one is what a client needs.
#   * and the device question has its own shape: `olesya` was ONLINE in the control plane
#     (a DERP session) with rx=0, no disco reply and no path from any relay — while the
#     ACL permitted an exit node and 4/4 relays were healthy. «Online» is not «has a path».
#
# Contracts:
#   A. scripts/port_audit.sh exists, is read-only, and separates the three questions
#      (listening / allowed / answering)
#   B. deploy/install-firewall.sh plans EVERY port port_audit expects — the parity that
#      keeps a rebuilt host from recreating the gap
#   C. scripts/device_connectivity.sh reports the five layers and cannot say OK without a
#      data path
#   D. the two facts are written down where an operator looks
#   E. discoverable: registered, tracked, indexed, roadmap, release notes
#   F. behaviour where this host can run it (SKIP elsewhere)

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

AUDIT="scripts/port_audit.sh"
FW="deploy/install-firewall.sh"
DEV="scripts/device_connectivity.sh"

# ═══════════════ A. the port audit ═══════════════
for f in "$AUDIT" "$DEV"; do
    if [ -f "$f" ]; then ok "A1: $f exists"; else bad "A1: $f is missing"; fi
    if bash -n "$f" 2>/dev/null; then ok "A2: $(basename "$f") parses"; else bad "A2: $(basename "$f") does not parse"; fi
done
# read-only: it must never call `ufw allow`
if grep -qE 'ufw (allow|delete|deny|enable)' "$AUDIT" 2>/dev/null; then
    bad "A3: the port audit can change the firewall — it must only read"
else
    ok "A3: the port audit only reads the firewall"
fi
miss=""
for p in 80/tcp 443/tcp 443/udp 8443/tcp 3478/udp; do
    grep -q "$p|" "$AUDIT" 2>/dev/null || miss="$miss $p"
done
if [ -z "$miss" ]; then
    ok "A4: it audits all five ports the product serves"
else
    bad "A4: the audit list misses:$miss"
fi
# the three separate questions
if grep -q 'listening_on' "$AUDIT" 2>/dev/null && grep -q 'ufw_allows' "$AUDIT" 2>/dev/null; then
    ok "A5: «listening» and «allowed by the firewall» are asked separately"
else
    bad "A5: the audit conflates listening with allowed"
fi
if grep -q 'a port that listens is not a service that answers\|STUN answers' "$AUDIT" 2>/dev/null; then
    ok "A6: it also asks «does STUN answer?» — a bound port is not a working service"
else
    bad "A6: nothing distinguishes a bound port from an answering one"
fi
# B500's own hard lesson: a prober that does not mimic a real client measures itself.
# derper answers SOFTWARE "tailnode" + FINGERPRINT and counts everything else as
# `not_stun` (measured 3/3 both ways), so the shared helper must send that shape.
if [ -f scripts/lib/stun_probe.py ] && grep -q 'tailnode' scripts/lib/stun_probe.py 2>/dev/null; then
    ok "A7: the STUN prober sends the client shape (SOFTWARE tailnode + FINGERPRINT)"
else
    bad "A7: the STUN prober does not mimic a client — a silence would say nothing about the relay"
fi
if grep -q 'lib/stun_probe.py' "$AUDIT" 2>/dev/null; then
    ok "A8: the audit reuses that one helper instead of inlining a packet"
else
    bad "A8: the audit builds its own STUN packet again (two implementations, two answers)"
fi
# The product has TWO STUN implementations (Go, for /admin/derp; shell, for this audit) and
# they must send the same shape. B265 fixed the Go one with the reason written in its
# comment; the shell one repeated the bug months later, and that is the whole cost of this
# block. A parity contract is the only thing that keeps them honest together.
go_shape="$(grep -c 'stunSoftwareValue.*=.*"tailnode"' internal/feature/admin/derp_stun.go 2>/dev/null)"
sh_shape="$(grep -c 'tailnode' scripts/lib/stun_probe.py 2>/dev/null)"
if [ "${go_shape:-0}" -ge 1 ] && [ "${sh_shape:-0}" -ge 1 ]; then
    ok "A9: both STUN implementations send SOFTWARE \"tailnode\" (Go since B265, shell now)"
else
    bad "A9: the two STUN implementations disagree (go=${go_shape:-0}, sh=${sh_shape:-0}) — one of them will report a healthy relay as closed"
fi
if grep -q 'ErrNoFingerprint' internal/feature/admin/derp_stun.go 2>/dev/null; then
    ok "A10: the Go probe still records WHY the shape matters (ErrNoFingerprint)"
else
    bad "A10: the Go probe lost the explanation — the next implementation will repeat it"
fi

# ═══════════════ B. installer parity ═══════════════
if grep -q '443/udp' "$FW" 2>/dev/null; then
    ok "B1: the firewall installer plans 443/udp (HTTP/3 — the port that was missing)"
else
    bad "B1: the installer still omits 443/udp — a rebuilt host would recreate the gap"
fi
if bash "$FW" --dry-run 2>/dev/null | grep -q '443/udp'; then
    ok "B2: the dry-run plan really prints it (not just a comment)"
else
    bad "B2: the dry-run plan does not include 443/udp"
fi
if grep -q '\-\-no-http3' "$FW" 2>/dev/null; then
    ok "B3: a host that pins HTTP/2 can opt out (--no-http3)"
else
    bad "B3: no way to opt out of 443/udp"
fi
# Parity: every port the audit requires must appear in the installer's plan, otherwise the
# two lists drift and the next host is provisioned from the wrong one.
drift=""
for p in 80/tcp 443/tcp 443/udp 8443/tcp 3478/udp; do
    grep -q "$p" "$AUDIT" 2>/dev/null || continue
    case "$p" in
        8443/tcp|3478/udp) grep -q 'DERP_PORT\|STUN_PORT' "$FW" 2>/dev/null || drift="$drift $p" ;;
        *) grep -q "$p" "$FW" 2>/dev/null || drift="$drift $p" ;;
    esac
done
if [ -z "$drift" ]; then
    ok "B4: the installer covers every port the audit expects (no drift between the lists)"
else
    bad "B4: the installer cannot produce:$drift"
fi

# ═══════════════ C. the device diagnostic ═══════════════
miss=""
for layer in control path disco acl relays; do
    grep -q "\"$layer\"" "$DEV" 2>/dev/null || grep -q "\b$layer\b" "$DEV" 2>/dev/null || miss="$miss $layer"
done
if [ -z "$miss" ]; then
    ok "C1: all five layers are reported (control / path / disco / acl / relays)"
else
    bad "C1: the diagnostic does not report:$miss"
fi
if grep -q 'no disco reply' "$DEV" 2>/dev/null; then
    ok "C2: a missing data path is a gap, not a footnote"
else
    bad "C2: the diagnostic can call a silent device healthy"
fi
if grep -qE 'online but silent' "$DEV" 2>/dev/null; then
    ok "C3: «online in the control plane» is not accepted as «working»"
else
    bad "C3: a stale control session would pass"
fi
if grep -qE 'ufw (allow|delete|deny)' "$DEV" 2>/dev/null; then
    bad "C4: the device diagnostic modifies the host"
else
    ok "C4: the device diagnostic only reads"
fi

# ═══════════════ D. written down ═══════════════
if grep -q 'port_audit' docs/troubleshooting.md 2>/dev/null; then
    ok "D1: troubleshooting names the port audit"
else
    bad "D1: the operator's guide does not mention the audit"
fi
if grep -q 'not_stun' docs/troubleshooting.md 2>/dev/null; then
    ok "D2: troubleshooting records the prober's own failure mode (the not_stun rule)"
else
    bad "D2: nothing explains why a silent probe may be the prober's fault"
fi
# The port must be documented where an operator provisions a host: the install guide
# (both languages) and the Caddy template they read while configuring the proxy.
for doc in docs/INSTALL.md docs/ru/INSTALL.md; do
    if grep -q '443/udp\|443` | \*\*udp\*\*\|443.*udp' "$doc" 2>/dev/null; then
        ok "D3: $doc lists UDP/443 next to TCP/443"
    else
        bad "D3: $doc never mentions UDP/443 — the exact pair that gets forgotten"
    fi
done
if grep -q 'udp/443\|443/udp\|UDP/443' deploy/caddy/native/Caddyfile.tmpl 2>/dev/null; then
    ok "D4: the Caddy template explains why UDP/443 matters (trap 5)"
else
    bad "D4: the Caddy template does not mention UDP/443"
fi
if grep -q '443/udp' deploy/install-caddy-native.sh 2>/dev/null; then
    ok "D5: the Caddy installer prints the firewall requirement"
else
    bad "D5: the Caddy installer stays silent about the UDP rule"
fi
# Provenance + the half-broken relay: the derper installer must pin a version and
# verify STUN, because a relay that binds 3478 and never answers looks healthy.
if grep -q 'DERP_VERSION' deploy/derp-init.sh 2>/dev/null; then
    ok "D6: the derper installer pins its version (no moving @latest)"
else
    bad "D6: the derper installer still builds @latest"
fi
if grep -q 'STUN probe' deploy/derp-init.sh 2>/dev/null; then
    ok "D7: it verifies STUN after starting the relay, instead of trusting «listening»"
else
    bad "D7: a silent STUN would ship unnoticed"
fi

# ═══════════════ E. discoverability ═══════════════
if grep -q 'check_b500_ports_and_path.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "E1: registered in the catalog"
else
    bad "E1: not registered in scripts/verify_pre_deploy.sh"
fi
for f in scripts/check_b500_ports_and_path.sh "$AUDIT" "$DEV"; do
    if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
        ok "E2: git tracks $f (trap #11)"
    elif git check-ignore -q "$f" 2>/dev/null; then
        bad "E2: $f is IGNORED by .gitignore (trap #11)"
    else
        ok "E2: $f is not ignored (untracked until the release commit)"
    fi
done
if grep -q '^\- \*\*B500\*\*' AGENTS.md 2>/dev/null; then ok "E3: the AGENTS index carries its line"; else bad "E3: no AGENTS index line"; fi
if grep -q 'B500' docs/ROADMAP.md 2>/dev/null; then ok "E4: the roadmap records it"; else bad "E4: the roadmap does not mention B500"; fi
if grep -q 'B500' RELEASE-NOTES.md 2>/dev/null; then ok "E5: the release notes describe it"; else bad "E5: no release-notes entry"; fi

# ═══════════════ F. behaviour (labelled) ═══════════════
if command -v ss >/dev/null 2>&1; then
    out="$(bash "$AUDIT" 2>&1)"; rc=$?
    case "$out" in
        *"PORT"*"STATE"*) ok "F1: the audit runs here and prints its table (rc=$rc)" ;;
        *) bad "F1: the audit produced no table: ${out:0:160}" ;;
    esac
else
    skip "F1: no ss on this host — the port audit cannot inspect listeners here (run it on the panel host)"
fi

echo
echo "  B500: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
