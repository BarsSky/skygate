# Clean Install Walkthrough — v1.5.4 (avoiding the 2026-09-11 issues)

> **Source:** 2026-09-11 deployment log from aro
> (`188.253.20.31`) — the 5 GitHub issues filed by Lamblador (Daniil)
> after the first live deploy against an existing headscale.
> Each step below notes the issue that motivated the change.
>
> **Audience:** operators with a fresh Linux VM who want to add
> skygate next to an existing headscale without hitting the
> same pain points the 2026-09-11 log surfaced.

## What was hard in 2026-09-11 (and what closes those gaps in v1.5.4)

| Pain point in the log | Closed by (v1.5.4) |
|------------------------|---------------------|
| `no-device` on /my/exit-rules despite headscale having 3 nodes | `1ab1b6fa` — `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` auto-syncs on first boot |
| `ensure headscale user: ERROR: invalid input syntax for type integer: ""` on startup | `08cafa35` — dialect-aware migration tracking (SQLite path uses `?`, PG path uses `$N`) |
| Pre-built image had no `linux/amd64` manifest for `:v1.5.2` | ❌ Still open — CI fix needed in `release.yml` (issue #4) |
| Operator couldn't bulk-claim CLI-joined nodes for a portal user | `66bc3897` — `PostAdminDevicesClaimAllForUser` ("Sync all nodes for this user" button) |
| Schema docs said `node_owner_map.user_id` but actual column is `headscale_user_id` | ⚠️ Partial — schema-docs regeneration is a v1.5.5 follow-up; the migration files are the source of truth |
| `lite` Compose had no PG variant (binary refused to start) | `cd28030c` — new `docker-compose.sqlite.yml` (no PG service required) |

## Pre-flight checklist (verifies your host is ready)

```bash
# 1. Linux x86_64 (amd64) host
uname -m    # → x86_64
#   If you see aarch64 / arm64, you need the
#   v1.5.2 multi-arch manifest fix (issue #4) —
#   use :latest / :v1 / :v1.5 tags which have
#   multi-arch manifests.

# 2. Docker + docker compose plugin
docker --version
docker compose version    # → v2.x

# 3. headscale reachable from the skygate VM
curl -fsS https://hs.your-domain.com/health
#   → 200 OK
#   If this fails, fix the network/DNS first —
#   skygate will crash-loop if it can't reach headscale.

# 4. headscale API key (admin scope)
docker exec headscale headscale apikeys create --expiration 365d
#   → outputs "API key: <long string>"
#   Copy this for the .env below.

# 5. headscale `dns.base_domain` (must match skygate env)
docker exec headscale headscale config get | grep base_domain
#   → "base_domain: hs.your-domain.com"
```

## Step 1 — Clone and configure

```bash
git clone https://github.com/BarsSky/skygate.git /opt/skygate
cd /opt/skygate

# Create .env with the values you collected above.
# Important: SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true is
# the v1.5.4 fix for the "no-device" issue — set it BEFORE
# the first boot so skygate auto-imports your existing
# headscale nodes.
cat > .env <<'EOF'
HEADSCALE_URL=https://hs.your-domain.com
HEADSCALE_API_KEY=<paste the key from step 4>
SKYGATE_BASE_DOMAIN=hs.your-domain.com
SKYGATE_JWT_SECRET=$(head -c 32 /dev/urandom | xxd -p -c 64)
SKYGATE_TS_HOSTNAME=skygate-host-1

# DB: pick one. v1.5.4 supports both. The SKYGATE_DB env var is
# the unified selector — `sqlite:/path` or `postgres://...` —
# detected by db.DetectDSN. Pre-v1.5.4 this was SKYGATE_DB_DSN
# only (PG-only); v1.5.4 restored SQLite and added SKYGATE_DB.
#
# Option A: SQLite (default for self-host, no PG needed).
#   SKYGATE_DB is UNSET in the heredoc → the SQLite compose
#   file defaults to sqlite:/var/lib/skygate/skygate.db
#   (bind-mounted from ./data/skygate.db on the host).
#   Nothing to do here for Option A.
#
# Option B: PostgreSQL (prod / HA, requires external PG).
#   ↓↓↓ UNCOMMENT the next line + replace <password> and <host>
#   ↓↓↓ BEFORE the first boot, or skygate will crash-loop with
#   ↓↓↓ "unknown DSN format" (the default SQLite path won't
#   ↓↓↓ match the lite compose file's empty SKYGATE_DB).
# SKYGATE_DB=postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable

# B-mod-first-run-adoption T7 — auto-import on first boot.
# Set to "true" for a clean install next to an existing
# headscale (no manual "Sync from headscale" click required).
SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true
EOF
```

## Step 2 — Pick your deploy mode

### Mode A: SQLite (recommended for getting started)

```bash
docker compose -f docker-compose.sqlite.yml up -d
docker compose -f docker-compose.sqlite.yml logs -f skygate
```

Look for in the logs:
- `first-run: auto-syncing N nodes from headscale` (if the
  flag is set and you have an existing headscale)
- `first-run: auto-sync complete (inserted=N updated=M)`
- `healthz listening on :8080`

Then open `http://localhost:8080/login`, sign in as the
bootstrap admin (defaults to `admin` / `admin` — **change
immediately**), and verify `/admin/devices` shows your
existing headscale nodes already imported.

#### Verify SKYGATE_ADMIN_USER ↔ headscale admin sync (v1.5.2+)

After first login, run the admin-user-sync B-check to verify the
bootstrap admin is consistent across skygate + headscale:

```bash
bash scripts/check_b_admin_user_sync.sh
# Expected output:
#   PASS  A: exactly one admin in portal_users
#   PASS  B: portal admin name (skyadmin) == SKYGATE_ADMIN_USER
#   PASS  C: admin has headscale_user_id=86
#   PASS  D: headscale has user with id=86 (name=skyadmin)
#   PASS  E: headscale name (skyadmin) == portal name (skyadmin)
```

If any contract FAILs (drift detected), `/admin/users` shows a
banner with the appropriate remediation:
- "Adopt as Admin" button — when headscale has the expected user
  but portal doesn't have a matching row (POST
  /admin/users/HSOrphan/adopt with promote_to_admin=true).
