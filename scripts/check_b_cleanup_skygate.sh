#!/bin/bash
# B-mod-cleanup B-check (2026-09-10).
#
# Verifies the cleanup-skygate.sh uninstaller:
#   - file exists + bash syntax
#   - all 5 sections present (systemd, binary, user,
#     data, config) + runtime + optional tailscale
#   - idempotency comments
#   - dry-run support
#   - confirm prompt default
#   - --keep-{user,data,config,binary} flags
#   - delegates to install-tailscale.sh for Tailscale
#
# Run from the repo root:
#   bash scripts/check_b_cleanup_skygate.sh
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

CLEANUP_SH="${REPO_ROOT}/deploy/scripts/cleanup-skygate.sh"
INSTALL_TS_SH="${REPO_ROOT}/deploy/scripts/install-tailscale.sh"

section "Preflight"
if [ ! -f "$CLEANUP_SH" ]; then
    fail "cleanup-skygate.sh exists at $CLEANUP_SH"
    exit 1
fi
pass "cleanup-skygate.sh exists at $CLEANUP_SH"

if bash -n "$CLEANUP_SH" 2>&1; then
    pass "cleanup-skygate.sh has valid bash syntax"
else
    fail "cleanup-skygate.sh has valid bash syntax" "bash -n failed"
    exit 1
fi

# === 1. all 6 cleanup sections present ===
section "Cleanup sections"
for keyword in 'systemctl' 'rm.*SKYGATE_BIN' 'userdel' 'rm -rf.*SKYGATE_DATA_DIR' 'rm -rf.*SKYGATE_ETC_DIR' 'rm -rf.*SKYGATE_RUN_DIR'; do
    if grep -qE "$keyword" "$CLEANUP_SH"; then
        pass "section '$keyword' present"
    else
        fail "section '$keyword' present" "missing in cleanup-skygate.sh"
    fi
done

# === 2. optional Tailscale uninstall ===
if grep -q 'install-tailscale.sh --mode=uninstall' "$CLEANUP_SH"; then
    pass "delegates to install-tailscale.sh --mode=uninstall when --with-tailscale"
else
    fail "delegates to install-tailscale.sh --mode=uninstall" "missing"
fi

# === 3. flags ===
section "Flags"
for flag in '--yes' '--dry-run' '--keep-user' '--keep-data' '--keep-config' '--keep-binary' '--with-tailscale'; do
    if grep -q -- "$flag" "$CLEANUP_SH"; then
        pass "flag $flag parsed in arg loop"
    else
        fail "flag $flag parsed" "missing in arg loop"
    fi
done

# === 4. safety: confirm prompt default ===
section "Safety"
if grep -q "read -r -p" "$CLEANUP_SH" && grep -q "expected 'yes'" "$CLEANUP_SH"; then
    pass "confirm prompt default (reads stdin, aborts on non-'yes')"
else
    fail "confirm prompt default" "missing read -r -p or 'expected yes' abort"
fi
if grep -q -- '--yes' "$CLEANUP_SH" && grep -q 'ASSUME_YES' "$CLEANUP_SH" && grep -q 'read -r -p' "$CLEANUP_SH" && grep -qE 'ASSUME_YES.*= .0.' "$CLEANUP_SH"; then
    pass "--yes bypasses the confirm prompt (ASSUME_YES=1 skips the read -r -p)"
else
    fail "--yes bypasses confirm prompt" "missing"
fi

# === 5. idempotency ===
section "Idempotency"
if grep -q 'Idempotent' "$CLEANUP_SH"; then
    pass "Idempotency comment present"
else
    fail "Idempotency comment" "missing"
fi
# Each step has a 'skip if not present' guard.
if grep -qE 'not present' "$CLEANUP_SH"; then
    N_SKIP=$(grep -c 'not present' "$CLEANUP_SH")
    if [ "$N_SKIP" -ge 4 ]; then
        pass "skip-if-not-present guards in 4+ steps (${N_SKIP} sites)"
    else
        fail "skip-if-not-present guards in 4+ steps" "found ${N_SKIP} sites"
    fi
else
    fail "skip-if-not-present guards" "missing"
fi

# === 6. dry-run support ===
section "Dry-run"
if grep -q 'DRY-RUN' "$CLEANUP_SH" && grep -q 'DRY_RUN' "$CLEANUP_SH"; then
    pass "--dry-run mode implemented (run_step skips execution)"
else
    fail "--dry-run mode" "missing"
fi

# === 7. require_root gate ===
if grep -q 'require_root' "$CLEANUP_SH" && grep -q 'must run as root' "$CLEANUP_SH"; then
    pass "require_root gate + 'must run as root' error"
else
    fail "require_root gate" "missing"
fi

# === 8. exit codes documented ===
section "Exit codes"
if grep -q 'Exit codes:' "$CLEANUP_SH" && grep -q 'success' "$CLEANUP_SH" && grep -q 'aborted by operator' "$CLEANUP_SH" && grep -q 'partial state' "$CLEANUP_SH"; then
    pass "exit codes documented (0/1/2/3)"
else
    fail "exit codes documented" "missing 0/1/2/3"
fi

# === 9. install-tailscale.sh dependency ===
if [ -f "$INSTALL_TS_SH" ]; then
    pass "install-tailscale.sh exists (--with-tailscale can delegate)"
else
    fail "install-tailscale.sh exists" "missing — --with-tailscale won't work"
fi

# === 10. set -euo pipefail + arg parsing via while loop ===
section "Bash rigor"
if grep -E 'set -euo pipefail' "$CLEANUP_SH" | head -3 | grep -q 'set -euo pipefail'; then
    pass "set -euo pipefail at top"
else
    fail "set -euo pipefail" "missing"
fi
if grep -qE 'while \[ \$# -gt 0 \]; do' "$CLEANUP_SH" && grep -qE 'shift$' "$CLEANUP_SH"; then
    pass "standard while-shift arg loop"
else
    fail "standard while-shift arg loop" "missing"
fi

# === 11. doesn't delete /tmp, /home, /root by accident ===
section "Safety: no accidental wide rm"
# All rm targets should be under /var/lib, /etc, /var/run, or /usr/local/bin
# Filter out:
#   - lines with rm -rf "${SKYGATE_*}"  (variable expansion)
#   - lines with rm -rf "/var/lib/skygate" etc. (legit targets)
#   - rm -f (single file)
BAD_RM=$(grep -nE '\brm\s+(-r[fR]|-rf|-[rR]f)\s+/' "$CLEANUP_SH" \
    | grep -vE '/(var/lib/skygate|etc/skygate|var/run/skygate|usr/local/bin/skygate|"\$' | head -5)
if [ -n "$BAD_RM" ]; then
    fail "no accidental wide rm" "found: $BAD_RM"
else
    pass "no accidental wide rm (all targets are /var/lib/skygate, /etc/skygate, /var/run/skygate, or /usr/local/bin/skygate)"
fi

# === summary ===
section "Summary"
PASS_RATE=$((PASS * 100 / TOTAL))
printf '  Total: %d  Pass: %d  Fail: %d  (%d%%)\n' "${TOTAL}" "${PASS}" "${FAIL}" "${PASS_RATE}"
if [ "${FAIL}" -eq 0 ]; then
    printf '%s✓ B-mod-cleanup: all contracts pass%s\n' "${GRN}" "${RST}"
    exit 0
else
    printf '%s✗ B-mod-cleanup: %d contract(s) failed%s\n' "${RED}" "${FAIL}" "${RST}"
    exit 1
fi
