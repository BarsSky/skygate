# Public TLS / HTTPS for skygate

This document consolidates the internal runbooks `https-setup.md` and
`derp-cert-sync.md`, which were removed in the 2026-09-18 documentation
restructure; the full original text stays in git history. It covers how to
put skygate, headscale, headplane and the bundled DERP relay behind TLS, how
the DERP certificate renewal path works, and how to verify and roll back the
whole thing. Related reading: [`deploy.md`](deploy.md) (env-var reference and
fresh install), [`derp.md`](derp.md) (relay configuration),
[`headplane.md`](headplane.md), [`oidc.md`](oidc.md),
[`networking.md`](networking.md), [`troubleshooting.md`](troubleshooting.md).

## Table of contents

1. [What must be publicly reachable and why](#1-what-must-be-publicly-reachable-and-why)
2. [TLS termination options](#2-tls-termination-options)
   - [2.1 Caddy sidecar (in-compose profile)](#21-caddy-sidecar-in-compose-profile)
   - [2.2 External nginx / Nginx Proxy Manager](#22-external-nginx--nginx-proxy-manager)
   - [2.3 Direct, Tailscale TLS, native headscale TLS](#23-direct-tailscale-tls-native-headscale-tls)
   - [2.4 Forwarded-header contract](#24-forwarded-header-contract)
3. [DNS / ports checklist and certificate renewal](#3-dns--ports-checklist-and-certificate-renewal)
   - [3.1 DNS records](#31-dns-records)
   - [3.2 Inbound ports](#32-inbound-ports)
   - [3.3 Certificate renewal](#33-certificate-renewal)
   - [3.4 DERP certificate sync path](#34-derp-certificate-sync-path)
4. [Verification and failure modes](#4-verification-and-failure-modes)
   - [4.1 Verification commands](#41-verification-commands)
   - [4.2 Failure modes: symptom → cause → fix](#42-failure-modes-symptom--cause--fix)
5. [Rollback](#5-rollback)

---

## 1. What must be publicly reachable and why

Out of the box skygate serves plain HTTP. The TLS terminator in front is what
makes the control plane usable by real clients (notably iOS/Android, which
refuse a plain-HTTP control plane).

| Module | Backend listen | Public? | Why it must be reachable over TLS |
|---|---|---|---|
| **skygate portal** | `:8080` HTTP | yes, on the issuer hostname | `/login`, `/dashboard`, `/my/*`, `/admin/*`, and the OIDC provider endpoints (see below). |
| **skygate OIDC provider** | `:8080` HTTP | yes, on the issuer hostname | headscale fetches `/.well-known/openid-configuration` at startup; Tailscale clients' browsers hit `/oidc/authorize`; headscale's backend calls `/oidc/token` and `/oidc/userinfo`. The issuer in every id_token is the literal `SKYGATE_OIDC_ISSUER` value, so it must be a public HTTPS URL. |
| **headscale control plane** | `:50444` HTTP (JSON API + OIDC callback), `:50443` gRPC (`/ts2021`, `/machine`, `/key`) | yes, on the headscale hostname | Tailscale clients dial `https://head.example.com` for registration, key exchange and netmap updates. headscale's OIDC callback lives at `https://head.example.com/oidc/callback` on the `:50444` listener. |
| **headplane (optional)** | `:50445` HTTP | usually operator-only | Admin UI. If it is exposed publicly, the session cookie must be `Secure` (see [§2.1](#21-caddy-sidecar-in-compose-profile) / [§2.3](#23-direct-tailscale-tls-native-headscale-tls)). Keeping it tailnet-only is the safer default. |
| **DERP relay (bundled, optional)** | `:443` TCP (HTTPS + DERP + WebSocket upgrade), `:3478` UDP (STUN) | yes, on the relay hostname | Clients dial the relay by its public hostname and expect a certificate whose CN/SAN matches that hostname. The relay additionally serves an HTTP redirect on its `--http-port` (default `80`) and a `derpmap.json` HTTP endpoint on `DERP_MAP_PORT` (default `8765`). |

### Request flow

```
                       ┌──────────────────────────────────────────┐
   public Internet ──► │  TLS terminator                          │
                       │  (Caddy sidecar, nginx/NPM, Cloudflare,  │
                       │   ALB, Tailscale TLS, …)                 │
                       │  :443 HTTPS · LE or operator certificate │
                       │  HSTS · HTTP→HTTPS redirect              │
                       └───────────────┬──────────────────────────┘
                                       │ plain HTTP, internal network
        ┌──────────────────────────────┼───────────────────────────────┐
        │                              │                               │
  skygate.example.com          head.example.com                derp.example.com
        │                              │                               │
   ┌────▼─────┐              ┌─────────▼──────────┐             ┌──────▼──────┐
   │ skygate  │              │ headscale          │             │  derper     │
   │ :8080    │              │ :50444 JSON + cb   │             │  :443 DERP  │
   │ /login   │              │ :50443 gRPC        │             │  :3478/udp  │
   │ /oidc/*  │              │ (/api, /ts2021,    │             │  (own cert  │
   │ /admin/* │              │  /machine, /key,   │             │   or manual)│
   └──────────┘              │  /oidc/callback)   │             └─────────────┘
                             └────────────────────┘
```

**Key point:** the terminator → backend hop is plain HTTP on the internal
Docker network (or `127.0.0.1` when the proxy runs on the host without
Docker). That is deliberate: only the terminator is reachable from the
public Internet, so the backends do not need their own TLS and
`grpc_allow_insecure: true` remains safe.

What may stay internal: skygate `:8080`, headscale `:50444` / `:50443`,
headplane `:50445`, PostgreSQL `:5432`, the NPM admin UI on `:81`, and the
DERP `derpmap.json` port `8765` (unless you serve the derpmap to clients
directly on a public hostname).

---

## 2. TLS termination options

Pick exactly one terminator per public hostname. Running two on the same
host (for example the in-compose Caddy sidecar **and** an external NPM) is a
classic silent outage: the inner one wins the port, cannot issue a
certificate for the placeholder hostname, and returns an opaque
`SSL alert 80 internal_error` or `internal_error`.

### 2.1 Caddy sidecar (in-compose profile)

Since v0.32.11 the in-compose Caddy is **off by default**
(`CADDY_ENABLED=false`). The service lives under
`profiles: ["caddy"]` in `docker-compose.yml`, so a plain
`docker compose up -d` never starts it. Opt in from `.env`:

```ini
CADDY_ENABLED=true
CADDY_HOSTS_HEAD=head.example.com          # your real hostname, NOT example.com
CADDY_HOSTS_HEADPLANE=headplane.example.com # optional
CADDY_HOSTS_DERP=derp.example.com           # optional
CADDY_DNS_PROVIDER=cloudflare               # or "http" for the HTTP-01 challenge
CADDY_DNS_API_TOKEN_FILE=/var/lib/skygate/secrets/caddy-dns-token
CADDY_HSTS=true
```

Write the DNS provider API token to the token file (needs `Zone:DNS:Edit` on
the apex domain) and lock it down:

```bash
mkdir -p /var/lib/skygate/secrets
printf '%s\n' '<your-DNS-provider-API-token>' > /var/lib/skygate/secrets/caddy-dns-token
chmod 600 /var/lib/skygate/secrets/caddy-dns-token
```

`deploy/deploy.sh` then: renders `deploy/templates/Caddyfile.tmpl` to
`${DEPLOY_SKYGATE_DIR}/caddy/Caddyfile`, writes
`${DEPLOY_SKYGATE_DIR}/caddy/caddy.env` (mode `0600`, only
`CADDY_DNS_TOKEN_VALUE`), warns for any vhost that does not resolve, and
appends `--profile caddy` to the `docker compose up -d` call. For HTTP-01
(`CADDY_DNS_PROVIDER=http`) no token is needed but port 80 must be reachable
from the public Internet.

Rendered Caddy routing (the same logic for every deployment shape):

```caddyfile
(common) {
    encode zstd gzip
    header Strict-Transport-Security "max-age=15552000; includeSubDomains; preload"
    tls {
        dns cloudflare           # or nothing at all for HTTP-01
    }
}

head.example.com {
    import common
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

headplane.example.com {
    import common
    reverse_proxy headplane:50445
}

derp.example.com {
    import common
    reverse_proxy 127.0.0.1:443
}
```

Notes:

- The `/oidc/*` path on the **headscale** hostname is headscale's own OIDC
  callback (`/oidc/callback`), not skygate's provider endpoints. skygate's
  provider endpoints live on the **skygate/issuer** hostname and are served
  by the catch-all `reverse_proxy skygate:8080`.
- `derp.example.com` proxies to `127.0.0.1:443` because the bundled derper
  runs with `network_mode: host`; if you attach derper to the Docker network
  instead, point Caddy at `derper:443`.
- If you want headplane behind TLS, set
  `HEADPLANE_SERVER__COOKIE_SECURE=true` so the session cookie is
  `Secure; HttpOnly; SameSite=Lax`.

If the Caddy container crash-loops with `module not registered:
dns.providers.cloudflare`, you are running the stock `caddy:2-alpine` image;
the shipped `Dockerfile.caddy` builds an image with the DNS plugin baked in.
For `CADDY_DNS_PROVIDER=http` the stock image is fine.

### 2.2 External nginx / Nginx Proxy Manager

Use this when an existing reverse proxy already terminates TLS in front of
the skygate VM (NPM on a separate host is the most common case).

**Nginx Proxy Manager (web UI).** Create one Proxy Host per public hostname
and route the OIDC paths with Custom Locations. With a fronting proxy on
`<PROXY_HOST>` and skygate on `<VM_HOST>:8080`:

| Field (Details tab) | Value |
|---|---|
| Domain Names | `skygate.example.com` |
| Scheme | `http` |
| Forward Hostname / IP | `<VM_HOST>` |
| Forward Port | `8080` |
| Cache Assets | on |
| Block Common Exploits | on |
| Websockets Support | on |
| Access List | `Public` |

SSL tab: request a new Let's Encrypt certificate (HTTP-01 — port 80 must be
reachable), enable **Force SSL** and **HTTP/2**, and enable **HSTS** only
after the certificate is confirmed working.

Paste the following into the **Advanced → Custom Nginx Configuration**
textarea of the `skygate.example.com` host. Discovery and JWKS are cached for
an hour; `/oidc/` gets a 60 s read timeout (the browser is in the loop).
Add the two `/admin/oidc` locations with the same header set — the
operator pages need no tuning, except `/admin/oidc/sync`, whose Apply POST
synchronously pushes the config to headscale and waits for its health check,
so give it `proxy_connect_timeout 5s; proxy_send_timeout 130s;
proxy_read_timeout 130s;`.

```nginx
# --- 1. OIDC discovery (cached 1h) ---
location = /.well-known/openid-configuration {
    proxy_pass http://<VM_HOST>:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Connection "";
    expires 1h;
    add_header Cache-Control "public, max-age=3600";
}

# --- 2. OIDC JWKS (cached 1h) ---
location = /oidc/jwks.json {
    proxy_pass http://<VM_HOST>:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Connection "";
    expires 1h;
    add_header Cache-Control "public, max-age=3600";
}

# --- 3. /oidc/ — authorize, token, userinfo (60s timeouts) ---
location /oidc/ {
    proxy_pass http://<VM_HOST>:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Connection "";
    proxy_connect_timeout 5s;
    proxy_send_timeout 60s;
    proxy_read_timeout 60s;
}

# --- 4. /admin/oidc and /admin/oidc/sync (operator pages) ---
location /admin/oidc {
    proxy_pass http://<VM_HOST>:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Connection "";
}
```

The headscale host (`head.example.com`) needs its own Proxy Host forwarding
to `<VM_HOST>:50444`, plus one extra location so the OIDC callback is routed
when the gRPC-gateway port serves both `/api/v1/*` and `/oidc/callback`:

```nginx
# inside the server { listen 443 ssl; server_name head.example.com; ... } block,
# OUTSIDE the location rules above — declare the upstream once:
upstream headscale_backend_50444 {
    server <VM_HOST>:50444;
    keepalive 32;
}

# ... and inside the server block:
location = /oidc/callback {
    proxy_pass http://headscale_backend_50444;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Connection "";
    proxy_connect_timeout 5s;
    proxy_send_timeout 30s;
    proxy_read_timeout 30s;
}
```

Reuse the existing headscale upstream declaration if the config already has
one (only the upstream *name* must match); do not declare it twice.

**Raw nginx (no NPM).** The ready-to-paste server block for the skygate
issuer hostname — the equivalent of what NPM writes when you paste the custom
locations — is:

```nginx
# Upstream: where skygate actually runs. <VM_HOST> is the skygate VM's
# address as seen by this nginx. Never point this at the public address —
# that would loop back through the terminator.
upstream skygate_backend {
    server <VM_HOST>:8080;
    keepalive 32;
}

# :80 — ACME HTTP-01 challenge + HTTP→HTTPS redirect.
server {
    listen 80;
    listen [::]:80;
    server_name skygate.example.com;

    location /.well-known/acme-challenge/ {
        root /var/www/html;
    }
    location / {
        return 301 https://$host$request_uri;
    }
}

# :443 — the skygate / OIDC vhost.
server {
    listen 443 ssl http2;
    server_name skygate.example.com;

    # Pick ONE certificate source.
    # A) certbot (auto-renewed by its systemd timer):
    ssl_certificate     /etc/letsencrypt/live/skygate.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/skygate.example.com/privkey.pem;
    # B) operator-uploaded certificate:
    # ssl_certificate     /etc/nginx/ssl/skygate.example.com.crt;
    # ssl_certificate_key /etc/nginx/ssl/skygate.example.com.key;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers 'ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305';
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;

    # Enable HSTS only after the certificate is verified working.
    # add_header Strict-Transport-Security "max-age=63072000; includeSubDomains; preload" always;

    access_log /var/log/nginx/skygate-access.log;
    error_log  /var/log/nginx/skygate-error.log warn;

    client_max_body_size 16m;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;

    location = /.well-known/openid-configuration {
        proxy_pass http://skygate_backend;
        expires 1h;
        add_header Cache-Control "public, max-age=3600";
    }

    location = /oidc/jwks.json {
        proxy_pass http://skygate_backend;
        expires 1h;
        add_header Cache-Control "public, max-age=3600";
    }

    location /oidc/ {
        proxy_pass http://skygate_backend;
        proxy_connect_timeout 5s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
    }

    location /admin/oidc/sync {
        proxy_pass http://skygate_backend;
        proxy_connect_timeout 5s;
        proxy_send_timeout 130s;
        proxy_read_timeout 130s;
    }

    location /admin/oidc {
        proxy_pass http://skygate_backend;
    }

    # Everything else: /login, /dashboard, /api/*, /static/*, …
    location / {
        proxy_pass http://skygate_backend;
    }
}
```

Apply and issue the certificate:

```bash
nginx -t
systemctl reload nginx
certbot certonly --nginx -d skygate.example.com     # writes the live/ cert + timer
systemctl reload nginx
```

If the HTTP-01 challenge fails, check that port 80 is reachable from Let's
Encrypt, that the A record points at the terminator, and that no more
specific `server_name` on port 80 is swallowing
`/.well-known/acme-challenge/`.

After the proxy is live, wire the skygate side (issuer URL + headscale push):

```bash
bash deploy/scripts/setup-skygate-public.sh --issuer https://skygate.example.com
```

That script validates the new issuer, updates `SKYGATE_OIDC_ISSUER` and
`SKYGATE_OIDC_REDIRECT_URIS` in `.env` (with a
`.env.pre-setup-public.<timestamp>` backup), restarts the skygate container,
verifies the discovery document reports the new issuer, then calls
`deploy/oidc-sync.sh` to push the `oidc:` block to headscale. It is
idempotent and takes `--redirect`, `--client-id`, `--env`,
`--headscale-config`, `--headscale-container` and `--skygate-service`
overrides.

### 2.3 Direct, Tailscale TLS, native headscale TLS

**Tailscale TLS (tailnet-only).** If every visitor is a tailnet member you
can skip public ACME entirely:

```bash
sudo tailscale cert head.example.com
# → /var/lib/tailscale/cert.pem, /var/lib/tailscale/key.pem
```

Mount the files into the proxy (or into skygate) and use them as the
certificate source. This only works for tailnet members; a friend outside the
tailnet cannot use it.

**Native headscale TLS (no proxy for the API).** headscale can terminate
TLS itself. Issue a certificate with any ACME client, place the files where
the headscale container can read them, and set in the rendered headscale
config:

```yaml
tls:
  cert_path: /etc/headscale/tls/cert.pem
  key_path:  /etc/headscale/tls/key.pem

grpc_allow_insecure: false
```

`grpc_allow_insecure: false` is mandatory in this shape — Tailscale clients
refuse a plaintext gRPC control channel. Keep `grpc_allow_insecure: true`
only when a proxy terminates TLS in front (the internal hop is then inside
the Docker network).

**Direct exposure.** Serving `:8080` (or `:50444`) on the public Internet
without a terminator is not supported: the browser will show mixed-content
warnings, iOS/Android clients refuse the control plane, and the OIDC issuer
would have to be `http://`. Use one of the options above.

### 2.4 Forwarded-header contract

Every terminator must send, on every request:

```nginx
proxy_set_header Host              $host;
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-Host  $host;
proxy_set_header X-Forwarded-Port  $server_port;
proxy_set_header Connection        "";
```

`X-Forwarded-For` **is** consumed by skygate (rate limiting and the Telegram
webhook client-IP display). `X-Forwarded-Proto` / `X-Forwarded-Host` are
**not** read by skygate today: the OIDC issuer is the literal
`SKYGATE_OIDC_ISSUER` value, and the login redirect uses the request `Host`
header. The old runbook and the nginx snippet claim skygate derives the
scheme from `X-Forwarded-Proto` — that is stale. Keep sending the headers
(they are standard, and any future proxy-awareness or logging will want
them), but treat `SKYGATE_OIDC_ISSUER` as the single source of truth for the
issuer URL (see [§4.2](#42-failure-modes-symptom--cause--fix)).

---

## 3. DNS / ports checklist and certificate renewal

### 3.1 DNS records

Replace `example.com` and the addresses with your own values.

```dns
skygate.example.com.      A     <PUBLIC_HOST>   # skygate portal + OIDC provider (issuer)
head.example.com.         A     <PUBLIC_HOST>   # headscale control plane + /oidc/callback
headplane.example.com.    A     <PUBLIC_HOST>   # optional admin UI
derp.example.com.         A     <PUBLIC_HOST>   # optional bundled DERP relay
```

- The **issuer** hostname and the **headscale** hostname must both resolve to
  the terminator, and both must be covered by certificates. They are usually
  different hostnames (`skygate.example.com` vs `head.example.com`); the same
  hostname with path-based routing also works, as long as `/oidc/callback` is
  routed to headscale and the OIDC provider paths to skygate.
- For DNS-01 (recommended when port 80 cannot be public) you need the
  provider API to create `_acme-challenge.<apex>` TXT records; Caddy/NPM
  clean them up automatically.
- The DERP hostname's A record must match the certificate CN/SAN, because
  derper uses `--hostname` to find `<certdir>/<hostname>.{crt,key}` and
  clients verify the relay's certificate against that name.

### 3.2 Inbound ports

| Port | Proto | Needed by | Public? |
|---|---|---|---|
| 80 | TCP | ACME HTTP-01 (Caddy `CADDY_DNS_PROVIDER=http`, NPM, certbot); derper `--http-port` (default `80`) HTTP→HTTPS redirect and its own HTTP-01 when `DERP_CERTMODE=letsencrypt` | only when HTTP-01 is used |
| 443 | TCP | the TLS terminator; the bundled derper's HTTPS/DERP listener (`--a=:443`) | yes |
| 3478 | UDP | bundled derper STUN (`--stun`, `DERP_STUN_PORT`) | yes (if DERP enabled) |
| 8765 | TCP | bundled `derpmap` container publishing `derpmap.json` (`DERP_MAP_PORT`) | only if clients fetch the map directly |
| 8080 | TCP | skygate backend | no (terminator→backend) |
| 50443 | TCP | headscale gRPC (`/ts2021`, `/machine`, `/key`) | no (terminator→backend) |
| 50444 | TCP | headscale JSON API + `/oidc/callback` | no (terminator→backend) |
| 50445 | TCP | headplane | no (or tailnet-only) |
| 5432 | TCP | PostgreSQL | no |
| 81 | TCP | NPM admin UI on `<PROXY_HOST>` | no |

Check what is actually listening on a host:

```bash
ss -tlnp | grep -E ':(80|443|8080|50443|50444|50445)\b'
ss -ulnp | grep ':3478\b'
```

Confirm which mode you are in (this decides the troubleshooting path):

```bash
# Is the in-compose Caddy running?
docker ps --format '{{.Names}}\t{{.Status}}\t{{.Ports}}' | grep caddy
#   empty   → Caddy is off (the default); an external terminator should be in front
#   present → Caddy is on; there must NOT be a second public terminator on this host

# Does anything else own 80/443?
ss -tlnp | grep -E ':(80|443)\b' | grep -v caddy
```

### 3.3 Certificate renewal

| Terminator | Who renews | What to check |
|---|---|---|
| Caddy sidecar | Caddy (ACME, automatic; no restart needed on renewal) | `docker logs caddy` shows "certificate obtained" for each vhost within ~2 minutes of the first start; cert state persists in the `caddy-data` volume. |
| NPM | NPM (built-in Let's Encrypt client, HTTP-01) | The certificate row shows an expiry date; NPM renews and reloads openresty itself. |
| certbot / nginx | the certbot systemd timer | `systemctl list-timers | grep certbot`; after a manual renewal run `systemctl reload nginx`. |
| derper (`DERP_CERTMODE=letsencrypt`) | derper itself (ACME HTTP-01 on `--http-port`) | `/admin/derp` shows "expires in X days"; skygate only reads `NotAfter`. |
| derper (`DERP_CERTMODE=manual`, cert from NPM) | **skygate** via the `derp_cert_sync` daily cron (see [§3.4](#34-derp-certificate-sync-path)) | `/admin/derp` → Cert auto-renewal section: last sync time, expiry, last error. |
| derper (paid/private CA) | operator | `mode=manual` is a no-op for skygate; it only surfaces the expiry warning before derper breaks. |

### 3.4 DERP certificate sync path

The bundled relay's certificate lives at
`<cert_dir>/<hostname>.crt` + `<cert_dir>/<hostname>.key`, with `cert_dir`
defaulting to `/var/lib/derper/certs`. derper reads it via
`--certdir=${DERP_CERT_DIR}` (mounted read-only into the container). The
`derp_cert_sync` table (one row per relay hostname) drives a daily cron in
skygate that keeps that pair fresh.

**Three modes:**

| Mode | When | What skygate does |
|---|---|---|
| `letsencrypt` | derper runs `--certmode=letsencrypt` and the HTTP-01 challenge on `--http-port` is reachable | **No-op.** derper renews by itself; skygate parses `NotAfter` and surfaces "expires in X days". |
| `npm` | derper runs `--certmode=manual` and the certificate comes from Nginx Proxy Manager | Every 24 h: log in to the NPM API (`POST /api/tokens` at `npm_base_url`), `GET /api/nginx/certificates/<npm_cert_id>/download`, extract `fullchain*.pem` + `privkey*.pem`, SHA256 the fullchain and compare with `last_cert_sha256`; when it changed, write `<cert_dir>/<hostname>.crt` (`0644`) and `<cert_dir>/<hostname>.key` (`0600`), then reload derper. |
| `manual` | paid CA / private CA / air-gapped certificate | **No-op.** skygate only reads `NotAfter` so the operator sees the warning before the relay breaks. |

**What writes the cert → where it lands → how derper picks it up:**

1. **Writer.** `mode=npm`: skygate's cert-sync cron
   (`StartCertSyncCron` / `SyncOne`). `mode=letsencrypt`: derper itself.
   `mode=manual`: the operator.
2. **Location.** `<cert_dir>/<hostname>.crt` and `.key`; default
   `cert_dir=/var/lib/derper/certs`. Note that the HA certsync path (B147)
   writes to a *different* directory (`/var/lib/skygate/certs`); if you use
   both, keep them in sync or point `cert_dir` at the directory your derper
   actually reads.
3. **Reload.** After writing a changed certificate skygate calls the reload
   strategy in order: `systemctl reload <derper_systemd_unit>` (default
   `derper.service`), then `/bin/kill -HUP $(cat <derper_pid_file>)` (default
   `/var/run/derper.pid`). SIGHUP is a graceful reload — existing DERP
   connections are **not** dropped, unlike the old hand-rolled
   `systemctl restart derper` which caused a 1–3 s blip.
4. **Docker path.** The containerized derper mounts
   `${DERP_CERT_DIR}:/var/lib/derper/certs:ro`, so new files are visible
   immediately. From inside the skygate container `systemctl` is usually
   unavailable and the host PID is not visible, so the SIGHUP strategies may
   fail; if `/admin/derp` reports a reload error, re-read the files with
   `docker restart derper` (a short DERP blip) or reload the host unit.

**Row configuration.** There is no add-row UI yet — insert the row with SQL
(or `deploy.sh --add-cert-sync` where available):

```sql
-- Mode 'letsencrypt' (no extra fields):
INSERT INTO derp_cert_sync (hostname, mode, cert_dir, enabled)
VALUES ('derp.example.com', 'letsencrypt', '/var/lib/derper/certs', 1);

-- Mode 'npm' (requires the NPM base URL + certificate id):
INSERT INTO derp_cert_sync (hostname, mode, npm_base_url, npm_cert_id,
                            cert_dir, derper_systemd_unit, enabled)
VALUES ('derp.example.com', 'npm',
        'http://<PROXY_HOST>:81', 33,
        '/var/lib/derper/certs', 'derper.service', 1);

-- Mode 'manual':
INSERT INTO derp_cert_sync (hostname, mode, cert_dir, enabled)
VALUES ('derp.example.com', 'manual', '/var/lib/derper/certs', 1);
```

NPM credentials live in `global_settings` so they can be rotated from
`/admin/derp/cert-sync/edit` without a restart (`global_settings` wins over
the `NPM_IDENTITY` / `NPM_SECRET` environment variables):

```sql
INSERT INTO global_settings (key, value) VALUES
  ('derp.npm.identity', '<npm_email>'),
  ('derp.npm.secret',   '<npm_password>');
```

**"Sync now".** `POST /admin/derp/cert-sync/run` runs `SyncOne` for every
enabled row once and redirects back to `/admin/derp`. Use it after rotating
NPM credentials, after a manual upstream CA rotation, or after fixing an
`npm login returned 401` error.

**Making skygate reach derper on the same host.** If the host resolves
`derp.example.com` to `127.0.0.1` (typical with `systemd-resolved` +
`/etc/hosts`), the skygate container inherits that resolver and its probe
lands on the container's own loopback, where nothing listens. Fix it with an
operator-side `extra_hosts` entry on the skygate service:

```yaml
services:
  skygate:
    extra_hosts:
      - "derp.example.com:${SKYGATE_DERP_PROBE_HOST:-127.0.0.1}"
```

`SKYGATE_DERP_PROBE_HOST` belongs in `.env` (gitignored) and should hold the
host's real address as seen from the container. On a non-Docker deployment,
an `/etc/hosts` entry mapping `derp.example.com` to the host address has the
same effect. Without it `/admin/derp` reports the DERP socket as closed even
though derper is healthy. `extra_hosts` is part of the container's network
config, so it only takes effect after
`docker compose up -d --force-recreate --no-deps skygate` — a plain
`docker compose restart skygate` will not apply it.

---

## 4. Verification and failure modes

### 4.1 Verification commands

Replace `skygate.example.com`, `head.example.com` and `derp.example.com`
with your hostnames.

```bash
# 1. The certificate chain is what you expect.
openssl s_client -connect skygate.example.com:443 -servername skygate.example.com \
    </dev/null 2>/dev/null | openssl x509 -noout -subject -issuer -dates
# expected: subject=CN = skygate.example.com, issuer=… R1x/R3 (Let's Encrypt)

# 2. HTTP → HTTPS redirect.
curl -sI http://skygate.example.com/ | head -3
# expected: HTTP/1.1 301/308 … Location: https://skygate.example.com/

# 3. Portal responds over HTTPS.
curl -sS -o /dev/null -w '%{http_code}\n' https://skygate.example.com/login       # 200
curl -sS -o /dev/null -w '%{http_code}\n' https://skygate.example.com/healthz     # 200

# 4. OIDC provider endpoints (the paths headscale and clients use).
curl -sS https://skygate.example.com/.well-known/openid-configuration | jq .issuer
# expected: "https://skygate.example.com" (no trailing slash)
curl -sS -o /dev/null -w '%{http_code}\n' https://skygate.example.com/oidc/jwks.json   # 200
curl -sS -o /dev/null -w 'code=%{http_code} loc=%{redirect_url}\n' \
  "https://skygate.example.com/oidc/authorize?response_type=code&client_id=headscale&redirect_uri=https%3A%2F%2Fhead.example.com%2Foidc%2Fcallback&state=test&scope=openid+profile+email"
# expected: code=302 loc=https://skygate.example.com/login?next=…
curl -sS -o /dev/null -w '%{http_code}\n' https://skygate.example.com/oidc/userinfo   # 401

# 5. headscale callback is routed (a fake code must reach headscale and be rejected).
curl -sS -o /dev/null -w '%{http_code}\n' https://head.example.com/oidc/callback      # 400

# 6. headscale JSON API is reachable through the terminator.
curl -sS -o /dev/null -w '%{http_code}\n' https://head.example.com/api/v1/node       # 200 (or 401 unauthenticated)

# 7. HSTS is present when enabled.
curl -sI https://skygate.example.com/ | grep -i strict-transport

# 8. DERP relay answers on HTTPS (426 = "DERP requires connection upgrade").
curl -sk https://derp.example.com/derp -H 'Host: derp.example.com' -o /dev/null -w '%{http_code}\n'

# 9. A real Tailscale client registers through the whole chain.
tailscale up --login-server https://head.example.com
```

### 4.2 Failure modes: symptom → cause → fix

| Symptom | Cause | Fix |
|---|---|---|
| Browser reports mixed content / assets fail to load after enabling HTTPS | The page is served over HTTPS but generates `http://` URLs — usually the OIDC issuer or `SKYGATE_CONTROL_URL` is still `http://…` (or a bare hostname) | Set `SKYGATE_OIDC_ISSUER=https://…`, `SKYGATE_CONTROL_URL=https://head.example.com`, `HEADSCALE_SERVER_URL=https://head.example.com`; restart skygate so the discovery doc re-renders. |
| Endless redirect loop between `/login` and `/oidc/authorize` | The proxy strips or rewrites the `Host` header (skygate falls back to the socket host), or the session cookie is not accepted | Send `Host $host` (and `X-Forwarded-Host`); make sure the cookie domain matches the public hostname; verify `SKYGATE_JWT_SECRET` is stable across restarts. |
| headscale logs `issuer mismatch`; discovery advertises `http://…` or the wrong host | `SKYGATE_OIDC_ISSUER` does not match `oidc.issuer` (trailing slash, `http` vs `https`, or the container is still on the old `.env`) | Set both to the identical `https://` URL without a trailing slash, restart skygate, re-run `deploy/scripts/setup-skygate-public.sh` (idempotent), then re-apply the headscale block. |
| Wrong issuer because `X-Forwarded-Proto` is missing | The old runbook and nginx snippet state that skygate derives the scheme from `X-Forwarded-Proto`. It does not — skygate never reads that header, and the issuer is the literal `SKYGATE_OIDC_ISSUER` | Send `X-Forwarded-Proto $scheme` anyway (standard, useful for logs and future proxy-aware code), but fix the actual cause: set `SKYGATE_OIDC_ISSUER` to the full `https://…` URL and restart skygate. |
| `/admin/derp` shows `DERPER-SERVICE: stopped` although derper runs | The probe speaks plain HTTP to a TLS-only listener: derper rejects it with `Client sent an HTTP request to an HTTPS server`. Older probes also used a non-routable placeholder address | The current probe detects the `https://` scheme and uses TLS with the relay's SNI; verify `/admin/derp` is on a build that includes it, and confirm the relay answers with `curl -sk https://derp.example.com/derp`. |
| `/admin/derp` shows the DERP socket closed inside a container, but it works from the host | The container's resolver maps the public relay hostname to `127.0.0.1` (inherited from the host's `systemd-resolved` / `/etc/hosts`), so the probe hits its own loopback | Add the `extra_hosts` entry from [§3.4](#34-derp-certificate-sync-path) and `docker compose up -d --force-recreate --no-deps skygate`; a plain `restart` will not apply it. |
| `502 Bad Gateway` from the proxy | The backend is unreachable on `<VM_HOST>:8080` (or `:50444`) from the proxy host | From the proxy host: `curl http://<VM_HOST>:8080/healthz`; check firewall / VPC ACL between `<PROXY_HOST>` and `<VM_HOST>`. |
| `525` / `526` TLS errors in the browser | A CDN/proxy (Cloudflare) is in front with the wrong SSL mode | Pause the CDN proxy (grey cloud) or set SSL mode to **Full**, and ensure the origin certificate is valid. |
| Certificate issuance hangs or fails with `forbidden by policy` | The vhost is still a placeholder (`head.example.com`) or DNS does not point at the terminator; Caddy logs "forbidden by policy" | Set `CADDY_HOSTS_*` (or the NPM domain) to a real hostname whose A record points at the terminator, then re-run deploy. |
| Public site returns `SSL alert 80 internal_error` / `internal_error` with no obvious cause | Two terminators are competing for 80/443 — typically the in-compose Caddy plus an external NPM | Keep exactly one: set `CADDY_ENABLED=false` and `docker compose stop caddy && docker compose rm -f caddy`, or remove the external terminator. |
| iOS/Android Tailscale client refuses to log in | The control plane is plain HTTP or the certificate is not trusted | Terminate TLS with a publicly trusted certificate on the headscale hostname; verify with `openssl s_client` and `tailscale up --login-server https://head.example.com`. |
| `tailscale up` opens the browser, login succeeds, but the device never appears | The `oidc:` block is missing from the headscale config (the callback never completes) | Re-run the OIDC sync ([`oidc.md`](oidc.md) §2) and confirm `https://head.example.com/oidc/callback` returns 400 (not 404) for a fake code. |

---

## 5. Rollback

Rollback is about restoring a **known-working** combination rather than
"turning HTTPS off". Pick the layer that broke.

**Caddy sidecar was enabled by mistake (or broke the site).**

```bash
cd /home/admin/skygate
docker compose stop caddy
docker compose rm -f caddy
echo "CADDY_ENABLED=false" >> .env          # or: sed -i 's/^CADDY_ENABLED=.*/CADDY_ENABLED=false/' .env
bash scripts/rebuild_deploy.sh               # no caddy is started, 80/443 stay free
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz   # 200
```

Then point the external terminator back at `<VM_HOST>:8080` (NPM: Proxy Hosts
→ edit → Forward Port `8080`) and verify from outside.

**Issuer / OIDC wiring was changed.** `setup-skygate-public.sh` and
`deploy/oidc-sync.sh` both write timestamped backups:

```bash
ls -1 /home/admin/skygate/.env.pre-setup-public.* /home/admin/skygate/.env.pre-oidc-sync.*
cp -p /home/admin/skygate/.env.pre-setup-public.<timestamp> /home/admin/skygate/.env
docker compose up -d --force-recreate --no-deps skygate
curl -sS http://127.0.0.1:8080/.well-known/openid-configuration | jq .issuer
```

headscale config backups are `<headscale-config>.pre-oidc-sync.<timestamp>`;
restore the file, restart headscale (or use the `download`/`manual` mode of
`/admin/oidc/sync` to inspect the generated block first), and confirm
`https://head.example.com/api/v1/node` still answers.

**DERP certificate path broke.** The relay keeps serving the old certificate
until it is told to reload, so a failed sync is not an outage by itself:

1. Restore the previous cert pair from your backup (or re-download it from
   NPM) into `<cert_dir>`.
2. Reload the relay: `sudo systemctl reload derper` (systemd) or
   `docker restart derper` (container). Plain `restart` drops DERP
   connections for 1–3 s; SIGHUP does not.
3. Disable the failing row so the cron stops retrying until you have fixed
   the credentials: `UPDATE derp_cert_sync SET enabled = 0 WHERE hostname = 'derp.example.com';`
4. Verify: `curl -sk https://derp.example.com/derp -o /dev/null -w '%{http_code}\n'`
   → `426`, and `sudo tailscale debug derp 900` → "Successfully established a
   DERP connection".

**Full TLS rollback (back to the previous terminator).** Keep the previous
proxy configuration (NPM export, nginx site file, Caddyfile) under version
control or in a backup directory. To revert: stop the new terminator, restore
the previous config file, reload it, and confirm the three probes from
[§4.1](#41-verification-commands) (`/login` 200, discovery issuer correct,
`/oidc/callback` 400 for a fake code). Because skygate and headscale both
speak plain HTTP internally, the rollback never requires a database change —
only the proxy config and the `.env` issuer value.
