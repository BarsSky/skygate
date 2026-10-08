#!/usr/bin/env bash
# check_b347_device_status_strip.sh — B347: the device → relay assignment must
# not be paginated away.
#
# WHY THIS FILE EXISTS (measured on the live host, 2026-10-04)
# -----------------------------------------------------------
# Operator report, twice: «в интерфейсе всплывает уведомление что правило уже есть
# но нигде не маркируется что у cyborg доступ теперь появился» and, days later,
# «сколько ни пробовал добавить доступ к youtube на cyborg не даёт».
#
# B343 made the state visible on the page — a red «нет exit-node» badge for a
# device with enabled rules and no preference, a green «выход через: <relay>» for a
# pinned one — but those badges live INSIDE the per-device rule groups, and the
# groups are built from the CURRENT PAGE of rule rows. Read on the live account:
#
#     Текущие правила (50)          ← page 1 of 5, 228 rules total
#     skyworker  выход через: karolina   ← the ONLY group on page 1
#     cyborg (1/500)                ← offered in the picker, no group, no badge
#
# So adding a rule for cyborg changed nothing the operator could see: the answer
# existed, on page 2.
#
# B347 makes the answer page-independent: `DeviceStatusRows` reads EVERY device
# with enabled rules and its relay in ONE query (no LIMIT, no window), the page
# renders that strip above the groups, and the post-save flash names the relay
# the device ended up on.
#
# What this script verifies:
#   A. the read is one unpaginated query with B343's exact "no preference"
#      definition, portable across SQLite and PostgreSQL
#   B. the page wires it and renders the strip BEFORE the paginated groups
#   C. the post-save flash names the relay (the half that answers "правило есть")
#   D. the keys are in BOTH catalogues and the behaviour is unit-tested
#   E. tracked, registered, indexed, and the touched files are gofmt-clean
#
# Usage:  bash scripts/check_b347_device_status_strip.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

B347=internal/feature/exit_rules/device_status_b347.go
B347_TEST=internal/feature/exit_rules/device_status_b347_test.go
FORM=internal/feature/exit_rules/form_my_rules.go
TPL=internal/handlers/templates/exit_rules.html
CATALOG=internal/i18n/catalog_exit_rules.go
ALLOW=scripts/gofmt_legacy_allowlist.txt
CAT=scripts/verify_pre_deploy.sh

for f in "$B347" "$B347_TEST" "$FORM" "$TPL" "$CATALOG" "$ALLOW" "$CAT"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B347 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# The SQL this file guards, extracted so the file's own prose ("no LIMIT, no page
# window") cannot satisfy or break a contract about the QUERY.
SQL="$(awk '/d\.Query\(`/{f=1} f{print} /`, *userID\)/{f=0}' "$B347")"

hdr "A. one unpaginated read with B343's definition"

if grep -q '^func DeviceStatusRows(' "$B347"; then
  ok "A1: DeviceStatusRows is defined (one query for every device with rules)"
else
  bad "A1: DeviceStatusRows is missing"
fi

if [ -n "$SQL" ]; then
  ok "A2: the query was found and extracted for the contracts below"
else
  bad "A2: could not extract the SQL from $B347"
fi

# THE contract of this block: a LIMIT would reintroduce exactly the pagination
# that hid cyborg.
if printf '%s' "$SQL" | grep -qi 'limit'; then
  bad "A3: the strip query contains LIMIT — the answer would depend on the page again (that is the B347 defect)"
else
  ok "A3: the strip query has NO LIMIT — it cannot be paginated away"
fi

if printf '%s' "$SQL" | grep -q 'enabled = 1'; then
  ok "A4: only ENABLED rules are counted (a disabled-only device must not appear)"
else
  bad "A4: the query does not filter on enabled = 1"
fi
if printf '%s' "$SQL" | grep -q "device_hostname <> ''"; then
  ok "A5: empty hostnames are skipped (pre-rename leftovers, matched through node_owner_map)"
else
  bad "A5: the query does not skip empty device_hostname rows"
