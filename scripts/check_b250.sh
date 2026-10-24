#!/bin/bash
# check_b250.sh — B250 dark-card text contrast fix.
#
# B250 (2026-09-15): operator reported two UI issues on /admin/acls:
#   1. The ACL JSON was rendered on a single line (hard to read).
#   2. The "v0.17.0: TAG-SUBNET-ROUTER" info card had dark text on
#      a dark background (invisible).
#
# The original fix prettified the JSON and forced the info card to a
# hand-picked dark palette with light text overrides
# (--text:#e0f2fe, --text-muted:#bae6fd, --text-subtle:#7dd3fc).
#
# 2026-10-24 CONTRACT RENEGOTIATION (B414). Two reasons.
#   (a) Those hardcoded light colours were themselves a theme-mixing defect: the
#       card's own surface follows the theme, so on the LIGHT themes (#ffffff
#       page) #e0f2fe text on a light tint is unreadable — the same class the
#       operator reported as «цвет текста в новых добавленных полях». The card now
#       paints its surface and its text from tokens (var(--info-bg) / var(--info) /
#       var(--text)), which is readable in all five themes; B414 measures the ratios.
#   (b) Contracts B1-B3 were INERT: they were written as
#       `check "desc" grep -A1 … file | grep -q …`, so the pipeline fed the check's
#       own "PASS" line into the second grep, the row was swallowed, and the
#       summary counted only the rows that survived (5 pass / 0 fail on a tree
#       where all three assertions were false). Trap #9 in another shape: a
#       pipeline must never be the verdict, and a check must never pipe its own
#       output. They now extract the card once and match with a shell builtin.
#
# This script pins:
#   - The handler calls json.Indent on the policy string.
#   - The info card paints surface + text from theme tokens and reintroduces no
#     hardcoded colour.
#   - The B250-era colour literals are gone from every template.
#
# Usage: bash scripts/check_b250.sh

set -u
PASS=0
FAIL=0

if [ -t 1 ]; then
  C_GREEN="\033[32m"; C_RED="\033[31m"; C_RESET="\033[0m"
else
  C_GREEN=""; C_RED=""; C_RESET=""
fi

ok() { echo -e "  ${C_GREEN}PASS${C_RESET}  $1"; PASS=$((PASS + 1)); }
bad() { echo -e "  ${C_RED}FAIL${C_RESET}  $1"; FAIL=$((FAIL + 1)); }
check() { local desc="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi; }

echo "=== A. Source contracts (handler pretty-prints JSON) ==="

# A1. Handler imports encoding/json + bytes.
check "internal/feature/admin/admin_pages.go imports encoding/json" \
  grep -q '"encoding/json"' internal/feature/admin/admin_pages.go
check "internal/feature/admin/admin_pages.go imports bytes" \
  grep -q '"bytes"' internal/feature/admin/admin_pages.go

# A2. Handler calls json.Indent on the policy string.
check "GetAdminACLs pretty-prints via json.Indent" \
  grep -q 'json.Indent' internal/feature/admin/admin_pages.go

# A3. Template wraps Policy in <pre> (preserves the indented newlines).
check "internal/handlers/templates/admin/acls.html wraps raw JSON in <pre> (PolicyView.RawJSON or legacy .Policy)" \
  grep -qE '<pre[^>]*>({{.*PolicyView.RawJSON|.Policy}})' internal/handlers/templates/admin/acls.html

echo ""
echo "=== B. Info card text contrast (acls.html) ==="

# Extract the card: the div line immediately before the title it belongs to.
ACLS_CARD="$(awk '{ prev = cur; cur = $0 } /v0_17_0_note_title/ { print prev; print cur; exit }' \
  internal/handlers/templates/admin/acls.html)"

case "$ACLS_CARD" in
  *'background:var(--info-bg)'*)
    ok "v0.17.0 info card paints its surface from a theme token" ;;
  *)
    bad "v0.17.0 info card surface is not var(--info-bg) — it cannot follow the theme" ;;
esac

case "$ACLS_CARD" in
  *'color:var(--text)'*)
    ok "v0.17.0 info card text is var(--text) (inherits the theme)" ;;
  *)
    bad "v0.17.0 info card text is not var(--text) — a fixed colour is invisible on another theme" ;;
esac

case "$ACLS_CARD" in
  *'color:var(--info)'*)
    ok "v0.17.0 info card title uses var(--info)" ;;
  *)
    bad "v0.17.0 info card title does not use var(--info)" ;;
esac

# The exact regression: any hardcoded hex in the card, or the pre-B414 light overrides.
case "$ACLS_CARD" in
  *'#'*)
    bad "v0.17.0 info card carries a hardcoded colour: $(printf '%s' "$ACLS_CARD" | grep -o '#[0-9a-fA-F]\{3,8\}' | head -3 | tr '\n' ' ')" ;;
  *)
    ok "v0.17.0 info card carries no hardcoded colour" ;;
esac

echo ""
echo "=== C. The B250-era colour literals are gone from every template ==="

B250_LITERALS="$(grep -rlE 'var\(--bg-info,#|var\(--bg-warn,#|#0a2a3a|#3a2a00|#e0f2fe|#bae6fd|#7dd3fc' \
  internal/handlers/templates --include='*.html' 2>/dev/null || true)"
if [ -z "$B250_LITERALS" ]; then
  ok "no template paints an info/warn card with a fixed dark palette or light-text override"
else
  bad "B250-era hardcoded card palette is back:"
  printf '%s\n' "$B250_LITERALS" | sed 's/^/       /'
fi

echo ""
echo "=== B250 summary: $PASS pass / $FAIL fail ==="
[ "$FAIL" -gt 0 ] && exit 1
exit 0
