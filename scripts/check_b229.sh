#!/usr/bin/env bash
# B-check for B229 (v1.5.2+): preferred-exit
# auto-reconciler. Closes the gap where
# device_rules existed but device_exit_node_prefs
# was empty, leaving per-CIDR grants in the headscale
# ACL without the `via:` clause (Tailscale routed
# through the default exit instead of the operator's
# chosen exit).
#
# Contracts pinned:
#   A:    internal/feature/exit_rules/reconciler.go
#         has the B229 surface (ReconcilerChange,
#         DevicePrefState, PlanDevicePrefChange,
#         ReconcileDeviceExitNodePrefs, the
#         ReconcilerNotifier interface, the rate-
#         limited shouldAlert helper).
#   B:    PlanDevicePrefChange has the 4 outcomes
#         (create / update / skip / no-op) wired to
#         the right Reason values.
#   C:    PreferredExitReconcilerLive reads
#         SKYGATE_PREFERRED_RECONCILER_LIVE (true/1/yes
#         on, anything else off; default off).
#   D:    internal/feature/exit_rules/reconciler_b229_test.go
#         pins the pure decision function + the env
#         reader + the rate-limit helper. 14 Test
#         functions cover all 4 outcomes + env variants.
#   E:    cmd/skygate/main.go wires
#         go app.RunPreferredExitReconciler(ctx, app.Notifier,
#         cfg.PrefReconcileInterval) gated on
#         cfg.PrefReconcileInterval > 0.
#   F:    internal/handlers/handlers.go has
#         RunPreferredExitReconciler wrapper.
#   G:    internal/handlers/handlers.go's
#         exitRulesRunner interface includes
#         ReconcileDeviceExitNodePrefs.
#   H:    internal/config/config.go has
#         PrefReconcileInterval + reads
#         SKYGATE_PREFERRED_RECONCILE_INTERVAL.
#   I:    AGENTS.md mentions B229.
#   J:    go build ./... + go vet ./... + go test on
#         the affected packages all pass.
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }
hasf() { grep -qF -- "$2" "$1" 2>/dev/null; }

RECON="internal/feature/exit_rules/reconciler.go"
TEST="internal/feature/exit_rules/reconciler_b229_test.go"
HANDLERS="internal/handlers/handlers.go"
MAIN="cmd/skygate/main.go"
CFG="internal/config/config.go"
AGENTS="AGENTS.md"

# --- A: reconciler.go has the B229 surface ---
all_a=1
for sym in \
  "type ReconcilerChange struct" \
  "type DevicePrefState struct" \
  "func PlanDevicePrefChange" \
  "func .*Service. ReconcileDeviceExitNodePrefs" \
  "type ReconcilerNotifier interface" \
  "func shouldAlert" \
  "func PreferredExitReconcilerLive"
do
  if ! has "$RECON" "$sym"; then
    echo "  [missing] $sym"
    all_a=0
  fi
done
if [ "$all_a" = "1" ]; then
  ok "A: reconciler.go has the B229 surface (ReconcilerChange, DevicePrefState, PlanDevicePrefChange, ReconcileDeviceExitNodePrefs, ReconcilerNotifier, shouldAlert, PreferredExitReconcilerLive)"
else
  fail "A: reconciler.go missing one or more required symbols"
fi

# --- B: PlanDevicePrefChange has 4 outcomes with right Reason values ---
all_b=1
for reason in 'missing-pref-unanimous' 'missing-pref-split' 'stale-tag' 'via-disabled-but-canonical'; do
  if ! hasf "$RECON" "$reason"; then
    echo "  [missing Reason] $reason"
    all_b=0
  fi
done
if [ "$all_b" = "1" ]; then
  ok "B: PlanDevicePrefChange handles 4 outcomes (create/skip/stale-tag/via-disabled) with right Reason values"
else
  fail "B: PlanDevicePrefChange missing one or more Reason constants"
fi

# --- C: PreferredExitReconcilerLive reads the env ---
if hasf "$RECON" 'SKYGATE_PREFERRED_RECONCILER_LIVE' && \
   grep -A2 'SKYGATE_PREFERRED_RECONCILER_LIVE' "$RECON" | grep -qE '"true"|"1"|"yes"'; then
  ok "C: PreferredExitReconcilerLive reads SKYGATE_PREFERRED_RECONCILER_LIVE (true/1/yes on, anything else off)"
