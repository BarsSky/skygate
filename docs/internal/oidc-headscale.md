# Connecting headscale to skygate OIDC provider (B161.4)

**Status**: B161.4 — headscale.conf snippet + e2e verification
**Target**: v1.5.0 (close the OIDC block: B161.1 + B161.2 + B161.3 are already shipped)
**Author**: Mavis (skygate) + admin

After B161.1+2+3 ship, skygate is a complete OIDC
provider. Tailscale clients can now authenticate
against headscale by going through the skygate
login flow. This doc shows the operator how to
wire headscale's `oidc:` block to skygate and how
to verify the end-to-end flow.

This is a **2-step operator runbook** + an e2e
verification that Mavis can run on the VM to
confirm the round-trip works before the operator
attaches a real Tailscale client.

---

## 1. headscale.conf snippet

Open the headscale config (typically at
`/etc/headscale/config.yaml` on the headscale
container host) and add the `oidc:` block.

The example below uses RFC 5737 example IPs + the
`example.com` placeholder domain. Replace every
`<...>` with the operator's real values.

```yaml
# ============================================================================
# OIDC provider for Tailscale user authentication
# B161.4 (v1.5.0) — skygate is the IdP
# ============================================================================
# Before this block, headscale uses its own
# built-in user store (CLI-managed). After this
# block, headscale delegates auth to skygate via
# OIDC. Tailscale clients hitting headscale for
# login get redirected to skygate's
# /oidc/authorize endpoint.
oidc:
  # The exact value of skygate's
  # SKYGATE_OIDC_ISSUER env var. Must match
  # WITHOUT a trailing slash. The skygate
  # discovery doc lives at:
  #   <issuer>/.well-known/openid-configuration
  issuer: "https://skygate.example.com"

  # Must match skygate's SKYGATE_OIDC_CLIENT_ID.
  # skygate has a single registered client for
  # headscale (default = "headscale").
  client_id: "headscale"

  # Must match skygate's SKYGATE_OIDC_CLIENT_SECRET.
  # Generate with:
  #   openssl rand -base64 32
  # Store the same value in /etc/skygate-secrets/
  # on the skygate host (chmod 0600). The value
  # NEVER touches the repo.
  client_secret: "<random-32-bytes-base64>"

  # Must match one of skygate's
  # SKYGATE_OIDC_REDIRECT_URIS (comma-separated).
  # Default is https://head.skynas.ru/oidc/callback.
  # Add the actual headscale callback URL the
  # operator is using.
  redirect_url: "https://head.skynas.ru/oidc/callback"

  # headscale uses these scopes. openid is
  # required; profile + email give headscale the
  # user metadata it needs to provision the
  # headscale user (email = username, name =
  # display name).
  scope: ["openid", "profile", "email"]

  # headscale auto-provisions a user on first
  # login (creates the headscale user named
  # after the OIDC sub claim). Set to true for
  # the "click-to-add" UX the operator wants.
  automatic_authorization: true

  # The claim to use as the headscale username.
  # "preferred_username" is what skygate returns
  # in the id_token + userinfo response. (B161
  # embeds the skygate portal_users.username as
  # "preferred_username" in both tokens.)
  username_claim: "preferred_username"

  # Strip the email-domain suffix from the email
  # claim to derive a shorter headscale username.
  # Set to "@example.com" if your user emails are
  # all in the example.com domain. Empty string
  # = use the full email as the username.
  email_to_username_claim_separator: ""
```

Save the file, then restart headscale:

```bash
# In the headscale container
docker restart headscale

# Or via systemd on a bare-metal install
sudo systemctl restart headscale
```

**The 4 values that must match** (operator
responsibility — typo here = silent auth failure):

| skygate env var | headscale field |
|---|---|
| `SKYGATE_OIDC_ISSUER` | `oidc.issuer` |
| `SKYGATE_OIDC_CLIENT_ID` | `oidc.client_id` |
| `SKYGATE_OIDC_CLIENT_SECRET` | `oidc.client_secret` |
| `SKYGATE_OIDC_REDIRECT_URIS` (first one) | `oidc.redirect_url` |

The same values must be set in `/etc/skygate-secrets/`
on the skygate host so both processes see the
same `client_secret` and `redirect_url`.

---

## 2. Verify the OIDC endpoints are reachable

Before attaching a Tailscale client, run a 3-step
smoke test from the skygate host (or any host that
can reach the skygate OIDC endpoints):

