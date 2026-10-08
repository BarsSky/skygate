#!/usr/bin/env bash
# check_b352_ownership_convergence.sh
#
# 2026-10-05 (B352) — the ownership decision must converge, and a relay that cannot be
# configured must not own prefixes.
#
# The live incident, in order (measured on the reference deployment):
#
#   19:44:10  karolina's route application times out — ONE SSH hiccup
#   19:54:07  the B309 exclusion fires: karolina leaves the healthy set and 251 of its
#             claimed prefixes move to emilia/shardlotta
#   19:54:10  the very next application to karolina SUCCEEDS — and nothing re-runs the
#             assignment, so it still owns ZERO prefixes 40 minutes later
#   20:2x     `basic`'s 21 prefixes are pinned via=emilia/via=sharlotta while the device
#             is pinned to karolina: no access, with every server-side fact green
#
# Three structural defects came out of it:
#
#   1. the ownership pass only ran from a sync, so a recovered relay never reclaimed
#      its prefixes (nothing recomputes the decision on the maintenance tick);
#   2. a relay skygate had NEVER applied routes to was a legal owner — sharlotta owned
#      95 prefixes while advertising 2, because the sync loops are built from the rules
#      and no rule ever named it;
#   3. an INSERT did not regenerate the ACL (only a CHANGE did), so a rebuilt table
#      left every per-CIDR `via=` pin naming the previous owner;
#
# plus the reason the stale advertisement survived at all: the sync lists come from
# `device_rules.exit_node_id`, so a relay that loses its last rule is never visited
# again and keeps its last `--advertise-routes` forever.
#
# CONTRACTS
#   A. the decision converges on its own (tick) and an INSERT re-applies the ACL
#   B. only a relay skygate has configured may take prefixes
#   C. a relay that lost its prefixes has its advertisement pruned
#   D. the two live ACL contracts follow the OWNER, not a frozen relay name
#   E. bookkeeping

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B352: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# sync.go was split three ways (2026-10-08, pure move): the ownership
# re-apply (sync_acl.go), the tick body (sync_domain.go) and the prune/
# keep-synced operands (sync_routes.go).
SYNC_ACL=internal/feature/exit_rules/sync_acl.go
SYNC_DOMAIN=internal/feature/exit_rules/sync_domain.go
SYNC_ROUTES=internal/feature/exit_rules/sync_routes.go
B309=internal/feature/exit_rules/relay_transport_b309.go
KEEPALIVE=internal/feature/exit_rules/relay_keepalive_b352.go
TESTS=internal/feature/exit_rules/relay_keepalive_b352_test.go

hdr "B352 — ownership converges, and an unconfigurable relay owns nothing"

# --- A: convergence ------------------------------------------------------------
A_MISS=""
# A1: an INSERT is a decision — the ACL must be regenerated for ins > 0 as well.
grep -q 'if ins > 0 || chg > 0 {' "$SYNC_ACL" \
  || A_MISS="${A_MISS}reconcilePrefixOwnership still re-applies the ACL only on CHANGE (ins > 0 is a decision too)"$'\n'
grep -q 'applyACLAfterOwnershipChange(ins, chg)' "$SYNC_ACL" \
  || A_MISS="${A_MISS}applyACLAfterOwnershipChange is not called from the ownership pass"$'\n'
# A2: the maintenance tick must run the ownership pass, or a recovered relay waits for
# a human to press Sync (the live 40-minute gap).
TICK_BLOCK=$(awk '/^func \(s \*Service\) DomainAutoUpdater/,/^}/' "$SYNC_DOMAIN")
if printf '%s' "$TICK_BLOCK" | grep -q 's.reconcilePrefixOwnership()'; then
  ok "A1: the ownership pass is part of the auto-updater tick, and an INSERT re-applies the ACL"
else
  bad "A1: the auto-updater tick never reconciles ownership — a relay that recovers after one failed apply keeps none of its prefixes until somebody presses Sync:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi
[ -z "$A_MISS" ] || bad "A1b: the ACL re-apply condition is still change-only:$(printf '\n%s' "$A_MISS")"
grep -q 'TestB352_RecoveredRelayReclaimsItsClaimedPrefixes' "$TESTS" \
  || bad "A2: no test drives the eviction → recovery → reclaim sequence"
grep -q 'TestB352_FirstAssignmentReappliesACL' "$TESTS" \
  || bad "A3: no test pins that a first-time assignment regenerates the ACL"
