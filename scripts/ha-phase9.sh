#!/usr/bin/env bash
# ============================================================================
# ha-phase9.sh — Phase 9 of HA v1.5.0: live DR drill
# B-new (v1.5.0+) — state-tracked wrapper around scripts/dr_drill.sh.
#
# See docs/internal/ha-v1.5.0-execution.md §3 (Phase 9).
#
# What this does
# --------------
# Same as scripts/dr_drill.sh (B153), but with state tracking:
#   - preflight: verify Phase 0 + Phase 7 are both completed in state file
#   - run_drill: call dr_drill.sh with --yes (unattended mode)
#   - post_verify: parse the drill output to extract pass/fail counts
#   - audit log + crash detection
#
# When to run this
# ----------------
# AFTER Phase 0 (Tailscale mesh) + Phase 7 (bootstrap standby) are both
# completed in the state file. Schedule during a low-traffic maintenance
# window (e.g. Sunday 03:00 UTC). The drill takes ~5-7 minutes.
#
# This script does NOT replace dr_drill.sh. dr_drill.sh is the "vanilla"
# implementation; ha-phase9.sh adds state tracking + auto-yes for
# unattended maintenance windows. Both produce the same end state.
#
# Usage
# -----
#   ssh skygate-host-1   (or any node with access to both primary + standby)
#   cd ~/skygate
#   bash scripts/ha-phase9.sh
#   # or --reset to re-run from scratch
#   # or --status to just print the current state
#   # or --skip-kill-both to skip step 5 (kill BOTH nodes)
#   # or --skip-regapi-check to skip step 4 (DNS verification)
# ============================================================================
set -euo pipefail

# --- source state machine ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HA_LIB="$SCRIPT_DIR/ha-state/state.sh"
DRILL_SCRIPT="$SCRIPT_DIR/dr_drill.sh"
[ -f "$HA_LIB" ] || { echo "FATAL: state machine lib not found: $HA_LIB" >&2; exit 1; }
[ -f "$DRILL_SCRIPT" ] || { echo "FATAL: dr_drill.sh not found: $DRILL_SCRIPT" >&2; exit 1; }
# shellcheck disable=SC1091
. "$HA_LIB"

# --- args ---
PHASE_ID="9_drill"
MAX_ATTEMPTS=2          # drill is destructive, retry max 2 times
BASE_BACKOFF=5          # backoff 5s/10s between attempts
DO_RESET=0
DO_STATUS=0
SKIP_REGAPI_CHECK=0
SKIP_KILL_BOTH=0
while [ $# -gt 0 ]; do
    case "$1" in
        --reset) DO_RESET=1; shift;;
        --status) DO_STATUS=1; shift;;
        --skip-regapi-check) SKIP_REGAPI_CHECK=1; shift;;
        --skip-kill-both) SKIP_KILL_BOTH=1; shift;;
        --attempts) MAX_ATTEMPTS="$2"; shift 2;;
        --backoff) BASE_BACKOFF="$2"; shift 2;;
        --help|-h)
            sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) ha_err "unknown flag: $1"; exit 2;;
    esac
done

# --- init state ---
ha_state_init
ha_state_crash_check

# reset: clear the phase so it re-runs from scratch
if [ "$DO_RESET" = "1" ]; then
    ha_log "resetting phase '$PHASE_ID'"
    ha_state_lock
    reset_cur=$(ha_state_read)
    if command -v jq >/dev/null 2>&1; then
        reset_new=$(printf '%s' "$reset_cur" | jq --arg p "$PHASE_ID" \
            'del(.phases[$p]) | .updated_at = now')
        ha_state_write "$reset_new"
    fi
    ha_state_unlock
fi

ha_state_ensure_phase "$PHASE_ID" "$MAX_ATTEMPTS"

# status-only mode
if [ "$DO_STATUS" = "1" ]; then
    ha_state_summary
    ha_log "phase '$PHASE_ID' status: $(ha_state_get_phase_status "$PHASE_ID")"
    exit 0
fi

ha_log "=== ha-phase9.sh (live DR drill for HA) ==="
ha_log "max attempts: $MAX_ATTEMPTS, base backoff: ${BASE_BACKOFF}s"
[ "$SKIP_REGAPI_CHECK" = "1" ] && ha_log "skip-regapi-check: yes"
[ "$SKIP_KILL_BOTH" = "1" ] && ha_log "skip-kill-both: yes"

