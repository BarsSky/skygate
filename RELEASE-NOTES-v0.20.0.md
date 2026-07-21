# v0.20.0 — headscale-update-monitor + auto-allocate subnet

2026-07-20

Two operator-side UX cleanups bundled because
they're both small and the operator asked for
both in the same message on 2026-07-20 (right
after the headscale 0.29.1 → 0.29.2 upgrade).

## 1. `headscale-update-monitor` — the operator
   no longer has to GitHub-stalk headscale

The pre-v0.20.0 workflow for "is there a newer
headscale than what I run?":

```bash
# On the operator's laptop, every few days:
curl -s https://api.github.com/repos/juanfont/headscale/releases/latest | jq -r .tag_name
# Then mentally compare to whatever they pinned
# in their compose file
```

Plus a Tailscale user would occasionally hit a
warning in the skygate log if a critical
headscale version shipped, but it was operator-
luck whether the operator saw it in time.

The fix: a **background goroutine** in skygate
polls the GitHub Releases API every 24h
(configurable), compares the latest tag against
the operator's pinned version, and surfaces the
result on three channels:

  1. **`/admin/headscale`** — new admin page
     (admin-only). Shows: pinned version, latest
     GitHub release, a status pill
     (`up to date` / `update available` /
     `⚠️ breaking change` / `service disabled`),
     a "Check now" button for an immediate
     re-poll, and a full history table of the
     last 20 releases the monitor has seen
     (newest first). Setup instructions at the
     bottom of the page explain the two new env
     vars.
  2. **Banner on `/admin/exit-nodes`** — when
     the monitor knows about a newer release,
     a coloured banner appears above the
     exit-nodes table (red for breaking changes,
     amber for patches). The banner links to
     `/admin/headscale` for the full picture.
  3. **Bot `/headscale`** (admin-only) — the
     same status as the page, formatted for
     Telegram HTML. Useful for the operator
     who doesn't have a browser handy.

When a newer release is detected, the monitor
also dispatches a **Telegram alert** (calm mode,
via the existing `Notifier.SendAlert`):
- `🔔 Headscale update available` (patch)
- `⚠️ Headscale update available` (breaking,
  major/minor bump per semver 11.4.4)

The alert includes a 800-char changelog preview
and a link to the GitHub release. The
`Notifier.SendAlert` returns the `telegram_alerts.id`,
which the operator can `/ack` later if they want
a record.

A **dedup map** (keyed by tag) ensures the
monitor doesn't spam the admin with the same
release N times across N hourly ticks if the
first send silently failed. The map resets when
the operator changes `SKYGATE_HEADSCALE_VERSION_PIN`
(e.g. after an upgrade).

### Schema (migration v0.41)

New `headscale_releases` table:

```sql
CREATE TABLE headscale_releases (
  version TEXT PRIMARY KEY,
  published_at INTEGER NOT NULL DEFAULT 0,
  first_seen_at INTEGER NOT NULL DEFAULT 0,
  html_url TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  is_breaking INTEGER NOT NULL DEFAULT 0,
  notified INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_headscale_releases_published
  ON headscale_releases (published_at DESC);
```

`INSERT OR IGNORE` on the primary key means a
re-poll from a different pinned version doesn't
create duplicates. The history view on
`/admin/headscale` reads the most recent 20
rows.

### Config

Two new env vars in `.env`:

```
SKYGATE_HEADSCALE_VERSION_PIN=0.29.2
SKYGATE_HEADSCALE_POLL_INTERVAL=24h
```

- `SKYGATE_HEADSCALE_VERSION_PIN` — the
  operator's currently-running headscale
  version. Empty = observe-only mode (the page
  and bot still work, no alerts). Update this
  after every headscale upgrade to silence the
  banner.
- `SKYGATE_HEADSCALE_POLL_INTERVAL` — default
  24h. Set to `off` or `0` to disable the
  goroutine entirely (the page and bot still
  work from the cache; "Check now" still
  works).

### Rate limiting

The monitor hits `https://api.github.com/repos/juanfont/headscale/releases/latest`
once per `SKYGATE_HEADSCALE_POLL_INTERVAL`. At
the default 24h, that's 1/60 of GitHub's
unauthenticated 60 req/h budget — leaves
56/60 unused for any concurrent `curl` from
the operator.

### Files

* `internal/headscale_version/{client,monitor,monitor_test}.go`
  (new, ~250 lines) — sibling of
  `internal/release/` (which monitors skygate
  itself). Structurally similar but with a
  different default cadence (24h vs 1h), a
  separate DB table (one row per release, not
  log-only), and a `BreakingAvailable` flag
  (the skygate monitor doesn't care about
  semver).
* `internal/db/migrations_v0.41.go` (new) —
  `headscale_releases` table.
* `internal/handlers/admin_headscale.go` (new) —
  `GetAdminHeadscale` + `PostAdminHeadscaleCheckNow`.
* `internal/handlers/templates/admin/headscale.html`
  (new) — the page template.
* `internal/handlers/admin_exit_nodes.go` —
  banner data + 5 small helpers
  (`headscaleUpdateForBanner`,
  `headscaleBreakingForBanner`, etc.) that the
  template calls as single conditions.
* `internal/handlers/templates/admin/exit_nodes.html`
  — the banner block.
* `internal/handlers/templates/dashboard.html`
  — sidebar entry.
* `internal/telegram/commands_headscale.go` (new) —
  `headscaleReply` formatter (Field-style key/value
  layout, HTML, last 3 history rows).
