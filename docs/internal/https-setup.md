# HTTPS / TLS setup for Skygate

> How to serve every Skygate module over HTTPS without a
> full reverse-proxy UI (no nginx Proxy Manager, no
> Traefik dashboard). The default deployment uses
> **Caddy** as a tiny TLS-terminating reverse proxy; the
> document also covers two alternatives for tailnet-only
> and "headscale-only" use cases.

---

## TL;DR — Caddy is OFF by default (since v0.32.11)

**Before you read the rest of this doc, know this:**

```bash
# In /home/admin/skygate/.env
CADDY_ENABLED=false   # this is the new default as of 2026-07-31
```

If you have an **external** TLS terminator already
(Nginx Proxy Manager on a different host — see
[§ Alternative: Nginx Proxy Manager (NPM)](#alternative-nginx-proxy-manager-npm-on-a-separate-vm)
below — Cloudflare Tunnel in front of this VM,
Caddy on a separate VM, Tailscale TLS, an ALB in
front, …), leave it at `false` and **do nothing
else**. The deploy script will not start the caddy
container, will not bind ports 80/443, will not
render a Caddyfile. Your external terminator keeps
working with no change.

If you want **this VM's Caddy to issue and renew
Let's Encrypt certs**, opt in:

```bash
# In /home/skygate/skygate/.env
CADDY_ENABLED=true                          # was false
CADDY_HOSTS_HEAD=head.your-real-domain.tld  # NOT example.com
CADDY_DNS_PROVIDER=cloudflare               # or "http" for HTTP-01
echo "<your-cloudflare-api-token>" > /var/lib/skygate/secrets/caddy-dns-token
chmod 600 /var/lib/skygate/secrets/caddy-dns-token
# then re-run deploy.sh — caddy starts under the
# `caddy` profile and gets a cert on first request
```

If you don't know which case you're in, read the
next section.

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
* Native ACME support for wildcard certs (`*.example.com`)
  with one DNS-01 credential.

If you prefer nginx + certbot, the same architecture works;
the differences are at the proxy-config-file level, not in
the modules.

---

## Caddy is off by default — the why and the opt-in

### Why we flipped the default (2026-07-31)

Until v0.32.10, `CADDY_ENABLED=true` was the default.
The deploy script rendered a Caddyfile with placeholder
hostnames (`head.example.com`, `headplane.example.com`,
`derp.example.com`) and started a caddy sidecar on
ports 80/443, expecting the operator to either (a) edit
`.env` to point at real domains before the first
`rebuild_deploy`, or (b) accept that caddy would
crash-loop on ACME issuance for `example.com` (a domain
reserved by RFC 2606, which Let's Encrypt refuses to
issue for) and then notice + fix it.

In practice, both expectations failed silently:

* **Most operators already run an external TLS
  terminator.** Nginx Proxy Manager on a separate host
  in the same LAN, Cloudflare Tunnel in front of the
  public IP, Tailscale TLS for tailnet-only access, a
  Caddy on a separate VM, an AWS / GCP / Yandex
  Application Load Balancer in front. None of these
  need an in-container caddy. But the deploy started
  one anyway, on ports 80/443, where it would
  intercept the external terminator's traffic and
  return an opaque `SSL alert 80 internal_error`
  because it had no cert to serve.
* **The placeholder hostnames masked the problem.**
  `head.example.com` made the Caddyfile *look* correct
  to the operator — it had all the right vhost blocks,
  reverse_proxy directives, HSTS header, even a DNS-01
  challenge config. The Caddy logs said
  `forbidden by policy` but the error happened during
  ACME issuance, hours after deploy, so by the time the
  operator noticed their site was down, they had
  forgotten caddy was even running.
* **CI doesn't catch it.** `make verify-pre` and the
  GitHub Actions `verify-pre` job don't talk to Let's
  Encrypt; they only check Go code, ACL rules, and
  shell-script lints. A broken Caddyfile with a real
  domain or a missing DNS token is invisible to CI.

The result was a silent outage: the operator
(2026-07-31) reported "ci github собрался но сайт
skygate не открывается" — and the root cause was that
the in-container caddy was sitting in front of NPM
silently returning `internal_error` because it
couldn't issue a cert for the placeholder hostname.

### What the new default changes

Three coordinated changes, all in v0.32.11:

1. **`deploy/deploy.sh:124`** — the
   `${CADDY_ENABLED:-true}` default is now
   `${CADDY_ENABLED:-false}`. The script logs a
   one-liner at deploy time so the choice is visible.
2. **`docker-compose.yml` caddy service** — moved
   under `profiles: ["caddy"]`. A plain
   `docker compose up -d` no longer starts caddy at
   all. To start it, the deploy script appends
   `--profile caddy` to the `up -d` invocation.
3. **`.env.example`** — the CADDY_ENABLED example now
   reads `false` and the comment explains both modes
   with copy-pasteable opt-in steps.

The Caddyfile template (`deploy/templates/Caddyfile.tmpl`),
the caddy Docker image build (`Dockerfile.caddy`),
and the `caddy-data` / `caddy-config` volumes stay in
the repo — they aren't deleted. An operator who opts
in gets the exact same setup as before v0.32.11, just
with an extra `true` in `.env`.

### How to tell which mode you're in (operator-side check)

```bash
ssh admin@192.0.2.1
cd /home/admin/skygate

# 1. Is caddy running on this VM?
docker ps --format '{{.Names}}\t{{.Status}}\t{{.Ports}}' | grep caddy
# empty  → you're already in the new default (caddy is OFF)
# present → caddy is ON; check the rest of the doc

# 2. Is there an external terminator in front of this VM?
#    - Nginx Proxy Manager on another host:    curl -fsS http://<npm-host>:81 | head -1
#    - Cloudflare Tunnel:                      cloudflared tunnel list
#    - Tailscale TLS:                          tailscale cert <hostname>
#    - An ALB / NLB with a cert:               see your cloud console
#    If you have one AND caddy is also on → conflict, fix one of them

# 3. Does anything on this host bind 0.0.0.0:80 or 0.0.0.0:443?
ss -tlnp | grep -E ':(80|443)\b'
# If only caddy shows up and there's no cert → caddy is the
# silent-outage culprit. Either set CADDY_ENABLED=false and
# re-deploy, or fix caddy (see opt-in below).
```

### Opt-in procedure (run caddy in front of skygate)

```bash
# 0. Make sure no other process is on ports 80/443 on this VM
ss -tlnp | grep -E ':(80|443)\b' | grep -v caddy
#  → empty required (kill anything else; caddy binds 0.0.0.0)

# 1. Edit .env — flip the flag + set your real domain
cd /home/admin/skygate
# (use whatever editor you prefer)
cat >> .env <<'EOF'
CADDY_ENABLED=true
CADDY_HOSTS_HEAD=head.your-domain.tld
CADDY_HOSTS_HEADPLANE=headplane.your-domain.tld
CADDY_HOSTS_DERP=derp.your-domain.tld
EOF
# CADDY_HOSTS_HEADPLANE / DERP are optional — only set them
# if you actually run those modules.

# 2a. If you have a Cloudflare / Route53 / etc. account (DNS-01)
mkdir -p /var/lib/skygate/secrets
echo "<your-DNS-provider-API-token>" > /var/lib/skygate/secrets/caddy-dns-token
chmod 600 /var/lib/skygate/secrets/caddy-dns-token
# Token needs Zone:DNS:Edit on the apex domain.
# (For Cloudflare: API Tokens → Create Token → Edit zone DNS → Zone Resources = <apex>)

# 2b. If port 80 is reachable from the public Internet (HTTP-01)
sed -i 's/^CADDY_DNS_PROVIDER=.*/CADDY_DNS_PROVIDER=http/' .env
# No token needed for HTTP-01.

# 3. Re-run deploy — caddy is added under --profile caddy
bash scripts/rebuild_deploy.sh

# 4. Verify (per-module TLS handshake)
curl -fsSv https://${CADDY_HOSTS_HEAD}/healthz 2>&1 | grep -E "subject|issuer|HTTP"
# expected: subject=CN = your-domain.tld, issuer=... R3/R10 (Let's Encrypt)
#           HTTP/2 200
```

### Opt-out procedure (already on caddy, want to switch to external)

```bash
# 1. Stop caddy and free ports 80/443
cd /home/admin/skygate
docker compose stop caddy
docker compose rm -f caddy

# 2. Flip the flag so future deploys don't bring it back
echo "CADDY_ENABLED=false" >> .env
# (or `sed -i 's/^CADDY_ENABLED=.*/CADDY_ENABLED=false/' .env`)

# 3. Point your external terminator at this VM's skygate port
#    (typically port 8080; check SKYGATE_PORT in .env)
#    For Nginx Proxy Manager: Proxy Hosts → Edit → Forward Port = 8080

# 4. Verify
curl -fsS http://localhost:8080/healthz
# expected: 200 + a build=... line
# (and from the outside, after the external terminator is
# repointed, the public hostname should resolve to a 200 too)
```

### When you'd still want caddy on (reasons to opt in)

* **Single-VM / home / SOHO deployments** where there
  is no other host to run NPM on and you want one
  container per concern. The Caddy sidecar is small
  (~150 MB custom image, vs. 70 MB nginx + 200 MB
  NPM + MySQL), auto-renews certs, and the operator
  doesn't need to think about it after the initial
  config.
* **Edge / remote VMs behind a strict firewall** that
  only allow the caddy container to make outbound
  DNS-01 calls. NPM and Cloudflare Tunnel both
  require a different egress pattern.
* **Greenfield deployments** where the operator
  hasn't picked a TLS strategy yet. Caddy on
  default is the lowest-friction "just give me
  HTTPS" option.

### When you'd definitely NOT want caddy on

* You have **Nginx Proxy Manager**, **Traefik
  Proxy**, **Caddy on a separate VM**, or any
  other reverse proxy already terminating TLS in
  front of skygate.
* You have a **Cloudflare Tunnel** in front of the
  public hostname (Cloudflare's edge terminates
  TLS; you don't need a second terminator behind
  it).
* You access skygate only over **Tailscale** (use
  `tailscale cert <hostname>` for tailnet-only
  TLS — see "Alternative: Tailscale TLS" below).
* The host has a **non-trivial egress firewall** that
  blocks the DNS-01 provider's API; opt-in caddy
  will then sit in a cert-issuance retry loop
  forever. Easier to disable and use HTTP-01 from a
  reverse proxy that you can debug at the
  process level.

---

## Architecture

```
                  ┌─────────────────────────────────────┐
                  │  TLS terminator                     │
                  │  (Caddy, NPM, Cloudflare, …)        │
                  │  • :443 HTTPS                       │
                  │  • Let's Encrypt or operator cert   │
                  │  • HSTS, OCSP stapling, HTTP→HTTPS │
                  └─────────────┬───────────────────────┘
                                │ plain HTTP
        ┌───────────────────────┼───────────────────────┐
        │                       │                       │
   head.example.com       headplane.example.com      derp.example.com
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
   │    because the terminator→headscale hop is on the  │
   │    internal Docker network, not the public         │
   │    Internet)                                       │
   └────────────────────────────────────────────────────┘
```

The "TLS terminator" box at the top is **Caddy (in
this container)** if you set `CADDY_ENABLED=true`, or
**whatever external terminator you already run**
(NPM, Cloudflare Tunnel, Tailscale TLS, an ALB, …)
if you leave it at the new `false` default. Both
modes are first-class; the rest of the diagram is
identical.

**Key insight:** the terminator→backend hop is on the
internal Docker network (or `127.0.0.1` if the
terminator runs on the host without Docker). The
operator's threat model is "the public Internet hits
only the terminator; everything else is internal".
The backends can run plain HTTP because nothing on
the public Internet can reach them directly.

---

## The operator's checklist

### 1. DNS records

You need an A record for each subdomain that will be
public. Replace `example.com` with your domain.

```dns
head.example.com.        A   <public-ip>     # skygate dashboard + headscale API
headplane.example.com.   A   <public-ip>     # (optional) headplane admin UI
derp.example.com.        A   <public-ip>     # (optional) self-hosted DERP
```

If you have only one public IP and one hostname, skip
the subdomains and use just `head.example.com` for
everything. Caddy will route by `Host:` header.

**For Let's Encrypt DNS-01** (recommended on private
subnets, no port-80 needed), add a wildcard record too:

