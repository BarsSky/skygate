#!/usr/bin/env bash
# check_b369_cluster_table_not_clipped.sh — B369 (2026-10-09): /admin/cluster's
# tables must scroll instead of being clipped.
#
# THE MEASUREMENT
# ---------------
# The operator's screenshot of /admin/cluster (1088 px desktop) shows the row
# action buttons — «Снять», «Слить и удалить», «Обновить» — hanging PAST the
# card's right border. Cause, and it is systemic rather than cosmetic:
#
#   * `body{overflow-x:hidden}` (themes.css:209) means there is NO page-level
#     horizontal scrollbar, so a table wider than its card is silently CLIPPED;
#   * `.table-wrap{overflow-x:auto}` (themes.css:459) is the only scroller and it
#     is OPT-IN — 27 template files use it, and cluster.html was the one
#     wide-table page with none;
#   * the B357 systemic fix (`.card{overflow-x:auto}`, `th{white-space:normal}`)
#     is inside `@media (max-width:768px)`, i.e. MOBILE only — desktop kept the
#     clipping;
#   * the nodes table has NINE columns, one of them the long B359 reason sentence
#     (which defined the table's preferred width) and one the action buttons.
#
# WHAT THIS SCRIPT PINS
#   A. every <table> in cluster.html lives inside a .table-wrap (so the card
#      scrolls), and the reason cell is bounded by a block-level max-width;
#   B. the B357 prohibitions are respected (no `overflow-wrap:anywhere`, no
#      `word-break:break-all` — either one collapses a cell to one glyph per
#      line, which is the bug B357 fixed);
#   C. the markup still PARSES: the handlers' own TestLoadTemplates panics on any
#      parse error, so running it is the behavioural half;
#   D. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs a tool this host lacks.
#
# Usage:  bash scripts/check_b369_cluster_table_not_clipped.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

TPL=internal/handlers/templates/admin/cluster.html
SELF=scripts/check_b369_cluster_table_not_clipped.sh

[ -f "$TPL" ] || { bad "A0: missing $TPL"; echo "B369 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }

# =====================================================================
hdr "A. every table on the page scrolls, and the long column is bounded"

TABLES="$(grep -c '<table' "$TPL" || true)"
WRAPS="$(grep -c 'class="table-wrap"' "$TPL" || true)"
if [ "$TABLES" -gt 0 ] && [ "$TABLES" -eq "$WRAPS" ]; then
  ok "A1: all $TABLES table(s) are wrapped in .table-wrap — the card scrolls instead of clipping"
else
  bad "A1: $TABLES table(s) but $WRAPS .table-wrap wrapper(s) — a table wider than the card is CLIPPED on desktop (body{overflow-x:hidden})"
fi

# A2 — the wrapper must come BEFORE the table it protects, not after it.
BAD_ORDER="$(awk '
  /class="table-wrap"/ { wrap=NR }
  /<table/            { if (wrap == 0 || NR - wrap > 3) print "line "NR": <table> has no .table-wrap in the 3 lines before it" }
' "$TPL")"
if [ -z "$BAD_ORDER" ]; then
  ok "A2: every <table> is preceded by its own .table-wrap"
else
  bad "A2: a table is not inside its wrapper:
$BAD_ORDER"
fi

if grep -q 'max-width:360px' "$TPL"; then
  ok "A3: the reason cell is bounded by a block-level max-width (it used to define the table width)"
else
  bad "A3: the long reason column has no width bound — nine columns of preferred width are what pushed the buttons out"
fi

# =====================================================================
hdr "B. the B357 prohibitions still hold"

# The template's own {{/* … */}} comments explain WHY those properties are
# banned and quote them, so the scan runs on the markup with the comments
# removed — otherwise the fix's own documentation fails the contract.
MARKUP="$SKY_TMP/cluster.markup.html"
awk '
  /\{\{\/\*/ { incomment = 1 }
  !incomment { print }
  /\*\}\}/   { incomment = 0 }
' "$TPL" > "$MARKUP"

if grep -q 'overflow-wrap *: *anywhere' "$MARKUP"; then
  bad "B1: cluster.html carries overflow-wrap:anywhere — it collapses the cell's min-content to ONE character (B357's «П о л ь з о в а т е л ь»)"
else
  ok "B1: no overflow-wrap:anywhere"
fi
if grep -q 'word-break *: *break-all' "$MARKUP"; then
  bad "B2: cluster.html carries word-break:break-all (B357 removed those)"
else
  ok "B2: no word-break:break-all"
fi

# =====================================================================
hdr "C. the markup still parses (the behavioural half)"

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "C1: no go binary in PATH — TestLoadTemplates was not run (it panics on any template parse error)"
else
  if out="$("$GOBIN" test ./internal/handlers/ -run 'TestLoadTemplates' -count=1 2>&1)"; then
    ok "C1: TestLoadTemplates passes — every embedded template, cluster.html included, still parses"
  else
    bad "C1: TestLoadTemplates failed:
$(printf '%s\n' "$out" | tail -15)"
  fi
fi

# =====================================================================
hdr "D. tracked by git (AGENTS trap #11), registered, indexed"

if git ls-files --error-unmatch "$SELF" >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  skip "D1: not yet tracked by git (the lead commits this block)"
fi
if grep -q 'run_check "B369"' scripts/verify_pre_deploy.sh; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B369\*\*' AGENTS.md; then
  ok "D3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "D3: AGENTS.md has no B369 bullet at line start"
fi

printf '\n\033[1mB369 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
