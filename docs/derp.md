# DERP relay

> **Status**: optional relay node, deploy-time toggled. 2026-07-15,
> Этап 14 v13 (docs) / Этап 14 v11 (deploy integration).

## What is DERP?

DERP (Designated Encrypted Relay for Packets) is a Tailscale
protocol relay — a server that helps Tailscale clients traverse
NAT/firewalls by relaying encrypted traffic when a direct
peer-to-peer connection isn't possible. Headscale can be
configured with a custom DERP map (the list of relays clients
should try) so the tailnet can keep working when the public
Tailscale DERP relay (`controlplane.tailscale.com`) is
unreachable.

Skygate can either run a DERP relay for you or point at one
you already operate. Both modes are deploy-time toggles;
there is no web-UI management yet (that's on the
`skygate-as-shell` roadmap, v0.11.0+).

## Two modes

| Mode | When | What deploy.sh does |
|------|------|---------------------|
| **Bundled DERP** | `DERP_ENABLED=true`, `DERP_EXTERNAL_URLS` empty | brings up a `derper` container in the headscale docker-compose, generates `derpmap.json`, and writes its hostname into the rendered headscale config |
| **Existing DERP** | `DERP_EXTERNAL_URLS=...` (one or more URLs) | skips the `derper` container; appends the URLs to the headscale `derp.urls` list (alongside the default Tailscale DERP) |
| **No custom DERP** | `DERP_ENABLED=false`, `DERP_EXTERNAL_URLS` empty | nothing custom — headscale uses only the public Tailscale DERP relays listed in `HEADSCALE_DERP_URLS` |

The default in `.env.example` is `DERP_ENABLED=false` and
`DERP_EXTERNAL_URLS` empty, which means "no custom DERP —
clients use the public Tailscale DERP". This is the right
choice for most installs.

## Bundled DERP

Set the following in `.env`:

```ini
DERP_ENABLED=true
DERP_HOSTNAME=derp.example.com   # public hostname clients dial
DERP_PRIVATE_KEY=<openssl rand -hex 32>
DERP_VERIFY_CLIENTS_URL=https://head.example.com  # optional
DERP_STUN_PORT=3478
DERP_HTTP_PORT=8443
DERP_MAP_PORT=8765
```

`deploy/deploy.sh` then:

1. Renders `derper-compose.yml.tmpl` to
   `${DEPLOY_HEADSCALE_DIR}/derper-compose.yml` and starts the
   `derper` container.
2. Generates `${DEPLOY_HEADSCALE_DIR}/derpmap.json` (a
   Tailscale-compatible DERP map with one custom region
   `900: Skygate DERP`).
3. Generates `${DEPLOY_HEADSCALE_DIR}/derper.conf` with the
   private key (the format Tailscale's `derper` expects).
4. Re-renders the headscale config with the bundled DERP map
   URL `https://${DERP_HOSTNAME}/derpmap.json` appended to
   `derp.urls`.

`deploy/backup.sh` saves the derper config + DERP map +
derper docker image. The data is purely the private key +
map JSON — no DERP state, since `derper` is stateless.

## Use an existing DERP relay (2026-07-15, v0.10.12)

If you already run one or more Tailscale `derper` instances
(e.g. on a separate VM, on a public relay you pay for), point
Skygate at them instead of starting a new one. One env var
controls the whole mode:

```ini
DERP_ENABLED=false              # don't start the bundled derper
DERP_EXTERNAL_URLS=https://derp1.example.com,https://derp2.example.com
```

When `DERP_EXTERNAL_URLS` is set, `deploy/deploy.sh`:

- Skips the `derper` service block entirely (no container, no
  derpmap.json generation, no private-key handling).
- Renders the headscale config with each URL appended to
  `derp.urls`, alongside the public Tailscale DERP relay.

The format is the same as Tailscale's own `derp.urls` setting
in headscale config — a comma-separated list of HTTPS URLs
that serve a Tailscale-compatible derpmap.json. Each URL must
be reachable from every tailnet client; if any of them is down
or unreachable, the clients just fall through to the next
relay in the list.

`deploy/backup.sh` skips the DERP artifacts (no `derper.conf`,
no `derpmap.json`, no derper image) when
`DERP_EXTERNAL_URLS` is set. The DERP URLs themselves live in
`.env` (also in the backup), so a restore on another host
re-renders the same headscale config with the same relays.

## Use both bundled and existing

The two modes are not mutually exclusive. You can run the
bundled DERP AND point at additional external ones — set
`DERP_ENABLED=true` AND `DERP_EXTERNAL_URLS=...`. The headscale
config will then have the bundled DERP map URL plus the
external URLs in `derp.urls`. Clients try them in order; the
first one to respond wins.

## Verifying

`/admin/derp` (added in Этап 14 v5) shows the live health of
each DERP region — the bundled one (region `900`) plus any
external ones if they're reachable. The probe runs every page
load and reports:

- **Online** — the derper responded to the debug endpoint
  (`/debug/`), and at least one peer is connected.
- **Reachable** — the derper responded, but no peers are
  connected yet (just brought up).
- **Unreachable** — the probe timed out after 5s. Either the
  hostname is wrong, the firewall is blocking port 443, or the
  derper process is down.

The admin page is read-only — clicking a region opens a
sub-page with the full derper `/debug/vars` JSON for that
node. There is no "configure DERP" UI in v0.10.12; the
configuration is deploy-time only.

## Web UI management (roadmap)

Editing the DERP list from the web UI is on the
`skygate-as-shell` roadmap (v0.11.0+). The intended UX:

- `/admin/derp/config` — form for adding/removing external DERP
  URLs. Writes to `global_settings` like backup-config-ui
  (v0.10.6). Reapplies headscale config on save.
- `/admin/derp/add-bundled` — toggle to enable/disable the
  bundled derper; the deploy script picks up the change on the
  next `./deploy/deploy.sh` run.
- "Test connection" button per URL — runs the same 5s probe
  from the admin page, no need to wait for clients to report.

Until that ships, edit `.env` and re-run `./deploy/deploy.sh`.

## See also

- `docs/headplane.md` — the same "use existing / bundled" pattern,
  for the operator UI sidecar.
- `docs/skygate-as-shell.md` — the wider roadmap for moving
  deploy-time config into a web UI.
- `docs/telegram-relay.md` — the use case: Tailscale clients
  reaching `api.telegram.org` through a custom DERP.
