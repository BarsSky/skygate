# Windows client setup (Tailscale + Skygate)

Reference for a Windows device that needs to:

- register with skygate's headscale control server (not Tailscale's SaaS),
- accept subnet routes advertised by skygate-managed exit nodes (relay-1 / relay-2 / relay-3),
- and let the user pick which exit node routes the traffic.

The current skygate architecture (v0.33+) is:

```
skygate (SQLite / headscale API)        user device (Windows)
  ┌──────────────────┐                     ┌──────────────────┐
  │ portal_users     │  preauth key         │ Tailscale client │
  │ device_rules     │ ──────────────────►  │ tailscale up     │
  │ exit_servers     │                     │ --accept-routes  │
  │ headscale_policy │                     │                  │
  └──────────────────┘                     └──────────────────┘
            │                                      ▲
            │ 1. set advertised-routes              │ 2. receives routes
            ▼    (via SSH to relay + headscale)    │    via headscale ACL
  ┌──────────────────┐                              │
  │ relay-N (Tailscale) ──── advertises routes ─────┘
  │ 0.0.0.0/0 + subnets    via headscale
  └──────────────────┘
```

The **client only runs `tailscale up --accept-routes`**. Skygate is the source of truth for the route table; the client just receives.

---

## Step 0 — Install Tailscale

Download from <https://tailscale.com/download/windows> (or `winget install tailscale.tailscale`). The MSI puts `tailscale.exe` on `%PATH%` and installs the system tray + the `tailscaled` service.

Verify:

```powershell
tailscale version
# Tailscale v1.80+ recommended; older versions may not support
# the auth-key + login-server flags in the same invocation.
```

## Step 1 — Get a preauth key from /my/preauth

In the web UI, go to **/my/preauth** → click **Generate preauth key**. The page returns a `tskey-auth-...` token. It is:

- **single-use** — each device consumes the key on its first `tailscale up`.
- **default 1h TTL** — generate it just before running the command below.
- **reusable per device** — re-run the same form to get a fresh key for a second machine.

Copy the key. The next step embeds it on the command line.

## Step 2 — First-time `tailscale up` (Windows)

Run from **PowerShell** (admin or user — Tailscale runs in user context by default):

```powershell
tailscale up `
  --login-server=https://head.example.com `
  --authkey=tskey-auth-XXXXXXXXXXXXXXXX `
  --accept-routes `
  --accept-dns=false `
  --hostname=my-windows-pc
```

| Flag | What it does | Why |
|---|---|---|
| `--login-server=https://head.example.com` | Points Tailscale at skygate's headscale, not `login.tailscale.com` | Without it, the device registers on the public SaaS and never joins the tailnet. Use the value from `SKYGATE_CONTROL_URL` in `.env`. |
| `--authkey=tskey-auth-...` | The preauth token from Step 1 | Tailscale uses it to register the device without an interactive browser login. **Single use.** |
| `--accept-routes` | Installs subnet routes that exit nodes advertise to headscale | This is the whole point: the client receives whitelisted CIDRs (Telegram, YouTube, Google, ...) and routes them through the chosen exit node. |
| `--accept-dns=false` | Keeps the system's existing DNS (corporate AD, ISP, etc.) | By default Tailscale replaces `127.0.0.1` DNS with MagicDNS (100.100.100.100). On a corporate laptop this breaks SSO. Set `true` only if you DO want tailnet DNS. |
| `--hostname=my-windows-pc` | Optional — gives the device a stable name in headscale | Without it, Tailscale picks something like `DESKTOP-7FQ3LAP2`. The hostname is what `tailscale status` shows and what skygate's `/my/devices` lists. |

### Common PowerShell pitfall — auth key with quotes

If you paste the command into a `.ps1` file, escape the `$` correctly. The authkey has no `$` so plain `tailscale up --authkey=tskey-auth-...` works without escaping. If you store the key in a variable:

```powershell
$key = "tskey-auth-XXXXXXXXXXXXXXXX"
tailscale up --login-server=https://head.example.com --authkey=$key --accept-routes --accept-dns=false
```

## Step 3 — Verify the device joined the tailnet

```powershell
tailscale status
# 100.64.0.X   my-windows-pc   user@   linux   -   active; direct 192.0.2.1, tx 15820 rx 9532
```

You should see:

1. **`100.64.100.X`** — your Tailscale IP (the `X` matches your device slot).
2. **`active`** — not `offline` or `idle`.
3. **`OS: windows`** — Tailscale detected the platform.

In the web UI, **/my/devices** should now show the device as `tag:private` (skygate auto-tags it on the next `/my/devices` load via `backfillNodeOwnership`).

## Step 4 — Verify exit-node routes are arriving

