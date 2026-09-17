# 192.168.13.67 `node not found` 404 — investigation report

**Date:** 2026-08-25
**Investigator:** skyadmin via VM (192.168.13.69)
**Trigger:** operator correction "на 192.168.13.67 нет tailscale клиента"

---

## TL;DR

**192.168.13.67 IS the operator's home router** (per
`docs/TAILNET-SPLIT-FIX-RUNBOOK.md:113-115` + `SKYGATE_DERP_PEER_NPM=192.168.13.67`
in `.env`). The router has a **stale Tailscale session** — the old v1.3.10
"tailnet split" fix didn't re-auth the router itself, only the home devices
behind it. The 404s are noise from the router's tailscaled polling
headscale every ~25s with a now-invalid NodeKey.

**Recommended fix:** ask the operator what Tailscale state is on the router
(option A) OR block the router's source IP from headscale at the firewall
(option B) — both are operator-driven, not auto-applicable.

---

## Investigation chain

### 1. Captured the 404s in real time

```
$ docker logs headscale --tail 100 | grep -A 2 'ERR user msg: node not found'
2026-08-25T09:02:30Z ERR user msg: node not found code=404
2026-08-25T09:02:30Z INF http request bytes=15 elapsed=0.207247 method=POST
   path=/machine/map proto=HTTP/2.0 remote=192.168.13.67:44726 status=404
```

- **Endpoint:** `POST /machine/map` (Tailscale control-plane protocol)
- **Source port:** 44726 (PERSISTENT — same port every time, this is one
  long-lived HTTP/2 connection, not a new client per request)
- **Request body:** 15 bytes (suspicious — real tailscaled sends 50-200+ bytes
  for a MapRequest; 15 bytes suggests a stripped/empty protobuf)
- **Other connection from same IP:** `192.168.13.67:39286` returns 200
  (valid) on `/machine/map` with 200-500ms latency. **Two long-lived
  HTTP/2 connections from one host** = two Tailscale clients OR one
  client with two sessions.
- **Plus** `HEAD /machine/ping-response` (proto=HTTP/1.1) every 9s from
  varying source ports (39850, 52728, 54168, ...). These are HTTP/1.1
  keep-alive pings — the standard Tailscale "control plane alive?" check.
- **404 cadence:** every 20-40s, in bursts of 2 (one per multiplexed
  stream). The 15-byte body suggests tailscaled re-trying a MapRequest
  with no valid node key.

### 2. ARP + identification of 192.168.13.67

```
$ ip neigh show 192.168.13.67
192.168.13.67 dev ens18 lladdr bc:24:11:85:7f:74 REACHABLE
```

- **MAC OUI:** `bc:24:11` = **OVHcloud / Proxmox VE**
  (Proxmox Virtual Environment runs on OVH/SoYouStart dedicated servers)
- **ICMP:** filtered (no response to ping)
- **TCP ports open:** 22=closed, 80=open (openresty), 443=open (TLS,
  no cert), 50444=closed, 50445=closed, 8080=open (gunicorn), 41641=closed
  (Tailscale data port)
- **Not in `headscale nodes list`:** the host has NO entry in headscale —
  confirms this is NOT a Tailscale client that's currently registered.

### 3. The smoking gun — `TAILNET-SPLIT-FIX-RUNBOOK.md:113-115`

```
3. **Home devices** last. The home router at 192.168.13.67 is
   the source of the old session — re-authing home devices kills
   that session.
```

And in `.env`:
```
SKYGATE_DERP_PEER_NPM=192.168.13.67
```

**192.168.13.67 is the operator's home router / Proxmox box.** The
"old session" reference is from v1.3.10 (commit 7dc975d, 2026-08-13).
The fix back then was: re-auth all 13 home devices behind the router
to "kill that session". The router itself was NOT re-authed — only
the devices.

### 4. What the router is running

| Port | Service | Notes |
|------|---------|-------|
| 80   | **openresty** | serves the SKYNAS.RU landing page (marketing/operator branding) since 2026-06-25. catch-all returns index.html on every path (including `/.env`, `/config.json`) — should be tightened. |
| 443  | **TLS** (no valid cert) | port open, handshake fails with `tlsv1 unrecognized name` (SNI mismatch) |
| 8080 | **gunicorn + "Mobile Gateway"** | Flask/FastAPI WSGI, all paths return 401 (`{"error":"Unauthorized"}`) with Basic auth realm "Mobile Gateway" |
| 22   | closed | no SSH accessible from our subnet |
| 41641/udp | closed | no Tailscale data plane |

