# Skygate HA Architecture — Tier 1 (hot standby)

**Status**: Tier 1 (active-passive) **code-side implemented** as of v1.5.0 (B145–B153). Operator-side phases (Phase 0 Tailscale mesh, Phase 7 <polygon-vm-hostname> bootstrap, Phase 9 live DR drill) remain for v1.5.0 release per `docs/internal/ha-v1.5.0-execution.md`. Tier 2+ out of scope.
**Last updated**: 2026-09-08
**See also**: [`docs/v0.27.0-postgres-ha.md`](v0.27.0-postgres-ha.md)
(the full 18-day plan, including Phase 2 PG HA setup, Phase 3
VM migration, Phase 4 DR drills, Phase 5 DNS cutover). This
file is the short summary so `docs/disaster-recovery.md` can
link to a stable target.

---

## Tiers (in increasing order of cost + RTO guarantee)

| Tier | Topology | RPO | RTO | Cost | Doc |
|---|---|---|---|---|---|
| **0** | Single VM, daily backups | 1h | 15-30 min | $0 | [`disaster-recovery.md`](disaster-recovery.md) |
| **0.5** | Multi-instance, single-writer active-router, Litestream | 0 | 5-15 min | ~$0.10/mo (S3) | [`ha-active-router.md`](ha-active-router.md) (Architecture B) |
| **1** | Active-passive, PG streaming replication, Patroni auto-failover | 0 | <1 min | ~$0.50/mo (S3) | this file + `v0.27.0-postgres-ha.md` |

Anything beyond Tier 1 (Tier 2: active-active, multi-region,
CDN in front) is out of scope for the current operator
deployment (household tailnet, ~4-20 users).

### Tier 0.5 — the lightweight active-router (Architecture B)

**What it adds on top of Tier 0**: a second skygate on a
second VM, with explicit `SKYGATE_HA_ROLE` config. The
second instance is read-only (serves GETs from a
Litestream-replicated SQLite copy), the first instance is
the writer. On primary VM failure, the operator flips the
env var on the second VM and updates DNS — total RTO is
5-15 min (limited by DNS TTL).

**Why not just Tier 1?** The PG-based Tier 1 is the right
choice for compliance / scale-out / RTO < 1 min, but it
requires the v0.33.0 PG cutover (a separate 2-3 week
project) plus a 2nd VM + S3 + etcd. Tier 0.5 works on the
**current SQLite + Litestream** stack and ships in 1-2 days.

**Why not active-active (Tier 2)?** The headscale API is
not designed for concurrent writers from multiple skygate
clients. The shared work-table pattern (Architecture C in
[`ha-active-router.md`](ha-active-router.md)) adds
operational complexity without a practical benefit for a
household tailnet (1-4 concurrent users). The active-router
semantics (one writer at a time) is the right primitive for
this deployment.

**Recommended for this deployment today**. The PG cutover
(v0.33.0) is still on the roadmap — when it ships, the
`SKYGATE_HA_ROLE` env var can be re-purposed as
"this VM is the PG primary" and the upgrade to Tier 1 is
mostly the same code.

---

## Tier 0 — current state (works as of v0.32.0)

```
operator's devices ─── tailscale0 ──► skygate-host-1 (192.0.2.1)
                                      ├─ skygate (Go) + sidecar
                                      ├─ headscale (control plane)
                                      ├─ caddy (TLS)
                                      └─ Tailscale client (skygate-host-1 identity)

daily cron → /var/backups/skygate/latest/
            /var/backups/headscale/latest/
```

- One VM (`192.0.2.1`, public `<operator-public-ip>`)
- SQLite for both skygate.db and headscale.db
- `deploy/backup.sh` runs daily (configurable)
- DR: 15-30 min, documented in `disaster-recovery.md`
- RPO: 1 hour (cron frequency)
- **RTO**: 15 min best case (rebuild from backup), 60-90 min worst
  case (re-provision from scratch + slow DNS)

---

