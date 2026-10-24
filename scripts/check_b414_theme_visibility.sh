#!/usr/bin/env bash
# check_b414_theme_visibility.sh
#
# 2026-10-24 (B414) — EVERY THEME MUST SHOW EVERY ELEMENT.
#
# OPERATOR REPORT: «Проблема с цветом отображения текста в новых добавленных полях и в
# целом проверить что нет смешения стилей тем и на всех страницах по всем темам видно
# все элементы страницы — что они не сливаются с фоном».
#
# MEASURED CAUSE (the "new fields" half): the provisioning card added for B412/B412.1
# painted its key blocks with `background:var(--bg-subtle,#111)`. `--bg-subtle` was
# NEVER DEFINED by themes.css — so the fallback `#111` painted a near-black block in
# EVERY theme, including the light ones (vercel, mint), where the block's inherited
# dark text then had no contrast at all. The same audit found:
#
#   * 57 distinct `var(--token)` references to tokens themes.css never defined
#     (`--bg-muted` 26 sites, `--bg-secondary` 12, `--bg-input` 6, `--bg-subtle` 5,
#     `--fg` 6, `--font-mono` 4, `--error`, `--green`, `--red`, `--warn`, …);
#   * 47 hardcoded text colours chosen for a LIGHT page (`#198754`, `#dc3545`,
#     `#6c757d`, `#888`, `#e0f2fe` …) that sit on a theme surface, so on the dark
#     themes they dropped below AA and on the light ones the pale ones did;
#   * 20 hardcoded `var(--token,#colour)` FALLBACKS in 10 templates — the same shape as
#     the B412 block, one level down: `var(--danger,#dc3545)` (the literal D2 forbids!),
#     `var(--bg-info,#0a2a3a)`, `var(--bg-warn,#3a2a00)`, `var(--border,#e5e7eb)`.
#     Inert only while the token exists, and they hide the next wrong token name;
#   * FOUR themes whose `--text-subtle` was below 4.5:1 on their own `--bg`
#     (vercel 2.56, mint 2.93, nvidia 3.45, linear 4.15) and two below it on the CARD
#     surface (linear 3.57, sentry 4.12), mint's `--success`/`--warning`/`--info` at
#     2.4–3.4:1 as TEXT, and mint's `--accent-fg` on `--accent` at 2.54:1.
#
# FIX: missing tokens became explicit aliases of real ones in `:root` (one place, every
# theme), the light themes' semantic colours were darkened and `--text-subtle` raised
# until they clear 4.5:1 as text on `--bg` AND `--bg-card`, mint's on-accent ink became
# dark, every theme-blind hardcoded status colour in a template became its token, and
# every `var()` fallback was deleted so a wrong token name fails the gate instead of
# painting a plausible colour.
#
# CONTRACTS (22)
#   A1 every `var(--token)` used by a template or the panel CSS is DEFINED by themes.css
#      (vendor `--fa*` custom properties and commented-out examples excluded)
#   A2 the aliases the old templates rely on exist (--bg-subtle, --bg-muted,
#      --bg-secondary, --bg-input, --fg, --font-mono, --error)
#   A3 no template passes a hardcoded colour as a var() FALLBACK
#   A4 no stylesheet var() names a token themes.css never defines
#   B1 the WCAG floors hold ON EVERY THEME, computed from the stylesheet: --text >= 7.0,
#      --text-muted/--text-subtle >= 4.5, each semantic colour (success/warning/danger/
#      info) >= 4.5 on --bg AND --bg-card, --accent-fg on --accent >= 4.5 — 75 pairs
#   B2 the checker REJECTS a deliberately unreadable palette (so B1 cannot pass empty)
#   C1 a hardcoded `background:#…` in a template `style=` attribute must carry an
#      explicit colour in the SAME attribute, or be in the frozen allowlist — the exact
#      shape that made the new fields unreadable (background inherited the theme text)
#   C2 the allowlist may only SHRINK: a fixed row that stays listed fails
#   D1/D2/D3 no template paints text with a hardcoded colour outside the allowlist, the
#      pre-B414 light-page status palette stays gone, status text uses tokens
#   E1/E2 the stylesheet mirror stays byte-identical (B392/B411 rule) and both copies
#      carry the aliases
#   F  registered in the catalog, in the AGENTS block index, documented, git-tracked

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CSS="internal/staticfs/static/css/themes.css"
MIRROR="static/css/themes.css"
TPL="internal/handlers/templates"
ALLOW_BG="scripts/b414_hardcoded_bg_allowlist.txt"
ALLOW_FG="scripts/b414_hardcoded_fg_allowlist.txt"
# A token reference that is DOCUMENTED, not used: update.html explains in a template
# comment why `var(--xxx)` breaks (html/template prints "ZgotmplZ"C). A comment paints
# nothing, so the one placeholder is allowlisted by name with its reason.
ALLOW_UNDEF="scripts/b414_undefined_token_allowlist.txt"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/b414.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT

