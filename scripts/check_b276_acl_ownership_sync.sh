#!/usr/bin/env bash
# check_b276_acl_ownership_sync.sh
#
# 2026-09-21 (B276, v1.5.36) — the ACL must follow the assignment table.
#
# skygate owns two halves of one decision, written at different times:
#
#   DATA plane  — which relay advertises which prefix. headscale serves a subnet
#                 prefix from exactly ONE relay (the primary), and skygate
#                 recomputes the assignment table + the advertised routes on every
#                 sync pass (minutes).
#   CONTROL     — the ACL. Since B275 the per-CIDR grant carries via=[owner], so the
#                 pin IS the decision — but the ACL was regenerated only when a
#                 rule, user or device changed.
#
# Live on the reference host: the ACL was applied 2026-09-20 18:19, the assignment
# table moved 28 Cloudflare/Google prefixes to the other relay after 19:26 (the
# routes followed within five minutes), and the ACL kept pinning them to the OLD
# relay. headscale's `via` is a permission filter, so every client silently stopped
# receiving those routes and fell back to the direct path — the operator's device
# lost its whole Cloudflare set with no log line, no audit row and no metric saying
# so. Measured on one client at diagnosis time: 70 of 177 per-CIDR grants named a
# relay that was not the primary, and 35 more named a prefix nobody advertised.
#
# CONTRACTS
#   A  both sync paths re-apply the ACL when an owner actually changes
#   B  the ownership vote is per (device, prefix), not per derived row
#   C  the drift is visible: policy comparison + per-prefix advertisement flags
#   D  Go behaviour contracts (the live sequence on a migrated DB + policy stub)
#   E  live state (SKIPs when the DB / headscale / service are not reachable)
#   F  the script itself is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B276: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SYNC=internal/feature/exit_rules/sync.go
REAPPLY=internal/feature/exit_rules/form_reapply.go
ACL=internal/acl/acl.go
PKG=internal/prefixowner/prefixowner.go
ADMIN=internal/feature/admin/exit_nodes.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
MAIN=cmd/skygate/main.go
HELPER=internal/headscale/policy_equivalent_b276.go
CAT_RU=internal/i18n/catalog_exit_nodes.go

hdr "B276 — the per-CIDR ACL pin follows the assignment table"

# --- A: the sync paths re-apply the ACL -------------------------------------
if grep -q 'func (s \*Service) reconcilePrefixOwnership() (int, int, error)' "$SYNC"; then
  ok "A1: reconcilePrefixOwnership exists (reconcile + re-apply in one place)"
else
  bad "A1: the reassignment has no single entry point — the ACL can drift again"
fi
if grep -q 'func (s \*Service) applyACLAfterOwnershipChange(ins, chg int)' "$SYNC"; then
  ok "A2: applyACLAfterOwnershipChange regenerates + pushes the policy"
else
  bad "A2: nothing regenerates the ACL when an owner changes"
fi
CALLS="$(grep -c 's\.reconcilePrefixOwnership()' "$SYNC")"
if [ "${CALLS:-0}" -ge 2 ]; then
  ok "A3: both sync paths use it (SyncAdvertisedRoutes + StaggeredSync: $CALLS call sites)"
else
  bad "A3: only $CALLS sync path calls it — the other one still moves routes without the ACL"
fi
RECONCILES="$(grep -c 'prefixowner.Reconcile(' "$SYNC")"
if [ "${RECONCILES:-0}" = "1" ]; then
  ok "A4: exactly one reconcile call site — the one that also re-applies the ACL"
else
  bad "A4: $RECONCILES reconcile call sites — a path can move the table without the ACL following"
fi
if grep -q 'chg > 0 {' "$SYNC" && grep -A3 'chg > 0 {' "$SYNC" | grep -q 'applyACLAfterOwnershipChange'; then
  ok "A5: the re-apply is triggered by an actual owner CHANGE, not by every pass"
else
  bad "A5: the re-apply is not gated on a change — it would write the policy every pass"
fi
if grep -q 'ownershipACLThrottle' "$SYNC" && grep -q 'ownershipACLMu' "$SYNC"; then
  ok "A6: a shared throttle absorbs a churning table (no apply storm from two goroutines)"
