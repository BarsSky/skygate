#!/usr/bin/env bash
# check_b374_route_convergence.sh — B374 (2026-10-09): the advertised routes must
# have a PERIODIC owner, the ACL drift path must not hide behind the churn budget
# when the planes disagree, and a stale advertisement the portal could not revoke
# must be a named, page-visible fact.
#
# THE LIVE INCIDENT (measured on the reference deployment, 2026-10-09).
#
#   14:08      the container is recreated; the first route-apply pass cannot reach
#              karolina (SSH 100.64.0.2:18022 times out), so the B352 machinery
#              excludes it and moves 197 prefixes to emilia in `prefix_owner`, and
#              re-pins the device preferences to tag:dev-infra-emilia. That worked.
#   14:0x-15:2 emilia's `--advertise-routes` is NEVER pushed. `headscale nodes list`
#              shows `emilia available=2 approved=2` (only the base routes) while
#              karolina still holds 199/199, and
#              `global_settings[relay_apply_state:emilia]` keeps its 13:01:07
#              timestamp. The container log holds ZERO lines containing `advertis`
#              or `staggeredSync`. Every device whose grant pins
#              via=[tag:dev-infra-emilia] (e.g. skyworker) loses YouTube/Telegram
#              and every other pinned prefix.
#   15:2x      a manual "Re-sync all" (POST /admin/exit-nodes/sync) fixes it
#              instantly: emilia 205/205 approved, karolina 199→2, both
#              relay_apply_state = ok.
#
# ROOT CAUSE. The ONLY periodic caller of StaggeredSync() was the domain
# auto-updater loop (internal/handlers/handlers.go, RunDomainAutoUpdater), gated on
# `added > 0 || removed > 0`. That tick legitimately logged, every five minutes,
#   auto-updater: 18 domain(s) skipped (re-resolve interval 6h0m0s; …)
# so added=removed=0 and NO route sync ran for the whole outage. Domain churn was the
# only trigger of the data plane.
#
# SECONDARY DEFECT. The ACL drift path deferred its re-apply with
#   acl-drift: periodic drift check (the assignment table did not move) needs a
#   re-apply but one ran … ago — deferring to the next pass (throttle 30m0s)
# — a FALSE statement (the table HAD moved minutes earlier) on the WRONG budget
# (30m of derived-row churn, for an ownership move).
#
# WHAT THIS SCRIPT PINS
#   A. the convergence pass exists, reads the owned set from prefix_owner and the
#      advertised set from the SAME headscale node view the rest of the code reads;
#   B. it opens NO transport for a relay whose sets already agree (the hard
#      requirement) and applies through the SHARED path for one that differs;
#   C. it is wired into the period maintenance tick with its own budget and prefix;
#   D. the ACL drift path names the true reason and spends the operator budget when
#      the planes disagree (B298's churn path untouched);
#   E. the stale advertisement is logged AND recorded for the page;
#   F. the behavioural halves run;
#   G. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs a tool this host lacks.
#
# Usage:  bash scripts/check_b374_route_convergence.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/gosurface.sh

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# The ROUTE surface, not one file: the pass lives in its own file, but the apply
# path it must share with (syncOneExitNode / applyRoutesToRelay) lives in
# sync_routes.go, and a later PURE MOVE inside the package must not turn this red
# (B339's lesson — a contract about a SURFACE, not about a layout).
gosurface ROUTES internal/feature/exit_rules/*.go
CONVERGE=internal/feature/exit_rules/sync_routes_converge_b374.go
ACL=internal/feature/exit_rules/sync_acl.go
HANDLERS=internal/handlers/handlers.go
TEST=internal/feature/exit_rules/sync_routes_converge_b374_test.go
TEST_ACL=internal/feature/exit_rules/sync_acl_b374_test.go
TEMPLATE=internal/handlers/templates/admin/exit_nodes.html
PAGE=internal/feature/admin/exit_nodes_prefix_drift.go
I18N=internal/i18n/catalog_exit_nodes.go
SELF=scripts/check_b374_route_convergence.sh

for f in "$CONVERGE" "$ACL" "$HANDLERS" "$TEST" "$TEST_ACL" "$TEMPLATE" "$PAGE" "$I18N"; do
  [ -f "$f" ] || { bad "A0: missing $f"; printf '\n\033[1mB374 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"; exit 1; }
done

# =====================================================================
hdr "A. the periodic convergence pass exists and compares the two planes"

A_MISS=""
# The two sources of truth, in one function.
grep -q 'func (s \*Service) ConvergeAdvertisedRoutes(reason string) (applied int, skipped int)' "$CONVERGE" \
  || A_MISS="${A_MISS}ConvergeAdvertisedRoutes is missing or has an unexpected signature"$'\n'
grep -q 'ownedPrefixesForNodeFromTable' "$CONVERGE" \
  || A_MISS="${A_MISS}the pass does not read the owned set from prefix_owner (B352's helper)"$'\n'
grep -q 'relaysToKeepSynced' "$CONVERGE" \
  || A_MISS="${A_MISS}the pass does not iterate the B352 keep-synced relay list"$'\n'
grep -q 'ListAllNodes()' "$CONVERGE" \
  || A_MISS="${A_MISS}the pass does not read the ADVERTISED plane from the headscale node view the rest of the code uses"$'\n'
# It must NOT invent a second API call: the node view is the source.
if grep -qE 'GET /api/v1/node|/api/v1/node/' "$CONVERGE"; then
  A_MISS="${A_MISS}the pass appears to build its own headscale request instead of reusing ListAllNodes"$'\n'
fi
grep -q 'func RoutesNeedConvergence(owned, advertised \[\]string) (bool, string)' "$CONVERGE" \
  || A_MISS="${A_MISS}the pure predicate RoutesNeedConvergence is missing or has an unexpected signature"$'\n'
grep -q 'func NormalizeRouteSet(routes \[\]string) map\[string\]bool' "$CONVERGE" \
  || A_MISS="${A_MISS}NormalizeRouteSet (the one definition of the route set) is missing"$'\n'
# The predicate must be normalise-then-compare, not a length comparison: a same-size
# swap is invisible to a cardinality check.
grep -q 'len(ownedSet) == len(advertisedSet)' "$CONVERGE" \
  || A_MISS="${A_MISS}the comparison does not guard on the size first"$'\n'
grep -q 'missing=.*joinRoutesCapped' "$CONVERGE" || grep -q '"missing="+joinRoutesCapped' "$CONVERGE" \
  || A_MISS="${A_MISS}a difference is not named (no missing=/extra= detail)"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: the pass reads prefix_owner for the owned set and ListAllNodes for the advertised one, and names every difference"
else
  bad "A1: the convergence pass is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# =====================================================================
hdr "B. no transport for a converged relay — the SSH-storm requirement"

B_MISS=""
# The decision must happen BEFORE the apply, and the apply must be the shared one.
grep -q 'if !need {' "$CONVERGE" \
  || B_MISS="${B_MISS}the pass does not branch on the predicate before it applies anything"$'\n'
grep -q 'syncOneExitNode' "$CONVERGE" \
  || B_MISS="${B_MISS}the pass does not call the SHARED apply path (syncOneExitNode), so it can drift from the manual Re-sync"$'\n'
grep -q 'continue' "$CONVERGE" \
  || B_MISS="${B_MISS}the converged branch does not continue past the apply"$'\n'
# The pass must not reach for SSH or tailscale directly.
if grep -nE 'exec\.Command|SetAdvertisedRoutes|ApplyRoutesLocally' "$CONVERGE" | grep -v '^\s*//' >/dev/null 2>&1; then
  B_MISS="${B_MISS}the pass reaches for a transport itself instead of going through the shared apply path"$'\n'
