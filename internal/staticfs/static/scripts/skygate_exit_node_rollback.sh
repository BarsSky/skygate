#!/bin/bash
# Skygate Exit Node Rollback Script
# Restores exit node to pre-Skygate state (advertise 0.0.0.0/0)
set -e

echo "=== Skygate Exit Node Rollback ==="
echo "Restoring --advertise-routes=0.0.0.0/0,::/0"
echo "(SSH remains enabled — use --ssh=false to disable)"
echo ""

tailscale set \
  --advertise-exit-node \
  --advertise-routes="0.0.0.0/0,::/0"

echo ""
echo "Done. Exit node now advertises full internet routes again."
tailscale status 2>/dev/null | grep -E "offers|exit" || true
