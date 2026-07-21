# HTTPS / TLS setup for Skygate

> How to serve every Skygate module over HTTPS without a
> full reverse-proxy UI (no nginx Proxy Manager, no
> Traefik dashboard). The default deployment uses
> **Caddy** as a tiny TLS-terminating reverse proxy; the
> document also covers two alternatives for tailnet-only
> and "headscale-only" use cases.

---

## Why this exists

Out of the box, Skygate serves:

| Module | Listen | TLS | Notes |
|---|---|---|---|
| **Skygate dashboard** | `:8080` (HTTP) | none | Tailscale clients see it on `https://${SKYGATE_CONTROL_URL}` only if a proxy terminates TLS in front. |
| **Headscale admin** | `:50444` (HTTP) + `:50443` (gRPC) | none | `grpc_allow_insecure: true` is the default so the gRPC port is reachable without TLS. |
| **Headplane admin** | `:50445` (HTTP) | none | `COOKIE_SECURE: false` so the auth cookie survives HTTP. |
| **DERP relay** | `:443` (HTTPS) | **automatic** | `certmode=letsencrypt` in `derper` already does Let's Encrypt via the HTTP-01 challenge. |

Without a TLS terminator in front of the first three, every
Tailscale client must override the "insecure control plane"
warning, and the Tailscale app on iOS / Android refuses to
connect to a plain-HTTP control plane. The fix is a
TLS-terminating reverse proxy.

The Skygate deploy is designed for **Caddy** because:

* ~12 MB image (vs. nginx ~70 MB + a separate Proxy Manager
  container + a database for the UI state).
* Single Caddyfile, automatic Let's Encrypt, automatic
  HTTP→HTTPS redirect, automatic OCSP stapling, automatic
  HSTS. No restarts on cert renewal.
* DNS-01 challenge works on private subnets (you don't
  need port 80 reachable from the public Internet; the
  challenge is answered at the DNS layer via a
  Cloudflare/Route53/whatever API token).
* Native ACME support for wildcard certs (`*.skynas.ru`)
  with one DNS-01 credential.

If you prefer nginx + certbot, the same architecture works;
the differences are at the proxy-config-file level, not in
the modules.

---

## Architecture

```
                  ┌─────────────────────────────────────┐
                  │ Caddy (:80 HTTP, :443 HTTPS)       │
                  │  • Let's Encrypt (DNS-01)          │
                  │  • HSTS, OCSP stapling, HTTP→HTTPS │
                  └─────────────┬───────────────────────┘
                                │
        ┌───────────────────────┼───────────────────────┐
        │                       │                       │
   head.skynas.ru       headplane.skynas.ru      derp.skynas.ru
        │                       │                       │
   ┌────▼────┐            ┌──────▼──────┐         ┌─────▼─────┐
   │ skygate │            │  headplane  │         │   derper  │
   │ :8080   │            │  :50445     │         │  :443     │
   │ /dashboard,         │  admin UI   │         │  DERP    │
   │ /admin/*            └─────────────┘         │  relay    │
   │ /my/*                                       │  (auto    │
   └─────────┘                                   │  cert)    │
                                                 └───────────┘
   ┌────────────────────────────────────────────────────┐
   │ headscale :50444 (HTTP), :50443 (gRPC)             │
   │  • internal network only                           │
   │  • gRPC: grpc_allow_insecure=true (insecure is OK  │
   │    because the Caddy→headscale hop is on the       │
   │    internal Docker network, not the public         │
   │    Internet)                                       │
   └────────────────────────────────────────────────────┘
```

**Key insight:** the Caddy→backend hop is on the internal
Docker network (or `127.0.0.1` if Caddy runs on the host
without Docker). The operator's threat model is "the
public Internet hits only Caddy; everything else is
internal". The backends can run plain HTTP because
nothing on the public Internet can reach them directly.

---

## The operator's checklist

### 1. DNS records

You need an A record for each subdomain that will be
public. Replace `skynas.ru` with your domain.

```dns
head.skynas.ru.        A   <public-ip>     # skygate dashboard + headscale API
headplane.skynas.ru.   A   <public-ip>     # (optional) headplane admin UI
derp.skynas.ru.        A   <public-ip>     # (optional) self-hosted DERP
```

If you have only one public IP and one hostname, skip
the subdomains and use just `head.skynas.ru` for
everything. Caddy will route by `Host:` header.

**For Let's Encrypt DNS-01** (recommended on private
subnets, no port-80 needed), add a wildcard record too:

```dns
*.skynas.ru.   CNAME   head.skynas.ru.   # only if you use a wildcard cert
_acme-challenge.skynas.ru.   TXT   "<Caddy writes this during issuance>"
```

### 2. `.env` additions

Append to your `.env` (see `.env.example` for the full
annotated version):

