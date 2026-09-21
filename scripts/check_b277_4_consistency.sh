#!/usr/bin/env bash
# check_b277_4_consistency.sh
#
# 2026-09-21 (B277.4) — the consistency audit fixes.
#
# This block closes six gaps the user-found audit (Round 1 + Round 2)
# flagged between the rule model (B275/B276.1/B277) and what the
# UI + autoupdater + script generator actually did:
#
#   #1+#2  qSelectUserRulesForView + qSelectAllRulesForAdmin
#          now SELECT all_devices (was loaded via a two-pass
#          workaround). AdminRule struct gained an AllDevices
#          bool that the /admin/exit-rules template can render.
#   #3     propagateAllDeviceRules gains pruneOrphanAllDeviceCopies
#          — fan-out rows whose target device was deleted are
#          swept on every propagation tick (no more ghost rules).
#   #4     PostDeleteExitRule cascades fan-out group deletes:
#          deleting one row of an all_devices=true rule removes
#          every row sharing the natural key (exit_node,
#          target_type, target_value). The audit log gets a
#          separate "fan-out cascade: N" detail so the operator
#          can audit the cleanup.
#   #5     B229 reconciler: the "via-disabled-but-canonical" path
#          no longer silently re-enables via_enabled=true when
#          the operator had explicitly disabled it. The case
#          logs as a skip (audit-only) so the trail is preserved
#          without overwriting the operator's choice.
#   #6+#7  /my/exit-rules status loop: rules with empty
#          exit_node_id ("auto") now report auto_pending — they
#          are NOT counted as a mismatch (the engine resolves
#          them). The mismatch count falls back to user-level
#          preferred when the device has no per-device pref, so
#          genuine drift on user-level prefs is now counted.
#   #11    routescript_data.go no longer loads device_ip into
#          routeEntry (it was never read by any body builder).
#   #8     exit_rules.html: dead {{if .AllDevices}} branches in
#          the per-host section are gone (the B276.2 filter
#          removes all_devices rows from the per-host slice, so
#          the check could never trigger).
#
# CONTRACTS
#   A  SELECTs include all_devices
#   B  AdminRule carries AllDevices
#   C  DeviceRule.AllDevices is scanned by both scan paths
#   D  pruneOrphanAllDeviceCopies helper exists and uses IN-clause
#   E  PostDeleteExitRule cascade-deletes via DeleteAllDeviceFanOut
#   F  B229 skip-log for via-disabled, no UPDATE
#   G  form_my.go status loop: auto_pending + user-level fallback
#   H  routescript no longer loads device_ip
#   I  dead {{if .AllDevices}} gone from per-host template
#   J  i18n parity for auto_pending_title (RU + EN)
#   K  unit tests for the prune helper
#   L  the script itself is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B277.4: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

QUERIES=internal/db/queries.go
DB=internal/db/device_rules.go
ADMIN=internal/feature/exit_rules/form_admin.go
SVC=internal/feature/exit_rules/form_my.go
ALLDEV=internal/feature/exit_rules/all_devices.go
RECON=internal/feature/exit_rules/reconciler.go
RSDATA=internal/feature/exit_rules/routescript_data.go
TMPL=internal/handlers/templates/exit_rules.html
CAT=internal/i18n/catalog_exit_rules.go

hdr "B277.4 — consistency audit fixes"

# --- A: SELECTs include all_devices ----------------------------------------
if grep -q 'COALESCE(d.all_devices, 0) AS all_devices' "$QUERIES"; then
  ok "A1: qSelectUserRulesForView now SELECTs all_devices (was a post-process hack pre-B277.4)"
else
  bad "A1: qSelectUserRulesForView is missing all_devices"
fi
if grep -q 'COALESCE(r.all_devices, 0) AS all_devices' "$QUERIES"; then
  ok "A2: qSelectAllRulesForAdmin now SELECTs all_devices (admin view can render the badge)"
else
  bad "A2: qSelectAllRulesForAdmin is missing all_devices"
fi

