# v0.24.2 — Download bundle for per-user subnet-router (2026-07-21)

The "user-friendly delivery" release. The previous release
(v0.24.0) shipped `deploy/subnet-router/setup.sh` so users
*could* set up their router — but the operator had to
manually copy the script + the rendered `tailscale up`
command into a chat / email / wiki page. v0.24.2 ships a
**one-click download** flow: the admin clicks "Download
bundle" on `/admin/users/{id}/subnet`, the server issues a
fresh preauth key, embeds it in a self-contained tar.gz,
and the user's browser saves
`skygate-subnet-router-bundle-<username>-<ts>.tar.gz`. The
user scps the bundle to their router host, untars, runs
`sudo bash commands.txt` (or `setup.sh`), and the rest is
the same 5-step end-to-end flow from v0.24.0.

## What did NOT change

- No Go code touched outside `admin_user_subnet_download.go`
  (a new file) and the templates / catalog / main.go
  wire-up. No new env vars, no schema migration.
- Same `tag:subnet-router` flow, same 1h TTL single-use
  preauth semantics, same auto-approval via
  `sidecar.SyncOnce` (30s tick).
- The 4 prod users are unchanged. Skyadmin's subnet
  (`10.0.1.0/24 active`) is still in the same state.

## What's in the bundle

```
setup.sh       (chmod +x) — the one-shot script from
                  deploy/subnet-router/setup.sh. The
                  same file the operator would have
                  pasted into chat; now bundled.
README.md      (chmod 644) — the bundle-local quick
                  start (5 commands: scp, untar, read,
                  run, ping). 100 lines.
commands.txt   (chmod +x) — the rendered `tailscale up`
                  command with the preauth key and
                  CIDR filled in. Self-documenting
                  (banner comments). User can run
                  `sudo bash commands.txt` directly
                  without retyping.
CIDR.txt       (chmod 644) — just the per-user CIDR,
                  in case the user wants to script
                  around it.
```

Total: ~12 KB compressed. The same content as the
admin's `/admin/users/{id}/subnet → Issue preauth key`
page, just bundled for `scp`.

## What changed in docs

`docs/subnet-router.md` got three new sections at the top:

1. **TL;DR — what does this actually do for me?** —
   concrete examples (`ping skygate-subnet-<user>`,
   `ping 10.0.<uid>.1`, `ping <nas-ip>`, `http://<nas>:5000`
   from a phone on 4G). Plus the explicit "without a
   subnet-router, none of this works" disclaimer.

2. **Quick start (5 minutes if you already have
   tailscaled)** — the 3-command setup for users who
   already have tailscaled installed: `curl setup.sh`,
   `sudo PREAUTH_KEY=... setup.sh`, `ping`. The rest of
   the document is the long-form.

3. **What to download** — the two files from this repo
   with their GitHub raw URLs, plus a security note
   ("always read the script before piping to bash").

## What changed in code

1. **`internal/handlers/admin_user_subnet_download.go`**
   (new file, 190 lines) — `GetAdminUserSubnetDownload`
   handler. Issues a fresh preauth via
   `Sidecar.GeneratePreauth`, builds the tar.gz in
   memory (stdlib `archive/tar` + `compress/gzip`),
   writes the audit_log row, returns the bundle as
   `application/gzip` with `Content-Disposition:
   attachment; filename="skygate-subnet-router-bundle-
   <user>-<ts>.tar.gz"`.

2. **`internal/handlers/bundles/`** (new package) —
   embed copies of `setup.sh` and `README.md`. The
   handler reads them via `//go:embed`. The copies are
   kept in sync via the new `make sync-bundles` /
   `make check-bundles` Makefile targets; CI runs
   `check-bundles` as part of `make test` to catch
   drift.

3. **`cmd/skygate/main.go`** — registers the new route
   `GET /admin/users/{id}/subnet/download` (admin-only,
   like the rest of the subnet endpoints).

4. **`internal/handlers/templates/admin/user_subnet.html`**
   — adds a "Download bundle" button next to the existing
   "Issue preauth key" button on
   `/admin/users/{id}/subnet`. Same handler, same auth
   check, just a `<a href>` instead of a `<form>`.

5. **`internal/i18n/catalog.go`** — 2 new keys × 2 langs
   (4 entries): `user_subnet.download_button` /
   `download_button_help` in both RU and EN.

6. **`Makefile`** — `sync-bundles` (refreshes the embed
   copies) + `check-bundles` (fails `make test` if the
   copies drift). Uses `git diff --no-index` for
   portability (Windows + Linux).

## Verification (live, on the operator's VM)

```
$ curl -fsS -b cookie \
  -D headers.txt -o bundle.tar.gz \
  http://localhost:8080/admin/users/1/subnet/download
HTTP 200, size: 4551 bytes

$ grep -i content- headers.txt
Content-Disposition: attachment; filename="skygate-subnet-router-bundle-skyadmin-20260721-162823.tar.gz"
Content-Type: application/gzip

$ tar tzf bundle.tar.gz
setup.sh
README.md
commands.txt
CIDR.txt

$ cat CIDR.txt
10.0.1.0/24

$ head -3 commands.txt
#!/bin/bash
# Skygate subnet-router setup for skyadmin
# Generated: 2026-07-21T16:28:23Z

$ tail -1 commands.txt
  --authkey=hskey-auth-ShebF0D7Zglv-...

$ sqlite3 skygate.db \
  "SELECT action, detail FROM audit_log ORDER BY id DESC LIMIT 1"
subnet_download|user_id=1 expires=2026-07-21T17:28:23Z bundle=tar.gz
```

The bundle's preauth key is real (issued by the
`Sidecar.GeneratePreauth` codepath, same as the existing
"Issue preauth key" button) and audit-tracked. Running
`sudo bash commands.txt` on a router host that has
`tailscaled` running will register the node tagged
`tag:subnet-router` with the matching hostname, the
sidecar's 30s tick will auto-approve the route, and the
status pill will flip from `pending` to `router_active`.

## Files

- `internal/handlers/admin_user_subnet_download.go`
  (190 lines, new)
- `internal/handlers/bundles/bundles.go` (15 lines, new
  — `//go:embed` declarations only)
- `internal/handlers/bundles/setup.sh` (copy of
  `deploy/subnet-router/setup.sh`)
- `internal/handlers/bundles/README.md` (copy of
  `deploy/subnet-router/README.md`)
- `deploy/subnet-router/README.md` (100 lines, new)
- `cmd/skygate/main.go` (+5 lines — route registration)
- `internal/handlers/templates/admin/user_subnet.html`
  (+3 lines — the new `<a href>` button)
- `internal/i18n/catalog.go` (+4 entries — 2 keys × 2
  langs)
- `Makefile` (+15 lines — `sync-bundles` + `check-bundles`)
- `docs/subnet-router.md` (+60 lines — TL;DR + Quick
  start + What to download)

17/17 packages green. `make test` includes
`check-bundles` and passes.

## What comes next

- **Per-user default exit-node** (v0.19.1) — still
  blocked on headscale 0.30+ for `dns.extra_records`.
  mavis cron `headscale-milestone-16-check` polls
  weekly.
- **Telegram bot `/mysubnet provision`** — the bot
  path for issuing the preauth is already wired up in
  v0.16.7; v0.25.0 could ship "send the bundle to the
  user via Telegram" so the admin doesn't have to
  email a tar.gz.
- **Per-user bot routing (v0.12.1 follow-up)** —
  small backfill, ~30 lines in
  `internal/telegram/notify.go`. Carried over from the
  v0.12 backlog.
