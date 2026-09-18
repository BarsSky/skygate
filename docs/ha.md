# Skygate high availability

Consolidated HA / clustering document. Replaces the pre-v1.6 HA runbooks and
architecture notes removed in the 2026-09-18 documentation restructure
(`ha-architecture.md`, `cluster-management.md`, `ha-active-router.md`,
`ha-v1.5.0-execution.md`, `v1.5.0-ha-and-deploy.md`, `pg-failover.md`,
`v0.27.0-postgres-ha.md`); their full text stays in git history. Durable design
and procedures only — the v1.5.0 date-stamped status log is not carried over.

**Anonymization rule (repo-wide, locked 2026-08-18):** placeholders and RFC 5737
addresses only. Never commit real IPs, hostnames, domains, logins, certs,
passwords or fingerprints.

| Placeholder | Meaning |
|---|---|
| `<VM_HOST>` / `<VM_HOST_2>` | P1 skygate host (preferred active) / P2 (standby); the MagicDNS names stored in `ha_chain` |
| `<DB_HOST>` | PostgreSQL host skygate dials (local PG, or pg-aware HAProxy) |
| `<VIP>` | Stable public address (DNS A-record target) |
| `<PUBLIC_HOST>` | Public FQDN, e.g. `skygate.example.com` |
| `<S3_BUCKET>` | Deploy/backup bucket, e.g. `skygate-backups` |
| `192.0.2.x` / `198.51.100.x` / `203.0.113.x` | RFC 5737 example public IPs |
| `100.64.0.x` | Tailscale CGNAT range (example tailnet IPs) |
| `<DOCKER_SUBNET>`, `<LAN_SUBNET>` | Advertised Tailscale subnet routes |

Shell examples quote the placeholders (`'<VM_HOST_2>'`); drop the quotes when
substituting real values.

## Contents

