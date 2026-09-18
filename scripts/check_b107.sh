#!/bin/bash
# scripts/check_b107.sh — invoked by verify_pre_deploy.sh B107 check.
#
# Background
# ----------
# v1.3.9 (2026-08-13): two follow-up CSS fixes for the
# admin sidebar on mobile / collapsed view:
#
#   1. The .admin-breadcrumb nav (renders "Админ › section ›
#      page" path on every admin page) is at the top of the
#      main area, BELOW the header. The 2026-08-13 v1.3.9
#      B105 fix added padding-left:60px to .title-row to
#      clear the mobile hamburger, but the breadcrumb is a
#      SIBLING nav, not a child of .title-row — it didn't
#      get the same offset. The operator-reported symptom:
#      "Админ" is half-hidden behind the hamburger on
#      /admin/devices. Fix: add padding-left:60px to
#      .admin-breadcrumb inside @media (max-width:768px).
#
#   2. When the sidebar is collapsed (52px wide on desktop,
#      the in-sidebar .toggle button shrinks the sidebar),
#      the section summary keeps its default 8px 10px
#      padding + 10px gap + 10px caret (::before) + 16px
#      icon (i) = 56px content. With the sidebar at 52px,
#      this overflows by 4px and triggers a horizontal
#      scroll bar inside the sidebar. The operator-reported
#      symptom: "когда меню скрыто иконки групп не
#      переходят в скрытый режим и появляется снизу
#      горизонтальный скролл". Fix: drop padding/gap on
#      collapsed, hide the caret, center the icon. The
#      summary becomes a single 16px icon that fits in 52px.
#
# Pinned contracts:
#   - .admin-breadcrumb{padding-left:60px} is INSIDE
#     @media (max-width:768px) (the rule is mobile-only;
#     desktop keeps the default 24px padding).
#   - .sidebar.collapsed .sidebar-section>summary has
#     padding:0;gap:0;justify-content:center in the main
#     CSS (not @media — collapsed can happen on any viewport
#     via the in-sidebar .toggle button).
#   - .sidebar.collapsed .sidebar-section>summary::before
#     has display:none (hides the caret on collapsed).
#   - The .sidebar.collapsed rules are AFTER the base
#     .sidebar-section>summary rules in the source file
#     (same specificity, later wins).

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# 1. .admin-breadcrumb{padding-left:60px} is inside @media (max-width:768px).
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
if ! echo "$MEDIA_BLOCK" | grep -qE '\.admin-breadcrumb\{padding-left:60px\}|\.admin-breadcrumb[[:space:]]*\{[[:space:]]*padding-left:[[:space:]]*60px'; then
  echo "SKY-FAIL: @media (max-width:768px) block missing .admin-breadcrumb{padding-left:60px} (breadcrumb is hidden behind the hamburger on mobile)" >&2
  exit 1
fi

# 2. .sidebar.collapsed .sidebar-section>summary has padding:0;gap:0;justify-content:center
#    in the MAIN CSS (outside @media). This is for desktop's
#    collapsed state (user clicks .toggle in the sidebar header).
if ! grep -qE '\.sidebar\.collapsed[[:space:]]+\.sidebar-section>summary\{[^}]*padding:[[:space:]]*0' static/css/themes.css; then
  echo "SKY-FAIL: .sidebar.collapsed .sidebar-section>summary missing padding:0 (icons overflow 52px sidebar)" >&2
  exit 1
fi
if ! grep -qE '\.sidebar\.collapsed[[:space:]]+\.sidebar-section>summary\{[^}]*justify-content:[[:space:]]*center' static/css/themes.css; then
  echo "SKY-FAIL: .sidebar.collapsed .sidebar-section>summary missing justify-content:center (icon should be centered in 52px sidebar)" >&2
  exit 1
fi
# 2b. The caret rule must be the display:none one (not the
#     pre-existing transform:rotate(0deg) rule). We use a
#     multi-line grep with a permissive regex that matches
#     both rules (rotate and display:none), then verify at
#     least one contains display:none. The awk approach is
#     more reliable on small files but error-prone; we use
#     a simpler pcregrep-style trick: read the lines and
#     look for the pattern.
if ! perl -0777 -ne 'exit !(/(\.sidebar\.collapsed\s+\.sidebar-section>summary::before\s*\{[^}]*display:\s*none)/s)' static/css/themes.css; then
  echo "SKY-FAIL: .sidebar.collapsed .sidebar-section>summary::before missing display:none (caret still visible in collapsed mode, adds 10px width)" >&2
  exit 1
fi

echo "B107 check passed: .admin-breadcrumb cleared on mobile (60px) + .sidebar-section summary fits 52px collapsed (no caret, no padding, centered icon)"
