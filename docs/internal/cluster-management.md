# Cluster Management Design & Plan (skygate v1.5.0+)

> **Status**: Phase 0 (design). D1–D8 confirmed 2026-09-01.
> **Owner**: BarsSky.
> **Goal**: admin users configure cluster via the project (UI/CLI/init/join), never via SSH or hand-edited .env.

---

## 1. Vision

**Today** (manual):
- new node = SSH + scp scripts + edit .env + run Patroni by hand
- DB move = pg_dump + scp + pg_restore + edit DSN + restart skygate
- failover = pray (or VNC + Patroni CLI)
- monitoring = ssh to each node + journalctl

**Target** (project-managed):
- new node = `skygate join <token>` or `/admin/nodes` button
- DB move = `/admin/database` → "Migrate to new host" → live progress, audit
- failover = `/admin/ha` → "Force failover" button (Patroni auto for PG, manual for skygate role)
- monitoring = `/admin/cluster` → one page: topology, health, replication, metrics, events

**Principle** (from user 2026-09-01): "If admin must SSH to do X, then X is a gap, not a workaround. The project's UI/CLI is the primary path."

---

## 2. Core Abstractions

| Abstraction | What | Where it lives |
|---|---|---|
| **Node** | VM/host that participates in the cluster | row in headscale `nodes` table |
| **Role** | What a node does: `app` / `db-primary` / `db-replica` / `control` / `derp` (combinable) | `nodes.role` field |
| **Cluster** | Set of nodes with a shared `cluster_id` | row in `clusters` |
| **Chain** | Ordered list of nodes for failover | `clusters.chain` |
| **Topology** | Current state: which node is active for which role | materialized view, refreshed on events |
| **Database** | PG cluster (1 primary + N replicas) | row in `databases` |
| **Migration** | Move a DB from one location to another | state machine in headscale: `planning` → `dumping` → `restoring` → `verifying` → `flipping` → `cleanup` |

---

## 3. User Workflows

### W1. Bootstrap new cluster from scratch

```
node-1:  skygate init --cluster-id=prod-1 --role=app,db-primary,control,derp
node-2:  skygate init --cluster-id=prod-1 --role=app,db-replica
```

(50+ manual steps today.)

### W2. Add a node to existing cluster

```
on existing:  skygate cluster invite --role=db-replica
  → prints: skygate join eyJhbGciOi...  (signed token)
on new VM:    skygate join eyJhbGciOi...
  → installs services, registers, joins cluster
in UI:        /admin/nodes shows the new node + "Approve" button
admin clicks: → node enters HA chain
```

### W3. Move DB to a new host

```
1. /admin/database → "Migrate" → form: new host, new port
2. UI shows live progress (SSE):
   - pre-check (disk, net, version, connectivity)
   - pg_dump on old host
   - scp to new host
   - pg_restore on new host
   - parallel run (both DBs accepting writes briefly)
   - flip DSN via headscale metadata
   - verify (counts, recent txns, replication status)
   - cleanup old DB
3. audit_log entry "db_migration node=old,new dsn=… status=ok"
```

### W4. Force failover

```
1. /admin/ha → "Force failover" → select target → confirm
2. Patroni promotes standby → primary
3. headscale notifies all skygate nodes of new DB DSN
4. skygate-watchdog re-reads DSN, refreshes pool (no full restart)
5. /admin/cluster shows new topology
6. audit_log "ha_failover from=node-1 to=node-2 reason=manual"
```

### W5. Monitor

```
/admin/cluster:
  [node-1 (agent)] app+db-primary+control  ● online  1.5G/2G RAM  12h uptime
  [node-2 (svi)]  app+db-replica          ● online  1.7G/1.9G RAM 28d uptime

  DB prod-1:
    primary:  node-1
    replicas: node-2 (lag=0s)
    size: 539MB
    xlog: 342GB
    last_backup: 2026-09-01 07:11Z (3.7MB)

  skygate active: node-1
  HA chain: node-1 → node-2

  Recent events:
    [10:33] svi: backup ok (3.7MB, 222 lines TOC)
    [10:21] agent: deployment 2dcd545 active
    [09:50] svi: patroni sync ok
```

---

## 4. Design Decisions (D1–D8) — CONFIRMED 2026-09-01

