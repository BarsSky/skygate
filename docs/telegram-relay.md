# Telegram relay setup — Tailscale in-image (Этап 14 v2)

This document describes how to set up a relay node so that
`skygate` (deployed in a jurisdiction where api.telegram.org is
blocked at the network level) can still reach the Telegram Bot
API.

## Architecture

```
[ skygate container ]
   ├─ eth0  (host network)  → headscale (tailnet)  — direct
   ├─ eth0  (host network)  → anything else         — BLOCKED on RF VPS
   └─ tailscale0 (TUN)      → 91.108.0.0/16, etc.  — via relay (RF bypass)
                              149.154.160.0/20
                              185.76.151.0/24

[ relay (e.g. emilia) ]
   └─ tailscaled --advertise-routes=<Telegram CIDRs>
       ↑ admin approves the routes in headscale
```

The key insight: we use **subnet routes**, not exit nodes. skygate
never has `tailscale set --exit-node=...` invoked, so its
non-tailnet traffic only goes via the relay for the specific
Telegram IP ranges the relay advertises. Everything else (the
headscale API, the local Docker network) stays direct.

## Why not exit-node?

`tailscale set --exit-node=...` replaces the **default route** for
that client. Every non-tailnet packet from skygate would go
through the relay, even packets that have nothing to do with
Telegram. With subnet routes, only the Telegram IP ranges are
re-routed — clean, auditable, and the relay isn't asked to
forward unrelated traffic.

## One-time setup

### 1. Pick a relay host

You need a host that:

* Has tailscaled installed and is logged in to your headscale
  control server.
* Has unrestricted internet egress (this is the whole point —
  the relay reaches api.telegram.org where skygate cannot).
* Is on the same headscale tailnet as skygate (or will be).

Current candidates on the tsnet.skynas.ru tailnet:
**emilia** (100.64.0.3), **sharlotta** (100.64.0.4), **karolina**
(100.64.0.2). All three are already configured as
exit-node-offerers, which is a good indicator that they have
unrestricted internet.

### 2. Advertise the Telegram IP ranges on the relay

SSH to the relay host and run:

```sh
sudo /opt/skygate/deploy/tailscale-relay/setup.sh
```

The script applies:

```
tailscale set --advertise-routes="\
  91.108.4.0/22,91.108.8.0/22,91.108.12.0/22,91.108.16.0/22,\
  91.108.20.0/22,91.108.56.0/22,149.154.160.0/20,185.76.151.0/24,\
  2001:67c:4e8::/48,2001:b28:f23c::/48,2001:b28:f23f::/48,2001:7a0:1::/48"
```

The IPv6 ranges are aspirational (the Go HTTP client does A
resolution by default); they are advertised for forward
compatibility.

### 3. Approve the routes in headscale

The relay can advertise routes, but the **headscale admin** must
approve them before any client picks them up. Two ways:

**A. Headplane web UI** (recommended for human-driven deploys):

1. Open `https://head.skynas.ru/` → Machines.
2. Click the relay node.
3. In the "advertised routes" section, tick each route and click
   "Approve".

**B. headscale CLI** (for scripted deploys):

```sh
ssh head.skynas.ru
docker exec headscale headscale nodes list
# find the relay's ID (numeric, e.g. 5)
docker exec headscale headscale nodes approve-routes \
    --identifier 5 \
    --routes "91.108.4.0/22,91.108.8.0/22,91.108.12.0/22,91.108.16.0/22,91.108.20.0/22,91.108.56.0/22,149.154.160.0/20,185.76.151.0/24"
```

### 4. Verify skygate picked them up

On the skygate host:

```sh
docker exec skygate tailscale status
# expected: a "Subnets" section listing the routes, each marked
# as "via 100.64.0.X" (the relay's tailnet IP)

docker exec skygate tailscale netcheck
# expected: a list of "tailscale latency" rows + "Uses subnet
# routes" or similar
```

Then on the `/admin/telegram` page in the skygate web UI the
probe banner should switch from "unreachable" to "reachable
(Tailscale relay)" within ~60s of the routes being approved.

## Maintenance

### Updating Telegram's IP ranges

Telegram occasionally adds new IP blocks. The relay's route
advertisement is static, so a new block would not be covered
until you re-run the setup.

The cron-friendly script is
`deploy/tailscale-relay/update-routes.sh`. It:

1. Resolves `api.telegram.org`, `web.telegram.org`, and
   `core.telegram.org` from three public DNS resolvers
   (1.1.1.1, 8.8.8.8, 9.9.9.9).
