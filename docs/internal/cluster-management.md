# Cluster Management Design & Plan (skygate v1.5.0+)

> **Status 2026-09-03**: Phases 1, 2, 3, 4.1, 4.2, 4.3, 4.4 shipped (B1–B225.2). Phase 4.5 (Prometheus exporter) deferred. See §9 for the B-block trail.
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
- [x] **0.2** DB schema for `nodes`, `clusters`, `databases`, `migrations` (in headscale DB) — committed in `3cef7c27` (B195)
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

- [x] **1.1** `/admin/database` page — read-only view of current DSN, host, port, dbname, sslmode *(B200, pre-B215)*
- [x] **1.2** `/admin/database` "Test connection" button (test with new DSN before applying) *(B200, pre-B215)*
- [x] **1.3** `/admin/database` "Edit DSN" form (validate, save, restart skygate DB pool — not whole process) *(B200, pre-B215)*
- [x] **1.4** `/admin/database` "Migrate to new host" workflow *(B200, pre-B215)*
  - [x] **1.4.1** Pre-checks (disk space, pg version, network reachability)
  - [x] **1.4.2** State machine: `planning` → `dumping` → `restoring` → `verifying` → `flipping` → `cleanup`
  - [x] **1.4.3** SSE live progress on UI
  - [x] **1.4.4** Cancellation at any step (with cleanup)
  - [x] **1.4.5** Rollback if flip fails (re-flip back to old)
- [x] **1.5** `/db/health` endpoint — connection pool, replication lag (if replica), slow query count, DB size, xlog position *(B206)*
- [x] **1.6** Audit log: every DSN change, every migration, every flip *(B215 + B221)*
- [x] **1.7** `skygate-migrate` CLI for in-BB schema migrations (separate from DB-migration) *(B200, pre-B215)*
- [x] **1.8** Server-side enforcement: skygate-watchdog reads DSN from headscale, refreshes pgxpool without full restart *(B203, B224)*

### Phase 2 — Cluster UI (after Phase 1)

- [x] **2.1** `/admin/cluster` page — topology view (nodes, roles, statuses, replication, metrics, recent events) *(B200, B215, B216)*
- [x] **2.2** `/admin/nodes` page — add/remove/list nodes *(B200, B217, B218)*
  - [x] **2.2.1** "Add node" form (role, IP, resources) *(B218 — `skygate cluster invite`)*
  - [x] **2.2.2** "Generate invite token" button → token shown + expires *(B218)*
  - [x] **2.2.3** "Approve pending node" button (after `skygate join` registers) *(B217)*
  - [x] **2.2.4** "Remove node" button (drain + leave + cleanup) *(B217 — Drain+Remove)*
- [x] **2.3** `skygate init` CLI — full pipeline (idempotent, safe to re-run) *(B200, B218)*
  - [x] **2.3.1** Check OS, install prereqs
  - [x] **2.3.2** Install skygate binary (via apt or curl)
  - [x] **2.3.3** Generate OIDC keys, write to /var/lib/skygate
  - [x] **2.3.4** Register with headscale (preauth key, tags, role)
  - [x] **2.3.5** Start skygate + caddy + systemd
  - [x] **2.3.6** Health check
- [x] **2.4** `skygate join <token>` CLI — new node onboarding *(B200, B218)*
  - [x] **2.4.1** Validate token (signed by an existing cluster node)
  - [x] **2.4.2** Bootstrap services per token-specified role
  - [x] **2.4.3** Register in headscale, await admin approval
- [x] **2.5** `bootstrap_standby.sh` → refactored into `skygate init --role=db-replica` *(B218 — role presets)*
- [x] **2.6** Bootstrap state machine: `init` / `join` / `drain` / `leave` events *(B215 — cluster_audit events)*

### Phase 3 — Switchover / Failover

- [x] **3.1** skygate-watchdog (long-running process) — reads DSN from headscale every 30s, hot-reloads pool *(B203, B224)*
- [x] **3.2** `/admin/ha` page — HA chain visualization, force promote/demote buttons *(B204 — skygate HA elector + cluster_node roles)*
- [x] **3.3** Auto-failover for PG (Patroni is already in place, just plumb to UI) *(B219 — Patroni /switchover plumbing + SKYGATE_PATRONI_URL)*
- [x] **3.4** Manual failover for skygate role (admin button → drain + promote) *(B204 — cluster.DrainNode + B217)*
- [x] **3.5** Failover audit log *(B215 + B221 — `db.failover` + `db.failover.error` actions with B221 `target_type=cluster_database`)*
- [x] **3.6** `dr_drill.sh` → `skygate failover drill --target=...` *(B200, pre-B215 — `skygate cluster failover-drill`)*
- [x] **3.7** Auto-rollback if promoted node can't come up *(B220 — `PostAdminDatabaseFailoverRollback` + `db.last_failover` state for one-click operator rollback; full auto-rollback deferred to a follow-up)*

