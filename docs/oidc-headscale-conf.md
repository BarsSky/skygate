# OIDC configuration for headscale (B161.4)

Operator 2026-08-24: after skygate v1.5.0 ships the
OIDC provider (B161.1-3) + the operator UX fixes
(B162-166), the last step to close the OIDC loop
is wiring **headscale** to use skygate as the OIDC
identity provider. This doc gives the operator a
copy-paste-ready `headscale.conf` snippet and the
verification steps.

**Audience:** the operator (or anyone running
headscale alongside skygate).

**Reference:** the OIDC discovery doc is at
`https://<skygate-host>/.well-known/openid-configuration`.
The B161.1-3 surfaces (in `internal/oidc/`) document
the exact JSON shape skygate publishes.

---

## 1. The `oidc:` block

Add this to `/etc/headscale/config.yaml` on the
headscale host. All values are placeholders — replace
with your real skygate host + the values that match
`SKYGATE_OIDC_*` env vars on the skygate container
(`/home/skyadmin/skygate/.env`).

```yaml
oidc:
  # The public URL of the skygate OIDC provider.
  # MUST match exactly what skygate's SKYGATE_OIDC_ISSUER
  # is set to. headscale uses this to discover the
  # endpoints (the discovery doc lives at
  # ${issuer}/.well-known/openid-configuration).
  issuer: "https://skygate.example.com"

  # Must match SKYGATE_OIDC_CLIENT_ID on the skygate
  # side. headscale sends this in every /token call;
  # skygate rejects any client_id that doesn't match
  # (RFC 6749 sec 2.3.1 — unknown client = invalid_client).
  client_id: "headscale"

  # Must match SKYGATE_OIDC_CLIENT_SECRET on the skygate
  # side. Use a real random secret in production
  # (e.g. `openssl rand -base64 32`). skygate uses
  # constant-time comparison (secureEqual helper) to
  # defend against timing attacks; a leaked secret
  # still requires a restart of skygate to take effect.
  client_secret: "<paste the value of SKYGATE_OIDC_CLIENT_SECRET from /home/skyadmin/skygate/.env>"

  # Must match SKYGATE_OIDC_REDIRECT_URIS on the skygate
  # side (default: https://head.skynas.ru/oidc/callback).
  # skygate's allowedRedirect() does an EXACT-STRING match
  # (RFC 6749 sec 3.1.2.3 — no wildcards, no substring
  # match) so headscale MUST send exactly the same string
  # including trailing slash, port, and protocol.
  redirect_uri: "https://head.skynas.ru/oidc/callback"

  # PKCE S256 only. skygate rejects "plain" at the
  # /authorize step (B161.2 contract H) and at /token
  # (B161.3 contract I). headscale 0.29.x defaults to
  # S256; if you have an older version, set this
  # explicitly.
  pkce:
    method: "S256"

  # Optional: override the default "openid" scope.
  # skygate advertises ["openid", "profile", "email"]
  # in the discovery doc and uses them to decide which
  # claims to include in the id_token + userinfo
  # (sub always, name + preferred_username from "profile",
  # email from "email"). headscale only needs "openid"
  # for auth; "profile" + "email" are nice-to-haves
  # for the user-mapping.
  scope: ["openid", "profile", "email"]

  # Optional: extra params headscale sends in the
  # authorize redirect. Not required for the basic
  # flow. Leave empty unless you have a specific
  # reason (e.g. acr_values for an IdP that supports
  # MFA prompts).
  extra_params: {}

  # Optional: list of allowed email domains for
  # auto-provisioning. NOT supported by skygate in
  # v1.5.0 — skygate always creates the user on
  # first login. Leave this empty.
  domain: ""
```

**That's the only block you need.** The rest of
`headscale.conf` (the `server_url`, `listen_addr`,
`dns_config`, etc.) is unchanged.

---

## 2. Apply + restart headscale

```bash
ssh <headscale-host>
sudo vi /etc/headscale/config.yaml  # paste the snippet above
sudo systemctl restart headscale
sudo journalctl -u headscale -n 50  # check for parse errors
```

**What to look for in the logs:**

- `INFO using OIDC issuer https://skygate.example.com`
  → headscale loaded the block + the issuer string