```bash
# ─── HTTPS reverse proxy (Caddy) ─────────────────────────────────────
# v0.15.0: optional Caddy sidecar handles TLS termination for all
# public-facing modules. Set CADDY_ENABLED=true to deploy it; false
# = no TLS (Caddy is not added to docker-compose). Caddy is the
# default; nginx + certbot works too but the template here is
# Caddy-specific.
CADDY_ENABLED=true
# DNS provider for Let's Encrypt DNS-01 challenge. Caddy
# supports 30+; see https://github.com/caddy-dns. The
# provider's API token goes in CADDY_DNS_API_TOKEN.
# Common choices:
#   cloudflare   Route53  gandi   digitalocean  googlecloud
#   hetzner      ovh      namecheap  porkbun  desec
# Set to "http" for the HTTP-01 challenge (simpler, but
# requires port 80 reachable from the public Internet).
CADDY_DNS_PROVIDER=cloudflare
# API token with Zone:DNS:Edit on the apex domain. Caddy
# writes a TXT record under _acme-challenge.<apex> during
# issuance and deletes it afterwards. The token is never
# written to the Caddyfile; it lives in a separate file
# (see "DNS API token" below) so the rendered Caddyfile
# can be safely committed to git.
CADDY_DNS_API_TOKEN_FILE=/var/lib/skygate/secrets/caddy-dns-token
# Public hostnames the Caddy sidecar will issue certs for.
# Each entry is a separate virtual host; the rule
# routing is below.
CADDY_HOSTS_HEAD=head.skynas.ru
CADDY_HOSTS_HEADPLANE=headplane.skynas.ru
CADDY_HOSTS_DERP=derp.skynas.ru
# Optional: enable HSTS (HTTP Strict Transport Security).
# Default true; the operator's browser will refuse
# plain-HTTP connections to these hostnames for 6 months
# after the first visit. Disable only for testing.
CADDY_HSTS=true
```

**The DNS API token** lives in a separate file (not in
`.env` and not in the rendered Caddyfile) so the Caddyfile
can be safely committed to git for backup. The file is
plain text; protect its permissions:

```bash
mkdir -p /var/lib/skygate/secrets
echo "your-cloudflare-api-token-here" > /var/lib/skygate/secrets/caddy-dns-token
chmod 600 /var/lib/skygate/secrets/caddy-dns-token
```

### 3. Per-module changes

#### Skygate
**No code changes.** The `:8080` listener continues to
serve plain HTTP; Caddy terminates TLS and forwards the
request with the original `Host:` header. The only thing
to verify: `SKYGATE_CONTROL_URL` already points at
`https://head.skynas.ru`, which Tailscale clients use to
reach the dashboard.

#### Headscale
Behind Caddy on the internal Docker network, **headscale
can keep `grpc_allow_insecure: true`** (the gRPC port is
not exposed to the public Internet). If you want headscale
to *also* terminate TLS natively (e.g. you're skipping
Caddy and using certbot on the headscale port), set:

```yaml
# in rendered headscale config.yaml
tls:
  cert_path: /etc/headscale/tls/fullchain.pem
  key_path:  /etc/headscale/tls/privkey.pem
grpc_allow_insecure: false
```

…and put the certs in `/etc/headscale/tls/` on the host
(the volume mount is already in `headscale-compose.yml.tmpl`).

The default `grpc_allow_insecure: true` is what we ship
for the Caddy-fronted path; Tailscale clients always dial
`https://head.skynas.ru:443` and Caddy forwards the HTTP+JSON
API to `:50444` and the gRPC-over-HTTP/2 to `:50443`.

#### Headplane
**One change**: enable secure cookies (so the session
cookie is `Secure; HttpOnly; SameSite=Lax` and only sent
over HTTPS). In `.env`:

```bash
HEADPLANE_SERVER__COOKIE_SECURE=true
```

If you skip Caddy and serve headplane on plain HTTP, set
`COOKIE_SECURE=false` (the default) — the trade-off is
that the cookie is vulnerable to MITM theft, which is
acceptable for a tailnet-only headplane (use Tailscale TLS
in that case, see "Alternative: Tailscale TLS" below).

#### DERP
**No changes.** DERP's built-in `certmode=letsencrypt` does
the HTTP-01 challenge on port 80 automatically. If you
prefer DNS-01, change the derper-compose.yml.tmpl to add
`--certmode=dns` (requires a DERP_PRIVATE_KEY that the
operator can manage) — but the default is fine for most
deployments.

---

## Caddyfile (rendered from `deploy/templates/Caddyfile.tmpl`)

