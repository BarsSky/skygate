#!/usr/bin/env bash
# check_b343_inert_rules_visible.sh — B343: «правило есть, а доступа нет» must be
# visible ON THE PAGE, not only in the journal.
#
# WHY THIS FILE EXISTS (operator report, 2026-10-02)
# --------------------------------------------------
# Verbatim: «в интерфейсе всплывает уведомление что правило уже есть но нигде не
# маркируется что у cyborg доступ теперь появился и соответственно доступа тоже на
# него не распространяется». The notice the operator saw (`exit_rules.duplicate`,
# "домен уже покрыт правилом") answers "did I add a second copy?", which is a
# different question from "does this device actually route through a relay NOW?".
#
# Measured live: `cyborg` had 11 youtube rules, NO `device_exit_node_prefs` row
# and nothing on any page said so — because the generated ACL looks CORRECT (the
# per-CIDR `via=` pins come from prefix_owner since B275, not from the rule), so
# every check that reads the policy passes while the device has no exit node at
# all. That is the state this block makes visible: a device with ENABLED rules and
# no per-device preference is inert, and both rule pages now name it.
#
# B341/B341.1 made the reconciler DERIVE the relay from prefix_owner on its own
# tick, so most devices heal automatically; the banner is the half that stays true
# when the derivation cannot decide (no owner / split owner / owner without a
# per-node tag), and it points at the journal reason (`missing-pref-…`).
#
# What this script verifies:
#   A. the query exists and means exactly "enabled rules and no preference"
#   B. both pages pass the marker into their template data
#   C. both templates render a banner AND a per-device badge (my page)
#   D. the RU and EN texts name the state and the journal reason
#   E. the unit test runs (the marker is exact in both directions)
#   F. tracked, registered, indexed (AGENTS §2 trap #11)
#
# Usage:  bash scripts/check_b343_inert_rules_visible.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GO=internal/feature/exit_rules/inert_rules_b343.go
MYGO=internal/feature/exit_rules/form_my.go
ADMGO=internal/feature/exit_rules/form_admin.go
MYTPL=internal/handlers/templates/exit_rules.html
ADMTPL=internal/handlers/templates/admin/exit_rules.html
I18N=internal/i18n/catalog_exit_rules.go
TEST=internal/feature/exit_rules/inert_rules_b343_test.go
CAT=scripts/verify_pre_deploy.sh

for f in "$GO" "$MYGO" "$ADMGO" "$MYTPL" "$ADMTPL" "$I18N" "$TEST" "$CAT"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B343 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

hdr "A. the query means exactly 'enabled rules and no preference'"

if grep -q 'LEFT JOIN device_exit_node_prefs' "$GO" && grep -q 'p.device_hostname IS NULL' "$GO"; then
  ok "A1: the marker is a LEFT JOIN that is NULL — i.e. 'there is no preference row'"
else
  bad "A1: the marker does not test for a MISSING preference row"
fi
if grep -q 'r.enabled = 1' "$GO" && grep -q "r.device_hostname <> ''" "$GO"; then
  ok "A2: disabled rules and pre-rename rows (empty hostname) are excluded"
else
  bad "A2: the marker would report disabled rules or hostname-less pre-rename rows — the banner becomes noise"
fi
if grep -q 'func DevicesWithoutExitNodePref(' "$GO"; then
  ok "A3: DevicesWithoutExitNodePref is the single implementation"
else
  bad "A3: the query helper is missing"
fi

hdr "B. both pages feed the marker to their template"

if grep -q 'DevicesWithoutExitNodePrefForService(c.UserID)' "$MYGO"; then
  ok "B1: the user page asks for the CURRENT user's inert devices"
else
  bad "B1: /my/exit-rules does not compute the marker"
fi
if grep -q '"no_exit_node_devices": noExitNodeDevices' "$MYGO" && grep -q '"no_exit_node_list":    inertDeviceLabels(noExitNodeDevices)' "$MYGO"; then
  ok "B2: the user page passes the map (per-device badge) and the label list (banner)"
else
  bad "B2: the user page does not pass the marker into the template"
fi
if grep -q 'DevicesWithoutExitNodePrefForService(uid)' "$ADMGO" && grep -q '"NoExitNodeList":    inertList' "$ADMGO"; then
  ok "B3: the admin page computes it per user that HAS rules (one query per user, not per rule)"
else
  bad "B3: the admin page does not pass the marker"
fi
if grep -q 'if inertErr != nil' "$MYGO"; then
  ok "B4: a read failure degrades to 'nothing marked' instead of a broken page"
else
  bad "B4: a failed marker query would break the page"
fi

hdr "C. the templates render it"

if grep -q 'id="my-no-exit-node"' "$MYTPL" && grep -q 'index \$.no_exit_node_devices \$host' "$MYTPL"; then
  ok "C1: /my/exit-rules renders the banner AND a per-device badge"
else
  bad "C1: the user page renders no banner/badge for inert rules"
fi
if grep -q 'id="admin-no-exit-node"' "$ADMTPL" && grep -q 'range \$i, \$d := .NoExitNodeList' "$ADMTPL"; then
  ok "C2: /admin/exit-rules renders the banner with the device list"
else
  bad "C2: the admin page renders no banner for inert rules"
fi
if grep -q 'exit_rules.no_exit_node_badge' "$MYTPL"; then
  ok "C3: the badge has its own translated label (not an icon-only marker)"
else
  bad "C3: the badge has no label"
fi

hdr "D. the texts say what the state IS, in both languages"

for key in no_exit_node_badge no_exit_node_help no_exit_node_banner; do
  n="$(grep -c "\"exit_rules.$key\"" "$I18N" || true)"
  if [ "$n" -eq 2 ]; then
    ok "D: exit_rules.$key is defined in RU and EN"
  else
    bad "D: exit_rules.$key appears $n time(s) in the catalogue — RU and EN are required"
  fi
done
if [ "$(grep -c 'missing-pref-' "$I18N" || true)" -ge 4 ]; then
  ok "D: both texts point at the journal reason (missing-pref-…) in both languages"
else
  bad "D: the banner does not tell the operator where the refusal is explained"
fi

hdr "E. the marker is exact in both directions"

GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "E1: go is not on PATH — run the B343 unit test on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/feature/exit_rules/ -run 'B343' -count=1 2>&1)"; then
    ok "E1: the B343 unit test passes ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "E1: the B343 unit test fails: $(printf '%s' "$OUT" | tail -3)"
  fi
fi

hdr "F. tracked, registered, indexed"

for f in scripts/check_b343_inert_rules_visible.sh "$GO" "$TEST"; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    ok "F: tracked by git: $f"
  else
    bad "F: NOT tracked by git (AGENTS §2 trap #11): $f"
  fi
done
if grep -q 'check_b343_inert_rules_visible.sh' "$CAT"; then
  ok "F: registered in scripts/verify_pre_deploy.sh"
else
  bad "F: not registered in scripts/verify_pre_deploy.sh — it would never run"
fi
if grep -q '\*\*B343\*\*' AGENTS.md; then
  ok "F: recorded in the AGENTS.md block index"
else
  bad "F: no B343 line in the AGENTS.md block index"
fi

hdr "B343 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
