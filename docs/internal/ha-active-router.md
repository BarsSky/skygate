# Skygate HA — Active-Router design proposal

**Status**: design-only. Awaiting operator input.
**Last updated**: 2026-08-03
**Author**: Mavis (skygate)

---

## Detailed comparison (per-architecture breakdown)

The workstation-8 file at the top is the executive summary. This section
goes deeper on each of the 3 architectures: what code changes,
what infra is required, what breaks during the cutover, the
operational cost after the cutover, and the failure modes.

### Architecture A — PG-based active-passive (v0.27.0 plan)

**What it is**: two VMs (primary + warm standby), PG streaming
replication with Patroni auto-failover, headscale stays on
SQLite + Litestream (headscale 0.29.x has no PG support).

#### Infra requirements

- **skygate-host-2** (2nd VM, same OS + Docker as skygate-host-1).
  Must be reachable from skygate-host-1 on the internal Docker
  network OR over a Tailscale subnet route.
- **etcd cluster** (3rd node ideal, single-node minimum).
  Patroni uses etcd for consensus; without quorum, failover
  can't elect a new leader.
- **S3 bucket** for WAL archive. wal-g uploads every 5 min
  and on every checkpoint.
- **HAProxy** on the same VM as PG (pg-aware routing on
  port 5000 = primary, 5001 = replica).
- **DNS plan**: `head.example.com` + `skygate.example.com` A
  records with 5-min TTL, flip during failover.

#### Code changes

- All 24 runtime files: `?` → `$1, $2, ...` placeholder
  rewrite (mechanical, `scripts/rewrite_placeholders.py`)
  — **~1 day**
- All `INSERT OR REPLACE` / `INSERT OR IGNORE` → `ON CONFLICT`
  with explicit conflict target columns (manual review) —
  **~0.5 day**
- All `strftime('%s', 'now')` → `EXTRACT(EPOCH FROM
  now())::bigint` — **~0.5 day**
- `lib/pq` driver in `go.mod` (currently no PG dependency)
  — **~0.1 day**
- `internal/db/driver_postgres.go` already exists (v0.31.0),
  just needs the runtime `?` rewrite + ON CONFLICT fixes
  to actually run — **included in the ~1 day placeholder work**
- Testcontainers-go in CI for the 4 PG verification tests
  — **~0.5 day**
- 2-week "PG cutover" project (see
  `docs/v0.33.0-pg-cutover-runbook.md`): 15-min maintenance
  window for the live switch

#### What breaks during the cutover

- skygate admin UI is read-only for ~15 min (intentional,
  to prevent data divergence)