```dns
*.example.com.   CNAME   head.example.com.   # only if you use a wildcard cert
_acme-challenge.example.com.   TXT   "<Caddy writes this during issuance>"
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
CADDY_HOSTS_HEAD=head.example.com
CADDY_HOSTS_HEADPLANE=headplane.example.com
CADDY_HOSTS_DERP=derp.example.com
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
`https://head.example.com`, which Tailscale clients use to
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
`https://head.example.com:443` and Caddy forwards the HTTP+JSON
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

# ─── head.example.com (skygate dashboard + headscale API) ───
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

# ─── headplane.example.com (admin UI) ───
${CADDY_HOSTS_HEADPLANE} {
    import common
    reverse_proxy headplane:50445
}

# ─── derp.example.com (DERP relay) ───
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
openssl s_client -connect head.example.com:443 -servername head.example.com \
    < /dev/null 2>/dev/null | openssl x509 -noout -subject -issuer -dates
# expected: subject=CN = head.example.com, issuer=CN = ... R3/R10 (Let's Encrypt)

# 2. HTTP→HTTPS redirect
curl -sI http://head.example.com/ | head -3
# expected: HTTP/1.1 308 Permanent Redirect
#           Location: https://head.example.com/

# 3. Dashboard loads over HTTPS
curl -sI https://head.example.com/login | head -3
# expected: HTTP/2 200 (or 302 to /dashboard)

