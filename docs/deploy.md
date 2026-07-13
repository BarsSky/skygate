# Deploy / backup / restore

This document covers the full lifecycle: from a fresh VM to a healthy
production deployment, and back again (backup → restore on a new
host). The deploy scripts in `deploy/` are **cross-platform** (they
work on Linux and Windows via Git Bash / WSL2) and **idempotent** —
re-running them on a healthy host is safe.

## TL;DR

```bash
# Fresh install
git clone <repo> skygate && cd skygate
cp .env.example .env && nano .env         # fill the secrets
./deploy/deploy.sh
./deploy/validate.sh

# Backup
./deploy/backup.sh /home/skyadmin/backups

# Restore on a new host
./deploy/deploy.sh --from-path /home/skyadmin/backups/skygate-full-20260713_153000
./deploy/validate.sh
```

## 1. Environment

Every tunable lives in `.env` (gitignored — never commit it). The
canonical template is `.env.example`. Required variables are marked
**[required]**; the rest have sensible defaults.

### Skygate

| Var | Default | What it does |
|---|---|---|
| `SKYGATE_PORT` | `8080` | HTTP listen port |
| `SKYGATE_DB` | `/var/lib/skygate/skygate.db` | SQLite path inside the container |
| `SKYGATE_JWT_SECRET` | — **[required]** | HS256 secret for session cookies. Generate with `openssl rand -hex 32`. |
| `SKYGATE_ADMIN_USER` | `skyadmin` | Initial admin username (bootstrapped on first start) |
| `SKYGATE_ADMIN_PASS` | — **[required]** | Initial admin password (bootstrapped on first start; ignored if `portal_users` already has the user) |
| `SKYGATE_CONTROL_URL` | derived from `HEADSCALE_URL` | Human-facing URL clients connect to (e.g. `https://head.skynas.ru`) |
| `SKYGATE_EXIT_SSH_KEY` | `/home/skyadmin/.ssh/skygate_sync` | SSH key path inside the skygate container for exit-node sync |
| `SKYGATE_DNS_AUTO_CHECK` | `5m` | Interval for `RunDomainAutoUpdater`. `0` or `off` to disable. |
| `SKYGATE_MAX_RULES_PER_DEVICE` | `200` | Per-device rule cap |
| `SKYGATE_MAX_TOTAL_RULES` | `10000` | Global rule cap |
| `SKYGATE_STAGGER_SYNC` | `true` | Split autoupdate work into batches |
| `SKYGATE_STAGGER_BATCH_SIZE` | `20` | Rules per batch |
| `SKYGATE_STAGGER_INTERVAL` | `30s` | Delay between batches |
| `SKYGATE_USER_MAX_RULES` | (empty) | Per-user caps. Format: `user1:N1,user2:N2`. Example: `skyadmin:2000,alice:500` |

### Headscale (only used by `deploy.sh` to render config)

| Var | Default | What it does |
|---|---|---|
| `HEADSCALE_URL` | `http://headscale:50444` | URL Skygate uses to call headscale |
| `HEADSCALE_API_KEY` | — **[required]** | API key Skygate uses. Generate with `docker exec headscale headscale apikeys create --expiration 365d` |
| `HEADSCALE_CONTAINER` | `headscale` | Container name for `docker exec` tag/CLI fallback |
| `HEADSCALE_SERVER_URL` | `https://head.example.com` | Public headscale URL (advertised to clients) |
| `HEADSCALE_BASE_DOMAIN` | `tsnet.example.com` | MagicDNS base domain |
| `HEADSCALE_AUTO_APPROVE_ROUTES` | `0.0.0.0/0,::/0` | Comma-separated CIDRs auto-approved on the headscale side |
| `HEADSCALE_DERP_URLS` | `https://controlplane.tailscale.com/derpmap/default` | Comma-separated DERP map URLs |
| `HEADSCALE_LOG_LEVEL` | `info` | |

### Headplane (optional UI on `:50445`)

