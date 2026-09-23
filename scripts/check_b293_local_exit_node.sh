#!/usr/bin/env bash
# check_b293_local_exit_node.sh
#
# 2026-09-23 (B293) — «тот exit-node расположен на той же машине, что и headscale и
# skygate … в таком случае доступ как таковой не нужен по ssh».
#
# CONFIRMED ON THE HOST (the operator ran it):
#
#   $ tailscale status --json | jq '{Self: {Host: .Self.HostName, IPs: .Self.TailscaleIPs}}'
#   {"Self": {"Host": "exit-node-vps", "IPs": ["100.64.0.1","fd7a:115c:a1e0::1"]}}
#
# So the local tailscaled IS the exit node. Managing it over SSH meant an SSH
# session from the host to itself, through the very tailnet it configures:
# pointless (a key + authorized_keys on the same box) and fragile (it fails
# exactly when the local tailscaled is the thing needing repair).
#
# The operator also asked that this must not depend on privilege:
# «на той машине где запущен skygate пользователь root с sudo доступом … но бывает
# что ставят без и для этого стоит тоже подобрать варианты реализации чтобы не было
# ситуации что нет возможности настроить по причине доступа».
#
# CONTRACTS
#   A. the local-relay decision is made from EVIDENCE (address equality with the
#      live daemon), never from a hostname guess
#   B. the sync applies routes locally for such a relay and keeps SSH otherwise
#   C. the privilege ladder exists (direct → sudo -n → privileged helper) and a
#      real tailscale error is never masked by trying another rung
#   D. the co-location footgun is guarded (never advertise a subnet this host sits
#      inside; the exit-node base routes stay)
#   E. the page shows a local relay as such and stops demanding an SSH key for it
#   F. the Telegram egress advice is corrected for this shape
#   G. the privileged helper ships with units + an idempotent installer
#   H. the regression tests exist and pass; the script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B293: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

NODE=internal/headscale/local_node_b293.go
APPLY=internal/headscale/local_apply_b293.go
ARGS=internal/headscale/route_args.go
SYNC=internal/feature/exit_rules/sync.go
EXIT=internal/feature/admin/exit_nodes.go
TG=internal/feature/admin/telegram.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
TGTMPL=internal/handlers/templates/admin/telegram.html
I18N=internal/i18n/catalog_telegram.go
APPSH=deploy/skygate-apply-routes.sh
INSTSH=deploy/install-routes-helper.sh
INSTCOMMON=deploy/install-common.sh

hdr "B293 — the exit node that IS this host must be managed locally"

# --- A: evidence-based detection ---------------------------------------------
if [ -f "$NODE" ]; then ok "A1: $NODE exists"; else bad "A1: $NODE is missing"; fi
for fn in LocalTailscaleSelf ParseLocalTailscaleStatus IsLocalRelay SelfCoveringRoutes TailscaleCLI; do
  if grep -q "^func $fn(" "$NODE"; then ok "A2.$fn: $fn exists"; else bad "A2.$fn: $fn is missing"; fi
done
if grep -q 'Self.TailscaleIPs' "$NODE" && grep -q 'EqualFold(strings.TrimSpace(theirs), m)' "$NODE"; then
  ok "A3: the decision compares the daemon's OWN addresses with the relay's (exact equality)"
else
  bad "A3: the local-relay decision is not address-based"
fi
if grep -q 'SKYGATE_TAILSCALE_CLI' "$NODE"; then
  ok "A4: the CLI path is overridable (SKYGATE_TAILSCALE_CLI)"
else
  bad "A4: the tailscale CLI path is hardcoded"
fi
if grep -q 'could not ask\|cannot ask the local tailscaled' "$SYNC" "$NODE"; then
  ok "A5: 'I could not ask' is distinguished from 'not the relay' (SSH stays the fallback)"
else
  bad "A5: a failed local probe is not distinguished from a negative answer"
fi

# --- B: the sync picks the transport ------------------------------------------
if grep -q 'headscale.DetectRelayPlacement(' "$SYNC" && grep -q 'placement.Local {' "$SYNC"; then
  ok "B1: the sync asks who this host is and takes the local branch"
else
  bad "B1: the sync never considers the local transport"
fi
if grep -q 'hs.ApplyRoutesLocally(kept, lookupAcceptRoutes(node))' "$SYNC"; then
  ok "B2: a local relay is configured by a LOCAL apply"
