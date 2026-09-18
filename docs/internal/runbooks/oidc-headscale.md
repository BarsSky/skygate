# OIDC integration: skygate ⇄ headscale (B161.4)

**Status:** B161.4 — headscale.conf snippet + end-to-end verification
**Target:** v1.5.0 (close the OIDC block: B161.1 + B161.2 + B161.3 are already shipped)
**Audience:** the operator (or anyone running headscale alongside skygate)

After B161.1+2+3 ship, skygate is a complete OIDC provider. Tailscale
clients can now authenticate against headscale by going through the
skygate login flow. This runbook walks the operator through wiring
headscale's `oidc:` block to skygate, verifying the endpoints are
reachable, and driving the end-to-end flow with a real Tailscale
client.

All example values use RFC 5737 IPs + `example.com` placeholder
domains. Replace every `<...>` with your real values before applying.

---

## 1. Prerequisites

- skygate is running with B161.3 or later (v1.4.4-30+ or any v1.5.x).
  Verify at `/admin/oidc` — the page should show "OIDC provider is
  enabled" with the 5 endpoint URLs filled in.
- You have admin access to headscale (SSH to the VM where headscale is
  running, or `kubectl exec` into the headscale pod).
- You have the skygate `SKYGATE_OIDC_CLIENT_SECRET` value (in
  `/etc/skygate-secrets/oidc.env` on the skygate host, or wherever your
  secrets live).

---

## 2. Verify the skygate side

Open `/admin/oidc` in your browser. You should see:

- A green "OIDC provider is reachable" banner (or click "Test
  connection" if not already there).
- The 5 endpoint URLs:
  - `Issuer`: `https://<your-skygate-domain>`
  - `Discovery`: `<issuer>/.well-known/openid-configuration`
  - `Authorization`: `<issuer>/oidc/authorize`
  - `Token`: `<issuer>/oidc/token`
  - `Userinfo`: `<issuer>/oidc/userinfo`
  - `JWKS`: `<issuer>/oidc/jwks.json`
- A copy-paste `headscale.conf` snippet with the issuer + client_id
  pre-filled.

Copy the snippet. You'll paste it into `headscale.conf` in the next
step.

---

## 3. The `oidc:` block

Open the headscale config (typically at `/etc/headscale/config.yaml`
on the headscale container host) and add the `oidc:` block at the top
level of the YAML (not nested inside any other section):

```yaml
# ============================================================================
# OIDC provider for Tailscale user authentication
# B161.4 (v1.5.0) — skygate is the IdP
# ============================================================================
oidc:
  # The exact value of skygate's SKYGATE_OIDC_ISSUER env var.
  # Must match WITHOUT a trailing slash. The skygate discovery
  # doc lives at: <issuer>/.well-known/openid-configuration
  issuer: "https://skygate.example.com"

  # Must match skygate's SKYGATE_OIDC_CLIENT_ID. skygate has a
  # single registered client for headscale (default = "headscale").
  client_id: "headscale"

  # Must match skygate's SKYGATE_OIDC_CLIENT_SECRET. Generate
  # with `openssl rand -base64 32`. Store the same value in
  # /etc/skygate-secrets/ on the skygate host (chmod 0600).
  # The value NEVER touches the repo. skygate uses constant-time
  # comparison to defend against timing attacks.
  client_secret: "<random-32-bytes-base64>"

  # Must match one of skygate's SKYGATE_OIDC_REDIRECT_URIS
  # (comma-separated). skygate's allowedRedirect() does an
  # EXACT-STRING match (RFC 6749 §3.1.2.3 — no wildcards, no
  # substring match) so headscale MUST send exactly the same
  # string including trailing slash, port, and protocol.
  redirect_uri: "https://head.example.com/oidc/callback"

  # PKCE S256 only. skygate rejects "plain" at the /authorize
  # step (B161.2 contract H) and at /token (B161.3 contract I).
  # headscale 0.29.x defaults to S256; if you have an older
  # version, set this explicitly.
  pkce:
    method: "S256"

  # openid is required; profile + email give headscale the user
  # metadata it needs to provision the headscale user.
  scope: ["openid", "profile", "email"]

  # Extra query parameters appended to the authorization request.
  # headscale does not require any; the usual reason to set this is an
  # upstream IdP hint (forcing a tenant / realm). Empty is fine.
  extra_params: {}

  # Optional allow-list of email domains. When set, a login whose email
  # claim is outside the list is rejected by headscale before skygate's
  # own checks run. Leave unset to accept every domain the IdP
  # authenticates (skygate still gates on its own user table).
  allowed_domains: []

  # When true, headscale refreshes its ACL + node list on EVERY OIDC
  # login. Recommended true for development (always the latest config)
  # and false for production (avoids a re-apply storm on every user
  # login). /admin/oidc writes this field into the generated snippet.
  auto_update: false

  # REMOVED in headscale 0.23+: the old switch that stripped the email
  # domain from the username. The modern replacement is
  # `email_to_username_claim_separator` (see the field table below).
  # Kept here only so an operator upgrading from an older headscale
  # understands why the key is rejected.
  strip_email_domain: false

  # headscale auto-provisions a user on first login (creates
  # the headscale user named after the OIDC sub claim). Set to
  # true for the "click-to-add" UX the operator wants.
  automatic_authorization: true

  # The claim to use as the headscale username.
  # "preferred_username" is what skygate returns in the id_token
  # + userinfo response (B161 embeds the skygate portal_users.
  # username as "preferred_username" in both tokens).
  username_claim: "preferred_username"

  # Strip the email-domain suffix from the email claim to derive
  # a shorter headscale username. Set to "@example.com" if your
  # user emails are all in the example.com domain. Empty string
  # = use the full email as the username.
  email_to_username_claim_separator: ""
```

**That's the only block you need.** The rest of `headscale.conf` (the
`server_url`, `listen_addr`, `dns_config`, etc.) is unchanged.

