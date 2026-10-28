#!/usr/bin/env bash
# check_b436_html_balance.sh — B436 (2026-10-28)
#
# «на маленьком экране сбивается компоновка элементов и часть из них наплывают друг на друга».
#
# The templates PARSE (html/template) and RENDER (the render tests in internal/handlers), so a
# page can answer 200 with markup that merely LOOKS fine and still lay out wrong, because one
# element is nested inside the wrong box. Measured on the live panel with the new
# scripts/audit_html_balance.py — three structural defects, all on pages the operator uses:
#
#   1. admin/telegram.html  the container card was OPENED TWICE with a single closing tag, so every
#                           card after it was nested inside that one and .card-grid stopped being a
#                           grid (the operator's «наплывают друг на друга»).
#   2. admin/derp_relays_init.html  a stray closing div ended .card right after the host section:
#                           the SSH/ports grids, the warning and the submit button rendered OUTSIDE
#                           the card, and the real close at the bottom became a stray tag.
#   3. admin/derp_relays.html       the per-row edit form was opened in the FIRST table cell and
#                           closed in the LAST one. A form cannot cross a cell boundary, so a strict
#                           parser ends it at the first cell close and the Save button belongs to no
#                           form at all. The fix uses the HTML5 form attribute: an empty form
#                           element per row plus form="relay-edit-<id>" on every control.
#
# CONTRACTS: 15. The audit's exit status is the contract; the planted violations prove the detector
# still fires (a budget of zero that nothing can trip is not a contract), and the "no false
# positive" case proves it is usable: /my/telegram carries markup-looking text inside an onclick
# attribute, and a naive regex scanner reports a phantom tag for it.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

AUDIT=scripts/audit_html_balance.py
GATE=scripts/verify_pre_deploy.sh
TPL=internal/handlers/templates
for f in "$AUDIT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
[ -d "$TPL" ] || { bad "missing template root: $TPL"; exit 1; }

PY="$(command -v python3 || command -v python || true)"
[ -n "$PY" ] || { bad "no python interpreter (python3/python) on PATH — this check needs one"; exit 1; }

# Printed FIRST: if this check ever dies before its first row, the log must still name the inputs
# (B435.1 measured that an early exit is otherwise indistinguishable from a failed contract).
printf '  DIAG B436: audit=%s python=%s tpl=%s pwd=%s\n' "$AUDIT" "$PY" "$TPL" "$PWD"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b436.XXXXXX" 2>/dev/null || true)"
if [ -z "${WORK:-}" ] || [ ! -d "$WORK" ]; then
  bad "cannot create a scratch directory under $PWD/tmp (B340: never a fixed /tmp path)"
  exit 1
fi
trap 'rm -rf "$WORK"' EXIT

# Count open/close of one tag in a template WITHOUT the audit: an independent statement of the
# same fact, so a bug in the audit cannot make both halves agree.
count_tag() { grep -o "$1" "$2" 2>/dev/null | wc -l | tr -d ' '; }
count_num() { local n; n="$(grep -c -- "$1" "$2" 2>/dev/null || true)"; printf '%s' "${n:-0}"; }

hdr "A. the balance audit is a tool, not a paragraph"
if "$PY" -c 'import sys; compile(open(sys.argv[1], encoding="utf-8").read(), sys.argv[1], "exec")' "$AUDIT" 2>/dev/null; then
  ok "A1: the audit compiles"
else
  bad "A1: the audit does not compile (python syntax error)"
fi
if git ls-files --error-unmatch "$AUDIT" >/dev/null 2>&1; then
  ok "A2: the audit is tracked by git (trap 11 — an untracked helper is not shipped)"
else
  bad "A2: the audit is not tracked by git"
fi
case "$(count_num '--root' "$AUDIT")" in
  0) bad "A3: the audit takes no --root, so a planted violation cannot prove the detector" ;;
  *) ok "A3: the audit accepts --root, so a planted violation can prove the detector" ;;
esac

hdr "B. every template is balanced, and the scan really walked the tree"
OUT="$("$PY" "$AUDIT" --root "$TPL" 2>&1)"; RC=$?
if [ "$RC" -eq 0 ]; then
  ok "B1: all templates balanced (rc=0): $(printf '%s\n' "$OUT" | tail -1)"
