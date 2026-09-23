#!/usr/bin/env bash
# check_b303_ownerless_device.sh
#
# 2026-09-23 (B303) — an ownerless device must have a working admin path, and no
# admin action on /admin/devices may answer with a raw error page.
#
# LIVE REPORT (operator screenshot, /admin/devices):
#
#   POST /admin/devices/transfer on a device that "turned out to belong to
#   nobody" opened a SEPARATE PAGE whose entire body was:
#
#       node not in node_owner_map: db: node_owner_map: no row
#
#   plus: "a device not bound by a tag is not given to the admin for tagging
#   (as if it were ignored by the scripts that should check and add devices)".
#
# ROOT CAUSE — one deadlock, two symptoms, all three pieces in node_owner_map:
#   1. PostAdminDeviceTransfer read the CURRENT row first and refused when it
#      was missing: the one button that exists to give a node an owner demanded
#      that it already had one.
#   2. PostAdminNodeTag recorded ownership only when headscale named a user AND
#      the tagged-devices branch called UpdateNodeOwnerTag (an UPDATE) — which
#      matches no row when the node has none. The tag landed in headscale, the
#      node stayed absent from node_owner_map, every per-device ACL rule missed
#      it, and the next Transfer click hit symptom 1.
#   3. findAdoptionCandidates dropped ownerless nodes (empty UserName / no
#      portal_users row / synthetic tagged-devices) from the adoption card, so
#      the page offered no action at all for them.
#
# CONTRACTS
#   A. Transfer treats a missing node_owner_map row as "no current owner" and
#      performs the assignment; a REAL db error is still refused (as a flash)
#   B. Tag always persists ownership, and cannot silently skip the live read
#   C. every refusal in both handlers is a 303 flash on /admin/devices — the
#      literal raw-error string from the screenshot is gone
#   D. the adoption card offers an explicit owner picker for ownerless rows
#   E. the regression tests exist and pass
#   F. the B257 contract that pinned the old skip rules is renegotiated in place
#   G. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B303: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DEV=internal/feature/admin/devices.go
ADOPT=internal/feature/admin/adopt_devices.go
TPL=internal/handlers/templates/admin/devices.html
RU=internal/i18n/catalog_my.go
TEST=internal/feature/admin/devices_b303_test.go
B257=internal/feature/admin/adopt_devices_b257_test.go

hdr "B303 — an ownerless device must be adoptable, and never a raw error page"

# --- A: transfer treats a missing row as the adoption it is --------------------
if grep -q 'errors.Is(err, db.ErrNodeOwnerNotFound)' "$DEV"; then
  ok "A1: Transfer distinguishes 'no row yet' from a real db error"
else
  bad "A1: Transfer still fails on a missing node_owner_map row"
fi
if grep -q 'currentRow = &db.NodeOwner{NodeID: nodeIDStr}' "$DEV" \
   && grep -q 'ownerless = true' "$DEV"; then
  ok "A2: the missing row becomes an empty current owner (the assignment proceeds)"
else
  bad "A2: the ownerless branch is missing — the deadlock is back"
fi
if grep -q 'device_transfer_adopted_ownerless' "$DEV"; then
  ok "A3: the audit log tells an adoption apart from a reassignment"
else
  bad "A3: an adoption through Transfer is not auditable as such"
fi
if grep -q 'SetNodeOwnerHostnameIfEmpty(s.dbc(), nodeIDStr, liveHostname)' "$DEV"; then
  ok "A4: the new row is stamped with the live hostname (B272.5)"
else
  bad "A4: an adopted-through-Transfer row would keep an empty hostname"
fi
if grep -q 'has an empty hostname in headscale' "$DEV"; then
  ok "A5: an empty hostname is refused instead of building an invalid dev-tag"
else
  bad "A5: an empty hostname would produce 'tag:dev-<user>-'"
fi

# --- B: tag always records ownership ------------------------------------------
if grep -q 'InsertIgnoreNodeOwnerWithHostname' "$DEV" \
   && grep -q 'errors.Is(gerr, db.ErrNodeOwnerNotFound)' "$DEV"; then
  ok "B1: Tag INSERTs the ownership row when the node has none"
else
  bad "B1: Tag can still land a tag with no node_owner_map row"
fi
if grep -q 'db.UpdateNodeOwnerTag(s.dbc(), nodeIDStr, tag, c.UserID)' "$DEV"; then
  ok "B2: an EXISTING row keeps the UPDATE path (tag bump, metadata preserved)"
else
  bad "B2: the existing-row branch disappeared"
fi
if grep -q 'node_tag_owner_row_created' "$DEV"; then
  ok "B3: creating the row is audited (B303)"
else
  bad "B3: the row creation is silent"
fi
if grep -q 'cannot read nodes from headscale' "$DEV" \
   && ! grep -q 'if nodes, err := hs.ListAllNodes(); err == nil {' "$DEV"; then
  ok "B4: the live node read is mandatory (its failure can no longer pass as an empty tag list)"