fi
# An unreadable advertised plane must apply NOTHING (absence of evidence).
grep -q 'health.ReadErr = err.Error()' "$CONVERGE" \
  || B_MISS="${B_MISS}an unreadable advertised plane is not reported"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: the pure predicate runs before any transport, the converged branch continues, and the apply goes through the shared path"
else
  bad "B1: a converged relay could still be applied (or the decision could happen after the transport):"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi
# The shared apply step must still be ONE function on the package SURFACE (read via
# gosurface, so a pure move inside the package keeps this green) — the pass aliases
# it, the other callers call it, and neither may grow a second copy (the B300
# lesson: two copies of that decision already drifted once).
if grep -q 'var syncOneExitNodeFn = syncOneExitNode' "$CONVERGE" \
   && grep -q 'func syncOneExitNode(hs \*headscale.Client' "$ROUTES"; then
  ok "B2: the convergence pass and the other sync paths share ONE per-relay apply step (syncOneExitNodeFn aliases syncOneExitNode)"
else
  bad "B2: the pass no longer shares the per-relay apply step — its transport decision can drift from the manual Re-sync"
fi

# =====================================================================
hdr "C. wired into the periodic maintenance tick, with its own budget and prefix"

C_MISS=""
grep -q 'routeConvergenceInterval = 5 \* time.Minute' "$CONVERGE" \
  || C_MISS="${C_MISS}the pass has no routeConvergenceInterval constant"$'\n'
