#!/usr/bin/env bash
# check_b418_mobile_drawer.sh
#
# 2026-10-25 (B418) — THE MOBILE DRAWER MUST BE THE VISIBLE VIEWPORT, AND IT MUST OWN THE
# FOOTER IT COVERS.
#
# Operator, with a phone screenshot of /admin/update:
#   «на экране смартфона меню в раскрытом состоянии перекрывает футтер, и также на
#    смартфоне кнопки в меню в самом низу (смена темы языка выход из системы) появляются
#    только если пролистать страницу в самый низ»
#
# MEASURED, two causes inside the mobile `@media (max-width:768px)` block:
#   1. the drawer kept `height:100vh`. On a phone `100vh` is the LARGE viewport (browser
#      UI hidden), so `.sidebar-user` — theme / language / logout — was painted BELOW the
#      visible edge; the drawer is position:fixed, so scrolling the page could not bring
#      it up until the URL bar collapsed (which is exactly the operator's "only when I
#      scroll to the very bottom").
#   2. the dim layer was `main::before`, a pseudo-element of <main>, while <footer> is a
#      SIBLING of <main>: the footer stayed under the finger and a tap on the dim area
#      could not close the drawer.
#
# FIX: the drawer is anchored with top:0;bottom:0;height:auto (+ max-height:100dvh), the
# nav is the only scroller (flex:1 + min-height:0), the bottom block keeps flex-shrink:0
# and gets safe-area padding, the dim layer is a REAL `<label for="sidebar-toggle"
# class="sidebar-backdrop">` that closes the drawer and covers the footer, and the page
# behind cannot scroll while the drawer is open (`body:has(…)`).
# THE DRAWER MUST NOT CLIP (`overflow:hidden` is a REJECTION, contract A4): the bell,
# theme and language popovers are absolutely-positioned children of `.sidebar-actions`
# and hang past the 280px edge — B392 measured the cut at ~38 % of the panel. Containment
# comes from the flex chain (B1-B3), not from a clip box.
#
# CONTRACTS (20)
#   A  the drawer is the visible viewport: anchored, dvh-capped, must NOT clip (B392), safe-area
#   B  the bottom block cannot be pushed out: nav is the only scroller + shrink-proof block
#   C  the backdrop owns the footer: real label in layout.html, checkbox-driven, old
#      `main::before` overlay gone, page scroll locked
#   D  both stylesheet copies agree (B392/B411 rule) and the Go pins are real
#   E  registered in the catalog, indexed in AGENTS.md, tracked by git

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b418.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

CSS=internal/staticfs/static/css/themes.css
MIRROR=static/css/themes.css
LAYOUT=internal/handlers/templates/layout.html
GO_TEST=internal/handlers/mobile_drawer_b418_test.go
GATE=scripts/verify_pre_deploy.sh

for f in "$CSS" "$MIRROR" "$LAYOUT" "$GO_TEST" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# The mobile block, extracted once (same shape the Go test uses).
awk '/@media \(max-width:768px\)\{/{f=1} f{print} f && /\.sidebar nav a\{padding:12px 14px/{exit}' "$CSS" >"$SKY_TMP/mobile.css"
if [ -s "$SKY_TMP/mobile.css" ]; then
  ok "A0: the mobile breakpoint block exists ($(wc -l <"$SKY_TMP/mobile.css" | tr -d ' ') lines)"
else
  bad "A0: could not extract the @media (max-width:768px) block"
fi

# The `.sidebar{…}` rule on its own. The mobile block also carries
# `body:has(…){overflow:hidden}`, so a substring search over the whole block cannot tell
# "the drawer clips its popovers away" from "the page behind the drawer is locked".
# Comments are stripped first: the rule's own banner NAMES the declaration it must not
# carry, and a contract that reads prose is a contract that fails on its own explanation
# (the B250 lesson — a check must look at the declaration, not at the sentence about it).
awk '!f && /^[[:space:]]*[.]sidebar[{]/{f=1} f{print} f && /^[[:space:]]*[}][[:space:]]*$/{exit}' \
  "$SKY_TMP/mobile.css" |
  awk 'BEGIN{d=0}
       { s=$0; out=""
         while (1) {
           if (d) { p=index(s,"*/"); if (p==0) { s=""; break } s=substr(s,p+2); d=0 }
           else   { p=index(s,"/*"); if (p==0) { out=out s; break }
                    out=out substr(s,1,p-1); s=substr(s,p+2); d=1 }
         }
         print out }' >"$SKY_TMP/drawer.css"

# --- A: the drawer is the visible viewport ---------------------------------
hdr "A. the drawer must be the VISIBLE viewport"
if grep -qF 'top:0;bottom:0;height:auto' "$SKY_TMP/mobile.css"; then
  ok "A1: the drawer is anchored with top:0;bottom:0;height:auto (not sized by 100vh)"
else
  bad "A1: the drawer is not viewport-anchored — 100vh on a phone is the LARGE viewport, so its bottom block is off-screen"
fi
if grep -qF 'max-height:100dvh' "$SKY_TMP/mobile.css"; then
  ok "A2: the drawer caps itself at 100dvh (dynamic viewport where supported)"
else
  bad "A2: no 100dvh cap"
fi
if grep -qF 'padding-bottom:env(safe-area-inset-bottom' "$SKY_TMP/mobile.css"; then
  ok "A3: safe-area padding keeps the bottom buttons off the home indicator"