else
  bad "B1: the templates are IMBALANCED (rc=$RC): $(printf '%s\n' "$OUT" | head -6 | tr '\n' ' ')"
fi
# A scan that walked nothing would also say "balanced" — a filter matching nothing must never
# report green (trap 16). The summary names a file count; require a plausible one.
SCANNED="$(printf '%s\n' "$OUT" | sed -n 's/^html-balance: \([0-9][0-9]*\) template.*/\1/p' | tail -1)"
if [ -n "${SCANNED:-}" ] && [ "$SCANNED" -ge 55 ]; then
  ok "B2: the scan covered $SCANNED templates (an empty walk cannot look green)"
else
  bad "B2: the scan reports '${SCANNED:-no count}' templates — it did not walk the tree"
fi
# The three repaired pages must balance ON THEIR OWN, not merely inside the sum of the tree.
for page in admin/telegram.html admin/derp_relays_init.html admin/derp_relays.html; do
  if [ ! -f "$TPL/$page" ]; then bad "B3: $page is missing"; continue; fi
  mkdir -p "$WORK/one/$(dirname "$page")"
  cp "$TPL/$page" "$WORK/one/$page"
  if "$PY" "$AUDIT" --root "$WORK/one" >/dev/null 2>&1; then
    ok "B3: $page balances on its own"
  else
    bad "B3: $page is still imbalanced: $("$PY" "$AUDIT" --root "$WORK/one" 2>&1 | head -3 | tr '\n' ' ')"
  fi
  rm -rf "$WORK/one"
done

hdr "C. the detector still fires (and does not cry wolf)"
mkdir -p "$WORK/extra"
printf '%s\n' '<div class="card">' '<p>ok</p>' '</div>' '</div>' > "$WORK/extra/broken_extra_close.html"
if ! "$PY" "$AUDIT" --root "$WORK/extra" >/dev/null 2>&1; then
  case "$("$PY" "$AUDIT" --root "$WORK/extra" 2>&1 | grep -c 'broken_extra_close' || true)" in
    0) bad "C1: the extra closing div was detected but the report does not name the file" ;;
    *) ok "C1: a planted extra closing div is detected and named" ;;
  esac
else
  bad "C1: a planted extra closing div PASSED — the detector is dead"
fi
rm -rf "$WORK/extra"

# The live /admin/derp/relays shape before this block: one form, two cells.
mkdir -p "$WORK/cells"
{
  printf '%s\n' '<table>' '  <tr>' '    <td>' '      <form method="POST" action="/save">'
  printf '%s\n' '      <input type="text" name="hostname" value="x">' '    </td>' '    <td>'
  printf '%s\n' '      <input type="text" name="url" value="y">' '    </td>'
  printf '%s\n' '    <td><button type="submit">Save</button></form></td>' '  </tr>' '</table>'
} > "$WORK/cells/form_across_cells.html"
if ! "$PY" "$AUDIT" --root "$WORK/cells" >/dev/null 2>&1; then
  ok "C2: a form opened across table cells is detected (the live /admin/derp/relays shape)"
else
  bad "C2: a form spanning table cells PASSED — the class this block fixed is not covered"
fi
rm -rf "$WORK/cells"

# The /my/telegram shape: markup-looking text inside an attribute value. A regex scanner reports a
# phantom element and then a stray closing tag; the tokenizer must ignore it.
mkdir -p "$WORK/attr"
{
  printf '%s\n' '<div class="card">'
  printf '%s\n' '  <button type="button" onclick="this.innerHTML=&#39;&lt;i class=&quot;fa-solid fa-check&quot;&gt;&lt;/i&gt; copied&#39;">'
  printf '%s\n' '    <i class="fa-solid fa-copy"></i> copy' '  </button>' '</div>'
} > "$WORK/attr/attr_markup.html"
if "$PY" "$AUDIT" --root "$WORK/attr" >/dev/null 2>&1; then
  ok "C3: markup inside an attribute value is not counted as a tag (no false positive)"
else
  bad "C3: the audit reports a phantom tag for markup inside an attribute: $("$PY" "$AUDIT" --root "$WORK/attr" 2>&1 | head -2 | tr '\n' ' ')"
