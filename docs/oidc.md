# OIDC integration: skygate ⇄ headscale

This document consolidates the internal runbook `oidc-headscale.md`, which
was removed in the 2026-09-18 documentation restructure; the full original
text stays in git history. It covers the two directions of OIDC in this
project, the headscale-facing provider setup end to end, every endpoint with a
copy-paste verification command, the login/tagging UX that matters
operationally, and rollback. Related reading: [`deploy.md`](deploy.md)
(env-var reference), [`https.md`](https.md) (public TLS / reverse proxy for
the issuer and callback), [`features.md`](features.md),
[`api.md`](api.md), [`troubleshooting.md`](troubleshooting.md),
[`acl-rules-reference.md`](acl-rules-reference.md).

## Table of contents

1. [The two directions of OIDC](#1-the-two-directions-of-oidc)
2. [Setup: skygate as headscale's OIDC provider](#2-setup-skygate-as-headscales-oidc-provider)
   - [2.1 Required configuration](#21-required-configuration)
   - [2.2 The generated headscale `oidc:` block](#22-the-generated-headscale-oidc-block)
   - [2.3 Field reference](#23-field-reference)
   - [2.4 Apply and restart headscale](#24-apply-and-restart-headscale)
   - [2.5 Boot-time auto-sync](#25-boot-time-auto-sync)
3. [Endpoints and smoke tests](#3-endpoints-and-smoke-tests)
   - [3.1 Endpoint summary](#31-endpoint-summary)
   - [3.2 Smoke test: discovery, jwks, authorize](#32-smoke-test-discovery-jwks-authorize)
   - [3.3 Token, userinfo and callback](#33-token-userinfo-and-callback)
4. [Client and login UX notes](#4-client-and-login-ux-notes)
   - [4.1 The `next` redirect](#41-the-next-redirect)
   - [4.2 What the operator sees on failure](#42-what-the-operator-sees-on-failure)
   - [4.3 Node auto-tagging for OIDC-registered nodes](#43-node-auto-tagging-for-oidc-registered-nodes)
5. [Verification checklist and troubleshooting](#5-verification-checklist-and-troubleshooting)
   - [5.1 End-to-end checklist](#51-end-to-end-checklist)
   - [Common e2e failures](#common-e2e-failures)
6. [Rollback](#6-rollback)

---

## 1. The two directions of OIDC

"OIDC in skygate" means two different things depending on who is the identity
provider. They share almost no code and must not be confused when debugging.

| | **Direction A — skygate is the IdP** | **Direction B — skygate is a relying party** |
|---|---|---|
| Who trusts whom | headscale (the service provider) trusts skygate as its OpenID Provider | skygate's portal login would trust an external IdP (Authentik, Keycloak, Google, …) |
| What skygate runs | the provider surface: discovery document, JWKS, `/oidc/authorize`, `/oidc/token`, `/oidc/userinfo`, plus the RS256 signing keypair | an OAuth2/OIDC *client*: redirect to the IdP, handle its callback, map claims to a `portal_users` row |
| Who logs in | a Tailscale client's user, in a browser, during `tailscale up` | an operator or portal user logging in to skygate itself |
| Where the tokens come from | skygate signs them (RS256); headscale verifies them against skygate's JWKS | the external IdP signs them; skygate would verify them |
| Status in this repo | **implemented** (B161.1–B161.4, plus B167/B168/B172/B174/B175) | **not implemented** |

**Direction A** is what the rest of this document is about. skygate's own
issuer URL (`SKYGATE_OIDC_ISSUER`) is the value headscale puts in
`oidc.issuer`; the callback URL that completes the flow belongs to **headscale**
(`https://head.example.com/oidc/callback`), not to skygate.

**Direction B** does not exist in the code today. The portal login remains
local username + password: `POST /login` checks `portal_users`, then sets the
`skygate_session` cookie as an HS256 JWT (signed with `SKYGATE_JWT_SECRET`,
24 h session). There is no OAuth2 client, no external-IdP configuration, and
no "log in with …" button in `login.html`. The OIDC handlers that skygate
mounts are provider-side only — they *issue* tokens, they never *consume* an
external IdP's tokens. If you need SSO for the portal itself, the options
today are:

1. keep local password auth (the supported path), or
2. put an authenticating proxy (for example `oauth2-proxy`, or the
   proxy's own auth layer) **in front of** skygate and let it gate access —
   skygate itself stays password-based behind it, and you must keep the
   portal's own login working for API/Telegram clients.

If a first-class "skygate as OIDC client" feature is added later, it will
need its own env vars, its own callback path on the skygate issuer host, and
its own claim-mapping to `portal_users`; nothing in Direction A changes.

---

## 2. Setup: skygate as headscale's OIDC provider

### 2.1 Required configuration

The provider is driven by five environment variables (plus one optional
auto-sync switch). Four of them **must match** the headscale side
byte-for-byte; a typo in any of them is a silent authentication failure.

| Env var | Default | Role |
|---|---|---|
| `SKYGATE_OIDC_ISSUER` | *(empty = provider disabled)* | The public HTTPS URL of skygate. Becomes the `iss` claim and the base for every endpoint URL in the discovery document. Must match headscale's `oidc.issuer` exactly, with **no trailing slash**. When empty, the OIDC routes still exist but return `503`. |
| `SKYGATE_OIDC_CLIENT_ID` | `headscale` | The single OAuth client headscale presents at `/oidc/authorize` and `/oidc/token`. Must match `oidc.client_id`. |
| `SKYGATE_OIDC_CLIENT_SECRET` | *(empty)* | The shared secret. Verify with a constant-time comparison; never echoed back in the admin UI. Generate with `openssl rand -base64 32` and paste the same value into the headscale config. |
| `SKYGATE_OIDC_REDIRECT_URIS` | `https://head.example.com/oidc/callback` | Comma-separated allowlist of redirect URIs. Matched with an **exact string comparison** (RFC 6749 §3.1.2.3 — no wildcards, no prefix match), so scheme, host, port, path and trailing slash must all match what headscale sends. |
| `SKYGATE_OIDC_KEY_DIR` | `./data/oidc-keys` | Directory for the RS256 signing keypair. It must be writable at first boot (the key is generated if missing) and **backed up** — losing it invalidates every previously issued token. |
| `SKYGATE_OIDC_AUTOSYNC` | `false` (unset) | Opt-in boot-time sync to headscale; see [§2.5](#25-boot-time-auto-sync). |

The full env-var table (ports, database, headscale, headplane, DERP, deploy
paths) lives in [`deploy.md`](deploy.md) — this document does not repeat it.
After changing any of these values, restart the skygate container so the new
values are loaded:

```bash
cd /home/admin/skygate
docker compose up -d --force-recreate --no-deps skygate
curl -sS http://127.0.0.1:8080/.well-known/openid-configuration | jq .issuer
```

The operator-facing source of truth is **`/admin/oidc`**: it shows the five
endpoint URLs, the current env-var values (with the secret masked), the
copy-paste `headscale.conf` snippet with your real issuer and client_id filled
in, and a **Test connection** button that probes discovery + JWKS + userinfo.
`/admin/oidc/sync` is the Apply surface described in
[§2.4](#24-apply-and-restart-headscale).

### 2.2 The generated headscale `oidc:` block

Add the `oidc:` key at the **top level** of the headscale config — typically
`config/config.yaml`, for example
`/home/admin/headscale/config/config.yaml`. The snippet below is the
canonical block the runbooks have shipped. Note that the two generators are
not identical: `/admin/oidc` renders a trimmed snippet, while
`deploy/oidc-sync.sh` produces the block the sync actually writes (it emits
`redirect_uris` as a list and deliberately omits `strip_email_domain`). When
in doubt, use what `/admin/oidc/sync` writes or downloads:

```yaml
# ============================================================================
# OIDC provider for Tailscale user authentication — skygate is the IdP.
# ============================================================================
oidc:
  # The exact value of skygate's SKYGATE_OIDC_ISSUER (no trailing slash).
  issuer: "https://skygate.example.com"

  # Must match skygate's SKYGATE_OIDC_CLIENT_ID (default: "headscale").
  client_id: "headscale"

  # Must match skygate's SKYGATE_OIDC_CLIENT_SECRET.
  # Generate with `openssl rand -base64 32`; store the same value in the
  # skygate secrets/env on the skygate host. It never touches the repo.
  client_secret: "<random-32-bytes-base64>"

  # Must be present in skygate's SKYGATE_OIDC_REDIRECT_URIS (exact match).
  redirect_uris:
    - "https://head.example.com/oidc/callback"

  # openid is required; profile + email give headscale the user metadata it
  # needs to provision the headscale user.
  scope: ["openid", "profile", "email"]

  # Extra query parameters appended to the authorization request.
  # headscale needs none; empty is fine.
  extra_params:
    domain: client_id

  # Optional allow-list of email domains. When set, a login whose email claim
  # is outside the list is rejected by headscale before skygate's own checks
  # run. Leave empty to accept every domain skygate authenticates.
  allowed_domains: []

  # When true, headscale refreshes its ACL + node list on EVERY OIDC login.
  # true is convenient for development, false avoids a re-apply storm in
  # production.
  auto_update: false

  # REMOVED in headscale 0.23+: the old switch that stripped the email domain
  # from the username. Kept here only so an operator upgrading from an older
  # headscale understands why the key is rejected. Do NOT add it to a modern
  # headscale config — `deploy/oidc-sync.sh` deliberately drops it.
  strip_email_domain: false

  # headscale auto-provisions a user on first login. This is the
  # "one-click add" UX; without it the operator must run
  # `headscale users create <email>` for every new OIDC user.
  automatic_authorization: true
```

Two important notes about this block:

- **`redirect_uris` is a list.** The generated block writes
  `redirect_uris: [...]` (plural). Older runbook text showed a singular
  `redirect_uri:` key — do not mix the two; use whatever your headscale
  version's own config example shows, and make sure the *value* is in
  `SKYGATE_OIDC_REDIRECT_URIS`.
- **`strip_email_domain` and `automatic_authorization` are version-sensitive.**
  `strip_email_domain` was removed in headscale 0.23+ and the sync script
  omits it for that reason. Keep the literal
  `automatic_authorization: true` line only if your headscale version accepts
  it (it is part of the canonical snippet this document inherits); if
  headscale refuses to start, remove that line and create users manually
  instead.

### 2.3 Field reference

| Field | Required | Notes |
|---|---|---|
| `issuer` | yes | The exact `SKYGATE_OIDC_ISSUER` value, no trailing slash. headscale fetches `<issuer>/.well-known/openid-configuration` at startup. |
| `client_id` | yes | Must match `SKYGATE_OIDC_CLIENT_ID` (default `headscale`). Case-sensitive. |
| `client_secret` | yes | Must match `SKYGATE_OIDC_CLIENT_SECRET`. Paste it from the skygate host; skygate uses constant-time comparison and returns `400 invalid_client` on mismatch (not `401`). |
| `redirect_uris` | yes | Must contain the URL headscale sends, exactly as it appears in `SKYGATE_OIDC_REDIRECT_URIS`. |
| `scope` | yes | Must include `openid`; `profile` + `email` are what make the user claims available. |
| `extra_params` | optional | Extra query parameters for the authorization request (for example a tenant hint). Empty is fine. |
| `allowed_domains` | optional | Email-domain allow-list enforced by headscale before skygate's own checks. |
| `allowed_users` / `allowed_groups` | optional | Per-user / per-group allow-lists (newer headscale versions). |
| `auto_update` | recommended | `true` = refresh ACL + node list on every login; `false` = no re-apply storm. `/admin/oidc` writes this field into the generated snippet. |
| `pkce.method` | version-dependent | skygate accepts **S256 only** and rejects `plain` at `/oidc/authorize` and `/token`. headscale 0.29+ defaults to S256; older versions may need it set explicitly. |
| `strip_email_domain` | no (removed) | Removed in headscale 0.23+. The key must not appear in a modern config. |

### 2.4 Apply and restart headscale

There are three ways to get the block into headscale. They are listed from
most to least automated.

**(a) `/admin/oidc/sync` — one click.** The page shows the current config and
an Apply form (headscale config path, container name, skygate `.env` path,
mode, redirect URIs). `POST /admin/oidc/sync` calls `deploy/oidc-sync.sh` via
the Go wrapper and reports the result back as a flash message plus the
generated YAML. The script is a 10-step pipeline: validate the input, probe
the issuer, back up the existing headscale config, inject/replace the `oidc:`
block (atomic write), update the skygate `.env`, apply the change, wait for
headscale health (up to `RESTART_TIMEOUT`, default 60 s), optionally probe the
callback, and print a JSON result on stdout. Re-running with the same inputs
is idempotent.

**(b) `deploy/scripts/setup-skygate-public.sh` — initial public wiring.**

```bash
cd /home/admin/skygate
bash deploy/scripts/setup-skygate-public.sh --issuer https://skygate.example.com
```

It validates the issuer URL, updates `SKYGATE_OIDC_ISSUER` and
`SKYGATE_OIDC_REDIRECT_URIS` in `.env` (with a timestamped backup), restarts
skygate, verifies the discovery document reports the new issuer, then calls
`deploy/oidc-sync.sh` and writes an `oidc_setup` audit row. See
[`https.md`](https.md) §2.2 for the proxy prerequisites.

**(c) Manual.** Copy the snippet from `/admin/oidc` into the headscale config,
then restart headscale and watch the logs:

```bash
# systemd
sudo systemctl restart headscale && sudo journalctl -u headscale -n 50
# Docker / docker compose
docker restart headscale && docker logs --tail 50 headscale
# Kubernetes
kubectl rollout restart deploy/headscale -n headscale
kubectl logs -n headscale deploy/headscale --tail=50
```

**Restart modes** selectable in the sync form / `--mode` flag:

| Mode | What it does |
|---|---|
| `auto` | Detect the deployment shape (docker container → docker; `headscale.service` → systemd; `kubectl` deployment → k8s) and use that. Falls back to `manual`. |
| `docker` | `docker restart <headscale-container>` (default name `headscale`). |
| `systemd` | `systemctl restart headscale`. |
| `k8s` | `kubectl rollout restart deploy/headscale -n headscale`, then waits for the pods to become ready. |
| `api` | Calls headscale's `configure oidc` CLI/gRPC path (`docker exec … headscale configure oidc --issuer … --client-id … --client-secret …`) instead of writing a file. Intended for headscale versions that support runtime OIDC configuration. |
| `manual` | Writes the config and updates the skygate `.env`, but restarts nothing — the operator restarts headscale by hand (for example when headscale lives on a different host). |
| `download` | Prints the generated YAML only; no file writes, no restarts, no `.env` change. Use it to pre-stage or review the block. |

What to look for in the headscale log after a restart:

- `using OIDC issuer https://skygate.example.com` — the block parsed and the
  issuer string was loaded.
- `OIDC discovery doc fetched` — headscale reached
  `/.well-known/openid-configuration` and parsed the endpoint URLs.
- `failed to fetch` / `no oidc provider configured` — the issuer is
  unreachable from the headscale host, or the YAML did not parse
  (indentation). Run `headscale validate` before restarting.

### 2.5 Boot-time auto-sync

When `SKYGATE_OIDC_AUTOSYNC=true`, skygate runs the same sync once at startup
(after the OIDC keypair is loaded, before the HTTP server accepts traffic).
This is for the "headscale lives on the same VM and the OIDC env vars are
already in `.env`" case: skygate pushes the config to headscale and headscale
picks it up on the same boot.

The auto-sync only runs when all three conditions hold — this prevents the
most common footguns:

1. `SKYGATE_OIDC_AUTOSYNC=true` (explicit opt-in; case-insensitive `true`),
2. `SKYGATE_OIDC_ISSUER` is non-empty,
3. `SKYGATE_OIDC_CLIENT_SECRET` is non-empty.

Otherwise skygate logs why it skipped and starts normally. A failed
auto-sync is logged and does **not** prevent skygate from starting.

---

## 3. Endpoints and smoke tests

### 3.1 Endpoint summary

All provider endpoints live on the **issuer** hostname (the skygate portal)
and are served by skygate's own OIDC handler. The callback belongs to
headscale.

| Endpoint | Method | Path | Returns |
|---|---|---|---|
| Discovery | `GET` | `/.well-known/openid-configuration` | `200` + JSON: `issuer`, `authorization_endpoint`, `token_endpoint`, `userinfo_endpoint`, `jwks_uri`, `response_types_supported: ["code"]`, `subject_types_supported: ["public"]`, `id_token_signing_alg_values_supported: ["RS256"]`, `scopes_supported: ["openid","profile","email"]`, `token_endpoint_auth_methods_supported: ["client_secret_post"]`, `claims_supported: ["sub","email","name","preferred_username"]`. `Cache-Control: public, max-age=3600`. `503` when the provider is disabled. |
| JWKS | `GET` | `/oidc/jwks.json` | `200` + `{"keys":[<one RS256 JWK>]}`. headscale uses it to verify the id_token signature. `503` if the keypair is not ready. |
| Authorize | `GET` | `/oidc/authorize` | `302` to the client's `redirect_uri?code=…&state=…` when a session exists; `302` to `/login?next=<full authorize URL>` when it does not; `400` for a bad `client_id` / `redirect_uri` / `response_type`; `302` back to the callback with `error=…` for unsupported response types. Requires PKCE S256 when a challenge is sent. |
| Token | `POST` | `/oidc/token` | `200` + `access_token`, `token_type: Bearer`, `expires_in: 3600`, `id_token`, `scope`. Errors are `400` + the RFC 6749 §5.2 JSON shape (`invalid_client`, `invalid_grant`, `unsupported_grant_type`, `invalid_request`, `server_error`). |
| Userinfo | `GET` | `/oidc/userinfo` | `200` + `sub`, `email`, `name`, `preferred_username`. Without a valid `Authorization: Bearer <access_token>` it returns `401` + `WWW-Authenticate: Bearer error="invalid_token" …`. |
| Callback | `GET` | `<headscale-host>/oidc/callback` | **Served by headscale, not by skygate.** Exchanges the auth code, provisions the user, and hands control back to the Tailscale client. With a fake code it returns `400` — which is the correct, reachable answer. |

### 3.2 Smoke test: discovery, jwks, authorize

Run this before any Tailscale client tries to log in. Every command below
uses `skygate.example.com` as the issuer and `head.example.com` as the
headscale host — replace them with your own.

```bash
# 1. discovery — the canonical "is the OIDC provider alive?" probe.
curl -sS https://skygate.example.com/.well-known/openid-configuration | jq .
# expected: issuer https://skygate.example.com plus the 4 endpoint URLs
# and the alg / scope / claim lists.

# 2. jwks — headscale uses this to verify the id_token's RS256 signature.
#    Must return exactly one key.
curl -sS https://skygate.example.com/oidc/jwks.json | jq '.keys | length'
# expected: 1

# 3. authorize — must be a 302 (not a 500, not a 404).
curl -sS -o /dev/null -w "%{http_code}\n" \
  "https://skygate.example.com/oidc/authorize?response_type=code&client_id=headscale&redirect_uri=https%3A%2F%2Fhead.example.com%2Foidc%2Fcallback&state=test&scope=openid+profile+email"
# expected: 302

# 4. the same request with the redirect target visible (unauthenticated):
curl -sS -o /dev/null -w 'code=%{http_code} loc=%{redirect_url}\n' \
  "https://skygate.example.com/oidc/authorize?response_type=code&client_id=headscale&redirect_uri=https%3A%2F%2Fhead.example.com%2Foidc%2Fcallback&state=test&scope=openid+profile+email&code_challenge=abc&code_challenge_method=S256"
# expected: code=302 loc=https://skygate.example.com/login?next=…
```

The same three checks are available with one click: `/admin/oidc` → **Test
connection** probes discovery (`200`), JWKS (`200`) and userinfo
(`401` + `WWW-Authenticate: Bearer`) and reports a one-line summary.

### 3.3 Token, userinfo and callback

```bash
# 4. token — proves the client_secret check is active.
curl -sS -o - -w '\n%{http_code}\n' -X POST https://skygate.example.com/oidc/token \
  -d grant_type=authorization_code -d code=fake \
  -d client_id=headscale -d client_secret=wrong \
  -d redirect_uri=https://head.example.com/oidc/callback
# expected: 400 + {"error":"invalid_client", …}

# 5. token — proves the auth-code store works (right secret, unknown code).
curl -sS -o - -w '\n%{http_code}\n' -X POST https://skygate.example.com/oidc/token \
  -d grant_type=authorization_code -d code=fake \
  -d client_id=headscale -d client_secret="$SKYGATE_OIDC_CLIENT_SECRET" \
  -d redirect_uri=https://head.example.com/oidc/callback
# expected: 400 + {"error":"invalid_grant", …}

# 6. userinfo — no token.
curl -sS -o - -w '\n%{http_code}\n' https://skygate.example.com/oidc/userinfo
# expected: 401 + {"error":"invalid_token", …} + WWW-Authenticate: Bearer …

# 7. callback — routed through the proxy to headscale (fake code).
curl -sS -o /dev/null -w '%{http_code}\n' https://head.example.com/oidc/callback
# expected: 400 (reached headscale; the code is fake — this is correct)
```

A full end-to-end run, for reference, looks like this:

```
Tailscale client → headscale
                → 302 to https://skygate.example.com/oidc/authorize
                → 302 to https://skygate.example.com/login?next=…      (no session yet)
                → user signs in at skygate
                → 302 back to /oidc/authorize (now authenticated)
                → 302 to https://head.example.com/oidc/callback?code=…&state=…
                → headscale POSTs to https://skygate.example.com/oidc/token
                → headscale GETs  https://skygate.example.com/oidc/userinfo
                → headscale creates the user (if needed) and returns the netmap
```

The interactive part (the user typing a password) takes 5–30 s; everything
else is 1–3 s.

---

## 4. Client and login UX notes

### 4.1 The `next` redirect

`/oidc/authorize` preserves the entire OIDC request across the login
round-trip by redirecting to `/login?next=<url-escaped current URL>`. The login
template renders that value as a hidden `next` input, and `PostLogin` honours
it after a successful password check. This is why the OIDC handshake survives
the browser detour instead of dying on the welcome page.

The value is validated by a same-origin check before it is used, so the login
page cannot be turned into an open redirect:

| `next` value | Result |
|---|---|
| empty | `/dashboard` |
| `/foo/bar` (relative path) | used as-is |
| `https://<same host as the request Host>/…` | used as-is |
| `//evil.example.com/path` (protocol-relative) | rejected → `/dashboard` |
| `https://evil.example.com/path` (different host) | rejected → `/dashboard` |
| `javascript:`, `data:`, `file:` and other non-`http(s)` schemes | rejected → `/dashboard` |

Operational consequences:

- The `Host` header must reach skygate unchanged (see
  [`https.md`](https.md) §2.4), otherwise the same-host check compares against
  the wrong host and falls back to `/dashboard`, dropping the OIDC handshake.
- A proxy that rewrites the request URL (for example stripping the query
  string) breaks the round-trip even though the redirect itself is valid.
- A session cookie that skygate cannot parse (wrong or rotated
  `SKYGATE_JWT_SECRET`) makes `/oidc/authorize` treat the user as anonymous and
  bounce back to `/login`, which the user experiences as "I logged in and it
  asked me to log in again". Keep `SKYGATE_JWT_SECRET` stable across restarts.

### 4.2 What the operator sees on failure

| Where | What you see | Meaning |
|---|---|---|
| Browser | `/login` re-renders with an empty password field right after submitting | The session cookie was not recognised on the way back (wrong `SKYGATE_JWT_SECRET`, or cookie not sent — see [§4.1](#41-the-next-redirect)). |
| Browser | The page refreshes with no visible feedback before submitting | The login form's loading state needs a recent build; older builds have no spinner/overlay. Not an auth failure by itself. |
| Browser | Landing on the skygate dashboard instead of the Tailscale client | The `next` parameter was lost (proxy stripped the query string, or the `Host` mismatch fallback fired). |
| Tailscale client | `authentication failed` / the client never reaches "Connected" | headscale rejected the flow after `/oidc/userinfo` — usually an unknown user (auto-provisioning disabled or the user was removed) or a claim mismatch. |
| headscale log | `OIDC discovery: failed to fetch …` | The issuer URL is wrong, or the headscale host cannot reach it. |
| headscale log | `OIDC: invalid client_id` / `redirect_uri mismatch` | One of the four must-match values drifted. |
| skygate log | `oidc.authorize: unknown client_id …` | headscale sent a `client_id` different from `SKYGATE_OIDC_CLIENT_ID`. |
| skygate log | `oidc.token: bad client_secret for …` | The headscale-side secret no longer matches `SKYGATE_OIDC_CLIENT_SECRET` (typically after a rotation on one side only). |
| `/admin/oidc` | "OIDC is currently disabled (`SKYGATE_OIDC_ISSUER` is empty)" | The provider is off; every OIDC route returns `503`. |
| `/admin/audit` | no `oidc.token` rows after a successful login attempt | The token exchange never completed. |
| `/my/devices` | the device shows "⏳ pending" for the tag | The node was registered but the per-device tag has not been applied yet — see [§4.3](#43-node-auto-tagging-for-oidc-registered-nodes). |

### 4.3 Node auto-tagging for OIDC-registered nodes

skygate maps a headscale node to a portal user (the `node_owner_map` table)
and then applies the per-device tag `tag:dev-<portal-user>-<device>`. The
autoupdater runs on an interval (default 5 minutes) and tries several
matching strategies in order:

| Strategy | Match |
|---|---|
| A | `PreAuthKeyID` equals a `preauth_keys.headscale_preauth_id` row (`/my/preauth` flow). |
| C | the node was created within a 1-hour window of a preauth key (temporal fallback for A). |
| D | the node already carries a `tag:dev-<user>-*` tag (post-hoc, after a manual tag). |
| E | **OIDC**: `PreAuthKeyID == ""` **and** `node.UserName == portal username`. |

Strategy E exists because OIDC-registered nodes have no preauth key, no
`preauth_keys` row and no tags yet — without it they stayed "pending"
forever. headscale creates the OIDC user with the name from skygate's
`name` claim, which is the skygate username, so the name comparison is
authoritative. The guards are deliberate: a non-empty `PreAuthKeyID` excludes
preauth nodes from Strategy E, and the synthetic `tagged-devices` sentinel
user never matches a real `portal_users.username`.

Caveats that matter in practice:

- **Tags are lowercase.** headscale rejects uppercase tags. The dev-tag is
  lowercased before it is applied; a manually invented tag with capitals will
  be rejected and the device stays pending.
- **`tagged-devices` is a dead end.** Once a node has a tag, headscale
  reassigns ownership to the synthetic `tagged-devices` user. Nodes that are
  already there with no live dev-tag are skipped by all four strategies
  (Strategy E cannot see them because the username no longer matches).
  For those legacy nodes the operator must apply the tag manually, for
  example `headscale nodes tag --force -i <id> -t 'tag:dev-<user>-<device>'`,
  after which Strategy D keeps it in sync.
- **`claim_map` breaks Strategy E.** If headscale is configured to derive the
  username from a different claim (for example the email local part), the
  headscale user name will not equal the skygate username and Strategy E will
  not match. Use the default claim behaviour, or plan to apply tags manually.
- **`automatic_authorization: true` is what makes the device appear at all.**
  Without user auto-provisioning, headscale has no user to attach the node to.
- Applying a tag can require the node to re-register before it picks up
  ACL-derived behaviour; `/my/devices` shows a re-register action for nodes in
  the `tagged-devices` sentinel.

---

## 5. Verification checklist and troubleshooting

### 5.1 End-to-end checklist

Work top to bottom; each line has a command that must pass before the next.

1. **Provider enabled** — `/admin/oidc` shows "OIDC provider is enabled" with
   the five endpoint URLs filled in.
2. **Discovery correct** —
   `curl -sS https://skygate.example.com/.well-known/openid-configuration | jq .issuer`
   prints exactly `https://skygate.example.com`.
3. **JWKS present** —
   `curl -sS https://skygate.example.com/oidc/jwks.json | jq '.keys | length'`
   prints `1`.
4. **Authorize redirects** —
   the unauthenticated `/oidc/authorize` smoke test returns `302` to
   `/login?next=…`.
5. **Token rejects bad creds** — `POST /oidc/token` with a wrong secret
   returns `400 invalid_client`.
6. **Userinfo guards tokens** — `GET /oidc/userinfo` without a bearer token
   returns `401` with `WWW-Authenticate: Bearer`.
7. **Callback routed** — `https://head.example.com/oidc/callback` returns
   `400` for a fake code (not `404`).
8. **headscale loaded the block** — the headscale log shows the OIDC issuer on
   startup and no discovery errors.
9. **One real client** — `tailscale up --login-server https://head.example.com`
   on a test device completes the browser flow and the device appears in
   headscale.
10. **Portal sees the user** — `/admin/users` shows the OIDC user (created or
    with a refreshed `last_login_at`), `/admin/audit` has an `oidc.token` row,
    and `/my/devices` shows the device with the dev-tag applied (within one
    autoupdater interval).

### Common e2e failures

Symptom → cause → fix.

| Symptom | Cause | Fix |
|---|---|---|
| `503` from every `/oidc/*` path | `SKYGATE_OIDC_ISSUER` is empty (provider disabled) | Set the env var in `.env` and recreate the skygate container; re-check `/admin/oidc`. |
| Discovery returns `404` | The `/.well-known/` path is not routed by the proxy | Add the discovery location ([`https.md`](https.md) §2.2) and reload the proxy. |
| Discovery returns `502` | skygate is not answering | `curl http://127.0.0.1:8080/healthz` on the skygate host; check the container and the DB. |
| `jwks_uri returned no keys` | The signing keypair is missing or the key directory is not writable | Check `SKYGATE_OIDC_KEY_DIR` is mounted and writable; restart skygate so the key is generated; back the directory up. |
| headscale: `issuer mismatch` | `oidc.issuer` ≠ `SKYGATE_OIDC_ISSUER` — trailing slash, `http` vs `https`, or a stale container | Make both byte-identical (full `https://…`, no trailing slash), restart skygate, then re-apply the block. |
| headscale: `OIDC: invalid client_id` | `oidc.client_id` ≠ `SKYGATE_OIDC_CLIENT_ID` (case-sensitive) | Align both values and restart both processes. |
| headscale: `OIDC: redirect_uri mismatch` | The URL headscale sends is not in `SKYGATE_OIDC_REDIRECT_URIS` | Compare scheme, host, port, path and trailing slash; the comparison is exact-string. |
| Browser stuck on `/oidc/authorize?` after a successful login | The login round-trip lost the request (`next` dropped by a proxy, or a `Host` mismatch) | Send `Host`/`X-Forwarded-Host` unchanged; confirm `/login?next=…` contains the full authorize URL. |
| Login succeeds but the device is never added | `oidc:` block missing/not applied, so headscale's callback never runs | Re-run the sync ([§2.4](#24-apply-and-restart-headscale)); confirm the callback returns `400` (reachable) not `404`. |
| `400 invalid_client` from `/oidc/token` | `client_secret` mismatch (often after rotating one side only) | Re-paste `SKYGATE_OIDC_CLIENT_SECRET` into the headscale config and restart headscale. skygate uses constant-time compare, so a partial mismatch is still `400`. |
| `400 invalid_grant` from `/oidc/token` | The auth code expired (5-minute TTL), was already used (single-use), or the PKCE `code_verifier` does not match the challenge | Retry the login; if it is persistent, check that both hosts' clocks are within a few minutes of each other. |
| `401 invalid_token` from `/oidc/userinfo` in the middle of a flow | The access token expired (1 h TTL) or was signed by a different key (the key directory was replaced) | Verify the JWKS `kid` matches the JWT header; restore/keep the original key directory and retry the whole flow. |
| "Invalid state parameter" | The `state` round-trip is broken — usually a byte difference between the callback URL and the allowlist entry | Make `SKYGATE_OIDC_REDIRECT_URIS` and the headscale-side value byte-identical. |
| Token claims look right but headscale creates no user | Auto-provisioning is unavailable in your headscale version | Create the user explicitly (`headscale users create <name>`) or reinstate `automatic_authorization: true` (see the version caveat in [§2.2](#22-the-generated-headscale-oidc-block)). |
| Clock/issuer mismatch class: flow fails intermittently, tokens rejected right after issue | The skygate and headscale hosts' clocks drifted beyond the token leeway | Sync both hosts with NTP; verify `date -u` on both; re-issue tokens after the fix. |
| Device registered but stuck "⏳ pending" forever | Pre-B175 behaviour, or the node is already in the `tagged-devices` sentinel, or the dev-tag was rejected | Confirm the autoupdater interval elapsed; check the skygate log for a tag error; apply the tag manually with `headscale nodes tag --force` (see [§4.3](#43-node-auto-tagging-for-oidc-registered-nodes)). |
| `/admin/oidc/sync` Apply fails with a reload/health error | The detected mode does not match the deployment, or headscale did not come back within the timeout | Re-run with an explicit mode; use `download` to inspect the YAML; restore the config backup if needed. |
| Tailscale shows "Logged in" but no IP / cannot ping | Not an OIDC problem — ACL or routing | Check `/admin/acls` and the headscale policy; see [`acl-rules-reference.md`](acl-rules-reference.md). |

---

## 6. Rollback

Disabling OIDC must never require deleting data. Choose the smallest rollback
that restores service.

**Disable the integration while keeping the configuration.**

1. Comment out the `oidc:` block in the headscale config.
2. Restart headscale (`systemctl restart headscale`, `docker restart
   headscale`, or the deployment-appropriate command).
3. headscale falls back to its pre-OIDC auth: CLI user creation, preauth
   keys, and the registration mode configured in the headscale config.
4. Already-authenticated Tailscale clients stay authenticated until their key
   expires or the headscale user/node is deleted.
5. Fix the failure (the most common causes are a rotated `client_secret` on
   one side only, an `allowed_domains` entry that excludes the user, or an
   issuer mismatch), then re-apply and restart headscale again.

skygate's provider endpoints stay up the whole time; clients that do not go
through headscale are unaffected.

**Restore the previous config files.** `deploy/oidc-sync.sh` writes
timestamped backups of both files it touches:

```bash
ls -1 /home/admin/headscale/config/config.yaml.pre-oidc-sync.*
ls -1 /home/admin/skygate/.env.pre-oidc-sync.* /home/admin/skygate/.env.pre-setup-public.*

# headscale config
cp -p /home/admin/headscale/config/config.yaml.pre-oidc-sync.<timestamp> \
      /home/admin/headscale/config/config.yaml
docker restart headscale          # or the systemd/k8s equivalent

# skygate env (issuer / redirect URIs)
cp -p /home/admin/skygate/.env.pre-oidc-sync.<timestamp> /home/admin/skygate/.env
cd /home/admin/skygate && docker compose up -d --force-recreate --no-deps skygate
```

**Turn the provider off entirely.** Remove (or empty) `SKYGATE_OIDC_ISSUER`
and recreate the skygate container. The OIDC routes remain mounted but answer
`503`, and `/admin/oidc` renders the "provider is disabled" banner. This is
the safe state when you are unsure whether the integration or something else
is at fault.

**Re-enable later.** Set `SKYGATE_OIDC_ISSUER`, `SKYGATE_OIDC_CLIENT_ID`,
`SKYGATE_OIDC_CLIENT_SECRET` and `SKYGATE_OIDC_REDIRECT_URIS`, restart
skygate, then push the headscale block from `/admin/oidc/sync` (or re-run
`deploy/scripts/setup-skygate-public.sh`). Reuse the **same**
`SKYGATE_OIDC_KEY_DIR` — a fresh keypair changes the JWKS `kid` and
invalidates every previously issued token.

**What to back up.** `portal_users`, `node_owner_map`, and the
`oidc-keys/` directory (the RSA keypair). Losing the keypair is the only
unrecoverable mistake in this flow. The full backup/restore procedure is in
[`backup-restore-and-migration.md`](backup-restore-and-migration.md), and
recovery scenarios are in [`disaster-recovery.md`](disaster-recovery.md).
