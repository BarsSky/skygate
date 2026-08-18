#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20.1 (B131) — Linear theme contrast bump
#
# Pins the v1.3.20.1 CSS fix that makes the Linear theme
# (the default :root block in static/css/themes.css) a
# comfortable dark theme. The pre-B131 Linear had a
# "barely visible" issue: --bg=#0a0a0a + --bg-card=#131313
# (delta only 9/255 ≈ 3.5%) made cards blend into the bg,
# the sticky header was identical-color to the body so
# the header band was invisible, and the 0.10-opacity
# alert backgrounds (--info-bg, --success-bg) rendered
# as a barely-visible tint.
#
# Post-B131: --bg lifted to #0e0f12, --bg-card to #1a1c21
# (delta ~5%, matches Mint's comfortable contrast), --border
# bumped to #383b42 (visible definition), --header-bg
# distinct from --bg, and alert opacities raised from 0.10
# → 0.15 so the auto-update banner + info/success alerts
# read clearly. Pure CSS change — no template/HTML/i18n.
#
# What this script verifies (live, on the VM):
#   A. :root block in themes.css has the post-B131 Linear
#      colors (--bg #0e0f12, --bg-card #1a1c21, --bg-elev
#      #25282e, --border #383b42, --header-bg #15171c)
#   B. Alert background opacities are at 0.15 (not 0.10)
#      for --info-bg / --success-bg / --warning-bg / --danger-bg
#   C. --bg-card is at least 5% lighter than --bg in sRGB
#      (the comfortable-contrast minimum; pre-B131 was 3.5%)
#   D. --header-bg is DIFFERENT from --bg (the sticky header
#      must be visually distinct from the body)
#   E. Input background uses var(--bg-elev), not the
#      pre-B131 hardcoded #1a1a1a (which would now be DARKER
#      than the surrounding card)
#   F. The Linear theme is still the default (no theme
#      rename; back-compat for existing users on Linear)
#   G. Other 4 themes (Vercel, Sentry, NVIDIA, Mint) still
#      have their own overrides (i.e. B131 only changed
#      :root, didn't break the other themes)
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

: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

THEMES="static/css/themes.css"
[ -f "${THEMES}" ] || { bad "source file not found: ${THEMES}"; exit 1; }

# ------------------------------------------------------------------------------
# Helper: extract a CSS variable value from the :root block.
# Reads from the FIRST occurrence (the :root block) to avoid
# matching a [data-theme="..."] override later in the file.
# ------------------------------------------------------------------------------
get_var() {
    local name="$1"
    # Look for "--name:" in the file; the FIRST match is in :root
    # (the :root block always appears before any [data-theme="..."] block).
    grep -oE "\\-\\-${name}:#[0-9a-fA-F]+" "${THEMES}" 2>/dev/null | head -1 | sed -E "s/^--${name}://; s/^#//"
}

# ------------------------------------------------------------------------------
# Contract A: post-B131 :root colors
# ------------------------------------------------------------------------------
echo
echo "=== A. :root has the post-B131 Linear colors (or stronger via B133) ==="
# 2026-08-18 (B133): the operator reported the B131 contrast
# bump wasn't enough. B133 bumps the bg→card delta from
# 5% to ~10% (bg #0c0d10 → card #23262e) + border at 18% lift
# (#555a64 vs the B131 #383b42). This contract test accepts
# EITHER the B131 values (bg #0e0f12, card #1a1c21) OR
# the B133 stronger values (bg #0c0d10, card #23262e).
# The sRGB contrast (Contract C below) is the authoritative
# check — if that's >= 5%, the contract holds.
a_bg=$(get_var bg)
a_card=$(get_var bg-card)
a_elev=$(get_var bg-elev)
a_border=$(get_var border)
a_header=$(get_var header-bg)
b131_ok=0
b133_ok=0
if [ "${a_bg}" = "0e0f12" ] && [ "${a_card}" = "1a1c21" ] && [ "${a_elev}" = "25282e" ] && [ "${a_border}" = "383b42" ] && [ "${a_header}" = "15171c" ]; then
    b131_ok=1
