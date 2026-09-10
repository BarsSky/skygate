#!/usr/bin/env bash
# Get policy via correct command
set +e
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 'docker exec headscale headscale policy get -o json' > /tmp/policy2.json 2>/dev/null
ls -la /tmp/policy2.json
cp /tmp/policy2.json C:/Projects/skygate/policy2.json 2>&1 | head -5
