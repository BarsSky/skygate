# Skygate HA Architecture — Tier 1 (hot standby)

**Status**: design-only. Not yet implemented.
**Last updated**: 2026-07-30
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
| **1** | Active-passive, PG streaming replication, Patroni auto-failover | 0 | <1 min | ~$0.50/mo (S3) | this file + `v0.27.0-postgres-ha.md` |

Anything beyond Tier 1 (Tier 2: active-active, multi-region,
CDN in front) is out of scope for the current operator
deployment (household tailnet, ~4-20 users).

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

- One VM (`192.0.2.1`, public `95.165.170.190`)
- SQLite for both skygate.db and headscale.db
- `deploy/backup.sh` runs daily (configurable)
- DR: 15-30 min, documented in `disaster-recovery.md`
- RPO: 1 hour (cron frequency)
- **RTO**: 15 min best case (rebuild from backup), 60-90 min worst
  case (re-provision from scratch + slow DNS)

---

## Tier 1 — target (NOT YET IMPLEMENTED, blocked on operator's 2nd VM + S3 + etcd)

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
**Blocked on**:
- skygate-host-2 VM provisioning (operator)
- S3 bucket for WAL archive (operator)
- etcd quorum decision (1 node = no quorum, 3 nodes = need
  3rd VM; 2 nodes doesn't work for etcd)

The full implementation plan is
[`docs/v0.27.0-postgres-ha.md`](v0.27.0-postgres-ha.md). This
file is the executive summary; that one is the 18-day
step-by-step.
