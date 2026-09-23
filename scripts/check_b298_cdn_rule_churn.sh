#!/usr/bin/env bash
# check_b298_cdn_rule_churn.sh
#
# 2026-09-23 (B298) — derived-rule churn must not restart the control plane.
#
# Live on the native host `aro`, every five minutes, forever:
#
#	acl-drift: ACL re-applied (snapshot v483, generated=7331 bytes) — auto-updater tick changed 18 rule(s) (added=17 removed=1)
#	auto-updater: dedup removed 16 redundant derived rule row(s)
#
# The database proved the two halves: 19 subnet rows, 19 distinct CIDRs, and
# `openai.com` present under TWO parent_domain values (`cdn:cloudflare:openai.com`
# 15 rows, bare `openai.com` 2 rows) — i.e. the same prefix carried by two domains.
# On a `policy.mode: file` host an ACL re-apply IS `systemctl restart headscale`,
# so this was 288 control-plane restarts a day. Two independent defects produced it:
#
#  1. `CollapseDuplicateDerivedRules` partitioned on FIVE columns (excluding
#     parent_domain) while `device_rules_natural_key_uniq` is a SIX-column index
#     (B237.23/V068) that deliberately lets two domains of one CDN keep a row per
#     CIDR each. The collapse deleted the row the CDN short-circuit looks for
#     (`parent_domain LIKE 'cdn:%:<domain>'`) and B184's status reads, so that domain
#     re-resolved and re-inserted its whole published range set on the next tick —
#     and the collapse deleted it again.
#  2. The auto-updater and the periodic drift check shared the 60-SECOND ownership
#     throttle, so even a single rotating /32 from DNS re-applied the policy.
#
# CONTRACTS
#   A. the collapse partitions on the same key as the UNIQUE index (so it removes
#      only EXACT duplicates) and the old five-column form is gone
#   B. the churn budget exists, the core is parameterised, operator actions keep the
#      60s budget and the derived-rule paths spend the long one
#   C. the layers the fix must not break: the CDN short-circuit, the 6-column
#      ON CONFLICT (B237.23) and the deferred dedup (B274)
#   D. the regression tests exist and pass; the renegotiated contracts say why; this
#      script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B298: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

OWNER=internal/feature/exit_rules/prefix_owner.go
SYNC=internal/feature/exit_rules/sync.go
TEST=internal/feature/exit_rules/churn_b298_test.go
B274=scripts/check_b274_prefix_ownership.sh
B276=scripts/check_b276_acl_ownership_sync.sh
B2761=scripts/check_b276_1_all_devices.sh
B288=scripts/check_b288_policy_drift_truth.sh

hdr "B298 — derived-rule churn must not restart the control plane"

# --- A: the collapse matches the schema ---------------------------------------
if grep -q 'PARTITION BY user_id, device_id, exit_node_id, target_type, target_value, parent_domain' "$OWNER"; then
  ok "A1: the collapse partitions on the natural key PLUS parent_domain (same key as the 6-col UNIQUE index)"
else
  bad "A1: the collapse still ignores parent_domain in its partition"
fi
if grep -qE 'PARTITION BY .*target_value[[:space:]]*$' "$OWNER"; then
  bad "A2: the pre-B298 five-column partition is back — it deletes another domain's derived rows every tick"
else
  ok "A2: the five-column partition is gone"
fi
if grep -q 'B298' "$OWNER" && grep -q 'added=17 removed=1' "$OWNER"; then
  ok "A3: the function carries the live evidence that explains why the partition changed"
else
  bad "A3: the reason for the six-column partition is undocumented"
fi

# --- B: the churn budget ------------------------------------------------------
if grep -q 'const churnACLThrottle = 30 \* time.Minute' "$SYNC" \
   && grep -q 'const ownershipACLThrottle = 60 \* time.Second' "$SYNC"; then
  ok "B1: two budgets exist: 60s for an operator action, 30m for derived-rule churn"
