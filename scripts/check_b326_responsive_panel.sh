#!/usr/bin/env bash
# check_b326_responsive_panel.sh
#
# 2026-09-25 (B326, v1.5.89) — the panel must not push a table off-screen on a phone.
#
# OPERATOR REPORT (verbatim):
#
#   «есть проблемы с отображением форм, выпадающих списков и прочих элементов … таблицы
#    становятся широками и большими от чего сложно нормально воспринимать информацию на
#    маленьком экране также некоторые таблицы начинают ломаться и не поддерживают скролл
#    уходя за экран отображения.»
#
# THE MEASURED ROOT CAUSE (panel audit, 2026-09-25):
#
#   * 84 `<table>` across 58 templates: 19 wrapped in `.table-wrap`, 64 NOT wrapped;
#   * `body{overflow-x:hidden}` in the stylesheet means there is NO page-level horizontal
#     scrollbar at all — an unwrapped wide table is not "scrollable off-screen", its
#     right-hand columns are silently CLIPPED and unreachable;
#   * the only scroller was the opt-in `.table-wrap{overflow-x:auto}`;
#   * `table{width:100%}` + `th{white-space:nowrap}` inflated the min-content width (one page
#     had 830px of headers alone);
#   * both `@media` blocks were `max-width:768px` and neither touched table scrolling.
#
# THE FIX is systemic and markup-free (wrapping 64 tables by hand is a large, error-prone
# sweep): under the mobile breakpoint the CARD becomes the horizontal scroll container, the
# forced `nowrap` on headers is dropped, long tokens wrap, and form controls cannot exceed
# their card. The table keeps its normal table layout (unlike `table{display:block}`).
#
# CONTRACTS
#   A. the systemic mobile rules exist in the EMBEDDED stylesheet (the one that is served)
#   B. no template has an unwrapped table outside a card (the CSS scroller is what covers it)
#   C. the reason is documented in the CSS itself, and the hand-wrapped path still exists
#   D. the stylesheet stays brace-balanced and this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B326: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# The stylesheet that is actually SERVED: internal/staticfs/static is embedded
# (`//go:embed static`) and mounted at /static. The repo-root static/ copy is legacy.
CSS=internal/staticfs/static/css/themes.css

hdr "B326 — narrow screens must not clip a table"

if [ -f "$CSS" ]; then
  ok "A0: the embedded stylesheet exists ($CSS)"
else
  bad "A0: the embedded stylesheet is missing — the panel would render unstyled"
fi

# The B326 block: everything after the last @media (max-width:768px) opening brace.
BLOCK="$(awk '/@media *\(max-width: *768px\)/{f=1} f' "$CSS" | tail -40 | tr -d '\r')"
if grep -q 'B326' <<< "$BLOCK"; then
  ok "A1: the responsive block is the B326 one (self-labelled)"
else
  bad "A1: the B326 responsive rules are not present in the embedded stylesheet"
fi

check_rule() { # label, greps... (all must match inside BLOCK)
  local label="$1"; shift
  local missing=""
  for pat in "$@"; do
    grep -qE -- "$pat" <<< "$BLOCK" || missing="$missing [$pat]"
  done
  if [ -z "$missing" ]; then
    ok "$label"
  else
    bad "$label — missing:$missing"
  fi
}

check_rule "A2: the CARD is the horizontal scroll container (covers all 64 unwrapped tables)" \
  '\.card *\{[^}]*overflow-x: *auto'
check_rule "A3: the forced nowrap on headers is dropped on narrow screens" \
  'th *\{[^}]*white-space: *normal'
check_rule "A4: long tokens (keys, DSNs, UUIDs) wrap instead of defining a column" \
  'overflow-wrap: *anywhere'
check_rule "A5: form controls cannot exceed their card (the dropdown half of the report)" \
  'select *, *input *, *textarea *\{[^}]*max-width: *100%'
check_rule "A6: the hard 3-column create-user form collapses on mobile" \
  '\.user-form-grid *\{[^}]*grid-template-columns: *1fr'
# B326.1: three forms carry an INLINE `display:grid;grid-template-columns:repeat(N,1fr)` that
# no class-based media query can reach, plus inline `min-width` on controls — the forms half of
# the operator's report («проблемы с отображением форм»).
check_rule "A7: an inline fixed-column grid collapses on narrow screens (attribute selector)" \
  '\[style\*="grid-template-columns"\][^{]*\{[^}]*grid-template-columns: *1fr'
check_rule "A8: an inline min-width on a control cannot exceed the card" \
  'min-width: *0!important'

# The reason the fix is needed at all: without a page-level scroller, clipped is worse than
# scrolled. If someone removes body{overflow-x:hidden} the card scroller is still correct —
# but the comment must stay so the next reader knows why the rule is there.
if grep -q 'overflow-x:hidden' "$CSS"; then
  ok "C1: the stylesheet still explains the clipping (body overflow-x:hidden is present)"
else
  skip "C1: body no longer clips horizontally — the card scroller is now belt-and-braces"
