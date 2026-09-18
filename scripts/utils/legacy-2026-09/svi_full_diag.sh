#!/usr/bin/env bash
set +e
# Full diagnostic on svi via jump
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 'ssh svi "
echo '=========================================='
echo ' 1. Tailscale state on svi'
echo '=========================================='
echo '-- tailscale status --'
tailscale status 2>&1
echo
echo '-- tailscale status --json peer count --'
tailscale status --json 2>/dev/null | python3 -c \"import json,sys; d=json.load(sys.stdin); print('peers:', len(d.get('Peer',{}))); print('self online:', d.get('SelfNode',{}).get('Online')); print('self tailscale ips:', d.get('TailscaleIPs', d.get('SelfNode',{}).get('TailscaleIPs')))\"
echo
echo '-- last seen for skygate-host-1-1 from svi view --'
tailscale status --json 2>/dev/null | python3 -c \"import json,sys
d=json.load(sys.stdin)
for k,p in d.get('Peer',{}).items():
    name = p.get('HostName','')
    if 'sky' in name.lower() or 'svi' in name.lower():
        print(' '*2, name, 'online=', p.get('Online'), 'lastSeen=', p.get('LastSeen'))\" 2>&1
echo
echo '-- tailscale ping from svi to skygate-host-1-1 (100.64.0.22) --'
tailscale ping -c 1 100.64.0.22 2>&1 | head -5
echo
echo '-- tailscale ping from svi to skyworker (100.64.0.1) --'
tailscale ping -c 1 100.64.0.1 2>&1 | head -5

echo
echo '=========================================='
echo ' 2. svi OS network state'
echo '=========================================='
echo '-- ip addr --'
ip -br addr
echo
echo '-- ip route --'
ip route
echo
echo '-- ip rule --'
ip rule
echo
echo '-- resolvectl status (DNS) --'
resolvectl status 2>&1 | head -20
echo
echo '-- /etc/resolv.conf --'
cat /etc/resolv.conf

echo
echo '=========================================='
echo ' 3. svi iptables / nftables / firewall'
echo '=========================================='
echo '-- iptables INPUT chain --'
sudo iptables -L INPUT -n -v --line-numbers 2>&1
echo
echo '-- iptables OUTPUT chain --'
sudo iptables -L OUTPUT -n -v --line-numbers 2>&1
echo
echo '-- nftables full ruleset --'
sudo nft list ruleset 2>&1
echo
echo '-- /etc/hosts.deny + hosts.allow --'
cat /etc/hosts.deny 2>&1
echo '---'
cat /etc/hosts.allow 2>&1
echo
echo '-- fail2ban status --'
sudo fail2ban-client status 2>&1 | head -10
echo
echo '-- ipset lists --'
sudo ipset list 2>&1 | head -20

echo
echo '=========================================='
echo ' 4. svi Tailscale daemon state'
echo '=========================================='
echo '-- systemctl status tailscaled --'
systemctl status tailscaled --no-pager 2>&1 | head -20
echo
echo '-- tailscaled log tail (if accessible) --'
sudo tail -20 /var/log/tailscaled.log 2>&1 || sudo journalctl -u tailscaled --no-pager -n 20 2>&1
echo
echo '-- tailscaled state file --'
sudo ls -la /var/lib/tailscale/ 2>&1 | head -10
echo
echo '-- check if tailscale is healthy --'
tailscale netcheck 2>&1 | head -20

echo
echo '=========================================='
echo ' 5. outbound connectivity from svi'
echo '=========================================='
echo '-- svi ping 95.165.170.190 (NPM) --'
ping -c 2 -W 3 95.165.170.190 2>&1 | tail -3
echo
echo '-- svi ping 8.8.8.8 --'
ping -c 2 -W 3 8.8.8.8 2>&1 | tail -3
echo
echo '-- svi ssh to skygate-host-1-1 (100.64.0.22) --'
ssh -o ConnectTimeout=5 -o BatchMode=yes root@100.64.0.22 echo SSH_TO_PRIMARY_OK 2>&1 | head -3
echo
echo '-- svi ssh to karolina via tailscale (100.64.0.2) --'
ssh -o ConnectTimeout=5 -o BatchMode=yes root@100.64.0.2 echo SSH_TO_KAROLINA_OK 2>&1 | head -3
" 2>&1' 2>&1
