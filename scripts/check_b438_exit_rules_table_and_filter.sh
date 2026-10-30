#!/usr/bin/env bash
# check_b438_exit_rules_table_and_filter.sh — B438 (2026-10-29)
#
# ДВЕ ЖАЛОБЫ ОПЕРАТОРА, измеренные на шаблонах:
#
#  1) «проблема с компоновкой таблиц в exit правилах». Счётного расхождения th/td
#     нет — причина другая и точнее: таблица диапазонов (5 колонок на /my, 6 на
#     /admin) была ВЛОЖЕНА в четвёртую ячейку внешней таблицы (4 колонки). «table-wrap»
#     имеет нулевую intrinsic-ширину, авто-раскладка отдаёт такой колонке почти ничего,
#     и заголовок вложенной таблицы физически не мог совпасть ни с одной колонкой
#     сверху — отсюда «Действие | Цель | Preferred» над пустым местом и всё,
#     сжатое в правую щель. Правка: раскрыватель — отдельная строка с colspan, равным
#     числу колонок внешнего заголовка; ни новых обёрток, ни инлайн-ширин, поэтому
#     храповики B410/B435.1 не двигаются.
#
#  2) «при выборе фильтра по устройствам перезагружается вся страница, и снятие
#     фильтра тоже». Фильтра по устройству в клиенте не было ВООБЩЕ: единственным
#     способом сузить список была ссылка `?device=…`, то есть полная навигация — и на
#     выборе, и на «показать все». Правка: `#filter-device` с onchange (без формы) +
#     `data-device` на строках и на контейнерах групп + предикат в существующем
#     applyFilter(). Ссылки `?device=` остаются: это no-JS путь и контракт B348 B6.
#
# CONTRACTS: 17.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

MY=internal/handlers/templates/exit_rules.html
ADM=internal/handlers/templates/admin/exit_rules.html
I18N=internal/i18n/catalog_exit_rules.go
GATE=scripts/verify_pre_deploy.sh
for f in "$MY" "$ADM" "$I18N" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B438: my=%s admin=%s i18n=%s pwd=%s\n' "$MY" "$ADM" "$I18N" "$PWD"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b438.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

count() { local n; n="$(grep -c -- "$2" "$1" 2>/dev/null || true)"; printf '%s' "${n:-0}"; }
# tables nested inside the outer table's cell — the defect itself
nested_in_cell() {
  awk '/<td class="col-delete">/{f=1} f&&/<table/{c++} f&&/<\/td>/{f=0} END{print c+0}' "$1"
}

hdr "A. the expander is a full-width row, not a nested column"
for f in "$MY" "$ADM"; do
  n="$(nested_in_cell "$f")"
  if [ "${n:-1}" -eq 0 ]; then
    ok "A1: $f — no table nested inside the outer table's cell"
  else
    bad "A1: $f — $n table(s) still nested inside <td class=\"col-delete\"> (the layout defect)"
  fi
  c="$(count "$f" 'colspan="4"')"
  if [ "${c:-0}" -ge 1 ]; then
    ok "A2: $f — the detail row spans all 4 outer columns"
  else
    bad "A2: $f — no colspan=\"4\": the detail row cannot line up with the outer header"
  fi
  if grep -q 'class="rule-detail"' "$f"; then
    ok "A3: $f — the expander is its own row (class=\"rule-detail\")"
  else
    bad "A3: $f — the expander is still a cell child"
  fi
done
# A4: the per-rule <form>s must sit AFTER the nested table closes, not inside it.
FORM_LN="$(grep -n 'form id="single-delete-' "$MY" | head -1 | sed 's/:.*//')"
if [ -n "${FORM_LN:-}" ]; then
  CLOSE_LN="$(awk -v f="$FORM_LN" 'NR<f && /<\/table>/{n=NR} END{print n+0}' "$MY")"
  if [ "${CLOSE_LN:-0}" -gt 0 ] && [ "${CLOSE_LN}" -lt "${FORM_LN}" ]; then
    ok "A4: <form> elements live outside the table (no foster parenting)"
  else
    bad "A4: a <form> is still between </tr> and </table> (line $FORM_LN, last table close $CLOSE_LN)"
  fi
else
  bad "A4: the per-rule delete form disappeared"
fi

hdr "B. the device filter is client-side (no page reload)"
if [ "$(count "$MY" 'id="filter-device"')" = "1" ]; then
  ok "B1: #filter-device exists exactly once"
else
  bad "B1: #filter-device is missing or duplicated"
fi
if [ "$(count "$MY" 'onchange="applyFilter()"')" -ge 2 ]; then
  ok "B2: the device select filters through applyFilter() like the type select"
else
  bad "B2: #filter-device does not call applyFilter() — it would need a submit (reload)"
