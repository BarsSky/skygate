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
        _DEFAULT_HOME="${HOME:-/home/admin}"
        ;;
    Darwin)
        export SKYGATE_OS="macos"
        export DOCKER_CMD="docker"
        _DEFAULT_HOME="${HOME:-/Users/admin}"
        ;;
    *)
        export SKYGATE_OS="unknown"
        export DOCKER_CMD="docker"
        _DEFAULT_HOME="${HOME:-/home/admin}"
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
SKYGATE_ADMIN_USER="${SKYGATE_ADMIN_USER:-admin}"
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
# DERP_HTTP_PORT is the --http-port value (HTTP→HTTPS redirect),
# default 80. The pre-B260.2 default of 8443 was a leftover from
# the LE-cert-era template that hardcoded --a=:443; the manual-cert
# flow (B-derper-cert, B260.2) wants :80 because derper serves the
# ACME HTTP-01 challenge on this port when certmode=manual.
DERP_HTTP_PORT="${DERP_HTTP_PORT:-80}"
DERP_MAP_PORT="${DERP_MAP_PORT:-8765}"
# B260.2 (2026-09-17): new variables for the manual-cert docker path.
# DERP_DERP_PORT: the main --a= listen port. Default :443 because derper's
# manual cert mode requires :443 (per `derper --help`: "Serves HTTPS if
# the port is 443 and/or -certmode is manual, otherwise HTTP" — combining
# --certmode=manual with a non-443 --a= silently produces a broken
# plain-HTTP derper).
DERP_DERP_PORT="${DERP_DERP_PORT:-443}"
# DERP_HOSTNAME: the hostname clients use to reach the relay. Must match
# the cert CN/SAN. Default falls back to CADDY_HOSTS_DERP (the vhost
# skygate would publish if CADDY_ENABLED=true), then to the headscale
# base domain.
DERP_HOSTNAME="${DERP_HOSTNAME:-${CADDY_HOSTS_DERP:-derp.example.com}}"
# DERP_CERTMODE: manual | letsencrypt. Default `manual` because the
# pre-B260.2 systemd unit uses --certmode=manual and the operator's
# certsync flow (B147) writes cert files to /var/lib/derper/certs/.
# Operators wanting LE should set this explicitly + make :80 reachable.
DERP_CERTMODE="${DERP_CERTMODE:-manual}"
# DERP_CERT_DIR: the host directory derper reads cert files from. The
# `command:` mounts this into the container at the same path so the
# cert lookup (which uses `hostname` to find the .crt/.key pair) works
# identically to the systemd path.
DERP_CERT_DIR="${DERP_CERT_DIR:-/var/lib/derper/certs}"
# DERP_CONFIG_DIR: separate from CERT_DIR because derper's `--c` (config)
# and `--certdir` (cert files) are independent on disk.
DERP_CONFIG_DIR="${DERP_CONFIG_DIR:-/var/lib/derper}"
# DERP_VERIFY_CLIENTS_URL: empty = no client verification (--verify-clients=
# with empty string). The systemd unit used --verify-clients=false; for
# docker derper, an empty URL is the equivalent (derper treats "" as
# "disabled" — no HTTP fetch, all clients accepted).
DERP_VERIFY_CLIENTS_URL="${DERP_VERIFY_CLIENTS_URL:-}"
# DERP_IMAGE: docker image tag. Default skygate-derper:latest (locally
# built by scripts/migrate_derper_to_docker.sh). Operators with ghcr.io
# access can set this to ghcr.io/tailscale/derper:latest — the upstream
# image's `/derper` binary is the same as the locally-built one.
DERP_IMAGE="${DERP_IMAGE:-skygate-derper:latest}"

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
    DEPLOY_HEADSCALE_DIR="${DEPLOY_HEADSCALE_DIR:-/home/admin/headscale}"
    DEPLOY_SKYGATE_DIR="${DEPLOY_SKYGATE_DIR:-/home/admin/skygate}"
    DEPLOY_BACKUP_DIR="${DEPLOY_BACKUP_DIR:-/home/admin/skygate/backup}"
    SSH_DIR="${SSH_DIR:-/home/admin/.ssh}"
fi

export SKYGATE_PORT SKYGATE_DB SKYGATE_ADMIN_USER SKYGATE_OS DOCKER_CMD
export HEADSCALE_URL HEADSCALE_CONTAINER HEADSCALE_LOG_LEVEL
export HEADSCALE_BASE_DOMAIN HEADSCALE_AUTO_APPROVE_ROUTES HEADSCALE_DERP_URLS
export DOCKER_NETWORK DOCKER_SUBNET
export DEPLOY_HEADSCALE_DIR DEPLOY_SKYGATE_DIR DEPLOY_BACKUP_DIR
export DERP_ENABLED DERP_STUN_PORT DERP_HTTP_PORT DERP_MAP_PORT
export DERP_DERP_PORT DERP_HOSTNAME DERP_CERTMODE DERP_CERT_DIR DERP_CONFIG_DIR DERP_VERIFY_CLIENTS_URL DERP_IMAGE
export CADDY_ENABLED CADDY_DNS_PROVIDER CADDY_DNS_API_TOKEN_FILE
export CADDY_HOSTS_HEAD CADDY_HOSTS_HEADPLANE CADDY_HOSTS_DERP CADDY_HSTS
export SSH_DIR
