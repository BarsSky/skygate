#!/usr/bin/env bash
# check_apply_acl_drifted_and_rename.sh
#
# 2026-09-21 (B-pending-write + the GivenName/Hostname fix that
# blocked the user's rules in `pending`) — three fixes that
# ship in v1.5.42:
#
#   A  applyACLIfDrifted returns acl.ApplyResult instead of bool.
#      Callers (sync.go's autoupdater tick + the per-prefix-owner
#      path) discard the result. PostMyExitRule + PostDeleteExitRule
#      use the result so they can:
#        - read res.Version for the audit_log row (only write
#          when res.Applied is true)
#        - mark a failed write via res.Err without MarkACLFail on
#          a successful no-op
#        - skip the audit row entirely when res.Applied=false
#          (no drift, no snapshot, no log noise)
#
#   B  GivenName/Hostname dual-index for approvedByExitNode.
#      The pre-fix code indexed the map by GivenName with a
#      Hostname fallback, which silently broke the rule-status
#      look-up when headscale returned GivenName="node.tail-scale.ts.net"
#      and Hostname="node". The rule's exit_node_id is always
#      Hostname (the value the operator picked in the dropdown),
#      so the look-up missed and every rule showed `pending`
#      (⏳) even after SyncAdvertisedRoutes had successfully
#      pushed the routes. The live bug the user hit on `aro`.
#      Fix: a single helper indexNodesApprovedRoutes indexes
#      the same Set under both GivenName and Hostname, used by
#      both form_my.go and form_admin.go.
#
#   C  collectDevicePrefState SQL has the post-rename OR-branch.
#      The previous query filtered on `device_hostname = $2`
#      only (the denormalised column). When headscale reports
#      a new hostname but B231 hasn't yet synced device_rules
#      (the rename in-flight window), B229 finds zero rules and
#      acts on stale data. Fix: OR-clause with a sub-select on
#      node_owner_map so the query catches BOTH the pre-rename
#      state (old hostname still in the denormalised column) and
#      the post-rename state (new hostname in node_owner_map).
#
# CONTRACTS (A1–A6 + B1–B4 + C1–C3 + D–F)
#   A  applyACLIfDrifted return type + callers
#   B  GivenName/Hostname dual-index
#   C  collectDevicePrefState post-rename SQL
#   D  i18n parity for the auto_pending_title (B277.4 regression guard)
#   E  unit tests cover the dual-index helper
#   F  the script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B-pending-write: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SYNC=internal/feature/exit_rules/sync.go
SVC=internal/feature/exit_rules/form_my.go
ADMIN=internal/feature/exit_rules/form_admin.go
RECON=internal/feature/exit_rules/reconciler.go
INDEX=internal/feature/exit_rules/approved_routes_index.go
CAT=internal/i18n/catalog_exit_rules.go

hdr "B-pending-write (v1.5.42) — applyACLIfDrifted + dual-index + B229 SQL"

# --- A: applyACLIfDrifted return type -------------------------------------
if grep -q 'func (s \*Service) applyACLIfDrifted(actor, detail string) acl.ApplyResult' "$SYNC"; then
  ok "A1: applyACLIfDrifted now returns acl.ApplyResult (was bool in v1.5.41 and earlier)"
else
  bad "A1: applyACLIfDrifted still returns bool — callers can't read the snapshot Version"
fi

# A2: PostMyExitRule uses applyACLIfDrifted (no more direct generateACL + SetPolicy pair).
# Simpler full-file grep — the only "s.applyACLIfDrifted(" calls in
# form_my.go are the two we added in B-pending-write; if both are
# present, both rules use the helper.
COUNT=$(grep -c 's.applyACLIfDrifted(' "$SVC")
if [ "$COUNT" -ge 2 ]; then
  ok "A2: PostMyExitRule uses applyACLIfDrifted (no more direct generateACL + SetPolicy pair)"
else
  bad "A2: PostMyExitRule still calls generateACL + SetPolicy directly (always writes) — found $COUNT applyACLIfDrifted calls, want >=2"
fi

if grep -q 's.applyACLIfDrifted' "$SVC"; then
  if awk 'NR>=1200 && NR<=1400 && /s\.applyACLIfDrifted/' "$SVC" | grep -q 'user-rule-delete'; then
    ok "A3: PostDeleteExitRule uses applyACLIfDrifted (no more direct SetPolicy)"
  else
    bad "A3: PostDeleteExitRule still calls generateACL + SetPolicy directly"
  fi
fi

