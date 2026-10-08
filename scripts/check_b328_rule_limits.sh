#!/usr/bin/env bash
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"   # B339: the contracts below read the cmd/skygate SURFACE, not one file
# check_b328_rule_limits.sh
#
# 2026-09-25 (B328, v1.5.94) — "could not add a rule to another device of the user",
# and the rule caps that were counted against the wrong thing.
#
# OPERATOR REPORT (measured on the live host, not inferred):
#
#	/my/exit-rules showed «cyborg (1/500)» in the device picker and refused the save
#	with «device limit exceeded: 500/500 user-facing rules on this device».
#
# Both numbers were real and they came from DIFFERENT queries:
#
#   * the page   — per (user, device), user-facing (non-subnet, non-derived) = 1;
#   * the guard  — `countUserFacing(0, devID, false)`, whose switch had no "device
#                  without user" case, so it fell through to `db.CountEnabledRules` =
#                  `SELECT COUNT(*) FROM device_rules WHERE enabled = 1` = 500.
#
# Against SKYGATE_MAX_RULES_PER_DEVICE=500 that refused EVERY insert for EVERY user.
# The correct helper existed (and its doc comment claimed this check used it) and the
# admin path already called the right one — only the /my path had degraded.
#
# WHAT THIS CONTRACT GUARDS
#   A. the per-device level can never read the system-wide count again (source + tests)
#   B. the operator can spread an EXISTING rule to their other devices, and the action
#      cannot invent a rule or duplicate rows
#   C. the admin page can do the same for another user's devices
#   D. the caps themselves are adjustable from the panel (db > env > default) instead of
#      requiring an `.env` edit plus a container recreate
#   E. the three surfaces (my form, admin form, API) use ONE unit of measure
#   F. the B328 tests actually RUN (a filter matching nothing exits 0 — the B322 class)
#   G. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B328: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LIMITS=internal/feature/exit_rules/rule_limits_b328.go
SETTINGS=internal/feature/exit_rules/limits_settings_b328.go
SPREAD=internal/feature/exit_rules/spread_b328.go
MY=internal/feature/exit_rules/form_my.go
MY_RULES=internal/feature/exit_rules/form_my_rules.go
ADMIN=internal/feature/exit_rules/form_admin.go
API=internal/feature/exit_rules/api.go
TPLMY=internal/handlers/templates/exit_rules.html
TPLADMIN=internal/handlers/templates/admin/exit_rules.html

hdr "B328 — one cap, one unit, and a level that counts what it names"

# --- A: the per-device level cannot read the system-wide count ------------------------
# A1 pins the exact pre-fix expression. It is the whole bug in one line, so its return is
# a regression regardless of how the surrounding code is refactored. Comment lines are
# excluded: the fix documents the old call by name (the B327 A9 lesson — a source
# contract must not match its own explanation).
if grep -vE '^[[:space:]]*//' "$MY" | grep -qF 'countUserFacing(0,'; then
  bad "A1: form_my.go calls countUserFacing(0, …) again — userID 0 with a device is what fell through to the SYSTEM-WIDE count (live: 500/500 for a device with 1 rule)"
else
  ok "A1: the pre-fix device query (countUserFacing(0, devID, false)) is gone"
fi

for sym in 'ruleLimits' 'ruleLimitCounts' 'func (l ruleLimits) ExceedReason' 'func measureRuleLimits' \
           'func countUserFacingForUserDevice' 'func countUserFacingForUser' 'func countAllEnabledRules'; do
  if grep -qF "$sym" "$LIMITS"; then
    ok "A2: rule_limits_b328.go defines ${sym#func }"
  else
    bad "A2: rule_limits_b328.go no longer defines '$sym' — the limit ladder is not the pure decision it was made into"
  fi
done

# A3: the structural half — the device count is only ever produced by a helper that
# requires BOTH identities, and the system count only by the helper that says so.
if grep -qE 'func countUserFacingForUserDevice\(d \*sql\.DB, userID int64, deviceID int\)' "$LIMITS"; then
  ok "A3a: countUserFacingForUserDevice takes (d, userID, deviceID) — no call can omit the user and silently get the system total"
else
  bad "A3a: countUserFacingForUserDevice lost its (userID, deviceID) signature"
fi
if grep -qF 'PerDevice = countUserFacingForUserDevice' "$LIMITS"; then
  ok "A3b: PerDevice is filled ONLY by the (user,device) query"
else
  bad "A3b: PerDevice is no longer filled by the per-(user,device) helper"
fi
if grep -qF 'Total = countAllEnabledRules' "$LIMITS"; then
  ok "A3c: Total is filled ONLY by the system-wide helper (which nothing else may use)"
else
  bad "A3c: the system-wide count is not the only producer of Total"
fi
if ! grep -vE '^[[:space:]]*//' "$LIMITS" | grep -q 'CountEnabledRulesForDevice'; then
  ok "A3d: the bare per-device helper (which counts derived/subnet rows) is not used for the user-facing cap"
else
  bad "A3d: CountEnabledRulesForDevice is in the limit path again — that counts EVERY enabled row, not the user-facing unit the page shows"
fi

