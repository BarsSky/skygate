#!/bin/bash
# install-tailscale.sh — V1 (B-mod-install, 2026-09-10)
# operator-facing wrapper for the 3 Tailscale install modes
# (os_level / in_container / attach / none) + uninstall.
#
# This script is the OUT-OF-BAND counterpart to the
# in-binary TailscaleModule (internal/module/tailscale/).
# Operators running install-debian.sh on a fresh VM run
# this script NEXT if they want Tailscale as the
# skygate-mesh transport.
#
# The in-binary module is the runtime lifecycle
# (Init/Start/Stop/Health). This script is the OS-level
# install (apt + systemctl OR docker run). Operators
# choose ONE; the in-binary module reads the
# SKYGATE_TS_INSTALL_MODE env var to know which one
# was used.
#
# Modes:
#   os_level     — apt install tailscale + systemctl
#                  enable --now tailscaled + tailscale up
#                  (VM install, requires root).
#   in_container — docker run tailscale/tailscale
#                  sidecar (NET_ADMIN + NET_RAW caps,
#                  --network=host, /dev/net/tun mount).
#                  Used in containerized skygate
#                  deployments.
#   attach       — assume tailscaled is already running
#                  on the host (operator-installed).
#                  Only runs `tailscale up`. Used in
#                  B209.1 / B223 / B-mod-tailscale attach
#                  mode.
#   none         — don't install anything. Just write
#                  the state file at
#                  /var/lib/skygate/modules/tailscale/
#                  state.json so the in-binary module
#                  knows the mode. Used for "I just
#                  want /admin/modules to show Tailscale
#                  as registered but I'm not bringing
#                  it up".
#   uninstall    — remove whatever the chosen mode
#                  installed (apt purge / docker rm).
#                  State file is removed too.
#
# Usage:
#   sudo bash install-tailscale.sh --mode=os_level \
#       --authkey=tskey-auth-XXXXX \
#       --login-server=https://head.skynas.ru \
#       --hostname=skygate-host-1-1
#   sudo bash install-tailscale.sh --mode=in_container \
#       --authkey=tskey-auth-XXXXX \
#       --container-name=skygate-tailscale
#   sudo bash install-tailscale.sh --mode=attach
#   sudo bash install-tailscale.sh --mode=none
#   sudo bash install-tailscale.sh --mode=uninstall
#
# Exit codes:
#   0 — success
#   1 — invalid mode or missing required arg
#   2 — install step failed (apt / docker / tailscale up)
#   3 — pre-flight failed (no systemctl, no docker,
#       tailscaled not running for attach)

set -euo pipefail

# === paths ===
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

SKYGATE_DATA_DIR="${SKYGATE_DATA_DIR:-/var/lib/skygate}"
MODULES_DIR="${SKYGATE_DATA_DIR}/modules"
STATE_DIR="${MODULES_DIR}/tailscale"
STATE_FILE="${STATE_DIR}/state.json"

# === arg parsing ===
MODE=""
AUTHKEY=""
LOGIN_SERVER=""
HOSTNAME=""
CONTAINER_NAME="skygate-tailscale"
SKIP_VERIFY="${SKIP_VERIFY:-false}"

usage() {
    sed -n '2,52p' "$0"
    exit "${1:-1}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --mode=*)           MODE="${1#*=}" ;;
        --mode)             MODE="${2:-}"; shift ;;
        --authkey=*)        AUTHKEY="${1#*=}" ;;
        --authkey)          AUTHKEY="${2:-}"; shift ;;
        --login-server=*)   LOGIN_SERVER="${1#*=}" ;;
        --login-server)     LOGIN_SERVER="${2:-}"; shift ;;
        --hostname=*)       HOSTNAME="${1#*=}" ;;
        --hostname)         HOSTNAME="${2:-}"; shift ;;
        --container-name=*) CONTAINER_NAME="${1#*=}" ;;
        --container-name)   CONTAINER_NAME="${2:-}"; shift ;;
        --skip-verify)      SKIP_VERIFY="true" ;;
        -h|--help)          usage 0 ;;
        *) echo "ERROR: unknown arg: $1" >&2; usage 1 ;;
    esac
    shift
done

