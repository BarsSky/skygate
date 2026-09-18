#!/usr/bin/env bash
# DERP-level probe + see if svyatoslava is on a different tailnet
set +e
echo "=== tailscale status (with peer relay) ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale status --json 2>&1 | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(\"backend_state:\", d.get(\"BackendState\"))
print(\"self online:\", d.get(\"SelfNode\",{}).get(\"Online\"))
print(\"peer count:\", len(d.get(\"Peer\",{})))
for k,p in d.get(\"Peer\",{}).items():
    name=p.get(\"HostName\",\"\")
    online=p.get(\"Online\")
    last_seen=p.get(\"LastSeen\")
    print(f\"  {name}: online={online} last_seen={last_seen}\")
'" 2>&1
echo ""
echo "=== tailnet policy (any chance svyatoslava ACLs deny the mesh?) ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale policy -o json 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
acls=d.get(\"acls\",[])
print(\"acls:\", len(acls))
for a in acls[:3]:
    print(\" \", a)
print(\"tagOwners:\", d.get(\"tagOwners\",{}))' 2>&1 | head -20
echo ""
echo "=== any rule in /etc/hosts that helps? ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "cat /etc/hosts | head -20; echo '---'; grep -i svyat /etc/hosts 2>&1" 2>&1
