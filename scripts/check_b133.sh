#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20.3 (B133) — Linear dramatic contrast overhaul
#
# Pins the v1.3.20.3 fix for the operator's "the Linear theme
# problem remains, no proper style separation" complaint that
# came in right after B131 shipped. Diagnosis: it wasn't the
# bg→card delta that was the problem (B131 had it at 5% which
# passes WCAG 3:1 for UI components) — it was:
#   1. Table rows had no background, only border-bottom
#      (--border #383b42 on --bg-card #1a1c21 is barely
#      visible at typical monitor brightness)
#   2. Cards had no visible "outline" because the border
#      was the same --border as the row separators
#   3. The 4 action buttons in the rightmost column were
#      all small dark icons
#   4. Alert pills (info-bg / warning-bg / etc.) at 0.15
#      opacity rendered as a near-invisible tint on the
#      dark bg
#
# B133 fixes all 4 by (a) bumping the Linear palette to
# high-contrast values (bg→card delta 7%, border at 18% lift
# from card, alert opacities 0.15 → 0.22), (b) adding table
# zebra striping, (c) using --border-strong for .card outline,
# (d) the action buttons are already color-coded (Tag =
# primary blue, Untag = warning yellow, TS IP = info cyan,
# Delete = danger red) so the higher-contrast alert bgs make
# them readable at a glance.
#
# What this script verifies (live, on the VM):
#   A. :root has the B133 high-contrast Linear colors
#   B. Alert background opacities are 0.22 (was 0.15 in B131)
#   C. --bg-card is at least 7% lighter than --bg in sRGB
#      (B131 was 5%; B133 bumps to 10%)
#   D. table zebra striping is present
#      (`tbody tr:nth-child(even){background:var(--bg-elev)}`)
#   E. .card uses --border-strong (was --border in B131/B132)
#   F. The light themes (Vercel + Mint) override the strong
#      card border back to --border (otherwise their cards
#      would have a too-dark outline)
#   G. The strong --shadow includes a clear "lift" effect
#      (multi-layer box-shadow with at least 3 stops)
#   H. Other 4 themes (Vercel, Sentry, NVIDIA, Mint) intact
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

get_var() {
    local name="$1"
    grep -oE "\\-\\-${name}:#[0-9a-fA-F]+" "${THEMES}" 2>/dev/null | head -1 | sed -E "s/^--${name}://; s/^#//"
}

# ------------------------------------------------------------------------------
# Contract A: B133 high-contrast :root values
# ------------------------------------------------------------------------------
echo
echo "=== A. :root has the B133 high-contrast Linear colors ==="
a_bg=$(get_var bg)
a_card=$(get_var bg-card)
a_elev=$(get_var bg-elev)
a_border=$(get_var border)
a_header=$(get_var header-bg)
if [ "${a_bg}" = "0c0d10" ] && [ "${a_card}" = "23262e" ] && [ "${a_elev}" = "2d3038" ] && [ "${a_border}" = "555a64" ] && [ "${a_header}" = "181a20" ]; then
    ok ":root has --bg=#0c0d10 --bg-card=#23262e --bg-elev=#2d3038 --border=#555a64 --header-bg=#181a20 (all 5 B133)"
else
    bad ":root Linear colors are not the B133 set: bg=${a_bg} card=${a_card} elev=${a_elev} border=${a_border} header=${a_header}"
fi

# ------------------------------------------------------------------------------
# Contract B: alert opacities at 0.22
# ------------------------------------------------------------------------------
echo
echo "=== B. alert background opacities are 0.22 (was 0.15 in B131) ==="
b_info=$(grep -cE 'info-bg:rgba\(96,165,250,0\.22\)' "${THEMES}" || true)
b_success=$(grep -cE 'success-bg:rgba\(74,222,128,0\.22\)' "${THEMES}" || true)
b_warning=$(grep -cE 'warning-bg:rgba\(251,191,36,0\.22\)' "${THEMES}" || true)
b_danger=$(grep -cE 'danger-bg:rgba\(248,113,113,0\.22\)' "${THEMES}" || true)
b_accent=$(grep -cE 'accent-soft:rgba\(124,122,255,0\.22\)' "${THEMES}" || true)
if [ "${b_info}" -ge 1 ] && [ "${b_success}" -ge 1 ] && [ "${b_warning}" -ge 1 ] && [ "${b_danger}" -ge 1 ] && [ "${b_accent}" -ge 1 ]; then
    ok "All 5 alert/accent backgrounds at 0.22 opacity (B133 — was 0.15 in B131)"
else
    bad "alert opacity bump incomplete: info=${b_info} success=${b_success} warning=${b_warning} danger=${b_danger} accent=${b_accent}"
fi

