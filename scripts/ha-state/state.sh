#!/usr/bin/env bash
# ============================================================================
# state.sh — state machine primitives for skygate HA phases
# B-new (v1.5.0+) — Phase 0/7/9 runner state tracking.
#
# See docs/internal/ha-v1.5.0-execution.md for context.
#
# This file is sourced (not executed) by ha-phase*.sh scripts.
# It provides:
#   - State file location + read/write helpers
#   - Phase status enum (pending|running|completed|failed)
#   - Step status tracking
#   - Atomic state transition (read → modify → write with .lock)
#   - Active recovery (auto-retry with exponential backoff)
#   - Audit log append (rotating, capped at 1000 entries)
#   - Crash detection (stale "running" status at boot)
#
# State file location: ${SKYGATE_HA_STATE:-/var/lib/skygate/ha-state/state.json}
# Audit log: ${SKYGATE_HA_STATE:-/var/lib/skygate/ha-state}/audit.log
# Lock file: ${SKYGATE_HA_STATE:-/var/lib/skygate/ha-state}/.lock
# Crash marker: ${SKYGATE_HA_STATE:-/var/lib/skygate/ha-state}/.crash
#
# State file schema (JSON):
#   {
#     "schema_version": 1,
#     "run_id": "<uuid>",
#     "started_at": "<rfc3339>",
#     "updated_at": "<rfc3339>",
#     "node": {"hostname": "<>", "role": "primary|standby|drill", "tailscale_ip": "<>"},
#     "phases": {
#       "0_mesh": {"status": "...", "started_at": "...", "completed_at": "...",
#                    "attempts": N, "max_attempts": 3, "last_error": "...",
#                    "steps": {"tailscale_install": {"status": "...", "output": "..."}}},
#       "7_bootstrap": {...},
#       "9_drill": {...}
#     },
#     "last_good_state": {"phase": "X", "at": "..."}
#   }
# ============================================================================
set -euo pipefail

# ---------- paths ----------
HA_STATE_DIR="${SKYGATE_HA_STATE:-/var/lib/skygate/ha-state}"
HA_STATE_FILE="$HA_STATE_DIR/state.json"
HA_AUDIT_FILE="$HA_STATE_DIR/audit.log"
HA_LOCK_FILE="$HA_STATE_DIR/.lock"
HA_CRASH_FILE="$HA_STATE_DIR/.crash"

# ---------- colors (only if TTY) ----------
if [ -t 1 ]; then
    _RED='\033[0;31m'; _GREEN='\033[0;32m'; _YELLOW='\033[1;33m'; _CYAN='\033[0;36m'; _NC='\033[0m'
else
    _RED=''; _GREEN=''; _YELLOW=''; _CYAN=''; _NC=''
fi

# ---------- logging primitives (stderr so we don't pollute stdout/captured) ----------
ha_log()   { echo -e "${_GREEN}[ha]${_NC} $*" >&2; }
ha_warn()  { echo -e "${_YELLOW}[WARN]${_NC} $*" >&2; }
ha_err()   { echo -e "${_RED}[ERROR]${_NC} $*" >&2; }
ha_dbg()   { [ "${SKYGATE_HA_DEBUG:-0}" = "1" ] && echo -e "${_CYAN}[dbg]${_NC} $*" >&2 || true; }

# ---------- uuid (RFC 4122 v4 via /proc + $RANDOM) ----------
# Avoids depending on uuidgen or python3.
ha_uuid() {
    if [ -r /proc/sys/kernel/random/uuid ]; then
        cat /proc/sys/kernel/random/uuid
        return
    fi
    # Fallback (rare): use $RANDOM + epoch
    printf '%04x%04x-%04x-4%03x-%04x-%04x%04x%04x\n' \
        $((RANDOM%65536)) $((RANDOM%65536)) $((RANDOM%65536)) \
        $((RANDOM%4096)) $((RANDOM%65536)) \
        $((RANDOM%65536)) $((RANDOM%65536)) $((RANDOM%65536))
}

# ---------- rfc3339 timestamp ----------
ha_now() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
ha_epoch() { date -u +"%s"; }

# ---------- ensure state dir exists ----------
ha_state_init() {
    mkdir -p "$HA_STATE_DIR"
    # State file: create if missing
    if [ ! -f "$HA_STATE_FILE" ]; then
        local rid; rid=$(ha_uuid)
        cat > "$HA_STATE_FILE" <<EOF
{
  "schema_version": 1,
  "run_id": "$rid",
  "started_at": "$(ha_now)",
  "updated_at": "$(ha_now)",
  "node": {"hostname": "$(hostname 2>/dev/null || echo unknown)", "role": "unknown", "tailscale_ip": ""},
  "phases": {},
  "last_good_state": null
}
EOF
        ha_log "initialized state file $HA_STATE_FILE (run_id=$rid)"
    fi
    # Audit log: create if missing
    [ -f "$HA_AUDIT_FILE" ] || : > "$HA_AUDIT_FILE"
    # Lock file: ensure dir is writable
    [ -w "$HA_STATE_DIR" ] || { ha_err "state dir $HA_STATE_DIR not writable"; return 1; }
}

