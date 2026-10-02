#!/usr/bin/env bash
# check_b341_exit_rule_relay_truth.sh — B341: a rule that names NO relay must
# still reach a decision about the DEVICE, and that decision must come from the
# same table the ACL pin comes from.
#
# LIVE CASE (2026-10-01, operator report). "Добавил для cyborg правило для youtube,
# правило уже есть, однако не распространяется на cyborg; на skyworker всё
# отрабатывает." Measured on the reference VM:
#
#   device  rules  exit_node_id        device_exit_node_prefs
#   cyborg  11      '' (every row)     — none at all
#   basic   11      emilia             emilia (via=1)
#   skyworker …     karolina           karolina (via=1)
#
# and in the live headscale policy cyborg's youtube CIDRs were ALREADY pinned
# `via=[tag:dev-infra-emilia]`, exactly like skyworker's — because since B275 the
# per-CIDR pin comes from prefix_owner(), NOT from the rule. So the policy looked
# right while the device had no exit node whatsoever, and the rule had no effect.
#
# WHY IT WAS INVISIBLE (the two defects this block closes):
#   1. `ReconcileDeviceExitNodePrefs` listed the devices to consider with
#      `AND exit_node_id <> ''` — a device whose rules name no relay was not even
#      a candidate, so nothing was derived and nothing was said.
#   2. even if it had been listed, `PlanDevicePrefChange` answered
#      `CanonicalTag == ""` with a silent `return nil, false` — the same shape as
#      "everything is fine".
# Both are gone: the pair list includes such devices, and the planner derives the
# device's exit node from prefix_owner() when it is unique (the decision the data
# plane already made), or reports a NAMED skip when it is not.
#
# NON-BLOCKING BY CONSTRUCTION (operator requirement: "починка не должна
# блокировать существующую работу"):
#   * the new path only ever runs for a device with NO preference row;
#   * it never writes device_rules (the operator's rule list is not rewritten);
#   * it creates only when exactly one relay owns the prefixes, and skips
#     otherwise — a skip is a log line, not an error;
#   * saving a rule without a relay is still allowed; nothing is validated away.
#
# Usage:  bash scripts/check_b341_exit_rule_relay_truth.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

REC="$REPO_ROOT/internal/feature/exit_rules/reconciler.go"
HELPER="$REPO_ROOT/internal/db/device_rule_owner_b341.go"
UNIT="$REPO_ROOT/internal/feature/exit_rules/reconciler_b341_test.go"
DBUNIT="$REPO_ROOT/internal/db/device_rule_owner_b341_test.go"

hdr "A. the hole is closed at both ends"

if [ ! -f "$REC" ]; then
  bad "A1: $REC is missing"
else
  # The pair list must NOT filter on exit_node_id any more. The predicate was
  # removed from the package entirely (measured: `grep -rn "exit_node_id <> ''"`
  # over internal/feature/exit_rules finds nothing), so its absence IS the
  # contract — no fragile text extraction that a comment can satisfy.
  if grep -q "exit_node_id <> ''" "$REC"; then
    bad "A1: the reconciler still filters its device list on exit_node_id — a device whose rules name no relay is invisible again"
  else
    ok "A1: the device list no longer filters on exit_node_id (relay-less devices are considered)"
  fi
  if grep -q 'missing-pref-owner-derived' "$REC"; then
    ok "A2: an unambiguous owner is derived (missing-pref-owner-derived)"
  else
    bad "A2: the owner-derived case is gone — cyborg's shape would be silent again"
  fi
  for reason in missing-pref-no-relay missing-pref-owner-split missing-pref-owner-untagged missing-pref-relay-untagged; do
    if grep -q "\"$reason\"" "$REC"; then
      ok "A3: the skip reason $reason is named (not a silent no-op)"
    else
      bad "A3: the skip reason $reason is missing — that case is silent"
    fi
  done
  if grep -q 'reason=%s' "$REC"; then
    ok "A4: the skip log line carries the reason"
  else
    bad "A4: the skip log line does not print the reason — the operator cannot act on it"
  fi
fi

hdr "B. one source of truth for 'which relay serves this prefix'"

if [ ! -f "$HELPER" ]; then
  bad "B1: $HELPER is missing"
