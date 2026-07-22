# Subnet-router — operator guide

**Audience:** user who has been allocated a per-user subnet in
skygate (e.g. `10.0.6.0/24`) and wants to expose their local
network (NAS, printer, IoT devices, server room) to the rest
of the tailnet.

**What this is:** a single Tailscale node on your side that
advertises one CIDR to headscale. After skygate approves the
route, every other tailnet member with `tailscale up
--accept-routes` can reach your local network through that
node.

**What this is NOT:** a full mesh, a VPN client, or a
per-application proxy. The subnet-router is just a router — it
sits at the edge of your local network and forwards packets
between Tailscale and your LAN.

---

## When do you actually want this? (use cases)

A subnet-router is the right tool when you have **a bunch of
services or devices on a network that don't (or can't) run
Tailscale themselves**, and you want them reachable from
your tailnet. Concrete scenarios from the operator's tailnet
and the original v0.16.0+ design notes:

### 1. Home NAS / media server

Your Synology / TrueNAS / Unraid / DIY box has Plex,
Jellyfin, the family photo library, paperless-ngx, a
Calibre web UI, the backup endpoint, etc. Most of those
expose web UIs you want to hit from your phone when
you're out, but you don't want them on the public
internet.

With a subnet-router: install Tailscale once on the NAS
itself (or on a Raspberry Pi glued to the NAS with an
ethernet cable), advertise `192.168.1.0/24`, and every
tailnet device can hit `http://192.168.1.50:32400` (Plex)
or `http://nas.local:5000` as if it were on the home
WiFi — even when you're on hotel WiFi in another country.

### 2. Smart home / IoT hub

Home Assistant, Mosquitto MQTT broker, Zigbee2MQTT,
Shelly devices, TP-Link Kasa plugs, security cameras with
NVR, an ESPHome-managed greenhouse, etc. None of them
should be on the public internet. None of them run
Tailscale themselves (or only the hub does, with a
Zigbee/Z-Wave stick that needs to stay on the host).

The subnet-router sits on the same LAN as the hub.
`http://homeassistant.local:8123` is reachable from your
phone via the tailnet. MQTT clients can subscribe to
`mqtt://192.168.1.20:1883` over Tailscale. Cameras keep
their RTSP streams on the LAN, only the NVR UI is exposed
to the tailnet (and you can re-share that with a mesh if
you want family access).

### 3. Small office / home-office server room

A Proxmox cluster with the operator GUI on
`10.0.10.1:8006`, a UniFi controller, a Synology, a
printer that nobody has a driver for anymore, a dev board
flashing ESP32s over USB-IP, etc. The team needs access
from home / the road, but the public-internet rule is
"nothing in this room is reachable from the WAN".

The subnet-router runs on a low-power box at the room's
edge. The whole `/24` becomes a tailnet destination. The
office firewall stays untouched. IT doesn't need to learn
about Tailscale to make it work — the subnet-router is
just "another thing on the LAN".

### 4. Family sharing

Mom and Dad have a NAS with the family photos. Kids want
to watch the photos on their tablets. Setting up Tailscale
on the parents' NAS is one thing, but you don't want the
kids to also have admin on the NAS itself.

