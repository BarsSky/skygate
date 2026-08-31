#!/bin/bash
# B188 — fix ghost `tag:exit-<hostname>` exit-node-pref tags
# by routing all form posts through db.NormalizeExitNodeTag
# (which looks up the canonical `tag:dev-infra-<hostname>`
# form from node_owner_map), and a one-time DB migration
# (migrateV061PG) that backfills the legacy rows that the
# v0.28.5 update missed.
#
# Operator 2026-08-25: "почему для устройства basic michail
# недоступен youtube несмотря на правила?" Root-cause chain:
#   1. The /my/devices, /admin/devices, and /my/exit-nodes
#      templates synthesised the legacy `tag:exit-<host>` form
#      inline (printf in the template) instead of reading the
#      real tag from node_owner_map. tag:exit-emilia is NOT in
#      policy tagOwners, so the via=[...] grant headscale sees
#      references a non-existent tag and the policy either
#      silently no-ops or is rejected outright.
#   2. The pre-handler didn't normalise the form's tag, so the
#      ghost tag was written to device_exit_node_prefs.
#   3. The migrateV047PG backfill (intended to set via_enabled=1
#      on pre-existing rows) was guarded by a `freshlyAdded`
#      check that returned false on production (the column
#      pre-existed), so every pref row shipped with
#      via_enabled=0. Without via_enabled=1, the per-device
#      grant in the headscale policy is NEVER emitted with
#      via=, so the user has to manually select the exit-node
#      in Tailscale.
#
# B188 fix (4 layers):
#   1. New helper db.NormalizeExitNodeTag(db, hostname) that
#      looks up node_owner_map and returns the canonical tag.
#   2. PostMyDevicePreferredExit, PostAdminDevicePreferredExit,
#      and PostMyExitNodePreferred call the normaliser BEFORE
#      the DB write (defense in depth: even if a future
#      template sends a legacy form, the DB sees the canonical
#      value).
#   3. The three dropdown templates (user/devices.html,
#      admin/devices.html, user/exit_nodes.html) read
#      NodeView.DevTag (a new field populated by the handler
#      from node_owner_map) instead of synthesising the
#      legacy form inline.
#   4. New migration migrateV061PG backfills existing rows:
#      a. tag:exit-<host> -> tag:dev-infra-<host> (lookup
#         in node_owner_map; rows with no match LEFT ALONE)
#      b. via_enabled=1 for every pre-existing row whose
#         tag points at a real headscale tag (the v0.28.5
#         re-run that the original migrateV047PG missed).
#
# Audit (2026-08-25) — exit-node pre-B188 DB state on the VM:
#   user_exit_node_prefs:
#     1|tag:dev-infra-emilia|0|...  (skyadmin, real tag)
#     6|tag:dev-infra-emilia|1|...  (michail, real tag, via=1)
#   device_exit_node_prefs:
#     1|a71|tag:exit-emilia|0|...         ← BUG
#     1|emilia|tag:dev-infra-emilia|0|... (real tag)
#     1|skygate-host-1|tag:dev-infra-emilia|0|... (real tag)
#     1|skyworker|tag:dev-infra-karolina|0|... (real tag)
#     6|basic|tag:exit-emilia|0|...         ← BUG (the operator's report)
#   All 4 infra nodes (emilia, karolina, sharlotta,
#   skygate-host-1) use the same tag:dev-infra-<host> form,
#   so the B188 migration fixes the 2 ghost rows + re-enables
#   via pinning on the other 3 (which the v0.28.5 missed).
#
# Contracts:
#  A. db.NormalizeExitNodeTag helper exists
#  B. The helper is called in PostMyDevicePreferredExit
#  C. The helper is called in PostAdminDevicePreferredExit
#  D. The helper is called in PostMyExitNodePreferred
#  E. The helper is called in the admin user-subnet page (defense in depth)
#  F. node_owner_map.tag is the source of truth (NodeView.DevTag populated)
#  G. node_owner_map.tag is the source of truth (admin user-subnet dropdown)
#  H. node_owner_map.tag is the source of truth (/my/devices dropdown)
#  I. node_owner_map.tag is the source of truth (/admin/devices dropdown)
#  J. node_owner_map.tag is the source of truth (/my/exit-nodes dropdown)
#  K. Template `user/devices.html` no longer uses tag:exit-% printf
#  L. Template `admin/devices.html` no longer uses tag:exit-% printf
#  M. Template `user/exit_nodes.html` no longer uses tag:exit-% printf
#  N. New migration migrateV061PG exists
#  O. migrateV061PG is registered in the migration chain
#  P. The via_enabled re-enable clause is in migrateV061PG
#  Q. exit_node_prefs_b188_test.go covers NormalizeExitNodeTag
#  R. migrations_v0_61_b188_test.go covers migrateV061PG
#  S. AGENTS.md mentions B188
#  T. verify_pre_deploy.sh includes check_b188
#  U. go build ./... + go vet ./... pass
#  V. (VM-only) live: post-migration device_exit_node_prefs
#     has no `tag:exit-%` rows (the backfill rewrote them)
#  W. (VM-only) live: post-migration via_enabled=1 for the
#     rows that pointed at a real headscale tag (the v0.28.5
#     re-run)
#  X. (VM-only) live: headscale policy has `tag:dev-michail-
#     basic -> h-rule-...` grants with `via: [tag:dev-infra-emilia]`
#     (the operator's reported bug is fixed)

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

