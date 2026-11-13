# Certificates — obtaining, publishing and renewing

Skygate can do three things with a TLS certificate:

1. **Obtain** one from Let's Encrypt over **HTTP-01**, from the panel
   (`/admin/certificates` → «Получить сертификат у Let's Encrypt»).
2. **Publish** a pair — either the one it obtained or a PEM pair the operator pastes — to the
   files `certsync` reads, to S3 (when configured), and to the proxy (when skygate manages it).
3. **Report** exactly what happened: the file it wrote, the S3 version, whether the proxy was
   reloaded, and the reason for every step that was skipped.

> **Historical note (B467, v1.5.214).** Before this version the upload button validated the
> pair, published nothing, and said «Cert uploaded … the certsync scheduler will pull within
> 30s». The callback it called was never wired on any install. If you are on an older version:
> that button does not work, use the file paths below directly.

---

## 1. The prerequisite for HTTP-01, stated plainly

Let's Encrypt validates a domain by fetching

```
http://<domain>/.well-known/acme-challenge/<token>
```

and it will not follow redirects to another location it did not choose, nor use a non-standard
port. **That request must reach skygate itself** (`SKYGATE_PORT`, default 8080). Skygate serves
that path on its own mux precisely so the check can work, but routing the path to skygate is a
fact about *your* deployment:

| Your setup | What has to be true |
|---|---|
| Public IP on the skygate host, nothing else on :80 | forward 80 → skygate's port |
| A reverse proxy in front (nginx, Caddy, NPM…) | a location rule for `/.well-known/acme-challenge/` → `http://<skygate host>:8080`, and **no** redirect on that path |
| DERP/other service already holding :80 | the path rule above is the only option short of moving that service |

If the request reaches anything else, the attempt fails at the challenge step and the panel says
so by name (the domain, the path and the token). Nothing is published on a failed attempt, and the
existing certificate keeps serving.

### Wildcards

`*.example.com` needs **DNS-01** (a TXT record), which this installation cannot do yet:
`internal/dnsexternal` is an A-record placeholder without TXT support. The form therefore
refuses a wildcard and says why instead of letting the CA refuse it later. List the names
explicitly (`skygate.example.com, head.example.com`).

---

## 2. The procedure

1. **Drill first with staging.** Tick «Проверочный контур (staging)». The certificate is signed by
   an untrusted test CA — browsers will warn, which is the point: it proves the routing and the
   whole order flow without touching the production rate limits (5 duplicate certificates per
   week, 50 per registered domain).
2. Enter the names, press «Выпустить сертификат». One attempt, one order.
3. Read the flash. It names the CA contour, the file (`cert.pem`), the fingerprint, the S3
   outcome and the reload outcome. Every «no» carries its reason.
4. When the staging drill passes, repeat **without** the staging tick. The production certificate
   replaces the staging one; the previous pair is kept as `cert.pem.prev` / `key.pem.prev`.
5. Make sure your proxy actually serves the new files (a reload is attempted when `caddy` is on
   `PATH` or a `caddy` systemd unit exists; otherwise reload it yourself).

### Where the files go

| Path | Meaning |
|---|---|
| `/var/lib/skygate/certs/cert.pem` | the live certificate (leaf + chain) — `certsync` reads this |
| `/var/lib/skygate/certs/key.pem` | the matching private key, `0600` |
| `/var/lib/skygate/certs/cert.pem.prev`, `key.pem.prev` | the pair that was replaced (one step back) |
| `/var/lib/skygate/certs/.certsync-version` | the version/SHA cache that stops `certsync` from re-pulling over your publish |
| `s3://<CertSyncBucket>/certs/{cert.pem,key.pem,.version}` | the shared copy, only when a bucket is configured |

`SKYGATE_CERTSYNC_LOCAL_DIR` overrides the directory.

### Rollback

```bash
cd /var/lib/skygate/certs
cp cert.pem.prev cert.pem && cp key.pem.prev key.pem   # one step back
caddy reload --config /etc/caddy/Caddyfile 2>/dev/null || systemctl reload caddy
```

If S3 is configured and you had published, note that `certsync` compares `.version` on every
tick: a rollback done only on disk will be re-overwritten by the newer S3 copy. Roll back with
the panel's upload form (it publishes a new version) or disable `certsync` for the rollback.

---

## 3. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `SKYGATE_ACME_EMAIL` | *(empty)* | account contact; the CA uses it to warn about an expiring certificate |
| `SKYGATE_ACME_DIRECTORY` | Let's Encrypt production | explicit CA directory URL; overrides the staging checkbox |
| `SKYGATE_ACME_ACCOUNT_KEY` | `/var/lib/skygate/acme-account.key` | the account key (created on first use, `0600`) — **back it up**: losing it means a new CA account and its rate-limit history |
| `SKYGATE_CERTSYNC_LOCAL_DIR` | `/var/lib/skygate/certs` | where the live pair lives |
| `SKYGATE_CERTSYNC_S3_BUCKET` | *(from `/admin/backup`)* | the shared bucket, when you have more than one host |
| `SKYGATE_READONLY_COPY` | *(unset)* | set on a restored copy: it must not obtain or publish (see `docs/deploy.md`) |

`SKYGATE_ACME_ACCOUNT_KEY` lives in the data directory on purpose: `/tmp` is not a place for the
identity of an installation.

---

## 4. What is NOT automated yet

**Renewal is manual.** Let's Encrypt certificates live 90 days and the panel shows the days left,
but nothing re-runs the order on a schedule. The renewal loop is the next block: it will reuse
this exact flow (the same `Issue`, the same `Publish`, the same precondition), so the manual
button and the future scheduler cannot drift apart. Until then: put a calendar reminder on day 60,
or drive `POST /admin/certificates/issue` from your own cron with an admin session.

**DNS-01 (wildcards, no reachable port 80)** needs TXT support in `internal/dnsexternal`, which
does not exist yet.

---

## 5. Audit and diagnosis

Every attempt writes one `audit_log` row, success or failure:

| Action | Meaning |
|---|---|
| `certsync.upload` | an operator-pasted pair was published (the row names path, S3, version, reload) |
| `certs.issue` | an ACME order succeeded and was published (domains, staging flag, account URL, elapsed) |
| `certs.issue_failed` | the order failed at a named step (the error carries the step and the token path) |
| `certs.issue_unpublished` / `certs.issue_publish_failed` | the CA returned a certificate that could not be written — the defect, not the order |

Journal lines to look for:

```
acme: account key …            (boot: which account file, which CA directory, which contact)
acme: order … created for …    (one order per attempt)
acme: <domain> validated …     (the CA fetched the challenge)
acme: certificate issued …     (the chain the CA returned)
certsync: pair published to …  (the local write)
certsync: publish ok — …       (the summary: sha, size, s3, reload)
```