else
  bad "B1: the churn budget (30m) or the ownership budget (60s) is missing"
fi
if grep -q 'func (s \*Service) applyACLIfDriftedThrottled(actor, detail string, logNoop bool, throttle time.Duration) acl.ApplyResult' "$SYNC"; then
  ok "B2: one decision core, with the budget as a parameter (no duplicated drift logic)"
else
  bad "B2: the throttle is not a parameter — a second copy of the decision would drift from the first"
fi
if grep -q 'return s.applyACLIfDriftedThrottled(actor, detail, true, ownershipACLThrottle)' "$SYNC"; then
  ok "B3: a plain applyACLIfDrifted (operator actions) still spends the 60s budget"
else
  bad "B3: operator actions no longer use the short budget"
fi
if grep -q 'func (s \*Service) applyACLIfDriftedChurn(actor, detail string, logNoop bool) acl.ApplyResult' "$SYNC" \
   && grep -q 'return s.applyACLIfDriftedThrottled(actor, detail, logNoop, churnACLThrottle)' "$SYNC"; then
  ok "B4: the churn wrapper exists and spends the long budget"
else
  bad "B4: the churn wrapper is missing or does not spend churnACLThrottle"
fi
if grep -q 's.applyACLIfDriftedChurn("skygate-auto-updater"' "$SYNC"; then
  ok "B5: the domain auto-updater spends the churn budget"
else
  bad "B5: the auto-updater is still on the 60s budget (a rotating /32 restarts headscale)"
fi
if grep -q 's.applyACLIfDriftedChurn("skygate-periodic-drift"' "$SYNC"; then
  ok "B6: the periodic drift check spends the churn budget too"
else
  bad "B6: the periodic check would re-apply five minutes after every auto-updater write"
fi
if grep -q 'deferring to the next pass (throttle %s)' "$SYNC" && grep -q 'time.Since(ownershipACLLastRun).Round(time.Second), throttle' "$SYNC"; then
  ok "B7: the deferral log names the budget it actually spent"
else
  bad "B7: the deferral log prints a hardcoded budget"
fi

# --- C: what must NOT have moved ---------------------------------------------
if grep -q "parent_domain LIKE \$4" "$SYNC" && grep -q 'isCDNMarker(existingMarker)' "$SYNC"; then
  ok "C1: the CDN short-circuit is untouched (it now fires because the row survives)"
else
  bad "C1: the CDN short-circuit changed — the insert/dedup cycle would come back another way"
fi
if grep -q 'ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING' "$SYNC"; then
  ok "C2: the six-column ON CONFLICT target is unchanged (B237.23)"
else
  bad "C2: the ON CONFLICT target drifted from the 6-col index"
fi
if grep -q 's.CollapseDuplicateDerivedRules()' "$SYNC"; then
  ok "C3: the dedup still runs after the resolve loop (B274)"
else
  bad "C3: the dedup call disappeared"
fi

# --- D: tests, renegotiated contracts, git ------------------------------------
if [ -f "$TEST" ]; then ok "D1: $TEST exists"; else bad "D1: $TEST is missing"; fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ -run 'B298' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "D2: the B298 tests pass"
  else
    bad "D2: the B298 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "D2: go not on PATH — run the B298 tests on the VM"
fi
reneg=""
for f in "$B274" "$B276" "$B2761" "$B288"; do
  grep -q 'B298' "$f" || reneg="$reneg $f"
done
if [ -z "$reneg" ]; then
  ok "D3: every contract the change touched documents the B298 renegotiation"
else
  bad "D3: these contracts were changed without saying why:$reneg"
fi
if git ls-files --error-unmatch scripts/check_b298_cdn_rule_churn.sh >/dev/null 2>&1; then
  ok "D4: scripts/check_b298_cdn_rule_churn.sh is tracked by git"
else
  bad "D4: scripts/check_b298_cdn_rule_churn.sh is NOT tracked"
fi

printf '\n\033[1mB298 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
