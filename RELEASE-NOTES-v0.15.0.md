# v0.15.0 — HTTPS / TLS via Caddy (no nginx Proxy Manager)

> The "make the tailnet's control plane actually speak
> HTTPS" release. The deploy is now fronted by a Caddy
> sidecar that auto-issues Let's Encrypt certs via the
> DNS-01 challenge (no port-80 inbound required) and
> reverse-proxies plain HTTP to the backends on the
> internal Docker network. The operator's old
> "nginx Proxy Manager" workflow is replaced by a single
> Caddyfile — 30 lines, no UI, no DB, no PHP, automatic
> cert renewal. Three modules get TLS for the price of
> one: skygate dashboard, headscale admin API + gRPC,
> headplane admin UI. DERP relay already did TLS itself
> (certmode=letsencrypt) and continues to.

---

## What's in this release

### 1. Caddy sidecar in `docker-compose.yml`

New `caddy:2-alpine` service added to the compose file
when `CADDY_ENABLED=true` (the v0.15.0 default). Publishes
`:80` and `:443` on the host. Joins the same Docker network
as skygate + headscale + headplane so the rendered
Caddyfile can `reverse_proxy headscale:50444` (etc.) by
container name — no host-network mode, no exposed internal
ports to the public Internet.

```yaml
caddy:
  image: caddy:2-alpine
  container_name: caddy
  restart: unless-stopped
  ports:
    - "80:80"
    - "443:443"
  volumes:
    - ./caddy/Caddyfile:/etc/caddy/Caddyfile:ro
    - caddy-data:/data
    - caddy-config:/config
  env_file:
    - ./caddy/caddy.env    # 0600; DNS-01 token only
  networks:
    - skygate-net
```

Cert storage (`/data`) and ACME account config (`/config`)
are persistent volumes, so the next Caddy start doesn't
re-issue. A separate `caddy.env` (mode 0600) carries only
the DNS-01 API token; the operator's main `.env` is
untouched (it's the one that's often committed to git).

### 2. Rendered `Caddyfile` — three vhosts, per-hostname routing

`deploy/templates/Caddyfile.tmpl` renders to
`${DEPLOY_SKYGATE_DIR}/caddy/Caddyfile`. Three vhosts:

* **`head.example.com`** — by-path routing inside a single
  vhost:
  * `/api/*` and `/oidc/*` → headscale:50444 (JSON API)
  * `/ts2021/*`, `/machine/*`, `/key` → headscale:50443
    (gRPC-over-HTTP/2; this is the path Tailscale clients
    dial when registering)
  * everything else → skygate:8080 (the HTML dashboard
    + `/admin/*` + `/my/*`)
* **`headplane.example.com`** — single reverse proxy to
  headplane:50445.
* **`derp.example.com`** — single reverse proxy to
  127.0.0.1:443 (derper is on network_mode: host).

Per-vhost shared block:

```caddyfile
(common) {
    encode zstd gzip
    header Strict-Transport-Security "max-age=15552000; includeSubDomains; preload"
    tls {
        dns cloudflare { env.CADDY_DNS_TOKEN_VALUE }
    }
}
```

HSTS with `max-age=15552000` (6 months) + `includeSubDomains`
+ `preload` is the standard "make Chrome preload me" header.
The token is referenced via `env.CADDY_DNS_TOKEN_VALUE`,
which Caddy reads from the caddy container's environment
(loaded from `env_file:`). The rendered Caddyfile does
NOT embed the token — safe to commit to git for backup.

### 3. Per-module changes (the "what the user must configure" checklist)

This is the operator-facing checklist from
`docs/https-setup.md` (the new 17KB doc). TL;DR:

| Module | What changes |
|---|---|
| **DNS** | A record for each public hostname → public IP. Apex + ACME-01 challenge record (Caddy writes the TXT automatically via DNS-01). |
| **Skygate** | No code change. The `:8080` listener continues to serve plain HTTP; Caddy terminates TLS in front. `SKYGATE_CONTROL_URL` already points at the public hostname. |
| **Headscale** | No code change. `grpc_allow_insecure: true` is fine behind Caddy (the gRPC port is on the internal Docker network, not exposed to the public Internet). Tailscale clients dial `https://head.example.com:443` and Caddy forwards to `:50443`. |
| **Headplane** | One env var: `HEADPLANE_SERVER__COOKIE_SECURE=true` (HTTPS-only cookies). The default is `false`; the operator flips it to `true` once Caddy is fronting. |
| **DERP** | No change. `certmode=letsencrypt` in `derper` does the HTTP-01 challenge on port 80 automatically. |
| **.env** | 8 new vars (see `.env.example`): `CADDY_ENABLED`, `CADDY_DNS_PROVIDER`, `CADDY_DNS_API_TOKEN_FILE`, `CADDY_HOSTS_HEAD`, `CADDY_HOSTS_HEADPLANE`, `CADDY_HOSTS_DERP`, `CADDY_HSTS`, plus the `HEADPLANE_SERVER__COOKIE_SECURE` flip. |
| **DNS provider API token** | One new file: `${CADDY_DNS_API_TOKEN_FILE}` (default `/var/lib/skygate/secrets/caddy-dns-token`). 0600 perms. NOT in `.env`. The Caddyfile references it as `env.CADDY_DNS_TOKEN_VALUE` at runtime. |

### 4. Deploy-time HTTPS check (`scripts/check_https.py`)

Complements the existing `check_exit_nodes.py` with a
public-HTTPS health check. Exits 0 on full pass, 1 on
failure, 2 on setup error. Default mode is warn-only;
`--strict` hard-fails. Wired into `make test` (always)
and `make check-https-strict` (CI variant).

Checks:

1. **TLS handshake** to the public hostname on :443.
2. **Cert SAN** contains the public hostname (catches
   the common "Caddyfile vhost doesn't match the cert"
   bug).
