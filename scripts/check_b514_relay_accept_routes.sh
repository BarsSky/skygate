#!/usr/bin/env bash
# check_b514_relay_accept_routes.sh — B514 (2026-11-22)
#
# THE LOOP. Measured live: a 5 MB download with exit=emilia took 25 s and left with
# KAROLINA's address while BOTH relays' counters grew (emilia +7.8 MB, karolina +16.3 MB);
# with exit=karolina it took 1 s and only karolina moved. Every relay had
# exit_servers.accept_routes=1, and the sync faithfully passed --accept-routes=true, so a
# relay installed the tailnet's 0.0.0.0/0 route and forwarded arriving exit traffic BACK
# into the tailnet to the primary instead of out of its own uplink.
#
# The rule pinned here: a relay that ADVERTISES routes must never ACCEPT them. The stored
# tri-state is not trusted for relays; the sync always passes --accept-routes=false, and the
# provisioning/registration snippets no longer write `--accept-routes` (true).

set -uo pipefail
PASS=0; FAIL=0; SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT" || exit 1
HELP=internal/feature/exit_rules/relay_accept_routes_b514.go
SYNC=internal/feature/exit_rules/sync_routes.go
PROV=internal/exitnode/provision_b412.go
REG=internal/feature/admin/exit_node_register.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
TRBL=docs/troubleshooting.md

for f in "$HELP" "$SYNC" "$PROV" "$REG" "$GATE" "$AGENTS" "$NOTES" "$TRBL"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B514 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

echo "[A] the sync forces it"
case "$(cat "$HELP")" in
  *'func relayAcceptRoutesSetting() int { return -1 }'*)
    ok "A1: the relay tri-state is forced to -1 (--accept-routes=false)" ;;
  *) bad "A1: no forced relay tri-state" ;;
esac
case "$(cat "$SYNC")" in
  *'ApplyRoutesLocally(kept, relayAcceptRoutesSetting())'*)
    ok "A2: the local transport passes the forced value" ;;
  *) bad "A2: the local transport still passes the stored value" ;;
esac
case "$(cat "$SYNC")" in
  *'applyRoutesOverSSHLadder(hs, node, approveRoutes, relayAcceptRoutesSetting()'*)
    ok "A3: the SSH transport passes the forced value" ;;
  *) bad "A3: the SSH transport still passes the stored value" ;;
esac
if grep -q 'lookupAcceptRoutes(node)' "$SYNC"; then
  bad "A4: a call site still honours the stored value (a DB row of 1 reintroduces the loop)"
else
  ok "A4: no relay call site honours the stored accept-routes value"
fi

echo "[B] the sources of the value are fixed"
case "$(cat "$PROV" "$REG")" in
  *'--advertise-exit-node --accept-routes --ssh'*)
    bad "B1: a provisioning/registration snippet still writes --accept-routes (true)" ;;
  *) ok "B1: no snippet writes --accept-routes (true) any more)" ;;
esac
case "$(cat "$PROV" "$REG")" in
  *'--accept-routes=false'*)
    ok "B2: the snippets carry --accept-routes=false explicitly" ;;
  *) bad "B2: the snippets do not state the value" ;;
esac

echo "[C] behaviour"
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ ./internal/exitnode/ -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "C1: the relay-route packages are green with the forced flag (B293/B309/B310/B370 contracts intact)"
  else
    bad "C1: go test failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
else
  skip "C1: no Go toolchain on PATH"
fi

echo "[D] registry"
case "$(cat "$GATE")" in *'run_check "B514"'*) ok "D1: registered in the gate" ;; *) bad "D1: not registered" ;; esac
case "$(cat "$AGENTS")" in *"**B514**"*) ok "D2: AGENTS.md block index" ;; *) bad "D2: no AGENTS.md entry" ;; esac
case "$(cat "$NOTES")" in *"B514"*) ok "D3: release notes explain it" ;; *) bad "D3: no release-notes entry" ;; esac
case "$(cat "$TRBL")" in *"B514"*) ok "D4: troubleshooting carries the procedure" ;; *) bad "D4: no troubleshooting entry" ;; esac

echo
echo "B514 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
