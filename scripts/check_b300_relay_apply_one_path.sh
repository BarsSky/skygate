#!/usr/bin/env bash
# check_b300_relay_apply_one_path.sh
#
# 2026-09-23 (B300) — one transport decision for every sync path.
#
# Live on `aro`, whose journal showed the per-node fixes were simply not on the
# path that runs:
#
#   staggeredSync(aggregated): exit-node-vps SSH err: ssh exit-node-vps (target from the
#     node name (exit_servers has neither ssh_target nor tailscale_ip for this relay),
#     key /var/lib/skygate/ssh/id_ed25519): ssh: Could not resolve hostname exit-node-vps
#
# while the host itself proved the relay IS local:
#
#   SELF exit-node-vps ['100.64.0.1', 'fd7a:115c:a1e0::1']      (tailscale status --json)
#   ROW  ('exit-node-vps', '', '')                              (exit_servers: both columns empty)
#   ID 1 | exit-node-vps | 100.64.0.1 | online                  (headscale nodes list)
#
# Two sync paths configure the same relay: the per-node one (`syncOneExitNode`,
# used by SyncAdvertisedRoutes / SyncAdvertisedRoutesForNode and the per-row
# Re-sync button) and the aggregated one (`staggeredSync(aggregated)`, which is
# what the periodic tick and the domain auto-updater actually run). The aggregated
# path carried its own SHORTER copy of the body: it never asked
# `DetectRelayPlacement` (B293) and never ran B292's target repair, so every tick
# handed `SetAdvertisedRoutes` an empty target, ssh fell back to the bare node
# name, and the local transport — which the evidence proves would match — was
# never considered. `routes-apply.status`/`.log` were never created, and every
# prefix stayed «нет маршрута».
#
# CONTRACTS
#   A. exactly ONE route-application tail: one SetAdvertisedRoutes call site and
#      BOTH sync paths call the shared helper
#   B. that tail contains both rungs — locality evidence (B293), the local apply
#      through the privilege ladder, B292's target repair and the approval
#   C. the previously SILENT case is named: a readable daemon that says "not
#      local" now logs its evidence
#   D. the contracts this refactor could have weakened are still intact, the
#      package tests pass, and this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B300: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SYNC=internal/feature/exit_rules/sync.go

hdr "B300 — one transport decision for every sync path"

# --- A: exactly one tail ------------------------------------------------------
if grep -q '^func applyRoutesToRelay(' "$SYNC"; then
  ok "A1: the shared tail applyRoutesToRelay exists"
else
  bad "A1: applyRoutesToRelay is missing"
fi
ssh_calls=$(grep -cE '\.SetAdvertisedRoutes\(' "$SYNC" || true)
if [ "${ssh_calls:-0}" -eq 1 ]; then
  ok "A2: exactly ONE SetAdvertisedRoutes call site (the shared tail) — a second copy is how the two paths drifted"
else
  bad "A2: SetAdvertisedRoutes is called from $ssh_calls places in sync.go, want 1"
fi
stray=$(grep -cE 's\.HS\.SetAdvertisedRoutes\(' "$SYNC" || true)
if [ "${stray:-0}" -eq 0 ]; then
  ok "A3: the aggregated loop no longer calls the SSH transport directly"
else
  bad "A3: the aggregated loop still calls SetAdvertisedRoutes itself ($stray occurrence(s))"
fi
if grep -q 'applyRoutesToRelay(hs, d, lookupAcceptRoutes, defaultKeyPath, node, approveRoutes)' "$SYNC"; then
  ok "A4: the per-node path (syncOneExitNode) uses the shared tail"
else
  bad "A4: the per-node path does not use the shared tail"
fi
if grep -q 'applyRoutesToRelay(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, n.name, batch)' "$SYNC"; then
  ok "A5: the aggregated staggered path uses the SAME tail (the path the periodic tick runs)"
else
  bad "A5: the aggregated path still has its own shorter body"
