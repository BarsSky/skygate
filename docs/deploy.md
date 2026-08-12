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
./deploy/backup.sh /home/admin/backups

# Restore on a new host
./deploy/deploy.sh --from-path /home/admin/backups/skygate-full-20260713_153000
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
| `SKYGATE_DB` | `/data/skygate.db` | **LEGACY** (v0.32.x). Pre-v1.3.0 SQLite file path. v1.3.0+ ignores this — the runtime is PG-only. Kept for backward compat with the v0.32.x-era `/data` bind mount. |
| `SKYGATE_DB_DSN` | `postgres://skygate:${PG_DB_PASSWORD}@postgres:5432/skygate?sslmode=disable` | **v1.3.0+ REQUIRED.** libpq URL form. The runtime opens this DSN at every `db.OpenDSN` call and runs `MigratePostgres` on it. For external PG (HA Patroni, RDS), replace `postgres` with the host/port of your cluster. |
| `PG_DB_PASSWORD` | (empty) | **v1.3.0+ REQUIRED for local-PG.** Generate with `openssl rand -hex 24`. The `postgres` docker service reads this as `POSTGRES_PASSWORD` and bakes it into `pg_authid` on first init. The same value goes into `SKYGATE_DB_DSN`. |
| `SKYGATE_JWT_SECRET` | — **[required]** | HS256 secret for session cookies. Generate with `openssl rand -hex 32`. |
| `SKYGATE_ADMIN_USER` | `admin` | Initial admin username (bootstrapped on first start) |
| `SKYGATE_ADMIN_PASS` | — **[required]** | Initial admin password (bootstrapped on first start; ignored if `portal_users` already has the user) |
| `SKYGATE_CONTROL_URL` | derived from `HEADSCALE_URL` | Human-facing URL clients connect to (e.g. `https://head.example.com`) |
| `SKYGATE_EXIT_SSH_KEY` | `/home/admin/.ssh/skygate_sync` | SSH key path inside the skygate container for exit-node sync |
| `SKYGATE_DNS_AUTO_CHECK` | `5m` | Interval for `RunDomainAutoUpdater`. `0` or `off` to disable. |
| `SKYGATE_MAX_RULES_PER_DEVICE` | `200` | Per-device rule cap |
| `SKYGATE_MAX_TOTAL_RULES` | `10000` | Global rule cap |
| `SKYGATE_STAGGER_SYNC` | `true` | Split autoupdate work into batches |
| `SKYGATE_STAGGER_BATCH_SIZE` | `20` | Rules per batch |
| `SKYGATE_STAGGER_INTERVAL` | `30s` | Delay between batches |
| `SKYGATE_USER_MAX_RULES` | (empty) | Per-user caps. Format: `user1:N1,user2:N2`. Example: `admin:2000,alice:500` |

### Headscale (only used by `deploy.sh` to render config)

| Var | Default | What it does |
|---|---|---|
| `HEADSCALE_URL` | `http://headscale:50444` | URL Skygate uses to call headscale |
| `HEADSCALE_API_KEY` | — **[required]** | API key Skygate uses. Generate with `docker exec headscale headscale apikeys create --expiration 365d` |
| `HEADSCALE_CONTAINER` | `headscale` | Container name for `docker exec` tag/CLI fallback |
| `HEADSCALE_SERVER_URL` | `https://head.example.com` | Public headscale URL (advertised to clients) |
| `HEADSCALE_BASE_DOMAIN` | `tsnet.example.com` | MagicDNS base domain (e.g. `<host>.tsnet.example.com` for a device named `host`) |
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
SKYGATE_EXIT_SSH_EXIT_NODE_A=root@relay-1.example.com
SKYGATE_EXIT_SSH_EXIT_NODE_B=root@relay-2.example.com
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
| `DEPLOY_HEADSCALE_DIR` | `/home/admin/headscale` | Where headscale's `config/`, `docker-compose.yml`, `headplane/` live |
| `DEPLOY_SKYGATE_DIR` | `/home/admin/skygate` | Skygate repo root |
| `DEPLOY_BACKUP_DIR` | `/home/admin/skygate/backup` | Default output for `./deploy/backup.sh` |
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
cd /home/admin/skygate
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

