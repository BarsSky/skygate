#!/usr/bin/env bash
# derp_relay_latency_test.sh — measure DERP relay latency from each VPS
#
# Operator context: skygate VM (Moscow) can reach hel=38ms fine, but
# sharlotta and karolina (Russian VPS) cannot reach Helsinki (RKN block).
# Need to find the best alternative DERP relay for each VPS.
#
# Method: `tailscale netcheck` returns all DERP regions with their
# measured latency (UDP latency to each DERP). Filter out hel from
# the result and pick the lowest-latency region per VPS.
#
# Usage:
#   bash scripts/derp_relay_latency_test.sh
#
# Output:
#   - Per-VPS table of DERP latencies (sorted ascending)
#   - Per-VPS recommended DERP region (lowest non-hel latency)
#   - Summary: emilia, sharlotta, karolina, skygate-vm recommendations

set -euo pipefail

# All DERP regions to test (subset of public Tailscale DERP map).
# hel is excluded (blocked from RF for sharlotta/karolina per operator).
DERP_REGIONS=(fra ams waw sfo nyc sin hkg par lhr mad zrh dxb sgp ord dfw sea atl bog lim mex jnb syd nrt)

# VPS targets (per /home/skyadmin/.ssh/config).
# emilia is the operator's reference (hel works there but we include for baseline).
VPS_LIST=(emilia sharlotta karolina)

# Header
echo "==================================================================="
echo "DERP RELAY LATENCY TEST"
echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "Excluded: hel (blocked from RF per operator 2026-08-25)"
echo "Method:   tailscale netcheck (UDP latency, ms)"
echo "==================================================================="
echo

# Test each VPS
for vps in "${VPS_LIST[@]}"; do
  echo "--- $vps ---"
  # Run tailscale netcheck on the VPS, parse JSON
  # Output: {"DERP": {"<region>": {"LatencyMillis": <ms>}, ...}, ...}
  if ! nc=$(ssh "$vps" 'tailscale netcheck' 2>&1); then
    echo "  ERR: SSH to $vps failed: $nc"
    echo
    continue
  fi
  # Parse the JSON output. tailscale netcheck outputs JSON to stdout.
  # Find the line starting with '{' and parse from there.
  json=$(echo "$nc" | grep -A 1000 '^{' | head -1)
  if [ -z "$json" ]; then
    echo "  ERR: no JSON in tailscale netcheck output"
    echo "  raw: $nc" | head -5
    echo
    continue
  fi
  # Extract DERP latencies using python
  python3 - "$json" <<'PYEOF' || echo "  (python parse failed)"
import json, sys
data = json.loads(sys.argv[1])
derp = data.get("DERP", {})
# Build a sorted list (lowest latency first, excluding hel)
results = []
for region, info in derp.items():
    lat = info.get("LatencyMillis", 99999)
    if region == "hel":
        continue
    results.append((lat, region, info.get("HostName", "?")))
results.sort()
for lat, region, host in results[:15]:
    bar = "█" * min(int(lat / 20), 30)
    print(f"  {region:5} {lat:4}ms  {bar}")
if not results:
    print("  (no DERP data)")
else:
    best_lat, best_region, best_host = results[0]
    print(f"  → RECOMMENDED: {best_region} ({best_lat}ms) via {best_host}")
PYEOF
  echo
done

echo "==================================================================="
echo "SUMMARY (operator action)"
echo "==================================================================="
echo
echo "To apply the recommended DERP per VPS:"
echo "  ssh <vps> 'tailscale set --preference=<best-region>'"
echo
echo "Or to pin only the relaying choice (per-session):"
echo "  ssh <vps> 'tailscale up --login-server=https://head.skynas.ru \\"
echo "    --accept-routes=true --netfilter-mode=on'"
echo
echo "Run scripts/derp_relay_apply.sh <vps> <region> to apply automatically."
echo
