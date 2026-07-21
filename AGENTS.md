# AGENTS.md — AI hints for Skygate

This file is for AI assistants (Hermes, Claude, Cline, Cursor, etc.) working on
or with Skygate. Read this **first** before suggesting changes or running tasks.

---

## Release status

* **Current**: v0.23.3 — node-expiry
  watcher (the "device
  won't stay connected"
  release)
  ([release notes](RELEASE-NOTES-v0.23.3.md)).
  Background goroutine in
  `internal/expirewatch` ticks
  every 5m, walks every non-tagged
  node in headscale, and extends
  any node whose Expiry is missing
  or within 7d of "now" out to 30d.
  Works around a Tailscale 1.98.x
  client behaviour where
  `RegisterRequest.Expiry` is only
  2-4s in the future and headscale
  0.29.x applies that Expiry verbatim
  — without the watcher, every fresh
  preauth-registered device gets
  force-logged-out within seconds.
  Discovered 2026-07-21 with the
  operator's Android phone (node 10 /
  skybars): manual `headscale nodes
  expire -i 10 --expiry +30d` was
  the one-shot fix; v0.23.3 makes it
  automatic. 4 new env vars
  (`SKYGATE_EXPIREWATCH_ENABLED` /
  `_INTERVAL` / `_THRESHOLD` /
  `_RENEWAL`, defaults `true` / `5m` /
  `168h` / `720h`); no `/admin/*`
  knobs (defaults are sensible).
  `NodeView.Expiry` added to the
  headscale client (was previously
  missing — required an extra
  `/api/v1/node/{id}` round-trip per
  node per watcher tick). Tagged
  nodes (`tag:exit-node` /
  `tag:public` / `tag:subnet-router` /
  `tag:client`) are skipped by
  the watcher because headscale's
  `state.go` explicitly guards
  `if !node.IsTagged()` around the
  `regReq.Expiry` branch. 8 unit
  tests in
  `internal/expirewatch/manager_test.go`
  (PicksOnlyNearExpiry /
  SkipsTagged / HandlesMissingExpiry /
  RespectsIntervalZero /
  RunStopsOnContextCancel /
  RecordsAuditOnRenew /
  ParsesRFC3339NanoExpiry /
  HandlesAPIFailure), all PASS.
  18/18 packages green (acl, auth,
  backup, config, db, expirewatch,
  handlers, headscale,
  headscale_version, i18n, invite,
  mesh, monitoring, release, sidecar,
  subnet, telegram). `check_v0.23.3.sh`
  — 5-step live verification: force a
  node's expiry to 2s, wait for the
  watcher to tick, confirm the expiry
  is now at least 7d out, audit log
  row written, tagged node untouched.
  The "v0.23.0 is for compliance, not
  default path" release. v0.23.0 shipped
  one-click per-user headscale
  provisioning; v0.23.1 makes explicit
  the cost (re-auth all devices + lose
  shared exit-nodes + lose mesh bridges)
  via a warning card on
  `/admin/users/{id}/plane`. New
  `check_cross_subnet_v0.23.1.sh` is an
  11-step live verification proving that
  the existing global headscale already
  delivers per-user subnets + shared
  exit-nodes + mesh for the 4 prod
  users — per-user control plane is
  not needed for the operator's actual
  goals. Use v0.23.0 only for compliance
  tier (SOX, multi-tenant SaaS,
  geographic isolation).
  Closes the v0.12.0 capability
  gap that left per-user control
  planes as a manual ssh + docker
  + headscale CLI flow. The
  bootstrap script
  (`deploy/headscale-users/headscale-bootstrap.sh`)
  creates a per-user docker
  container (port 50450+uid%50,
  base_domain `<username>.tsnet.skynas.ru`),
  issues a 10-year API key, returns
  JSON. The handler encrypts the
  key with SKYGATE_SECRET_KEY
  and persists to
  `portal_users.headscale_api_key_enc`.
  The deprovision script
  (`headscale-deprovision.sh`)
  tears down + preserves the
  per-user data dir for recovery.
  `internal/headscale/provision.go`
  is a Go wrapper (8 unit tests,
  all PASS). Skyadmin pilot
  verified live: container up +
  healthy, DB has the URL + encrypted
  key, /admin/users/1/plane shows
  the post-provision UI. 11/11
  check_v0.23.0.sh steps PASS.
  Smoke 83/83 still green. **Phase 1
  is infrastructure only — no data
  migration yet.** skyadmin still
  uses the global headscale for
  all node operations. Phase 2
  (v0.23.1) is the data migration
  step.
  The "why is my subnet `pending`?"
  release. Pre-v0.22.3 the status
  semantics was `active` ⇔
  subnet-router up, which left
  every user in `pending` because
  nobody deployed a sidecar. v0.22.3
  flips it: `pending` ⇔ 0 devices
  in tailnet, `active` ⇔ ≥1 device
  (logical namespace),
  `router_active` ⇔ bonus on top
  (real subnet-router up too).
  `subnet.SyncStatus(db, uid, hasRouter)`
  encapsulates the new logic; called
  from `backfillNodeOwnership` after
  every `/my/devices` load. UI gets
  colored pills (green/green/yellow/muted)
  on `/admin/users/{id}/subnet` +
  `/admin/users` subnet column, plus
  a new "Your personal subnet" card
  on `/my/devices`. 7 new unit tests
  in `internal/subnet/manager_test.go`
  (PendingWhenNoDevices / ActiveWhenDevices /
  RouterActiveWhenHasRouter / DisabledPreserved
  / NoSubnetRow / Idempotent / SetStatusAcceptsRouterActive).
  8 files, +405/-18 lines, 7 new tests,
  smoke 83/83 still green. For the 4
  production users (skyadmin/michail/
  guest/daniil) their subnets flip
  from `pending` to `active` on the
  next `/my/devices` load — guest
  (0 devices) stays `pending`, which
  is the intended behavior.

* **Previous**: v0.23.0 — one-click
  per-user headscale
  provisioning (Phase 1)
  ([release notes](RELEASE-NOTES-v0.23.0.md)).
  Closes the v0.12.0 capability
  gap that left per-user control
  planes as a manual ssh + docker
  + headscale CLI flow. The
  bootstrap script
  (`deploy/headscale-users/headscale-bootstrap.sh`)
  creates a per-user docker
  container (port 50450+uid%50,
  base_domain `<username>.tsnet.skynas.ru`),
  issues a 10-year API key, returns
  JSON. The handler encrypts the
  key with SKYGATE_SECRET_KEY
  and persists to
  `portal_users.headscale_api_key_enc`.
  The deprovision script
  (`headscale-deprovision.sh`)
  tears down + preserves the
  per-user data dir for recovery.
  `internal/headscale/provision.go`
  is a Go wrapper (8 unit tests,
  all PASS). Skyadmin pilot
  verified live: container up +
  healthy, DB has the URL + encrypted
  key, /admin/users/1/plane shows
  the post-provision UI. 11/11
  check_v0.23.0.sh steps PASS.
  Smoke 83/83 still green. **v0.23.0
  is infrastructure only — no data
  migration. v0.23.1 follows up
  with the compliance-tier warning
  + the cross-subnet verification
  (proves global headscale already
  gives the operator per-user subnets
  + shared exit-nodes + mesh without
  needing per-user control plane).**

* **Previous**: v0.22.3 — subnet
  status reflects device
  ownership, not subnet-router
  ([release notes](RELEASE-NOTES-v0.22.3.md)).
  The "why is my subnet `pending`?"
  release. Pre-v0.22.3 the status
  semantics was `active` ⇔
  subnet-router up, which left
  every user in `pending` because
  nobody deployed a sidecar. v0.22.3
  flips it: `pending` ⇔ 0 devices
  in tailnet, `active` ⇔ ≥1 device
  (logical namespace),
  `router_active` ⇔ bonus on top
  (real subnet-router up too).
  `subnet.SyncStatus(db, uid, hasRouter)`
  encapsulates the new logic; called
  from `backfillNodeOwnership` after
  every `/my/devices` load. UI gets
  colored pills (green/green/yellow/muted)
  on `/admin/users/{id}/subnet` +
  `/admin/users` subnet column, plus
  a new "Your personal subnet" card
  on `/my/devices`. 7 new unit tests
  in `internal/subnet/manager_test.go`
  (PendingWhenNoDevices / ActiveWhenDevices /
  RouterActiveWhenHasRouter / DisabledPreserved
  / NoSubnetRow / Idempotent / SetStatusAcceptsRouterActive).
  8 files, +405/-18 lines, 7 new tests,
  smoke 83/83 still green. For the 4
  production users (skyadmin/michail/
  guest/daniil) their subnets flip
  from `pending` to `active` on the
  next `/my/devices` load — guest
  (0 devices) stays `pending`, which
  is the intended behavior.

* **Previous**: v0.22.2 — fix
  auto-apply tag:private for
  tagless nodes (MSI bug)
  ([release notes](RELEASE-NOTES-v0.22.2.md)).
  The operator reported on
  2026-07-20 that MSI (id=15),
  registered via skygate preauth
  (id=98), never received
  tag:private in headscale. Root
  cause: backfillNodeOwnership's
  Strategy A branch set
  matchedTag = firstTagOrFallback(n),
  which returns "tag:untagged" for
  tagless nodes. The subsequent
  branch check `if matchedTag ==
  "tag:private"` failed, so
  HS.TagNode(15, "tag:private") was
  NEVER called. Strategy C had the
  same bug; it was fixed on
  2026-07-10 but Strategy A was
  missed. v0.22.2 fix applies the
  same override to Strategy A:
  when the preauth key came from
  skygate, default matchedTag to
  "tag:private". firstTagOrFallback
  is only used when the node ALREADY
  has tags (e.g. skygate-vm has
  tag:private in headscale, so the
  result is unchanged for that
  case). Two new tests in
  internal/handlers/handlers_node_ownership_test.go
  pin the fix. 8/8 live-validation
  checks PASS on the VM
  (check_v0.22.2.sh). Smoke 83/83
  (EN 83 + RU 83), check_exit_nodes
  3/3, check_https PASS.

* **Previous**: v0.22.1 — /my/meshes
  web UI (was bot-only in v0.22.0)
  ([release notes](RELEASE-NOTES-v0.22.1.md)).
  v0.22.0 shipped the mesh (shared
  network) feature bot-only
  (/mesh create|join|leave|meshes).
  The operator flagged that users have
  no obvious place in the WEB interface
  to (1) create a shared network, (2)
  enter an invite code from another user.
  v0.22.1 fixes the gap: GET /my/meshes
  + 3 POST routes (create, join, leave)
  with the same form-based UX as
  /my/tokens / /my/devices. Web + bot
  share the same internal/mesh package
  state, so a mesh created via the web
  shows up in the bot's /meshes list (and
  vice versa). Sidebar entry + 34 new
  i18n keys (RU+EN, 68 entries). 10/10
  live-validation checks PASS on the VM
  (caught a real i18n-key-prefix bug in
  the first deploy; hotfix on top of
  the initial v0.22.1 commit). Smoke
  132/132 (EN 66 + RU 66), check_exit_nodes
  3/3, check_https PASS.

* **Previous**: v0.22.0 — mesh (shared
  network) + safe user migration design
  ([release notes](RELEASE-NOTES-v0.22.0.md)).
  The 3rd primitive in the user-to-user
  networking stack (after the v0.17.1
  one-directional share + v0.21.0
  one-on-one invite bridge). A mesh is
  a named group of users whose personal
  subnets are all mutually visible to
  each other — like radmin VPN's
  "shared network". N-way bridge,
  automatic, deduped with v0.17.1 share
  rows. Migration v0.43 adds
  `meshes` + `mesh_members` tables.
  Bot commands `/mesh create|join|leave`
  + `/meshes` (user-scope) drive the
  workflow; `/admin/meshes` (admin-only,
  read-only) is for oversight. The
  operator's 2026-07-20 backlog message
  asked for this + 3 concerns about
  cross-subnet ACL, exit-node global
  access, and skyadmin migration — all
  three verified by Phase 1 (12
  integration tests, all PASS locally)
  + Phase 1b (7 live-validation checks
  on real headscale round-trip, all
  PASS on VM). 18 files, +1932/-8
  lines, 130/130 smoke + 3/3
  check_exit_nodes + check_https PASS.
  Phase 3 (the safe user migration
  tool) is explicitly DEFERRED to a
  follow-up release — the operator's
  "только после проверки и гарантии
  работы" is honored literally, and
  the migration tool is a separate,
  opt-in, audit-tracked operation.

* **Previous**: v0.21.1 — fix headscale-side
  user delete (typo: `-u` should be `-i`)
  ([release notes](RELEASE-NOTES-v0.21.1.md)).
  Pre-existing bug discovered while cleaning up
  test users after v0.21.0. Every
  `POST /admin/users/{id}/delete` left a
  stale "orphan" headscale user behind,
  surfacing as the "HSOrphans" banner on
  `/admin/users`. The root cause: a typo in
  the headscale CLI args — the code used
  `users delete -u -f <id>` but headscale's
  `users delete --help` shows the correct
  flag is `-i, --identifier` (the `--force`
  global flag has no short alias in 0.29.x).
  The audit log captured every failed
  attempt with `Error: unknown shorthand
  flag: 'u' in -u`. Fix: `-u -f <id>` →
  `-i <id> --force` in
  `internal/headscale/users.go`, extracted
  to a `deleteUserCmd` method for
  testability. Three new regression tests
  assert the correct args and reject the
  pre-fix shape. The 4 existing orphans
  from v0.21.0 test user cleanup get removed
  by a post-deploy manual `docker exec ...
  headscale users delete -i <id> --force`
  per orphan. After the post-deploy cleanup,
  `/admin/users` no longer shows the
  HSOrphans banner. Smoke 126/126 still
  green.

  **What comes next**: the three "close the
  backlog" features from the 2026-07-20
  message are done. v0.19.1 (the re-attempt
  of the reverted v0.19.0 dns.extra_records
  feature) is still blocked on headscale
  0.30+ — the weekly mavis cron
  (`headscale-milestone-16-check`) checks
  headscale milestone #16 (DNS Work) every 7
  days and reports if any progress lands.

