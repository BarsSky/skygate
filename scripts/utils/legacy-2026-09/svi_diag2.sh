#!/usr/bin/env bash
set +e
# Full diagnostic on svi via jump — simplified
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 'ssh svi "
echo '==== 1. Tailscale state on svi ===='
echo '-- tailscale status --'
tailscale status 2>&1
echo
echo '-- tailscale ping 100.64.0.22 --'
tailscale ping -c 1 100.64.0.22 2>&1 | head -3
echo
echo '-- tailscale ping 100.64.0.2 karolina --'
tailscale ping -c 1 100.64.0.2 2>&1 | head -3

echo
echo '==== 2. svi OS network state ===='
echo '-- ip addr --'
ip -br addr
echo '-- ip route --'
ip route
echo '-- /etc/resolv.conf --'
cat /etc/resolv.conf
echo '-- resolvectl status --'
resolvectl status 2>&1 | head -15

echo
echo '==== 3. svi iptables / firewall ===='
echo '-- INPUT chain --'
sudo iptables -L INPUT -n -v --line-numbers 2>&1
echo '-- OUTPUT chain --'
sudo iptables -L OUTPUT -n -v --line-numbers 2>&1 | head -20
echo '-- nft ruleset --'
sudo nft list ruleset 2>&1 | grep -v '# Warning' | head -60
echo '-- hosts.deny / hosts.allow --'
cat /etc/hosts.deny 2>&1; echo '---'; cat /etc/hosts.allow 2>&1
echo '-- ufw status --'
sudo ufw status verbose 2>&1

echo
echo '==== 4. tailscaled service state ===='
systemctl status tailscaled --no-pager 2>&1 | head -20
echo '-- last 30 log lines --'
sudo journalctl -u tailscaled --no-pager -n 30 2>&1 | tail -30
echo
echo '-- state file dir --'
sudo ls -la /var/lib/tailscale/ 2>&1
echo '-- tailscale netcheck --'
tailscale netcheck 2>&1 | head -25

echo
echo '==== 5. outbound from svi ===='
echo '-- svi ping 95.165.170.190 --'
ping -c 2 -W 3 95.165.170.190 2>&1 | tail -3
echo '-- svi ping 8.8.8.8 --'
ping -c 2 -W 3 8.8.8.8 2>&1 | tail -3
echo '-- svi ssh 100.64.0.22 skygate --'
ssh -o ConnectTimeout=5 -o BatchMode=yes root@100.64.0.22 echo SSH_TO_PRIMARY_OK 2>&1 | head -3
echo '-- svi ssh 100.64.0.2 karolina --'
ssh -o ConnectTimeout=5 -o BatchMode=yes root@100.64.0.2 echo SSH_TO_KAROLINA_OK 2>&1 | head -3
" 2>&1' 2>&1