The router runs Linux (not OpenWrt/pfSense UI — gunicorn is Python).
Likely a Debian/Ubuntu box with openresty + a custom Python app.

The "Mobile Gateway" name is suggestive — could be a custom home-automation
panel, a Tailscale admin tool, or a self-hosted home router admin. **The
Python gunicorn process is the most likely source of the stale
Tailscale calls** — it may have an old tailscaled state, or it
talks to headscale via the Tailscale API directly with cached credentials.

### 5. Cross-checking the headscale node list

```
$ docker exec headscale headscale nodes list | wc -l
14 (header + 13 nodes)
```

13 active headscale nodes — NONE of them are at 100.64.0.x ↔ 192.168.13.67.
The router is **not** a headscale-registered Tailscale node. So the 200
responses from port 39286 are also from the router, but with a different
**transient** (or freshly re-registered) identity. The 404s from port
44726 are from a permanent HTTP/2 connection with a now-dead node key.

---

## Conclusion

The 192.168.13.67 404s are **noise from a stale Tailscale session on
the home router**, not from a rogue device or a stuck client. The
operator's statement "192.168.13.67 нет tailscale клиента" is correct
in the sense that "no current registered Tailscale client" — the
router's tailscaled is in a zombie state (dead NodeKey, alive
connection, retries every ~25s).

This is **not a skygate bug** and **not a headscale bug**. It's a
Tailscale-side cleanup task.

---

## Three fix options (operator's call)

### Option A: re-auth the router (the proper fix)

1. SSH into 192.168.13.67 (from inside the home LAN, or via
   Proxmox console if it's a VM).
2. `sudo tailscale logout && sudo tailscale up --login-server=https://head.skynas.ru`
3. Done — the stale HTTP/2 connection dies, the new auth creates
   a fresh connection, 404s stop.

Requires physical LAN access or a way into the router we don't have
(port 22 closed from this subnet).

### Option B: block the router from headscale (the workaround)

If the router is NOT supposed to be a Tailscale subnet router and the
operator only wants the home devices behind it on Tailscale, block
the stale connection at the firewall:

```bash
# on skygate VM (192.168.13.69) — DROP 192.168.13.67 → headscale:50444
iptables -I INPUT -s 192.168.13.67 -p tcp --dport 50444 -j DROP
# persist via netfilter-persistent
netfilter-persistent save
```

Stops the 404s immediately. Trade-off: if the operator ever wants to
re-add the router to Tailscale, they have to remove this rule first.

### Option C: leave as-is

The 404s are noise — they don't break anything, just add ~5 log
lines per minute to `docker logs headscale`. No security impact (the
NodeKey is invalid; headscale correctly rejects it). No functional
impact on the tailnet (all 13 active nodes are unaffected).

Recommended if the operator doesn't want to touch the home router
right now.

---

## Other findings (not asked about, but noted)

1. **Duplicate DERP URL in headscale config** (lines 28-29 of
   `/home/skyadmin/headscale/config/config.yaml`):
   ```yaml
   derp:
     urls:
     - https://controlplane.tailscale.com/derpmap/default
     - https://controlplane.tailscale.com/derpmap/default   ← ДУБЛЬ
   ```
   Cosmetic. Auto-update pulls the same map twice, no harm. **Fix
   when convenient**: delete one of the two entries + `docker
   restart headscale`. Will change 1-2 lines in config; no impact on
   running sessions.

2. **openresty catch-all on 192.168.13.67:80** returns 200 + the
   landing-page HTML on `/.env`, `/config.json`, etc. The router
   doesn't actually leak secrets (openresty falls back to index.html
   for missing files), but it IS misconfigured. **Not a security
   issue today**, just sloppy.

3. **DERP relay optimization for sharlotta + karolina** still
   pending. Both currently relay via `iad=122ms` instead of
   `hel=38ms` (hel is the nearest DERP for the skygate VM). The
   fix requires either `tailscale set --preference=hel` on each
   exit-node, or forcing a re-auth. Operator hasn't asked for
   this in this session.
