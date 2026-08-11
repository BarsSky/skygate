# PG failover runbook (Architecture A, v0.32.21)

**Status**: design + deploy artifacts. NOT YET EXECUTED.
**Last updated**: 2026-08-03
**When to use**: primary PG node is down, replica needs to
be promoted, or planned maintenance requires manual failover.

This runbook covers **two scenarios**:

1. **Auto-failover** (Patroni handles it) — primary dies
   unexpectedly, etcd is still up, replica on skygate-host-2
   promotes itself.
2. **Manual failover** (operator-driven) — planned maintenance
   on the primary, or auto-failover didn't trigger (etcd
   unreachable, single-replica config, etc).

---

## 1. Verify the cluster state

Before doing anything, check what Patroni thinks the state is.
Run `check_pg_health.sh` on the ALIVE node (or any node whose
Patroni is reachable):

```bash
ssh admin@192.0.2.1  # or skygate-host-2
cd /home/admin/skygate/deploy/pg-ha
bash check_pg_health.sh
```

Expected output (healthy):
```
Local node: skygate-host-1 (state: running)
...
Primary count: 1  (expect 1)
Replica count: 1  (expect >= 1)
Max replication lag: 0.5s
OK: cluster healthy
```

Exit codes:
- 0 = healthy
- 1 = degraded (lag > 10s, no failover needed but investigate)
- 2 = critical (no primary, no replicas, etcd down)

If exit 2, proceed to one of the sections below.

---

## 2. Auto-failover (Patroni promotes replica automatically)

**When this happens**: skygate-host-1 (primary) is dead/unreachable,
skygate-host-2 (replica) is alive, etcd on skygate-host-2 is alive.

**What Patroni does**: detects the leader lock expired after
`ttl=30s` (the configured TTL in `patroni.yml`), elects the
replica as new primary, updates the cluster state in etcd.

**Timeline**:
- t=0: skygate-host-1 dies
- t=30s: Patroni on skygate-host-2 notices leader lock expired
- t=30-40s: Patroni on skygate-host-2 promotes self to primary
- t=30-60s: skygate's HAProxy on skygate-host-1... wait, skygate-host-1
  is dead, so its HAProxy is also dead. **DNS is the key now.**

**What the operator does**:
1. Verify the new primary is on skygate-host-2:
   ```bash
   ssh root@skygate-host-2
   cd /home/admin/skygate/deploy/pg-ha
   bash check_pg_health.sh
   # Expected: "Local node: skygate-host-2 (state: running)"
   ```
2. Update the DNS A record for `skygate.example.com` →
   skygate-host-2's public IP (198.51.100.1). Wait 5 min for
   DNS TTL to expire.
3. Verify:
   ```bash
   curl -fsS http://localhost:8080/healthz
   # Expected: 200 with "db_backend": "postgres"
   ```
4. Once skygate-host-1 is back, re-init the old primary as a
   new replica:
   ```bash
   ssh admin@192.0.2.1
   cd /home/admin/skygate/deploy/pg-ha
   # Set SKYGATE_PG_NODE_NAME=skygate-host-1, SKYGATE_PG_NODE_IP=192.0.2.1
   # (in .env)
   bash init-pg-primary.sh  # but with --join-existing-cluster
   # (TODO: implement --join-existing-cluster in init script)
   ```

**RTO**: 30-60s (auto-failover) + 5 min (DNS TTL) = **5-6 min**.

---

## 3. Manual failover (operator-driven)

**When this happens**:
- Planned maintenance on skygate-host-1 (kernel upgrade, Docker
  restart, etc).
- Auto-failover didn't trigger (etcd unreachable).
- Primary is up but degraded (high lag, broken replication).

**What the operator does**:
1. Pause writes on skygate (or accept a 30s write window):
   ```bash
   ssh admin@192.0.2.1
   # Set skygate in read-only mode
   # Edit .env: SKYGATE_READ_ONLY=true
   docker compose up -d --force-recreate skygate
   # Verify: curl -fsS http://localhost:8080/admin/update | grep -i read
   ```
2. Switchover to skygate-host-2:
   ```bash
   ssh root@skygate-host-2
   curl -fsS -X POST http://localhost:8008/failover \
     -H "Content-Type: application/json" \
     -d '{"leader":"skygate-host-1","candidate":"skygate-host-2"}'
   # Patroni switches the leader, the new primary is skygate-host-2
   ```