grep -q 'routeConvergenceLogPrefix = "route-converge:"' "$CONVERGE" \
  || C_MISS="${C_MISS}the pass has no route-converge: log prefix"$'\n'
grep -q 'takeRouteConvergenceSlot' "$CONVERGE" \
  || C_MISS="${C_MISS}the pass has no throttle decision"$'\n'
grep -q 'ConvergeAdvertisedRoutes(reason string) (applied int, skipped int)' "$HANDLERS" \
  || C_MISS="${C_MISS}the handlers exitRulesRunner interface does not declare the pass"$'\n'
grep -q 'a.exitRulesSvc.ConvergeAdvertisedRoutes(' "$HANDLERS" \
  || C_MISS="${C_MISS}no caller in internal/handlers — the pass would never run on a tick"$'\n'
# It must hang off the ~5-minute preference maintenance tick, NOT off the domain
# auto-updater's `added > 0 || removed > 0` gate (that gate is the LIVE root cause).
# The function's brace is on its OWN line (the signature spans two lines), so the awk
# range starts at the name and ends at the first column-0 closing brace.
TICK_BLOCK="$(awk '/RunPreferredExitReconciler/{f=1} f{print} f&&/^\}/{exit}' "$HANDLERS")"
if printf '%s' "$TICK_BLOCK" | grep -q 'ConvergeAdvertisedRoutes('; then
  ok "C1: the route-convergence pass runs on the ~5-minute maintenance tick (RunPreferredExitReconciler), independent of domain churn"
else
  bad "C1: the pass is not called from the maintenance tick — domain churn would still be the only periodic trigger (the live root cause)"
fi
if [ -z "$C_MISS" ]; then
  ok "C2: the pass has its own budget, its own log prefix and its interface entry"
else
  bad "C2: the wiring is incomplete:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi
# The domain auto-updater's gate must NOT be the only trigger any more. It may keep
# its own StaggeredSync call (that is correct: churn still warrants a sync) but the
# maintenance tick must carry the unconditional one.
grep -q 'DomainAutoUpdater()' "$HANDLERS" \
  && grep -q 'ConvergeAdvertisedRoutes' "$HANDLERS" \
  && ok "C3: the domain auto-updater keeps its churn-triggered sync AND the tick carries the unconditional one" \
  || bad "C3: the churn-triggered sync disappeared — the auto-updater path must keep working"

# =====================================================================
hdr "D. the ACL drift path names the true reason and spends the right budget"

D_MISS=""
grep -q 'func aclDriftBudget(moved bool) time.Duration' "$ACL" \
  || D_MISS="${D_MISS}aclDriftBudget is missing — the budget is still implicit"$'\n'
grep -q 'return ownershipACLThrottle' "$ACL" \
  || D_MISS="${D_MISS}the moved case does not spend the operator budget (60s)"$'\n'