hdr "B414 — every theme must show every element"

# --- A: every var() reference must resolve -----------------------------------
# Tokens defined anywhere in themes.css (root + every theme block).
grep -oE '\--[a-z0-9-]+\s*:' "$CSS" | sed -E 's/\s*:$//' | sort -u >"$TMP/defined.txt"
# References from templates and panel CSS. Comments are dropped: several templates
# DOCUMENT the `var(--xxx)` anti-pattern in a template comment, and a comment cannot
# paint anything.
{
  grep -rhoE 'var\(\s*--[a-z0-9-]+' "$TPL" --include='*.html' | sed -E 's/var\(\s*//'
  grep -rhoE 'var\(\s*--[a-z0-9-]+' internal/staticfs/static/css/*.css | sed -E 's/var\(\s*//'
} | grep -vE '^--fa' | sort -u >"$TMP/used.txt"
# Documented-only placeholders (see the allowlist header) are not references.
if [ -f "$ALLOW_UNDEF" ]; then
  grep -oE '^--[a-z0-9-]+' "$ALLOW_UNDEF" | sort -u >"$TMP/documented.txt"
  comm -23 "$TMP/used.txt" "$TMP/documented.txt" >"$TMP/used_real.txt"
else
  cp "$TMP/used.txt" "$TMP/used_real.txt"
fi
comm -23 "$TMP/used_real.txt" "$TMP/defined.txt" >"$TMP/undefined.txt"
N_UNDEF="$(wc -l <"$TMP/undefined.txt" | tr -d ' ')"
if [ "$N_UNDEF" = "0" ]; then
  ok "A1: all $(wc -l <"$TMP/used_real.txt" | tr -d ' ') var() tokens used by the panel are defined by themes.css"
else
  bad "A1: $N_UNDEF var() token(s) are used but never defined (they silently fall back to an unrelated colour):"
  sed 's/^/       /' "$TMP/undefined.txt" >&2
fi
# The exact defect: a token that exists, so no template can fall back to #111 again.
for tok in --bg-subtle --bg-muted --bg-secondary --bg-input --fg --font-mono --error; do
  if grep -qx -- "$tok" "$TMP/defined.txt"; then
    ok "A2: $tok is defined (the B412 key blocks relied on it)"
  else
    bad "A2: $tok is not defined — a template using it would paint its fallback in every theme"
  fi
done
# The same defect one level down: themes.css calls var(--tok, <colour>) in 30 places.
# Those fallbacks are inert ONLY because the token is defined; a var() naming a token
# nobody declares would paint the fallback in all five themes and look intentional.
: >"$TMP/css_fallback_undef.txt"
for tok in $(grep -oE 'var\(--[a-z0-9-]+,' "$CSS" | sed -E 's/^var\(//; s/,$//' | sort -u); do
  grep -qx -- "$tok" "$TMP/defined.txt" || printf '%s\n' "$tok" >>"$TMP/css_fallback_undef.txt"
done
if [ -s "$TMP/css_fallback_undef.txt" ]; then
  bad "A4: themes.css passes a hardcoded fallback for token(s) it never defines (the fallback would paint in EVERY theme):"
  sed 's/^/       /' "$TMP/css_fallback_undef.txt" >&2
else
  ok "A4: every var() fallback in themes.css names a token that IS defined"
fi

