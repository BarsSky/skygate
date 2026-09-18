#!/usr/bin/env bash
set +e
# Restart tailscaled on svi to refresh peer list
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 "ssh svi 'echo == before restart, peers ==; tailscale status 2>&1 | head -5; echo; systemctl restart tailscaled 2>&1; sleep 3; echo == after restart, peers ==; tailscale status 2>&1 | head -15; echo; echo == ping 100.64.0.22 ==; ping -c 2 -W 3 100.64.0.22 2>&1 | tail -3; echo; echo == tailscale ping skygate-host-1-1 ==; tailscale ping -c 1 100.64.0.22 2>&1 | head -3' 2>&1" 2>&1 | head -40
