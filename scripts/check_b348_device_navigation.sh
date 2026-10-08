#!/usr/bin/env bash
# check_b348_device_navigation.sh — B348: a device must never be invisible
# because of pagination.
#
# WHY THIS FILE EXISTS (measured on the live host, 2026-10-04)
# -----------------------------------------------------------
# Operator report, right after adding a rule for cyborg: «правила пропали у
# cyborg у пользователя добавились но админ не видит также пропали правила из
# админ страницы exit rules и по остальным устройствам что не skyworker».
#
# NOTHING was lost: `device_rules` held 12 rows for cyborg, 215 for skyworker and
# 89 for basic, all enabled. What the pages showed was the pagination WINDOW:
#
#	/my/exit-rules      page 1 of 5, "Текущие правила (50)" → ONLY skyworker's rows
#	/admin/exit-rules   page 1 of 7 → ONLY skyworker (50 правил)
#
# Both pages slice ROWS and then build their device groups from that slice, so the
# device owning the most rows owns the page — and page 1 was all skyworker because
# the auto-updater's CDN re-resolve pass had just added ~54 new /32 rows for it. A
# device with no rows in the window is indistinguishable from a device with no
# rules, which is exactly what the operator saw.
#
# B348 adds navigation by DEVICE: one unpaginated inventory query (every device
# with enabled rules + its count) rendered as an index on both pages, every entry
# linking to a per-device drill-down that shows that device's rules UNPAGED; on
# /my/exit-rules the post-save landing derives the same filter from
# `form_device_id`, so "I just added a rule" always lands on that device's rules.
#
# What this script verifies:
#   A. one unpaginated inventory, with the same "enabled + real hostname" rule as
#      B343/B347, and a drill-down filter that also matches by device id
#   B. /my/exit-rules wires it, applies the filter BEFORE the grouping, and
#      survives a save by landing on the saved device
#   C. /admin/exit-rules renders the cross-user index into its existing drill-down
#   D. the keys are in BOTH catalogues and the behaviour is unit-tested
#   E. tracked, registered, indexed, gofmt-clean
#
# Usage:  bash scripts/check_b348_device_navigation.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LIB=internal/feature/exit_rules/device_index_b348.go
LIB_TEST=internal/feature/exit_rules/device_index_b348_test.go
FORM_MY=internal/feature/exit_rules/form_my_rules.go
FORM_ADMIN=internal/feature/exit_rules/form_admin.go
TPL_MY=internal/handlers/templates/exit_rules.html
TPL_ADMIN=internal/handlers/templates/admin/exit_rules.html
CATALOG=internal/i18n/catalog_exit_rules.go
CAT=scripts/verify_pre_deploy.sh

for f in "$LIB" "$LIB_TEST" "$FORM_MY" "$FORM_ADMIN" "$TPL_MY" "$TPL_ADMIN" "$CATALOG" "$CAT"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B348 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

count() { grep -c -- "$1" "$2" 2>/dev/null || true; }

hdr "A. the inventory and the drill-down"

for fn in DeviceRuleCountsForUser DeviceRuleCountsForAdmin DeviceRuleCountsForUserService DeviceRuleCountsForAdminService filterRulesByDevice equalFoldASCII; do
  if grep -q "^func $fn(" "$LIB" || grep -q "func (s \*Service) $fn(" "$LIB"; then
    ok "A1: $fn is defined in $LIB"
  else
    bad "A1: $fn is missing"
  fi
done

# The inventory MUST NOT be paginated: that is the bug.
if grep -qi 'limit' "$LIB"; then
  bad "A2: the inventory file mentions LIMIT — a windowed inventory would hide the device with the fewest rows again"
else
  ok "A2: the inventory is unpaginated (no LIMIT anywhere in the block)"
fi
if [ "$(count 'enabled = 1' "$LIB")" -ge 2 ] && [ "$(count "device_hostname <> ''" "$LIB")" -ge 2 ]; then
  ok "A3: both inventories use the same 'enabled rules, real hostname' rule as B343/B347"
else
  bad "A3: the inventories do not both filter enabled=1 and device_hostname <> ''"
fi
if grep -q 'r.user_id = \$1' "$LIB"; then
  ok "A4: the per-user inventory is scoped by \$1 (one statement for SQLite and PostgreSQL)"
else
  bad "A4: the per-user inventory has no \$1 scope"
fi

# The offline-device case: the page groups by DeviceName (a headscale lookup), the
# link carries the denormalised hostname, and an unresolved node has NO name — so
# the filter must accept the resolved device id as well.
if grep -q 'deviceIDs\[r.DeviceID\]' "$LIB"; then
  ok "A5: the filter matches the resolved device id, so a device whose name did not resolve is still reachable"
else
  bad "A5: the filter only matches the displayed name — an offline/unresolved device would open an empty page"
fi
if grep -q 'equalFoldASCII(r.DeviceName, device)' "$LIB"; then
  ok "A6: the filter matches the displayed name case-insensitively"
else
  bad "A6: the filter does not compare the displayed device name"
fi

hdr "B. /my/exit-rules: filter, order, and the post-save landing"