else
  bad "B2: the sync still SSHes into a co-located relay"
fi
if grep -q 'hs.SetAdvertisedRoutes(node, approveRoutes, lookupAcceptRoutes(node), sshTarget, sshKeyPath)' "$SYNC"; then
  ok "B3: remote relays keep the SSH transport (unchanged)"
else
  bad "B3: the SSH path was dropped — remote relays would break"
fi
if grep -q 'local=ok via ' "$SYNC" && grep -q 'local=err=' "$SYNC"; then
  ok "B4: the result string names the local transport and its failure"
else
  bad "B4: the local outcome is not reported in the sync result"
fi
if grep -q 'func liveExitNodeIPs(' "$SYNC"; then
  ok "B5: every address headscale reports is considered (IPv4 + IPv6)"
else
  bad "B5: only one address is read, so an IPv6-only match would be missed"
fi

# --- C: the privilege ladder --------------------------------------------------
if grep -q 'func (c \*Client) ApplyRoutesLocally(' "$APPLY"; then
  ok "C1: ApplyRoutesLocally exists"
else
  bad "C1: ApplyRoutesLocally is missing"
fi
for rung in 'direct' '"sudo"' '"helper"'; do
  if grep -q "$rung" "$APPLY"; then ok "C2: the $rung rung is implemented"; else bad "C2: the $rung rung is missing"; fi
done
if grep -qF '[]string{"-n", bin}' "$APPLY"; then
  ok "C3: the sudo rung is non-interactive (-n), so it cannot hang the sync"
else
  bad "C3: the sudo rung could block on a password prompt"
fi
if grep -q 'func isPrivilegeRefusal(' "$APPLY" && grep -q 'a password is required' "$APPLY"; then
  ok "C4: only a PRIVILEGE refusal falls through to the next rung"
else
  bad "C4: the ladder cannot tell a permission refusal from a real failure"
fi
if grep -q 'return LocalTransport{}, out, fmt.Errorf("tailscale set (local, direct)' "$APPLY"; then
  ok "C5: a real tailscale error is surfaced as-is (never masked)"
else
  bad "C5: a real failure can be masked by trying another rung"
fi
if grep -q 'func RoutesFallbackHint(' "$APPLY" && grep -q 'install-routes-helper.sh' "$APPLY" && grep -q 'operator' "$APPLY" && grep -q 'sudoers' "$APPLY"; then
  ok "C6: the failure names all four ways to grant access (root / --operator / sudoers / helper)"
else
  bad "C6: the privilege failure is not actionable"
fi
if grep -q 'func LocalTransports(' "$APPLY"; then
  ok "C7: the page can show which rung will be used"
else
  bad "C7: LocalTransports is missing"
fi
if grep -q 'func RequestRoutesApply(' "$APPLY" && grep -q 'func ReadRoutesApplyStatus(' "$APPLY"; then
  ok "C8: the helper request + verdict files are implemented"
else
  bad "C8: the helper handoff is incomplete"
fi
if grep -q 'func isPlainRoute(' "$APPLY" && grep -q 'not a bare CIDR' "$APPLY"; then
  ok "C9: only bare CIDRs may be staged for a root-run command"
else
  bad "C9: the helper request is not validated"
fi
if grep -q 'routesHelperArmedFn()' "$APPLY" && grep -q 'could not be confirmed installed' "$APPLY"; then
  ok "C10: a staged request is not success when nobody consumes it"
else
  bad "C10: 'queued' can be mistaken for 'applied' (the B288.1 lesson)"
fi

# --- D: the co-location footgun ----------------------------------------------
if grep -q 'func SelfCoveringRoutes(' "$NODE" && grep -q 'func isExitNodeBaseRoute(' "$NODE"; then
  ok "D1: routes that contain this host are filtered, base routes exempt"
else
  bad "D1: a co-located relay can advertise its own network (the documented loop)"
fi
if grep -q 'SelfCoveringRoutes(approveRoutes, placement.SelfIPs)' "$SYNC" && grep -q 'self_subnet_skipped=' "$SYNC"; then
  ok "D2: the sync skips + reports them instead of looping silently"
else
  bad "D2: the loop guard is not applied (or not reported)"
fi