```powershell
tailscale status --json | ConvertFrom-Json | Select-Object -ExpandProperty Peer
# or simpler:
tailscale netcheck
# or:
tailscale status --peers
```

For a **specific exit node** (relay-N):

```powershell
tailscale status | findstr /R "relay"
# relay-1     100.64.100.3   user@   linux   -   active; offers exit node; ...
# relay-2     100.64.100.4   user@   linux   -   active; offers exit node; ...
# relay-3     100.64.100.2   user@   linux   -   active; offers exit node; ...
```

If a relay does **not** show `offers exit node`, headscale did not approve its `--advertise-exit-node` request yet. Check **/admin/exit-nodes** in the skygate UI.

## Step 5 — Pick an exit node (split-tunnel or full tunnel)

Tailscale handles exit-node selection through the **system tray menu**, not the CLI:

1. Right-click the Tailscale tray icon.
2. Click **Exit node** → choose `relay-1`, `relay-2`, or `relay-3` (or **None** to disable exit-node routing).
3. The Tailscale log shows `Set exit node: relay-1` and your public IP flips to the relay's IP.

### What `--accept-routes` does for split-tunnel

The `--accept-routes` flag (Step 2) installs the **subnet routes that exit nodes advertise to headscale**. These arrive automatically — you do not need to `route add` anything by hand:

- skygate's `/admin/exit-rules/sync` button (or auto-sync from `/my/exit-rules` add/remove) SSHes into the relay and runs `tailscale set --advertise-routes=...` on the relay.
- The relay advertises its full route set to headscale.
- headscale auto-approves (admin has approved the routes in skygate).
- Tailscale on your Windows machine sees the new routes via the netmap push (~5-10s) and installs them in the Windows routing table at metric < your LAN gateway.
- When a destination in the whitelist is accessed, the Windows kernel routes the packet through the chosen exit node.

If you pick **None** (no exit node), the routes are still installed but traffic to whitelisted destinations fails (no path to relay). To get split-tunnel behaviour: pick an exit node in the Tailscale menu.

### What if the Windows route table shows duplicate / lower-metric routes?

Tailscale's split-tunnel works as long as the Tailscale-installed routes have a lower metric than your LAN's `0.0.0.0/0`. If something else (e.g. a corporate VPN) installs a more-specific route, you'll have a routing conflict. Diagnostic:

```powershell
route print -4
# Look for 0.0.0.0/0 routes — Tailscale's should be at metric 50,
# your LAN's at metric 100+. If a corporate VPN is lower, raise
# the Tailscale route's metric OR exclude the corporate VPN
# from the destination set.
```

## Step 6 — `--accept-routes` after a reboot / `tailscale down`

Tailscale persists the auth state across reboots, so the device stays in the tailnet. But if you run `tailscale down` (manual disconnect) or if the `tailscaled` service gets recreated, re-apply the flags:

```powershell
tailscale up --accept-routes --accept-dns=false
```

Note: this is the **short form** (no `--login-server`, no `--authkey`) — Tailscale remembers those from the first registration. The key is consumed once.

## Quick reference

```powershell
# First-time
$key = (Invoke-WebRequest -UseBasicParsing -Uri "https://head.example.com/my/preauth" -SessionVariable ...).Content  # or paste
tailscale up --login-server=https://head.example.com --authkey=$key --accept-routes --accept-dns=false --hostname=my-pc

# Subsequent
tailscale up --accept-routes --accept-dns=false

# Verify
tailscale status
tailscale netcheck
route print -4

# Switch exit node
# (Tailscale tray menu → Exit node → relay-1/2/3)
```

## What this page is NOT

- **Not a script for a relay / exit node.** Exit nodes (relay-1/2/3) use a *different* command — they `--advertise-exit-node` and `--advertise-routes=...`. See `deploy/tailscale-relay/setup.sh` in the repo.
- **Not a subnet-router setup.** A subnet-router advertises LAN routes from a Tailscale node on the LAN. See `docs/subnet-router.md`.
- **Not a Linux setup.** The same flags work on Linux/macOS; the only difference is the install method (`apt install tailscale` vs MSI).

## Related pages

- `/my/preauth` — generate a fresh preauth key.
- `/my/exit-rules` — your rules, the per-device `.cmd`/`.sh` download (legacy, see Step 5 note), and the help card.
- `/my/exit-rules/help` — full API reference (`POST /my/exit-rules/api` for bulk add).
- `/admin/exit-nodes` — the three relays, online status, advertised routes.
- `/admin/exit-rules/sync` — the button that pushes the route set to the relays (admin-only).
- `docs/subnet-router.md` — the sidecar / per-user LAN bridge.
- `docs/tailscale-relay.md` (in `deploy/tailscale-relay/`) — how the relays are set up.