fi
if printf '%s' "$SQL" | grep -q 'LEFT JOIN device_exit_node_prefs'; then
  ok "A6: LEFT JOIN device_exit_node_prefs — the same 'no preference row' definition B343 uses"
else
  bad "A6: the query does not LEFT JOIN the preference table"
fi
if printf '%s' "$SQL" | grep -q 'p.user_id = r.user_id' && printf '%s' "$SQL" | grep -q 'p.device_hostname = r.device_hostname'; then
  ok "A7: the join is scoped to the same (user, device) pair — another user's preference cannot leak in"
else
  bad "A7: the join is not scoped by user_id AND device_hostname"
fi
# The B319 lesson: device_rules.device_id is INTEGER while node_owner_map.node_id
# is TEXT, so a join between them answers differently per backend. The strip must
# not introduce one.
if printf '%s' "$SQL" | grep -q 'node_owner_map'; then
  bad "A8: the strip joins node_owner_map — that is the INTEGER-vs-TEXT class (B319); it must stay on TEXT columns"
else
  ok "A8: no node_owner_map join (no INTEGER/TEXT comparison across backends)"
fi
if printf '%s' "$SQL" | grep -q '\$1'; then
  ok "A9: the universal \$N placeholder — one statement for SQLite and PostgreSQL"
else
  bad "A9: the query does not use the \$N placeholder form"
fi
if grep -q 'func (s \*Service) DeviceStatusRowsForService(' "$B347"; then
  ok "A10: the Service wrapper exists (the page needs no raw handle)"
else
  bad "A10: DeviceStatusRowsForService is missing"
fi
if grep -q 'func AssignedRelayFor(' "$B347" && grep -q 'TrimPrefix(row.Relay, tagPrefix)\|TrimPrefix(tag, tagPrefix)' "$B347"; then
  ok "A11: the tag: prefix is stripped — the page prints the name --exit-node= accepts"
else
  bad "A11: AssignedRelayFor is missing or the tag: prefix is not stripped"
fi

hdr "B. the page renders it, above the paginated groups"

if grep -q 's.DeviceStatusRowsForService(c.UserID)' "$FORM"; then
  ok "B1: the page handler reads the strip for the current user"
else
  bad "B1: form_my does not call DeviceStatusRowsForService"
fi
if grep -q '"device_status":' "$FORM"; then
  ok "B2: the strip is passed to the template as device_status"
else
  bad "B2: device_status is not in the render data"
fi
if grep -q 'id="my-device-status"' "$TPL"; then
  ok "B3: the strip card is rendered"
else
  bad "B3: the strip card (id=my-device-status) is missing"
fi
if grep -q 'id="device-status-active-{{.Hostname}}"' "$TPL" && grep -q 'id="device-status-inert-{{.Hostname}}"' "$TPL"; then
  ok "B4: every row carries a stable per-device hook (active/inert) for both a human and a test"
else
  bad "B4: the per-device hooks device-status-active-/inert- are missing"
fi

# The ORDER is the whole fix: the strip must come BEFORE the range that builds the
# per-page groups, otherwise it inherits the same page window.
STRIP_LINE="$(grep -n 'id="my-device-status"' "$TPL" | head -1 | cut -d: -f1 || true)"
GROUPS_LINE="$(grep -n '{{range \$host, \$nodes := .GroupedByHostname}}' "$TPL" | head -1 | cut -d: -f1 || true)"
if [ -n "$STRIP_LINE" ] && [ -n "$GROUPS_LINE" ] && [ "$STRIP_LINE" -lt "$GROUPS_LINE" ]; then
  ok "B5: the strip (line $STRIP_LINE) is rendered BEFORE the per-page groups (line $GROUPS_LINE)"
else
  bad "B5: the strip is not before the GroupedByHostname range (strip=$STRIP_LINE groups=$GROUPS_LINE)"
fi
if grep -q '{{range .device_status}}' "$TPL"; then
  ok "B6: the strip iterates .device_status (the unpaginated slice), not the grouped page data"
