#!/bin/bash
# B-mod-admin B-check (2026-09-10).
#
# Verifies the /admin/modules list + /admin/modules/{name}
# detail handlers + templates + i18n keys + routes. Like
# check_b_tailscale_module.sh, the script runs every
# contract independently so a single failure doesn't
# short-circuit the rest.
#
# Run from the repo root:
#   bash scripts/check_b_modules_admin.sh
#
# Exit code: 0 on full pass, 1 on any failure.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# --- counters ---
PASS=0
FAIL=0
TOTAL=0
RED=$'\033[0;31m'
GRN=$'\033[0;32m'
YEL=$'\033[1;33m'
RST=$'\033[0m'

pass() { PASS=$((PASS + 1)); TOTAL=$((TOTAL + 1)); printf '%s PASS %s [contract %d] %s\n' "${GRN}" "${RST}" "${TOTAL}" "$1"; }
fail() { FAIL=$((FAIL + 1)); TOTAL=$((TOTAL + 1)); printf '%s FAIL %s [contract %d] %s\n' "${RED}" "${RST}" "${TOTAL}" "$1"; if [ $# -ge 2 ]; then printf '           %s\n' "$2"; fi; }
section() { printf '\n%s== %s ==%s\n' "${YEL}" "$1" "${RST}"; }

# --- preflight ---
section "Preflight"
if ! command -v go >/dev/null 2>&1; then
    if [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
        export PATH="/mnt/c/Program Files/Go/bin:$PATH"
    fi
fi
if ! command -v go >/dev/null 2>&1; then
    fail "go on PATH" "go not found"
    exit 1
fi
pass "go on PATH ($(go version 2>&1))"

# --- 1. files exist ---
section "File presence"
for f in \
    "internal/feature/admin/modules.go" \
    "internal/feature/admin/modules_test.go" \
    "internal/i18n/catalog_modules.go" \
    "internal/handlers/templates/admin/modules.html" \
    "internal/handlers/templates/admin/module_detail.html"; do
    if [ -f "${REPO_ROOT}/${f}" ]; then
        pass "file ${f} exists"
    else
        fail "file ${f} exists" "missing"
    fi
done

# --- 2. compile + vet ---
section "Compile + vet"
if go build ./... 2>&1 | grep -q .; then
    fail "go build ./..." "build returned output"
else
    pass "go build ./... clean"
fi
if go vet ./... 2>&1 | grep -q .; then
    fail "go vet ./..." "vet returned warnings"
else
    pass "go vet ./... clean"
fi

# --- 3. unit tests pass ---
TEST_OUT="$(go test -count=1 -timeout=60s -run 'TestAdmin|TestActionsFor|TestInstallModeDisplay|TestUrlEscape' ./internal/feature/admin/... 2>&1)"
if printf '%s' "${TEST_OUT}" | grep -q '^FAIL'; then
    fail "modules tests pass" "see output"
    printf '%s\n' "${TEST_OUT}" | sed 's/^/           /' | head -20
elif printf '%s' "${TEST_OUT}" | grep -q '^ok'; then
    pass "modules tests pass"
else
    fail "modules tests pass" "no PASS/FAIL line"
fi

# --- 4. i18n catalog wired ---
section "i18n"
if grep -q '"modules.title"' "${REPO_ROOT}/internal/i18n/catalog_modules.go"; then
    pass "i18n key modules.title present in catalog"
else
    fail "i18n key modules.title present" "missing"
fi
# Verify the keys list in catalog.go includes ruModules / enModules.
if grep -q 'ruModules' "${REPO_ROOT}/internal/i18n/catalog.go"; then
    pass "ruModules registered in perFeatureRU"
else
    fail "ruModules registered" "missing from catalog.go"
fi
if grep -q 'enModules' "${REPO_ROOT}/internal/i18n/catalog.go"; then
    pass "enModules registered in perFeatureEN"
else
    fail "enModules registered" "missing from catalog.go"
fi

# --- 5. handler symbols present ---
section "Handler symbols"
for sym in \
    'func (s \*Service) AdminModulesList' \
    'func (s \*Service) AdminModuleDetail' \
    'func (s \*Service) AdminModulePost' \
    'func (s \*Service) AdminModulesListCSRF'; do
    if grep -q "${sym}" "${REPO_ROOT}/internal/feature/admin/modules.go"; then
        pass "handler ${sym} defined"
    else
        fail "handler ${sym} defined" "missing"
    fi
done

# --- 6. App wrappers in handlers.go ---
for sym in \
    'func (a \*App) AdminModulesList' \
    'func (a \*App) AdminModuleDetail' \
    'func (a \*App) AdminModulePost' \
    'func (a \*App) AdminModulesListCSRF'; do
    if grep -q "${sym}" "${REPO_ROOT}/internal/handlers/handlers.go"; then
        pass "App wrapper ${sym} defined"
    else
        fail "App wrapper ${sym} defined" "missing"
    fi
done

# --- 7. adminSvcHandle interface extended ---
if grep -q 'AdminModulesList(w http.ResponseWriter, r \*http.Request)' "${REPO_ROOT}/internal/handlers/handlers.go"; then
    pass "adminSvcHandle interface lists AdminModulesList"
else
    fail "adminSvcHandle interface" "missing"
fi

# --- 8. routes registered in main.go ---
section "Routes"
for route in \
    'mux.Handle("GET /admin/modules"' \
    'mux.Handle("GET /admin/modules/{name}"' \
    'mux.Handle("POST /admin/modules/{name}/{action...}"' \
    'mux.Handle("GET /admin/modules/csrf"'; do
    if grep -q "${route}" "${REPO_ROOT}/cmd/skygate/main.go"; then
        pass "route ${route} registered"
    else
        fail "route ${route} registered" "missing in main.go"
    fi
done

# --- 9. module.Installer interface ---
if grep -q 'type Installer interface' "${REPO_ROOT}/internal/module/module.go"; then
    pass "module.Installer interface defined (B-mod-admin precondition for /admin/modules/{name}/install)"
else
    fail "module.Installer interface defined" "missing"
fi

# --- 10. module.Manager field on admin.Service ---
if grep -q 'Modules \*module.Manager' "${REPO_ROOT}/internal/feature/admin/service.go"; then
    pass "Service.Modules field added to admin.Service"
else
    fail "Service.Modules field added" "missing"
fi

# --- 11. templates reference CSRF + URL field ---
section "Templates"
if grep -q '"admin/modules.html"' "${REPO_ROOT}/internal/feature/admin/modules.go"; then
    pass "modules list template name matches"
fi
if grep -q 'name="csrf"' "${REPO_ROOT}/internal/handlers/templates/admin/modules.html"; then
    pass "modules list template has csrf hidden input"
else
    fail "modules list template has csrf hidden input" "missing"
fi
if grep -q 'name="csrf"' "${REPO_ROOT}/internal/handlers/templates/admin/module_detail.html"; then
    pass "module detail template has csrf hidden input"
else
    fail "module detail template has csrf hidden input" "missing"
fi
if grep -q '/admin/modules/{' "${REPO_ROOT}/internal/handlers/templates/admin/module_detail.html"; then
    pass "module detail template references sub-feature action URLs"
else
    fail "module detail template references sub-feature action URLs" "missing"
fi

# --- 12. CSRF cookie name constant ---
if grep -q 'skygate_modules_csrf' "${REPO_ROOT}/internal/feature/admin/modules.go"; then
    pass "CSRF cookie name skygate_modules_csrf used"
else
    fail "CSRF cookie name skygate_modules_csrf used" "missing"
fi

# --- 13. i18n parity (RU + EN same keys) ---
RU_KEYS=$(grep -oE '"modules\.[a-z_]+"' "${REPO_ROOT}/internal/i18n/catalog_modules.go" | sort -u | wc -l)
EN_KEYS=$(grep -A 1000 'var enModules' "${REPO_ROOT}/internal/i18n/catalog_modules.go" | grep -oE '"modules\.[a-z_]+"' | sort -u | wc -l)
if [ "${RU_KEYS}" -eq "${EN_KEYS}" ]; then
    pass "i18n RU/EN parity (${RU_KEYS} keys each)"
else
    fail "i18n RU/EN parity" "RU=${RU_KEYS} EN=${EN_KEYS}"
fi

# --- summary ---
section "Summary"
PASS_RATE=$((PASS * 100 / TOTAL))
printf '  Total: %d  Pass: %d  Fail: %d  (%d%%)\n' "${TOTAL}" "${PASS}" "${FAIL}" "${PASS_RATE}"
if [ "${FAIL}" -eq 0 ]; then
    printf '%s✓ B-mod-admin: all contracts pass%s\n' "${GRN}" "${RST}"
    exit 0
else
    printf '%s✗ B-mod-admin: %d contract(s) failed%s\n' "${RED}" "${FAIL}" "${RST}"
    exit 1
fi
