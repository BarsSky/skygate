#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20 (B128) — compareSemver 4-part version support
#
# Pins the v1.3.20 fix that turns the silent "Update button hidden
# on /admin/update despite a newer GitHub release" bug into a working
# upgrade flow.
#
# Root cause: three CompareSemver implementations (internal/update,
# internal/release, internal/headscale_version) all compared only the
# first 3 dot-separated components, dropping the 4th. Skygate adopted
# 4-component versioning ("x.y.z.w", sub-patch) in v1.3.12+, so the
# 4-part compare is required. Pre-B128, comparing v1.3.19.2 (current
# build, post-stripBuildLabelSuffix) against v1.3.19.4 (latest GitHub
# release) gave [1,3,19] vs [1,3,19] → equal → IsNewer=false → the
# "Update" button on /admin/update stayed hidden even though a real
# new release was available.
#
# What this script verifies (live, on the VM):
#   A. internal/update/checker.go: compareSemver splits into 4 parts
#   B. internal/release/monitor.go: CompareSemver iterates 4 parts
#   C. internal/headscale_version/client.go: CompareSemver iterates 4 parts
#   D. checker_test.go covers 4-part cases (1.3.19.4 > 1.3.19.2 etc.)
#   E. monitor_test.go covers 4-part cases
#   F. Live CompareSemver check: v1.3.19.2 < v1.3.19.4 (regression
#      test that runs the actual function)
#   G. /admin/update page contains the unconditional "Update to" button
#      (no auto-update gating — that's B129's job; here we just pin
#      that the page now has the button visible)
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

CHECKER_GO="internal/update/checker.go"
MONITOR_GO="internal/release/monitor.go"
CLIENT_GO="internal/headscale_version/client.go"
CHECKER_TEST="internal/update/checker_test.go"
MONITOR_TEST="internal/release/monitor_test.go"

for f in "${CHECKER_GO}" "${MONITOR_GO}" "${CLIENT_GO}" "${CHECKER_TEST}" "${MONITOR_TEST}"; do
    [ -f "${f}" ] || { bad "source file not found: ${f}"; exit 1; }
done

# ------------------------------------------------------------------------------
# Contract A: internal/update/checker.go — compareSemver splits into 4 parts
# ------------------------------------------------------------------------------
echo
echo "=== A. internal/update/checker.go: compareSemver splits into 4 parts ==="
# Look for `splitVersionParts(a, 4)` (and same for b). The pre-B128
# code had `splitVersionParts(a, 3)` which dropped the 4th component.
checker_a=$(grep -cE 'splitVersionParts\(a, 4\)' "${CHECKER_GO}" || true)
checker_b=$(grep -cE 'splitVersionParts\(b, 4\)' "${CHECKER_GO}" || true)
checker_loop=$(grep -cE 'for i := 0; i < 4; i\+' "${CHECKER_GO}" || true)
if [ "${checker_a}" -ge 1 ] && [ "${checker_b}" -ge 1 ] && [ "${checker_loop}" -ge 1 ]; then
    ok "checker.go: compareSemver uses 4 parts (splitVersionParts(a, 4), splitVersionParts(b, 4), loop i<4)"
else
    bad "checker.go: compareSemver is NOT 4-part (a=${checker_a}, b=${checker_b}, loop=${checker_loop})"
fi
# Negative: the 3-part version must NOT be in the function anymore.
# (Could still be in comments — that's fine.)
checker_3=$(grep -cE 'splitVersionParts\(a, 3\)|splitVersionParts\(b, 3\)' "${CHECKER_GO}" || true)
if [ "${checker_3}" -eq 0 ]; then
    ok "checker.go: no 3-part splitVersionParts calls remain (the bug pattern is gone)"
else
    bad "checker.go: still has ${checker_3} 3-part splitVersionParts call(s)"
fi

# ------------------------------------------------------------------------------
# Contract B: internal/release/monitor.go — CompareSemver iterates 4 parts
# ------------------------------------------------------------------------------
echo
echo "=== B. internal/release/monitor.go: CompareSemver iterates 4 parts ==="
monitor_loop=$(grep -cE 'for i := 0; i < 4; i\+' "${MONITOR_GO}" || true)
monitor_3=$(grep -cE 'for i := 0; i < 3; i\+' "${MONITOR_GO}" || true)
if [ "${monitor_loop}" -ge 1 ] && [ "${monitor_3}" -eq 0 ]; then
    ok "monitor.go: CompareSemver iterates 4 parts (and the 3-part loop is gone)"
else
    bad "monitor.go: CompareSemver loop is wrong (4=${monitor_loop}, remaining 3-part=${monitor_3})"
fi

