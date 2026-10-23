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
# RENEGOTIATED 2026-10-22 (B411) — same intent, one level up.
# B411 moved the sidebar reservation from `.shell` to <main>: the old
# `main .shell{margin-left:220px}` overrode only the LEFT half of the shell's
# `margin:0 auto`, so `margin-right:auto` swallowed every spare pixel and the
# content stayed pinned to the sidebar while the blank band grew on the right
# (measured 520px at 1920, 1160px at 2560). The breadcrumb is a SIBLING inside
# <main>, so it now inherits that offset — which means B120's mirrored 220px/52px
# margins would be applied TWICE and must be gone. What this script still
# guarantees is the B120 outcome: the breadcrumb is never under the sidebar.
#
# What this script verifies:
#   A. themes.css: <main> carries `margin-left:220px` (desktop expanded) AND no
#      `main .shell{margin-left:<px>}` rule survives (that rule is the band).
#   B. themes.css: `.sidebar.collapsed ~ main{margin-left:52px}` (desktop
#      collapsed — the column moves, children follow).
#   C. themes.css: @media (max-width:768px) block resets `main` to
#      `margin-left:0 !important` (mobile drawer, no column reserved).
#   C2. themes.css: the breadcrumb has no offset of its own (`margin-left:0`,
#      `width:100%`) — the double-offset guard.
#   D. layout.html: <nav class="admin-breadcrumb"> comes BEFORE
#      <div class="shell"> (the breadcrumb is a SIBLING of
#      .shell, not nested — so the inherited offset applies
#      to both elements independently).
#   E. Go unit tests (TestB411_*, which absorbed the B120 assertions) in
#      internal/handlers/ pass for both the embedded stylesheet and the root
#      mirror.
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
# Contract A: themes.css — desktop expanded reservation on <main> (220px)
# ------------------------------------------------------------------------------
echo
echo "=== A. themes.css: desktop expanded reservation on <main> (220px) ==="
if grep -E '^[[:space:]]*main[[:space:]]*\{[^}]*margin-left[[:space:]]*:[[:space:]]*220px' \
   "${THEMES_CSS}" >/dev/null; then
    ok "main { margin-left: 220px } (desktop expanded — the reservation moved here in B411)"
else
    bad "missing <main> { margin-left: 220px } — nothing keeps the content out from under the fixed sidebar"
fi
if grep -E '^[[:space:]]*main[[:space:]]+\.shell[[:space:]]*\{[^}]*margin-left[[:space:]]*:[[:space:]]*[0-9]+px' \
   "${THEMES_CSS}" >/dev/null; then
    bad "main .shell{margin-left:<N>px} is back — it overrides only the left half of margin:0 auto, so all spare width goes to the RIGHT (the empty band of B411)"
else
    ok "no main .shell{margin-left:<N>px} rule — the shell stays centred inside <main>"
fi

# ------------------------------------------------------------------------------
# Contract B: themes.css — desktop collapsed reservation (52px) on <main>
# ------------------------------------------------------------------------------
echo
echo "=== B. themes.css: desktop collapsed reservation (52px) ==="
if grep -E '\.sidebar\.collapsed[[:space:]]*~[[:space:]]*main[[:space:]]*\{[^}]*margin-left[[:space:]]*:[[:space:]]*52px' \
   "${THEMES_CSS}" >/dev/null; then
    ok ".sidebar.collapsed ~ main { margin-left: 52px } (desktop collapsed — the column moves, its children follow)"
else
    bad "missing `.sidebar.collapsed ~ main { margin-left: 52px }` (B411 desktop collapsed)"
fi

# ------------------------------------------------------------------------------
# Contract C: themes.css — mobile @media reset (margin-left: 0) on <main>
# ------------------------------------------------------------------------------
echo
echo "=== C. themes.css: mobile @media reservation reset (0) ==="
# Find the @media (max-width:768px) block and check it resets <main>.
if awk '
    /@media \(max-width:768px\)/ { in_media = 1 }
    in_media { buf = buf "\n" $0 }
    in_media && /^\}/ { print buf; buf = ""; in_media = 0 }
' "${THEMES_CSS}" | grep -E '^[[:space:]]*main[[:space:]]*\{[^}]*margin-left[[:space:]]*:[[:space:]]*0[[:space:]]*!important' >/dev/null; then
    ok "@media (max-width:768px) resets <main> to margin-left:0!important (mobile drawer, no column)"
else
    bad "missing @media (max-width:768px) main { margin-left: 0 !important } — the whole page would stay shifted on a phone"
fi

# ------------------------------------------------------------------------------
# Contract C2: the breadcrumb must NOT carry an offset of its own (B411)
# ------------------------------------------------------------------------------
echo
echo "=== C2. themes.css: no double offset on the breadcrumb ==="
BC_BODY=$(awk '
    /^[[:space:]]*main[[:space:]]+\.admin-breadcrumb[[:space:]]*\{/ { inside = 1 }
    inside { buf = buf $0 }
    inside && /\}/ { print buf; exit }
' "${THEMES_CSS}")
if [ -z "${BC_BODY}" ]; then
    bad "missing the desktop main .admin-breadcrumb{...} rule"
else
    case "${BC_BODY}" in
      *"margin-left:0"*) ok "main .admin-breadcrumb states margin-left:0 (no offset of its own)" ;;
      *) bad "the breadcrumb's desktop rule does not state margin-left:0 — an inherited <main> offset may be doubled" ;;
    esac
    case "${BC_BODY}" in
      *"220px"*|*"52px"*) bad "the breadcrumb still carries a sidebar offset (${BC_BODY}) — <main> already provides it, so it would be applied twice" ;;
      *) ok "no 220/52px offset on the breadcrumb (the B120 mirror is gone, as B411 requires)" ;;
    esac
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
# Contract E: Go unit tests in internal/handlers/ pass
# ------------------------------------------------------------------------------
# B411 absorbed the B120 assertions into TestB411_* (same intent, new carrier:
# <main> instead of the breadcrumb). Capture the output first, then match — a
# `go test | grep -q` pipeline dies on SIGPIPE under pipefail (AGENTS trap #9).
echo
echo "=== E. Go unit tests pass (TestB411_* — the B120 intent, renegotiated) ==="
if [ -f "internal/handlers/layout_v1_3_19_2_test.go" ]; then
    if command -v go >/dev/null 2>&1; then
        OUT="$(go test -count=1 -short ./internal/handlers/ -run 'TestB411_' 2>&1)"
        rc=$?
        # Match with a shell builtin, never `printf "$VAR" | grep -q` (B386/B388:
        # the pipeline's status is not the producer's, so a host transient becomes a
        # product verdict — AGENTS trap #9/#15).
        case "$OUT" in
          "ok "[[:space:]]*|*$'\n'"ok "[[:space:]]*) T_OK=1 ;;
          *) T_OK=0 ;;
        esac
        if [ "$T_OK" -eq 1 ]; then
            ok "TestB411_* Go unit tests pass (pinned at source-code level)"
        elif [ "${rc}" -ne 0 ]; then
            bad "TestB411_* Go unit tests failed — re-run with -v to see which contract regressed"
        else
            # rc==0 but no 'ok' line: the filter matched nothing (the B322 C1 class).
            bad "the -run 'TestB411_' filter matched no test — the pin is not running"
        fi
    else
        warn "go not on PATH — skipping the Go test verification (the other contracts still hold)"
    fi
else
    bad "internal/handlers/layout_v1_3_19_2_test.go not found"
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
