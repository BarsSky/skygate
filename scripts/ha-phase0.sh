#!/usr/bin/env bash
# ============================================================================
# ha-phase0.sh — Phase 0 of HA v1.5.0: Tailscale mesh + subnet routes
# B-new (v1.5.0+) — operator-driven pre-flight for the HA chain.
#
# See docs/internal/ha-v1.5.0-execution.md §3 (Phase 0).
#
# Prerequisites
# -------------
# - skygate-host-1 (primary) is running with tailscale up
# - <polygon-vm-hostname> (standby) is reachable via SSH from this host
# - HEADSCALE__API_KEY is set in the env (for tailscale commands)
# - The pre-existing 192.168.13.0/24 + 172.18.0.0/16 + 172.17.0.0/16
#   subnets are advertised by skygate-host-1 (operator's existing
#   config) — Phase 0 doesn't add new subnets
#
# What this does
# --------------
# 1. Pre-flight: both nodes have tailscale installed and joined
# 2. Verify subnet routes are advertised on skygate-host-1
# 3. Verify skygate-host-1 routes are approved in headscale
# 4. Tailscale SSH from <polygon-vm-hostname> to skygate-host-1 works
# 5. Tailnet MagicDNS resolves both hostnames
# 6. Write audit + state file
#
# State tracking: every step is tracked in $HA_STATE_FILE.
# Idempotency: re-running this script is safe — each step checks
# current state before doing the work.
#
# Recovery: on transient failure, the script retries with
# exponential backoff (2s, 4s, 8s). On hard failure, it marks
# the phase as failed and emits an operator alert (write to
# /var/lib/skygate/ha-state/.crash + audit log).
#
# Usage
# -----
#   ssh <polygon-vm-hostname>
#   cd ~/skygate
#   bash scripts/ha-phase0.sh
#   # or --reset to re-run from scratch (idempotent)
#   # or --status to just print the current state
# ============================================================================
set -euo pipefail

# Source state machine
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HA_LIB="$SCRIPT_DIR/ha-state/state.sh"
[ -f "$HA_LIB" ] || { echo "FATAL: state machine lib not found: $HA_LIB" >&2; exit 1; }
# shellcheck disable=SC1091
. "$HA_LIB"

# ---------- args ----------
PHASE_ID="0_mesh"
MAX_ATTEMPTS=3
BASE_BACKOFF=2
DO_RESET=0
DO_STATUS=0
STANDBY_HOST="${SKYGATE_STANDBY_HOST:-<polygon-vm-hostname>}"
PRIMARY_HOST="${SKYGATE_PRIMARY_HOST:-skygate-host-1}"

while [ $# -gt 0 ]; do
    case "$1" in
        --reset) DO_RESET=1; shift;;
        --status) DO_STATUS=1; shift;;
        --primary) PRIMARY_HOST="$2"; shift 2;;
        --standby) STANDBY_HOST="$2"; shift 2;;
        --attempts) MAX_ATTEMPTS="$2"; shift 2;;
        --backoff) BASE_BACKOFF="$2"; shift 2;;
        --help|-h)
            sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) ha_err "unknown flag: $1"; exit 2;;
    esac
done

# ---------- init ----------
ha_state_init
ha_state_crash_check
ha_state_ensure_phase "$PHASE_ID" "$MAX_ATTEMPTS"

# status-only mode
if [ "$DO_STATUS" = "1" ]; then
    ha_state_summary
    ha_log "phase '$PHASE_ID' status: $(ha_state_get_phase_status "$PHASE_ID")"
    exit 0
fi

ha_log "=== ha-phase0.sh (Tailscale mesh for HA) ==="
ha_log "primary: $PRIMARY_HOST"
ha_log "standby: $STANDBY_HOST"
ha_log "max attempts: $MAX_ATTEMPTS, base backoff: ${BASE_BACKOFF}s"

# ---------- step 1: tailscale installed on both nodes ----------
ha_state_run_step "$PHASE_ID" "tailscale_install_primary" bash -c '
    ssh -o ConnectTimeout=5 "'"$PRIMARY_HOST"'" "command -v tailscale >/dev/null 2>&1" || {
        echo "tailscale not found on '"$PRIMARY_HOST"'" >&2
        exit 1
    }
' || true  # ha_state_run_step will mark step failed

# check that tailscale is installed on the standby (this host)
if ! command -v tailscale >/dev/null 2>&1; then
    ha_warn "tailscale not found on $STANDBY_HOST ($(hostname))"
    ha_state_run_step "$PHASE_ID" "tailscale_install_standby" bash -c '
        echo "install via: curl -fsSL https://tailscale.com/install.sh | sh"
        exit 1
    '
    # We do NOT auto-install tailscale (operator must decide). Mark phase
    # as failed and exit cleanly with operator guidance.
    ha_audit "phase0_failed" "phase='"$PHASE_ID"'" "reason=missing_tailscale_on_standby"
    ha_err "tailscale not installed on $STANDBY_HOST"
    ha_err "fix: ssh $STANDBY_HOST 'curl -fsSL https://tailscale.com/install.sh | sh'"
    ha_err "then: cd ~/skygate && bash scripts/ha-phase0.sh"
    exit 1
else
    ha_state_run_step "$PHASE_ID" "tailscale_install_standby" bash -c 'true'
fi