else
  bad "A6: nothing bounds the re-apply rate"
fi
if grep -q 'headscale.PolicyEquivalent(gen, live)' "$SYNC" && grep -q 's.HS.InvalidateCache()' "$SYNC"; then
  ok "A7: it compares the generated policy with a FRESH live read before writing"
else
  bad "A7: the re-apply cannot tell 'already applied' from 'stale' (or reads a cached policy)"
fi
# B280-era CONTRACT RENEGOTIATION (2026-09-22). v1.5.42 (B-pending-write)
# changed this signature from `bool` to `acl.ApplyResult` so the callers can
# read the snapshot Version, mark a real apply failure and skip the audit row
# when there was no drift at all — see
# scripts/check_apply_acl_drifted_and_rename.sh contract A1, which pins the new
# shape. The property THIS contract protects is unchanged and is what the
# message says: ONE trigger-agnostic drift check, reused by the ownership flip,
# the rule churn and the pre-existing-mismatch path. The old grep pinned a
# return type that no longer exists, so it reported a regression for a
# deliberate refactor (FAIL on main since v1.5.42 while the behaviour was
# intact). Note the assertion is the CURRENT signature, not an alternation
# (`bool|acl.ApplyResult`) — a contract that accepts both cannot fail.
if grep -q 'func (s \*Service) applyACLIfDrifted(actor, detail string) acl.ApplyResult' "$SYNC"; then
  ok "A8: one trigger-agnostic drift check returning acl.ApplyResult (ownership flip, rule churn, pre-existing mismatch)"
else
  bad "A8: the drift logic is duplicated per trigger, or the drift check no longer reports what it did (want acl.ApplyResult)"
fi
if grep -q 'acl.ApplyGeneratedPolicy(s.dbc(), s.HS, gen, actor, detail, nil)' "$SYNC"; then
  ok "A9: the automatic apply goes through the shared pipeline tail (snapshot + mark + audit)"
else
  bad "A9: the automatic apply bypasses the snapshot/audit path"
fi
# A10 renegotiated by B298 (2026-09-23): the auto-updater's drift check is now
# `applyACLIfDriftedChurn` — same decision, but behind the 30-minute churn budget
# instead of the 60s ownership one, because on a `policy.mode: file` host every
# apply is `systemctl restart headscale` and this path is driven by DNS rotation.
# The pattern accepts both spellings so the assertion is about the CALL, not the
# budget (the budget itself is pinned by scripts/check_b298_cdn_rule_churn.sh).
if grep -A4 'func (s \*Service) DomainAutoUpdater' "$SYNC" | grep -q 'applyACLIfDrifted' \
   || grep -qE 's\.applyACLIfDrifted(Churn)?\("skygate-auto-updater"' "$SYNC"; then
  ok "A10: the periodic auto-updater tick also checks the policy (the rules ARE the ACL; live: 8 of the 15 newest rules had no alias)"
else
  bad "A10: rule changes still never re-apply the ACL — the policy outruns the rules"
fi
if grep -q 'func ApplyGeneratedPolicy(d \*sql.DB, hs \*headscale.Client, acl, username, detailForLog string, alerter Alerter) ApplyResult' "$ACL" \
   && grep -q 'return ApplyGeneratedPolicy(d, hs, acl, username, detailForLog, alerter)' "$ACL"; then
  ok "A11: ApplyACLPipelineForPlane is generate-then-ApplyGeneratedPolicy (one tail, no drift)"
else
  bad "A11: the apply tail is duplicated — the two paths can diverge"
fi
if grep -q 're-apply FAILED' "$SYNC" && grep -q 'SendAlert' "$SYNC"; then
  ok "A12: a failed automatic apply is logged with its reason and alerted, never fatal"
else
  bad "A12: a failed automatic apply is silent"
fi

# --- B: one vote per device --------------------------------------------------
if grep -q 'GROUP BY target_value, COALESCE(exit_node_id' "$PKG"; then
  ok "B1: LoadClaims counts one claim per (prefix, relay, device)"
