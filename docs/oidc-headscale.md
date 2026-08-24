# OIDC integration: skygate ⇄ headscale (B161.4)

This runbook walks the operator through enabling Tailscale
user authentication against skygate's OIDC provider, so
that headscale creates users on demand when a Tailscale
client signs in. The flow is:

```
Tailscale client → headscale
                → 302 to https://<skygate>/oidc/authorize
                → 302 to https://<skygate>/login (if not signed in)
                → user signs in at skygate
                → 302 to https://<skygate>/oidc/authorize (again, now authenticated)
                → 302 to <headscale>/oidc/callback?code=...&state=...
                → headscale POSTs to https://<skygate>/oidc/token
                → headscale GETs https://<skygate>/oidc/userinfo
                → headscale creates the user (if not exists)
                → Tailscale client is now authenticated
```

The skygate side (B161.1 + B161.2 + B161.3) is already
shipped and live. **The only remaining work is on the
headscale side**: paste a config block into `headscale.conf`
and restart headscale.

---

## 1. Prerequisites

- skygate is running with B161.3 or later (v1.4.4-30+
  or any v1.5.x). Verify at `/admin/oidc` — the
  page should show "OIDC provider is enabled" with
  the 5 endpoint URLs filled in.
- You have admin access to headscale (SSH to the VM
  where headscale is running, or `kubectl exec` into
  the headscale pod).
- You have the skygate `SKYGATE_OIDC_CLIENT_SECRET`
  value (in `/home/admin/skygate/.env` on the skygate
  host, or wherever your secrets live).

---

## 2. Verify the skygate side

Open `/admin/oidc` in your browser. You should see:

- A green "OIDC provider is reachable" banner (or
  click "Test connection" if not already there).
- The 5 endpoint URLs:
  - `Issuer`: `https://<your-skygate-domain>`
  - `Discovery`: `<issuer>/.well-known/openid-configuration`
  - `Authorization`: `<issuer>/oidc/authorize`
  - `Token`: `<issuer>/oidc/token`
  - `Userinfo`: `<issuer>/oidc/userinfo`
  - `JWKS`: `<issuer>/oidc/jwks.json`
- A copy-paste `headscale.conf` snippet with the
  issuer + client_id pre-filled.

Copy the snippet. You'll paste it into headscale.conf
in the next step.

---

## 3. Edit `headscale.conf`

Open the `headscale.conf` file on the headscale host
(default: `/etc/headscale/config.yaml` or
`/etc/headscale/config.yml`).

Find the `oidc:` block (if it doesn't exist, add it
at the top level of the YAML, not nested inside any
other section). Paste the snippet from `/admin/oidc`:

```yaml
oidc:
  issuer: https://<your-skygate-domain>
  client_id: headscale
  client_secret: <paste-SKYGATE_OIDC_CLIENT_SECRET-here>
  scope: [openid, profile, email]
  extra_params:
    domain: client_id
  allowed_domains:
    - example.com     # CHANGE THIS to your tailnet's email base domain
  auto_update: true
  strip_email_domain: true
```

Adjust the values:

- `client_secret` — paste the value from the
  `SKYGATE_OIDC_CLIENT_SECRET` env var (do NOT use
  the placeholder).
- `allowed_domains` — replace `example.com` with the
  email domain your Tailscale users use (e.g. if
  your tailnet users have `@ts.net` emails, set
  this to `ts.net`). headscale rejects logins from
  users outside this list, so a typo here is the
  most common reason for "I see the login page but
  headscale says access denied".

Save the file.

---

## 4. Restart headscale

```bash
# If headscale runs as a systemd service:
sudo systemctl restart headscale

# If headscale runs in a container:
docker restart headscale

# If headscale runs in Kubernetes:
kubectl rollout restart deployment/headscale
```

Wait 5-10 seconds for headscale to come back up. Watch
the logs:

```bash
sudo journalctl -u headscale -f
# or
docker logs -f headscale
```

Look for: `OIDC config loaded`, `OIDC provider URL: ...`,
`OIDC client_id: headscale`. If you see
`OIDC discovery failed` or `OIDC issuer not reachable`,
double-check the `issuer` URL — headscale is strict
about trailing slashes (must NOT have one).

---

## 5. Test the integration

Open a Tailscale client (phone, laptop, or
`tailscale up --login-server=...` on a server). Try
to sign in.

Expected flow:

1. Tailscale asks headscale for an auth URL.
2. headscale returns `302 https://<skygate>/oidc/authorize?...`
3. Your browser opens the skygate login page.
4. If you're not signed in to skygate, you see the
   skygate `/login` form.
