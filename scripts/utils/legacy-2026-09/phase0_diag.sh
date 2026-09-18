#!/usr/bin/env bash
set +e
# Diagnose why verify_tailnet_join failed
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 'ssh svi "cd /home/skyadmin/skygate; echo == tailscale status ==; tailscale status 2>&1 | head -10; echo; echo == tailscale ip ==; tailscale ip -4 2>&1; echo; echo == which tailscale ==; which tailscale; tailscale version 2>&1; echo; echo == ssh test to primary ==; ssh -o ConnectTimeout=5 -o BatchMode=yes root@skygate-host-1 echo SSH_TO_PRIMARY_OK 2>&1" 2>&1' 2>&1 | head -30
