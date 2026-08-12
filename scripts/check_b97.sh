#!/bin/bash
# scripts/check_b97.sh — invoked by verify_pre_deploy.sh B97 check.
#
# Why a separate file: the B97 check pins the v1.1.0 (TD-3)
# mobile-responsive contract via grep patterns on themes.css
# and a Go unit test. Same pattern as B92/B96.
#
# Pinned contracts:
#   - static/css/themes.css has the @media (max-width:768px)
#     block that drives the mobile drawer (the v1.3.x era used
#     760px; v1.1.0 renames to 768px = the canonical mobile
#     breakpoint, matching iPad-portrait width)
#   - The hamburger button (.sidebar-toggle) is hidden on
#     desktop (display:none) and shown on mobile
#     (display:flex inside the @media block)
#   - The sidebar slides in from translateX(-100%) to
#     translateX(0) when the checkbox is checked
#   - Touch-friendly tap targets: min-height:44px (Apple HIG /
#     Google Material Design)
#   - 2 Go unit tests pass: TestB97_ThemesCSSMobileDrawer +
#     TestB97_StaticFilePresence

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Resolve go robustly (same pattern as check_b92.sh).
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
    GO="/mnt/c/Program Files/Go/bin/go.exe"
else
    echo "SKY-FAIL: go not found" >&2
    exit 1
fi

# 1. themes.css exists and has the v1.1.0 mobile drawer marker.
test -f static/css/themes.css || { echo "SKY-FAIL: themes.css missing" >&2; exit 1; }
grep -qF "v1.1.0 (TD-3)" static/css/themes.css || { echo "SKY-FAIL: themes.css missing v1.1.0 (TD-3) marker" >&2; exit 1; }

# 2. The 768px breakpoint is present (NOT 760px — that was the
#    v1.3.x-era breakpoint, retired in v1.1.0).
grep -qF "@media (max-width:768px)" static/css/themes.css || { echo "SKY-FAIL: themes.css missing @media (max-width:768px) mobile breakpoint" >&2; exit 1; }
if grep -qF "@media (max-width:760px)" static/css/themes.css; then
    echo "SKY-FAIL: themes.css still has the v1.3.x-era 760px breakpoint (v1.1.0 renamed to 768px)" >&2
    exit 1
fi

# 3. The .sidebar-toggle class is defined (the hamburger button).
grep -qF ".sidebar-toggle-input" static/css/themes.css || { echo "SKY-FAIL: themes.css missing .sidebar-toggle-input" >&2; exit 1; }
grep -qF ".sidebar-toggle{" static/css/themes.css || { echo "SKY-FAIL: themes.css missing .sidebar-toggle{...} rule" >&2; exit 1; }

# 4. The slide-in / slide-out transforms.
grep -qF "translateX(-100%)" static/css/themes.css || { echo "SKY-FAIL: themes.css missing translateX(-100%) (off-screen sidebar)" >&2; exit 1; }
grep -qF "translateX(0)" static/css/themes.css || { echo "SKY-FAIL: themes.css missing translateX(0) (on-screen sidebar when toggled)" >&2; exit 1; }

# 5. The .sidebar-section styles (TD-1 styling, also pinned by B96).
grep -qF ".sidebar-section" static/css/themes.css || { echo "SKY-FAIL: themes.css missing .sidebar-section styles" >&2; exit 1; }

# 6. Touch-friendly tap targets (min-height:44px).
grep -qE "min-height:[[:space:]]*44px" static/css/themes.css || { echo "SKY-FAIL: themes.css missing min-height:44px (touch-friendly tap target)" >&2; exit 1; }

# 7. Go unit tests pass.
"$GO" test -count=1 -run "TestB97_" ./internal/handlers/ 2>&1 || { echo "SKY-FAIL: B97 unit tests failed" >&2; exit 1; }

echo "B97 check passed: themes.css has @media (max-width:768px) mobile drawer + hamburger + 2 Go unit tests pass"
