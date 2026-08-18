#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.2 follow-up (B124) — dev version element + semver suffix fix
#
# Pins the v1.3.19.2 follow-up that:
#   1. Adds SKYGATE_DEV_BUILD env var. When set, /admin/update
#      shows a "dev build" banner instead of the "update
#      available" alert and hides the auto-apply button.
#   2. Fixes compareSemver in internal/update/checker.go so
#      the git-describe "-N-g<hex>" suffix is stripped before
#      comparison. Before B124, "1.3.9" miscompared as > than
#      "1.3.11-27-g03a1d97" (the lex fallback put "9" > "11-..."
#      because '9' > '1').
#
# What this script verifies (live, on the VM):
#   A. DevBuild field exists in config.Config
#   B. SKYGATE_DEV_BUILD env var is read (true → DevBuild=true)
#   C. Service.DevBuild is wired from cfg.DevBuild
#   D. /admin/update template renders the dev banner when
#      DevBuild=true (has the i18n key + the div with the class)
#   E. /admin/update template does NOT render the "update
#      available" alert when DevBuild=true (gated by {{if and
#      .IsNewer (not .DevBuild)}})
#   F. /admin/update template does NOT render the auto-apply
#      button when DevBuild=true (gated by {{if and .IsNewer
#      .AutoUpdateEnabled (not .DevBuild)}})
#   G. compareSemver strips git-describe "-N-g<hex>" suffix
#      before fallback compare (TestCompareSemver cases pass)
#   H. compareSemver preserves pre-release markers (no false
#      stripping of "0.29.0-rc.1")
#   I. Go tests for DevBuild config field pass
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

CONFIG_GO="internal/config/config.go"
SERVICE_GO="internal/feature/admin/service.go"
MAIN_GO="cmd/skygate/main.go"
TEMPLATE="internal/handlers/templates/admin/update.html"
CATALOG="internal/i18n/catalog_update.go"
CHECKER_GO="internal/update/checker.go"
CHECKER_TEST_GO="internal/update/checker_test.go"
CONFIG_TEST_GO="internal/config/dev_build_test.go"

[ -f "${CONFIG_GO}" ]      || { bad "source file not found: ${CONFIG_GO}"; exit 1; }
[ -f "${SERVICE_GO}" ]     || { bad "source file not found: ${SERVICE_GO}"; exit 1; }
[ -f "${MAIN_GO}" ]        || { bad "source file not found: ${MAIN_GO}"; exit 1; }
[ -f "${TEMPLATE}" ]        || { bad "source file not found: ${TEMPLATE}"; exit 1; }
[ -f "${CATALOG}" ]        || { bad "source file not found: ${CATALOG}"; exit 1; }
[ -f "${CHECKER_GO}" ]      || { bad "source file not found: ${CHECKER_GO}"; exit 1; }
[ -f "${CHECKER_TEST_GO}" ] || { bad "source file not found: ${CHECKER_TEST_GO}"; exit 1; }
[ -f "${CONFIG_TEST_GO}" ]  || { bad "source file not found: ${CONFIG_TEST_GO}"; exit 1; }

# ------------------------------------------------------------------------------
# Contract A: DevBuild field exists in config.Config
# ------------------------------------------------------------------------------
echo
echo "=== A. config.Config has DevBuild field ==="
if grep -qE '^\s*DevBuild\s+bool' "${CONFIG_GO}"; then
    ok "Config.DevBuild bool field exists"
else
    bad "Config.DevBuild field missing — /admin/update can't render the dev banner"
fi

# ------------------------------------------------------------------------------
# Contract B: SKYGATE_DEV_BUILD env var is read (true → DevBuild=true)
# ------------------------------------------------------------------------------
echo
echo "=== B. SKYGATE_DEV_BUILD env var → cfg.DevBuild ==="
if grep -qE 'SKYGATE_DEV_BUILD' "${CONFIG_GO}"; then
    ok "SKYGATE_DEV_BUILD is referenced in config.go"
else
    bad "SKYGATE_DEV_BUILD env var is not read"
fi
if grep -qE 'DevBuild:\s+getenv\("SKYGATE_DEV_BUILD"' "${CONFIG_GO}"; then
    ok "DevBuild is set via getenv(\"SKYGATE_DEV_BUILD\")"
else
    bad "DevBuild is not wired from getenv(\"SKYGATE_DEV_BUILD\")"
fi

# ------------------------------------------------------------------------------
# Contract C: Service.DevBuild is wired from cfg.DevBuild
# ------------------------------------------------------------------------------
echo
echo "=== C. adminSvc.Service.DevBuild is wired from cfg.DevBuild ==="
if grep -qE '^\s*DevBuild\s+bool' "${SERVICE_GO}"; then
    ok "Service.DevBuild field exists"
else
    bad "Service.DevBuild field missing"
fi
if grep -qE 'DevBuild:\s+app\.Config\(\)\.DevBuild' "${MAIN_GO}"; then
    ok "main.go wires adminSvc.DevBuild from app.Config().DevBuild"
