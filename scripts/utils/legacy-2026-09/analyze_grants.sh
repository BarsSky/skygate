#!/usr/bin/env bash
set +e
# Analyze grants and svi's access
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 "
python3 -c '
import json
with open(chr(0x2f)+chr(0x74)+chr(0x6d)+chr(0x70)+chr(0x2f)+chr(0x70)+chr(0x6f)+chr(0x6c)+chr(0x69)+chr(0x63)+chr(0x79)+chr(0x5f)+chr(0x66)+chr(0x75)+chr(0x6c)+chr(0x6c)+chr(0x2e)+chr(0x6a)+chr(0x73)+chr(0x6f)+chr(0x6e)) as f:
    d = json.load(f)
print(\"GRANTS:\")
for i, g in enumerate(d.get(chr(0x67)+chr(0x72)+chr(0x61)+chr(0x6e)+chr(0x74)+chr(0x73), [])):
    src = g.get(chr(0x73)+chr(0x72)+chr(0x63), [])
    dst = g.get(chr(0x64)+chr(0x73)+chr(0x74), [])
    via = g.get(chr(0x76)+chr(0x69)+chr(0x61), [])
    print(f\"  [{i}] src={src}\")
    print(f\"      dst={dst}\")
    if via: print(f\"      via={via}\")
    print()
print()
print(\"USERS:\")
import subprocess
r = subprocess.run([\"docker\", \"exec\", \"headscale\", \"headscale\", \"users\", \"list\", \"-o\", \"json\"], capture_output=True, text=True)
try:
    users = json.loads(r.stdout)
    for u in users:
        print(f\"  id={u.get(chr(0x69)+chr(0x64))} name={u.get(chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65))} display_name={u.get(chr(0x64)+chr(0x69)+chr(0x73)+chr(0x70)+chr(0x6c)+chr(0x61)+chr(0x79)+chr(0x5f)+chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65))}\")
except Exception as e:
    print(\"error:\", e, r.stdout[:200])
print()
print(\"NODE 45 (<polygon-vm-hostname>) user:\")
r2 = subprocess.run([\"docker\", \"exec\", \"headscale\", \"headscale\", \"nodes\", \"list\", \"-o\", \"json\"], capture_output=True, text=True)
try:
    nodes = json.loads(r2.stdout)
    for n in nodes:
        if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
            u = n.get(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72), {})
            print(f\"  node 45 user: id={u.get(chr(0x69)+chr(0x64))} name={u.get(chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65))}\")
            print(f\"  forced_tags={n.get(chr(0x66)+chr(0x6f)+chr(0x72)+chr(0x63)+chr(0x65)+chr(0x64)+chr(0x5f)+chr(0x74)+chr(0x61)+chr(0x67)+chr(0x73))}\")
            print(f\"  valid_tags={n.get(chr(0x76)+chr(0x61)+chr(0x6c)+chr(0x69)+chr(0x64)+chr(0x5f)+chr(0x74)+chr(0x61)+chr(0x67)+chr(0x73))}\")
except Exception as e:
    print(\"error:\", e, r2.stdout[:200])
'
" 2>&1 | head -80