# A4: no call site may read the raw config field any more. Three surfaces reading
# Cfg.MaxRulesPerDevice directly is exactly how the page and the guard can disagree.
DIRECT=$(grep -nE 'Cfg\.MaxRulesPerDevice|Cfg\.MaxTotalRules' "$MY" "$ADMIN" "$API" 2>/dev/null || true)
if [ -z "$DIRECT" ]; then
  ok "A4: no limit call site reads Cfg.MaxRulesPerDevice/Cfg.MaxTotalRules directly — all three go through the resolver"
else
  bad "A4: a limit call site reads the raw config field again (bypassing the db>env>default resolver):"
  printf '%s\n' "$DIRECT" | head -5 | sed 's/^/       /' >&2
fi

# A5/A6: the admin path and the API share the decision, and the API uses the same UNIT.
if grep -qF 'measureRuleLimits' "$ADMIN" && grep -qF 'effectiveRuleLimits' "$ADMIN"; then
  ok "A5: the admin path uses the shared ladder and the shared resolver"
else
  bad "A5: the admin path does not use effectiveRuleLimits/measureRuleLimits — it can drift from /my again"
fi
if grep -qF 'countUserFacingForUserDevice(s.dbc(), c.UserID, rl.DeviceID)' "$API"; then
  ok "A6: the API counts the caller's user-facing rules on the device (same unit as the page)"
else
  bad "A6: the API no longer uses the user-facing per-(user,device) count — pre-B328 it used db.CountEnabledRulesForDevice, which counts the 454 derived subnet ranges and would refuse at 500 where the page says 33/500"
fi

# The regression proof must be a test that names the operator's numbers.
if grep -qF 'device limit exceeded: 500/500 user-facing rules on this device' \
      internal/feature/exit_rules/rule_limits_b328_test.go; then
  ok "A7: the test suite pins the operator's exact message as the shape the fix must NOT reproduce"
else
  bad "A7: no test pins the operator's message — the regression proof is gone"
fi
if grep -qF 'TestB328_HandlerInsertsWhenTheSystemTotalReachesTheDeviceCap' \
      internal/feature/exit_rules/rule_limits_b328_handler_test.go; then
  ok "A8: the handler-level regression test exists (a helper-only test would keep passing if the CALL SITE regressed)"
else
  bad "A8: the handler-level regression test is missing"
fi