## Tier 1 — implemented (v1.5.0, B145–B153)

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
                          │      head.example.com          │
                          └─────┬──────────────────┬─────┘
                                │                  │
                  active ◄──────┘                  └──────► warm standby
                       │                                      │
              ┌────────▼────────┐                  ┌────────▼────────┐
              │  PRIMARY VM     │  sync streaming  │  STANDBY VM     │
              │  (skygate-host-1)   │  replication     │  (skygate-host-2)  │
              │                 │ ◄──────────────► │                 │
              │  skygate + hs   │  (synchronous)   │  skygate + hs   │
              │  PG primary     │                  │  PG replica     │
              │  Tailscale up   │                  │  Tailscale up   │
              └────────┬────────┘                  └────────┬────────┘
                       │                                    │
                       └──────────┬─────────────────────────┘
                                  │
                          ┌───────▼────────┐
                          │  Patroni       │
                          │  + etcd quorum │
                          │  (1-3 nodes)   │
                          └───────┬────────┘
                                  │
                          ┌───────▼────────┐
                          │  S3 bucket     │
                          │  WAL archive   │
                          │  (wal-g)       │
                          └────────────────┘
```

### Failure modes

| Failure | Detection | Response | RTO |
|---|---|---|---|
| skygate dies | Docker restart (auto) | container back in 5-10 sec | 10 sec |
| headscale dies | Docker restart (auto) | container back in 5-10 sec | 10 sec |
| PRIMARY VM kernel panic | etcd loses quorum, Patroni promotes replica | STANDBY takes over | 30-60 sec |
| PRIMARY VM network partition | PG replica stops receiving WAL, Patroni triggers election | STANDBY becomes primary, DNS repointed | 30-60 sec + DNS TTL (5 min) |
| STANDBY dies | PRIMARY keeps running, replica gone | no impact | 0 |
| S3 unavailable | WAL archive fails, but primary keeps accepting writes | data loss if PRIMARY dies before WAL catches up | RPO = WAL archive lag |
| Both VMs die | manual recovery from S3 backup | restore from last pg_dump + headscale Litestream | 30 min |

### Implementation phases (full plan in v0.27.0-postgres-ha.md)

| Phase | What | Est |
|---|---|---|
| 2.1 | Install PG 16 on both VMs | 0.5 day |
| 2.2 | Streaming replication setup | 0.5 day |
| 2.3 | WAL archiving to S3 (wal-g) | 0.5 day |
| 2.4 | Patroni + etcd cluster | 1 day |
| 2.5 | HAProxy pg-aware routing | 0.5 day |
| 2.6 | Phase 2 verification (failover drill) | 0.5 day |
| 3.x | VM data migration (tailscale state, headscale data, caddy) | 4 days |
| 4.x | DR drills (6 scenarios) | 0.5 day |
| 5 | DNS cutover (operator-driven) | 0.5 day |

**Total: ~2-3 weeks of focused work after the operator
provides: 2nd VM (skygate-host-2), S3 bucket, etcd quorum (3rd VM
or accept single-node).**

### Why not Tier 2 (active-active)?

Active-active is harder than active-passive (write conflicts
on `audit_log`, `personal_api_tokens` etc.) and provides no
practical benefit for a household tailnet. Tier 1's <1 min
RTO is "snappy enough" for the operator's stated use case.

---

## Tracking

This work is tracked as Priority 3 in
[`docs/BACKLOG.md`](BACKLOG.md#priority-3--ha-skygate-host-2--tier-1-hot-standby-blocked-on-2nd-vm--etcd-quorum--s3).
**Operator-side remaining (code-side DONE)**:
- Phase 0: Tailscale mesh between <polygon-vm-hostname> + skygate-host-1
  (subnet routes approved on headscale side)
- Phase 7: <polygon-vm-hostname> bootstrap (run `scripts/bootstrap_standby.sh`
  on the new VM; script provisions Patroni replica + headscale
  replica + skygate in standby role + certsync)
- Phase 9: live DR drill (operator picks a maintenance window
  and runs `scripts/dr_drill.sh`; Q9 in `ha-v1.5.0-execution.md` §4)

The full implementation tracker is
[`docs/internal/ha-v1.5.0-execution.md`](ha-v1.5.0-execution.md).
This file is the executive summary; that one is the per-tick
checklist with the 26 code-side ticks all marked [x] as of
2026-09-08 (after B145 / B146 / B148 / B149 / B150 / B151 / B152 /
B153 / B237.24 — the release.yml + Dockerfile + Go-1.25 fixes
that unblocked the v1.5.0 release pipeline).

Tier 0.5 (active-router with Litestream) and Tier 2
(active-active, multi-region) are not in scope.
