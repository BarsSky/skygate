# Skygate

Self-service web portal for Tailscale headscale — gives users a friendly
UI to grab preauth keys, see their devices, pick an exit node, without
touching the headscale CLI.

## What it does

- Admin (skyadmin) creates portal users in `/admin/users`
- Each user logs in to `/login` and gets a one-time preauth key at `/my/preauth`
- User runs `tailscale up --authkey <key>` on a new device — it joins the tailnet
- Admin sees all devices across all namespaces in `/admin/devices`
- Admin can tag/untag nodes, view ACL policy (read-only link to Headplane),
  audit log, DERP relay status, exit nodes

## Architecture

- **Backend:** Go 1.23, single binary, stdlib `net/http` router
- **Storage:** SQLite (one file, embedded in container volume)
- **Templates:** html/template, `embed.FS` — no Node, no JS bundler
- **Auth:** bcrypt (cost 12) + JWT (HS256) cookie, HttpOnly + SameSite=Lax
- **Headscale integration:** REST API with API key
- **Deploy:** Docker (Linux/WSL2) or native Go binary (any OS with Go 1.23+)

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

## Using a remote / alternative headscale server

Skygate talks to headscale over HTTP. Point `HEADSCALE_URL` at **any**
reachable headscale instance — same LAN, Tailscale-only, behind a
reverse proxy, etc. The default (`http://headscale:50444`) only works
when both containers are on the same docker network.

Edit `.env`:

```bash
# Same host, Skygate runs natively (not in docker):
HEADSCALE_URL=http://localhost:50444

# Another host on the LAN (e.g. 192.168.13.69):
HEADSCATE_URL=http://192.168.13.69:50444

# Headscale reachable only via Tailscale (no public IP):
HEADSCALE_URL=http://100.64.0.1:50444

# Headscale behind an HTTPS reverse proxy:
HEADSCALE_URL=https://headscale.example.com

# Headscale on a remote VPS in Frankfurt / Helsinki / etc:
HEADSCALE_URL=http://karolina.example.com:50444
```

**Important:** the host:port must be reachable from wherever Skygate
itself runs. If Skygate is in a Docker container on host A and headscale
is on host B, use host B's LAN IP or Tailscale IP — `localhost` will
not work.

The API key (`HEADSCALE_API_KEY`) is global to that headscale instance
and grants full admin access. Create it on the headscale host, paste
into Skygate's `.env`, never share it.

## Installing on Windows

Three paths, pick what matches your machine.

### Option 1 — WSL2 + Docker Desktop (recommended)

Most faithful to the Linux path, runs the same `docker compose` recipe.