# 4. Headscale API over HTTPS (the path the Tailscale
#    client dials when registering)
curl -sI https://head.example.com/api/v1/node | head -3
# expected: HTTP/2 200 (or 401 if you haven't authed)

# 5. HSTS header is set
curl -sI https://head.example.com/ | grep -i strict-transport
# expected: strict-transport-security: max-age=15552000; ...

# 6. Tailscale client can register
tailscale up --login-server=https://head.example.com
# expected: "Success" + a Tailscale IP from the headscale
#           prefix range (100.64.100.0/10)
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
sudo tailscale cert head.example.com
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
    -d head.example.com
sudo cp /etc/letsencrypt/live/head.example.com/fullchain.pem \
       /var/lib/headscale/tls/cert.pem
sudo cp /etc/letsencrypt/live/head.example.com/privkey.pem \
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

## When to choose NPM over Caddy (operator-side decision)

Both paths are supported. Pick the one that matches
your existing infrastructure:

| Situation | Use this path |
|-----------|---------------|
| This is a **fresh** Skygate install + this VM has the public IP | **Caddy** (default) — single binary, no extra config |
| You already run **Nginx Proxy Manager on a separate fronting VM** | **NPM** (this section) — reuse the existing web UI |
| You have an **ALB / Cloudflare Tunnel / Cloudflare proxy** in front | Leave `CADDY_ENABLED=false`, set the X-Forwarded-Proto at the cloud layer |
| Tailnet-only deployment (Tailscale TLS) | See [§ Alternative: Tailscale TLS](#alternative-tailscale-tls-no-certbot-no-dns-01-no-caddy) below |

The rest of this section is the **NPM** runbook. It
was written against the live verified setup on
2026-08-24 (operator VM: `95.165.170.190` public IP,
skygate VM internal: `192.168.13.69:8080`,
hostnames: `head.skynas.ru` + `skygate.skynas.ru`).

---

## Alternative: Nginx Proxy Manager (NPM) on a separate VM

This is the **fully verified** path for operators who
already run NPM on a fronting VM. NPM is a web UI
for managing nginx reverse proxies + Let's Encrypt
cert renewals, backed by a MariaDB database. It
runs on its own VM (typically on the public IP
that receives the operator's DNS A-records).

### Architecture (NPM path)

```
Public internet
     │
     ▼ DNS A-record: skygate.skynas.ru → 95.165.170.190
     ▼ DNS A-record: head.skynas.ru    → 95.165.170.190
     │
┌────┴─────────────────────────────┐
│ Fronting VM (NPM)                │  ← 95.165.170.190
│   openresty (NPM)                │
│   - skygate.skynas.ru:443 (TLS)  │  ← NPM issues + renews cert
│   - head.skynas.ru:443    (TLS)  │
└────┬─────────────────────────────┘
     │ 192.168.13.69:8080  (internal network)
     ▼
┌────┴─────────────────────────────┐
│ skygate VM (this project)         │  ← 192.168.13.69
│   - skygate container :8080       │     (skygate-skygate-1)
│   - headscale container :50444   │
│   - headplane container :50445   │
└──────────────────────────────────┘
```

### Step 1: Add a Proxy Host in NPM

`NPM web UI → Hosts → Proxy Hosts → Add Proxy Host`

**Details tab**:

| Field | Value |
|-------|-------|
| Domain Names | `skygate.skynas.ru` |
| Scheme | `http` |
| Forward Hostname / IP | `192.168.13.69` (the skygate VM's internal IP) |
| Forward Port | `8080` |
| Cache Assets | ✅ |
| Block Common Exploits | ✅ |
| Websockets Support | ✅ |
| Access List | `Public` |

**SSL tab**:

| Field | Value |
|-------|-------|
| SSL Certificate | `Request a new SSL Certificate` (Let's Encrypt) |
| Email | your email (for cert-expiry notifications) |
| Terms of Service | ✅ agree |
| Force SSL | ✅ |
| HTTP/2 | ✅ |
| HSTS | ✅ (enable after cert is verified) |

Click **Save**. NPM will obtain the cert via
HTTP-01 challenge on port 80, then reload its
own openresty. Takes 30-60 sec.

### Step 2: Custom Locations (Advanced tab)

`NPM → Hosts → Proxy Hosts → skygate.skynas.ru → Advanced tab`

Paste the following into the **Custom Nginx Configuration**
textarea. These 5 location rules override the default
forward for the OIDC-specific paths so we can tune
timeouts, cache headers, and proxy headers:

```nginx
# ============================================================
# OIDC-specific tuning for skygate.skynas.ru (B168)
# ============================================================

# --- 1. OIDC Discovery (cached 1h) ---
location = /.well-known/openid-configuration {
    proxy_pass http://192.168.13.69:8080;
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
    proxy_pass http://192.168.13.69:8080;
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

# --- 3. /oidc/ — authorize, token, userinfo (60s timeout) ---
location /oidc/ {
    proxy_pass http://192.168.13.69:8080;
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

# --- 4. /admin/oidc (operator-facing page) ---
location /admin/oidc {
    proxy_pass http://192.168.13.69:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Connection "";
}

# --- 5. /admin/oidc/sync (B167 Apply button, 130s timeout) ---
location /admin/oidc/sync {
    proxy_pass http://192.168.13.69:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Connection "";
    proxy_connect_timeout 5s;
    proxy_send_timeout 130s;
    proxy_read_timeout 130s;
}
```

Click **Save**. NPM reloads openresty with the
new config in 5-10 sec.

### Step 3: Run the B168 setup on the skygate VM

On the skygate VM, the project ships a one-shot
script that updates `SKYGATE_OIDC_ISSUER` in
`/home/skyadmin/skygate/.env`, restarts skygate,
verifies the new issuer, and pushes the new
`oidc:` block to `headscale.conf`:

```bash
cd /home/skyadmin/skygate
bash deploy/scripts/setup-skygate-public.sh --issuer https://skygate.skynas.ru
```

Expected output (5 steps + summary):

```
[setup] === setup-skygate-public.sh (B168) ===
[setup] issuer:        https://skygate.skynas.ru
[setup] redirect_uris: https://head.skynas.ru/oidc/callback
[setup] [1/5] validate https://skygate.skynas.ru is reachable
[setup]   https://skygate.skynas.ru/.well-known/openid-configuration returns 200
[setup] [2/5] update .env
[setup]   wrote SKYGATE_OIDC_ISSUER to /home/skyadmin/skygate/.env
[setup] [3/5] restart skygate container
[setup] [4/5] wait for skygate /healthz + verify new issuer
[setup] [5/5] push the new OIDC config to headscale (docker mode)
```

If the script fails at step 1 with `discovery returned
NNN`, the NPM cert isn't ready yet — wait 30 sec and
re-run. If step 4 fails with `skygate did not become
healthy within 30s`, run `chmod +x entrypoint.sh` once
on the skygate VM (this happens after `git reset --hard`).

### Step 4: End-to-end verification (from any external host)

```bash
# 1. discovery: 200 + correct issuer
curl -s https://skygate.skynas.ru/.well-known/openid-configuration | grep -o '"issuer":"[^"]*"'
# → "issuer":"https://skygate.skynas.ru"

# 2. JWKS: 200
curl -sk -o /dev/null -w '%{http_code}\n' https://skygate.skynas.ru/oidc/jwks.json
# → 200

# 3. authorize (unauth): 302 → /login
curl -sk -o /dev/null -w 'code=%{http_code} loc=%{redirect_url}\n' \
  "https://skygate.skynas.ru/oidc/authorize?client_id=headscale&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback&response_type=code&state=test&scope=openid+profile+email&code_challenge=abc&code_challenge_method=S256"
# → code=302 loc=https://skygate.skynas.ru/login?next=...

# 4. userinfo (no auth): 401 + WWW-Authenticate Bearer
curl -sk -o /dev/null -w '%{http_code}\n' https://skygate.skynas.ru/oidc/userinfo
# → 401

# 5. cross-check: headscale /oidc/callback reachable
curl -sk -o /dev/null -w '%{http_code}\n' https://head.skynas.ru/oidc/callback
# → 400 (reaches headscale, rejects fake code — correct)
```

All 5 should pass. If step 1 returns `skygate.example.com`
as the issuer, the skygate container is still on the
old `.env` — re-run `setup-skygate-public.sh` (idempotent).

### Step 5: Live Tailscale client test

```bash
# On a test device (phone, laptop, fresh VM):
tailscale up --login-server https://head.skynas.ru
```

The Tailscale client opens the browser to
`https://head.skynas.ru/oidc/authorize?client_id=headscale&...`
→ headscale 302s to `https://skygate.skynas.ru/oidc/authorize?...`
→ skygate 302s to `https://skygate.skynas.ru/login?next=...`
→ user types skygate admin creds → skygate 302s to
`https://skygate.skynas.ru/oidc/authorize?code=...`
→ skygate 302s to `https://head.skynas.ru/oidc/callback?code=...&state=...`
→ headscale exchanges the code, creates the user, returns
to the Tailscale client.

### NPM gotchas (lessons from the live verify)

| Symptom | Cause | Fix |
|---------|-------|-----|
| `502 Bad Gateway` from NPM | skygate VM unreachable on 192.168.13.69:8080 from fronting VM | `curl http://192.168.13.69:8080/healthz` from fronting VM; check firewall / VPC ACL |
| Cert issuance hangs at "pending" | Port 80 closed from internet on 95.165.170.190 | Open 80/tcp; check ISP / hosting provider firewall |
| `525 / 526` errors in browser | Cloudflare SSL mismatch (you have a CF proxy in front) | Pause CF proxy (grey cloud) OR set CF SSL = "Full" |
| `200 OK` but discovery shows `issuer: skygate.example.com` | skygate .env not updated | Run `setup-skygate-public.sh` on skygate VM |
| `302 → /login` but `next=` param is empty | `X-Forwarded-Proto` header missing from NPM | Verify the custom locations include `proxy_set_header X-Forwarded-Proto $scheme;` |
| `tailscale up` shows "connection refused" | headscale container down | `docker ps` on skygate VM; restart if needed |
| `tailscale up` opens browser, login succeeds, but device never appears in tailnet | headscale's `oidc:` block missing from config | Run `setup-skygate-public.sh` step 5 (oidc-sync) again |

### Files shipped in B168 for the NPM path

* `deploy/snippets/nginx-skygate-oidc.conf` — the
  canonical raw nginx config (the equivalent of
  what NPM writes when you paste the custom
  locations above). Useful if you migrate off NPM
  to raw nginx.
* `deploy/scripts/setup-skygate-public.sh` — the
  5-step setup script.
* `scripts/check_b168.sh` — 19 B-check contracts
  that pin the B168 surface.

---

## When NOT to use Caddy (legacy doc — see NPM section above)

The previous "Why not nginx + certbot?" section is
preserved for reference. nginx + certbot is
supported as a documented fallback — but **NPM is
the recommended path if you already have it
running**, not raw nginx. The Caddyfile shipped in
`deploy/templates/Caddyfile.tmpl` is the alternative
for fresh installs.

---

## Files added in v0.15.0

* `docs/https-setup.md` — this file
* `deploy/templates/Caddyfile.tmpl` — Caddyfile template
* `docker-compose.yml` — new `caddy` service when
  `CADDY_ENABLED=true`
* `.env.example` — new `CADDY_*` and `HEADPLANE_SERVER__COOKIE_SECURE`
  knobs

## Files added in v1.5.2 (B168 — NPM path)

* `docs/internal/https-setup.md` — new "Alternative:
  Nginx Proxy Manager (NPM)" section above
* `deploy/snippets/nginx-skygate-oidc.conf` — the
  canonical raw nginx config equivalent to what
  NPM generates from the custom-locations paste
* `deploy/scripts/setup-skygate-public.sh` — the
  5-step operator script (update .env, restart
  skygate, push to headscale, write audit row)
* `scripts/check_b168.sh` — 19 B-check contracts
  that pin the B168 surface

## Files NOT changed in v0.15.0

* `internal/acl/acl.go` — no change
* `internal/handlers/*` — no change
* `internal/telegram/*` — no change
* `internal/release/*` — no change
* `internal/monitoring/*` — no change

The HTTPS layer is entirely outside the Go process; it's
a deploy-time + Caddy-time concern.