# ---------- audit log (rotating) ----------
# Writes a single line: <ts> <event> <key=value>...
# Caps audit.log at 1000 lines (tail to .bak, keep current).
ha_audit() {
    local event="$1"; shift
    ha_state_init || return 0  # never fail the caller on audit
    {
        printf '%s %s' "$(ha_now)" "$event"
        for kv in "$@"; do
            # quote values with spaces (key=value form)
            printf ' %s' "$kv"
        done
        printf '\n'
    } >> "$HA_AUDIT_FILE" 2>/dev/null || true
    # Rotate if > 1000 lines
    local nlines
    nlines=$(wc -l < "$HA_AUDIT_FILE" 2>/dev/null || echo 0)
    if [ "$nlines" -gt 1000 ]; then
        tail -n 800 "$HA_AUDIT_FILE" > "$HA_AUDIT_FILE.bak" 2>/dev/null || true
        mv "$HA_AUDIT_FILE.bak" "$HA_AUDIT_FILE" 2>/dev/null || true
    fi
    ha_dbg "audit: $event $@"
}

# ---------- read state (returns JSON via stdout) ----------
# Atomic read via mkdir-based lock (POSIX-portable, no flock needed).
ha_state_lock() {
    local lockdir="$HA_LOCK_FILE.lock"
    local waited=0
    while ! mkdir "$lockdir" 2>/dev/null; do
        waited=$((waited + 1))
        # Stale lock detection: if older than 5 min, the holder crashed
        if [ "$waited" -gt 300 ]; then
            local lockage
            lockage=$(( $(date +%s) - $(stat -c %Y "$lockdir" 2>/dev/null || echo 0) ))
            if [ "$lockage" -gt 300 ]; then
                ha_warn "stale lock $lockdir age=${lockage}s, breaking it"
                rm -rf "$lockdir" 2>/dev/null || true
                continue
            fi
        fi
        sleep 1
    done
    # Lock acquired. Trap to ensure cleanup on exit.
    trap "rm -rf '$lockdir' 2>/dev/null || true" EXIT
}

ha_state_unlock() {
    rm -rf "$HA_LOCK_FILE.lock" 2>/dev/null || true
    trap - EXIT
}

# Read state file (caller must hold lock)
ha_state_read() {
    if [ ! -f "$HA_STATE_FILE" ]; then
        ha_state_init
    fi
    cat "$HA_STATE_FILE"
}

