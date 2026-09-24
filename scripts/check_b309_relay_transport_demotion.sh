#!/usr/bin/env bash
# check_b309_relay_transport_demotion.sh
#
# 2026-09-23 (B309, v1.5.74) — a relay skygate cannot CONFIGURE must not keep
# owning prefixes.
#
# LIVE CAUSE (agent VM, measured 2026-09-23):
#
#   /admin/exit-nodes carried «владелец не объявляет: 29» and a prefix table full of
#   «нет маршрута». Pressing «Пересобрать и применить ACL» changed nothing, and the
#   journal showed why — the OWNER of those prefixes was unreachable:
#
#     18:21:36 staggeredSync(aggregated): karolina advertising 114 unique routes
#     18:21:38 exit-node sync(karolina): not a local relay (local daemon unreadable) — using the SSH transport
#     18:21:49 staggeredSync(aggregated): karolina SSH err: ssh root@100.64.0.2:18022 … Operation timed out
#     18:21:52 staggeredSync(aggregated): emilia advertised: ssh=ok approved=1 …
#
#   `prefix_owner` kept 75 prefixes on karolina because the healthy set used for
#   assignment was derived from the exit-NODE health table, which is computed from
#   headscale state: an online node skygate cannot SSH into is "healthy" there and can
#   never advertise anything. So the prefixes stayed on a relay that would never serve
#   them, the ACL pinned via=karolina, and the operator saw permanent "нет маршрута"
#   with nothing anywhere saying the owner was unreachable.
#
# CONTRACTS
#   A. every route application records its outcome per relay, in the KV store (no
#      schema change), and an unreadable record never looks like a failure
#   B. BOTH transports count (SSH and local), and an approval failure counts too
#   C. the assignment uses the transport-aware healthy set, and both sync paths write
#      the record (the aggregated staggered path is the one that actually runs)
#   D. the page names the relay, the reason and the age, and says an ACL re-apply
#      cannot fix it
#   E. the exit-node HEALTH table is not rewritten by this feature (B273 stays the
#      single answer to "is this relay up?")
#   F. the tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B309: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

STATE=internal/feature/exit_rules/relay_transport_b309.go
SYNC=internal/feature/exit_rules/sync.go
ADMIN=internal/feature/admin/exit_nodes.go
TPL=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go
TEST=internal/feature/exit_rules/relay_transport_b309_test.go

hdr "B309 — an unreachable relay loses its prefixes (and gets them back when it answers)"

# --- A: the record -------------------------------------------------------------
if grep -q 'SettingRelayApplyStatePrefix = "relay_apply_state:"' "$STATE"; then
  ok "A1: the per-relay key prefix is namespaced"
else
  bad "A1: the key prefix is missing or renamed"
fi
if grep -q 'RelayApplyFailureWindow = 15 \* time.Minute' "$STATE"; then
  ok "A2: the failure window is 15m (three ticks)"
else
  bad "A2: the window changed — a single SSH hiccup could move every prefix"
fi
if grep -q 'db.SetGlobalSetting(d, RelayApplyStateKey(relay)' "$STATE" \
   && grep -q 'db.GetGlobalSetting(d, RelayApplyStateKey(relay)' "$STATE"; then
  ok "A3: the record lives in global_settings (no migration, both dialects)"
else
  bad "A3: the record is not read/written through the KV store"
fi
if grep -q 'must not silently reassign the operator.s prefixes' "$STATE" \
   && grep -q 'return RelayApplyState{}' "$STATE"; then
  ok "A4: an unreadable record means healthy, never a failure"
else
  bad "A4: a storage hiccup could be read as a relay failure"
fi

# --- B: what counts as a failure ------------------------------------------------
if grep -q '"ssh=err=", "local=err="' "$STATE"; then
  ok "B1: BOTH transports count (ssh and local)"
else
  bad "B1: only one transport is treated as a failure"
fi
if grep -q 'out.ApproveErr != nil' "$STATE" && grep -q '"approve failed: "' "$STATE"; then
  ok "B2: an unapprovable route set also stops the relay owning the prefix"
else
  bad "B2: an approval failure would keep the prefix on a relay that cannot serve it"
fi
if grep -q 'if now < st.At {' "$STATE" && grep -q 'return true // clock skew' "$STATE"; then
  ok "B3: a clock step keeps the recorded failure instead of forgetting it"
