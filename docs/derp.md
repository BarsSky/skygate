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

See `docs/troubleshooting.md` for
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

## B289.1: dial the ADDRESS, speak the HOSTNAME (2026-09-23)

If your own relay never appears in the map — `/admin/derp` is green, `/admin/derp/relays`
lists the row, headscale has `http://skygate:8080/admin/derp/relays/derpmap.json` in
`derp.urls`, and yet `tailscale netcheck` never shows your region — read this first.

The map skygate serves is the ONLY thing that tells clients the relay exists. A node is
published only when the reachability guard can complete a TCP+TLS handshake to it, and
that guard used to dial the relay's **hostname**. Inside the skygate container the host's
`/etc/hosts` is inherited, so a host that maps its own public name to `127.0.0.1`
(AGENTS deployment trap #2) makes the relay unreachable *by name* from inside the
container — the guard dropped the node and answered `{"Regions":{}}`.

`SKYGATE_DERP_PROBE_HOST` is the escape hatch: it names an address the container **can**
reach (typically the host's LAN address). It needs no `docker-compose.yml` change — the
variable travels in `.env` and the container reads it at start, so after editing:

```bash
cd /home/<operator>/skygate
docker compose up -d skygate     # recreate (NOT `restart`) so the env is picked up
curl -s http://127.0.0.1:8080/admin/derp/relays/derpmap.json   # must contain "900"
docker restart headscale         # make headscale re-fetch the map
```

> Since v1.5.62 you can skip all of that: the same address can be saved on
> `/admin/derp/relays` and applies to the next probe — see §B296 below.

**The rule that makes the fallback work** (B289.1): the probe must *connect* to the
operator's address while it *speaks* the relay's public hostname. `derper
--certmode=manual` resolves its certificate **by SNI**, and Go's TLS client sends no SNI
at all for an IP literal — so dialling an IP with that IP as `ServerName` fails with
`cert mismatch with hostname: ""` (visible in `docker logs derper`). The same rule now
applies to every probe skygate makes:

| probe | connects to | speaks |
|---|---|---|
| map guard (`/admin/derp/relays/derpmap.json`) | `SKYGATE_DERP_PROBE_HOST`, else the hostname | the relay's hostname |
| `/admin/derp` status (`/debug/*`, `/active-conn`) | the first candidate that accepts TCP | the relay's hostname |
| `derp_health` cron (the dashboard's latency/health) | the same rule, honouring the port in the row's URL | the relay's hostname |

A relay row that cannot be published is reported twice: in the journal
(`derpmap: skipping region=900 … unreachable (tried …)`, and an `ERROR region=900 is
BUNDLED but publishes NO node`) and per row on `/admin/derp/relays` («в карте /
пропущен + причина»). If you see the ERROR, the cause is in the same line — do not
restart things hoping it clears: the two known causes are the loopback leak above and a
dead row (a second bundled row on a port nothing listens on, e.g. a stale `:8443`).

## B296: set the probe address from the panel (2026-09-23)

Since v1.5.62 you do not have to touch `.env` at all. `/admin/derp/relays` has a
**«Адрес проверки релея»** card:

```text
Сейчас используется: 192.0.2.10   [источник: сохранено здесь]
Адрес (IP или имя хоста): [ 192.0.2.10        ]  [Сохранить] [Очистить]
```

Save it and the very next map fetch, STUN probe and `derp_health` tick use it. Nothing
is recreated, nothing is restarted, `docker-compose.yml` is never edited.

### Why this is a separate knob and not just `.env`

The container is created from `env_file: .env`, and Docker freezes that environment at
container **creation**. `docker compose restart` re-runs the entrypoint but keeps the
old environment, so applying an edited value meant `docker compose up -d --force-recreate`
(AGENTS trap #3) from an SSH session — for a value the operator can see is wrong right on
the page. The resolved order is:

| layer | set by | applies |
|---|---|---|
| `global_settings.derp.probe_host` | the card above | on the **next probe** (no restart) |
| `SKYGATE_DERP_PROBE_HOST` in `.env` | `deploy/deploy.sh`, or the operator | at container **creation** |
| unset | — | B289 behaviour: try the row's own name first |

The DB layer wins over `.env` on purpose (an address you typed must not be silently
overruled by an older `.env` value); **Очистить** deletes it and `.env` takes over again.
The card always shows which layer is in effect, so it is never a guess.

The field takes a bare address — `192.0.2.10`, `2001:db8::1` or `derp.example.com`. A
scheme (`https://…`), a port (`192.0.2.10:443` — the port comes from the relay row's URL),
a path, a list, a typo'd IPv4 (`999.0.2.10`) and anything that is not a hostname are
refused, each with its own explanation, and the value is **not** stored. Every change is
written to the audit log (`derp_relay.probe_host`).

Still prefer `.env` when the value must survive a database reset or be provisioned
unattended (that is what `deploy.sh` writes); use the card when you are looking at a
«пропущен» row and want it fixed now.

## B315: the relay's metrics, and why they need a bridge (2026-09-24)

derper publishes its metrics on `/debug/vars` (accepts, bytes, packets, clients,
current connections, the STUN counters) and its debug HTML on `/debug/`. Upstream
`tsweb.AllowDebugAccess` admits **loopback and Tailscale sources only** and answers
everything else `403 debug access denied` — and the verdict is made on the request's
**source address**, not on the address it reached:

| from | `GET /debug/vars` |
|---|---|
| the host's own loopback | `200`, ~6.3 KB of JSON |
| the skygate container | `403 debug access denied` |

That is why `.env`-level tricks and the B296 probe address cannot help here: B296 makes
the probes **reach** the relay, this decides whether they are **admitted**.

Since v1.5.80 `/admin/derp` shows an explicit **«Метрики релея»** card with the current
endpoint, the layer it came from, the named failure and the exact commands to start the
bridge — and the traffic tiles render «—» plus "metrics unavailable" instead of `0`,
because a zero on a busy relay reads as a measurement.

### Start the bridge (once, on the host that runs derper)

The bridge is a subcommand of the binary you already deploy — no new dependency, no new
image, no `docker-compose.yml` edit. It dials the relay's **loopback** over TLS (so derper
sees a loopback source and admits it) and re-serves five read-only paths to the docker
bridge: `/debug/`, `/debug/vars`, `/active-conn`, `/all-recent` and its own `/healthz`.

Docker install (same image, host network):

```bash
docker run -d --name skygate-derp-metrics --restart unless-stopped \
  --network host ghcr.io/barssky/skygate:v1.5.80 derp-metrics-proxy \
  --listen 172.18.0.1:8767 --upstream 127.0.0.1:443 \
  --server-name derp.example.com --insecure
```

Native / systemd install (binary on the host):

```bash
skygate derp-metrics-proxy \
  --listen 172.18.0.1:8767 --upstream 127.0.0.1:443 \
  --server-name derp.example.com --insecure
```

* `--listen` is the address the **container** dials: the docker bridge gateway
  (`docker network inspect headscale_default -f '{{(index .IPAM.Config 0).Gateway}}'`).
  Do not bind `0.0.0.0` unless you also widen `--allow-net`.
* `--server-name` is the relay's certificate name; `--insecure` is legitimate **only
  here**, because the upstream hop never leaves the host (loopback → loopback). Without a
  matching `--server-name` the TLS handshake fails and the bridge's response body says so,
  naming both flags.
* The forwarded paths are a **closed allow-list** (the upstream also carries the DERP
  protocol), and a client address outside loopback/private/CGNAT is refused even on a
  `0.0.0.0` bind.
* `skygate derp-metrics-proxy --help` prints the full flag list.

### Point skygate at it

`/admin/derp` → «Метрики релея» → paste `http://172.18.0.1:8767` → **Проверить** (probes
without saving) → **Сохранить**. The resolved order is the same two layers as B296:

| layer | set by | applies |
|---|---|---|
| `global_settings.derp.debug_url` | the card above | on the **next render** (no restart, no recreate) |
| `SKYGATE_DERP_DEBUG_URL` in `.env` | the operator / provisioning | at container **creation** |
| unset | — | metrics are read from the relay directly (and get `403`) |

The value is a base URL: `http://172.18.0.1:8767` (the `http://` scheme is assumed when
you paste a bare `host:port`), or `https://…` if your bridge terminates TLS itself. A
blank value clears the row. Every save, clear, test and refusal is audited
(`derp.metrics_endpoint`).

### What the page does with the metrics

* **Traffic tiles** show real numbers, or «—» with "metrics unavailable" — never a zero
  that was not measured.
* **The STUN tile names its vantage point.** With metrics readable it reports derper's
  own `stun.counter_requests` (`success` / `not_stun`) — what clients experience. Without
  them it falls back to the B265 UDP round trip from **inside** the skygate container, and
  says so, because that path is not the clients' path (the container's UDP egress can be
  blackholed while clients score the relay fine).
* **`/debug/vars` needs the `derp` block.** A `200` that is not derper's metrics does not
  mark the relay as running, and a `502` from a broken bridge is reported as `502` with the
  bridge's own explanation, never as a `403` from the relay.

### The STUN probe must not use a connected socket

The same release fixes a defect in skygate's own instrument. The STUN probe used
`net.DialTimeout("udp", …)`, i.e. a **connected** UDP socket, and Linux only delivers a
datagram to a connected UDP socket when its **source** matches the peer it was connected
to. Where the container's path to the relay rewrites the address (the reference host
DNATs `:3478` onto the docker bridge gateway), the reply arrives from the rewritten
source and the kernel drops it before Go sees it — so the tile said «UDP-проба не прошла»
on a relay that had answered in 28 ms.

Measured in the same network namespace, at the same instant:

| socket | probe | result |
|---|---|---|
| unconnected (python) | `192.168.13.69:3478` | `REPLY 0x0101`, 44 bytes, source `172.18.0.1` |
| connected (Go, pre-fix) | `192.168.13.69:3478` | `read udp 172.18.0.3:…->192.168.13.69:3478: i/o timeout` |
| connected (Go, pre-fix) | `172.18.0.1:3478` | `REPLY`, rtt 198 µs |
| unconnected (Go, post-fix) | `192.168.13.69:3478` | `REPLY`, rtt 28 ms, `reply came from 172.18.0.1:3478` |

The probe now uses an unconnected socket and accepts a reply from any source — the fresh
96-bit transaction id (compared byte for byte) is what proves the datagram is ours, and a
foreign one is still rejected. When the answering address differs from the address dialled
the tile says so: «ответ пришёл с `<addr>`».

### Reading the two address rows

`/admin/derp` → «Сервис» has two rows that answer two different questions:

* **«Публичный IP»** — the address **clients** dial (DNS of `SKYGATE_DERP_HOSTNAME`, or of
  the hostname derper reports). When the name does not resolve from inside the container
  the row says «не удалось определить» and shows the fallback (skygate's own egress
  address) **as a fallback with the reason**, instead of printing it under a label that
  promises a public address.
* **«Адрес связи из контейнера»** — the address **skygate itself** dials to reach the relay
  (the B296 value: DB > `SKYGATE_DERP_PROBE_HOST` > none). A private address here is
  normal and correct; it is not the clients' path.

## See also

- `docs/headplane.md` — the same "use existing / bundled" pattern,
  for the operator UI sidecar.
- `docs/LESSONS.md` — the wider roadmap for moving
  deploy-time config into a web UI.
- `docs/telegram-relay.md` — the use case: Tailscale clients
  reaching `api.telegram.org` through a custom DERP.
