# Sidecar Mode — Installing skygate next to an existing headscale

> Audience: operators who already have a working headscale (any
> version, typically behind nginx/Caddy with HTTPS) and want to add
> skygate's exit-rules + portal features without replacing the control
> plane.
>
> If you don't already have headscale, use the all-in-one
> `docker-compose.yml` (which starts headscale + skygate + DERP +
> headplane) instead. This doc is for the "skygate is OPTIONAL
> tooling on top of my existing headscale" use case.

## What this doc covers

- Prerequisite assumptions (you already have headscale running).
- Env vars you MUST set + MUST NOT set.
- Step-by-step deploy (Docker compose or local binary).
- First-run adoption (banner + Sync from headscale + bulk claim +
  exit-server auto-detect + optional auto-sync flag).
- Reapply-ACL flow + warning about overwriting your existing ACL.
- Troubleshooting (the 4 most common failure modes from the
  2026-09-11 operator deployment log).

## What skygate does NOT do in sidecar mode

- Does NOT start its own headscale.
- Does NOT start its own DERP server (unless you explicitly enable it).
- Does NOT replace headplane (you can keep using headplane for
  read-only monitoring).
- Does NOT auto-import your existing ACL into device_rules (see
  "Re-apply ACL" below).
- Does NOT auto-claim nodes joined via
  `headscale preauthkeys create` — UNLESS you set
  `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` (see "First-run
  adoption" below).

## Prerequisites

- headscale running + reachable from the skygate VM via HTTPS.
  Verify: `curl -sI https://hs.your-domain.com/health` returns 200.
- API key with full perms:
  ```bash
  # If headscale is in Docker:
  docker exec headscale headscale apikeys create --expiration 365d
  # If binary on host:
  headscale apikeys create --expiration 365d
  ```
- Postgres or SQLite reachable (skygate v1.5.4+ supports both —
  see "Choosing SQLite vs PostgreSQL" below).
- The `dns.base_domain` in headscale config — must match
  `SKYGATE_BASE_DOMAIN` in skygate env.

## Choosing SQLite vs PostgreSQL (v1.5.4+)

skygate supports two DB backends. The default install
(`install-debian.sh`) prompts for the choice.

- **SQLite** (default for self-host): single file at
  `/var/lib/skygate/skygate.db`. Zero setup, but the file MUST
  be backed up off-host (rsync, restic, etc.) — there is no
  replication. Good for: single-host deployments, dev/test,
  small teams (< 50 users).
- **PostgreSQL** (recommended for prod / HA): set
  `SKYGATE_DB_TYPE=postgres` at install time (or set
  `SKYGATE_DB=postgres://...` in .env). Multi-host safe via your
  existing PG cluster (Patroni, etc.). Good for: production, HA
  setups, > 50 users.

You can switch between the two at any time via
`skygate db-migrate --from=<dsn> --to=<dsn>`. See
`docs/ROADMAP.md` for
the full conversion plan.

## Step-by-step deploy

### Option A: Docker compose (SQLite, recommended for getting started)

```bash
# 1. Clone the skygate repo on the host.
git clone https://github.com/BarsSky/skygate.git
cd skygate

# 2. Edit .env: set HEADSCALE_URL, HEADSCALE_API_KEY,
#    SKYGATE_BASE_DOMAIN. Default DB is SQLite (file-backed at
#    ./data/skygate.db — auto-created on first run).
cat > .env <<EOF
HEADSCALE_URL=https://hs.your-domain.com
HEADSCALE_API_KEY=<paste from `headscale apikeys create`>
SKYGATE_BASE_DOMAIN=hs.your-domain.com
SKYGATE_JWT_SECRET=$(head -c 32 /dev/urandom | xxd -p -c 64)
SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true
EOF

# 3. (Optional) Set SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true to
#    auto-import your existing headscale nodes on first boot.
#    See "First-run adoption" below.

# 4. Bring it up.
docker compose -f docker-compose.sqlite.yml up -d

# 5. Open http://localhost:8080/login and log in as the
#    bootstrap admin (defaults to admin / admin; change
#    immediately).
```

### Option B: Docker compose (PostgreSQL)

```bash
# Same as Option A but use docker-compose.lite.yml + set
# SKYGATE_DB=postgres://skygate:<password>@<host>:5432/skygate
# in .env. Requires a running PG instance reachable from the
# skygate container.
```

### Option C: Bare Debian/Ubuntu install (systemd)

```bash
# Run the interactive installer. Prompts for SQLite vs
# PostgreSQL.
curl -fsSL https://raw.githubusercontent.com/BarsSky/skygate/main/deploy/install.sh | sudo SKYGATE_VERSION=v1.5.4 bash

# Or explicit:
sudo SKYGATE_VERSION=v1.5.4 SKYGATE_DB_TYPE=sqlite bash install-debian.sh
sudo SKYGATE_VERSION=v1.5.4 SKYGATE_DB_TYPE=postgres bash install-debian.sh
```

## First-run adoption

After the first login as admin, the B-mod-first-run-adoption
features (T1-T7) make the empty /admin/devices state
operator-friendly. There are two flows:

### Flow A: Fully automatic (recommended for new deploys)

1. Set `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` in .env
   BEFORE the first `docker compose up`.
2. On first boot, skygate automatically:
   - Detects that node_owner_map is empty AND at least one
     portal_user exists.
   - Pulls every node from headscale via
     `hs.ListAllNodes()`.
   - INSERTs each node into `node_owner_map` with the
     headscale's tag.
   - **Auto-detects exit-nodes** (T6): any node with
     `tag:exit-node` or advertised routes `0.0.0.0/0` / `::/0`
     is INSERTed into `exit_servers` (with `enabled=1` by
     default — the operator can disable specific ones via
     /admin/exit-nodes).
   - Writes an audit row (`action=first_run_auto_sync`,
     `target_type=system`).
3. Operator can immediately visit `/admin/devices` and see
   every node already populated.

### Flow B: Manual (no env var set)

1. After first login, visit `/admin/devices` — a yellow banner
   appears: "headscale has N un-adopted users".
2. Click the "Sync from headscale" button at the top of the
   page. This populates `node_owner_map` + auto-detects
   exit-nodes (same as Flow A, just manually triggered).
3. For each portal user that should own some nodes, visit
   `/admin/users/{id}` and click "Claim all nodes for this user"
   (T4-T5). This updates `node_owner_map.tagged_by_user_id` for
   every node owned by the headscale user linked to that portal
   user.

### Re-running after upgrades

The first-run auto-sync is a one-shot: it runs ONCE on first
boot (when `node_owner_map` is empty). On subsequent boots, the
auto-sync sees non-empty `node_owner_map` and silently no-ops.
To force a re-sync, click "Sync from headscale" on
`/admin/devices` — the pre-existing admin button.

## Re-apply ACL — the dangerous step

When you click "Re-apply ACL" on `/admin/exit-rules` for the
first time, skygate writes a NEW ACL built from its
`device_rules` table (which is empty on first run = empty ACL
grants). **This OVERWRITES your existing headscale ACL.**

To avoid losing your existing ACL:

```bash
docker exec headscale headscale policy get > ~/headscale-acl-backup.json
```

BEFORE the first Re-apply. The audit log records the SHA-256 of
every ACL apply — you can recover from `acl_snapshots` table via
psql.

## Troubleshooting

### "I see /admin/devices but no devices — what do I do?"

This is the most common first-run issue. Two fixes:

1. **Manual fix**: click "Sync from headscale" on
   /admin/devices (Flow B above).
2. **Auto fix for future deploys**: set
   `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` in .env before
   the next deploy (Flow A above).

### "I see /admin/devices but the page renders as plain HTML (no CSS, no icons)"

This is the B-mod-static-embed prebuilt-image bug. Fixed in
v1.5.3+. Make sure your `SKYGATE_IMAGE` is `:v1.5.4` or
later.

### "Postgres unreachable" / "connection refused"

For docker-compose: ensure the skygate container can reach
the Postgres host (check `docker network ls` + the
`SKYGATE_DB` env var's host:port).

For bare/systemd install: `systemctl status skygate` + check
`/etc/skygate/skygate.env` for the `SKYGATE_DB` value.

### "Nodes joined via `headscale preauthkeys create` don't show up"

Two flows:

- **Auto-flow**: the preauthkey + node are picked up by
  `nodeownership.Backfill` (Strategies A/C/D/E) on the next
  `/my/devices` or `/admin/devices` page load. If a node
  doesn't show up, click "Sync from headscale" on
  /admin/devices to force a re-sync.
- **Bulk-claim flow**: for the operator's escape hatch
  ("I imported headscale users as portal users, now I need
  to attach their existing nodes"), visit
  `/admin/users/{id}` and click "Claim all nodes for this
  user" (T4-T5). The form action is
  `POST /admin/devices/claim-all-for-user`.

## See also

- `docs/deploy.md` — full deployment guide (covers the
  all-in-one path too)
- `docs/ROADMAP.md` — closed-out issues (including the 5
  B-mod-* blocks + the 19+ B-mod-tailscale features that
  shipped in v1.5.3)
- `docs/ROADMAP.md`
  — full implementation plan (T1-T11) for the first-run
  adoption features
- `docs/ROADMAP.md`
  — full implementation plan (T1-T7) for the SQLite+PG
  bidirectional support + conversion tool
- `AGENTS.md` — AI assistant hints (read this if you're
  using skygate as context for an AI agent)
