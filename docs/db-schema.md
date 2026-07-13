# Database schema

This document lists every table Skygate creates, what each column
means, and which migration introduced it. The schema lives in
`internal/db/migrations*.go` as a chain of `migrateV020` through
`migrateV030`; the chain runs on every start of a v0.7.0+ binary, in
order, and is **idempotent** (every ALTER swallows "column exists"
errors, every CREATE uses `IF NOT EXISTS`).

> The DB file is `skygate.db`, opened with SQLite WAL mode via
> `mattn/go-sqlite3` (CGO). Backups should copy `skygate.db` +
> `skygate.db-wal` + `skygate.db-shm` for a consistent view, or run
> `PRAGMA wal_checkpoint(FULL)` first.

## Migration history

| Version | When | What it added |
|---|---|---|
| v0.20 | 2026-07-08 (refactor v0.6.0) | `exit_servers`, `device_rules`, `acl_snapshots`, `exit_rule_logs` |
| v0.21 | 2026-07-08 | `device_rules.action` (accept/deny), `global_settings` |
| v0.22 | 2026-07-09 (fix) | `device_rules.device_ip` (was missing in fresh DBs — original V022 had a syntax error) |
| v0.23 | 2026-07-09 (fix) | `personal_api_tokens` (was missing in fresh DBs) |
| v0.24 | 2026-07-09 | `exit_servers.ssh_target`, `exit_servers.ssh_key_path` |
| v0.25 | 2026-07-09 (refactor v0.6.0) | `portal_users`, `preauth_keys`, `devices`, `audit_log`, `node_owner_map` (re-declared as canonical; live DBs kept their old copies) |
| v0.26 | 2026-07-09 | `exit_servers.accept_routes` (−1/0/+1) |
| v0.27 | 2026-07-11 | `telegram_alerts` (ring buffer) + `idx_telegram_alerts_unacked` |
| v0.28 | 2026-07-12 (fix) | `device_rules.parent_domain`, `node_owner_map.{username,headscale_user_id,tag,tagged_by_user_id,tagged_at}`, `preauth_keys.headscale_preauth_id` (all backfills) |
| v0.29 | 2026-07-13 | `telegram_bindings` (`chat_id → portal_user`) + `idx_telegram_bindings_user` |
| v0.30 | 2026-07-13 | `portal_users.default_device_node_id`, `portal_users.default_exit_node_id` |

> **Migrating between v0.5.0 and v0.6.0+ on a live DB:** safe. Every
> migration since v0.20 is `IF NOT EXISTS` / swallows duplicate-column
> errors, so you can run a v0.7.0+ binary against a v0.5.0 DB and
> nothing breaks. Verify with
> `sqlite3 skygate.db "SELECT name FROM sqlite_master WHERE type='table';"`
> — you should see all 13 tables.

## Table reference

### `portal_users`

Portal-side users. One row per `skyadmin`, `alice`, etc. Each portal
user has a 1:1 headscale user (linked by `headscale_user_id`).

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `username` | TEXT UNIQUE NOT NULL | login name, also headscale username |
| `password_hash` | TEXT NOT NULL | bcrypt cost 12 |
| `is_admin` | INTEGER NOT NULL DEFAULT 0 | 0 = user, 1 = admin |
| `headscale_user_id` | INTEGER | FK to headscale user.id (set on first login) |
| `created_at` | INTEGER NOT NULL | unix seconds |
| `theme` | TEXT NOT NULL DEFAULT 'linear' | one of: linear / classic / solar / mono |
| `default_device_node_id` | TEXT NOT NULL DEFAULT '' | (v0.30) headscale node_id the user picked as default for `/add_rule` |
| `default_exit_node_id` | TEXT NOT NULL DEFAULT '' | (v0.30) headscale node_id the user picked as default exit-node |

Indexes: implicit on `id` (PK) and `username` (UNIQUE).

### `preauth_keys`

