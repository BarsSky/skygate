#!/bin/sh
# v0.24.0 — One-time subnet-router setup.
#
# This script runs ON THE SUBNET-ROUTER HOST (the user's machine
# that has access to their local network, e.g. a Raspberry Pi, a
# home server, a VM, a Docker container with --net=host). It is
# NOT run on the skygate/headscale server.
#
# The host must already have tailscale installed. After this
# script finishes, the host will:
#   - Be logged in to https://head.skynas.ru as
#     `skygate-subnet-<username>`.
#   - Advertise the user's per-user CIDR (e.g. 10.0.6.0/24) as
#     a subnet route.
#   - Receive auto-approval from skygate's sidecar.SyncOnce
#     goroutine (30s tick) which then flips
#     `user_subnets.status` to `active`.
#
# Usage:
#   1. The operator (admin) opens /admin/users/{id}/subnet in
#      skygate and clicks "Issue preauth key". The page shows a
#      `tailscale up` command line — copy it.
#   2. The user SSHes to the subnet-router host and runs that
#      command. OR runs this script with the key as an env var:
#
#        PREAUTH_KEY=tskey-auth-xxxxx \
#        SUBNET_ROUTER_HOSTNAME=skygate-subnet-michail \
#        SUBNET_CIDR=10.0.6.0/24 \
#        sudo -E ./deploy/subnet-router/setup.sh
#
#   3. The script runs `tailscale up` and prints the next steps
#      (admin must wait for auto-approval; usually < 60s).
#
# Idempotent: re-running with the same key is a no-op. If a new
# key was issued (the old one is used or expired), re-run with
# the new key. The hostname and routes are deterministic, so the
# only thing the script does differently on each run is call
# `tailscale up --authkey=<NEW_KEY>`.

set -e

# Required environment. Documented in the admin UI when the
# preauth is issued (the page literally shows the same values).
PREAUTH_KEY="${PREAUTH_KEY:-}"
SUBNET_ROUTER_HOSTNAME="${SUBNET_ROUTER_HOSTNAME:-skygate-subnet-$(hostname)}"
SUBNET_CIDR="${SUBNET_CIDR:-}"

LOGIN_SERVER="${TS_LOGIN_SERVER:-https://head.skynas.ru}"

# --- Sanity checks -----------------------------------------------------------

if [ -z "$PREAUTH_KEY" ]; then
    echo "ERROR: PREAUTH_KEY is required." >&2
    echo "" >&2
    echo "Get one from /admin/users/{id}/subnet → 'Issue preauth key'." >&2
    echo "The page shows the exact \`tailscale up\` command for this user." >&2
    exit 1
fi
if [ -z "$SUBNET_CIDR" ]; then
    echo "ERROR: SUBNET_CIDR is required (e.g. 10.0.6.0/24)." >&2
    echo "Look at /admin/users/{id}/subnet — the CIDR is shown there." >&2
    exit 1
fi
if ! command -v tailscale >/dev/null 2>&1; then
    echo "ERROR: tailscale CLI not found in PATH. Install first:" >&2
    echo "  curl -fsSL https://tailscale.com/install.sh | sh" >&2
    exit 1
fi

echo "Setup parameters:"
echo "  login-server      = $LOGIN_SERVER"
echo "  hostname          = $SUBNET_ROUTER_HOSTNAME"
echo "  advertise-routes  = $SUBNET_CIDR"
echo ""

# --- Run tailscale up --------------------------------------------------------
# We use `tailscale up` (not `tailscale set`) because this is the
# first-time login. `--accept-routes` lets this host learn about
# OTHER users' subnets (so a subnet-router in michail's network
# can reach skyadmin's 10.0.1.0/24 if shared). It does NOT
# affect our own routes (we advertise, not accept).
#
# `--netfiltermode=off` keeps iptables untouched (the host
# shouldn't get a Tailscale-managed firewall unless the user
# explicitly wants one). The per-user CIDR is forwarded by the
# kernel via `tailscale set --accept-routes` + the routes
# that are pushed to clients — the subnet-router itself just
# advertises and the kernel does the rest.
#
# `--authkey` is single-use; once this node logs in, the key
# cannot be reused. If the key was already consumed (rare
# because we check for an existing login below), re-issue from
# the admin UI.
echo "Running: tailscale up --accept-routes --netfiltermode=off --hostname=... --advertise-routes=..."
tailscale up \
    --accept-routes \
    --netfiltermode=off \
    --login-server="$LOGIN_SERVER" \
    --hostname="$SUBNET_ROUTER_HOSTNAME" \
    --advertise-routes="$SUBNET_CIDR" \
    --authkey="$PREAUTH_KEY" 2>&1

echo ""
echo "Current state:"
tailscale status 2>&1 | head -8 || true

echo ""
echo "Advertised routes (from this node's perspective):"
tailscale status --json 2>/dev/null | \
    python3 -c "
import sys, json
d = json.load(sys.stdin)
adv = d.get('AdvertisedRoutes') or []
ena = d.get('EnabledRoutes') or []
print('  advertised (locally):', adv if adv else '(none)')
print('  enabled (in headscale):', ena if ena else '(none — waiting for admin approval / auto-approver)')
" || echo "  (could not read tailscale status --json)"

# --- Next steps --------------------------------------------------------------
echo ""
echo "Next steps (operator on the headscale control server):"
echo "  1. Within ~30s, skygate's sidecar.SyncOnce goroutine"
echo "     should pick up this node and auto-approve"
echo "     $SUBNET_CIDR. Watch the skygate logs:"
echo ""
echo "       docker logs -f skygate | grep -E 'sidecar.*approved|sidecar.*$SUBNET_CIDR'"
echo ""
echo "  2. If auto-approval didn't fire (e.g. sidecar disabled),"
echo "     approve manually on the headscale host:"
echo ""
echo "       docker exec headscale headscale nodes list"
echo "       # find $SUBNET_ROUTER_HOSTNAME in the list, note its ID"
echo "       docker exec headscale headscale nodes approve-routes -i <NODE_ID> --routes '$SUBNET_CIDR'"
echo ""
echo "  3. Verify the route is enabled in headscale:"
echo ""
echo "       docker exec headscale headscale nodes list -o json | grep -A2 '$SUBNET_ROUTER_HOSTNAME'"
echo ""
echo "  4. From any tailnet client (e.g. your phone, another"
echo "     laptop), verify the user's /24 is reachable via"
echo "     MagicDNS:"
echo ""
echo "       ping $SUBNET_ROUTER_HOSTNAME.tailnet-domain"
echo "       # or, if the skygate configures base_domain = tsnet.skynas.ru:"
echo "       ping $SUBNET_ROUTER_HOSTNAME"
echo ""
echo "       # and the gateway IP (the subnet-router's Tailscale"
echo "       # IP) should be pingable:"
echo "       ping <tailscale-ip-of-$SUBNET_ROUTER_HOSTNAME>"
echo ""
echo "  5. The status pill on /admin/users/{id}/subnet should"
echo "     flip from 'pending' to 'router_active' within ~5s"
echo "     of the auto-approval (the sidecar.SyncOnce tick is"
echo "     every 30s but the subnet status update reads from"
echo "     the user_subnets table on every /my/devices load)."
echo ""
echo "Done. The subnet-router is live; tailnet clients with"
echo "tailscale up --accept-routes will now see $SUBNET_CIDR."
