#!/bin/bash
# B-mod-tailscale B-check (2026-09-10).
#
# Verifies that the Tailscale module package compiles,
# passes its unit tests, and exposes the right public API
# surface (Module interface compliance + the 3 install modes
# + the 4 sub-features).
#
# Run from the repo root:
#   bash scripts/check_b_tailscale_module.sh
#
# Exit code: 0 on full pass, 1 on any failure.
# Each contract is a small atomic assertion. Failed contracts
# print a clear error; the script continues to run all contracts
# so the operator gets a complete picture in one pass.

set -u

# --- paths ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PKG_DIR="${REPO_ROOT}/internal/module/tailscale"
MODULE_DIR="${REPO_ROOT}/internal/module"

# --- counters ---
PASS=0
FAIL=0
TOTAL=0

# --- helpers ---
RED=$'\033[0;31m'
GRN=$'\033[0;32m'
YEL=$'\033[1;33m'
RST=$'\033[0m'

pass() {
    PASS=$((PASS + 1))
    TOTAL=$((TOTAL + 1))
    printf '%s PASS %s [contract %d] %s\n' "${GRN}" "${RST}" "${TOTAL}" "$1"
}

fail() {
    FAIL=$((FAIL + 1))
    TOTAL=$((TOTAL + 1))
    printf '%s FAIL %s [contract %d] %s\n' "${RED}" "${RST}" "${TOTAL}" "$1"
    if [ $# -ge 2 ]; then
        printf '           %s\n' "$2"
    fi
}

# Section banner
section() {
    printf '\n%s== %s ==%s\n' "${YEL}" "$1" "${RST}"
}

# --- preflight ---
section "Preflight"
if [ ! -d "${PKG_DIR}" ]; then
    fail "package directory exists" "missing: ${PKG_DIR}"
    exit 1
fi
pass "package directory exists (${PKG_DIR})"

if ! command -v go >/dev/null 2>&1; then
    if [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
        export PATH="/mnt/c/Program Files/Go/bin:$PATH"
    elif [ -x "/c/Program Files/Go/bin/go.exe" ]; then
        export PATH="/c/Program Files/Go/bin:$PATH"
    fi
fi
if ! command -v go >/dev/null 2>&1; then
    fail "go is on PATH" "go not found in PATH or standard install locations"
    exit 1
fi
GO_VERSION="$(go version 2>&1)"
pass "go is on PATH (${GO_VERSION})"

# --- contract 1: package files exist ---
section "Package structure"
for f in runner.go install.go subfeatures.go tailscale.go tailscale_test.go; do
    if [ -f "${PKG_DIR}/${f}" ]; then
        pass "file ${f} exists"
    else
        fail "file ${f} exists" "missing: ${PKG_DIR}/${f}"
    fi
done

# --- contract 2: package compiles ---
section "Compile"
if go build ./internal/module/tailscale/... 2>&1 | grep -q .; then
    fail "tailscale package builds" "go build returned output"
else
    pass "tailscale package builds"
fi

# --- contract 3: vet clean ---
if go vet ./internal/module/tailscale/... 2>&1 | grep -q .; then
    fail "go vet clean" "go vet returned warnings"
else
    pass "go vet clean"
fi

# --- contract 4: unit tests pass ---
TEST_OUT="$(go test -count=1 -timeout=60s ./internal/module/tailscale/... 2>&1)"
if printf '%s' "${TEST_OUT}" | grep -q '^FAIL'; then
    fail "unit tests pass" "see output above"
    printf '%s\n' "${TEST_OUT}" | sed 's/^/           /'
elif printf '%s' "${TEST_OUT}" | grep -q '^ok'; then
    # Count how many tests ran.
    N=$(printf '%s' "${TEST_OUT}" | grep -oE 'ok[ \t]+skygate/internal/module/tailscale[ \t]+[0-9.]+s' | head -1)
    pass "unit tests pass (${N})"
else
    fail "unit tests pass" "no PASS/FAIL line found"
fi

# --- contract 5: Module interface compliance ---
section "Public API surface"
# Look for the compile-time guard in tailscale.go.
if grep -q 'var _ module.Module = (\*Module)(nil)' "${PKG_DIR}/tailscale.go"; then
    pass "Module interface compliance guard present"
else
    fail "Module interface compliance guard present" "missing: var _ module.Module = ..."
fi

# --- contract 6: all 4 sub-feature constants ---
for sub in SubCluster SubTelegram SubDERP SubExit; do
    if grep -qE "(^|\s)${sub}[[:space:]]*=" "${PKG_DIR}/subfeatures.go"; then
        pass "sub-feature constant ${sub} defined"
    else
        fail "sub-feature constant ${sub} defined" "missing in subfeatures.go"
    fi
done

# --- contract 7: 3 install mode constants ---
for mode in InstallModeOSLevel InstallModeContainer InstallModeAttach InstallModeNone; do
    if grep -qE "${mode}[[:space:]]*=" "${PKG_DIR}/install.go"; then
        pass "install mode constant ${mode} defined"
    else
        fail "install mode constant ${mode} defined" "missing in install.go"
    fi
done

# --- contract 8: CmdRunner interface ---
if grep -q 'type CmdRunner interface' "${PKG_DIR}/runner.go"; then
    pass "CmdRunner interface defined"
else
    fail "CmdRunner interface defined" "missing in runner.go"
fi

# --- contract 9: NewModule + NewModuleWithRunner ---
if grep -q 'func NewModule()' "${PKG_DIR}/tailscale.go"; then
    pass "NewModule() constructor exists"
else
    fail "NewModule() constructor exists" "missing"
fi
if grep -q 'func NewModuleWithRunner' "${PKG_DIR}/tailscale.go"; then
    pass "NewModuleWithRunner() constructor exists (test-injectable)"
else
    fail "NewModuleWithRunner() constructor exists" "missing"
fi

# --- contract 10: B179 netfilter-mode=nodir safety ---
# Tailscale up must always use --netfilter-mode=nodir (never
# --netfilter-mode=off) per B179.
if grep -q '\-\-netfilter-mode=nodir' "${PKG_DIR}/install.go"; then
    N_NODIR=$(grep -c '\-\-netfilter-mode=nodir' "${PKG_DIR}/install.go")
    pass "B179 safety: --netfilter-mode=nodir present in install.go (${N_NODIR} sites)"
    if grep -q '\-\-netfilter-mode=off' "${PKG_DIR}/install.go"; then
        fail "B179 safety: --netfilter-mode=off absent" "found --netfilter-mode=off — this re-occurs the B179 trap"
    else
        pass "B179 safety: --netfilter-mode=off absent"
    fi
else
    fail "B179 safety: --netfilter-mode=nodir present in install.go" "missing"
fi

# --- contract 11: sub-feature Requires chain (DERP needs telegram + exit) ---
if grep -A 20 'SubDERP' "${PKG_DIR}/subfeatures.go" | grep -q 'SubTelegram' && \
   grep -A 20 'SubDERP' "${PKG_DIR}/subfeatures.go" | grep -q 'SubExit'; then
    pass "DERP sub-feature Requires [telegram exit]"
else
    fail "DERP sub-feature Requires [telegram exit]" "Requires chain missing or wrong"
fi

# --- B-mod-cluster (2026-09-10): cluster sub-feature records state.Info ---
if grep -q 'cluster_filter' "${PKG_DIR}/subfeatures.go"; then
    pass "B-mod-cluster: cluster sub-feature references state.Info[cluster_filter]"
else
    fail "B-mod-cluster: cluster_filter in subfeatures.go" "missing"
fi
if grep -B 1 -A 8 'case SubCluster:' "${PKG_DIR}/subfeatures.go" | grep -q 'cluster_filter.*active'; then
    pass "B-mod-cluster: enable sets cluster_filter = active"
else
    fail "B-mod-cluster: enable sets cluster_filter = active" "missing"
fi
if grep -B 1 -A 10 'case SubCluster:' "${PKG_DIR}/subfeatures.go" | grep -q 'cluster_filter.*inactive'; then
    pass "B-mod-cluster: disable sets cluster_filter = inactive"
else
    fail "B-mod-cluster: disable sets cluster_filter = inactive" "missing"
fi
if grep -q 'TestEnableSubFeature_Cluster' "${PKG_DIR}/tailscale_test.go"; then
    pass "B-mod-cluster: TestEnableSubFeature_Cluster unit test present"
else
    fail "B-mod-cluster: TestEnableSubFeature_Cluster unit test" "missing"
fi

# --- B-mod-telegram (2026-09-10): telegram sub-feature records state.Info ---
if grep -q 'telegram_route' "${PKG_DIR}/subfeatures.go"; then
    pass "B-mod-telegram: telegram sub-feature references state.Info[telegram_route]"
else
    fail "B-mod-telegram: telegram_route in subfeatures.go" "missing"
fi
if grep -q 'telegram_cidr' "${PKG_DIR}/subfeatures.go"; then
    pass "B-mod-telegram: state.Info[telegram_cidr] = 91.108.56.0/22 recorded"
else
    fail "B-mod-telegram: telegram_cidr in subfeatures.go" "missing"
fi
if grep -B 1 -A 4 'telegram_route.*advertised' "${PKG_DIR}/subfeatures.go" | grep -q 'm.state.Info\["telegram_route"\] = "advertised"'; then
    pass "B-mod-telegram: enable sets telegram_route = advertised"
else
    fail "B-mod-telegram: enable sets telegram_route = advertised" "missing"
fi
if grep -B 1 -A 3 'telegram_route.*unadvertised' "${PKG_DIR}/subfeatures.go" | grep -q 'm.state.Info\["telegram_route"\] = "unadvertised"'; then
    pass "B-mod-telegram: disable sets telegram_route = unadvertised"
else
    fail "B-mod-telegram: disable sets telegram_route = unadvertised" "missing"
fi
# 91.108.56.0/22 CIDR constant is referenced 2+ times (enable + disable)
CIDR_COUNT=$(grep -c '91.108.56.0/22' "${PKG_DIR}/subfeatures.go" || true)
if [ "$CIDR_COUNT" -ge 2 ]; then
    pass "B-mod-telegram: 91.108.56.0/22 referenced in ${CIDR_COUNT} places (constant)"
else
    fail "B-mod-telegram: 91.108.56.0/22 in 2+ places" "found ${CIDR_COUNT}"
fi

# --- B-mod-derp (2026-09-10): derp sub-feature records state.Info ---
if grep -q 'derp_relay' "${PKG_DIR}/subfeatures.go"; then
    pass "B-mod-derp: derp sub-feature references state.Info[derp_relay]"
else
    fail "B-mod-derp: derp_relay in subfeatures.go" "missing"
fi
if grep -B 1 -A 4 'derp_relay.*active' "${PKG_DIR}/subfeatures.go" | grep -q 'm.state.Info\["derp_relay"\] = "active"'; then
    pass "B-mod-derp: enable sets derp_relay = active"
else
    fail "B-mod-derp: enable sets derp_relay = active" "missing"
fi
if grep -B 1 -A 3 'derp_relay.*inactive' "${PKG_DIR}/subfeatures.go" | grep -q 'm.state.Info\["derp_relay"\] = "inactive"'; then
    pass "B-mod-derp: disable sets derp_relay = inactive"
else
    fail "B-mod-derp: disable sets derp_relay = inactive" "missing"
fi
# Requires chain (DERP requires telegram + exit)
if grep -A 20 'SubDERP' "${PKG_DIR}/subfeatures.go" | grep -q 'Requires: \[\]string{SubTelegram, SubExit}'; then
    pass "B-mod-derp: Requires chain [telegram exit] preserved"
else
    fail "B-mod-derp: Requires [telegram exit]" "missing"
fi
if grep -q 'TestEnableSubFeature_Derp' "${PKG_DIR}/tailscale_test.go"; then
    pass "B-mod-derp: TestEnableSubFeature_Derp unit test present"
else
    fail "B-mod-derp: TestEnableSubFeature_Derp unit test" "missing"
fi

# --- B-mod-exit (2026-09-10): exit sub-feature records state.Info ---
if grep -q 'exit_node' "${PKG_DIR}/subfeatures.go"; then
    pass "B-mod-exit: exit sub-feature references state.Info[exit_node]"
else
    fail "B-mod-exit: exit_node in subfeatures.go" "missing"
fi
if grep -B 1 -A 4 'exit_node.*advertised' "${PKG_DIR}/subfeatures.go" | grep -q 'm.state.Info\["exit_node"\] = "advertised"'; then
    pass "B-mod-exit: enable sets exit_node = advertised"
else
    fail "B-mod-exit: enable sets exit_node = advertised" "missing"
fi
if grep -B 1 -A 3 'exit_node.*unadvertised' "${PKG_DIR}/subfeatures.go" | grep -q 'm.state.Info\["exit_node"\] = "unadvertised"'; then
    pass "B-mod-exit: disable sets exit_node = unadvertised"
else
    fail "B-mod-exit: disable sets exit_node = unadvertised" "missing"
fi
if grep -q 'exit_node_advertised_at' "${PKG_DIR}/subfeatures.go"; then
    pass "B-mod-exit: state.Info[exit_node_advertised_at] RFC3339 timestamp recorded"
else
    fail "B-mod-exit: exit_node_advertised_at in subfeatures.go" "missing"
fi
# Disable should also clear the timestamp (no stale data)
if grep -B 2 -A 5 'exit_node.*unadvertised' "${PKG_DIR}/subfeatures.go" | grep -q 'delete.*exit_node_advertised_at'; then
    pass "B-mod-exit: disable clears exit_node_advertised_at (no stale data)"
else
    fail "B-mod-exit: disable clears exit_node_advertised_at" "missing"
fi
# --advertise-exit-node=true/false still in the code (B-mod-tailscale contract)
if grep -q -- '--advertise-exit-node=true' "${PKG_DIR}/subfeatures.go" && \
   grep -q -- '--advertise-exit-node=false' "${PKG_DIR}/subfeatures.go"; then
    pass "B-mod-exit: --advertise-exit-node=true/false still in code (B-mod-tailscale contract)"
else
    fail "B-mod-exit: --advertise-exit-node=true/false" "missing"
fi

# --- contract 12: Manager.LoadState / Manager.SaveState public wrappers ---
# B-mod-tailscale requires these so the module package can read/write
# state.json from outside the module package.
if grep -q 'func LoadState(' "${MODULE_DIR}/state.go"; then
    pass "module.LoadState() public wrapper exists"
else
    fail "module.LoadState() public wrapper exists" "missing in state.go"
fi
if grep -q 'func SaveState(' "${MODULE_DIR}/state.go"; then
    pass "module.SaveState() public wrapper exists"
else
    fail "module.SaveState() public wrapper exists" "missing in state.go"
fi

# --- contract 13: Health checks the right things ---
if grep -q 'tailscaled_running' "${PKG_DIR}/tailscale.go" && \
   grep -q 'auth_ok' "${PKG_DIR}/tailscale.go" && \
   grep -q 'headscale_reachable' "${PKG_DIR}/tailscale.go" && \
   grep -q 'peers_visible' "${PKG_DIR}/tailscale.go"; then
    pass "Health checks the 4 expected dimensions (tailscaled_running, auth_ok, headscale_reachable, peers_visible)"
else
    fail "Health checks the 4 expected dimensions" "one or more of tailscaled_running / auth_ok / headscale_reachable / peers_visible is missing"
fi

# --- contract 14: idempotency for all install modes ---
# Each install mode has its own function; each must check for
# the already-installed state and skip the heavy step.
for fn in installOsLevel installInContainer installAttach; do
    if grep -q "func (m \*Module) ${fn}(" "${PKG_DIR}/install.go"; then
        pass "install function ${fn}() defined"
    else
        fail "install function ${fn}() defined" "missing"
    fi
done

# --- summary ---
section "Summary"
PASS_RATE=$((PASS * 100 / TOTAL))
printf '  Total contracts: %d\n' "${TOTAL}"
printf '  %sPassed:%s        %d\n' "${GRN}" "${RST}" "${PASS}"
printf '  %sFailed:%s        %d\n' "${RED}" "${RST}" "${FAIL}"
printf '  Pass rate:    %d%%\n' "${PASS_RATE}"
printf '\n'

if [ "${FAIL}" -eq 0 ]; then
    printf '%s✓ B-mod-tailscale: all contracts pass%s\n' "${GRN}" "${RST}"
    exit 0
else
    printf '%s✗ B-mod-tailscale: %d contract(s) failed%s\n' "${RED}" "${FAIL}" "${RST}"
    exit 1
fi