- Per-row "Rename" button — when the portal admin's username
  doesn't match SKYGATE_ADMIN_USER (POST /admin/users/{id}/rename).

See `docs/internal/ha-v1.5.0-execution.md` and the
`B-mod-admin-user-sync` row in `AGENTS.md` for the full design
notes (option c, full rename flow).

### Mode B: PostgreSQL (recommended for prod)

```bash
# 1. Ensure your PG is reachable + skygate user exists.
psql -h <pg-host> -U postgres -c "CREATE USER skygate WITH PASSWORD '<password>';"
psql -h <pg-host> -U postgres -c "CREATE DATABASE skygate OWNER skygate;"

# 2. Edit .env:
#    a) UNCOMMENT the SKYGATE_DB=postgres://... line from Step 1
#       (the heredoc leaves it commented out by default — see
#       the "Option B: PostgreSQL" comment block there).
#    b) Replace <password> and <host> with your real values.
#    Final form:
#       SKYGATE_DB=postgres://skygate:<password>@<pg-host>:5432/skygate?sslmode=disable
#    (Or use the legacy SKYGATE_DB_DSN env var — same format,
#     still honored when SKYGATE_DB is unset, for v1.3.0-v1.5.3
#     PG-deployment backward compat.)

# 3. Bring it up.
docker compose -f docker-compose.lite.yml up -d
docker compose -f docker-compose.lite.yml logs -f skygate
```

Same log lines to look for. PG backend is auto-detected via
the `postgres://` DSN prefix (see `internal/db/dialect.go`'s
`DetectDSN`).

## Step 3 — Verify the auto-import worked

```bash
# Open skygate + check /admin/devices
#   - "headscale users detected (N)" banner: HIDDEN
#     (auto-import populated node_owner_map)
#   - Nodes listed: every headscale node shows up
#   - Exit-servers (if any): auto-detected, enabled=1

# Cross-check via the SQLite file or PG:
docker compose -f docker-compose.sqlite.yml exec skygate \
    sqlite3 /var/lib/skygate/skygate.db \
    "SELECT COUNT(*) AS nodes FROM node_owner_map;"
#   → should match your headscale's `headscale nodes list` count
```

## Step 4 — Claim nodes for each portal user

If you have multiple portal users (e.g. `admin`, `daniil`)
and headscale users that should own their respective nodes,
the v1.5.4 bulk-claim flow is the cleanest path:

1. Open `http://localhost:8080/admin/users` — see every portal
   user with their headscale_user_id link.
2. For each user, click "Claim all nodes for this user" (this
   is the new `POST /admin/devices/claim-all-for-user` route
   from B-mod-first-run-adoption T4-T5).