[ -f "$TESTS" ] && grep -q 'TestB352_RecoveredRelayReclaimsItsClaimedPrefixes' "$TESTS" && grep -q 'TestB352_FirstAssignmentReappliesACL' "$TESTS" \
  && ok "A2: behavioural tests cover the reclaim sequence and the insert-only pass"

# --- B: only a configured relay may own ----------------------------------------
B_MISS=""
grep -q 'proven' "$B309" \
  || B_MISS="${B_MISS}healthyExitRelaysForAssignment has no notion of a PROVEN relay"$'\n'
grep -q 'st.At > 0 && st.OK' "$B309" \
  || B_MISS="${B_MISS}a relay is not required to have a recorded SUCCESSFUL apply to be a candidate"$'\n'
grep -q 'TestB352_NeverAppliedRelayIsNotAnAssignmentCandidate' "$TESTS" \
  || B_MISS="${B_MISS}no test pins that a never-applied relay cannot take prefixes"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: a relay skygate has never applied routes to cannot take prefixes while a proven relay exists"
else
  bad "B1: the candidate set can still hand prefixes to a relay skygate cannot configure (live: sharlotta owned 95 prefixes while advertising 2):"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi
# The B309 default must survive: with nothing proven yet, the healthy set is returned.
# Assert the CODE (the fallback return), not a comment string — B352.1 renamed the
# comment and a comment-pinned contract failed for the wording, which is the L-50 class.
if grep -q 'return append(append(proven, staleFailure...), neverApplied...)' "$B309"; then
  ok "B2: with nothing proven yet every healthy relay is returned (a fresh install keeps working, and one aged-out failure cannot empty the candidate set)"
else
  bad "B2: the never-applied rule no longer has its fresh-install fallback"
fi

# --- C: prune the stale advertisement -----------------------------------------
C_MISS=""
grep -q 'func (s \*Service) relaysToKeepSynced' "$KEEPALIVE" \
  || C_MISS="${C_MISS}relaysToKeepSynced does not exist"$'\n'
grep -q 'SettingRelayApplyStatePrefix' "$KEEPALIVE" \
  || C_MISS="${C_MISS}the keep-synced list ignores the recorded apply state"$'\n'
grep -q 's.relaysToKeepSynced(' "$SYNC_ROUTES" \
  || C_MISS="${C_MISS}no sync path uses the keep-synced list"$'\n'
grep -q 'hasRelayApplyRecord(node)' "$SYNC_ROUTES" \
  || C_MISS="${C_MISS}the per-row Re-sync still answers info=no rules and touches nothing"$'\n'
grep -q "exit_node_id <> ''" "$SYNC_ROUTES" \
  || C_MISS="${C_MISS}the sync still builds a node named \"\" from rules with no exit node"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: a relay that lost its prefixes is visited and its advertisement pruned to the owned set (incl. the empty set)"
else
  bad "C1: a stale advertisement still survives a relay losing its prefixes:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi
grep -q 'TestB352_RelaysToKeepSynced' "$TESTS" && grep -q 'TestB352_PruneTargetIsTheOwnedSet' "$TESTS" \
  && ok "C2: behavioural tests cover the keep-synced list and the empty owned set" \
  || bad "C2: no test pins the keep-synced list / the prune target"

# --- D: the live contracts must follow the owner, not a frozen relay -----------
# Only NON-COMMENT lines count: the scripts legitimately document the historical
# `tag:dev-infra-emilia` state in their headers (the B188 migration audit quotes the
# live rows it repaired), so a naive grep would fail on the very explanation of why the
# contract changed.
code_lines() { grep -v '^[[:space:]]*#' "$1"; }
D_MISS=""
if code_lines scripts/check_b188.sh | grep -q 'tag:dev-infra-emilia'; then
  D_MISS="${D_MISS}check_b188.sh still hardcodes tag:dev-infra-emilia in an ASSERTION (B275 makes the owner a living decision)"$'\n'
fi
grep -q 'X-policy-pins-name-the-owner' scripts/check_b188.sh \
  || D_MISS="${D_MISS}check_b188.sh X was not renegotiated"$'\n'
if code_lines scripts/check_b188_2.sh | grep -q 'T-per-cidr-rules-pinned-via-emilia'; then
  D_MISS="${D_MISS}check_b188_2.sh still asserts the frozen emilia pin"$'\n'
fi
grep -q 'T-per-cidr-pins-name-the-owner' scripts/check_b188_2.sh \
  || D_MISS="${D_MISS}check_b188_2.sh T was not renegotiated"$'\n'
grep -q 'prefix_owner' scripts/check_b188.sh && grep -q 'prefix_owner' scripts/check_b188_2.sh \
  || D_MISS="${D_MISS}the renegotiated contracts do not compare against the assignment table"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: the two live ACL contracts assert 'every pin names its prefix's owner' instead of a frozen relay name"
