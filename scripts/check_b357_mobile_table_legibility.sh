#!/usr/bin/env bash
# check_b357_mobile_table_legibility.sh
#
# 2026-10-06 (B357) — on a phone the tables printed ONE GLYPH PER LINE.
#
# Measured by the mobile audit on the live v1.5.101 panel at 390x844 (headless browser):
# `overflow-wrap:anywhere` in the mobile block of themes.css REDUCES a cell's intrinsic
# min-content width to ONE character, so under `table{width:100%}` + auto layout the
# column collapsed to 29px and `/admin/devices` rendered `100.64.0.3` as TEN lines at
# 1.00 chars/line — «П о л ь з о в а т е л ь». The scroller existed and could not help:
# scrolling cannot widen a 29px column. Eleven more pages shared the cause, four of them
# through an inline `word-break:break-all`, which breaks at ANY character.
#
# CONTRACTS
#   A. the mobile block breaks words only at a break opportunity (break-word, not anywhere)
#   B. addresses/timestamps stay on one line, and no template forces break-all
#   C. the pages that need a scroller have one
#   D. this check cannot be dropped silently
#
# SKIPs never apply here (all contracts are static source greps).

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || { printf 'B357: cannot locate cmd/skygate/main.go\n' >&2; exit 2; }

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CSS=internal/staticfs/static/css/themes.css
TPL=internal/handlers/templates

hdr "B357 — a narrow viewport must not print one glyph per line"

if grep -q 'td code,td .mono,td pre,code,.mono,pre{overflow-wrap:break-word}' "$CSS"; then
  ok "A1: the mobile text-break rule uses break-word (it cannot shrink min-content to 1 char)"
else
  bad "A1: the mobile rule does not use break-word — a narrow cell will stack letters again"
fi
if grep -q 'td code,td .mono,td pre,code,.mono,pre{overflow-wrap:anywhere}' "$CSS"; then
  bad "A1b: overflow-wrap:anywhere is back (that is the reported «П о л ь з о в а т е л ь»)"
else
  ok "A1b: no mobile rule uses overflow-wrap:anywhere"
fi
if grep -q 'td .mono,td code{white-space:nowrap}' "$CSS"; then
  ok "B1: addresses/timestamps are kept on one line inside a data cell"
else
  bad "B1: IP/date cells may still wrap mid-token"
fi
if grep -rq 'break-all' "$TPL"; then
  bad "B2: a template still forces word-break:break-all: $(grep -rl 'break-all' "$TPL" | tr '\n' ' ')"
else
  ok "B2: no template forces break-all (the audited 18 sites are gone)"
fi
if grep -qE 'overflow-wrap: ?break-word' "$TPL/admin/database.html" && grep -qE 'overflow-wrap: ?break-word' "$TPL/exit_rules.html"; then
  ok "B3: the long-token pages (DSN, rule targets) break at boundaries instead of per character"
else
  bad "B3: the DSN/rule-target pages still break per character"
fi
# A wide table needs a scrollable ancestor somewhere: either the template wraps it in
# .table-wrap, or the mobile shell scrolls. body{overflow-x:hidden} clips otherwise.
if grep -q '.shell{overflow-x:auto}' "$CSS"; then
  ok "C1: the mobile shell scrolls, so a bare wide table is not clipped"
else
  bad "C1: no mobile scroller — a bare wide table loses its right-hand columns to body{overflow-x:hidden}"
fi
if grep -rq 'table-wrap' "$TPL"; then
  ok "C1b: templates that opt in still wrap their tables explicitly"
else
  bad "C1b: no template wraps a wide table"
fi
if git ls-files --error-unmatch scripts/check_b357_mobile_table_legibility.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: scripts/check_b357_mobile_table_legibility.sh is NOT tracked by git"
fi
if grep -q 'check_b357_mobile_table_legibility.sh' scripts/verify_pre_deploy.sh; then
  ok "D2: verify_pre_deploy.sh registers B357"
else
  bad "D2: the gate does not run this contract"
fi
if grep -q '^- \*\*B357\*\*' AGENTS.md; then
  ok "D3: AGENTS.md's block index carries B357"
else
  bad "D3: the block index does not know B357"
fi

hdr "B357 summary"
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
