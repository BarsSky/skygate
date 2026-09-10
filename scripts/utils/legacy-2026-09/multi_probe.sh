#!/usr/bin/env bash
# Probe from multiple angles - run ping native (no Select-Object in bash)
set +e
echo "=== 1. From this Windows host (ping.exe) ==="
echo "--> 45.152.198.217 (svyatoslava public)"
ping -n 3 -w 4 45.152.198.217 2>&1 | head -8
echo ""
echo "--> 95.165.170.190 (NPM fronting)"
ping -n 3 -w 4 95.165.170.190 2>&1 | head -8
echo ""
echo "--> 192.168.13.20 (LAN host)"
ping -n 3 -w 4 192.168.13.20 2>&1 | head -8
echo ""
echo "--> 192.168.13.1 (LAN gateway)"
ping -n 3 -w 4 192.168.13.1 2>&1 | head -8
echo ""
echo "--> 192.168.13.69 (skygate VM)"
ping -n 3 -w 4 192.168.13.69 2>&1 | head -8