```caddyfile
# Skygate v0.15.0 — Caddyfile template
# Rendered: deploy/deploy.sh → /var/lib/skygate/caddy/Caddyfile
# 
# Three virtual hosts; each gets its own Let's Encrypt
# cert via the DNS-01 challenge. Per-vhost reverse proxy
# forwards plain HTTP to the matching backend on the
# internal Docker network.
#
# Common Caddy directives used here:
#   encode zstd gzip   — compress responses
#   reverse_proxy      — pass the request to a backend
#   header Strict-Transport-Security — HSTS
#   tls { dns <provider> ... } — DNS-01 challenge config

(common) {
    encode zstd gzip
    header Strict-Transport-Security "max-age=15552000; includeSubDomains; preload"
    # The cloudflare module reads the token from the
    # file path in ${CADDY_DNS_API_TOKEN_FILE} (or the
    # value of ${CADDY_DNS_API_TOKEN} if non-empty).
    tls {
        dns ${CADDY_DNS_PROVIDER} ${CADDY_DNS_API_TOKEN_OR_FILE}
    }
}

# ─── head.skynas.ru (skygate dashboard + headscale API) ───
${CADDY_HOSTS_HEAD} {
    import common
    # Tailscale's control-plane protocol hits the same
    # hostname on different paths:
    #   /          → skygate dashboard (HTML)
    #   /api/v1/*   → headscale JSON API
    #   /ts2021     → headscale gRPC-over-HTTP/2
    #   /machine/   → headscale gRPC-over-HTTP/2 (alt path)
    #   /key        → headscale gRPC-over-HTTP/2
    #   /oidc/*     → skygate OIDC callback (if you set it up)
    # We split these by path: the API + gRPC go to
    # headscale, everything else goes to skygate.
    @headscale_api {
        path /api/*
        path /oidc/*
    }
    @headscale_grpc {
        path /ts2021/*
        path /machine/*
        path /key
    }
    reverse_proxy @headscale_api  headscale:50444
    reverse_proxy @headscale_grpc headscale:50443
    reverse_proxy                   skygate:8080
}

# ─── headplane.skynas.ru (admin UI) ───
${CADDY_HOSTS_HEADPLANE} {
    import common
    reverse_proxy headplane:50445
}

# ─── derp.skynas.ru (DERP relay) ───
# DERP's built-in derper does the HTTP-01 challenge
# itself, so this vhost just proxies to :443 on the
# derper container (network_mode: host in the
# derper-compose.yml.tmpl means the derper is on
# 127.0.0.1:443 from the host's perspective).
${CADDY_HOSTS_DERP} {
    import common
    reverse_proxy 127.0.0.1:443
}
```

The above is a template — `deploy/deploy.sh` renders it
from `.env`. The Caddyfile is written to
`/var/lib/skygate/caddy/Caddyfile` and mounted into the
Caddy container at `/etc/caddy/Caddyfile` (Caddy's default
config path).

---

## Deploying Caddy

When `CADDY_ENABLED=true`, the deploy system:

1. Renders `deploy/templates/Caddyfile.tmpl` to
   `${DEPLOY_SKYGATE_DIR}/caddy/Caddyfile`.
2. Adds the `caddy` service to `docker-compose.yml` with
   the `caddy:2-alpine` image, the internal network, and
   a published `:80` + `:443` to the host (or to a
   reverse-proxy in front of the host if you have one).
3. Mounts the rendered Caddyfile + the DNS API token
   file.
4. Pulls the cert volume into the Caddy container at
   `/data` (Caddy's default cert path; survives container
   restarts so you don't have to re-issue every restart).

The first time the Caddy container starts, it issues
certificates for each vhost (the DNS-01 challenge
usually takes 30-60 seconds per vhost). Watch the
`docker logs caddy` output; you should see "certificate
obtained" for each vhost within the first 2 minutes.

---

## Verification

```bash
# 1. Cert is valid
openssl s_client -connect head.skynas.ru:443 -servername head.skynas.ru \
    < /dev/null 2>/dev/null | openssl x509 -noout -subject -issuer -dates
# expected: subject=CN = head.skynas.ru, issuer=CN = ... R3/R10 (Let's Encrypt)

# 2. HTTP→HTTPS redirect
curl -sI http://head.skynas.ru/ | head -3
# expected: HTTP/1.1 308 Permanent Redirect
#           Location: https://head.skynas.ru/

# 3. Dashboard loads over HTTPS
curl -sI https://head.skynas.ru/login | head -3
# expected: HTTP/2 200 (or 302 to /dashboard)

# 4. Headscale API over HTTPS (the path the Tailscale
#    client dials when registering)
curl -sI https://head.skynas.ru/api/v1/node | head -3
# expected: HTTP/2 200 (or 401 if you haven't authed)

# 5. HSTS header is set
curl -sI https://head.skynas.ru/ | grep -i strict-transport
# expected: strict-transport-security: max-age=15552000; ...

# 6. Tailscale client can register
tailscale up --login-server=https://head.skynas.ru
# expected: "Success" + a Tailscale IP from the headscale
#           prefix range (100.64.0.0/10)
```

A deploy-time check (in the v0.15.0 follow-up) wraps the
above into a single command. Today, run them by hand.

---

## Alternative: Tailscale TLS (no certbot, no DNS-01, no Caddy)

If **every** access to the dashboard comes from a
Tailscale client (the common case for a single-operator
deployment), you don't need Caddy at all. Tailscale signs
a short-lived cert for the node's `100.x.x.x` Tailscale IP
*or* a custom hostname you set up via MagicDNS.

```bash
# On the skygate VM (already in the tailnet):
sudo tailscale cert head.skynas.ru
# → /var/lib/tailscale/cert.pem
# → /var/lib/tailscale/key.pem

# Mount these into skygate (one extra bind mount in
# docker-compose.yml):
#   - /var/lib/tailscale/cert.pem:/etc/skygate/tls/cert.pem:ro
#   - /var/lib/tailscale/key.pem:/etc/skygate/tls/key.pem:ro
```

Then either:

* **A)** switch skygate to listen on `:443` natively
  (requires a code change — listen on :443 + use the
  cert files; not currently supported by skygate but
  trivially addable in v0.15.x), or
