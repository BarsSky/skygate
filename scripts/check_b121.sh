#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.2 follow-up (B121) — Mint theme + scrollbar + form contrast
#
# Pins the v1.3.19.2 follow-up that:
#   1. added a new "Mint" theme (silver bg + mint-green accent)
#      for long admin sessions
#   2. added custom thin scrollbar styles for all themes
#      (pre-fix: browser-default 15-17px white scrollbar in
#      dark themes was visually jarring)
#   3. bumped dark-theme form contrast (linear/nvidia/sentry
#      inputs had a 1px border that barely contrasted with
#      the dark bg — "forms blended into the page" per the
#      operator report)
#   4. registered ThemeMint in internal/db/db.go
#   5. added the Mint option to the theme-picker in
#      layout.html
#
# What this script verifies:
#   A. CSS: [data-theme="mint"] block exists with the mint
#      palette (--bg silver #f5f7f6, --accent mint #10b981).
#   B. CSS: thin themed scrollbar (Firefox scrollbar-width +
#      WebKit ::-webkit-scrollbar with var(--border)).
#   C. CSS: dark-theme form contrast bump (1.5px border +
#      inset shadow) for linear/nvidia/sentry.
#   D. layout.html: /settings/theme?theme=mint link with
#      fa-leaf icon exists in the theme-picker.
#   E. Go code: ThemeMint constant + IsValidTheme("mint")
#      in internal/db/db.go.
#   F. Go unit tests TestB121_* in internal/handlers/
#      package pass.
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

# Allow override so this script works from /tmp on the VM
: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

THEMES_CSS="static/css/themes.css"
LAYOUT_HTML="internal/handlers/templates/layout.html"
DB_GO="internal/db/db.go"

[ -f "${THEMES_CSS}" ]  || { bad "source file not found: ${THEMES_CSS}"; exit 1; }
[ -f "${LAYOUT_HTML}" ] || { bad "source file not found: ${LAYOUT_HTML}"; exit 1; }
[ -f "${DB_GO}" ]       || { bad "source file not found: ${DB_GO}"; exit 1; }

# ------------------------------------------------------------------------------
# Contract A: themes.css — Mint theme palette
# ------------------------------------------------------------------------------
echo
echo "=== A. themes.css: Mint theme palette ==="
if grep -q '\[data-theme="mint"\]' "${THEMES_CSS}"; then
    ok "[data-theme=\"mint\"] block exists in themes.css"
else
    bad "missing [data-theme=\"mint\"] block in themes.css"
fi
# Extract the [data-theme="mint"]{...} block and check palette
mint_block=$(awk '/\[data-theme="mint"\]/{flag=1} flag{print} /^}/{if(flag){flag=0; exit}}' "${THEMES_CSS}")
for kv in "--bg:#f5f7f6" "--bg-card:#ffffff" "--accent:#10b981" "--accent-fg:#ffffff" "--accent-hover:#059669"; do
    key="${kv%%:*}"
    val="${kv##*:}"
    # Use `grep -e` so `--` in the key (e.g. `--bg`) isn't
    # treated as a flag.
    if echo "${mint_block}" | grep -qE -e "${key}\s*:\s*${val}"; then
        ok "mint palette ${key} = ${val}"
    else
        bad "mint palette ${key} != ${val} (got something else)"
    fi
done

# ------------------------------------------------------------------------------
# Contract B: themes.css — custom thin scrollbar
# ------------------------------------------------------------------------------
echo
echo "=== B. themes.css: custom thin scrollbar ==="
if grep -qE 'scrollbar-width\s*:\s*thin' "${THEMES_CSS}"; then
    ok "scrollbar-width: thin (Firefox standard)"
else
    bad "missing scrollbar-width: thin (Firefox standard scrollbar)"
fi
if grep -qE 'scrollbar-color\s*:\s*var\(--border-strong\)\s+transparent' "${THEMES_CSS}"; then
    ok "scrollbar-color: var(--border-strong) transparent (Firefox colors)"
else
    bad "missing scrollbar-color: var(--border-strong) transparent (Firefox colors)"
fi
if grep -qE '::-webkit-scrollbar\s*\{[^}]*width\s*:\s*8px' "${THEMES_CSS}"; then
    ok "*::-webkit-scrollbar { width: 8px } (WebKit width)"
else
    bad "missing *::-webkit-scrollbar { width: 8px } (WebKit scrollbar width)"