# --- E: the page ---------------------------------------------------------------
if grep -q 'LocalRelay' "$EXIT" && grep -q 'headscale.IsLocalRelay(headscale.LocalSelf{IPs: selfIPs}' "$EXIT"; then
  ok "E1: the page marks the relay(s) this host owns"
else
  bad "E1: the page cannot tell a local relay"
fi
if grep -q 'SSHKeyState = "ok"' "$EXIT" && grep -q 'SSHKeyNote = ""' "$EXIT"; then
  ok "E2: the SSH-key warning is suppressed for a local relay (no permanent false alarm)"
else
  bad "E2: a local relay would keep reporting a missing SSH key"
fi
if grep -q '{{if .LocalRelay}}' "$TMPL" && grep -q 'exit_nodes.local_relay_badge' "$TMPL"; then
  ok "E3: the template renders the local-node badge"
else
  bad "E3: the template does not show a local relay"
fi
RU=$(grep -c '"exit_nodes.local_relay_badge"' internal/i18n/catalog_exit_nodes.go || true)
EN=$(grep -c '"exit_nodes.local_relay_tip"' internal/i18n/catalog_exit_nodes.go || true)
if [ "${RU:-0}" -ge 2 ] && [ "${EN:-0}" -ge 2 ]; then
  ok "E4: the new i18n keys exist in RU and EN"
else
  bad "E4: i18n keys missing (badge=${RU:-0}, tip=${EN:-0}; want 2 each)"
fi
if grep -q 'splitCommaList(nodes\[i\].TailscaleIP)' "$EXIT"; then
  ok "E5: the per-row check looks at every address of the relay"
else
  bad "E5: the per-row check reads a single address"
fi

# --- F: the Telegram egress advice --------------------------------------------
if grep -q 'LocalSelfRelay' "$TG" && grep -q 'LocalSelfText' "$TG"; then
  ok "F1: the egress state distinguishes 'this host's own node' from B265's case"
else
  bad "F1: the egress advice cannot tell the two co-location shapes apart"
fi
if grep -q '{{if .State.Egress.LocalSelfRelay}}' "$TGTMPL" && grep -q 'telegram.egress_local_self' "$TGTMPL"; then
  ok "F2: the page explains that such a relay cannot be an egress detour (and the probe is representative)"
else
  bad "F2: the corrected egress message is not rendered"
fi
if [ "$(grep -c '"telegram.egress_local_self"' "$I18N")" -ge 2 ]; then
  ok "F3: telegram.egress_local_self exists in RU and EN"
else
  bad "F3: telegram.egress_local_self is missing a language"
fi

# --- G: the privileged helper --------------------------------------------------
if [ -f "$APPSH" ]; then ok "G1: $APPSH exists"; else bad "G1: $APPSH is missing"; fi
if bash -n "$APPSH" 2>/dev/null; then ok "G2: $APPSH parses"; else bad "G2: $APPSH has a syntax error"; fi
if grep -q 'ACCEPT_ROUTES' "$APPSH" && grep -q 'grep -Eq' "$APPSH" && grep -q 'refusing route' "$APPSH"; then
  ok "G3: the applier re-validates the request and passes it as ONE argv element"
else
  bad "G3: the applier trusts the request file"
fi
if grep -q 'status_write ok' "$APPSH" && grep -q 'status_write failed' "$APPSH"; then
  ok "G4: the applier records its verdict for skygate to read"
else
  bad "G4: the applier has no verdict file"
fi
if [ -f "$INSTSH" ] && bash -n "$INSTSH" 2>/dev/null; then
  ok "G5: $INSTSH exists and parses (idempotent re-install on an existing host)"
else
  bad "G5: $INSTSH is missing or does not parse"
fi
if grep -q '^write_routes_units()' "$INSTCOMMON" && grep -q 'write_routes_units "\$update_dir"' "$INSTCOMMON"; then
  ok "G6: the installer writes the routes units (single source of truth)"
else
  bad "G6: install-common.sh does not install the routes helper"
fi
if grep -q 'skygate-routes.path' "$INSTCOMMON" && grep -q 'PathExists=${update_dir}/routes.request.props' "$INSTCOMMON"; then
  ok "G7: the path unit watches the same file skygate writes"
else
  bad "G7: the path unit watches a different path than skygate stages"
fi