3. Update DNS A record → skygate-host-2.
4. Wait 5 min for DNS TTL.
5. Verify:
   ```bash
   curl -fsS http://localhost:8080/healthz
   ```
6. (After maintenance) Switchover back:
   - Repeat step 2 with leader/candidate swapped.

**RTO**: 30s (switchover) + 5 min (DNS TTL) = **5-6 min**.

---

## 4. Re-attach skygate-host-1 as a replica (after primary restart)

**When this happens**: skygate-host-1 is back up, but Patroni
on it is stale (the old primary). We need to re-init it as
a replica of skygate-host-2.

**What the operator does**:
1. SSH to skygate-host-1.
2. Stop the old Patroni container:
   ```bash
   docker stop skygate-patroni
   docker rm skygate-patroni
   rm -rf /var/lib/docker/volumes/patroni-data/_data/*
   ```
3. Update `.env` on skygate-host-1 to point at skygate-host-2 as
   the primary:
   ```bash
   SKYGATE_PRIMARY_IP=198.51.100.1  # skygate-host-2
   ```
4. Re-run `init-pg-replica.sh`:
   ```bash
   cd /home/admin/skygate/deploy/pg-ha
   bash init-pg-replica.sh
   # Patroni takes a basebackup from skygate-host-2, starts
   # streaming replication, joins the cluster as a replica.
   ```
5. Verify:
   ```bash
   bash check_pg_health.sh
   # Expected: 2 replicas (skygate-host-1 + skygate-host-2), 1 primary
   ```

---

## 5. Restore from wal-g backup (catastrophic failure)

**When this happens**: both primary AND replica's data is
corrupted (e.g. operator accidentally `DROP DATABASE`).
WAL archive in MinIO is intact.

**What the operator does**:
1. Pick a node to restore (usually skygate-host-1).
2. Stop Patroni:
   ```bash
   docker stop skygate-patroni
   ```
3. Restore from the most recent wal-g backup:
   ```bash
   source /etc/wal-g/env.sh
   # Find the most recent backup name
   wal-g backup-list
   # Restore it
   wal-g backup-fetch /var/lib/postgresql/data <backup-name>
   ```
4. Restart Patroni (it'll detect the restored data and join
   as a new primary):
   ```bash
   docker start skygate-patroni
   ```
5. Re-init skygate-host-2 as a replica of the restored skygate-host-1.
6. Verify cluster state.

**RPO**: time between last wal-g archive push and the
corruption event. With `archive_timeout=60s` (PG default),
RPO is at most 60s.

---

## 6. Common failure modes

### 6.1 etcd down

If etcd on skygate-host-2 is down, Patroni can't elect a new
primary. But the existing primary (skygate-host-1) keeps running
— reads and writes continue. The cluster is "frozen" at the
current state.

**Recovery**: restart etcd, the cluster resumes within 30s
(Patroni's `loop_wait`).

### 6.2 Both VMs down

Manual recovery from wal-g backup. See section 5.

### 6.3 Network partition (split-brain)

If skygate-host-1 and skygate-host-2 can't reach each other but
both are alive, the etcd on skygate-host-2 might elect it as
primary even though skygate-host-1 is still primary.

**Risk**: writes to the "old" primary (skygate-host-1) are
invisible to the "new" primary (skygate-host-2). Data divergence.

**Mitigation**: HAProxy on skygate-host-1 checks Patroni's
`/primary` endpoint. If the local Patroni is no longer
primary, HAProxy stops forwarding writes. Plus: DNS TTL
means clients only hit one IP at a time, so the writes
that did go to skygate-host-1 are isolated.

**Recovery**: resolve the network issue, re-run the
`init-pg-replica.sh` on the demoted node (Patroni will
detect the timeline mismatch and rebase via pg_rewind).

### 6.4 wal-g archive stuck

`wal-g backup-push` fails (MinIO is down or network blip).
WAL files pile up in the primary's `pg_wal/` directory,
eventually filling the disk.

**Mitigation**: monitor MinIO availability + disk space on
skygate-host-1. R31 in `verify_post_deploy.sh` already catches
disk > 85% full.
