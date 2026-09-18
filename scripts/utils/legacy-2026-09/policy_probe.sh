#!/usr/bin/env bash
# Check tailnet policy for svyatoslava ACLs
set +e
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 'docker exec headscale headscale policy -o json 2>/dev/null' > /tmp/policy.json
echo "=== policy saved to /tmp/policy.json ==="
cat /tmp/policy.json | python3 -c '
import json,sys
try:
    d=json.load(sys.stdin)
    acls=d.get("acls",[])
    print("acls count:", len(acls))
    for i,a in enumerate(acls[:5]):
        print(f"  acl {i}:", a)
    to=d.get("tagOwners",{})
    print("tagOwners:", json.dumps(to,indent=2)[:500])
    ssv=d.get("ssh",{})
    print("ssh rules:", json.dumps(ssv,indent=2)[:300])
except Exception as e:
    print("error:", e)
    print("first 500 chars:", sys.stdin.read()[:500])
' 2>&1
echo ""
echo "=== file size + ACL grep for svyatoslava/skyworker ==="
wc -c /tmp/policy.json
grep -i svyat /tmp/policy.json | head -3
grep -i skyworker /tmp/policy.json | head -3