# --- step 1: preflight — Phase 0 + Phase 7 must be completed ---
ha_state_run_step "$PHASE_ID" "preflight" bash -c '
    phase0_status=$(ha_state_get_phase_status 0_mesh)
    phase7_status=$(ha_state_get_phase_status 7_bootstrap)
    if [ "$phase0_status" != "completed" ]; then
        echo "Phase 0 (Tailscale mesh) not completed: status=$phase0_status" >&2
        echo "fix: run bash scripts/ha-phase0.sh first" >&2
        exit 1
    fi
    if [ "$phase7_status" != "completed" ]; then
        echo "Phase 7 (bootstrap standby) not completed: status=$phase7_status" >&2
        echo "fix: ssh '"$STANDBY_HOST"' and run bash scripts/ha-phase7.sh" >&2
        exit 1
    fi
    echo "preflight ok: Phase 0 = $phase0_status, Phase 7 = $phase7_status"
' || {
    ha_err "preflight failed — Phase 0 or 7 not completed"
    ha_audit "phase9_failed" "phase=$PHASE_ID" "step=preflight"
    exit 1
}

# --- step 2: run the drill (with --yes for unattended mode) ---
DRILL_FLAGS="--yes"
[ "$SKIP_REGAPI_CHECK" = "1" ] && DRILL_FLAGS="$DRILL_FLAGS --skip-regapi-check"
[ "$SKIP_KILL_BOTH" = "1" ] && DRILL_FLAGS="$DRILL_FLAGS --skip-kill-both"
ha_log "running: bash $DRILL_SCRIPT $DRILL_FLAGS"

# Capture drill output to a temp file so we can parse it for the
# step results in step 3. dr_drill.sh writes to stderr; we
# redirect both to a file.
DRILL_OUT=$(mktemp "$HA_STATE_DIR/.drill.XXXXXX")
if ha_state_run_step "$PHASE_ID" "run_drill" bash -c "
    bash '$DRILL_SCRIPT' $DRILL_FLAGS >'$DRILL_OUT' 2>&1
"; then
    DRILL_OK=1
else
    DRILL_OK=0
fi

# --- step 3: parse drill output to count pass/fail ---
if [ "$DRILL_OK" = "1" ]; then
    # count 'PASS:' lines in the drill output
    PASS_COUNT=$(grep -c '^\s*PASS:' "$DRILL_OUT" 2>/dev/null || echo 0)
    STEP_COUNT=$(grep -cE '^\[drill\].*step [0-9]/[0-9]' "$DRILL_OUT" 2>/dev/null || echo 0)
    ha_state_run_step "$PHASE_ID" "post_verify" bash -c "
        echo \"drill completed: \$PASS_COUNT pass markers across ~\$STEP_COUNT steps\"
        echo \"drill output saved at: $DRILL_OUT\"
    "
    ha_audit "drill_complete" "phase=$PHASE_ID" "pass_count=$PASS_COUNT" "step_count=$STEP_COUNT"
else
    # determine which step failed by scanning the drill output
    LAST_STEP=$(grep -oE 'step [0-9]/[0-9][^:]*' "$DRILL_OUT" 2>/dev/null | tail -1 || echo "unknown")
    ha_state_run_step "$PHASE_ID" "post_verify" bash -c "
        echo \"drill failed at: $LAST_STEP\" >&2
        echo \"drill output: $DRILL_OUT (last 30 lines):\" >&2
        tail -30 '$DRILL_OUT' >&2
    "
    ha_audit "drill_failed" "phase=$PHASE_ID" "last_step=$LAST_STEP" "output=$DRILL_OUT"
    ha_err "DR drill failed at: $LAST_STEP"
    ha_err "output saved at: $DRILL_OUT (last 30 lines: see step output above)"
    ha_state_set_phase "$PHASE_ID" status failed
    exit 1
fi

# --- done ---
ha_state_set_phase "$PHASE_ID" status completed
ha_state_set_phase "$PHASE_ID" completed_at "$(ha_now)"
ha_audit "phase_completed" "phase=$PHASE_ID"

ha_log ""
ha_log "=== ha-phase9.sh complete ==="
ha_log ""
ha_log "DR drill passed. next:"
ha_log "  1. open /admin/audit — confirm ha.failover + ha.standby_rejoin events are recorded"
ha_log "  2. tag the release: git tag v1.5.3 (or v1.6.0)"
ha_log "  3. cleanup: rm $DRILL_OUT"
ha_log ""
ha_log "drill output saved at: $DRILL_OUT"