* **B)** point Caddy at these cert files instead of
  Let's Encrypt (`tls /etc/skygate/tls/cert.pem
  /etc/skygate/tls/key.pem` in the Caddyfile, no
  `dns` block).

Tailscale TLS only works for tailnet members. If you
want to share the dashboard with a friend who isn't on
your tailnet, fall back to Let's Encrypt via Caddy.

---

## Alternative: native headscale TLS (skip Caddy for the API only)

If you want to skip the Caddy sidecar entirely and have
headscale terminate its own TLS, you can. The headscale
container stays on the host, you run certbot on the host,
and you mount the certs into the headscale container:

```bash
# On the host
sudo certbot certonly --dns-cloudflare \
    --dns-cloudflare-credentials /root/.cloudflare.ini \
    -d head.skynas.ru
sudo cp /etc/letsencrypt/live/head.skynas.ru/fullchain.pem \
       /var/lib/headscale/tls/cert.pem
sudo cp /etc/letsencrypt/live/head.skynas.ru/privkey.pem \
       /var/lib/headscale/tls/key.pem
```

Then in `headscale-config.yaml.tmpl`:

```yaml
tls:
  cert_path: /etc/headscale/tls/cert.pem
  key_path:  /etc/headscale/tls/key.pem

grpc_allow_insecure: false
```

The `grpc_allow_insecure: false` is the key change —
Tailscale clients will refuse the connection if the
gRPC traffic is in plaintext. The cert path can be
a path inside the container (mounted from
`/var/lib/headscale/tls/` on the host).

This is the path the user takes when they only need
headscale (and don't care about the skygate dashboard's
TLS). For the full dashboard TLS, Caddy is the simpler
choice.

---

## Why not nginx + certbot?

nginx works fine. The downsides for the Skygate use
case are:

* **More config**: nginx.conf + sites-enabled/* + the
  certbot cron + the `dhparam.pem` for modern TLS. Caddy's
  single Caddyfile is ~30 lines for the same setup.
* **No automatic OCSP stapling**: nginx needs
  `ssl_stapling on; ssl_stapling_verify on;` plus a
  resolver. Caddy does this automatically.
* **No automatic HTTP→HTTPS redirect**: nginx needs
  a separate `server { listen 80; return 301 https://... }`
  block. Caddy does this automatically.
* **nginx Proxy Manager specifically** is a third
  product (PHP app + MariaDB) that adds another moving
  part (the database, the PHP-FPM process, the UI login
  itself). For a single-operator deployment, that's
  overkill — the operator would have to maintain the
  Proxy Manager UI, the cert renewal cron, the database
  backups, and the Proxy Manager upgrade path. Caddy is
  one binary.

The Skygate v0.15.0 release ships Caddy as the
recommended path because it removes the most moving
parts. nginx + certbot is supported as a documented
fallback (you can write the same architecture with
nginx if you prefer; the per-module changes are
identical).

---

## Files added in v0.15.0

* `docs/https-setup.md` — this file
* `deploy/templates/Caddyfile.tmpl` — Caddyfile template
* `docker-compose.yml` — new `caddy` service when
  `CADDY_ENABLED=true`
* `.env.example` — new `CADDY_*` and `HEADPLANE_SERVER__COOKIE_SECURE`
  knobs

## Files NOT changed in v0.15.0

* `internal/acl/acl.go` — no change
* `internal/handlers/*` — no change
* `internal/telegram/*` — no change
* `internal/release/*` — no change
* `internal/monitoring/*` — no change

The HTTPS layer is entirely outside the Go process; it's
a deploy-time + Caddy-time concern.