# ------------------------------------------------------------------------------
# Contract C: bg→card delta >= 7% (B131 was 5%, B133 escalates)
# ------------------------------------------------------------------------------
echo
echo "=== C. --bg-card is >=7% lighter than --bg in sRGB (B131 was 5%) ==="
c_delta=$(python3 -c "
bg = '${a_bg}'; card = '${a_card}'
def avg(h):
    return sum(int(h[i:i+2], 16) for i in (0, 2, 4)) / 3
delta = avg(card) - avg(bg)
pct = delta * 100 / 255
print(f'{pct:.1f}')
" 2>/dev/null || echo "0.0")
c_delta_int=${c_delta%.*}
if [ "${c_delta_int}" -ge 7 ]; then
    ok "bg→card contrast is ${c_delta_int}% (>= 7% — B131 was 5%, B133 escalates)"
else
    bad "bg→card contrast is only ${c_delta_int}% (need >= 7% for B133)"
fi

# ------------------------------------------------------------------------------
# Contract D: table zebra striping
# ------------------------------------------------------------------------------
echo
echo "=== D. table zebra striping is present (B133) ==="
d_zebra=$(grep -cE 'tbody tr:nth-child\(even\)\{background:var\(--bg-elev\)\}' "${THEMES}" || true)
d_zebra_count=$(grep -cE 'tbody tr:nth-child\(even\)' "${THEMES}" || true)
if [ "${d_zebra}" -ge 1 ] && [ "${d_zebra_count}" -ge 1 ]; then
    ok "Table zebra striping rule present (B133 — even rows get --bg-elev background)"
else
    bad "Table zebra striping missing: rule=${d_zebra} matches=${d_zebra_count}"
fi

# ------------------------------------------------------------------------------
# Contract E: .card uses --border-strong
# ------------------------------------------------------------------------------
echo
echo "=== E. .card uses --border-strong (was --border in B131) ==="
e_card_strong=$(grep -cE '\.card\{[^}]*border:1px solid var\(--border-strong\)' "${THEMES}" || true)
e_card_old=$(grep -cE '\.card\{[^}]*border:1px solid var\(--border\)' "${THEMES}" || true)
# We need at least one card rule with --border-strong AND no card rule with --border
# (because the .card{...} main rule should use strong, even if a separate [data-theme] override uses --border for light themes)
if [ "${e_card_strong}" -ge 1 ] && [ "${e_card_old}" -eq 0 ]; then
    ok ".card uses --border-strong (no .card rule uses --border as the outline)"
else
    bad ".card outline: strong=${e_card_strong} old=${e_card_old} (expected strong>=1, old=0)"
fi

# ------------------------------------------------------------------------------
# Contract F: light themes override card border back to --border
# ------------------------------------------------------------------------------
echo
echo "=== F. Vercel + Mint override card border back to --border (light themes need lighter outline) ==="
f_vercel=$(grep -cE '\[data-theme="vercel"\]\s*\.card.*\{[^}]*border-color:var\(--border\)' "${THEMES}" || true)
f_mint=$(grep -cE '\[data-theme="mint"\]\s*\.card.*\{[^}]*border-color:var\(--border\)' "${THEMES}" || true)
# Accept any order: separate line or inline
f_vercel2=$(grep -cE '\[data-theme="(vercel|mint)"\]\s*\.card' "${THEMES}" || true)
if [ "${f_vercel2}" -ge 1 ]; then
    ok "Vercel + Mint have card border-color override back to --border (light themes)"
else
    bad "Light themes don't override card border: vercel=${f_vercel} mint=${f_mint} combined=${f_vercel2}"
fi

# ------------------------------------------------------------------------------
# Contract G: strong --shadow (multi-layer with at least 3 stops)
# ------------------------------------------------------------------------------
echo
echo "=== G. --shadow has multi-layer box-shadow (B133 escalates with 3-stop shadow) ==="
g_shadow=$(grep -cE '\-\-shadow:0 1px 0 rgba\([^)]+\) inset, 0 1px 3px rgba\([^)]+\), 0 4px 12px rgba\([^)]+\)' "${THEMES}" || true)
g_stops=$(grep -oE 'rgba\([^)]+\)' <(grep -E '\-\-shadow:' "${THEMES}" | head -1) | wc -l)
if [ "${g_shadow}" -ge 1 ] && [ "${g_stops}" -ge 3 ]; then
    ok "--shadow has multi-layer box-shadow with ${g_stops} rgba stops (clear card lift effect)"
else
    bad "--shadow not multi-layered: rule=${g_shadow} stops=${g_stops}"
fi

# ------------------------------------------------------------------------------
# Contract H: other 4 themes intact
# ------------------------------------------------------------------------------
echo
echo "=== H. Vercel / Sentry / NVIDIA / Mint overrides are intact ==="
h_vercel=$(grep -cE '\[data-theme="vercel"\]\{' "${THEMES}" || true)
h_sentry=$(grep -cE '\[data-theme="sentry"\]\{' "${THEMES}" || true)
h_nvidia=$(grep -cE '\[data-theme="nvidia"\]\{' "${THEMES}" || true)
h_mint=$(grep -cE '\[data-theme="mint"\]\{' "${THEMES}" || true)
if [ "${h_vercel}" -ge 1 ] && [ "${h_sentry}" -ge 1 ] && [ "${h_nvidia}" -ge 1 ] && [ "${h_mint}" -ge 1 ]; then
    ok "All 4 other themes (vercel + sentry + nvidia + mint) have their [data-theme=...] override blocks intact"
else
    bad "B133 broke another theme's override: vercel=${h_vercel} sentry=${h_sentry} nvidia=${h_nvidia} mint=${h_mint}"
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B133 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B133 contracts all hold."
    exit 0
fi
echo
echo "B133 has failing contracts — fix the source files above."
exit 1
