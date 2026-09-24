#!/usr/bin/env bash
# check_b316_device_mesh_ownership.sh
#
# 2026-09-24 (B316, v1.5.81) — a device must not lose its device-to-device grants because
# headscale rewrote its owner.
#
# OPERATOR REPORT (native host aro): `workpc` and `homepc` did not ping each other over
# their tailnet addresses although both are under one user, and the panel showed the
# contradiction — the row said daniil, the PER-DEVICE ACL column said «—». His own wording:
# «skygate считает эти устройства как у пользователя daniil как и должно быть но во все
# устройства отображает такую картину по тегам, из-за чего скорей всего и идет конфликт».
#
# MEASURED CAUSE (read-only, both hosts):
#
#   aro:        node_owner_map  2 workpc  tagged-devices  tag:dev-daniil-workpc
#                               3 laptop tagged-devices  tag:dev-daniil-laptop
#                               6 homepc daniil          tag:dev-daniil-homepc
#               live policy     0 grants of the form tag:dev-* → tag:dev-*
#   agent VM:   node_owner_map  every row a REAL portal username
#               live policy     13 tag→tag grants (6+3+4, one per device)
#
# headscale rewrites a tagged node's user to the synthetic `tagged-devices` (B287), and the
# device mesh grouped devices BY THAT COLUMN (`db.GetPerUserDeviceTags`, a JOIN on
# portal_users). So on aro daniil appeared to have ONE device, writePerDeviceGrants skipped
# him (`len(userTags) < 2 → continue`) and the tailnet had NO device-to-device grant at
# all — silently, on both machines that were online.
#
# CONTRACTS
#   A. the mesh source resolves the owner from the ownership row, then the device's own tag,
#      then the user its rules were created under
#   B. devices nobody can attribute are RETURNED and named (never silently dropped), and
#      they are never granted to anybody
#   C. the ACL generators (both of them) use that source and log what cannot be resolved
#   D. the repair rewrites only a provable sentinel row, is idempotent, and names each change
#   E. the repair runs from the periodic maintenance tick (so a fresh install self-heals)
#   F. the panel's per-device ACL column uses the same resolved source
#   G. the tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B316: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

OWNER=internal/db/device_owner_b316.go
ACL=internal/acl/acl.go
PERDEV=internal/acl/acl_perdevice.go
AUTO=internal/nodeownership/auto.go
ADMIN=internal/feature/admin/devices.go
TESTDB=internal/db/device_owner_b316_test.go
TESTACL=internal/acl/acl_b316_test.go

hdr "B316 — the device mesh follows the DEVICE, not a username headscale rewrote"

# --- A: the resolve half ----------------------------------------------------------
if grep -q 'func MeshDevices(' "$OWNER" && grep -q 'func DeviceTagsForMesh(' "$OWNER"; then
  ok "A1: the mesh source exists (MeshDevices + the ACL-shaped DeviceTagsForMesh)"
else
  bad "A1: the resolved mesh source is missing"
fi
if grep -q 'case username != "" && username != SentinelDeviceOwner && portalHas(portal, username):' "$OWNER" \
   && grep -q 'if u, ok := PerDeviceTagUser(tag); ok && portalHas(portal, u)' "$OWNER"; then
  ok "A2: the owner comes from the row, then from the device's own tag"
else
  bad "A2: the tag is not used as an ownership source"
fi
if grep -q 'ruleOwnerForDevice(ruleOwner, nodeID, hostname)' "$OWNER"; then
  ok "A3: the user its RULES were created under is the last resort"
else
  bad "A3: the rules-side owner is not consulted"
fi
if grep -q 'PerDeviceTagUser(tag)' "$OWNER" && ! grep -q 'tag:dev-tagged-devices' "$OWNER"; then
  ok "A4: the tag parser is B288's, and the synthetic owner is never turned into a tag"
else
  bad "A4: the sentinel could be minted into a tag (the B284 class of bug)"
fi
if grep -q 'const SentinelDeviceOwner = "tagged-devices"' "$OWNER"; then
  ok "A5: the synthetic owner is a named constant (one spelling everywhere)"
else
  bad "A5: the sentinel is spelled ad hoc"
fi
# `?` is a PostgreSQL syntax error (SQLSTATE 42601 — see internal/db/placeholders.go): the
# aro install that NEEDS this repair is SQLite while the agent VM is PostgreSQL, so the same
# SQL text must be valid on both. Caught live: the repair used `?` and would have died with a
# syntax error on the very hosts that run PostgreSQL.
if ! grep -nE "IN \('', \?\)|SET username = \?|node_id = \?" "$OWNER" | grep -q .; then
  ok "A6: the repair's SQL uses \$N placeholders (\`?\` is a PG syntax error)"
else
  bad "A6: the repair uses a \`?\` placeholder — it would fail on PostgreSQL:"
  grep -nE "IN \('', \?\)|SET username = \?|node_id = \?" "$OWNER" | sed 's/^/       /' >&2
fi

# --- B: nothing is dropped silently ------------------------------------------------
if grep -q 'func MeshTagsByHost(' "$OWNER" && grep -q 'problems\[h\] = u.Reason' "$OWNER"; then
  ok "B1: the page projection returns the devices it could NOT resolve, with a reason"
else
  bad "B1: unresolved devices are not reported to the page"
