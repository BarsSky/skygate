#!/usr/bin/env bash
set +e
# Get the full headscale policy to see grants
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 '
echo "== get full policy and look for grants =="
docker exec headscale headscale policy get -o json 2>/dev/null > /tmp/policy_full.json
echo "policy file size:"
wc -c /tmp/policy_full.json
echo
echo "== top-level keys in policy =="
python3 -c "
import json
with open(chr(0x2f)+chr(0x74)+chr(0x6d)+chr(0x70)+chr(0x2f)+chr(0x70)+chr(0x6f)+chr(0x6c)+chr(0x69)+chr(0x63)+chr(0x79)+chr(0x5f)+chr(0x66)+chr(0x75)+chr(0x6c)+chr(0x6c)+chr(0x2e)+chr(0x6a)+chr(0x73)+chr(0x6f)+chr(0x6e)) as f:
    d=json.load(f)
print(list(d.keys()))
print()
if chr(0x67)+chr(0x72)+chr(0x61)+chr(0x6e)+chr(0x74)+chr(0x73) in d:
    print(chr(0x67)+chr(0x72)+chr(0x61)+chr(0x6e)+chr(0x74)+chr(0x73)+\":\")
    for g in d[chr(0x67)+chr(0x72)+chr(0x61)+chr(0x6e)+chr(0x74)+chr(0x73)]:
        print(json.dumps(g, indent=2))
        print()
else:
    print(chr(0x4e)+chr(0x4f)+\" \"+chr(0x67)+chr(0x72)+chr(0x61)+chr(0x6e)+chr(0x74)+chr(0x73)+\" key in policy\")
" 2>&1
echo
echo "== file content (head 100 lines) =="
head -50 /tmp/policy_full.json 2>&1
' 2>&1 | head -50
