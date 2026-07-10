#!/usr/bin/env python3
"""scripts/check_exit_nodes.py — verify all exit-nodes advertise 0.0.0.0/0 + ::/0.

Hits the headscale HTTP API directly. Reads HEADSCALE_URL and
HEADSCALE_API_KEY from env. Exits 0 on full pass, 1 on any failure,
2 on env/setup error.

Usage:
  HEADSCALE_URL=http://headscale:50444 HEADSCALE_API_KEY=hskey-... \\
    python3 scripts/check_exit_nodes.py
"""
import json
import os
import sys
import urllib.error
import urllib.request


def ok(msg):
    print(f"PASS: {msg}")


def fail(msg):
    print(f"FAIL: {msg}")


def main():
    base = os.environ.get("HEADSCALE_URL", "http://localhost:50444").rstrip("/")
    api_key = os.environ.get("HEADSCALE_API_KEY", "")

    if not api_key:
        print("ERROR: HEADSCALE_API_KEY not set in env")
        return 2

    # Fetch list of nodes
    url = base + "/api/v1/node"
    req = urllib.request.Request(url, headers={"Authorization": "Bearer " + api_key})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            data = json.load(r)
    except (urllib.error.URLError, urllib.error.HTTPError) as e:
        print(f"ERROR: cannot reach {url}: {e}")
        return 2
    except json.JSONDecodeError as e:
        print(f"ERROR: invalid JSON from {url}: {e}")
        return 2

    nodes = data.get("nodes", [])
    if not nodes:
        print(f"FAIL: no nodes returned from {url}")
        return 1

    print(f"--- {len(nodes)} nodes from headscale")

    # Find exit-capable nodes (tag:exit-node or tag:public)
    exit_nodes = []
    for n in nodes:
        tags = n.get("tags", []) or []
        if "tag:exit-node" in tags or "tag:public" in tags:
            exit_nodes.append(n)

    if not exit_nodes:
        fail("no exit-nodes found (tag:exit-node / tag:public)")
        return 1

    required = ["0.0.0.0/0", "::/0"]
    passed = True
    for n in exit_nodes:
        name = n.get("givenName") or n.get("name") or n.get("id", "?")
        approved = n.get("approvedRoutes", []) or []
        print(f"--- exit-node: {name} ({len(approved)} approved routes)")
        for r in required:
            if r in approved:
                ok(f"{name} advertises {r}")
            else:
                fail(f"{name} missing {r}")
                passed = False

    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