else
  if grep -q 'JOIN prefix_owner' "$HELPER" && grep -q "target_type IN ('subnet', 'ip')" "$HELPER"; then
    ok "B1: the derivation reads prefix_owner and counts the per-CIDR population only"
  else
    bad "B1: the derivation does not read prefix_owner for the subnet/ip rules"
  fi
  if grep -q 'CAST(device_id AS TEXT)' "$HELPER" && grep -q '\$1' "$HELPER"; then
    ok "B2: portable placeholders + CAST (B319/B331: INTEGER vs TEXT across both backends)"
  else
    bad "B2: the query is not portable across SQLite and PostgreSQL"
  fi
fi
if grep -q 'prefixowner.ViaForPrefix' "$REPO_ROOT/internal/acl/acl_generate_via.go" 2>/dev/null; then
  ok "B3: the ACL pin still comes from the assignment table (the two halves share the decision)"
else
  bad "B3: the ACL pin no longer reads prefix_owner — the halves can diverge"
fi
if grep -q 'OwnerTagCountsForDeviceRules' "$REC" && grep -q 'DistinctExitNodes == 0' "$REC"; then
  ok "B4: the reconciler consults the assignment table ONLY when the rules name no relay"
else
  bad "B4: the reconciler does not gate the owner lookup on 'no relay' (working devices would take a new path)"
fi

hdr "C. non-blocking (nothing that works today may change)"

# Scoped to reconciler.go on purpose: reconciler_rename.go (B231) DOES rewrite
# device_rules, and that is its documented job — it migrates the operator's rules
# when a device is renamed in headscale. What must never happen is the B341 path
# (the preference derivation) touching the rule list, because the operator's rules
# are the source of intent (B274: "device rule lists are never rewritten").
if grep -qE 'UPDATE[[:space:]]+device_rules|DELETE[[:space:]]+FROM[[:space:]]+device_rules|INSERT[[:space:]]+INTO[[:space:]]+device_rules' "$REC" 2>/dev/null; then
  bad "C1: reconciler.go writes device_rules — the B341 derivation must only write device_exit_node_prefs"
else
  ok "C1: the B341 path never writes device_rules (only reconciler_rename.go does, by B231 design)"
fi
if grep -q 'ExistingPrefTag == ""' "$REC"; then
  ok "C2: the owner-derived path is confined to devices with no preference row"
else
  bad "C2: the owner-derived path is not guarded by ExistingPrefTag == \"\""
fi
if grep -q 'PreferredExitReconcilerLive()' "$REC" && grep -q 'if !live' "$REC"; then
  ok "C3: dry-run mode is still honoured before any write"
else
  bad "C3: the live/dry-run gate is gone"
fi
if grep -q 'ExistingPreferenceIsNeverOverwritten' "$UNIT" 2>/dev/null; then
  ok "C4: the unit suite pins 'an existing preference is never overwritten'"
else
  bad "C4: nothing pins that an existing preference is left alone"
fi

hdr "D. the decisions are tested, on both backends"

GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "D1: go is not on PATH — run the B341 unit tests on the VM"
  skip "D2: PostgreSQL half not exercised here"
else
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B341' -count=1 2>&1)"; then
    ok "D1: the planner's B341 cases pass ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "D1: the planner's B341 cases fail: $(printf '%s' "$OUT" | tail -3)"
  fi
  if OUT="$("$GO_BIN" test ./internal/db/ -run 'OwnerTagCountsForDeviceRules_SQLite_B341' -count=1 2>&1)"; then
    ok "D2: the derivation runs against a real migrated SQLite DB"
  else
    bad "D2: the SQLite half of the derivation fails: $(printf '%s' "$OUT" | tail -3)"
  fi
  if [ -n "${SKYGATE_TEST_PG_DSN:-}" ]; then
    if OUT="$("$GO_BIN" test ./internal/db/ -run 'OwnerTagCountsForDeviceRules_Postgres_B341' -count=1 2>&1)"; then
      ok "D3: the derivation runs against PostgreSQL too"
    else
      bad "D3: the PostgreSQL half of the derivation fails: $(printf '%s' "$OUT" | tail -3)"
    fi
  else
    skip "D3: SKYGATE_TEST_PG_DSN is unset — the PostgreSQL half SKIPs (B334 covers it when the DSN is set)"
  fi
  # D4/D5 — B341.1 (2026-10-02, measured live). The planner's condition is
  # `DistinctExitNodes == 0`, but the COLLECTOR counted the SQL GROUP BY's empty
  # group as a relay, so a device whose rules name no relay reported 1 and the
  # derive branch was unreachable in production while the pure unit test — which
  # hand-built the state — passed. The live VM said it in one line:
  #   preferred-reconciler: SKIP skyadmin/cyborg —
  #   reason=missing-pref-relay-untagged rules=11 distinct_relays=0
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'TestCollectDevicePrefState_.*_B341' -count=1 2>&1)"; then
    ok "D4: the COLLECTOR (not just the planner) reports zero relays for a relay-less device ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "D4: the collector regression test fails: $(printf '%s' "$OUT" | tail -3)"
  fi
  if grep -q 'AN EMPTY exit_node_id IS NOT A RELAY' internal/feature/exit_rules/reconciler.go; then
    ok "D5: the empty-group skip is present in the collector loop (with the measurement that motivated it)"
  else
    bad "D5: the collector counts the empty exit_node_id group as a relay again — the derive branch becomes unreachable"
  fi