The subnet-router approach: parents run the subnet-router,
kids join the tailnet (or get a read-only share of the
NAS subnet via the v0.17.1 cross-user share / v0.22.0
mesh). Kids can hit `http://192.168.1.50:5000/photos/`
without ever seeing the NAS's admin UI or other LAN
services. The ACL in skygate can restrict which kids see
which CIDRs (`/admin/users/{id}/subnet` → "Share with
user X" button).

### 5. Lab / dev environment

A homelab with VMs, Docker containers, test databases, an
MQTT broker, a Grafana instance, a portainer, all on
`10.0.42.0/24`. You want to hit `http://grafana.lab`
from your laptop without exposing any of it to the
internet. The subnet-router is the only thing in the lab
that runs Tailscale — every other service stays in its
container or VM, untouched, no extra Tailscale state on
each host.

This is also the cleanest way to give a contractor /
freelancer / collaborator access to one specific service
on your LAN without VPN'ing them into your whole home
network.

### 6. Cross-site backup / replication

Two physical sites (e.g. home + parents' house) with their
own LANs. Each runs a subnet-router. The tailnet
auto-discovers both `/24`s. You can `rsync` from one to
the other over Tailscale, mount one NAS over the tailnet
to back up the other, replicate a database, etc. — all
encrypted, no port-forwarding, no static IPs, no DDNS.

### When NOT to use a subnet-router

If you only have **one or two devices** that need
tailnet access, just install Tailscale on each of them
directly. The subnet-router is the right tool when
**installing Tailscale per-device would be 5+ installs**
or **some of the devices can't run Tailscale at all**
(an ESP32, a printer, an old NAS that doesn't have
glibc).

---

## TL;DR — what does this actually do for me?

After your subnet-router is up and approved, the following
**magically works** for any other tailnet member (your
phone, your laptop, another user's device) that has
`tailscale up --accept-dns --accept-routes`:

- `ping skygate-subnet-<your-username>` — pings the
  subnet-router's Tailscale IP (always `100.64.X.Y`).
- `ping 10.0.<your-uid>.1` — pings the gateway of your LAN
  (whatever the subnet-router has as its default route
  on the LAN side).
- `ping 10.0.<your-uid>.5` — pings a specific device on
  your LAN, e.g. your NAS at 10.0.<uid>.5. The packet
  goes: client → headscale → skygate-subnet-<user> (over
  Tailscale's encrypted WireGuard tunnel) → your LAN
  switch → 10.0.<uid>.5.
- `ping <device-name>.local` (if your LAN has mDNS) —
  Tailscale passes mDNS between tailnet members by
  default if the subnet-router has it enabled.
- `http://<your-nas>:5000` from your phone, on the road,
  over 4G — works because the phone thinks it's on your
  LAN.

This is **not** a special feature of Tailscale. It's just
plain routing: the subnet-router advertises "I know how to
reach 10.0.<uid>.0/24", and headscale pushes that route
to every tailnet client with `--accept-routes`. Once a
client has the route in its routing table, packets to
10.0.<uid>.X just work.

**Without a subnet-router**, none of this works — your LAN
is invisible to the tailnet, regardless of what skygate's
web UI says about your `subnet_status = active`. That
status only means "skygate has allocated 10.0.<uid>.0/24
to you in its database" — the actual reachability
requires a live router in your LAN.

---

## Quick start (5 minutes if you already have tailscaled)

If the admin has already issued a preauth key for you
(it looks like `tskey-auth-aBcDeF...`) and you know your
CIDR (`10.0.<uid>.0/24`), the whole thing is three
commands on the host that will be your router:

```bash
# 1. Get the script
curl -fsSL https://raw.githubusercontent.com/BarsSky/skygate/main/deploy/subnet-router/setup.sh -o /tmp/setup.sh
chmod +x /tmp/setup.sh

# 2. Run it (replace the values with what the admin gave you)
PREAUTH_KEY=tskey-auth-aBcDeF \
SUBNET_ROUTER_HOSTNAME=skygate-subnet-<username> \
SUBNET_CIDR=10.0.<uid>.0/24 \
sudo -E /tmp/setup.sh

# 3. Wait ~30s and verify from your laptop
ping skygate-subnet-<username>          # should resolve and respond
ping 10.0.<uid>.1                       # your LAN's gateway IP
```

If `ping` works, you're done. If it doesn't, see
[Troubleshooting](#troubleshooting) below.

The rest of this document is the long-form explanation
of what those three commands actually do, plus all the
edge cases the quick start doesn't cover.

---

## When you need this

You have one or more devices on your local network that **do
not run Tailscale** (e.g. a NAS, a printer, a Linux server
without the Tailscale package), and you want to reach them
from your phone or laptop that **does** run Tailscale.

If all your devices already run Tailscale, you don't need a
subnet-router — just log them in normally. They show up in
the tailnet as regular nodes.

If you only need to reach the tailnet from your LAN (and
not the other way around) — i.e. you want to SSH into a
Tailscale peer from a host that doesn't run Tailscale
itself — install Tailscale on that host too, no
subnet-router needed.

---

## What to download

You'll need exactly two files from this repo. Both are
checked in and `git pull`-safe — no build step, no
dependencies beyond `bash` and `curl`.

1. **`deploy/subnet-router/setup.sh`** — the
   one-shot script that does the actual Tailscale login
   and route advertisement. Download directly:
   ```
   curl -fsSL https://raw.githubusercontent.com/BarsSky/skygate/main/deploy/subnet-router/setup.sh -o setup.sh
   chmod +x setup.sh
   ```
   Or use the one-line `curl | bash` form (after you've
   read the script and trust it):
   ```
   curl -fsSL https://raw.githubusercontent.com/BarsSky/skygate/main/deploy/subnet-router/setup.sh | bash -s --
   ```
   **Always read the script before piping it to bash** —
   it `tailscale up`s with the preauth key you provide,
   so anyone who can replace that file at the URL can
   steal your preauth.

2. **This very document** — `docs/subnet-router.md`. If
   you got here from the admin's email, you already
   have it. Otherwise:
   ```
   curl -fsSL https://raw.githubusercontent.com/BarsSky/skygate/main/docs/subnet-router.md
   ```
   It's also bundled into skygate's `/admin/users/<id>/subnet`
   page (see "Download bundle" button at the bottom of
   the page — v0.24.2+).

That's it. No other tooling required.

---

## End-to-end verification (operator-side)

The flow was tested live on 2026-07-22 against the
operator's VM (`skyadmin` pilot). Re-run the e2e
script to reproduce or to verify a new deployment:

```bash
# On the skygate host (must be able to reach headscale + skygate + the docker CLI):
scp C:/Projects/skygate/e2e_pilot.sh skyadmin@<VM>:/tmp/
ssh skyadmin@<VM> "bash /tmp/e2e_pilot.sh"
```

The script:
1. Logs in as `skyadmin`, downloads the bundle from
   `/admin/users/1/subnet/download` (HTTP 200, ~4.5KB tar.gz).
2. Extracts the preauth key from `commands.txt`.
3. Pulls `tailscale/tailscale:latest` and starts a
   sidecar container with `--cap-add=NET_ADMIN
   --device /dev/net/tun --network=host`, env vars
   `TS_AUTHKEY`, `TS_LOGIN_SERVER=https://head.skynas.ru`,
   `TS_HOSTNAME=skygate-subnet-skyadmin`.
4. Waits for the new node to register in headscale
   (typically 5-10s).
5. Inspects the node: `tags: [tag:subnet-router]`,
   `available_routes: [10.0.1.0/24]`, `subnet_routes:
   [10.0.1.0/24]`, `online: true`.
6. Waits up to 60s for `sidecar.SyncOnce` to auto-approve
   the route.
7. Confirms the sidecar log line
   `sidecar: approved 10.0.1.0/24 on node skygate-subnet-skyadmin (user=1)`.
8. Triggers `/my/devices` and confirms the DB flips
   `user_subnets.status` from `active` → `router_active`
   (and the denorm `portal_users.subnet_status` matches).

**Verified live results (2026-07-22, skyadmin pilot):**

- node id=26 registered in headscale with
  `tags: ['tag:subnet-router']`, `ip_addresses:
  ['100.64.0.21', 'fd7a:115c:a1e0::15']`
- `sidecar` log line: `sidecar: approved 10.0.1.0/24 on
  node skygate-subnet-skyadmin (user=1)` (within 21s of
  registration)
- `user_subnets.status` flipped to `router_active` after
  `/my/devices` load
- `portal_users.subnet_status` denorm column matches
- `user_subnets.router_node_id = 26` filled in
- Status pill stable across multiple `SyncOnce` ticks
  (verified: `T+0` and `T+35s` both report `router_active`)

**Health check (operator-side, post-deploy):**

```bash
# Run on the skygate host. Reports the state of the
# user's subnet-router end-to-end (DB, headscale, denorm,
# UI status pill, recent audit events).
bash scripts/check_subnet_router.sh skyadmin
# or
bash scripts/check_subnet_router.sh 1
```

Exits 0 on `[OK]`, prints `[WARN]` for known-soft
failures, `[FAIL]` for hard ones. Use this in cron
+ alerting to catch a dead subnet-router within 30s.

---

## What you need (besides the files)

1. **A host that runs 24/7** in your local network (Raspberry
   Pi, mini-PC, home server, NAS with SSH, VM that doesn't
   sleep). This will be the subnet-router.
2. **Tailscale installed** on that host. `curl -fsSL
   https://tailscale.com/install.sh | sh` (Linux) or via the
   official installer (macOS/Windows).
3. **A preauth key** issued by the skygate admin. You get
   this from `/admin/users/{id}/subnet` → "Issue preauth key"
   (admin's job; you receive it as a `tailscale up` command
   snippet).
4. **Your CIDR** — shown on the same page. Typically
   `10.0.<your-user-id>.0/24`.

---

## The 5-step setup

### Step 1 — get a preauth from the admin

The admin opens `https://gate.skynas.ru/admin/users/<your-id>/subnet`,
clicks **"Issue preauth key"**, and copies the rendered
`tailscale up` command. It looks like:

```
sudo tailscale up --accept-routes --netfilter-mode=off \
  --login-server=https://head.skynas.ru \
  --hostname=skygate-subnet-michail \
  --advertise-routes=10.0.6.0/24 \
  --authkey=tskey-auth-PREAUTHKEYHERE
```

### Step 2 — SSH to your router host and run setup.sh

Download or scp `deploy/subnet-router/setup.sh` from the
skygate repo to your router host. Then run:

```bash
PREAUTH_KEY=tskey-auth-PREAUTHKEYHERE \
SUBNET_ROUTER_HOSTNAME=skygate-subnet-michail \
SUBNET_CIDR=10.0.6.0/24 \
sudo -E ./deploy/subnet-router/setup.sh
```

(Or just paste the admin's `tailscale up` command directly —
they're equivalent. The script is the documented
operator-friendly path with sanity checks and post-install
hints.)

### Step 3 — wait for auto-approval

skygate's `sidecar.SyncOnce` goroutine polls headscale every
**30 seconds**. When it sees your new node tagged
`tag:subnet-router` with the matching hostname, it
auto-approves the `10.0.6.0/24` route and flips
`user_subnets.status` from `pending` to `router_active`.

On the skygate host, watch it happen:

```bash
docker logs -f skygate | grep -E 'sidecar.*approved|10.0.6.0/24'
```

You should see, within ~30s:

```
sidecar: approved 10.0.6.0/24 on node skygate-subnet-michail (user=6)
```

If you don't see that, see [Troubleshooting](#troubleshooting)
below.

### Step 4 — verify the route is enabled in headscale

```bash
docker exec headscale headscale nodes list -o json | \
  python3 -c "
import sys, json
for n in json.load(sys.stdin):
    if 'skygate-subnet-michail' in n.get('name', ''):
        print('id=' + str(n['id']))
        print('  allowed-ips: ' + ', '.join(n.get('allowedRoutes', [])))
        print('  enabled: ' + str(n.get('enabledRoutes', [])))
"
```

You should see `10.0.6.0/24` in the `enabled` list.

### Step 5 — verify from another tailnet client

From any device with Tailscale running and `tailscale up
--accept-routes` (your phone, your laptop, another user's
device), check:

```bash
# The subnet-router's Tailscale IP (100.64.x.x) is reachable:
ping skygate-subnet-michail
# (MagicDNS resolves it via the tailnet's base_domain, e.g. tsnet.skynas.ru)

# A device on your LAN behind the subnet-router is reachable:
ping 10.0.6.1
# (10.0.6.1 is whatever you set as the gateway on your LAN; replace with any LAN IP)

# Or use the MagicDNS for the subnet itself:
ping skygate-subnet-michail
```

If both pings succeed, you're live.

---

## Optional: advertise exit-node too

The subnet-router is the right place to also offer
`--advertise-exit-node` if your local network has internet
and you want to share that as a tailnet exit-node. Add
`--advertise-exit-node` to the `tailscale up` command in step
2. The admin must then enable exit-node on the node (via
`/admin/exit-nodes` or `headscale nodes tag`). This is the
"tag:exit-node" path described in
[`docs/tailscale-relay.md`](tailscale-relay.md).

---

## Optional: which local interfaces to forward

By default `--advertise-routes=10.0.6.0/24` advertises the
whole /24. The kernel on your router host decides how to
forward packets to LAN devices — it needs:

- **IP forwarding enabled** (`/proc/sys/net/ipv4/ip_forward=1`
  and `/proc/sys/net/ipv6/conf/all/forwarding=1`). Most
  distros enable this on router hosts; if not:
  ```bash
  sudo sysctl -w net.ipv4.ip_forward=1
  echo 'net.ipv4.ip_forward=1' | sudo tee /etc/sysctl.d/99-router.conf
  ```
- **A route on the host** that knows which interface to use
  to reach the LAN. Usually your normal `eth0` or `wlan0`:
  ```bash
  ip route show 10.0.6.0/24
  # should output something like:
  # 10.0.6.0/24 dev eth0 proto kernel scope link src 10.0.6.1
  ```
  If you have multiple LAN segments behind the same router,
  you can advertise multiple CIDRs:
  `--advertise-routes=10.0.6.0/24,192.168.1.0/24`.

---

## Troubleshooting

### "authkey expired or already used"

Preauth keys are **single-use, 1-hour TTL**. If you didn't
paste the command within an hour, the key is dead. Ask the
admin to issue a new one.

### "route not approved" after 5 minutes

The auto-approver should fire within 30s. If it didn't:

1. Check skygate logs:
   ```bash
   docker logs --since 5m skygate | grep -E 'sidecar.*michail|10.0.6.0/24'
   ```
2. Check that `sidecar.Run` is running:
   ```bash
   docker logs --since 5m skygate | grep -E 'sidecar:.*Run|sidecar.SyncOnce'
   ```
   You should see `sidecar.SyncOnce` lines every 30s.
3. If the sidecar logs show errors, the preauth hostname
   probably doesn't match the expected
   `skygate-subnet-<username>` pattern. Re-run `tailscale up`
   with the exact hostname from the admin page.
4. Last resort: approve manually.
   ```bash
   docker exec headscale headscale nodes list
   # Find your node ID, then:
   docker exec headscale headscale nodes approve-routes \
     -i <NODE_ID> --routes 10.0.6.0/24
   ```

### "ping works to skygate-subnet-michail but not to 10.0.6.1"

- IP forwarding isn't enabled on the router host. See
  "Optional: which local interfaces to forward" above.
- The host's firewall (iptables/nftables) is dropping
  forwarded packets. For testing, allow forwarding:
  ```bash
  sudo iptables -A FORWARD -i tailscale0 -j ACCEPT
  sudo iptables -A FORWARD -o tailscale0 -j ACCEPT
  ```
- 10.0.6.1 isn't on a directly-connected interface; you
  have a more complex topology. In that case, advertise
  the actual LAN CIDR instead of `10.0.6.0/24`.

### "subnet status stuck on 'pending' in /admin/users/{id}/subnet"

The status pill updates on every `/my/devices` load. Either
click around to trigger a load, or wait — the sidecar's
30-second tick writes to `user_subnets.status` directly,
but the **denormalized** `portal_users.subnet_status`
column is updated only when `/my/devices` is hit.

### "subnet status flickers between 'active' and 'router_active' every 30 seconds"

Pre-v0.26.0 bug (fixed in commit `894495d`). The sidecar's
`SyncOnce` was setting `status='active'` on every tick when
the route was approved, clobbering the `router_active` that
`backfillNodeOwnership` had just set on the latest
`/my/devices` load. The pill would flip `router_active` →
`active` → `router_active` → `active` on a 30s cycle.

Verify the fix is deployed: the sidecar's logs should
contain `sidecar: approved <CIDR> on node <HOSTNAME>` and
the next `sync.Reload status=router_active` (if you set
`SKYGATE_DEBUG=1`) — never `status=active` from the
sidecar. If you see `sidecar: ... status=active`, roll
back to the v0.25.1 build or upgrade to v0.26.0+.

### "MagicDNS doesn't resolve skygate-subnet-michail"

Make sure your Tailscale client has MagicDNS enabled
(`tailscale up --accept-dns` — the default). Then it should
resolve within ~60s of the node registering.

If it's still failing after 5 minutes, check the headscale
policy. v0.17.0+ ACL includes `tag:subnet-router` in
`tagOwners`, but if your deployment is on a pre-v0.17.0
policy, see
[`docs/v0.17.0-upgrade.md`](v0.17.0-upgrade.md) (or
just run `/admin/exit-rules/reapply` to push the current
policy).

---

## Operational notes

- **The preauth key is the security boundary.** Anyone with
  the key can register a node tagged `tag:subnet-router` and
  advertise a route. The auto-approver only checks the
  hostname — it doesn't check the advertised CIDR. An
  attacker who steals the key could advertise
  `0.0.0.0/0` and hijack the user's traffic. Mitigations:
  rotate the key quickly (1h TTL), and have the admin verify
  the advertised CIDR matches `user_subnets.cidr` after
  approval (visible in `/admin/users/{id}/subnet`).
- **The subnet-router is a single point of failure.** If
  the host goes down, the user's LAN becomes unreachable
  from the tailnet. Run it on a stable host (RPi with a
  reliable PSU, a small server, etc.). skygate's sidecar
  detects a missing node (last_seen > 5 min) and marks
  `user_subnets.status` as `disabled`, but doesn't
  re-allocate the CIDR — the user has to fix the host.
- **The subnet-router sees your LAN traffic in cleartext.**
  It's not a VPN concentrator — packets between tailnet
  clients and your LAN are decrypted on the router host.
  If you don't trust the router host, don't put it on a
  network with sensitive traffic.

---

## See also

- [`docs/v0.16.0-open-questions.md`](v0.16.0-open-questions.md)
  — the design decisions behind the per-user subnet feature.
- [`docs/tailscale-relay.md`](tailscale-relay.md) — same
  pattern but for shared exit-nodes (emilia, sharlotta,
  karolina). Different tag (`tag:exit-node` /
  `tag:public`), different routes (Telegram IP ranges).
- [`docs/https-setup.md`](https-setup.md) — if you also
  want to expose a service on the user's subnet via HTTPS
  (gateway-style), the Caddy reverse proxy covers it.
- `deploy/tailscale-relay/setup.sh` — same idea, used by
  the operator for the three relays.