What's in the archive (v1.3.0+):

| Item | Source | Why |
|---|---|---|
| `.env` | `${PROJECT_DIR}/.env` | Skygate secrets (chmod 600 in the backup) |
| `skygate-repo.bundle` | `git bundle create --all` | Source code, restorable with `git clone` |
| `skygate-git-log.txt` | `git log --oneline -10` | Quick eyeball of HEAD |
| `skygate-pg.sql` | `pg_dump -Fp --clean --if-exists` from the live PG cluster (v1.3.0+) | Portal DB. Replayable with `psql -f skygate-pg.sql`. Pre-v1.3.0 archives had `skygate.db` (SQLite file) instead. |
| `headscale-db.sqlite` | docker volume `headscale_headscale_data` | Headscale DB (still SQLite in headscale 0.29.x) |
| `headscale-config/` | `${DEPLOY_HEADSCALE_DIR}/config/` | `config.yaml`, `noise_private.key`, etc. |
| `headplane-config.yaml` | `${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml` | |
| `headplane-data/` | docker volume `headscale_headplane_data` | |
| `ssh/` | `${SSH_DIR}/skygate_sync{,.pub}` | |
| `derper.conf`, `derpmap.json` | DERP paths (if enabled) | |
| `skygate-image.tar`, `headscale-image.tar`, `headplane-image.tar` | `docker save` | Pre-pulled images, in case the registry is down on restore |
| `inventory.txt` | generated | Manifest |

> **Backup integrity check:** the script runs `psql` against the live
> PG cluster (`SELECT count(*) FROM pg_tables WHERE schemaname='public'`
> + presence check on `portal_users`/`device_rules`/`acl_snapshots`/
> `audit_log`). Pre-v1.3.0 this was `sqlite3 ... 'PRAGMA integrity_check'`.
> The v1.3.0+ check is stronger: it proves the cluster has all
> expected public tables AND the 4 critical tables. The dump-replay
> check (run separately by `scripts/verify_backup.sh` on a cron)
> replays the dump into a throwaway postgres:15-alpine container
> and asserts the same invariants on the replayed DB — proof that
> the dump is structurally valid AND replayable.

> **WAL on backup (headscale only):** the script calls
> `PRAGMA wal_checkpoint(FULL)` on `headscale_headscale_data/db.sqlite`
> before `docker run … cp`. Without this, the .db file alone is
> inconsistent if a write was in-flight. Skygate's PG is unaffected
> — PG's WAL is managed by the cluster, not by skygate.

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
10. **If `skygate-pg.sql` is in the backup (v1.3.0+)**:
    a. The PG cluster must be running first. If using the
       `local-pg` profile: `docker compose --profile local-pg up -d`
       brings it up; the cluster is empty (no database yet).
    b. Apply the dump: `docker run --rm -i --network headscale_default \
       -e PGPASSWORD="$PG_DB_PASSWORD" postgres:15-alpine \
       psql -h postgres -U skygate -d skygate -v ON_ERROR_STOP=1 < skygate-pg.sql`.
    c. The dump uses `--clean --if-exists` so it's idempotent against
       a partially-populated database.
    d. For external PG (Patroni, RDS), use the operator's psql client
       with the appropriate `-h host -p port -U user -d db` flags.
11. **If `skygate.db` is in the backup (v0.32.x legacy archive)**,
    the operator is on a pre-v1.3.0 SQLite archive. v1.3.0+ cannot
    read SQLite. See "PostgreSQL migration from SQLite" below for the
    one-time conversion.
12. Starts skygate.
13. (DERP, if enabled) restores `derper.conf` + `derpmap.json`.

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
   `http://192.0.2.1:50444` for LAN headscale).
6. `mkdir C:\skygate\data`
7. Run foreground: `.\skygate.exe` (no auto-restart — use NSSM or
   Task Scheduler for service mode).

