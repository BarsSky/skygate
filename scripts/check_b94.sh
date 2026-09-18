#!/bin/bash
# scripts/check_b94.sh — invoked by verify_pre_deploy.sh B94 check.
#
# Why a separate file: same as check_b91.sh / check_b92.sh / check_b93.sh.
# The B94 check has 7+ grep-pins + 1 unit-test run. Inline printf
# in run_check triggers PowerShell backtick-quote issues. A dedicated
# shell script avoids all of that.
#
# Pinned contracts (v0.33.1.42 code debt cleanup):
#   - D1 (R34 cookie auth): scripts/verify_login.sh exists
#     and POSTs to /login for cookie-based auth; the result
#     is used by R31/R32/R34 in verify_post_deploy.sh
#   - D2 (R35 tailscale status --json): the new R35 check
#     exists in verify_post_deploy.sh and reads BackendState
#     from `docker exec ... tailscale status --json`
#   - D4 (SKYGATE_HEADSCALE_WAIT_TIMEOUT): entrypoint.sh
#     reads the env var and falls back to 60s default
#   - D5 (DB-only /readyz Healthy): service.go has
#     `state.Healthy = state.DB == "ok"` (DB-only) AND
#     `state.DependenciesHealthy` keeps pre-D5 behavior
#   - D6 (/admin/services in sidebar): layout.html has
#     the link between system_tests and headplane
#   - D8 (Tailscale BackendState): main.go has
#     `tailscaleBackendState()` helper that parses JSON
#     and returns state + ok; the TailscaleFn in
#     main.go uses it

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
    GO="/mnt/c/Program Files/Go/bin/go.exe"
else
    echo "SKY-FAIL: go not found" >&2
    exit 1
fi

# 1. D1: verify_login.sh exists.
test -f scripts/verify_login.sh || { echo "SKY-FAIL: scripts/verify_login.sh missing (D1 R34 cookie auth)" >&2; exit 1; }
grep -qF "skygate_session" scripts/verify_login.sh || { echo "SKY-FAIL: verify_login.sh missing skygate_session cookie extraction (D1)" >&2; exit 1; }
grep -qF "POST" scripts/verify_login.sh || { echo "SKY-FAIL: verify_login.sh missing POST /login" >&2; exit 1; }

# 2. D1: verify_post_deploy.sh R31/R32/R34 use the cookie-jar pattern.
# Pre-D1 they used `-u "admin:$SKYGATE_ADMIN_PASSWORD"` (basic auth).
# Post-D1 they source verify_login.sh and use the returned cookie path
# in subsequent curls.
grep -qF 'verify_login.sh' scripts/verify_post_deploy.sh || { echo "SKY-FAIL: verify_post_deploy.sh doesn't call verify_login.sh (D1 R34 cookie auth)" >&2; exit 1; }
if grep -qF 'u "admin:' scripts/verify_post_deploy.sh; then
    echo "SKY-FAIL: verify_post_deploy.sh still uses basic auth (D1 should replace with cookie auth)" >&2
    exit 1
fi

# 3. D2: R35 tailscale status --json check.
grep -qF 'R35' scripts/verify_post_deploy.sh || { echo "SKY-FAIL: R35 check missing from verify_post_deploy.sh (D2)" >&2; exit 1; }
grep -qF 'BackendState' scripts/verify_post_deploy.sh || { echo "SKY-FAIL: R35 doesn't read BackendState (D2)" >&2; exit 1; }
grep -qF 'tailscale status --json' scripts/verify_post_deploy.sh || { echo "SKY-FAIL: R35 doesn't use 'tailscale status --json' (D2)" >&2; exit 1; }

# 4. D4: SKYGATE_HEADSCALE_WAIT_TIMEOUT in entrypoint.sh.
grep -qF 'SKYGATE_HEADSCALE_WAIT_TIMEOUT' entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh missing SKYGATE_HEADSCALE_WAIT_TIMEOUT (D4)" >&2; exit 1; }
grep -qF 'HS_WAIT_TIMEOUT' entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh missing HS_WAIT_TIMEOUT variable" >&2; exit 1; }

# 5. D5: DB-only /readyz Healthy.
grep -qF 'state.Healthy = state.DB == "ok"' internal/feature/healthz/service.go || { echo "SKY-FAIL: service.go doesn't gate Healthy on DB only (D5)" >&2; exit 1; }
grep -qF 'DependenciesHealthy' internal/feature/healthz/service.go || { echo "SKY-FAIL: service.go missing DependenciesHealthy field (D5)" >&2; exit 1; }
grep -qF 'dependencies_healthy' internal/feature/healthz/types.go || { echo "SKY-FAIL: types.go missing dependencies_healthy JSON field (D5)" >&2; exit 1; }

# 6. D6: /admin/services in admin sidebar.
grep -qF '/admin/services' internal/handlers/templates/layout.html || { echo "SKY-FAIL: /admin/services missing from layout.html sidebar (D6)" >&2; exit 1; }

# 7. D8: Tailscale BackendState helper in main.go.
grep -qF 'tailscaleBackendState' cmd/skygate/main.go || { echo "SKY-FAIL: main.go missing tailscaleBackendState helper (D8)" >&2; exit 1; }
grep -qF 'BackendState' cmd/skygate/main.go || { echo "SKY-FAIL: main.go doesn't reference BackendState in TailscaleFn (D8)" >&2; exit 1; }

# 8. Unit tests for D5 readyzState changes — the existing
#    TestAvailability_AllOK / TestAvailability_JSON cover
#    the snapshot shape; the D5 changes are validated by
#    the test passing (Healthy / DependenciesHealthy are
#    unexported fields, so the integration test confirms
#    the JSON shape is correct).
"$GO" test -count=1 -short -run 'TestAvailability_|TestNewChecker_|TestChecker_' ./internal/feature/healthz/ 2>&1 || { echo "SKY-FAIL: healthz availability tests failed" >&2; exit 1; }

echo "B94 check passed: v0.33.1.42 code debt (D1-D8) wired"