5. Sign in with your skygate username + password.
6. skygate redirects back to headscale's
   `/oidc/callback?code=...&state=...`
7. headscale exchanges the code for an access token
   (POST to `https://<skygate>/oidc/token`)
8. headscale calls `https://<skygate>/oidc/userinfo`
   with the access token
9. skygate returns `{sub, email, name, preferred_username}`
10. headscale creates the user (if not exists) and
    lets the Tailscale client in.

If anything fails, the most common causes (in order
of frequency):

1. **`allowed_domains` typo** — headscale says
   "access denied" right after the skygate login.
2. **Trailing slash in `issuer`** — headscale says
   "OIDC discovery failed". Remove the trailing `/`.
3. **Wrong `client_secret`** — headscale says
   "invalid_client" on the token exchange. Re-paste
   the value from `/home/admin/skygate/.env`.
4. **skygate not reachable from headscale** — check
   that headscale can `curl https://<skygate>/.well-known/openid-configuration`
   successfully.

---

## 6. Verify on the skygate side

After the first Tailscale client signs in via OIDC:

- Check `/admin/oidc` — the page should now show that
  the OIDC flow is working.
- Check `/admin/users` — a new portal_users row should
  have been created for the OIDC user (or the existing
  row's `last_login_at` should be updated).
- Check `/admin/audit` — you should see an
  `oidc.token` audit row with the username + client_id.

---

## 7. Field reference (full)

See `/admin/oidc` → "Field-by-field reference" for the
full explanation of every headscale.conf field in the
snippet. The short version:

| Field | Required | Notes |
|---|---|---|
| `issuer` | yes | The exact `SKYGATE_OIDC_ISSUER` value (no trailing `/`) |
| `client_id` | yes | Must match `SKYGATE_OIDC_CLIENT_ID` (default: `headscale`) |
| `client_secret` | yes | Must match `SKYGATE_OIDC_CLIENT_SECRET` (paste from skygate host) |
| `scope` | yes | Must include `openid`; `profile` + `email` recommended |
| `extra_params.domain` | no | Non-OIDC-standard; any non-empty value works |
| `allowed_domains` | yes | Email domain(s) headscale accepts (post-`@`) |
| `auto_update` | no | `true` for dev, `false` for production (avoid re-apply storms) |
| `strip_email_domain` | no | `true` to use email's local part as the headscale username |

---

## 8. headscale version matrix

The `oidc:` config block has been stable since
headscale 0.23.0 (2024). All fields listed above
are supported in:

- headscale 0.23.x — supported (early form)
- headscale 0.25.x — supported
- headscale 0.27.x — supported
- headscale 0.29.x — supported (recommended)
- headscale 0.30.x — supported (current)

For older headscale (< 0.23.0), the OIDC config
block is in a different location — see the
headscale docs for your specific version.

---

## 9. Rolling back

If the OIDC integration breaks production, disable
it without removing the config:

1. Comment out the `oidc:` block in `headscale.conf`.
2. Restart headscale.
3. headscale falls back to its pre-OIDC auth (CLI
   user creation, preauth keys, etc.).
4. Tailscale clients that were already authenticated
   stay authenticated until their auth key expires
   (or the headscale user record is deleted).
5. Investigate the failure mode (most common: a
   misconfigured `allowed_domains` or a `client_secret`
   that was rotated on the skygate side but not the
   headscale side).
6. Fix the config + restart headscale again.

The skygate OIDC provider stays up the whole time —
Tailscale clients that aren't routed through headscale
are unaffected.

---

## 10. e2e test (skygate repo)

The skygate repo has a unit-level end-to-end test at
`internal/oidc/e2e_test.go::TestE2E_HeadscaleClientFlow`
that exercises the full headscale-style flow:

1. `GET /.well-known/openid-configuration`
2. `GET /oidc/jwks.json`
3. `GET /oidc/authorize?response_type=code&...`
4. `POST /oidc/token` (with `code_verifier` PKCE)
5. `GET /oidc/userinfo` (Bearer auth)

If a future refactor breaks the cross-endpoint
contract, this test fails. It's run on every push
(`.githooks/pre-push` → `scripts/verify_pre_deploy.sh`).
The contract pins 24 sub-checks; all are in
`scripts/check_b161.sh` contract I.

For a real end-to-end smoke test against the live
skygate, open `/admin/oidc` and click "Test
connection" — the probe runs in <1s and confirms
discovery + JWKS + userinfo are all reachable.