fi
if grep -q 'cannot join the device mesh and get NO device-to-device grant' "$ACL"; then
  ok "B2: the generator names what it could not attribute"
else
  bad "B2: an unattributable device is still dropped in silence"
fi
if grep -q 'adopt them on /admin/devices (or fix their tag)' "$ACL"; then
  ok "B3: the log line says what to DO about it"
else
  bad "B3: the warning names no fix"
fi

# --- C: both generators ------------------------------------------------------------
n=$(grep -c 'meshTagsByUser, unattributed, meshErr := db.DeviceTagsForMesh(d)' "$ACL" || true)
if [ "${n:-0}" -eq 2 ]; then
  ok "C1: BOTH generators (live format + plane) use the resolved mesh source"
else
  bad "C1: DeviceTagsForMesh appears ${n:-0} time(s) in acl.go, want 2"
fi
n=$(grep -c 'writePerDeviceGrants(&sb, usernames, meshTagsByUser)' "$ACL" || true)
if [ "${n:-0}" -eq 2 ]; then
  ok "C2: both call sites pass the resolved map to the mesh writer"
else
  bad "C2: writePerDeviceGrants calls with the resolved map: ${n:-0}, want 2"
fi
if grep -q 'falling back to the ownership JOIN' "$ACL"; then
  ok "C3: a lookup failure degrades to the old behaviour instead of emitting no mesh at all"
else
  bad "C3: a lookup failure is unhandled"
fi
if grep -q 'len(userTags) < 2 {' "$PERDEV"; then
  ok "C4: the single-device guard is still there (it is correct — with one device there is no pair)"
else
  bad "C4: the mesh writer changed shape unexpectedly"
fi

# --- D: the repair -----------------------------------------------------------------
if grep -q 'func RepairSentinelDeviceOwners(' "$OWNER"; then
  ok "D1: the repair exists"
else
  bad "D1: no repair for the sentinel rows"
fi
if grep -q "WHERE LOWER(COALESCE(username, '')) IN ('', \$1)" "$OWNER"; then
  ok "D2: it only touches rows whose username is the sentinel or empty (never a real owner)"
else
  bad "D2: the repair could overwrite a real owner"
fi
if grep -q 'if !ok || !portalHas(portal, user) {' "$OWNER"; then
  ok "D3: it refuses an owner it cannot prove (tag must parse AND name a portal user)"
else
  bad "D3: the repair may invent an owner"
fi
if grep -q 'from "+ch.tag+"' "$OWNER"; then
  ok "D4: every change is reported, with the tag it was derived from"
else
  bad "D4: repairs are silent"
fi

# --- E: it runs by itself ----------------------------------------------------------
if grep -q 'db.RepairSentinelDeviceOwners(dbConn.Current())' "$AUTO"; then
  ok "E1: the maintenance tick repairs the rows (a fresh install self-heals)"
else
  bad "E1: the repair never runs outside a manual call"
fi
if grep -q 'rejoin the ACL device mesh' "$AUTO"; then
  ok "E2: the tick logs the consequence of the repair"
else
  bad "E2: the tick does not say what the repair achieved"
fi

# --- F: the page -------------------------------------------------------------------
if grep -q 'db.MeshTagsByHost(s.dbc())' "$ADMIN"; then
  ok "F1: the per-device ACL column uses the resolved source (no more «—» on a device that has a tag)"
else
  bad "F1: the page still reads the username JOIN only"
fi
if grep -q 'has no device-to-device ACL entry' "$ADMIN"; then
  ok "F2: the page logs the devices it cannot resolve"
else
  bad "F2: the page is silent about them"
fi

# --- G: tests + git -----------------------------------------------------------------
for f in "$TESTDB" "$TESTACL"; do
  if [ -f "$f" ]; then ok "G1: $f exists"; else bad "G1: $f is missing"; fi
done
if grep -q "tag:dev-daniil-workpc" "$TESTDB" && grep -q "tag:dev-daniil-workpc" "$TESTACL"; then
  ok "G2: both test files use the LIVE aro fixture (tagged-devices + tag:dev-daniil-*)"
else
  bad "G2: the tests do not reproduce the live data"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ ./internal/acl/ ./internal/nodeownership/ -run 'B316|Mesh|Repair' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "G3: the B316 tests pass"
  else
    bad "G3: the B316 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "G3: go not on PATH — run the B316 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b316_device_mesh_ownership.sh >/dev/null 2>&1; then
  ok "G4: scripts/check_b316_device_mesh_ownership.sh is tracked by git"
else
  bad "G4: scripts/check_b316_device_mesh_ownership.sh is NOT tracked"
fi
# The registration in verify_pre_deploy.sh must name THIS file EXACTLY. Caught live on CI:
# the run_check line pointed at `scripts/check_B316_…` (capital B) while the file is
# `scripts/check_b316_…`, so `test -f` failed with no output and the catalog reported a bare
# FAIL B316 that no local run could reproduce (a local run invokes the script directly).
if grep -q "scripts/check_b316_device_mesh_ownership.sh" scripts/verify_pre_deploy.sh; then
  ok "G5: the gate registers this script by its real (lower-case) path"
else
  bad "G5: the run_check line in verify_pre_deploy.sh does not name scripts/check_b316_device_mesh_ownership.sh"
fi

printf '\n\033[1mB316 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