| Var | Default | What it does |
|---|---|---|
| `HEADPLANE_HEADSCALE__URL` | `http://headscale:50444` | Headplane → headscale |
| `HEADPLANE_HEADSCALE__INSECURE` | `true` | HTTP, not HTTPS |
| `HEADPLANE_HEADSCALE__API_KEY` | same as `HEADSCALE_API_KEY` | Headplane → headscale auth |
| `HEADPLANE_SERVER__HOST` | `0.0.0.0` | |
| `HEADPLANE_SERVER__PORT` | `50445` | |
| `HEADPLANE_SERVER__COOKIE_SECURE` | `false` | |
| `HEADPLANE_SERVER__COOKIE_SECRET` | — **[required]** | `openssl rand -hex 16` |

### Exit-node SSH sync (per exit-node)

Each exit-node has a separate env var:

```
SKYGATE_EXIT_SSH=user1@exit1.example.com
SKYGATE_EXIT_SSH_EMILIA=root@emilia.example.com
SKYGATE_EXIT_SSH_SHARLOTTA=root@sharlotta.example.com
```

The variable name pattern is `SKYGATE_EXIT_SSH_<UPPERNAME>`. The
admin UI at `/admin/exit-nodes` reads them and stores in
`exit_servers.ssh_target`.

### DERP relay (optional)

| Var | Default | What it does |
|---|---|---|
| `DERP_ENABLED` | `false` | |
| `DERP_HOSTNAME` | `derp.example.com` | |
| `DERP_PRIVATE_KEY` | — **[required if DERP on]** | `openssl rand -hex 32` |
| `DERP_VERIFY_CLIENTS_URL` | `https://head.example.com` | |
| `DERP_STUN_PORT` | `3478` | |
| `DERP_HTTP_PORT` | `8443` | |
| `DERP_MAP_PORT` | `8765` | |

### Deployment paths (deploy.sh)

| Var | Default | What it does |
|---|---|---|
| `DEPLOY_HEADSCALE_DIR` | `/home/skyadmin/headscale` | Where headscale's `config/`, `docker-compose.yml`, `headplane/` live |
| `DEPLOY_SKYGATE_DIR` | `/home/skyadmin/skygate` | Skygate repo root |
| `DEPLOY_BACKUP_DIR` | `/home/skyadmin/skygate/backup` | Default output for `./deploy/backup.sh` |
| `DOCKER_NETWORK` | `headscale_default` | The shared docker network headscale + headplane + skygate all attach to |
| `DOCKER_SUBNET` | `172.18.0.0/16` | |

## 2. Fresh install

```bash
# 1. Get the secrets
openssl rand -hex 32          # SKYGATE_JWT_SECRET
openssl rand -hex 16          # HEADPLANE_SERVER__COOKIE_SECRET
openssl rand -hex 32          # DERP_PRIVATE_KEY (if DERP_ENABLED=true)
docker exec headscale headscale apikeys create --expiration 365d   # HEADSCALE_API_KEY

# 2. Clone + configure
git clone <repo> skygate
cd skygate
cp .env.example .env
nano .env    # paste the secrets; set HEADSCALE_URL, SKYGATE_ADMIN_PASS, etc.

# 3. Deploy (renders configs, builds, brings up containers)
./deploy/deploy.sh

# 4. Validate
./deploy/validate.sh
```

`deploy.sh` runs six steps:

1. **Directories & network** — creates `${DEPLOY_HEADSCALE_DIR}/{config,headplane}`,
   ensures `${DOCKER_NETWORK}` exists at the requested subnet.
2. **Headscale config** — renders `deploy/templates/headscale-config.yaml.tmpl`
   to `${DEPLOY_HEADSCALE_DIR}/config/config.yaml`, generates `noise_private.key`
   if missing (preserved on subsequent runs).
3. **Headplane config** — copies `deploy/templates/headplane-config.yaml`
   to `${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml`.
4. **Start headscale + headplane** — `docker compose up -d`, waits for
   `http://localhost:50444/api/v1/node` to return 200, waits for
   `http://localhost:50445/admin/` to be 2xx.
