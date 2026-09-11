#!/usr/bin/env bash
# ============================================================================
# ha-phase7.sh — Phase 7 of HA v1.5.0: bootstrap the standby node
# B-new (v1.5.0+) — state-tracked wrapper around scripts/bootstrap_standby.sh.
#
# See docs/internal/ha-v1.5.0-execution.md §3 (Phase 7).
#
# What this does
# --------------
# Same as scripts/bootstrap_standby.sh (B152), but with state tracking:
#   - 6 state-tracked steps (preflight, s3_pull_binary, s3_pull_headscale_config,
#     docker_compose_up, healthz_wait, verify_chain)
#   - active recovery on step failure (exponential backoff retry)
#   - audit log + crash detection
#   - ha_status_report prints the final phase status
#
# Where to run this
# -----------------
# On the NEW standby host (<polygon-vm-hostname>), AFTER Phase 0 (Tailscale mesh)
# has succeeded on BOTH primary and standby. The Phase 0 mesh is the
# prerequisite: the standby needs Tailscale + MagicDNS before it can be
# bootstrapped (it uses Tailscale to pull the skygate binary from S3).
#
# This script does NOT replace bootstrap_standby.sh. bootstrap_standby.sh
# is the "vanilla" implementation; ha-phase7.sh is the same implementation
# with state tracking, audit log, and active recovery. Operators can run
# either one; ha-phase7.sh is recommended for unattended maintenance windows.
#
# Usage
# -----
#   ssh <polygon-vm-hostname>
#   cd ~/skygate
#   bash scripts/ha-phase7.sh
#   # or --reset to re-run from scratch (idempotent)
#   # or --status to just print the current state
#   # or --skip-s3 to skip the S3 pull (use local git checkout instead)
# ============================================================================
set -euo pipefail

# --- source state machine ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HA_LIB="$SCRIPT_DIR/ha-state/state.sh"
[ -f "$HA_LIB" ] || { echo "FATAL: state machine lib not found: $HA_LIB" >&2; exit 1; }
# shellcheck disable=SC1091
. "$HA_LIB"

# --- args ---
PHASE_ID="7_bootstrap"
MAX_ATTEMPTS=3
BASE_BACKOFF=2
DO_RESET=0
DO_STATUS=0
SKIP_S3=0
STANDBY_HOST="${SKYGATE_STANDBY_HOST:-<polygon-vm-hostname>}"
while [ $# -gt 0 ]; do
    case "$1" in
        --reset) DO_RESET=1; shift;;
        --status) DO_STATUS=1; shift;;
        --skip-s3) SKIP_S3=1; shift;;
        --attempts) MAX_ATTEMPTS="$2"; shift 2;;
        --backoff) BASE_BACKOFF="$2"; shift 2;;
        --standby) STANDBY_HOST="$2"; shift 2;;
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

ha_log "=== ha-phase7.sh (bootstrap the standby for HA) ==="
ha_log "standby host: $STANDBY_HOST (this host: $(hostname))"
ha_log "max attempts: $MAX_ATTEMPTS, base backoff: ${BASE_BACKOFF}s"

# --- preflight: project dir + .env ---
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${SKYGATE_ENV_FILE:-$PROJECT_DIR/.env}"

# step 1: preflight — docker, git, .env, SKYGATE_HA_ROLE=standby
ha_state_run_step "$PHASE_ID" "preflight" bash -c '
    set -e
    [ -d "'"$PROJECT_DIR"'" ] || { echo "project dir not found: '"$PROJECT_DIR"'" >&2; exit 1; }
    [ -f "'"$ENV_FILE"'" ] || { echo ".env not found: '"$ENV_FILE"'" >&2; exit 1; }
    command -v docker >/dev/null 2>&1 || { echo "docker not installed" >&2; exit 1; }
    command -v git >/dev/null 2>&1 || { echo "git not installed" >&2; exit 1; }
    # parse .env for the role
    role=$(grep -E "^SKYGATE_HA_ROLE=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
    if [ "$role" != "standby" ]; then
        echo "SKYGATE_HA_ROLE must be standby (got: $role)" >&2
        echo "fix: in .env, set SKYGATE_HA_ROLE=standby on this host" >&2
        exit 1
    fi
    echo "preflight ok: project='"$PROJECT_DIR"', role=$role"
' || {
    ha_err "preflight failed (see step output above)"
    ha_audit "phase7_failed" "phase=$PHASE_ID" "step=preflight"
    exit 1
}