check_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "  PASS [$label] $actual"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] expected=$expected got=$actual"
    FAIL=$((FAIL+1))
  fi
}

check_ge() {
  local label="$1" min="$2" actual="$3"
  if [ "$actual" -ge "$min" ] 2>/dev/null; then
    echo "  PASS [$label] actual=$actual (>= $min)"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] actual=$actual (expected >= $min)"
    FAIL=$((FAIL+1))
  fi
}

count() {
  # count(file, pattern) — number of times pattern appears
  local file="$1" pat="$2"
  if [ -f "$file" ]; then
    grep -c -- "$pat" "$file" 2>/dev/null || echo 0
  else
    echo 0
  fi
}

# A. NormalizeExitNodeTag helper exists in db package.
A=$(count "$REPO/internal/db/exit_node_prefs.go" 'func NormalizeExitNodeTag')
check_ge "A-NormalizeExitNodeTag" 1 "$A"

# B. PostMyDevicePreferredExit calls the normaliser.
B=$(count "$REPO/internal/feature/my/device_exit_pref.go" 'NormalizeExitNodeTag')
check_ge "B-PostMyDevicePreferredExit-calls" 1 "$B"

# C. PostAdminDevicePreferredExit calls the normaliser.
# Same file as B; verify by counting the per-user vs per-admin
# distinct call sites. The handler has 2 distinct call blocks
# (one per function), each calling NormalizeExitNodeTag.
C=$(grep -c 'NormalizeExitNodeTag' "$REPO/internal/feature/my/device_exit_pref.go" 2>/dev/null || echo 0)
check_ge "C-PostAdminDevicePreferredExit-calls" 2 "$C"

# D. PostMyExitNodePreferred calls the normaliser.
D=$(count "$REPO/internal/feature/my/exit_nodes.go" 'NormalizeExitNodeTag')
check_ge "D-PostMyExitNodePreferred-calls" 1 "$D"

# E. Admin user-subnet page also uses the normaliser (defense in
# depth — both the dropdown builder and the POST handler do).
E=$(count "$REPO/internal/feature/admin/user_subnet.go" 'NormalizeExitNodeTag')
check_ge "E-admin-user-subnet-calls" 2 "$E"

# F. NodeView.DevTag field exists + is populated in /my/devices.
F1=$(count "$REPO/internal/headscale/nodes.go" 'DevTag')
F2=$(count "$REPO/internal/feature/my/devices.go" 'n.DevTag = tagByHost')
check_ge "F-NodeView-DevTag-field" 1 "$F1"
check_ge "F-my-devices-populates-DevTag" 1 "$F2"

# G. /admin/devices populates NodeView.DevTag.
G=$(count "$REPO/internal/feature/admin/devices.go" 'DevTag')
check_ge "G-admin-devices-populates-DevTag" 2 "$G"

# H. /my/devices populates NodeView.DevTag for public nodes.
H=$(count "$REPO/internal/feature/my/devices.go" 'n.DevTag =')
check_ge "H-my-devices-publicnodes-DevTag" 1 "$H"

# I. /my/exit-nodes populates NodeView.DevTag.
I=$(count "$REPO/internal/feature/my/exit_nodes.go" 'exits\[i\].DevTag')
check_ge "I-my-exit-nodes-populates-DevTag" 1 "$I"

# J. All four exit-node templates now read .DevTag (or fall
# back to the legacy form when DevTag is empty).
J=$(grep -lE 'or \.DevTag' "$REPO/internal/handlers/templates/user/devices.html" \
                        "$REPO/internal/handlers/templates/admin/devices.html" \
                        "$REPO/internal/handlers/templates/user/exit_nodes.html" 2>/dev/null | wc -l)
check_eq "J-three-templates-read-DevTag" "3" "$J"

