#!/usr/bin/env bash
#===============================================================================
# env.sh — Load and validate the unified .env file
#===============================================================================
set -euo pipefail

# Try .env locations in order
_LOADED=false
for _CANDIDATE in "${SKYGATE_ENV:-}" "${DEPLOY_SKYGATE_DIR:-/home/skyadmin/skygate}/.env" ./env .env ../.env ../../.env; do
    if [ -f "${_CANDIDATE}" ]; then
        set -a
        # shellcheck source=/dev/null
        source "${_CANDIDATE}"
        set +a
        _LOADED=true
        break
    fi
done

if [ "${_LOADED}" != "true" ]; then
    echo "ERROR: .env not found. Copy .env.example → .env and fill in values." >&2
    exit 1
fi

# ── Required variables ─────────────────────────────────────────────────────
_required_vars=(
    SKYGATE_JWT_SECRET
    SKYGATE_ADMIN_PASS
    HEADSCALE_API_KEY
    HEADSCALE_SERVER_URL
    HEADPLANE_HEADSCALE__API_KEY
    HEADPLANE_SERVER__COOKIE_SECRET
)

_missing=()
for _var in "${_required_vars[@]}"; do
    _val="${!_var:-}"
    if [ -z "${_val}" ] || [[ "${_val}" == *"<"*">"* ]]; then
        _missing+=("${_var}")
    fi
done

if [ ${#_missing[@]} -gt 0 ]; then
    echo "ERROR: The following required variables are missing or still have placeholder values:" >&2
    for _v in "${_missing[@]}"; do
        echo "  - ${_v}" >&2
    done
    echo "" >&2
    echo "Edit .env and fill in real values, then re-run." >&2
    exit 1
fi

# ── Derived defaults ────────────────────────────────────────────────────────
SKYGATE_PORT="${SKYGATE_PORT:-8080}"
SKYGATE_DB="${SKYGATE_DB:-/data/skygate.db}"
SKYGATE_ADMIN_USER="${SKYGATE_ADMIN_USER:-skyadmin}"
HEADSCALE_URL="${HEADSCALE_URL:-http://headscale:50444}"
HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"
HEADSCALE_LOG_LEVEL="${HEADSCALE_LOG_LEVEL:-info}"
HEADSCALE_BASE_DOMAIN="${HEADSCALE_BASE_DOMAIN:-tsnet.example.com}"
HEADSCALE_AUTO_APPROVE_ROUTES="${HEADSCALE_AUTO_APPROVE_ROUTES:-0.0.0.0/0,::/0}"
DOCKER_NETWORK="${DOCKER_NETWORK:-headscale_default}"
DOCKER_SUBNET="${DOCKER_SUBNET:-172.18.0.0/16}"
DEPLOY_HEADSCALE_DIR="${DEPLOY_HEADSCALE_DIR:-/home/skyadmin/headscale}"
DEPLOY_SKYGATE_DIR="${DEPLOY_SKYGATE_DIR:-/home/skyadmin/skygate}"
DEPLOY_BACKUP_DIR="${DEPLOY_BACKUP_DIR:-/home/skyadmin/skygate/backup}"
DERP_ENABLED="${DERP_ENABLED:-false}"
DERP_STUN_PORT="${DERP_STUN_PORT:-3478}"
DERP_HTTP_PORT="${DERP_HTTP_PORT:-8443}"
DERP_MAP_PORT="${DERP_MAP_PORT:-8765}"

export SKYGATE_PORT SKYGATE_DB SKYGATE_ADMIN_USER
export HEADSCALE_URL HEADSCALE_CONTAINER HEADSCALE_LOG_LEVEL
export HEADSCALE_BASE_DOMAIN HEADSCALE_AUTO_APPROVE_ROUTES
export DOCKER_NETWORK DOCKER_SUBNET
export DEPLOY_HEADSCALE_DIR DEPLOY_SKYGATE_DIR DEPLOY_BACKUP_DIR
export DERP_ENABLED DERP_STUN_PORT DERP_HTTP_PORT DERP_MAP_PORT
