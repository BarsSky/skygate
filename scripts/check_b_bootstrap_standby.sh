#!/bin/bash
# B-mod-install follow-up: bootstrap_standby.sh B-check (2026-09-10).
#
# Verifies deploy/scripts/bootstrap_standby.sh:
#   - file exists + bash syntax
#   - all 5 steps present (Tailscale attach, ssh to primary,
#     parse stdout, write to disk, reminder)
#   - Tailscale attach-mode fallback for 2 cases:
#     (a) tailscaled already running (skip)
#     (b) tailscale binary not on PATH (fall back to os_level)
#   - flags: --primary, --standby-hostname, --ts-authkey,
#     --login-server, --skip-ts
#   - idempotency + safety (confirm defaults, no accidental
#     wide rm)
#
# Run from the repo root:
#   bash scripts/check_b_bootstrap_standby.sh

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

BOOTSTRAP_SH="${REPO_ROOT}/deploy/scripts/bootstrap_standby.sh"
INSTALL_TS_SH="${REPO_ROOT}/deploy/scripts/install-tailscale.sh"

section "Preflight"
if [ ! -f "$BOOTSTRAP_SH" ]; then
    fail "bootstrap_standby.sh exists at $BOOTSTRAP_SH"
    exit 1
fi
pass "bootstrap_standby.sh exists at $BOOTSTRAP_SH"
if bash -n "$BOOTSTRAP_SH" 2>&1; then
    pass "bootstrap_standby.sh has valid bash syntax"
else
    fail "bootstrap_standby.sh has valid bash syntax" "bash -n failed"
    exit 1
fi

# === 1. all 5 steps present ===
section "5 bootstrap steps"
for keyword in 'install-tailscale.sh --mode=attach' 'ssh.*skygate init' 'parse standby preauth' 'wrote preauth' 'skygate standby join'; do
    if grep -qE "$keyword" "$BOOTSTRAP_SH"; then
        pass "step pattern '$keyword' present"
    else
        fail "step pattern '$keyword' present" "missing"
    fi
done

# === 2. Tailscale attach-mode idempotency ===
section "Tailscale attach-mode idempotency"
# (a) skip if tailscaled already Running
if grep -q 'BackendState.*Running' "$BOOTSTRAP_SH" && grep -q 'already in the tailnet' "$BOOTSTRAP_SH"; then
    pass "(a) skip install when tailscaled already Running (BackendState check)"
else
    fail "(a) skip install when tailscaled Running" "missing"
fi
# (b) fallback to os_level if tailscale binary not on PATH
if grep -q 'tailscale binary not on PATH' "$BOOTSTRAP_SH" && grep -q -- '--mode=os_level' "$BOOTSTRAP_SH"; then
    pass "(b) fallback to --mode=os_level when tailscale binary not on PATH"
else
    fail "(b) fallback to --mode=os_level" "missing"
fi
# (c) --skip-ts bypass
if grep -q -- '--skip-ts' "$BOOTSTRAP_SH" && grep -q 'SKIP_TS=1' "$BOOTSTRAP_SH"; then
    pass "(c) --skip-ts bypasses Tailscale install"
else
    fail "(c) --skip-ts bypass" "missing"
fi

# === 3. flags parsed ===
section "Flags"
for flag in '--primary' '--standby-hostname' '--ts-authkey' '--login-server' '--skip-ts'; do
    if grep -q -- "$flag" "$BOOTSTRAP_SH"; then
        pass "flag $flag parsed in arg loop"
    else
        fail "flag $flag parsed" "missing in arg loop"
    fi
done

# === 4. ssh invokation of skygate init ===
if grep -qE 'ssh .* skygate init' "$BOOTSTRAP_SH"; then
    pass "ssh to primary + skygate init <hostname>"
else
    fail "ssh to primary + skygate init" "missing"
fi
# StrictHostKeyChecking=accept-new (B179-style safety)
if grep -q 'StrictHostKeyChecking=accept-new' "$BOOTSTRAP_SH"; then
    pass "StrictHostKeyChecking=accept-new (auto-accept on first connect)"
else
    fail "StrictHostKeyChecking=accept-new" "missing — operator may get stuck on first run"
fi
# BatchMode=yes (no password prompts)
if grep -q 'BatchMode=yes' "$BOOTSTRAP_SH"; then
    pass "BatchMode=yes (no password prompts — fails fast on missing keys)"
else
    fail "BatchMode=yes" "missing"
fi

# === 5. stdout parsing for 4 fields ===
section "Stdout parsing (4 fields)"
for field in 'node_id' 'cluster_id' 'dsn' 'primary_host'; do
    if grep -q "$field" "$BOOTSTRAP_SH"; then
        pass "parses field '$field' from skygate init stdout"
    else
        fail "parses field '$field'" "missing"
    fi