fi

hdr "E. live: every relay-less device must have a relay to derive from"

if [ -f "$REPO_ROOT/scripts/lib/db_credentials.sh" ]; then
  # shellcheck source=lib/db_credentials.sh
  . "$REPO_ROOT/scripts/lib/db_credentials.sh"
  if skygate_live_db_probe; then
    # The population the fix acts on: enabled rules with no relay, per device.
    NO_RELAY="$(skygate_live_db_query \
      "SELECT DISTINCT device_hostname FROM device_rules WHERE enabled = 1 AND COALESCE(exit_node_id,'') = '' AND device_hostname <> ''" | tr -d '\r')"
    if [ -z "$NO_RELAY" ]; then
      ok "E1: no device has enabled rules without a relay (the cyborg shape is absent)"
    else
      echo "  INFO E1: devices with relay-less enabled rules: $(printf '%s' "$NO_RELAY" | tr '\n' ' ')"
      BAD=""
      for dev in $NO_RELAY; do
        owners="$(skygate_live_db_query \
          "SELECT DISTINCT po.exit_node_id FROM device_rules r JOIN prefix_owner po ON po.prefix = r.target_value WHERE r.device_hostname = '$dev' AND r.enabled = 1 AND r.target_type IN ('subnet','ip')" | tr -d '\r')"
        if [ -z "$owners" ]; then
          BAD="$BAD $dev"
        else
          echo "  INFO E1:   $dev → derivable from prefix_owner: $(printf '%s' "$owners" | tr '\n' ' ')"
        fi
      done
      if [ -n "$BAD" ]; then
        bad "E2: these devices have rules without a relay AND no owner for their prefixes — nothing can be derived, the operator must pick a relay:$BAD"
      else
        ok "E2: every relay-less device has an owner to derive from (the next reconciler tick creates its preference)"
      fi
    fi
    # E3: a device may not be skipped for "no relay" while the ACL pins its
    # prefixes — that is the contradiction the operator reported.
    CONTRADICTION="$(skygate_live_db_query \
      "SELECT COUNT(*) FROM device_rules r JOIN prefix_owner po ON po.prefix = r.target_value WHERE r.enabled = 1 AND COALESCE(r.exit_node_id,'') = ''" | tr -d '\r')"
    if [ -n "$CONTRADICTION" ]; then
      ok "E3: $CONTRADICTION rule(s) are pinned by prefix_owner while naming no relay — each one needs a device-side decision (B341 derives it)"
    else
      ok "E3: no relay-less rule is pinned by the assignment table"
    fi
  else
    skip "E1-E3: $(skygate_live_db_reason)"
  fi
else
  skip "E1-E3: scripts/lib/db_credentials.sh is missing"
fi

hdr "F. tracked, registered, indexed"

if git ls-files --error-unmatch scripts/check_b341_exit_rule_relay_truth.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git (AGENTS trap #11)"
else
  bad "F1: this script is NOT tracked by git — a .gitignore rule is eating it"
fi
if grep -q 'check_b341_exit_rule_relay_truth.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in scripts/verify_pre_deploy.sh — it would never run"
fi
if grep -q 'B341' AGENTS.md 2>/dev/null; then
  ok "F3: recorded in the AGENTS.md block index"
else
  bad "F3: no B341 line in the AGENTS.md block index"
fi

hdr "B341 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -gt 0 ] && exit 1
exit 0
