#!/usr/bin/env bash
# More ports + check what DNS is on 45.152.198.217:53
set +e
echo "=== deeper port scan of 45.152.198.217 ==="
for p in 21 22 23 25 53 80 81 110 111 135 139 143 443 445 465 587 873 993 995 1080 1194 1433 1521 1812 2049 2082 2083 2086 2087 2095 2096 2222 2379 3128 3306 3389 3478 3690 4000 4443 5000 5044 5060 5222 5432 5672 5900 5984 6379 6667 7474 8000 8080 8081 8443 8883 8888 9000 9090 9100 9200 9418 10000 11211 15672 19999 27017 32400 41641 50443; do
    out=$(timeout 2 nc -zv -w 1 45.152.198.217 $p 2>&1)
    if echo "$out" | grep -q "succeeded\|open"; then
        echo "  $p: OPEN"
    fi
done
echo ""
echo "=== DNS query against 45.152.198.217:53 ==="
# Try to use Windows nslookup
echo "exit" | timeout 3 nc 45.152.198.217 53 2>&1 | head -5
echo ""
echo "=== what is the response to a DNS query? ==="
echo "Use python to query 45.152.198.217:53 for a known domain"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 'python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(3)
# build DNS query for example.com
import struct
header = b\"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01\"
s.sendto(header, (\"45.152.198.217\", 53))
try:
    data, addr = s.recvfrom(1024)
    print(\"got response:\", len(data), \"bytes\")
    print(\"first 50 bytes hex:\", data[:50].hex())
except Exception as e:
    print(\"no response:\", e)
"' 2>&1
echo ""
echo "=== check 45.152.198.217 rDNS ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "python3 -c '
import socket
try:
    name = socket.gethostbyaddr(\"45.152.198.217\")
    print(\"rDNS:\", name)
except Exception as e:
    print(\"rDNS error:\", e)
'" 2>&1
echo ""
echo "=== 95.165.170.190 rDNS (NPM) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "python3 -c '
import socket
try:
    name = socket.gethostbyaddr(\"95.165.170.190\")
    print(\"rDNS:\", name)
except Exception as e:
    print(\"rDNS error:\", e)
'" 2>&1
