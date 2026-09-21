#!/usr/bin/env bash
# check_b276_1_all_devices.sh
#
# 2026-09-21 (B276.1, v1.5.36) — "all my devices" must survive a NEW device.
#
# /my/exit-rules has offered «все мои устройства» since B275.3, but the form only
# materialised the rule for the devices that existed at that moment: `PostMyExitRule`
# inserted one row per device and forgot why. A laptop registered a week later had no
# rule — the user saw a per-user intent in the UI that silently did not cover their new
# device, and nothing anywhere said so.
#
# V074 stores the intent (device_rules.all_devices) and a periodic pass re-materialises
# it for the user's current device set, in the SAME tick and BEFORE that tick's
# policy-drift check (so the ACL generated in that pass already covers the new device).
#
# CONTRACTS
#   A  the intent exists in the schema (both chains) and is written when the form says "all"
#   B  the propagation pass copies exactly the all-devices rules, idempotently
#   C  the pass runs before the ACL drift check in the same tick
#   D  the UI shows which rules are all-devices (RU + EN)
#   E  Go behaviour contracts
#   F  live state (SKIPs when no DB)
#   G  the script itself is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B276.1: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MIG=internal/db/migrations_v0_74_all_devices.go
PGDRV=internal/db/driver_postgres.go
SQDRV=internal/db/driver_sqlite.go
DBH=internal/db/device_rules_all_devices.go
PROP=internal/feature/exit_rules/all_devices.go
FORM=internal/feature/exit_rules/form_my.go
SYNC=internal/feature/exit_rules/sync.go
TMPL=internal/handlers/templates/exit_rules.html
SCHEMA_TEST=internal/db/migrations_sqlite_schema_test.go

hdr "B276.1 — «все мои устройства» applies to devices registered later"

# --- A: the intent in the schema ---------------------------------------------
if [ -f "$MIG" ] && grep -q 'all_devices INTEGER NOT NULL DEFAULT 0' "$MIG"; then
  ok "A1: V074 adds device_rules.all_devices (additive, default 0 — existing rules stay single-device)"
else
  bad "A1: no migration stores the 'all my devices' intent"
fi
if grep -q 'migrateV074PG' "$PGDRV" && grep -q 'migrateV074SQLite' "$SQDRV"; then
  ok "A2: registered in BOTH migration chains (rule 9)"
else
  bad "A2: the migration is missing from one of the chains"
fi
if grep -q 'execSQLiteDDL' "$MIG"; then
  ok "A3: the SQLite side goes through the single DDL chokepoint"
else
  bad "A3: raw DDL on SQLite — addColumnIfMissing would not run"
fi
if grep -q 'device_rules.all_devices exists (V074' "$SCHEMA_TEST" && grep -q 'maxV != 74' "$SCHEMA_TEST"; then
  ok "A4: the SQLite schema test asserts the column and that the chain reaches V74"
else
  bad "A4: the schema test would not notice a missing V074 on SQLite"
fi
if grep -q 'db.MarkDeviceRulesAllDevices(s.dbc(), c.UserID, exitNode, typeToInsert, ip)' "$FORM"; then
  ok "A5: saving for «все мои устройства» records the intent on the fan-out group"
else
  bad "A5: the fan-out still forgets why the copies exist"
fi
ALLDEV_LINE="$(grep -n 'allDevices := devRaw == "all"' "$FORM" | head -1 | cut -d: -f1)"
MARK_LINE="$(grep -n 'db.MarkDeviceRulesAllDevices(s.dbc(), c.UserID, exitNode, typeToInsert, ip)' "$FORM" | head -1 | cut -d: -f1)"
MARK_N="$(grep -c 'db.MarkDeviceRulesAllDevices(' "$FORM")"
if [ -n "$ALLDEV_LINE" ] && [ -n "$MARK_LINE" ] && [ "$MARK_LINE" -gt "$ALLDEV_LINE" ] && [ "$MARK_N" = "1" ]; then
  ok "A6: the intent is written only inside the all-devices branch (line $MARK_LINE, after the guard on line $ALLDEV_LINE)"
else
  bad "A6: the marker is not scoped to the all-devices branch (guard=$ALLDEV_LINE mark=$MARK_LINE calls=$MARK_N)"
fi

# --- B: the propagation pass --------------------------------------------------
if [ -f "$PROP" ] && grep -q 'func (s \*Service) propagateAllDeviceRules() (int, error)' "$PROP"; then
  ok "B1: propagateAllDeviceRules exists"
else
  bad "B1: nothing re-materialises the intent for a new device"
fi
if grep -q 'ListAllDeviceRuleGroups' "$DBH" && grep -q 'DeviceIDsForPortalUser' "$DBH"; then
  ok "B2: it reads the all-devices groups and the user's CURRENT devices from node_owner_map"
else
  bad "B2: the propagation has no source of truth for either side"
fi
if grep -q 'errors.Is(ferr, db.ErrNotFound)' "$PROP" && grep -q 'insertRuleUnique' "$PROP"; then
  ok "B3: rows are matched on the natural key first, so re-running the pass cannot duplicate a rule"