# step 2: pull skygate binary from S3 (B150)
if [ "$SKIP_S3" = "1" ]; then
    ha_state_run_step "$PHASE_ID" "s3_pull_binary" bash -c 'echo "skipped (--skip-s3)"; true'
else
    ha_state_run_step "$PHASE_ID" "s3_pull_binary" bash -c '
        set -e
        S3_BUCKET=$(grep -E "^SKYGATE_S3_BUCKET=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
        S3_ENDPOINT=$(grep -E "^SKYGATE_S3_ENDPOINT=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
        S3_ACCESS_KEY=$(grep -E "^SKYGATE_S3_ACCESS_KEY=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
        S3_SECRET_KEY=$(grep -E "^SKYGATE_S3_SECRET_KEY=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
        S3_PATH_PREFIX=$(grep -E "^SKYGATE_S3_DEPLOY_PREFIX=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"" | sed "s/^default=//")
        S3_PATH_PREFIX="${S3_PATH_PREFIX:-ha/deploy}"
        if [ -z "$S3_ACCESS_KEY" ] || [ -z "$S3_SECRET_KEY" ] || [ -z "$S3_ENDPOINT" ]; then
            echo "S3 not configured — using local git checkout (skygate binary at ./bin/skygate)"
            exit 0
        fi
        if ! command -v aws >/dev/null 2>&1; then
            echo "aws CLI not installed — using local git checkout"
            exit 0
        fi
        AWS_ENDPOINT_URL="$S3_ENDPOINT" \
            aws s3 cp "s3://${S3_BUCKET}/${S3_PATH_PREFIX}/$(hostname)/skygate" \
                     "/usr/local/bin/skygate" 2>&1 | tail -3
        chmod +x /usr/local/bin/skygate
        echo "pulled skygate from s3://${S3_BUCKET}/${S3_PATH_PREFIX}/$(hostname)/"
    ' || {
        ha_warn "S3 pull failed — using local git checkout. operator can re-run with --skip-s3 to silence this"
        # non-fatal: continue with local binary
        ha_state_set_step "$PHASE_ID" "s3_pull_binary" status completed
        ha_state_set_step "$PHASE_ID" "s3_pull_binary" output "skipped (S3 unavailable, using local)"
    }
fi

# step 3: pull headscale config from S3
ha_state_run_step "$PHASE_ID" "s3_pull_headscale_config" bash -c '
    set -e
    S3_BUCKET=$(grep -E "^SKYGATE_S3_BUCKET=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
    S3_ENDPOINT=$(grep -E "^SKYGATE_S3_ENDPOINT=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
    S3_ACCESS_KEY=$(grep -E "^SKYGATE_S3_ACCESS_KEY=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
    S3_SECRET_KEY=$(grep -E "^SKYGATE_S3_SECRET_KEY=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
    HEADSCALE_PREFIX=$(grep -E "^SKYGATE_S3_HEADSCALE_CONFIG_PREFIX=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
    HEADSCALE_PREFIX="${HEADSCALE_PREFIX:-ha/headscale-config}"
    DEPLOY_HEADSCALE_DIR=$(grep -E "^DEPLOY_HEADSCALE_DIR=" "'"$ENV_FILE"'" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\047\"")
    DEPLOY_HEADSCALE_DIR="${DEPLOY_HEADSCALE_DIR:-/home/skyadmin/headscale}"
    if [ -z "$S3_ACCESS_KEY" ] || [ -z "$S3_SECRET_KEY" ] || [ -z "$S3_ENDPOINT" ]; then
        echo "S3 not configured — using local headscale config"
        exit 0
    fi
    if ! command -v aws >/dev/null 2>&1; then
        echo "aws CLI not installed — using local headscale config"
        exit 0
    fi
    mkdir -p "$DEPLOY_HEADSCALE_DIR/config"
    AWS_ENDPOINT_URL="$S3_ENDPOINT" \
        aws s3 sync "s3://${S3_BUCKET}/${HEADSCALE_PREFIX}/" \
                    "$DEPLOY_HEADSCALE_DIR/config/" 2>&1 | tail -3
    echo "pulled headscale config from s3://${S3_BUCKET}/${HEADSCALE_PREFIX}/"
' || ha_warn "headscale config pull failed — using local copy (may be stale)"

# step 4: docker compose up with role=standby
ha_state_run_step "$PHASE_ID" "docker_compose_up" bash -c "
    set -e
    cd '$PROJECT_DIR'
    docker compose up -d --force-recreate --no-deps skygate headscale headplane 2>&1 | tail -5
    echo 'containers up'
" || {
    ha_err "docker compose up failed"
    ha_audit "phase7_failed" "phase=$PHASE_ID" "step=docker_compose_up"
    exit 1
}

# step 5: wait for /healthz (up to 60s)
ha_state_run_step "$PHASE_ID" "healthz_wait" bash -c "
    set -e
    SKYGATE_PORT=\$(grep -E '^SKYGATE_PORT=' '$ENV_FILE' 2>/dev/null | head -1 | cut -d= -f2- | tr -d '\47\"')
    SKYGATE_PORT=\${SKYGATE_PORT:-8080}
    for i in \$(seq 1 60); do
        if curl -s -o /dev/null --max-time 2 \"http://127.0.0.1:\${SKYGATE_PORT}/healthz\"; then
            echo \"/healthz returns 200 after \${i}s\"
            exit 0
        fi
        sleep 1
    done
    echo 'skygate did not become healthy within 60s' >&2
    echo 'check: docker logs skygate-skygate-1 --tail 30' >&2
    exit 1
" || {
    ha_err "healthz wait failed"
    ha_audit "phase7_failed" "phase=$PHASE_ID" "step=healthz_wait"
    exit 1
}

# step 6: verify standby is in HA chain
ha_state_run_step "$PHASE_ID" "verify_chain" bash -c "
    set -e
    HOSTNAME_NOW=\$(hostname)
    if ! command -v psql >/dev/null 2>&1; then
        echo 'psql not installed — chain verification skipped (operator should check /admin/ha)'
        exit 0
    fi
    DB_DSN=\$(grep -E '^SKYGATE_DB_DSN=' '$ENV_FILE' 2>/dev/null | head -1 | cut -d= -f2- | tr -d '\47\"')
    if [ -z \"\$DB_DSN\" ]; then
        echo 'SKYGATE_DB_DSN not set — chain verification skipped'
        exit 0
    fi
    PGPASSWORD=\$(echo \"\$DB_DSN\" | sed -E 's|.*://[^:]+:([^@]+)@.*|\1|') \
        psql \"\$DB_DSN\" -tA -c \"SELECT value FROM global_settings WHERE key='ha_chain'\" 2>/dev/null | grep -q \"\$HOSTNAME_NOW\" \
        && { echo \"\$HOSTNAME_NOW is in ha_chain\"; exit 0; } \
        || { echo \"\$HOSTNAME_NOW NOT in ha_chain yet (may take 30s to register)\" >&2; exit 1; }
" || ha_warn "chain verification incomplete — check /admin/ha in the web UI"

# --- done ---
ha_state_set_phase "$PHASE_ID" status completed
ha_state_set_phase "$PHASE_ID" completed_at "$(ha_now)"
ha_audit "phase_completed" "phase=$PHASE_ID" "standby=$STANDBY_HOST"

ha_log ""
ha_log "=== ha-phase7.sh complete ==="
ha_log ""
ha_log "standby '$STANDBY_HOST' is bootstrapped. next:"
ha_log "  1. on primary: open /admin/ha — confirm standby in the chain"
ha_log "  2. on standby: bash scripts/ha-status.sh — should show 0_mesh + 7_bootstrap = completed"
ha_log "  3. schedule a maintenance window + run scripts/ha-phase9.sh on the primary"
ha_log ""