# --- B: spreading an EXISTING rule to the user's other devices ------------------------
gosurface SKY_MAIN cmd/skygate/*.go
if grep -qF '"POST /my/exit-rules/spread"' "$SKY_MAIN"; then
  ok "B1: POST /my/exit-rules/spread is routed"
else
  bad "B1: the spread route is not registered"
fi
if grep -qF 'db.MarkDeviceRulesAllDevices' "$SPREAD"; then
  ok "B2: the action records the all-devices INTENT (so the periodic pass covers a device registered later)"
else
  bad "B2: the spread action does not mark the intent — a device registered later would not inherit the rule"
fi
if grep -qF 's.propagateAllDeviceRules()' "$SPREAD"; then
  ok "B3: the action reuses propagateAllDeviceRules instead of a second fan-out implementation"
else
  bad "B3: the spread action re-implemented the fan-out — the button and the five-minute tick can now produce different rows"
fi
if grep -qF 'found = true' "$SPREAD" && grep -qF 'no rule %s %s via %s belongs to you' "$SPREAD"; then
  ok "B4: the action refuses to invent a rule (the natural key must already belong to the caller)"
else
  bad "B4: the spread action can build a rule that did not exist"
fi
SPREAD_BUTTONS=$(grep -c 'form="spread-{{.ID}}"' "$TPLMY" || true)
if [ "${SPREAD_BUTTONS:-0}" -ge 4 ]; then
  ok "B5: the button is rendered in all $SPREAD_BUTTONS rule-row variants (two sections × grouped/ungrouped)"
else
  bad "B5: the spread button appears ${SPREAD_BUTTONS:-0} time(s) in exit_rules.html, want >= 4"
fi
if grep -qF '{{if not .AllDevices}}' "$TPLMY"; then
  ok "B6: the button is hidden for a rule that already carries the all-devices marker"
else
  bad "B6: the button is offered on already-spread rules"
fi
for key in 'exit_rules.spread_button_title' 'exit_rules.spread_ok' 'exit_rules.spread_marked' 'exit_rules.spread_already'; do
  n=$(grep -c "\"$key\"" internal/i18n/catalog_exit_rules.go || true)
  if [ "${n:-0}" -ge 2 ]; then
    ok "B7: $key is defined in BOTH catalogues"
  else
    bad "B7: $key has $n definition(s) — RU+EN parity is broken"
  fi
done

# --- C: the same option on /admin/exit-rules -----------------------------------------
if grep -qF 'db.MarkDeviceRulesAllDevices' "$ADMIN"; then
  ok "C1: the admin path marks the all-devices intent too (pre-B328 it never called this at all)"
else
  bad "C1: /admin/exit-rules still has no all-devices path — mirroring a rule across another user's device set must be done one device at a time"
fi
if grep -qF 'name="all_devices"' "$TPLADMIN"; then
  ok "C2: the admin form offers the option"
else
  bad "C2: the admin template has no all_devices control"
fi
if grep -qF 'db.DeviceIDsForPortalUser' "$ADMIN"; then
  ok "C3: an empty device field resolves to the TARGET user's first device (so the ownership/exit-node checks still run against a real device)"
else
  bad "C3: the admin all-devices path does not resolve a real target device"
fi
n=$(grep -c '"exit_rules_admin.add_form_all_devices"' internal/i18n/catalog_exit_rules.go || true)
if [ "${n:-0}" -ge 2 ]; then
  ok "C4: exit_rules_admin.add_form_all_devices is defined in BOTH catalogues"
else
  bad "C4: exit_rules_admin.add_form_all_devices has $n definition(s)"
fi

# --- D: the caps are adjustable from the panel ----------------------------------------
if grep -qF 'SourceDB' "$SETTINGS" && grep -qF 'SourceEnv' "$SETTINGS" && grep -qF 'limitSourceDefault' "$SETTINGS"; then
  ok "D1: the caps resolve in three layers (db > env > default) with the source reported"
else
  bad "D1: the cap resolver lost its layering or its source reporting"
fi
if grep -qF 'func SaveRuleLimits' "$SETTINGS" && grep -qF 'db.DeleteGlobalSetting' "$SETTINGS"; then
  ok "D2: an empty value CLEARS the override (hands the level back to .env/default)"
else
  bad "D2: clearing an override does not delete the row — .env could never take over again"
fi
if grep -qF '"POST /admin/exit-rules/limits"' "$SKY_MAIN" && grep -qF 'func (s *Service) PostAdminExitRuleLimits' "$SETTINGS"; then
  ok "D3: POST /admin/exit-rules/limits is routed and handled"
else
  bad "D3: the limits endpoint is missing"
fi
if grep -qF 'c == nil || !c.IsAdmin' "$SETTINGS"; then
  ok "D4: the endpoint is admin-only"
else
  bad "D4: the limits endpoint is not gated on IsAdmin — a user could widen their own quota"
fi
if grep -qF 'dbLimitOverride' "$ADMIN" && grep -qF 'limit_per_device_override' "$ADMIN"; then
  ok "D5: the inputs show the STORED override, never the effective value (saving the form cannot silently promote an env value into a DB row)"
else
  bad "D5: the limits inputs are pre-filled with the effective value"
fi
if grep -qF 'limits_col_source' "$TPLADMIN" && grep -qF 'limit_per_device_source' "$TPLADMIN"; then
  ok "D6: the card shows which layer each effective cap came from"
else
  bad "D6: the card does not show the source of each cap"
fi
for key in 'exit_rules_admin.limits_title' 'exit_rules_admin.limits_help' 'exit_rules_admin.limits_saved' \
           'exit_rules_admin.limits_err' 'exit_rules_admin.limits_per_user_note'; do
  n=$(grep -c "\"$key\"" internal/i18n/catalog_exit_rules.go || true)
  if [ "${n:-0}" -ge 2 ]; then
    ok "D7: $key is defined in BOTH catalogues"
  else
    bad "D7: $key has $n definition(s)"
  fi
done
# D8: the DISPLAYED caps must come from the same call the guard uses.
if grep -qF 'displayLimits, _ := s.effectiveRuleLimits(c.Username)' "$MY_RULES" && \
   grep -qF 'adminLimits, limitSources := s.effectiveRuleLimits(c.Username)' "$ADMIN"; then
  ok "D8: both pages display the caps the guard enforces (one resolver call each)"
else
  bad "D8: a page still displays a cap it read separately — that is how 'cyborg (1/500)' ended up next to a refusal quoting 500/500"
fi

# --- F: the tests must RUN ------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  LIST="$(go test -list 'TestB328' ./internal/feature/exit_rules/ 2>&1)"
  MATCHED=$(printf '%s\n' "$LIST" | grep -cE '^TestB328')
  if [ "${MATCHED:-0}" -ge 9 ]; then
    ok "F1: the B328 tests are discoverable ($MATCHED matched by -list; a vacuous filter cannot pass)"
  else
    bad "F1: only ${MATCHED:-0} TestB328 functions are discoverable, want >= 9:"
    printf '%s\n' "$LIST" | tail -5 | sed 's/^/       /' >&2
  fi
  OUT="$(go test -count=1 -run 'TestB328' ./internal/feature/exit_rules/ 2>&1)"
  if grep -q 'no tests to run' <<< "$OUT"; then
    bad "F2: go test reports '[no tests to run]' for the B328 filter — the contract is vacuous"
  elif grep -q '^ok' <<< "$OUT"; then
    ok "F2: the B328 tests run and pass"
  else
    bad "F2: the B328 tests failed:"
    printf '%s\n' "$OUT" | tail -12 | sed 's/^/       /' >&2
  fi
else
  skip "F1/F2: go not on PATH — run them on the VM"
fi

# --- G: git --------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b328_rule_limits.sh >/dev/null 2>&1; then
  ok "G1: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "G1: this script is NOT tracked by git"
fi

printf '\n\033[1mB328 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
