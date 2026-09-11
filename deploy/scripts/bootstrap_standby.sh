#!/bin/bash
# bootstrap_standby.sh — V1 (B-mod-install, 2026-09-10)
# bootstraps a new skygate HA standby VM.
#
# This script is the consumer side of the B218 / B-new
# "HA standby auto-provisioning" flow. The PRIMARY
# skygate VM's `skygate init <standby-hostname>` subcommand
# emits a standby preauth key on stdout (line 1 = node_id,
# line 2 = cluster_id, line 3 = dsn, line 4 = primary_host).
# This script:
#
#   1. Optionally installs Tailscale (attach mode) if
#      SKYGATE_STANDBY_TS_AUTHKEY is set + tailscale
#      is not yet in the tailnet. Uses install-tailscale.sh
#      --mode=attach (the operator-installed tailscaled
#      + tailscale up flow per B209.1 / B223).
#   2. Calls `skygate init <standby-hostname>` on the
#      primary via ssh (delegates Tailscale SSH fallback
#      in B-new). Stdout is parsed for the preauth key.
#   3. Writes the standby preauth key to disk + prints
#      it on stdout (so the operator can paste it into
#      a follow-up `skygate standby join` invocation).
#   4. Idempotent: re-running on a partially-bootstrapped
#      VM skips the steps that already completed.
#
# Usage:
#   sudo bash deploy/scripts/bootstrap_standby.sh \
#       --primary=skyadmin@skygate-primary-host \
#       --standby-hostname=skygate-host-1-2 \
#       --ts-authkey=tskey-auth-XXX \
#       --login-server=https://head.skynas.ru
#
# Env-var defaults (in case --primary is omitted):
#   SKYGATE_PRIMARY=skyadmin@skygate-primary
#   SKYGATE_STANDBY_HOSTNAME=$(hostname)
#   SKYGATE_TS_AUTHKEY=(empty by default — no Tailscale install)
#   SKYGATE_TS_LOGIN_SERVER=https://head.skynas.ru
#
# Exit codes:
#   0 — standby preauth key captured (Tailscale install optional)
#   1 — invalid args / missing required tool
#   2 — primary unreachable / skygate init failed
#   3 — Tailscale install failed
#   4 — standby preauth key invalid (missing fields)

set -euo pipefail

# === paths ===
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
INSTALL_TS_SH="${SCRIPT_DIR}/install-tailscale.sh"

# === arg parsing ===
PRIMARY="${SKYGATE_PRIMARY:-}"
STANDBY_HOSTNAME="${SKYGATE_STANDBY_HOSTNAME:-$(hostname 2>/dev/null || echo skygate-standby)}"
TS_AUTHKEY="${SKYGATE_TS_AUTHKEY:-}"
TS_LOGIN_SERVER="${SKYGATE_TS_LOGIN_SERVER:-https://head.skynas.ru}"
SKIP_TS=0

usage() {
    sed -n '2,40p' "$0"
    exit "${1:-1}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --primary=*)          PRIMARY="${1#*=}" ;;
        --primary)            PRIMARY="${2:-}"; shift ;;
        --standby-hostname=*) STANDBY_HOSTNAME="${1#*=}" ;;
        --standby-hostname)   STANDBY_HOSTNAME="${2:-}"; shift ;;
        --ts-authkey=*)       TS_AUTHKEY="${1#*=}" ;;
        --ts-authkey)         TS_AUTHKEY="${2:-}"; shift ;;
        --login-server=*)     TS_LOGIN_SERVER="${1#*=}" ;;
        --login-server)       TS_LOGIN_SERVER="${2:-}"; shift ;;
        --skip-ts)            SKIP_TS=1 ;;
        -h|--help)            usage 0 ;;
        *) echo "ERROR: unknown arg: $1" >&2; usage 1 ;;
    esac
    shift
done

# === helpers ===
log()  { printf '[bootstrap-standby] %s\n' "$*"; }
warn() { printf '[bootstrap-standby] WARN: %s\n' "$*" >&2; }
fail() { printf '[bootstrap-standby] ERROR: %s\n' "$*" >&2; exit "${2:-1}"; }

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        fail "this script must run as root (use sudo)" 1
    fi
}

require_cmd() {
    if ! command -v "$1" >/dev/null 2>&1; then
        fail "required command not found: $1" 1
    fi
}

# === preflight ===
require_root
require_cmd ssh
if [ -z "$PRIMARY" ]; then
    fail "--primary is required (e.g. skyadmin@skygate-primary-host)" 1
fi
if [ -z "$STANDBY_HOSTNAME" ]; then
    fail "--standby-hostname is required (or set SKYGATE_STANDBY_HOSTNAME)" 1
fi

log "starting bootstrap of standby '$STANDBY_HOSTNAME' from primary '$PRIMARY'"

# === step 1: Tailscale attach (B-mod-install) ===
if [ "$SKIP_TS" = "1" ]; then
    log "step 1: skip Tailscale (--skip-ts set)"