fi
if [ "${a_bg}" = "0c0d10" ] && [ "${a_card}" = "23262e" ] && [ "${a_elev}" = "2d3038" ] && [ "${a_border}" = "555a64" ] && [ "${a_header}" = "181a20" ]; then
    b133_ok=1
fi
if [ "${b131_ok}" -eq 1 ]; then
    ok ":root has the B131 Linear colors (bg #0e0f12, card #1a1c21, etc.)"
elif [ "${b133_ok}" -eq 1 ]; then
    ok ":root has the B133 stronger Linear colors (bg #0c0d10, card #23262e, etc.) — B131's bump wasn't enough, B133 escalates"
else
    bad ":root Linear colors are neither B131 nor B133 set: bg=${a_bg} card=${a_card} elev=${a_elev} border=${a_border} header=${a_header}"
fi

# ------------------------------------------------------------------------------
# Contract B: alert opacities at 0.15
# ------------------------------------------------------------------------------
echo
echo "=== B. alert background opacities are 0.15 or higher (was 0.10) ==="
# B133 bumped from 0.15 → 0.22. Contract: any value >= 0.15
# is acceptable. Pin the exact B131 value (0.15) OR the
# B133 value (0.22) — both pass.
b_info=$(grep -cE 'info-bg:rgba\(96,165,250,0\.(15|22)\)' "${THEMES}" || true)
b_success=$(grep -cE 'success-bg:rgba\(74,222,128,0\.(15|22)\)' "${THEMES}" || true)
b_warning=$(grep -cE 'warning-bg:rgba\(251,191,36,0\.(15|22)\)' "${THEMES}" || true)
b_danger=$(grep -cE 'danger-bg:rgba\(248,113,113,0\.(15|22)\)' "${THEMES}" || true)
b_accent=$(grep -cE 'accent-soft:rgba\(124,122,255,0\.(18|22)\)' "${THEMES}" || true)
if [ "${b_info}" -ge 1 ] && [ "${b_success}" -ge 1 ] && [ "${b_warning}" -ge 1 ] && [ "${b_danger}" -ge 1 ] && [ "${b_accent}" -ge 1 ]; then
    ok "All 5 alert/accent backgrounds >= 0.15 opacity (B131: 0.15/0.18, B133: 0.22)"
else
    bad "alert opacity bump incomplete: info=${b_info} success=${b_success} warning=${b_warning} danger=${b_danger} accent=${b_accent}"
fi

