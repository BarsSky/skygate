#!/usr/bin/env bash
# Look for stale subnet routes or DOCKER-USER blocks + check tailscale state
set +e
echo "=== adverised routes for node 45 (svyatoslava) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys
for n in json.load(sys.stdin):
    if str(n.get(\"id\")) == \"45\":
        print(\"id:\", n.get(\"id\"))
        print(\"given_name:\", n.get(\"given_name\"))
        print(\"online:\", n.get(\"online\"))
        print(\"advertised_routes:\", n.get(\"advertised_routes\"))
        print(\"approved_routes:\", n.get(\"approved_routes\"))'" 2>&1
echo ""
echo "=== skygate VM advertised routes ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys
for n in json.load(sys.stdin):
    if n.get(\"given_name\", \"\").startswith(\"skygate-host-1\"):
        print(\"id:\", n.get(\"id\"), \"name:\", n.get(\"given_name\"))
        print(\"advertised_routes:\", n.get(\"advertised_routes\"))
        print(\"approved_routes:\", n.get(\"approved_routes\"))'" 2>&1
echo ""
echo "=== /var/lib/tailscale/ state file (if we can read) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo ls -la /var/lib/tailscale/ 2>&1" 2>&1
echo ""
echo "=== tailscale serve / tailscale funnel (any local services exposed?) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale serve status 2>&1; echo '==='; tailscale funnel status 2>&1" 2>&1
echo ""
echo "=== do any recent log files mention 45.152 or svyat? ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo grep -i '45\.152\|svyat' /var/log/syslog /var/log/kern.log 2>/dev/null | tail -10" 2>&1