# --- I: the privilege-free fallback (B293.1) ----------------------------------
# The live follow-up: «команда ничего не дала» — the native install runs skygate as
# an unprivileged service user and tailscaled's socket is root-owned unless
# `--operator` was granted, so `tailscale status --json` fails and the daemon-only
# detection left the sync on SSH for a relay that IS this host.
if grep -q '^func LocalInterfaceIPs(' "$NODE" && grep -q 'net.InterfaceAddrs()' "$NODE"; then
  ok "I1: the host's own addresses can be read without touching the daemon"
else
  bad "I1: detection still depends on daemon access (an unprivileged service user cannot see the relay as local)"
fi
if grep -q 'IsLoopback()' "$NODE" && grep -q 'IsLinkLocalUnicast()' "$NODE"; then
  ok "I2: loopback/link-local addresses are excluded (a loopback match would make every relay local)"
else
  bad "I2: the interface probe does not filter loopback"
fi
if grep -q '^func DetectRelayPlacement(' "$NODE" && grep -q 'localSelfFn()' "$NODE" && grep -q 'localIfacesFn()' "$NODE"; then
  ok "I3: the evidence chain is daemon → interfaces (injectable, unit-tested)"
else
  bad "I3: there is no fallback chain between the daemon and the interface list"
fi
if grep -q 'DaemonErr' "$NODE"; then
  ok "I4: why the daemon could not be asked is carried to the log"
else
  bad "I4: a permission failure of the local probe is invisible"
fi
if grep -q 'headscale.DetectRelayPlacement(' "$SYNC" && grep -q 'placement.Evidence' "$SYNC"; then
  ok "I5: the sync uses the chain and names the evidence in its log line"
else
  bad "I5: the sync still uses the daemon-only probe"
fi
if grep -q 'headscale.LocalSelfIPs()' "$EXIT" && grep -q 'headscale.LocalSelfIPs()' "$TG"; then
  ok "I6: the page and the Telegram card use the fallback-aware self addresses"
else
  bad "I6: the pages still require daemon access to recognise a local relay"
fi
if grep -q 'SelfCoveringRoutes(approveRoutes, placement.SelfIPs)' "$SYNC"; then
  ok "I7: the loop guard also works without daemon access"
else
  bad "I7: the loop guard depends on the daemon answer"
fi

# --- H: tests + git ------------------------------------------------------------
for t in internal/headscale/local_node_b293_test.go internal/headscale/local_apply_b293_test.go internal/feature/admin/exit_nodes_b293_test.go; do
  if [ -f "$t" ]; then ok "H1: $t exists"; else bad "H1: $t is missing"; fi
done
if grep -q 'TestDetectRelayPlacement_B293_1' internal/headscale/local_node_b293_test.go 2>/dev/null; then
  ok "H1b: the fallback chain is pinned by a test"
else
  bad "H1b: the fallback chain is not tested"
fi
# H1c: the helper-rung test's fake applier must poll to a DEADLINE. A fixed
# iteration count (200 busy os.Stat calls) can finish before the request is
# staged, so no verdict is ever written and the test fails with "queued for the
# privileged helper (no verdict yet …)" — exactly what happened on CI run
# 35834971383 while the same test passed everywhere else.
if grep -q 'time.Now().Add(6 \* time.Second)' internal/headscale/local_apply_b293_test.go 2>/dev/null \
   && ! grep -q 'for i := 0; i < 200; i++' internal/headscale/local_apply_b293_test.go 2>/dev/null; then
  ok "H1c: the fake applier polls until a deadline (the fixed-count flake cannot return)"
else
  bad "H1c: the fake applier uses a fixed iteration count — the verdict race is back"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ ./internal/feature/admin/ ./internal/feature/exit_rules/ -run 'B293|LocalExitNode|ApplyRoutesLocally|RequestRoutesApply|ReadRoutesApplyStatus|LocalTransports|IsLocalRelay|SelfCoveringRoutes|ParseLocalTailscaleStatus|SplitCommaList|LiveExitNodeIP|ConfigSSHKeyPath' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "H2: the B293 tests pass"
  else
    bad "H2: the B293 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "H2: go not on PATH — run the B293 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b293_local_exit_node.sh >/dev/null 2>&1; then
  ok "H3: scripts/check_b293_local_exit_node.sh is tracked by git"
else
  bad "H3: scripts/check_b293_local_exit_node.sh is NOT tracked"
fi

printf '\n\033[1mB293 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