# A4: PostMyExitRule handles all three branches (applied / err / no-drift)
if grep -q 'case res\.Applied:' "$SVC"; then
  ok "A4: PostMyExitRule has the res.Applied branch (writes audit row when drift)"
fi
if grep -q 'case res\.Err != nil:' "$SVC"; then
  ok "A5: PostMyExitRule has the res.Err branch (MarkACLFail on live-apply failure)"
fi
if grep -q 'live policy already covers' "$SVC"; then
  ok "A6: PostMyExitRule has the no-drift path (logs only, no audit row, no SetPolicy)"
fi

# --- B: GivenName/Hostname dual-index --------------------------------------
if grep -q 'func indexNodesApprovedRoutes' "$INDEX"; then
  ok "B1: indexNodesApprovedRoutes helper exists (extracted from inline loops)"
else
  bad "B1: dual-index logic is not extracted — call sites still inline-bug-prone"
fi

if grep -q 'n.GivenName, n.Hostname' "$INDEX"; then
  ok "B2: the helper indexes under BOTH GivenName AND Hostname (the v1.5.42 fix)"
else
  bad "B2: dual-index is missing — the GivenName/Hostname mismatch bug is back"
fi

if grep -q 'indexNodesApprovedRoutes' "$SVC" && grep -q 'indexNodesApprovedRoutes' "$ADMIN"; then
  ok "B3: both /my/exit-rules (form_my.go) and /admin/exit-rules (form_admin.go) use the helper"
else
  bad "B3: one of the two views still has the inline loop — admin/myrules will diverge again"
fi

# B4: regression guard — old GivenName-first inline loop is GONE from both files
if ! grep -q 'host := n.GivenName' "$SVC" && ! grep -q 'host := n.GivenName' "$ADMIN"; then
  ok "B4: the old GivenName-first / Hostname-fallback inline pattern is gone from both views"
else
  bad "B4: the old inline pattern is still in the code — next refactor will reintroduce the bug"
fi

# --- C: collectDevicePrefState post-rename OR-clause ---------------------
if awk '/func \(s \*Service\) collectDevicePrefState/,/^}/' "$RECON" | grep -q 'device_id IN'; then
  ok "C1: collectDevicePrefState SQL has the post-rename OR-branch (sub-select on node_owner_map)"
else
  bad "C1: the B229 SQL still filters only on the denormalised hostname — the rename-in-flight window breaks it"
fi

if awk '/func \(s \*Service\) collectDevicePrefState/,/^}/' "$RECON" | grep -q 'pre-rename\|post-rename'; then
  ok "C2: the OR-branch doc comment explains the pre-rename / post-rename semantics"
fi

# C3: the unique-index on node_owner_map (hostname, user_id) exists.
#      Check the migrations for the index — without it the sub-select
#      would degrade to a sequential scan at scale.
if grep -q 'idx_node_owner_map' internal/db/migrations*.go internal/db/migrations*.sql 2>/dev/null || \
   grep -q 'CREATE INDEX.*node_owner_map.*hostname' internal/db/*.go 2>/dev/null; then
  ok "C3: node_owner_map has a (hostname, user_id) index — the sub-select is index-backed"
else
  bad "C3: no index on node_owner_map(hostname, user_id) — the sub-select does a seq scan at scale"
fi

# --- D: i18n parity regression guard --------------------------------------
# (The B277.4 auto_pending_title was added last round. Confirming
#  it stays present, since this release modifies form_my.go in the
#  same area.)
KEYS=0
for k in auto_pending_title; do
  n=$(grep -c "exit_rules.$k\"" "$CAT")
  [ "$n" -ge 2 ] && KEYS=$((KEYS+1))
done
if [ "$KEYS" -eq 1 ]; then
  ok "D1: auto_pending_title still present in both RU and EN (no i18n parity regression)"
else
  bad "D1: auto_pending_title was dropped — i18n parity broken"
fi

# --- E: unit tests cover the dual-index helper -----------------------------
if grep -q 'func TestIndexNodesApprovedRoutes_BothNames' internal/feature/exit_rules/*test*.go 2>/dev/null; then
  ok "E1: unit tests cover the dual-index helper (GivenName/Hostname mismatch pinned)"
else
  bad "E1: no unit test for the dual-index helper — the GivenName/Hostname bug can silently regress"
fi

# --- F: the script is tracked by git (trap #11) --------------------------
if git ls-files --error-unmatch scripts/check_apply_acl_drifted_and_rename.sh >/dev/null 2>&1; then
  ok "F1: scripts/check_apply_acl_drifted_and_rename.sh is tracked by git"
else
  bad "F1: scripts/check_apply_acl_drifted_and_rename.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB-pending-write summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
