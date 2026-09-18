#!/usr/bin/env bash
set +e
# Restart headscale to pick up DB change
ssh skyadmin@192.168.13.69 '
docker restart headscale 2>&1
sleep 5
docker exec headscale headscale nodes list -o json 2>&1 | python3 -c "
import json,sys
for n in json.load(sys.stdin):
    if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
        u=n.get(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72),{})
        print(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72), u.get(chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65)), chr(0x69)+chr(0x64), u.get(chr(0x69)+chr(0x64)))
"
' 2>&1
