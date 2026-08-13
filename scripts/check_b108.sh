#!/usr/bin/env bash
# B108: v1.3.9 round 6 — section summary in collapsed sidebar auto-expands.
# Pins the JS contract in internal/handlers/templates/layout.html:
#   1. <script> tag exists after </footer> and before </body>
#   2. Script queries '#sidebar' (the hard-coded id from layout.html:38)
#   3. Script iterates '.sidebar-section>summary' (the 6 collapsible sections)
#   4. On click, removes 'collapsed' class from sidebar (so the drawer
#      expands to 220px and the pages inside the just-opened <details>
#      become visible — they were hidden by
#      `.sidebar.collapsed .sidebar-section[open]>a{display:none}`)
#   5. Does NOT call preventDefault() — the native <details> toggle
#      must still happen so the section opens.
#
# Operator-reported symptom (2026-08-13): "не работают по нажатию кнопки
# групп для того чтобы раскрыть меню и выбрать страницу из списка" — the
# group button click in collapsed sidebar does nothing visible. Root cause
# was that the click toggles <details> but the page links are hidden by
# the collapsed-sidebar rule, so the user can't navigate. Fix: auto-expand
# the sidebar on section summary click.
#
# Exit 0 = PASS, exit 1 = FAIL.

set -u

LAYOUT="internal/handlers/templates/layout.html"

if [ ! -f "$LAYOUT" ]; then
  echo "B108 FAIL: $LAYOUT not found"
  exit 1
fi

# 1. <script> tag must exist between </footer> and </body>.
#    Use awk to extract the region (handles multi-line Go template
#    comments between </footer> and </body>), then grep for <script>.
REGION=$(awk '
  /<\/footer>/ { capture=1; next }
  /<\/body>/ { if (capture) { print buf; capture=0; buf="" } }
  capture { buf = buf $0 "\n" }
' "$LAYOUT")
if [ -z "$REGION" ]; then
  echo "B108 FAIL: could not extract </footer>...</body> region"
  exit 1
fi
if ! echo "$REGION" | grep -q '<script>'; then
  echo "B108 FAIL: no <script> tag between </footer> and </body>"
  exit 1
fi
if ! echo "$REGION" | grep -q '</script>'; then
  echo "B108 FAIL: no </script> closing tag between </footer> and </body>"
  exit 1
fi

# 2. Script queries '#sidebar' — the hard-coded id from layout.html:38
if ! grep -q "getElementById('sidebar')" "$LAYOUT"; then
  echo "B108 FAIL: script does not query '#sidebar'"
  exit 1
fi

# 3. Script iterates '.sidebar-section>summary' — the 6 collapsible sections
if ! grep -q "'.sidebar-section>summary'" "$LAYOUT"; then
  echo "B108 FAIL: script does not query '.sidebar-section>summary'"
  exit 1
fi

# 4. On click, removes 'collapsed' class — the auto-expand behavior
if ! grep -q "sidebar.classList.remove('collapsed')" "$LAYOUT"; then
  echo "B108 FAIL: script does not remove 'collapsed' class on click"
  exit 1
fi

# 5. Must NOT call preventDefault() — the native <details> toggle
#    must still happen so the section opens after the sidebar expands.
#    Search only within the script block (between <script> and </script>
#    that lives between </footer> and </body>) to avoid false positives
#    from Go template comments that mention the word.
SCRIPT_BLOCK=$(echo "$REGION" | awk '/<script>/{p=1;next} /<\/script>/{p=0} p')
if echo "$SCRIPT_BLOCK" | grep -q 'preventDefault'; then
  echo "B108 FAIL: script calls preventDefault() — would block native <details> toggle"
  exit 1
fi

echo "B108 PASS: section summary in collapsed sidebar auto-expands + opens section"
exit 0
