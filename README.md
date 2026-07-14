# Skygate

[![CI](https://github.com/BarsSky/skygate/actions/workflows/ci.yml/badge.svg)](https://github.com/BarsSky/skygate/actions/workflows/ci.yml)
![Version](https://img.shields.io/badge/version-v0.10.7-blue)
![Headscale](https://img.shields.io/badge/headscale-0.29-green)
![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8)
![License](https://img.shields.io/badge/license-proprietary-lightgrey)

Self-service web portal for [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale) — gives users a friendly
UI to grab preauth keys, see their devices, manage per-device exit-node
rules, and (optionally) interact with the whole thing from a Telegram bot
without ever touching the headscale CLI.

> **Status:** at tag `v0.10.7`. Work in progress on `main` is `v0.10.8-dev`.
> **Last `make test` on VM:** green — `59+59` smoke assertions, 3/3 exit-nodes
> advertise `0.0.0.0/0` + `::/0` (emilia, sharlotta, karolina), `go test ./...`
> race-free.

## What it does

For **users**:

- Log in at `/login` (no Tailscale account needed — portal-managed)
- Grab a one-time preauth key at `/my/preauth` and run
  `tailscale up --authkey <key>` on a new device
- See your devices at `/my/devices`, your preauth keys at `/my/keys`
  (with revoke), your exit-rules at `/my/exit-rules` (add / multi-delete /
  filter / search / cascade / cleanup)
- Browse available exit-nodes at `/my/exit-nodes` (Tailscale IP + country)
- Create personal API tokens (Bearer auth) at `/my/tokens` for AI / scripting
- Change your own password at `/my/account`
- Switch UI language (EN / RU) from the sidebar

For **admins** (`/admin/*`):

- `users` — create / list / delete portal users; each is a headscale user too
- `devices` — all nodes across all namespaces, with tag / un-tag buttons
- `exit-rules` — cross-user hierarchical view; cleanup duplicate `device_id`s
- `exit-rules/rollback` — restore a previous ACL snapshot
- `exit-rules/sync` — re-trigger advertised-routes sync
- `exit-nodes` — manage the per-exit-node Tailscale state (host, IP,
  AcceptRoutes, SSH target)
- `acls` — read-only view of the live headscale ACL
- `audit` — who-did-what log (filters: `?action=…`, `?user=…`)
- `derp` — DERP relay status (peers, conn summary)
- `backup` — backup / restore the headscale ACL policy
- `telegram` — bot config (token in `global_settings`, hot-swap)
- `settings` — per-user rule limits, max total rules, DNS auto-update

For **ops** (Telegram bot, optional but recommended):

- Phase 1–4 read-only: `/status /help /nodes /rules /audit /exit_nodes /quota
  /ack /version /restart /help <command>`
- Phase 11–14 real ops: `/add_device /add_rule /delrule /clearrules
  /myexitnodes` — issue preauth keys, add/delete exit-rules, manage your
  own devices, all from the chat
- Triggers: ACL applied, password reset, rule add/delete, ACL rollback,
  ACL apply fail — all with `[#<id>]` prefix so `/ack <id>` can dismiss
- See [docs/TELEGRAM.md](docs/TELEGRAM.md)

## Architecture

- **Backend:** Go 1.23, single binary, stdlib `net/http` router
- **Storage:** SQLite (one file, embedded in container volume, WAL mode)
- **Templates:** `html/template`, `embed.FS` — no Node, no JS bundler
- **Auth:** bcrypt (cost 12) + JWT (HS256) cookie, HttpOnly + SameSite=Lax;
  personal API tokens (Bearer) for the public REST API
- **Headscale integration:** REST API with API key; CLI fallback via
  `docker exec headscale headscale …` for tag changes (admin API lacks
  the permission); SSH for exit-node advertised-routes sync
- **i18n:** 270+ catalog keys EN+RU, per-request locale via `atomic.Value`
  + funcmap `Tr/Trf`
- **Rate limits:** in-memory token bucket (per-username / per-IP),
  429 + `Retry-After` on block
- **Deploy:** Docker (Linux/WSL2) or native Go binary (any OS with Go 1.23+)

See [docs/architecture.md](docs/architecture.md) for the full component
map, [docs/db-schema.md](docs/db-schema.md) for the data model,
[docs/api.md](docs/api.md) for the HTTP surface, and
[docs/deploy.md](docs/deploy.md) for the install/backup/restore flow.

## Quick start (Linux + same-host headscale)

This is the fastest path: headscale and Skygate in the same docker compose
project (or two containers on the same `headscale_default` network).

```bash
# 1. Get a headscale API key (run on headscale host)
docker exec headscale headscale apikeys create --expiration 365d
# or: headscale apikeys create --expiration 365d

# 2. Generate a JWT secret
openssl rand -hex 32

# 3. Clone & configure
git clone <repo> skygate
cd skygate
cp .env.example .env
nano .env          # fill HEADSCALE_API_KEY, SKYGATE_JWT_SECRET, SKYGATE_ADMIN_PASS
# Leave HEADSCALE_URL=http://headscale:50444 for same-network setup.

# 4. Build & run
docker compose up -d --build
docker compose logs -f skygate

# 5. Open in browser
curl -I http://localhost:8080/login         # should return 200
# then visit http://localhost:8080/login
```

Default admin: `skyadmin` + the password you set in `SKYGATE_ADMIN_PASS`.

For the full cross-platform install (Windows, restore from backup, DERP
relay, headplane sidecar) see [docs/deploy.md](docs/deploy.md).

## Using a remote / alternative headscale server

Skygate talks to headscale over HTTP. Point `HEADSCALE_URL` at **any**
reachable headscale instance — same LAN, Tailscale-only, behind a
reverse proxy, etc. The default (`http://headscale:50444`) only works
when both containers are on the same docker network.

```bash
# Same host, Skygate runs natively (not in docker):
HEADSCALE_URL=http://localhost:50444

# Another host on the LAN (e.g. 192.168.13.69):
HEADSCALE_URL=http://192.168.13.69:50444

# Headscale reachable only via Tailscale (no public IP):
HEADSCALE_URL=http://100.64.0.1:50444

# Headscale behind an HTTPS reverse proxy:
HEADSCALE_URL=https://headscale.example.com
```

**Important:** the host:port must be reachable from wherever Skygate
itself runs. If Skygate is in a Docker container on host A and headscale
is on host B, use host B's LAN IP or Tailscale IP — `localhost` will
not work.

The API key (`HEADSCALE_API_KEY`) is global to that headscale instance
and grants full admin access. Create it on the headscale host, paste
into Skygate's `.env`, never share it.

## Reverse proxy + HTTPS

Skygate is HTTP only. Always put it behind a TLS terminator.

- **Nginx Proxy Manager** (easiest): add proxy host `skygate.example.com`
  → `http://192.168.13.69:8080`, request LE cert, force SSL.
- **Caddy** (one-liner):
  ```
  skygate.example.com {
      reverse_proxy 192.168.13.69:8080
  }
  ```
- **nginx** (manual): see https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/

Cookies are HttpOnly + SameSite=Lax — works behind any standard reverse
proxy. Make sure the proxy does NOT strip the `Set-Cookie` header.

## Security

**Where secrets live**

| Secret | File | Permissions |
|---|---|---|
| `HEADSCALE_API_KEY` | `.env` on the Skygate host | `chmod 600` (root or skyadmin) |
| `SKYGATE_JWT_SECRET` | `.env` on the Skygate host | `chmod 600` |
| `SKYGATE_ADMIN_PASS` | `.env` on the Skygate host | `chmod 600`; used only on first start |
| `skygate.db` (contains bcrypt hashes + audit log) | volume `/var/lib/skygate` | `chmod 700` |

`.env` is in `.gitignore` — never committed.

**Rotation**

- `HEADSCALE_API_KEY`:
  ```bash
  # on headscale host
  docker exec headscale headscale apikeys create --expiration 365d
  # paste new token into Skygate's .env, restart container
  docker compose restart skygate
  # delete the old key when ready
  docker exec headscale headscale apikeys expire <old-key-id>
  ```
- `SKYGATE_JWT_SECRET`: regenerate, paste into `.env`, restart.
  **Warning:** this logs out every user and revokes all personal API tokens.
- `SKYGATE_ADMIN_PASS`: drop the user from SQLite, set a new
  `SKYGATE_ADMIN_PASS`, restart.

**What is NOT exposed in the UI**

The `HEADSCALE_API_KEY` is **never rendered in HTML**. To use the key
for Headplane, copy it manually from the Skygate host's `.env`. This is
intentional: any rendered secret can leak via screenshots, browser
extensions, or XSS.

**Other hardening**

- Admin password: bcrypt cost 12 (slow on purpose)
- Sessions: JWT HS256, TTL 24h, HttpOnly + SameSite=Lax
- Cookies: behind HTTPS, the reverse proxy must not strip `Secure`
  (set `proxy_cookie_flags Secure httponly` in nginx)
- Bind Skygate to `127.0.0.1` and only expose via reverse proxy:
  add `ports: ["127.0.0.1:8080:8080"]` in `docker-compose.yml`
- Per-IP and per-username rate limits on `/login` and `/api`

## Development

```bash
# Quick iteration
make build              # GOTOOLCHAIN=local go build -o ./skygate ./cmd/skygate
make run                # build + ./skygate
make go-test            # go test ./...
make smoke              # HTTP smoke (59+59 = 118 assertions, bilingual)
make check-nodes        # verifies exit-nodes advertise 0.0.0.0/0 + ::/0
make audit-routes       # static main.go vs handlers route-vs-handler audit
make test               # go-test + audit-routes + smoke + check-nodes (the whole thing)
```

Templates live in `internal/handlers/templates/` and are embedded into
the binary at build time via `//go:embed`. Edit them, rebuild, restart.

For AI assistants: read [AGENTS.md](AGENTS.md) first — it has the full
file map, schema gotchas, and the VM-vs-Windows working rules.

## Where to look

| You want… | Go to |
|---|---|
| Component map, data flow | [docs/architecture.md](docs/architecture.md) |
| All DB tables + columns | [docs/db-schema.md](docs/db-schema.md) |
| Every HTTP endpoint + curl | [docs/api.md](docs/api.md) |
| Deploy / backup / restore / DERP | [docs/deploy.md](docs/deploy.md) |
| Telegram bot config + commands | [docs/TELEGRAM.md](docs/TELEGRAM.md) |
| Per-version change history | [CHANGELOG.md](CHANGELOG.md) |
| File map, gotchas, AI hints | [AGENTS.md](AGENTS.md) |
| First-time client setup scripts | [docs/scripts/skygate_exit_node_setup.sh](docs/scripts/skygate_exit_node_setup.sh) |
| AI agent sync workflow | [docs/SYNC.md](docs/SYNC.md) |
| Russian-language version | [README.ru.md](README.ru.md) |

## Status (live)

- **CI:** green (see badge above — `go vet + go test -race + go build +
  audit_routes.py` on `ubuntu-24.04`)
- **VM `make test`:** green as of last push — see commit footer for the
  exact `git describe --tags --always` label
- **Source code map:** see [AGENTS.md](AGENTS.md) — kept up-to-date with
  the latest `handlers*.go` / `exit_rules*.go` / `internal/headscale/*.go`
  decomposition status

## Roadmap

### Done (v0.6.0+)

- ✅ Per-user headscale ACL with granular visibility (Android bug fix)
- ✅ Audit log filters by user and action (`/admin/audit?action=…&user=…`)
- ✅ Per-user rule limits (`SKYGATE_USER_MAX_RULES=skyadmin:2000,alice:500`)
- ✅ Cleanup of orphaned /32 rules (`/admin/exit-rules/cleanup`)
- ✅ Per-exit-node Tailscale `AcceptRoutes` policy
- ✅ Bilingual EN/RU web UI (270+ keys)
- ✅ Telegram bot (real ops: preauth, rules, devices, restart, version)
- ✅ Personal API tokens (Bearer auth)
- ✅ Self-service password change
- ✅ Rate limits (login + api)
- ✅ Static route audit (`scripts/audit_routes.py` in CI)
- ✅ Unit tests for `acl`, `headscale`, `telegram`, `i18n`, `db`

### Not done yet

- ⏳ Audit log filter by **date** (only `?action=` and `?user=` work today)
- ⏳ Email notifications on user creation
- ⏳ QR code for mobile registration (alternative to
  `tailscale up --authkey …`)
- ⏳ Device rename through UI (currently headscale-side only)
- ⏳ Gitea integration (per-user API key provisioning)
- ⏳ Admin API key rotation UI (the headscale API key rotation is
  documented in [Security → Rotation](#security) but isn't a one-click
  form yet)
- ⏳ Headplane replacement — `GenerateACL()` is still hand-written; the
  long-term plan is to keep it as a fallback and have Headplane own the
  policy editor for non-trivial configs

`ACL editor (currently Headplane-only)` was on the v0.6.0 roadmap and
remains so by design — we don't have a custom visual ACL editor, and
Headplane is the canonical tool. If you need a visual editor today,
point Headplane at the same `HEADSCALE_API_KEY` (see the
[Security](#security) section for the secret-handling caveat).

---

## License

Proprietary — see the upstream `LICENSE` if/when it lands. For now,
treat as "all rights reserved" and ask before redistributing.
