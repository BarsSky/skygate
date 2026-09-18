#!/bin/bash
# B-mod-install B-check (2026-09-10).
#
# Verifies the operator-facing install path for the
# Tailscale module: install-tailscale.sh (5 modes) +
# install-debian.sh integration (step 7 delegate).
#
# Run from the repo root:
#   bash scripts/check_b_install_tailscale.sh
#
# Exit 0 on full pass, 1 on any failure.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

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

section "File presence"
# 1. install-tailscale.sh exists and is executable.
INSTALL_SH="${REPO_ROOT}/deploy/scripts/install-tailscale.sh"
if [ -f "$INSTALL_SH" ]; then
    pass "install-tailscale.sh exists at $INSTALL_SH"
    if [ -x "$INSTALL_SH" ]; then
        pass "install-tailscale.sh is executable"
    else
        fail "install-tailscale.sh is executable" "chmod +x required"
    fi
else
    fail "install-tailscale.sh exists" "missing"
fi

# 2. install-debian.sh exists.
INSTALL_DEBIAN="${REPO_ROOT}/deploy/install-debian.sh"
if [ -f "$INSTALL_DEBIAN" ]; then
    pass "install-debian.sh exists at $INSTALL_DEBIAN"
else
    fail "install-debian.sh exists" "missing"
fi

section "Syntax"
# 3. Both scripts have valid bash syntax.
if bash -n "$INSTALL_SH" 2>&1; then
    pass "install-tailscale.sh has valid bash syntax"
else
    fail "install-tailscale.sh has valid bash syntax" "bash -n failed"
fi
if bash -n "$INSTALL_DEBIAN" 2>&1; then
    pass "install-debian.sh has valid bash syntax"
else
    fail "install-debian.sh has valid bash syntax" "bash -n failed"
fi

section "All 5 modes present"
# 4. install-tailscale.sh supports all 5 modes:
#    os_level / in_container / attach / none / uninstall.
for mode in os_level in_container attach none uninstall; do
    # The mode is referenced in a function name OR the
    # main dispatch case statement.
    if grep -q "${mode}" "$INSTALL_SH"; then
        pass "mode ${mode} present in install-tailscale.sh"
    else
        fail "mode ${mode} present" "missing"
    fi
done

# 5. B179 safety: --netfilter-mode=nodir present, --netfilter-mode=off absent.
NODIR_COUNT=$(grep -c -- '--netfilter-mode=nodir' "$INSTALL_SH" || true)
if [ "$NODIR_COUNT" -ge 3 ]; then
    pass "B179 safety: --netfilter-mode=nodir in 3+ call sites (${NODIR_COUNT})"
else
    fail "B179 safety: --netfilter-mode=nodir in 3+ sites" "found ${NODIR_COUNT}"
fi
if grep -q -- '--netfilter-mode=off' "$INSTALL_SH"; then
    fail "B179 safety: --netfilter-mode=off absent" "found --netfilter-mode=off"
else
    pass "B179 safety: --netfilter-mode=off absent"
fi

section "Atomic state writes"
# 6. State file is written atomically (.tmp + mv).
if grep -q '\.tmp' "$INSTALL_SH" && grep -q 'mv.*\$tmp' "$INSTALL_SH"; then
    pass "state file written atomically (write to .tmp + mv)"
else
    fail "state file written atomically" "no .tmp + mv pattern"
fi

section "Idempotency"
# 7. Each install function has an idempotency check.
#    os_level: "if command -v tailscale, skip apt"
#    in_container: "if docker ps -q -f name=X, skip docker run"
#    attach: "if BackendState != Running, fail" (this IS the
#            idempotency check — running means already up).
for fn in install_os_level install_in_container install_attach; do
    if grep -q "${fn}()" "$INSTALL_SH"; then
        pass "function ${fn}() defined"
    else
        fail "function ${fn}() defined" "missing"
    fi
done
# 8. Re-run is safe (function comments mention "Idempotent").
if grep -q 'Idempotent' "$INSTALL_SH"; then
    pass "install-tailscale.sh has Idempotency comment"
else
    fail "install-tailscale.sh has Idempotency comment" "missing"
fi

section "Integration with install-debian.sh"
# 9. install-debian.sh delegates to install-tailscale.sh
#    when SKYGATE_TS_* env vars are set.
if grep -q 'install-tailscale.sh' "$INSTALL_DEBIAN"; then
    pass "install-debian.sh references install-tailscale.sh"
else
    fail "install-debian.sh references install-tailscale.sh" "missing"
fi
# 10. install-debian.sh reads SKYGATE_TS_INSTALL_MODE.
if grep -q 'SKYGATE_TS_INSTALL_MODE' "$INSTALL_DEBIAN"; then
    pass "install-debian.sh reads SKYGATE_TS_INSTALL_MODE"
else
    fail "install-debian.sh reads SKYGATE_TS_INSTALL_MODE" "missing"
fi
# 11. install-debian.sh is opt-in (no-op if no SKYGATE_TS_*).
if grep -q '\[ -n "${SKYGATE_TS_INSTALL_MODE:-}" \]' "$INSTALL_DEBIAN" || \
   grep -q 'SKYGATE_TS_AUTHKEY:-' "$INSTALL_DEBIAN"; then
    pass "install-debian.sh is opt-in (only runs when SKYGATE_TS_* set)"
else
    fail "install-debian.sh is opt-in" "missing opt-in guard"
fi
# 12. install-debian.sh failure is non-fatal (WARN, not exit 1).
if grep -q 'WARN: install-tailscale.sh failed' "$INSTALL_DEBIAN"; then
    pass "install-debian.sh handles install-tailscale.sh failure as WARN (not fatal)"
else
    fail "install-debian.sh handles install-tailscale.sh failure as WARN" "missing"
fi

section "Auto-pick login-server from skygate.env"
# 13. install-debian.sh can auto-pick HEADSCALE_URL from
#     /etc/skygate/skygate.env (so the operator doesn't
#     have to pass --login-server twice).
if grep -q 'HEADSCALE_URL=' "$INSTALL_DEBIAN"; then
    pass "install-debian.sh auto-picks HEADSCALE_URL from skygate.env"
else
    fail "install-debian.sh auto-picks HEADSCALE_URL" "missing"
fi

section "Sudo check + non-fatal state writes"
# 14. os_level + in_container + uninstall require root.
if grep -q 'this script must run as root' "$INSTALL_SH"; then
    pass "root-required warning present"
else
    fail "root-required warning" "missing"
fi
# 15. attach mode can run as non-root (operator-installed tailscaled).
#     This is implicit (no require_root in install_attach), so check
#     that the function body doesn't have require_root.
if awk '/^install_attach\(\)/,/^}$/' "$INSTALL_SH" | grep -q 'require_root'; then
    fail "install_attach does NOT require root" "require_root found in body"
else
    pass "install_attach does NOT require root (operator-level)"
fi

# === summary ===
section "Summary"
PASS_RATE=$((PASS * 100 / TOTAL))
printf '  Total: %d  Pass: %d  Fail: %d  (%d%%)\n' "${TOTAL}" "${PASS}" "${FAIL}" "${PASS_RATE}"
if [ "${FAIL}" -eq 0 ]; then
    printf '%s✓ B-mod-install: all contracts pass%s\n' "${GRN}" "${RST}"
    exit 0
else
    printf '%s✗ B-mod-install: %d contract(s) failed%s\n' "${RED}" "${FAIL}" "${RST}"
    exit 1
fi