if [ -z "$MODE" ]; then
    echo "ERROR: --mode is required (os_level / in_container / attach / none / uninstall)" >&2
    usage 1
fi

# === helpers ===
log()  { printf '[install-tailscale] %s\n' "$*"; }
warn() { printf '[install-tailscale] WARN: %s\n' "$*" >&2; }
fail() { printf '[install-tailscale] ERROR: %s\n' "$*" >&2; exit "${2:-2}"; }

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        fail "this script must run as root (use sudo)" 1
    fi
}

require_cmd() {
    if ! command -v "$1" >/dev/null 2>&1; then
        fail "required command not found: $1" 3
    fi
}

# === state file ===
write_state() {
    local mode="$1"
    local started_at="$2"
    mkdir -p "$STATE_DIR"
    # Atomic write (write to .tmp + rename) — same
    # pattern as internal/module/state.go.
    local tmp="${STATE_FILE}.tmp"
    cat > "$tmp" <<EOF
{
  "name": "tailscale",
  "state": "${started_at:-installed}",
  "enabled": true,
  "install_mode": "${mode}",
  "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "sub_features": {}
}
EOF
    mv "$tmp" "$STATE_FILE"
    log "wrote state to $STATE_FILE (mode=$mode)"
}

clear_state() {
    rm -f "$STATE_FILE" 2>/dev/null || true
    rm -f "${STATE_FILE}.tmp" 2>/dev/null || true
    log "cleared state at $STATE_FILE"
}

# === os_level install ===
# Idempotent: re-run on a host that already has
# tailscale skips the apt step and goes straight to
# the tailscale up call.
install_os_level() {
    require_root
    require_cmd systemctl
    if command -v tailscale >/dev/null 2>&1; then
        log "tailscale already installed, skipping apt"
    else
        require_cmd apt-get
        log "apt-get update + install tailscale"
        apt-get update -qq || warn "apt-get update non-zero (continuing — cache may be fresh)"
        if ! apt-get install -y tailscale; then
            fail "apt-get install tailscale failed" 2
        fi
    fi
    log "systemctl enable --now tailscaled"
    if ! systemctl enable --now tailscaled; then
        fail "systemctl enable --now tailscaled failed" 2
    fi
    if [ -z "$AUTHKEY" ]; then
        warn "no --authkey supplied, skipping 'tailscale up' (operator must run it manually)"
        write_state "os_level" "installed"
        return 0
    fi
    if [ -z "$LOGIN_SERVER" ]; then
        fail "--login-server is required when --authkey is set" 1
    fi
    local args=(up
        "--login-server=${LOGIN_SERVER}"
        "--authkey=${AUTHKEY}"
        "--accept-routes=false"
        "--netfilter-mode=nodir"   # B179 safety
        "--accept-dns=false")
    if [ -n "$HOSTNAME" ]; then
        args+=("--hostname=${HOSTNAME}")
    fi
    log "tailscale ${args[*]}"
    if ! tailscale "${args[@]}"; then
        fail "tailscale up failed" 2
    fi
    write_state "os_level" "running"
    log "os_level install complete"
}

# === in_container install ===
install_in_container() {
    require_root
    require_cmd docker
    # Idempotency: skip if container already running.
    if docker ps -q -f "name=${CONTAINER_NAME}" | grep -q .; then
        log "container ${CONTAINER_NAME} already running, skipping docker run"
    else
        # Remove a stopped container with the same name, if any.
        docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
        local docker_args=(run -d
            "--name=${CONTAINER_NAME}"
            "--network=host"
            "--cap-add=NET_ADMIN"
            "--cap-add=NET_RAW"
            "-v" "/var/lib/${CONTAINER_NAME}:/var/lib/tailscale"
            "-v" "/dev/net/tun:/dev/net/tun"
            "tailscale/tailscale"
            "tailscaled"
            "--state=/var/lib/tailscale/tailscaled.state"
            "--socket=/var/run/tailscale/tailscaled.sock")
        log "docker ${docker_args[*]}"
        if ! docker "${docker_args[@]}"; then
            fail "docker run failed" 2
        fi
    fi
    if [ -z "$AUTHKEY" ]; then
        warn "no --authkey supplied, skipping 'tailscale up' in container"
        write_state "in_container" "installed"
        return 0
    fi
    if [ -z "$LOGIN_SERVER" ]; then
        fail "--login-server is required when --authkey is set" 1
    fi
    local up_args=(exec "$CONTAINER_NAME"
        tailscale up
        "--login-server=${LOGIN_SERVER}"
        "--authkey=${AUTHKEY}"
        "--accept-routes=false"
        "--netfilter-mode=nodir"   # B179 safety
        "--accept-dns=false")
    if [ -n "$HOSTNAME" ]; then
        up_args+=("--hostname=${HOSTNAME}")
    fi
    log "docker ${up_args[*]}"
    if ! docker "${up_args[@]}"; then
        fail "docker exec tailscale up failed" 2
    fi
    write_state "in_container" "running"
    log "in_container install complete"
}

