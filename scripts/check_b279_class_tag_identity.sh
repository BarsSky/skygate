#!/usr/bin/env bash
# check_b279_class_tag_identity.sh
#
# 2026-09-22 (B279, v1.5.46) — "class tag is not a node identity".
#
# LIVE CASE (native host `aro`): the operator's only relay carried the
# CLASS tag `tag:exit-node` and nothing else. Four independent copies of
# "strip tag: to get a hostname" disagreed about that value:
#
#   internal/acl/acl.go              -> "node", caller treats as non-match (correct)
#   internal/feature/exit_rules      -> "node" used AS a hostname (the bug)
#   internal/db/exit_node_prefs.go   -> accepted it as a per-node tag
#   internal/feature/admin/user_subnet.go -> had its own inline guard
#
# What the bug produced, all from audit_log / journal of the live host:
#   * `my_preferred_exit_set tag=tag:exit-node` — the class tag stored as
#     the operator's preferred exit-node;
#   * `my_exit_rules_apply_preferred preferred=node updated=21` (and
#     again `updated=3` eight minutes later) — 24 device_rules rows
#     rewritten to a relay that does not exist (headscale had
#     exit-node-vps, workpc, laptop, homepc — no "node");
#   * `prefix-owner: node drops 19 claimed prefix(es) owned by another
#     relay` — nothing advertised the prefixes, so nothing was approved
#     and every rule stayed pending;
#   * the operator's per-node tag in node_owner_map was reverted within
#     one tick, because exit-node-monitor's auto-sync fed headscale's
#     `Tags[0]` (= the class tag) back into the row.
#
# CONTRACTS
#   A. one predicate exists (db/tag_kind.go) and knows the class set
#   B. NormalizeExitNodeTag refuses a class tag (ErrClassTagNotPerNode)
#   C. TagToHostname refuses a class tag (returns "")
#   D. the reconciler clears a class-tag preference and never derives one
#   E. no producer takes headscale's Tags[0] any more (PickPerNodeTag)
#   F. the headscale→DB syncs cannot overwrite a per-node tag with a class tag
#   G. bulk-apply validates its target against live exit nodes
#   H. the assignment table's relays are synced even when no rule names them
#   I. /my/exit-rules names the device (paged enrichment)
#   J. the UI stops offering "store the class tag" (no ghost-tag fallback)
#   K. i18n keys exist in RU + EN
#   L. the B279 regression tests pass
#   M. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B279: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

KIND=internal/db/tag_kind.go
PREFS=internal/db/exit_node_prefs.go
NOM=internal/db/node_owner_map.go
CHECK=internal/feature/exit_rules/preferred_check.go
RECON=internal/feature/exit_rules/reconciler.go
HYG=internal/feature/exit_rules/pref_hygiene.go
SYNC=internal/feature/exit_rules/sync.go
PO=internal/feature/exit_rules/prefix_owner.go
FORMMY=internal/feature/exit_rules/form_my.go
STORE=internal/feature/exit_rules/store.go
MON=internal/monitoring/exit_node_monitor.go
TPL=internal/handlers/templates/user/exit_nodes.html
MYEXIT=internal/feature/my/exit_nodes.go

hdr "B279 (v1.5.46) — a class tag names a role, not a node"

# --- A: the shared predicate -------------------------------------------------
if [ -f "$KIND" ]; then
  ok "A1: internal/db/tag_kind.go exists (the single predicate)"
else
  bad "A1: internal/db/tag_kind.go is missing — the four copies can disagree again"
fi
for fn in IsClassTag IsPerNodeTag PickPerNodeTag; do
  if grep -q "^func $fn(" "$KIND" 2>/dev/null; then
    ok "A2: $fn is defined"
  else
    bad "A2: $fn is missing from $KIND"
  fi
done
for tag in 'tag:exit-node' 'tag:public' 'tag:private' 'tag:subnet-router'; do
  if grep -q "\"$tag\"" "$KIND" 2>/dev/null; then
    ok "A3: the class set lists $tag"
  else
    bad "A3: $tag is not listed as a class tag — it would be treated as a node identity"
  fi
done

# --- B: NormalizeExitNodeTag refuses a class tag -----------------------------
if grep -q 'IsClassTag(tag)' "$PREFS"; then
  ok "B1: NormalizeExitNodeTag checks IsClassTag before returning a tag"
else
  bad "B1: NormalizeExitNodeTag does not refuse class tags (this is how tag:exit-node became a node's 'canonical tag')"
fi
if grep -q 'var ErrClassTagNotPerNode' "$PREFS"; then
  ok "B2: ErrClassTagNotPerNode exists (the failure is named, not generic)"
else
  bad "B2: ErrClassTagNotPerNode is missing"
