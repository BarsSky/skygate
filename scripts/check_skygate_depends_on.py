#!/usr/bin/env python3
"""
B91 helper: verify docker-compose.yml has no depends_on on skygate
service that references headscale/headplane.

caddy has depends_on: - skygate, which is fine (caddy waits for
skygate, not the other way around). The check ONLY enforces the
reverse direction: skygate must not wait for headscale/headplane.

Used by verify_pre_deploy.sh B91 check. Returns exit code 0 if OK,
1 if skygate has depends_on (loose-coupling violation), 2 if
PyYAML is not available.
"""
import sys

try:
    import yaml
except ImportError:
    print("SKY-FAIL: PyYAML not available", file=sys.stderr)
    sys.exit(2)

with open("docker-compose.yml") as f:
    data = yaml.safe_load(f)

services = data.get("services") or {}
skygate = services.get("skygate") or {}

if "depends_on" in skygate:
    print(
        f"SKY-FAIL: skygate has depends_on: {skygate['depends_on']} "
        "(loose-coupling violation — skygate must start independently of "
        "headscale/headplane so admin can fix HEADSCALE_URL via "
        "/admin/headscale even if headscale is down)",
        file=sys.stderr,
    )
    sys.exit(1)

print("OK: skygate has no depends_on (loose-coupling preserved)")
sys.exit(0)
