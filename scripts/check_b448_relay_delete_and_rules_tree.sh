#!/usr/bin/env bash
# check_b448_relay_delete_and_rules_tree.sh — B448 (2026-10-30)
#
# Two reports, one round:
#
#	«нет возможности удалить релей что выключен»
#	«проблема с админской таблицей правил группировка получилась по одной строчке и нельзя
#	 свернуть всю таблицу чтобы дойти до самого низа»
#
# A. THE DISABLED RELAY COULD NOT BE DELETED. Measured on the reference install:
#
#	derp_relays   2 | derp.skynas.ru | https://derp.skynas.ru:443  | 900 | is_bundled=1 | enabled=1
#	              3 | derp.skynas.ru | https://derp.skynas.ru:8443 | 900 | is_bundled=1 | enabled=0
#
# BOTH rows carry is_bundled=1 (the public relay and the LAN-direct entry point, both
# auto-registered), and DeleteDerpRelay refused EVERY bundled row — so the page hid the delete
# button on both and the only way out was SQL on the live database. The protection that matters
# is not "bundled" but "the LAST bundled row": that row is the on/off switch for the bundled
# derper container (IsBundledDerpRelayEnabled), and deleting it would silently remove the only
# control over a container that may still be running. A DUPLICATE bundled row carries no such
# duty and is deletable now.
#
# The same pair exposed a second defect: IsBundledDerpRelayEnabled ran
# SELECT enabled … WHERE is_bundled = 1 LIMIT 1 with NO ORDER BY, so with one row enabled and
# one disabled the container switch was a coin flip. It is "any enabled bundled row" now.
#
# B. THE RULES TREE HID ITS ONLY ROW. /admin/exit-rules nests user → device → exit node →
# rules, and the exit-node level was ALWAYS closed, so a device whose rules all go through one
# relay hid its only child behind a header reading «1» — one click per one line. A group that is
# the only group of its device opens now (adminNodeGroupOpens), and the B376 collapse/expand
# controls became reachable from where the operator actually is: the bar is sticky, a second
# copy sits at the BOTTOM of the tree, and the choice is remembered in localStorage so paging
# through 50-rule windows does not re-expand everything.
#
# CONTRACTS: 27.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

DEL="internal/db/derp_relays.go"
DEL_T="internal/db/derp_relays_b448_test.go"
RL="internal/feature/admin/derp_relays.go"
TPL="internal/handlers/templates/admin/derp_relays.html"
FORM="internal/feature/exit_rules/form_admin.go"
FORM_T="internal/feature/exit_rules/form_admin_b448_test.go"
TREE="internal/handlers/templates/admin/exit_rules.html"
TREE_T="internal/handlers/exit_rules_render_b448_test.go"
I18N="internal/i18n/catalog_derp.go"
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$DEL" "$DEL_T" "$RL" "$TPL" "$FORM" "$FORM_T" "$TREE" "$TREE_T" "$I18N" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B448: relays=%s tree=%s\n' "$DEL" "$TREE"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b448.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. a disabled duplicate relay can be deleted again"
GOFMT_OUT="$(gofmt -l "$DEL" "$DEL_T" "$RL" "$FORM" "$FORM_T" "$TREE_T" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
DEL_SRC="$(cat "$DEL" 2>/dev/null)"
case "$DEL_SRC" in
  *'SELECT count(*) FROM derp_relays WHERE is_bundled = 1'*)
    ok "A2: the delete guard asks HOW MANY bundled rows exist, not just whether this one is bundled" ;;
  *) bad "A2: the guard still refuses every bundled row — the operator's disabled duplicate stays" ;;
esac
case "$DEL_SRC" in
  *'if bundled <= 1 {'*)
    ok "A3: only the LAST bundled row is refused (it is the container switch)" ;;
  *) bad "A3: the last-bundled rule is missing or inverted" ;;