fi
# B279.1 moved the logic itself into tag_kind.go (IsExitNodeTagForm), so the
# chain is now isExitNodeTagForm -> IsExitNodeTagForm -> IsPerNodeTag.
if grep -q 'func isExitNodeTagForm' "$PREFS" && grep -q 'return IsExitNodeTagForm(tag)' "$PREFS"; then
  ok "B3: isExitNodeTagForm delegates to db.IsExitNodeTagForm (TD-17.1 + B279 in one place)"
else
  bad "B3: isExitNodeTagForm no longer delegates to db.IsExitNodeTagForm — the form check is duplicated again"
fi
if grep -q 'if !IsPerNodeTag(t)' "$KIND" 2>/dev/null; then
  ok "B3b: db.IsExitNodeTagForm is built on IsPerNodeTag (one predicate, one class-tag rule)"
else
  bad "B3b: db.IsExitNodeTagForm does not consult IsPerNodeTag"
fi

# --- C: TagToHostname refuses a class tag ------------------------------------
if grep -q 'db.IsClassTag(t)' "$CHECK"; then
  ok "C1: exit_rules.TagToHostname guards on db.IsClassTag"
else
  bad "C1: TagToHostname would strip tag:exit- off tag:exit-node and return the phantom hostname \"node\""
fi

# --- D: reconciler clears / never derives ------------------------------------
if grep -q 'class-tag-pref-cleared' "$RECON"; then
  ok "D1: PlanDevicePrefChange clears an existing class-tag preference"
else
  bad "D1: a stored class-tag preference is left armed"
fi
if grep -q 'db.IsClassTag(s.CanonicalTag)' "$RECON"; then
  ok "D2: a preference is never DERIVED from a class tag"
else
  bad "D2: the reconciler can still auto-create a preference from a class tag"
fi
if grep -q 'case "clear"' "$RECON"; then
  ok "D3: applyReconcilerChange handles the clear action (dry-run + live)"
else
  bad "D3: the clear action has no applier — the change would be reported and dropped"
fi
if [ -f "$HYG" ] && grep -q 'func (s \*Service) ClearClassTagPrefs' "$HYG"; then
  ok "D4: ClearClassTagPrefs exists (user-level rows + devices without rules)"
else
  bad "D4: ClearClassTagPrefs is missing — a class-tag preference can survive forever"
fi
if grep -q 'ClearClassTagPrefs(ctx)' internal/handlers/handlers.go; then
  ok "D5: the reconciler tick calls ClearClassTagPrefs"
else
  bad "D5: ClearClassTagPrefs is never called"
fi

# --- E: no producer uses headscale's Tags[0] --------------------------------
# The live reverter: the monitor fed Tags[0] (the class tag) back into
# node_owner_map on every tick. PickPerNodeTag is the replacement.
# Comment lines are excluded (the fix documents what it replaced), and the
# producer is captured before matching — `awk | grep -q` under `pipefail`
# fails spuriously when grep -q exits at the first match (AGENTS trap #9).
STRAY=$(grep -rn 'Tags\[0\]' --include='*.go' internal cmd 2>/dev/null \
  | grep -v '_test.go' | grep -v 'tag_kind.go' | grep -vE ':[0-9]+:[[:space:]]*//' | wc -l | tr -d '[:space:]')
if [ "${STRAY:-1}" = "0" ]; then
  ok "E1: no production code takes headscale's Tags[0] (order decided the DB tag before B279)"
else
  bad "E1: $STRAY production site(s) still take Tags[0]:"
  grep -rn 'Tags\[0\]' --include='*.go' internal cmd 2>/dev/null \
    | grep -v '_test.go' | grep -v 'tag_kind.go' | grep -vE ':[0-9]+:[[:space:]]*//' | sed 's/^/       /' >&2
fi
PICKERS=$(grep -rl 'PickPerNodeTag' --include='*.go' internal cmd 2>/dev/null | grep -v '_test.go' | wc -l | tr -d '[:space:]')
if [ "${PICKERS:-0}" -ge 5 ]; then
  ok "E2: PickPerNodeTag is used by $PICKERS production file(s) (monitor, telegram, admin, first-run sync, nodeownership)"
else
  bad "E2: PickPerNodeTag is used by only ${PICKERS:-0} production file(s), want >= 5"
fi

# --- F: the headscale→DB syncs cannot clobber a real tag --------------------
if grep -q 'ELSE node_owner_map.tag' "$NOM"; then
  ok "F1: SyncNodesFromHeadscale keeps the existing tag when headscale sends a class tag"
else
  bad "F1: SyncNodesFromHeadscale still does `tag = excluded.tag` unconditionally — the aro revert"
fi
if grep -q "tag = '' OR tag = 'tag:untagged'" "$NOM"; then
  ok "F2: SyncTagsFromHeadscale only lets a class tag FILL an empty/untagged row"
else
  bad "F2: SyncTagsFromHeadscale can overwrite a per-node tag with a class tag"
fi
if grep -q 'IsClassTag(want)' "$NOM"; then
  ok "F3: SyncTagsFromHeadscale branches on IsClassTag (the decision is named)"