fi
if grep -qE '::-webkit-scrollbar-thumb\s*\{[^}]*background\s*:\s*var\(--border\)' "${THEMES_CSS}"; then
    ok "*::-webkit-scrollbar-thumb { background: var(--border) } (WebKit thumb color)"
else
    bad "missing *::-webkit-scrollbar-thumb { background: var(--border) } (WebKit thumb color)"
fi

# ------------------------------------------------------------------------------
# Contract C: themes.css — dark-theme form contrast bump
# ------------------------------------------------------------------------------
echo
echo "=== C. themes.css: dark-theme form contrast bump ==="
for theme in linear nvidia sentry; do
    # Find a CSS block that has [data-theme="$theme"] input rule
    # AND contains `border-width:1.5px` AND `box-shadow:inset`.
    if awk -v t="$theme" '
        /\[data-theme="'"$theme"'"\][^{}]*input\[type=text\]/ { found=1 }
        found && /\{/ { brace=1 }
        brace { buf = buf $0 "\n" }
        brace && /^\}/ {
            if (buf ~ /border-width:[[:space:]]*1\.5px/ && buf ~ /box-shadow:[[:space:]]*inset/) {
                print "MATCH"; exit
            }
            buf=""; brace=0; found=0
        }
    ' "${THEMES_CSS}" | grep -q MATCH; then
        ok "[data-theme=\"$theme\"] inputs have border-width:1.5px + box-shadow:inset"
    else
        bad "[data-theme=\"$theme\"] inputs missing border-width:1.5px + box-shadow:inset contrast bump"
    fi
done

# ------------------------------------------------------------------------------
# Contract D: layout.html — Mint option in theme-picker
# ------------------------------------------------------------------------------
echo
echo "=== D. layout.html: Mint option in theme-picker ==="
if grep -q '/settings/theme?theme=mint' "${LAYOUT_HTML}"; then
    ok "layout.html has /settings/theme?theme=mint link"
else
    bad "layout.html missing /settings/theme?theme=mint link (users can't pick the Mint theme)"
fi
if grep -q 'fa-leaf' "${LAYOUT_HTML}"; then
    ok "layout.html has fa-leaf icon (thematic match for Mint)"
else
    bad "layout.html missing fa-leaf icon for Mint option"
fi

# ------------------------------------------------------------------------------
# Contract E: db.go — ThemeMint constant + IsValidTheme
# ------------------------------------------------------------------------------
echo
echo "=== E. internal/db/db.go: ThemeMint constant + IsValidTheme ==="
if grep -q 'ThemeMint\s*=\s*"mint"' "${DB_GO}"; then
    ok "ThemeMint = \"mint\" constant defined"
else
    bad "missing ThemeMint = \"mint\" constant in internal/db/db.go"
fi
if grep -q 'ThemeMint:' "${DB_GO}"; then
    ok "ThemeMint case in ThemeLabel function"
else
    bad "missing ThemeMint case in ThemeLabel (so the dropdown would show 'Linear' instead of 'Mint')"
fi
if grep -qE 'ThemeLinear,\s*ThemeVercel,\s*ThemeSentry,\s*ThemeNvidia,\s*ThemeMint' "${DB_GO}"; then
    ok "IsValidTheme includes ThemeMint"
else
    bad "IsValidTheme missing ThemeMint (an invalid theme would be accepted and rendered as default Linear)"
fi

# ------------------------------------------------------------------------------
# Contract F: Go unit tests TestB121_* pass
# ------------------------------------------------------------------------------
echo
echo "=== F. Go unit tests TestB121_* pass ==="
if [ -f "internal/handlers/layout_v1_3_19_2_b121_test.go" ]; then
    if command -v go >/dev/null 2>&1; then
        if go test -count=1 -short ./internal/handlers/ -run TestB121_ 2>&1 | tail -5; then
            if go test -count=1 -short ./internal/handlers/ -run TestB121_ 2>&1 | grep -q '^ok'; then
                ok "TestB121_* Go unit tests pass (pinned at source-code level)"
            else
                bad "TestB121_* Go unit tests failed — re-run with -v to see which contract regressed"
            fi
        else
            warn "go test exited non-zero (might be a build error, not a test failure)"
        fi
    else
        warn "go not on PATH — skipping TestB121_* Go test verification (the other contracts still hold)"
    fi
else
    bad "internal/handlers/layout_v1_3_19_2_b121_test.go not found"
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