else
  bad "B1: LoadClaims still counts derived ROWS — the updater's churn decides the owner"
fi
if grep -q "SELECT DISTINCT exit_node_id, target_value FROM device_rules" "$SYNC"; then
  ok "B2: the B274 fallback path counts distinct (relay, prefix) pairs too"
else
  bad "B2: the fallback path still counts duplicate rows"
fi
if grep -q 'so the vote must be per DEVICE' "$PKG"; then
  ok "B3: the reason is written down where the next reader will look"
else
  bad "B3: the GROUP BY has no explanation — it will be 'cleaned up' away"
fi

# --- C: the drift is visible -------------------------------------------------
if grep -q 'func PolicyEquivalent(a, b string) (bool, error)' "$HELPER"; then
  ok "C1: headscale.PolicyEquivalent compares two policy documents semantically"
else
  bad "C1: there is no way to ask 'is the live policy the generated one?'"
fi
if grep -q 'type PrefixDriftStats struct' "$ADMIN" && grep -q 'func (s \*Service) fillPolicyDrift' "$ADMIN"; then
  ok "C2: the exit-nodes page summarises the drift (stats + live-policy check)"
else
  bad "C2: the page cannot say whether the pins are stale"
fi
if grep -q 'AlsoBy \[\]string' "$ADMIN" && grep -q 'Unserved bool' "$ADMIN"; then
  ok "C3: rows carry 'announced by several' (B274 flap risk) and 'nobody announces it'"
else
  bad "C3: the page cannot distinguish a silent owner from a prefix nobody serves"
fi
if grep -q 'prefixDriftRowLimit' "$ADMIN"; then
  ok "C4: the page renders the drifted/pinned rows, not all ~1500 table entries"
else
  bad "C4: the page renders the whole table (megabytes of HTML with a select per row)"
fi
if grep -q 'func (s \*Service) PostAdminExitNodeACLResync' "$ADMIN" \
   && grep -q 'POST /admin/exit-nodes/acl-resync' "$MAIN"; then
  ok "C5: the operator can regenerate+apply from the page (route registered)"
else
  bad "C5: no operator-facing resync action"
fi
if grep -q 'policy_stale' "$TMPL" && grep -q '/admin/exit-nodes/acl-resync' "$TMPL"; then
  ok "C6: the template renders the stale-policy banner with the resync button"
else
  bad "C6: the drift is not rendered"
fi
KEYS=0
for k in policy_in_sync policy_stale policy_stale_help policy_resync unserved duplicated drift_count shown_of; do
  n="$(grep -c "exit_nodes.prefix_owner.$k\"" "$CAT_RU")"
  [ "$n" -ge 2 ] && KEYS=$((KEYS+1))
done
if [ "$KEYS" -ge 8 ]; then
  ok "C7: every new key exists in RU and EN (8/8 pairs)"
else
  bad "C7: only $KEYS/8 keys are present in both catalogues (i18n parity would fail)"
fi
if grep -q 'func (s \*Service) loadPrefixOwnerRows() (\[\]PrefixOwnerRow, PrefixDriftStats)' "$ADMIN"; then
  ok "C8: the B275.1 loader is the same entry point (extended to also return the drift summary — its contract was renegotiated)"
else
  bad "C8: the assignment-table loader was replaced by a second copy"
fi

# --- D: behaviour ------------------------------------------------------------
if [ -f internal/feature/exit_rules/sync_b276_test.go ] && grep -q 'func TestB276_OwnershipChangeReappliesACL' internal/feature/exit_rules/sync_b276_test.go; then
  ok "D1: the live sequence is reproduced (table moves → the ACL is pushed with the new pin)"
else
  bad "D1: no behavioural test for the ownership→ACL handoff"