# K. Template user/devices.html reads .DevTag (post-B188).
# The old broken form was `printf "tag:exit-%s" .Hostname` —
# the new form is `or .DevTag (printf "tag:exit-%s" ...)`,
# so the printf may still appear in the fallback arm. The
# contract is: the template MUST reference .DevTag.
K=$(grep -c '\.DevTag' "$REPO/internal/handlers/templates/user/devices.html" 2>/dev/null || echo 0)
check_ge "K-user-devices-reads-DevTag" 1 "$K"

# L. Template admin/devices.html reads .DevTag.
L=$(grep -c '\.DevTag' "$REPO/internal/handlers/templates/admin/devices.html" 2>/dev/null || echo 0)
check_ge "L-admin-devices-reads-DevTag" 1 "$L"

# M. Template user/exit_nodes.html reads .DevTag.
M=$(grep -c '\.DevTag' "$REPO/internal/handlers/templates/user/exit_nodes.html" 2>/dev/null || echo 0)
check_ge "M-exit-nodes-reads-DevTag" 1 "$M"

# N. New migration migrateV061PG exists.
N=$(count "$REPO/internal/db/migrations_pg.go" 'func migrateV061PG')
check_ge "N-migrateV061PG-exists" 1 "$N"

# O. migrateV061PG is registered in the migration chain
# (1 mention in driver_postgres.go) + the function declaration
# (1 mention in migrations_pg.go). Total >= 2.
O1=$(count "$REPO/internal/db/driver_postgres.go" 'migrateV061PG')
O2=$(count "$REPO/internal/db/migrations_pg.go" 'migrateV061PG')
O=$((O1 + O2))
check_ge "O-migration-chain-registered" 2 "$O"

# P. The via_enabled re-enable UPDATE is in the migration.
P1=$(count "$REPO/internal/db/migrations_pg.go" 'user_exit_node_prefs')
P2=$(grep -A 1 'via_enabled = 1' "$REPO/internal/db/migrations_pg.go" 2>/dev/null | grep -c 'user_exit_node_prefs\|device_exit_node_prefs' || echo 0)
check_ge "P-via-enabled-reenable-clause" 2 "$P2"

# Q. Test file for NormalizeExitNodeTag.
Q=$(count "$REPO/internal/db/exit_node_prefs_b188_test.go" 'TestNormalizeExitNodeTag')
check_ge "Q-NormalizeExitNodeTag-tests" 4 "$Q"

# R. Test file for migrateV061PG.
R=$(count "$REPO/internal/db/migrations_v0_61_b188_test.go" 'TestMigrateV061PG')
check_ge "R-migrateV061PG-tests" 5 "$R"

# S. AGENTS.md mentions B188.
S=$(count "$REPO/AGENTS.md" 'B188')
check_ge "S-AGENTS-md-B188" 1 "$S"

# T. verify_pre_deploy.sh includes check_b188.
if [ -f "$REPO/scripts/verify_pre_deploy.sh" ]; then
  T=$(count "$REPO/scripts/verify_pre_deploy.sh" 'check_b188')
  check_ge "T-verify-includes-check" 1 "$T"
else
  check_eq "T-verify" ">=1" "0"
fi

# U. Build + vet pass. We honour $GO (env var, set by
# the caller — typically PowerShell or the deploy
# script — when bash on a sandboxed shell can't see
# go.exe). Falls back to `command -v go` on platforms
# where go is on PATH.
GO_BIN="${GO:-$(command -v go 2>/dev/null || true)}"
if [ -n "$GO_BIN" ] && (cd "$REPO" && "$GO_BIN" build ./... >/dev/null 2>&1 && "$GO_BIN" vet ./... >/dev/null 2>&1); then
  echo "  PASS [U-build-vet] ok ($GO_BIN)"
  PASS=$((PASS+1))
else
  # On Windows-sandboxed bash where go isn't reachable,
  # this check is a no-op rather than a hard fail.
  if [ -z "$GO_BIN" ]; then
    echo "  SKIP [U-build-vet] go not reachable from this shell; set \$GO to its path to enable"
  else
    echo "  FAIL [U-build-vet] go build or go vet returned non-zero (go=$GO_BIN)"
    FAIL=$((FAIL+1))
  fi
fi

# V. (VM-only) post-migration device_exit_node_prefs has no
# tag:exit-% rows. The migration rewrote the 2 ghost rows
# (a71, basic) to tag:dev-infra-emilia; the migration is
# idempotent so re-runs are no-ops.
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    GHOST_COUNT=$(PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tAc \
      "SELECT COUNT(*) FROM device_exit_node_prefs WHERE exit_node_tag LIKE 'tag:exit-%'" 2>/dev/null)
    check_eq "V-no-ghost-device-rows" "0" "${GHOST_COUNT:-<err>}"
    GHOST_USER_COUNT=$(PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tAc \
      "SELECT COUNT(*) FROM user_exit_node_prefs WHERE exit_node_tag LIKE 'tag:exit-%'" 2>/dev/null)
    check_eq "V-no-ghost-user-rows" "0" "${GHOST_USER_COUNT:-<err>}"
  else
    echo "  SKIP [V] psql not available"
  fi