else
  bad "B3: clock skew handling is gone"
fi

# --- C: wiring ------------------------------------------------------------------
# B312 added the location preference on top of the same healthy set, so either call
# shape satisfies this contract — the property is that the transport-aware set is what
# the assignment receives, never the headscale-only list.
if grep -q 'prefixowner.ReconcileWithPreference(s.dbc(), healthyExitRelaysForAssignment(s.dbc())' "$SYNC" \
   || grep -q 'prefixowner.Reconcile(s.dbc(), healthyExitRelaysForAssignment(s.dbc()))' "$SYNC"; then
  ok "C1: the assignment uses the transport-aware healthy set"
else
  bad "C1: reconcilePrefixOwnership still passes the headscale-only healthy set"
fi
cnt="$(grep -c 'recordRelayApply(' "$SYNC")"
if [ "$cnt" -eq 2 ]; then
  ok "C2: both sync paths record the outcome (per-node + aggregated staggered)"
else
  bad "C2: recordRelayApply appears $cnt time(s) in sync.go, want 2 (the aggregated path is the one that runs)"
fi
if grep -q 'excluded from the healthy set — its last route application failed' "$STATE"; then
  ok "C3: the exclusion is logged with the reason and the age"
else
  bad "C3: the demotion is silent — the operator sees an unexplained reassignment"
fi
if grep -q 'a successful apply must clear the failure record immediately\|returns automatically after the next successful apply' "$STATE" \
   || grep -q 'RelayApplyState{At: now, OK: true}' "$STATE"; then
  ok "C4: a success clears the record (no permanent demotion, nothing to un-block)"
else
  bad "C4: a recovered relay would stay demoted"
fi

# --- D: the page ------------------------------------------------------------------
# B310 renamed the loader (fillTransportFailures → fillTransportState) because it now
# fills BOTH halves: the failures and the transport each relay's last apply used.
if grep -q 's.fillTransportState(&stats, all)' "$ADMIN" || grep -q 's.fillTransportFailures(&stats, all)' "$ADMIN"; then
  ok "D1: the exit-nodes page fills the transport failures"
else
  bad "D1: the page never renders why the prefixes moved"
fi
if grep -q 'range .TransportFailed' "$TPL" && grep -q 'transport_failed_help' "$TPL"; then
  ok "D2: the template lists the relay, its reason and the age"
else
  bad "D2: the banner is not rendered"
fi
for k in transport_failed transport_failed_ago transport_failed_prefixes transport_failed_help; do
  c="$(grep -cF "\"exit_nodes.prefix_owner.$k\"" "$I18N" 2>/dev/null || echo 0)"
  if [ "$c" -eq 2 ]; then
    ok "D3: i18n key present exactly once per map: exit_nodes.prefix_owner.$k"
  else
    bad "D3: i18n key exit_nodes.prefix_owner.$k appears $c time(s), want 2 (RU+EN)"
  fi
done
if grep -q 'не проблема ACL' "$I18N" && grep -q 'not an ACL problem' "$I18N"; then
  ok "D4: the banner says an ACL re-apply cannot fix it (RU+EN)"
else
  bad "D4: the banner does not rule out the ACL explanation"
fi

# --- E: B273 stays the single answer to "is this relay up?" -----------------------
if grep -qE '(UPDATE|INSERT INTO)[[:space:]]+exit_node_health' "$STATE"; then
  bad "E1: B309 writes the health table — a healthy tailnet node that cannot be configured would be reported as DOWN"
else
  ok "E1: the health table is untouched (transport is not health)"
fi

# --- F: tests + git ----------------------------------------------------------------
if [ -f "$TEST" ]; then ok "F1: $TEST exists"; else bad "F1: $TEST is missing"; fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ -run 'B309' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F2: the B309 tests pass"
  else
    bad "F2: the B309 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "F2: go not on PATH — run the B309 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b309_relay_transport_demotion.sh >/dev/null 2>&1; then
  ok "F3: scripts/check_b309_relay_transport_demotion.sh is tracked by git"
else
  bad "F3: scripts/check_b309_relay_transport_demotion.sh is NOT tracked"
fi

printf '\n\033[1mB309 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