else
  bad "B3: the pass would duplicate rules on every tick"
fi
if grep -q 'MarkDeviceRulesAllDevices' "$PROP"; then
  ok "B4: the copies keep the intent (otherwise the NEXT pass would stop covering new devices)"
else
  bad "B4: the copies lose the intent and are treated as single-device rules"
fi
if grep -q "no attributed device" "$PROP"; then
  ok "B5: a rule whose owner has no attributed device is reported instead of silently skipped"
else
  bad "B5: the unattributed case is silent again"
fi
if grep -q 'GROUP BY user_id, COALESCE(exit_node_id' "$DBH"; then
  ok "B6: one group per (user, exit node, type, value) — not one per row"
else
  bad "B6: the groups are read per row, so the pass would churn"
fi

# --- C: ordering inside the tick ---------------------------------------------
PROP_LINE="$(grep -n 's.propagateAllDeviceRules()' "$SYNC" | head -1 | cut -d: -f1)"
DRIFT_LINE="$(grep -n 's.applyACLIfDrifted("skygate-auto-updater"' "$SYNC" | head -1 | cut -d: -f1)"
if [ -n "$PROP_LINE" ] && [ -n "$DRIFT_LINE" ] && [ "$PROP_LINE" -lt "$DRIFT_LINE" ]; then
  ok "C1: the propagation runs BEFORE the drift check in the same tick (line $PROP_LINE < $DRIFT_LINE), so the ACL covers the new device immediately"
else
  bad "C1: the new rows would wait a whole tick for their grants (propagation=$PROP_LINE drift=$DRIFT_LINE)"
fi

# --- D: the UI shows it -------------------------------------------------------
if grep -q 'func (s \*Service) allDeviceRuleKeys(userID int64) map\[string\]bool' "$PROP" \
   && grep -q 'allDeviceRuleKey(' "$PROP"; then
  ok "D1: the view resolves which rows are all-devices rules (one key builder for both sides)"
else
  bad "D1: the user cannot tell a rule that follows their new device from one that does not"
fi
if grep -q 'AllDevices bool' internal/db/device_rules.go && grep -q '\.AllDevices = keys\[' "$FORM"; then
  ok "D2: DeviceRule carries the marker and the page fills it in"
else
  bad "D2: the marker never reaches the template"
fi
if grep -q 'all_devices_badge' "$TMPL"; then
  ok "D3: the rule row renders the badge"
else
  bad "D3: no badge in the template"
fi
KEYS=0
for k in all_devices_badge all_devices_badge_tip; do
  n="$(grep -c "exit_rules.$k\"" internal/i18n/catalog_exit_rules.go)"
  [ "$n" -ge 2 ] && KEYS=$((KEYS+1))
done
if [ "$KEYS" -ge 2 ]; then
  ok "D4: both keys exist in RU and EN (2/2 pairs), and the tooltip explains the propagation"
else
  bad "D4: only $KEYS/2 keys are present in both catalogues"
fi

# --- E: behaviour -------------------------------------------------------------
if [ -f internal/feature/exit_rules/all_devices_b276_1_test.go ] \
   && grep -q 'func TestB2761_PropagationCoversADeviceRegisteredLater' internal/feature/exit_rules/all_devices_b276_1_test.go; then
  ok "E1: the functional gap is reproduced (a device registered later inherits the rule, a single-device rule is NOT copied)"
else
  bad "E1: no behavioural test for the propagation"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(timeout 600 go test -count=1 -run 'B2761' ./internal/feature/exit_rules/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "E2: the B276.1 contracts pass (covers a new device, idempotent, labels the rows, marks the group)"
  else
    bad "E2: the B276.1 contracts failed: $OUT"
  fi
  OUT2="$(timeout 600 go test -count=1 -run 'SQLiteSchema' ./internal/db/ 2>&1)"
  if grep -q '^ok' <<< "$OUT2" && ! grep -q '^FAIL' <<< "$OUT2"; then
    ok "E3: the SQLite schema test passes with V074 (parity with PostgreSQL)"
  else
    bad "E3: the schema test failed: $OUT2"
  fi
else
  skip "E2/E3: go not on PATH"
fi

# --- F: live state ------------------------------------------------------------
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'skygate-pg-local'; then
  N="$(docker exec skygate-pg-local psql -U admin -d skygate_staging -t -A -c "SELECT COUNT(*) FROM device_rules WHERE all_devices = 1" 2>/dev/null | tr -d '[:space:]')"
  if [ -n "${N:-}" ]; then
    ok "F1: $N row(s) carry the all-devices intent on the live database (0 is normal before the first save since V074)"
  else
    skip "F1: the all_devices column is not in this database yet (the migration runs on the next start)"
  fi
else
  skip "F1: no local skygate PostgreSQL to inspect (live checks belong on the VM)"
fi

# --- G: tracked by git (trap #11) --------------------------------------------
if git ls-files --error-unmatch scripts/check_b276_1_all_devices.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b276_1_all_devices.sh is tracked by git"
else
  bad "G1: scripts/check_b276_1_all_devices.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB276.1 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