- `INFO OIDC discovery doc fetched: 5 endpoints
  (authorization, token, userinfo, jwks, ...)`
  → headscale successfully hit `/.well-known/openid-configuration`
  and parsed all 4 endpoint URLs
- `WARN OIDC discovery: failed to fetch ...`
  → check the issuer URL is reachable from the headscale
  host (curl `https://skygate.example.com/.well-known/openid-configuration`
  from the headscale host — should return 200 + JSON)
- `FATAL no `oidc` provider configured`
  → the YAML block didn't parse (indentation issue?)
  — `headscale validate` (headscale 0.29.x) before restarting

---

## 3. Verify the discovery doc (optional but recommended)

Before any Tailscale client tries to log in, confirm
headscale can talk to skygate:

```bash
# On the headscale host:
curl -sS https://skygate.example.com/.well-known/openid-configuration | python3 -m json.tool
```

You should see all 4 endpoint URLs (authorization_endpoint,
token_endpoint, userinfo_endpoint, jwks_uri) with the
right scheme + host. If any of them is wrong, headscale
will silently fall back to 4xx on the first real auth
attempt — much harder to debug.

---

## 4. The end-to-end OIDC flow (what happens on first login)

This is the B161.1-3 flow. If you're seeing weird
errors, this section is the map.

```
Tailscale client (on user's laptop/phone)
   │
   │ 1. User opens Tailscale → "Sign in"
   │    (with custom coord server = skygate.example.com)
   │
   ▼
headscale (on the headscale host)
   │
   │ 2. headscale sees an unknown OIDC user, generates
   │    a state + nonce + PKCE challenge, builds the
   │    authorize URL, redirects the browser to:
   │
   │    302 Location: https://skygate.example.com/oidc/authorize?
   │        response_type=code&
   │        client_id=headscale&
   │        redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback&
   │        scope=openid+profile+email&
   │        state=<random-32-bytes-base64url>&
   │        nonce=<random-32-bytes-base64url>&
   │        code_challenge=<sha256(verifier)-base64url>&
   │        code_challenge_method=S256
   │
   ▼
Browser (user's) → skygate.example.com/oidc/authorize
   │
   │ 3. skygate checks:
   │    - client_id matches "headscale"  ✓
   │    - redirect_uri is in the allowlist (exact match)  ✓
   │    - response_type == "code"  ✓
   │    - code_challenge_method == "S256"  ✓
   │    - state is set (echoed back)  ✓
   │
   │ 4. IF the user is NOT logged in to skygate:
   │    302 Location: /login?next=/oidc/authorize?...full params...
   │    (skygate's standard login flow, with the OIDC
   │    params preserved in the next= redirect so the
   │    browser comes back to /authorize after login)
   │
   │    IF the user IS logged in:
   │    302 Location: https://head.skynas.ru/oidc/callback?code=<32-bytes>&state=<same-state>
   │
   ▼
Browser → head.skynas.ru/oidc/callback?code=...&state=...
   │
   │ 5. headscale extracts code + state, calls skygate:
   │    POST https://skygate.example.com/oidc/token
   │    body: grant_type=authorization_code&code=...&client_id=headscale&client_secret=...&redirect_uri=...&code_verifier=...
   │
   ▼
skygate.example.com/oidc/token
   │
   │ 6. skygate checks:
   │    - client_id + client_secret match (constant-time compare)  ✓
   │    - code exists in the in-memory store + not expired  ✓
   │    - redirect_uri matches the one from /authorize (token-side
   │      defense — prevents code-stealing)  ✓
   │    - code_verifier's SHA256 matches the stored
   │      code_challenge (PKCE S256)  ✓
   │    - code is consumed (deleted from the store — single-use)  ✓
   │
   │ 7. skygate signs an RS256 id_token (with the user
   │    profile claims: sub, email, name, preferred_username)
   │    + an RS256 access_token (same claims), returns:
   │
   │    { "access_token": "...", "id_token": "...",
   │      "token_type": "Bearer", "expires_in": 3600,
   │      "scope": "openid profile email" }
   │
   ▼
headscale calls skygate:
   │
   │ 8. GET https://skygate.example.com/oidc/userinfo
   │    Authorization: Bearer <access_token>
   │
   ▼
skygate verifies access_token (RS256 + kid match) →
returns:
   │
   │    { "sub": "alice",
   │      "email": "alice@example.com",
   │      "name": "alice",
   │      "preferred_username": "alice" }
   │
   ▼
headscale creates the headscale user (if not already
present) + adds them to a default ACL, gives the
Tailscale client a tailnet IP. The user is now in.
```