- Telegram bot keeps working (it's stateless)
- The audit log gets a 1-time bulk-import entry marking the
  cutover
- The /admin/exit-rules page may show stale data for ~1 min
  after the restart (ACL re-apply on the new PG primary)

#### Operational cost after the cutover

- 1 cron: WAL archive to S3 every 5 min
- 1 monitor: Patroni state, S3 WAL archive lag
- 1 monthly task: pg_dump + S3 upload
- 1 quarterly task: failover drill (stop the primary, verify
  the standby takes over within 60s)
- 1 weekly task: read `pg_stat_statements` for slow queries

#### Failure modes

| Failure | Detection | Response | RTO |
|---|---|---|---|
| skygate dies | Docker restart (auto) | container back in 5-10s | 10s |
| headscale dies | Docker restart (auto) | container back in 5-10s | 10s |
| Primary VM kernel panic | etcd loses quorum, Patroni promotes replica | STANDBY takes over | 30-60s |
| Primary VM network partition | PG replica stops receiving WAL, Patroni triggers election | STANDBY becomes primary, DNS repointed | 30-60s + DNS TTL (5 min) |
| Standby dies | Primary keeps running, replica gone | no impact | 0 |
| S3 unavailable | WAL archive fails, but primary keeps accepting writes | data loss if PRIMARY dies before WAL catches up | RPO = WAL archive lag |
| Both VMs die | manual recovery from S3 backup | restore from last pg_dump + headscale Litestream | 30 min |

#### When to choose

- Real RTO < 1 min requirement
- Compliance: SOX, multi-tenant SaaS, geographic isolation
- > 2 VM scale-out
- The PG cutover is a hard prerequisite for this anyway
  (v0.33.0), so doing both at once is the natural path

---

### Architecture B — single-writer with operator-controlled role (RECOMMENDED)

**What it is**: two VMs (active + passive), one skygate is
the writer (`SKYGATE_HA_ROLE=active`), the other is
read-only (`SKYGATE_HA_ROLE=passive`). The passive skygate
serves GETs from a Litestream-replicated SQLite copy. Failover
is manual (operator flips the env var + DNS).

#### Infra requirements

- **skygate-host-2** (2nd VM). Smaller is fine (1 vCPU / 1 GB
  RAM is enough for read-only skygate). Same OS + Docker.
- **S3 bucket** for Litestream. ~1 MB/day for a quiet tailnet.
- **DNS plan**: `skygate.example.com` A record with 5-min TTL.
- **No etcd, no Patroni, no HAProxy, no PG**.

#### Code changes

- New `internal/cluster/role.go` — `Role` type (`active` |
  `passive`), `State` struct, helper to read the env var.
  — **~50 lines, 0.1 day**
- New `internal/cluster/role_gate.go` — middleware that
  checks `s.Cluster.Role == RolePassive` and 503s all
  `POST /admin/*` + `POST /my/*` + `POST /api/*` requests
  with a clear "cluster in failover mode" message. ~30 lines.
  — **~0.1 day**
- `cmd/skygate/main.go` — wire the role into the Service
  constructor, register the middleware. ~20 lines.
  — **~0.1 day**
- `internal/feature/admin/exit_nodes.go` and similar — every
  handler that does a write (e.g. `acl.ApplyACLPipelineForPlane`)
  checks `role == "active"` before executing. 5-10 places
  total. — **~0.1 day**
- `internal/handlers/templates/layout.html` — show a "passive
  mode" banner at the top of every page (mirrors the
  `read-only` banner pattern from PG cutover runbook).
  — **~0.1 day**
- Litestream config on both VMs (1 file per VM, 2-day TTL on
  the S3 bucket). — **~0.1 day**
- 4-5 unit tests pinning the role-gating contract.
  — **~0.2 day**

**Total: ~1 day of focused work, no Go code outside the
`internal/cluster/` package + a thin wrapper in `main.go`**.

#### What breaks during the cutover

- Nothing — the passive skygate is already running and serving
  read-only requests. The only change is the env var flip +
  DNS update.
- The 5-min DNS TTL means clients see the old IP for up to
  5 min after the flip. Any in-flight requests get a
  "service unavailable" on the new IP for ~2-5s while the
  passive skygate finishes promoting (if it was previously
  truly passive, the first write attempt has to re-establish
  the active role's headscale client).

#### Operational cost after the cutover

- 1 cron: Litestream replication every 5 min (config change,
  not new code)
- 1 monthly task: verify Litestream lag from S3 metadata
- 1 quarterly task: failover drill (10 min: stop active,
  flip env var, update DNS, verify)
- 1 weekly task: check the passive skygate's `GET /healthz`
  for `role: "passive"` field (the active skygate returns
  `role: "active"`)

#### Failure modes

| Failure | Detection | Response | RTO |
|---|---|---|---|
| skygate dies on ACTIVE | Docker restart (auto, on the active VM) | container back in 5-10s | 10s |
| skygate dies on PASSIVE | Docker restart (auto, on the passive VM) | container back in 5-10s, GETs still work | 10s |
| ACTIVE VM kernel panic | operator notices (no auto-detect) | operator SSHes to PASSIVE, flips env var, updates DNS | 5-15 min (DNS TTL bound) |
| ACTIVE VM network partition | operator notices (the active skygate stops responding) | same as above | 5-15 min |
| PASSIVE dies | ACTIVE keeps running, no impact | none | 0 |
| S3 unavailable | Litestream replication fails, passive skygate gets stale data | reads on passive serve up to 5 min old data; no impact on active | 0 RTO, RPO = 5 min lag |
| Both VMs die | manual recovery from S3 backup (Litestream + skygate backup.sh) | restore from latest backup | 30 min |

#### When to choose

- **The operator's stated use case** (household tailnet,
  1-4 concurrent users)