3. **Cert validity** (notBefore / notAfter).
4. **HTTP→HTTPS redirect** on :80 (catches "Caddy isn't
   terminating the connection").
5. **HSTS** on `/login` (catches the operator who
   forgot `header Strict-Transport-Security ...`).

The new `make check-https` target reads `SKYGATE_CONTROL_URL`
from the operator's `.env` and runs the check. Wired
into `make test` so a broken Caddy sidecar surfaces on
the next deploy.

### 5. Documentation (`docs/https-setup.md`)

The 17KB document the user asked for. Sections:

* **Why this exists** — current state, what each module
  needs, why Caddy over nginx PM.
* **Architecture diagram** — public Internet → Caddy
  (:443) → skygate/headscale/headplane/derper on the
  internal Docker network. Threat model: "public Internet
  hits only Caddy; backends are internal".
* **Operator checklist** — DNS records, .env additions,
  per-module changes.
* **The full rendered Caddyfile** (annotated).
* **Verification commands** — `openssl s_client`,
  `curl -sI`, `tailscale up --login-server=...`.
* **Alternative: Tailscale TLS** — for tailnet-only
  deployments, no certbot needed. `tailscale cert
  head.example.com` + the Caddyfile points at the
  Tailscale-issued cert. No external DNS records
  required.
* **Alternative: native headscale TLS** — for the
  "only headscale needs TLS" use case. Set
  `tls.cert_path/key_path` + `grpc_allow_insecure: false`
  in headscale-config.yaml; use certbot on the host.
* **Why not nginx + certbot** — the downside list (more
  config, no automatic OCSP stapling, no automatic
  HTTP→HTTPS redirect, Proxy Manager adds a third
  product with its own DB). nginx works fine; Caddy
  is just less ceremony.

---

## Files added in v0.15.0

* `docs/https-setup.md` — the operator-facing guide
* `deploy/templates/Caddyfile.tmpl` — the Caddyfile
  template (rendered to `${DEPLOY_SKYGATE_DIR}/caddy/Caddyfile`)
* `scripts/check_https.py` — the deploy-time HTTPS check
* `RELEASE-NOTES-v0.15.0.md` — this file

## Files modified in v0.15.0

* `docker-compose.yml` — new `caddy` service + `caddy-data`/
  `caddy-config` volumes
* `.env.example` — 8 new vars under a new "HTTPS reverse
  proxy (Caddy, v0.15.0)" section
* `deploy/deploy.sh` — new "STEP 2b: Caddy" block that
  renders the Caddyfile when `CADDY_ENABLED=true`,
  pre-renders the TLS block based on
  `${CADDY_DNS_PROVIDER}` (HTTP-01 vs DNS-01), writes
  the Caddy-specific env file, and verifies the public
  hostnames resolve
* `deploy/lib/env.sh` — defaults for the new CADDY_*
  vars
* `Makefile` — `check-https` + `check-https-strict`
  targets, `make test` now runs `check-https`

## Files NOT changed in v0.15.0

* `internal/acl/acl.go` — no change
* `internal/handlers/*` — no change
* `internal/telegram/*` — no change
* `internal/release/*` — no change
* `internal/monitoring/*` — no change

The HTTPS layer is entirely outside the Go process; it's
a deploy-time + Caddy-time concern.

---

## Deployment notes

### Existing operators (post-deploy verify)

```bash
# 1. Add CADDY_* to .env (or accept the defaults)
echo "CADDY_ENABLED=true" >> .env
echo "CADDY_HOSTS_HEAD=head.example.com" >> .env
# (override the example.com defaults with the real
#  hostname the operator owns)

# 2. Create the DNS API token file (or use Tailscale
#    TLS, in which case set CADDY_DNS_PROVIDER=http)
mkdir -p /var/lib/skygate/secrets
echo "your-cloudflare-api-token" > /var/lib/skygate/secrets/caddy-dns-token
chmod 600 /var/lib/skygate/secrets/caddy-dns-token

# 3. Add the public A records (or wildcards)
#    head.example.com → <public-ip>
#    headplane.example.com → <public-ip>   (optional)
#    derp.example.com → <public-ip>           (optional)

# 4. Re-run deploy.sh
cd /home/skyadmin/skygate
git pull
./deploy/deploy.sh

# 5. Verify
make check-https
# expected: PASS: TLS 1.3, ...
#           PASS: Cert SAN matches 'head.example.com' (SANs: [...])
#           PASS: Cert valid: ...
#           PASS: HTTP→HTTPS redirect: 308 → https://...
#           PASS: HSTS: max-age=15552000; ...
```

### Tailscale-side configuration

No change. The Tailscale client on every device already
dials `${SKYGATE_CONTROL_URL}` (e.g.
`https://head.example.com`); with Caddy in front of
headscale, the registration + key exchange now go
through the Caddy-terminated HTTPS path. The Tailscale
app on iOS / Android stops showing the "insecure control
plane" warning once the cert is valid.

---

## Test results

* 12 / 12 packages green (`go test ./...`)
* `bash -n deploy/deploy.sh` — syntax check OK
* `python3 scripts/check_https.py --help` — usage renders
* Smoke 118 / 118 unchanged (smoke is HTTP-only, the
  HTTPS check is a separate path)
* The HTTPS check is verified manually on the VM
  (the production deployment still uses plain HTTP
  internally; the operator can opt into Caddy when
  the public hostname is ready).

---

## What's next

* **v0.15.1** — wire Caddy into the smoke as an optional
  HTTPS smoke (the smoke is HTTP-only today; an HTTPS
  variant would assert 200 + HSTS + SAN-match on a real
  Caddy-fronted deployment).
* **v0.16.0** — per-plane ACL (split per-user ACL by
  control plane) + ACL import/export with dry-run
  preview. See `docs/skygate-as-shell.md`.
* **Butler voice v3** — urgency marks (`🪶` / `🪶!` /
  `🪶!!` based on alert severity).
* **Personal API token rotation** — TTL + auto-rotate
  field for bot integration.