1. [HA topology](#1-ha-topology) — components, links, diagram, tiers
2. [Cluster management](#2-cluster-management) — D1…D8, schema, shared vs per-host state, adding a host, health endpoints, split-brain, Phase 0…9
3. [Active router / failover](#3-active-router--failover) — chain, elector, force actions, DNS, operator symptoms
4. [Database failover](#4-database-failover) — detection, promotion, DSN, rollback, limits
5. [Certsync / certificate continuity](#5-certsync--certificate-continuity-in-ha)
6. [Bringing up / draining a host](#6-bringing-up--draining-a-host)
7. [Known gaps and open questions](#7-known-gaps-and-open-questions)
8. [Failure playbook](#8-failure-playbook)
9. [Cross-references](#9-cross-references)

---

## 1. HA topology

### 1.1 Components

| Component | What it is | HA role |
|---|---|---|
| **skygate instance** | Go app, container `skygate-skygate-1`, HTTP `:8080`; the entrypoint runs `go build` from the mounted repo each start (no `docker build` needed for source changes) | Serves the portal; one instance is the writer |
| **PostgreSQL primary** | PG 16, `wal_level=replica`, `synchronous_standby_names`, WAL archived by `wal-g` | Source of truth for skygate state |
| **PostgreSQL replica** | PG 16 streaming replica (`pg_basebackup … -Xs -R`) | Warm standby; promoted by Patroni |
| **Patroni + etcd** | PG role manager (`ttl: 30s`, `loop_wait: 10s`, `maximum_lag_on_failover: 1048576`, REST `:8008`); etcd = consensus store | Auto-failover; authoritative "am I primary" |
| **HAProxy** | pg-aware proxy, `option httpchk GET /primary` / `/replica`, `port 8008` | `:5000` writable primary, `:5001` replicas; one per PG host |
| **headscale** | Control plane; **stays SQLite + Litestream** (0.29.x has no PG support) | Serves the tailnet |
| **headplane** | Read-only headscale UI (`HEADPLANE_URL`, probe `/admin/healthz`) | Convenience; advisory |
| **DERP / derper** | Self-hosted relay (`:443` TCP, `:3478` UDP/STUN) — [derp.md](derp.md) | Connectivity, not state |
| **TLS terminator** | Caddy (`caddy reload`) or Nginx Proxy Manager on a fronting VM | Terminates HTTPS |
| **Active router** | The project's own HA chain + elector + external-DNS A-record failover. **No** keepalived, **no** VIP, **no** nginx upstream for skygate HTTP | Moves traffic between instances |
| **skygate-watchdog** | `internal/watchdog` goroutine | Hot-reloads the pgxpool on a DSN change |
| **S3 bucket** | `ha/certs/`, `ha/headscale-config/`, `deploy/<target>/`, WAL archive | Transport for certsync, deploy, config, WAL |

### 1.2 How they talk to each other

| From → To | Transport | Key / detail |
|---|---|---|
| Clients → TLS terminator → skygate | HTTPS on `<VIP>`, then HTTP `:8080` | DNS A-record for `<PUBLIC_HOST>` |
| skygate → headscale | HTTP/gRPC | `HEADSCALE_URL` (default `http://headscale:50444`) |
| skygate → headplane | HTTP `/admin/healthz` | `HEADPLANE_URL` (default: headscale URL + `:8080`) |
| skygate → PostgreSQL | `postgres://` via pgx pool | `SKYGATE_DB_DSN`, overridable from `cluster_database.current_dsn` |
| skygate → Patroni (read-only) | `GET /patroni` `:8008` | `SKYGATE_PATRONI_URL` (default `http://localhost:8008`) |
| skygate ↔ peers | HTTP over Tailscale IPs (`100.64.0.x`) | `ha_chain` members' `tailscale_ip` |
| Patroni → etcd; HAProxy → Patroni | etcd `:2379`; `GET /primary` / `/replica` | `etcd.host` in `/etc/patroni.yml`; `option httpchk` in `haproxy.cfg` |
| skygate → DNS provider | HTTPS (mTLS + form auth) | `SKYGATE_DNS_PROVIDER` + encrypted `ha.dns.*` |
| skygate → S3; wal-g → S3 | minio-go / `aws`; `archive_command = 'wal-g wal-push %f %p'` | `SKYGATE_CERTSYNC_S3_BUCKET`, `SKYGATE_HA_DEPLOY_S3_BUCKET`, `/etc/wal-g/env.sh` |

### 1.3 Topology diagram (active-passive + priority chain)

```
                    ┌──────────────────────────────┐
                    │      Tailscale clients       │
                    └─────────────┬────────────────┘
                                  │ tailscale0
                                  ▼
                    ┌──────────────────────────────┐
                    │   TLS terminator (Caddy/NPM) │
                    │   skygate.example.com        │
                    │   head.example.com           │
                    └─────┬──────────────────┬─────┘
                          │                  │
             active ◄─────┘                  └─────► warm standby
                  │                                      │
        ┌─────────▼─────────┐                ┌───────────▼───────┐
        │  P1: <VM_HOST>    │  heartbeats    │  P2: <VM_HOST_2>  │
        │  role=active      │ ◄────────────► │  role=standby     │
        │  skygate + hs     │  (Tailscale)   │  skygate + hs     │
        │  PG primary       │                │  PG replica       │
        └─────────┬─────────┘                └───────────┬───────┘
                  └──────────────┬───────────────────────┘
                    ┌────────────▼─────────────┐
                    │ Patroni + etcd quorum    │
                    └────────────┬─────────────┘
                    ┌────────────▼─────────────┐
                    │ <S3_BUCKET>: WAL archive │
                    │ + ha/certs + deploy/     │
                    └──────────────────────────┘
```

Tier 0 baseline: one VM (`<VM_HOST>`) running skygate + headscale + caddy + a
Tailscale client, daily cron to `/var/backups/{skygate,headscale}/latest/`.

### 1.4 Tiers

| Tier | Topology | RPO | RTO | Cost | Status |
|---|---|---|---|---|---|
| **0** | Single VM, daily backups | 1 h | 15–30 min | $0 | Baseline ([disaster-recovery.md](disaster-recovery.md)) |
| **0.5** | Multi-instance single-writer active-router, Litestream (`SKYGATE_HA_ROLE=active|passive`) | 0 | 5–15 min | ~$0.10/mo | **Deferred** — superseded by Tier 1 |
| **1** | Active-passive, PG streaming replication, Patroni auto-failover, priority chain | 0 | <1 min DB + DNS TTL | ~$0.50/mo | **Shipped (v1.5.0)** |

Tier 2 (active-active) is out of scope: two writers → split-brain → corruption,
headscale's API is not built for concurrent writers, and a household tailnet
(~4–20 users) gains nothing. Exactly one writer (active-router semantics) is the
right primitive here.

---

## 2. Cluster management

### 2.1 Design decisions D1 … D8

All eight were acked by the operator on **2026-09-01**, are implemented, and are
locked (do not revisit without operator sign-off).

- **D1** — cluster state lives in the **headscale/skygate DB metadata** (no Consul, no new KV store) — confirmed 2026-09-01.
- **D2** — a node learns its role from a **CLI flag at init plus a persistent per-host state file** (`/etc/skygate/state.json`; the shipped CLI writes `/etc/skygate/cluster-state.json`) — confirmed 2026-09-01.
- **D3** — skygate finds the DB DSN **dynamically from the `cluster_database` row, with `.env` as the fallback** — confirmed 2026-09-01.
- **D4** — a new node joins with a **signed invite token** from `skygate cluster invite` / `/admin/cluster` — confirmed 2026-09-01.
- **D5** — failover is **automatic via Patroni for PG, admin-gated via UI/CLI for the skygate role** — confirmed 2026-09-01.
- **D6** — DDL migrations live **in the DB, applied by the `skygate-migrate` CLI and versioned in the applied-migrations table** — confirmed 2026-09-01.
- **D7** — monitoring starts with **skygate's own health endpoints** (`/healthz`, `/readyz`, `/db/health`); Prometheus is additive — confirmed 2026-09-01.
- **D8** — `.env` is **hybrid**: `.env` is the default, a DB-side value wins on conflict, and the override is logged ("DSN override from headscale") — confirmed 2026-09-01.

Principle behind D1–D8: *"If admin must SSH to do X, then X is a gap, not a
workaround."* Adding a node, editing the chain, managing DNS credentials,
forcing a failover and pushing a binary all have in-app surfaces.

### 2.2 Abstractions and the `cluster_*` schema

| Abstraction | What | Lives in |
|---|---|---|
| **Node / Role** | A participating host; `skygate` / `skygate-standby` / `db-primary` / `db-replica` / `control` / `derp` | `cluster_node`, `cluster_node.roles` |
| **Cluster / Chain** | Nodes sharing a `cluster_id`; ordered app-role failover list | `cluster`, `global_settings.ha_chain` + `cluster.chain` |
| **Topology / Database** | Who is active for which role now; PG primary + replicas + DSN template | rendered from `ha_chain` + `cluster_node`; `cluster_database` |
| **Migration** | Moving a DB between locations | `planning` → `dumping` → `restoring` → `verifying` → `flipping` → `cleanup` |

Created by `internal/db/migrations_v0_64_b195.go` (PG + SQLite drivers),
prefixed `cluster_` so they can't collide with headscale's tables, idempotent
(`IF NOT EXISTS`): `cluster`, `cluster_node`, `cluster_database`,
`cluster_migration`, `cluster_invite`, `cluster_audit`. `cluster_audit` is the
durable membership/migration/failover record; `audit_log` (with `target_type` +
`target_id`) carries the admin-action surface. Columns:
[db-schema.md](db-schema.md).

Node states: `pending` → **Approve** → `ready` → `draining` (maintenance or
rolling upgrade) → `failed` (heartbeats missed); `RejoinNode` moves
`draining|failed` → `ready`. Approving a non-`pending` node is refused:
`cannot approve node in state %q (only state=pending can be approved; for
failed/draining, re-run skygate init on the box)`.

```bash
skygate cluster invite --role=<role> [--ttl 24h]
skygate cluster join <token>                        # writes /etc/skygate/cluster-state.json
skygate cluster nodes | dbs | audit [--limit 20]
skygate cluster failover --target=<node>            # admin-gated app-role swap
skygate cluster failover-drill --target=<node>      # non-destructive rehearsal
skygate cluster upgrade --target=<host>|--all       # rolling upgrade
skygate cluster heartbeat-daemon                    # POST /api/cluster/heartbeat every ~30s
```

Machine-to-machine endpoints (the `sgn1` token *is* the auth): `POST
/api/cluster/join`, `POST /api/cluster/heartbeat`. Errors: 400 bad JSON, 401 bad
token, 403 hostname mismatch, 409 invite used, 410 invite expired/revoked, 500
DB. Heartbeat staleness 3 missed intervals (~90 s) → `state=failed`.

### 2.3 Shared state vs per-host state

**The database is the only shared state** — no shared filesystem, no shared
SQLite, no replicated disk. Everything else is a secret or a per-host pointer.
Shared (in the DB, so it follows a DB failover): portal data (users, devices,
ACL, `audit_log`), `global_settings` (HA chain, DNS credentials, DSN override),
and the `cluster_*` tables.

Must match on **every** host:

| Item | Env key | Mismatch consequence |
|---|---|---|
| Session/JWT secret | `SKYGATE_JWT_SECRET` | Cookies minted on one host fail on the other: `forbidden — the JWT may have been signed with a different SKYGATE_JWT_SECRET than skygate is using to verify (re-source .env on both sides)`; users get bounced to login |
| Master secret (AES-256-GCM) | `SKYGATE_SECRET_KEY` (64 hex) | DNS creds + per-user API keys can't be decrypted: `db.ErrSecretKeyUnset` (`db: SKYGATE_SECRET_KEY is not set; per-user secrets cannot be encrypted`); a rotated key leaves rows unreadable. Also the invite HMAC key |
| headplane API key | `HEADPLANE_HEADSCALE__API_KEY` | headplane can't read the control plane |
| Control plane | `HEADSCALE_URL`, `CONTROL_URL` | Nodes registered against the wrong control plane |
| OIDC issuer + RSA keypair | `SKYGATE_OIDC_ISSUER`, `SKYGATE_OIDC_KEY_DIR` | Keypair is generated on first start; an "empty" host publishes a different JWKS `kid` (§5) |
| Same build | — | `dr_drill.sh` step 1 aborts |

Per-host (never auto-synced):

| State | Purpose |
|---|---|
| `/etc/skygate/cluster-state.json` | This node's `node_id` + heartbeat credential |
| `/etc/skygate/state.json` | Persistent role state per D2 |
| `/var/lib/skygate/ha-state/state.json` | Phase 0/7/9 runner state (`SKYGATE_HA_STATE` overrides) |
| `SKYGATE_UPDATE_STATE_PATH` (`/data/skygate-update-status.json`; native `<data_dir>/skygate-update-status.json`) + `SKYGATE_UPDATE_DIR` | **Auto-update status is per-host by design**; a native install must set both or writes are silent no-ops |
| `/var/lib/skygate/certs/.certsync-version` | Local certsync cache |
| `.env` / `/etc/skygate/skygate.env` / `/etc/conf.d/skygate` | Per-host; secret values must agree |

`.env` is **not** replicated: S3 `deploy/` carries the binary + `meta.json`,
`ha/headscale-config/` the headscale config. `bootstrap_standby.sh` fails fast
when `SKYGATE_HA_ENABLED`, `SKYGATE_HA_ROLE` or
`HEADPLANE_HEADSCALE__API_KEY` are wrong/missing.

### 2.4 Adding a second skygate host

**Phase 0 prerequisite:** a second VM on the Tailscale mesh with subnet routes
advertised **and approved**, so the standby reaches the primary's PostgreSQL /
object store / headplane behind the Docker bridge.

```bash
# On every node — join the tailnet pointed at your own headscale
sudo systemctl enable --now tailscaled   # unit must expose --socket=/run/tailscale/tailscaled.sock
sudo tailscale up --login-server=https://head.example.com \
  --authkey=$SKYGATE_KEY --hostname=<VM_HOST_2> \
  --accept-routes --accept-dns=false --netfilter-mode=off \
  --advertise-routes=<DOCKER_SUBNET_A>,<DOCKER_SUBNET_B>,<LAN_SUBNET>

# On the headscale host — approve routes, then tag the node
docker exec headscale headscale nodes list -o json        # find the node id
docker exec headscale headscale nodes approve-routes -i <ID> \
  --routes <DOCKER_SUBNET_A>,<DOCKER_SUBNET_B>,<LAN_SUBNET>
docker exec headscale headscale nodes tag -i <ID> \
  --tags 'tag:dev-<operator>-<VM_HOST_2>,tag:private' --force
```

Verify both directions (`tailscale ping <peer-tailscale-ip>` → `pong`, not `no
matching peer`) and the subnet routes.

> **Traps.** `--hostname` collides with an old Tailscale-SaaS node of the same
> name and gets a `-1` suffix (cosmetic). A `tailscaled` started with the
> deprecated `--statedir=` flag creates a state dir but no socket, so `tailscale
> up` hangs. A first `tailscale up` can take 60+ min on slow NAT (use 24 h
> preauth keys). headscale may need a restart after `nodes tag`.
> `--netfilter-mode=off` does not clean up a previous session's iptables rules
> (§6.4). With headscale 0.29.1 + grants, **untagged** user-owned nodes do not
> appear in other nodes' netmaps even when a grant allows the traffic.

**Phase 7 — provision the standby.** Mint the preauth key on the primary so the
node lands under the right headscale user (not the synthetic `tagged-devices`
user, which gets no per-device ACL grants):

```bash
# On the primary
NEW_KEY=$(bash deploy/scripts/create-standby-preauth.sh --hostname <VM_HOST_2>)

# On the NEW host
ssh <VM_HOST_2>
cd ~/skygate
export SKYGATE_STANDBY_TS_AUTHKEY="$NEW_KEY"
bash scripts/bootstrap_standby.sh
```

`bootstrap_standby.sh` (B152; state-tracked wrapper `scripts/ha-phase7.sh`):

0. **Tailscale auth**, gated on `SKYGATE_STANDBY_TS_AUTHKEY`: runs
   `tailscale up --login-server=https://head.example.com --netfilter-mode=nodir`
   — deliberately **`nodir`, never `off`**, which would re-create the iptables
   trap. Idempotent (`BackendState=Running` → skip); unset → assume already
   joined (legacy path).
1. Pre-flight idempotency check.
2. Pull the binary from `s3://<S3_BUCKET>/ha/deploy/<hostname>/` (falls back to
   the local git checkout without S3 creds).
3. Pull the headscale config from `s3://<S3_BUCKET>/ha/headscale-config/`.
4. `docker compose up -d --force-recreate --no-deps skygate headscale headplane`.
5. Poll `http://127.0.0.1:8080/healthz` for up to 60 s.
6. Verify the hostname is in `global_settings.ha_chain`; write an `ha.bootstrap`
   audit row.

Required: `SKYGATE_HA_ENABLED=true`, `SKYGATE_HA_ROLE=standby`, non-empty
`HEADPLANE_HEADSCALE__API_KEY`. Cluster membership (distinct from the HA chain):

```bash
skygate cluster invite --role=skygate-standby --ttl 24h   # on the primary
skygate cluster join <sgn1-token>                          # on the new host
skygate cluster heartbeat-daemon &                         # or a systemd unit
# UI: /admin/cluster → Approve (pending → ready)
```

Auto-discovery (`POST /admin/cluster/discover`; ticker every 5 min,
`SKYGATE_DISCOVERY_INTERVAL_SEC`, optional `SKYGATE_DISCOVERY_TAG`) adds new
tailnet peers as `cluster_node` rows in `state=pending`; the admin still
approves. Finally add the host to the HA chain (§3.2).

### 2.5 Health and readiness endpoints

Router/proxy contract; all unauthenticated.

| Endpoint | Semantics | Body |
|---|---|---|
| `GET /healthz` | **Liveness** — always `200` while the process lives; never touches DB or headscale | `status`, `instance_id`, `build`, `timestamp` |
| `GET /readyz` | **Readiness** — `200` iff the DB is reachable, else `503`; cached 1 s, refresher every 30 s (`SKYGATE_AVAILABILITY_CHECK_INTERVAL`) | `healthy` (DB-only), `dependencies_healthy` (AND of db/headscale/headplane/tailscale), `db`, `headscale`, `headplane`, `tailscale`, `instance_id`, `build`, `uptime_sec`, `timestamp`, `checks{}`, `availability{}` |
| `GET /db/health` | Cached 30 s sample | `server{}`, `database{}`, `replication{}` (`lag_bytes`, `lag_seconds`, `replay_lsn`), `maintenance{}`, `xlog{}`, `pool`, `slow_queries`, `sampled_at` |
| `GET /metrics` | Prometheus textfmt exporter (B226); same exposure as `/healthz` | metrics text |
| `<HEADPLANE_URL>/admin/healthz` | headplane probe (path appended if absent) | headplane health |
| `GET /admin/services` | Operator view over the same cached availability | HTML |

Since D5 `healthy` is **DB-only**: a headscale outage no longer 503s the
instance (skygate serves independently of headscale — the B91 principle).
`dependencies_healthy` keeps the strict AND-of-all view; `skipped*` counts as
healthy.

### 2.6 Split-brain rules

1. **Exactly one writer.** Active-passive is locked; two writers → corruption.
   One active node holds the public FQDN, the A-record, the cert and secrets.
2. **Patroni is ground truth** for the DB. The elector *reads* `/patroni`
   (`state == "running"` **and** `role == "primary"`) and never joins the
   election; if it disagrees with operator intent, Patroni wins next tick.
3. **DNS TTL is the isolation layer** — clients resolve one IP at a time, so
   writes that reached the "wrong" node during a partition stay isolated.
4. **HAProxy stops forwarding writes** once the local Patroni is no longer
   primary (`option httpchk GET /primary`).
5. **No auto-reclaim** — a returning P1 does not take the role back (§3.5).
6. **Partition recovery is explicit:** fix the network, `skygate ha reclaim` on
   the higher-priority node, re-init the demoted PG node as a replica (Patroni
   detects the timeline mismatch, rebases via `pg_rewind`).

Two different electors — do not confuse them:

| Package | Responsibility | Writes |
|---|---|---|
| `internal/ha` (B145) | The **HA chain**: which skygate host is active, the DNS update, Telegram on transition | `global_settings.ha_chain`, `ha.role_change` rows |
| `internal/elector` (B204) | **Node liveness** of `cluster_node` + a recommendation only | `cluster_audit` `node_health`, `failover_recommend`; promotion stays an admin-gated `skygate cluster failover` |

### 2.7 Implementation phases (Phase 0 … Phase 9)

| Phase | What | Status |
|---|---|---|
| **Phase 0** | Tailscale mesh prerequisite (join, advertise + approve routes, tag, verify ping) | **DONE** (2026-08-31); runner `scripts/ha-phase0.sh` |
| **Phase 1** | HA chain + elector (`internal/ha/{chain,storage,elector}.go`, B145) | **DONE (v1.5.0)** |
| **Phase 2** | reg.ru DNS client + failover (`internal/dns*`, B146) | **DONE (v1.5.0)**; live test 2026-09-07 — §3.10 |
| **Phase 3** | Certsync S3 ↔ local (`internal/certsync`, B147) | **DONE (v1.5.0)** |
| **Phase 4** | `/admin/certificates` upload + DNS-01 toggle (B148) | **DONE (v1.5.0)**; LE acquisition DEFERRED (G4) |
| **Phase 5** | `/admin/ha` chain editor + failover controls (B149); **5.1** admin-managed credentials | **DONE (v1.5.0)** |
| **Phase 6** | `skygate deploy {push,pull,sync,status}` + `/admin/deploy` (B150) | **DONE (v1.5.0)** |
| **Phase 7** | Standby bootstrap (`scripts/bootstrap_standby.sh` B152 + `create-standby-preauth.sh`); runner `scripts/ha-phase7.sh` | **DONE** (scripts); running it on a new host is an operator step |
| **Phase 8** | `scripts/init-headplane.sh` — mint + apply the headplane API key (B151) | **DONE (v1.5.0)** |
| **Phase 9** | Live DR drill (`scripts/dr_drill.sh` B153; runner `scripts/ha-phase9.sh`; `scripts/ha-status.sh`) | **Scripts DONE**; the live drill was never run — Q9 |
| (Phase 10) | Release tag + GitHub release + deploy | **DONE (v1.5.0)** |

---

## 3. Active router / failover

### 3.1 Where the "router" actually lives

No keepalived, no floating VIP, no nginx/HAProxy upstream for skygate HTTP.
Traffic moves by **changing the public DNS A-record**; which host owns that
record is decided by the project's own active-router component — the **HA chain
+ elector** in every skygate process, with Patroni as the DB-side authority:

```
Patroni (is this node the PG primary?) ──► HA elector (should this node be
                                            the chain's "active"?)
                                                 │
                                                 ▼
                                   DNS A-record update for <PUBLIC_HOST>
                                                 │
                                                 ▼
                                        clients → the active node
```

### 3.2 The HA chain

A single JSON blob in `global_settings.ha_chain`:

```json
{
  "members": [
    {"hostname": "<VM_HOST>",   "priority": 1, "public_ip": "192.0.2.10", "tailscale_ip": "100.64.0.10", "last_seen_unix": 1724000000, "role": "active"},
    {"hostname": "<VM_HOST_2>", "priority": 2, "public_ip": "192.0.2.11", "tailscale_ip": "100.64.0.11", "last_seen_unix": 1724000000, "role": "standby"}
  ],
  "auto_failover_enabled": true,
  "last_transition_unix": 0
}
```

- "Alive" = heartbeat within `MissedThreshold * HeartbeatInterval` (= **15 s**
  with defaults 3 × 5 s); "next active to promote" = the **lowest-priority ALIVE**
  member.
- `SaveChain` is transactional and idempotent (`changed=false` when unchanged);
  an invalid chain fails `Validate()` on the next load.

| `global_settings` key | Meaning | Encrypted? |
|---|---|---|
| `ha_chain` | Chain JSON | no |
| `ha_apply_active_role` | Elector "force target" (empty = elector decides) | no |
| `ha.dns.cert_pem_enc` / `ha.dns.password_enc` | DNS provider client cert / API password | **yes** (AES-256-GCM via `SKYGATE_SECRET_KEY`) |
| `ha.dns.zone` / `ha.dns.login` / `ha.dns.provider` | Zone / login / `external` | no |

Bootstrap: set `SKYGATE_HA_ENABLED=true` (optional
`SKYGATE_HA_HEARTBEAT_INTERVAL=5s`, `SKYGATE_HA_MISSED_THRESHOLD=3`,
`SKYGATE_HA_ROLE=auto`) on each node, open `/admin/ha` §3, add every member
(`hostname` = MagicDNS name, `priority`, `public_ip` = the IP the A-record should
point at, optional `tailscale_ip`), set the policy in §2, **Save chain**, confirm
the `ha.bootstrap` row in §6. The elector picks it up on its next 5 s tick.
`HAEnabled` defaults to **false**; while false the boot path logs `ha: HA
disabled (SKYGATE_HA_ENABLED=false; /admin/ha page or env-var will enable once
the chain is configured)` — no false-promotion risk during ramp-up. Removing the
**active** member with auto-failover on promotes the next-priority alive member
within 5 s; disable auto-failover first for a clean remove.

### 3.3 The elector tick

Every `SKYGATE_HA_HEARTBEAT_INTERVAL` (default **5 s**) the elector:

1. Asks the local Patroni REST API (`SKYGATE_PATRONI_URL`, path `/patroni`)
   whether **this** node is primary.
2. Asks each remote member over its Tailscale IP whether it has seen a recent
   heartbeat from this node; members reciprocate, so the chain converges.
3. Decides the global role: primary **and** the active slot is empty or points at
   a higher-priority member now unreachable → self-promote; **not** primary but
   still marked active → self-demote (write the chain with the lowest-priority
   ALIVE member active).
4. Writes via `SaveChain`, appends `ha.role_change from=<X> to=<Y>
   reason=patroni_primary` (through `db.AppendExitRuleLog`; no separate audit
   table), and sends a Telegram alert.

Force actions never bypass the elector: they write `ha_apply_active_role` and the
next tick either confirms (Patroni agrees) or overwrites it.

### 3.4 Failover behaviour

| Scenario | auto-failover ON | auto-failover OFF |
|---|---|---|
| Active dies, standby alive | Standby misses 3 heartbeats (15 s), self-promotes, writes the chain, sends Telegram | Nothing; operator clicks **Force promote** or runs `skygate ha promote '<VM_HOST_2>'` |
| Active comes back | Standby **stays** active (auto-reclaim OFF); operator clicks **Reclaim primary** / `skygate ha reclaim` | Same |
| Standby dies | Active logs `ha.member_unreachable`, keeps serving | Same |
| Both nodes die (etcd down) | Patroni frozen at the last known state; reads/writes may stall if you failed over mid-write; restart etcd, resumes within ~30 s (`loop_wait`) | Same |
| Network partition | Risk: both think they are active. Mitigations: Patroni leader lock, elector's Patroni read, HAProxy `/primary` gate, DNS TTL. Recovery: fix the network, `skygate ha reclaim` on the higher-priority node | Same |

Budget: container restart 5–10 s; chain promotion after 3 missed heartbeats 15 s;
operator-visible failover 30–60 s plus DNS propagation (TTL 5 min worst case).

### 3.5 Anti-flap and auto-reclaim

**Locked rule:** a returning P1 does not auto-flip back — this avoids the flap
(P1 dies → P2 takes over → P1 returns → P1 takes over → P1 dies again → …). The
operator reclaims explicitly with `skygate ha reclaim` or the **Reclaim primary**
button. Auto-reclaim is **OFF by default** (toggle: `/admin/ha` §2).

### 3.6 Force actions

All three write `ha_apply_active_role` and let the elector act next tick. CLI,
`/admin/ha` §5 and `/admin/deploy` §3 share one code path.

| Action | Effect | Reversible? |
|---|---|---|
| `skygate ha promote '<host>'` | Sets `ha_apply_active_role=<host>`; elector promotes within 5 s | Yes — clear / reclaim |
| `skygate ha demote '<host>'` | Sets **then immediately clears** the key: net effect "revoke any auto-promote intent to that host" (avoids "demote, then auto-reclaim brings it back") | Yes |
| `skygate ha reclaim` | Sets `ha_apply_active_role=""`; elector re-picks (P1 wins if alive) | Yes |

Related but different:

| Command / button | Effect |
|---|---|
| `skygate cluster failover --target=<node>` (`POST /admin/ha/cluster/failover`) | App-role swap on `cluster_node`: a ready `skygate-standby` becomes role `skygate`, the primary becomes `state=draining`; one transaction |
| `POST /admin/database/failover` | Patroni `/switchover` to a named candidate (§4.3) |
| `POST /admin/deploy/test-failover` | Dry run: what the elector *would* do now; touches nothing |
| `skygate cluster failover-drill --target=<node>` | Non-destructive rehearsal of the app-role swap |

### 3.7 Promotion and demotion procedure

```bash
# Planned promotion to P2
skygate deploy status                                  # both nodes on the same build
curl -fsS http://localhost:8080/readyz                 # on both hosts
# dry run first: /admin/deploy → "Test failover"
skygate ha promote '<VM_HOST_2>'                       # elector acts within 5 s

# Watch it land
psql "$SKYGATE_DB_DSN" -c "SELECT value FROM global_settings WHERE key='ha_chain';"
psql "$SKYGATE_DB_DSN" -c "SELECT created_at, actor, action, target, reason \
  FROM audit_log WHERE action LIKE 'ha.%' ORDER BY id DESC LIMIT 5;"
dig +short <PUBLIC_HOST>
curl -fsS https://<PUBLIC_HOST>/healthz

# Planned demotion of the current active: pause writes first
#   .env: SKYGATE_READ_ONLY=true
docker compose up -d --force-recreate skygate
curl -fsS http://localhost:8080/admin/update | grep -i read
skygate ha promote '<VM_HOST_2>'     # or: skygate ha demote '<VM_HOST>'

# Reclaim after the preferred node returns
skygate ha reclaim
```

Never promote without checking Patroni: if the target is not the PG primary the
elector will not promote it and the audit row explains why. Resolve Patroni first
(`patronictl -c /etc/patroni.yml list`), then re-promote.

### 3.8 DNS failover

The A-record for `<PUBLIC_HOST>` (and `head.example.com`) has a **5-minute TTL**
and is moved by the active node through a pluggable provider:

```go
type Provider interface {
    Name() string
    GetRecord(ctx, zone, name string) (ip string, err error)
    UpdateRecord(ctx, zone, name, ip string) error
    TestConnection(ctx) error
}
```

`BuildProvider` reads `SKYGATE_DNS_PROVIDER`. At v1.5.0 only the external /
`regapi` provider is fully implemented; `cloudflare`, `route53`, `rfc2136` are
reserved names (anything else → `ErrUnknownProvider`). Adding one = implement the
four methods, add the `case` in `BuildProvider`, add a unit test against a local
`httptest.Server` (never the live API), add the fields to `/admin/ha` §4, add a
B-check.

Credentials live in the encrypted `ha.dns.*` rows, edited on `/admin/ha` §4
(login, alternative password, zone, cert PEM, key PEM). **Test** calls
`TestConnection`, which pings the provider's zone-lookup endpoint; only HTTP
status and `error_code` matter.

```bash
SKYGATE_DNS_PROVIDER=regapi
skygate regapi-credentials set     # writes the encrypted rows (needs SKYGATE_SECRET_KEY)
skygate regapi-credentials show
skygate regapi-credentials test
bash scripts/b146_regapi_live.sh   # standalone live test; re-run after any rotation
```

| Error code | Meaning | Fix |
|---|---|---|
| `NO_AUTH` | Wrong auth shape, cert not registered, or wrong alternative password | Use the working shape: mTLS **plus** top-level `username` + `password` form fields — never the password inside `input_data` JSON |
| `ACCESS_DENIED_FROM_IP` | VM public IP not whitelisted (or cached / Save not clicked) | Add `<vm-public-ip>/32`, click Save, wait 5–30 min |
| `IP_EXCEEDED_ALLOWED_CONNECTION_RATE` | Provider rate limit (per user and per IP) | Wait an hour; stop repeating identical requests |
| `DOMAIN_NOT_FOUND` | Wrong zone, or the account doesn't own the domain | Fix `ha.dns.zone` |
| Persistent `ACCESS_DENIED_FROM_IP` with correct creds | "10 identical wrong requests" sub-limit | **Only provider support can reset it** |

`scripts/b146_regapi_live.sh` preflights cert + key + `SKYGATE_DNS_REGAPI_USER` /
`_PASSWORD` / `_ZONE`, **SKIPs with exit 2** and a precise reason when something
is missing, and prints `PASS: <zone>/<subdomain> -> <ip>`, `FAIL: …` or `SKIP: …`.

### 3.9 What the operator sees

| Failure mode | Symptom | Immediate action |
|---|---|---|
| Active container dies | Container restarting in `docker ps`; a few seconds of 502s; `/healthz` fails on that host | Nothing (restart policy) or `docker compose up -d --no-deps skygate`; back in 5–10 s |
| Active **host** dies | `/admin/ha` on the survivor flips the chain within ~15 s (auto-failover ON) + Telegram; DNS still points at the dead IP → clients time out | Wait for the elector + TTL, or `skygate ha promote '<VM_HOST_2>'`, then `dig +short <PUBLIC_HOST>` |
| DNS did not move after promotion | `/admin/ha` says `active`, `dig` returns the old IP | `/admin/ha` §4 → **Test**, then fix per §3.8 |
| Heartbeat blip (false failover) | Roles flipped though both nodes are healthy; repeated `ha.role_change` rows | `tailscale status` both nodes; fix connectivity; `skygate ha reclaim` |
| Both nodes show `active` | Split-brain; writes may go to both | `skygate ha reclaim` on the higher-priority node; verify Patroni |
| Promoted but nothing happened | `ha.promote` row in `/admin/audit`, `/admin/ha` unchanged | Target is not the Patroni primary — fix Patroni, re-promote |
| Chain not loading on one node; `last_seen_unix` stale (>60 s) | No heartbeats / old timestamp for that node | `SKYGATE_HA_ENABLED=false` there → set `true`; else `journalctl -u skygate`, `curl /readyz` |
| Save creds → `db.ErrSecretKeyUnset` | "SKYGATE_SECRET_KEY is not set" | Add `SKYGATE_SECRET_KEY`, restart skygate |
| Bad chain pushed via psql | `/admin/ha` fails to render / `Validate()` errors | Restore the last known-good `ha_chain` JSON |

### 3.10 Live test record (Phase 2 / DNS failover)

- **Tested:** the end-to-end DNS failover path for `Phase 2` — authentication
  against the reg.ru DNS API (`regapi` provider: mTLS client cert **plus** HTTP
  Basic with the alternative password, sent as **top-level form fields**), reading
  the `A` record for the subdomain behind `<PUBLIC_HOST>`, and comparing it with
  the node's public IP (the same check the drill performs in step 4).
- **How / where:** `scripts/b146_regapi_live.sh` (bash + curl + a Python JSON
  parser) on the VM holding the provider cert/key.
- **Date:** **2026-09-07** (B146 productionized; the blocking open questions Q1
  credentials and Q2 IP whitelist were resolved the same day).
- **Outcome:** PASS — credentials accepted, cert accepted, A-record read back,
  `Phase 2` / B146 marked done. Two failure classes were eliminated first:
  `NO_AUTH` (the password must not live inside `input_data`) and
  `ACCESS_DENIED_FROM_IP` (the VM's public IP had to be whitelisted).
- **Placeholders:** `<PUBLIC_HOST>`, `head.example.com`; no operator domain,
  login, cert or IP is reproduced. Re-run the script after every cert renewal,
  password rotation or whitelist change (it is not in the pre-deploy B-check
  catalog because it needs live credentials).

---

## 4. Database failover

Two scenarios: **auto-failover** (Patroni; primary died unexpectedly, etcd alive)
and **manual failover** (planned maintenance, or auto-failover did not fire).
Deploy artifacts: `deploy/pg-ha/`. Backup/restore:
[backup-restore-and-migration.md](backup-restore-and-migration.md),
[disaster-recovery.md](disaster-recovery.md).

### 4.1 Detection

```bash
ssh admin@<DB_HOST>
cd /home/admin/skygate/deploy/pg-ha
bash check_pg_health.sh
```

Healthy output: `Local node: <DB_HOST> (state: running)` / `Primary count: 1
(expect 1)` / `Replica count: 1 (expect >= 1)` / `Max replication lag: 0.5s` /
`OK: cluster healthy`.

Exit codes: `0` healthy; `1` degraded (lag > 10 s — investigate, no failover);
`2` critical (no primary / no replicas / etcd down) → continue below. Other
signals: `/db/health` (`replication_lag_seconds`, `replay_lsn`), the Telegram
alerts `❌ DB health DEGRADED` / `✅ DB health recovered` (sampler transition) and
`❌ PG health DEGRADED` (watchdog, after 3 consecutive `cluster_database` read
failures).

### 4.2 Auto-failover (Patroni)

**When:** primary dead/unreachable, replica alive, etcd alive. **What happens:**
Patroni sees the leader lock expire after `ttl=30s`, elects the replica, and
updates the state in etcd. **Timeline:** t=0 primary dies → t=30 s the survivor
notices → t=30–40 s it promotes itself → the dead host's HAProxy died with it, so
**DNS is what matters now**.

```bash
# 1. Verify the new primary
ssh root@<VM_HOST_2>
cd /home/admin/skygate/deploy/pg-ha
bash check_pg_health.sh          # "Local node: <VM_HOST_2> (state: running)"

# 2. Point the <PUBLIC_HOST> A-record at the surviving host (or let the HA
#    elector do it — §3.8); wait out the 5-min TTL.

# 3. Verify
curl -fsS http://localhost:8080/healthz

# 4. When the old primary returns, re-init it as a replica (§4.7)
```

**RTO:** 30–60 s + DNS TTL ≤5 min ≈ **5–6 min** end-to-end.

### 4.3 Manual failover / switchover

```bash
# 1. Pause writes (or accept a ~30 s write window)
ssh admin@<VM_HOST>
#   .env: SKYGATE_READ_ONLY=true
docker compose up -d --force-recreate skygate
curl -fsS http://localhost:8080/admin/update | grep -i read

# 2. Switch the PG leader via the Patroni REST API
curl -fsS -X POST http://localhost:8008/failover \
  -H "Content-Type: application/json" \
  -d '{"leader":"<DB_HOST>","candidate":"<DB_HOST_2>"}'

# 3. Move DNS to the new active host; wait out the TTL
# 4. Verify
curl -fsS http://localhost:8080/healthz
# 5. After maintenance, repeat step 2 with leader/candidate swapped
```

**RTO:** 30 s + DNS TTL ≈ **5–6 min**.

In-product equivalent: `POST /admin/database/failover` (B219) — type the candidate
PG node name; Patroni promotes it and the old primary becomes a replica. If
`SKYGATE_PATRONI_URL` doesn't resolve, the button returns a clear error and
skygate stays on the old primary (safe failure). One-click rollback of the last
failover: `POST /admin/database/failover/rollback` (B220, driven by the
`db.last_failover` state).

### 4.4 Reconfiguring skygate's DSN

Three layers keep a DB move restart-free:

1. **DB row is the source of truth (D3/D8).** `cluster_database.current_dsn`
   overrides `dsn_template`; editing the DSN in `/admin/database` (B197) writes
   that row and is audited.
2. **The watchdog hot-reloads the pool.** `internal/watchdog` ticks, reads
   `cluster_database`, and on change opens a new pgx pool, pings it, then
   `ResettableDB.Reset(newPool)` — no restart:

   ```
   dbmigrate-watchdog: DSN change detected; swapping to <redacted-dsn>
   dbmigrate-watchdog: pool swapped successfully (new backend pid: <pid>)
   dbmigrate-watchdog: cluster_database row not present (fresh deploy or no override configured; keeping env-DSN pool)
   dbmigrate-watchdog: read cluster_database: <err> (keeping current pool)
   ```

   A pool that fails to open or ping is discarded and the current one kept —
   never a hard cutover to an unverified backend.
3. **Every consumer follows the hot-reload**: services hold the `ResettableDB`
   (`db.DBSource`), not a captured `*sql.DB` (the B224 stabilization), so health
   sampler / watchdog / backup / monitor / discovery re-read `Current()`.

Manual fallback, or the first move before a `cluster_database` row exists:

```bash
#   .env: SKYGATE_DB_DSN=postgres://skygate:<pw>@<DB_HOST>:5432/skygate?sslmode=disable
docker compose up -d --force-recreate --no-deps skygate
```

Use `/admin/database` → **Test connection** before applying a new DSN; it returns
`{ok, latency_ms, error}` so a typo never becomes an outage.

### 4.5 Verification

```bash
patronictl -c /etc/patroni.yml list        # one Leader, N replicas
curl -fsS http://localhost:8080/readyz     # "db":"ok", healthy:true
curl -fsS http://localhost:8080/db/health  # replication lag, size, xlog
psql -h <DB_HOST> -p 5000 -U skygate skygate -c "SELECT pg_is_in_recovery();"  # f = primary
psql -h <DB_HOST> -p 5001 -U skygate skygate -c "SELECT pg_is_in_recovery();"  # t = replica
```

Confirm the watchdog swapped: the `dbmigrate-watchdog:` log lines, plus
`global_settings` rows (`key LIKE 'ha.%'`).

### 4.6 Rollback

| Situation | Rollback |
|---|---|
| Wrong/premature switchover | Repeat with leader/candidate swapped, or the `/admin/database` rollback button (B220) |
| New DSN is wrong | Edit it back in `/admin/database` (or clear `current_dsn` so the env DSN applies); the watchdog swaps back |
| Broken build after a rolling upgrade | `skygate deploy pull` the previous binary, or `git checkout <last-known-good-tag> && make build && skygate deploy push --target=<host>`, then `docker compose up -d --force-recreate --no-deps skygate` |
| DNS points at the wrong host | Change the A-record back; the old host still runs |
| Data corruption | Restore from the latest dump / `wal-g` backup (§4.8) |

### 4.7 Re-attaching the old primary as a replica

```bash
docker stop skygate-patroni                      # 1. drop the stale Patroni state
docker rm skygate-patroni
rm -rf /var/lib/docker/volumes/patroni-data/_data/*
# 2. .env: SKYGATE_PRIMARY_IP=<new-primary-ip>    (e.g. 198.51.100.1)
cd /home/admin/skygate/deploy/pg-ha              # 3. re-init as a replica
bash init-pg-replica.sh                          #    (basebackup + streaming)
bash check_pg_health.sh                          # 4. verify: 1 primary, lag ~0
```

> **Caveat:** re-joining an *existing* cluster from a former primary
> (`--join-existing-cluster`) was a TODO in the init script. Patroni detects the
> timeline mismatch and rebases with `pg_rewind`; if the rejoin refuses, use
> `patronictl reinit` (§7).

### 4.8 Restore from a wal-g backup

```bash
docker stop skygate-patroni                      # 1. stop Patroni
source /etc/wal-g/env.sh                         # 2. restore
wal-g backup-list
wal-g backup-fetch /var/lib/postgresql/data <backup-name>
docker start skygate-patroni                     # 3. comes up as primary
# 4. re-init the other node as a replica (§4.7); 5. verify (§4.5)
```

**RPO:** the interval between the last `wal-g` archive push and the corruption
event — at most **60 s** with `archive_timeout=60s` (PG default).

### 4.9 Guarantees and limitations

**Guaranteed:** RPO = 0 for the app DB under synchronous streaming replication
while the primary lives and ≥1 sync standby is connected; RTO 30–60 s for the DB
role (`+ DNS TTL` ≤5 min for clients, container restarts 5–10 s); no write loss
on a clean switchover (coordinated through etcd); skygate survives a DB move
**without a restart** (watchdog pool swap).

**Lost / at risk**

- **Writes accepted by a partitioned "old primary" are lost** when the partition
  heals; DNS TTL limits but does not remove the window. Repair: re-init the
  demoted node as a replica; diverged rows are not merged.
- **WAL archive lag is the real RPO when the object store is down.** A failing
  `wal-g wal-push` fills `pg_wal/` and can fill the disk (the post-deploy check
  `R31` flags disk > 85 %).
- **etcd quorum.** With single-node etcd there is no quorum: if it dies, Patroni
  cannot elect a leader until it returns (the current primary keeps serving —
  "frozen", not down). Restart etcd; resumes within ~30 s (`loop_wait`). A 3-node
  etcd is the permanent fix (G2).
- **headscale is not on PG** (SQLite + Litestream), so it has its own RPO/RTO.
- **Both hosts down** ⇒ manual recovery from the last dump + headscale Litestream,
  ~30 min.

**Manual repair required after**

| Event | Step |
|---|---|
| Partition healed | Re-init the demoted node as a replica; verify with `check_pg_health.sh` |
| Former primary rejoins | `--join-existing-cluster` may be missing → use `patronictl reinit` |
| Data loaded from a dump | Verify every PG sequence is at `MAX(id)+1` (the classic "duplicate key on next INSERT") |
| `cluster_database` row deleted | None — the watchdog logs `cluster_database row not present … keeping env-DSN pool` (B248 exemption) |

---

## 5. Certsync / certificate continuity in HA

**Problem solved.** The pre-B147 pipeline (`cert-renew.sh` + system cron) ran on
the **active node only** and wrote the PEM/key locally, so after a failover the
new active 502'd until an operator re-ran the renew script. B147 makes every node
poll a shared S3 manifest: failover is always cert-fresh, no operator action.

```
s3://<S3_BUCKET>/ha/certs/
    cert.pem      # current cert
    key.pem       # matching private key
    .version      # {"version": 5, "sha256": "...", "uploaded_at": "..."}
s3://<S3_BUCKET>/deploy/<target-hostname>/{skygate,meta.json}
s3://<S3_BUCKET>/ha/headscale-config/...
```

**Tick (30 s default), on every node:**

1. Read `.version`.
2. If the remote version is higher **or** the remote cert SHA-256 differs:
   download `cert.pem` + `key.pem` to a temp file; verify a valid x509 cert with a
   matching RSA/EC/Ed25519 key (`crypto/x509.ParseCertificate` + PKCS#1 / PKCS#8 /
   SEC1 via `matchedAny`); **atomic rename** into
   `SKYGATE_CERTSYNC_LOCAL_DIR/{cert.pem,key.pem}`; fire the Caddy reload callback
   (here `docker exec skygate-caddy caddy reload`); update the local cache; write a
   `certsync.pull` audit row with version + SHA.
3. Self-check every tick: a cert expiring within **7 days** logs a WARNING and
   sends a Telegram alert.

A mismatched or unparseable pair is **rejected before the rename**, so a bad
upload cannot take the listener down — the operator gets an alert + audit row.
Local cache: `.certsync-version`. `.version` is an operator-controlled counter
(not cert `NotAfter`) because certs can be renewed early: "pull when I upload" is
the explicit intent.

```
SKYGATE_CERTSYNC_ENABLED=true                              # default true
SKYGATE_CERTSYNC_S3_BUCKET=<S3_BUCKET>                     # default skygate-backups
SKYGATE_CERTSYNC_LOCAL_DIR=/var/lib/skygate/certs          # default
SKYGATE_CERTSYNC_INTERVAL=30s                              # default
```

Startup log: `certsync: enabled (interval=30s bucket=<S3_BUCKET> local_dir=…
caddy_reload=…)` or `certsync: disabled (SKYGATE_CERTSYNC_ENABLED=false).
Pre-B147 system-cron cert-renew.sh continues to run.`

**Operator flow:** `/admin/certificates` on any node (current cert info, upload
form, DNS-01 toggle, recent events) → paste/upload the cert PEM **and** key
(validation reuses `certsync.ValidateCertKeyPair`) → wait ≤30 s → every node
serves the new cert, Caddy reloaded, a `certsync.pull` row per node. Failure
modes: §8. TLS setup: [https.md](https.md).

**What certsync does *not* cover** — the rest of the auth material:

| Material | Location | HA rule |
|---|---|---|
| TLS cert + key | synced by certsync | automatic |
| `SKYGATE_JWT_SECRET` | `.env` per host | **byte-identical**, or sessions issued on one node are rejected on the other |
| `SKYGATE_SECRET_KEY` (AES-256-GCM, also the invite HMAC key) | `.env` per host | **byte-identical**, or DNS creds / per-user API keys can't be decrypted |
| OIDC RSA keypair | `SKYGATE_OIDC_KEY_DIR` (default `./data/oidc-keys`) | Generated on first start if absent; replicate the directory to every host, or a host publishes a different JWKS `kid` and headscale rejects the other host's `id_token`s. Keep `SKYGATE_OIDC_ISSUER` on the stable FQDN. See [oidc.md](oidc.md) |
| headplane API key | `.env` (`HEADPLANE_HEADSCALE__API_KEY`) | Via the S3 `deploy/` subdir (decision #16), or `scripts/init-headplane.sh` mints one |
| DNS provider credentials | `global_settings.ha.dns.*` (encrypted) | In the DB, so it follows a DB failover; readable only where `SKYGATE_SECRET_KEY` matches |
| `/etc/skygate/*` state files | per host | Not synced; regenerate via `skygate cluster join` / the bootstrap |

---

## 6. Bringing up / draining a host

### 6.1 Checklist before bringing up a host

- [ ] Same OS + Docker/docker-compose-plugin as the existing node (Ubuntu 22.04+).
- [ ] Tailscale joined, subnet routes **advertised and approved**, bidirectional `tailscale ping` works (Phase 0), node tagged (`tag:dev-<operator>-<host>`).
- [ ] SSH from the primary works; Patroni + etcd reachable (same etcd cluster).
- [ ] `.env` copied and edited: `SKYGATE_HA_ENABLED=true`, `SKYGATE_HA_ROLE=standby`, `HEADPLANE_HEADSCALE__API_KEY` non-empty, and the **same** `SKYGATE_JWT_SECRET` / `SKYGATE_SECRET_KEY` as the primary.
- [ ] `SKYGATE_HA_DEPLOY_S3_BUCKET` set on the host doing `skygate deploy` (else `ErrNoS3Config`); certsync bucket set on every node.
- [ ] Preauth key minted by `deploy/scripts/create-standby-preauth.sh`; patience for a first `tailscale up` (60+ min on slow NAT — use 24 h preauth keys).

Then run §2.4, followed by `scripts/ha-phase7.sh`, `scripts/ha-status.sh` and
`skygate cluster nodes`.

### 6.2 Checklist after bringing up a host

- [ ] `curl -fsS http://127.0.0.1:8080/healthz` → 200; `/readyz` → `"db":"ok"`; `docker ps` shows skygate + headscale (+ headplane) up.
- [ ] Host present in `global_settings.ha_chain` (or added via `/admin/ha` §3) with the right priority + `public_ip`; `skygate deploy status` matches the primary's build.
- [ ] PG replica streaming (`check_pg_health.sh` → 1 primary, ≥1 replica, lag ≈ 0).
- [ ] `cat /var/lib/skygate/certs/.version` matches `s3://<S3_BUCKET>/ha/certs/.version`; `sha256sum /var/lib/skygate/certs/cert.pem` matches its `sha256`.
- [ ] `ha.bootstrap` (and, for cluster joins, `node_init`) rows visible in `/admin/audit` / `cluster_audit`; `GET /metrics` reachable if scraped.

### 6.3 Draining a host

Two auditable steps: `state=draining` first (row kept for inspection), removal
second — one transaction with two `cluster_audit` rows (`node_drain`,
`node_leave`).

- **UI** (`/admin/cluster`): **Drain** marks `state=draining` and stops refreshing
  the heartbeat; **Drain & Remove** does drain-then-delete. Approving a
  non-`pending` node is refused on purpose.
- **Rolling upgrade** (`skygate cluster upgrade --target=<host>` or `--all`): the
  orchestrator marks the target `state=draining`; **you** push the binary
  (`skygate deploy push` from the workstation, then `skygate deploy pull` on the
  node); it polls the node's `/healthz` for up to **5 minutes** for the new build;
  on success it rejoins (`draining → ready`) with a rejoin `cluster_audit` row, on
  failure the node stays `draining`; the self-node is skipped.
- **Before:** the surviving node is ready and can carry the load; `skygate deploy
  status` is in sync; no migration/failover run is in flight; the chain has an
  alive lower-priority member; if removing the **active** member, either disable
  auto-failover first or accept promotion within 5 s.
- **After:** `cluster_node` shows the intended state and both `node_drain` /
  `node_leave` rows exist; `/admin/ha` shows exactly one `active`; `curl -fsS
  https://<PUBLIC_HOST>/readyz` → `"healthy":true`; `dig +short <PUBLIC_HOST>`
  returns the surviving host; if a PG primary was drained, the replica was promoted
  and `check_pg_health.sh` is green; to bring the host back, re-run the bootstrap
  and **Approve**/**Rejoin** it — the design never silently flips a
  `failed`/`draining` node back.

> **`--force-recreate` is required** after any `extra_hosts` / volume / network
> change: a plain `docker compose restart` re-loads the binary but keeps the old
> container network config. And `docker compose` interpolation is **CWD-relative**
> — run it from the project directory (`cd /home/admin/skygate && sudo docker
> compose …`) or pass `--env-file`, or `${VAR:-default}` silently resolves to the
> default.

### 6.4 Recovering a host whose Tailscale iptables rules ate the network

Symptom: the host is up (headscale may even show it `online=true` with a stale
`last_seen`) but SSH/ICMP time out, and the console is the only way in after a
`--netfilter-mode=off` session left a `ts-input` chain with a REJECT policy.

```bash
# From the console
systemctl stop tailscaled
pkill -9 tailscaled
iptables -F; iptables -X ts-*        # repeat across filter/nat/mangle as needed
iptables -P INPUT ACCEPT
systemctl start tailscaled
tailscale up --netfilter-mode=nodir --login-server=https://head.example.com ...

# If `tailscale up` hangs at "regen=true but server says NodeKeyExpired",
# the local state still carries a deleted node's machine key:
rm /var/lib/tailscale/tailscaled.state
rm -rf /var/lib/tailscale/{files,profile-data}
```

Always use `--netfilter-mode=nodir` going forward (the bootstrap script does).

---

## 7. Known gaps and open questions

### 7.1 The ten open questions from the v1.5.0 tracker

| # | Question | Status | Answer / reason |
|---|---|---|---|
| **Q1** | External DNS provider API credentials | **DONE (v1.5.0)** | Provided 2026-09-07; working shape is mTLS + top-level form fields (never the password inside `input_data`) |
| **Q2** | Provider API IP whitelist — both VMs' public IPs | **DONE (v1.5.0)** | Operator confirmed the whitelist is filled 2026-09-07 |
| **Q3** | Use Tailscale Funnel? | **DONE (v1.5.0)** | Decision #4: **NO** — the tailnet is not reachable that way, and headscale ≠ Tailscale SaaS |
| **Q4** | Create a dedicated `…-ha/` S3 bucket? | **DONE (v1.5.0)** | 2026-08-18: reuse the backup bucket with an `ha/` prefix |
| **Q5** | S3 IAM credentials for the skygate process | **DONE (v1.5.0)** | 2026-08-18: reuse the backup credentials (scoping caveat: G10) |
| **Q6** | Auto-failover default: ON or OFF? | **DONE (v1.5.0)** | Default **ON**, manual override always available |
| **Q7** | Standby hostname in headscale | **DONE (v1.5.0)** | Stable pair `skygate` / `skygate-standby`; rendered here as `<VM_HOST>` / `<VM_HOST_2>` |
| **Q8** | Caddy installation path on the new VM | **REMOVED (v1.5.0)** | Decided on-site during implementation; the question is void |
| **Q9** | Date for the live DR drill | **OPEN** | The drill scripts are **DONE**, but the **live drill on the production pair has still never been run**; it needs a maintenance window (Sunday 03:00 UTC recommended). It blocks nothing else, yet it is the difference between "the failover path is code-complete" and "the failover path is proven" |
| **Q10** | Backup bucket name + IAM for the HA prefix | **DONE (v1.5.0)** | 2026-08-18 — same answer as Q4/Q5 |

### 7.2 Other known gaps

| # | Gap | Status | Reason / impact |
|---|---|---|---|
| G1 | **Phase 0 runner requires `python3`** (parses `tailscale status --json`) | **OPEN** | Without python3, steps 3–4 (`verify_routes_advertised`, `verify_routes_approved`) fail; a `jq` fallback is future work |
| G2 | **etcd has no quorum** (single node) | **OPEN** | No 3rd VM; losing it freezes Patroni (the primary keeps serving, no new leader) |
| G3 | **Providers beyond external/`regapi`** (`cloudflare`, `route53`, `rfc2136`) | **DEFERRED (v1.5.x)** | Interface, factory and reserved names exist; the clients are not written |
| G4 | **Let's Encrypt automatic issuance via DNS-01** | **DEFERRED (v1.5.x)** | The toggle and `dns01_enabled` row store intent only; upload certs manually until it ships |
| G5 | **Full auto-rollback after a failed promotion** | **DEFERRED** | B220 ships one-click operator rollback; automatic rollback was left out so a flapping node cannot fight the operator |
| G6 | **Tier 0.5 Litestream active-router** (`SKYGATE_HA_ROLE=active|passive` middleware, passive banner, POST-503 gating) | **DEFERRED** | Superseded by the PG-based Tier 1; do not look for a passive-mode banner |
| G7 | **Tier 2 active-active / multi-writer** | **DEFERRED (out of scope)** | Two writers → corruption; headscale is single-writer by design. Evaluated and rejected |
| G8 | **Prometheus exporter** | **DONE (v1.5.0+, B226)** | `/metrics` textfmt registered, unauthenticated, same exposure as `/healthz` |
| G9 | **Formal review/approval of the Phase 0.3/0.4 design** (API contracts, bootstrap state machine) | **DONE (v1.5.0, B200–B225) / doc-review OPEN** | The contracts and the state machine shipped; the "reviewed and approved" checkbox was never ticked before the doc was retired — this file replaces it |
| G10 | **S3 IAM scope is broader than the HA prefix** | **OPEN** | Scope the reused backup credentials to `s3://<S3_BUCKET>/ha/*`; not enforced by code |
| G11 | **`bootstrap_standby.sh` writes its `ha.bootstrap` row with SQLite date functions** (`strftime('%s','now')`) | **OPEN** | On a PG-backed cluster the INSERT fails and the script only warns (`audit row insert failed`), so the event can be missing from `audit_log`; step 6's "registers itself in the chain on startup" comment is also inaccurate (membership is operator-edited) |
| G12 | **No second DNS provider and no automated manual fallback** | **OPEN** | If the provider API is unusable, moving the A-record is manual; only provider support can clear the "10 identical wrong requests" lockout |
| G13 | **`init-headplane.sh` external mode needs the operator to paste URL + key** | **DONE (v1.5.0, B151)** | Bundled mode auto-mints (`docker exec headscale apikeys create -e 365d`); external mode prompts on first boot; idempotent (`NEEDS_KEY` gate) |
| G14 | **Standby host viability** — the spare VM was intermittently unreachable (hoster NAT, plus the Tailscale iptables trap) | **OPEN (environmental)** | The pair works over Tailscale; the standby need not be publicly reachable — only the active node holds the DNS record |

---

## 8. Failure playbook

| Symptom | Likely cause | Immediate action | Permanent fix |
|---|---|---|---|
| `/healthz` fails, container restarting | skygate crash / bad build | `docker logs skygate-skygate-1 --tail 30`; `docker compose up -d --no-deps skygate` | Fix the regression; the entrypoint rebuilds each start |
| `/readyz` 503, `"db":"error: …"` | DB unreachable / wrong DSN / PG down | `check_pg_health.sh`; edit the DSN in `/admin/database` (watchdog hot-reloads) | Keep `SKYGATE_DB_DSN` a valid fallback |
| `/readyz` 200 but `headscale: error:` | headscale down/unreachable | Restart headscale; verify `HEADSCALE_URL` | Not gating (`healthy` is DB-only) |
| Clients 502/504 after a failover | DNS still points at the dead node | `dig +short <PUBLIC_HOST>`; update the record or `skygate ha promote` | Fix provider creds/whitelist; keep the 5-min TTL |
| `/admin/ha` shows two `active` members | Split-brain after a healed partition | `skygate ha reclaim` on the higher-priority node | Fix the partition cause; keep the anti-flap rule |
| Roles flipped though both nodes are healthy | Heartbeat blip (3 missed × 5 s) | `tailscale status`; fix connectivity; reclaim | Raise the interval/threshold only if the tailnet is flaky |
| `ha.promote` audited but no promotion | Target is not the Patroni primary | `patronictl -c /etc/patroni.yml list`; fix PG; re-promote | Never bypass the elector |
| `/admin/ha` Test → `auth_error` | Stale provider creds | Re-paste creds in `/admin/ha` §4 | Rotate on a schedule; re-run `b146_regapi_live.sh` |
| Test → `ACCESS_DENIED_FROM_IP` | Public IP not whitelisted (or cached / not saved) | Add `<vm-public-ip>/32`, Save, wait 5–30 min | Keep the whitelist current |
| Test → `IP_EXCEEDED_ALLOWED_CONNECTION_RATE` | Provider rate limit | Wait an hour | Avoid repeated identical requests |
| Persisting `ACCESS_DENIED_FROM_IP` with correct creds | "10 identical wrong requests" sub-limit | Contact provider support — only they can reset it | Use the live-test script's actionable errors |
| Save creds → `db.ErrSecretKeyUnset` | `SKYGATE_SECRET_KEY` missing on that node | Add it, restart skygate | Keep it identical on every host |
| Sessions invalid after failover | `SKYGATE_JWT_SECRET` differs between hosts | Fix `.env` on the wrong host, restart | Treat it + `SKYGATE_SECRET_KEY` as cluster-wide constants |
| headscale rejects `id_token`s after failover | Promoted node generated its own OIDC keypair | Copy `SKYGATE_OIDC_KEY_DIR` from the original host, restart | Replicate the keypair during provisioning |
| Standby serves an expired/old TLS cert | Certsync disabled / S3 unreachable / broken reload callback / corrupt `.version` cache | Check `certsync.pull` rows + `journalctl -u skygate`; `aws s3 ls s3://<S3_BUCKET>/ha/certs/` | Keep certsync enabled everywhere; verify the reload callback |
| Certsync no-ops, logs `.version 404`, or warns "expires within 7 days" | S3 blip / no cert uploaded yet / cert near expiry | Nothing (self-healing) / upload via `/admin/certificates` / renew | Monitor S3; automate renewal (G4) |
| Uploaded cert rejected or HTTPS broken | Cert and key don't match, or the cert is for the wrong FQDN | Live cert untouched; read the alert + audit row; re-upload the pair | Validate the SAN before uploading |
| Caddy reload callback fails | `docker exec skygate-caddy caddy reload` errors | Cert is on disk; reload manually | Fix the container name / callback |
| Chain not loading on one node | `SKYGATE_HA_ENABLED=false` there | Set `true`, restart skygate | Bake it into the node's `.env` template |
| `last_seen_unix` stale on a member | That node's elector stalled | `journalctl -u skygate`; `curl /readyz` | Fix the underlying dependency |
| `skygate deploy push` → `ErrNoS3Config` | `SKYGATE_HA_DEPLOY_S3_BUCKET` unset | Set it in `.env` on the pushing host | Add it to the deploy checklist |
| `deploy pull` brought up a broken build | Regression in the new binary | Roll back (`git checkout <last-good> && make build && …push`, then `pull`) | Smoke-test `/readyz` between nodes |
| Rolling upgrade stuck in `draining`, or Approve refused for a `failed`/`draining` node | New build never appeared on `/healthz` within 5 min; Approve only accepts `pending` | Push the binary and rejoin, or re-run `skygate init` | Push the binary before the upgrade; use **Drain** for maintenance |
| `cluster join` returns 410 / 403 | Invite expired (24 h) / hostname mismatch | Issue a new token for the correct hostname | Keep the TTL short; don't rename hosts |
| New standby invisible to other tailnet nodes | Untagged node + headscale grants (0.29.1 omits untagged nodes from peers' netmaps) | Tag the node (`headscale nodes tag … --force`); restart headscale | Mint keys with `create-standby-preauth.sh` |
| New standby lost SSH / all non-Tailscale traffic | `--netfilter-mode=off` left a `ts-input` REJECT chain | Console recovery (§6.4) | Always use `--netfilter-mode=nodir` |
| `tailscale up` hangs | Old `tailscaled` without `--socket`, or a stale machine key | Kill the old pid / wipe `/var/lib/tailscale` state (§6.4) | Use the systemd unit; delete stale nodes |
| WAL archive failing, disk filling | Object store down / network blip | Restore connectivity; watch the primary's disk | Monitor `wal-g backup-list`; alert on disk > 85 % (`R31`) |
| No `dbmigrate-watchdog: pool swapped…` after a DSN change | Watchdog disabled, or the row was deleted | Edit the DSN again / recreate the row | Keep `cluster_database.current_dsn` the single knob |
| `check_pg_health.sh` exits 2 / 1 | No primary / no replicas / etcd down; or replication lag > 10 s | §4.2 or §4.3; restart etcd if frozen; investigate lag | 3-node etcd quorum (G2); tune `maximum_lag_on_failover` |

---

## 9. Cross-references

**Docs** — [README.md](README.md) (index, reading orders) ·
[architecture.md](architecture.md) · [deploy.md](deploy.md) (initial deploy, env
setup, health gates, install kinds) · [operations.md](operations.md)
(release/deploy/DB-cutover procedures, audit, admin surfaces) ·
[internals.md](internals.md) (package map, invariants, the B-check system) ·
[ROADMAP.md](ROADMAP.md) · [LESSONS.md](LESSONS.md) · [INSTALL.md](INSTALL.md) ·
[UPDATE.md](UPDATE.md) (per-host update state) · [https.md](https.md) (TLS
termination, certs, the OIDC hostname wiring) · [oidc.md](oidc.md) ·
[networking.md](networking.md) (mesh, subnet routes, grants, MagicDNS) ·
[db-schema.md](db-schema.md) (`cluster_*`, `global_settings`, `audit_log`) ·
[acl-rules-reference.md](acl-rules-reference.md) ·
[backup-restore-and-migration.md](backup-restore-and-migration.md) ·
[disaster-recovery.md](disaster-recovery.md) (Tier 0 recovery, RTO/RPO) ·
[derp.md](derp.md) · [headplane.md](headplane.md) ·
[sidecar-mode.md](sidecar-mode.md) · [TELEGRAM.md](TELEGRAM.md) (where failover /
DB-health / certsync alerts land) · [troubleshooting.md](troubleshooting.md) ·
[features.md](features.md) · [api.md](api.md) (`/api/cluster/*`).

**Scripts** — `scripts/ha-phase0.sh` · `scripts/bootstrap_standby.sh` ·
`scripts/ha-phase7.sh` · `scripts/dr_drill.sh` (`--yes`, `--skip-kill-both`,
`--skip-regapi-check`) · `scripts/ha-phase9.sh` (preflight on Phases 0+7, max 2
attempts) · `scripts/ha-status.sh` (`--json`, `--phase <id>`) ·
`scripts/ha-state/state.sh` (lock, crash detection, retry/backoff; needs `jq`) ·
`scripts/init-headplane.sh` · `scripts/b146_regapi_live.sh` ·
`scripts/check_ha_state.sh` (73 contracts) ·
`deploy/scripts/create-standby-preauth.sh` (correct headscale user +
`tag:dev-infra-<host>`) · `deploy/pg-ha/check_pg_health.sh` (exit 0/1/2) ·
`deploy/pg-ha/init-pg-replica.sh` · `deploy/skygate-apply-update.sh` (verify →
atomic swap → restart → rollback).