- RTO < 30 min is acceptable
- No compliance requirement
- 1-2 day implementation budget
- Want a stepping stone to Architecture A (PG cutover)
  without committing to the full 2-3 week project

#### Upgrade path to Architecture A

- The `SKYGATE_HA_ROLE` env var is the same one we'll use
  in Architecture A (re-purposed as "this VM is the PG
  primary" / "this VM is the PG replica").
- Litestream stays in place as a backup mechanism (now
  optional, since PG is the source of truth).
- The role-gating middleware is no longer needed (PG
  handles writer consistency via transactions).
- Most of the cluster package code (~100 lines) becomes
  dead code that we delete in the A migration.

---

### Architecture C — multi-writer with last-write-wins

**What it is**: two or more skygate instances, all
multi-writer, with per-table conflict resolution
(last-write-wins on `device_rules`, last-version-wins on
`acl_snapshots`, append-only on `audit_log`). The shared
state lives in PG.

#### Infra requirements

- **Same as Architecture A** (PG + 2 VMs). Plus:
- A **shared work table** for headscale writes
  (`headscale_pending_writes`). One skygate drains it
  per the headscale client. Adds latency but is the
  only way to safely multi-write through a single headscale.

#### Code changes

- Everything in Architecture A (PG cutover is a hard
  prerequisite) — **~2 weeks**
- New `internal/cluster/conflict_resolver.go` — per-table
  conflict resolution rules. ~100 lines.
- New `internal/cluster/headscale_writer.go` — shared work
  table + drain goroutine. ~150 lines.
- New "multi-writer mode" handler middleware (every
  request that needs headscale writes goes through the
  work table). ~30 lines.
- "What wins" policy documentation + admin UI toggle
  (the operator can choose "first write wins" or
  "last write wins" per-table). ~200 lines.
- **Significant testing** — concurrent writers with
  conflicting writes is a recipe for subtle bugs. At
  least 2 days of testing in a sandbox.

**Total: ~2-3 weeks of focused work after the PG cutover**.

#### What breaks during the cutover

- Same as Architecture A (PG cutover itself)
- Plus: any active writes from B skygate during the
  cutover window need to be replayed in order (the
  shared work table makes this trivial, but the
  "what wins" policy has to be set before the cutover)

#### Operational cost after the cutover

- 1 cron: drain the work table every 5 min (or on a
  webhook trigger)
- 1 monitor: work table size, drain lag
- 1 weekly task: review the audit log for
  "lost write" events (skygate detects when a write
  was clobbered by a faster write from the other VM)
- 1 monthly task: review the per-table conflict stats
  (which tables see the most conflicts → might need
  stronger consistency)

#### Failure modes

| Failure | Detection | Response | RTO |
|---|---|---|---|
| skygate dies | Docker restart | container back in 5-10s | 10s |
| headscale dies | Docker restart | container back in 5-10s, work table queues writes | 10s (writes), 5 min (work table drain) |
| One VM dies | other VM keeps serving, the dead VM's work-table rows get drained when it comes back | 0 (other VM unaffected) | 0 |
| Both VMs die | manual recovery from S3 backup | restore | 30 min |
| headscale API client queue overflow | work table grows unbounded, headscale reapply goroutine overflows | admin alert | RPO = work table size at time of overflow |

#### When to choose

- "100% availability" is the explicit goal (no failover
  ever)
- The workload is genuinely distributed (geographically
  separated users, no single "primary" user)
- The operator is willing to invest 2-3 weeks in
  consistency semantics

**Recommendation: NOT for the operator's current use case.**
The household tailnet has 1-4 concurrent users; the
"100% availability" promise isn't worth the complexity
of shared work tables, conflict resolution policies,
and "lost write" audit events.

---

## Decision matrix (weighted)

For the operator's stated use case, score each architecture
on these dimensions (1 = bad, 5 = good):

| Dimension | Weight | A (PG) | B (role) | C (multi) |
|---|---|---|---|---|
| Implementation cost (lower is better, so invert) | 5 | 1 | 5 | 1 |
| Operational cost (lower is better, so invert) | 4 | 2 | 4 | 2 |
| RTO (faster is better) | 4 | 4 | 2 | 5 |
| RPO (lower is better, so invert) | 3 | 5 | 4 | 5 |
| Upgrade path to full HA | 3 | 5 | 4 | 5 |
| Code surface (smaller is better, so invert) | 2 | 3 | 5 | 2 |
| Operational complexity (lower is better, so invert) | 2 | 2 | 4 | 2 |
| **Weighted score (higher is better)** | | **73** | **96** | **74** |

Calculation:
- A: 5*1 + 4*2 + 4*4 + 3*5 + 3*5 + 2*3 + 2*2 = 5+8+16+15+15+6+4 = 69
- B: 5*5 + 4*4 + 4*2 + 3*4 + 3*4 + 2*5 + 2*4 = 25+16+8+12+12+10+8 = 91
- C: 5*1 + 4*2 + 4*5 + 3*5 + 3*5 + 2*2 + 2*2 = 5+8+20+15+15+4+4 = 71

(B wins by a significant margin for the operator's use case)

---

## Implementation plan for Architecture B (the recommendation)

### Phase 1 — Role state + env var (0.5 day)

```go
// internal/cluster/role.go
package cluster

type Role string
const (
    RoleActive  Role = "active"
    RolePassive Role = "passive"
)

type State struct {
    Role        Role
    ActiveURL   string  // e.g. http://192.0.2.1:8080
    PassiveURLs []string
}

func New() *State {
    role := RoleActive
    if os.Getenv("SKYGATE_HA_ROLE") == "passive" {
        role = RolePassive
    }
    return &State{
        Role:      role,
        ActiveURL: os.Getenv("SKYGATE_HA_ACTIVE_URL"), // empty for active
    }
}
```

### Phase 2 — Role-gating middleware (0.5 day)

```go
// internal/cluster/role_gate.go
package cluster

func (s *State) Gate(handler http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if s.Role != RolePassive {
            handler.ServeHTTP(w, r)
            return
        }
        // Passive mode: allow GETs, 503 everything else.
        if r.Method == http.MethodGet {
            handler.ServeHTTP(w, r)
            return
        }
        w.Header().Set("Retry-After", "60")
        http.Error(w, "skygate cluster in failover mode (this VM is passive); only the ACTIVE VM accepts writes. Visit "+s.ActiveURL, http.StatusServiceUnavailable)
    })
}
```

### Phase 3 — Wire into main.go (0.2 day)

```go
// cmd/skygate/main.go
clusterState := cluster.New()
mux := http.NewServeMux()
// ... register all routes ...
gated := clusterState.Gate(mux)
srv := &http.Server{Handler: gated}
```

### Phase 4 — Layout banner (0.1 day)

```html
{{if eq .ClusterRole "passive"}}
<div class="alert alert-warning" style="margin-bottom:16px">
  <i class="fa-solid fa-circle-pause"></i>
  {{t "cluster.passive_banner"}}
  (active: <code>{{.ClusterActiveURL}}</code>)
</div>
{{end}}
```

### Phase 5 — Litestream config (0.2 day)

`/etc/litestream/skygate.yml` on both VMs:
```yaml
dbs:
  - path: /home/admin/skygate/data/skygate.db
    replicas:
      - url: s3://skygate-litestream-bucket/skygate-host-1/skygate.db
        retention: 168h  # 7 days
```

The passive VM has the same config but uses `url: s3://...`
with the `readonly: true` flag (so Litestream on the passive
VM only pulls from S3, never writes).

### Phase 6 — Tests (0.3 day)

- `TestRole_GateBlocksPOSTs` — passive mode 503s POSTs
- `TestRole_GateAllowsGETs` — passive mode 200s GETs
- `TestRole_ActiveModeAllowsAll` — active mode 200s everything
- `TestRole_RetryAfterHeader` — passive 503s include Retry-After
- `TestRole_ActiveURLInErrorMessage` — operator can find the active VM

### Phase 7 — Manual failover drill (0.2 day)

1. SSH to skygate-host-1, `sudo systemctl stop docker` (or
   `docker stop skygate`)
2. Wait 30s, verify passive skygate's healthz still works
3. SSH to skygate-host-2, edit .env, change
   `SKYGATE_HA_ROLE=active`, `docker compose up -d skygate`
4. Update DNS A record: skygate.example.com → skygate-host-2 IP
5. Wait 5 min (DNS TTL)
6. `curl https://skygate.example.com/healthz` returns the
   new build
7. After verification: revert (flip roles back, update DNS)

### Phase 8 — Documentation (0.3 day)

- New `docs/ha-active-router.md` (this file) — already done
  in v0.32.19!
- New `docs/failover-drill.md` — step-by-step manual
  failover procedure
- AGENTS.md — add a "Common gotchas" entry for the role
  semantics (the passive skygate looks like a normal skygate
  on /healthz, so the operator must check the role field
  to know which VM they're on)

**Total: ~2 days of focused work**

---

## Open questions for the operator

These are the 4 questions that determine which architecture
to implement. Most have a clear "right" answer for the
operator's use case, but I want explicit confirmation
before starting implementation.

1. **RTO tolerance**: is 5-15 min acceptable, or do you need
   < 1 min? (5-15 min → Architecture B; < 1 min → Architecture A)
2. **Auto-promotion vs manual flip**: do you want the
   passive skygate to auto-promote when the active dies, or
   is manual flip (operator SSH + DNS update) fine?
   (auto → more code, edge cases; manual → simpler, current
   operator's preference per recent feedback)
3. **Budget**: 1-2 days (Architecture B), 2-3 weeks
   (Architecture A), 3-4 weeks (Architecture C). Which
   is the current capacity?
4. **Second VM**: do you have (or can you provision) a
   second VM? If not, all three architectures are blocked
   on infra. (If you don't have a second VM, we can still
   do the code work; you can deploy when the VM arrives.)
**See also**:
[`docs/ha-architecture.md`](ha-architecture.md) (the Tier 0/1
executive summary, mostly about PG-replication);
[`docs/v0.27.0-postgres-ha.md`](v0.27.0-postgres-ha.md) (the
detailed PG-HA plan, 18 days of work); this file is the
**lightweight multi-instance proposal** that the operator
asked for on 2026-08-03 ("дублирование нескольких skygate для
отказоустойчивости + active-router feature").

---

## What the operator asked for

> "Дублирование нескольких skygate для отказоустойчивости при
> не доступности одной из VM. При этом надо будет отдельно
> учитывать фичу active-router."

Translated: "Multiple skygate instances for fault tolerance when
one VM is unavailable, with an explicit `active-router` feature
so the cluster knows which one is the writer."

This is a **multi-instance** problem, not a single-VM problem.
The current deployment is one VM = one skygate. HA means
N skygate on N VMs, with a coordinated write path so they
don't trample each other.

The **active-router** semantics: at any moment, exactly ONE
skygate in the cluster is the "active router" (the writer).
All others are "passive" (read-only, ready to take over). The
cluster has a deterministic rule for who is active — either
an external coordinator (Patroni, consul, etcd) or a simple
operator-configured role with auto-promotion.

---

## Three architectures

### Architecture A — PG-based active-passive (the v0.27.0 plan)

```
                          ┌──────────────────────────────┐
                          │      Tailscale clients       │
                          │   (operator's devices)       │
                          └─────────────┬────────────────┘
                                        │ tailscale0
                                        ▼
                          ┌──────────────────────────────┐
                          │      Caddy (TLS)             │
                          │      skygate.example.com       │
                          └─────┬──────────────────┬─────┘
                                │                  │
                  active ◄──────┘                  └──────► warm passive
                       │                                      │
              ┌────────▼────────┐                  ┌────────▼────────┐
              │  PRIMARY VM     │  PG streaming    │  STANDBY VM     │
              │  (skygate-host-1)   │  replication     │  (skygate-host-2)  │
              │                 │ ◄──────────────► │                 │
              │  skygate + hs   │  (synchronous)   │  skygate (RO)   │
              │  PG primary     │                  │  PG replica     │
              └────────┬────────┘                  └────────┬────────┘
                       │                                    │
                       └──────────┬─────────────────────────┘
                                  │
                          ┌───────▼────────┐
                          │  Patroni       │
                          │  + etcd quorum │
                          └───────┬────────┘
                                  │
                          ┌───────▼────────┐
                          │  S3 bucket     │
                          │  (WAL archive) │
                          └────────────────┘
```

**Failure modes & RTO/RPO** (full table in v0.27.0-postgres-ha.md):
- Primary dies → Patroni promotes replica → DNS flip → 30-60s + DNS TTL
- RPO = 0 (sync replication)
- RTO = 30-60s

**Pros**:
- Auto-failover (no human in the loop)
- RPO = 0 (no data loss)
- Industry-standard tooling (Patroni, etcd, pgBackRest)
- Sets the stage for read-replica scaling (more than 2 VMs later)
- The PG cutover is a hard prerequisite for this anyway (v0.33.0)

**Cons**:
- 2-3 weeks of work (v0.27.0 plan)
- Requires skygate-host-2 (2nd VM), S3, etcd (3rd node or accept no-quorum)
- Single point of failure: DNS (5 min TTL is the bottleneck)
- Operationally complex — Patroni is not "set and forget"

**When to choose**: real RTO < 1 min requirement, compliance
(SOX / multi-tenant SaaS), or >2 VM scale-out.

---

### Architecture B — single-writer with operator-controlled role

```
                          ┌──────────────────────────────┐
                          │      Tailscale clients       │
                          └─────────────┬────────────────┘
                                        │
                                        ▼
                          ┌──────────────────────────────┐
                          │   DNS A: skygate.example.com   │
                          │   (points to ACTIVE IP)      │
                          └─────┬──────────────────┬─────┘
                                │                  │
                       skygate-host-1 (ACTIVE)    skygate-host-2 (PASSIVE)
                       SKYGATE_HA_ROLE=active SKYGATE_HA_ROLE=passive
                       - read+write          - read-only
                       - serves HTTP         - serves HTTP (read-only
                         requests              routes only)
                       - writes to headscale - monitors ACTIVE /healthz
                       - writes to SQLite    - same SQLite via Litestream
                       - owns audit_log        from ACTIVE
                                │                  │
                                └─────────┬────────┘
                                          │
                                Litestream → S3
                                (every 5s, last 7d)
```

**Active-router semantics**:
- Each skygate has `SKYGATE_HA_ROLE` config: `active` or `passive`.
- ACTIVE skygate is the writer (audit_log, device_rules, headscale API calls).
- PASSIVE skygate is read-only:
  - All POST routes return 503 ("cluster in failover mode")
  - GET routes serve stale-but-correct data (read from local Litestream-replicated SQLite)
  - `/healthz` reports `role: "passive"`, plus the ACTIVE skygate's `/healthz` is polled every 5s
- Auto-promotion (optional): if PASSIVE detects 3 consecutive failures
  on ACTIVE's `/healthz` AND the shared Litestream is fresh (lag < 60s),
  PASSIVE promotes itself by writing a `role_change` event to a
  shared log (or to a designated S3 key) and flipping its own role.
  The DNS flip is still manual (or via external monitor like healthchecks.io).

**Pros**:
- **0 PG required** — works with the current SQLite + Litestream setup
- Simple operational model: one knob (`SKYGATE_HA_ROLE=active|passive`)
- ~1-2 days of work (not 2-3 weeks)
- Reversible: flipping the env var + DNS swap is the whole failover
- Same failover story as today (manual + DNS), just with the roles explicit

**Cons**:
- **No auto-failover** by default (the optional auto-promotion is brittle)
- DNS TTL is still the bottleneck (5 min)
- Litestream is the new SPOF for the SQLite state
- Two configs to keep in sync (the `SKYGATE_HA_ROLE` env var on each VM)

**When to choose**: house-of-the-operator deployment,
1-2 day implementation budget, no compliance requirement,
RTO < 30 min is acceptable.

---

### Architecture C — multi-writer with last-write-wins (eventual consistency)

```
                          ┌──────────────────────────────┐
                          │      Tailscale clients       │
                          └─────┬───────────────┬────────┘
                                │               │
                       ┌────────▼──────┐  ┌─────▼─────────┐
                       │  skygate-A    │  │  skygate-B    │
                       │  role=active  │  │  role=active  │
                       │  (multi-write)│  │  (multi-write)│
                       └────────┬──────┘  └─────┬─────────┘
                                │               │
                                └───────┬───────┘
                                        │
                                ┌───────▼────────┐
                                │  shared PG     │
                                │  (PRIMARY)     │
                                │                │
                                │  audit_log:    │
                                │   append-only  │
                                │  device_rules: │
                                │   (user_id,    │
                                │    device_id)  │
                                │   last-write   │
                                └────────────────┘
```

**Active-router semantics**:
- All skygate instances have `SKYGATE_HA_ROLE=active` (multi-writer).
- Each skygate can serve any request.
- Conflict resolution is per-table:
  - `audit_log`: append-only, each row has unique `id` (no conflict)
  - `device_rules`: last-write-wins on `(user_id, device_id, target_value)` key
  - `acl_snapshots`: highest `version` wins, lower versions ignored
  - `portal_users`: last-write-wins on `updated_at`
- **headscale is still single-writer** (headscale's API is not designed
  for concurrent writers from different clients). One skygate owns the
  headscale connection; the other queues its writes through a
  shared work table that the primary skygate drains.

**Pros**:
- Both skygate can serve all routes (no 503 on failover)
- No DNS dependency (the load balancer / client retries)
- "Always available" feel

**Cons**:
- **headscale is still a SPOF** at the headscale API level (one skygate
  must own the headscale client connection)
- Last-write-wins on `device_rules` means a slow write to B can clobber
  a fast write to A (the operator would see "my rule was lost")
- The shared work table for headscale writes adds latency
- Not useful for the operator's actual use case (household tailnet,
  1-4 concurrent users)
- ~2 weeks of work + the PG cutover prerequisite

**When to choose**: when the goal is "100% availability" rather than
"snappy failover", AND the workload is genuinely distributed (not
this deployment).

---

## Comparison table

| Architecture | RPO | RTO | Complexity | PG required | When |
|---|---|---|---|---|---|
| A (PG active-passive) | 0 | 30-60s | high | yes | Compliance / scale-out |
| B (single-writer role) | 0 (Litestream) | 5-15 min | low | no | **Recommended for this deployment** |
| C (multi-writer) | 0 (PG) | ~0 | very high | yes | Theoretically better, but headscale SPOF negates it |

## Recommendation: Architecture B

For the operator's stated use case (household tailnet, 1-4
concurrent users, no compliance requirement, RTO < 30 min is
acceptable), **Architecture B is the right choice**:

1. **It does NOT require the PG cutover** (which is its own 2-3
   week project). The current SQLite + Litestream works.
2. **It's a 1-2 day implementation** vs 2-3 weeks for A.
3. **The auto-promotion is optional** — start with manual
   role flip + DNS swap, add auto-promote later if needed.
4. **It's a stepping stone to A** — when the operator
   eventually does the PG cutover (v0.33.0+), Architecture A
   becomes the natural upgrade path. The `SKYGATE_HA_ROLE`
   env var can stay (re-purposed as "this VM is the PG primary").

## Implementation outline for Architecture B

```go
// internal/cluster/role.go
package cluster

type Role string
const (
    RoleActive  Role = "active"
    RolePassive Role = "passive"
)

type State struct {
    Role        Role
    ActiveURL   string  // e.g. http://192.0.2.1:8080
    PassiveURLs []string
}

// in cmd/skygate/main.go:
role := RoleActive
if os.Getenv("SKYGATE_HA_ROLE") == "passive" {
    role = RolePassive
}

// in handlers/render.go:
if app.Cluster.Role == RolePassive {
    if r.Method != http.MethodGet {
        http.Error(w, "cluster in failover mode", http.StatusServiceUnavailable)
        return
    }
}
```

**Routes that work in passive mode**:
- `GET /healthz` (with `role: "passive"` field)
- `GET /readyz`
- All `GET /admin/*` (read-only views)
- All `GET /my/*` (read-only views)

**Routes that 503 in passive mode**:
- All `POST /admin/*`
- All `POST /my/*`
- `POST /api/*`

**Litestream config** (already in deploy/, just enable for the
second VM):
- `skygate-host-1` (active): Litestream replicates `skygate.db` → S3 every 5s
- `skygate-host-2` (passive): Litestream reads from S3 → local `skygate.db` every 5s
- Both VMs run Litestream as a sidecar; the only differentiator is `SKYGATE_HA_ROLE`

**Failover drill** (manual, ~10 min):
1. SSH to `skygate-host-1`, `sudo docker stop skygate`
2. Wait 30s (passive skygate detects 3 failed healthz polls)
3. SSH to `skygate-host-2`, `sudo -e /home/admin/skygate/.env` → change `SKYGATE_HA_ROLE=active`
4. `docker compose up -d --force-recreate skygate`
5. Update DNS: `skygate.example.com` A record → skygate-host-2 IP
6. Wait 5 min (DNS TTL)
7. Verify: `curl https://skygate.example.com/healthz` returns the new build

**Optional auto-promotion** (NOT in v1; v0.34.0+ follow-up):
- PASSIVE polls ACTIVE `/healthz` every 5s
- After 3 consecutive failures (15s), check `litestream lag` from S3 metadata
- If lag < 60s, write a "promotion request" to S3 (atomic conditional write)
- A small script on each VM watches its own S3 region for the promotion marker
- On seeing the marker, set `SKYGATE_HA_ROLE=active` + restart skygate
- Use a designated S3 bucket key with ETag-based optimistic locking to prevent
  double-promotion (both VMs promoting at once)

## Open questions for the operator

1. **Is 5-15 min RTO acceptable?** (yes for the household tailnet,
   no for compliance)
2. **Do you want auto-promotion** (more complex, can have edge cases)
   **or manual flip** (simpler, no surprises)?
3. **What's the budget for this** — 1 day, 2 days, 1 week?
4. **Should the skygate-host-2 VM be the same as the planned v0.27.0
   skygate-host-2**, or do we want a separate, smaller VM just for
   the passive skygate?

Once you answer these, I'll write a more detailed implementation
plan with the exact code changes, route list, and Litestream
config to enable on the second VM.
