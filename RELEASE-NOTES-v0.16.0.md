# v0.16.0 — backlog release

**Date**: 2026-07-16
**Branch**: `feature/v0.10.12-bot-ux`
**Build**: post-`94f3731` (v0.15.6 admin page i18n)

The "clean up the deferred v0.12 / v0.13 backlog before tackling
v0.16" release. Six previously-deferred features ship in one
go:

1. **v0.12.1 — per-user bot routing.** `/add_device` from user
   alice now issues the preauth key on alice's own control
   plane (when she has a per-plane override). Same for
   `/add_rule` and `/delrule` — the ACL is generated for the
   right plane's identities and pushed to the right
   headscale instance.
2. **v0.13.0 — per-plane ACL.** `GenerateACL` is now
   `GenerateACLForPlane(planeURL)` and only includes the
   identities on that plane. `ApplyACLForAllPlanes` iterates
   every distinct control plane and pushes the right policy
   to each in one go.
3. **v0.13.0 — ACL import/export with dry-run preview.**
   `/admin/acls/export` downloads the current
   `GenerateACLForPlane("")` output as JSON;
   `/admin/acls/import` accepts a file upload or pasted
   JSON, shows a side-by-side dry-run, and only pushes the
   imported policy when the operator clicks "Apply".
4. **Butler voice v3 — urgency marks.** The gate envelope
   now supports `!` (warning) and `!!` (critical) marks
   appended to the icon, so `🔑!!` in the chat list reads
   as "critical preauth reply" and `🪶` reads as "normal
   reply". Applied to `/add_device` for now.
5. **Personal API token rotation.** `/my/token` now has a
   TTL dropdown (1h / 1d / 7d / 30d / never) and an
   auto-rotate checkbox. Expired tokens are rejected by
   the Bearer-auth path.
6. **Documentation**: per-user subnets roadmap entry +
   `docs/v0.16.0-open-questions.md` parking the 8 design
   decisions for the next major work.

All five deferred items are now done — the v0.16.0
release is the "no more backlog" point. Future releases
can start fresh on the per-user subnets direction.

## What's in

### v0.12.1 — per-user bot routing

Before: every bot command used `env.HS` (the global
headscale client). A per-user `portal_users.headscale_url`
override had no effect on the bot path — `/add_device`
always issued a preauth on the operator's primary headscale,
even if the user was on a different plane.

Now: `BotEnv` carries `HSForPortalUser func(userID int64) *headscale.Client`
and `PortalPlaneURL func(userID int64) string`. Both are
populated by `RealNotifier.env()` from closures the App
installs at startup (via `rn.SetHSForUser(a.HSForUser)` and
`rn.SetPlaneURLForUser(a.PlaneURLForUser)`). The two new
helpers:

- `env.userHS()` — returns the per-user client (falls
  through to `env.HS` if no per-user override)
- `env.userPlaneURL()` — returns the per-user plane URL
  (falls through to `""` — the global default plane)

Replaced every `env.HS.X` call site with `env.userHS().X`
across `/add_device`, `/add_rule`, `/delrule`,
`/clearrules`, `/sync_nodes`, `/my_nodes`,
`/setdefaultdevice`, `/setexitnode`, etc. ACL generation
now uses `acl.ApplyACLPipelineForPlane(d, env.userHS(), env.userPlaneURL(), ...)`
so the policy is generated for the right plane's
identities.

**Test:** `TestAddDeviceReplyV0121_PerUserRouting` builds
two fake headscale servers, points the bot at one per
user via `HSForPortalUser`, and verifies that `/add_device`
for alice (uid=2) hits plane A (`hskey-fake-7`) while
bob (uid=3) hits plane B (`hskey-fake-8`).

### v0.13.0 — per-plane ACL

Before: `GenerateACL(d)` read every portal user and built
one big policy. Multi-plane deploys (v0.12.0) couldn't use
the same `SetPolicy` for all planes because headscale
rejects unknown identities in `tagOwners`.

Now: `GenerateACLForPlane(d, planeURL)` returns a policy
scoped to the identities on the given plane. The query
`qSelectPortalUsernamesForPlane` matches rows where
`headscale_url = ?` (plus rows where both are empty for
the global default plane). The per-plane pipeline
`ApplyACLPipelineForPlane(d, hs, planeURL, ...)` does
the full 4-step dance scoped to one plane. The wrapper
`ApplyACLForAllPlanes` iterates every distinct plane
and pushes the right policy to each.

**Test:** `TestGenerateACLForPlane_ScopesToPlaneUsers`
seeds alice on the default plane and bob+carol on
`https://plane-b.example`, then verifies the default
plane's policy contains alice but not bob/carol, and
plane B's policy contains bob/carol but not alice.
`TestListControlPlanesGroupsByURL` verifies the
distinct-plane count. `TestApplyACLPipelineForPlane_UsesCorrectClient`
captures the SetPolicy body and verifies it's the
plane-scoped JSON.

### v0.13.0 — ACL import/export with dry-run preview

Before: the only way to back up the headscale policy was
the `acl_snapshots` table (in-DB, not portable). Loading
a saved policy meant re-adding every user via the web UI
before headscale would accept a `SetPolicy` with their
identities.

Now: four new routes:

- `GET /admin/acls/export` — downloads
  `GenerateACLForPlane("")` as `<timestamp>-skygate-acl.json`
- `GET /admin/acls/import` — form (file upload + paste
  textarea)
- `POST /admin/acls/import` — parses, validates shape
  (must be JSON, must have `acls` / `tagOwners` / `groups` /
  `ssh` top-level keys), renders a side-by-side dry-run