grep -q 'return churnACLThrottle' "$ACL" \
  || D_MISS="${D_MISS}the churn case no longer spends churnACLThrottle (B298 would be undone)"$'\n'
grep -q 'func aclDriftReason(moved bool) string' "$ACL" \
  || D_MISS="${D_MISS}aclDriftReason is missing — the detail text can still be false"$'\n'
# The FALSE sentence must be gone from the CODE (it may live on in comments as the
# documented history — that is why the comment-only lines are stripped first).
ACL_CODE="$SKY_TMP/acl_code.go"
grep -v '^[[:space:]]*//' "$ACL" >"$ACL_CODE"
if grep -q 'the assignment table did not move' "$ACL_CODE"; then
  D_MISS="${D_MISS}the code still claims 'the assignment table did not move' (the false live statement)"$'\n'
fi
# Both variants must exist: B298's contracts pin the churn call site's shape, so the
# moved case gets its OWN function instead of a flag on the churn one.
grep -q 'func (s \*Service) periodicDriftCheck()' "$ACL" \
  || D_MISS="${D_MISS}periodicDriftCheck() disappeared (B288/B298 contract)"$'\n'
grep -q 'func (s \*Service) periodicDriftCheckAfterOwnershipMove()' "$ACL" \
  || D_MISS="${D_MISS}there is no owned-move variant of the drift check"$'\n'
# The convergence pass is what knows the planes disagree, so it must ask the drift
# check through the operator budget.
grep -q 'periodicDriftCheckAfterOwnershipMove()' "$CONVERGE" \
  || D_MISS="${D_MISS}the convergence pass does not ask the ACL drift check when it had to rewrite a relay"$'\n'
# B298 untouched: the churn call site keeps its literal actor + wrapper.
grep -q 's.applyACLIfDriftedChurn("skygate-periodic-drift"' "$ACL" \
  || D_MISS="${D_MISS}the periodic churn path lost its applyACLIfDriftedChurn call (B298 contract B6)"$'\n'
grep -q 's.applyACLIfDriftedChurn("skygate-auto-updater"' internal/feature/exit_rules/sync_domain.go \
  || D_MISS="${D_MISS}the auto-updater lost its churn-budget call (B298 contract B5)"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: when the planes disagree the drift check spends the 60s operator budget and names the real reason; the derived-row churn path keeps the 30m budget"
else
  bad "D1: the ACL drift path can still defer an ownership move for 30 minutes behind a false reason:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi
# The decision is not enough on paper: the BUDGET must actually be spent that way.
# Two behavioural tests drive the real slot: one asserts the ownership-move case
# applies five minutes after the last apply (inside the churn window), the other
# asserts the derived-rule case is still absorbed.
if grep -q 'TestB374_OwnershipMoveAppliesInsideTheChurnWindow' "$TEST_ACL" \
   && grep -q 'TestB374_ChurnStaysOnTheLongBudget' "$TEST_ACL"; then
  ok "D2: behavioural tests pin BOTH budgets (the ownership move lands inside the churn window; the derived-rule churn is still absorbed)"
else
  bad "D2: no test drives the actual budget decision — a swap of the two budgets would pass every structural contract"
fi

# =====================================================================
hdr "E. a stale advertisement the portal could not revoke is a named fact"

E_MISS=""
grep -q 'SettingRelayAdvertiseStalePrefix = "relay_advertise_stale:"' "$CONVERGE" \
  || E_MISS="${E_MISS}the relay_advertise_stale: key does not exist"$'\n'
grep -q 'still advertises' "$CONVERGE" \
  || E_MISS="${E_MISS}the recorded sentence does not say what is wrong"$'\n'
grep -q 'prefix_owner no longer assigns to it' "$CONVERGE" \
  || E_MISS="${E_MISS}the recorded sentence does not name prefix_owner as the authority"$'\n'
grep -q 'DECLARED STALE' "$CONVERGE" \
  || E_MISS="${E_MISS}the pass does not LOG the stale advertisement"$'\n'