```bash
# 1. Discovery doc — the canonical "is the OIDC
# provider alive?" probe. headscale uses this
# URL to discover token_endpoint, jwks_uri, etc.
curl -sS https://skygate.example.com/.well-known/openid-configuration | jq .

# Expected output: JSON with at least these fields
#   "issuer": "https://skygate.example.com"
#   "authorization_endpoint": ".../oidc/authorize"
#   "token_endpoint":         ".../oidc/token"
#   "userinfo_endpoint":      ".../oidc/userinfo"
#   "jwks_uri":               ".../oidc/jwks.json"
#   "id_token_signing_alg_values_supported": ["RS256"]
#   "response_types_supported": ["code"]
#   "scopes_supported": ["openid", "profile", "email"]
#   "token_endpoint_auth_methods_supported": ["client_secret_post", "client_secret_basic"]

# 2. JWKS — headscale uses this to verify the
# id_token's RS256 signature. Must return exactly
# 1 key (B161 ships a single RSA-2048 keypair).
curl -sS https://skygate.example.com/oidc/jwks.json | jq .

# Expected output:
#   { "keys": [ { "kty": "RSA", "alg": "RS256",
#                 "use": "sig", "kid": "<16-hex>",
#                 "n": "<base64url>", "e": "AQAB" } ] }

# 3. The /oidc/authorize endpoint must be reachable
# and return a 302 redirect (either to /login if
# the user is not authenticated, or to the
# redirect_uri if they are). NOT a 500.
curl -sS -o /dev/null -w "%{http_code}\n" \
    "https://skygate.example.com/oidc/authorize?response_type=code&client_id=headscale&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback&state=test&scope=openid+profile+email"
# Expected: 302 (redirect to /login or to the redirect_uri)
```

If any of these fail, the operator gets a clear
error in the headscale logs (`journalctl -u
headscale` or `docker logs headscale | tail -20`):

| Error | Fix |
|---|---|
| `OIDC discovery failed: 404` | skygate's `/.well-known/openid-configuration` is not routed. Check `mux.Handle("/.well-known/", ...)` in main.go. |
| `OIDC discovery failed: 502` | skygate is not responding. Check `curl http://localhost:8080/healthz` on the skygate host. |
| `jwks_uri returned no keys` | The OIDC keypair is not initialised. Check `SKYGATE_OIDC_KEY_DIR` (default `./data/oidc-keys`) is mounted + writable. |
| `issuer mismatch` | `oidc.issuer` in headscale.conf != `SKYGATE_OIDC_ISSUER` (trailing slash + case mismatches are the most common). |
| `client_id mismatch` | `oidc.client_id` in headscale.conf != `SKYGATE_OIDC_CLIENT_ID` (default `"headscale"`). |
| `redirect_uri mismatch` | The redirect_uri headscale sends doesn't match the value in skygate's `SKYGATE_OIDC_REDIRECT_URIS` (exact-string match per B161.2). |

---

## 3. End-to-end with a real Tailscale client

Once the smoke test passes, attach a real Tailscale
client to verify the full flow.

### 3.1. Prerequisites

- A Tailscale client (macOS, Windows, Linux, iOS,
  Android) with the "Use custom coordination server"
  option in the GUI.
- The operator's headscale URL, e.g.
  `https://head.skynas.ru`.
- An skygate user account (the operator must log
  into `https://skygate.example.com/login` first
  to create the session cookie that the OIDC
  flow expects).

### 3.2. The flow (what should happen)

1. User runs `tailscale up --login-server=https://head.skynas.ru`
   on a device.
2. Tailscale opens the browser to headscale's
   login page.
3. headscale redirects to:
   `https://skygate.example.com/oidc/authorize?
      response_type=code
      &client_id=headscale
      &redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback
      &state=<csrf>
      &scope=openid+profile+email`
4. skygate checks the session cookie. If the
   user is logged in → 302 to headscale's
   `redirect_uri?code=<auth_code>&state=<csrf>`.
   If not → 302 to `/login?next=<the full /oidc/authorize URL>`.
5. After login, the user lands on headscale's
   callback. headscale POSTs to:
   `https://skygate.example.com/oidc/token` with
   `grant_type=authorization_code&code=<auth_code>&...`
6. skygate returns the id_token + access_token
   (RS256-signed JWTs).
7. headscale calls
   `https://skygate.example.com/oidc/userinfo`
   with `Authorization: Bearer <access_token>`.
8. skygate returns the user claims (sub + email +
   name + preferred_username).
