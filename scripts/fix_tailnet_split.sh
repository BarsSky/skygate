#!/usr/bin/env bash
# fix_tailnet_split.sh — operator-side helper for the TAILNET
# SPLIT fix (skygate v1.3.10, B110).
#
# Run on EACH node that needs re-authentication. Generates
# the exact `tailscale up` command with all non-default flags
# preserved (avoids the "Error: changing settings via
# 'tailscale up' requires mentioning all non-default flags"
# trap that bites the operator during the re-auth).
#
# Usage
# =====
#   # On VPSes (emilia, karolina, sharlotta, svyatoslava-1):
#   PREAUTH_KEY=hskey-auth-... ./fix_tailnet_split.sh
#   sudo bash /tmp/fix_tailnet_split.sh   # if you copied it
#
#   # On home devices (skybars, skyworker, a71, olesya, etc.):
#   # Same — operator pastes the preauth key, runs the script,
#   # sees the exact `tailscale up` command, pastes it back.
#
# What it does
# ============
# 1. Reads current `tailscale status --json` to capture
#    HostName, ControlURL, AcceptRoutes, AcceptDNS, etc.
# 2. Builds the `tailscale up` command with ALL non-default
#    flags preserved, including the new --auth-key.
# 3. Prints the command for the operator to copy-paste.
# 4. Does NOT auto-execute (operator must confirm).

set -u

# --- preflight ------------------------------------------------

if [ -z "${PREAUTH_KEY:-}" ]; then
    echo "ERROR: PREAUTH_KEY env var not set" >&2
    echo "Generate one with:" >&2
    echo "  docker exec headscale headscale preauthkeys create --user 1 --reusable --expiration 24h" >&2
    exit 1
fi

if ! command -v tailscale >/dev/null 2>&1; then
    echo "ERROR: tailscale not in PATH" >&2
    exit 1
fi

if ! tailscale status --json >/dev/null 2>&1; then
    echo "ERROR: tailscaled not running on this node" >&2
    echo "Try: sudo systemctl start tailscaled" >&2
    exit 1
fi

# --- extract current settings ---------------------------------

TS_JSON=$(tailscale status --json 2>/dev/null)

# python3 for portable JSON parsing.
CURRENT=$(python3 - <<PYEOF
import json
data = json.loads('''$TS_JSON''')
flags = []
flags.append(f'--hostname={data.get("HostName", "node")}')
flags.append(f'--login-server={data.get("ControlURL", "https://controlplane.tailscale.com")}')
if data.get("AcceptRoutes"):
    flags.append('--accept-routes')
if not data.get("AcceptDNS", True):
    flags.append('--accept-dns=false')
else:
    flags.append('--accept-dns=true')
# Tags if present
if data.get("Tags"):
    tags_str = ','.join(data["Tags"])
    flags.append(f'--advertise-tags={tags_str}')
# Exit node if advertised
if data.get("AdvertiseExitNode"):
    flags.append('--advertise-exit-node')
# Shields Up
if data.get("ShieldsUp"):
    flags.append('--shields-up')
# SSH
if data.get("RunSSH"):
    flags.append('--ssh=true')
# Subnet routes
for r in data.get("AdvertisedRoutes", []) or []:
    # Routes go via --advertise-routes=... only on first up;
    # subsequent ups don't need them (already approved)
    pass
print('\n'.join(flags))
PYEOF
)

# --- assemble the command -------------------------------------

CMD="sudo tailscale up \\"
for line in $CURRENT; do
    CMD="$CMD
    $line \\"
done
CMD="$CMD
    --auth-key=$PREAUTH_KEY"

cat <<EOF
=== TAILNET SPLIT FIX (v1.3.10) ===

Preauth key: ${PREAUTH_KEY:0:30}...
Current node settings detected:
$(echo "$CURRENT" | sed 's/^/  /')

Run this exact command (sudo, all flags preserved):

$CMD

If you see "Error: changing settings via 'tailscale up' requires
mentioning all non-default flags" — copy the EXACT command that
the error message suggests and try again. Different nodes have
different non-default flag sets.

After re-auth, verify with:
  tailscale status | wc -l    # should be ~17 (peers + header)
  docker exec skygate-skygate-1 bash /app/scripts/tailnet_probe.sh
EOF