| # | Question | Decision | User ack |
|---|----------|----------|----------|
| **D1** | Where does cluster state live? | **headscale metadata** (don't add Consul) | ✅ |
| **D2** | How does a node learn its role? | **CLI flag at init + persistent state file** (`/etc/skygate/state.json`) | ✅ |
| **D3** | How does skygate find DB DSN? | **dynamic from headscale + .env as default fallback** | ✅ |
| **D4** | How does a new node join? | **signed invite token** (printed by `skygate cluster invite`) | ✅ |
| **D5** | How is failover triggered? | **Patroni auto for PG, manual UI button for skygate role** | ✅ |
| **D6** | Where do DDL migrations live? | **in DB via `skygate-migrate` CLI** (versioned in `applied_migrations`) | ✅ |
| **D7** | How do we monitor? | **skygate health endpoints first (`/cluster/health`, `/db/health`), Prometheus later** | ✅ |
| **D8** | What about .env files? | **hybrid: .env as default, headscale wins on conflict, with explicit log "DSN override from headscale"** | ✅ |

---

## 5. Phases (priority-ordered)

### Phase 0 — Design Finalization [IN PROGRESS]

- [x] **0.1** Confirm D1–D8 with user (done 2026-09-01)
- [~] **0.2** DB schema for `nodes`, `clusters`, `databases`, `migrations` (in headscale DB)
- [ ] **0.3** API contracts for W1–W5 (HTTP routes + CLI subcommands)
- [ ] **0.4** Bootstrap state machine: `init` / `join` / `leave` / `migrate` / `failover` events
- [ ] **0.5** This file reviewed and approved (pending user review)

#### 0.2 — DB schema (D1: headscale metadata)

Tables to add in headscale DB (or skygate's DB if D1 changes). Prefixed `cluster_` to avoid conflict with headscale's existing tables.

```sql
-- 0.2.1 cluster — definition of one cluster
CREATE TABLE cluster (
  id          TEXT PRIMARY KEY,                -- "prod-1", "staging-2"
  name        TEXT NOT NULL,
  chain       JSONB NOT NULL DEFAULT '[]',     -- ["node-1", "node-2"]
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 0.2.2 cluster_node — one node in a cluster
CREATE TABLE cluster_node (
  id              TEXT PRIMARY KEY,            -- "node-1", "agent-1", "svi-1"
  cluster_id      TEXT NOT NULL REFERENCES cluster(id),
  hostname        TEXT,
  tailscale_ip    INET,
  roles           TEXT[] NOT NULL DEFAULT '{}',-- ["app", "db-primary", "control"]
  state           TEXT NOT NULL DEFAULT 'pending',-- pending|active|draining|failed
  skygate_version TEXT,
  joined_at       TIMESTAMPTZ,
  last_seen_at    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_cluster_node_cluster ON cluster_node(cluster_id);

-- 0.2.3 cluster_database — one DB cluster, with primary + replicas
CREATE TABLE cluster_database (
  id                TEXT PRIMARY KEY,        -- "skygate-staging"
  cluster_id        TEXT NOT NULL REFERENCES cluster(id),
  primary_node_id   TEXT REFERENCES cluster_node(id),
  replica_node_ids   TEXT[] NOT NULL DEFAULT '{}',
  dsn_template      TEXT NOT NULL,           -- "postgres://user:pass@%s:5432/db?sslmode=disable"
  dbname            TEXT NOT NULL,
  username          TEXT NOT NULL,
  sslmode           TEXT NOT NULL DEFAULT 'disable',
  current_dsn       TEXT,                    -- overrides dsn_template if set
  updated_by        TEXT,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_cluster_database_cluster ON cluster_database(cluster_id);

-- 0.2.4 cluster_migration — DDL migration history (D6)
CREATE TABLE cluster_migration (
  id              BIGSERIAL PRIMARY KEY,
  cluster_id      TEXT NOT NULL REFERENCES cluster(id),
  database_id     TEXT NOT NULL REFERENCES cluster_database(id),
  version         TEXT NOT NULL,            -- "V001__initial_schema"
  description     TEXT,
  applied_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  applied_by_node TEXT,
  checksum        TEXT,                     -- SHA256 of migration file
  duration_ms     INTEGER,
  UNIQUE (database_id, version)
);

-- 0.2.5 cluster_invite — pending node invites (D4)
CREATE TABLE cluster_invite (
  id              TEXT PRIMARY KEY,        -- token id (random)
  cluster_id      TEXT NOT NULL REFERENCES cluster(id),
  role            TEXT NOT NULL,
  target_hostname TEXT,
  issued_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at      TIMESTAMPTZ NOT NULL,
  used_at         TIMESTAMPTZ,
  used_by_node_id TEXT,
  signature       TEXT NOT NULL,           -- HMAC-SHA256
  status          TEXT NOT NULL DEFAULT 'pending'-- pending|used|expired|revoked
);

-- 0.2.6 cluster_audit — admin actions log
CREATE TABLE cluster_audit (
  id              BIGSERIAL PRIMARY KEY,
  cluster_id      TEXT,
  actor           TEXT,                    -- admin username or "system"
  action          TEXT NOT NULL,            -- "db_migration", "node_add", "failover", etc.
  target_node_id  TEXT,
  detail          JSONB,                    -- event-specific
  result          TEXT,                     -- "ok", "error"
  error_message   TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_cluster_audit_cluster_time ON cluster_audit(cluster_id, created_at DESC);
```

**Migration file**: `internal/db/migrations_v0_64_b195.go` — creates all 6 tables, indexes. Idempotent (IF NOT EXISTS).

#### 0.3 — API contracts

**HTTP routes (skygate)**:

```
GET  /admin/database                           # UI page (current DSN, source, health)
GET  /api/database/{cluster}/current           # JSON: {dsn, source, host, port, dbname, sslmode, reachable}
POST /api/database/{cluster}/test              # test connection: form: {dsn}, returns: {ok, latency_ms, error}
GET  /db/health                                # JSON: {pool: {active, idle}, replication_lag_s, db_size_bytes, xlog, last_vacuum}
GET  /admin/cluster                            # UI: topology
GET  /api/cluster/{cluster}/nodes              # JSON: list of nodes with role/state
POST /api/cluster/{cluster}/invite             # generate invite token: {role, ttl_hours} → {token, expires_at}
POST /api/cluster/{cluster}/nodes/{id}/approve # approve pending node
GET  /admin/ha                                 # UI: HA chain, force failover
POST /api/cluster/{cluster}/failover           # {target_node, reason} → starts failover
GET  /admin/nodes                              # UI: add/remove nodes
```

**CLI subcommands**:

```
skygate init --cluster-id=<id> --role=<list>   # bootstrap a new node
skygate join <token>                            # join a cluster via invite token
skygate cluster invite --role=<list>            # generate invite token (printed)
skygate cluster nodes                           # list nodes
skygate cluster dbs                             # list databases
skygate cluster failover --target=<node>         # force failover
skygate-migrate up                              # apply pending DDL migrations
skygate-migrate down <version>                  # rollback
skygate-migrate status                          # show applied migrations
```

#### 0.4 — Bootstrap state machine

States and transitions:

```
init:
  init_starting ──(register)──> init_registered ──(admin approve)──> init_active
                                     │
                                     └──(error)──> init_failed ──(retry)──> init_starting

join:
  join_invited ──(skygate join)──> join_registered ──(admin approve)──> join_active
                                                                └──(admin reject)──> join_rejected

migrate (DB migration workflow):
  migrate_planning ──(pre-check ok)──> migrate_dumping ──(dump ok)──> migrate_restoring
       │                                    │                                │
       └──(pre-check fail)──> migrate_failed  └──(dump fail)──> migrate_failed  └──(restore fail)──> migrate_failed
                                                                                          │
                                                                                          v
                                                                              migrate_verifying ──(verify ok)──> migrate_flipping ──(flip ok)──> migrate_cleanup ──(cleanup ok)──> migrate_complete
                                                                                          │                       │                       │                       │
                                                                                          └──(verify fail)──>       └──(flip fail)──>      └──(cleanup fail)──>
                                                                                              migrate_failed          migrate_failed         (manual review)

failover (skygate role switch):
  failover_initiated ──(quorum ok)──> failover_promoting ──(promote ok)──> failover_demoting ──(demote ok)──> failover_complete
                                          │                                    │
                                          └──(promote fail)──>                 └──(demote fail)──>
                                              failover_failed                      (auto-rollback)
```

Each transition emits a `cluster_audit` row.
Each transition updates `cluster_node.state`.
Each transition triggers SSE event for UI live progress.


### Phase 1 — DB Management (HIGHEST) [PENDING D1-D8 ack]

The user is about to **move DB from svi to agent** — this is the immediate need.

- [ ] **1.1** `/admin/database` page — read-only view of current DSN, host, port, dbname, sslmode
- [ ] **1.2** `/admin/database` "Test connection" button (test with new DSN before applying)
- [ ] **1.3** `/admin/database` "Edit DSN" form (validate, save, restart skygate DB pool — not whole process)
- [ ] **1.4** `/admin/database` "Migrate to new host" workflow
  - [ ] **1.4.1** Pre-checks (disk space, pg version, network reachability)
  - [ ] **1.4.2** State machine: `planning` → `dumping` → `restoring` → `verifying` → `flipping` → `cleanup`
  - [ ] **1.4.3** SSE live progress on UI
  - [ ] **1.4.4** Cancellation at any step (with cleanup)
  - [ ] **1.4.5** Rollback if flip fails (re-flip back to old)
- [ ] **1.5** `/db/health` endpoint — connection pool, replication lag (if replica), slow query count, DB size, xlog position
- [ ] **1.6** Audit log: every DSN change, every migration, every flip
- [ ] **1.7** `skygate-migrate` CLI for in-BB schema migrations (separate from DB-migration)
- [ ] **1.8** Server-side enforcement: skygate-watchdog reads DSN from headscale, refreshes pgxpool without full restart

### Phase 2 — Cluster UI (after Phase 1)

- [ ] **2.1** `/admin/cluster` page — topology view (nodes, roles, statuses, replication, metrics, recent events)
- [ ] **2.2** `/admin/nodes` page — add/remove/list nodes
  - [ ] **2.2.1** "Add node" form (role, IP, resources)
  - [ ] **2.2.2** "Generate invite token" button → token shown + expires
  - [ ] **2.2.3** "Approve pending node" button (after `skygate join` registers)
  - [ ] **2.2.4** "Remove node" button (drain + leave + cleanup)
- [ ] **2.3** `skygate init` CLI — full pipeline (idempotent, safe to re-run)
  - [ ] **2.3.1** Check OS, install prereqs
  - [ ] **2.3.2** Install skygate binary (via apt or curl)
  - [ ] **2.3.3** Generate OIDC keys, write to /var/lib/skygate
  - [ ] **2.3.4** Register with headscale (preauth key, tags, role)
  - [ ] **2.3.5** Start skygate + caddy + systemd
  - [ ] **2.3.6** Health check
- [ ] **2.4** `skygate join <token>` CLI — new node onboarding
  - [ ] **2.4.1** Validate token (signed by an existing cluster node)
  - [ ] **2.4.2** Bootstrap services per token-specified role
  - [ ] **2.4.3** Register in headscale, await admin approval
- [ ] **2.5** `bootstrap_standby.sh` → refactored into `skygate init --role=db-replica`
- [ ] **2.6** Bootstrap state machine: `init` / `join` / `drain` / `leave` events

### Phase 3 — Switchover / Failover

- [ ] **3.1** skygate-watchdog (long-running process) — reads DSN from headscale every 30s, hot-reloads pool
- [ ] **3.2** `/admin/ha` page — HA chain visualization, force promote/demote buttons
- [ ] **3.3** Auto-failover for PG (Patroni is already in place, just plumb to UI)
- [ ] **3.4** Manual failover for skygate role (admin button → drain + promote)
- [ ] **3.5** Failover audit log
- [ ] **3.6** `dr_drill.sh` → `skygate failover drill --target=...`
- [ ] **3.7** Auto-rollback if promoted node can't come up

### Phase 4 — Production-ready

- [ ] **4.1** Generic audit log for all admin actions (additions, removals, migrations, failovers)
- [ ] **4.2** `skygate upgrade` — rolling upgrade (one node at a time, drain → upgrade → rejoin)
- [ ] **4.3** Auto-discovery via Tailscale — new node appears in cluster list (admin still approves)
- [ ] **4.4** Alerting (Telegram on failover / migration failure / DB health degraded)
- [ ] **4.5** Prometheus exporter (deferred — health endpoints first)

---

## 6. Mapping gaps to phases (recap)

| Gap | Description | Phase |
|---|---|---|
| G1 | `/admin/database` page | 1.1 |
| G2 | DB migration workflow | 1.4 |
| G3 | DB health monitoring | 1.5 |
| G4 | `/admin/cluster` topology | 2.1 |
| G5 | `/admin/nodes` add/remove | 2.2 |
| G6 | Bootstrap orchestrator (`init`/`join`) | 2.3–2.4 |
| G7 | Dynamic DSN | 3.1 |
| G8 | Audit log for admin ops | 4.1 |
| G9 | Failover orchestrator | 3.2–3.4 |
| G10 | Auto-discovery via Tailscale | 4.3 |
| G11 | Rolling upgrade | 4.2 |

---

## 7. Open questions for user (before Phase 1)

- [x] **D1–D8 ack**: confirmed 2026-09-01 (all as recommended)
- [ ] **Phase 1 sub-task order**: 1.1 (read-only `/admin/database`), 1.4 (migration workflow), or 1.5 (DB health endpoint) first?
- [ ] **Schema location**: confirmed in headscale DB
- [ ] **Token issuance** (D4): admin via UI generates token, or `skygate cluster invite` on existing node?
- [ ] **DSN hot-reload** (D3): pgxpool.Reset() without process restart — ok technically, or must skygate restart?
- [x] **State file** (D2): confirmed `/etc/skygate/state.json`

---

## 8. Status legend

- [ ] not started
- [~] in progress
- [x] done
- [!] blocked
- [—] deferred

(Use this file as the single source of truth for what's been done and what's next.)
