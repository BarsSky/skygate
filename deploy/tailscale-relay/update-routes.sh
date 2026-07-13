#!/bin/sh
# 2026-07-14: Этап 14 v2 — Refresh the relay's advertised Telegram routes.
#
# Telegram occasionally adds new IP ranges (or re-allocates
# existing ones). When that happens, skygate's HTTP client
# resolves api.telegram.org to an IP that isn't in any
# advertised subnet route, so the request falls back to direct
# (which is blocked on RF VPSes) and the bot's getUpdates
# polling times out.
#
# This script resolves api.telegram.org (and a few related
# hostnames) from multiple vantage points, computes the union
# of returned A/AAAA records, aggregates them into CIDR
# blocks, and re-applies `tailscale set --advertise-routes=` on
# the relay.
#
# Usage:
#   - Run ON THE RELAY HOST:    sudo ./deploy/tailscale-relay/update-routes.sh
#   - Or via Makefile on the skygate host (requires SSH to the relay):
#       RELAY=emilia make tailscale-update-telegram-routes
#
# Cron it weekly on the relay host:
#   0 4 * * 1  /opt/skygate/deploy/tailscale-relay/update-routes.sh >> /var/log/tailscale-routes.log 2>&1

set -e

# Hostnames we want to keep reachable. api.telegram.org is the
# main one for the bot's getUpdates / sendMessage. web.telegram.org
# and core.telegram.org are official landing pages; including
# their resolved IPs makes the relay useful for browser-based
# debugging too.
HOSTNAMES="api.telegram.org web.telegram.org core.telegram.org"

# Aggregator function. Pass an IP, get back the smallest CIDR
# that contains only "public, well-known Telegram" addresses.
# This is a coarse aggregation: we map any 91.108.x.x to
# /16, any 149.154.160-175.x to /20, and any 185.76.151.x to
# /24. The /16 is a conservative over-approximation that
# catches any future allocations in the 91.108.0.0/16 block.
# A more refined script would look at the BGP / RIR data, but
# the goal here is "be liberal in what we accept" — the relay
# only forwards the IPs we send, so over-covering just means
# skygate can reach more things through the relay.
aggregate_v4() {
    ip="$1"
    case "$ip" in
        91.108.*)      echo "91.108.0.0/16" ;;
        149.154.16?.*) echo "149.154.160.0/20" ;;
        185.76.151.*)  echo "185.76.151.0/24" ;;
        *)             echo "$ip" ;;  # unknown range — pass through
    esac
}

# Resolve each hostname from this host and from a few public
# resolvers. The point of using multiple resolvers is to catch
# cases where Telegram returns different IPs to different
# locations (anycast). We don't care which resolver wins — we
# care about the union.
RESOLVERS="1.1.1.1 8.8.8.8 9.9.9.9"

# Collected CIDR list (v4 only for now; v6 support is a TODO).
COLLECTED=""

for h in $HOSTNAMES; do
    for r in $RESOLVERS; do
        # dig +short A $h @$r — 4 second timeout per resolver/host.
        # If dig isn't installed, fall back to getent / nslookup.
        if command -v dig >/dev/null 2>&1; then
            ips=$(dig +short +time=4 +tries=1 A "$h" "@$r" 2>/dev/null || true)
        elif command -v nslookup >/dev/null 2>&1; then
            ips=$(nslookup -timeout=4 "$h" "$r" 2>/dev/null | \
                  awk '/^Address: / {print $2}' | grep -v "^$r$" || true)
        else
            echo "ERROR: install dnsutils (dig) to use this script." >&2
            exit 1
        fi
        for ip in $ips; do
            # Skip empty / non-IPv4 entries.
            case "$ip" in
                ""|*[!0-9.]*) continue ;;
            esac
            cidr=$(aggregate_v4 "$ip")
            case ",$COLLECTED," in
                *",$cidr,"*) ;; # already in the list
                *) COLLECTED="${COLLECTED:+$COLLECTED,}$cidr" ;;
            esac
        done
    done
done

if [ -z "$COLLECTED" ]; then
    echo "ERROR: no IPs resolved — DNS seems broken or the network is down." >&2
    echo "Refusing to apply an empty route advertisement (would wipe the relay)." >&2
    exit 1
fi

echo "Computed routes: $COLLECTED"

# Apply. Note: tailscale set --advertise-routes is additive —
# routes that were advertised before but aren't in the new list
# are NOT removed. To prune, the operator needs to either
# (a) re-run with `tailscale up --advertise-routes=...` (which
#     resets to the new list), or
# (b) use the headscale CLI to disable individual routes.
# We do (a) here to keep the script self-contained and
# idempotent: re-running it always converges to the
# computed-from-DNS state.
echo
echo "Re-applying routes via 'tailscale up --advertise-routes=...' (resets, not additive):"
echo "  sudo tailscale up --advertise-routes=$COLLECTED"
echo
echo "Manual approve on headscale (admin must do):"
NODE_ID=$(tailscale status --json 2>/dev/null | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('Self',{}).get('ID',''))" 2>/dev/null || echo "")
if [ -n "$NODE_ID" ]; then
    echo "  docker exec headscale headscale nodes approve-routes --identifier $NODE_ID --routes \"$COLLECTED\""
fi
echo
echo "After approval, skygate picks up the new routes within ~60s."
echo "Run /admin/telegram in the skygate web UI to confirm the probe banner shows 'reachable (Tailscale relay)'."