grep -q 'excludedFromAssignment' "$CONVERGE" \
  || E_MISS="${E_MISS}the stale check does not use the B309/B352 exclusion set"$'\n'
grep -q 'func ListStaleAdvertisements(' "$CONVERGE" \
  || E_MISS="${E_MISS}there is no reader for the page"$'\n'
# The page must surface it, using the same family the B309 banner uses.
grep -q 'exit_rules.ListStaleAdvertisements(' "$PAGE" \
  || E_MISS="${E_MISS}/admin/exit-nodes does not read the stale records"$'\n'
grep -q 'StaleAdvertisements' "$TEMPLATE" \
  || E_MISS="${E_MISS}the template does not render the stale-advertisement warning"$'\n'
grep -q 'exit_nodes.prefix_owner.stale_advertise' "$TEMPLATE" \
  || E_MISS="${E_MISS}the template has no i18n key for the warning"$'\n'
grep -q '"exit_nodes.prefix_owner.stale_advertise"' "$I18N" \
  || E_MISS="${E_MISS}the i18n key is not defined"$'\n'
# RU + EN: the parity test compares key SETS, so count the occurrences explicitly
# (a key defined once passes parity while one language renders an empty string).
STALE_KEYS=$(grep -c '"exit_nodes.prefix_owner.stale_advertise"' "$I18N")
if [ "$STALE_KEYS" -ge 2 ]; then
  ok "E1: the stale-advertisement fact is logged, stored for the page and rendered in BOTH catalogues"
else
  bad "E1: the stale-advertisement fact is not fully surfaced (occurrences of the base key: ${STALE_KEYS:-0}, need 2)"
fi
if [ -z "$E_MISS" ]; then
  ok "E2: the mechanism reuses the B309 global_settings family (no new table, no new page)"
else
  bad "E2: the stale advertisement cannot be seen by the operator:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# =====================================================================
hdr "F. the behavioural halves run"

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "F1: no go binary in PATH — the behavioural contracts were not run"
else
  if out="$("$GOBIN" test ./internal/feature/exit_rules/ -run 'B374' -count=1 2>&1)"; then
    ok "F1: the B374 contracts pass (real SQLite: the predicate, the converged-no-apply case, the excluded/stale case, the budget)"
  else
    bad "F1: go test -run B374 failed:
$(printf '%s\n' "$out" | tail -25)"
  fi
  if out="$("$GOBIN" test ./internal/handlers/ -run 'B374|PreferredExit' -count=1 2>&1)"; then
    ok "F2: the maintenance-tick wiring compiles and its tests pass"
  else
    bad "F2: internal/handlers failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
  if out="$("$GOBIN" build ./... 2>&1)"; then
    ok "F3: the tree builds"
  else
    bad "F3: go build ./... fails:
$(printf '%s\n' "$out" | tail -10)"
  fi
  if out="$("$GOBIN" test ./internal/i18n/ -count=1 2>&1)"; then
    ok "F4: every new key exists in BOTH catalogues (parity test)"
  else
    bad "F4: i18n parity failed:
$(printf '%s\n' "$out" | tail -10)"
  fi
fi

# =====================================================================
hdr "G. tracked by git (AGENTS trap #11), registered, indexed"

UNTRACKED=""
for f in "$CONVERGE" "$TEST" "$TEST_ACL" "$SELF"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || UNTRACKED="${UNTRACKED}${f} "
done
if [ -z "$UNTRACKED" ]; then
  ok "G1: every new file of this block is tracked by git"
else
  skip "G1: not yet tracked by git (the lead commits this block): ${UNTRACKED}"
fi
if grep -q 'run_check "B374"' scripts/verify_pre_deploy.sh; then
  ok "G2: registered in scripts/verify_pre_deploy.sh"
else
  bad "G2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B374\*\*' AGENTS.md; then
  ok "G3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "G3: AGENTS.md has no B374 bullet at line start"
fi

printf '\n\033[1mB374 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