* `internal/telegram/{commands,notify}.go` —
  `/headscale` in the dispatch table + `adminOnly`
  gate, `BotEnv.HeadscaleUpdateMonitor` field,
  `RealNotifier.SetHeadscaleUpdateMonitor()` setter.
* `internal/i18n/catalog.go` — 50 new keys
  (RU+EN), catalog parity test green.
* `cmd/skygate/main.go` — `hsMon` constructed
  after the existing monitors, goroutine
  started via `hsMon.Start(ctx)`, `app.HeadscaleUpdateMonitor`
  + `rn.SetHeadscaleUpdateMonitor(hsMon)`
  set. The `rn` variable was hoisted out of
  the Telegram block (was anonymous `{ }`
  before) so the cross-block setter works.
* `scripts/smoke.sh` — added `/admin/headscale`
  to the admin pages loop and the HTML render
  check loop (2 new PASS assertions per
  language).

### Tests

* `internal/headscale_version/monitor_test.go` —
  9 unit tests: `CompareSemver` (16 cases
  including pre-release), `IsBreaking`,
  `FormatAlert` (patch / breaking / truncation),
  `TestRepoURL` (URL contract drift guard),
  `TestMonitorTickUpdatesSnapshotForNewerVersion`,
  `TestMonitorTickNoAlertWhenSameVersion`,
  `TestMonitorDedup`.
* `go test ./...` — all packages PASS
  (`headscale_version`, `i18n` catalog parity,
  `acl`, `db`, `handlers`, `telegram`, etc).

## 2. Auto-allocate subnet on user create —
   closes the v0.16.6 "extra click" wart

The pre-v0.20.0 workflow for a new portal user
to get a personal subnet:

1. Create the user on `/admin/users` (form
   POST).
2. Click on the new user's row → go to
   `/admin/users/{id}/subnet`.
3. Click "Allocate" (form POST).
4. The sidecar preauth is still issued
   separately (v0.16.7).

Steps 2-3 are gone. `PostAdminUser` now
automatically calls `subnet.Create(userID)` after
the `portal_users` row is inserted, controlled
by `SKYGATE_AUTO_ALLOCATE_SUBNET=true` (default).
The operator's stated preference: "I want
subnets allocated by default, not via a
separate button click."

Best-effort: a subnet allocation failure is
logged (`log.Printf`) but doesn't roll back the
user — the user is still created; the operator
can retry via the manual "Allocate" button on
`/admin/users/{id}/subnet`. The `audit_log` row
records both the `user_create` action and
(if applicable) the `subnet_allocate` outcome
(`auto_allocate=ok` or
`auto_allocate=FAIL: <err>`), so a future
failure is debuggable from the audit log alone
without needing to correlate the
`subnet.Create` call separately.

The manual "Allocate" button on
`/admin/users/{id}/subnet` is **unchanged**. It
remains for:
- Re-issue flows (operator disables → re-enables
  a subnet and wants a fresh row).
- The `disabled → active` transition
  (`subnet.SetStatus(active)`).
- Operators who set `SKYGATE_AUTO_ALLOCATE_SUBNET=false`
  to revert to the v0.16.0-v0.18.1 behaviour.

`subnet.Create` is idempotent (`INSERT OR
IGNORE` on the `(user_id)` UNIQUE constraint,
returns `ErrAlreadyExists` if a row is already
present), so the button is safe to click even
with auto-allocate enabled.

### Files

* `internal/handlers/handlers_admin_users.go` —
  `PostAdminUser` now calls `subnet.Create(...)`
  inside the `a.Cfg.AutoAllocateSubnetOnUserCreate`
  branch. Captures the new user ID from
  `db.InsertPortalUser` (was previously discarded
  with `_`).
* `internal/config/config.go` — new
  `AutoAllocateSubnetOnUserCreate bool` field,
  read from `SKYGATE_AUTO_ALLOCATE_SUBNET`
  (default `true`).
* `internal/handlers/handlers_admin_users.go` —
  `log` import added.

No DB migration needed — `user_subnets` was
added in v0.16.6 (migration v0.38).

## Live verification

* `make test` — **smoke 122/122** (EN 61 + RU 61,
  both 0 fail), check_exit_nodes PASS (3
  relays), check_https PASS (TLS, SAN, cert
  validity, HTTP→HTTPS redirect, HSTS via /
  fallback).
* `/admin/headscale` GET 200, HTML renders
  without template error. Title "Обновления
  headscale" / "Headscale updates" appears in
  RU/EN respectively.
* "Check now" end-to-end: a live test of
  `POST /admin/headscale/check-now` wrote
  `v0.29.2` to `headscale_releases` with
  `is_breaking=0, notified=0` (no alert sent
  because the latest equals the pinned).
* Auto-allocate test: created a test user
  via the admin form, the SQLite query
  immediately returned
  `v0200test_1784548703|10.0.11.0/24|pending`
  — subnet auto-allocated on the create call,
  no manual click required.
* `/admin/exit-nodes` GET 200, banner not yet
  visible because the monitor's first 24h tick
  is pending (and the current latest is
  0.29.2 = pinned = up to date).

19 files changed, +1740/-8 lines. Migration
v0.41 adds the `headscale_releases` table.

Build `c05a7a8` on `feature/v0.10.12-bot-ux`,
live on VM at `192.168.13.69`.

## What comes next

v0.21.0 — **user-to-user subnet bridge** (the
"share my subnet with another user by invite
code" feature the operator asked for alongside
v0.20.0). New `invite_codes` table
(migration v0.42), bot `/invite` + `/accept`
commands, admin `/admin/invites` page,
auto-bridge on accept (writes a
`user_subnet_shares` row + re-applies the ACL
pipeline per v0.17.1's auto-reapply trigger).
