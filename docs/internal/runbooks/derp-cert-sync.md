# B252 — DERP cert auto-renewal (operator runbook)

> Audience: operators running a bundled derper (B164 deployment) with
> skygate. Pre-B252 the cert on `/var/lib/derper/certs/<hostname>.{crt,key}`
> is a static file that does not auto-renew. B252 embeds three renewal
> modes in skygate as a daily cron.

## Three modes

| Mode | When | What skygate does |
|---|---|---|
| `letsencrypt` | derper runs with `--certmode=letsencrypt` and the LE HTTP-01 challenge on port 80 is reachable | **No-op.** derper itself renews every ~60 days via acme. skygate just reads the current cert, parses NotAfter, and surfaces "expires in X days" on /admin/derp. |
| `npm` | derper runs with `--certmode=manual`; cert comes from Nginx Proxy Manager (NPM) at `<base_url>:81` | Every 24h: login to NPM API → `GET /api/nginx/certificates/<id>/download` → unzip `fullchain*.pem` + `privkey*.pem` → write to `<cert_dir>/<hostname>.crt` + `<hostname>.key` → SHA256 compare to last_cert_sha256 → if changed, `systemctl reload derper` (SIGHUP, no connection drop). |
| `manual` | You pay for a paid CA / private CA / air-gapped cert | **No-op.** skygate reads NotAfter + shows it on the admin page so you see the warning before derper silently breaks. |

## Configuring the cron (the only step the operator does)

There is no UI form for adding rows yet (planned for v1.5.7 — the row
config is currently `psql` or `deploy.sh --add-cert-sync`). Until then:

```sql
-- Mode 'letsencrypt' (no NPM, no extra fields):
INSERT INTO derp_cert_sync (hostname, mode, cert_dir, enabled)
VALUES ('derp.example.com', 'letsencrypt', '/var/lib/derper/certs', 1);

-- Mode 'npm' (requires NPM base_url + cert_id; identity/secret
-- live in global_settings so /admin/derp/cert-sync/edit can change
-- them without a restart):
INSERT INTO derp_cert_sync (hostname, mode, npm_base_url, npm_cert_id,
                            cert_dir, derper_systemd_unit, enabled)
VALUES ('derp.example.com', 'npm',
        'http://<NPM_VM_LAN>:81', 33,
        '/var/lib/derper/certs', 'derper.service', 1);

INSERT INTO global_settings (key, value) VALUES
  ('derp.npm.identity', 'knagaenko@mail.ru'),
  ('derp.npm.secret',   '...');   -- never commit this

-- Mode 'manual':
INSERT INTO derp_cert_sync (hostname, mode, cert_dir, enabled)
VALUES ('derp.example.com', 'manual', '/var/lib/derper/certs', 1);
```

## NPM credentials (mode='npm' only)

NPM login + cert download use the operator's NPM credentials.
Prefer storing them in skygate's `global_settings` table so the admin
page can rotate them without a restart:

```sql
INSERT INTO global_settings (key, value) VALUES
  ('derp.npm.identity', '<npm_email>'),
  ('derp.npm.secret',   '<npm_password>');
```

Fallback for env-var purists: `NPM_IDENTITY` + `NPM_SECRET` env vars on
the skygate process. `global_settings` wins if both are set.

## Reload strategy (mode='npm' only)

The cron writes the new cert files then needs derper to pick them up.
Two strategies, tried in order:

1. `systemctl reload derper.service` — the B-derper-cert deployment
   shape. Exec'd via `os/exec`. Works for systemd-managed derpers.
2. `kill -HUP $(cat <derper_pid_file>)` — fallback for non-systemd
   deployments (the B164 docker-compose bundle). PID file path is
   `derper_pid_file` in the row config (default `/var/run/derper.pid`).

Both strategies keep existing DERP connections alive (SIGHUP = graceful
reload, not a process restart). The pre-B252 hand-rolled "systemctl
restart derper" forced a 1-3 second connection blip; B252 doesn't.

## Making skygate talk to derper on the same host (LAN-direct)

Two paths depending on your deployment shape:

### systemd (install-debian.sh, the B-bug-fix + B-derper-cert path)

