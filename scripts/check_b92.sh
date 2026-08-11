#!/bin/bash
# scripts/check_b92.sh — invoked by verify_pre_deploy.sh B92 check.
#
# Why a separate file: the B92 check has too many grep patterns
# (one per file + one per unit test) to safely inline in
# verify_pre_deploy.sh's run_check helper, which builds the
# command via printf "%s" "...". The escaped quotes and backslashes
# in inline `\\"entrypoint\\"` patterns break the bash -c
# invocation on the pre-push hook (similar issue that B91 ran
# into). A dedicated shell script avoids all of that.
#
# Pinned contracts:
#   - internal/feature/healthz/availability.go exists with
#     IntegrationKind enum (headscale, headplane, tailscale),
#     Availability struct, Checker struct (NewCheckerFromEnv +
#     Start + Stop + Snapshot), runOnce() that probes each
#     integration, and a default 30s check interval
#   - main.go: AvailabilityChecker wired to adminSvc so the
#     /admin/services page can read the cached snapshot
#   - /admin/services route registered in main.go
#   - internal/handlers/templates/admin/services.html: defines
#     body-admin-services (renderBody funcmap convention)
#   - i18n keys: services.* in both ru + en; title.admin_services
#     in catalog_common
#   - 8 unit tests pass:
#     TestNewChecker_IntervalClamping,
#     TestChecker_HeadscaleOK / _Down / _EmptyURLSkipped,
#     TestChecker_HeadplaneOK,
#     TestChecker_TailscaleFn / _FnNil,
#     TestAvailability_AllOK,
#     TestAvailability_JSON

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Resolve go robustly (same pattern as verify_pre_deploy.sh)
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
    GO="/mnt/c/Program Files/Go/bin/go.exe"
else
    echo "SKY-FAIL: go not found" >&2
    exit 1
fi

# 1. availability.go has the v0.33.1.40 marker + all expected types
test -f internal/feature/healthz/availability.go || { echo "SKY-FAIL: availability.go missing" >&2; exit 1; }
grep -qF "v0.33.1.40" internal/feature/healthz/availability.go || { echo "SKY-FAIL: availability.go missing v0.33.1.40 marker" >&2; exit 1; }
grep -qF "IntegrationHeadscale" internal/feature/healthz/availability.go || { echo "SKY-FAIL: IntegrationHeadscale not defined" >&2; exit 1; }
grep -qF "IntegrationHeadplane" internal/feature/healthz/availability.go || { echo "SKY-FAIL: IntegrationHeadplane not defined" >&2; exit 1; }
grep -qF "IntegrationTailscale" internal/feature/healthz/availability.go || { echo "SKY-FAIL: IntegrationTailscale not defined" >&2; exit 1; }
grep -qF "NewCheckerFromEnv" internal/feature/healthz/availability.go || { echo "SKY-FAIL: NewCheckerFromEnv not defined" >&2; exit 1; }

# 2. main.go: AvailabilityChecker wired + /admin/services route registered
grep -qF "AvailabilityChecker" cmd/skygate/main.go || { echo "SKY-FAIL: AvailabilityChecker not wired in main.go" >&2; exit 1; }
grep -qF "/admin/services" cmd/skygate/main.go || { echo "SKY-FAIL: /admin/services route not registered in main.go" >&2; exit 1; }

# 3. Template defines body-admin-services
test -f internal/handlers/templates/admin/services.html || { echo "SKY-FAIL: services.html template missing" >&2; exit 1; }
grep -qF "body-admin-services" internal/handlers/templates/admin/services.html || { echo "SKY-FAIL: services.html missing body-admin-services define" >&2; exit 1; }

# 4. i18n keys
grep -qF "title.admin_services" internal/i18n/catalog_common.go || { echo "SKY-FAIL: title.admin_services missing from catalog_common" >&2; exit 1; }
grep -qF "services.subtitle" internal/i18n/catalog_admin.go || { echo "SKY-FAIL: services.subtitle missing from catalog_admin" >&2; exit 1; }

# 5. Unit tests exist (8 tests)
test -f internal/feature/healthz/availability_test.go || { echo "SKY-FAIL: availability_test.go missing" >&2; exit 1; }
for t in TestNewChecker_IntervalClamping TestChecker_HeadscaleOK TestChecker_HeadscaleDown TestChecker_EmptyURLSkipped TestChecker_HeadplaneOK TestChecker_TailscaleFn TestChecker_TailscaleFnNil TestAvailability_AllOK TestAvailability_JSON; do
    grep -qF "$t" internal/feature/healthz/availability_test.go || { echo "SKY-FAIL: $t missing from availability_test.go" >&2; exit 1; }
done

# 6. All unit tests actually pass (not just exist)
"$GO" test -count=1 -run "TestNewChecker_|TestChecker_|TestAvailability_" ./internal/feature/healthz/ 2>&1 || { echo "SKY-FAIL: unit tests failed" >&2; exit 1; }

echo "B92 check passed: skygate verifies headscale/headplane availability + /admin/services page wired + 9 unit tests pass"