* **Previous**: v0.21.0 — user-to-user subnet
  bridge (invite codes + bot /invite + /accept +
  /admin/invites)
  ([release notes](RELEASE-NOTES-v0.21.0.md)).
  Closes the third feature the operator asked
  for in the 2026-07-20 backlog message. The
  v0.17.1 admin-mediated "share" path is
  unchanged; v0.21.0 adds the user-mediated
  path: A generates a code, B types it in the
  bot, the bridge auto-applies. New
  `invite_codes` table (migration v0.42) with
  a 32-char alphabet code (8 chars, ~1.1T
  possibilities, 7-day TTL). Bot commands:
  `/invite <username>` (grantor side, generates
  a code), `/accept <code>` (grantee side,
  validates + atomically consumes + applies the
  bridge via `invite.ApplyBridge` which writes
  a `user_subnet_shares` row + triggers the
  per-plane ACL re-apply goroutine), `/invites`
  (list the caller's outstanding + incoming
  invites, 10 per side). Admin UI:
  `/admin/invites` (admin-only overview with a
  Revoke button for active rows). The bot path
  does NOT require admin; the bridge row is
  written the same way the admin share would
  write it. `grantee_username` is TEXT (not an
  FK) so A can invite "bob" before bob has a
  skygate account — the consume path resolves
  the username to a user_id at consume time.
  16 files, +2348/-2 lines, smoke 126/126
  (EN 63 + RU 63), check_exit_nodes 3/3,
  check_https PASS.

  **v0.21.0 hotfix** (commit `cb94b37`,
  shipped immediately after v0.21.0):
  `cmd/skygate/main.go` had a duplicate
  registration of the `/admin/headscale` route
  (introduced by the v0.21.0 edit pattern that
  matched the v0.20.0 insertion twice). The
  first deploy of v0.21.0 panicked on boot
  with `pattern "GET /admin/headscale"...
  conflicts with pattern "GET /admin/headscale"`.
  The hotfix removes the duplicate, leaving
  the v0.20.0 registration (lines 320+325) as
  the single source of truth. Build verified
  live on VM; smoke 126/126 again.

  **What comes next**: the three "close the
  backlog" features from the 2026-07-20
  message are done. v0.19.1 (the re-attempt
  of the reverted v0.19.0 dns.extra_records
  feature) is still blocked on headscale
  0.30+ — the weekly mavis cron
  (`headscale-milestone-16-check`) checks
  headscale milestone #16 (DNS Work) every 7
  days and reports if any progress lands.