else
  bad "A3: no safe-area padding"
fi
if [ ! -s "$SKY_TMP/drawer.css" ]; then
  bad "A4: could not extract the mobile .sidebar{...} rule"
elif grep -qF 'overflow:hidden' "$SKY_TMP/drawer.css"; then
  bad "A4: the drawer clips with overflow:hidden — the bell/theme/language popovers hang past its 280px edge and lose their right part (B392 measured the cut at ~38 %)"
else
  ok "A4: the drawer does not clip (B392: its popovers must stay whole); containment comes from B1-B3, not from a clip"
fi

# --- B: the bottom block cannot be pushed out ------------------------------
hdr "B. theme / language / logout must always be visible"
if grep -qF '.sidebar nav{min-height:0}' "$SKY_TMP/mobile.css"; then
  ok "B1: the nav may shrink (min-height:0), so the bottom block is never pushed out"
else
  bad "B1: `.sidebar nav{min-height:0}` is missing — a flex item refuses to shrink below its content"
fi
if grep -qF 'flex:1;overflow-y:auto' "$CSS"; then
  ok "B2: the nav is still the drawer's only scroller (flex:1 + overflow-y:auto)"
else
  bad "B2: the nav is no longer the scroller"
fi
if grep -qF 'flex-shrink:0' "$CSS" && grep -q 'sidebar-user{[^}]*flex-shrink:0' "$CSS"; then
  ok "B3: the user block (theme / language / logout) is shrink-proof"
else
  bad "B3: `.sidebar-user` lost flex-shrink:0"
fi
if OUT="$(go test ./internal/handlers/ -run 'B418' -count=1 2>&1)"; then
  ok "B4: the Go pins pass (drawer viewport, bottom block, backdrop, stacking)"
else
  bad "B4: go test -run B418 failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

# --- C: the backdrop owns the footer ---------------------------------------
hdr "C. the dim layer must close the drawer AND cover the footer"
if grep -qF '.sidebar-backdrop{' "$SKY_TMP/mobile.css"; then
  ok "C1: the backdrop is a real element's rule (not a pseudo-element of <main>)"
else
  bad "C1: no .sidebar-backdrop rule"
fi
if grep -qF '.sidebar-toggle-input:checked ~ .sidebar-backdrop{display:block}' "$SKY_TMP/mobile.css"; then
  ok "C2: the backdrop is driven by the same checkbox as the drawer"
else
  bad "C2: the backdrop is not tied to the drawer's checkbox"
fi
if grep -q 'sidebar-toggle-input:checked ~ main::before' "$SKY_TMP/mobile.css"; then
  bad "C3: the old main::before overlay is back — it cannot close the drawer and leaves the footer reachable"
else
  ok "C3: the pseudo-element overlay is gone (the footer is covered by the backdrop)"
fi
if grep -qF 'body:has(.sidebar-toggle-input:checked){overflow:hidden}' "$SKY_TMP/mobile.css"; then
  ok "C4: the page behind the drawer cannot scroll (that scroll used to reveal the drawer's own bottom block)"
else
  bad "C4: no scroll lock while the drawer is open"
fi
if grep -qF '<label for="sidebar-toggle" class="sidebar-backdrop"' "$LAYOUT"; then
  ok "C5: layout.html ships the backdrop label (tap outside closes the drawer)"
else
  bad "C5: layout.html has no backdrop label"
fi
# The label must sit OUTSIDE <main>: the footer is a sibling of main, and the backdrop is
# what covers it. A label nested inside main could not be a sibling of the checkbox — and
# the `~` selector needs the SAME PARENT, so the anchor `^<label` (column 0, the level of
# `</aside>` and `<main>`) is part of the contract, not cosmetics: an indented label is a
# backdrop that never displays.
if [ "$(grep -n '^<label for="sidebar-toggle" class="sidebar-backdrop"' "$LAYOUT" | head -1 | cut -d: -f1)" -lt "$(grep -n '^<main>' "$LAYOUT" | head -1 | cut -d: -f1)" ]; then
  ok "C6: the backdrop label is a top-level sibling of <main>, before it (so \`~\` can reach it)"
else
  bad "C6: the backdrop label is nested inside a container or after <main> — the sibling selector cannot reach it"
fi

# --- D: both stylesheet copies agree ---------------------------------------
hdr "D. the two stylesheet copies agree"
if cmp -s "$CSS" "$MIRROR"; then
  ok "D1: $CSS and $MIRROR are byte-identical (B392/B411 rule)"
else
  bad "D1: the served stylesheet and the one the Go tests read differ"
fi
if grep -q 'func TestB418_' "$GO_TEST"; then
  ok "D2: the Go pins live in $GO_TEST"
else
  bad "D2: no TestB418_* in the Go test file"
fi

# --- E: registration --------------------------------------------------------
hdr "E. registration"
if grep -q 'run_check "B418"' "$GATE"; then
  ok "E1: B418 is registered in the catalog"
else
  bad "E1: B418 is not registered — this contract would never run"
fi
if grep -q '\*\*B418\*\*' AGENTS.md; then
  ok "E2: AGENTS.md block index carries B418"
else
  bad "E2: AGENTS.md has no B418 entry"
fi
if git ls-files --error-unmatch scripts/check_b418_mobile_drawer.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap 11)"
else
  bad "E3: this script is not tracked by git"
fi

printf '\n\033[1mB418 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