- `POST /admin/acls/import/apply` — pushes the imported
  policy to every plane and writes an `acl_snapshots` row

The dry-run page is a single page (not a redirect): it
shows the current policy next to the imported one with
SHA-256 fingerprints so the operator can see "this is
byte-identical to the current" or "this differs" before
hitting Apply. Apply is a separate POST (not a link
with confirm) so a typo can't wipe a working policy.
The Apply button has a JS confirm for one final guard.

The `acl.SetACLForAllPlanes` helper pushes the pre-built
policy to every plane (one entry per distinct URL)
without re-running `GenerateACL`. Used by both the
import flow and any future "load policy from disk"
endpoint.

**Test:** `TestSetACLForAllPlanes_PreBuiltPolicy` verifies
the pre-built policy is pushed byte-for-byte (via a
captured HTTP body) and the `acl_snapshots` row carries
the imported config.

### Butler voice v3 — urgency marks

Before: every bot reply header was `🪶 ═══ Skygate ═══`.
The operator couldn't tell at a glance whether the
reply was normal / warning / critical — they had to
open every bot message in the chat list.

Now: `WithUrgency(level)` (an option on `butlerEnvelope`)
appends `!` (warning) or `!!` (critical) to the chosen
icon. The mark attaches to the icon, not always `🪶`,
so:

- normal: `🪶` (or the per-command icon, e.g. `🔑` for
  `/add_device`)
- warning: `🪶!` (or `🔑!`)
- critical: `🪶!!` (or `🔑!!`)

Applied to `/add_device` for now (`WithUrgency(UrgencyWarning)`)
because the reply contains a preauth key — a credential
to act on. The operator sees `🔑!` in the chat list and
knows the body has something sensitive.

**Test:** `TestButlerEnvelope_WithUrgency` covers all
six combinations (3 levels × 2 icon modes: default 🪶
and per-command).

### Personal API token rotation

Before: tokens never expired. Operators could only
revoke manually. The bot integration had to "trust" a
token forever.

Now: `/my/token` has a TTL dropdown (1h / 1d / 7d / 30d /
never) and an auto-rotate checkbox. The token row
carries `expires_at` (unix timestamp) and `auto_rotate`
(boolean). The Bearer-auth path (`AuthenticateBearer`)
filters out expired tokens before granting claims.

**Schema:** `migrations_v0.37.go` adds
`expires_at INTEGER NOT NULL DEFAULT 0` and
`auto_rotate INTEGER NOT NULL DEFAULT 0` to
`personal_api_tokens`, plus an index on
`expires_at` for the future rotation job. 0 = never
expires (the pre-v0.15.5 default; existing rows are 0
so the auth middleware's expiry check is a no-op for
legacy tokens).

**Audit detail:** the `token_create` audit row now
includes `label=… ttl=… auto_rotate=…` so the operator
can see the policy applied at creation time.

**Auto-rotate v0.16.0 follow-up:** the column is in
v0.15.5 so the UI can store + read it, but the
background rotation job itself is a v0.16.0 design call
(interval, secret-key handling, the question of "do
we keep the old token valid for N minutes after rotation
to avoid breaking in-flight requests", etc.). Tracked
separately.

**Test:** `internal/db/personal_api_tokens_test.go`
updated to call `InsertAPIToken(d, uid, hash, label, 0, false)`
(the new 6-arg signature). Other tests still pass; the
schema migration runs in `openTestDB` so the new
columns exist from the start.

## What stayed

- **No SQL schema breaking changes** — every new column
  has a DEFAULT, every migration is `IF NOT EXISTS`, and
  the test schema picks up the new columns on
  `openTestDB`.
- **No breaking API changes** — `InsertAPIToken` is the
  only signature change (the only internal caller, the
  test, was updated). `GenerateACL(d)` is preserved as a
  wrapper around `GenerateACLForPlane(d, "")` for
  single-plane callers.
- **Single-plane deploys unaffected** — when there's
  only one plane (the common case), the per-plane code
  path is identical to the old single-plane path: the
  global default is `planeURL = ""` and the only plane
  is the global. The `ListControlPlanes` returns
  exactly one row.
- **Pre-v0.12.0 tokens unaffected** — `expires_at = 0`
  preserves the "never expires" behaviour.
- **No new env vars** — `SKYGATE_SECRET_KEY` already
  existed for v0.12.0 per-user headscale routing; the
  v0.13.0 per-plane work reuses the same key.
- **No breaking config** — every new feature is opt-in
  (the TTL dropdown has "never" as a choice, the
  urgency mark is per-reply, etc.).

## Verification

- 12/12 packages green (`go test -count=1 ./...`)
- `TestCatalogsParity` + `TestPlaceholderOrder` green
  (every new i18n key has both RU + EN, same `%s` / `%d`
  arg counts)
- 4 new v0.13.0 tests:
  - `TestGenerateACLForPlane_ScopesToPlaneUsers`
  - `TestApplyACLPipelineForPlane_UsesCorrectClient`
  - `TestListControlPlanesGroupsByURL`
  - `TestSetACLForAllPlanes_PreBuiltPolicy`
- 1 new v0.12.1 test:
  - `TestAddDeviceReplyV0121_PerUserRouting`
- 1 new butler v3 test:
  - `TestButlerEnvelope_WithUrgency` (6 sub-cases)
- 1 schema migration test passes (v0.37 default
  columns + index)
- `TestLoadTemplates` green (every `{{t "..."}}` and
  `{{t ... | safeHTML}}` reference parses cleanly)
- Pending: VM `make test` (smoke 118/118) before push
  to GitHub