* **Previous**: v0.20.0 — headscale-update-monitor +
  auto-allocate subnet on user create
  ([release notes](RELEASE-NOTES-v0.20.0.md)).
  Two operator-side UX cleanups bundled because
  they're both small and the operator asked for
  them in the v0.18.1 retro:

  1. **`/admin/headscale` page + monitor goroutine**
     — polls the juanfont/headscale GitHub
     Releases API every 24h (configurable via
     `SKYGATE_HEADSCALE_POLL_INTERVAL`), compares
     the latest tag against the operator's pinned
     version (`SKYGATE_HEADSCALE_VERSION_PIN`,
     e.g. "0.29.2"), and dispatches a Telegram
     alert + writes a row to `headscale_releases`
     when a newer version is available. New bot
     command `/headscale` (admin-only) renders
     the same status. `/admin/exit-nodes` gets
     a banner above the table when a newer
     headscale is known. `headscale_releases`
     table (migration v0.41) holds the history
     so the page has a "seen releases" view that
     survives skygate restarts. Page has a
     "Check now" button for an immediate re-poll.
     GitHub rate limit: 60 req/h unauthenticated;
     24h polling leaves 56/60 unused.

  2. **Auto-allocate subnet on user create** —
     `PostAdminUser` now calls `subnet.Create(userID)`
     automatically after the `portal_users` row
     is inserted, controlled by
     `SKYGATE_AUTO_ALLOCATE_SUBNET` (default
     `true`). The operator's stated preference
     was "by default, not via a separate button
     click". The manual "Allocate" button on
     `/admin/users/{id}/subnet` is unchanged
     (re-issue / disabled→re-allocate flows).
     `subnet.Create` is idempotent, so the
     button is safe to click even with auto-
     allocate enabled. Allocations failures are
     logged but don't roll back the user
     (the user is still created; the operator
     can retry via the manual button). The
     audit row records both `user_create` and
     the `subnet_allocate` outcome.

  19 files changed, +1740/-8 lines. Migration
  v0.41 adds the `headscale_releases` table.
  Config: `SKYGATE_HEADSCALE_VERSION_PIN`,
  `SKYGATE_HEADSCALE_POLL_INTERVAL`,
  `SKYGATE_AUTO_ALLOCATE_SUBNET`. Verified live
  on VM: smoke 122/122 (EN 61 + RU 61),
  check_exit_nodes 3/3, check_https PASS, "Check
  now" button end-to-end works (writes
  v0.29.2 to headscale_releases with
  is_breaking=0, notified=0 because it matches
  the pinned version).
  ([release notes](RELEASE-NOTES-v0.18.1.md)).
  Operator-flagged issues from the v0.18.0 deploy,
  all closed in one small release:

  1. **`check_https.py` HSTS /login 404** — the VM
     uses openresty (not Caddy as the docs say) and
     openresty 404s `/login`. `check_hsts` now falls
     back to `/`, `/api/v1/apikey` in order and
     accepts HSTS from whichever path returns a real
     response. 4 new regression tests in
     `scripts/test_check_https.py`. `make test` is
     now FULLY green.

  2. **`/admin/exit-nodes` "Tag as exit-node" /
     "Untag" buttons** — replaces the operator's
     two manual `docker exec headscale headscale
     nodes ...` invocations (approve-routes + tag)
     with a single click. Approves ONLY
     `0.0.0.0/0` + `::/0` (NOT the full
     availableRoutes set, to avoid accidentally
     approving karolina's 200+ subnets). Applies
     `tag:exit-node`. New headscale API
     `ApproveRoutesForNodeID`. 4 new handler tests
     + 6 new i18n keys (RU+EN).

  3. **`ControlURL` auto-injection in
     `renderWithLayout`** — the `/admin/exit-nodes`
     Step-2 tutorial and `/my/preauth` result page
     rendered with an EMPTY `--login-server=`
     because the handlers didn't pass ControlURL in
     the data map. `renderWithLayout` now
     auto-injects `data["ControlURL"] = a.ControlURL`
     on every page render. The operator's
     `SKYGATE_CONTROL_URL` env var flows through
     `New(...)` → `App.ControlURL` → data map →
     template. 2 new regression tests in
     `handlers_test.go`.

  12/12 packages green, smoke 118/118, live at
  build `45d25a9`.

  **Note on the v0.19.0 attempt (reverted)**: a
  v0.19.0 release was deployed briefly and then
  reverted (commit `0c394bd`) because the
  `exitnode.skygate-subnet-<user>.<base-domain>`
  DNS-record feature relied on headscale's
  `dns.extra_records` policy field, which
  headscale 0.29.x (the operator's version —
  0.29.2 as of 2026-07-20) doesn't support —
  pushing a policy with the `dns` key returns
  `unknown field: "dns"` and the policy is rejected.
  The v0.16.0+ subnets roadmap's "exitnode" record
  is **blocked on headscale 0.29.x** and will
  return as v0.19.1 once the operator upgrades
  headscale to a version that supports
  `dns.extra_records` (0.30+ based on headscale
  changelog history — v0.30.0 was removed from
  the "unreleased" section of headscale's
  CHANGELOG in commit 8eea894, which suggests
  it's close). The schema migration
  (`preferred_exit_node_id` column), helper
  functions, and the per-user-subnet UI/bot code
  paths are all in git history (commit `646f8fb`)
  and can be re-enabled cheaply via
  `git revert 0c394bd && git push` once the
  headscale upgrade lands.

  **Note on the headscale 0.29.2 upgrade (2026-07-20)**:
  the operator upgraded headscale from
  `headscale/headscale:0.29.1` to
  `headscale/headscale:0.29.2` (commit
  `8eea89488c642f3d5f617fab5493d5f51f6f4ad0`,
  build 2026-07-01). Three bugfixes ship in
  0.29.2 (none of which add `dns.extra_records`,
  so v0.19.0 is still blocked):

  1. **Map-generation serialization fix (#3358)**
     — fixes a stall on the policy lock that
     could push clients into `unexpected EOF`
     retry loops during a mass reconnect on
     `autogroup:self`, via or relay policies.
     **Relevant to us**: the policy uses
     `autogroup:self` (admin→tag:public, admin→
     tag:exit-node SSH rules) and we have 3
     relays in the mesh, so a relay hiccup or
     a mass-reconnect event would have hit
     this. Now safe.
  2. **`/ts2021` WebSocket GET fix (#3359)** —
     previously returned 405 to Tailscale
     JS/WASM control clients. Verified live:
     `curl -H 'Connection: Upgrade' -H
     'Upgrade: websocket' http://localhost:50444/
     ts2021` now returns `101 Switching Protocols`
     with a valid `Sec-Websocket-Accept`. (Note:
     openresty on the VM does NOT yet forward
     WebSocket Upgrade headers — `https://head.
     skynas.ru/ts2021` still 500s. Tailscale
     native clients don't use this path, so
     the tailnet itself is unaffected; only
     a future JS/WASM client deployment would
     need an openresty config change. Out of
     scope for this upgrade.)
  3. **Invalid FQDN handling (#3349)** —
     nodes with empty or too-long FQDNs no
     longer fail map delivery; the offender
     is logged at startup with the fix
     command. Defensive: we don't have any
     such nodes today, but it's nice to have.

  **Upgrade procedure used** (reproducible for
  future bumps):
  1. Backup SQLite DB + config to
     `/tmp/headscale-backup-<timestamp>/` via
     a throwaway `alpine:3.20` container
     `docker run --rm -v
     headscale_headscale_data:/from:ro -v
     $BACKUP_DIR:/to alpine:3.20 cp -a /from/.
     /to/`. The headscale_data volume isn't
     readable by skyadmin directly, so the
     throwaway container is the cleanest path.
     `acl.hujson` (399 B, generated) +
     `acl_policy.hujson` (11 B, the live
     config-file policy) + db.sqlite (8.3 MB)
     + db.sqlite-wal (4 MB) = 12 MB total.
  2. `sed -i 's|0.29.1|0.29.2|g'`
     `/home/skyadmin/headscale/docker-compose.yml`
     (the headscale compose lives outside the
     skygate repo, in `/home/skyadmin/headscale/`)
  3. `docker compose stop headscale && docker
     compose up -d --force-recreate headscale`
     — came up in 3 s, no policy churn
     (`updatedAt` unchanged from the v0.17.1
     deploy at `2026-07-20T09:37:26Z`).
  4. Verification: 11 nodes (8 online, 3
     offline, same as before), 256 ACL rules
     unchanged, 4 tagOwners unchanged (tag:exit-
     node, tag:private, tag:public,
     tag:subnet-router), 2 SSH rules unchanged,
     4 groups unchanged. `make test` 118/118
     PASS (smoke 59+59 en+ru), `check_exit_nodes
     .py` 3/3 PASS, `check_https.py` PASS via
     `/` fallback.

  **Why no skygate release tag for this?**
  This is a pure ops-level headscale image bump
  — no skygate code changed, no new i18n keys,
  no API surface delta. The next skygate release
  (whatever it ends up being — likely the v0.19.1
  re-attempt once headscale 0.30+ lands) will
  have the headscale version in its release
  notes. For now the v0.19.0 blocker note above
  is the only consumer-facing reference.

* **Previous**: v0.18.0 — MagicDNS for personal
  subnets
  ([release notes](RELEASE-NOTES-v0.18.0.md)).
  Roadmap step 5 of the v0.16.0+ per-user subnets
  plan. Each user's sidecar now has a stable,
  auto-resolving FQDN
  (`skygate-subnet-<username>.tsnet.skynas.ru`)
  so tailnet clients can reach the user's
  `10.0.<uid>.0/24` subnet without remembering
  the sidecar's tailnet IP. New
  `internal/subnet/magicdns.go` (pure string
  functions `ComputeMagicDNSNames` +
  `FormatMagicDNSNames`, no DB). Admin UI:
  `/admin/users/{id}/subnet` gets a "DNS имена"
  `<details>` card; `/admin/subnets` gets a new
  "DNS (MagicDNS)" column. Bot: `/mysubnet` reply
  appends a "MagicDNS" section. 12 new i18n keys
  (6 admin + 5 bot + 1 col_dns) RU+EN. 4 new
  unit tests in `magicdns_test.go`.
  `BaseDomain = "tsnet.skynas.ru"` matches
  `internal/acl/acl.go`'s `baseDomain` constant.
  The `exitnode.skygate-subnet-<user>` special
  record is NOT shipped in v0.18.0 (headscale 0.29
  doesn't support per-user service records);
  v0.19.0 is the planned home. 12/12 packages
  green, smoke 118/118, live at build `8d722af`.

  2. **Auto-reapply ACL on Allocate/Share/Revoke** —
     the v0.17.0 caveat ("click Re-apply ACL to push
     the new rule") is closed. New subnets are
     routable within ~1s of allocation.

  Files:
  - `internal/db/migrations_v0.39.go` +
    `portal_users.go` + `queries.go` —
    `user_subnet_shares` table, FK CASCADE,
    `GetSharedSubnetsForPlane` query
  - `internal/subnet/shares.go` (new) — `Grant`,
    `Revoke`, `ListSharedBy`, `ListSharedWith`,
    `ErrSelfShare`, `ErrShareNotFound`
  - `internal/acl/acl.go` — per-user dst list now
    includes every grantor's CIDR shared with the
    user
  - `internal/handlers/admin_user_subnet.go` —
    `PostAdminUserSubnetShare` / `Revoke` +
    auto-reapply on `Allocate`
  - `internal/handlers/templates/admin/user_subnet.html` —
    Cross-user sharing card with two columns +
    share form
  - `internal/telegram/commands.go` +
    `commands_user.go` — `/mysubnet share|revoke`
    subcommands
  - `internal/i18n/catalog.go` — 23 new keys × 2
    langs (12 admin + 11 bot)
  - 8 new tests (6 subnet + 2 ACL)

  12/12 packages green, smoke 118/118, live on VM
  at build `2c8176c`.
* **Previous**: v0.16.7 — per-user subnet sidecar
  (auto-approver + preauth)
  ([release notes](RELEASE-NOTES-v0.16.7.md)). Real
  sidecar runtime for the v0.16.0+ subnets feature
  (the schema shipped in v0.16.6, the UI in v0.16.8,
  the sidebar fix in v0.16.9). Adds:
  - `internal/sidecar/` package (~700 lines):
    Manager with GeneratePreauth (tag:subnet-router,
    1h TTL, single-use), SyncOnce (auto-approves
    routes + flips status active/disabled based on
    headscale state), Run (30s ticker), LastStats
    for admin UI
  - Admin UI: `/admin/users/{id}/subnet` "Issue
    preauth key" button + suggested `tailscale up`
    command snippet
  - Bot: `/mysubnet provision` — same preauth in
    chat reply (butler voice)
  - headscale API: `CreatePreauthKeyWithTags` for
    `tag:subnet-router` preauth; `ApprovedRoutes`
    field on NodeView (was only `AvailableRoutes`)
  - 11 new sidecar tests + 1 new admin handler test
    + 2 new bot tests
  - 2 critical fixes during the first deploy:
    `go sidecarMgr.Run(ctx)` (was inline, blocked
    main before HTTP could bind) +
    `HSForUser(0)` short-circuit (avoids 30s log spam
    for the global-plane sentinel)
  - 12/12 packages green, smoke 118/118, live on VM
    at build `ac73b8c`.
* **Previous**: v0.16.8 — UI: Subnet column + button
  in /admin/users
  ([release notes](RELEASE-NOTES-v0.16.8.md)). The
  v0.16.6 release shipped the
  `/admin/users/{id}/subnet` page (4 routes, full
  template) but the page was unreachable from the UI
  — no link from `/admin/users`, no sidebar entry, no
  "Subnet" column. Operator reported "where are the
  buttons?". Fix: extend `User` struct with the 3
  v0.16.6 denorm fields, extend
  `qSelectAllPortalUsers` from 6 to 9 columns, add a
  "Subnet" column to `/admin/users` (CIDR + status
  pill: green active / amber pending / muted disabled
  / dim "—" none) and a "Subnet" link in the per-user
  `<details>` menu. 6 new i18n keys (RU+EN). 2 new
  tests. 12/12 packages green, smoke 118/118, live
  on VM at build `3fc44a2`.
* **Previous**: v0.16.7 — hotfix: t vs tf arg count
  in update banner
  ([release notes](RELEASE-NOTES-v0.16.7.md)). The
  v0.16.6 release shipped an "update available" banner
  with `{{t "update.banner_body" .Version
  .UpdateLatest.TagName}}` — but `t` takes 1 arg, the
  call had 3. Every admin page rendered with only the
  banner (the only thing that survives a template
  panic mid-render) and no body. Operator reported it
  immediately. Fix: change to `{{tf ...}}` (varargs
  formatter). Plus `TestTemplateArgsMatchCatalog`
  regression guard in `templates_test.go` — walks
  every embedded template, verifies the arg count of
  every `{{t ...}}` / `{{tf ...}}` call matches the
  catalog's placeholder count for that key
  (handles `%%` escapes). 12/12 packages green,
  smoke 118/118, live on VM at build `19d8981`.
* **Previous**: v0.16.6 — per-user subnets foundation
  ([release notes](RELEASE-NOTES-v0.16.6.md)). The
  first concrete step of the 6-release per-user
  subnets roadmap (v0.16.6 → v0.19.0) documented in
  `docs/v0.16.0-open-questions.md` (8 operator
  decisions confirmed 2026-07-17). v0.16.6 ships the
  data model + CRUD + admin form + bot `/mysubnet`;
  the actual sidecar container management is the
  v0.16.7 follow-up. Adds:
  - `user_subnets` table (11 columns, UNIQUE on
    user_id + cidr, FK to portal_users ON DELETE
    CASCADE) + 3 denormalized columns on
    `portal_users` (`subnet_cidr`, `subnet_status`,
    `subnet_router_node_id`) — read by `/mysubnet`
    and `/admin/users/{id}` without JOIN
  - `control_plane_url` column on `user_subnets` for
    multi-plane (per-user headscale since v0.12.0)
  - `internal/subnet/allocator.go` — pure function
    `AllocateCIDR(userID) → 10.0.<uid>.0/24` (256
    users max; `/28` migration reserved as
    `subnet_bits` column without DB schema change)
  - `internal/subnet/manager.go` — CRUD layer with
    pre-check (avoids "FOREIGN KEY constraint
    failed") + `tx.Rollback` before `Get` (avoids
    SQLite write-lock deadlock after failed UNIQUE
    INSERT) + denorm sync on every mutation
  - `/admin/users/{id}/subnet` — 4 routes
    (allocate, disable, test, list) with idempotent
    allocate
  - Bot `/mysubnet` — reads denormalized columns
    (no JOIN), shows CIDR + status + router
    hostname + plane label
  - 30 new catalog keys (14 `bot.mysubnet.*` + 16
    `user_subnet.*`) RU+EN, parity test green
  - 21 new tests (4 allocator + 10 manager + 5
    admin + 2 bot)
  - 12/12 packages green, smoke 118/118, live on
    VM at build `a450fa7`.
* **Previous**: v0.16.5 — split long bot replies into
  multiple bubbles
  ([release notes](RELEASE-NOTES-v0.16.5.md)). The
  operator reported that on a phone, long bot replies
  (`/help`, `/audit`, `/my_rules`) are hard to scan
  because Telegram's default font is small and the
  entire reply sits in one bubble. Telegram's HTML
  subset has no font-size tag, so the cleanest fix is
  to break long replies into multiple shorter bubbles
  — each section gets its own screen real estate and
  the bubble boundary acts as a visual break. Adds
  `splitMessageMarker` sentinel + `splitReplyParts`
  helper. `RealNotifier.reply` detects the marker and
  issues separate `sendMessage` calls. Applied to:
  - `/help`: 3 bubbles (Auth / User-scope / Admin) for
    admin, 2 for user, 1 for locked
  - `/audit`: split if > 10 entries (LIMIT 20 max);
    first bubble ends with "(N more — see next
    message)" hint
  - `/my_rules`: split if > 12 rules; same hint
  5 new tests. 12/12 packages green, smoke 118/118,
  live on VM at build `22b97c8`.
* **Previous**: v0.16.4 — fix HTML-unsafe `<` / `>` in
  catalog keys
  ([release notes](RELEASE-NOTES-v0.16.4.md)). Hotfix
  for v0.16.3 — the v0.16.3 "more HTML" pass for `/help`
  shipped the reply with `parse_mode=HTML`, but several
  `bot.*` catalog keys still contained literal
  `<word>` placeholders (like `<команда>`, `<ключ>`,
  `<HEADSCALE_URL>`). Telegram's HTML parser rejects
  the whole `sendMessage` payload with HTTP 400
  "can't parse entities: Unsupported start tag" when
  it sees a literal `<word>` that isn't a known HTML
  tag — so the live `/help` was silently failing. Fix
  HTML-escapes 11 catalog keys (only the ones whose
  replies go through `parse_mode=HTML`; plain-text
  keys keep their literal `<word>`). New test
  `TestHTMLSafeCatalog` in `i18n_test.go` pins the
  contract. 12/12 packages green, smoke 118/118, live
  on VM at build `27ee8e6`.
* **Previous**: v0.16.3 — "more HTML" pass for /help
  ([release notes](RELEASE-NOTES-v0.16.3.md)). The
  v0.16.1/v0.16.2 "more HTML" pass left `/help` in
  plain text, so the catalog's markdown backticks
  (`<id>`, `<target>`, etc.) showed up as literal
  characters. This release:
  1) converts 37 `bot.help.*` catalog entries from
     markdown backticks to `<code>` tags (with `&`, `<`,
     `>` HTML-escaped inside the `<code>`)
  2) rewrites `helpReply` so each of the three sections
     (Auth / User-scope / Admin) renders as a tabular
     `<pre>` block with a 20-char gutter for the
     command column. `markHTMLReply()` at the top so
     `parse_mode=HTML` is set.
  1 test rewrite (`TestHelpReplyV0155Layout`) + 1 test
  extension (`TestHTMLRepliesMarkParseMode` adds
  the `/help` sub-case). 12/12 packages green, smoke
  118/118, live on VM at build `cdbefe5`.
* **Previous**: v0.16.2 — "more HTML" pass bug fix
  ([release notes](RELEASE-NOTES-v0.16.2.md)). Hotfix
  for v0.16.1 — the v0.16.1 release shipped HTML
  formatting in 8 bot replies but forgot to set
  `parse_mode=HTML` on the sendMessage payload, so the
  `<b>/<i>/<pre>/<code>` tags showed up as raw source
  text. Adds `markHTMLReply()` helper in
  `internal/telegram/commands.go` and calls it at the
  top of: `myStatusReply`, `myNodesReply`,
  `myRulesReply`, `myQuotaReply`, `myExitNodesReply`,
  `versionReply`, `auditReply`,
  `exitNodesHealthReply`. Also fixes a related bug
  inside `myExitNodesReply` where the inline-keyboard
  assignment was wiping the `ParseMode` set by
  `markHTMLReply`. 2 new tests (9 sub-cases total).
  12/12 packages green, smoke 118/118, live on VM at
  build `39d6af6`.
* **Previous**: v0.16.1 — "more HTML" pass
  ([release notes](RELEASE-NOTES-v0.16.1.md)). The
  "bot reply formatting should look like a table, not
  a wall of text" release. `internal/telegram/format.go`
  adds a small helper layer (`Field()` / `Section()` /
  `PreLinesRaw()` / `Code()` / `Header()` /
  `BulletList()` / `HeaderLine()`) and the remaining
  four read commands that were still in prose format
  now use the new helpers:
  * `/my_rules` — tabular `<pre>` (ID / EXIT / TYPE /
    TARGET / ACTION)
  * `/my_quota` — three `Field()` lines (rules / fill
    / cap) under a `Section()` divider
  * `/myexitnodes` — tabular `<pre>` (HOSTNAME / NODE /
    STATUS / DEFAULT) with a `Section()`+`Field()`
    summary, and the default marker is now `✓`
    (was `[default]`)
  * `/ack` — already clean (one-line summary), left
    unchanged
  * `~50 new catalog keys (RU+EN)`. `12/12 packages
    green`, smoke `118/118`, live on VM at build
    `006f3d5`.
* **Previous**: v0.16.0 — backlog release
  ([release notes](RELEASE-NOTES-v0.16.0.md)). The
  "clean up the deferred v0.12 / v0.13 backlog before
  tackling v0.16" release. Six previously-deferred
  features ship in one go:
  1. **v0.12.1 — per-user bot routing**. `BotEnv`
     carries `HSForPortalUser` and `PortalPlaneURL`
     closures; every `/add_device`, `/add_rule`,
     `/delrule` etc. now routes to the right
     control plane.
  2. **v0.13.0 — per-plane ACL**.
     `GenerateACLForPlane(planeURL)` only includes
     the identities on that plane. `ApplyACLForAllPlanes`
     iterates every distinct URL and pushes the
     right policy to each.
  3. **v0.13.0 — ACL import/export with dry-run
     preview**. `/admin/acls/export` downloads the
     current policy; `/admin/acls/import` accepts
     a JSON file or pasted text, shows a
     side-by-side dry-run, and only pushes when
     the operator clicks Apply.
  4. **Butler voice v3 — urgency marks**.
     `WithUrgency(level)` appends `!` (warning) or
     `!!` (critical) to the chosen icon, so `🔑!!`
     in the chat list reads as "critical preauth reply".
     Applied to `/add_device`.
  5. **Personal API token rotation**. `/my/token`
     now has a TTL dropdown (1h / 1d / 7d / 30d /
     never) and an auto-rotate checkbox. Expired
     tokens are rejected by the Bearer-auth path.
     Background rotation job is v0.16.0+ follow-up
     (column is in v0.15.5 so the UI can store + read).
  6. **Documentation**: per-user subnets roadmap
     entry in AGENTS.md + `docs/v0.16.0-open-questions.md`
     parking the 8 design decisions for the next
     major work.
  * All five backlog items done in one release —
    the v0.12 / v0.13 backlog is now empty.
  * 4 new v0.13.0 tests + 1 new v0.12.1 test + 1 new
    butler v3 test (6 sub-cases) + 1 schema migration
    test.
  * 12/12 packages green
* **Previous**: v0.15.6 — /admin/backup + /admin/exit-nodes
  full localization
  ([release notes](RELEASE-NOTES-v0.15.6.md)). The
  "no hardcoded English left in the admin pages" release.
  46 new catalog keys (RU + EN) cover the backup history
  table headers, the migration-to-another-host warning +
  5-item + 6-item ordered lists (with embedded `<code>`
  for the docker restart command), the "Run backup now?"
  JS confirm, the exit-nodes 5-step tutorial narrative
  (headings, "Run on the exit-node (one-time)" intro, the
  inline code-explanation paragraphs after the tailscale
  up command, and the long "for nodes that run other
  VPN services..." warning), the exit-nodes status pills
  (off / synced / idle), the accept-routes dropdown
  options (default / false / true with explanations), and
  the form label "Headscale Node ID". Code blocks in the
  tutorial stay verbatim — those are shell commands the
  operator types. After v0.15.6 every admin sidebar page
  has a complete Russian translation.
  * 46 new catalog keys (RU + EN, 92 entries)
  * `internal/handlers/templates/admin/backup.html`
  * `internal/handlers/templates/admin/exit_nodes.html`
  * 12/12 packages green, TestCatalogsParity +
    TestPlaceholderOrder + TestLoadTemplates all green
* **Previous**: v0.15.5 — admin body butler-voice polish +
  /help alignment + /unbind_self
  ([release notes](RELEASE-NOTES-v0.15.5.md)). The
  "admin replies should read like a butler, not a log;
  /help columns should line up" release. Three fixes:
  1. Drop log-voice prefixes (`sync_nodes:`, `audit:`,
     `exit_nodes_health:`, `restart:`, `add_rule:`,
     `delrule:`, `clearrules:`) from every admin reply
     and capitalise the first letter; the
     `target:` / `rule_ids=` / `ACL v#` technical
     fields stay verbatim, the `✓` / `⚠` status
     markers stay where they were.
  2. Widen the /help command gutter from 12 chars to
     18 (max command today is `/exit_nodes_health`
     at 17 chars) and drop the duplicate
     `\`<cmd>\` — <explanation>` from every description
     — the gutter is the command, the description is
     the explanation, the args hint lives at the end
     as `[args: <hint>]`.
  3. Add `/unbind_self` to the Auth section of /help
     (was in the dispatch table since v0.14.0 but
     missing from the listing).
  * ~80 catalog keys rewritten (RU + EN, ~160 entries)
  * `commands.go` `helpReply()` — `gutter` const 18,
    new `TestHelpReplyV0155Layout` pins the contract
  * 12/12 packages green, smoke 118/118, live on VM
    at build `7650c5e`
* **Previous**: v0.15.1 — final /admin/telegram localization
  ([release notes](RELEASE-NOTES-v0.15.1.md)). The
  "no hardcoded English left in the Telegram admin
  page" release. 32 new `telegram.*` keys × 2 langs
  cover the probe banner (3 states), status pills,
  the Send Test / Rotate token / Disable bot / Strict
  mode paths, and the where-to-look hints. i18n
  parity test green.
* **Previous**: v0.15.0 — HTTPS / TLS via Caddy
  ([release notes](RELEASE-NOTES-v0.15.0.md)). The
  "make the tailnet's control plane actually speak
  HTTPS" release. Adds a Caddy sidecar that terminates
  TLS for skygate, headscale, and headplane; auto-issues
  Let's Encrypt certs via the DNS-01 challenge (no
  port-80 inbound required); per-hostname routing
  inside a single 30-line Caddyfile. No nginx Proxy
  Manager, no PHP, no DB. DERP relay already did TLS
  itself (certmode=letsencrypt).
  * `docs/https-setup.md` — 17KB operator guide with
    per-module checklist, full rendered Caddyfile,
    verification commands, alternatives for tailnet-only
    / headscale-only / Tailscale TLS deployments.
  * `scripts/check_https.py` — deploy-time HTTPS check
    (TLS handshake, cert SAN, cert validity, HTTP→HTTPS
    redirect, HSTS on /login; --strict hard-fail
    variant). Wired into `make test`.
  * Per-module: skygate no change, headscale no change
    (gRPC stays `grpc_allow_insecure: true` because
    the hop is on the internal Docker network), headplane
    one env var (`COOKIE_SECURE=true`), DERP no change.
  * 8 new `.env` vars under "HTTPS reverse proxy
    (Caddy, v0.15.0)". DNS-01 API token in a separate
    0600 file (not in `.env`).
  * `make check-https` + `make check-https-strict`
    targets; `make test` now runs `check-https`.
  * 12/12 packages green, `bash -n deploy.sh` OK.
* **Previous**: v0.14.0 — bot UX overhaul
  ([release notes](RELEASE-NOTES-v0.14.0.md)). The
  "make the bot usable" release. Five operator-visible
  problems fixed: `/exit_nodes` empty (new
  `SyncNodesFromHeadscale` + admin button + `/sync_nodes`
  bot command), bot menu refresh path (`Refresh bot menu`
  button on `/admin/telegram`), `/help` restructured to a
  sectioned table (🔐 Auth / ✦ Your data / 🛠 Admin),
  inline keyboards for `/lang` + `/myexitnodes`, web
  update banner via `release.Monitor.Snapshot()`.
* **Previous**: v0.13.0 — exit-node health monitor
  ([release notes](RELEASE-NOTES-v0.13.0.md)). The
  "is my tailnet's egress actually working?" release.
  A background goroutine polls headscale every 5 min
  (`SKYGATE_EXIT_NODE_CHECK_INTERVAL`), classifies each
  configured exit-node as `online` / `degraded` / `offline`,
  surfaces the result on `/admin/exit-nodes` and the new
  `/exit_nodes_health` bot command, and dispatches
  **calm-mode** alerts (online↔offline only) via the existing
  Notifier. Plus a `--strict` flag on the deploy-time
  `check_exit_nodes.py` so CI / automated deploys can
  hard-fail when an exit-node is offline.
* **Previous**: v0.12.0.2 — Android exit-node routing + Telegram
  tab speed + admin tab RU
  ([release notes](RELEASE-NOTES-v0.12.0.2.md)). Three
  operator-visible follow-ups to v0.12.0.1:
  1. **Android exit-node routing restored** — the v0.12.0.1
     catch-all removal closed the inter-user security hole but
     also killed the internet-egress primitive that exit-node
     routing depends on. The last ACL rule is now
     `* → autogroup:internet:*` (Tailscale's standard
     internet-egress group, supported by headscale 0.23+).
     `autogroup:internet` explicitly excludes the 100.64.0.0/10
     tailnet range, so inter-user isolation is preserved.
  2. **`/admin/telegram` no longer blocks for 5 s on every
     page load** — added a 30 s result cache for the
     `api.telegram.org` reachability probe, keyed by the
     bot-token fingerprint. Save / rotate / disable / strict
     invalidate the cache eagerly. Subsequent GETs within the
     30 s window render in ~1.5 ms instead of 5 s.
  3. **Settings + Exit Rules admin tabs fully translated to
     RU** — 35 new `settings.*` / `exit_rules_admin.*` i18n
     keys wired through `{{t}}` / `{{tf}}` in the templates
     (the inline `<script>` for the sync status uses
     `{{t ... | safeJS}}`). 12/12 packages green, smoke
     118/118, live headscale policy verified (autogroup:internet
     present, no `*:*` catch-all).
* **Previous**: v0.12.0.1 — ACL catch-all security fix +
  /help Russian translation + login form fixes
  ([release notes](RELEASE-NOTES-v0.12.0.1.md)). Drops the
  literal `"*:*"` catch-all from the generated ACL to close
  the inter-user leak (each portal user could previously
  reach every other user's `tag:private` device via the
  catch-all's first-match fallback). The fix breaks exit-node
  routing on clients without explicit per-device rules;
  v0.12.0.2 restores it via `autogroup:internet`. Also:
  full Russian translation of `/help` (92 new `help.*` keys),
  login form `v0.2` hardcode → `{{.Version}}`, missing NVIDIA
  theme added to the picker.
* **Previous**: v0.12.0 — per-user headscale control plane
  ([release notes](RELEASE-NOTES-v0.12.0.md)). Skygate-as-shell
  step 2: each `portal_users` row now carries its own
  `(headscale_url, headscale_api_key)` override, encrypted
  with `SKYGATE_SECRET_KEY` (AES-GCM, 32 bytes hex). The
  per-user router (`App.HSForUser(userID)`) routes
  user-scoped requests (`/my/devices`, `/my/preauth`,
  `/my/keys`, `/my/exit-nodes`, `/dashboard`) to the user's
  own headscale; cross-user admin pages
  (`/admin/devices`) use `App.HSGlobal()` explicitly. New
  pages: `/admin/control-planes` (lists every distinct
  plane + user counts), `/admin/users/{id}/plane` (per-user
  edit form with URL + encrypted API key fields).
  35 new tests, 22 new i18n keys. Bot handlers
  (`/my_nodes`, `/admin_nodes` in the Telegram bot) still
  use the global `env.HS` — per-user bot routing is a
  v0.12.1 follow-up. `GenerateACL()` still writes to the
  global headscale; per-plane ACL is v0.13.0. 12/12
  packages green, smoke 118/118.
* **Previous**: v0.10.14 — /clearrules body i18n (закрытие
  RU-долга)
  ([release notes](RELEASE-NOTES-v0.10.14.md)). The last
  hardcoded-English path in the bot — `/clearrules` — now
  goes through `i18n.T` / `i18n.Tf` on every visible
  line. 5 new `bot.clearrules.mint_*` and
  `bot.clearrules.scan_error` keys (× 2 languages). Audit
  log details and the `Notifier.SendAlert` body on
  SetPolicy failure stay in English by design (operator
  surface, not user reply). 6 new
  `TestClearRulesReplyRussian*` tests pin the RU reply
  on every major branch.
* **What we're working on next (v0.12.0 candidates)**:
  - **Pluggable headscale per portal user (DONE in v0.12.0)** —
    `portal_users` gets `headscale_url` + `headscale_api_key_enc`
    columns (AES-GCM encrypted via SKYGATE_SECRET_KEY).
    HSForUser() routes user-scoped requests to the right
    plane. /admin/control-planes + /admin/users/{id}/plane.
  - **Per-user bot routing (v0.12.1)** — bot handlers still
    use the global env.HS; the BotEnv needs a
    HeadscaleRouter interface and a new dispatcher in
    notify.go. Small follow-up.
  - **Per-plane ACL (v0.13.0)** — GenerateACL() is still
    global. Per the v0.12.0 scope decision, v0.13.0 splits
    the per-user ACL by control plane (separate policy per
    plane, with the operator's-eye view of all planes on
    /admin/acls).
  - **ACL import/export** (v0.13.0)** — load a JSON policy
    file into the current ACL with a dry-run preview.
  - **`/clearrules` i18n** (DONE in v0.10.14)
  - **Butler voice v3** (deferred until user feedback on v2 lands):
    header carries urgency level (`🪶` / `🪶!` / `🪶!!`), body uses
    subtle inline color marks for status.
  - **Personal API token rotation** (admin override): TTL +
    auto-rotate field, so the bot integration can issue 24h / 7d /
    30d tokens. Currently tokens only have manual revocation.
  - **Per-user subnets + cross-subnet exit-node sharing
    (v0.16+ candidate, TBD with operator)** — architectural
    evolution. Today every portal user lives in the same
    flat `100.64.0.0/10` headscale, separated only by ACL.
    The next level is to give each user their own personal
    subnet (e.g. `10.0.<user_id>.0/24`) routed through a
    per-user subnet-router node, while keeping the existing
    `tag:exit-node` and `tag:public` infrastructure globally
    accessible to all subnets via ACL.

    **Why:** the flat 100.64.0.0/10 design works for the
    operator's current ~10-user tailnet, but the moment
    skygate grows to multiple customers (multi-tenant SaaS),
    per-user subnets become the cleaner primitive. They give:
    - IP-address predictability per user (user 42's devices
      are always in `10.0.42.0/24`)
    - Cleaner user-side firewall rules
      ("10.0.42.0/24 = my office")
    - Independent routing decisions per user
    - Foundation for per-user services (run a web server on
      `10.0.42.5:8080`, only that user reaches it)

    **Sketch (dependency chain — each release builds on the
    previous):**
    1. **v0.16.0 — schema + CIDR allocator**:
       - `user_subnets` table: `(user_id, cidr, router_node_id,
         created_at, status)` — per-row lifetime
       - `portal_users.subnet_cidr TEXT` for quick lookups
       - CIDR allocator: `10.0.<user_id>.0/24` (one /24 per
         user, up to 256 users in /16) or `/28` per user
         (up to 4096 users in /16). Operator chooses.
       - Admin UI: extend `/admin/control-planes` with a
         subnet map (which user owns which CIDR)
    2. **v0.16.1 — per-user subnet router node**:
       - Auto-create a "subnet router" headscale node per
         portal user on first login
       - New tag: `tag:subnet-router` (separate from
         `tag:private` and `tag:exit-node`)
       - Advertise routes: the user's personal CIDR +
         `0.0.0.0/0` + `::/0` (so the user can route through
         exit-nodes through the personal subnet)
       - Where the router runs is a TBD — options: (a) on
         the user's own machine (one-time `tailscale up` with
         `--advertise-routes`), (b) skygate-managed Docker
         sidecar per user, (c) shared skygate-side router
         that terminates all personal subnets. The
         sub-router-tag ACL allows (a) — the user runs
         their own Tailscale client with a per-user
         preauth key.
    3. **v0.17.0 — ACL for cross-subnet exit-node sharing**:
       - `GenerateACL()` gains per-user-subnet rules:
         `{src: ["<user>@tsnet"], dst: ["<user_subnet>:*"]}`
       - **Keep `tag:exit-node` global** — every user can
         still reach the exit-nodes regardless of which
         personal subnet they're in
         (the `* → tag:exit-node:*` rule already handles this)
       - Add `tag:subnet-router` to `tagOwners` so the
         headscale parser doesn't reject the new tag
       - Verify exit-node egress: the user's Tailscale
         client `--accept-routes`, then routes `0.0.0.0/0`
         through the exit-node advertised by the subnet
         router. End-to-end internet egress survives the
         subnet split.
    4. **v0.17.1 — cross-user subnet sharing** (the
       "share access to existing exit-nodes" angle, extended
       to personal subnets):
       - "Share my subnet with user X" button in
         /my/account or /admin/users/{id}/subnet
       - ACL: `{src: ["<user_X>@tsnet"], dst: ["<user_Y_subnet>:*"]}`
       - Bot: `/share_subnet <username>` for power users
       - **Exit-nodes are still global** — this only
         governs the per-user personal subnet. The
         `tag:exit-node` sharing is already in place from
         v0.12.0.1 and is unaffected by this change.
    5. **v0.18.0 — MagicDNS for personal subnets**:
       - `skygate-<username>.tailnet.skynas.ru` resolves to
         the user's subnet router
       - Per-device records:
         `<device>.skygate-<username>.tailnet.skynas.ru`
       - Per-user records:
         `exitnode.skygate-<username>.tailnet.skynas.ru` —
         this is the key one — points to the user's chosen
         exit-node, but reachable cross-subnet because
         `tag:exit-node` is in the user's ACL
    6. **v0.19.0 — per-user services on the personal
       subnet**:
       - Port forwarding: user can publish
         `10.0.42.5:8080 → service.skygate-<username>...`
       - Headscale "service" records (headscale 0.23+ feature
         that lets you publish a TCP/UDP service as a
         named DNS record)

    **The key insight the operator is asking for:** the
    exit-node layer (`tag:exit-node` + `tag:public`) stays
    shared across all subnets. The personal subnet adds a
    layer of IP-address predictability + service isolation
    on top, without breaking the global exit-node mesh
    that all the relays depend on. So:

    - User A on `10.0.42.0/24` can route to exit-nodes
      (emilia, sharlotta, karolina) just like today —
      the ACL still says `* → tag:exit-node:*`
    - User A can ALSO have their own personal services
      on `10.0.42.5` that only they can see
    - User A can SHARE their personal subnet with
      User B explicitly, without sharing with User C
    - All of this is orthogonal to the exit-node mesh,
      which keeps the relay model intact

    **Migration path:**
    - Existing users keep their `100.64.0.0/10` Tailscale
      IPs (no forced migration — that would break every
      running client)
    - New users get a personal subnet from day one
    - Admin can opt-in existing users one-by-one via
      `/admin/users/{id}/subnet` (creates a subnet router
      alongside their existing flat-IP device; the user's
      devices get optional `--advertise-routes` for the
      personal CIDR)
    - Once a user has BOTH a flat device AND a subnet
      router, their ACL has both — the subnet router
      starts being useful immediately, and the flat device
      can be phased out by the user at their own pace

    **Open questions for the operator:**
    - Where does the subnet router run? (user's own
      machine vs. skygate sidecar vs. shared router) —
      affects per-user operational cost
    - Does the bot get a `/mysubnet` command? Probably yes,
      parallel to `/myexitnodes` and `/mysettings`
    - CIDR strategy: /24 per user (256 users max in /16)
      or /28 per user (4096 users max)? Operator's choice
      based on customer count
    - Multi-plane (per-user headscale since v0.12.0): each
      plane is its own headscale, so its own /16 for
      subnets. The `user_subnets` table needs a
      `control_plane_url` column

---

## What is Skygate?

Tailscale/headscale management portal. Stack: **Go 1.23 + SQLite + Docker +
headscale 0.29 API + embedded HTML templates**.

Key features:
- **Exit-node rules** with per-device accept/deny ACL
- **Automatic DNS-driven /32 resolution** for domain rules (autoupdater)
- **Multi-user**, per-user rule limits (`SKYGATE_USER_MAX_RULES=skyadmin:2000`)
- **Per-device limits** (`SKYGATE_MAX_RULES_PER_DEVICE=500`)
- **Cleanup of orphaned /32** (admin endpoint)
- **Sync to exit-node advertised-routes** (staggered per node)
- **Per-user headscale ACL** (each user sees only their own devices)
- **Tag-aware device ownership** (`tag:private` per portal user,
  `tag:public` shared exit-nodes)
- **Personal API tokens** for AI integration

User-facing pages:
- `/my/exit-rules` — user's own rules (add/delete/filter/search/multi-delete)
- `/my/exit-rules/help` — full help page with API reference
- `/admin/exit-rules` — admin view of all users' rules
- `/admin/exit-rules/cleanup` — admin: merge duplicate device_ids
- `/admin/exit-rules/sync` — admin: trigger advertised-routes sync
- `/admin/exit-rules/rollback` — admin: rollback ACL to a previous version
- `/admin/devices` — admin: list of all nodes with manual tag/untag
- `/admin/devices/taged` — admin: POST to tag a node
- `/admin/users` — admin: user CRUD
- `/admin/acls` — admin: ACL view (read-only)
- `/admin/audit` — admin: audit_log view
- `/admin/derp` — admin: DERP relay status
- `/admin/exit-nodes` — admin: list exit nodes
- `/admin/backup` — admin: backup/restore ACL
- `/admin/telegram` — admin: bot config (token in `global_settings`, sendMessage via Go-native HTTP in `internal/telegram/notify.go`)
- `/my/account` — self-service password change (current + new + confirm)
- Rate limits (in-memory, single-instance only):
  - POST /login: 5 attempts per username per 15s, 20 per IP per 30s
  - /api endpoints: 30 requests per IP per 60s
  - 429 + Retry-After header on block; sweep every 5 min
- `/my/tokens` — personal API tokens
- `/my/devices` — user's devices (tagged via portal)

API:
- `GET/POST /my/exit-rules/api` — list / bulk create rules (Bearer auth or
  cookie). **POST returns `{added, duplicates, errors, ids: [N1, N2, ...]}`
  so clients can clean up.**
- `POST /my/exit-rules/delete` — delete one (`id=X`) or many (`ids=X&ids=Y&...`)

---

## Per-user control plane: when to use (v0.23.0/v0.23.1)

The v0.23.0 + v0.23.1 releases added a "one-click per-user
headscale" capability. **This is a compliance tier, not the
default path.** The architectural decision documented in
[RELEASE-NOTES-v0.23.1.md](RELEASE-NOTES-v0.23.1.md) is:

> "Per-user control plane (v0.23.0) requires re-auth of all
>  devices, and the user loses access to shared exit-nodes
>  (emilia/sharlotta/karolina) and mesh bridges with other
>  users. For most scenarios, per-user subnet already works
>  as a logical namespace in the global headscale (v0.16.6+).
>  Use v0.23.0 provisioning ONLY for compliance tier (SOX,
>  multi-tenant SaaS, geographic isolation)."

The reason: **Tailscale's protocol is one control server per
node**. Two headscales cannot share nodes. If user A is in
`headscale-A` and user B is in `headscale-B`, they cannot
see each other's devices, even if both are in the same
physical network. Cross-control-server routing does not
exist (Tailnet Lock/Sharing is enterprise-only, not in
headscale 0.29.x).

### When to use per-user control plane (v0.23.0)

Use ONLY when the operator has a real need for:
- **SOX / compliance**: tenant isolation, audit log separation,
  per-tenant API keys (compliance audit)
- **Multi-tenant SaaS**: each "customer" gets their own
  headscale container (no shared resources)
- **Geographic isolation**: per-region control plane (e.g.
  US users on us-east, EU users on eu-west)
- **Tailnet Key rotation**: per-tenant key with independent
  noise_private.key

### When NOT to use per-user control plane

The default path. **Don't use v0.23.0 for any of these** —
they're already solved by the global headscale:
- "Per-user subnet" — v0.16.6+ gives each user `10.0.<uid>.0/24`
  as a logical ACL namespace
- "Shared exit-nodes" — `tag:exit-node` in global ACL makes
  emilia/sharlotta/karolina accessible from all users
- "Mesh between users" — v0.22.0 N-way bridge gives
  cross-user subnet visibility via ACL cross-CIDR
- "Cross-user share" — v0.17.1 share rows
- "Tailscale --accept-routes" — works in global

### How to provision (when actually needed)

1. Open `/admin/users/{id}/plane`
2. Read the warning card carefully (re-auth cost, lost access)
3. Click "Provision per-user headscale"
4. Confirm the JS dialog
5. Wait ~15s for the container to come up
6. SSH to each of the user's devices, run:
   ```
   sudo tailscale logout
   sudo tailscale up --login-server=https://head.<username>.skynas.ru \
     --authkey=<preauth from /admin/users/{id}/plane>
   ```
7. The user is now on their own control plane. The old
  device entries in the global headscale become orphaned
  (delete them via `docker exec headscale headscale nodes
  delete -i <N>`).

### How to deprovision

1. Open `/admin/users/{id}/plane` (user must be on per-user)
2. Click "Decommission per-user headscale"
3. Confirm the JS dialog
4. The container is stopped, the per-user data dir is
  preserved at `~/.decommissioned-<ts>` (recoverable for 30
  days)
5. The DB override is cleared — `HSForUser(uid)` falls back
  to `HSGlobal()`. The user's devices (still in the per-user
  headscale) are now invisible to skygate until they re-auth
  to the global headscale.

---

## v0.16.0+ per-user subnets (DEFAULT — use this)

For the 4 prod users (skyadmin/michail/guest/daniil), the
default path is per-user subnets in the global headscale
(v0.16.6+). Each user has `10.0.<uid>.0/24` as a logical
ACL namespace. Exit-nodes are shared. Mesh is cross-user.
No re-auth, no separate control plane. **Use this for 95% of
scenarios.**

### Operational note: fixing `node_owner_map` attribution for tag-bearing devices

**Symptom**: A user has 5+ devices in headscale (all with
`tag:private`), but their `/my/devices` page shows 0 devices.
`portal_users.subnet_status` stays `pending` even though the
user clearly has devices. Querying `node_owner_map` shows
all the user's rows with `username=tagged-devices` instead
of the user's actual username.

**Root cause** (v0.3.9 + v0.22.2 limitation): When headscale
applies a tag to a node, it reassigns ownership to a
synthetic `tagged-devices` user. The `backfillNodeOwnership`
function tries to recover the original owner via two
strategies:

- **Strategy A**: match `node.PreAuthKeyID` against a
  stored preauth (`preauth_keys.headscale_preauth_id`).
  Requires the preauth to have been issued through skygate
  AND have its headscale_id captured.
- **Strategy C**: temporal fallback — node created within
  1 hour of a preauth. Only works for very fresh devices.

For devices registered before v0.12.0 (when
`headscale_preauth_id` capture was added), Strategy A
cannot match. Strategy C doesn't work for old devices. The
manual recovery path is needed.

**Fix** (one-off, applied 2026-07-21 for skyadmin): update
`node_owner_map` to attribute the known devices to the
right user:

```sql
UPDATE node_owner_map
   SET username = 'skyadmin', tag = 'tag:private', tagged_by_user_id = 1
 WHERE hostname IN ('skyworker','skybars','skybars-1',
                     'skygate-vm','desktop-cuo0tfb','msi');
```

After the UPDATE, the next `/my/devices` load (which fires
`backfillNodeOwnership` → `subnet.SyncStatus`) flips the
status from `pending` to `active`. The `backfillNodeOwnership`
GC pass doesn't undo the manual fix (it only removes rows
for nodes that no longer exist in headscale, not for nodes
that exist with the wrong username).

The `fix_skyadmin_attribution.sh` script in the repo root
does this end-to-end (UPDATE → trigger → verify). It's
idempotent — re-running is a no-op.

**When to use**:
- A user has devices in headscale but `node_owner_map` has
  them as `tagged-devices` (look for the symptom above).
- The operator can enumerate the user's devices (by host
  or by checking `headscale nodes list -o json | jq` for
  `user.name == "tagged-devices"` and matching the device
  by preauth or registration time).
- The preauth was issued before v0.12.0, so
  `headscale_preauth_id` is NULL.

**When NOT to use**:
- New devices (post-v0.12.0) have `headscale_preauth_id`
  captured at issue time, so the backfill attributes them
  automatically. No manual fix needed.
- The user has no devices in headscale (the `pending` status
  is correct — they're not opted in to Tailscale yet).

### Operational note: node-expiry watcher (v0.23.3, the "device won't stay connected" release)

**Symptom**: User generates a preauth via `/my/preauth`,
pastes the key into a Tailscale client, the client
registers successfully, but the device disconnects within
seconds and never reconnects. The preauth is now `used=true`,
so the user can't re-register with it either. The Android
client shows "Sign in" with a key that was never accepted.

**Root cause** (discovered 2026-07-21 with the operator's
Android phone / node 10 / skybars): Tailscale 1.98.x's
`RegisterRequest.Expiry` field is only 2-4 seconds in
the future. headscale 0.29.x's `HandleNodeFromAuthPath`
(in `hscontrol/state.go`) applies that Expiry verbatim:

```go
if !node.IsTagged() {
    if !regReq.Expiry.IsZero() {
        node.Expiry = &regReq.Expiry
    } else if s.cfg.Node.Expiry > 0 {
        // ...
    } else {
        node.Expiry = nil
    }
}
```

The next netmap push to the client reports
`Expired: true, MachineAuthorized: false`, the client
interprets this as "your key was rejected, log out", and
the device goes back to `NeedsLogin`. The preauth is
already `used=true`, so re-registration is impossible.

**Fix** (v0.23.3): a background goroutine in
`internal/expirewatch` ticks every 5 minutes, walks
every non-tagged node in headscale, and extends any node
whose Expiry is missing or within 7 days of "now" out
to 30 days. Tagged nodes (`tag:exit-node`, `tag:public`,
`tag:subnet-router`, `tag:client`, …) are skipped because
headscale's `state.go` explicitly guards
`if !node.IsTagged()` around the regReq.Expiry branch,
so they keep their nil/none Expiry naturally.

**Verification**:
- `bash /tmp/check_v0.23.3.sh` — live test: force a
  node's expiry to 2s, wait for the watcher to tick,
  confirm the expiry is now at least 7d out and an
  `audit_log` row with `username=expirewatch,
  action=renewed, detail=node_id=<N> old_expiry=<...>
  new_expiry=<...>` was written.
- `docker logs skygate | grep expirewatch.tick` — every
  tick logs `seen=N renewed=N skipped=N errors=N`.
- The audit log table itself — every renewal is one
  row, queryable via `/admin/audit?action=renewed` (or
  `?username=expirewatch`).

**Tuning** (env vars, all optional, defaults are fine):
- `SKYGATE_EXPIREWATCH_ENABLED=true` — `false` disables
  the goroutine entirely.
- `SKYGATE_EXPIREWATCH_INTERVAL=5m` — tick frequency.
  `off` / `0` disables. Set to `1m` for faster recovery
  in exchange for more API calls.
- `SKYGATE_EXPIREWATCH_THRESHOLD=168h` (7d) — nodes
  within this window get renewed.
- `SKYGATE_EXPIREWATCH_RENEWAL=720h` (30d) — new
  expiry when renewing.

**One-shot manual fix** (if you can't immediately
deploy v0.23.3 or the watcher is disabled):
```bash
docker exec headscale headscale nodes expire \
  -i <NODE_ID> --expiry "$(date -u -d '+30 days' +'%Y-%m-%dT%H:%M:%SZ')"
```
The CLI `headscale nodes expire -i <id> --disable` also
works for "node never expires" — used for tagged nodes
manually, but the watcher skips tagged nodes so this
shouldn't be needed in normal operation.

**When NOT to look here**:
- A device that never registered in the first place
  (the issue is the preauth issuance path, not expiry
  — check `preauth_issued` audit events).
- A device that registered but immediately got the
  wrong ACL (issue is the policy, not expiry — check
  `headscale policy get` and the
  `/admin/devices/{id}/tag` flow).

---

## Code structure (where to look)

```
cmd/skygate/main.go                                — entry point, HTTP routes
internal/handlers/handlers.go                       — shared infra only: App struct + New + render/renderWithLayout + pageFromName/pageTitle/dataValue + currentUser/audit + getMaxRulesForUser (~257 lines)
internal/handlers/handlers_dashboard.go             — TailnetMetrics + PreauthKeyStats types + computeTailnetMetrics + GetDashboard + countMyPreauthKeys (~185 lines)
internal/handlers/handlers_auth.go                  — GetLogin/PostLogin/PostLogout + i18n PostLang cookie (~93 lines)
internal/handlers/handlers_node_ownership.go        — backfillNodeOwnership + firstTagOrFallback helper (Strategy C temporal preauth->tag:private match) (~248 lines)
internal/handlers/handlers_my_account.go            — self-service password change at /my/account (~84 lines)
internal/handlers/handlers_api_tokens.go            — personal API tokens (Bearer auth) at /my/tokens (~52 lines)
internal/handlers/handlers_admin_pages.go           — admin read-only views: /admin/audit, /admin/acls (~58 lines)
internal/handlers/handlers_derp.go                  — /admin/derp handlers + DerpStatus/DerpPeer/ConnSummary/DerpSnapshot types (~115 lines)
internal/handlers/handlers_derp_collect.go          — collectDerpStatus + httpGet + parseDerper{DebugHTML,Vars} (fetch & parse derper debug endpoints) (~245 lines)
internal/handlers/handlers_derp_classify.go         — classifyDerpPeer(s) + summarizeDerpPeers + derpLAN/derpTailscale/derpPeerNPM constants (~80 lines)
internal/handlers/handlers_admin_users.go           — admin user CRUD (~209 lines)
internal/handlers/handlers_admin_nodes.go           — admin device/tag handlers (~91 lines)
internal/handlers/exit_rules.go                     — DeviceRule struct + DB helpers (insertRuleUnique, getDeviceRules, getUserDevices) + GenerateACL() + ACL helpers (~359 lines)
internal/handlers/exit_rules_form_my.go             — /my/exit-rules: GetMyExitRules (incl. ?script= download), PostMyExitRule (DNS resolve + dedup), PostDeleteExitRule (multi-delete with cascade); owns countUserFacing closure (~625 lines)
internal/handlers/exit_rules_form_admin.go          — /admin/exit-rules: AdminExitRules (cross-user hierarchical view) (~165 lines)
internal/handlers/exit_rules_form_rollback.go       — /admin/exit-rules/rollback: PostAdminRollbackACL (~40 lines)
internal/handlers/exit_rules_form_reapply.go        — /admin/exit-rules/reapply: PostAdminACLReapply (Этап 14 v7, push current GenerateACL output to headscale without needing exit-rule churn) (~57 lines)
internal/acl/acl.go                                 — Free function GenerateACL(db) (per-user policy + ssh rules + tagOwners). Was inside exit_rules.go before v7 (~190 lines)
internal/acl/acl_test.go                            — TestGenerateACLValidJSONShape + TestGenerateACLIncludesDeviceRules + per-identity tests (~210 lines)
internal/handlers/exit_rules_api.go                 — public REST API (~159 lines)
internal/handlers/exit_rules_sync.go                — ACL sync, staggeredSync, autoupdater (~387 lines)
internal/handlers/exit_rules_routescript.go              — route-setup script orchestrator: GenerateRouteSetupScript (~42 lines)
internal/handlers/exit_rules_routescript_data.go         — DB query (loadRoutesForScript) + HS exit-node IP lookup (resolveExitNodeIPForScript) + routeEntry struct (~67 lines)
internal/handlers/exit_rules_routescript_windows_body.go — buildWindowsRouteScript + writeWindows{Setup,Restore}Script helpers — pure .cmd builder, no I/O (~185 lines)
internal/handlers/exit_rules_routescript_linux_body.go   — buildLinuxRouteScript + writeLinux{Setup,Restore}Script helpers — pure .sh builder for Linux + macOS, no I/O (~147 lines)
internal/handlers/exit_rules_cleanup.go              — admin cleanup + orphan /32 cleanup (~357 lines)
internal/handlers/admin_backup.go                   — admin backup/restore ACL (~247 lines)
internal/handlers/admin_telegram.go                 — admin telegram UI + save/test/rotate/disable (~303 lines)
internal/handlers/admin_exit_nodes.go               — admin exit nodes (~164 lines)
internal/telegram/notify.go                         — Notifier interface + RealNotifier (hot-swap, getUpdates loop) + reply/send HTTP (~245 lines)
internal/telegram/commands.go                       — `BotEnv` + `HandleCommand` dispatch + /status + /help (~96 lines)
internal/telegram/commands_phase2.go                — /nodes + /rules + /audit (DB queries, trimForTelegram) (~166 lines)
internal/telegram/commands_phase3.go                — /exit_nodes + /quota + /ack + unixToShort (~222 lines)
internal/telegram/commands_phase4.go                — /version + /restart (token confirm, SIGTERM) + /help <command> (~205 lines)
internal/telegram/alerts.go                         — `SendAlert` on Notifier + telegram_alerts ring buffer (cap 500) (~85 lines)
internal/handlers/templates.go                      — `//go:embed` for all HTML (~117 lines)
internal/handlers/static.go                         — empty stub (file is unused placeholder)
internal/handlers/templates/exit_rules.html         — /my/exit-rules UI (filter, search, multi-delete)
internal/handlers/templates/exit_rules_help.html    — /my/exit-rules/help page
internal/handlers/templates/admin/                  — admin templates
internal/handlers/templates/user/                   — user-facing templates (/my/devices, account, exit_nodes, tokens, etc.)
internal/config/config.go                           — env-based config
internal/db/secrets.go                              — telegram/bot credentials (encrypted at rest)
internal/headscale/                                 — headscale API client (incl. CLI fallback for tag/untag). Split:
  - headscale.go (3.5 KB) — Client struct, New, HTTP do() helper, InvalidateCache + cache fields
  - users.go (3.8 KB)     — HSUser, ListUsers, CreateUser, DeleteUser
  - preauth.go (6.8 KB)   — PreauthKey, CreatePreauthKey, ExpirePreauthKey (API + docker exec CLI fallback)
  - nodes.go (8.1 KB)     — HSNode, NodeView, ListAllNodes, ListNodesByUser, ListExitNodes, DeleteNode, NodeList, NodeInfo + hasExitNodeTag
  - tags.go (3.5 KB)      — TagPublicTag, TagPrivateTag, TagNode, UntagNode + IsPublic/IsPublicView/IsPrivateView
  - acl.go (3.7 KB)       — ACLPolicy, GetACL (cached), SetPolicy (API + file-mode fallback)
  - routes.go (4.3 KB)    — ApproveAllRoutes* (headscale CLI) + SetAdvertisedRoutes (SSH)
  - route_args.go (3.3 KB) — pure helpers for `tailscale set` command (BuildTailscaleSetRoutes, AcceptRoutesFlag)
  - headscale_test.go + route_args_test.go — unit tests (parseDuration, durationFlag, hasExitNodeTag, IsPublic*)
internal/db/                                        — SQLite layer
internal/auth/                                      — JWT session + API tokens
internal/handlers/templates/themes.css              — CSS embedded from static/css/themes.css
deploy/{deploy,backup,validate}.sh                  — deployment scripts
scripts/smoke.sh                                    — 56-step HTTP smoke test (uses make test)
scripts/check_exit_nodes.py                         — verifies all exit-nodes advertise 0.0.0.0/0 + ::/0
scripts/audit_routes.py                             — static main.go vs handlers route-vs-handler audit
Makefile                                            — build / run / test / smoke / audit targets
AGENTS.md                                           — this file
```

---

## Per-user headscale ACL policy

`GenerateACL()` in `internal/acl/acl.go` (was inside `internal/handlers/exit_rules.go` before Этап 14 v7; extracted to its own package so the telegram bot can call it without an `*App` reference) builds a **per-user** headscale ACL using identities from `portal_users`. The catch-all `*:*` rule that used to be first is REMOVED.

```json
{
  "acls": [
    {"src": ["skyadmin@tsnet.skynas.ru"], "dst": ["skyadmin@tsnet.skynas.ru:*"]},
    {"src": ["michail@tsnet.skynas.ru"], "dst": ["michail@tsnet.skynas.ru:*"]},
    ... per-device exit-rule targets (DNS, telegram IPs, etc) ...
    {"src": ["*"], "dst": ["tag:public:*"]},
    {"src": ["*"], "dst": ["tag:exit-node:*"]},
    {"src": ["*"], "dst": ["*:*"]}    // internet egress (last rule)
  ],
  "tagOwners": {
    "tag:private":   ["skyadmin@...", "michail@...", ...ALL portal users...],
    "tag:public":    ["skyadmin@tsnet.skynas.ru"],
    "tag:exit-node": ["skyadmin@tsnet.skynas.ru"]   // added in v7 — was missing
  },
  "groups": { "group:skyadmin": [...], "group:michail": [...], ... },
  "ssh": [
    {"action":"accept","src":["tag:private","skyadmin@…"],"dst":["tag:exit-node"],"users":["root"]},
    {"action":"accept","src":["skyadmin@…"],"dst":["tag:public"],"users":["root"]}
  ]
}
```

Tailscale ACL semantics: **first matching rule wins**. The catch-all `*:*` rule
that used to be first is gone; only the per-user rule applies to most traffic.
Each user can only talk to their own tag:private devices. tag:public /
tag:exit-node are visible to everyone (so users can pick exit-nodes).

**When editing `GenerateACL()`**: do NOT add `{"*", "*:*"}` as the first rule.
First-match semantics make it override everything else. The internet egress
must remain LAST, after per-user and tag rules. Also remember that every
`tag:*` referenced in `acls[]` or `ssh[]` must have a corresponding entry in
`tagOwners{}` (the v7 fix that broke reapply otherwise — see
"Admin SSH into tag:public relays" above for the full story).

The headscale base domain is hard-coded as `tsnet.skynas.ru` for now — it
is the only deployment. If you add another deployment, refactor to read it
from `config.Config`.

---

## Tailscale in skygate (Этап 14 v2 + v3 + v7, 2026-07-14)

The skygate container runs `tailscaled` in its own network namespace
and joins the tailnet with `tailscale up --accept-routes --accept-dns=false`.
The default-flag set has been `--accept-routes` only (no `--exit-node`):
the bot's traffic to api.telegram.org used to be routed through a
relay's subnet routes rather than a global exit-node. As of Этап 14
v7 the operator unified the relay model (see "Unified exit-node +
accept-routes" below) and may switch skygate to
`tailscale up --accept-routes --exit-node=<chosen-relay>` —
either is fine; the probe (described further down) is the source of
truth for whether a packet actually goes through Tailscale.

### Why not a sidecar (Этап 14 v2)

* **Sidecar (skygate-ts, removed in Этап 14 v2)**: `network_mode:
  service:tailscale` broke docker's embedded DNS (127.0.0.11:53
  refused UDP). The sidecar's `entrypoint.sh` also called
  `tailscale up --state=...` with a flag `tailscale up` doesn't
  accept, so the sidecar died at startup and took skygate down
  with it (exit 137).
* **Subnets-route / accept-routes model won** (Этап 14 v2) because
  per-destination routing keeps Docker's DNS, doesn't hijack the
  default route, and is auditable.

### Container layout

* `Dockerfile` (multi-stage): pulls `tailscale` + `tailscaled` from
  `tailscale/tailscale:latest`, copies them into the skygate runtime
  image along with `iptables`, `ip6tables`, `libcap`, etc.
* `entrypoint.sh`: if `TS_AUTHKEY_FILE` is set, starts `tailscaled`,
  runs `tailscale up --accept-routes --accept-dns=false`. Otherwise
  logs "Tailscale skipped (non-RF mode)" and continues with the
  skygate build. tailscaled is reparented to skygate (PID 1) when
  skygate execs.
* `docker-compose.yml`: skygate gets `NET_ADMIN` + `SYS_ADMIN` +
  `/dev/net/tun` + the `ts_authkey` docker secret. Tailscale state
  persists at `./data/ts/` across container restarts so we don't
  re-auth on every `docker compose restart`.

### `--accept-dns=false` is required

Tailscale's MagicDNS replaces `/etc/resolv.conf` with `100.100.100.100`,
which only knows about tailnet names. The Docker service name
`headscale` (used by `HEADSCALE_URL=http://headscale:50444`) stops
resolving, and skygate's API client dies with "lookup headscale on
100.100.100.100:53: no such host". With `--accept-dns=false` the
container keeps Docker's `127.0.0.11` DNS, and only the tailnet's
subnet routes (not its DNS) are accepted. Tailnet-name resolution
isn't currently needed.

### Unified exit-node + accept-routes (Этап 14 v7, 2026-07-14)

The project principle (confirmed by the operator) is that **every
relay node does BOTH things** and is interchangeable:

  1. **Exit node** — `tailscale set --advertise-exit-node` makes
     a node appear in the client's exit-node menu.
  2. **Accept-routes (subnet routes)** — the same node advertises
     a set of CIDRs that other tailnet members receive when they
     run `tailscale up --accept-routes`. The exit-node client then
     has both its default route AND the subnet routes pointing at
     that node, with the kernel doing the right thing for each
     destination.

There is no "Telegram-special" logic and no "primary" exit node.
skygate-vm is a regular client — it can be pointed at any relay,
and the operator may change it if a relay becomes flaky. The
client's Tailscale GUI shows all available exit nodes and
auto-failover happens at the metric level (Tailscale native).

The three relay nodes (Этап 14 v7 state):

* **emilia** (100.64.0.3) — exit-node + Telegram 8 v4 + 4 v6 CIDRs
  (`91.108.4.0/22` etc.) + 2 v6 (Telegram 2001:.../48). Approx 14
  routes, all approved.
* **sharlotta** (100.64.0.4) — exit-node + the same Telegram 8 v4
  + 4 v6 CIDRs as emilia. Approx 10 routes, all approved.
* **karolina** (100.64.0.2) — exit-node + ~148 PrimaryRoutes that
  were configured by the operator's Windows setup (WARP/Google/
  Cloudflare/Telegram/Amazon/... — whatever `tailscale up` was
  told to advertise on the operator's box). Approved as-is, do
  not touch without explicit operator request.

For an admin to enable exit-node on a fresh relay:

```bash
# On the relay (as root or via sudo):
sudo tailscale set --advertise-exit-node
# Then on the headscale host:
docker exec headscale headscale nodes approve-routes \
  --identifier <N> --routes 0.0.0.0/0,::/0
```

To re-synchronise karolina's full route set after a re-install:

```bash
# On headscale host (uses headscale API key from .env):
API_KEY=$(grep ^HEADSCALE_API_KEY= /home/skyadmin/skygate/.env | cut -d= -f2-)
ROUTES=$(curl -s -H "Authorization: Bearer $API_KEY" \
  http://localhost:50444/api/v1/node/11 | python3 -c \
  "import sys,json; print(','.join(json.load(sys.stdin)['node']['availableRoutes']))")
docker exec headscale headscale nodes approve-routes \
  --identifier 11 --routes "$ROUTES"
```

### Relay setup scripts

* `deploy/tailscale-relay/setup.sh` — one-time per node: joins
  tailnet, advertises the canonical Telegram 8 v4 + 4 v6 CIDRs.
* `deploy/tailscale-relay/update-routes.sh` — cron-friendly refresh
  of the Telegram IP ranges. Resolves api.telegram.org from three
  public resolvers, aggregates to canonical CIDRs, re-applies.
  Refuses to apply an empty route list.
* `Makefile` has a `tailscale-update-telegram-routes RELAY=<host>`
  target that SSHes to the relay and runs the update script.

### 3-state reachability probe

`/admin/telegram` runs a 5s GET probe to api.telegram.org on every
page load. Banner shows one of three states:

* **ok_direct** — kernel route for the resolved IPs goes via
  eth0 (direct internet, no Tailscale involvement for this
  destination). Typical for non-RF VPSes.
* **ok_relay** — kernel route for the resolved IPs goes via
  tailscale0, which means a relay's subnet route covers the
  destination. Typical for RF deployments.
* **unreachable** — 5s timeout, 5xx, or DNS failure. Banner shows
  a troubleshooting bullet list with the resolved IPs.

The check is per-IP via `ip route get <ip>` (shell-out with a
2s timeout safety net). It's more accurate than the v1
"is tailscaled running" heuristic — tailscaled can be running
(joining the tailnet for admin / headscale access) without any
subnet route covering api.telegram.org, in which case the actual
traffic still goes via eth0. The kernel routing table is the
source of truth for "would this packet go via Tailscale?".

Implementation: `internal/handlers/handlers_telegram_probe.go` +
tests in `handlers_telegram_probe_test.go` (17 unit tests, all
PASS — including `TestProbeDirectEvenWithTailscaled` which is
the explicit regression guard for the v1 → v2 behavior fix).
Template: `internal/handlers/templates/admin/telegram.html`
(`.alert-probe` / `.probe-ok-direct` / `.probe-ok-relay` /
`.probe-unreachable`).

### Relay failover (Этап 14 v3)

All three relays offer the same exit-node capability. Tailscale's
client GUI lists them all; the client picks based on metric and
auto-failover is native. If a relay goes down, the client just
uses the next one — no skygate-side logic involved.

`update-routes.sh` on emilia and sharlotta is still cron'd weekly
(`0 4 * * 1`) to refresh the Telegram CIDR list from DNS. The
operator's karolina route set is a one-shot — no cron.

### Admin SSH into tag:public relays (Этап 14 v7)

The default headscale ACL is per-user isolation; without an
explicit rule, no Tailscale peer can SSH into the relay VPSes
(emilia, sharlotta, karolina) because the broker-level `acls[]`
rule "allow * → tag:public:*" is overridden by Tailscale's
SSH-enforcement layer (which only consults `ssh[]`).

Two pieces are required to make admin SSH work:

1. **ACL rule** in `internal/acl/acl.go`:
   ```json
   {"action":"accept","src":["skyadmin@tsnet.skynas.ru"],
    "dst":["tag:public"],"users":["root"]}
   ```
   The existing `tag:exit-node` rule is preserved. Both rules
   must be present in the rendered JSON (asserted by
   `TestGenerateACLValidJSONShape`).
2. **tagOwners entry**: `tag:exit-node` is referenced in the
   SSH rules and elsewhere in the policy, so the parser requires
   it in `tagOwners`. Without it, `headscale policy set` rejects
   the policy with "tag not found: tag:exit-node".

After editing `acl.go` (e.g. to add new tags or new rules), the
policy must be re-applied. Three paths exist:

  - `POST /my/exit-rules` or `POST /my/exit-rules/delete` —
    any data change to exit rules triggers a SetPolicy
  - `POST /admin/exit-rules/rollback` — restore a previous
    `acl_snapshots` row
  - **NEW in v7**: `POST /admin/exit-rules/reapply` — regenerates
    the policy from the current DB state and pushes to headscale.
    Use this when only the *shape* of the policy changed (a new
    SSH rule, a new tag) but no exit rule was added/removed.
    Has a "Re-apply ACL" button on `/admin/exit-rules` (admin-only).

Tailscale on each relay polls for the new ACL within ~5-10 min
(usually faster). Until then, SSH from a Tailscale client to that
relay still says "tailnet policy does not permit you to SSH".

### Files for this feature

* `Dockerfile` — multi-stage with tailscale binaries
* `entrypoint.sh` — tailscaled + tailscale up --accept-routes
* `docker-compose.yml` — caps + tun + secret
* `internal/handlers/handlers_telegram_probe.go` — probe logic
* `internal/handlers/handlers_telegram_probe_test.go` — 17 tests
* `internal/handlers/admin_telegram.go` — integrates probe
* `internal/handlers/templates/admin/telegram.html` — banner
* `static/css/themes.css` — probe-state CSS
* `deploy/tailscale-relay/setup.sh` — one-time relay setup
* `deploy/tailscale-relay/update-routes.sh` — IP refresh
* `docs/telegram-relay.md` — full procedure + troubleshooting
* `docs/headplane.md` — Headplane (optional sidecar UI) integration
  contract, version pin policy, compatibility matrix, optional/required
  status, upgrade procedure, **existing-Headplane mode
  (`HEADPLANE_EXTERNAL_URL`)** added in v0.10.12. The module is documented as a peer
  service that talks to Headscale independently — Skygate has no
  code-level integration with it.
* `docs/derp.md` — DERP relay (bundled + existing) integration
  contract. `DERP_ENABLED` and `DERP_EXTERNAL_URLS` cover both
  modes; admin-side web-UI config is the v0.11.0 follow-up.
* `docs/skygate-as-shell.md` — the v0.11.0+ roadmap for
  pluggable Headscale / multi-control-plane / ACL import.
  Architectural doc, no code; tracks B and C from the
  user's "shelled module" idea.
  service that talks to Headscale independently — Skygate has no
  code-level integration with it.
* `internal/acl/acl.go` — GenerateACL (per-user policy + ssh rules
  + tagOwners). Edit + reapply via `/admin/exit-rules/reapply`.
* `internal/handlers/exit_rules_form_reapply.go` — admin
  "Re-apply ACL" endpoint (v7)
* `internal/handlers/templates/admin/exit_rules.html` — adds
  "Re-apply ACL" button to the admin exit-rules page (v7)

---

## Node tagging (tag:private auto-applied)

`backfillNodeOwnership` (method on `*App` since commit `cebabab`) propagates
each portal user's nodes from skygate `node_owner_map` to headscale:

- **Direct match**: `node.PreAuthKeyID == preauth_keys.headscale_preauth_id`
- **Temporal fallback (Strategy C)**: preauth key created within 1 hour before
  the node was registered — sets `matchedTag = "tag:private"` for the matched
  node, calls `HS.TagNode(nodeIDInt, "tag:private")` to push to headscale,
  and clears tag:untagged rows via UPDATE-then-INSERT.

When the backfill injects `tag:private`, existing `tag:public` exit-node rows
are **preserved** (the UPDATE only fires when the current tag is empty or
`tag:untagged`). Admin still owns `PostAdminNodeTag` for manual overrides.

The UI at `/my/devices` shows the local `node_owner_map.tag` snapshot (so the
Tailscale Android client must wait ~60 s after a tag change for ACL updates
to propagate through to the Tailscale clients).

---

## Tailnet node state (Этап 14 v7, 2026-07-14)

All nodes in the tailnet `tsnet.skynas.ru`, headscale id assignments
approximate — they shift on node re-create.

**Relays (`tag:public`, all `offers exit node` since 2026-07-14):**

* `emilia` (100.64.0.3, headscale id=3) — exit-node + 8 v4 + 4 v6
  Telegram CIDRs. Update-routes cron: weekly Monday 04:00.
* `sharlotta` (100.64.0.4, id=4) — exit-node + same Telegram 8 v4
  + 4 v6 CIDRs. Update-routes cron: weekly Monday 04:00.
* `karolina` (100.64.0.2, id=11) — exit-node + ~148 PrimaryRoutes
  (operator's Windows setup, includes WARP/Google/Cloudflare/Amazon
  /Telegram/...). No cron — one-shot config.

**Clients (`tag:private`):**

* `skygate-vm` (100.64.0.10, id=13) — the in-image skygate container.
  Was `skygate-vm-1` originally, auto-promoted after the old
  host-side node was deleted (commit `f784b48`). The host's
  `tailscaled` was stopped and disabled on 2026-07-14 to eliminate
  the duplicate `skygate-vm-1` node.
* `skyworker` (100.64.0.1, id=9) — operator's Windows machine.
  Has `tailscale up --accept-routes` and may pick any relay as
  exit-node from the Tailscale GUI.
* `base` (100.64.0.7, id=7) — older Windows box, currently
  `offline` since 2026-07-13. Tagged `tag:private` but not in
  active use.
* `skybars` (100.64.0.5, id=10) — Android phone, `active; relay
  "mow"` (uses DERP for direct, not direct endpoint).
* `skybars-1` (100.64.0.8, id=8) — older phone, `offline` since
  2026-07-14 morning.
* `nothing-phone-2` (100.64.0.6, id=6) — Android phone, `active`
  via DERP relay.

**Health check pattern:** Tailscale on any relay that doesn't have
an `ssh[]` rule covering itself prints to `sudo tailscale status`:

> `# Health check:`
> `#     - Tailscale SSH enabled, but access controls don't allow`
> `#       anyone to access this device. Update your tailnet's`
> `#       ACLs to allow access.`

This is a noisy "ACL doesn't permit SSH inbound" warning — it
appears on relays because no rule says "allow SSH into this
specific node". The `ssh[]` rules in `acl.go` only say
"admin → tag:exit-node" and "admin → tag:public" — they permit
SSH *to* the tag, not from the tag to itself. The warning is
**expected** and does not affect exit-node functionality. To
silence it, add a rule like
`{"src":["skyadmin@…"],"dst":["autogroup:self"],"users":["root"]}`
to `ssh[]` — but it's a cosmetic improvement, not a functional
one.

---

## Working environment (VM vs Windows)

**The VM is the source of truth for runtime behaviour.** All deployment,
runtime, and end-to-end verification work happens on the VM:
`skyadmin@192.168.13.69` (a.k.a. `192.168.13.69`).

**VM is for:**
- Building skygate (`docker compose restart skygate`)
- Running `make test` (smoke + `check_exit_nodes.py`)
- Any `docker exec` / `docker compose` / `headscale` CLI work
- Final go/no-go decision before pushing to `origin/main`

**Windows (this workspace) is for:**
- Editing source code, SQL migrations, configs
- Static checks only — schema diffs, migration ordering, env-var review in
  `internal/config/config.go`, headscale API surface checks
- Fast iteration on code (build locally for syntax/compile sanity)

**Never** use Windows as the `make test` source for a shipping decision.
If local and VM results disagree, **VM wins**. Local build = iteration
speed; VM `make test` green = ship.

Quick rule: before any `git push`, ssh to the VM, pull, and run
`make test`. Only push if `FINAL_EXIT=0`.

---

## Smoke testing (make test)

```bash
make test                        # = smoke (bilingual: ru + en) + check_exit_nodes
SMOKE_LANG=ru make test          # one language only
SMOKE_LANG=en make test          # one language only
```

`scripts/smoke.sh` is a bilingual HTTP-level smoke test that exercises login,
device listing, /my/exit-rules CRUD, multi-delete, cascading, the /help page,
admin sync, admin cleanup, /admin/exit-rules/sync, /admin/users, /admin/devices,
static assets. Each step uses `curl` against `localhost:8080`.

**Bilingual mode (since 2026-07-11).** When `SMOKE_LANG` is unset, the script
re-invokes itself once per language (ru, then en) and prints two SUMMARY
lines. All curl calls carry `-H "Accept-Language: $SMOKE_LANG"`; each
sub-run uses its own cookie jar (`/tmp/smoke_ck.<lang>`). Per-language UI
strings (active-count label, page headings, add-rule button text, etc.)
are checked in steps 2/4/11 — a missing or stale `enCatalog` key now fails
the run. ok/bad/note are prefixed `[ru]` or `[en]` so the two streams are
visually separable when interleaved. Total budget: 59 + 59 = 118 smoke
assertions per `make test`.

**Critical pitfalls smoke catches**:
- API returns `ids: [N]` after POST so cleanup-by-id works (was: API didn't
  return ids; smoke couldn't delete its own test rules, accumulating "198.51.100.x"
  orphans in the DB).
- Multi-delete accepts `?id=N&ids=N1&ids=N2` (union of single + many).
- `r.Form` is lazy in Go net/http — handlers must call `r.ParseForm()` before
  reading `ids`.
- Don't accidentally re-introduce a `*:*` first ACL rule; smoke would not
  detect it (smoke runs skygate, not headscale).

Run smoke after ANY change to:
- `internal/handlers/exit_rules*.go`
- `internal/handlers/handlers*.go`
- `scripts/smoke.sh`
- `Makefile`

Skymate rebuilds on every `docker compose restart`. There is no separate
build step in the container — `entrypoint.sh` does `go build -o /app/skygate
./cmd/skygate`. So `docker compose restart skygate` is enough.

---

## Common gotchas

1. **`r.Form` is lazy**: handlers reading form-data MUST call
   `r.ParseForm()` first. Forgetting causes "empty form" bugs.
2. **Go embed**: `templates.go` does `//go:embed templates/*.html
   templates/*/*.html`. New template files appear in the binary automatically
   on rebuild, no manual registration needed.
3. **`TagNode` uses CLI fallback** (`HS.ExecContainer` = env
   `HEADSCALE_CONTAINER`, default "headscale"). The admin API lacks the
   permission for `/api/v1/node/{id}/tag`, so most tag changes go via
   `docker exec headscale headscale nodes tag`. Skymate fires this from
   `backfillNodeOwnership` and from `PostAdminNodeTag`.
4. **`acl_snapshots.config` is a BLOB** of the JSON policy sent to
   headscale. The most recent version is what's *in* headscale; older
   versions are rollback snapshots accessible via
   `/admin/exit-rules/rollback`. After `GenerateACL()` writes a snapshot,
   `SetPolicy()` applies it. If `SetPolicy()` fails, the snapshot stays
   with `applied_success=0` (you can re-trigger via `PostAdminRollbackACL`).
5. **WAL on docker cp**: copying `skygate.db` requires the `.db-wal` and
   `.db-shm` files for an in-flight consistent view, OR `sqlite3 ... "PRAGMA
   wal_checkpoint(FULL);"` to flush. Skymate uses WAL mode by default.
6. **Tailscale Android visibility lag**: tag changes propagate to Tailscale
   clients in ~60-90 s. To force a refresh: tap the Tailscale icon, swipe
   the toggle off and on.
7. **Headscale 0.29 image has no shell in PATH** (no `sh`, `bash`, or
   busybox). `docker exec headscale sh -c "cat > /etc/headscale/..."`
   fails with `exec: "sh": executable file not found in $PATH`. Use
   `docker cp <tmpfile> headscale:/etc/headscale/...` instead — the
   daemon writes the file via its API, no shell inside the target
   container required. The v0.11.1 runtime renderer uses this pattern.
8. **Apply paths must load the full config from DB**, not the form's
   partial struct. The DERP form only has DERP fields, so its cfg
   has `HeadplaneMode == ""` (zero value), which would match the "off"
   branch in `applyHeadplane` and accidentally stop the running
   `headplane` container. The fix: `applyAndRenderDerp` re-reads
   `db.LoadIntegrationsFromOS` after Save and overlays the form's
   fields on top, so the apply reflects the FULL saved config.
9. **`docker compose restart` does NOT rebuild the skygate binary**.
   The entrypoint only runs on container create, not on restart. To
   pick up a new build, use `docker compose up -d --force-recreate
   --no-deps skygate`. After a code change, the version in the
   `/version` / web footer stays on the old commit until you do this.
   (Applies to the production VM at `192.168.13.69`.)

---

## Editing checklist

Before committing a change to handlers/, scripts/, or Makefile:

```bash
# 1. sanity-build (fast iterative)
cd /home/skyadmin/skygate
docker compose restart skygate

# 2. wait for build (~5 min on first compile)
while pgrep -f "go build" > /dev/null; do sleep 3; done

# 3. smoke + check_exit_nodes
make test
```

If smoke fails at "step 8" (delete) — `smoke.sh` expects the API to return
the new rule id in `{ids: [N]}`. Check `internal/handlers/exit_rules_api.go`.

If smoke fails at "step 11" (UI sanity: localized strings) — a key is
missing in the active language's catalog. Run `go test -count=1
./internal/i18n/...` to find it (TestCatalogsParity catches missing
keys; TestPlaceholderOrder catches %s/%d count mismatches between
languages).

If smoke fails at "step 10" (admin sync) — check `/admin/exit-rules/sync`
route registration in `cmd/skygate/main.go`.

---

## Decomposition status

`handlers.go` was a god-object at ~1100 lines and has been the main
decomposition target. Progress so far:
- `handlers_node_ownership.go` (248) — `backfillNodeOwnership` + `firstTagOrFallback` extracted.
- `handlers_dashboard.go` (185) — `TailnetMetrics` + `PreauthKeyStats` + `computeTailnetMetrics`
  + `GetDashboard` + `countMyPreAuthKeys` extracted.
- `handlers_auth.go` (100) — `GetLogin` / `PostLogin` / `PostLogout` /
  `PostLang` extracted.
- `handlers_my_account.go` (92) — self-service password change extracted.
- `handlers_api_tokens.go` (59) — personal API tokens extracted.
- `handlers_admin_pages.go` (63) — read-only admin views extracted.
- `handlers_admin_users.go` (222) — admin user CRUD extracted.
- `handlers_admin_nodes.go` (102) — admin device/tag extracted.
- `handlers_derp.go` (115) — /admin/derp handlers + DerpStatus/DerpPeer/ConnSummary/DerpSnapshot types.
- `handlers_derp_collect.go` (245) — `collectDerpStatus` + `httpGet` + `parseDerperDebugHTML` + `parseDerperVars` (fetch & parse derper debug endpoints).
- `handlers_derp_classify.go` (80) — `classifyDerpPeer(s)` + `summarizeDerpPeers` + IP-net constants.
- `handlers_settings.go` (63) — theme switcher extracted.
- `handlers_help.go` (20) — /help page extracted.
- `handlers_my_preauth.go` (44) — POST /my/preauth extracted.
- `handlers_my_exit_nodes.go` (23) — GET /my/exit-nodes extracted.
- `handlers_my_keys.go` (173) — /my/keys list+expire extracted.
- `handlers_my_devices.go` (127) — GET /my/devices extracted.
- `exit_rules_routescript_data.go` (67) — `loadRoutesForScript` + `resolveExitNodeIPForScript` + `routeEntry` struct.
- `exit_rules_routescript_windows_body.go` (185) — `buildWindowsRouteScript` + `writeWindows{Setup,Restore}Script` helpers (pure .cmd builder).
- `exit_rules_routescript_linux_body.go` (147) — `buildLinuxRouteScript` + `writeLinux{Setup,Restore}Script` helpers (pure .sh builder).
- `exit_rules_form_my.go` (576) — /my/exit-rules: Get + Post + Delete (script download, DNS resolve, multi-delete cascade).
- `exit_rules_form_admin.go` (150) — /admin/exit-rules cross-user view.
- `exit_rules_form_rollback.go` (37) — /admin/exit-rules/rollback restore.

`handlers.go` is now **~236 lines** — pure shared infrastructure
(App struct, render helpers, currentUser, audit, getMaxRulesForUser).
Nothing left to extract; the file is no longer a god-object.

`exit_rules_routescript.go` was a ~300-line generator dominated by
inline shell script literals. After Этап 6 it is a 42-line
orchestrator: `load data → dispatch to OS builder`. The OS-specific
bodies (Windows .cmd / Linux bash) are pure functions in
`exit_rules_routescript_{windows,linux}_body.go` (note the `_body`
suffix — the Go code that builds a Windows .cmd script or Linux
bash is platform-independent, so the original `_windows.go` /
`_linux.go` filenames would have triggered GOOS build constraints
and broken the build on the wrong host OS).

`exit_rules.go` (1146 → 359) was already largely decomposed; the form
handlers lived in `exit_rules_form.go` (787 lines, Этап 7 split into form_my/admin/rollback)
candidate for further splitting if we ever revisit it.

When adding a new handler, prefer creating a focused file rather than
growing either god-object:
- `internal/handlers/handlers_yourfeature.go` for user-facing handlers
- `internal/handlers/exit_rules_yourfeature.go` for exit-rule-related logic
- `internal/handlers/handlers_admin_*.go` for admin pages

Sister files in `internal/handlers/` (current line counts):
- `handlers.go` (257) — shared infra only: App + New + render/renderWithLayout + pageFromName/pageTitle/dataValue + currentUser/audit + getMaxRulesForUser
- `handlers_dashboard.go` (185) — TailnetMetrics + PreauthKeyStats + computeTailnetMetrics + GetDashboard + countMyPreAuthKeys
- `handlers_auth.go` (100) — GetLogin / PostLogin / PostLogout / PostLang
- `handlers_node_ownership.go` (248) — backfillNodeOwnership + firstTagOrFallback
- `handlers_my_account.go` (92) — self-service password change
- `handlers_api_tokens.go` (59) — personal API tokens
- `handlers_admin_pages.go` (~115) — read-only admin views (audit, ACLs); audit supports `?action=` and `?user=` filters (Phase 5, 2026-07-11)
- `handlers_derp.go` (115) — /admin/derp handlers + DerpStatus/DerpPeer/ConnSummary/DerpSnapshot types
- `handlers_derp_collect.go` (245) — collectDerpStatus + httpGet + parseDerper{DebugHTML,Vars}
- `handlers_derp_classify.go` (80) — classifyDerpPeer(s) + summarizeDerpPeers
- `handlers_admin_users.go` (222) — admin user CRUD
- `handlers_admin_nodes.go` (102) — admin device/tag
- `handlers_settings.go` (63) — /settings/theme (theme switcher)
- `handlers_help.go` (20) — /help
- `handlers_my_preauth.go` (44) — POST /my/preauth (issue 1h single-use key)
- `handlers_my_exit_nodes.go` (23) — GET /my/exit-nodes
- `handlers_my_keys.go` (173) — /my/keys (list + expire)
- `handlers_my_devices.go` (127) — GET /my/devices (with lazy node_owner_map backfill)
- `exit_rules.go` (359) — DeviceRule struct + DB helpers + `GenerateACL()` + ACL helpers
- `exit_rules_form_my.go` (625) — /my/exit-rules: Get + Post + Delete (incl. script download, DNS resolve, multi-delete cascade, user-facing counters)
- `exit_rules_form_admin.go` (165) — /admin/exit-rules cross-user view (hierarchical by user → device → exit_node)
- `exit_rules_form_rollback.go` (40) — /admin/exit-rules/rollback (restore prev acl_snapshot)
- `exit_rules_form_reapply.go` (57) — /admin/exit-rules/reapply (v7: regenerate policy without exit-rule churn)
- `acl/acl.go` (190) — GenerateACL as a free function so the bot (no *App) can reuse it; per-user policy + ssh rules + tagOwners. v7 added tag:exit-node to tagOwners + skyadmin→tag:public SSH rule.
- `acl/acl_test.go` (210) — parity + placeholder + per-identity tests.
- `exit_rules_api.go` (159) — public REST API
- `exit_rules_sync.go` (387) — ACL sync, staggeredSync, autoupdater
- `exit_rules_routescript.go` (42) — orchestrator: `GenerateRouteSetupScript` (load data → dispatch to OS builder)
- `exit_rules_routescript_data.go` (67) — `loadRoutesForScript` + `resolveExitNodeIPForScript` + `routeEntry` struct
- `exit_rules_routescript_windows_body.go` (185) — `buildWindowsRouteScript` + `writeWindows{Setup,Restore}Script` helpers (pure .cmd builder, no I/O)
- `exit_rules_routescript_linux_body.go` (147) — `buildLinuxRouteScript` + `writeLinux{Setup,Restore}Script` helpers (pure .sh builder, no I/O)
- `exit_rules_cleanup.go` (357) — admin cleanup + orphan /32 cleanup
- `admin_backup.go` (247) — backup/restore ACL
- `admin_telegram.go` (303) — telegram UI; test handler routes through `app.Notifier.SendTelegram` (Go-native HTTP, no curl)
- `notify.go` (245) — `Notifier` interface (`SendTelegram` + `SendAlert`); `RealNotifier` is always armed, sleeps 5s when token absent
- `alerts.go` (85) — `SendAlert` returns alert id from `telegram_alerts`; outgoing message is prefixed with `[#<id>]` so `/ack <id>` can find it
- `commands.go` (96) — `HandleCommand(ctx, env BotEnv, raw)`; `BotEnv` carries DB + per-user rule limits (`/quota`) + build version (`/version`); dispatch table for /status /help /nodes /rules /audit /exit_nodes /quota /ack /version /restart
- `commands_phase2.go` (166) — read-only DB-query commands; `trimForTelegram` (cap 3800) shared with phase 3
- `commands_phase3.go` (222) — /exit_nodes (filter on tag:exit-node + last_seen), /quota (per-user bars), /ack (idempotent UPDATE WHERE acked_at=0 + audit_log mirror)
- `commands_phase4.go` (205) — /version (build + Go runtime + DB schema), /restart (6-char token confirm, 30s TTL, SIGTERM via `os.FindProcess` for cross-platform compile, audit_log row), /help <command> (detailed per-command help)
- `admin_exit_nodes.go` (164) — exit node admin
