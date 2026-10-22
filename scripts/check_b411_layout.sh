#!/usr/bin/env bash
# check_b411_layout.sh
#
# 2026-10-22 (B411) — THE SHELL MUST BE CENTRED IN THE SPACE THAT EXISTS, AND A
# ROTATING CDN IS NOT A BROKEN MECHANISM.
#
# WHY (two operator-facing defects in one block)
#
#  1. «в целом большинство страниц на широких экранах не оставляет справа много
#     свободного места, но для этого надо делать глобальный рефактор и проверку
#     инструментами по каждой странице».
#     Measured with docs/responsive-audit.md + the skygate-web-ui-audit method:
#     .shell carries `max-width:1180px;margin:0 auto`, and a later rule overrode
#     ONLY its left margin to 220px — so `margin-right:auto` absorbed every spare
#     pixel and the content stayed pinned to the sidebar: 1366px viewport → 0px
#     empty, 1440 → 40, 1920 → 520, 2560 → 1160, 3440 → 2040. The shell was never
#     centred; it was left-aligned inside a centred 1180px box.
#     Fix: the sidebar reservation moved to <main> (220px, 52px collapsed, 0 on
#     mobile), .shell keeps `margin:0 auto` and therefore centres inside the real
#     content column, and two media steps widen it on large displays
#     (1400px at ≥1600px, 1600px at ≥2200px) instead of leaving 2000px blank.
#     The breadcrumb is a sibling INSIDE <main>, so it inherits the offset — B120's
#     mirrored 220/52px margins are gone, because a second offset would double it.
#
#  2. `exit_rules.domain_prefix` was red on a HEALTHY installation: measured
#     minutes apart, 6 of 13 addresses "stale", then after the next pass — 5
#     published ranges untouched, 8 obsolete /32 rows removed, the new answer
#     written as a gap. The mechanism was right; the assertion compared today's
#     DNS with the stored rows, which a rotating provider (Akamai 62.115.x, AWS
#     registry edges) can never satisfy while B308 re-resolves every six hours.
#     The verdict is now about the mechanism (covered / pending-next-pass /
#     stale), decided by exit_rules.DomainRowsVerdictFor from the same per-domain
#     resolve stamp the updater writes.
#
# CONTRACTS
#   A. geometry: the reservation lives on <main>, .shell centres, the mobile reset
#      follows it, the breadcrumb carries no offset of its own, the wide-screen
#      steps exist, and BOTH copies of the stylesheet are byte-identical (the
#      panel serves the embedded one; Go tests read the mirror — B392).
#   B. per-page overflow ratchet: no inline min-width that forces horizontal
#      overflow, and the number of inline container max-widths may only go down.
#   C. the domain_prefix verdict: the engine exists, the test uses it, only the
#      "will not heal" case fails, and the pure decision function is covered.
#   D. registered, tracked, documented (trap #11).
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/staticfs/static/css/themes.css ] || {
  printf 'B411: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b411.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

CSS=internal/staticfs/static/css/themes.css
MIRROR=static/css/themes.css
VERDICT=internal/feature/exit_rules/domain_rows_verdict_b411.go
TESTGO=internal/feature/exit_rules/domain_rows_verdict_b411_test.go
SYSTEST=internal/feature/admin/system_tests_b394_new.go
BUDGET=scripts/b411_inline_width_allowlist.txt
TPL_DIR=internal/handlers/templates

hdr "B411 — centred shell, honest domain_prefix verdict"

# ---------------------------------------------------------------------------
# A. geometry
# ---------------------------------------------------------------------------
hdr "A — the sidebar reservation lives on <main>, so .shell can centre"

if grep -qE '^main\{margin-left:220px' "$CSS"; then
  ok "A1: <main> carries the desktop reservation (margin-left:220px)"
else
  bad "A1: <main> has no 220px reservation — the content would sit under the fixed sidebar"
fi
if grep -qE '^main[[:space:]]+\.shell\{[^}]*margin-left:[0-9]+px' "$CSS"; then
  bad "A2: a main .shell{margin-left:<N>px} rule is present — it overrides only the LEFT half of margin:0 auto and pushes every spare pixel to the right (the band: 520px at 1920, 1160px at 2560)"
else
  ok "A2: nothing overrides the shell's left margin — the free space is split evenly"
fi
if grep -qE '^\.sidebar\.collapsed ~ main\{margin-left:52px\}' "$CSS"; then
  ok "A3: a collapsed sidebar moves the whole column (52px)"
else
  bad "A3: missing .sidebar.collapsed ~ main{margin-left:52px}"
fi
if grep -qE '^\.shell\{max-width:1180px;margin:0 auto;' "$CSS"; then
  ok "A4: .shell keeps max-width:1180px with margin:0 auto — the centring this block relies on"
else
  bad "A4: .shell lost its centring (max-width:1180px + margin:0 auto)"
fi
if grep -qE '@media \(min-width:1600px\)\{main \.shell\{max-width:1[4-9][0-9][0-9]px\}\}' "$CSS" \
   && grep -qE '@media \(min-width:2200px\)\{main \.shell\{max-width:[12][0-9][0-9][0-9]px\}\}' "$CSS"; then
  ok "A5: wide screens get more room in two steps (≥1600px and ≥2200px)"
else
  bad "A5: no wide-screen max-width step — a 3440px display would still get a 1180px column and the right-hand band survives"
fi
if awk '/@media \(max-width:768px\)/{m=1} m{buf=buf $0 "\n"} m&&/^\}/{print buf; buf=""; m=0}' "$CSS" \
   | grep -qE '^[[:space:]]*main\{margin-left:0 !important\}'; then
  ok "A6: the mobile block resets <main> to 0 (the drawer reserves no column)"
else
  bad "A6: the mobile block does not reset <main> — on a phone the whole page stays shifted 220px right"
fi
BC_BODY="$(awk '/^main \.admin-breadcrumb\{/{i=1} i{buf=buf $0} i&&/\}/{print buf; exit}' "$CSS")"
if [ -n "$BC_BODY" ] && ! printf '%s' "$BC_BODY" | grep -qE 'margin-left:(220|52)px' \
   && printf '%s' "$BC_BODY" | grep -q 'margin-left:0'; then
  ok "A7: the breadcrumb has no offset of its own — <main> provides it once"
else
  bad "A7: the breadcrumb carries its own offset (${BC_BODY}) — <main> already offsets it, so it would be applied twice"
fi
if git ls-files --error-unmatch "$MIRROR" >/dev/null 2>&1; then
  if cmp -s "$CSS" "$MIRROR"; then
    ok "A8: the embedded stylesheet and the root mirror are byte-identical (the panel serves the first, Go tests read the second)"
  else
    bad "A8: $CSS and $MIRROR differ — a layout fix applied to one side leaves the gate green and the panel broken (B392)"
  fi
else
  bad "A8: $MIRROR is not tracked (trap #11)"
fi

# ---------------------------------------------------------------------------
# B. per-page overflow ratchet
# ---------------------------------------------------------------------------
hdr "B — per-page overflow ratchet (the operator's «проверка по каждой странице»)"

WIDE_MIN="$SKY_TMP/wide_min.txt"
: > "$WIDE_MIN"
for f in $(grep -rlE 'min-width:[0-9]{3,}px' "$TPL_DIR" --include='*.html' 2>/dev/null); do
  if grep -qE 'min-width:(3|4|5|6|7|8|9|[0-9]{4,})[0-9]{2}px' "$f"; then
    printf '%s\n' "${f#$TPL_DIR/}" >> "$WIDE_MIN"
  fi
done
if [ -s "$WIDE_MIN" ]; then
  bad "B1: template(s) force an inline min-width ≥ 300px, which overflows a 360px phone: $(tr '\n' ' ' < "$WIDE_MIN")"
else
  ok "B1: no template forces an inline min-width ≥ 300px (measured 0 before this block, so the ratchet starts at zero)"
fi

CONTAINERS="$SKY_TMP/containers.txt"
: > "$CONTAINERS"
for f in $(grep -rlE '<(div|section|form|main)[^>]*style="[^"]*max-width:(6|7|8|9|[0-9]{4,})[0-9]{2}px' "$TPL_DIR" --include='*.html' 2>/dev/null); do
  n=$(grep -ohE '<(div|section|form|main)[^>]*style="[^"]*max-width:(6|7|8|9|[0-9]{4,})[0-9]{2}px' "$f" | wc -l | tr -d ' ')
  i=1
  while [ "$i" -le "$n" ]; do
    printf '%s\n' "${f#$TPL_DIR/}" >> "$CONTAINERS"
    i=$((i+1))
  done
done
sort -o "$CONTAINERS" "$CONTAINERS"
NOW=$(wc -l < "$CONTAINERS" | tr -d ' ')
if [ -f "$BUDGET" ]; then
  grep -v '^#' "$BUDGET" | grep -v '^$' | sort > "$SKY_TMP/budget.txt"
  CAP=$(wc -l < "$SKY_TMP/budget.txt" | tr -d ' ')
else
  CAP=0
fi
if [ "${NOW:-0}" -le "${CAP:-0}" ]; then
  ok "B2: inline container widths ≥ 600px: ${NOW}, budget ${CAP} (only ever downwards)"
else
  bad "B2: ${NOW} inline container width(s) ≥ 600px but the budget is ${CAP} — a page is hard-coding its own width instead of using .shell"
fi
if [ -f "$BUDGET" ]; then
  MISSING=$(comm -23 <(sort -u "$CONTAINERS") <(sort -u "$SKY_TMP/budget.txt") | head -5)
  if [ -z "$MISSING" ]; then
    ok "B3: every measured container width is named in the budget file"
  else
    bad "B3: unnamed container width(s): $(printf '%s' "$MISSING" | tr '\n' ' ')"
  fi
fi

# ---------------------------------------------------------------------------
# C. the domain_prefix verdict
# ---------------------------------------------------------------------------
hdr "C — a rotating CDN is not a broken mechanism"

if [ -f "$VERDICT" ] && grep -q 'func DomainRowsVerdictFor(' "$VERDICT" \
   && grep -q 'DomainRowsCovered' "$VERDICT" \
   && grep -q 'DomainRowsPendingNextPass' "$VERDICT" \
   && grep -q 'DomainRowsStale' "$VERDICT"; then
  ok "C1: exit_rules.DomainRowsVerdictFor exists with its three verdicts"
else
  bad "C1: the verdict engine is missing"
fi
if grep -q 'exit_rules.DomainRowsVerdictFor(' "$SYSTEST"; then
  ok "C2: exit_rules.domain_prefix asks the verdict engine instead of comparing with today's DNS"
else
  bad "C2: the test still decides staleness by itself"
fi
if awk '/if len\(staleDomains\) > 0 \{/,/^			\}/' "$SYSTEST" | grep -q 'SystemTestFail' \
   && awk '/if len\(pendingDomains\) > 0 \{/,/^			\}/' "$SYSTEST" | grep -q 'SystemTestPass'; then
  ok "C3: only 'will not heal by itself' fails; a one-rotation-behind rule PASSES and says so"
else
  bad "C3: the fail/pass boundary is not the mechanism one"
fi
if grep -q 'if !autoOn && verdict == exit_rules.DomainRowsPendingNextPass' "$SYSTEST"; then
  ok "C4: with auto-update OFF a pending rotation is escalated to stale (nothing will run)"
else
  bad "C4: an OFF auto-updater could still report a benign rotation"
fi
if grep -q 'SettingDomainResolveAtPrefix' internal/feature/admin/system_tests_domain_verdict_b411.go \
   && grep -q 'SettingDomainResolveAtPrefix' internal/feature/exit_rules/domain_interval_b308.go; then
  ok "C5: the panel reads the SAME per-domain stamp key the updater writes"
else
  bad "C5: the panel reads a different key than the updater writes"
fi
if [ -f "$TESTGO" ] && command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 ./internal/feature/exit_rules/ -run 'TestB411_' 2>&1)"
  if printf '%s\n' "$OUT" | grep -q '^ok'; then
    ok "C6: the pure decision function is covered (covered / pending / stale / never-resolved / disabled gate / backwards clock / boundaries)"
  else
    bad "C6: the B411 verdict tests do not pass: $(printf '%s\n' "$OUT" | tail -3 | tr '\n' ' ')"
  fi
else
  skip "C6: go not on PATH (the source contracts above still hold)"
fi

# ---------------------------------------------------------------------------
# D. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "D — registered, tracked, documented"

if git ls-files --error-unmatch scripts/check_b411_layout.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git (trap #11)"
else
  bad "D1: this script is not tracked"
fi
if grep -q 'check_b411_layout' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in the catalog"
fi
if grep -q '^\- \*\*B411\*\*' AGENTS.md 2>/dev/null; then
  ok "D3: one-line entry in the AGENTS.md block index"
else
  bad "D3: no B411 entry in the AGENTS block index"
fi
if grep -q '^## v1.5.155' RELEASE-NOTES.md 2>/dev/null; then
  ok "D4: RELEASE-NOTES.md carries the v1.5.155 section"
else
  bad "D4: no v1.5.155 section in RELEASE-NOTES.md"
fi

printf '\n\033[1mB411: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