**The whole flow takes 1-3 seconds end-to-end** (excluding
the human login step, which is 5-30s for the user to
type their password).

---

## 5. Common failure modes (operator triage)

| Symptom | Likely cause | Fix |
|---|---|---|
| headscale log: `OIDC discovery: failed to fetch` | issuer URL is wrong OR the headscale host can't reach skygate | `curl https://<issuer>/.well-known/openid-configuration` from the headscale host. If 200, the URL is wrong (typo). If timeout, the network path is blocked. |
| headscale log: `OIDC: invalid client_id` | headscale's `client_id` doesn't match skygate's `SKYGATE_OIDC_CLIENT_ID` | Check both configs. Note: case-sensitive. |
| headscale log: `OIDC: redirect_uri mismatch` | headscale's `redirect_uri` doesn't match skygate's allowlist (the value of `SKYGATE_OIDC_REDIRECT_URIS`) | Check for trailing slash, http vs https, port number. skygate uses exact-string match. |
| Browser stuck on `/oidc/authorize?` after login | headscale's `redirect_uri` doesn't match the value in skygate's allowlist | Same as above. The login succeeded, but skygate can't redirect back. |
| `400 invalid_client` from /oidc/token | `client_secret` mismatch | Re-paste from `.env`. Note: skygate uses constant-time compare, so a partial mismatch returns 400 (not 401) per RFC 6749 sec 5.2. |
| `400 invalid_grant` from /oidc/token | Auth code expired (5min TTL) OR already used OR PKCE verifier doesn't match | Refresh the page, try again. If persistent, check that the system clocks of headscale + skygate are within 5min of each other. |
| Tailscale client gets tailnet IP, but is offline / can't ping other nodes | ACL issue, not OIDC | Check `/admin/acls` + the headscale `acl.policy.hujson`. The B111 catch-all `* → tag:dev-infra-X` should still apply. |

---

## 6. After it works

- **Backup**: skygate's `derp_relays`, `node_owner_map`, `oidc-keys/` (RSA keypair for JWT signing) should all be in your nightly backup. The OIDC keypair is critical — losing it invalidates every issued JWT (the kid changes, clients can't verify).
- **Monitoring**: add a watch on `headscale` journal for `OIDC` log lines. An unexpected spike in 4xx is usually a misconfigured client (or an attacker — B161 uses constant-time compare + 5min code TTL + single-use, so brute-force is hard).
- **Rotation**: rotate `SKYGATE_OIDC_CLIENT_SECRET` by editing `.env` and restarting the skygate container. Existing issued JWTs stay valid (1h TTL); after 1h, the operator's headscale will see a new auth challenge.

---

## 7. References

- **skygate side**: `internal/oidc/` — discovery.go, jwks.go, authorize.go, token.go, userinfo.go, jwt.go
- **Live state**: VM at `v1.5.0-alpha1-23-gd7c8ca6` (commit `d7c8ca6`), 4 OIDC env vars in `/home/skyadmin/skygate/.env`
- **OIDC env vars (skygate side)**:
  - `SKYGATE_OIDC_ISSUER` (e.g. `https://skygate.example.com`)
  - `SKYGATE_OIDC_CLIENT_ID` (e.g. `headscale`)
  - `SKYGATE_OIDC_CLIENT_SECRET` (e.g. `openssl rand -base64 32`)
  - `SKYGATE_OIDC_KEY_DIR` (e.g. `/data/oidc-keys`)
  - `SKYGATE_OIDC_REDIRECT_URIS` (e.g. `https://head.skynas.ru/oidc/callback`)
- **headscale docs**: <https://headscale.net/stable/ref/oidc/>
- **B161 spec**: <https://openid.net/specs/openid-connect-core-1_0.html> (OIDC Core), <https://openid.net/specs/openid-connect-discovery-1_0.html> (Discovery)