elif [ -z "$TS_AUTHKEY" ]; then
    log "step 1: skip Tailscale (SKYGATE_TS_AUTHKEY empty, operator-installed Tailscale assumed)"
else
    log "step 1: install Tailscale (attach mode)"
    # Idempotency: if tailscale is already in the tailnet
    # (BackendState=Running), skip the install.
    if command -v tailscale >/dev/null 2>&1; then
        status_json=$(tailscale status --json 2>/dev/null || true)
        if printf '%s' "$status_json" | grep -q '"BackendState"[[:space:]]*:[[:space:]]*"Running"'; then
            log "  skip: tailscaled already Running (already in the tailnet)"
        else
            log "  tailscaled not Running — running install-tailscale.sh --mode=attach"
            if [ ! -x "$INSTALL_TS_SH" ]; then
                fail "install-tailscale.sh not found at $INSTALL_TS_SH (clone the skygate repo and re-run, or set --skip-ts)" 3
            fi
            if ! bash "$INSTALL_TS_SH" --mode=attach \
                --authkey="$TS_AUTHKEY" \
                --login-server="$TS_LOGIN_SERVER" \
                --hostname="$STANDBY_HOSTNAME"; then
                fail "install-tailscale.sh --mode=attach failed (check the authkey, login server, or Tailscale logs)" 3
            fi
        fi
    else
        # tailscale binary not on PATH — install via
        # install-tailscale.sh --mode=os_level (the
        # B209.1 attach mode requires tailscaled to be
        # pre-installed; in this case fall back to
        # os_level install + attach up call).
        log "  tailscale binary not on PATH — running install-tailscale.sh --mode=os_level"
        if [ ! -x "$INSTALL_TS_SH" ]; then
            fail "install-tailscale.sh not found at $INSTALL_TS_SH" 3
        fi
        if ! bash "$INSTALL_TS_SH" --mode=os_level \
            --authkey="$TS_AUTHKEY" \
            --login-server="$TS_LOGIN_SERVER" \
            --hostname="$STANDBY_HOSTNAME"; then
            fail "install-tailscale.sh --mode=os_level failed" 3
            # (could fall through to in_container if operator
            # prefers docker; --mode=os_level is the default
            # for VM installs)
        fi
    fi
    log "  ok: Tailscale attached"
fi

# === step 2: skygate init on primary ===
log "step 2: ssh $PRIMARY skygate init $STANDBY_HOSTNAME"
if ! init_output=$(ssh -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o BatchMode=yes "$PRIMARY" "skygate init $STANDBY_HOSTNAME" 2>/tmp/bootstrap_standby_init.err); then
    cat /tmp/bootstrap_standby_init.err >&2
    fail "skygate init on primary failed (check the standby hostname + primary reachability)" 2
fi

# === step 3: parse stdout ===
log "step 3: parse standby preauth key"
# skygate init emits 4 lines on stdout:
#   1: node_id
#   2: cluster_id
#   3: dsn
#   4: primary_host
# See internal/ha/standby/init.go InitStandby for the
# exact format. Empty fields = error.
node_id=$(echo "$init_output" | sed -n '1p')
cluster_id=$(echo "$init_output" | sed -n '2p')
dsn=$(echo "$init_output" | sed -n '3p')
primary_host=$(echo "$init_output" | sed -n '4p')

if [ -z "$node_id" ] || [ -z "$cluster_id" ] || [ -z "$dsn" ] || [ -z "$primary_host" ]; then
    log "raw init output:"
    echo "$init_output" | sed 's/^/    /'
    fail "init output missing one or more required fields (node_id, cluster_id, dsn, primary_host)" 4
fi

log "  node_id=$node_id"
log "  cluster_id=$cluster_id"
log "  dsn=${dsn:0:32}..."
log "  primary_host=$primary_host"

# === step 4: write to disk + print on stdout ===
OUT_DIR="${SKYGATE_DATA_DIR:-/var/lib/skygate}/standby"
mkdir -p "$OUT_DIR"
OUT_FILE="$OUT_DIR/$node_id.preauth.json"
cat > "$OUT_FILE" <<EOF
{
  "node_id": "$node_id",
  "cluster_id": "$cluster_id",
  "dsn": "$dsn",
  "primary_host": "$primary_host",
  "standby_hostname": "$STANDBY_HOSTNAME",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
chmod 0600 "$OUT_FILE"
log "  wrote preauth to $OUT_FILE"

# Also print on stdout (for operators who want to
# paste it into a manual `skygate standby join`).
echo "===== STANDBY PREAUTH KEY ====="
cat "$OUT_FILE"
echo "==============================="

# === step 5: reminder ===
log "next step: run 'skygate standby join' on this VM with the preauth above"
log "  example:"
log "    skygate standby join --node-id=$node_id --cluster-id=$cluster_id --dsn='...' --primary=$primary_host"

log "done"
exit 0
