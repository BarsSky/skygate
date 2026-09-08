#!/usr/bin/env bash
# ============================================================================
# ha-status.sh — print the current HA state for all phases
# B-new (v1.5.0+) — operator convenience wrapper around ha_state_summary.
#
# See docs/internal/ha-v1.5.0-execution.md.
#
# What this does
# --------------
# Prints a one-page status report of all HA phases tracked in the state
# file. Useful for:
#   - "is the standby bootstrapped yet?" — check Phase 7 status
#   - "when was the last DR drill?" — check Phase 9 completed_at
#   - "is there a crash marker?" — check /var/lib/skygate/ha-state/.crash
#
# Usage
# -----
#   bash scripts/ha-status.sh            # full summary
#   bash scripts/ha-status.sh --json     # raw state.json (for piping to jq)
#   bash scripts/ha-status.sh --phase 7  # one phase only
# ============================================================================
set -euo pipefail

# --- source state machine ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HA_LIB="$SCRIPT_DIR/ha-state/state.sh"
[ -f "$HA_LIB" ] || { echo "FATAL: state machine lib not found: $HA_LIB" >&2; exit 1; }
# shellcheck disable=SC1091
. "$HA_LIB"

# --- args ---
MODE="summary"
PHASE_FILTER=""
while [ $# -gt 0 ]; do
    case "$1" in
        --json) MODE="json"; shift;;
        --phase) PHASE_FILTER="$2"; shift 2;;
        --help|-h)
            sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) ha_err "unknown flag: $1"; exit 2;;
    esac
done

# --- init state (does nothing if already exists) ---
ha_state_init

# --- dispatch ---
case "$MODE" in
    summary)
        echo ""
        ha_state_summary
        echo ""
        # crash marker
        if [ -f "$HA_CRASH_FILE" ]; then
            echo "[!] crash marker present at $HA_CRASH_FILE:"
            cat "$HA_CRASH_FILE"
            echo ""
        fi
        # audit log tail
        if [ -f "$HA_AUDIT_FILE" ]; then
            echo "audit log (last 5): $HA_AUDIT_FILE"
            tail -5 "$HA_AUDIT_FILE" 2>/dev/null | sed 's/^/  /'
            echo ""
        fi
        # phase filter
        if [ -n "$PHASE_FILTER" ]; then
            status=$(ha_state_get_phase_status "$PHASE_FILTER")
            attempts=$(ha_state_get_phase_attempts "$PHASE_FILTER")
            echo "phase '$PHASE_FILTER': status=$status attempts=$attempts"
        fi
        ;;
    json)
        if [ -n "$PHASE_FILTER" ]; then
            jq --arg p "$PHASE_FILTER" '.phases[$p]' "$HA_STATE_FILE" 2>/dev/null \
                || cat "$HA_STATE_FILE"
        else
            cat "$HA_STATE_FILE"
        fi
        ;;
esac
