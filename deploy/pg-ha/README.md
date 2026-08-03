# Skygate PG HA — Architecture A deploy artifacts (v0.32.21)

**Status**: Phase 1 (deploy artifacts). NOT YET DEPLOYED.
**Target version**: v0.33.0 (PG cutover) + v0.34.0 (HA active)
**Last updated**: 2026-08-03
**See also**:
[`docs/ha-active-router.md`](../../docs/ha-active-router.md) (the
design proposal that picks Architecture A);
[`docs/v0.33.0-pg-cutover-runbook.md`](../../docs/v0.33.0-pg-cutover-runbook.md)
(the cutover sequence);
[`docs/v0.27.0-postgres-ha.md`](../../docs/v0.27.0-postgres-ha.md) (the
detailed PG HA plan, 18 days).

---

## What this directory contains

Deployment artifacts for **Architecture A** (PG + Patroni
auto-failover). Two VMs:

- `skygate-host-1` (192.0.2.1) — PG primary + Patroni + skygate
- `skygate-host-2` (198.51.100.1, DNS: `skygatev2.example.com`) — PG
  replica + Patroni + skygate (read-only when primary is up)

Plus optional 3rd node for MinIO (self-hosted S3-compatible
storage for WAL archive). Per the operator's choice
(2026-08-03), MinIO is self-hosted, single-node, on **skygate-host-1**
(no separate VM needed; HA is for the WAL archive, which
re-builds from pg_dump + nightly S3 sync on failure).

**etcd topology**: single-node on **skygate-host-2** (passive
role). Rationale: when skygate-host-1 dies, the etcd on
skygate-host-2 is still up, so the Patroni replica on skygate-host-2
can elect itself as new primary. The trade-off: when
skygate-host-2 dies, etcd dies too, but skygate-host-1 (already
primary) doesn't need to re-elect. Single-node etcd = 1/1
quorum, which is the minimum Patroni accepts.

**DNS TTL**: 5 min (default). RTO is bounded by the DNS TTL
because clients cache the A record for skygate.example.com.

## Subdirectories

- `etcd/` — single-node etcd docker-compose (deploy on skygate-host-2)
- `minio/` — MinIO docker-compose (deploy on skygate-host-1, self-hosted S3)
- `patroni.yml` — Patroni config (used by both primary + replica)
- `haproxy.cfg` — HAProxy config (pg-aware routing on both VMs)
- `init-pg-primary.sh` — bootstrap primary VM (run ONCE on skygate-host-1)
- `init-pg-replica.sh` — bootstrap replica VM (run ONCE on skygate-host-2)
- `check_pg_health.sh` — health check script (run on both VMs, cron-friendly)
- `wal-g.env.example` — environment template for wal-g
- `docs/runbooks/pg-failover.md` — manual + auto failover procedure
  (in the docs/ tree, not here)

## Quick start (operator)

```bash
# 0. Prerequisites
#    - skygate-host-1 and skygate-host-2 both running Ubuntu 26.04 + Docker
#    - Passwordless SSH between them (for Patroni callbacks)
#    - skygate-host-1 has a hostname resolvable from skygate-host-2 and vice versa

# 1. Deploy MinIO on skygate-host-1 (self-hosted S3)
ssh admin@192.0.2.1
cd /home/admin/skygate/deploy/pg-ha/minio
docker compose up -d
# Verify: http://localhost:9001 (MinIO console, admin: <see .env>)

# 2. Create the WAL archive bucket
mc alias set local http://localhost:9000 <MINIO_ROOT_USER> <MINIO_ROOT_PASSWORD>
mc mb local/skygate-wal-archive
mc anonymous set download local/skygate-wal-archive
# (read-only anonymous for the replica to fetch WAL)

# 3. Deploy etcd on skygate-host-2 (single-node)
ssh root@skygate-host-2
cd /home/admin/skygate/deploy/pg-ha/etcd
docker compose up -d
# Verify: docker exec etcd etcdctl endpoint health

# 4. Bootstrap PG primary on skygate-host-1
ssh admin@192.0.2.1
cd /home/admin/skygate/deploy/pg-ha
# Edit .env: SKYGATE_PG_NODE_NAME=skygate-host-1, SKYGATE_PG_NODE_IP=192.0.2.1, etc.
# Edit patroni.yml: same fields
sudo bash init-pg-primary.sh
# This starts Patroni + HAProxy + wal-g. Patroni initializes PG.

# 5. Bootstrap PG replica on skygate-host-2
ssh root@skygate-host-2
cd /home/admin/skygate/deploy/pg-ha
# Edit .env: SKYGATE_PG_NODE_NAME=skygate-host-2, SKYGATE_PG_NODE_IP=198.51.100.1
sudo bash init-pg-replica.sh
# This basebackup from primary, starts streaming replication.

# 6. Verify
ssh admin@192.0.2.1
bash check_pg_health.sh
# Expected output:
#   primary: skygate-host-1 (192.0.2.1:5000)
#   replica: skygate-host-2 (198.51.100.1:5000)
#   replication lag: 0.5s
#   wal-g last archive: 2026-08-03 12:00:00
#   etcd: healthy (1/1)

# 7. (Only after the PG cutover: v0.33.0) Switch skygate from
#    SQLite to PG:
#    Edit /home/admin/skygate/.env: SKYGATE_DB_DSN=postgres://...
#    docker compose up -d --force-recreate skygate
#    Verify: curl http://localhost:8080/healthz | grep postgres
```

## Failover procedure (operator)

See `docs/runbooks/pg-failover.md` (in the repo root's docs
tree). Both auto-failover (Patroni) and manual-failover
(force-promote the replica) are documented.

## Status

| Phase | What | Status |
|---|---|---|
| 1.1 | deploy/pg-ha/ structure | ✅ done (v0.32.21) |
| 1.2 | patroni.yml + haproxy.cfg | ✅ done (v0.32.21) |
| 1.3 | MinIO + wal-g env | ✅ done (v0.32.21) |
| 1.4 | etcd single-node | ✅ done (v0.32.21) |
| 1.5 | check_pg_health.sh | ✅ done (v0.32.21) |
| 1.6 | pg-failover.md runbook | ✅ done (v0.32.21) |
| 2.1 | rewrite placeholders `?` → `$N` | ⏳ pending (Phase 2) |
| 2.2 | INSERT OR REPLACE → ON CONFLICT | ⏳ pending (Phase 2) |
| 2.3 | strftime → EXTRACT | ⏳ pending (Phase 2) |
| 2.4 | R27 verify on PG-staging | ⏳ pending (PG-staging) |
| 2.5 | 15-min cutover window | ⏳ pending (operator) |
| 3.x | HA setup on skygate-host-2 | ⏳ pending (skygate-host-2) |
| 4.x | skygate-side HA | ⏳ pending (Phase 3) |
