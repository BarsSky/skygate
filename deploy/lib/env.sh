#!/usr/bin/env bash
#===============================================================================
# env.sh — Load and validate the unified .env file
# Detects OS and sets platform-specific variables (DOCKER_CMD, SSH_DIR, etc.)
#===============================================================================
set -euo pipefail

# ── OS detection ────────────────────────────────────────────────────────────
_KERNEL="$(uname -s 2>/dev/null || echo 'Windows')"
case "${_KERNEL}" in
    MINGW*|MSYS*|CYGWIN*)
        export SKYGATE_OS="windows"
        export DOCKER_CMD="docker.exe"
        _DEFAULT_HOME="${USERPROFILE:-$HOME}"
        _DEFAULT_HOME="$(echo "${_DEFAULT_HOME}" | sed 's|\\|/|g' | sed 's|^\([A-Z]\):|/mnt/\L\1|')"
        ;;
    Linux)
        export SKYGATE_OS="linux"
        export DOCKER_CMD="docker"
        _DEFAULT_HOME="${HOME:-/home/skyadmin}"
        ;;
    Darwin)
        export SKYGATE_OS="macos"
        export DOCKER_CMD="docker"
        _DEFAULT_HOME="${HOME:-/Users/skyadmin}"
        ;;
    *)
        export SKYGATE_OS="unknown"
        export DOCKER_CMD="docker"
        _DEFAULT_HOME="${HOME:-/home/skyadmin}"
        ;;
esac

# ── Load .env ───────────────────────────────────────────────────────────────
_LOADED=false
for _CANDIDATE in "${SKYGATE_ENV:-}" "${DEPLOY_SKYGATE_DIR:-${_DEFAULT_HOME}/skygate}/.env" .env ../.env ../../.env; do
    if [ -f "${_CANDIDATE}" ]; then
        set -a; source "${_CANDIDATE}"; set +a
        _LOADED=true; break
    fi
done
if [ "${_LOADED}" != "true" ]; then
    echo "ERROR: .env not found. Copy .env.example -> .env and fill in values." >&2
    exit 1
fi

# ── Required variables ──────────────────────────────────────────────────────
_required_vars=(SKYGATE_JWT_SECRET SKYGATE_ADMIN_PASS HEADSCALE_API_KEY
    HEADSCALE_SERVER_URL HEADPLANE_HEADSCALE__API_KEY HEADPLANE_SERVER__COOKIE_SECRET)
_missing=()
for _var in "${_required_vars[@]}"; do
    _val="${!_var:-}"
    [ -z "${_val}" ] || [[ "${_val}" == *"<"*">"* ]] && _missing+=("${_var}")
done
if [ ${#_missing[@]} -gt 0 ]; then
    echo "ERROR: Required variables missing or still have placeholders:" >&2
    for _v in "${_missing[@]}"; do printf '  - %s
' "${_v}" >&2; done
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
HEADSCALE_DERP_URLS="${HEADSCALE_DERP_URLS:-https://controlplane.tailscale.com/derpmap/default}"
DOCKER_NETWORK="${DOCKER_NETWORK:-headscale_default}"
DOCKER_SUBNET="${DOCKER_SUBNET:-172.18.0.0/16}"
DERP_ENABLED="${DERP_ENABLED:-false}"
DERP_STUN_PORT="${DERP_STUN_PORT:-3478}"
DERP_HTTP_PORT="${DERP_HTTP_PORT:-8443}"
DERP_MAP_PORT="${DERP_MAP_PORT:-8765}"

# Platform-specific path defaults
if [ "${SKYGATE_OS}" = "windows" ]; then
    DEPLOY_HEADSCALE_DIR="${DEPLOY_HEADSCALE_DIR:-${_DEFAULT_HOME}/headscale}"
    DEPLOY_SKYGATE_DIR="${DEPLOY_SKYGATE_DIR:-${_DEFAULT_HOME}/skygate}"
    DEPLOY_BACKUP_DIR="${DEPLOY_BACKUP_DIR:-${_DEFAULT_HOME}/skygate/backup}"
    SSH_DIR="${SSH_DIR:-${USERPROFILE:-$HOME}/.ssh}"
else
    DEPLOY_HEADSCALE_DIR="${DEPLOY_HEADSCALE_DIR:-/home/skyadmin/headscale}"
    DEPLOY_SKYGATE_DIR="${DEPLOY_SKYGATE_DIR:-/home/skyadmin/skygate}"
    DEPLOY_BACKUP_DIR="${DEPLOY_BACKUP_DIR:-/home/skyadmin/skygate/backup}"
    SSH_DIR="${SSH_DIR:-/home/skyadmin/.ssh}"
fi

export SKYGATE_PORT SKYGATE_DB SKYGATE_ADMIN_USER SKYGATE_OS DOCKER_CMD
export HEADSCALE_URL HEADSCALE_CONTAINER HEADSCALE_LOG_LEVEL
export HEADSCALE_BASE_DOMAIN HEADSCALE_AUTO_APPROVE_ROUTES HEADSCALE_DERP_URLS
export DOCKER_NETWORK DOCKER_SUBNET
export DEPLOY_HEADSCALE_DIR DEPLOY_SKYGATE_DIR DEPLOY_BACKUP_DIR
export DERP_ENABLED DERP_STUN_PORT DERP_HTTP_PORT DERP_MAP_PORT
export SSH_DIR
