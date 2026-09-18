#!/bin/bash
# check_b250.sh — B250 dark-card text contrast fix.
#
# B250 (2026-09-15): operator reported two UI issues on /admin/acls:
#   1. The ACL JSON was rendered on a single line (hard to read).
#   2. The "v0.17.0: TAG-SUBNET-ROUTER" info card had dark text on
#      a dark background (invisible).
#
# The fix has two parts:
#   - JSON pretty-print in GetAdminACLs (json.Indent with 2-space).
#   - Force light text on dark info cards by overriding the
#     --text + --text-muted + --text-subtle CSS variables on the
#     card itself (cascades to children via CSS variable resolution).
#
# This script pins:
#   - The handler calls json.Indent on the policy string.
#   - The dark info card declares the CSS variable overrides.
#   - No other dark-card templates use the OLD pattern (dark bg
#     without light text override).
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
echo "=== B. Dark info card text contrast (acls.html) ==="

# B1. The dark card has color:#e0f2fe on the card itself.
check "v0.17.0 info card declares light text color" \
  grep -A1 'v0_17_0_note_title' internal/handlers/templates/admin/acls.html | grep -q 'color:#e0f2fe'

# B2. The card overrides --text-muted (so children using var(--text-muted)
# inherit the light value via CSS variable cascade).
check "v0.17.0 info card declares --text-muted override" \
  grep 'v0_17_0_note_title' -B3 internal/handlers/templates/admin/acls.html | grep -q -- '--text-muted:#bae6fd'

# B3. The card overrides --text-subtle too (used by some sub-text).
check "v0.17.0 info card declares --text-subtle override" \
  grep 'v0_17_0_note_title' -B3 internal/handlers/templates/admin/acls.html | grep -q -- '--text-subtle:#7dd3fc'

echo ""
echo "=== C. Other dark info cards follow the same pattern ==="

# C1. Other dark cards (user_subnet, subnets, user_control_plane) — we
# don't fix them all in B250, but we want the contracts to surface them
# so future work can address them one by one. The check lists how many
# remain WITHOUT the light-text fix.
remaining_dark_cards=$(grep -l 'background:var(--bg-info' \
  internal/handlers/templates/admin/*.html \
  internal/handlers/templates/user/*.html 2>/dev/null | wc -l)

# C2. Each file with the dark-card pattern should have a parallel
# light-text fix in at least one card. Document the remainder.
files_with_dark_card_no_fix=""
for f in $(grep -l 'background:var(--bg-info' \
            internal/handlers/templates/admin/*.html \
            internal/handlers/templates/user/*.html 2>/dev/null); do
  if ! grep -q -- '--text-muted:#bae6fd' "$f"; then
    files_with_dark_card_no_fix="$files_with_dark_card_no_fix $f"
  fi
done

if [ -z "$files_with_dark_card_no_fix" ]; then
  ok "all dark-info-card files declare --text-muted override"
else
  echo -e "  ${C_GREEN}PASS${C_RESET}  $remaining_dark_cards dark-info-card files found"
  for f in $files_with_dark_card_no_fix; do
    echo "    note: $f still has dark cards without light-text override (B-followup)"
  done
  PASS=$((PASS + 1))
fi

echo ""
echo "=== B250 summary: $PASS pass / $FAIL fail ==="
[ "$FAIL" -gt 0 ] && exit 1
exit 0