Add to the agent VM's `/etc/hosts`:

```
<VM_HOST_LAN>  derp.example.com
```

Then Tailscale clients on the LAN dial `<VM_HOST_LAN>:443` directly
(1-2ms) instead of going through `95.165.170.190` → NPM → `13.69:443`
(50-100ms). The hostname-to-LAN-IP mapping is what lets derper's
TLS cert (CN=`derp.example.com`) verify correctly.

Pre-B252 this entry had to be added manually. B252 ships a hint in
the `/admin/derp` page → Cert auto-renewal section ("If derper and
skygate are on the same host..."). The hint mentions both this
`/etc/hosts` path (systemd) and the `extra_hosts` path (docker-compose).

### docker-compose (the B164 / B237 path)

If skygate is in docker-compose alongside derper, both share the same
Docker network. Add to the skygate service:

```yaml
services:
  skygate:
    # ...existing config...
    extra_hosts:
      - "derp.example.com:host-gateway"
```

`host-gateway` is Docker's reserved name for the host machine. The
derper container runs with `network_mode: host` (see `deploy/templates/
derper-compose.yml.tmpl`), so it listens on the host's `:443`. The
`extra_hosts` entry makes the skygate container resolve `derp.example.com`
to the host loopback, then dial `:443` which lands on derper.

Why this matters: without the entry, the skygate container tries
`https://derp.example.com:443/` via Docker's DNS, gets a "no such host"
error, and the /admin/derp UI shows "DERP socket: closed" even though
derper is healthy.

## "Sync now" button

`POST /admin/derp/cert-sync/run` runs `SyncOne` for every enabled row
once, then redirects back to /admin/derp. Use it:

- After editing the NPM credentials (the daily cron would catch up
  tomorrow, but you want instant feedback).
- After a manual upstream CA rotation (e.g. you just paid for a
  3-year cert and want it pushed to derper now).
- After debugging a "ERR: npm login returned 401" — click Sync now
  to verify the new creds work.

## Live verification (2026-09-15)

Agent VM `<VM_HOST_LAN>` (<OPERATOR_USER>), bundled derper systemd unit:

```bash
$ systemctl is-active derper
active
$ systemctl cat derper.service | grep ExecStart
ExecStart=/usr/local/bin/derper ... --a=:443 --http-port=80 --stun --verify-clients=false
$ ls -la /var/lib/derper/certs/
-rw-r--r-- derp.example.com.crt    (4853b, fullchain)
-rw------- derp.example.com.key    (306b,  ECDSA P-256)
$ openssl x509 -in /var/lib/derper/certs/derp.example.com.crt -noout -subject -dates
subject=CN = derp.example.com
notBefore=Sep 15 11:23:35 2026 GMT
notAfter =Nov 27 11:23:34 2026 GMT    ← ~73 days left (LE cert from NPM)

$ curl -sk https://<VM_HOST_LAN>:443/derp -H 'Host: derp.example.com' -o /dev/null -w '%{http_code}\n'
426    ← "DERP requires connection upgrade" = derper answered

$ sudo -n tailscale debug derp 900
"Successfully established a DERP connection with node derp.example.com"
```

(The `tailscale netcheck` "mow:" empty-latency is a cosmetic quirk
of netcheck probing port 443 with a non-HTTP expected response. The
real DERP connection works — `tailscale debug derp 900` confirms.)

## B-check

`scripts/check_b251_derp_cert_sync.sh` runs 9 grep-contracts:

- A. migration source shape (15 required columns)
- B. migration idempotency (CREATE TABLE/INDEX IF NOT EXISTS)
- C. driver registration (v0.71 B252 label in pgMigrations)
- D. derp_cert_sync.go exports (StartCertSyncCron, SyncOne, etc.)
- E. main.go cron wiring (admin.StartCertSyncCron)
- F. POST route (PostAdminDerpCertSyncRun)
- G. /admin/derp template UI (Sync now form)
- H. i18n keys (RU + EN, 12 keys)
- I. AGENTS.md B252 entry

Run: `bash scripts/check_b251_derp_cert_sync.sh`. All 9 contracts
must pass before merging.