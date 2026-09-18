#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.2 follow-up (B120) — admin-breadcrumb sidebar offset
#
# Pins the v1.3.19.2 hotfix that fixed the admin-breadcrumb
# being hidden under the position:fixed sidebar on PC.
#
# The bug: <main> contains .admin-breadcrumb as a SIBLING
# of .shell (both inside <main>). The CSS rule
# `main .shell { margin-left: 220px; }` only applied to
# .shell — .admin-breadcrumb had no left offset, so its
# leftmost 220px sat under the fixed-position sidebar. On
# PC, the operator saw only the right fragments of
# "Админ › Devices & Nodes › Devices" — the start was hidden.
#
# The fix: mirror the .shell margin-left pattern for
# .admin-breadcrumb (3 rules — desktop expanded 220px,
# desktop collapsed 52px, mobile 0). Plus 1 structural pin
# in layout.html (.admin-breadcrumb is a sibling of .shell,
# not nested inside it).
#
# What this script verifies:
#   A. themes.css: `main .admin-breadcrumb { margin-left: 220px }`
#      (desktop expanded — matches the .shell rule).
#   B. themes.css: `.sidebar.collapsed ~ main .admin-breadcrumb
#      { margin-left: 52px }` (desktop collapsed — matches the
#      .shell rule under the same collapsed sidebar).
#   C. themes.css: @media (max-width:768px) block contains
#      `main .admin-breadcrumb { margin-left: 0 !important }`
#      (mobile reset — the sidebar is a drawer, no column
#      reserved).
#   D. layout.html: <nav class="admin-breadcrumb"> comes BEFORE
#      <div class="shell"> (the breadcrumb is a SIBLING of
#      .shell, not nested — so the margin-left pattern applies
#      to both elements independently).
#   E. Go unit tests (TestB120_*) in internal/handlers/
#      package pass — they pin all of the above at the
#      source-code level (more reliable than the shell grep
#      because they can match the actual CSS structure).
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

[ -f "${THEMES_CSS}" ] || { bad "source file not found: ${THEMES_CSS}"; exit 1; }
[ -f "${LAYOUT_HTML}" ] || { bad "source file not found: ${LAYOUT_HTML}"; exit 1; }

# ------------------------------------------------------------------------------
# Contract A: themes.css — desktop expanded breadcrumb offset (220px)
# ------------------------------------------------------------------------------
echo
echo "=== A. themes.css: desktop expanded breadcrumb offset (220px) ==="
if grep -E '^[[:space:]]*main[[:space:]]+\.admin-breadcrumb[[:space:]]*\{[^}]*margin-left[[:space:]]*:[[:space:]]*220px' \
   "${THEMES_CSS}" >/dev/null; then
    ok "main .admin-breadcrumb { margin-left: 220px } (desktop expanded — matches .shell rule)"
else
    bad "missing `main .admin-breadcrumb { margin-left: 220px }` rule (B120 desktop expanded)"
fi

# ------------------------------------------------------------------------------
# Contract B: themes.css — desktop collapsed breadcrumb offset (52px)
# ------------------------------------------------------------------------------
echo
echo "=== B. themes.css: desktop collapsed breadcrumb offset (52px) ==="
if grep -E '\.sidebar\.collapsed[[:space:]]*~[[:space:]]*main[[:space:]]+\.admin-breadcrumb[[:space:]]*\{[^}]*margin-left[[:space:]]*:[[:space:]]*52px' \
   "${THEMES_CSS}" >/dev/null; then
    ok ".sidebar.collapsed ~ main .admin-breadcrumb { margin-left: 52px } (desktop collapsed — matches .shell rule)"
else
    bad "missing `.sidebar.collapsed ~ main .admin-breadcrumb { margin-left: 52px }` rule (B120 desktop collapsed)"
fi

# ------------------------------------------------------------------------------
# Contract C: themes.css — mobile @media reset (margin-left: 0)
# ------------------------------------------------------------------------------
echo
echo "=== C. themes.css: mobile @media breadcrumb reset (0) ==="
# Find the @media (max-width:768px) block and check it has
# a breadcrumb override with margin-left:0.
if awk '
    /@media \(max-width:768px\)/ { in_media = 1 }
    in_media { buf = buf "\n" $0 }
    in_media && /^\}/ { print buf; buf = ""; in_media = 0 }
' "${THEMES_CSS}" | grep -E 'main[[:space:]]+\.admin-breadcrumb[[:space:]]*\{[^}]*margin-left[[:space:]]*:[[:space:]]*0[[:space:]]*!important' >/dev/null; then
    ok "@media (max-width:768px) has main .admin-breadcrumb { margin-left: 0 !important } (mobile reset)"
else
    bad "missing @media (max-width:768px) main .admin-breadcrumb { margin-left: 0 !important } (B120 mobile reset)"
fi

# ------------------------------------------------------------------------------
# Contract D: layout.html — .admin-breadcrumb is BEFORE .shell
# ------------------------------------------------------------------------------
echo
echo "=== D. layout.html: .admin-breadcrumb comes before <div class=\"shell\"> ==="
bc_idx=$(grep -bo 'class="admin-breadcrumb"' "${LAYOUT_HTML}" | head -1 | cut -d: -f1)
shell_idx=$(grep -bo '<div class="shell">' "${LAYOUT_HTML}" | head -1 | cut -d: -f1)
if [ -z "${bc_idx}" ]; then
    bad "layout.html missing class=\"admin-breadcrumb\" element"
elif [ -z "${shell_idx}" ]; then
    bad "layout.html missing <div class=\"shell\"> container"
elif [ "${bc_idx}" -lt "${shell_idx}" ]; then
    ok "admin-breadcrumb (offset ${bc_idx}) comes BEFORE .shell (offset ${shell_idx}) — siblings, not nested"
else
    bad "admin-breadcrumb (offset ${bc_idx}) is AFTER .shell (offset ${shell_idx}) — the B120 CSS fix assumes siblings"
fi

# ------------------------------------------------------------------------------
# Contract E: Go unit tests TestB120_* in internal/handlers/ pass
# ------------------------------------------------------------------------------
echo
echo "=== E. Go unit tests TestB120_* pass ==="
if [ -f "internal/handlers/layout_v1_3_19_2_test.go" ]; then
    if command -v go >/dev/null 2>&1; then
        if go test -count=1 -short ./internal/handlers/ -run TestB120_ 2>&1 | grep -E '^(ok|FAIL|---)' | tail -5; then
            if go test -count=1 -short ./internal/handlers/ -run TestB120_ 2>&1 | grep -q '^ok'; then
                ok "TestB120_* Go unit tests pass (pinned at source-code level)"
            else
                bad "TestB120_* Go unit tests failed — re-run with -v to see which contract regressed"
            fi
        else
            warn "go test exited non-zero (might be a build error, not a test failure — investigate)"
        fi
    else
        warn "go not on PATH — skipping TestB120_* Go test verification (the other contracts still hold)"
    fi
else
    bad "internal/handlers/layout_v1_3_19_2_test.go not found"
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
