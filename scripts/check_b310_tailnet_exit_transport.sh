#!/usr/bin/env bash
# check_b310_tailnet_exit_transport.sh
#
# 2026-09-23 (B310, v1.5.75) — SSH to an exit node must work OVER THE TAILNET, and
# when it cannot, skygate must say why instead of timing out.
#
# LIVE CAUSE (agent VM, measured 2026-09-23)
#
#   The operator reported that `karolina` had been blocked ("не пингуется"). skygate's
#   route sync pointed at the relay's TAILNET address — the right idea, because that
#   path survives a provider-level block of the public IP — and yet it could never
#   work on this host, with nothing anywhere saying so:
#
#     $ ip route get 100.64.0.2
#     100.64.0.2 via 192.168.13.1 dev ens18 src 192.168.13.10      # the LAN gateway!
#     $ tailscale status
#     failed to connect to local tailscaled; it doesn't appear to be running
#     $ docker exec skygate-skygate-1 tailscale status
#     failed to connect to local tailscaled; it doesn't appear to be running
#     $ docker exec skygate-skygate-1 sh -c 'ssh -i /ssh-sync/skygate_sync -p 18022 root@100.64.0.2 …'
#     ssh: connect to host 100.64.0.2 port 18022: Operation timed out
#
#   Neither the host nor the container is in the tailnet (SKYGATE_TS_AUTHKEY_FILE=/dev/null,
#   container Tailscale disabled), so every packet to 100.64.0.0/10 left through the LAN
#   gateway. B292's "use the live Tailscale IP" fallback then LOOKED like an automatic
#   repair while being structurally impossible: the page showed an IP, the sync showed a
#   timeout, and "the node is down" was indistinguishable from "skygate is not on the
#   network it is trying to use".
#
# CONTRACTS
#   A. every way to reach a relay is data (tailnet / public / name), and a tailnet
#      address is recognised as such
#   B. the ladder probes each candidate and uses the first that works, recording the
#      transport that carried the routes
#   C. skygate's OWN tailnet presence is checked for real: a kernel interface is
#      required, and every "why not" is named (not running / needs login / userspace)
#   d. the failure names EVERY candidate and the unavailable tailnet path, so an
#      ssh timeout can no longer be mistaken for a relay problem
#   E. onboarding hands skygate's own public key to the new relay (idempotently, and
#      only for a well-formed OpenSSH key) and the page says which key was embedded
#   F. /admin/exit-nodes shows the path management is using and warns when skygate is
#      not on the tailnet, with a link to enable it
#   G. tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B310: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LADDER=internal/feature/exit_rules/relay_transport_tailnet_b310.go
SYNC=internal/feature/exit_rules/sync.go
STATE=internal/feature/exit_rules/relay_transport_b309.go
REG=internal/feature/admin/exit_node_register.go
ADMIN=internal/feature/admin/exit_nodes.go
TPL=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go
TEST=internal/feature/exit_rules/relay_transport_tailnet_b310_test.go

hdr "B310 — management of exit nodes over the tailnet (and a named failure when it cannot)"

# --- A: the candidates are data ------------------------------------------------
if grep -q 'func relaySSHEndpoints(' "$LADDER"; then
  ok "A1: relaySSHEndpoints builds the candidate list"
else
  bad "A1: the endpoint list is missing"
fi
if grep -q 'func IsTailnetAddress(' "$LADDER" && grep -q 'ip4\[0\] == 100 && ip4\[1\] >= 64' "$LADDER"; then
  ok "A2: a tailnet address (100.64.0.0/10) is recognised"
else
  bad "A2: the tailnet range check is missing"
fi
if grep -q 'RelayEndpointTailnet = "tailnet"' "$LADDER" \
   && grep -q 'RelayEndpointPublic  = "public"' "$LADDER" \
   && grep -q 'RelayEndpointName    = "name"' "$LADDER"; then
  ok "A3: the three transports are named constants (they are recorded and rendered)"
else
  bad "A3: the transport constants are missing"
fi
if grep -q 'headscale reports this tailnet address for the relay' "$LADDER" \
   && grep -q 'operator ssh_target' "$LADDER" \
   && grep -q 'last resort: the node name' "$LADDER"; then
  ok "A4: every candidate says WHERE it came from"
else
  bad "A4: a candidate without a provenance cannot be explained to the operator"
fi

# --- B: the ladder --------------------------------------------------------------
if grep -q 'func applyRoutesOverSSHLadder(' "$LADDER" && grep -q 'ProbeRelayEndpoint(ep, relayProbeTimeout)' "$LADDER"; then
  ok "B1: every candidate is probed before ssh runs"
else
  bad "B1: the ladder does not probe its candidates"
fi
calls=$(grep -cE '\.SetAdvertisedRoutes\(' "$LADDER" || true)
if [ "${calls:-0}" -eq 1 ]; then
  ok "B2: exactly one SetAdvertisedRoutes call site (B300's single-tail property holds)"
else
  bad "B2: SetAdvertisedRoutes appears ${calls:-0} time(s) in the ladder, want 1"
fi
if grep -q 'routes applied over the %s transport' "$LADDER"; then
  ok "B3: the transport that carried the routes is logged"
else
  bad "B3: using a transport is silent"
fi
if grep -q 'RelayApplyViaKey' "$STATE" && grep -q 'SettingRelayApplyViaPrefix = "relay_apply_via:"' "$STATE"; then
  ok "B4: the transport used is recorded per relay (own key, so B309's error text is never re-parsed)"
else
  bad "B4: the transport is not persisted"
fi
if grep -q 'out.Label = "ssh=ok via " + ladder.Via' "$SYNC" && grep -q 'out.Via = ladder.Via' "$SYNC"; then
  ok "B5: the sync reports which transport ran"
