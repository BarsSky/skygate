# v0.16.6 — Per-user subnets foundation

2026-07-17

The first step of the v0.16.6+ per-user subnets roadmap.
This release ships the **foundation** — schema, CIDR
allocator, CRUD layer, admin UI, bot command. The
actual sidecar container management (start tailscaled,
tag the headscale node, approve routes) is the
v0.16.7 follow-up.

## What changed

### 1. `user_subnets` table + 3 denormalized columns

`internal/db/migrations_v0.38.go` adds:

- The `user_subnets` table — one row per portal user
  who has opted in to a personal subnet. Unique
  constraints on `user_id` and `cidr` make the DB the
  source of truth for "who owns which CIDR".
- 3 denormalized columns on `portal_users`
  (`subnet_cidr`, `subnet_status`,
  `subnet_router_node_id`) — quick-lookup copies
  used by `/mysubnet` and `/admin/users/{id}` without
  a JOIN. Kept in sync by the manager.

Both pieces are covered by `TestHTMLSafeCatalog`
(actually the v0.16.4 test) for the literal `<` /
`>` HTML-safety, plus the new manager tests for
the denorm-in-sync contract.

### 2. `internal/subnet/` package — allocator + manager

| File | Purpose |
|------|---------|
| `allocator.go` | Pure-function CIDR assignment: `AllocateCIDR(userID) → "10.0.<uid>.0/24"`. Deterministic + idempotent + capped at 256 users. |
| `manager.go` | CRUD: `Create` (idempotent, atomic), `Get`, `List`, `ListByStatus`, `SetStatus` (lifecycle), `SetRouter` (v0.16.1 stub). All ops keep denorm columns in sync. |
| `allocator_test.go` | Unit tests: valid range, out-of-range, idempotent, output-is-parseable. |
| `manager_test.go` | Integration tests: create+get, duplicate, user-not-found, get-not-found, status lifecycle, list, router stub. |

The schema column `subnet_bits` is reserved for a
future migration to `/28` per user (4096 users). The
v0.16.6 release uses `/24` per the operator's
decision on 2026-07-17.

### 3. `control_plane_url` column

`user_subnets.control_plane_url TEXT NOT NULL DEFAULT ''`
— the per-plane context (v0.12.0 multi-plane). The
admin page uses the user's existing `headscale_url`
override; empty string = global plane. The column
exists from v0.16.6 so v0.13.0's per-plane ACL
generation can scope subnet rules to the right plane.

### 4. Bot `/mysubnet` command

`internal/telegram/commands_user.go` adds
`mySubnetReply(env)`. The reply reads the denormalized
columns (no JOIN) and shows:

- the user's CIDR (e.g. `10.0.42.0/24`)
- the status (`pending` / `active` / `disabled`)
- the router hostname (or "not yet provisioned" while
  pending)
- the per-plane context (URL or "global")
- a "v0.17.1" placeholder for cross-user sharing
  (empty in v0.16.6)

The reply uses the v0.16.3+ HTML helpers
(`Field()` / `Section()`) so the table is rendered
cleanly on mobile. `markHTMLReply()` sets
`parse_mode=HTML`. Two new tests
(`TestMySubnetReplyEmpty` + `TestMySubnetReplyAllocated`)
pin the empty/allocated cases.

### 5. Admin page `/admin/users/{id}/subnet`

`internal/handlers/admin_user_subnet.go` adds:

- `GET /admin/users/{id}/subnet` — page with status
  table + actions
- `POST /admin/users/{id}/subnet/allocate` — creates
  the row in `pending` state (idempotent)
- `POST /admin/users/{id}/subnet/disable` — transitions
  to `disabled` (manual opt-out)
- `POST /admin/users/{id}/subnet/test` — runs a sanity
  check (denorm-in-sync + status validity)

The "Sanity check" button catches the most likely
v0.16.6 bug: a future migration that updates one table
but forgets the other. The test compares `user_subnets`
against `portal_users` denorm columns and reports any
discrepancy.

5 new tests in `admin_user_subnet_test.go` pin the
admin flow end-to-end (allocate / disable / test /
forbidden / no-subnet).

## v0.16.6 release scope (what's NOT here)

Per the operator's decisions in
`docs/v0.16.0-open-questions.md`, v0.16.0 is the
foundation. The following is **explicitly deferred**:

| Component | Ships in |
|-----------|---------|
| Real sidecar container management | v0.16.1 |
| headscale preauth key issuance | v0.16.1 |
| headscale node tagging + route approval | v0.16.1 |
| `tag:subnet-router` in `tagOwners` ACL | v0.17.0 |
| Cross-user sharing (IP-level) | v0.17.1 |
| Cross-user sharing (DNS service records) | v0.19.0 |

The operator can verify v0.16.0 by:
1. /admin/users/{id}/subnet → "Allocate subnet"
2. /admin/users/{id}/subnet → "Sanity check" — should
   report all green
3. /mysubnet (Telegram bot) — should show the user's
   CIDR + `pending` status

## Tests

- 1 new schema migration (`v0.38`)
- 2 new Go packages (`internal/subnet` allocator + manager)
- 4 new test files (allocator + manager + admin + bot)
- 16 new test functions
- All existing tests still pass
- 12/12 packages green, smoke 118/118 expected on VM

## Operator impact

After `docker compose up -d --force-recreate --no-deps
skygate`:

- Migration `v0.38` adds the `user_subnets` table +
  3 portal_users columns. Idempotent on re-run
  (ALTER TABLE ADD COLUMN with DEFAULT doesn't fail
  on duplicate column)
- A new admin page at `/admin/users/{id}/subnet`
  lets you provision a personal subnet for any user
  (idempotent: clicking "Allocate" twice is safe)
- A new bot command `/mysubnet` shows the user's
  personal subnet
- No breaking changes — existing users see empty
  subnets (denorm columns default to '' / 'none')
- v0.16.1 work adds the actual sidecar container
  provisioning (start tailscaled, issue preauth,
  tag the node). After v0.16.1 ships, clicking
  "Allocate" will provision a real sidecar.

## Files

**New**:
- `internal/db/migrations_v0.38.go`
- `internal/subnet/allocator.go`
- `internal/subnet/manager.go`
- `internal/subnet/allocator_test.go`
- `internal/subnet/manager_test.go`
- `internal/handlers/admin_user_subnet.go`
- `internal/handlers/admin_user_subnet_test.go`
- `internal/handlers/templates/admin/user_subnet.html`
- `docs/v0.16.0-open-questions.md` (updated with the
  8 confirmed decisions)
- `RELEASE-NOTES-v0.16.0.md` (this file)

**Modified**:
- `internal/db/db.go` — register migration v0.38
- `internal/i18n/catalog.go` — 16 new keys (8 RU + 8 EN)
- `internal/telegram/commands.go` — dispatch `/mysubnet`
- `internal/telegram/commands_user.go` — `mySubnetReply()`
- `internal/telegram/commands_test.go` — 2 new tests
- `internal/handlers/handlers_my_telegram_test.go` —
  test schema (portal_users denorm + user_subnets)
- `cmd/skygate/main.go` — register 4 new admin routes