3. The handler `UPDATE node_owner_map SET tagged_by_user_id = ?
   WHERE headscale_user_id = ?` for that user's headscale_user_id.
4. Audit row written to `audit_log` (action=`claim_all_for_user`,
   target_type=`portal_user`, target_id=`<portal_user_id>`).

## Step 5 — Re-apply ACL (CAREFUL — overwrites existing)

This is the dangerous step. skygate's
`GenerateACLForPlane` builds a NEW ACL from its `device_rules`
table (empty on first run = empty ACL grants) and OVERWRITES
the existing headscale ACL via `PUT /api/v1/policy`.

**Before clicking Re-apply on /admin/exit-rules:**

```bash
# 1. Back up the existing ACL.
docker exec headscale headscale policy get > ~/headscale-acl-backup.json

# 2. (Recommended) Read the backup into skygate's device_rules
#    via the /admin/acls/import form. This converts the
#    existing ACL into skygate's per-user shape, so Re-apply
#    is a no-op (regenerates the same policy).
```

If you skip the import + Re-apply with empty device_rules,
**all existing headscale ACL grants are wiped**. The audit
log records the SHA-256 of every ACL apply — you can recover
from the `acl_snapshots` table via `psql`.

## Step 6 — Backups

For SQLite (mode A):
```bash
# The DB is at /var/lib/skygate/skygate.db inside the container.
# Back up the bind-mounted host copy:
docker compose -f docker-compose.sqlite.yml exec skygate \
    sqlite3 /var/lib/skygate/skygate.db ".backup /tmp/backup.db"
docker compose -f docker-compose.sqlite.yml cp skygate:/tmp/backup.db \
    ./backups/skygate-$(date +%F).db
```

For PostgreSQL (mode B):
```bash
# Use your existing PG backup (pg_dump / wal-g / barman / etc.)
pg_dump -h <pg-host> -U skygate skygate > ./backups/skygate-pg-$(date +%F).sql
```

## Step 7 — Switching between DBs (v1.5.4 conversion tool)

If you started on SQLite and want to move to PostgreSQL
(or vice versa), use the new `skygate db-migrate` subcommand
that ships with v1.5.4:

```bash
# SQLite → PostgreSQL (typical "self-host dev → prod scale-up" flow):
docker compose -f docker-compose.sqlite.yml exec skygate \
    skygate db-migrate \
        --from=sqlite:/var/lib/skygate/skygate.db \
        --to=postgres://skygate:<password>@<pg-host>:5432/skygate

# Then update .env:
#   SKYGATE_DB=postgres://skygate:<password>@<pg-host>:5432/skygate
# And restart:
docker compose -f docker-compose.sqlite.yml down
docker compose -f docker-compose.lite.yml up -d
```

The conversion tool (`internal/db/convert.go`) is dialect-aware:
it reads the source schema, translates types (BIGSERIAL ↔
INTEGER PK AUTOINCREMENT, BOOLEAN ↔ INTEGER, JSONB ↔ TEXT,
TIMESTAMPTZ ↔ INTEGER, bytea ↔ BLOB), and copies data row-by-row
in FK-dep order.

## What to do if something goes wrong

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `healthz` returns 503 after 30s | headscale not reachable from skygate container | `docker compose exec skygate curl -v https://$HEADSCALE_URL/health` |
| `no-device` banner on /admin/devices | `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN` was false at first boot | Set it to true + restart, OR click "Sync from headscale" manually |
| `policy get` returns empty after Re-apply | You Re-applied with empty `device_rules` | Restore from `~/headscale-acl-backup.json` via `/admin/acls/import` |
| Image pull fails on amd64 | Multi-arch manifest missing (issue #4, v1.5.2 only) | Use `:latest` / `:v1` / `:v1.5` tags instead of `:v1.5.X` |
| PG crash-loops on `strftime` function error | pre-v1.5.4 binary | Upgrade to v1.5.4 (issue #5) |

## See also

- `docs/sidecar-mode.md` — the operator-facing narrative
  guide (closes the 2026-09-11 gap with the banner +
  bulk-claim + auto-sync chain)
- `docs/issues-closeout.md` — the GitHub issue close-out
  map (commits that close each of the 5 open issues)
- `docs/internal/2026-09-11-skygate-adoption-audit.md` —
  the original audit document
- `docs/superpowers/plans/2026-09-11-b-mod-sqlite-pg-bidi.md`
- `docs/superpowers/plans/2026-09-11-b-mod-first-run-adoption.md`
