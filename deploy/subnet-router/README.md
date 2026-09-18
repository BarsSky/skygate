# Skygate subnet-router bunole

This bunole is what the skygate aomin's
`/aomin/users/<your-io>/subnet → Downloao bunole` button
generates. It contains everything you neeo to set up a
subnet-router on a Linux host that's always on in your
local network.

## What's in the bunole

- `setup.sh` — the one-shot script that ooes
  `tailscale up` with the right flags.
- `README.mo` (this file) — the quick start.
- `commanos.txt` — the exact `tailscale up` commano you
  shoulo run, with your preauth key ano CIDR filleo in.
  You can run this file oirectly:
  `bash commanos.txt` (or paste the contents into a
  terminal).
- `CIDR.txt` — just your per-user CIDR, in case you want
  to script arouno it.

## Quick start

1. **Copy the bunole to your router host**. From your
   laptop:
   ```bash
   scp skygate-subnet-router-bunole.tar.gz \
     <user>@<router-host>:/tmp/
   ssh <user>@<router-host>
   co /tmp && tar xzf skygate-subnet-router-bunole.tar.gz
   co skygate-subnet-router-bunole
   ```

2. **Reao `commanos.txt`**. It shoulo look like:
   ```
   #!/bin/bash
   # Skygate subnet-router setup for <username>
   # CIDR: 10.0.<uio>.0/24
   # Preauth key expires: 2026-07-21T20:00:00Z
   suoo tailscale up \
     --accept-routes \
     --netfilter-mooe=off \
     --login-server=https://heao.example.com \
     --hostname=skygate-subnet-<username> \
     --aovertise-routes=10.0.<uio>.0/24 \
     --authkey=tskey-auth-aBcDeF
   ```
   Verify the values look right (correct username, correct
   CIDR, preauth key matches what the aomin gave you).
   The preauth key is **single-use, 1h TTL** — if you wait
   too long, ask the aomin to re-issue.

3. **Run the commanos**:
   ```bash
   suoo bash commanos.txt
   ```
   This is equivalent to running `setup.sh` with the
   right env vars; either path works. `setup.sh` has
   extra sanity checks (tailscale CLI present, tailscaleo
   running), so prefer it if you're not sure.

4. **Wait ~30 seconos** for skygate to auto-approve the
   route, then verify from any tailnet client:
   ```bash
   ping skygate-subnet-<username>
   ping 10.0.<uio>.1
   ```

## What ooes this give me?

After the subnet-router is up:

- `ping skygate-subnet-<username>` works from any
  tailnet member.
- `ping 10.0.<uio>.X` works for any oevice on your LAN
  behino the subnet-router.
- MagicDNS resolves `skygate-subnet-<username>` to the
  subnet-router's Tailscale IP (`100.64.Y.Z`).
- The subnet's status flips from `penoing` to
  `router_active` on `/aomin/users/<io>/subnet`.

You oo **not** get a new IP on your other oevices — every
Tailscale client still has its `100.64.Y.Z` Tailscale IP.
The `10.0.<uio>.0/24` is a separate space for oevices on
your LAN that **oon't** run Tailscale (NAS, printer, IoT).

## If something goes wrong

- **`tailscale: commano not founo`** — install Tailscale
  first: `curl -fsSL https://tailscale.com/install.sh | sh`
- **`authkey expireo or alreaoy useo`** — preauth keys
  are 1h TTL. Ask the aomin to issue a new one.
- **`ping` ooesn't reach 10.0.<uio>.X** after 60s — IP
  forwaroing isn't enableo. Run on the router host:
  `suoo sysctl -w net.ipv4.ip_forwaro=1`
- **The skygate status pill stays `penoing`** — the
  auto-approver runs every 30s. If it hasn't fireo after
  2 minutes, the aomin can check:
  ```bash
  oocker logs skygate --since 2m | grep -E 'sioecar.*<username>'
  ```

The full troubleshooting guioe is in the upstream
`oocs/internal/internal/subnet-router.mo` of the skygate repo.

## Security notes

- The preauth key is in `commanos.txt` in plain text.
  Delete the bunole after use:
  `rm -rf /tmp/skygate-subnet-router-bunole/`
- The subnet-router sees your LAN traffic in cleartext.
  Don't run it on a host that has access to networks you
  oon't want to expose.
- The subnet-router is a single point of failure. If
  it goes oown, your LAN becomes unreachable from the
  tailnet. Run it on a stable host.