else
  bad "B4: the swallowed ListAllNodes error is back — the exit-node guard would run blind"
fi

# --- C: no raw error page ------------------------------------------------------
if grep -q 'http.Error(w, "node not in node_owner_map' "$DEV"; then
  bad "C1: the exact raw error string from the operator's screenshot is still there"
else
  ok "C1: the raw 'node not in node_owner_map' page is gone"
fi
if grep -q 'func (s \*Service) devicesFlashErr(w http.ResponseWriter, r \*http.Request, msg string)' "$DEV"; then
  ok "C2: the shared flash helper exists (log + ?err= redirect)"
else
  bad "C2: the flash helper is missing"
fi
XFER_ERRS=$(sed -n '/func (s \*Service) PostAdminDeviceTransfer/,/^}/p' "$DEV" | grep -c 'http.Error')
if [ "$XFER_ERRS" -eq 1 ]; then
  ok "C3: Transfer keeps exactly one http.Error (the 403 for a non-admin caller)"
else
  bad "C3: Transfer has $XFER_ERRS http.Error call(s) — every operator-facing refusal must be a flash"
fi
TAG_ERRS=$(sed -n '/func (s \*Service) PostAdminNodeTag/,/^}/p' "$DEV" | grep -c 'http.Error')
if [ "$TAG_ERRS" -eq 1 ]; then
  ok "C4: Tag keeps exactly one http.Error (the 403 for a non-admin caller)"
else
  bad "C4: Tag has $TAG_ERRS http.Error call(s) — expected only the 403"
fi

# --- D: the adoption card offers an owner picker -------------------------------
if grep -q 'NeedsOwnerPick' "$ADOPT" \
   && [ "$(grep -c 'NeedsOwnerPick: true' "$ADOPT")" -ge 2 ]; then
  ok "D1: the classifier returns owner-pick candidates for BOTH ownerless shapes"
else
  bad "D1: ownerless nodes are still classified away"
fi
if grep -q 'devices.adoption_pick_owner' "$TPL" \
   && grep -q 'select name="target_username"' "$TPL" \
   && grep -q 'range \$u := \$.TransferTargets' "$TPL"; then
  ok "D2: the card renders a real owner dropdown for those rows"
else
  bad "D2: the card cannot name an owner for an ownerless row"
fi
for k in adoption_pick_owner adoption_button_pick adoption_no_owner adoption_ownerless_hint; do
  cnt="$(grep -cF "\"devices.$k\"" "$RU" 2>/dev/null || echo 0)"
  if [ "$cnt" -ge 2 ]; then
    ok "D3: i18n key present (RU+EN): devices.$k"
  else
    bad "D3: i18n key devices.$k only in $cnt/2 maps (need RU+EN)"
  fi
done

# --- E: the regression tests ---------------------------------------------------
for t in TestPostAdminDeviceTransfer_OwnerlessNodeIsAdopted_B303 \
         TestPostAdminDeviceTransfer_MissingRowIsNotRawError_B303 \
         TestPostAdminDeviceTransfer_HeadscaleDownNamesIt_B303 \
         TestPostAdminNodeTag_RecordsOwnershipWithoutARow_B303 \
         TestPostAdminNodeTag_HeadscaleUnreadableIsAFailureNotASilentTag_B303 \
         TestPostAdminNodeTag_UnknownNodeIsAFlash_B303; do
  if grep -qF "func $t" "$TEST"; then
    ok "E1: $t exists"
  else
    bad "E1: $t must exist in $TEST"
  fi
done
if grep -q 'TestClassifyNodeForAdoption_EmptyUserNameBecomesOwnerPick_B303' "$B257" \
   && grep -q 'TestClassifyNodeForAdoption_OrphanHeadscaleUserBecomesOwnerPick_B303' "$B257"; then
  ok "E2: the renegotiated classifier rules are pinned as tests"
else
  bad "E2: the renegotiated classifier rules are not pinned"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B303' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "E3: the B303 tests pass"
  else
    bad "E3: the B303 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "E3: go not on PATH — run the B303 tests on the VM"
fi

# --- F: the renegotiated B257 contract ----------------------------------------
if grep -q 'Renegotiated by B303' scripts/check_b257_adopt_devices.sh; then
  ok "F1: check_b257 documents the B303 renegotiation of its skip rules"
else
  bad "F1: check_b257 still pins the pre-B303 skip rules"
fi
if grep -q 'TestClassifyNodeForAdoption_EmptyUserNameBecomesOwnerPick_B303' scripts/check_b257_adopt_devices.sh; then
  ok "F2: check_b257's test list points at the new names"
else
  bad "F2: check_b257's test list is stale"
fi

# --- G: git --------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b303_ownerless_device.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b303_ownerless_device.sh is tracked by git"
else
  bad "G1: scripts/check_b303_ownerless_device.sh is NOT tracked"
fi

printf '\n\033[1mB303 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
