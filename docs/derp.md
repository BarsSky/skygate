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

## Web UI management

Editing the DERP list from the web UI is **available** as of
v1.3.17 — `/admin/derp/relays` is the per-row management
surface (like `/admin/exit-nodes`): add / edit / delete /
toggle / per-row "Test connection". Backed by the
`derp_relays` PG table. The bundled derper container is
managed via a single row with `is_bundled=1` (toggle its
`enabled` flag to start/stop the container).

The older `/admin/derp/config` form (v0.11.0) is still
served for backward compat — it reads/writes the same
`global_settings.derp.*` keys, and `AutoMigrateDerpRelays`
copies them into the new `derp_relays` table on the first
GET of the new page.

Direct `.env` + `./deploy/deploy.sh` editing is no longer
required for any DERP change.

## B237: skygate-managed derpmap.json endpoint + headscale re-apply (2026-09-04)

As of v1.5.2 (B237), the `derp_relays` table is also
the source of a **skygate-managed derpmap.json** that
headscale fetches and merges with the public Tailscale
derpmap. This means:

- **Single source of truth**: every DERP row in
  `derp_relays` (operator's own + bundled 901) shows
  up in headscale's `derp.urls` automatically once
  the operator clicks "Apply to headscale".
- **No SSH to headscale host** required. The button
  rewrites `/home/admin/headscale/config/config.yaml`
  (configurable via `SKYGATE_HEADSCALE_CONFIG_PATH`)
  and `docker restart headscale` — all from the
  skygate UI.
- **Idempotent**: re-apply with the same skygate URL
  is a no-op (rewriteDerpURLs dedupes).
- **Audit**: every apply writes a `derp_apply_headscale`
  row in the `exit_rule_logs` table with the new
  config snippet + docker restart output.

Endpoint: `GET /admin/derp/relays/derpmap.json`
(returns a Tailscale-shaped JSON with the operator's
own + bundled DERP rows; no authMW because headscale
inside the docker network fetches it from
`http://skygate:8080/...`).

Apply button: `POST /admin/derp/relays/apply-headscale`
on the `/admin/derp/relays` page. The button is
admin-only + CSRF-protected + onsubmit=confirm().

See `docs/internal/2026-09-04-tailnet-fixes.md` for
the B237 root cause (pre-B237 the operator had to
SSH into the headscale host and edit config.yaml by
hand — a `rwxr-x---` anti-pattern).

## B237.2: correct Public IP display on /admin/derp (2026-09-04)

As of v1.5.2 (B237.2), the "Публичный IP" field on
`/admin/derp` shows the **real** public IP the derper
listens on, not the skygate container's docker-bridge
egress IP.

**Mechanism:**
1. `resolvePublicDERPIP()` tries:
   - `SKYGATE_DERP_HOSTNAME` env var (operator's
     hostname override, e.g. `derp.example.com`).
   - The derper status page's parsed "TLS hostname".
   - Last-resort: `detectEgressIP()` (skygate
     container's own egress — usually wrong, but
     better than empty).
2. `net.LookupHost(hostname)` returns the DNS A
   record, which IS the public IP. For
   `derp.example.com` this returns `203.0.113.50`.
3. A new `WhiteIPSource` field records which
   source the resolver used (`dns:env` /
   `dns:derper` / `egress`).
4. The template shows a small annotation
   `(dns:env)` next to the IP, with a tooltip
   explaining the source.

**Configuration:**
```ini
# Set the operator's DERP hostname so the resolver
# uses the correct DNS A record:
SKYGATE_DERP_HOSTNAME=derp.example.com
```

**Pre-B237.2:** `/admin/derp` showed `198.51.100.10`
(skygate container's docker-bridge IP, RFC 5737 example),
which was misleading — the real public IP is the
DNS A record of the operator's `SKYGATE_DERP_HOSTNAME`.

## See also

- `docs/headplane.md` — the same "use existing / bundled" pattern,
  for the operator UI sidecar.
- `docs/skygate-as-shell.md` — the wider roadmap for moving
  deploy-time config into a web UI.
- `docs/internal/internal/telegram-relay.md` — the use case: Tailscale clients
  reaching `api.telegram.org` through a custom DERP.