esac
case "$DEL_SRC" in
  # The old query, verbatim. The comment above it quotes the pre-fix shape with an ellipsis, so
  # this matches the CODE and not the explanation of the code.
  *'SELECT enabled FROM derp_relays WHERE is_bundled = 1 LIMIT 1'*)
    bad "A4: IsBundledDerpRelayEnabled still reads an arbitrary row (LIMIT 1, no ORDER BY)" ;;
  *'SELECT count(*) FROM derp_relays WHERE is_bundled = 1 AND enabled = 1'*)
    ok "A4: the container switch is 'any enabled bundled row', so a disabled duplicate cannot mask it" ;;
  *) bad "A4: the container switch no longer answers 'is a bundled relay switched on'" ;;
esac
if has "$RL" 'type DerpRelayRow struct {' && has "$RL" 'DerpRelay: r, Deletable: !r.IsBundled || bundled > 1'; then
  ok "A5: the row model carries the SAME predicate the handler enforces"
else
  bad "A5: no row model with the delete rule — the button and the handler can disagree again"
fi
# v1.5.199 shipped the flag on RelayMapStatus while the actions cell iterates the RELAYS table, so
# /admin/derp/relays died with "can't evaluate field Deletable in type db.DerpRelay". The wiring is
# the whole point: the page must FEED the model its template reads.
if has "$RL" '"Relays":       DerpRelayRows(relays),'; then
  ok "A6: the handler feeds the page the annotated rows (not the raw db rows)"
else
  bad "A6: the page is given rows without the delete decision — that is the v1.5.199 dead page"
fi
if has "$TPL" '{{if .Deletable}}' && has "$TPL" 'derp.relays_last_bundled_no_delete_help'; then
  ok "A7: the delete button follows Deletable, and the refused case names WHY"
else
  bad "A7: the template still keys the button on .IsBundled (the pre-fix behaviour)"
fi
# The guard that was missing: an EXECUTING render test for THIS page. The exit-rules render tests
# could not catch the dead page because they render a different template.
if has internal/handlers/derp_relays_render_b448_test.go 'body-admin-derp_relays' \
   && has internal/handlers/derp_relays_render_b448_test.go 'TestDerpRelaysPageRejectsTheRawRowModel_B448'; then
  ok "A8: the relays page is rendered in a test, and feeding it the RAW row model must fail"
else
  bad "A8: nothing executes the relays template — the v1.5.199 dead page can ship again"
fi
I18N_SRC="$(cat "$I18N" 2>/dev/null)"
case "$I18N_SRC" in
  *'"derp.relays_last_bundled_no_delete"'*'"derp.relays_last_bundled_no_delete_help"'*)
    ok "A9: the new lock label + tooltip exist" ;;
  *) bad "A7: derp.relays_last_bundled_no_delete(_help) is missing" ;;
esac
RU_CNT="$(printf '%s' "$I18N_SRC" | grep -c 'derp.relays_last_bundled_no_delete_help' || true)"
if [ "${RU_CNT:-0}" -ge 2 ]; then
  ok "A10: defined in BOTH catalogues (RU + EN)"
else
  bad "A10: only ${RU_CNT:-0} definition(s) — the parity test wants RU and EN"
fi
case "$I18N_SRC" in
  *'Bundled релеи нельзя удалять'*|*'Bundled relays are not deletable'*)
    bad "A11: the flash still claims bundled relays are undeletable" ;;
  *'Последний bundled-релей'*|*'The LAST bundled relay'*)
    ok "A11: the flash names the LAST-bundled rule instead of banning the flag" ;;
  *) bad "A11: the undeletable flash does not explain the new rule" ;;
esac
if has "$DEL_T" 'TestB448_BundledDuplicateIsDeletable' && has "$DEL_T" 'TestB448_BundledSwitch'; then
  ok "A12: both halves are pinned by db tests (delete + the switch)"
else
  bad "A12: no behavioural test for the delete rule / the switch"
fi

