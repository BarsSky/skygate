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
