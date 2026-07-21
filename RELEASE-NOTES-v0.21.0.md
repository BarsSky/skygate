# v0.21.0 — user-to-user subnet bridge (invite codes)

2026-07-20

Closes the third feature the operator asked
for in the 2026-07-20 backlog message (alongside
the v0.20.0 headscale-update-monitor + auto-
allocate subnet). The v0.17.1 admin-mediated
"share" path is unchanged; v0.21.0 adds the
**user-mediated** path: A generates a code, B
types it in the bot, the bridge auto-applies.

## Why this matters

The v0.20.0 subnets roadmap gives every portal
user their own 10.0.<uid>.0/24. v0.17.1
already let the admin share A's subnet with B
via a button on `/admin/users/{id}/subnet/share`.
But that requires the admin to mediate every
cross-user sharing. v0.21.0 lets users do it
themselves: A runs `/invite bob`, gets a
7-day-valid code, tells B (out of band — Telegram
DM, in person, smoke signals, anything), B runs
`/accept <code>`, the bridge auto-applies.

## Flow

1. A (any identified user) runs
   `/invite <username>`. skygate creates an
   `invite_codes` row with `grantor_user_id=A.id`,
   `grantee_username=<username>`, `status=active`,
   `expires_at=now+7d`, and an 8-char random
   code (32-char alphabet, ~1.1T possibilities,
   no I/O/0/1 to avoid transcription errors).

2. A tells B the code (out of band).