else
  bad "F3: SyncTagsFromHeadscale has no class-tag branch"
fi

# --- G: bulk-apply validates its target -------------------------------------
# Capture the function body first: `awk | grep -q` under `pipefail` can
# fail spuriously (AGENTS trap #9 — grep -q exits at the first match and
# awk dies with SIGPIPE).
APPLY_PREF_BODY="$(awk '/func \(s \*Service\) PostMyExitRulesApplyPreferred/,/^}/' "$FORMMY" 2>/dev/null)"
if grep -q 'ListExitNodes' <<< "$APPLY_PREF_BODY"; then
  ok "G1: apply-preferred checks the preferred hostname against the LIVE exit nodes before writing"
else
  bad "G1: apply-preferred can still write a relay that does not exist (the live aro 21-rule rewrite)"
fi
if grep -q 'is not a live exit-node' <<< "$APPLY_PREF_BODY"; then
  ok "G2: the refusal is logged with the phantom name"
else
  bad "G2: the refusal path is not logged (silent again)"
fi

# --- H: assignment-table relays are synced ----------------------------------
if grep -q 'func OwnedPrefixesForRelay' "$PO"; then
  ok "H1: OwnedPrefixesForRelay exists (the table IS the candidate list)"
else
  bad "H1: OwnedPrefixesForRelay is missing — table-owned prefixes can be advertised by nobody"
fi
if grep -q 'relaysFromAssignment(owners, exitRoutes)' "$SYNC"; then
  ok "H2: the sync loop adds the relays the assignment table names"
else
  bad "H2: the sync loop still iterates only device_rules.exit_node_id"
fi
LOSERS_BODY="$(awk '/func reportPrefixLosers/,/^}/' "$SYNC" 2>/dev/null)"
if grep -q 'owners\[c.Prefix\]' <<< "$LOSERS_BODY"; then
  ok "H3: reportPrefixLosers compares against the assignment table (it used to ignore its owners parameter)"
else
  bad "H3: the losers report still describes a different decision than the loop applies"
fi

# --- I: /my/exit-rules names the device -------------------------------------
if grep -q 'func (s \*Service) enrichDeviceNames' "$STORE"; then
  ok "I1: enrichDeviceNames exists (extracted for the paged path)"
else
  bad "I1: enrichDeviceNames is missing"
fi
if grep -q 's.enrichDeviceNames(rules)' "$FORMMY"; then
  ok "I2: the paged GET handler enriches DeviceName (rules grouped under \"2\" before)"
else
  bad "I2: /my/exit-rules still groups by raw device_id"
fi

# --- J: the UI stops offering the class tag ---------------------------------
if grep -q 'NoPerNodeTag' "$TPL" && grep -q 'NoPerNodeTag' "$MYEXIT"; then
  ok "J1: /my/exit-nodes marks nodes without a per-node tag and explains the fix"
else
  bad "J1: /my/exit-nodes still offers 'Set preferred' for a class-tagged node"
fi
if ! grep -q 'printf "tag:exit-%s"' "$TPL"; then
  ok "J2: the synthesised ghost tag (tag:exit-<host>) is gone from the template"
else
  bad "J2: the template still synthesises tag:exit-<host>, which exists in no tagOwners"
fi
if ! grep -q 'tag = "tag:exit-" + n.Hostname' internal/feature/admin/user_subnet.go; then
  ok "J3: the admin dropdown no longer falls back to the ghost tag"
else
  bad "J3: the admin dropdown still falls back to tag:exit-<host>"
fi

# --- K: i18n parity for the new keys ----------------------------------------
for key in exit_nodes.no_per_node_tag exit_nodes.no_per_node_tag_help user_subnet.preferred_exit_no_per_node_tag user_subnet.preferred_exit_class_tag_refused; do
  n=$(grep -c "\"$key\"" internal/i18n/*.go 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
  if [ "${n:-0}" -ge 2 ]; then
    ok "K1: $key present in RU + EN"
  else
    bad "K1: $key found ${n:-0} time(s), want 2 (RU + EN)"
  fi
done

# --- L: the regression tests pass -------------------------------------------
for tf in internal/db/tag_kind_b279_test.go internal/feature/exit_rules/preferred_check_b279_test.go; do
  if [ -f "$tf" ]; then
    ok "L1: $tf exists"
  else
    bad "L1: $tf is missing"
  fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ ./internal/feature/exit_rules/ -run 'B279' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "L2: the B279 Go tests pass"
  else
    bad "L2: the B279 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "L2: go not on PATH — run the B279 tests on the VM"
fi

# --- M: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b279_class_tag_identity.sh >/dev/null 2>&1; then
  ok "M1: scripts/check_b279_class_tag_identity.sh is tracked by git"
else
  bad "M1: scripts/check_b279_class_tag_identity.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB279 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