fi
case "$(cat "$MY")" in
  *"filter-device"*)
    case "$(awk '/window.applyFilter = function/,/^      };/' "$MY")" in
      *"filter-device"*) ok "B3: applyFilter() reads the device select" ;;
      *) bad "B3: applyFilter() ignores #filter-device" ;;
    esac ;;
  *) bad "B3: no filter-device at all" ;;
esac
if [ "$(count "$MY" 'data-device=')" -ge 4 ]; then
  ok "B4: rows/containers carry data-device ($(count "$MY" 'data-device=') sites)"
else
  bad "B4: data-device is missing on the rule rows — the client filter has nothing to match"
fi
# B5: no NEW navigation path. The page legitimately has ONE: the page-size selector
# re-assigns location.search. A hard reload or a history.back() would be new, and the
# device filter must not add a second location.search assignment.
HARD="$(grep -cE 'location\.reload|history\.back' "$MY" 2>/dev/null || true)"
NAV="$(grep -cE 'location\.search[[:space:]]*=' "$MY" 2>/dev/null || true)"
if [ "${HARD:-0}" -eq 0 ] && [ "${NAV:-0}" -le 1 ]; then
  ok "B5: no reload path was added (page-size selector is the only navigation: ${NAV:-0})"
else
  bad "B5: the page gained a navigation path (reload/back=${HARD:-0}, location.search=${NAV:-0})"
fi
# B6: the no-JS drill-down must survive (behavioural: run the existing contract).
if bash scripts/check_b348_device_navigation.sh >"$WORK/b348.log" 2>&1; then
  ok "B6: the ?device= links still work (B348 green — no-JS path intact)"
else
  bad "B6: B348 went red — the drill-down links were broken: $(grep -c FAIL "$WORK/b348.log" 2>/dev/null) FAIL row(s)"
fi

hdr "C. the duplicated heading is gone"
if [ "$(count "$MY" 'exit_rules.device_status_title')" -le 1 ]; then
  ok "C1: the device-status heading is rendered once"
else
  bad "C1: 'Устройства и назначенный exit-узел' still appears $(count "$MY" 'exit_rules.device_status_title') times on one page"
fi
if grep -q 'exit_rules_admin.device_index_title' "$MY"; then
  ok "C2: the device index uses its own label (devices + rule counts)"
else
  bad "C2: the device index has no label of its own"
fi

hdr "D. the two new strings come in RU+EN pairs"
for k in exit_rules.filter_device exit_rules.filter_device_all; do
  n="$(count "$I18N" "\"$k\"")"
  if [ "${n:-0}" -eq 2 ]; then
    ok "D1: $k is defined twice (RU + EN)"
  else
    bad "D1: $k is defined ${n:-0} time(s) — B325 requires a pair"
  fi
done
if go test ./internal/i18n -run TestCatalogsParity -count=1 >"$WORK/i18n.log" 2>&1; then
  ok "D2: TestCatalogsParity is green"
else
  bad "D2: TestCatalogsParity failed — $(tail -2 "$WORK/i18n.log" | tr '\n' ' ')"
fi

hdr "E. the markup stays valid and the frozen ratchets do not move"
. scripts/lib/python_audit.sh   # B446: python3 OR python, and never a FAIL for a missing tool
PY="$(skygate_python)"
if [ -z "$PY" ]; then
  skip "E1: no python interpreter (python3/python) on PATH — the markup balance audit cannot run here"
elif "$PY" scripts/audit_html_balance.py >"$WORK/bal.log" 2>&1; then
  ok "E1: every template is still balanced (B436 audit)"
else
  bad "E1: the markup is imbalanced after the restructure: $(tail -1 "$WORK/bal.log")"
fi
if bash scripts/check_b410_table_overflow.sh >"$WORK/b410.log" 2>&1; then
  ok "E2: B410 still green (no new unwrapped table)"
else
  bad "E2: B410 went red — $(grep -m1 FAIL "$WORK/b410.log")"
fi
if bash scripts/check_b435_1_mobile_layout.sh >"$WORK/b4351.log" 2>&1; then
  ok "E3: B435.1 still green (T/W/N budgets unchanged)"
else
  bad "E3: B435.1 went red — $(grep -m1 -E 'over budget|FAIL' "$WORK/b4351.log")"
fi

hdr "F. registered, indexed, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b438_exit_rules_table_and_filter.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: this script is not tracked by git"
fi
if grep -qF 'check_b438_exit_rules_table_and_filter' "$GATE" 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in the catalog"
fi
if grep -q '^\- \*\*B438\*\*' AGENTS.md 2>/dev/null; then
  ok "F3: one-line entry in the AGENTS.md block index"
else
  bad "F3: no B438 entry in the AGENTS block index"
fi
DESC="$(grep -A3 'run_check "B438"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "F4: the B438 catalog description contains a backtick (TD-15)" ;;
  "")    bad "F4: the B438 catalog entry was not found" ;;
  *)     ok "F4: the B438 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB438 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