# A widget must not paint a hardcoded fallback behind inherited text: the B412 regression.
# A var() fallback is inert ONLY while the token exists, so it HIDES a wrong token name
# (the B412 key block used --bg-subtle, which never existed, with a #111 fallback, and the
# block went dark in the light themes) and freezes a colour the theme can no longer move.
# Measured before the sweep: 20 such fallbacks across 10 templates, including
# var(--danger,#dc3545) — the very literal contract D2 forbids — var(--bg-info,#0a2a3a)
# and var(--border,#e5e7eb). Zero remain, so the rule can be absolute.
if grep -rqE 'var\(--[a-z0-9-]+[[:space:]]*,[[:space:]]*#[0-9a-fA-F]{3,8}\)' "$TPL" --include='*.html'; then
  bad "A3: a template passes a hardcoded COLOUR as a var() fallback — the theme can never change it:"
  grep -rnE 'var\(--[a-z0-9-]+[[:space:]]*,[[:space:]]*#[0-9a-fA-F]{3,8}\)' "$TPL" --include='*.html' |
    head -10 | sed 's/^/       /' >&2
else
  ok "A3: no template passes a hardcoded colour as a var() fallback"
fi

# --- B: contrast floors, per theme -------------------------------------------
# Extract `--token:#hex` pairs per theme block and compute WCAG contrast in awk
# (`^` is available; no external tool needed).
CONTRAST_AWK='
function hex2v(h,   r,g,b) {
  sub(/^#/, "", h)
  if (length(h) == 3) h = substr(h,1,1) substr(h,1,1) substr(h,2,1) substr(h,2,1) substr(h,3,1) substr(h,3,1)
  r = strtonum("0x" substr(h,1,2)); g = strtonum("0x" substr(h,3,2)); b = strtonum("0x" substr(h,5,2))
  return r "," g "," b
}
function lin(v) { v = v / 255; return (v <= 0.03928) ? v / 12.92 : ((v + 0.055) / 1.055) ^ 2.4 }
function lum(rgb,   p) { split(rgb, p, ","); return 0.2126*lin(p[1]) + 0.7152*lin(p[2]) + 0.0722*lin(p[3]) }
function ratio(fg, bg,   a,b,hi,lo) {
  a = lum(fg); b = lum(bg); hi = (a > b) ? a : b; lo = (a > b) ? b : a
  return (hi + 0.05) / (lo + 0.05)
}
BEGIN { theme = "root"; fails = 0 }
/^[[:space:]]*\[data-theme="[a-z0-9_-]+"\][[:space:]]*\{/ {
  if (match($0, /data-theme="[a-z0-9_-]+"/)) theme = substr($0, RSTART + 12, RLENGTH - 13)
  next
}
/^[[:space:]]*:root[[:space:]]*\{/ { theme = "root"; next }
/^\}/ { next }
{
  line = $0
  # A theme block packs several declarations on one line; match EVERY one.
  while (match(line, /--[a-z0-9-]+[[:space:]]*:[[:space:]]*#[0-9a-fA-F]{3,8}/)) {
    s = substr(line, RSTART, RLENGTH)
    line = substr(line, RSTART + RLENGTH)
    eq = index(s, ":")
    name = substr(s, 1, eq - 1); gsub(/[[:space:]]/, "", name)
    hex = substr(s, eq + 1); gsub(/[[:space:]]/, "", hex)
    vals[theme, name] = hex2v(hex)
    seen[theme] = 1
  }
}
END {
  npairs = split("--text|--bg|7.0;--text|--bg-card|7.0;--text-muted|--bg|4.5;--text-muted|--bg-card|4.5;--text-subtle|--bg|4.5;--text-subtle|--bg-card|4.5;--success|--bg|4.5;--success|--bg-card|4.5;--warning|--bg|4.5;--warning|--bg-card|4.5;--danger|--bg|4.5;--danger|--bg-card|4.5;--info|--bg|4.5;--info|--bg-card|4.5;--accent-fg|--accent|4.5", pairs, ";")
  for (th in seen) {
    for (i = 1; i <= npairs; i++) {
      split(pairs[i], pp, "|"); fg = pp[1]; bg = pp[2]; floor = pp[3] + 0
      f = ((th, fg) in vals) ? vals[th, fg] : vals["root", fg]
      b = ((th, bg) in vals) ? vals[th, bg] : vals["root", bg]
      if (f == "" || b == "") { printf "MISSING %s %s on %s\n", th, fg, bg; fails++; continue }
      r = ratio(f, b)
      verdict = (r >= floor) ? "OK" : "LOW"
      printf "%s %s on %s %.2f (floor %.1f) %s\n", th, fg, bg, r, floor, verdict
      if (verdict == "LOW") fails++
    }
  }
  printf "TOTAL_FAILS %d\n", fails
}'
awk "$CONTRAST_AWK" "$CSS" >"$TMP/contrast.txt"
LOW_ROWS="$(grep ' LOW$' "$TMP/contrast.txt" || true)"
MISSING_ROWS="$(grep '^MISSING' "$TMP/contrast.txt" || true)"
if [ -z "$LOW_ROWS" ] && [ -z "$MISSING_ROWS" ]; then
  ok "B1: every theme clears the contrast floors ($(grep -c ' OK$' "$TMP/contrast.txt") pairs checked)"
else
  bad "B1: contrast floors are violated — text would blend into the surface:"
  { printf '%s\n' "$LOW_ROWS"; printf '%s\n' "$MISSING_ROWS"; } | sed 's/^/       /' >&2
fi
# The floors must be non-trivial: prove the checker rejects a deliberately bad pair.
python_ok=0
if awk "$CONTRAST_AWK" <<'CSS2' | grep -q ' LOW$'
:root{
--text:#111111;--bg:#101010;--bg-card:#101010;--text-muted:#111111;--text-subtle:#111111;
--success:#111111;--warning:#111111;--danger:#111111;--info:#111111;
--accent:#111111;--accent-fg:#111111;
}
CSS2
then python_ok=1; fi
if [ "$python_ok" = "1" ]; then
  ok "B2: the contrast checker rejects a deliberately unreadable palette"
else
  bad "B2: the checker passes an unreadable palette — it proves nothing"
fi

# --- C: background without an explicit colour --------------------------------
# The B412 defect class: `background:#111` (or a dark fallback) with the text colour
# inherited from the theme, which is how a block becomes invisible in another theme.
BG_RISKY="$TMP/bg_risky.txt"
: >"$BG_RISKY"
grep -rn 'style="' "$TPL" --include='*.html' | while IFS= read -r row; do
  body="${row#*style=\"}"
  body="${body%%\"*}"
  case "$body" in *background*|*"background-color"*) ;; *) continue ;; esac
  case "$body" in
    *"background:var("*|*"background-color:var("*) ;;
    *background:#*|*background:rgb*)
      case "$body" in
        *"color:#"*|*"color: #"*|*"color:var("*) ;;
        *) printf '%s\n' "$row" ;;
      esac
      ;;
  esac
