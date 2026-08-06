# Skygate

[![CI](https://github.com/BarsSky/skygate/actions/workflows/ci.yml/badge.svg)](https://github.com/BarsSky/skygate/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/BarsSky/skygate?label=Latest)](https://github.com/BarsSky/skygate/releases/latest)
![Headscale](https://img.shields.io/badge/headscale-0.29.x-green)
![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8)
![License](https://img.shields.io/badge/license-MIT-green)

Self-service web portal for [Tailscale](https://tailscale.com) and
[headscale](https://github.com/juanfont/headscale). Gives every user a
friendly UI to grab preauth keys, see their devices, manage per-device
exit-node rules with DNS auto-update, switch preferred exit-nodes per
device, and (optionally) interact with the whole thing from a Telegram
bot — without ever touching the headscale CLI.

> **Status (v0.33.1.17):** cross-check between `device_rules` and the
> device's preferred exit-node (catches the "rule saved but Tailscale
> ignores it" bug). All 27 packages green (`go test -count=1 -short
> ./...`), 66/66 verify-pre checks pass, in-process system_tests cover
> 22+ tests including the new `exit_rules.preferred_mismatch`.
> See the [latest release notes](https://github.com/BarsSky/skygate/releases/latest).

## What it does

For **users** (`/my/*`):

- Log in at `/login` (no separate Tailscale account — portal-managed)
- Grab a one-time preauth key at `/my/preauth` and run
  `tailscale up --authkey <key>` on a new device
- See your devices at `/my/devices` (with auto-detected OS + device
  type, per-device preferred exit-node, "set as preferred" button)
- Manage preauth keys at `/my/keys` (with revoke)
- Manage exit-rules at `/my/exit-rules` (add / multi-delete / filter
  / search / cascade / cleanup; DNS auto-update turns `domain` rules
  into the right `/32` or CDN CIDR set; cross-check warning banner
  when a rule's exit-node doesn't match your device's preferred
  exit-node)
- Browse available exit-nodes at `/my/exit-nodes` (Tailscale IP,
  country, online status)
- Create personal API tokens (Bearer auth) at `/my/tokens` for AI /
  scripting
- Change your own password at `/my/account`
- Switch UI language (EN / RU) from the sidebar

For **admins** (`/admin/*`):

- `users` — create / list / delete portal users (each is a headscale
  user too)
- `devices` — all nodes across the tailnet, with tag / un-tag
  buttons and a per-device "dead rules" count (v0.33.1.17+)
- `exit-rules` — cross-user hierarchical view with per-row
  "Preferred" indicator; cleanup duplicate `device_id`s
- `exit-rules/rollback` — restore a previous ACL snapshot
- `exit-rules/sync` — re-trigger advertised-routes sync
- `exit-nodes` — manage the per-exit-node Tailscale state (host, IP,
  AcceptRoutes, SSH target, preferred as admin override)
- `acls` — read-only view of the live headscale ACL
- `audit` — who-did-what log (filters: `?action=…`, `?user=…`,
  `?ip=…`)
- `derp` — DERP relay status (peers, conn summary)
- `backup` — backup / restore the headscale ACL policy
- `telegram` — bot config (token in `global_settings`, hot-swap,
  per-chat egress relay selector)
- `headscale` — headscale release monitor (latest tag from
  juanfont/headscale GitHub)
- `headscale/acl` — visual ACL editor for the headscale policy
- `system_tests` — in-process test suite (network / db / headscale /
  exit_rules / disk / replication / backup / integrations)
- `settings` — per-user rule limits, max total rules, DNS auto-update
- `update` — in-app self-update orchestrator with auto-rollback
- `telegram-bind`, `meshes`, `invites`, `integrations`, `derp`,
  `subnets` — additional operator surfaces

For **ops** (Telegram bot, optional but recommended):

- Read-only: `/status /help /nodes /rules /audit /exit_nodes /quota
  /ack /version /restart /help <command>`
- Real operations: `/add_device /add_rule /delrule /clearrules
  /myexitnodes` — issue preauth keys, add/delete exit-rules, manage
  your own devices, all from the chat
- Triggers: ACL applied, password reset, rule add/delete, ACL rollback,
  ACL apply fail — all with `[#<id>]` prefix so `/ack <id>` can dismiss
- See [docs/TELEGRAM.md](docs/TELEGRAM.md)

## Architecture

- **Backend:** Go 1.25+ (single binary, stdlib `net/http` router)
- **Storage:** SQLite by default; PostgreSQL 14+ optional via
  `-tags postgres` build flag (`SKYGATE_DB_DSN=postgres://…`). Same
  schema, same migrations, same `db.BackendOf` dispatch — no code
  changes needed to switch.
- **Templates:** `html/template`, `embed.FS` — no Node, no JS bundler.
  Per-feature templates under `internal/handlers/templates/`.
- **Auth:** bcrypt (cost 12) + JWT (HS256) cookie, HttpOnly +
  SameSite=Lax; personal API tokens (Bearer) for the public REST API
- **Headscale integration:** REST API with API key; CLI fallback via
  `docker exec headscale headscale …` for tag changes (admin API
  lacks the permission); SSH for exit-node advertised-routes sync
- **Headplane (optional sidecar):** visual ACL editor + admin
  cockpit. Version-pinned via `HEADPLANE_IMAGE` in `.env`,
  default `ghcr.io/tale/headplane:0.6.3`. See
  [docs/headplane.md](docs/headplane.md) for the integration
  contract. Set `HEADPLANE_ENABLED=false` to skip the sidecar.
- **i18n:** 1 000+ catalog keys EN + RU, per-request locale via
  `atomic.Value` + funcmap `Tr / Trf`. Per-feature catalog files
  (12 of them) under `internal/i18n/catalog_*.go`.
- **Rate limits:** in-memory token bucket (per-username / per-IP),
  429 + `Retry-After` on block
- **Deploy:** Docker (Linux/WSL2) or native Go binary (any OS with
  Go 1.25+)

See [docs/architecture.md](docs/architecture.md) for the full component
map, [docs/db-schema.md](docs/db-schema.md) for the data model,
[docs/api.md](docs/api.md) for the HTTP surface, and
[docs/deploy.md](docs/deploy.md) for the install/backup/restore flow.

## Feature highlights (v0.16 → v0.33)

- **Per-user subnets** — each user gets a logical `10.0.<uid>.0/24`
  ACL namespace; subnet router advertises the user's LAN (v0.16.6+)
- **Per-user preferred exit-node** — set at `/my/devices` per device
  or `/admin/users/{id}/subnet` per user (v0.28.1+ / v0.28.4+)
- **Exit-rule / preferred cross-check** — banner + button on
  `/my/exit-rules`, per-row "Preferred" column on `/admin/exit-rules`,
  per-device dead-rule badge on `/admin/devices`, and
  `exit_rules.preferred_mismatch` test in `/admin/system_tests`
  (v0.33.1.17)
- **Domain rules with DNS auto-update** — `target_type='domain'`
  resolves to `/32` every 5 min; for Cloudflare/Fastly/Google/Akamai
  domains the per-IP churn is replaced with the CDN's published CIDR
  ranges (v0.30+ `cdn.go`)
- **Mesh (N-way bridge)** — group users whose personal subnets are
  all mutually visible (v0.22+)
- **Per-user headscale control plane** — compliance tier for SOX /
  multi-tenant SaaS / geographic isolation (v0.23+, opt-in)
- **Self-update orchestrator** — `Apply update` button on
  `/admin/update` rebuilds + recreates with auto-rollback (v0.29+)
- **PostgreSQL backend** — opt-in via `go build -tags postgres`,
  identical schema and migrations, `db.BackendOf` dispatch
  (v0.31+ foundation, v0.32.x+ live cutover)
- **Tailscale in-image** — optional `tailscaled` inside the skygate
  container for tailnet-only deployments (off by default since
  v0.32.15)
- **Telegram egress relay selector** — `/admin/telegram` lets the
  admin pick which enabled exit-node runs the canonical Telegram
  CIDR list (v0.33.1.8)
- **In-process system tests** — `/admin/system_tests` runs 22+ tests
  covering network, db, headscale, exit_rules, disk, replication,
  backup, integrations; results stored in `system_tests_runs`
- **Headplane integration** — optional visual ACL editor, version
  pinned, opt-in
- **Health + readyz probes** — `/healthz` and `/readyz` for
  monitoring (R1 / R2 in the verify-post catalog)

## Quick start (Linux + same-host headscale)

This is the fastest path: headscale and Skygate in the same docker
compose project (or two containers on the same `headscale_default`
network).

```bash
# 1. Get a headscale API key (run on the headscale host)
docker exec headscale headscale apikeys create --expiration 365d
# or: headscale apikeys create --expiration 365d

# 2. Generate a JWT secret
openssl rand -hex 32

# 3. Clone & configure
git clone https://github.com/BarsSky/skygate
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

Default admin: `admin` (rename on first login recommended) + the
password you set in `SKYGATE_ADMIN_PASS`.

For the full cross-platform install (Windows, restore from backup,
DERP relay, headplane sidecar, PostgreSQL backend) see
[docs/deploy.md](docs/deploy.md).

## Tailscale: OFF by default (v0.32.15+)

The skygate container can optionally run `tailscaled` and join the
tailnet (lets you reach `https://skygate.example.com` from a Tailscale
client without opening the VM's port 443 to the internet). This was
the default in v0.29.x but is now **disabled by default** because
of two incidents in v0.32.8 / v0.32.11:

- `secrets/ts_authkey` was a 0-byte file (Tailscale preauth never
  provisioned). `tailscale up --authkey=` waits for stdin forever —
  entrypoint hangs, container never reports healthy.
- The `TS_AUTHKEY_FILE` env var in `docker-compose.yml` was a
  literal string that `.env` overrides did NOT replace. The
  v0.33.1.16 fix removed the hardcoded value from `environment:`
  and added a "Restart skygate" button on `/admin/tailscale` that
  writes the new effective value to `.env` atomically.

If your VM is already behind Nginx Proxy Manager (NPM) and you
have a public DNS record (e.g. `skygate.example.com`), **you don't
need the in-container Tailscale at all** — the recommended setup
is `NPM → 127.0.0.1:8080`, no Tailscale in the skygate container.
Tailscale is only useful for tailnet-only deployments where you
want zero public attack surface.

**Re-enable Tailscale (3 manual steps)**:

1. Provision a real preauth key on the headscale host:
   ```bash
   docker exec headscale headscale preauthkeys create \
     --user admin --reusable --expiration 24h
   ```
2. Write it to `secrets/ts_authkey` on the Skygate host
   (the file is bind-mounted into the container at
   `/run/secrets/ts_authkey`).
3. In `docker-compose.yml`, un-gate the `secrets:` block and
   set `SKYGATE_TS_AUTHKEY_FILE=/run/secrets/ts_authkey` in the
   skygate `environment:`. Then
   `docker compose up -d --force-recreate skygate`.

## Using a remote / alternative headscale server

Skygate talks to headscale over HTTP. Point `HEADSCALE_URL` at **any**
reachable headscale instance — same LAN, Tailscale-only, behind a
reverse proxy, etc. The default (`http://headscale:50444`) only works
when both containers are on the same docker network.

```bash
# Same host, Skygate runs natively (not in docker):
HEADSCALE_URL=http://localhost:50444

# Another host on the LAN (RFC 5737 example IP):
HEADSCALE_URL=http://192.0.2.1:50444

# Headscale reachable only via Tailscale (no public IP):
HEADSCALE_URL=http://100.64.0.1:50444

# Headscale behind an HTTPS reverse proxy:
HEADSCALE_URL=https://headscale.example.com
```

**Important:** the host:port must be reachable from wherever Skygate
itself runs. If Skygate is in a Docker container on host A and
headscale is on host B, use host B's LAN IP or Tailscale IP —
`localhost` will not work.

The API key (`HEADSCALE_API_KEY`) is global to that headscale
instance and grants full admin access. Create it on the headscale
host, paste into Skygate's `.env`, never share it.

## Reverse proxy + HTTPS

Skygate is HTTP only. Always put it behind a TLS terminator.

- **Nginx Proxy Manager** (easiest): add proxy host
  `skygate.example.com` → `http://<skygate-host>:8080`, request a
  Let's Encrypt cert, force SSL.
- **Caddy** (one-liner):
  ```
  skygate.example.com {
      reverse_proxy <skygate-host>:8080
  }
  ```
- **nginx** (manual): see <https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/>

Cookies are HttpOnly + SameSite=Lax — works behind any standard
reverse proxy. Make sure the proxy does NOT strip the `Set-Cookie`
header. See [docs/https-setup.md](docs/https-setup.md) for a Caddy
+ Let's Encrypt walkthrough.

## Security

**Where secrets live**

| Secret | File | Permissions |
|---|---|---|
| `HEADSCALE_API_KEY` | `.env` on the Skygate host | `chmod 600` (root or admin) |
| `SKYGATE_JWT_SECRET` | `.env` on the Skygate host | `chmod 600` |
| `SKYGATE_ADMIN_PASS` | `.env` on the Skygate host | `chmod 600`; used only on first start |
| `skygate.db` / PG (bcrypt hashes + audit log) | volume or DB | `chmod 700` / DB-level access |

`.env` is in `.gitignore` — never committed.

**Rotation**

- `HEADSCALE_API_KEY`:
  ```bash
  # on the headscale host
  docker exec headscale headscale apikeys create --expiration 365d
  # paste the new token into Skygate's .env, restart the container
  docker compose restart skygate
  # delete the old key when ready
  docker exec headscale headscale apikeys expire <old-key-id>
  ```
- `SKYGATE_JWT_SECRET`: regenerate, paste into `.env`, restart.
  **Warning:** this logs out every user and revokes all personal
  API tokens.
- `SKYGATE_ADMIN_PASS`: drop the user from the DB, set a new
  `SKYGATE_ADMIN_PASS`, restart.

**What is NOT exposed in the UI**

The `HEADSCALE_API_KEY` is **never rendered in HTML**. To use the
key for Headplane, copy it manually from the Skygate host's `.env`.
This is intentional: any rendered secret can leak via screenshots,
browser extensions, or XSS.

**Other hardening**

- Admin password: bcrypt cost 12 (slow on purpose)
- Sessions: JWT HS256, TTL 24h, HttpOnly + SameSite=Lax
- Cookies behind HTTPS: the reverse proxy must not strip `Secure`
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
make smoke              # HTTP smoke (118+118 = 236 assertions, bilingual)
make check-nodes        # verifies exit-nodes advertise 0.0.0.0/0 + ::/0
make audit-routes       # static main.go vs handlers route-vs-handler audit
make test               # go-test + audit-routes + smoke + check-nodes (the whole thing)

# PostgreSQL backend (opt-in)
go build -tags postgres ./cmd/skygate
# Run the 4 PG-specific verification tests:
docker run -d --name skygate-pgtest -e POSTGRES_USER=skygate \
  -e POSTGRES_PASSWORD=skygate_dev -e POSTGRES_DB=skygate \
  -p 5432:5432 postgres:16
export SKYGATE_TEST_PG_DSN='postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable'
go test -tags postgres -count=1 -v -run "TestPG" ./internal/db/
```

Templates live in `internal/handlers/templates/` and are embedded
into the binary at build time via `//go:embed`. Edit them, rebuild,
restart.

For AI assistants: read [AGENTS.md](AGENTS.md) first — it has the
full file map, schema gotchas, the guarantee catalog (B1–B66 build,
R1–R27 runtime), and the VM-vs-Windows working rules.

## Where to look

| You want… | Go to |
|---|---|
| Component map, data flow | [docs/architecture.md](docs/architecture.md) |
| All DB tables + columns | [docs/db-schema.md](docs/db-schema.md) |
| Every HTTP endpoint + curl | [docs/api.md](docs/api.md) |
| Deploy / backup / restore / DERP / HTTPS | [docs/deploy.md](docs/deploy.md), [docs/disaster-recovery.md](docs/disaster-recovery.md) |
| Telegram bot config + commands | [docs/TELEGRAM.md](docs/TELEGRAM.md) |
| Per-version change history | [CHANGELOG.md](CHANGELOG.md), [RELEASE-NOTES.md](RELEASE-NOTES.md) |
| File map, gotchas, AI hints, guarantee catalog | [AGENTS.md](AGENTS.md) |
| First-time client setup scripts | [docs/scripts/skygate_exit_node_setup.sh](docs/scripts/skygate_exit_node_setup.sh) |
| Russian-language version | [README.ru.md](README.ru.md) |
| Known back-burner items | [docs/BACKLOG.md](docs/BACKLOG.md) |

## Status (live)

- **CI:** green on every push to `main` and every PR (see badge —
  `go vet + go test -race + go build + audit_routes.py` on
  `ubuntu-24.04`)
- **Verify-pre:** 66/66 PASS (`bash scripts/verify_pre_deploy.sh`)
- **Latest release:** see [Releases](https://github.com/BarsSky/skygate/releases)
- **Source code map:** see [AGENTS.md](AGENTS.md) — kept up to date
  with the latest `internal/feature/*` decomposition
- **In-process tests:** `/admin/system_tests` runs 22+ tests; the
  `exit_rules.preferred_mismatch` test added in v0.33.1.17 is the
  canonical "did the operator misconfigure anything?" check

## Roadmap

### Done (highlights since v0.6.0)

- ✅ Per-user subnets (`10.0.<uid>.0/24` per user as logical ACL
  namespace; subnet router advertises the user's LAN)
- ✅ Per-user preferred exit-node (per-device + per-user pref, with
  strict pinning via the `via` flag)
- ✅ Domain rules with DNS auto-update; CDN-range substitution for
  Cloudflare / Fastly / Google / Akamai (no more anycast-churn)
- ✅ Exit-rule / preferred exit-node cross-check on three pages
  plus the in-process `exit_rules.preferred_mismatch` system test
- ✅ Mesh (N-way bridge) between users' personal subnets
- ✅ Per-user headscale control plane (compliance tier — SOX, multi-
  tenant SaaS, geographic isolation)
- ✅ Self-update orchestrator with auto-rollback (`/admin/update`)
- ✅ PostgreSQL backend (opt-in via `-tags postgres`); 27 PG
  migrations + 4 verification tests
- ✅ Tailscale in-image integration (opt-in, off by default since
  v0.32.15)
- ✅ In-process system tests (`/admin/system_tests` — 22+ tests)
- ✅ /healthz + /readyz probes for monitoring
- ✅ Device auto-detect (OS + device_type on every `/my/devices` load)
- ✅ Telegram egress relay selector (per-chat pick of which
  enabled exit-node runs the Telegram CIDR list)
- ✅ Headplane integration (optional sidecar, version-pinned)
- ✅ Per-feature refactor (handlers split into `internal/feature/*`
  per the refactor-v0.30 plan; AGENTS.md tracks the decomposition)
- ✅ Bilingual EN/RU web UI (1 000+ catalog keys)
- ✅ Personal API tokens (Bearer auth) with TTL + auto-rotate
- ✅ Self-service password change
- ✅ Rate limits (login + api)
- ✅ Per-exit-node `AcceptRoutes` policy
- ✅ Static route audit (`scripts/audit_routes.py` in CI)
- ✅ Guarantee catalog: 66 build-time (B1–B66) + 27 runtime
  (R1–R27) checks pinned by `verify_pre_deploy.sh` /
  `verify_post_deploy.sh`

### Not done yet

- ⏳ Audit log filter by **date** (only `?action=` and `?user=` work
  today; `?ip=` was added recently)
- ⏳ Email notifications on user creation
- ⏳ QR code for mobile registration (alternative to
  `tailscale up --authkey …`)
- ⏳ Device rename through the web UI (currently headscale-side only)
- ⏳ Gitea integration (per-user API key provisioning)
- ⏳ UI form for one-click headscale API key rotation (the
  procedure is documented above but is not a one-click form yet)
- ⏳ `?device=NAME` query filter on `/admin/exit-rules` (the link
  from the per-device dead-rule badge points there but the handler
  doesn't filter yet — 10-line follow-up)
- ⏳ Standalone visual ACL editor (Headplane remains the
  recommended tool; `GenerateACL()` is still hand-written)

---

## License

[MIT](LICENSE) — Copyright (c) 2026. Use, modify, redistribute under
the terms of the MIT License. See [LICENSE](LICENSE) for the full
text.

---

## Trademarks

*Tailscale* is a trademark of Tailscale Inc. *headscale* is an
open-source project by Juan Font. Skygate is an independent
self-service portal and is not affiliated with or endorsed by
either project.
