#!/bin/sh
# 2026-07-14: Этап 14 v2 — One-time relay setup.
#
# This script runs ON THE RELAY HOST (e.g. emilia), not on skygate.
# It configures the relay's tailscaled to advertise the canonical
# Telegram IP ranges as subnet routes, so any tailnet client with
# --accept-routes (skygate) routes api.telegram.org traffic through
# this host.
#
# Usage:
#   1. SSH to the relay host (the one that already has tailscaled
#      running and is logged in to head.skynas.ru).
#   2. Run: sudo ./deploy/tailscale-relay/setup.sh
#
# After this:
#   - The relay advertises the Telegram IP ranges.
#   - An admin must approve the routes in headscale (one-time per
#     range) — see the printed instructions.
#   - skygate (which has --accept-routes) automatically picks them
#     up; no further action needed on the skygate host.
#
# Idempotent: running it again is a no-op (tailscale up is a no-op
# when the desired state is already applied; missing routes are
# added).

set -e

# Canonical Telegram IP ranges (as of 2026-07-14). These are the
# ranges used by api.telegram.org, web.telegram.org, and the
# official Telegram clients. Sourced from Telegram's published
# documentation and verified via dig + short on multiple vantage
# points.
TELEGRAM_ROUTES="91.108.4.0/22,91.108.8.0/22,91.108.12.0/22,91.108.16.0/22,91.108.20.0/22,91.108.56.0/22,149.154.160.0/20,185.76.151.0/24"

# IPv6 ranges — same set, for the future when skygate starts
# resolving AAAA records. Currently the Go HTTP client only does A
# resolution by default, so these are aspirational.
TELEGRAM_ROUTES_V6="2001:67c:4e8::/48,2001:b28:f23c::/48,2001:b28:f23f::/48,2001:7a0:1::/48"

LOGIN_SERVER="${TS_LOGIN_SERVER:-https://head.skynas.ru}"

if ! command -v tailscale >/dev/null 2>&1; then
    echo "ERROR: tailscale CLI not found in PATH. Install tailscale first:" >&2
    echo "  curl -fsSL https://tailscale.com/install.sh | sh" >&2
    exit 1
fi

# Sanity: tailscaled is up and the node is logged in. If not, we
# bail with a clear hint instead of running `tailscale up` with
# new flags and accidentally changing unrelated state.
if ! tailscale status >/dev/null 2>&1; then
    echo "ERROR: tailscaled is not running or this node is not logged in." >&2
    echo "Start it first:" >&2
    echo "  sudo tailscaled &" >&2
    echo "  sudo tailscale up --login-server=${LOGIN_SERVER} --hostname=$(hostname)" >&2
    exit 1
fi

# Sanity: the node is an exit-node-offerer. If it isn't, the user
# probably picked the wrong host. (Not strictly required for the
# subnet-router pattern, but a relay that has internet access to
# the rest of the world AND is on the tailnet is the only useful
# target. The script doesn't enforce; the operator picks.)
echo "Current node:"
tailscale status | head -3
echo

echo "Applying routes: ${TELEGRAM_ROUTES}"
# `tailscale set` is the right command: it adjusts the node's
# settings without going through the full `tailscale up` flow
# (which would re-validate authkey, hostname, etc. and reject
# mismatches).
#
# We pass --advertise-routes= with the v4 + v6 list. The IPv6
# block is currently aspirational (the Go HTTP client does A
# resolution by default), but having them in the route
# advertisement means future IPv6 traffic is covered too.
#
# NOTE: `tailscale set --advertise-routes` is additive — it does
# not remove previously-advertised routes. If the operator wants
# to drop a route, they need to use the headscale API to disable
# the route on the admin side.
tailscale set --advertise-routes="${TELEGRAM_ROUTES},${TELEGRAM_ROUTES_V6}" 2>&1

echo
echo "Current advertised routes (from this node's perspective):"
tailscale status --json 2>/dev/null | \
    python3 -c "
import sys, json
d = json.load(sys.stdin)
adv = d.get('AdvertisedRoutes') or []
ena = d.get('EnabledRoutes') or []
print('  advertised (locally):', adv if adv else '(none)')
print('  enabled (in headscale):', ena if ena else '(none — admin needs to approve in headscale)')
"

echo
echo "Next steps (admin must do on the headscale control server):"
echo "  1. SSH to head.skynas.ru"
echo "  2. List nodes:    docker exec headscale headscale nodes list"
echo "  3. Find this relay (look for the hostname above)"
echo "  4. Approve routes: docker exec headscale headscale nodes approve-routes --identifier <NODE_ID> --routes \"${TELEGRAM_ROUTES}\""
echo
echo "  Or via the Headplane web UI:"
echo "  https://head.skynas.ru/ → Machines → this relay → Approve advertised routes"
echo
echo "After approval, skygate's `tailscale up --accept-routes` will"
echo "see the new routes within ~60s and api.telegram.org traffic"
echo "will start flowing through this relay."