### The 4 values that MUST match (operator responsibility — typo = silent auth failure)

| skygate env var | headscale field |
|---|---|
| `SKYGATE_OIDC_ISSUER` | `oidc.issuer` |
| `SKYGATE_OIDC_CLIENT_ID` | `oidc.client_id` |
| `SKYGATE_OIDC_CLIENT_SECRET` | `oidc.client_secret` |
| `SKYGATE_OIDC_REDIRECT_URIS` (first one) | `oidc.redirect_url` |

The same values must be set in `/etc/skygate-secrets/` on the skygate
host so both processes see the same `client_secret` and `redirect_url`.

### Field reference (full)

| Field | Required | Notes |
|---|---|---|
| `issuer` | yes | The exact `SKYGATE_OIDC_ISSUER` value (no trailing `/`) |
| `client_id` | yes | Must match `SKYGATE_OIDC_CLIENT_ID` (default: `headscale`) |
| `client_secret` | yes | Must match `SKYGATE_OIDC_CLIENT_SECRET` (paste from skygate host) |
| `redirect_url` | yes | Must match one of `SKYGATE_OIDC_REDIRECT_URIS` exactly |
| `scope` | yes | Must include `openid`; `profile` + `email` recommended |
| `pkce.method` | yes | `S256` (skygate rejects `plain`) |
| `automatic_authorization` | recommended | `true` for "one-click" user provisioning |
| `username_claim` | recommended | `preferred_username` (matches skygate's id_token claim) |
| `email_to_username_claim_separator` | optional | Strip email domain; empty = use full email |

---

## 4. Apply + restart headscale

```bash
# If headscale runs as a systemd service:
sudo systemctl restart headscale

# If headscale runs in a container:
docker restart headscale

# If headscale runs in Kubernetes:
kubectl rollout restart deployment/headscale

# Wait 5-10s for headscale to come back, then check logs:
sudo journalctl -u headscale -n 50     # systemd
docker logs -f headscale               # container
```

**What to look for in the logs:**

- `INFO using OIDC issuer https://skygate.example.com` → headscale
  loaded the block + the issuer string.
- `INFO OIDC discovery doc fetched: 5 endpoints ...` → headscale
  successfully hit `/.well-known/openid-configuration` and parsed all
  4 endpoint URLs.
- `WARN OIDC discovery: failed to fetch ...` → check the issuer URL
  is reachable from the headscale host.
- `FATAL no oidc provider configured` → the YAML block didn't parse
  (indentation issue?). Run `headscale validate` (headscale 0.29.x)
  before restarting.

---

## 5. Verify the discovery doc (optional but recommended)

Before any Tailscale client tries to log in, confirm headscale can talk
to skygate. Run a 3-step smoke test from the headscale host (or any
host that can reach the skygate OIDC endpoints):

```bash
# 1. Discovery doc — the canonical "is the OIDC provider alive?" probe.
curl -sS https://skygate.example.com/.well-known/openid-configuration | jq .
# Expected: JSON with issuer, authorization_endpoint, token_endpoint,
# userinfo_endpoint, jwks_uri, id_token_signing_alg_values_supported,
# response_types_supported, scopes_supported,
# token_endpoint_auth_methods_supported.

# 2. JWKS — headscale uses this to verify the id_token's RS256
# signature. Must return exactly 1 key.
curl -sS https://skygate.example.com/oidc/jwks.json | jq .

# 3. /oidc/authorize must return 302 (not 500).
curl -sS -o /dev/null -w "%{http_code}\n" \
    "https://skygate.example.com/oidc/authorize?response_type=code&client_id=headscale&redirect_uri=https%3A%2F%2Fhead.example.com%2Foidc%2Fcallback&state=test&scope=openid+profile+email"
```

If any of these fail, the operator gets a clear error in headscale
logs (`journalctl -u headscale` or `docker logs headscale | tail -20`):

| Error | Fix |
|---|---|
| `OIDC discovery failed: 404` | skygate's `/.well-known/openid-configuration` is not routed. Check `mux.Handle("/.well-known/", ...)` in main.go. |
| `OIDC discovery failed: 502` | skygate is not responding. Check `curl http://localhost:8080/healthz` on the skygate host. |
| `jwks_uri returned no keys` | The OIDC keypair is not initialised. Check `SKYGATE_OIDC_KEY_DIR` (default `./data/oidc-keys`) is mounted + writable. |
| `issuer mismatch` | `oidc.issuer` in headscale.conf != `SKYGATE_OIDC_ISSUER` (trailing slash + case mismatches are the most common). |
| `client_id mismatch` | `oidc.client_id` in headscale.conf != `SKYGATE_OIDC_CLIENT_ID` (default `"headscale"`). |
| `redirect_uri mismatch` | The redirect_uri headscale sends doesn't match the value in skygate's `SKYGATE_OIDC_REDIRECT_URIS` (exact-string match per B161.2). |

---

## 6. The end-to-end OIDC flow

```
Tailscale client → headscale
                → 302 to https://<skygate>/oidc/authorize
                → 302 to https://<skygate>/login (if not signed in)
                → user signs in at skygate
                → 302 to https://<skygate>/oidc/authorize (again, authenticated)
                → 302 to <headscale>/oidc/callback?code=...&state=...
                → headscale POSTs to https://<skygate>/oidc/token
                → headscale GETs https://<skygate>/oidc/userinfo
                → headscale creates the user (if not exists)
                → Tailscale client is now authenticated
```

The whole flow takes 1-3 seconds end-to-end (excluding the human login
step, which is 5-30s for the user to type their password).

### 6.1. Prerequisites

- A Tailscale client (macOS, Windows, Linux, iOS, Android) with the
  "Use custom coordination server" option in the GUI.
- The operator's headscale URL, e.g. `https://head.example.com`.
- An skygate user account (the operator must log into
  `https://skygate.example.com/login` first to create the session
  cookie that the OIDC flow expects).

### 6.2. Step-by-step (what should happen)

1. User runs `tailscale up --login-server=https://head.example.com` on
   a device.
2. Tailscale opens the browser to headscale's login page.
3. headscale redirects to:
   `https://skygate.example.com/oidc/authorize?
      response_type=code
      &client_id=headscale
      &redirect_uri=https%3A%2F%2Fhead.example.com%2Foidc%2Fcallback
      &state=<csrf>
      &scope=openid+profile+email
      &code_challenge=<sha256(verifier)-base64url>
      &code_challenge_method=S256`
4. skygate checks the session cookie. If the user is logged in →
   302 to headscale's `redirect_uri?code=<auth_code>&state=<csrf>`.
   If not → 302 to `/login?next=<the full /oidc/authorize URL>`.
5. After login, the user lands on headscale's callback. headscale
   POSTs to: `https://skygate.example.com/oidc/token` with
   `grant_type=authorization_code&code=<auth_code>&client_id=headscale&client_secret=...&redirect_uri=...&code_verifier=...`
6. skygate returns the id_token + access_token (RS256-signed JWTs).
7. headscale calls `https://skygate.example.com/oidc/userinfo` with
   `Authorization: Bearer <access_token>`.
8. skygate returns the user claims (sub + email + name +
   preferred_username).
9. headscale auto-provisions the user (because
   `automatic_authorization: true`) and issues Tailscale a netmap. The
   Tailscale client transitions to "Connected".

### 6.3. Common failure modes (operator triage)

| Symptom | Likely cause | Fix |
|---|---|---|
| headscale log: `OIDC discovery: failed to fetch` | issuer URL is wrong OR the headscale host can't reach skygate | `curl https://<issuer>/.well-known/openid-configuration` from the headscale host. If 200, the URL is wrong (typo). If timeout, the network path is blocked. |
| headscale log: `OIDC: invalid client_id` | `oidc.client_id` doesn't match `SKYGATE_OIDC_CLIENT_ID` | Check both configs. Note: case-sensitive. |
| headscale log: `OIDC: redirect_uri mismatch` | `oidc.redirect_url` doesn't match skygate's allowlist | Check trailing slash, http vs https, port number. skygate uses exact-string match. |
| Browser stuck on `/oidc/authorize?` after login | redirect_uri mismatch | Same as above. Login succeeded, but skygate can't redirect back. |
| `400 invalid_client` from /oidc/token | `client_secret` mismatch | Re-paste from `/etc/skygate-secrets/oidc.env`. Note: skygate uses constant-time compare, so partial mismatch returns 400 (not 401) per RFC 6749 §5.2. |
| `400 invalid_grant` from /oidc/token | Auth code expired (5min TTL) OR already used OR PKCE verifier mismatch | Refresh the page. If persistent, check system clocks of headscale + skygate are within 5min. |
| "Invalid state parameter" | headscale logs | The `state` round-trip is broken. Check `redirect_uri` and `SKYGATE_OIDC_REDIRECT_URIS` are byte-identical. |
| "User not found" / 500 on /oidc/userinfo | skygate logs (`oidc.userinfo: parseAccessToken: ...`) | The skygate access_token can't verify with the JWKS. Check skygate's JWKS has the same `kid` as the JWT header. |
| "tailscale: authentication failed" | Tailscale client | headscale returned 401 after /oidc/userinfo. The user is unknown to headscale. Check `automatic_authorization: true`. |
| Tailscale shows "Logged in" but no IP / can't ping | ACL issue, not OIDC | Check `/admin/acls` + `acl.policy.hujson`. |

---

## 7. Verify on the skygate side

After the first Tailscale client signs in via OIDC:

- Check `/admin/oidc` — the page should now show that the OIDC flow is
  working.
- Check `/admin/users` — a new `portal_users` row should have been
  created for the OIDC user (or the existing row's `last_login_at`
  should be updated).
- Check `/admin/audit` — you should see an `oidc.token` audit row with
  the username + client_id.

---

## 8. headscale version matrix

The `oidc:` config block has been stable since headscale 0.23.0 (2024).
All fields listed above are supported in:

- headscale 0.23.x — supported (early form)
- headscale 0.25.x — supported
- headscale 0.27.x — supported
- headscale 0.29.x — supported (recommended)
- headscale 0.30.x — supported (current)

For older headscale (< 0.23.0), the OIDC config block is in a different
location — see the headscale docs for your specific version.

---

## 9. After it works

- **Backup**: skygate's `derp_relays`, `node_owner_map`, `oidc-keys/`
  (RSA keypair for JWT signing) should all be in your nightly backup.
  The OIDC keypair is critical — losing it invalidates every issued
  JWT (the kid changes, clients can't verify).
- **Monitoring**: add a watch on `headscale` journal for `OIDC` log
  lines. An unexpected spike in 4xx is usually a misconfigured client
  (or an attacker — B161 uses constant-time compare + 5min code TTL +
  single-use, so brute-force is hard).
- **Rotation**: rotate `SKYGATE_OIDC_CLIENT_SECRET` by editing the
  secrets file and restarting the skygate container. Existing issued
  JWTs stay valid (1h TTL); after 1h, the operator's headscale will
  see a new auth challenge.

---

## 10. Rolling back

If the OIDC integration breaks production, disable it without removing
the config:

1. Comment out the `oidc:` block in `headscale.conf`.
2. Restart headscale.
3. headscale falls back to its pre-OIDC auth (CLI user creation,
   preauth keys, etc.).
4. Tailscale clients that were already authenticated stay authenticated
   until their auth key expires (or the headscale user record is
   deleted).
5. Investigate the failure mode (most common: a misconfigured
   `allowed_domains` or a `client_secret` that was rotated on the
   skygate side but not the headscale side).
6. Fix the config + restart headscale again.

The skygate OIDC provider stays up the whole time — Tailscale clients
that aren't routed through headscale are unaffected.

---

## 11. e2e test (skygate repo)

The skygate repo has a unit-level end-to-end test at
`internal/oidc/e2e_test.go::TestE2E_HeadscaleClientFlow` that exercises
the full headscale-style flow:

1. `GET /.well-known/openid-configuration`
2. `GET /oidc/jwks.json`
3. `GET /oidc/authorize?response_type=code&...`
4. `POST /oidc/token` (with `code_verifier` PKCE)
5. `GET /oidc/userinfo` (Bearer auth)

If a future refactor breaks the cross-endpoint contract, this test
fails. It's run on every push (`.githooks/pre-push` →
`scripts/verify_pre_deploy.sh`). The contract pins 24 sub-checks; all
are in `scripts/check_b161.sh` contract I.

For a real end-to-end smoke test against the live skygate, open
`/admin/oidc` and click "Test connection" — the probe runs in <1s and
confirms discovery + JWKS + userinfo are all reachable.

The B161.4 B-check (`scripts/check_b161_4.sh`) covers the LIVE
end-to-end:
- discovery doc reachable + has the right fields
- JWKS has 1 key with RS256 + the expected kid
- /oidc/authorize returns 302 (not 500) for a valid request
- /oidc/token returns 400 `invalid_client` for bad creds (proves the
  client_secret check is active)
- /oidc/token returns 400 `invalid_grant` for an unknown code (proves
  the auth code store works)

---

## 12. Reusable lessons (for future OIDC integrations)

1. **The 4 values that MUST match** between skygate and headscale:
   `issuer`, `client_id`, `client_secret`, `redirect_uri`. A typo in
   any of them = silent auth failure.
2. **The discovery doc is the OIDC "heartbeat"** — if it's reachable,
   the IdP is alive. If 502, it's down. If 404, it isn't routing the
   well-known path. Check that first.
3. **The redirect_uri must match byte-for-byte**. RFC 6749 §3.1.2: the
   authorization server MUST require exact match. skygate does an
   exact-string match (B161.2). A trailing slash or query param
   mismatch = 400.
4. **`automatic_authorization: true` is the "one-click UX"** the
   operator wants. Without it, the operator has to manually run
   `headscale users create <email>` for every new OIDC user.
5. **Tailscale's auth flow is server-driven**: the client just opens
   the browser to the login-server URL. Everything else happens
   between headscale (the SP) and skygate (the IdP). The Tailscale
   client has no idea OIDC is involved — it just sees a successful
   auth.

---

## 13. References

- B161 release notes in AGENTS.md §"Current"
- `docs/features.md` §"OIDC (B161)"
- `docs/internal/runbooks/ha-v1.5.0-execution.md` §"Locked-in
  decisions" (decisions #16 + #18 on how the OIDC + headplane secrets
  are stored)
- RFC 6749 (OAuth 2.0 Authorization Framework)
- RFC 8414 (OAuth 2.0 Authorization Server Metadata)
- headscale docs: https://headscale.net/apidoc/ (search for `oidc:`
  block)