if grep -q 'r.URL.Query().Get("device")' "$FORM_MY"; then
  ok "B1: the page reads an explicit ?device="
else
  bad "B1: the my-page has no ?device= filter"
fi
if grep -q 'form_device_id' "$FORM_MY" && grep -q 'deviceFilter = di.Hostname' "$FORM_MY"; then
  ok "B2: the post-save landing derives the filter from form_device_id (a save lands ON that device)"
else
  bad "B2: the post-save landing does not focus the saved device"
fi

# ORDER: grouping is built from `rules`, so a filter applied after it would leave
# the rendered groups untouched — the half-applied change this block exists to
# avoid.
FILTER_LINE="$(grep -n 'deviceFilter = di.Hostname\|if deviceFilter != ""' "$FORM_MY" | head -1 | cut -d: -f1 || true)"
GROUP_LINE="$(grep -n 'groupedByHostname := ' "$FORM_MY" | head -1 | cut -d: -f1 || true)"
if [ -n "$FILTER_LINE" ] && [ -n "$GROUP_LINE" ] && [ "$FILTER_LINE" -lt "$GROUP_LINE" ]; then
  ok "B3: the device filter (line $FILTER_LINE) is resolved BEFORE the grouping (line $GROUP_LINE)"
else
  bad "B3: the filter is not applied before the group maps are built (filter=$FILTER_LINE groups=$GROUP_LINE)"
fi
if grep -q '"DeviceFilter":' "$FORM_MY" && grep -q '"DeviceIndex":' "$FORM_MY"; then
  ok "B4: DeviceFilter and DeviceIndex reach the template"
else
  bad "B4: the my-page does not pass the filter/index state"
fi
if grep -q 'id="my-device-filter-banner"' "$TPL_MY" && grep -q 'id="my-device-index"' "$TPL_MY"; then
  ok "B5: the my-page renders both the drill-down banner and the device index"
else
  bad "B5: the my-page is missing the banner or the index"
fi
if grep -q 'exit_rules.device_status_open' "$TPL_MY"; then
  ok "B6: every strip row links to that device's rules («показать правила»)"
else
  bad "B6: the strip rows do not link to the per-device view"
fi

hdr "C. /admin/exit-rules: the cross-user index"

if grep -q 'DeviceRuleCountsForAdminService()' "$FORM_ADMIN"; then
  ok "C1: the admin page builds the cross-user inventory"
else
  bad "C1: the admin page has no inventory — page 1 stays one device"
fi
if grep -q '"DeviceIndex": adminDeviceIndex' "$FORM_ADMIN"; then
  ok "C2: DeviceIndex reaches the admin template"
else
  bad "C2: the admin render map does not carry DeviceIndex"
fi
if grep -q 'id="admin-device-index"' "$TPL_ADMIN" && grep -q 'href="/admin/exit-rules?device=' "$TPL_ADMIN"; then
  ok "C3: the admin index links every device into the existing unpaged drill-down"
else
  bad "C3: the admin index is missing or does not link to the drill-down"
fi

hdr "D. catalogues and behaviour"

MISSING=""
for key in exit_rules.device_status_open exit_rules_admin.device_index_title; do
  N="$(count "\"$key\"" "$CATALOG")"
  if [ "$N" -lt 2 ]; then MISSING="$MISSING $key($N)"; fi
done
if [ -z "$MISSING" ]; then
  ok "D1: the new keys exist in RU and EN"
else
  bad "D1: keys not defined in both catalogues:$MISSING"
fi
for tn in TestDeviceRuleCounts_B348 TestFilterRulesByDevice_B348 TestEqualFoldASCII_B348; do
  if grep -q "func $tn(" "$LIB_TEST"; then
    ok "D2: $tn exists"
  else
    bad "D2: $tn is missing"
  fi
done
GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "D3: go is not on PATH — run the B348 tests on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B348' -count=1 2>&1)"; then
    ok "D3: the B348 cases pass ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "D3: the B348 cases fail: $(printf '%s' "$OUT" | tail -3)"
  fi
fi

hdr "E. tracked, registered, indexed, gofmt-clean"

if git ls-files --error-unmatch scripts/check_b348_device_navigation.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (AGENTS trap #11)"
else
  bad "E1: NOT tracked by git"
fi
if grep -q 'check_b348_device_navigation.sh' "$CAT"; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered — it would never run"
fi
if grep -q '\*\*B348\*\*' AGENTS.md; then
  ok "E3: recorded in the AGENTS.md block index"
else
  bad "E3: no B348 line in the AGENTS.md block index"
fi
if [ -z "$(command -v gofmt || true)" ]; then
  skip "E4: gofmt not on PATH — the VM run covers rule 3"
else
  DIRTY="$(gofmt -l "$LIB" "$LIB_TEST" "$FORM_MY" "$FORM_ADMIN" "$CATALOG" 2>/dev/null || true)"
  if [ -z "$DIRTY" ]; then
    ok "E4: every file this block touches is gofmt-clean"
  else
    bad "E4: gofmt is not clean for: $(printf '%s' "$DIRTY" | tr '\n' ' ')"
  fi
fi

hdr "B348 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