5. **Start skygate** — `docker compose up -d`, waits for
   `http://localhost:${SKYGATE_PORT}/login` to return 200.
6. **DERP** (if `DERP_ENABLED=true`) — renders `derper-compose.yml`,
   generates `derpmap.json` + `derper.conf` if missing, starts the
   derper container.

After success, services are at:

```
Headscale API:  http://localhost:50444
Headplane UI:   http://localhost:50445/admin/
Skygate:        http://localhost:${SKYGATE_PORT}/login
DERP relay:     https://${DERP_HOSTNAME}    (if enabled)
```

## 3. Validate (post-deploy / post-restore)

```bash
./deploy/validate.sh
```

Checks containers, HTTP endpoints, headscale nodes, Skygate DB user
count and rule count, ACL policy reachability, and (if DERP) the
derper container. Exits 0 if all green, 1 otherwise. Safe to run
repeatedly.

## 4. In-place update

After `git pull`:

```bash
cd /home/skyadmin/skygate
docker compose restart skygate    # rebuilds inside the container (entrypoint.sh runs `go build`)
while pgrep -f "go build" > /dev/null; do sleep 3; done
make test                          # bilingual smoke + check_exit_nodes
```

The first compile after a major dependency bump takes ~5 min. Subsequent
restarts are fast (incremental Go build).

## 5. Backup

```bash
./deploy/backup.sh [/path/to/output]
```

Default output: `${DEPLOY_BACKUP_DIR}/skygate-full-YYYYMMDD_HHMMSS/`,
packaged as `.tar.gz` (with SHA256).

What's in the archive:

| Item | Source | Why |
|---|---|---|
| `.env` | `${PROJECT_DIR}/.env` | Skygate secrets (chmod 600 in the backup) |
| `skygate-repo.bundle` | `git bundle create --all` | Source code, restorable with `git clone` |
| `skygate-git-log.txt` | `git log --oneline -10` | Quick eyeball of HEAD |
| `skygate.db` | docker volume `skygate-data` (WAL-checkpointed first) | Portal DB |
| `headscale-db.sqlite` | docker volume `headscale_headscale_data` | Headscale DB |
| `headscale-config/` | `${DEPLOY_HEADSCALE_DIR}/config/` | `config.yaml`, `noise_private.key`, etc. |
| `headplane-config.yaml` | `${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml` | |
| `headplane-data/` | docker volume `headscale_headplane_data` | |
| `ssh/` | `${SSH_DIR}/skygate_sync{,.pub}` | |
| `derper.conf`, `derpmap.json` | DERP paths (if enabled) | |
| `skygate-image.tar`, `headscale-image.tar`, `headplane-image.tar` | `docker save` | Pre-pulled images, in case the registry is down on restore |
| `inventory.txt` | generated | Manifest |

> **WAL on backup:** the script calls `PRAGMA wal_checkpoint(FULL)`
> on `skygate-data/skygate.db` and `headscale_headscale_data/db.sqlite`
> before `docker run … cp`. Without this, the .db file alone is
> inconsistent if a write was in-flight.

## 6. Restore

```bash
# On a fresh host with docker + python3 installed
git clone <repo> skygate
cd skygate
./deploy/deploy.sh --from-path /path/to/skygate-full-YYYYMMDD_HHMMSS
./deploy/validate.sh
```

What `--from-path` does:

1. Loads `.env` from the backup (if present) as `SKYGATE_ENV`.
2. Renders headscale config the same as fresh install.
3. **If `noise_private.key` is in the backup**, copies it to
   `${DEPLOY_HEADSCALE_DIR}/config/`. **Warning:** if the noise
   key is missing, **all existing headscale API keys are invalid** —
   you must regenerate them after restore.
4. Renders headplane config.
5. Starts headscale + headplane.
6. **If `headscale-db.sqlite` is in the backup**, copies it into
   the `headscale_headscale_data` volume (using `docker run` with
   bind-mounted source).