9. headscale auto-provisions the user (because
   `automatic_authorization: true`) and issues
   Tailscale a netmap. The Tailscale client
   transitions to "Connected".

### 3.3. Common e2e failures

| Failure | Where to look | Fix |
|---|---|---|
| "Invalid state parameter" | headscale logs | The `state` round-trip is broken. Check that headscale's `redirect_uri` and skygate's `SKYGATE_OIDC_REDIRECT_URIS` are byte-identical (including trailing slash + query params). |
| "Invalid client" | headscale logs | `oidc.client_id` != `SKYGATE_OIDC_CLIENT_ID` (typo, case, or trailing whitespace). |
| "Invalid redirect URI" | headscale logs | The URL headscale is sending back doesn't match skygate's allowlist. Add the URL to `SKYGATE_OIDC_REDIRECT_URIS` (comma-separated). |
| "User not found" / 500 on /oidc/userinfo | skygate logs (`oidc.userinfo: parseAccessToken: ...`) | The skygate access_token can't verify with the JWKS. Check that skygate's JWKS has the same `kid` as the JWT header. |
| "tailscale: authentication failed" | Tailscale client | headscale returned 401 after /oidc/userinfo. The user is unknown to headscale. Check `automatic_authorization: true` in headscale.conf. |
| "Tailscale shows 'Logged in as <name>' but no IP" | Tailscale client | headscale provisioned the user but the ACL doesn't grant them access. Check /admin/acls. |

---

## 4. End-to-end test via skygate (Mavis-side)

The B161 B-check (`scripts/check_b161.sh`) covers
the in-process OIDC flow (the auth code + token
exchange + userinfo). The new B161.4 B-check
(`scripts/check_b161_4.sh`) covers the LIVE
end-to-end:

- discovery doc reachable + has the right fields
- JWKS has 1 key with RS256 + the expected kid
- /oidc/authorize returns 302 (not 500) for a
  valid request
- /oidc/token returns 400 invalid_client for bad
  creds (proves the client_secret check is
  active)
- /oidc/token returns 400 invalid_grant for an
  unknown code (proves the auth code store works)

The full happy-path test (with a real
authorization code, simulated Tailscale client)
requires the operator to run a Tailscale device
through the flow once. There is no headless way
to drive the real Tailscale auth flow without the
Tailscale client SDK.

A close substitute: drive the flow with `curl` +
a session cookie. The operator can do this
manually on the skygate host (after `curl`ing
`/login` to authenticate + set the session
cookie, then `curl`ing `/oidc/authorize` to get
the auth code, then `curl`ing `/oidc/token` to
exchange it). The B161.4 B-check documents the
exact `curl` sequence.

---

## 5. Reusable lessons (for future OIDC integrations)

1. **The 4 values that MUST match** between
   skygate and headscale: `issuer`, `client_id`,
   `client_secret`, `redirect_uri`. A typo in any
   of them = silent auth failure. The table in
   §1 is the canonical checklist.
2. **The discovery doc is the OIDC
   "heartbeat"** — if it's reachable, the IdP is
   alive. If it returns 502, the IdP is down. If
   it returns 404, the IdP isn't routing the
   well-known path. Check that first.
3. **The redirect_uri must match byte-for-byte**.
   RFC 6749 §3.1.2: "the authorization server
   MUST require that the redirect_uri match one
   of those registered for the client". skygate
   does an exact-string match (B161.2). A
   trailing slash or query param mismatch = 400.
4. **`automatic_authorization: true` is the
   "one-click UX"** the operator wants. Without
   it, the operator has to manually run
   `headscale users create <email>` for every
   new OIDC user (the "click-to-add" promise
   B161 makes is broken).
5. **Tailscale's auth flow is server-driven**:
   the client just opens the browser to the
   login-server URL. Everything else happens
   between headscale (the SP) and skygate (the
   IdP). The Tailscale client has no idea OIDC
   is involved — it just sees a successful auth.

---

## 6. References

- B161 release notes in AGENTS.md §"Current"
- `docs/features.md` §"OIDC (B161)"
- `docs/internal/ha-v1.5.0-execution.md`
  §"Locked-in decisions" (decisions #16 + #18
  on how the OIDC + headplane secrets are stored)
- RFC 6749 (OAuth 2.0 Authorization Framework)
- RFC 8414 (OAuth 2.0 Authorization Server Metadata)
- headscale docs: https://headscale.net/apidoc/
  (search for `oidc:` block)