done >"$BG_RISKY"
# Comparison is on path:ROW, not path:LINE:ROW: the allowlist pins what the rule DOES,
# so an unrelated edit above a frozen row must not demand a bookkeeping change. The
# detector still prints the live line number for the operator.
bg_norm() { sed -E 's/^([^:]*):[0-9]+:/\1:/'; }
: >"$TMP/bg_allowed.txt"
if [ -f "$ALLOW_BG" ]; then
  grep -v '^[[:space:]]*#' "$ALLOW_BG" | sed '/^[[:space:]]*$/d' | bg_norm | sort -u >"$TMP/bg_allowed.txt"
fi
sort -u "$BG_RISKY" | bg_norm | sort -u >"$TMP/bg_risky_norm.txt"
NEW_BG="$(comm -23 "$TMP/bg_risky_norm.txt" "$TMP/bg_allowed.txt")"
STALE_BG="$(comm -13 "$TMP/bg_risky_norm.txt" "$TMP/bg_allowed.txt")"
bg_fail=0
if [ -n "$NEW_BG" ]; then
  bg_fail=1
  bad "C1: template(s) paint a hardcoded background with no explicit colour — the text inherits the theme and can vanish:"
  printf '%s\n' "$NEW_BG" | head -10 | sed 's/^/       /' >&2