One-time or reusable preauth keys the user has generated. Mirrored
from headscale (the headscale key id is in `headscale_preauth_id`).

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `user_id` | INTEGER NOT NULL | FK → `portal_users.id` |
| `key` | TEXT UNIQUE NOT NULL | the actual preauth string the user pastes into `tailscale up --authkey …` |
| `headscale_preauth_id` | TEXT NOT NULL DEFAULT '' | (v0.28 backfill) headscale-side key id, used by node-ownership backfill |
| `reusable` | INTEGER NOT NULL DEFAULT 0 | 0 = single-use, 1 = reusable |
| `used` | INTEGER NOT NULL DEFAULT 0 | 0 = fresh, 1 = burned |
| `expires_at` | INTEGER DEFAULT 0 | unix seconds, 0 = never |
| `created_at` | INTEGER | unix seconds |

Indexes: implicit on `id` and `key` (UNIQUE).

### `devices`

Cached view of tailnet devices. Refreshed lazily by `GetMyDevices`
through the headscale API; this table is **read-mostly** (the source
of truth is headscale's own DB).

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `user_id` | INTEGER NOT NULL | FK → `portal_users.id` |
| `hostname` | TEXT NOT NULL | human label |
| `node_id` | TEXT DEFAULT '' | headscale node id (TEXT, not int) |
| `headscale_node_id` | TEXT DEFAULT '' | (synonym for `node_id`; legacy) |
| `ip_addresses` | TEXT DEFAULT '' | comma-separated Tailscale IPs |
| `os` | TEXT DEFAULT '' | "linux", "windows", "darwin", "android", "ios" |
| `last_seen` | INTEGER DEFAULT 0 | unix seconds |
| `online` | INTEGER DEFAULT 0 | 0/1 |
| `created_at` | INTEGER | unix seconds |

> **Note:** `node_owner_map` (not `devices`) is the source of truth for
> "which user owns which node". `devices` is a denormalized cache for
> the `/my/devices` page.

### `node_owner_map`

Attribution of headscale nodes to portal users. Built by
`backfillNodeOwnership` (`internal/handlers/handlers_node_ownership.go`).

| Column | Type | Notes |
|---|---|---|
| `node_id` | TEXT PRIMARY KEY | headscale node id |
| `user_id` | INTEGER NOT NULL | FK → `portal_users.id` |
| `username` | TEXT NOT NULL DEFAULT '' | (v0.28 backfill) denormalized for fast list |
| `headscale_user_id` | INTEGER NOT NULL DEFAULT 0 | (v0.28 backfill) denormalized for fast list |
| `tag` | TEXT NOT NULL DEFAULT '' | (v0.28 backfill) headscale tag — `tag:private`, `tag:public`, `tag:exit-node`, or '' (untagged) |
| `tagged_by_user_id` | INTEGER NOT NULL DEFAULT 0 | (v0.28 backfill) admin who applied the tag |
| `attributed_at` | INTEGER | unix seconds |
| `tagged_at` | INTEGER | unix seconds (tag change time) |

Indexes: implicit on `node_id` (PK).

> **Strategy C temporal fallback** (in `backfillNodeOwnership`):
> when a node's `PreAuthKeyID` doesn't match any preauth_key's
> `headscale_preauth_id`, look for a preauth created within 1 hour
> before the node was registered. If found, attribute the node to
> that preauth's user as `tag:private`. Pushes the tag to headscale
> via `HS.TagNode(nodeID, "tag:private")`.

### `exit_servers`

Per-exit-node state. Built by admin at `/admin/exit-nodes` and
consumed by `HS.SetAdvertisedRoutes`.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `node_id` | TEXT NOT NULL UNIQUE | headscale node id |
| `hostname` | TEXT NOT NULL | human label |
| `tailscale_ip` | TEXT NOT NULL DEFAULT '' | Tailscale IP, set after first sync |
| `description` | TEXT DEFAULT '' | free text |
| `enabled` | INTEGER DEFAULT 1 | 0 = paused (no sync, no admin show) |
| `created_at` | INTEGER | unix seconds |
| `ssh_target` | TEXT NOT NULL DEFAULT '' | (v0.24) `user@host` for the SSH sync |
| `ssh_key_path` | TEXT NOT NULL DEFAULT '' | (v0.24) path inside the skygate container |
| `accept_routes` | INTEGER NOT NULL DEFAULT 0 | (v0.26) −1 = false, 0 = unset, +1 = true |

> **Why `accept_routes` matters:** on a node that also runs
> Amnezia-AWG / OpenVPN / WireGuard, setting `--accept-routes=true`
> makes Tailscale pull Google / Telegram into source-routing table 52
> and traffic from the other VPN gets routed to the wrong peer.
> Setting this to -1 on a co-hosted node is the only correct fix.

### `device_rules`

The exit-rules. One row = one allow/deny rule for one (user, device,
exit-node, target) tuple. Deduplicated by `(device_id, target_type,
target_value, action)`.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `user_id` | INTEGER NOT NULL | FK → `portal_users.id` |
| `device_id` | INTEGER NOT NULL | headscale node id (TEXT-as-INT; the column type is INTEGER but it holds the headscale node id cast to int) |
| `exit_node_id` | TEXT NOT NULL | headscale node id of the exit-node |
| `target_type` | TEXT NOT NULL DEFAULT 'domain' | 'domain' or 'ip' |
| `target_value` | TEXT NOT NULL | `google.com` or `142.250.190.46` |
| `action` | TEXT NOT NULL DEFAULT 'accept' | 'accept' or 'deny' |
| `enabled` | INTEGER DEFAULT 1 | 0 = disabled (not synced to headscale) |
| `created_at` | INTEGER | unix seconds |
| `device_ip` | TEXT NOT NULL DEFAULT '' | (v0.22 backfill) the device's Tailscale IP at rule-creation time, for the headscale ACL `dst` |
| `parent_domain` | TEXT NOT NULL DEFAULT '' | (v0.28 backfill) the domain this /32 was derived from, set by `RunDomainAutoUpdater` |

Indexes: implicit on `id`; `(device_id, target_type, target_value, action)` is the natural dedup key (enforced in `InsertRuleUnique`).

### `acl_snapshots`

Every ACL policy we ever sent to headscale, with a version number
(monotonic, per Apply). Used for `/admin/exit-rules/rollback`.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `version` | INTEGER NOT NULL | monotonic; v1, v2, v3… |
| `config` | TEXT NOT NULL | the JSON policy as a string (BLOB-encoded in Go) |
| `created_by` | TEXT NOT NULL | portal username |
| `applied_success` | INTEGER DEFAULT NULL | 1 = applied, 0 = failed, NULL = not yet attempted |
| `error_msg` | TEXT DEFAULT '' | on failure, the SetPolicy error body (truncated) |
| `created_at` | INTEGER | unix seconds |

Indexes: implicit on `id`; queries are usually `ORDER BY id DESC LIMIT 1`.

### `exit_rule_logs`

Append-only log of every exit-rule mutation. Used by the audit page
filter and by `/admin/audit`.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `version` | INTEGER NOT NULL | the ACL version this action is part of |
| `action` | TEXT NOT NULL | 'create' / 'delete' / 'bulk_add' / 'rollback' / 'cleanup' |
| `detail` | TEXT DEFAULT '' | free-form description |
| `created_at` | INTEGER | unix seconds |

> **Schema is append-only.** Don't drop old rows from a live DB
> without first archiving them — `/admin/audit` reads them.

### `audit_log`

Broader audit: login attempts, password resets, ACL changes,
telegram_ack, etc. Independent of `exit_rule_logs` because some
actions touch neither exit-rules nor ACLs.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `user_id` | INTEGER DEFAULT 0 | 0 = anonymous (e.g. failed login) |
| `username` | TEXT DEFAULT '' | denormalized for fast display |
| `action` | TEXT NOT NULL | 'login' / 'login_fail' / 'password_reset' / 'logout' / 'telegram_ack' / 'restart' / … |
| `detail` | TEXT DEFAULT '' | JSON or free-form |
| `ip_address` | TEXT DEFAULT '' | client IP at the time |
| `created_at` | INTEGER | unix seconds |

Indexes: implicit on `id`; `/admin/audit?action=…&user=…` filters on
`action` and `username` (no index — fine for now, dataset is small).

### `personal_api_tokens`

Bearer-auth tokens for the public REST API at `/my/exit-rules/api`.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `user_id` | INTEGER NOT NULL | FK → `portal_users.id` |
| `token_hash` | TEXT NOT NULL UNIQUE | sha256 of the actual token (the cleartext is only shown once at creation) |
| `label` | TEXT NOT NULL DEFAULT '' | human description, e.g. "OpenCode CLI" |
| `last_used_at` | INTEGER DEFAULT 0 | unix seconds |
| `created_at` | INTEGER | unix seconds |

Indexes: implicit on `id` and `token_hash` (UNIQUE).

### `global_settings`

Key/value bag for app-wide config. Currently holds Telegram
credentials (the canonical source — `.env` is only the bootstrap).

| Column | Type | Notes |
|---|---|---|
| `key` | TEXT PRIMARY KEY | |
| `value` | TEXT NOT NULL DEFAULT '' | |
| `updated_at` | INTEGER | unix seconds |

Known keys: `exit_policy` (default 'allow_all'), `telegram.token`,
`telegram.chat_id`.

### `telegram_alerts`

Ring buffer of every alert Skygate sent. Cap 500 rows; prune
fire-and-forget on each insert.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | the `id` we prefix as `[#<id>]` in the Telegram message |
| `body` | TEXT NOT NULL | the full alert text |
| `sent_at` | INTEGER | unix seconds |
| `acked_at` | INTEGER NOT NULL DEFAULT 0 | 0 = open, >0 = acked (unix seconds) |
| `acked_by` | TEXT NOT NULL DEFAULT '' | who ran `/ack <id>` |

Indexes: `idx_telegram_alerts_unacked` (partial, `WHERE acked_at = 0`).

### `telegram_bindings`

Per-chat → portal-user binding. Replaces the implicit "one chat_id
in global_settings is admin" model.

| Column | Type | Notes |
|---|---|---|
| `chat_id` | INTEGER PRIMARY KEY | Telegram chat id (negative for groups) |
| `portal_user_id` | INTEGER NOT NULL | FK → `portal_users.id` |
| `is_admin` | INTEGER NOT NULL DEFAULT 0 | denormalized from `portal_users.is_admin` at bind time |
| `bound_at` | INTEGER | unix seconds |
| `bound_by_user_id` | INTEGER NOT NULL DEFAULT 0 | admin who created the binding, 0 for system |

Indexes: `idx_telegram_bindings_user` (on `portal_user_id`).

Cascade on user-delete: `DeleteTelegramBindingsByUserID(uid)` in
`internal/db/telegram_bindings.go`.

## Schema invariants (read before editing)

1. **`GenerateACL()` is the only writer of `acl_snapshots.config`.**
   Don't write to it from anywhere else. The version number comes from
   the previous snapshot's version + 1.
2. **`node_owner_map.tag` is the source of truth for "who owns this
   node in the portal UI"**, but the headscale side has its own tag
   state. `backfillNodeOwnership` reconciles them; manual
   `HS.TagNode` calls should be followed by an `UPDATE node_owner_map
   SET tag=…` to keep the local view in sync.
3. **Don't drop `exit_rule_logs` rows.** The audit page reads them.
4. **Don't rename `parent_domain`** — `RunDomainAutoUpdater` looks
   it up by name.
5. **`acl_snapshots.config` is a TEXT column but the Go side encodes
   JSON as bytes.** `db.SaveACLSnapshot` calls `string(jsonBytes)`
   before INSERT. To read: `[]byte(row.Config)`.

## Quick verification queries

```sql
-- 1. All tables
SELECT name FROM sqlite_master WHERE type='table' ORDER BY name;

-- 2. Active exit-rules per user
SELECT u.username, COUNT(*) AS n
FROM device_rules r JOIN portal_users u ON r.user_id = u.id
WHERE r.enabled = 1
GROUP BY u.username ORDER BY n DESC;

-- 3. Last applied ACL
SELECT id, version, created_by, applied_success, created_at
FROM acl_snapshots ORDER BY id DESC LIMIT 5;

-- 4. Unacked Telegram alerts
SELECT id, body, sent_at FROM telegram_alerts
WHERE acked_at = 0 ORDER BY id DESC LIMIT 20;

-- 5. Portal users with no headscale_user_id (bootstrap-broken)
SELECT id, username FROM portal_users WHERE headscale_user_id IS NULL;

-- 6. Nodes without an owner
SELECT n.id, n.hostname FROM devices n
LEFT JOIN node_owner_map m ON n.node_id = m.node_id
WHERE m.node_id IS NULL;
```

## See also

- [docs/architecture.md](architecture.md) — request flow + goroutines
- [docs/api.md](api.md) — every endpoint that reads/writes these tables
- `internal/db/queries.go` — every SQL string the app uses
- `internal/db/migrations.go` + `migrations_v0.20..v0.30.go` — schema
  source of truth
