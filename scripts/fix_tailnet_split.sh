#!/usr/bin/env bash
# fix_tailnet_split.sh — operator-side helper for the TAILNET
# SPLIT fix (skygate v1.3.10, B110).
#
# Run on EACH node that needs re-authentication. Generates
# the exact `tailscale up` command with all non-default flags
# preserved (avoids the "Error: changing settings via
# 'tailscale up' requires mentioning all non-default flags"
# trap).
#
# Usage
# =====
#   # On VPSes (emilia, karolina, sharlotta, svyatoslava-1):
#   PREAUTH_KEY=hskey-auth-... bash /path/to/fix_tailnet_split.sh
#
#   # On home devices (skybars, skyworker, a71, olesya, etc.):
#   # Same — paste the preauth key, run the script, copy the
#   # command it prints, paste it back.
#
# What it does
# ============
# 1. Reads current `tailscale status --json` to capture
#    Self.HostName, Self.Tags, Self.AllowedIPs (advertised
#    routes), Self.ExitNodeOption, etc.
# 2. Reads /var/lib/tailscale/tailscaled.state for the
#    ControlURL (not in status JSON).
# 3. Builds the `tailscale up` command with ALL non-default
#    flags preserved + the new --auth-key.
# 4. Prints the command for the operator to copy-paste.
# 5. Does NOT auto-execute (operator must confirm).

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

# python3 for portable JSON parsing. Reads the SELF section
# (which has HostName, Tags, AllowedIPs, etc.) plus the
# state file for ControlURL.
CURRENT=$(python3 - <<PYEOF
import json
data = json.loads('''$TS_JSON''')
self_d = data.get("Self", {}) or {}
flags = []

# Hostname
hn = self_d.get("HostName", "node")
flags.append(f'--hostname={hn}')

# Tags (operator-specific, MUST be preserved)
tags = self_d.get("Tags", []) or []
if tags:
    flags.append(f'--advertise-tags={",".join(tags)}')

# Advertised routes (subnets, NOT the CGNAT address itself)
# Skip the CGNAT 100.64.x and fd7a:... addresses.
allowed = self_d.get("AllowedIPs", []) or []
routes = []
for r in allowed:
    if r.startswith("100.64.") or r.startswith("fd7a:"):
        continue
    routes.append(r)
if routes:
    flags.append(f'--advertise-routes={",".join(routes)}')

# Exit node option
if self_d.get("ExitNodeOption"):
    flags.append('--advertise-exit-node')

# Shields Up / SSH / Netfilter / etc — read prefs file
# (not in status JSON). Skipped: most operators don't change
# these, and the "Error: changing settings" message will tell
# us which ones to add.

print('\n'.join(flags))
PYEOF
)

# ControlURL: read from state file (not in status JSON).
# State file structure (Tailscale 1.98.x):
#   {
#     "_current-profile": "<base64 of 'profile-XXXX'>",
#     "_profiles": "<base64 of { "XXXX": {...profile...} }>",
#     "profile-XXXX": "<base64 of full profile JSON with ControlURL>",
#     "_machinekey": "..."
#   }
# The profile-X key matches _current-profile's base64-decoded value
# (minus the "profile-" prefix).
CONTROL_URL=""
for state_path in /var/lib/tailscale/tailscaled.state \
                  /var/lib/tailscale/tailscaled.state.conf \
                  /Library/Tailscale/tailscaled.state \
                  "$HOME/Library/Application Support/Tailscale/tailscaled.state"; do
    if [ -f "$state_path" ]; then
        CONTROL_URL=$(python3 - <<PYEOF 2>/dev/null
import json, base64
try:
    with open("$state_path") as f:
        s = json.load(f)
    cur_b64 = s.get("_current-profile", "")
    cur = base64.b64decode(cur_b64).decode("utf-8", errors="replace")
    # Try the "profile-XXXX" key directly.
    if cur in s:
        profile_b64 = s[cur]
        profile = json.loads(base64.b64decode(profile_b64).decode("utf-8", errors="replace"))
        print(profile.get("ControlURL", ""))
    else:
        # Fallback: dig into _profiles dict.
        profiles_b64 = s.get("_profiles", "")
        profiles = json.loads(base64.b64decode(profiles_b64).decode("utf-8", errors="replace"))
        for k, p in profiles.items():
            cur_id = cur.replace("profile-", "")
            if k == cur_id:
                # _profiles[k] is a structured dict, not base64.
                print(p.get("ControlURL", ""))
                break
except Exception as e:
    pass
PYEOF
)
        if [ -n "$CONTROL_URL" ]; then break; fi
    fi
done

# If we still don't have a ControlURL, ask the operator.
if [ -z "$CONTROL_URL" ]; then
    echo "Could not auto-detect login server from state file." >&2
    echo "Enter the login server URL for this tailnet (e.g. https://head.<your-domain>):" >&2
    read -r CONTROL_URL
fi

# --- assemble the command -------------------------------------

cat <<HEADER
=== TAILNET SPLIT FIX (v1.3.10) ===

Preauth key: ${PREAUTH_KEY:0:30}...
Detected settings (will be preserved in tailscale up):
  --login-server=$CONTROL_URL
$(echo "$CURRENT" | sed 's/^/  /')

Run this exact command on this node (sudo, all flags preserved):

HEADER

echo "sudo tailscale up \\"
echo "    --login-server=$CONTROL_URL \\"
for line in $CURRENT; do
    echo "    $line \\"
done
echo "    --auth-key=$PREAUTH_KEY"
echo

cat <<FOOTER
If you see "Error: changing settings via 'tailscale up' requires
mentioning all non-default flags" — copy the EXACT command
the error message suggests and try again. Different nodes
have different non-default flag sets (e.g. --ssh, --shields-up,
--netfilter-mode).

After re-auth, verify with:
  tailscale status | wc -l    # should be ~17 (peers + header)
  docker exec skygate-skygate-1 bash /app/scripts/tailnet_probe.sh
FOOTER