else
  bad "D1: a live contract is still pinned to a relay that the assignment table may legitimately move away from:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- D2: the "Use Tailscale IP" button must write the TAILNET address --------------
# B352.2: it called LookupExitServerSSHTarget, the sync path's "effective target"
# resolver, which returns an operator-set ssh_target FIRST — so on the one row the button
# exists for (a public ssh_target the deployment cannot reach) it echoed that value back
# and the row did not change. Live: emilia answered «SSH target set to Tailscale IP:
# root@<its own public IP>».
D2_MISS=""
grep -q 'func TailscaleSSHTargetFor' internal/db/exit_servers.go \
  || D2_MISS="${D2_MISS}db.TailscaleSSHTargetFor does not exist"$'\n'
grep -q 'db.TailscaleSSHTargetFor(s.dbc(), hostname)' internal/feature/admin/exit_nodes_handlers.go \
  || D2_MISS="${D2_MISS}PostAdminExitNodeUseTailscaleIP does not use the tailnet resolver"$'\n'
# Comment lines are stripped first: the handler DOCUMENTS the resolver it must not use,
# and a naive grep on the body would fail on that explanation (the same trap the B188
# check hit).
D2_BUTTON=$(python3 - <<'PY'
import re
src = open("internal/feature/admin/exit_nodes_handlers.go").read()
m = re.search(r"func \(s \*Service\) PostAdminExitNodeUseTailscaleIP.*?\n}\n", src, re.S)
if not m:
    print("handler-not-found")
else:
    body = "\n".join(l for l in m.group(0).splitlines() if not l.strip().startswith("//"))
    print("uses-effective-resolver" if "LookupExitServerSSHTarget" in body else "ok")
PY
)
case "$D2_BUTTON" in
  ok) ;;
  *) D2_MISS="${D2_MISS}the button's body still reaches the effective-target resolver ($D2_BUTTON)"$'\n' ;;
esac
grep -q 'TestTailscaleSSHTargetFor_IgnoresTheOperatorTarget_B352_2' internal/db/exit_servers_b352_2_test.go 2>/dev/null \
  || D2_MISS="${D2_MISS}no test pins the two resolvers apart"$'\n'
if [ -z "$D2_MISS" ]; then
  ok "D2: the Use-Tailscale-IP button writes the relay's tailnet address, while the sync path keeps preferring an explicit ssh_target"
else
  bad "D2: the button and the sync path share one resolver, so the button can echo the value it must replace:"
  printf '%s' "$D2_MISS" | sed 's/^/       /' >&2
fi

# --- E: bookkeeping ------------------------------------------------------------
E_MISS=""
git ls-files --error-unmatch scripts/check_b352_ownership_convergence.sh >/dev/null 2>&1 \
  || E_MISS="${E_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
grep -q 'check_b352_ownership_convergence.sh' scripts/verify_pre_deploy.sh \
  || E_MISS="${E_MISS}verify_pre_deploy.sh does not register B352 — the contract would never run"$'\n'
grep -q 'B352' AGENTS.md \
  || E_MISS="${E_MISS}AGENTS.md's block index has no B352 entry (AGENTS rule 2)"$'\n'
if [ -z "$E_MISS" ]; then
  ok "E1: tracked, registered in the catalog, indexed in AGENTS.md"
else
  bad "E1: the block bookkeeping is incomplete:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# The live half is REPORT-ONLY here on purpose: these are post-deploy facts (the same
# scope lesson B351's D2 learned — a pre-deploy catalog must not redden a commit for a
# state the running build has not produced yet). The assertions live in
# scripts/verify_post_deploy.sh (R-B352).
if command -v docker >/dev/null 2>&1 && sudo -n docker ps >/dev/null 2>&1; then
  OWNERS=$(timeout 20 sudo -n docker exec skygate-pg-local psql -U admin -d skygate_staging -tAc \
    "SELECT DISTINCT exit_node_id FROM prefix_owner" 2>/dev/null | tr -d '\r' | tr '\n' ' ')
  if [ -z "$OWNERS" ]; then
    skip "E2: live prefix_owner not reachable from here (run this script on the reference VM)"
  else
    skip "E2: live prefix_owner owners = ${OWNERS}— informational; R-B352 in verify_post_deploy.sh asserts that each of them has a recorded successful apply"
  fi
else
  skip "E2: docker unavailable or passwordless sudo not configured"
fi

printf '\n\033[1mB352 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
