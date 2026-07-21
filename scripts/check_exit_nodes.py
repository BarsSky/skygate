#!/usr/bin/env python3
"""scripts/check_exit_nodes.py — verify exit-nodes are online and advertise the right routes.

2026-07-15: v0.13.0 — extended with --strict flag and online
check.

Two checks, two failure modes:

  1. STATIC (always): the headscale admin API
     (/api/v1/node) reports the node has the required
     approved routes (0.0.0.0/0 + ::/0). This is the
     pre-existing check, unchanged.

  2. DYNAMIC (--strict only): the node is also "online"
     in headscale's sense (the `online` field in the
     node view, AND the lastSeen timestamp is within
     --offline-after minutes). A node that's reachable
     but its Tailscale session is closed (e.g. a
     laptop that's sleeping) is treated as offline.

The default behaviour is WARN: print a non-empty "WARN:
..." line and exit 0. CI / automated deploys pass
--strict to hard-fail.

Exit codes:
  0  all checks pass (or only warnings under default mode)
  1  static or dynamic check failed (or --strict + dynamic
     check failed)
  2  environment / setup error (missing API key, can't
     reach headscale)

Usage:
  HEADSCALE_URL=http://headscale:50444 HEADSCALE_API_KEY=hskey-... \\
    python3 scripts/check_exit_nodes.py                  # warn-only
  HEADSCALE_URL=... python3 scripts/check_exit_nodes.py --strict  # hard-fail
"""
import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone


# 2026-07-15: v0.13.0 — constants for the online check.
# OFFLINE_AFTER_SECONDS is the time window after lastSeen
# beyond which a node is considered offline. Mirrors
# SKYGATE_EXIT_NODE_OFFLINE_AFTER default (2 min). Operators
# can override via --offline-after.
DEFAULT_OFFLINE_AFTER_SECONDS = 120


def ok(msg):
    print(f"PASS: {msg}")


def warn(msg):
    print(f"WARN: {msg}")


def fail(msg):
    print(f"FAIL: {msg}")


def parse_rfc3339(s):
    """Parse an RFC3339 / RFC3339Nano timestamp. Returns a
    timezone-aware datetime, or None on failure.

    headscale returns either format depending on the
    build; we try the common ones and fall through."""
    if not s:
        return None
    for fmt in (
        "%Y-%m-%dT%H:%M:%S.%fZ",
        "%Y-%m-%dT%H:%M:%SZ",
        "%Y-%m-%dT%H:%M:%S.%f%z",
        "%Y-%m-%dT%H:%M:%S%z",
    ):
        try:
            return datetime.strptime(s, fmt).replace(tzinfo=timezone.utc)
        except ValueError:
            continue
    return None


def is_node_online(node, offline_after_seconds):
    """2026-07-15: v0.13.0 — online check.

    The rule:
      online = node['online'] is true AND
               (lastSeen is empty OR lastSeen is within
                offline_after_seconds of now).

    The second clause is the forgiving fallback. headscale's
    `online` field flips to false the moment the WireGuard
    session closes; a long-lived laptop briefly losing WiFi
    would otherwise flap the check. The lastSeen timestamp
    gives us a small grace period."""
    if not node.get("online"):
        # If lastSeen is recent, treat as online anyway.
        last_seen = parse_rfc3339(node.get("lastSeen"))
        if last_seen is None:
            return False
        age = (datetime.now(timezone.utc) - last_seen).total_seconds()
        return age <= offline_after_seconds
    return True


def main():
    p = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    p.add_argument(
        "--strict",
        action="store_true",
        help="hard-fail (exit 1) when a required exit-node is "
             "offline. Without --strict the same condition is "
             "a warning (exit 0).",
    )
    p.add_argument(
        "--offline-after",
        type=int,
        default=DEFAULT_OFFLINE_AFTER_SECONDS,
        help="seconds after lastSeen beyond which a node is "
             "considered offline (default: %(default)s).",
    )
    p.add_argument(
        "--headscale-url",
        default=os.environ.get("HEADSCALE_URL", "http://localhost:50444"),
    )
    p.add_argument(
        "--api-key",
        default=os.environ.get("HEADSCALE_API_KEY", ""),
    )
    args = p.parse_args()

    if not args.api_key:
        print("ERROR: HEADSCALE_API_KEY not set in env (or pass --api-key)")
        return 2

    base = args.headscale_url.rstrip("/")
    api_key = args.api_key

    # 1. Fetch list of nodes.
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

    # 2. Find exit-capable nodes (tag:exit-node or tag:public).
    exit_nodes = []
    for n in nodes:
        tags = n.get("tags", []) or []
        if "tag:exit-node" in tags or "tag:public" in tags:
            exit_nodes.append(n)

    if not exit_nodes:
        fail("no exit-nodes found (tag:exit-node / tag:public)")
        return 1

    required = ["0.0.0.0/0", "::/0"]
    static_passed = True  # tracks the routes-approved check
    dynamic_failed_any = False  # tracks the online check (--strict only)
    for n in exit_nodes:
        name = n.get("givenName") or n.get("name") or n.get("id", "?")
        approved = n.get("approvedRoutes", []) or []
        online = is_node_online(n, args.offline_after)
        online_str = "online" if online else "offline"
        last_seen = parse_rfc3339(n.get("lastSeen"))
        last_seen_str = "never" if last_seen is None else last_seen.isoformat()
        print(
            f"--- exit-node: {name} "
            f"({len(approved)} approved routes, {online_str}, "
            f"lastSeen: {last_seen_str})"
        )
        for r in required:
            if r in approved:
                ok(f"{name} advertises {r}")
            else:
                fail(f"{name} missing {r}")
                static_passed = False
        if not online:
            # 2026-07-15: v0.13.0 — online check. Default is
            # warn-only (don't block the deploy on a sleeping
            # laptop); --strict makes it hard-fail.
            msg = (
                f"{name} is OFFLINE "
                f"(lastSeen: {last_seen_str}, threshold: "
                f"{args.offline_after}s)"
            )
            if args.strict:
                fail(msg)
                dynamic_failed_any = True
            else:
                warn(msg + " (use --strict to hard-fail)")

    if not static_passed:
        return 1
    if dynamic_failed_any:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
