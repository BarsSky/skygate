# 192.168.13.67 `node not found` 404 — investigation v2 (operator-corrected)

**Date:** 2026-08-25 (12:09 UTC, 3h after v1)
**Operator correction:** "это не домашний роутер проверь еще раз внимательно в чем причины"
**Status:** root cause narrowed; remaining ambiguity needs operator input

---

## TL;DR (corrected)

The v1 conclusion "192.168.13.67 is the home router" was **WRONG**.
The runbook reference is stale (12 days old, 2026-08-13). Per the
operator's correction and additional evidence:

- **192.168.13.67 IS the Nginx Proxy Manager (NPM)** — confirmed by
  multiple independent sources (port 81 React SPA with title
  "Nginx Proxy Manager", `derper_snapshot.py:26` classifying it
  as `NPM_HOST = "192.168.13.67"`, the `.env` `SKYGATE_DERP_PEER_NPM`
  variable). The runbook's "home router" was a misnomer or the IP
  was repurposed after the v1.3.10 split fix.
- **The 404s come from a Tailscale-protocol-speaking process on
  192.168.13.67** with a stale NodeKey. The 15-byte POST body
  is too small for a real MapRequest, suggesting a degraded or
  custom client.
- **The operator's "no Tailscale client on .67" is contradicted
  by the data** — headscale logs show BOTH a 200 (valid, port
  39286) and a 404 (stale, port 44726) from 192.168.13.67. Two
  long-lived HTTP/2 connections from one host = two Tailscale
  clients (or one client with two node keys). The 200 client IS
  a Tailscale client; the 404 client is what we need to identify.
- The 8080 "Mobile Gateway" gunicorn is a **custom Flask app
  that's NOT part of standard NPM** (standard NPM only has
  openresty on 80/443 + admin on 81). This is the prime
  suspect for the source of the 404s.

---

## What changed since v1

| Aspect | v1 (wrong) | v2 (corrected) |
|--------|-----------|----------------|
| Identity of .67 | "Home router" (per stale runbook) | **NPM** (confirmed 3 ways) |
| 8080 service | Unknown | **"Mobile Gateway" — custom Flask app, NOT standard NPM** |
| Valid 200 from .67 | Not analyzed | **Two Tailscale clients: valid (39286) + stale (44726)** |
| Source of 404s | "Stale Tailscale state on home router" | **Stale Tailscale-protocol client on the NPM host** |
| Operator's "no Tailscale client" claim | Treated as full denial | **Treated as "no obvious tailscaled, but something is using Tailscale protocol"** |

---

## Evidence chain (v2)

### 1. NPM identity confirmed

```
$ curl -sS http://192.168.13.67:81/ | head -5
<!DOCTYPE html>
<html lang="en">
    <head>
        <meta charset="utf-8" />
        <title>Nginx Proxy Manager</title>
        <meta name="description" content="In The Office Planner" />
```

Standard NPM React SPA. The operator confirmed by clicking through
this UI — it's the local NPM (fronting skygate + skynas.ru marketing
landing).

`/usr/local/bin/derper_snapshot.py:26`:
```python
NPM_HOST = "192.168.13.67"  # ws_admin (Nginx Proxy Manager WebSocket pool)
```

The `derper` snapshot script explicitly classifies connections from
this IP as "ws_admin" (NPM admin traffic, not Tailscale relay).

`/home/skyadmin/skygate/internal/feature/admin/derp.go:430`:
```go
// classifyDerpPeer labels a connection source.
//   ws_admin - Nginx Proxy Manager WebSocket pool (SKYGATE_DERP_PEER_NPM)
```

The Go code also references 192.168.13.67 as the NPM.

### 2. Two long-lived Tailscale connections from 192.168.13.67

```
$ docker logs headscale --tail 50 | grep '192.168.13.67' | tail -10
2026-08-25T09:15:15Z INF http request bytes=0   elapsed=153.2ms  POST /machine/map proto=HTTP/2.0 remote=192.168.13.67:39286 status=200
2026-08-25T09:15:16Z INF http request bytes=0   elapsed=423.6ms  POST /machine/map proto=HTTP/2.0 remote=192.168.13.67:39286 status=200
2026-08-25T09:15:33Z ERR user msg: node not found code=404
2026-08-25T09:15:33Z INF http request bytes=15  elapsed=0.27ms   POST /machine/map proto=HTTP/2.0 remote=192.168.13.67:44726 status=404
2026-08-25T09:16:15Z ERR user msg: node not found code=404
2026-08-25T09:16:15Z INF http request bytes=15  elapsed=0.36ms   POST /machine/map proto=HTTP/2.0 remote=192.168.13.67:44726 status=404
```

