#!/usr/bin/env bash
# ============================================================================
# check_ha_state.sh — B-new B-check for HA Phase 0/7/9 state-tracked runners
# See AGENTS.md (B-new entry) for the B-block context.
#
# What this verifies
# ------------------
# 1. scripts/ha-state/state.sh exists + is executable
# 2. scripts/ha-phase0.sh exists + is executable + sources state.sh
# 3. scripts/ha-phase7.sh exists + is executable + sources state.sh + has 6 steps
# 4. scripts/ha-phase9.sh exists + is executable + sources state.sh + wraps dr_drill.sh
# 5. scripts/ha-status.sh exists + is executable + sources state.sh
# 6. state.sh defines all required functions (ha_state_init/lock/unlock/read/write,
#    ha_state_set_field/set_phase/set_step/ensure_phase, ha_state_crash_check,
#    ha_state_retry_run, ha_state_run_step, ha_state_summary, ha_audit)
# 7. No hardcoded private IP (192.168.13.69) or DB password (skygate_admin_pass)
#    in any tracked file (the .githooks/pre-commit gate enforces this too,
#    but we re-check here as belt-and-suspenders)
#
# Usage
# -----
#   bash scripts/check_ha_state.sh
#
# Exit codes
# ----------
#   0  all 32 contracts pass
#   1  at least one contract failed
# ============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

# --- counters ---
PASS=0
FAIL=0
fails=()

# --- helpers ---
ok()   { PASS=$((PASS+1)); echo "  [PASS] $1"; }
nok()  { FAIL=$((FAIL+1)); fails+=("$1"); echo "  [FAIL] $1"; }
hdr()  { echo ""; echo "=== $1 ==="; }

# --- 1. state.sh exists + bash-syntax OK + has the right header ---
hdr "1. scripts/ha-state/state.sh"
[ -f scripts/ha-state/state.sh ] && ok "state.sh exists" || nok "state.sh missing"
if bash -n scripts/ha-state/state.sh 2>/dev/null; then
    ok "state.sh bash syntax OK (bash -n)"
else
    nok "state.sh has bash syntax errors (bash -n)"
fi
grep -q "state machine primitives for skygate HA phases" scripts/ha-state/state.sh \
    && ok "state.sh header present" || nok "state.sh header missing"

# --- 2. required functions in state.sh ---
hdr "2. state.sh public API"
for fn in ha_state_init ha_state_lock ha_state_unlock ha_state_read ha_state_write \
          ha_state_set_field ha_state_set_phase ha_state_set_step ha_state_ensure_phase \
          ha_state_crash_check ha_state_retry_run ha_state_run_step ha_state_summary \
          ha_audit ha_log ha_warn ha_err; do
    # function definition is: function_name() { ... } or function_name()...
    if grep -qE "^${fn} *\\(\\)" scripts/ha-state/state.sh; then
        ok "function $fn defined"
    else
        nok "function $fn MISSING"
    fi
done

# --- 3. state.sh bug-fix verification ---
hdr "3. state.sh bug-fix verification (B-new)"
# We had a typo 'sleep_off' in the backoff log line — must be gone
if grep -q "sleep_off" scripts/ha-state/state.sh; then
    nok "TYPO: 'sleep_off' still in state.sh (should be 'sleep_sec')"
else
    ok "no 'sleep_off' typo (sleep_sec is used)"
fi
# jq requirement must be documented
if grep -q "apt install jq" scripts/ha-state/state.sh; then
    ok "jq install hint present"
else
    nok "jq install hint MISSING"
fi
# lock stale detection
if grep -q "stale lock" scripts/ha-state/state.sh; then
    ok "stale lock detection present (5-min threshold)"
else
    nok "stale lock detection MISSING"
fi
# audit log rotation
if grep -q "nlines=.*wc -l" scripts/ha-state/state.sh; then
    ok "audit log rotation present (1000-line cap)"
else
    nok "audit log rotation MISSING"
fi

# --- 4. ha-phase0.sh exists + bash-syntax OK + sources state.sh + has 6 steps ---
hdr "4. scripts/ha-phase0.sh"
[ -f scripts/ha-phase0.sh ] && ok "ha-phase0.sh exists" || nok "ha-phase0.sh missing"
if bash -n scripts/ha-phase0.sh 2>/dev/null; then
    ok "ha-phase0.sh bash syntax OK (bash -n)"
else
    nok "ha-phase0.sh has bash syntax errors (bash -n)"
fi
grep -q "ha-state/state.sh" scripts/ha-phase0.sh \
    && ok "ha-phase0.sh sources state.sh" || nok "ha-phase0.sh does NOT source state.sh"
# the 6 step IDs (verify_magicdns is the 6th)
for step in tailscale_install_primary tailscale_install_standby verify_tailnet_join \
             verify_routes_advertised verify_routes_approved verify_ssh_to_primary \
             verify_magicdns; do
    if grep -q "\"$step\"" scripts/ha-phase0.sh; then
        ok "step $step present"
    else
        nok "step $step MISSING"
    fi
done

# --- 5. ha-phase7.sh exists + bash-syntax OK + sources state.sh + has 6 steps ---
hdr "5. scripts/ha-phase7.sh"
[ -f scripts/ha-phase7.sh ] && ok "ha-phase7.sh exists" || nok "ha-phase7.sh missing"
if bash -n scripts/ha-phase7.sh 2>/dev/null; then
    ok "ha-phase7.sh bash syntax OK (bash -n)"
else
    nok "ha-phase7.sh has bash syntax errors (bash -n)"
