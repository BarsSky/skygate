#!/usr/bin/env bash
# check_b345_owner_beats_rule_relay.sh — B345: THE OWNER WINS over a rule that
# names a relay which does not serve the prefixes that rule covers.
#
# WHY THIS FILE EXISTS (measured on the live host, 2026-10-03)
# -----------------------------------------------------------
# Operator report: «сколько ни пробовал добавить доступ к youtube на cyborg — не
# даёт». The live state explained it in three rows:
#
#   device_rules            : youtube.com → **karolina**  (a duplicate the operator
#                             had added; the other youtube.com row was «авто»)
#   prefix_owner            : all ten youtube prefixes → **emilia**
#   device_exit_node_prefs  : cyborg → **tag:dev-infra-karolina**
#
# and emilia was the ONLY node advertising those prefixes (37 routes, all approved;
# karolina advertised none of them). The reconciler had pinned the DEVICE to the
# relay the RULE named, while every per-CIDR ACL pin — and the route itself — said
# emilia. headscale treats `via` as a permission FILTER, so the device's chosen exit
# node could not serve the destinations its own grants allowed: YouTube did not open
# however many rules were added.
#
# B275 made `prefix_owner` authoritative for the DESTINATION half (which relay
# serves which prefix) and B265/B341.1 for the DEVICE half (which relay this device
# uses). B345 is the missing constraint between them: when the covered prefixes have
# exactly ONE owner and that owner is tagged, the owner's tag is what the device
# preference must name — a stored or rule-derived value naming somebody else is
# provably unable to carry the traffic, so it is REPAIRED (reason
# `owner-overrides-rule-relay`) instead of preserved.
#
# This narrows — deliberately, and in the open — the B341 promise that an existing
# preference is never overwritten: it is never overwritten UNLESS it names a relay
# that cannot serve the destinations its own ACL grants allow. The renegotiated
# guarantee is pinned by TestPlan_B345_ExistingPreferenceIsKeptUnlessItNamesANonOwner
# (four cases: the owner, a non-owner, no known owner, split owners).
#
# What this script verifies:
#   A. the planner repairs BOTH shapes (no pref yet, and an existing wrong pref)
#   B. the collector computes the owner even when the rules name a relay
#   C. the override stays NARROW (one owner, tagged)
#   D. the tests exist and pass, including the live-shape database case
#   E. tracked, registered, indexed (AGENTS §2 trap #11)
#
# Usage:  bash scripts/check_b345_owner_beats_rule_relay.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

REC=internal/feature/exit_rules/reconciler.go
PLAN_TEST=internal/feature/exit_rules/reconciler_b341_test.go
DB_TEST=internal/feature/exit_rules/reconciler_b341_collector_test.go
CAT=scripts/verify_pre_deploy.sh

for f in "$REC" "$PLAN_TEST" "$DB_TEST" "$CAT"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B345 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

hdr "A. the planner repairs both shapes"

N="$(grep -c 'owner-overrides-rule-relay' "$REC" || true)"
if [ "$N" -ge 2 ]; then
  ok "A1: the reason appears in both halves — the CREATE path (rules named a non-owner) and the UPDATE path (an existing pref names one)"
else
  bad "A1: 'owner-overrides-rule-relay' appears $N time(s) — expected the create AND the update path"
fi
if grep -q 'AN EXISTING PREFERENCE THAT' "$REC"; then
  ok "A2: the existing-preference repair is documented in place (with the measurement)"
else
  bad "A2: the existing-preference repair has no explanation — the next reader will delete it as a bug"
fi
if grep -q 'NewTag:         s.OwnerCanonicalTag' "$REC"; then
  ok "A3: the value written is the OWNER's canonical tag, not the rule's relay"
else
  bad "A3: the override does not write the owner's tag"
fi

hdr "B. the collector asks for the owner even when a rule names a relay"

if grep -q 'if totalRules > 0 {' "$REC"; then
  ok "B1: the owner lookup is unconditional (a rule can name a NON-owner)"
else
  bad "B1: the owner lookup still runs only when no rule names a relay — the B345 case would be invisible"
fi
if grep -q 'if totalRules > 0 && state.DistinctExitNodes == 0' "$REC"; then
  bad "B2: the old B341 guard is still present — the owner would not be computed for a rule-named relay"
else
  ok "B2: the B341-only guard is gone"
fi

hdr "C. the override stays narrow"

if [ "$(grep -c 's.OwnerDistinct == 1 && s.OwnerCanonicalTag != ""' "$REC" || true)" -ge 2 ]; then
  ok "C1: both halves require exactly ONE owner AND a resolved tag — an unknown or split owner never overrides the operator"
else
  bad "C1: the override is not guarded by 'one owner, tagged' in both halves"
fi
if grep -q 'TestPlan_B345_ExistingPreferenceIsKeptUnlessItNamesANonOwner' "$PLAN_TEST"; then
  ok "C2: the renegotiated guarantee is pinned by name (kept unless it names a non-owner)"
else
  bad "C2: the renegotiated guarantee has no test"
fi
if grep -q 'TestPlan_B341_ExistingPreferenceIsNeverOverwritten' "$PLAN_TEST"; then
  bad "C3: the OLD absolute guarantee is still asserted — it contradicts the live repair"
else
  ok "C3: the old absolute guarantee is gone (renegotiated, with the reason in the test's doc comment)"
fi

hdr "D. the tests run, including the live shape"

if grep -q 'TestCollectDevicePrefState_OwnerBeatsRuleRelay_B345' "$DB_TEST"; then
  ok "D1: the database-backed live-shape test exists (rules name karolina, prefix_owner says emilia)"
else
  bad "D1: the live-shape regression test is missing"
fi
GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "D2: go is not on PATH — run the B345 tests on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B341|B345' -count=1 2>&1)"; then
    ok "D2: the B341 + B345 cases pass ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "D2: the planner/collector cases fail: $(printf '%s' "$OUT" | tail -3)"
  fi
fi

hdr "E. tracked, registered, indexed"

if git ls-files --error-unmatch scripts/check_b345_owner_beats_rule_relay.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (AGENTS trap #11)"
else
  bad "E1: NOT tracked by git — a .gitignore rule can eat a new script"
fi
if grep -q 'check_b345_owner_beats_rule_relay.sh' "$CAT"; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered — it would never run"
fi
if grep -q '\*\*B345\*\*' AGENTS.md; then
  ok "E3: recorded in the AGENTS.md block index"
else
  bad "E3: no B345 line in the AGENTS.md block index (AGENTS rule 2)"
fi

hdr "B345 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