> **CGO:** `mattn/go-sqlite3` needs CGO. The official Go installer
> ships gcc via MinGW, so `go build` works out of the box. If you
> see CGO errors, install TDM-GCC or MSYS2.

## 8. Cross-cutting rules

- **VM is the source of truth for runtime behaviour.** All deploy
  and runtime verification happens on `admin@192.0.2.1`.
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
         \"DELETE FROM portal_users WHERE username='admin';\""
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

## 10. PostgreSQL

v1.3.0+ is PostgreSQL-only. The `mattn/go-sqlite3` driver and all 30
SQLite migrations are gone (commits `b1baa4a` and `5bc0017`).
The runtime opens a single PG connection pool via `db.OpenDSN(dsn)` and
runs `MigratePostgres` on every container start (idempotent — safe
across restarts).

### Two deployment modes

The same `SKYGATE_DB_DSN` env var works for both modes. The operator
picks ONE.

#### Mode A — local docker-compose PG (default for fresh installs)

The `docker-compose.yml` ships a `postgres` service (gated behind the
`local-pg` profile) that brings up a single `postgres:15-alpine` container
on the `headscale_default` docker network. The same `.env` drives both
the postgres container (`POSTGRES_PASSWORD=$PG_DB_PASSWORD`) and the
skygate container (`SKYGATE_DB_DSN=postgres://skygate:$PG_DB_PASSWORD
@postgres:5432/skygate?sslmode=disable`).

```bash
# 1. Set the password
echo "PG_DB_PASSWORD=$(openssl rand -hex 24)" >> .env

# 2. Bring up skygate + postgres
docker compose --profile local-pg up -d

# 3. Validate
./deploy/validate.sh
# R1 /healthz, R2 /readyz (db:ok, headscale:ok), R30 PG integrity — all PASS
```

The named volume `skygate-pg-data` persists the cluster. Data survives
`docker compose down`; only `docker volume rm skygate-pg-data` wipes
it. To restore from a backup: see Section 6 step 10.

#### Mode B — external PG (HA Patroni, RDS, etc.)

Operators with an existing PG cluster (e.g. svyatoslava on
`45.152.198.217:5432` behind Patroni) point skygate at it:

```bash
# 1. Create the skygate database + user on the cluster
CREATE USER skygate WITH PASSWORD '...';
CREATE DATABASE skygate OWNER skygate;

# 2. Set the DSN
cat >> .env <<EOF
SKYGATE_DB_DSN=postgres://skygate:<password>@45.152.198.217:5432/skygate?sslmode=require
PG_DB_PASSWORD=<unused in this mode>
EOF

# 3. Bring up skygate (no postgres service needed)
docker compose up -d
```

The `postgres` service is gated behind `local-pg`; it's not started
in Mode B. The `skygate` container still needs `SKYGATE_DB_DSN` (read
from `.env` via `env_file`); `PG_DB_PASSWORD` is unused in this mode
but kept for consistency.

### Verifying the live cluster

```bash
# From the operator workstation (over SSH):
bash scripts/verify_post_deploy.sh
# R1-R30 — all the runtime checks, including R30 (PG integrity via psql_vm)

# From the skygate host directly (one-off):
docker exec skygate-postgres-1 psql -U skygate -d skygate -c "\dt"
#   — lists all public tables
docker exec skygate-postgres-1 psql -U skygate -d skygate -c \
  "SELECT count(*) FROM pg_tables WHERE schemaname='public'"
#   — should be ≥20 after v1.3.0 migrations
```

### Backup

`scripts/backup.sh` runs `pg_dump -Fp --clean --if-exists` via a
throwaway `postgres:15-alpine` container on the docker bridge, writes
the output to `skygate-pg.sql` in the backup archive, then verifies
the cluster with a `psql` connectivity + table-count check (exit 0
if ≥20 public tables, 1 otherwise).

To restore from a backup, see Section 6 step 10.

### Recovery from corruption

`scripts/recover_db_corruption.sh` is the operator runbook for
recovery. Pre-v1.3.0 it used the `sqlite3 .recover` flow to rebuild
a clean SQLite file from a corrupted one. v1.3.0+ focuses on the
realistic PG failure modes:

1. **Disk full** — PG flips to read-only mode (`default_transaction_read_only=on`).
   The script detects this and runs `ALTER SYSTEM RESET default_transaction_read_only;
   SELECT pg_reload_conf();` to flip back.
2. **Container down** — restart the container (`docker compose --profile
   local-pg up -d`).
3. **Cluster unrecoverable** (data dir corruption, wrong permissions) —
   the script prints the exact `scripts/restore.sh` invocation to
   replay the latest `skygate-pg.sql` backup. **Restoration is
   destructive and requires explicit operator confirmation** (no
   auto-restore).

PG's WAL+full_page_writes=on prevents the btree-inconsistency class
of failures that motivated the v0.32.5 SQLite flow. The .recover +
rebuild pattern was SQLite-specific and is not applicable to PG.

## 11. PostgreSQL migration from SQLite (one-time, for v0.32.x legacy)

If you have a v0.32.x-era archive (containing `skygate.db` instead of
`skygate-pg.sql`), the v1.3.0+ skygate binary cannot read it. You need
a one-time conversion.

### When is this needed?

- Your backup archive contains `skygate.db` (SQLite file) — not
  `skygate-pg.sql` (text-format pg_dump).
- Your live `skygate-data` named volume still has a `skyage.db` file
  from v0.32.x.

If neither is true, you're already on v1.3.0+ — skip this section.

### Convert a v0.32.x `skygate.db` file to a PG cluster

The `cmd/apply_pg_migrations/main.go` binary applies the v0.32.x
migration set to a fresh PG cluster (v0.20 through v0.49 are bundled
in `internal/db/migrations_pg.go` and `migrations_v0.50_pg.go`). The
flow is:

```bash
# 1. Bring up an empty PG cluster (local or external)
docker compose --profile local-pg up -d postgres
# 2. Run the migrations
SKYGATE_TEST_PG_DSN=postgres://skygate:$PG_DB_PASSWORD@localhost:5432/skygate?sslmode=disable \
  go run -tags postgres ./cmd/apply_pg_migrations
# Output: "Applied N migrations" + table list

# 3. Translate the SQLite rows to PG INSERTs
python3 scripts/dump_sqlite.py /path/to/skygate.db /tmp/skygate_pg_dump.sql
# The script handles:
#   - INTEGER timestamp → TIMESTAMPTZ literal
#   - '?' placeholder → '$1' / '$2' / ...
#   - 'true'/'false' → 'true'/'false' (already compatible)
#   - json_object/json_group_array → json_build_object/json_agg
#   - 'INTEGER PRIMARY KEY AUTOINCREMENT' → 'SERIAL PRIMARY KEY'

# 4. Apply the dump
docker run --rm -i --network headscale_default \
  -e PGPASSWORD="$PG_DB_PASSWORD" postgres:15-alpine \
  psql -h postgres -U skygate -d skygate -v ON_ERROR_STOP=1 < /tmp/skygate_pg_dump.sql
```

The `dump_sqlite.py` script is a one-shot translator (not committed
to the repo). It was used internally during the v1.3.0 cutover
(2026-08-03) on the live skygate-vm → svyatoslava. If your data
volume is small (<10k rows) the manual `sqlite3 ... .dump` + sed
replacements work too.

### After migration

1. Verify: `bash scripts/verify_post_deploy.sh` (R19 should show
   `user_prefs` + `device_prefs` JSON arrays matching what
   `sqlite3 skygate.db "SELECT ... FROM user_exit_node_prefs"` showed
   pre-migration).
2. Take a fresh backup with `scripts/backup.sh` — the new archive
   will contain `skygate-pg.sql` (not `skygate.db`).
3. Delete the old SQLite file from the `skygate-data` volume to
   free disk: `docker exec skygate rm -f /data/skygate.db`.

The `skygate-data` named volume itself is kept (it's a no-op
container for v1.3.0+ — the runtime is PG-only). Operators who
want the disk back can `docker volume rm skygate-data` after
confirming nothing references it.
