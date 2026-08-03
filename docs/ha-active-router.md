# Skygate HA — Active-Router design proposal

**Status**: design-only. Awaiting operator input.
**Last updated**: 2026-08-03
**Author**: Mavis (skygate)
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