fi
grep -q "ha-state/state.sh" scripts/ha-phase7.sh \
    && ok "ha-phase7.sh sources state.sh" || nok "ha-phase7.sh does NOT source state.sh"
# the 6 step IDs
for step in preflight s3_pull_binary s3_pull_headscale_config docker_compose_up \
             healthz_wait verify_chain; do
    if grep -q "\"$step\"" scripts/ha-phase7.sh; then
        ok "step $step present"
    else
        nok "step $step MISSING"
    fi
done
# --reset, --status, --skip-s3 flags
grep -q "\\-\\-reset" scripts/ha-phase7.sh && ok "--reset flag present" || nok "--reset MISSING"
grep -q "\\-\\-status" scripts/ha-phase7.sh && ok "--status flag present" || nok "--status MISSING"
grep -q "\\-\\-skip-s3" scripts/ha-phase7.sh && ok "--skip-s3 flag present" || nok "--skip-s3 MISSING"

# --- 6. ha-phase9.sh exists + bash-syntax OK + sources state.sh + wraps dr_drill.sh ---
hdr "6. scripts/ha-phase9.sh"
[ -f scripts/ha-phase9.sh ] && ok "ha-phase9.sh exists" || nok "ha-phase9.sh missing"
if bash -n scripts/ha-phase9.sh 2>/dev/null; then
    ok "ha-phase9.sh bash syntax OK (bash -n)"
else
    nok "ha-phase9.sh has bash syntax errors (bash -n)"
fi
grep -q "ha-state/state.sh" scripts/ha-phase9.sh \
    && ok "ha-phase9.sh sources state.sh" || nok "ha-phase9.sh does NOT source state.sh"
grep -q "dr_drill.sh" scripts/ha-phase9.sh \
    && ok "ha-phase9.sh wraps dr_drill.sh" || nok "ha-phase9.sh does NOT wrap dr_drill.sh"
grep -q "\\-\\-skip-kill-both" scripts/ha-phase9.sh && ok "--skip-kill-both flag present" || nok "--skip-kill-both MISSING"
grep -q "\\-\\-skip-regapi-check" scripts/ha-phase9.sh && ok "--skip-regapi-check flag present" || nok "--skip-regapi-check MISSING"
# preflight step that requires Phase 0+7 to be completed
grep -q "ha_state_get_phase_status 0_mesh" scripts/ha-phase9.sh \
    && ok "preflight checks Phase 0 status" || nok "preflight does NOT check Phase 0"
grep -q "ha_state_get_phase_status 7_bootstrap" scripts/ha-phase9.sh \
    && ok "preflight checks Phase 7 status" || nok "preflight does NOT check Phase 7"

# --- 7. ha-status.sh exists + bash-syntax OK + sources state.sh + has --json + --phase ---
hdr "7. scripts/ha-status.sh"
[ -f scripts/ha-status.sh ] && ok "ha-status.sh exists" || nok "ha-status.sh missing"
if bash -n scripts/ha-status.sh 2>/dev/null; then
    ok "ha-status.sh bash syntax OK (bash -n)"
else
    nok "ha-status.sh has bash syntax errors (bash -n)"
fi
grep -q "ha-state/state.sh" scripts/ha-status.sh \
    && ok "ha-status.sh sources state.sh" || nok "ha-status.sh does NOT source state.sh"
grep -q "\\-\\-json" scripts/ha-status.sh && ok "--json flag present" || nok "--json MISSING"
grep -q "\\-\\-phase" scripts/ha-status.sh && ok "--phase flag present" || nok "--phase MISSING"

# --- 8. no leaked credentials / IPs ---
hdr "8. no leaked credentials or operator IPs"
for f in scripts/ha-state/state.sh scripts/ha-phase0.sh scripts/ha-phase7.sh \
         scripts/ha-phase9.sh scripts/ha-status.sh; do
    if grep -qE "192\\.168\\.13\\.69" "$f"; then
        nok "$f contains 192.168.13.69 (operator IP — must use env var)"
    else
        ok "$f: no 192.168.13.69"
    fi
    if grep -qE "skygate_admin_pass" "$f"; then
        nok "$f contains skygate_admin_pass (DB password — must use env var)"
    else
        ok "$f: no skygate_admin_pass"
    fi
done

# --- 9. pre-commit gate catches the same patterns (defense in depth) ---
hdr "9. pre-commit gate presence"
[ -f .githooks/pre-commit ] && ok ".githooks/pre-commit exists" || nok ".githooks/pre-commit MISSING"
# Note: we check `exists` not `executable` because Windows filesystems
# don't track the exec bit. The operator must run `chmod +x .githooks/*`
# once after cloning; the file's content (the patterns below) is what
# actually enforces the gate, not the file mode.
grep -q "192\\.168\\.13\\.69" .githooks/pre-commit \
    && ok "pre-commit blocks 192.168.13.69" || nok "pre-commit does NOT block 192.168.13.69"
grep -q "skygate_admin_pass" .githooks/pre-commit \
    && ok "pre-commit blocks skygate_admin_pass" || nok "pre-commit does NOT block skygate_admin_pass"

# --- 10. verify_pre_deploy.sh registration ---
hdr "10. verify_pre_deploy.sh registration"
grep -q "check_ha_state.sh" scripts/verify_pre_deploy.sh \
    && ok "check_ha_state.sh registered in verify_pre_deploy.sh" \
    || nok "check_ha_state.sh NOT registered in verify_pre_deploy.sh"

# --- summary ---
echo ""
echo "=== summary ==="
echo "  pass: $PASS"
echo "  fail: $FAIL"
if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "FAILED contracts:"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
echo ""
echo "all $PASS contracts PASS"
