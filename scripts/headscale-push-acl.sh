#!/bin/bash
# scripts/headscale-push-acl.sh — push a fresh per-user ACL to headscale.
# Used to fix the Tailscale Android "all nodes visible" bug.
#
# Builds a policy with:
#   - per-user rules (user@... → user@...:*)
#   - tag:public / tag:exit-node visible to all
#   - internet egress (last *:*)
#   - tagOwners: all portal users own tag:private
#   - groups: one per portal user
#   - SSH rule kept from current policy
#
# Usage:
#   bash scripts/headscale-push-acl.sh
#
# Reads HEADSCALE_API_KEY from /home/skyadmin/skygate/.env.

set -e

#!/bin/bash
set -e
KEY=$(grep -oP 'HEADSCALE_API_KEY=\K[^\s\n]+' /home/skyadmin/skygate/.env)
[ -z "$KEY" ] && { echo "no key"; exit 1; }
echo "key prefix: ${KEY:0:12}..."

# Save python script (no interpolation issues)
cat > /tmp/do_push.py <<'PYTHON'
import sys, json, re, urllib.request

K = sys.stdin.read().strip()
def req(method, path, data=None):
    h = {"Authorization": "Bearer " + K, "Content-Type": "application/json"}
    body = json.dumps(data).encode() if data is not None else None
    r = urllib.request.Request("http://localhost:50444" + path, data=body, headers=h, method=method)
    return json.loads(urllib.request.urlopen(r).read().decode(), strict=False)

raw = req("GET", "/api/v1/policy")["policy"]
fixed = re.sub(r",(\s*[}\]])", r"\1", raw)
current = json.loads(fixed, strict=False)
print("current tagOwners:", current.get("tagOwners"))

users = req("GET", "/api/v1/user")["users"]
user_emails = [u["name"] + "@tsnet.skynas.ru" for u in users]
print("users:", user_emails)

new_acls = []
for ue in user_emails:
    new_acls.append({"action": "accept", "src": [ue], "dst": [ue + ":*"]})
new_acls.append({"action": "accept", "src": ["*"], "dst": ["tag:public:*"]})
new_acls.append({"action": "accept", "src": ["*"], "dst": ["tag:exit-node:*"]})
# NOTE: deliberately no "*:*" rule. Without it headscale applies
# default-deny, and the Tailscale Android client hides nodes that
# are not allowed for the current user. Internet egress still works
# because each user@...:* rule above covers the device's own
# advertised routes (including 0.0.0.0/0 advertised by tag:exit-node
# devices). Direct internet from a device is denied.

new_tag_owners = {
    "tag:public": ["skyadmin@tsnet.skynas.ru"],
    "tag:exit-node": ["skyadmin@tsnet.skynas.ru"],
    "tag:client": ["skyadmin@tsnet.skynas.ru"],
    "tag:private": user_emails,
}
new_groups = {}
for ue in user_emails:
    gname = "group:" + ue.split(chr(64))[0]
    new_groups[gname] = [ue]

new_ssh = current.get("ssh", [])
if not new_ssh:
    new_ssh = [
        {
            "action": "accept",
            "src": ["tag:private", "skyadmin@tsnet.skynas.ru"],
            "dst": ["tag:exit-node"],
            "users": ["root"],
        }
    ]

new_policy = {
    "acls": new_acls,
    "tagOwners": new_tag_owners,
    "groups": new_groups,
    "ssh": new_ssh,
}
print("new policy size:", len(json.dumps(new_policy)))

result = req("PUT", "/api/v1/policy", {"policy": json.dumps(new_policy)})
print("PUT result:", result)

raw2 = req("GET", "/api/v1/policy")["policy"]
fixed2 = re.sub(r",(\s*[}\]])", r"\1", raw2)
new = json.loads(fixed2, strict=False)
print("NEW tagOwners:", new.get("tagOwners"))
print("NEW groups keys:", list(new.get("groups", {}).keys()))
print("NEW acls count:", len(new.get("acls", [])))
PYTHON

chmod 666 /tmp/do_push.py
head -3 /tmp/do_push.py

echo "$KEY" | python3 /tmp/do_push.py