# ------------------------------------------------------------------------------
# Contract C: --bg-card is at least 5% lighter than --bg in sRGB
# (the pre-B131 value 9/255 ≈ 3.5% was below the comfortable
# threshold; B131 bumps it to 13/255 ≈ 5.1%)
# ------------------------------------------------------------------------------
echo
echo "=== C. --bg-card is >=5% lighter than --bg in sRGB ==="
# Use python for the sRGB luminance delta — bash integer
# arithmetic is fragile with hex colors.
c_delta=$(python3 -c "
bg = '${a_bg}'; card = '${a_card}'
def avg(h):
    return sum(int(h[i:i+2], 16) for i in (0, 2, 4)) / 3
delta = avg(card) - avg(bg)
pct = delta * 100 / 255
print(f'{pct:.1f}')
" 2>/dev/null || echo "0.0")
c_delta_int=${c_delta%.*}
if [ "${c_delta_int}" -ge 5 ]; then
    ok "bg→card contrast is ${c_delta_int}% (>= 5% minimum for comfortable dark theme)"
else
    bad "bg→card contrast is only ${c_delta_int}% (need >= 5% — pre-B131 was 3.5%)"
fi

# ------------------------------------------------------------------------------
# Contract D: --header-bg != --bg (sticky header must be
# visually distinct from the body)
# ------------------------------------------------------------------------------
echo
echo "=== D. --header-bg is distinct from --bg ==="
if [ "${a_header}" != "${a_bg}" ]; then
    ok "--header-bg (#${a_header}) is distinct from --bg (#${a_bg}) — sticky header has its own surface"
else
    bad "--header-bg is identical to --bg (#${a_header}) — sticky header is invisible"
fi

# ------------------------------------------------------------------------------
# Contract E: input background uses var(--bg-elev), not #1a1a1a
# ------------------------------------------------------------------------------
echo
echo "=== E. input backgrounds use var(--bg-elev) (not pre-B131 hardcoded #1a1a1a) ==="
# Pre-B131 had two CSS rules with hardcoded #1a1a1a in the
# [data-theme="linear"] input blocks. Post-B131 both rules use
# var(--bg-elev). Confirm:
#   (a) the file has BOTH linear+input blocks using var(--bg-elev)
#   (b) the literal "background:#1a1a1a" is no longer present
#       inside any [data-theme="linear"] input selector
e_new=$(awk '/\[data-theme="linear"\] input/,/^}/' "${THEMES}" | grep -c 'background:var(--bg-elev)' || true)
e_old=$(awk '/\[data-theme="linear"\] input/,/^}/' "${THEMES}" | grep -c 'background:#1a1a1a' || true)
if [ "${e_new}" -ge 1 ] && [ "${e_old}" -eq 0 ]; then
    ok "linear input backgrounds use var(--bg-elev) (B131) — pre-B131 hardcoded #1a1a1a is gone"
else
    bad "input background rule is wrong: new_var_pattern=${e_new} old_hardcoded_count=${e_old}"
fi

# ------------------------------------------------------------------------------
# Contract F: Linear is still the default (no theme rename)
# ------------------------------------------------------------------------------
echo
echo "=== F. Linear is still the default :root theme ==="
# :root IS the Linear default. Pinned by: (1) the :root block
# exists at the top of themes.css, (2) the layout.html theme
# picker still lists "linear", (3) the Go code still has
# ThemeLinear = "linear" in internal/db.
f_default=$(awk '/^:root\{/{flag=1; next} /^\}/{flag=0} flag' "${THEMES}" | wc -l)
f_picker=$(grep -c 'theme=linear' internal/handlers/templates/layout.html 2>/dev/null || true)
f_db=$(grep -rn 'ThemeLinear = "linear"' internal/db/ 2>/dev/null | wc -l || true)
if [ "${f_default}" -ge 1 ] && [ "${f_picker}" -ge 1 ] && [ "${f_db}" -ge 1 ]; then
    ok "Linear theme is still the default :root (${f_default} lines) + still in theme picker (${f_picker} link) + still in ThemeLinear Go constant"
else
    bad "Linear default contract is broken: :root_lines=${f_default} picker=${f_picker} db_const=${f_db}"
fi

# ------------------------------------------------------------------------------
# Contract G: other 4 themes still have their own overrides
# ------------------------------------------------------------------------------
echo
echo "=== G. Vercel / Sentry / NVIDIA / Mint overrides are intact ==="
g_vercel=$(grep -cE '\[data-theme="vercel"\]\{' "${THEMES}" || true)
g_sentry=$(grep -cE '\[data-theme="sentry"\]\{' "${THEMES}" || true)
g_nvidia=$(grep -cE '\[data-theme="nvidia"\]\{' "${THEMES}" || true)
g_mint=$(grep -cE '\[data-theme="mint"\]\{' "${THEMES}" || true)
if [ "${g_vercel}" -ge 1 ] && [ "${g_sentry}" -ge 1 ] && [ "${g_nvidia}" -ge 1 ] && [ "${g_mint}" -ge 1 ]; then
    ok "All 4 other themes (vercel + sentry + nvidia + mint) have their [data-theme=...] override blocks intact"
else
    bad "B131 broke another theme's override: vercel=${g_vercel} sentry=${g_sentry} nvidia=${g_nvidia} mint=${g_mint}"
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B131 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B131 contracts all hold."
    exit 0
fi
echo
echo "B131 has failing contracts — fix the source files above."
exit 1