fi
approve_calls=$(grep -cE '\.ApproveAllRoutesWithList\(' "$SYNC" || true)
if [ "${approve_calls:-0}" -eq 1 ]; then
  ok "A6: exactly one approval call site, shared by both transports"
else
  bad "A6: ApproveAllRoutesWithList is called from $approve_calls places, want 1"
fi

# --- B: the tail contains both rungs ------------------------------------------
if grep -q 'headscale.DetectRelayPlacement(' "$SYNC" && grep -q 'placement.Local {' "$SYNC"; then
  ok "B1: the tail asks B293's locality question (evidence chain, not a hostname guess)"
else
  bad "B1: the locality decision is missing from the tail"
fi
if grep -q 'hs.ApplyRoutesLocally(kept, lookupAcceptRoutes(node))' "$SYNC"; then
  ok "B2: a local relay is applied through the privilege ladder (direct → sudo → helper)"
else
  bad "B2: the local transport is missing from the tail"
fi
if grep -q 'exit_servers has no ssh_target and no tailscale_ip' "$SYNC" \
   && grep -q 'SetExitServerTailscaleIPIfEmpty' "$SYNC"; then
  ok "B3: B292's target repair (use the live Tailscale IP AND persist it) is in the tail"
else
  bad "B3: the SSH target repair is missing from the tail — ssh would get the bare node name again"
fi
if grep -q 'headscale.SelfCoveringRoutes(approveRoutes, placement.SelfIPs)' "$SYNC"; then
  ok "B4: the self-covering-route refusal still runs (a co-located relay must not advertise its own LAN)"
else
  bad "B4: the route-loop guard disappeared"
fi
if grep -q 'out.Label = "local=ok via " + transport.Name' "$SYNC" \
   && grep -q 'out.Label = "ssh=err=" + sshErr.Error()' "$SYNC"; then
  ok "B5: both transports report their outcome in the same shape"
else
  bad "B5: the outcome labels are missing"
fi

# --- C: the silent case is named ----------------------------------------------
if grep -q 'the local daemon answered: matched' "$SYNC"; then
  ok "C1: a readable daemon that says NOT local now logs its evidence (it used to be silent)"
else
  bad "C1: 'not local' is still silent — the journal cannot answer 'why ssh?'"
fi
if grep -q 'local daemon unreadable' "$SYNC"; then
  ok "C2: the B293.1 'could not ask' fallback still names its reason"
else
  bad "C2: the unreadable-daemon reason disappeared"
fi
if grep -q 'routes applied locally via' "$SYNC"; then
  ok "C3: a successful local apply still says so (the operator sees 'no SSH involved')"
else
  bad "C3: the local success line disappeared"
fi

# --- D: nothing weakened, tests pass, script tracked --------------------------
weak=""
grep -q 'func syncOneExitNode' "$SYNC" || weak="$weak syncOneExitNode-gone"
grep -q 'DetectRelayPlacement(' "$SYNC" || weak="$weak b293-grep"
grep -q 'syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, OwnedPrefixes(node, routes, owners), result)' "$SYNC" || weak="$weak b274-b2"
grep -q 'func (c \*Client) ApplyRoutesLocally(' internal/headscale/local_apply_b293.go || weak="$weak apply-routes-locally"
if [ -z "$weak" ]; then
  ok "D1: the entry points the B132/B274/B293 contracts pin are all still in place"
else
  bad "D1: the refactor removed something a contract pins:$weak"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "D2: the exit_rules package tests pass (B293/B274/B276/B298 suites included)"
  else
    bad "D2: the exit_rules tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "D2: go not on PATH — run the exit_rules tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b300_relay_apply_one_path.sh >/dev/null 2>&1; then
  ok "D3: scripts/check_b300_relay_apply_one_path.sh is tracked by git"
else
  bad "D3: scripts/check_b300_relay_apply_one_path.sh is NOT tracked"
fi

printf '\n\033[1mB300 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