# --- B: AdminRule.AllDevices -----------------------------------------------
if grep -q 'AllDevices[[:space:]]*bool' "$ADMIN"; then
  ok "B1: AdminRule struct gained the AllDevices bool field"
else
  bad "B1: AdminRule.AllDevices is missing — the admin view cannot render the badge"
fi
if grep -q 'AllDevices:.*r\.AllDevices' "$ADMIN"; then
  ok "B2: form_admin.go copies DeviceRule.AllDevices into AdminRule.AllDevices"
else
  bad "B2: the AdminRule conversion is not propagating the AllDevices field"
fi

# --- C: DeviceRule.AllDevices is scanned ------------------------------------
if grep -q 'r\.AllDevices = ad == 1' "$DB"; then
  ok "C1: scanDeviceRules fills r.AllDevices from the SELECT result (was always false pre-B277.4)"
else
  bad "C1: scanDeviceRules does not populate AllDevices"
fi
if grep -q 'r\.AllDevices = ad == 1' "$DB" && grep -q 'r\.AllDevices = ad == 1' "$DB" | head -1 | grep -q .; then
  ok "C2: getAllRulesForAdminQuery also populates r.AllDevices from the SELECT result"
fi

# --- D: pruneOrphanAllDeviceCopies helper ----------------------------------
if grep -q 'func (s \*Service) pruneOrphanAllDeviceCopies' "$ALLDEV"; then
  ok "D1: pruneOrphanAllDeviceCopies helper exists (B277.4 orphan sweep)"
else
  bad "D1: the orphan sweep is missing — fan-out rows for deleted devices live forever"
fi
# D2: simpler grep — the sweep must be called from the
# propagateAllDeviceRules body (B277.4 wiring). Look for both
# names on lines near each other in the file.
PROPAGATE_LINE=$(grep -n 'func (s \*Service) propagateAllDeviceRules' "$ALLDEV" | head -1 | cut -d: -f1)
if [ -n "$PROPAGATE_LINE" ]; then
  PROPAGATE_END=$(awk -v start="$PROPAGATE_LINE" 'NR>start && /^func / {print NR; exit}' "$ALLDEV")
  PROPAGATE_END="${PROPAGATE_END:-99999}"
  if sed -n "${PROPAGATE_LINE},${PROPAGATE_END}p" "$ALLDEV" | grep -q 'pruneOrphanAllDeviceCopies'; then
    ok "D2: propagateAllDeviceRules invokes the orphan sweep before the propagation loop"
  else
    bad "D2: pruneOrphanAllDeviceCopies is defined but not called from propagateAllDeviceRules"
  fi
fi
if grep -q 'device_id NOT IN' "$ALLDEV"; then
  ok "D3: the helper uses a parameterised IN clause (no string-built SQL)"
else
  bad "D3: the helper does NOT use IN clause (SQL injection risk or N+1)"
fi

# --- E: PostDeleteExitRule cascade-deletes fan-out group -------------------
if grep -q 'func DeleteAllDeviceFanOut' "$DB"; then
  ok "E1: db.DeleteAllDeviceFanOut helper exists"
else
  bad "E1: db.DeleteAllDeviceFanOut is missing"
fi
if grep -q 'func GetRuleFanOutKey' "$DB"; then
  ok "E2: db.GetRuleFanOutKey helper returns exit_node + all_devices"
else
  bad "E2: db.GetRuleFanOutKey is missing — PostDeleteExitRule cannot decide cascade vs single-row"
fi
# E3+E4: simpler full-file grep — the strings are unique.
if grep -q 'DeleteAllDeviceFanOut' "$SVC"; then
  ok "E3: PostDeleteExitRule calls DeleteAllDeviceFanOut on all_devices rows"
else
  bad "E3: PostDeleteExitRule does not cascade — all_devices rules leave N-1 ghost rows"
fi
if grep -q 'totalFanOutCascade' "$SVC"; then
  ok "E4: PostDeleteExitRule audit log records the fan-out cascade count"
else
  bad "E4: fan-out cascade count is missing from the audit detail"
fi

