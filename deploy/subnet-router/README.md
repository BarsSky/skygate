# Skygate subnet-router bundle

This bundle is what the skygate admin's
`/admin/users/<your-id>/subnet → Download bundle` button
generates. It contains everything you need to set up a
subnet-router on a Linux host that's always on in your
local network.

## What's in the bundle

- `setup.sh` — the one-shot script that does
  `tailscale up` with the right flags.
- `README.md` (this file) — the quick start.
- `commands.txt` — the exact `tailscale up` command you
  should run, with your preauth key and CIDR filled in.
  You can run this file directly:
  `bash commands.txt` (or paste the contents into a
  terminal).
- `CIDR.txt` — just your per-user CIDR, in case you want
  to script around it.

## Quick start

1. **Copy the bundle to your router host**. From your
   laptop:
   ```bash
   scp skygate-subnet-router-bundle.tar.gz \
     <user>@<router-host>:/tmp/
   ssh <user>@<router-host>
   cd /tmp && tar xzf skygate-subnet-router-bundle.tar.gz
   cd skygate-subnet-router-bundle
   ```

2. **Read `commands.txt`**. It should look like:
   ```
   #!/bin/bash
   # Skygate subnet-router setup for <username>
   # CIDR: 10.0.<uid>.0/24
   # Preauth key expires: 2026-07-21T20:00:00Z
   sudo tailscale up \
     --accept-routes \
     --netfiltermode=off \
     --login-server=https://head.skynas.ru \
     --hostname=skygate-subnet-<username> \
     --advertise-routes=10.0.<uid>.0/24 \
     --authkey=tskey-auth-aBcDeF
   ```
   Verify the values look right (correct username, correct
   CIDR, preauth key matches what the admin gave you).
   The preauth key is **single-use, 1h TTL** — if you wait
   too long, ask the admin to re-issue.

3. **Run the commands**:
   ```bash
   sudo bash commands.txt
   ```
   This is equivalent to running `setup.sh` with the
   right env vars; either path works. `setup.sh` has
   extra sanity checks (tailscale CLI present, tailscaled
   running), so prefer it if you're not sure.

4. **Wait ~30 seconds** for skygate to auto-approve the
   route, then verify from any tailnet client:
   ```bash
   ping skygate-subnet-<username>
   ping 10.0.<uid>.1
   ```

## What does this give me?

After the subnet-router is up:

- `ping skygate-subnet-<username>` works from any
  tailnet member.
- `ping 10.0.<uid>.X` works for any device on your LAN
  behind the subnet-router.
- MagicDNS resolves `skygate-subnet-<username>` to the
  subnet-router's Tailscale IP (`100.64.Y.Z`).
- The subnet's status flips from `pending` to
  `router_active` on `/admin/users/<id>/subnet`.

You do **not** get a new IP on your other devices — every
Tailscale client still has its `100.64.Y.Z` Tailscale IP.
The `10.0.<uid>.0/24` is a separate space for devices on
your LAN that **don't** run Tailscale (NAS, printer, IoT).

## If something goes wrong

- **`tailscale: command not found`** — install Tailscale
  first: `curl -fsSL https://tailscale.com/install.sh | sh`
- **`authkey expired or already used`** — preauth keys
  are 1h TTL. Ask the admin to issue a new one.
- **`ping` doesn't reach 10.0.<uid>.X** after 60s — IP
  forwarding isn't enabled. Run on the router host:
  `sudo sysctl -w net.ipv4.ip_forward=1`
- **The skygate status pill stays `pending`** — the
  auto-approver runs every 30s. If it hasn't fired after
  2 minutes, the admin can check:
  ```bash
  docker logs skygate --since 2m | grep -E 'sidecar.*<username>'
  ```

The full troubleshooting guide is in the upstream
`docs/subnet-router.md` of the skygate repo.

## Security notes

- The preauth key is in `commands.txt` in plain text.
  Delete the bundle after use:
  `rm -rf /tmp/skygate-subnet-router-bundle/`
- The subnet-router sees your LAN traffic in cleartext.
  Don't run it on a host that has access to networks you
  don't want to expose.
- The subnet-router is a single point of failure. If
  it goes down, your LAN becomes unreachable from the
  tailnet. Run it on a stable host.