# ---------- step 2: verify both nodes joined the same tailnet ----------
ha_state_run_step "$PHASE_ID" "verify_tailnet_join" bash -c "
    primary_ip=\$(ssh -o ConnectTimeout=5 '$PRIMARY_HOST' 'tailscale ip -4' 2>/dev/null | head -1)
    standby_ip=\$(tailscale ip -4 2>/dev/null | head -1)
    [ -n \"\$primary_ip\" ] || { echo 'primary has no tailscale IP' >&2; exit 1; }
    [ -n \"\$standby_ip\" ] || { echo 'standby has no tailscale IP' >&2; exit 1; }
    primary_net=\$(echo \"\$primary_ip\" | cut -d. -f1-2)
    standby_net=\$(echo \"\$standby_ip\" | cut -d. -f1-2)
    [ \"\$primary_net\" = \"\$standby_net\" ] || { echo \"primary and standby in different nets: primary=\$primary_net standby=\$standby_net\" >&2; exit 1; }
    echo \"primary=\$primary_ip standby=\$standby_ip net=\$primary_net\"
" || exit 1

# ---------- step 3: verify primary advertises required subnets ----------
ha_state_run_step "$PHASE_ID" "verify_routes_advertised" bash -c "
    advertised=\$(ssh -o ConnectTimeout=5 '$PRIMARY_HOST' 'tailscale status --json' 2>/dev/null | python3 -c '
import json, sys
d = json.load(sys.stdin)
for r in (d.get(\"AdvertisedRoutes\") or {}).keys():
    print(r)
' 2>/dev/null)
    expected='172.17.0.0/16
172.18.0.0/16
192.168.13.0/24'
    missing=\$(comm -23 <(echo \"\$expected\" | sort) <(echo \"\$advertised\" | sort))
    if [ -n \"\$missing\" ]; then
        echo \"missing advertised routes:\" \$missing >&2
        exit 1
    fi
    echo \"advertised: \$advertised\"
" || ha_warn "subnet routes not fully advertised — check skygate-host-1 tailscale status"

# ---------- step 4: verify routes approved in headscale ----------
ha_state_run_step "$PHASE_ID" "verify_routes_approved" bash -c "
    node_id=\$(ssh -o ConnectTimeout=5 '$PRIMARY_HOST' 'tailscale status --json' 2>/dev/null | python3 -c '
import json, sys
d = json.load(sys.stdin)
print(d.get(\"SelfNodeId\") or \"\")
')
    if [ -z \"\$node_id\" ]; then
        echo 'could not get self node id' >&2
        exit 1
    fi
    # headscale CLI: docker exec headscale headscale nodes list -o json
    approved=\$(docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c \"
import json, sys
nodes = json.load(sys.stdin)
for n in nodes:
    if str(n.get('id')) == '\$node_id':
        for r in (n.get('approved_routes') or []):
            print(r)
        break
\")
    expected='172.17.0.0/16
172.18.0.0/16
192.168.13.0/24'
    missing=\$(comm -23 <(echo \"\$expected\" | sort) <(echo \"\$approved\" | sort))
    if [ -n \"\$missing\" ]; then
        echo \"missing approved routes:\" \$missing >&2
        echo \"fix: docker exec headscale headscale nodes approve-routes -i \$node_id --routes $expected\" >&2
        exit 1
    fi
    echo \"approved: \$approved\"
" || ha_warn "subnet routes not fully approved in headscale — may need approve-routes"

# ---------- step 5: Tailscale SSH from standby to primary works ----------
ha_state_run_step "$PHASE_ID" "verify_ssh_to_primary" bash -c "
    output=\$(ssh -o ConnectTimeout=10 -o BatchMode=yes '$PRIMARY_HOST' 'whoami && uname -n && cat /etc/os-release | head -1' 2>&1)
    rc=\$?
    if [ \$rc -ne 0 ]; then
        echo \"ssh failed (rc=\$rc): \$output\" >&2
        echo \"fix: on $STANDBY_HOST, run: sudo tailscale up --ssh\" >&2
        echo \"also: enable Tailscale SSH ACL in headscale policy: tailnet.lockless/ssh.acl has 'ssh' action\" >&2
        exit 1
    fi
    echo \"ssh ok: \$output\"
" || exit 1

# ---------- step 6: MagicDNS resolves both hostnames ----------
ha_state_run_step "$PHASE_ID" "verify_magicdns" bash -c "
    resolver='100.100.100.100'
    for h in '$PRIMARY_HOST' '$STANDBY_HOST'; do
        ip=\$(dig +short +time=3 +tries=1 @\$resolver \"\$h\" 2>/dev/null | head -1)
        if [ -z \"\$ip\" ]; then
            echo \"MagicDNS did not resolve \$h via \$resolver\" >&2
            exit 1
        fi
        echo \"\$h -> \$ip\"
    done
" || ha_warn "MagicDNS did not resolve both hostnames — check split DNS or MagicDNS config in headscale"

# ---------- done ----------
ha_state_set_phase "$PHASE_ID" status completed
ha_state_set_phase "$PHASE_ID" completed_at "$(ha_now)"
ha_audit "phase_completed" "phase=$PHASE_ID" "primary=$PRIMARY_HOST" "standby=$STANDBY_HOST"

ha_log ""
ha_log "=== ha-phase0.sh complete ==="
ha_log ""
ha_log "next: run scripts/ha-phase7.sh on $STANDBY_HOST"
ha_log "       to bootstrap the standby (Phase 7)"
ha_log ""
ha_log "verification:"
ha_log "  tailscale status (both nodes)"
ha_log "  /var/lib/skygate/ha-state/state.json"
ha_log "  ha-state audit log: /var/lib/skygate/ha-state/audit.log"
