#!/usr/bin/env bash
set +e
# Diagnose Tailscale mesh on svi
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 'ssh svi "echo == svi tailnet peers ==; tailscale status 2>&1 | head -15; echo; echo == svi tailscale0 IP ==; tailscale ip -4 2>&1; echo; echo == dig skygate-host-1 from svi ==; dig +short +time=2 skygate-host-1 2>&1; echo; echo == skygate-host-1-1 ==; dig +short +time=2 skygate-host-1-1 2>&1; echo; echo == ping 100.64.0.22 (skygate) ==; ping -c 2 -W 3 100.64.0.22 2>&1 | tail -3; echo; echo == nc 100.64.0.22:22 ==; nc -zv -w 3 100.64.0.22 22 2>&1; echo; echo == ssh 100.64.0.22 ==; ssh -o ConnectTimeout=3 -o BatchMode=yes root@100.64.0.22 echo SSH_VIA_TS_OK 2>&1" 2>&1' 2>&1 | head -35