7. **If `skygate-repo.bundle` is in the backup** and `.git` is
   missing in `${DEPLOY_HEADSCALE_DIR}`, restores source.
8. **If `.env` is in the backup**, copies to `${PROJECT_DIR}/.env`.
9. **If `ssh/` is in the backup**, copies keys to `${SSH_DIR}`.
10. **If `skygate.db` is in the backup**, copies it into the
    `skygate-data` volume.
11. Starts skygate.
12. (DERP, if enabled) restores `derper.conf` + `derpmap.json`.

> **The backup archive is self-contained.** You don't need to keep
> `${DEPLOY_BACKUP_DIR}` around — the `.tar.gz` is the unit of
> restore.

## 7. Windows specifics

Two paths, pick what matches your machine.

### WSL2 + Docker Desktop (recommended)

1. `wsl --install` (reboot if asked)
2. Install Docker Desktop with WSL2 backend
3. In a WSL2 terminal, follow the Linux path above. Files live at
   `\\wsl$\Ubuntu\home\<user>\skygate\`.
4. Open in VS Code: install the WSL extension, then `code .` from
   the WSL terminal.

### Native Go on Windows (no Docker)

1. Install Go 1.23+ (Windows MSI)
2. Install Git for Windows (gives you `bash` + `ssh`)
3. `git clone <repo> skygate`, `cd skygate`
4. `go build -o skygate.exe ./cmd/skygate`
5. `copy .env.example .env`, `notepad .env` — set
   `SKYGATE_DB=C:\skygate\data\skygate.db`,
   `HEADSCALE_URL=http://localhost:50444` (or
   `http://192.168.13.69:50444` for LAN headscale).
6. `mkdir C:\skygate\data`
7. Run foreground: `.\skygate.exe` (no auto-restart — use NSSM or
   Task Scheduler for service mode).

> **CGO:** `mattn/go-sqlite3` needs CGO. The official Go installer
> ships gcc via MinGW, so `go build` works out of the box. If you
> see CGO errors, install TDM-GCC or MSYS2.

## 8. Cross-cutting rules

- **VM is the source of truth for runtime behaviour.** All deploy
  and runtime verification happens on `skyadmin@192.168.13.69`.
  Windows (this workspace) is for code edits and fast iteration
  only. See [AGENTS.md](../AGENTS.md#working-environment-vm-vs-windows).
- **No commit without `make test` green on VM.** CI is a safety net,
  not a primary signal — the `scripts/smoke.sh` smoke test runs
  Skygate itself, which CI doesn't.
- **Backup before any schema change.** Migrations since v0.20 are
  idempotent and safe to apply, but a bad `INSERT INTO ... SELECT`
  in a migration can be unrecoverable. `cp /data/skygate.db
  /data/skygate.db.pre-migration` is two seconds and saves hours.

## 9. Operational runbook

### Restart stuck skygate

```bash
docker compose restart skygate
docker logs --tail 100 -f skygate
```

### Reset the admin password

```bash
docker run --rm -v skygate-data:/data alpine \
  sh -c "apk add --no-cache sqlite >/dev/null && \
         sqlite3 /data/skygate.db \
         \"DELETE FROM portal_users WHERE username='skyadmin';\""
# Edit .env to set SKYGATE_ADMIN_PASS=newpass
docker compose restart skygate
```

### Force-regenerate headscale API key (after restore without noise key)

```bash
docker exec headscale headscale apikeys create --expiration 365d
# paste into .env
docker compose restart skygate
# delete the old key
docker exec headscale headscale apikeys expire <old-key-id>
```

### Wipe and start over

```bash
docker compose down -v   # removes skygate-data volume
docker compose up -d
# portal_users, device_rules, acl_snapshots, etc. are gone
# headscale_db is intact (different volume)
```

## See also

- [README.md](../README.md) — top-level orientation
- [docs/architecture.md](architecture.md) — runtime topology
- [docs/db-schema.md](db-schema.md) — what gets written
- [CHANGELOG.md](../CHANGELOG.md) — version history