fi
rm -rf "$WORK/attr"

case "$WORK" in
  "$PWD"/tmp/*) ok "C4: the scratch tree lives under PWD/tmp, never a fixed /tmp path (trap 13)" ;;
  *) bad "C4: the scratch tree is outside PWD/tmp: $WORK" ;;
esac

hdr "D. the repaired pages still DO their job"
RELAYS="$TPL/admin/derp_relays.html"
if [ -f "$RELAYS" ]; then
  NAMED_CTL="$(count_num 'form="relay-edit-' "$RELAYS")"
  ROW_FORM="$(count_num '<form id="relay-edit-' "$RELAYS")"
  if [ "${NAMED_CTL:-0}" -ge 9 ] && [ "${ROW_FORM:-0}" -eq 1 ]; then
    ok "D1: the row has one form element and $NAMED_CTL controls name it (form attribute, not nesting)"
  else
    bad "D1: controls naming the row form=${NAMED_CTL:-0} (want >=9), row form elements=${ROW_FORM:-0} (want 1)"
  fi
  RELAYS_ALL="$(cat "$RELAYS")"
  MISSING=""
  for act in edit toggle test delete; do
    case "$RELAYS_ALL" in
      *"/admin/derp/relays/$act"*) : ;;
      *) MISSING="$MISSING $act" ;;
    esac
  done
  if [ -z "$MISSING" ]; then
    ok "D2: all four per-row POST actions survive the markup fix (edit/toggle/test/delete)"
  else
    bad "D2: the markup fix dropped the action(s):$MISSING"
  fi
else
  bad "D1/D2: $RELAYS is missing"
fi
# /admin/derp/relays/init: the card must still contain the whole form — the submit row must come
# BEFORE the card's closing tag, not after it (that was the visible defect).
INIT_TPL="$TPL/admin/derp_relays_init.html"
if [ -f "$INIT_TPL" ]; then
  INSIDE="$(awk '
    /derp_init.submit/                  { submit = NR }
    /^<\/div>/ && submit && NR > submit { print "inside"; exit }
  ' "$INIT_TPL")"
  if [ "${INSIDE:-}" = "inside" ]; then
    ok "D3: the submit row is inside the card on /admin/derp/relays/init (the card no longer ends early)"
  else
    bad "D3: the card closes BEFORE the submit row on /admin/derp/relays/init"
  fi
else
  bad "D3: $INIT_TPL is missing"
fi
# /admin/telegram: the container card is opened once, and the page's own div tags balance in count
# (an independent statement of the same fix — if the audit ever regressed, this still decides).
TGCARD="$TPL/admin/telegram.html"
if [ -f "$TGCARD" ]; then
  KEY="$(count_num 'telegram.container_title' "$TGCARD")"
  DO="$(count_tag '<div' "$TGCARD")"
  DC="$(count_tag '</div>' "$TGCARD")"
  if [ "${KEY:-0}" -eq 1 ] && [ "${DO:-0}" -eq "${DC:-1}" ]; then
    ok "D4: the container card is rendered once and the page's div count balances (${DO} open = ${DC} close)"
  else
    bad "D4: container key count=${KEY:-0} (want 1), div open=${DO:-0} vs close=${DC:-0}"
  fi
else
  bad "D4: $TGCARD is missing"
fi

hdr "E. registered, indexed, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b436_html_balance.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (trap 11)"
else
  bad "E1: this script is not tracked by git"
fi
if grep -qF 'check_b436_html_balance' "$GATE" 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
if grep -q '^\- \*\*B436\*\*' AGENTS.md 2>/dev/null; then
  ok "E3: one-line entry in the AGENTS.md block index"
else
  bad "E3: no B436 entry in the AGENTS block index"
fi
B436_DESC="$(grep -A3 'run_check "B436"' "$GATE" 2>/dev/null)"
case "$B436_DESC" in
  *'`'*) bad "E4: the B436 catalog description contains a backtick (TD-15: it is evaluated by bash)" ;;
  "")    bad "E4: the B436 catalog entry was not found" ;;
  *)     ok "E4: the B436 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB436 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