else
    bad "main.go does NOT wire adminSvc.DevBuild from cfg.DevBuild"
fi
# Service passthrough to template
if grep -qE '"DevBuild":\s+s\.DevBuild' "internal/feature/admin/update.go"; then
    ok "update.go passes DevBuild to the template data dict"
else
    bad "update.go does NOT pass DevBuild to the template data dict"
fi

# ------------------------------------------------------------------------------
# Contract D: /admin/update template renders the dev banner
# ------------------------------------------------------------------------------
echo
echo "=== D. /admin/update template renders the dev banner ==="
if grep -qE 'banner_dev_build' "${TEMPLATE}"; then
    ok "update.html references the banner_dev_build i18n key"
else
    bad "update.html does NOT reference the banner_dev_build i18n key"
fi
if grep -qE '\{\{if \.DevBuild\}\}' "${TEMPLATE}"; then
    ok "update.html has a {{if .DevBuild}} block"
else
    bad "update.html is missing the {{if .DevBuild}} block"
fi
# i18n keys exist in both RU and EN
ru_keys=$(grep -c '"update.banner_dev_build"' "${CATALOG}")
if [ "${ru_keys}" -ge 2 ]; then
    ok "banner_dev_build i18n key present in both RU + EN (${ru_keys} occurrences)"
else
    bad "banner_dev_build i18n key missing or only in one language (${ru_keys} occurrences, want >=2)"
fi

# ------------------------------------------------------------------------------
# Contract E: "update available" alert is hidden when DevBuild=true
# ------------------------------------------------------------------------------
echo
echo "=== E. /admin/update hides 'update available' when DevBuild=true ==="
if grep -qE 'if and \.IsNewer \(not \.DevBuild\)' "${TEMPLATE}"; then
    ok "banner_new_version is gated by {{if and .IsNewer (not .DevBuild)}}"
else
    bad "banner_new_version is NOT gated by DevBuild=false — dev builds would still see 'update available'"
fi

# ------------------------------------------------------------------------------
# Contract F: auto-apply button is hidden when DevBuild=true
# 2026-08-18 (B129): the pre-B129 contract was
#   {{if and .IsNewer .AutoUpdateEnabled (not .DevBuild)}}
# The B129 redesign (per the operator's 2026-08-17 feedback
# "некорректно называть это автообновлением так как по сути
# это просто уведомление") removed the .AutoUpdateEnabled
# gate — the Apply button is now always visible when IsNewer
# (and !DevBuild). The "auto-update" concept moved to the
# separate Schedule section (B129's new card). The DevBuild
# gate is preserved.
# ------------------------------------------------------------------------------
echo
echo "=== F. /admin/update hides auto-apply button when DevBuild=true (B129: no .AutoUpdateEnabled gate) ==="
if grep -qE 'if and \.IsNewer \(not \.DevBuild\)' "${TEMPLATE}"; then
    ok "auto-apply button is gated by {{if and .IsNewer (not .DevBuild)}} (B129 contract — .AutoUpdateEnabled gate removed)"
else
    bad "auto-apply button is NOT gated by .IsNewer and .DevBuild — the B129 contract is broken"
fi
# Negative: the pre-B129 .AutoUpdateEnabled gate must NOT be present
# in the apply form's conditional. (B129 removed it.)
if grep -qE 'if and \.IsNewer \.AutoUpdateEnabled \(not \.DevBuild\)' "${TEMPLATE}"; then
    bad "pre-B129 .AutoUpdateEnabled gate is still on the Apply button (B129 should have removed it)"
else
    ok "pre-B129 .AutoUpdateEnabled gate is gone from the Apply button (B129 contract)"
fi

# ------------------------------------------------------------------------------
# Contract G: compareSemver strips git-describe suffix
# ------------------------------------------------------------------------------
echo
echo "=== G. compareSemver strips git-describe '-N-g<hex>' suffix ==="
if grep -qE 'stripBuildLabelSuffix' "${CHECKER_GO}"; then
    ok "compareSemver calls stripBuildLabelSuffix"
else
    bad "compareSemver does NOT strip the git-describe suffix (B124 fix missing)"
fi
if grep -qE 'func stripBuildLabelSuffix' "${CHECKER_GO}"; then
    ok "stripBuildLabelSuffix helper exists"
else
    bad "stripBuildLabelSuffix helper is missing"
fi
if grep -qE 'func gitDescribeSuffixStart' "${CHECKER_GO}"; then
    ok "gitDescribeSuffixStart helper exists (regex-driven suffix detection)"
else
    bad "gitDescribeSuffixStart helper is missing"
fi
# B124 the specific test case: "1.3.11-27-g03a1d97" vs "1.3.9" must return +1
# (local 1.3.11+27 commits is AHEAD of GitHub v1.3.9). Without B124's
# fix, this returned -1 because the lex fallback put "9" > "11-...".
if grep -qF '1.3.11-27-g03a1d97", "1.3.9", 1' "${CHECKER_TEST_GO}"; then
    ok "TestCompareSemver pins the B124 case (local v1.3.11+27 vs GitHub v1.3.9 → +1)"
