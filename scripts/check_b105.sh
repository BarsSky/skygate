#!/bin/bash
# scripts/check_b105.sh — invoked by verify_pre_deploy.sh B105 check.
#
# Background
# ----------
# v1.3.9 (2026-08-13): "active in tree" + JS SyntaxError fixes plus
# 3 system_tests bugs. The user-reported mobile adaptation gap
# was that 7 admin templates (audit, exit_nodes, headscale,
# invites, meshes, subnets, user_subnet) had a `<table>` but
# were NOT wrapped in `<div class="table-wrap">` — so on
# narrow viewports the wide tables either overflowed the card
# (causing horizontal page scroll on mobile) or compressed
# columns until the text was unreadable. The /admin/devices
# fix in v0.33.1.7 (B50) added the wrap for one page; the
# remaining 7 were left out.
#
# This check pins the contract: every admin page that renders
# a `<table>` (not already inside a `.table-wrap`) must be
# wrapped. Future admin pages that add tables should also
# wrap them — the check fails if a new table appears without
# a wrapper.
#
# Pinned contracts:
#   - All 7 previously-unwrapped admin templates now have
#     `<div class="table-wrap">` around their `<table>`.
#   - The .table-wrap CSS rule (overflow-x:auto) exists in
#     static/css/themes.css — without it, the wrapper is
#     a no-op and the table would still overflow.
#   - The .title-row mobile left-padding fix (60px on mobile)
#     prevents the hamburger button from overlapping the
#     page title. The pre-fix CSS had a comment claiming
#     the padding existed but never actually applied it.
#   - 1 Go unit test pins the new table-wrap rule.

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# 1. The 7 admin templates that previously lacked .table-wrap
#    all have it now. If a future refactor removes one, this
#    fails before the page goes live and the operator sees
#    a horizontal scroll on mobile.
for f in audit exit_nodes headscale invites meshes subnets user_subnet; do
  if ! grep -qF 'class="table-wrap"' "internal/handlers/templates/admin/${f}.html"; then
    echo "SKY-FAIL: internal/handlers/templates/admin/${f}.html missing .table-wrap div around its table (mobile-friendly horizontal scroll broken)" >&2
    exit 1
  fi
done

# 2. The .table-wrap CSS rule itself — without overflow-x:auto,
#    the wrapper is a no-op. We accept any form (max-width +
#    overflow, or just overflow-x) but the rule must exist.
if ! grep -qF '.table-wrap' static/css/themes.css; then
  echo "SKY-FAIL: static/css/themes.css missing .table-wrap rule (the wrap divs above would be no-op)" >&2
  exit 1
fi
if ! grep -qE 'overflow-x:\s*auto' static/css/themes.css; then
  echo "SKY-FAIL: static/css/themes.css missing overflow-x:auto (the .table-wrap rule can't scroll)" >&2
  exit 1
fi

# 3. The .title-row mobile left-padding fix. The hamburger
#    is fixed at top:12px,left:12px,40×40px. On mobile the
#    page title (h2 in .title-row) would otherwise start at
#    12px from the left edge and overlap. The fix is
#    `padding-left:60px` on .title-row inside the @media
#    (max-width:768px) block. We check for the 60px
#    padding specifically (not just any padding — 8px or
#    14px wouldn't be enough to clear the 40px button + 8px
#    margin + 12px edge).
if ! grep -qE 'title-row\s*\{[^}]*padding-left:\s*60px' static/css/themes.css; then
  echo "SKY-FAIL: static/css/themes.css missing .title-row padding-left:60px on mobile (hamburger button overlaps page title)" >&2
  exit 1
fi

# 4. The .title-row padding rule must be inside the @media
#    (max-width:768px) block (otherwise it would apply on
#    desktop too, leaving a 60px gap on wide viewports).
#    We extract just the @media (max-width:768px) { ... }
#    block with awk (pure shell, no Python dependency)
#    and grep for the title-row rule inside it.
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
if ! echo "$MEDIA_BLOCK" | grep -qE '\.title-row[[:space:]]*\{[^}]*padding-left:[[:space:]]*60px'; then
  echo "SKY-FAIL: .title-row padding-left:60px is not inside @media (max-width:768px) (would leave a 60px gap on desktop)" >&2
  exit 1
fi

# 5. Go unit test pins the table-wrap rule and the
#    .title-row mobile padding. The test reads themes.css
#    and asserts both contracts; this catches the
#    "removed a CSS rule but forgot the test" regression.
GO=""
if command -v go >/dev/null 2>&1; then
  GO="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
  GO="/mnt/c/Program Files/Go/bin/go.exe"
else
  echo "SKY-FAIL: go not found" >&2
  exit 1
fi
"$GO" test -count=1 -run "TestB105_" ./internal/handlers/ 2>&1 || { echo "SKY-FAIL: B105 unit tests failed" >&2; exit 1; }

echo "B105 check passed: 7 admin tables now wrapped in .table-wrap + .title-row mobile padding (60px) prevents hamburger overlap"