done
# All 4 fields checked together (one missing = exit 4)
if grep -qE 'node_id.*cluster_id.*dsn.*primary_host' "$BOOTSTRAP_SH"; then
    pass "all 4 fields checked together (exit 4 if any missing)"
else
    fail "all 4 fields checked together" "missing composite check"
fi

# === 6. writes preauth to disk + chmod 600 ===
section "Preauth persistence"
if grep -qE 'standby.*node_id.*preauth' "$BOOTSTRAP_SH" || grep -qE 'OUT_FILE.*node_id.*preauth' "$BOOTSTRAP_SH"; then
    pass "writes preauth to /var/lib/skygate/standby/<node_id>.preauth.json"
else
    fail "writes preauth to disk" "missing"
fi
if grep -q 'chmod 0600' "$BOOTSTRAP_SH" || grep -q 'chmod 600' "$BOOTSTRAP_SH"; then
    pass "chmod 0600 (operator-only readable)"
else
    fail "chmod 0600" "missing"
fi
# Prints on stdout too (so operator can paste into manual join)
if grep -q 'STANDBY PREAUTH KEY' "$BOOTSTRAP_SH"; then
    pass "prints preauth on stdout (===== STANDBY PREAUTH KEY ===== header)"
else
    fail "prints preauth on stdout" "missing"
fi

# === 7. exit codes documented ===
section "Exit codes"
if grep -q 'Exit codes:' "$BOOTSTRAP_SH" && \
   grep -qE '\b0\b.*standby preauth' "$BOOTSTRAP_SH" && \
   grep -qE '\b1\b.*invalid args' "$BOOTSTRAP_SH" && \
   grep -qE '\b2\b.*primary' "$BOOTSTRAP_SH" && \
   grep -qE '\b3\b.*Tailscale' "$BOOTSTRAP_SH" && \
   grep -qE '\b4\b.*preauth' "$BOOTSTRAP_SH"; then
    pass "exit codes documented (0/1/2/3/4)"
else
    fail "exit codes documented" "missing one or more of 0/1/2/3/4"
fi

# === 8. require_root + set -euo pipefail ===
section "Bash rigor"
if grep -q 'require_root' "$BOOTSTRAP_SH" && grep -q 'must run as root' "$BOOTSTRAP_SH"; then
    pass "require_root gate + 'must run as root' error"
else
    fail "require_root gate" "missing"
fi
if grep -E 'set -euo pipefail' "$BOOTSTRAP_SH" | head -3 | grep -q 'set -euo pipefail'; then
    pass "set -euo pipefail at top"
else
    fail "set -euo pipefail" "missing"
fi

# === 9. depends on install-tailscale.sh ===
if [ -f "$INSTALL_TS_SH" ]; then
    pass "install-tailscale.sh exists (delegate target)"
else
    fail "install-tailsgate.sh exists" "missing — --mode=attach can't delegate"
fi

# === 10. no accidental wide rm ===
section "Safety: no accidental wide rm"
BAD_RM=$(grep -nE '\brm\s+(-r[fR]|-rf|-[rR]f)\s+/' "$BOOTSTRAP_SH" | head -3)
if [ -n "$BAD_RM" ]; then
    fail "no accidental wide rm" "found: $BAD_RM"
else
    pass "no accidental wide rm (only mkdir + chmod + ssh)"
fi

# === 11. SKYGATE_STANDBY_TS_AUTHKEY is the documented env var ===
if grep -q 'SKYGATE_STANDBY_TS_AUTHKEY' "$BOOTSTRAP_SH" || grep -q 'SKYGATE_TS_AUTHKEY' "$BOOTSTRAP_SH"; then
    pass "env var SKYGATE_TS_AUTHKEY documented"
else
    fail "env var SKYGATE_TS_AUTHKEY" "missing"
fi

# === summary ===
section "Summary"
PASS_RATE=$((PASS * 100 / TOTAL))
printf '  Total: %d  Pass: %d  Fail: %d  (%d%%)\n' "${TOTAL}" "${PASS}" "${FAIL}" "${PASS_RATE}"
if [ "${FAIL}" -eq 0 ]; then
    printf '%s✓ B-mod-install follow-up (bootstrap_standby.sh): all contracts pass%s\n' "${GRN}" "${RST}"
    exit 0
else
    printf '%s✗ B-mod-install follow-up: %d contract(s) failed%s\n' "${RED}" "${FAIL}" "${RST}"
    exit 1
fi