fi
if grep -q 'overflow-x:hidden' <(grep -A30 'B326' "$CSS"); then
  ok "C2: the B326 comment names the clipping as the root cause"
else
  bad "C2: the B326 block does not explain WHY the card must scroll"
fi

# --- B: unwrapped tables must sit inside a card (that is what the CSS covers) ----------
UNCOVERED=""
for f in $(grep -rl '<table' internal/handlers/templates --include='*.html' 2>/dev/null); do
  grep -q 'table-wrap' "$f" && continue          # hand-wrapped: the existing scroller
  grep -q 'class="card' "$f" && continue         # inside a card: covered by the CSS scroller
  UNCOVERED="$UNCOVERED $(basename "$f")"
done
if [ -z "$UNCOVERED" ]; then
  ok "B1: every unwrapped table lives in a card (or is hand-wrapped), so the card scroller reaches it"
else
  bad "B1: template(s) with an unwrapped table and NO card (nothing scrolls them):$UNCOVERED"
fi
NW=$(grep -rl 'table-wrap' internal/handlers/templates --include='*.html' 2>/dev/null | wc -l | tr -d ' ')
if [ "${NW:-0}" -ge 5 ]; then
  ok "B2: the hand-wrapped path is still used ($NW templates use .table-wrap)"
else
  bad "B2: .table-wrap disappeared from the templates ($NW files) — the mobile scroller regressed"
fi

# B326.1 / B325.1 (ratchet): a <select> with no accessible label. The audit found 4 in
# admin/exit_nodes.html; a full pass over the panel's selects is the remaining accessibility
# work, so this freezes the current number: it may go DOWN as labels are added, never up.
#
# 2026-09-25 (B325.1): the sweep labelled every one of them, and this contract gained a
# correctness fix on the way. It used to look only at the SINGLE LINE the <select> starts on,
# so a select carrying its aria-label (or its id) on a continuation line was counted as
# unlabelled — `admin/devices.html` line 133 was the live example, and because the contract is
# a COUNT the false positive inflated the budget every other select was measured against. It
# now reads the whole TAG, however many lines it spans.
SELECT_UNLABELLED_BUDGET="${B326_SELECT_BUDGET:-0}"
UNLABELLED=0
UNLABELLED_DETAIL=""
for f in $(grep -rl '<select' internal/handlers/templates --include='*.html' 2>/dev/null); do
  OUT=$(awk '
    { L[NR] = $0; ALL = ALL "\n" $0 }
    END {
      for (i = 1; i <= NR; i++) {
        if (L[i] !~ /<select/) continue
        tag = ""
        for (j = i; j <= NR; j++) { tag = tag " " L[j]; if (L[j] ~ />/) break }
        if (tag ~ /aria-label/) continue
        if (tag ~ /<label/) continue
        id = ""
        if (match(tag, /id="[^"]+"/)) id = substr(tag, RSTART + 4, RLENGTH - 5)
        if (id != "" && ALL ~ ("for=\"" id "\"")) continue
        printf "%d\t%s\n", i, tag
      }
    }
  ' "$f")
  if [ -n "$OUT" ]; then
    UNLABELLED=$((UNLABELLED + $(printf '%s\n' "$OUT" | grep -c .)))
    UNLABELLED_DETAIL="$UNLABELLED_DETAIL$(printf '%s\n' "$OUT" | sed "s|^|       $f:|")"$'\n'
  fi
done
if [ "${UNLABELLED:-0}" -le "$SELECT_UNLABELLED_BUDGET" ]; then
  ok "B3: selects without an accessible label = $UNLABELLED (budget $SELECT_UNLABELLED_BUDGET; lower, never raise)"
else
  bad "B3: selects without an accessible label = $UNLABELLED, over the frozen budget $SELECT_UNLABELLED_BUDGET"
  printf '%s' "$UNLABELLED_DETAIL" | head -8 >&2
fi

# --- D: cheap syntax guard + git ------------------------------------------------------
OPEN=$(tr -cd '{' < "$CSS" | wc -c | tr -d ' ')
CLOSE=$(tr -cd '}' < "$CSS" | wc -c | tr -d ' ')
if [ "$OPEN" = "$CLOSE" ]; then
  ok "D1: the stylesheet is brace-balanced ($OPEN rules)"
else
  bad "D1: the stylesheet is NOT brace-balanced ($OPEN open vs $CLOSE close) — rules after the break are ignored"
fi
if [ -f static/css/themes.css ]; then
  if cmp -s "$CSS" static/css/themes.css; then
    ok "D2: the legacy copy matches the embedded stylesheet"
  else
    skip "D2: the repo-root static/css/themes.css copy differs (legacy; the embedded one is served)"
  fi
fi
if git ls-files --error-unmatch scripts/check_b326_responsive_panel.sh >/dev/null 2>&1; then
  ok "D3: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "D3: this script is NOT tracked by git"
fi

printf '\n\033[1mB326 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