1. Install **WSL2**: `wsl --install` (reboot if asked)
2. Install **Docker Desktop** with WSL2 backend enabled
   (https://www.docker.com/products/docker-desktop/)
3. In a WSL2 terminal:
   ```powershell
   wsl
   ```
4. Inside WSL, follow the **Quick start (Linux + same-host headscale)**
   above. Files live in `\\wsl$\Ubuntu\home\<user>\skygate\`.
5. Open in VS Code: install the **WSL** extension, then
   `code .` from the WSL terminal.

This is the path with the least friction. Use it unless you have a
specific reason not to.

### Option 2 — Native Go on Windows (no Docker)

For when you want a single .exe, no Docker, full control.

1. Install **Go 1.23+** from https://go.dev/dl/ (use the Windows MSI)
2. Install **Git for Windows** (gives you `bash` + `ssh`)
3. Clone & build:
   ```powershell
   git clone <repo> skygate
   cd skygate
   go build -o skygate.exe ./cmd/skygate
   ```
4. Create `.env` from the example:
   ```powershell
   copy .env.example .env
   notepad .env
   ```
   Important Windows-specific values:
   ```
   SKYGATE_DB=C:\skygate\data\skygate.db
   HEADSCALE_URL=http://localhost:50444     # if headscale also runs on Windows
                                             # or http://192.168.13.69:50444 for LAN
   ```
5. Make sure the data dir exists:
   ```powershell
   mkdir C:\skygate\data
   ```
6. Run as a foreground process (or wrap in NSSM / WinSW for a service):
   ```powershell
   .\skygate.exe
   ```
7. Open `http://localhost:8080/login`.

The first start bootstraps the admin user from `SKYGATE_ADMIN_PASS`.
There is no auto-restart on crash; use NSSM or run under Windows
Task Scheduler if you need resilience.

**Note on Windows + SQLite:** the `go-sqlite3` driver needs CGO. The
official Go installer ships `gcc` via MinGW, so `go build` should work
out of the box. If you see CGO errors, install TDM-GCC or MSYS2.

### Option 3 — Docker Desktop on Windows without WSL

Works but slower than WSL2 (Hyper-V backend has filesystem perf issues).

1. Install **Docker Desktop** (Hyper-V backend, not WSL2)
2. In PowerShell:
   ```powershell
   git clone <repo> skygate
   cd skygate
   copy .env.example .env
   notepad .env
   docker compose up -d --build
   docker compose logs -f skygate
   ```
3. Open `http://localhost:8080/login`.

The Windows-native paths in `.env` (`SKYGATE_DB=C:\skygate\data\...`)
do NOT apply when running inside Docker — keep `SKYGATE_DB=/data/skygate.db`
in that case.

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

## Usage

### Admin

- `/admin/users` — create / list / delete portal users (each is a headscale user too)
- `/admin/devices` — all nodes across all namespaces, with tag/un-tag buttons
- `/admin/acls` — link to Headplane ACL editor (read-only here)
- `/admin/audit` — who-did-what log
- `/admin/derp` — DERP relay status (if you also run the local derper)

### User

- `/my/devices` — your nodes only
- `/my/preauth` — get a one-time preauth key for a new device
- `/my/exit-nodes` — list of available exit nodes (other VPS) with Tailscale IP + country

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
  **Warning:** this logs out every user.
- `SKYGATE_ADMIN_PASS`: drop the user from SQLite, set a new
  `SKYGATE_ADMIN_PASS`, restart.

**What is NOT exposed in the UI**

As of v0.3.11, the `HEADSCALE_API_KEY` is **never rendered in HTML**
(it used to be on `/admin/acls` for Headplane auto-login — removed).
To use the key for Headplane, copy it manually from the Skygate host's
`.env`. This is intentional: any rendered secret can leak via
screenshots, browser extensions, or XSS.

**Other hardening**

- Admin password: bcrypt cost 12 (slow on purpose)
- Sessions: JWT HS256, TTL 24h, HttpOnly + SameSite=Lax
- Cookies: behind HTTPS, the reverse proxy must not strip `Secure`
  (set `proxy_cookie_flags Secure httponly` in nginx)
- Bind Skygate to `127.0.0.1` and only expose via reverse proxy:
  add `ports: ["127.0.0.1:8080:8080"]` in `docker-compose.yml`

## Development

```bash
go build ./...
SKYGATE_JWT_SECRET=$(openssl rand -hex 32) \
  SKYGATE_ADMIN_PASS=testpass123 \
  HEADSCALE_URL=http://localhost:50444 \
  HEADSCALE_API_KEY=<your-key> \
  go run ./cmd/skygate
```

Templates live in `internal/handlers/templates/` and are embedded into
the binary at build time via `//go:embed`. Edit them, rebuild, restart.

## Files & layout

```
skygate/
├── cmd/skygate/main.go         # entrypoint, routing, bootstrap
├── internal/
│   ├── auth/                   # bcrypt + JWT
│   ├── config/                 # env loading
│   ├── db/                     # SQLite schema + queries
│   ├── handlers/               # HTTP handlers + embedded templates
│   │   ├── handlers.go
│   │   ├── templates.go        # //go:embed
│   │   └── templates/          # *.html
│   ├── headscale/              # REST client
│   └── middleware/             # auth check
├── Dockerfile
├── docker-compose.yml
├── .env.example                # ← copy to .env
├── .gitignore
└── README.md
```

## Roadmap (not implemented yet)

- Email notifications on user creation
- QR code for mobile registration
- Device rename through UI
- ACL editor (currently Headplane-only)
- Gitea integration (per-user API key provisioning)
- Audit log filters (by user / action / date)
- Admin API key rotation UI