# Write state atomically: tmp file + fsync + rename
# Args: new state JSON content (via stdin or $1)
ha_state_write() {
    local content
    if [ $# -gt 0 ]; then
        content="$1"
    else
        content=$(cat)
    fi
    local tmpfile
    tmpfile=$(mktemp "$HA_STATE_DIR/.state.XXXXXX")
    printf '%s\n' "$content" > "$tmpfile"
    # best-effort fsync (Linux only)
    command -v sync >/dev/null 2>&1 && sync "$tmpfile" 2>/dev/null || true
    mv -f "$tmpfile" "$HA_STATE_FILE"
}

# Update a single field using jq (atomic under lock).
# Args: <jsonpath-expression> <new-value-as-json>
# Example: ha_state_set_field '.node.hostname' '"skygate-host-1"'
ha_state_set_field() {
    local path="$1" new="$2"
    ha_state_lock
    local cur
    cur=$(ha_state_read)
    local new_state
    if command -v jq >/dev/null 2>&1; then
        new_state=$(printf '%s' "$cur" | jq --arg v "$new" "$path = \$v")
    else
        ha_err "jq not found — install jq for state updates (apt install jq / brew install jq)"
        ha_state_unlock
        return 1
    fi
    ha_state_write "$new_state"
    ha_state_unlock
}

# Set a phase field (status, attempts, etc.).
# Args: <phase-id> <field> <value>
# Example: ha_state_set_phase 0_mesh status running
ha_state_set_phase() {
    local phase="$1" field="$2" value="$3"
    ha_state_lock
    local cur; cur=$(ha_state_read)
    local new_state
    if command -v jq >/dev/null 2>&1; then
        new_state=$(printf '%s' "$cur" | jq --arg v "$value" ".phases[\"$phase\"].$field = \$v | .updated_at = \"$(ha_now)\"")
    else
        ha_err "jq not found"
        ha_state_unlock
        return 1
    fi
    ha_state_write "$new_state"
    ha_state_unlock
}

# Set a step field within a phase.
# Args: <phase-id> <step-id> <field> <value>
ha_state_set_step() {
    local phase="$1" step="$2" field="$3" value="$4"
    ha_state_lock
    local cur; cur=$(ha_state_read)
    local new_state
    if command -v jq >/dev/null 2>&1; then
        new_state=$(printf '%s' "$cur" | jq --arg v "$value" \
            ".phases[\"$phase\"].steps[\"$step\"].$field = \$v | .updated_at = \"$(ha_now)\"")
    else
        ha_err "jq not found"
        ha_state_unlock
        return 1
    fi
    ha_state_write "$new_state"
    ha_state_unlock
}

# Initialize a phase if it doesn't exist yet.
# Args: <phase-id> <max-attempts>
ha_state_ensure_phase() {
    local phase="$1" max_attempts="${2:-3}"
    ha_state_lock
    local cur; cur=$(ha_state_read)
    if command -v jq >/dev/null 2>&1; then
        local exists
        exists=$(printf '%s' "$cur" | jq --arg p "$phase" '.phases[$p] != null')
        if [ "$exists" = "false" ]; then
            local new_state
            new_state=$(printf '%s' "$cur" | jq --arg p "$phase" --argjson m "$max_attempts" \
                '.phases[$p] = {
                    "status": "pending",
                    "started_at": null,
                    "completed_at": null,
                    "attempts": 0,
                    "max_attempts": $m,
                    "last_error": null,
                    "steps": {}
                } | .updated_at = now')
            ha_state_write "$new_state"
            ha_log "initialized phase '$phase' (max_attempts=$max_attempts)"
        fi
    fi
    ha_state_unlock
}

# ---------- phase status enum ----------
# valid: pending, running, completed, failed
ha_state_get_phase_status() {
    local phase="$1"
    ha_state_lock
    local cur; cur=$(ha_state_read)
    local status
    if command -v jq >/dev/null 2>&1; then
        status=$(printf '%s' "$cur" | jq -r --arg p "$phase" '.phases[$p].status // "pending"')
    else
        status="pending"
    fi
    ha_state_unlock
    printf '%s' "$status"
}

ha_state_get_phase_attempts() {
    local phase="$1"
    ha_state_lock
    local cur; cur=$(ha_state_read)
    local n
    if command -v jq >/dev/null 2>&1; then
        n=$(printf '%s' "$cur" | jq -r --arg p "$phase" '.phases[$p].attempts // 0')
    else
        n=0
    fi
    ha_state_unlock
    printf '%s' "$n"
}

# ---------- crash detection ----------
# Run at boot of any ha-phase*.sh. If a previous run is still
# marked "running" in the state file, that's a crash — mark as
# failed, record the crash, alert.
ha_state_crash_check() {
    ha_state_lock
    local cur; cur=$(ha_state_read)
    local crashed_phase
    if command -v jq >/dev/null 2>&1; then
        crashed_phase=$(printf '%s' "$cur" | jq -r '
            .phases | to_entries[] | select(.value.status == "running") | .key' | head -1)
    else
        crashed_phase=""
    fi
    if [ -n "$crashed_phase" ]; then
        ha_warn "detected crash: phase '$crashed_phase' was 'running' at last shutdown"
        # Mark as failed
        local new_state
        new_state=$(printf '%s' "$cur" | jq --arg p "$crashed_phase" \
            '.phases[$p].status = "failed" | .phases[$p].last_error = "interrupted (crash detected at boot)" | .updated_at = now')
        ha_state_write "$new_state"
        # Write crash marker for operator attention
        echo "$(ha_now) phase=$crashed_phase status=interrupted" > "$HA_CRASH_FILE"
        ha_audit "crash_detected" "phase=$crashed_phase" "previous_state=running"
    fi
    ha_state_unlock
}

# ---------- active recovery (retry with exponential backoff) ----------
# Args: <phase-id> <max-attempts> <base-backoff-sec> <command...>
# Runs command, on failure sleeps with backoff 2^n*base, retries.
# On success, marks phase completed. On max attempts, marks failed.
# MUST be called AFTER ha_state_ensure_phase.
ha_state_retry_run() {
    local phase="$1" max_attempts="$2" base_backoff="$3"
    shift 3
    local attempts
    attempts=$(ha_state_get_phase_attempts "$phase")
    attempts=$((attempts + 1))
    ha_state_set_phase "$phase" attempts "$attempts"
    ha_state_set_phase "$phase" status running
    ha_state_set_phase "$phase" started_at "$(ha_now)"
    ha_audit "phase_retry_start" "phase=$phase" "attempt=$attempts" "max=$max_attempts"
    local rc=0 output err_output
    while [ "$attempts" -le "$max_attempts" ]; do
        ha_log "phase '$phase' attempt $attempts/$max_attempts"
        if output=$("$@" 2>&1); then
            ha_state_set_phase "$phase" status completed
            ha_state_set_phase "$phase" completed_at "$(ha_now)"
            ha_audit "phase_completed" "phase=$phase" "attempts=$attempts"
            printf '%s' "$output"  # return last successful stdout
            return 0
        else
            rc=$?
            err_output="$output"
            ha_warn "phase '$phase' attempt $attempts failed (rc=$rc): $err_output"
            ha_state_set_phase "$phase" last_error "$err_output"
            ha_audit "phase_attempt_failed" "phase=$phase" "attempt=$attempts" "rc=$rc" "error=$err_output"
            if [ "$attempts" -ge "$max_attempts" ]; then
                ha_state_set_phase "$phase" status failed
                ha_audit "phase_failed" "phase=$phase" "attempts=$attempts" "max=$max_attempts"
                # Write crash marker for operator attention
                echo "$(ha_now) phase=$phase status=failed attempts=$attempts" > "$HA_CRASH_FILE"
                return 1
            fi
            # Exponential backoff: base * 2^(attempt-1)
            local sleep_sec
            sleep_sec=$(( base_backoff * (1 << (attempts - 1)) ))
            # Cap at 300s (5 min) for sanity
            [ "$sleep_sec" -gt 300 ] && sleep_sec=300
            ha_log "  backing off ${sleep_sec}s before next attempt"
            sleep "$sleep_sec"
            attempts=$((attempts + 1))
            ha_state_set_phase "$phase" attempts "$attempts"
        fi
    done
    return 1
}

# ---------- step wrapper (single command within a phase) ----------
# Args: <phase-id> <step-id> <command...>
# Records step status (pending|running|completed|failed) in the phase.
ha_state_run_step() {
    local phase="$1" step="$2"
    shift 2
    ha_state_lock
    local cur; cur=$(ha_state_read)
    if command -v jq >/dev/null 2>&1; then
        local new_state
        new_state=$(printf '%s' "$cur" | jq --arg p "$phase" --arg s "$step" \
            '.phases[$p].steps[$s] = {"status": "running", "started_at": now, "output": ""}')
        ha_state_write "$new_state"
    fi
    ha_state_unlock
    ha_log "step '$phase/$step' running"
    if output=$("$@" 2>&1); then
        ha_state_lock
        cur=$(ha_state_read)
        if command -v jq >/dev/null 2>&1; then
            local escaped
            escaped=$(printf '%s' "$output" | jq -Rs .)
            new_state=$(printf '%s' "$cur" | jq --arg p "$phase" --arg s "$step" --argjson o "$escaped" \
                '.phases[$p].steps[$s].status = "completed" | .phases[$p].steps[$s].completed_at = now | .phases[$p].steps[$s].output = $o')
            ha_state_write "$new_state"
        fi
        ha_state_unlock
        ha_log "step '$phase/$step' completed"
        ha_audit "step_completed" "phase=$phase" "step=$step"
        return 0
    else
        local rc=$?
        ha_state_lock
        cur=$(ha_state_read)
        if command -v jq >/dev/null 2>&1; then
            local escaped
            escaped=$(printf '%s' "$output" | jq -Rs .)
            new_state=$(printf '%s' "$cur" | jq --arg p "$phase" --arg s "$step" --argjson o "$escaped" --argjson r "$rc" \
                '.phases[$p].steps[$s].status = "failed" | .phases[$p].steps[$s].completed_at = now | .phases[$p].steps[$s].output = $o | .phases[$p].steps[$s].rc = $r')
            ha_state_write "$new_state"
        fi
        ha_state_unlock
        ha_err "step '$phase/$step' failed (rc=$rc)"
        ha_audit "step_failed" "phase=$phase" "step=$step" "rc=$rc"
        return "$rc"
    fi
}

# ---------- public status report ----------
# Prints a one-line summary of all phases.
ha_state_summary() {
    ha_state_lock
    local cur; cur=$(ha_state_read)
    ha_state_unlock
    if command -v jq >/dev/null 2>&1; then
        printf '%s\n' "$cur" | jq -r '
            "ha-state summary:",
            "  run_id:    \(.run_id)",
            "  started:   \(.started_at)",
            "  updated:   \(.updated_at)",
            "  node:      \(.node.hostname) (role=\(.node.role))",
            "  phases:",
            (.phases | to_entries[] |
                "    - \(.key): \(.value.status) attempts=\(.value.attempts)/\(.value.max_attempts) last_error=\(.value.last_error // "none")"),
            (.last_good_state // "  last_good_state: none")
        '
    else
        cat "$HA_STATE_FILE"
    fi
}