2. Aggregates the union of returned A records into the
   canonical Telegram CIDRs (91.108.0.0/16,
   149.154.160.0/20, 185.76.151.0/24).
3. Re-applies the routes via `tailscale up
   --advertise-routes=...` (resets, not additive — so old
   routes that Telegram no longer uses are pruned).
4. Prints the `headscale nodes approve-routes` command the
   admin needs to run.

The script is deployed to `/usr/local/bin/skygate-update-telegram-routes`
on each relay. A weekly cron on the relay refreshes the routes
automatically:

```cron
0 4 * * 1  /usr/local/bin/skygate-update-telegram-routes >> /var/log/skygate-telegram-routes.log 2>&1
```

**Current state (2026-07-14):** cron installed on both
emilia and sharlotta. To re-deploy after a fresh relay setup:

```sh
scp deploy/tailscale-relay/update-routes.sh relay:/usr/local/bin/skygate-update-telegram-routes
ssh relay 'chmod +x /usr/local/bin/skygate-update-telegram-routes && \
           echo "0 4 * * 1 /usr/local/bin/skygate-update-telegram-routes >> /var/log/skygate-telegram-routes.log 2>&1" | crontab -'
```

Or trigger it manually:

```sh
ssh relay-host
sudo /usr/local/bin/skygate-update-telegram-routes
```

The script refuses to apply an empty route list (would wipe
the relay's advertisement and break the bot).

### Failover to a different relay

If emilia goes down, the bot will start timing out on
getUpdates. To fail over to sharlotta:

1. SSH to sharlotta, run `setup.sh` (it will add sharlotta's
   advertisement of the same routes).
2. SSH to emilia, run `tailscale set --advertise-routes=` (no
   routes) to drop emilia's advertisement. Or disable the
   routes via headscale's CLI/admin UI.
3. Wait ~60s for the new advertisement to propagate; skygate
   will start using sharlotta automatically (Tailscale picks
   the relay with the lower metric by default).

Both emilia and sharlotta can advertise the same routes
simultaneously — Tailscale uses the route metric to pick one.
Only disable a route on the down relay when the operator
wants to force the other one.

**Current state (2026-07-14):** both emilia and sharlotta are
configured as primary relays (Tailscale picks one based on
metrics — typically the closer / faster one). karolina is
available as a third-tier backup; flip the same way if both
emilia and sharlotta go down.

### Disabling Tailscale on skygate entirely

For a non-RF deployment (or to test direct internet from an
RF VPS that happens to have a working route), remove the
`TS_AUTHKEY_FILE` env var and the `secrets/ts_authkey` mount
from `docker-compose.yml`. The skygate entrypoint will then
skip tailscaled entirely; skygate's bot polling will go
through eth0 directly.

This is the "single image, two modes" property the new
design is built around: the same Docker image, the same
docker-compose.yml, the same go binary — the only thing
that changes is whether `TS_AUTHKEY_FILE` is set.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `/admin/telegram` shows "unreachable" | No relay routes approved in headscale | Run the headscale approve-routes command printed by `setup.sh` |
| `/admin/telegram` shows "unreachable" with the resolved IPs visible | Those IPs are not in the relay's advertised routes | Run `update-routes.sh` on the relay; check that the new ranges are approved in headscale |
| `tailscale status` in skygate says "Tailscale is stopped" | tailscaled failed to start in the container | `docker logs skygate 2>&1 \| grep -iE 'tailscale\|tailscaled'`; check `/var/log/tailscaled.log` inside the container |
| Relay's `tailscale status` shows routes as advertised but headscale shows them as not enabled | Admin hasn't approved them in headscale | Run the approve-routes command |
| Probe banner says "reachable (direct internet)" on an RF VPS | skygate is going through eth0, not the relay. Tailscale's accepted routes don't include the resolved IPs. | Confirm `tailscale status` in skygate shows the routes; confirm the resolved IPs are in the relay's advertised routes |
| Bot polling works but with high latency | The relay is slow / far | Either accept the latency or pick a closer relay |

## See also

* `Dockerfile` — installs tailscale + tailscaled in the skygate image
* `entrypoint.sh` — starts tailscaled, runs `tailscale up --accept-routes`
* `docker-compose.yml` — wires up the docker secret + tun device + caps
* `internal/handlers/handlers_telegram_probe.go` — the probe implementation
* `Makefile` — has a `tailscale-update-telegram-routes` target