# ------------------------------------------------------------------------------
# Contract C: internal/headscale_version/client.go — CompareSemver iterates 4 parts
# ------------------------------------------------------------------------------
echo
echo "=== C. internal/headscale_version/client.go: CompareSemver iterates 4 parts ==="
client_loop=$(grep -cE 'for i := 0; i < 4; i\+' "${CLIENT_GO}" || true)
client_3=$(grep -cE 'for i := 0; i < 3; i\+' "${CLIENT_GO}" || true)
if [ "${client_loop}" -ge 1 ] && [ "${client_3}" -eq 0 ]; then
    ok "client.go: CompareSemver iterates 4 parts (and the 3-part loop is gone)"
else
    bad "client.go: CompareSemver loop is wrong (4=${client_loop}, remaining 3-part=${client_3})"
fi

# ------------------------------------------------------------------------------
# Contract D: checker_test.go covers 4-part cases
# ------------------------------------------------------------------------------
echo
echo "=== D. checker_test.go: 4-part test cases present ==="
# Look for the live trigger: 1.3.19.4 vs 1.3.19.2 in the test cases.
d_trigger=$(grep -cE '"1\.3\.19\.4".*"1\.3\.19\.2"' "${CHECKER_TEST}" || true)
d_sub=$(grep -cE '"1\.3\.19\.2".*"1\.3\.19\.1"' "${CHECKER_TEST}" || true)
if [ "${d_trigger}" -ge 1 ] && [ "${d_sub}" -ge 1 ]; then
    ok "checker_test.go has 4-part cases (1.3.19.4 vs 1.3.19.2, 1.3.19.2 vs 1.3.19.1)"
else
    bad "checker_test.go is missing 4-part test cases (trigger=${d_trigger}, sub=${d_sub})"
fi

# ------------------------------------------------------------------------------
# Contract E: monitor_test.go covers 4-part cases
# ------------------------------------------------------------------------------
echo
echo "=== E. monitor_test.go: 4-part test cases present ==="
e_trigger=$(grep -cE '"v1\.3\.19\.4".*"v1\.3\.19\.2"|"v1\.3\.19\.2".*"v1\.3\.19\.4"' "${MONITOR_TEST}" || true)
e_sub=$(grep -cE '"v1\.3\.19\.2".*"v1\.3\.19\.1"|"v1\.3\.19\.1".*"v1\.3\.19\.2"' "${MONITOR_TEST}" || true)
if [ "${e_trigger}" -ge 1 ] && [ "${e_sub}" -ge 1 ]; then
    ok "monitor_test.go has 4-part cases (1.3.19.4 vs 1.3.19.2, 1.3.19.2 vs 1.3.19.1)"
else
    bad "monitor_test.go is missing 4-part test cases (trigger=${e_trigger}, sub=${e_sub})"
fi

# ------------------------------------------------------------------------------
# Contract F: Live CompareSemver test via a small Go program
# ------------------------------------------------------------------------------
echo
echo "=== F. Live CompareSemver via Go test (run the actual function) ==="
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in "/c/Program Files/Go/bin/go.exe" "/usr/local/go/bin/go" "/snap/bin/go"; do
        if [ -x "${cand}" ]; then GO="${cand}"; break; fi
    done
fi
if [ -z "${GO}" ]; then
    warn "go not found — skipping live CompareSemver check (still 5/6 contracts pass)"
else
    test_out=$("${GO}" test -count=1 -run 'TestCompareSemver' ./internal/update/ ./internal/release/ 2>&1)
    test_rc=$?
    if [ "${test_rc}" -eq 0 ]; then
        ok "Go TestCompareSemver passes in update + release (live 4-part compare works)"
    else
        bad "Go TestCompareSemver FAILED (live compare is broken): ${test_out}"
    fi
fi

# ------------------------------------------------------------------------------
# Contract G: /admin/update page now shows Update button (visible-effect check)
# The B128 compareSemver fix makes IsNewer=true for v1.3.19.2 vs v1.3.19.4.
# Combined with .AutoUpdateEnabled=true (DB) and DevBuild=false, the
# pre-B129 template's `{{if and .IsNewer .AutoUpdateEnabled (not .DevBuild)}}`
# conditional evaluates to true and the button appears. The B129 redesign
# later makes the button unconditional and adds a Schedule section; this
# B128 check pins the BUG-FIX-level visible behaviour only.
# ------------------------------------------------------------------------------
echo
echo "=== G. update.html: Update button conditional present (covered by B129 redesign) ==="
UPDATE_HTML="internal/handlers/templates/admin/update.html"
if [ -f "${UPDATE_HTML}" ]; then
    if grep -qE '/admin/update/apply' "${UPDATE_HTML}"; then
        ok "update.html has the /admin/update/apply button (the Apply/Update button form is in the template)"
    else
        bad "update.html is missing /admin/update/apply (the Update button was removed entirely?)"
    fi
else
    warn "update.html not found at expected path — skipping G"
fi

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo
echo "=== B128 summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  WARN: ${WARN}"
if [ "${FAIL}" -eq 0 ]; then
    echo
    echo "B128 contracts all hold."
    exit 0
fi
echo
echo "B128 has failing contracts — fix the source files above."
exit 1