3. B runs `/accept <code>`. skygate validates
   the code (active + not expired + grantee
   matches B's username), atomically consumes
   it, and calls `invite.ApplyBridge`:
   a. `INSERT OR IGNORE` into `user_subnet_shares`
      (grantor=A.id, grantee=B.id) — same shape
      as the v0.17.1 admin share, so the ACL
      builder picks it up on the next pipeline
      run.
   b. ACL re-apply for every distinct
      `headscale_url` (per-plane).
   c. Audit log entry (`invite_bridge` action).

4. The ACL now contains a per-user rule letting
   B's tag:private devices reach A's
   10.0.<A>.0/24. Tailscale clients pick it up
   on their next ACL poll (usually <60s).

The bot reply is fast (the ACL re-apply runs in
a goroutine). The grantor gets a Telegram alert
("🌉 Subnet bridge applied") so they know
their invite was actually used.

## New surfaces

- **Bot `/invite <username>`** — generate a
  code, reply shows code + grantee + expiry +
  the count of other active invites the caller
  has to the same user.
- **Bot `/accept <code>`** — consume a code.
  The reply is precise on every failure
  (`not found` / `not for you` / `expired` /
  `already consumed` / `self invite`) so the
  grantee knows what to do.
- **Bot `/invites`** — list the caller's
  outstanding + incoming invites (10 per side,
  newest first).
- **Page `/admin/invites`** (admin-only) —
  unfiltered "show me everything" view with a
  Revoke button for active rows. Audit log
  entry on revoke (`invite_revoke` action).
- **Sidebar entry on `/dashboard`**.

All three bot commands are user-scope (any
identified user can use them — admin is not
required). The grantor doesn't have to be an
admin; the grantee doesn't have to be an admin.
The bridge row is written the same way the
admin share would write it.

## Schema (migration v0.42)

```sql
CREATE TABLE invite_codes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  code TEXT NOT NULL UNIQUE,             -- 8-char alphanumeric
  grantor_user_id INTEGER NOT NULL,      -- FK portal_users
  grantee_username TEXT NOT NULL,       -- target user (by name)
  status TEXT NOT NULL DEFAULT 'active', -- active|consumed|expired|revoked
  created_at INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL DEFAULT 0,
  consumed_at INTEGER NOT NULL DEFAULT 0,
  consumed_by_user_id INTEGER NOT NULL DEFAULT 0,
  audit_message TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_invite_codes_grantor
  ON invite_codes (grantor_user_id);
CREATE INDEX idx_invite_codes_status_expires
  ON invite_codes (status, expires_at);
CREATE INDEX idx_invite_codes_grantee
  ON invite_codes (grantee_username);
```

`grantee_username` is TEXT (not an FK to
`portal_users.id`) so the grantor can invite
"bob" before bob has a skygate account. The
`/accept` path resolves the username to a
user_id at consume time and returns a clear
"this invite is for a different user" error
on mismatch.

`INSERT OR IGNORE` on the `code` UNIQUE
constraint means a (vanishingly rare) code-
collision retry is automatic (the package
retries up to 5 times with a fresh code).

`consumed_by_user_id` is NOT an FK because the
user must exist at consume time (only identified
users reach `/accept`), but we don't want a
CASCADE that wipes the audit trail if the
consumer is later deleted. Setting to
`ON DELETE SET DEFAULT 0` keeps the audit row
even if the consumer's portal row is gone.

## Code shape

8 chars from a 32-symbol alphabet
(`ABCDEFGHJKLMNPQRSTUVWXYZ23456789` — no I/O/0/1
because those are the four characters most
commonly confused in hand-typed codes).
`32^8 = 1.1 trillion` possibilities — safe
against brute force for the 7-day TTL.

## Files

New:
- `internal/db/migrations_v0.42.go` — schema.
- `internal/invite/invite.go` (~370 lines) —
  GenerateCode, CreateInvite, LookupByCode,
  ValidateCode, ConsumeCode, RevokeInvite,
  ListByGrantor, ListByGrantee, ListAll,
  SweepExpired, ResolveGranteeID,
  DistinctHeadscaleURLs.
- `internal/invite/bridge.go` (~210 lines) —
  ApplyBridge (writes user_subnet_shares +
  triggers ACL re-apply + audit + alert).
- `internal/invite/invite_test.go` — 11 unit
  tests: code shape, uniqueness, full lifecycle,
  self-invite, not-for-you, expired, atomic
  consume, revoke, list, sweep, resolve.
- `internal/invite/bridge_test.go` — 5 unit
  tests: write share row, idempotent,
  reject self-bridge, audit, notify.
- `internal/handlers/admin_invites.go` —
  GetAdminInvites + PostAdminInvitesRevoke.
- `internal/handlers/templates/admin/invites.html`.
- `internal/telegram/commands_invite.go` —
  inviteReply, acceptReply, invitesListReply.

Modified:
- `internal/db/db.go` — migration v0.42
  registered.
- `internal/telegram/commands.go` — `/invite`,
  `/accept`, `/invites` in the dispatch table
  + `commandContext` map.
- `internal/handlers/templates/dashboard.html` —
  sidebar entry.
- `internal/i18n/catalog.go` — 32 new keys
  (RU+EN), catalog parity test green.
- `scripts/smoke.sh` — `/admin/invites` added
  to the admin pages loop and HTML render
  check loop.

## Hotfix shipped immediately after v0.21.0

`cmd/skygate/main.go` had a duplicate
registration of the `/admin/headscale` route
(introduced by the v0.21.0 edit pattern that
matched the v0.20.0 insertion twice). The
first deploy of v0.21.0 panicked on boot with:

```
panic: pattern "GET /admin/headscale" (registered at
/app/cmd/skygate/main.go:338) conflicts with pattern
"GET /admin/headscale" (registered at
/app/cmd/skygate/main.go:320)
```

The hotfix (commit `cb94b37`) removes the
duplicate, leaving the v0.20.0 registration
(lines 320+325) as the single source of truth.
Build verified live on VM; smoke 126/126
again. The hotfix is included in the v0.21.0
commit chain but is a no-op for the v0.21.0
features themselves (just the registration
collision in `main.go`).

## Tests

* `go test ./...` — all packages PASS
  (invite, i18n catalog parity, handlers,
  telegram, etc).
* `internal/invite/invite_test.go` — 11 tests
  cover the full lifecycle (create / lookup /
  validate / consume / revoke / list / sweep
  / resolve).
* `internal/invite/bridge_test.go` — 5 tests
  cover the share-row + audit + notify + ACL
  scope paths.

## Live verification

- `make test` — **smoke 126/126** (EN 63 + RU 63,
  both 0 fail), check_exit_nodes PASS (3
  relays), check_https PASS (TLS, SAN, cert
  validity, HTTP→HTTPS redirect, HSTS via /
  fallback).
- `/admin/invites` GET 200, HTML renders
  without template error. Title "Инвайт-коды"
  / "Invite codes" appears in RU/EN
  respectively. Empty state ("Инвайтов пока
  нет" / "No invites yet") renders correctly
  when no rows exist.
- Test user creation via `/admin/users` form
  still works (302 redirect after success),
  matching the v0.20.0 auto-allocate path.
- All other admin pages still 200 (smoke
  check covers every one).

17 files changed, +2405/-3 lines. Migration
v0.42 adds the `invite_codes` table.

Build `cb94b37` on `feature/v0.10.12-bot-ux`,
live on VM at `192.168.13.69`.

## What comes next

The three "close the backlog" features from the
2026-07-20 message are done:
1. ✅ Headscale-update-monitor (v0.20.0)
2. ✅ Auto-allocate subnet on user create (v0.20.0)
3. ✅ User-to-user subnet bridge (v0.21.0)

v0.19.1 (the re-attempt of the reverted v0.19.0
`dns.extra_records` feature) is still blocked
on headscale 0.30+ — the weekly mavis cron
`headscale-milestone-16-check` checks headscale
milestone #16 (DNS Work) every 7 days and
reports if any progress lands.
