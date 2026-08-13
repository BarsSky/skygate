#!/bin/bash
# scripts/check_b106.sh — invoked by verify_pre_deploy.sh B106 check.
#
# Background
# ----------
# v1.3.9 (2026-08-13): the in-sidebar .toggle button (line 41 in
# layout.html) was visible on mobile. It toggles the `.collapsed`
# class on the sidebar, which on desktop collapses 220px → 52px
# and pushes the main content's margin-left. On mobile the
# mobile @media block forces `width:280px !important` (the drawer
# pattern — sidebar is overlay, not a column) and the
# `width:280px !important` wins over `.sidebar.collapsed{width:52px}`,
# so clicking the .toggle on mobile does NOTHING visible. The
# operator-reported symptom: the sidebar drawer covers the main
# content with no way to make it "smaller" on mobile, and the
# `.collapsed` class gets stuck in the DOM (no auto-reset on
# viewport resize), breaking future desktop sessions.
#
# The fix:
#   1. `.sidebar .toggle{display:none}` on mobile — hide the
#      in-sidebar button. The hamburger at top:12px,left:12px
#      is the only mobile control.
#   2. `.sidebar.collapsed{width:280px}` on mobile — force-clear
#      the stuck state so even if the class persists, the
#      sidebar is at full drawer width.
#
# Pinned contracts:
#   - The `display:none` rule is INSIDE @media (max-width:768px)
#     so it only applies on mobile (desktop keeps the button
#     visible).
#   - The `display:none` rule is AFTER the earlier
#     `.sidebar .toggle{width:44px;height:44px}` rule (the v1.1.0
#     touch-target rule). Both have specificity 0,0,2,0; the
#     later rule wins the cascade. If a future refactor moves
#     the `display:none` before the touch-target rule, the
#     toggle button becomes visible on mobile again.
#   - The `.sidebar.collapsed{width:280px}` force-clear rule
#     is also inside the @media block, so it only applies on
#     mobile. On desktop the original `.sidebar.collapsed{width:52px}`
#     still works (the .collapsed state means "compact sidebar").

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# 1. The .toggle button still exists in layout.html (the desktop
#    control). We don't remove it from the template — we just
#    hide it via CSS on mobile. Removing it would break desktop.
grep -qF 'class="toggle"' internal/handlers/templates/layout.html \
    || { echo "SKY-FAIL: layout.html missing .toggle button (the desktop control)" >&2; exit 1; }

# 2. The mobile @media block contains a .sidebar .toggle{display:none}
#    rule. We use awk to extract the block first, then grep inside
#    the block. The awk brace-counting handles nested @media (CSS
#    doesn't have any here, but defensive).
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
' static/css/themes.css)
if [ -z "$MEDIA_BLOCK" ]; then
  echo "SKY-FAIL: could not extract @media (max-width:768px) block from themes.css" >&2
  exit 1
fi

# 3. The .toggle display:none rule is inside the @media block.
if ! echo "$MEDIA_BLOCK" | grep -qE '\.sidebar[[:space:]]+\.toggle[[:space:]]*\{[^}]*display:[[:space:]]*none'; then
  echo "SKY-FAIL: @media (max-width:768px) block missing .sidebar .toggle{display:none} (the in-sidebar button is still visible on mobile)" >&2
  exit 1
fi

# 4. The display:none rule comes AFTER the .sidebar .toggle{width:44px;height:44px}
#    touch-target rule. Both have specificity 0,0,2,0; the later
#    rule wins the cascade. We extract the LINE NUMBERS of both
#    rules inside the @media block and verify the display:none
#    rule has a HIGHER line number.
TOUCH_LINE=$(echo "$MEDIA_BLOCK" | grep -nE '\.sidebar[[:space:]]+\.toggle[[:space:]]*\{[^}]*width:[[:space:]]*44px' | head -1 | cut -d: -f1)
HIDE_LINE=$(echo "$MEDIA_BLOCK" | grep -nE '\.sidebar[[:space:]]+\.toggle[[:space:]]*\{[^}]*display:[[:space:]]*none' | head -1 | cut -d: -f1)
if [ -z "$TOUCH_LINE" ] || [ -z "$HIDE_LINE" ]; then
  echo "SKY-FAIL: could not find both .sidebar .toggle rules in the @media block" >&2
  exit 1
fi
if [ "$HIDE_LINE" -le "$TOUCH_LINE" ]; then
  echo "SKY-FAIL: .sidebar .toggle{display:none} (line $HIDE_LINE) is BEFORE the touch-target rule (line $TOUCH_LINE) — cascade makes the .toggle button VISIBLE on mobile" >&2
  exit 1
fi

# 5. The .sidebar.collapsed{width:280px} force-clear is also
#    inside the @media block.
if ! echo "$MEDIA_BLOCK" | grep -qE '\.sidebar\.collapsed\{width:280px\}|\.sidebar\.collapsed[[:space:]]*\{[[:space:]]*width:[[:space:]]*280px'; then
  echo "SKY-FAIL: @media (max-width:768px) block missing .sidebar.collapsed{width:280px} (stuck collapsed state would not be auto-cleared on mobile)" >&2
  exit 1
fi

echo "B106 check passed: .sidebar .toggle hidden on mobile + .collapsed state force-cleared (drawer pattern works as designed)"