else
  bad "B5: the sync does not carry the transport into its outcome"
fi

# --- C: skygate's own tailnet presence ------------------------------------------
if grep -q 'func SkygateTailnetState()' "$LADDER" && grep -q 'func detectTailnetState()' "$LADDER"; then
  ok "C1: skygate's own tailnet presence is checked"
else
  bad "C1: nothing checks whether skygate can reach the tailnet at all"
fi
if grep -q 'IsTailnetAddress(ip.String())' "$LADDER" && grep -q 'ifc.Name' "$LADDER"; then
  ok "C2: a usable KERNEL path (an interface with a tailnet address) is required"
else
  bad "C2: the check does not prove a route exists"
fi
for reason in 'tailscaled is not running' 'NOT logged in (NeedsLogin)' 'userspace-networking mode cannot carry a plain ssh' 'no tailnet interface and no tailscale binary'; do
  if grep -qF "$reason" "$LADDER"; then
    ok "C3: the reason is named: $reason"
  else
    bad "C3: the reason is not named: $reason"
  fi
done
if grep -q 'tailnetStateTTL' "$LADDER"; then
  ok "C4: the check is cached (the staggered sync walks every relay)"
else
  bad "C4: the check runs per relay with no cache"
fi

# --- D: the failure names everything --------------------------------------------
if grep -q 'is not answering: ' "$LADDER" && grep -q 'answered but the routes could not be applied' "$LADDER"; then
  ok "D1: each attempt is reported with its own reason"
else
  bad "D1: the ladder does not report per-candidate reasons"
fi
if grep -q 'skygate itself is not on the tailnet, so %s is unlikely to answer' "$LADDER"; then
  ok "D2: the unavailable tailnet path is named (the live failure mode)"
else
  bad "D2: a tailnet address that cannot work is still silent"
fi
if grep -q 'parts := append(\[\]string{}, ladder.Attempts...)' "$SYNC" \
   && grep -q 'out.Label = "ssh=err=" + strings.Join(parts, "; ")' "$SYNC"; then
  ok "D3: the ssh failure carries every attempt AND the notes"
else
  bad "D3: the failure message loses the notes"
fi
if grep -q 'targetSource' internal/headscale/routes.go; then
  ok "D4: B292's 'target from …' diagnosis is intact for the single-target path"
else
  bad "D4: B292's diagnosis was removed"
fi

# --- E: onboarding hands over the key -------------------------------------------
if grep -q 'func exitNodeAuthorizedKeyStep(' "$REG" && grep -q 'authorized_keys' "$REG"; then
  ok "E1: the register command installs skygate's management key"
else
  bad "E1: a new relay is not given skygate's key"
fi
if grep -q "grep -qF '" "$REG"; then
  ok "E2: the step is idempotent (a re-run does not duplicate the key)"
else
  bad "E2: the authorized_keys step is not idempotent"
fi
if grep -q 'func sanitizeSSHPublicKey(' "$REG" && grep -q 'func isSSHPublicKey(' "$REG"; then
  ok "E3: only a well-formed OpenSSH public key is rendered into a root shell"
else
  bad "E3: the key that goes into the pasted command is not validated"
fi
if grep -q 'func (s \*Service) managementSSHPublicKey()' "$REG" && grep -q '/ssh-sync/skygate_sync.pub' "$REG"; then
  ok "E4: the key is read from the configured path or the deployed defaults"
else
  bad "E4: the management key is never read"
fi

# --- F: the page ------------------------------------------------------------------
if grep -q 'TailnetReady' "$ADMIN" && grep -q 'TailnetReason' "$ADMIN"; then
  ok "F1: the page carries skygate's tailnet state"
else
  bad "F1: the page cannot say whether the tailnet path is usable"
fi
if grep -q 'exit_rules.SkygateTailnetState()' "$ADMIN"; then
  ok "F2: the state comes from the shared check (one answer, not two)"
else
  bad "F2: the page invents its own tailnet verdict"
fi
if grep -q 'TransportPaths' "$TPL" && grep -q 'tailnet_missing' "$TPL"; then
  ok "F3: the template shows the transport in use and warns when the tailnet is unusable"
else
  bad "F3: the transport is not rendered"
fi
if grep -q 'href="/admin/tailscale"' "$TPL"; then
  ok "F4: the warning links to the page that fixes it"
else
  bad "F4: the warning has no way to act on it"
fi
for k in tailnet_ok tailnet_missing tailnet_missing_help tailnet_enable transport_paths register_mgmt_key register_mgmt_key_missing; do
  c="$(grep -cF "\"exit_nodes.prefix_owner.$k\"" "$I18N" 2>/dev/null || true)"
  c2="$(grep -cF "\"exit_nodes.$k\"" "$I18N" 2>/dev/null || true)"
  total=$(( ${c:-0} + ${c2:-0} ))
  if [ "$total" -eq 2 ]; then
    ok "F5: i18n key present exactly once per map: $k"
  else
    bad "F5: i18n key $k appears $total time(s), want 2 (RU+EN)"
  fi
done

# --- G: tests + git ----------------------------------------------------------------
if [ -f "$TEST" ]; then ok "G1: $TEST exists"; else bad "G1: $TEST is missing"; fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ ./internal/feature/admin/ -run 'B310|ExitNode' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "G2: the B310 tests pass"
  else
    bad "G2: the B310 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "G2: go not on PATH — run the B310 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b310_tailnet_exit_transport.sh >/dev/null 2>&1; then
  ok "G3: scripts/check_b310_tailnet_exit_transport.sh is tracked by git"
else
  bad "G3: scripts/check_b310_tailnet_exit_transport.sh is NOT tracked"
fi

printf '\n\033[1mB310 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
