#!/bin/bash
# Skygate Exit Node Setup Script
# Safely enables --ssh + --advertise-routes on exit nodes
# Run this ON THE EXIT NODE itself (karolina/emilia/sharlotta)
# 
# What it does:
#   1. Saves current tailscale state (for rollback)
#   2. Enables --ssh (for Tailscale SSH access from tag:private nodes)
#   3. Sets --advertise-routes to the whitelist (from Skygate rules)
#   4. Keeps --advertise-exit-node (for full-tunnel clients like Android)
#
# IMPORTANT: --advertise-routes REPLACES the current list.
# 0.0.0.0/0 and ::/0 are intentionally REMOVED from advertised routes.
# The exit node still works for full-tunnel clients (--exit-node).
# Split-tunnel clients (--accept-routes) get only the whitelist.

set -e

WHITELIST_ROUTES="91.108.4.0/22,91.108.8.0/22,91.108.12.0/22,91.108.16.0/22,91.108.20.0/22,91.108.56.0/22,91.105.192.0/23,149.154.160.0/20,185.76.151.0/24,8.8.4.0/24,8.8.8.0/24,8.34.208.0/20,8.35.192.0/20,34.0.0.0/15,74.125.0.0/16,142.250.0.0/15,172.217.0.0/16,173.194.0.0/16,216.58.192.0/19"

echo "=== Skygate Exit Node Setup ==="
echo ""
echo "This script will:"
echo "  - Enable Tailscale SSH (--ssh)"
echo "  - Set advertised routes to Skygate whitelist"
echo "  - Keep exit node capability (--advertise-exit-node)"
echo ""
echo "Current state:"
tailscale status 2>/dev/null | grep -E "offers|exit" || true
echo ""

# Save current state for rollback
echo "Saving current state to /tmp/tailscale_pre_skygate.txt..."
tailscale status > /tmp/tailscale_pre_skygate.txt 2>/dev/null || true
tailscale debug prefs > /tmp/tailscale_prefs_pre_skygate.txt 2>/dev/null || true
echo "  Saved."

# Apply new config
echo ""
echo "Applying new config..."
echo "  tailscale set --ssh --advertise-exit-node --advertise-routes=$WHITELIST_ROUTES"

tailscale set \
  --ssh \
  --advertise-exit-node \
  --advertise-routes="$WHITELIST_ROUTES"

echo ""
echo "=== Done ==="
echo ""
echo "New state:"
tailscale status 2>/dev/null | grep -E "offers|exit|SSH" || true
echo ""
echo "Rollback (if needed):"
echo "  tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0,::/0"
echo "  (SSH will remain enabled unless you add --ssh=false)"