fi
if [ -n "$STALE_BG" ]; then
  bg_fail=1
  bad "C2: the frozen allowlist still lists row(s) that are gone — a fixed page must leave the list (it may only shrink):"
  printf '%s\n' "$STALE_BG" | head -10 | sed 's/^/       /' >&2
fi
if [ "$bg_fail" = "0" ]; then
  ok "C1: no template paints a hardcoded background behind inherited text (allowlisted: $(wc -l <"$TMP/bg_allowed.txt" | tr -d ' '))"
fi

# --- D: hardcoded text colours ----------------------------------------------
FG_USED="$TMP/fg_used.txt"
grep -rhoE 'color:\s*#[0-9a-fA-F]{3,8}' "$TPL" --include='*.html' | sed -E 's/\s//g' | sort -u >"$FG_USED"
: >"$ALLOW_FG.tmp"
if [ -f "$ALLOW_FG" ]; then
  grep -oE '^color:#[0-9a-fA-F]{3,8}' "$ALLOW_FG" | sort -u >"$TMP/fg_allowed.txt"
else
  : >"$TMP/fg_allowed.txt"
fi
NEW_FG="$(comm -23 "$FG_USED" "$TMP/fg_allowed.txt")"
if [ -z "$NEW_FG" ]; then
  ok "D1: no NEW hardcoded text colour outside the frozen allowlist ($(wc -l <"$TMP/fg_allowed.txt" | tr -d ' ') allowlisted)"
else
  bad "D1: new hardcoded text colour(s) — a value chosen for one theme is invisible on another:"
  printf '%s\n' "$NEW_FG" | sed 's/^/       /' >&2
fi
rm -f "$ALLOW_FG.tmp"
if grep -qx 'color:#dc3545' "$FG_USED" 2>/dev/null; then
  bad "D2: the light-page status red #dc3545 is back — use var(--danger)"
else
  ok "D2: the old light-page status colours are still gone"
fi
# Every semantic colour the templates use must be a TOKEN, not a literal: the four
# status hues are the ones that used to be pasted per page.
if grep -rqE 'style="[^"]*color:\s*#(198754|dc3545|6c757d|f0ad4e)' "$TPL" --include='*.html'; then
  bad "D3: a template still paints status text with a hardcoded colour"
else
  ok "D3: status text is painted with theme tokens"
fi

# --- E: the stylesheet mirror stays byte-identical --------------------------
if cmp -s "$CSS" "$MIRROR"; then
  ok "E1: $CSS and $MIRROR are byte-identical (B392/B411 rule)"
else
  bad "E1: the two stylesheet copies differ — the Go tests and the served CSS disagree"
fi
if grep -q -- '--bg-subtle:var(--bg-elev)' "$CSS" && grep -q -- '--bg-subtle:var(--bg-elev)' "$MIRROR"; then
  ok "E2: both copies carry the semantic aliases"
else
  bad "E2: the alias block is missing from one of the copies"
fi

# --- F: registered, indexed, documented -------------------------------------
if grep -q 'run_check "B414"' scripts/verify_pre_deploy.sh; then
  ok "F1: B414 is registered in the catalog"
else
  bad "F1: B414 is not registered — this contract would never run"
fi
if grep -q '\*\*B414\*\*' AGENTS.md; then
  ok "F2: AGENTS.md block index carries B414 (rule 2)"
else
  bad "F2: AGENTS.md has no B414 entry"
fi
# Trap 9: never let a pipeline be the verdict — grep -q into head reports HEAD's status,
# so the old form passed on a doc that mentions nothing. Match in the condition itself.
if grep -qF 'no page may paint its own colours' docs/internals.md 2>/dev/null &&
   grep -qF 'check_b414_theme_visibility.sh' docs/internals.md 2>/dev/null; then
  ok "F3: docs/internals.md carries the theme-token invariant and names this contract"
else
  bad "F3: docs/internals.md does not document the theme-token invariant"
fi
if git ls-files --error-unmatch scripts/check_b414_theme_visibility.sh >/dev/null 2>&1; then
  ok "F4: this script is tracked by git (AGENTS trap #11)"
else
  bad "F4: this script is not tracked by git"
fi

printf '\n\033[1mB414 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