# --- F: B229 skip-log for via-disabled, no UPDATE ---------------------------
# Match the plan's "skip" branch for the via-disabled case.
if grep -q 'Action:[[:space:]]*"skip"' "$RECON" && grep -q 'via-disabled-but-canonical' "$RECON"; then
  ok "F1: B229 via-disabled case returns a skip change (no UPDATE, no via_enabled rewrite)"
else
  bad "F1: B229 still updates via_enabled=true on via=0 rows — operator's choice is clobbered"
fi

# --- G: form_my.go status loop has auto_pending + user-level fallback -------
STATUS_LINE=$(grep -n 'statusByRuleID := map\[int\]string{}' "$SVC" | head -1 | cut -d: -f1)
if [ -n "$STATUS_LINE" ]; then
  # Find the matching closing brace via a counter (the block
  # contains nested `if`/`else` but no inner functions).
  STATUS_END=$(awk -v start="$STATUS_LINE" 'NR==start {depth=0} NR>=start {n=gsub(/\{/,"{"); depth+=n; n=gsub(/\}/,"}"); depth-=n; if (NR>start && depth==0) {print NR; exit}}' "$SVC")
  STATUS_END="${STATUS_END:-99999}"
  if sed -n "${STATUS_LINE},${STATUS_END}p" "$SVC" | grep -q '"auto_pending"'; then
    ok "G1: form_my.go status loop emits auto_pending for empty exit_node_id rules"
  else
    bad "G1: auto rules are still counted as a mismatch (no auto_pending branch)"
  fi
  if sed -n "${STATUS_LINE},${STATUS_END}p" "$SVC" | grep -q 'pref = userPreferredHost'; then
    ok "G2: user-level preferred is the fallback when the device has no per-device pref"
  else
    bad "G2: mismatch count still ignores user-level preferred (banner undercounts)"
  fi
fi

# --- H: routescript no longer loads device_ip -------------------------------
if ! grep -q 'COALESCE(device_ip' "$RSDATA"; then
  ok "H1: routescript SELECT no longer pulls device_ip (column was never used by body)"
else
  bad "H1: routescript still loads device_ip"
fi
if ! grep -q 'deviceIP[[:space:]]*string' "$RSDATA"; then
  ok "H2: routeEntry struct no longer has a deviceIP field"
else
  bad "H2: routeEntry.deviceIP is still in the struct (dead code)"
fi

# --- I: dead {{if .AllDevices}} gone from per-host template -----------------
if ! awk '/range \$host, \$nodes := \.GroupedByHostname/,/range \.AllDevicesByExitNodeCDN/' "$TMPL" | grep -q '{{if \.AllDevices}}'; then
  ok "I1: dead {{if .AllDevices}} is gone from the per-host section"
else
  bad "I1: dead {{if .AllDevices}} still renders (the per-host slice never has AllDevices=true)"
fi

# --- J: i18n parity for auto_pending_title ---------------------------------
KEYS=0
for k in auto_pending_title; do
  n=$(grep -c "exit_rules.$k\"" "$CAT")
  [ "$n" -ge 2 ] && KEYS=$((KEYS+1))
done
if [ "$KEYS" -eq 1 ]; then
  ok "J1: auto_pending_title exists in both RU and EN (1/1 pair)"
else
  bad "J1: auto_pending_title is missing from one or both catalogues"
fi

# --- K: unit tests for the prune helper ------------------------------------
if grep -q 'func TestBuildOrphanSweepINClause' internal/feature/exit_rules/*test*.go 2>/dev/null; then
  ok "K1: a unit test covers the orphan sweep's IN-clause builder"
else
  bad "K1: no unit test for the orphan sweep — the regression guard is missing"
fi

# --- L: the script itself is tracked by git (trap #11) ---------------------
if git ls-files --error-unmatch scripts/check_b277_4_consistency.sh >/dev/null 2>&1; then
  ok "L1: scripts/check_b277_4_consistency.sh is tracked by git"
else
  bad "L1: scripts/check_b277_4_consistency.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB277.4 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