else
  fail "C: PreferredExitReconcilerLive doesn't read SKYGATE_PREFERRED_RECONCILER_LIVE correctly"
fi

# --- D: tests present ---
if [ -f "$TEST" ]; then
  n=$(grep -c "^func Test" "$TEST" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 10 ]; then
    ok "D: B229 unit tests present (${n} Test functions, expected >=10)"
  else
    fail "D: B229 unit tests insufficient (${n} < 10)"
  fi
  for t in \
    "TestPreferredExitReconcilerLive_DefaultOff" \
    "TestPreferredExitReconcilerLive_TrueVariants" \
    "TestPreferredExitReconcilerLive_OtherValuesOff" \
    "TestShouldAlert_RateLimit_1hWindow" \
    "TestShouldAlert_DifferentReasonsAreIndependent" \
    "TestShouldAlert_DifferentHostnamesAreIndependent" \
    "TestPlanDevicePrefChange_Create_MissingPrefUnanimous" \
    "TestPlanDevicePrefChange_Skip_SplitRules" \
    "TestPlanDevicePrefChange_Skip_NoCanonicalTag" \
    "TestPlanDevicePrefChange_Update_StaleTag" \
    "TestPlanDevicePrefChange_Update_ViaDisabledButCanonical" \
    "TestPlanDevicePrefChange_NoOp_CanonicalAndPinned" \
    "TestPlanDevicePrefChange_Skip_HostnameDeleted"; do
    if ! hasf "$TEST" "$t"; then
      fail "D: missing required test $t"
    fi
  done
  ok "D2: all 13 required B229 tests present"
else
  fail "D: B229 test file $TEST missing"
fi

# --- E: main.go wires RunPreferredExitReconciler ---
if hasf "$MAIN" "go app.RunPreferredExitReconciler(" && \
   hasf "$MAIN" "cfg.PrefReconcileInterval"; then
  ok "E: cmd/skygate/main.go wires go app.RunPreferredExitReconciler gated on cfg.PrefReconcileInterval"
else
  fail "E: cmd/skygate/main.go missing RunPreferredExitReconciler wiring"
fi

# --- F: handlers.go has RunPreferredExitReconciler wrapper ---
if has "$HANDLERS" "func .a .App. RunPreferredExitReconciler" && \
   hasf "$HANDLERS" "ReconcileDeviceExitNodePrefs"; then
  ok "F: internal/handlers/handlers.go has RunPreferredExitReconciler wrapper"
else
  fail "F: handlers.go missing RunPreferredExitReconciler wrapper"
fi

# --- G: exitRulesRunner interface includes ReconcileDeviceExitNodePrefs ---
if grep -A12 "type exitRulesRunner interface" "$HANDLERS" | grep -q "ReconcileDeviceExitNodePrefs"; then
  ok "G: exitRulesRunner interface includes ReconcileDeviceExitNodePrefs"
else
  fail "G: exitRulesRunner interface missing ReconcileDeviceExitNodePrefs"
fi

# --- H: config has PrefReconcileInterval ---
if has "$CFG" "PrefReconcileInterval" && \
   hasf "$CFG" "SKYGATE_PREFERRED_RECONCILE_INTERVAL"; then
  ok "H: internal/config/config.go has PrefReconcileInterval + SKYGATE_PREFERRED_RECONCILE_INTERVAL"
else
  fail "H: config.go missing PrefReconcileInterval or SKYGATE_PREFERRED_RECONCILE_INTERVAL"
fi

# --- I: AGENTS.md mentions B229 ---
if has "$AGENTS" "B229"; then
  ok "I: AGENTS.md mentions B229"
else
  echo "[skip] I: AGENTS.md doesn't mention B229 (will be added before commit)"
fi

# --- J: build + vet + test ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "J: go build ./... succeeds"
  else
    fail "J: go build ./... FAILED"
  fi
  if go vet ./... >/dev/null 2>&1; then
    ok "J2: go vet ./... succeeds"
  else
    fail "J2: go vet ./... FAILED"
  fi
  if go test ./internal/feature/exit_rules/... ./internal/handlers/... ./internal/config/... >/dev/null 2>&1; then
    ok "J3: go test on affected packages passes"
  else
    fail "J3: go test on affected packages FAILED"
  fi
else
  echo "[skip] J: go not on PATH"
fi

echo ""
echo "B229 B-check: $ok_count passed"
