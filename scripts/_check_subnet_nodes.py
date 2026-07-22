#!/usr/bin/env python3
"""_check_subnet_nodes.py — Python helper for check_subnet_router.sh.

Two modes:

  --list-with-tag
      Print every headscale node that carries tag:subnet-router.
      Used when the DB has no router_node_id (the user hasn't
      run setup.sh yet, or the registered node was deleted).

  --node-id N --cidr C --json-file F
      Look up node N in JSON file F (headscale nodes list -o json
      output), print the relevant fields, and assert that
        - the node exists
        - the node carries tag:subnet-router
        - the node has C in approved_routes (or enabledRoutes as fallback)

Exits 0 on success, 1 on any failure. The shell script greps
the [OK] / [FAIL] / [WARN] tags in the output.
"""
import argparse, json, sys

def strip_bom(s: str) -> str:
    return s.lstrip("\ufeff")

def load_nodes(path: str):
    with open(path, "r", encoding="utf-8") as f:
        return json.loads(strip_bom(f.read()))

def list_with_tag(data):
    found = False
    for n in data:
        tags = n.get("tags") or []
        if "tag:subnet-router" in tags:
            found = True
            print(
                f"    id={n.get('id')}  "
                f"name={n.get('name')}  "
                f"ip={n.get('ip_addresses')}  "
                f"online={n.get('online')}"
            )
    if not found:
        print("    (none — user has not yet run setup.sh on their subnet-router host)")

def check_node(data, node_id, expected_cidr):
    target = None
    for n in data:
        if str(n.get("id")) == str(node_id):
            target = n
            break
    if target is None:
        print(f"[FAIL] headscale has no node with id={node_id} (the user may have re-registered)")
        sys.exit(1)
    print(
        f"  id                = {target.get('id')}\n"
        f"  name              = {target.get('name')}\n"
        f"  ip_addresses      = {target.get('ip_addresses')}\n"
        f"  tags              = {target.get('tags')}\n"
        f"  available_routes  = {target.get('available_routes')}\n"
        f"  approved_routes   = {target.get('approved_routes')}\n"
        f"  subnet_routes     = {target.get('subnet_routes')}\n"
        f"  online            = {target.get('online')}\n"
        f"  last_seen         = {target.get('last_seen')}"
    )
    tags = target.get("tags") or []
    if "tag:subnet-router" not in tags:
        print(
            "  [WARN] node does NOT carry tag:subnet-router — "
            "was it clobbered by the /my/devices backfill? "
            "(v0.26.0+ AddTag fix should prevent this)"
        )
    approved = target.get("approved_routes") or target.get("enabled_routes") or []
    if expected_cidr not in approved:
        print(
            f"  [FAIL] {expected_cidr} is NOT in approved_routes — "
            f"sidecar did not auto-approve"
        )
        sys.exit(1)
    print("  [OK] node + route look healthy")

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--list-with-tag", action="store_true")
    ap.add_argument("--node-id")
    ap.add_argument("--cidr")
    ap.add_argument("--json-file", required=True)
    args = ap.parse_args()
    data = load_nodes(args.json_file)
    if args.list_with_tag:
        list_with_tag(data)
    elif args.node_id is not None and args.cidr is not None:
        check_node(data, args.node_id, args.cidr)
    else:
        print("usage: --list-with-tag  OR  --node-id N --cidr C", file=sys.stderr)
        sys.exit(2)

if __name__ == "__main__":
    main()
