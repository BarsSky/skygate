#!/usr/bin/env bash
# check_b399_pref_optout.sh
#
# 2026-10-19 (B399) — AN OPERATOR'S "NO" MUST SURVIVE THE NEXT TICK, AND RE-POINTING A
# DEVICE MUST NOT SILENTLY RESTORE A STRICT PIN.
#
# WHY (measured on the operator's installation, verbatim)
#   «теперь несмотря на то что для skyworker я отключаю strict mode по karolina он
#    ставиться обратно»
#
#   The audit log for those six hours, twice:
#
#     19:08:56  skyadmin  my_device_preferred_exit_set   hostname=skyworker tag=
#     18:17:12  system    preferred_exit_reconciled      CREATE pref hostname=skyworker
#               tag=tag:dev-infra-karolina via=1 reason=missing-pref-unanimous rules=269
#     17:30:39  skyadmin  my_device_preferred_exit_set   hostname=skyworker tag=
#     13:17:12  system    preferred_exit_reconciled      CREATE pref hostname=skyworker
#               tag=tag:dev-infra-karolina via=1 reason=missing-pref-unanimous rules=224
#
#   `SetDeviceExitNodePref(…, "")` DELETED the row, so the B229 preferred-exit
#   reconciler saw "a device with rules and no preference" and created one again with
#   `via_enabled = 1` — restoring exactly the filter the operator had just removed.
#   Because `via` on an `autogroup:internet` grant is a FILTER (B398), restoring the
#   pin restores the restriction. A second half sat in the same pass: the reconciler's
#   UPDATE wrote via_enabled=true unconditionally, so re-pointing a device switched a
#   deliberate opt-out back on.
#
# CONTRACTS
#   A. the schema remembers the decision (V080, both chains)
#   B. clearing RECORDS it instead of deleting; setting clears the flag
#   C. the reconciler refuses to re-create it, and carries the strict flag forward
#   D. the operator can SEE the state (both device pages + the exit-node help)
#   E. behavioural pins, registration, documentation
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/db/exit_node_prefs.go ] || {
  printf 'B399: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b399.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

MIG=internal/db/migrations_v0_80_pref_optout.go
PREFS=internal/db/exit_node_prefs.go
PG=internal/db/driver_postgres.go
LITE=internal/db/driver_sqlite.go
REC=internal/feature/exit_rules/reconciler.go
MYH=internal/feature/my/device_exit_pref.go
ADMDEV=internal/feature/admin/devices.go
MYDEV=internal/feature/my/devices.go
T_ADMIN=internal/handlers/templates/admin/devices.html
T_USER=internal/handlers/templates/user/devices.html
T_EXITS=internal/handlers/templates/admin/exit_nodes.html
CAT_MY=internal/i18n/catalog_my.go
CAT_EXITS=internal/i18n/catalog_exit_nodes.go
T_DB=internal/db/exit_node_prefs_b399_test.go
T_REC=internal/feature/exit_rules/reconciler_b399_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

MIG_SRC="$(src "$MIG")"; PREFS_SRC="$(src "$PREFS")"; PG_SRC="$(src "$PG")"; LITE_SRC="$(src "$LITE")"
REC_SRC="$(src "$REC")"; MYH_SRC="$(src "$MYH")"; ADMDEV_SRC="$(src "$ADMDEV")"; MYDEV_SRC="$(src "$MYDEV")"
TA_SRC="$(src "$T_ADMIN")"; TU_SRC="$(src "$T_USER")"; TE_SRC="$(src "$T_EXITS")"
CM_SRC="$(src "$CAT_MY")"; CE_SRC="$(src "$CAT_EXITS")"
TDB_SRC="$(src "$T_DB")"; TREC_SRC="$(src "$T_REC")"

hdr "B399 — the operator's «no» must survive the next tick"

# ---------------------------------------------------------------------------
# A. the schema remembers the decision
# ---------------------------------------------------------------------------
hdr "A — the schema can tell «nobody decided» from «the human said no»"

if has 'opted_out INTEGER NOT NULL DEFAULT 0' "$MIG_SRC"; then
  ok "A1: V080 adds device_exit_node_prefs.opted_out (additive, default 0 — existing installs unchanged)"
else
  bad "A1: no opted_out column — an operator's clear is still indistinguishable from an absence"
fi
if has 'migrateV080PG' "$PG_SRC" && has 'migrateV080SQLite' "$LITE_SRC"; then
  ok "A2: registered in BOTH migration chains (rule 9)"
else
  bad "A2: the migration is missing from one of the chains"
fi
if has 'execSQLiteDDL' "$MIG_SRC"; then
  ok "A3: the SQLite side goes through the single DDL chokepoint"
else
  bad "A3: the SQLite leg does not use execSQLiteDDL"
fi

# ---------------------------------------------------------------------------
# B. clearing RECORDS the decision instead of deleting the row
# ---------------------------------------------------------------------------
hdr "B — a clear is remembered, and an explicit choice clears the memory"

if has 'func ClearDeviceExitNodePref(' "$PREFS_SRC"; then
  ok "B1: ClearDeviceExitNodePref exists (the named writer of a human decision)"
else
  bad "B1: no ClearDeviceExitNodePref — the clear path is still a DELETE"
fi
if has 'opted_out = 1' "$PREFS_SRC"; then
  ok "B2: the clear path writes opted_out = 1"
else
  bad "B2: the clear path does not record the decision"
fi
if has 'return ClearDeviceExitNodePref(d, userID, deviceHostname, setByUserID)' "$PREFS_SRC"; then
  ok "B3: SetDeviceExitNodePref(\"\") routes to the recording path (one implementation)"
else
  bad "B3: the empty-tag path is not routed to the recording writer"
fi
if has 'opted_out = 0' "$PREFS_SRC"; then
  ok "B4: setting a tag clears the flag (a newer explicit decision wins)"
else
  bad "B4: a set does not clear the opt-out — the device could never be pinned again"
fi
if has 'COALESCE(p.opted_out,0)' "$PREFS_SRC"; then
  ok "B5: every reader selects the flag with a COALESCE (an un-migrated row cannot break the JOIN)"
else
  bad "B5: the list/get readers do not carry the flag"
fi
if has 'OptedOut bool' "$PREFS_SRC"; then
  ok "B6: DeviceExitNodePref exposes it (the reconciler and both pages need it)"
else
  bad "B6: the struct does not expose opted_out"
fi

# ---------------------------------------------------------------------------
# C. the reconciler refuses to re-create it
# ---------------------------------------------------------------------------
hdr "C — the engine must not overrule a person"

if has 'if s.ExistingPrefOptedOut {' "$REC_SRC"; then
  ok "C1: the planner checks the opt-out FIRST (before any create branch)"
else
  bad "C1: no opt-out guard — the live create-on-every-tick loop is intact"
fi
if has '"operator-opted-out"' "$REC_SRC"; then
  ok "C2: the skip is NAMED (visible in the journal and the audit, not silence)"
else
  bad "C2: the skip has no name"
fi
if has 'state.ExistingPrefOptedOut = existing.OptedOut' "$REC_SRC"; then
  ok "C3: the state loader carries the flag from the database"
else
  bad "C3: the flag is never populated — the guard above would never fire"
fi
if has 'ViaEnabled:     s.ExistingPrefVia' "$REC_SRC"; then
  ok "C4: an UPDATE carries the EXISTING strict flag (re-pointing is not permission to re-pin)"
else
  bad "C4: an update still hardcodes the strict flag"
fi
if has 'ch.NewTag, 0, ch.ViaEnabled)' "$REC_SRC"; then
  ok "C5: both write sites pass the planned flag instead of the literal true"
else
  bad "C5: a write site still hardcodes true"
fi
if has 'ViaEnabled:     true' "$REC_SRC"; then
  ok "C6: a derived CREATE still defaults to strict (the pre-B399 behaviour is preserved)"
else
  bad "C6: a create no longer sets the flag"
fi

# ---------------------------------------------------------------------------
# D. the operator can see the state
# ---------------------------------------------------------------------------
hdr "D — the state is visible where the decision is made"

if has 'DeviceExitOptedOutMap' "$ADMDEV_SRC" && has 'DeviceExitOptedOutMap' "$TA_SRC"; then
  ok "D1: /admin/devices renders «auto-pick off» for such a device"
else
  bad "D1: the admin page shows an unexplained empty cell"
fi
if has 'DeviceExitOptedOut' "$MYDEV_SRC" && has 'DeviceExitOptedOut' "$TU_SRC"; then
  ok "D2: /my/devices renders it too (the owner can see why nothing happens)"
else
  bad "D2: the user page does not show the state"
fi
ru="$(grep -c '"devices.device_exit_opted_out"' "$CAT_MY" 2>/dev/null || true)"
if [ "${ru:-0}" -ge 2 ]; then
  ok "D3: the badge text exists in RU + EN ($ru entries)"
else
  bad "D3: the badge text is not in both catalogues ($ru entries)"
fi
if has 'exit_nodes.client_routing.title' "$TE_SRC" && has 'exit_nodes.client_routing.body' "$CE_SRC"; then
  ok "D4: /admin/exit-nodes explains why the local network disappears with an exit node"
else
  bad "D4: the exit-node page does not explain the client-side LAN behaviour"
fi
if has 'exit-node-allow-lan-access' "$CE_SRC"; then
  ok "D5: the explanation names the EXACT client switch (--exit-node-allow-lan-access)"
else
  bad "D5: the explanation does not name the fix"
fi
n="$(grep -c '"exit_nodes.client_routing.body"' "$CAT_EXITS" 2>/dev/null || true)"
if [ "${n:-0}" -ge 2 ]; then
  ok "D6: the explanation exists in RU + EN ($n entries)"
else
  bad "D6: the explanation is not in both catalogues ($n entries)"
fi
if has 'client-local' "$TE_SRC" || has 'safeHTML' "$TE_SRC"; then
  ok "D7: the block is rendered as text, not as a hardcoded table row (B325)"
else
  bad "D7: the help block is not rendered from the catalogue"
fi

# ---------------------------------------------------------------------------
# E. behavioural pins, registration, documentation
# ---------------------------------------------------------------------------
hdr "E — pinned by tests, registered, documented"

for n in TestB399_OptedOutIsNeverRecreated TestB399_OptedOutBeatsEveryDerivationPath TestB399_DerivedRowIsStillManaged TestB399_UpdatePreservesTheStrictFlag; do
  if has "func $n(" "$TREC_SRC"; then ok "E1: $n pins the reconciler half"; else bad "E1: $n is missing"; fi
done
for n in TestB399_ClearRecordsTheDecisionInsteadOfDeleting TestB399_SetAfterClearClearsTheFlag TestB399_ClearOnAMissingRowStillRecordsIt; do
  if has "func $n(" "$TDB_SRC"; then ok "E2: $n pins the storage half"; else bad "E2: $n is missing"; fi
done
if git ls-files --error-unmatch scripts/check_b399_pref_optout.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap #11)"
else
  bad "E3: this script is not tracked"
fi
if grep -q 'check_b399_pref_optout' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E4: registered in scripts/verify_pre_deploy.sh"
else
  bad "E4: not registered in the catalog"
fi
if grep -q '^\- \*\*B399\*\*' AGENTS.md 2>/dev/null; then
  ok "E5: one-line entry in the AGENTS.md block index"
else
  bad "E5: no B399 entry in the AGENTS block index"
fi
if grep -q 'B399' docs/ROADMAP.md 2>/dev/null && grep -q 'B399' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E6: both roadmaps record the block (rule 12)"
else
  bad "E6: a roadmap does not record the block"
fi

printf '\n\033[1mB399: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
