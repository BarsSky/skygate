#!/usr/bin/env bash
# B109: v1.3.9 round 7 — desktop breadcrumb padding-left bumped
# from 24px to 40px. The breadcrumb nav (renders "Админ › section
# › page" path on every admin page) lives on its own line with
# its own bg-card background + bottom border, separate from the
# .shell content. The 24px "standard" padding that header .shell
# uses looked visually too tight against the 220px sidebar.
# Operator-reported symptom (2026-08-13): "в новых страницах групп
# не учитывает смещение от меню" — the breadcrumb element doesn't
# account for the sidebar offset, it sits too close to the menu.
# Fix: bump padding-left from 24px to 40px so the breadcrumb
# text has visible breathing room from the 220px sidebar edge.
#
# Pins 3 contracts:
#   1. .admin-breadcrumb has padding-left:40px in the MAIN CSS
#      (outside @media — desktop-only, mobile keeps its own
#      padding-left:60px from B107 to clear the hamburger).
#   2. The padding shorthand is `padding:10px 24px 10px 40px`
#      (top, right, bottom, left) — keeps the original 10px
#      vertical and 24px right padding intact, only the LEFT
#      padding changes.
#   3. The @media (max-width:768px) B107 rule still pins
#      padding-left:60px on mobile (this check verifies the
#      mobile rule wasn't accidentally removed).
#
# Exit 0 = PASS, exit 1 = FAIL.

set -u

CSS="static/css/themes.css"

if [ ! -f "$CSS" ]; then
  echo "B109 FAIL: $CSS not found"
  exit 1
fi

# 1 + 2. Main CSS (outside @media) has the breadcrumb with
#    padding-left:40px and the 4-value shorthand padding:10px 24px 10px 40px
#    The rule block may contain comments between { and padding, so we
#    need multiline matching. Use perl -0777 to slurp the whole file
#    and match across newlines.
if ! perl -0777 -ne 'exit !(/\.admin-breadcrumb\s*\{[^}]*padding:\s*10px\s+24px\s+10px\s+40px/s)' "$CSS"; then
  echo "B109 FAIL: .admin-breadcrumb missing padding:10px 24px 10px 40px (40px left padding for desktop breathing room from 220px sidebar)" >&2
  exit 1
fi

# 3. @media (max-width:768px) still has padding-left:60px (B107 not broken)
MEDIA_BLOCK=$(awk '
  /@media \(max-width:768px\)/ { capture=1; depth=0 }
  capture {
    n = gsub(/\{/, "{")
    depth += n
    n = gsub(/\}/, "}")
    depth -= n
    block = block $0 "\n"
    if (depth == 0) { print block; capture=0; block="" }
  }
' "$CSS")
if [ -z "$MEDIA_BLOCK" ]; then
  echo "B109 FAIL: could not extract @media (max-width:768px) block" >&2
  exit 1
fi
if ! echo "$MEDIA_BLOCK" | grep -qE '\.admin-breadcrumb\{padding-left:60px\}|\.admin-breadcrumb[[:space:]]*\{[[:space:]]*padding-left:[[:space:]]*60px'; then
  echo "B109 FAIL: @media (max-width:768px) missing .admin-breadcrumb{padding-left:60px} (B107 mobile hamburger-clearance broken)" >&2
  exit 1
fi

echo "B109 PASS: desktop breadcrumb padding-left 40px (breathing room from 220px sidebar) + B107 mobile 60px preserved"
exit 0