else
  echo "  SKIP [V] not on VM"
fi

# W. (VM-only) post-migration via_enabled=1 for rows
# pointing at a real headscale tag. The v0.28.5 re-run.
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    RE_ENABLED=$(PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tAc \
      "SELECT COUNT(*) FROM device_exit_node_prefs WHERE via_enabled = 1 AND exit_node_tag LIKE 'tag:dev-infra-%'" 2>/dev/null)
    check_ge "W-via-enabled-reenabled" 1 "${RE_ENABLED:-0}"
  else
    echo "  SKIP [W] psql not available"
  fi
else
  echo "  SKIP [W] not on VM"
fi

# X. (VM-only) headscale policy has tag:dev-michail-basic
# grants with via=[tag:dev-infra-emilia] (the operator's
# reported bug fix). We check that AT LEAST ONE grant with
# src=tag:dev-michail-basic has via=[tag:dev-infra-emilia].
if [ -d /home/skyadmin/skygate ]; then
  if command -v docker >/dev/null 2>&1; then
    X=$(docker exec headscale headscale policy get -o json 2>/dev/null | \
        python3 -c "
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get('grants', []):
    if 'tag:dev-michail-basic' in g.get('src', []) and 'tag:dev-infra-emilia' in (g.get('via') or []):
        n += 1
print(n)
" 2>/dev/null)
    check_ge "X-policy-has-via-for-basic" 1 "${X:-0}"
  else
    echo "  SKIP [X] docker not available"
  fi
else
  echo "  SKIP [X] not on VM"
fi

# Y. skygate acl-apply subcommand exists (B188.1 operator
# escape hatch). Forces a one-shot headscale ACL re-apply
# after a migration that changed exit-node-pref data
# without triggering any of the user-facing handlers.
Y=$(grep -c 'case "acl-apply"' "$REPO/cmd/skygate/main.go" 2>/dev/null || echo 0)
check_ge "Y-acl-apply-subcommand" 1 "$Y"

# Z. (TD-17.1) source: NormalizeExitNodeTag + isExitNodeTagForm
# exist, ErrUserDeviceDevTagNotExitNode sentinel is exported,
# and the user-device dev-tag form (tag:dev-<user>-<host>) is
# explicitly rejected. This pins the 2026-08-27 michail/basic
# data-corruption fix.
Z1=$(grep -c 'func isExitNodeTagForm' "$REPO/internal/db/exit_node_prefs.go" 2>/dev/null || echo 0)
check_ge "Z1-isExitNodeTagForm-defined" 1 "$Z1"
Z2=$(grep -c 'isExitNodeTagForm(tag)' "$REPO/internal/db/exit_node_prefs.go" 2>/dev/null || echo 0)
check_ge "Z2-isExitNodeTagForm-called-from-NormalizeExitNodeTag" 1 "$Z2"
Z3=$(grep -c 'ErrUserDeviceDevTagNotExitNode' "$REPO/internal/db/exit_node_prefs.go" 2>/dev/null || echo 0)
check_ge "Z3-ErrUserDeviceDevTagNotExitNode-defined" 1 "$Z3"
Z4=$(grep -c 'isExitNodeTagForm' "$REPO/internal/db/exit_node_prefs_td17_test.go" 2>/dev/null || echo 0)
check_ge "Z4-TD17-test-file-exists" 1 "$Z4"

# AA. (TD-17.1, VM-only) live: device_exit_node_prefs has
# NO row whose exit_node_tag starts with the user-device dev-tag
# form (tag:dev-<user>-<host>) — the form should be rejected
# at write time and the live data must be clean.
if [ -d /home/skyadmin/skygate ]; then
  if command -v psql >/dev/null 2>&1; then
    USER_DEV_TAGS=$(PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging -tAc \
      "SELECT COUNT(*) FROM device_exit_node_prefs WHERE exit_node_tag LIKE 'tag:dev-_%' AND exit_node_tag NOT LIKE 'tag:dev-infra-%'" 2>/dev/null)
    check_eq "AA-no-user-device-devtag-in-prefs" "0" "${USER_DEV_TAGS:-<err>}"
  else
    echo "  SKIP [AA] psql not available"
  fi
else
  echo "  SKIP [AA] not on VM"
fi

echo
echo "=== B188 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