else
  bad "B6: the strip does not iterate .device_status"
fi

hdr "C. the post-save flash names the relay"

if grep -q 'assignedRelay' "$FORM" && grep -q 'form_device_id' "$FORM"; then
  ok "C1: the handler resolves the saved device's relay from the form's device id"
else
  bad "C1: the handler does not compute the assigned relay for the flash"
fi
if grep -q '"AssignedRelay":' "$FORM"; then
  ok "C2: AssignedRelay is passed to the template"
else
  bad "C2: AssignedRelay is not in the render data"
fi
if grep -q 'id="my-assigned-relay"' "$TPL" && grep -q 'exit_rules.assigned_now' "$TPL"; then
  ok "C3: the flash is rendered — «правило сохранено, устройство выходит через: <relay>»"
else
  bad "C3: the assigned-relay flash is missing from the template"
fi

hdr "D. catalogues and behaviour"

# Every exit_rules.* key the NEW template block uses must exist in RU and EN.
# Scoped to the strip/flash blocks so an unrelated missing key is B325's problem,
# not a silent pass here.
BLOCK="$(sed -n '/B347 (2026-10-04, operator report)/,/^      {{range \$host, \$nodes/p' "$TPL")"
MISSING=""
for key in $(printf '%s' "$BLOCK" | grep -o 't "exit_rules\.[a-z_]*"' | sed 's/t "//;s/"//' | sort -u); do
  N="$(grep -c "\"$key\"" "$CATALOG" || true)"
  if [ "$N" -lt 2 ]; then MISSING="$MISSING $key($N)"; fi
done
if [ -z "$MISSING" ]; then
  ok "D1: every exit_rules.* key in the new block exists in RU and EN"
else
  bad "D1: keys not defined in both catalogues:$MISSING"
fi

if grep -q 'func TestDeviceStatusRows_B347(' "$B347_TEST" && grep -q 'func TestAssignedRelayFor_B347(' "$B347_TEST"; then
  ok "D2: the read and the flash source are unit-tested"
else
  bad "D2: the B347 tests are missing"
fi

GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "D3: go is not on PATH — run the B347 tests on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B347' -count=1 2>&1)"; then
    ok "D3: the B347 cases pass ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "D3: the B347 cases fail: $(printf '%s' "$OUT" | tail -3)"
  fi
fi

hdr "E. tracked, registered, indexed, gofmt-clean"

if git ls-files --error-unmatch scripts/check_b347_device_status_strip.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (AGENTS trap #11)"
else
  bad "E1: NOT tracked by git — a .gitignore rule can eat a new script"
fi
if grep -q 'check_b347_device_status_strip.sh' "$CAT"; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered — it would never run"
fi
if grep -q '\*\*B347\*\*' AGENTS.md; then
  ok "E3: recorded in the AGENTS.md block index"
else
  bad "E3: no B347 line in the AGENTS.md block index (AGENTS rule 2)"
fi
# B347 touched form_my.go and catalog_exit_rules.go; neither is allow-listed, so
# rule 3 requires both to be gofmt-clean — and the ratchet must not have grown.
if grep -qx 'internal/feature/exit_rules/form_my.go' "$ALLOW" || grep -qx 'internal/i18n/catalog_exit_rules.go' "$ALLOW"; then
  bad "E4: a file this change formatted is still allow-listed (B337 D1)"
else
  ok "E4: the touched files are not frozen in the gofmt allow-list"
fi
if [ -z "$(command -v gofmt || true)" ]; then
  skip "E5: gofmt not on PATH — the VM run covers rule 3"
else
  DIRTY="$(gofmt -l "$B347" "$B347_TEST" "$FORM" "$CATALOG" 2>/dev/null || true)"
  if [ -z "$DIRTY" ]; then
    ok "E5: every file this block touches is gofmt-clean"
  else
    bad "E5: gofmt is not clean for: $(printf '%s' "$DIRTY" | tr '\n' ' ')"
  fi
fi

hdr "B347 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