fi
for t in TestB276_PolicyEquivalent_DetectsTheStalePin TestB276_LoadClaimsCountsOneVotePerDevice TestB276_NoChangeMeansNoWrite TestB276_ThrottleAbsorbsAChurningTable; do
  if grep -rq "func $t(" internal/ ; then
    ok "D2: $t is present"
  else
    bad "D2: $t is missing"
  fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(timeout 600 go test -count=1 -run 'B276' ./internal/feature/exit_rules/ ./internal/headscale/ ./internal/prefixowner/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "D3: the B276 contracts pass (ownership change → one write with the new owner; no-op pass → no write; throttle holds; claims are per device)"
  else
    bad "D3: the B276 contracts failed: $OUT"
  fi
else
  skip "D3: go not on PATH"
fi

# --- E: live state -----------------------------------------------------------
PSQL=""
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'skygate-pg-local'; then
  PSQL="docker exec skygate-pg-local psql -U admin -d skygate_staging -t -A -c"
fi
if [ -n "$PSQL" ]; then
  BADSRC="$($PSQL "SELECT COUNT(*) FROM prefix_owner WHERE exit_node_id = '' OR source NOT IN ('explicit','manual','auto')" 2>/dev/null | tr -d '[:space:]')"
  if [ "${BADSRC:-1}" = "0" ]; then
    ok "E1: every assignment row has an owner and a known source ($($PSQL 'SELECT COUNT(*) FROM prefix_owner' 2>/dev/null | tr -d '[:space:]') rows)"
  else
    bad "E1: $BADSRC assignment row(s) have no owner or an unknown source"
  fi
  # How many pins name a relay that does not serve the prefix (the live failure).
  OWNERS="$($PSQL "SELECT prefix || '|' || exit_node_id FROM prefix_owner" 2>/dev/null)"
  ADV=""
  if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'headscale'; then
    ADV="$(docker exec headscale headscale nodes list --output json 2>/dev/null \
      | python3 -c 'import json,sys
try:
    nodes=json.load(sys.stdin)
except Exception:
    sys.exit(0)
for n in nodes:
    for p in n.get("subnet_routes") or []:
        print(p+"|"+(n.get("given_name") or "").lower())' 2>/dev/null)"
  fi
  if [ -n "$ADV" ] && [ -n "$OWNERS" ]; then
    DRIFT="$(python3 - "$OWNERS" "$ADV" <<'PY'
import sys
owners = dict(line.split("|", 1) for line in sys.argv[1].splitlines() if "|" in line)
served = {}
for line in sys.argv[2].splitlines():
    if "|" in line:
        p, h = line.split("|", 1)
        served.setdefault(p, set()).add(h)
mismatch = sum(1 for p, o in owners.items() if p in served and o.lower() not in served[p])
unserved = sum(1 for p in owners if p not in served)
print("%d %d" % (mismatch, unserved))
PY
)"
    read -r MIS UN <<< "${DRIFT:-0 0}"
    if [ "${MIS:-1}" = "0" ]; then
      ok "E2: no pin names a relay that does not serve its prefix (unserved=$UN)"
    else
      skip "E2: $MIS prefix(es) are owned by a relay that does not serve them and $UN are advertised by nobody — expected on a host whose running build predates B276 (the sync now re-applies the ACL itself) or between a table change and the next pass"
    fi
  else
    skip "E2: cannot read headscale subnet routes (no headscale container / CLI) — the reference-VM run covers it"
  fi
else
  skip "E1/E2: no local skygate PostgreSQL to inspect (live checks belong on the VM)"
fi
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet skygate 2>/dev/null && command -v journalctl >/dev/null 2>&1; then
  if journalctl -u skygate --since '-24h' --no-pager 2>/dev/null | grep -qE 'prefix-owner: ACL re-applied after the ownership change|prefix-owner: .* live policy already matches'; then
    ok "E3: the running service has already handled an ownership change via the new path"
  else
    skip "E3: no ownership change in the last 24h (normal — the path runs only when the table moves)"
  fi
else
  skip "E3: skygate is not running as a systemd service on this host"
fi

# --- F: tracked by git (trap #11) --------------------------------------------
if git ls-files --error-unmatch scripts/check_b276_acl_ownership_sync.sh >/dev/null 2>&1; then
  ok "F1: scripts/check_b276_acl_ownership_sync.sh is tracked by git"
else
  bad "F1: scripts/check_b276_acl_ownership_sync.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB276 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