else
    bad "TestCompareSemver is missing the B124 regression case"
fi
if grep -qF '1.3.9", "1.3.11-27-g03a1d97", -1' "${CHECKER_TEST_GO}"; then
    ok "TestCompareSemver pins the swapped case (GitHub v1.3.9 vs local v1.3.11+27 → -1)"
else
    bad "TestCompareSemver is missing the swapped B124 case"
fi
if grep -qF '1.3.11-27-g03a1d97", "1.3.11", 0' "${CHECKER_TEST_GO}"; then
    ok "TestCompareSemver pins the self-case (local v1.3.11+27 vs tag v1.3.11 → 0)"
else
    bad "TestCompareSemver is missing the self B124 case"
fi
# 4-component version (skygate v1.3.12+ convention):
if grep -qF '1.3.19.2", "1.3.9", 1' "${CHECKER_TEST_GO}"; then
    ok "TestCompareSemver pins the 4-part case (local v1.3.19.2 vs GitHub v1.3.9 → +1)"
else
    bad "TestCompareSemver is missing the 4-part regression case"
fi
if grep -qF '1.3.9", "1.3.19.2", -1' "${CHECKER_TEST_GO}"; then
    ok "TestCompareSemver pins the 4-part swapped case (GitHub v1.3.9 vs local v1.3.19.2 → -1)"
else
    bad "TestCompareSemver is missing the 4-part swapped B124 case"
fi
if grep -qE 'func splitVersionParts' "${CHECKER_GO}"; then
    ok "splitVersionParts helper exists (4-component version support)"
else
    bad "splitVersionParts helper is missing"
fi
if grep -qE 'func TestStripBuildLabelSuffix' "${CHECKER_TEST_GO}"; then
    ok "TestStripBuildLabelSuffix exists (helper regression guard)"
else
    bad "TestStripBuildLabelSuffix is missing"
fi

# ------------------------------------------------------------------------------
# Contract H: pre-release markers are preserved
# ------------------------------------------------------------------------------
echo
echo "=== H. pre-release markers (rc/beta) are NOT treated as git-describe ==="
if grep -qE '0\.29\.0-rc\.1", "0\.29\.0-rc\.1"' "${CHECKER_TEST_GO}"; then
    ok "TestStripBuildLabelSuffix pins '0.29.0-rc.1' is preserved (not stripped)"
else
    bad "TestStripBuildLabelSuffix missing the rc.1 case"
fi
if grep -qE '0\.29\.0-beta", "0\.29\.0-beta"' "${CHECKER_TEST_GO}"; then
    ok "TestStripBuildLabelSuffix pins '0.29.0-beta' is preserved"
else
    bad "TestStripBuildLabelSuffix missing the beta case"
fi

# ------------------------------------------------------------------------------
# Contract I: Go tests for DevBuild config field
# ------------------------------------------------------------------------------
echo
echo "=== I. Go tests for DevBuild config field ==="
if [ -f "${CONFIG_TEST_GO}" ]; then
    ok "test file exists: ${CONFIG_TEST_GO}"
else
    bad "test file missing: ${CONFIG_TEST_GO}"
fi
if grep -qE 'func TestDevBuild_DefaultFalse' "${CONFIG_TEST_GO}"; then
    ok "TestDevBuild_DefaultFalse exists"
else
    bad "TestDevBuild_DefaultFalse missing"
fi
if grep -qE 'func TestDevBuild_TrueWhenEnvTrue' "${CONFIG_TEST_GO}"; then
    ok "TestDevBuild_TrueWhenEnvTrue exists"
else
    bad "TestDevBuild_TrueWhenEnvTrue missing"
fi
if grep -qE 'func TestDevBuild_FalseOnOtherTruthyValues' "${CONFIG_TEST_GO}"; then
    ok "TestDevBuild_FalseOnOtherTruthyValues exists"
else
    bad "TestDevBuild_FalseOnOtherTruthyValues missing"
fi

# ------------------------------------------------------------------------------
# Live: run the unit tests (best-effort, B-check works on VM host too)
# ------------------------------------------------------------------------------
echo
echo "=== J. live: go test on the relevant packages ==="
if command -v go >/dev/null 2>&1; then
    if go test -count=1 -short -run 'TestDevBuild' ./internal/config/ >/dev/null 2>&1; then
        ok "TestDevBuild_* all pass (config)"
    else
        bad "TestDevBuild_* failed — run 'go test -run TestDevBuild ./internal/config/'"
    fi
    if go test -count=1 -short -run 'TestCompareSemver|TestStripBuildLabelSuffix|TestHasPrereleaseSuffix' ./internal/update/ >/dev/null 2>&1; then
        ok "TestCompareSemver + TestStripBuildLabelSuffix all pass (update)"
    else
        bad "TestCompareSemver / TestStripBuildLabelSuffix failed — run 'go test -run \"TestCompareSemver|TestStripBuildLabelSuffix\" ./internal/update/'"
    fi
else
    warn "go not on PATH — skipping live test run (file-exists checks are the contract pin)"
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