- **Port 39286: VALID client** (0-byte POST, 200 in 100-700ms = real MapRequest response). This is a normal Tailscale client with a valid NodeKey.
- **Port 44726: STALE client** (15-byte POST, 404 in 0.2-0.5ms = headscale "no such node" reject). This is what's making noise.
- **Plus `HEAD /machine/ping-response`** every 9s from varying ephemeral source ports (HTTP/1.1 keep-alive pings).

**The "no Tailscale client" claim is partially false** — there IS a Tailscale client on 192.168.13.67 (the 200 one). What the operator might mean is "no obvious tailscaled process I can see". The "Mobile Gateway" gunicorn on 8080 is the prime suspect for being a Tailscale-aware custom app that has TWO node keys — one valid, one expired.

### 3. The "Mobile Gateway" gunicorn (port 8080) — NOT standard NPM

```
$ curl -sS -I http://192.168.13.67:8080/
HTTP/1.1 401 UNAUTHORIZED
Server: gunicorn
WWW-Authenticate: Basic realm="Mobile Gateway"
```

- **Server: gunicorn** (Python WSGI, NOT openresty)
- **Realm: "Mobile Gateway"** — a Flask/FastAPI app authenticating with Basic auth
- **All paths return 401** (`{"error":"Unauthorized"}`) — every endpoint requires auth
- Standard NPM does NOT have a gunicorn on 8080. NPM has:
  - openresty on 80/443
  - A separate openresty on 81 (admin UI)
  - A SQLite/Postgres database
- So port 8080 is a **separate service** that the operator has deployed alongside NPM on the same host.

This is the most likely source of the 404s:
- A Tailscale-aware app (uses the Tailscale control protocol)
- Has cached credentials (one valid NodeKey, one stale)
- Periodically polls headscale with the stale key → 404

### 4. The runbook "home router" was the operator's prior context

`docs/TAILNET-SPLIT-FIX-RUNBOOK.md:113-115` (from 2026-08-13, 12 days ago):
> 3. **Home devices** last. The home router at 192.168.13.67 is
>    the source of the old session — re-authing home devices kills
>    that session.

This was either:
- A misnomer (the author meant "gateway at 192.168.13.67", not "home router")
- The IP was the home router 12 days ago, then repurposed for NPM
- The home router was a separate device that used 192.168.13.67 as its management IP

Doesn't matter — 192.168.13.67 is the NPM now. The runbook is stale.

### 5. The DERP config has a duplicate (FIXED)

`config.yaml:23` had two identical entries. Fixed to one. The fix is
in the file but **headscale has NOT been restarted** — operator's
call on when to bounce the service (5-second blip, no impact on
existing sessions because they re-connect automatically).

---

## What's left to determine (operator input needed)

### 1. What is the "Mobile Gateway" gunicorn on 192.168.13.67:8080?

It has Basic auth on all paths. From the outside, I can only see:
- It's a Python Flask/FastAPI app
- Auth realm is "Mobile Gateway"
- Likely a Tailscale-aware tool (given the protocol traffic)

The operator knows what's deployed there. Likely candidates:
- **Custom tailscale admin panel** for the operator (login + view nodes)
- **Home automation app** that integrates Tailscale
- **Custom DERP client** (less likely, DERP doesn't talk control plane)
- **A skygate-side test client** that was never cleaned up

If the operator can tell me what the gunicorn is + provide the
credentials (or just kill the process), we can verify if it's the
source of the 404s.

### 2. Why is there a stale NodeKey on 192.168.13.67?

The 404 client has a NodeKey that headscale doesn't recognize. This
means the key was once valid but the node was deleted (or never
existed under that key).

Scenarios:
- The "Mobile Gateway" was once a registered Tailscale client (NodeKey
  issued by the operator). Then the operator rotated keys / deleted
  the node, but the app kept the old key.
- The 192.168.13.67 host once ran tailscaled (perhaps for the
  "skynas.ru home router" function in v1.3.10). The tailscaled was
  uninstalled but the NodeKey is still in some app's config.
- It's a custom Tailscale integration that was set up with a NodeKey
  from a test environment, not the current headscale.

### 3. The 200 client — what is it?

The 200 client (port 39286) is a real Tailscale client with a valid
NodeKey. headscale returns the network map (100-700ms latency = real
Tailscale response time). It could be:
- The operator's primary work machine (probably not — the user said
  no Tailscale client on .67)
- The "Mobile Gateway" itself, with TWO NodeKeys (one valid, one stale)
- A different service on .67 that we haven't identified

---

## How to silence the 404s (still waiting for operator's call)

Given the "what is it?" question is open, here are the options
in order of how invasive they are:

### Option A: iptables block (immediate, no app access needed)

```bash
# On skygate VM (192.168.13.69) — DROP 192.168.13.67 → headscale:50444
iptables -I INPUT -s 192.168.13.67 -p tcp --dport 50444 -j DROP
netfilter-persistent save
```

Stops the 404s. **But also blocks the 200 client** (the valid
Tailscale client on .67 from getting map updates). If the 200
client is the "Mobile Gateway" or any other live service, this
breaks it.