hdr "B. the rules tree never hides a single row, and can be collapsed from the bottom"
FORM_SRC="$(cat "$FORM" 2>/dev/null)"
case "$FORM_SRC" in
  *'func adminNodeGroupOpens(nodeGroups int) bool {'*)
    ok "B1: the 'does this group open' rule has a name (and a test)" ;;
  *) bad "B1: no adminNodeGroupOpens helper" ;;
esac
case "$FORM_SRC" in
  *'return nodeGroups == 1'*)
    ok "B2: exactly one group of a device opens; two or more stay closed (there the header IS the choice)" ;;
  *) bad "B2: the open rule is not 'the only group of its device'" ;;
esac
if has "$FORM" 'dg.SingleNode = adminNodeGroupOpens(len(dg.Nodes))'; then
  ok "B3: the flag is filled from the helper, not by a second inline comparison"
else
  bad "B3: SingleNode is not driven by the helper"
fi
if has "$TREE" '{{if $devData.SingleNode}} open{{end}}'; then
  ok "B4: the template opens the only exit-node group"
else
  bad "B4: the exit-node level still hides everything behind a click"
fi
if has "$TREE" 'position:sticky'; then
  ok "B5: the view/collapse bar is sticky — reachable at any scroll depth"
else
  bad "B5: the controls are still only at the top of a thousands-of-pixels page"
fi
if has "$TREE" 'id="admin-view-controls-bottom"'; then
  ok "B6: a second control bar sits at the BOTTOM of the tree"
else
  bad "B6: there is no way to collapse from the bottom"
fi
if has "$TREE" 'skygate.admin.exit_rules.tree_open'; then
  ok "B7: the collapse choice is remembered across pages (localStorage)"
else
  bad "B7: every page load re-expands the tree, so the choice is lost on page 2"
fi
if has "$TREE" 'root.querySelectorAll('"'"'details'"'"').forEach'; then
  ok "B8: expand/collapse reaches EVERY nesting level (not just the outer <details>)"
else
  bad "B8: collapse-all only touches one level"
fi
if has "$FORM_T" 'TestAdminNodeGroupOpens_B448' && has "$TREE_T" 'TestAdminTreeSingleGroupRendersOpen_B448' \
   && has "$TREE_T" 'TestAdminTreeCollapseControlsReachable_B448'; then
  ok "B9: the rule AND the rendered markup are pinned by tests"
else
  bad "B9: one half of the tree behaviour has no test"
fi

hdr "C. pinned by running the tests"
if ! command -v go >/dev/null 2>&1; then
  skip "C1: no go toolchain on PATH — cannot run TestB448 here (CI is authoritative)"
else
  go test ./internal/feature/exit_rules ./internal/handlers -run 'B448' -count=1 >"$WORK/t.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C1: the tree half of TestB448 is green (rule + render)"
  else
    bad "C1: TestB448 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
  go test ./internal/db -run 'B448' -count=1 >"$WORK/d.log" 2>&1
  if [ $? -eq 0 ]; then
    if grep -q 'SKIP' "$WORK/d.log" 2>/dev/null; then
      skip "C2: the db half needs SKYGATE_TEST_PG_DSN (set in CI) — verified there"
    else
      ok "C2: the db half of TestB448 is green (delete rule + container switch)"
    fi
  else
    bad "C2: the db half of TestB448 failed — $(tail -3 "$WORK/d.log" | tr '\n' ' ')"
  fi
fi

hdr "D. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b448_relay_delete_and_rules_tree.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: this script is not tracked by git"
fi
if grep -qF 'check_b448_relay_delete_and_rules_tree' "$GATE" 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B448"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "D3: the B448 catalog description contains a backtick (TD-15)" ;;
  "")    bad "D3: the B448 catalog entry was not found" ;;
  *)     ok "D3: the B448 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B448**' "$AGENTS" 2>/dev/null; then
  ok "D4: indexed in AGENTS.md"
else
  bad "D4: no one-line index entry in AGENTS.md"
fi

printf '\n\033[1mB448 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