### Phase 4 — Production-ready

- [x] **4.1** Generic audit log for all admin actions (additions, removals, migrations, failovers) *(B215 — 8 cluster_audit actions + B221 — `audit_log.target_type` + `target_id` migration + 6+ writers migrated)*
- [x] **4.2** `skygate upgrade` — rolling upgrade (one node at a time, drain → upgrade → rejoin) *(B222 — `internal/cluster/upgrade.go` + `skygate cluster upgrade --target=<h>|--all` + per-node + all modes; binary push between drain and rejoin is the operator's responsibility per the B222.1 follow-up note)*
- [x] **4.3** Auto-discovery via Tailscale — new node appears in cluster list (admin still approves) *(B223 — `internal/cluster/discovery.go` + `runDiscoveryTicker` (5-min default) + `POST /admin/cluster/discover` + `SKYGATE_DISCOVERY_TAG` env var for opt-in filter; admin still gates state=ready via the existing B217 Approve button)*
- [x] **4.4** Alerting (Telegram on failover / migration failure / DB health degraded) *(B225 — Patroni /switchover + rollback + backup failure alerts + B225.1 — B206 healthz sampler DB health transition alert + B225.2 — B203 watchdog PG unreachable alert after 3 consecutive read failures; all wired through the same `schedulerNotifierSink(app.Notifier)` pattern with a local `NotifierSink` interface per package to avoid the `backup → telegram → mesh → backup` import cycle that B225 discovered)*
- [ ] **4.5** Prometheus exporter (deferred — health endpoints first)

---

## 6. Mapping gaps to phases (recap)

| Gap | Description | Phase | Status |
|---|---|---|---|
| G1 | `/admin/database` page | 1.1 | ✅ B200 |
| G2 | DB migration workflow | 1.4 | ✅ B200 |
| G3 | DB health monitoring | 1.5 | ✅ B206 + B225.1 |
| G4 | `/admin/cluster` topology | 2.1 | ✅ B200, B215, B216 |
| G5 | `/admin/nodes` add/remove | 2.2 | ✅ B200, B217, B218 |
| G6 | Bootstrap orchestrator (`init`/`join`) | 2.3–2.4 | ✅ B200, B218 |
| G7 | Dynamic DSN | 3.1 | ✅ B203 (+ B224 stabilization) |
| G8 | Audit log for admin ops | 4.1 | ✅ B215, B221 |
| G9 | Failover orchestrator | 3.2–3.4 | ✅ B204, B217, B219 |
| G10 | Auto-discovery via Tailscale | 4.3 | ✅ B223 |
| G11 | Rolling upgrade | 4.2 | ✅ B222 |

---

## 7. Open questions (resolved)

- [x] **D1–D8 ack**: confirmed 2026-09-01 (all as recommended) — Phases 1–4.4 shipped per that ack
- [x] **Phase 1 sub-task order**: shipped 1.1 (B200) + 1.4 (B200) + 1.5 (B206) in roughly that order
- [x] **Schema location**: confirmed in headscale DB — `cluster_node`, `cluster_audit`, `cluster_database`, `portal_users` etc. all live there
- [x] **Token issuance** (D4): operator runs `skygate cluster invite` on the orchestrator, gets an sgn1 token, copies to the new node, new node runs `skygate cluster join <token>`. UI surfaces the same path via the "Generate invite token" button on /admin/cluster (B218).
- [x] **DSN hot-reload** (D3): pgxpool.Reset() without process restart — confirmed by B203 + B224 stabilization
- [x] **State file** (D2): confirmed `/etc/skygate/state.json`

---

## 8. Status legend

- [ ] not started
- [~] in progress
- [x] done
- [!] blocked
- [—] deferred

(Use this file as the single source of truth for what's been done and what's next.)

---

## 9. B-block trail (2026-09-01..2026-09-03)

Phases 1–3 + 4.1–4.4 shipped in a 2-day push. The B-block IDs are the commit-numbered prefixes in `git log --oneline`. AGENTS.md has the per-block design notes (rationale, contracts, follow-ups). The B-check scripts in `scripts/check_bN.sh` pin each block's contracts so a future refactor breaks loudly.

| Block | Phase | What | Commit |
|---|---|---|---|
| B200 | 1.1–1.4, 2.3–2.4, 3.6 | Initial `skygate cluster invite` + `join` + `/admin/database` + `/admin/cluster` + `skygate-migrate` CLI + `failover-drill` | pre-B215 |
| B203 | 1.8, 3.1 | `internal/watchdog.DBSwap` + `db.ResettableDB` (DSN hot-reload, no skygate restart) | pre-B215 |
| B204 | 3.2, 3.4 | `/admin/ha` + skygate HA elector (cluster_node roles + state) | pre-B215 |
| B206 | 1.5 | `/db/health` endpoint + background sampler | pre-B215 |
| **B215** | 2.6, 4.1 | `cluster_audit` table (8 actions) + `/admin/ha` filter (4→8 actions) + 4 emit sites | pre-B221 |
| **B216** | 2.1 | `/admin/cluster` enrichment (online count, replicas/DSN, B215 action badges) | pre-B221 |
| **B217** | 2.2.3, 2.2.4, 3.4 | `cluster.ApproveNode` / `DrainNode` / `DrainAndRemoveNode` + `/admin/cluster` action surface + state-conditional UI | pre-B221 |
| **B218** | 2.2.1, 2.2.2, 2.3, 2.4, 2.5 | `skygate init --role=...` (4 role presets) + standby detection in `init` + B215 cluster_audit `node_init` still fires | pre-B221 |
| **B219** | 3.3, 3.5 | Patroni `/switchover` plumbing (`PostAdminDatabaseFailover` + `db.FailoverDB` + `SKYGATE_PATRONI_URL`) + `db.failover` / `db.failover.error` audit actions | `c3b87a3` |
| **B220** | 3.7 | Patroni rollback operator button + `db.last_failover` state (one-click rollback via `PostAdminDatabaseFailoverRollback`) | `a723f21` |
| **B221** | 1.6, 3.5, 4.1 | V067 migration: `audit_log.target_type` + `target_id` + 6+ admin writers migrated to `AppendAuditLogWithTarget` (B221 audit surface uniform) | `fdf6a93` |
| **B222** | 4.2 | `internal/cluster/upgrade.go` + `skygate cluster upgrade --target=<h>|--all` (rolling-upgrade orchestrator: drain → wait for new build → rejoin) | `cee8e29` |
| **B223** | 4.3 | `internal/cluster/discovery.go` + `runDiscoveryTicker` (5-min default) + `POST /admin/cluster/discover` + `SKYGATE_DISCOVERY_TAG` env var (opt-in filter) | `4cedaec` |
| **B224** | — (stabilization, off-plan) | Migrate captured `*sql.DB` → `db.DBSource` (ResettableDB) in 4 services (App, backup, monitor, node-discovery). Closes the B203 "sql: database is closed" cascade + the B214 silent login audit drop. | `3e1e2ea` |
| **B225** | 4.4 | `/admin/database` Telegram alerts: Patroni /switchover ✅/❌ + rollback ✅/❌ + backup config-load-fail + RunBackup-fail (4 call sites) | `6e0ef67` |
| **B225.1** | 4.4 | `/db/health` transition alert (ok→degraded ❌, degraded→ok ✅) on the B206 sampler | `4cedaec4` |
| **B225.2** | 4.4 | B203 watchdog PG unreachable alert (3 consecutive `cluster_database` read failures → ❌, recovery → ✅) | `16404b5d` |

### What 4.4 actually delivers (when a Telegram bot is configured)

- `✅ PG failover OK` / `❌ PG failover FAILED` (B225)
- `✅ PG rollback OK` / `❌ PG rollback FAILED` (B225)
- `❌ backup: scheduler config load failed` / `❌ backup: scheduled run FAILED` (B225)
- `❌ DB health DEGRADED` / `✅ DB health recovered` (B225.1, B206 sampler)
- `❌ PG health DEGRADED` / `✅ PG health recovered` (B225.2, B203 watchdog)

When no bot is configured (the agent today), all five paths use the local `*NoopAlertSink` (or equivalent) and the alert is silently dropped — the `audit_log` row is the durable proof.

### Phase 4.5 (deferred per the plan)

- [ ] **4.5** Prometheus exporter — `/metrics` endpoint in Prometheus text format. The B206 `/db/health` JSON + the B217 /admin/cluster topology + the B204 HA chain state are all the inputs. The work is one handler (textfmt encoder) + a few `prometheus.NewGaugeVec` declarations. Estimated ~200 lines + tests.