# === attach install ===
install_attach() {
    # attach doesn't require root — tailscale up is
    # operator-level. But we still want to write the
    # state file (which is in /var/lib/skygate/, owned
    # by skygate user).
    if ! command -v tailscale >/dev/null 2>&1; then
        fail "tailscale binary not on PATH (install it first, or use --mode=os_level / --mode=in_container)" 3
    fi
    log "checking tailscale status (BackendState=Running?)"
    local status_json
    status_json="$(tailscale status --json 2>/dev/null || true)"
    if ! grep -q '"BackendState"[[:space:]]*:[[:space:]]*"Running"' <<<"$status_json"; then
        fail "tailscaled is not Running (status=${status_json:0:80}...). Start it first or switch to os_level/in_container." 3
    fi
    if [ -z "$AUTHKEY" ]; then
        warn "no --authkey supplied, skipping 'tailscale up'"
        write_state "attach" "running"
        return 0
    fi
    if [ -z "$LOGIN_SERVER" ]; then
        fail "--login-server is required when --authkey is set" 1
    fi
    local args=(up
        "--login-server=${LOGIN_SERVER}"
        "--authkey=${AUTHKEY}"
        "--accept-routes=false"
        "--netfilter-mode=nodir"   # B179 safety
        "--accept-dns=false")
    if [ -n "$HOSTNAME" ]; then
        args+=("--hostname=${HOSTNAME}")
    fi
    log "tailscale ${args[*]}"
    if ! tailscale "${args[@]}"; then
        fail "tailscale up failed" 2
    fi
    # State file write needs write access to /var/lib/skygate.
    if [ ! -w "$STATE_DIR" ] 2>/dev/null; then
        if [ "$(id -u)" -ne 0 ]; then
            warn "cannot write state file at $STATE_FILE (not root) — run as root to record the install"
        else
            write_state "attach" "running"
        fi
    else
        write_state "attach" "running"
    fi
    log "attach install complete (tailscaled was already running, skygate just attached)"
}

# === none install ===
install_none() {
    # Just write the state file. No OS-level install.
    require_root
    write_state "none" "installed"
    log "none install complete (state-only, no tailscaled installed)"
}

# === uninstall ===
do_uninstall() {
    require_root
    # Try each mode's uninstall in turn (idempotent).
    # Apt purge.
    if command -v tailscale >/dev/null 2>&1 && dpkg -l tailscale 2>/dev/null | grep -q '^ii'; then
        log "apt-get purge tailscale"
        apt-get purge -y tailscale || warn "apt-get purge tailscale failed (continuing)"
        apt-get autoremove -y || true
    fi
    # Systemctl disable.
    if command -v systemctl >/dev/null 2>&1; then
        log "systemctl disable --now tailscaled (best effort)"
        systemctl disable --now tailscaled 2>/dev/null || true
    fi
    # Docker rm.
    if command -v docker >/dev/null 2>&1; then
        log "docker rm -f ${CONTAINER_NAME} (best effort)"
        docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
    fi
    # State file.
    clear_state
    log "uninstall complete (tailscaled binary may still be on the host — operator can apt purge manually if needed)"
}

# === dispatch ===
case "$MODE" in
    os_level)     install_os_level ;;
    in_container) install_in_container ;;
    attach)       install_attach ;;
    none)         install_none ;;
    uninstall)    do_uninstall ;;
    *) fail "unknown mode: $MODE (valid: os_level / in_container / attach / none / uninstall)" 1 ;;
esac

log "done"
exit 0
