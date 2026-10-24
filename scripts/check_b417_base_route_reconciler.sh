#!/usr/bin/env bash
# check_b417_base_route_reconciler.sh
#
# 2026-10-24 (B417) — SKYGATE MUST FINISH WHAT IT STARTED, WITHOUT A BUTTON.
#
# After the B416 run lost its last step to a proxy timeout, the operator was left with
# a relay that advertised `0.0.0.0/0` and a red banner telling him to press «Tag as
# exit-node». Turning a VPS into a working exit node is skygate's job, so a periodic
# pass now approves the missing BASE routes itself.
#
# SCOPE — decided by the operator on 2026-10-24, and this file is where that decision
# is enforced:
#   * ONLY 0.0.0.0/0 and ::/0 (never a subnet route — those remain an operator action);
#   * ONLY nodes with a row in exit_servers (i.e. relays skygate itself registered);
#   * ONLY when the node is online, advertises a base route, and headscale has not
#     approved it;
#   * the repair is audited (`exit_node_base_route_autorepair`) and announced through
#     the notifier, so a silent auto-action can never happen.
#
# CONTRACTS (15)
#   A  the scope guard: exactly two base routes, the pure decision refuses offline /
#      foreign / unregistered / already-approved nodes, and it is unit-tested
#   B  the pass: reads exit_servers, approves through ApproveRoutesForNodeID, audits
#      and notifies; failures are rate-limited in the log
#   C  it runs on its own: a ticker started from main, 2-minute interval
#   D  registered in the catalog, indexed in AGENTS.md, tracked by git

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b417.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

REC=internal/feature/admin/exit_nodes_reconcile_b417.go
GATE=scripts/verify_pre_deploy.sh

for f in "$REC" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# 2026-10-24 (B339, refactor Phase D): the start call is read from the cmd/skygate
# PACKAGE surface, not from main.go — the reconciler is an optional background
# scheduler, so it belongs in main_schedulers.go beside the other five, and pinning a
# single file would turn that move (or any future split) into a false FAIL.
ROOT="$(pwd)"
# shellcheck source=scripts/lib/gosurface.sh
. "$ROOT/scripts/lib/gosurface.sh"
if ! gosurface CMD_GO "$ROOT"/cmd/skygate/*.go; then
  bad "could not build the cmd/skygate surface (scripts/lib/gosurface.sh)"
  exit 1
fi

# --- A: the scope -----------------------------------------------------------
hdr "A. the auto-repair may approve ONLY the base routes of skygate's own relays"
if grep -q 'var baseRoutes = \[\]string{"0.0.0.0/0", "::/0"}' "$REC"; then
  ok "A1: exactly the two base routes are in scope"
else
  bad "A1: baseRoutes is not the exact pair — a subnet could be auto-approved"
fi
if grep -q 'func baseRouteRepairDecision' "$REC" && grep -q 'baseRouteRepairDecision(row.Hostname, n)' "$REC"; then
  ok "A2: the decision is a pure function AND the pass uses it"
else
  bad "A2: the decision is inlined (untestable) or unused"
fi
if grep -q 'db.ListExitServers' "$REC"; then
  ok "A3: the scope comes from exit_servers (nodes skygate registered)"
else
  bad "A3: the pass does not restrict itself to exit_servers rows"
fi
if grep -q '!n.Online' "$REC" && grep -q 'strings.EqualFold' "$REC"; then
  ok "A4: an offline or differently-named node is never repaired"
else
  bad "A4: the online / hostname guards are missing"
fi
OUT="$(go test ./internal/feature/admin/ -run 'B417' -count=1 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
  ok "A5: the decision is unit-tested (approved / offline / foreign / unregistered / subnet-only / empty)"
else
  bad "A5: go test on the B417 decision failed (rc=$rc): $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

# --- B: the pass ------------------------------------------------------------
hdr "B. the pass repairs, records and announces"
if grep -q 'ApproveRoutesForNodeID(id, baseRoutes)' "$REC"; then
  ok "B1: the repair uses the same headscale call the manual button uses"
else
  bad "B1: the repair does not call ApproveRoutesForNodeID with baseRoutes"
fi
if grep -q 'exit_node_base_route_autorepair' "$REC"; then
  ok "B2: every repair (and every failure) leaves an audit row"
else
  bad "B2: the auto-repair is silent in the audit log"
fi
if grep -q 'notifyBaseRouteRepair' "$REC" && grep -q 'SendAlert' "$REC"; then
  ok "B3: the operator is told when skygate repaired routes on its own"
else
  bad "B3: the auto-repair is invisible to the operator"
fi
if grep -q 'baseRouteRepairErrorInterval' "$REC" && grep -q 'shouldLog' "$REC"; then
  ok "B4: a permanently broken relay does not write a log line every tick"
else
  bad "B4: failures would flood the log"
fi

# --- C: it runs by itself ---------------------------------------------------
hdr "C. it runs without anyone pressing anything"
if grep -q 'StartBaseRouteReconciler' "$CMD_GO"; then
  ok "C1: the cmd/skygate surface starts the reconciler (main_schedulers.go, beside the other schedulers)"
else
  bad "C1: nothing starts the reconciler — the pass would never run"
fi
if grep -q 'BaseRouteReconcileInterval = 2 \* time.Minute' "$REC"; then
  ok "C2: the interval is two minutes (announced on the card)"
else
  bad "C2: the interval is not the documented two minutes"
fi
if grep -q 'time.NewTicker(BaseRouteReconcileInterval)' "$REC"; then
  ok "C3: a ticker drives it (the same shape as the other background passes)"
else
  bad "C3: no ticker"
fi

# --- D: registration --------------------------------------------------------
hdr "D. registration"
if grep -q 'run_check "B417"' "$GATE"; then
  ok "D1: B417 is registered in the catalog"
else
  bad "D1: B417 is not registered — this contract would never run"
fi
if grep -q '\*\*B417\*\*' AGENTS.md; then
  ok "D2: AGENTS.md block index carries B417"
else
  bad "D2: AGENTS.md has no B417 entry"
fi
if git ls-files --error-unmatch scripts/check_b417_base_route_reconciler.sh >/dev/null 2>&1; then
  ok "D3: this script is tracked by git (trap 11)"
else
  bad "D3: this script is not tracked by git"
fi

printf '\n\033[1mB417 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
