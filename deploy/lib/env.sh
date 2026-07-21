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
# 2026-07-14: Этап 14 v11 — Headplane is now an optional
# module. HEADPLANE_ENABLED defaults to true for backward
# compat (the original deploy shipped Headplane by default);
# set HEADPLANE_ENABLED=false in .env to skip the sidecar.
# HEADPLANE_IMAGE pins the upstream tag so a Skygate upgrade
# never silently bumps the dependency.
HEADPLANE_ENABLED="${HEADPLANE_ENABLED:-true}"
HEADPLANE_IMAGE="${HEADPLANE_IMAGE:-ghcr.io/tale/headplane:0.6.3}"
# 2026-07-15: v0.10.12 — point Skygate at an EXISTING Headplane
# instead of starting a second sidecar. When set, deploy.sh
# strips the headplane service block from docker-compose.yml
# and /admin/acls links to this URL. Leave empty to use the
# bundled sidecar. See docs/headplane.md "Use an existing
# Headplane" for the full contract.
HEADPLANE_EXTERNAL_URL="${HEADPLANE_EXTERNAL_URL:-}"
# 2026-07-15: v0.10.12 — comma-separated URLs of EXISTING DERP
# relays. When non-empty, deploy.sh skips the bundled derper
# container and appends these URLs to headscale's derp.urls
# list. See docs/derp.md "Use an existing DERP relay" for
# the full contract.
DERP_EXTERNAL_URLS="${DERP_EXTERNAL_URLS:-}"
HEADSCALE_AUTO_APPROVE_ROUTES="${HEADSCALE_AUTO_APPROVE_ROUTES:-0.0.0.0/0,::/0}"
HEADSCALE_DERP_URLS="${HEADSCALE_DERP_URLS:-https://controlplane.tailscale.com/derpmap/default}"
DOCKER_NETWORK="${DOCKER_NETWORK:-headscale_default}"
DOCKER_SUBNET="${DOCKER_SUBNET:-172.18.0.0/16}"
DERP_ENABLED="${DERP_ENABLED:-false}"
DERP_STUN_PORT="${DERP_STUN_PORT:-3478}"
DERP_HTTP_PORT="${DERP_HTTP_PORT:-8443}"
DERP_MAP_PORT="${DERP_MAP_PORT:-8765}"

# 2026-07-15: v0.15.0 — Caddy TLS terminator. Default
# true (the v0.15.0 release ships Caddy as the
# recommended HTTPS layer). Set CADDY_ENABLED=false in
# .env to skip Caddy entirely; the operator takes
# responsibility for the TLS layer per
# docs/https-setup.md. CADDY_DNS_PROVIDER is the Caddy
# DNS-01 module name (cloudflare, route53, gandi,
# digitalocean, googlecloud, hetzner, ovh, namecheap,
# porkbun, desec, ...); "http" = HTTP-01 challenge
# (no DNS API token, port 80 must be reachable).
CADDY_ENABLED="${CADDY_ENABLED:-true}"
CADDY_DNS_PROVIDER="${CADDY_DNS_PROVIDER:-cloudflare}"
CADDY_DNS_API_TOKEN_FILE="${CADDY_DNS_API_TOKEN_FILE:-/var/lib/skygate/secrets/caddy-dns-token}"
# Default hostnames. The operator overrides these in
# .env with their actual public DNS names.
CADDY_HOSTS_HEAD="${CADDY_HOSTS_HEAD:-head.example.com}"
CADDY_HOSTS_HEADPLANE="${CADDY_HOSTS_HEADPLANE:-headplane.example.com}"
CADDY_HOSTS_DERP="${CADDY_HOSTS_DERP:-derp.example.com}"
# HSTS (max-age 6 months + subdomains + preload) is
# enabled by default. Disable only for testing (the
# operator is about to bring a real hostname online
# but the cert isn't issued yet).
CADDY_HSTS="${CADDY_HSTS:-true}"

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
export CADDY_ENABLED CADDY_DNS_PROVIDER CADDY_DNS_API_TOKEN_FILE
export CADDY_HOSTS_HEAD CADDY_HOSTS_HEADPLANE CADDY_HOSTS_DERP CADDY_HSTS
export SSH_DIR