### Option B: block ONLY the stale port (preserves the 200 client)

The 404s come from source port 44726 (fixed). The 200s come from
ephemeral ports on 39286. **Can't filter on destination port alone**
since both are HTTP/2. **Would need to filter on connection state**
or use `conntrack` to identify the offending stream.

Actually, the 200 client uses HTTP/2 stream multiplexing on its
connection. Both 200s and 404s are over HTTP/2. So a single
firewall rule per source port won't work cleanly.

**This option needs a smarter approach** (e.g., run headscale behind
nginx and filter by NodeKey in the URL, or modify headscale to log
+ reject stale keys with a custom error).

### Option C: kill the stale process on 192.168.13.67 (clean fix)

If the operator can SSH to 192.168.13.67 (despite port 22 being
closed from our subnet) and identify the stale client, they can
kill it. Then the 404s stop naturally.

**Caveat:** port 22 is closed from the skygate subnet. The operator
would need to SSH from inside the home network, or from the
NPM's console, or some other path.

### Option D: delete the stale node from headscale (cosmetic)

If the operator can identify the NodeKey, deleting the node from
headscale might make the client re-register. But without a valid
preauth key, it would just keep failing.

**This doesn't help** — headscale already doesn't recognize the key.

### Option E: ignore (5 log lines per minute is fine)

The 404s don't break anything. They're just noise in
`docker logs headscale`. If the operator doesn't want to touch
the NPM right now, this is a valid choice.

---

## Recommended next step

**Ask the operator:** "What is the 'Mobile Gateway' gunicorn on
192.168.13.67:8080?" — once known, we can:
- Decide if it's expected to talk to headscale (and we just need to
  refresh its credentials)
- Decide if it's unexpected and should be killed/removed
- Decide if iptables is acceptable (Option A) given the impact on
  the valid 200 client

---

## Other findings (still pending)

- **DERP relay optimization for sharlotta + karolina**: operator's
  note "helsinki not available from RF only for sharlotta and
  karolina" — skygate VM can use hel=38ms but the Russian VPS
  cannot. Need to test alternative DERP regions (fra, ams, waw,
  sfo, etc.) from each VPS to find the best non-hel relay.
  Suggested plan: `tailscale ping <derp-region>` from each VPS to
  measure latency, then `tailscale set --preference=<best-region>`
  on the slowest node.

- **headscale restart for DERP dedup**: the duplicate URL is removed
  from config, but headscale is still running with the old config
  in memory. Operator should run:
  ```bash
  docker compose -f /home/skyadmin/headscale/docker-compose.yml restart headscale
  ```
  5-second blip, sessions resume automatically. Not urgent.

- **Node 35 "SkyBars" still shows pending** in /my/devices because
  the `node_owner_map` row has hostname "skybars-1" but the live
  headscale has "SkyBars". The DB row is stale. Workaround: manual
  `headscale nodes tag` was applied (B176) but the DB row didn't
  update. Could be a Strategy F (legacy orphan) follow-up B-check.

---

## B177 follow-up: dev-tag rename strips old tag on AddTag failure

While investigating the rename of id=35 from skybars-1 to skybars-secure,
a second bug surfaced: the autoupdater's rename block does
UntagNode(old_tag) BEFORE AddTag(new_tag). When headscale rejected the
new 	ag:dev-skyadmin-skybars-secure with InvalidArgument: requested
tags are invalid or not permitted (the tag had never been whitelisted,
so headscale 0.29's ACL rejected it on first sight), the old
	ag:dev-skyadmin-skybars had already been removed — leaving id=35 with
**no dev-tag at all** (verified at 10:22:40 in skygate stderr:
DBG backfill rename node=35 old_hostname=skybars-1 new_hostname=skybars-secure
untag=tag:dev-skyadmin-skybars add=tag:dev-skyadmin-skybars-secure immediately
followed by warn: auto-apply dev tag  tag:dev-skyadmin-skybars-secure to node 35:
tag: exit status 1).

**B177** (v1.5.2) fixes this by swapping the order in
internal/nodeownership/nodeownership.go's rename block: AddTag(new_tag)
runs first, and UntagNode(old_tag) only fires on success. The DB row
update (UpdateNodeOwnerHostnameAndTag) moves inside the AddTag success
branch so a failed AddTag doesn't leave the row out of sync with headscale.
The warn log now says keeping existing tags as fallback to make the
defensive intent visible in skygate's stderr.

This is a defensive fix: the underlying headscale ACL issue (a new
	ag:dev-* tag cannot be added to a node that has never seen it) is a
separate concern, and operator can fix it by adding the tag to the
appropriate ACL 	ag_owners block.

10 contracts in scripts/check_b177.sh. Live-verified 2026-08-25 on id=35
after the manual headscale nodes tag --force re-apply restored the B176
state to 	ag:dev-skyadmin-skybars + 	ag:private.
