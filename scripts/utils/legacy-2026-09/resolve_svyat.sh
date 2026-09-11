#!/usr/bin/env bash
# Resolve: who is <polygon-vm-public-ip> now? Find svyatoslava's real IP
set +e
echo "=== full DNS response from <polygon-vm-public-ip>:53 (capture 512 bytes) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 'python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(3)
# query for skynas.ru
header = b\"\xab\xcd\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x05skynas\x02ru\x00\x00\x01\x00\x01\"
s.sendto(header, (\"<polygon-vm-public-ip>\", 53))
data, addr = s.recvfrom(4096)
print(\"got\", len(data), \"bytes\")
# decode answer
i = 0
def skip_name(data, i):
    while True:
        n = data[i]
        if n == 0: return i+1
        if n & 0xc0: return i+2
        i += n+1
i = 12  # skip header
qdcount = (data[4]<<8) | data[5]
ancount = (data[6]<<8) | data[7]
print(\"qdcount:\", qdcount, \"ancount:\", ancount)
for _ in range(qdcount):
    i = skip_name(data, i)
    i += 4  # type+class
print(\"answers:\")
for _ in range(ancount):
    i = skip_name(data, i)
    rtype = (data[i]<<8) | data[i+1]
    rclass = (data[i+2]<<8) | data[i+3]
    ttl = (data[i+4]<<24)|(data[i+5]<<16)|(data[i+6]<<8)|data[i+7]
    rdlen = (data[i+8]<<8) | data[i+9]
    i += 10
    rdata = data[i:i+rdlen]
    print(f\"  type={rtype} class={rclass} ttl={ttl} rdata={rdata.hex()}\")
    i += rdlen
"' 2>&1
echo ""
echo "=== check if <polygon-vm-public-ip> is on a /24 known to be operator's ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "
    echo '== <polygon-vm-public-ip> =='
    ip route get <polygon-vm-public-ip> 2>&1
    echo '== 95.165.170.190 =='
    ip route get 95.165.170.190 2>&1
    echo '== arp table (local subnet) =='
    cat /proc/net/arp 2>&1
" 2>&1 | head -20
echo ""
echo "=== can skygate reach <polygon-vm-public-ip> on port 53? ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "timeout 3 nc -zv <polygon-vm-public-ip> 53 2>&1; echo '---'; echo 'queries from skygate:'; dig +short +time=3 +tries=1 @<polygon-vm-public-ip> example.com 2>&1; dig +short +time=3 +tries=1 @<polygon-vm-public-ip> <polygon-vm-hostname>.skynas.ru 2>&1" 2>&1
echo ""
echo "=== try common svyatoslava hostnames ==="
for h in svyatoslava <polygon-vm-hostname> skyworker-standby svyatoslava.skynas.ru <polygon-vm-hostname>.skynas.ru; do
    echo "  $h:"
    timeout 3 nslookup $h 2>&1 | head -5
done
echo ""
echo "=== check headscale for svyat endpoints (any old IP recorded) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys,time
now=int(time.time())
for n in json.load(sys.stdin):
    if str(n.get(\"id\")) == \"45\":
        d=n
        ls=d.get(\"last_seen\",{}).get(\"seconds\",0)
        print(f\"id={d.get(\\\"id\\\")} name={d.get(\\\"given_name\\\")} online={d.get(\\\"online\\\")} age={int(now-ls)}s\")
        print(f\"  advertised_routes={d.get(\\\"advertised_routes\\\")}\")
        print(f\"  approved_routes={d.get(\\\"approved_routes\\\")}\")
        print(f\"  hostinfo.os={d.get(\\\"hostinfo\\\",{}).get(\\\"os\\\") if d.get(\\\"hostinfo\\\") else None}\")
        print(f\"  hostinfo.hostname={d.get(\\\"hostinfo\\\",{}).get(\\\"hostname\\\") if d.get(\\\"hostinfo\\\") else None}\")
        print(f\"  hostinfo.services={d.get(\\\"hostinfo\\\",{}).get(\\\"services\\\") if d.get(\\\"hostinfo\\\") else None}\")
        # raw endpoints:
        print(f\"  endpoints={d.get(\\\"endpoints\\\")}\")
'" 2>&1
