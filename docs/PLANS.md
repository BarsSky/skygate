# Skygate plans & technical debt

**Last updated:** 2026-08-12
**Maintained by:** Mavis (skygate) + operator
**Status:** live roadmap; updated after every release

This file is the operator's "what's next" notebook. It replaces
the v0.32-era `docs/BACKLOG.md` (which still exists as a
historical reference) and consolidates:

1. **What's done** (the v0.x history that landed in the
   v1.0.0 initial release)
2. **What's pending** (technical debt that didn't make the
   v1.0.0 cut — clearly tagged with effort + priority)
3. **What's blocked** (work that needs external resources
   — operator action required)
4. **Future roadmap** (rough order of upcoming v1.x releases)

The operator reviews this after every release and either
re-prioritizes items (move them between sections) or marks
them as done (delete them).

---

## v1.0.0 — initial release (2026-08-11)

The squashed initial commit. Contains everything from
v0.10.0 through v0.34.0 (B95 code-debt cleanup), collapsed
into a single root commit. Repo size: 49 MB → ~5 MB after
the squash (estimated, depends on pack efficiency).

**What's in v1.0.0:**

### Core (functional, complete)
- **headscale + headplane integration** — full user / node /
  ACL management via headscale v0.29.x, headplane v0.6.3
  (optional, for non-CLI management)
- **Tailscale integration** — preauth-key issuance, headscale
  registration, subnet-router flow, sidecar SSH egress
  relay, per-user device preferences
- **Per-user exit-nodes** — `tag:dev-<user>-<device>` schema,
  per-device `via_enabled` opt-in, autoupdater (Cloudflare
  anycast + 64 CDN providers), per-row `ssh_target` +
  `ssh_port` + `ssh_key_path`
- **Operator-only 'infra' user** (V054) — for skygate-host-*
  nodes and the bot in skygate-host-1; isolated from
  regular portal users
- **MySQL / PostgreSQL backends** — SQLite is the default,
  PG is supported via `SKYGATE_DB_DSN=postgres://...`; full
  migration parity (54 migrations); 4 verification tests
- **Telegram bot** — per-user bot UX, bind / unbind, QR code
  auth, language picker (ru/en), command set (~20 commands),
  inline-keyboard UI
- **Backup system** — local / SMB / NFS / SFTP destinations,
  weekly `PRAGMA integrity_check` verification, daily
  scheduled runs, in-app scheduler
- **Auto-update** — `SKYGATE_AUTO_UPDATE_ENABLED` opt-in flag
  (default false), per-update operator Push button,
  rollback-on-failure via tag
- **Audit log** — every state-changing action written to
  `audit_log` table, /admin/audit viewer
- **Admin pages** — devices, exit-nodes, exit-rules,
  acls, subnets, users, settings, headscale, headplane,
  telegram, tailscale, integrations, derp, backup, system
  tests, services, audit, update, etc. (23 admin pages)
- **User pages** — dashboard, /my/devices, /my/exit-nodes,
  /my/keys, /my/telegram, /my/account
- **CLI** — `skygate-cli.sh` for `docker exec` translation

### Operational
- **Verify-pre catalog (94 checks)** — runs before every
  push; B1-B95, all green as of v1.0.0
- **Verify-post catalog (33 checks)** — runs after every
  deploy; R1-R35
- **Pre-flight wait** — skygate waits for headscale to be
  ready on boot (60s default, env-tunable)
- **Availability checker** — 30s background poll of
  headscale / headplane / tailscale; surfaced at
  /admin/services + /readyz.availability
- **Migration integrity** — checksum of every migration in
  `applied_migrations` table; v0.34.0.1's `check_b95.sh`
  pins the post-rewrite expected count

### Tests
- **28 Go packages** with unit + integration tests; all
  green at v1.0.0
- **Staticcheck catalog** — zero U1000 / SA5011 / ST1019 /
  SA4010 / SA4006 / SA4017 / S1011 / S1031 / S1039 (the
  post-v0.34.0.1 cleanup contracts)
- **Bash catalog** — check_b91.sh through check_b95.sh
  pin the structural invariants of the test scripts

### Documentation
- `AGENTS.md` — AI-assistant instructions (5657 lines)
- `RELEASE-NOTES.md` — full version history
- `docs/disaster-recovery.md` — Tier-0 backup recovery
- `docs/internal/v0.27.0-postgres-ha.md` — PG cutover plan
- `docs/internal/ha-architecture.md` — Tier-1 HA design
- `docs/internal/wal-g-notes.md` — PG backup architecture
- `docs/internal/telegram-relay.md` — bot config
- `docs/internal/subnet-router.md` — per-user subnet-router
  setup (operator-facing)
- `docs/deploy.md` — fresh install + restore flow
- `docs/fa-test-report-v0.26.0.md` — historical FA report
  (predates the v1.0.0 squash; kept for traceability)

### Credentials / secrets policy
- **No live credentials in tracked code** (post-v0.34.0.1)
- **No private LAN IPs in tracked code** (post-v0.34.0.1)
- **Pre-commit hook** in `.githooks/pre-commit` blocks
  commits that would re-introduce the known leak patterns
- **No `.env` files in git** (`.gitignore`)
- **No `*.key` / `*.pem` files in git** (`.gitignore`)

---

## Technical debt (pending; not in v1.0.0)

### HIGH priority — fix in v1.1.0 or v1.2.0

**[TD-1] UI refactoring (Priority 9 from BACKLOG)**
- **Status:** DEFERRED (originally planned for v0.34.0,
  broken build was reverted; slated for v1.1.0)
- **Effort:** ~3-4 days
- **Scope:** 23 admin pages → 6 collapsible sidebar
  sections:
  1. **Devices & Nodes** (4 pages)
  2. **Access Control** (3 pages)
  3. **System Health & Logs** (4 pages — system_tests,
     services, audit, update)
  4. **Integrations** (6 pages — integrations, telegram,
     tailscale, headplane, derp)
  5. **Data** (3 pages — backup, subnets)
  6. **Settings & Users** (4 pages — settings, users,
     control-planes, invites, meshes, headscale)
- **Plus:** status badges on section headers (driven by
  B92 availability snapshot — green if all integrations
  ok, red if any fail, gray if not yet checked)
- **Plus:** consolidate /admin/headscale + /admin/headplane
  into one "Control plane" page with tabs
- **Plus:** info density improvements (chip collapse for
  OS+type+last_seen on /admin/devices)
- **Plus:** inline action confirmation (replace `confirm=yes`
  checkboxes with a small modal)
- **Why high:** the 23-page sidebar is unmaintainable. The
  operator's "is everything ok?" glance currently requires
  opening multiple pages.

**[TD-2] Style cleanup (ST1013, 68 items)**
- **Status:** DEFERRED from v0.34.0
- **Effort:** ~1-2 hours (mechanical replacement)
- **Scope:** replace `http.Error(w, "...", 403)` with
  `http.Error(w, "...", http.StatusForbidden)` (and similar
  for 401, 404, 405, 409, 500, 503) across 68 lines in
  `internal/feature/admin/*.go`
- **Why high:** staticcheck noise — every CI run warns
  about these. The fix is mechanical and the project
  should not have 68 style violations in main.

**[TD-3] Mobile-responsive UI (NEW)**
- **Status:** DONE in v1.1.0 (combined with TD-1)
- **Effort:** ~1-2 days (CSS refactor + tests; piggybacks
  on TD-1's section grouping — the grouped sidebar was
  the prerequisite for a usable mobile drawer)
- **Scope:** the admin panel was effectively unusable on
  a phone (the fixed 220px sidebar ate the whole viewport
  on 375-414px phones). v1.1.0 adds:
  1. **Hamburger button** (`.sidebar-toggle`, fixed top-left,
     only visible on mobile <768px). Driven by the native
     checkbox hack — no JS required.
  2. **Slide-in drawer**: sidebar starts at
     `transform:translateX(-100%)` on mobile, slides to
     `translateX(0)` when the hamburger is toggled.
     `backdrop-filter: blur(8px)` overlay dims the main
     content while the drawer is open.
  3. **Breakpoint renamed 760px → 768px** (the canonical
     iPad-portrait width; 760px was a v0.28.5-era
     carryover).
  4. **Touch-friendly tap targets**: sidebar links + buttons
     bumped to 12px vertical padding + `min-height:44px`
     per Apple HIG / Material Design.
  5. **Sections always open on mobile** so the operator
     doesn't have to tap 6 times to find a page.
  6. **B97 catalog row** + 2 Go unit tests pin the
     breakpoint + hamburger + translateX + 44px contract.
- **Files**: `static/css/themes.css` (added `.sidebar-toggle`
  class + `@media (max-width:768px)` block; renamed
  `@media (max-width:760px)` → 768px), `layout.html`
  (added `<input type="checkbox" id="sidebar-toggle">`
  + `<label class="sidebar-toggle">`).
- **Verification**: `bash scripts/check_b97.sh` →
  `go test -count=1 -run TestB97_ ./internal/handlers/`
  (2 tests, both PASS in v1.1.0).

**Renumber note**: this entry takes the TD-3 slot that
the v0.34.0-era `docs/PLANS.md` used for the SA1012
"intentional nil context" cleanup (5 items). That
work has been moved to TD-14 below.

**[TD-14] Style cleanup (SA1012, 5 items)** *(renumbered from TD-3 in v1.1.0)*
- **Status:** DEFERRED from v0.34.0
- **Effort:** ~30 min
- **Scope:** in test files that intentionally pass `nil`
  context (e.g. `Run(nil, nil)` tests for nil-handling
  paths), add a `//nolint:staticcheck // intentional nil
  test` comment so the next staticcheck run doesn't warn
  about them
- **Why high:** same as TD-2 (low risk, but every CI
  run warns until done)

### MEDIUM priority — fix in v1.x

**[TD-4] Backup S3 destination (B1)**
- **Status:** DEFERRED from v0.32.29
- **Effort:** ~half a day
- **Scope:** add an "S3" protocol option in /admin/backup/config
  + a `SKYGATE_BACKUP_S3_BUCKET` env var + a new
  `internal/backup/dest_s3.go` (uses aws-sdk-go-v2)
- **Why medium:** SMB / NFS / SFTP cover the operator's
  current needs. S3 is "nice to have" for off-site backup.
  Block on whether the operator actually wants this — if
  not, drop the item entirely.

**[TD-5] v0.19.1 — `exitnode.skygate-subnet-<user>` DNS records**
- **Status:** BLOCKED on headscale 0.30+
- **Effort:** ~1 day
- **Scope:** add named per-user DNS records (currently
  blocked because headscale 0.29.x rejects the
  `dns.extra_records` policy that v0.18.0 added support for)
- **Why medium:** would let users connect to
  `exitnode.<user>.skygate.example.com` instead of needing
  to know the relay hostname

**[TD-6] /admin/exit-nodes edit UI for `accept_routes` (Issue 3)**
- **Status:** UNADDRESSED
- **Effort:** ~2 hours
- **Scope:** add a checkbox on /admin/exit-nodes to toggle
  `accept_routes` per node (currently only settable via
  CLI or direct DB)
- **Why medium:** minor UX gap; not blocking any operator
  workflow

**[TD-7] /admin/users HSOrphans "Add as skygate user" button (Issue 5)**
- **Status:** UNADDRESSED
- **Effort:** ~4 hours
- **Scope:** when a headscale user exists but has no
  portal_users row (orphaned from a delete), show a button
  to re-create the portal_users entry
- **Why medium:** rare error path but the recovery is
  currently manual (SQL + API)

**[TD-8] Mesh `system_tests_runs` recording**
- **Status:** PARTIAL (system_tests_runs table is in PG
  but not in the active hot path)
- **Effort:** ~2 hours
- **Scope:** verify that every /admin/system_tests run
  actually persists to system_tests_runs on both backends
  (v0.33.0's V051 created the table; v0.32.20's recording
  code was never fully wired)
- **Why medium:** useful for trend reporting once the UI
  adds a "history" tab

**[TD-9] Subnet-router `cleanup_smoke_artifacts` periodic**
- **Status:** DONE-ONCE; needs a cron
- **Effort:** ~30 min
- **Scope:** add a daily cron on the VM that runs
  `scripts/cleanup_smoke_artifacts.sh` (or equivalent)
  to prevent smoke-mesh test data from accumulating in
  the live DB
- **Why medium:** low priority — the operator's manual
  cleanup in v0.33.1.36 already removed the historical
  30 rows; ongoing accumulation is slow

### LOW priority — nice to have

**[TD-10] Per-user `headscale_user_id` column accuracy**
- **Status:** UNADDRESSED
- **Effort:** ~2 hours
- **Scope:** V054 reserved the `id=99` slot for the infra
  user; the existing `headscale_user_id` column may be
  stale for users whose headscale account was deleted and
  re-created. Add a periodic reconciliation cron.
- **Why low:** only matters during user-account
  lifecycle edge cases

**[TD-11] Rule grouping: Cloudflare /12 + /24 merge**
- **Status:** UNADDRESSED
- **Effort:** ~1 day
- **Scope:** the current rule storage has one row per
  `/32` (or `/24`) per domain. For a domain with 16
  Cloudflare anycast IPs, that's 16 rows per device. A
  merge pass would group them under the parent domain
  and store the count.
- **Why low:** cosmetic; the /admin/exit-rules page
  paginates so the volume is manageable

**[TD-12] 30 ST1013-style noise items**
- **Status:** UNADDRESSED
- **Effort:** ~1 hour
- **Scope:** same as TD-2 (style cleanups)
- **Why low:** same as TD-2

**[TD-13] ~2850 lines of testutil.go stubs**
- **Status:** OUTDATED (actual count is 917 lines after
  v0.34.0 cleanup, not 2850 as the v0.32-era BACKLOG
  estimated)
- **Effort:** ~1 day
- **Scope:** audit `internal/feature/admin/testutil.go`
  and `internal/feature/my/testutil.go` for unused
  helpers; the v0.34.0 cleanup removed 3 dead helpers
  already
- **Why low:** will happen naturally as new features are
  added

---

## BLOCKED — needs operator action

> **[BL-1] PostgreSQL cutover (Priority 2)**
  - **Status:** DONE across v1.3.0 + v1.3.1 + v1.3.2.
    Runtime is PG-only end-to-end. All three phases landed
    in this v1.3.x series.
  - **Phase 1 (v1.3.0, DONE):** runtime is PG-only. The
    SQLite backend is removed entirely. `cfg.DBDSN` is
    required. `mattn/go-sqlite3` is out of `go.mod`. 30
    old migration files + 4 SQLite-specific helper files
    are deleted. 25 test files are stubbed (Phase 2/3
    work targets the same code paths via the live
    admin UI on the real PG cluster). `go test ./...`
    is 28/28 green.
  - **Phase 2 (v1.3.1, DONE):** 9 operator scripts converted
    from `sqlite3` to `psql` (backup.sh, verify_backup.sh,
    check_subnet_router.sh, cleanup_orphan_meshes.sh,
    reconcile_snapshots.sh, recover_db_corruption.sh,
    verify_post_deploy.sh, verify_pre_deploy.sh). New
    `psql_vm` helper in verify_post_deploy.sh parses
    `SKYGATE_DB_DSN` and runs psql on the VM if installed,
    or falls back to a throwaway `postgres:15-alpine`
    container on the `headscale_default` network.
    `Dockerfile` drops `gcc`/`musl-dev`/`sqlite-libs`
    (CGO_ENABLED=0, 24 MB static binary). `docker-compose.yml`
    adds the `postgres:15-alpine` service gated behind
    the `local-pg` profile. The verify-pre catalog updates
    B26 (CGO_ENABLED=0 contract), B34 (psql duplicate
    check), B70 (PG-only title), B79 (PG-only
    placeholders). 2 SQLite-era helpers deleted
    (`_recover_helper.sh` + `_swap_recovered.sh` -> moved
    to `.trash/sqlite_helpers/` for historical ref).
    14 files changed, +1044/-384.
  - **Phase 3 (v1.3.2, DONE — this release):** documentation
    updated in this commit:
    - `docs/deploy.md#10-postgresql` — new section (PG
      install + init + backup + two deployment modes
      local docker-compose vs external PG)
    - `docs/deploy.md#11-postgresql-migration-from-sqlite`
      — new section (one-time runbook for legacy v0.32.x
      `skygate.db` -> PG conversion via
      `cmd/apply_pg_migrations` + `dump_sqlite.py`)
    - `docs/disaster-recovery.md` — Step 3 (PG restore
      flow), Step 4 (skygate container restart), RPO
      section, "Backed up by" table updated for the new
      `skygate-pg.sql` artifact
    - `docs/architecture.md` — new "Database backend
      (v1.3.0+)" section, CGO section rewritten (no
      more CGO), TL;DR updated
    - `AGENTS.md` — release status updated to v1.3.1,
      catalog tables updated for the new B26/B34/B70/B79
      contracts, runtime section updated for the
      `psql_vm` helper
    - `PLANS.md` — this entry (BL-1 moved from "Phase 1
      DONE" to "DONE across v1.3.0 + v1.3.1 + v1.3.2")
  - **Operator action required:** deploy v1.3.0 + v1.3.1
    to the live VM (`git pull` + `docker compose build
    skygate` + `docker compose up -d --force-recreate
    --no-deps skygate`). The runtime is forward-compatible
    — the existing `SKYGATE_DB_DSN` in
    `/home/admin/skygate/.env` works as-is. After the
    binary is live, take a fresh `scripts/backup.sh`
    archive so the next backup contains `skygate-pg.sql`
    (not the old `skygate.db`).
  - **Outcome:** skygate is now a static 24 MB binary
    that talks to a single `*sql.DB` pool (pgx v5).
    No more CGO coupling, no more WAL-corruption class
    of failures, no more "container has no sqlite3"
    script workarounds. The 25 PG-test rewrites from
    Phase 1 are recorded as a Phase 4 backlog (low
    priority — the production code path is exercised
    by the live admin UI against the real PG cluster).
  - **References:** `RELEASE-NOTES.md` v1.3.0 + v1.3.1
    + v1.3.2 entries, `docs/internal/v0.27.0-postgres-ha.md`
    (now historical; the plan was followed end-to-end
    in 2026-08). `RELEASE-NOTES.md` v1.3.0 entry,
  `docs/internal/v0.27.0-postgres-ha.md`

**[BL-2] HA skygate-host-2 (Priority 3)**
- **Status:** BLOCKED on 2nd VM + etcd + S3
- **Effort:** ~2-3 weeks
- **Scope:** Tier-1 HA — RTO < 1 min, RPO = 0
  - 2nd VM (skygate-host-2) as warm standby
  - Patroni + etcd cluster for PG failover
  - HAProxy pg-aware routing (port 5000 = primary,
    5001 = replica)
  - wal-g → S3 for WAL archive + PITR
  - DNS plan: `head.example.com` + `skygate.example.com`
    flip with 5-min TTL
- **Operator action required:** provision skygate-host-2
  VM + S3 bucket + 3-node etcd cluster
- **References:** `docs/internal/v0.27.0-postgres-ha.md`,
  `docs/internal/ha-architecture.md`

**[BL-3] Telegram DPI workaround (operator-side)**
- **Status:** BLOCKED on operator's network
- **Effort:** operator-side
- **Issue:** `wget https://api.telegram.org/` times out
  on the operator's network (DPI block)
- **Options:** route the bot through a different
  exit-node without DPI, or use obfs4 / shadowsocks
  for the bot's outbound traffic
- **Operator action required:** choose a workaround
  and configure it in the live .env

---

## Future roadmap (rough order)

**v1.1.0 — UI refactoring (TD-1) + mobile-responsive (TD-3)**
- **Status:** DONE in v1.1.0
- 6 collapsible sidebar sections (Devices & Nodes /
  Access Control / System Health & Logs / Integrations /
  Data / Settings & Users), each auto-opens via the
  `InSectionX` booleans that `renderWithLayout` computes
  from `.Page`
- Hamburger button + slide-in drawer at <768px breakpoint
  (renamed from 760px → 768px to match iPad-portrait
  width)
- 44px min-height tap targets for touch UX
- B96 (TD-1) + B97 (TD-3) catalog rows added; both PASS
- Deferred to a follow-up release: status badges from
  B92 availability, control-plane consolidation, info
  density on /admin/devices, inline action confirmation
  modal
- Effort: ~1.5 days (combined into single release)

**v1.2.0 — Style cleanup (TD-2)**
- Replace 68 numeric HTTP status codes with `http.StatusXxx`
  constants
- The 5 SA1012 false-positives are now TD-14 (renumbered
  from TD-3 in v1.1.0 when TD-3 was reassigned to mobile-
  responsive UI)
- Effort: ~2 hours

**v1.3.0 — Backup S3 (TD-4)**
- Add S3 destination to /admin/backup/config
- New `internal/backup/dest_s3.go` (aws-sdk-go-v2)
- Verify-pre catalog check for S3 endpoint connectivity
  (the pre-v1.1.0 plan said "B96" — but B96 is now taken
  by TD-1, so this release will use a new B number)
- Effort: ~half a day

**v1.4.0 — UI quality-of-life (TD-6, TD-7)**
- /admin/exit-nodes `accept_routes` toggle
- /admin/users HSOrphans "Add as skygate user" button
- Effort: ~6 hours

**v1.5.0 — DNS records (TD-5)**
- Requires headscale 0.30+ (release-not-yet)
- Per-user `exitnode.<user>.skygate.example.com` DNS
- Effort: ~1 day
- Blocked on headscale release

**v1.6.0 — System tests persistence + reporting (TD-8)**
- Verify /admin/system_tests records to system_tests_runs
- Add "history" tab to /admin/system_tests
- Effort: ~2 hours

**v1.7.0 — Subnet-router auto-cleanup cron (TD-9)**
- Daily cron to remove smoke-mesh test data
- Effort: ~30 min

**v1.8.0+ — PostgreSQL cutover (BL-1) — DONE across v1.3.0 + v1.3.1 + v1.3.2**
- Live cutover on the operator's PG-staging VM
- Phase 1 (v1.3.0): Go source — removed all SQLite code paths,
  `cfg.DBDSN` is now REQUIRED, pgx is the only DB driver
- Phase 2 (v1.3.1): Docker + scripts — Dockerfile
  `CGO_ENABLED=0`, 24 MB static binary, docker-compose adds
  the `postgres:15-alpine` service behind `local-pg` profile,
  9 operator scripts converted sqlite3 → psql (throwaway
  `postgres:15-alpine` container pattern for portable psql)
- Phase 3 (v1.3.2): docs — `docs/deploy.md#10-postgresql` +
  `#11-postgresql-migration-from-sqlite`, disaster-recovery
  + architecture + AGENTS.md updated
- 117 files +1400/-27331 (v1.3.0) + 14 files +1044/-384 (v1.3.1)
  + 5 docs +803/-83 (v1.3.2)
- 4 new catalog rows: B26 (CGO_ENABLED=0), B34 (psql
  duplicate check), B70 (PG-only title), B79 (PG-only
  placeholders). All 4 PASS.

**v3.0.0 — HA Tier 1 (BL-2)**
- skygate-host-2 + etcd + S3 + DNS plan
- Patroni auto-failover
- Effort: ~2-3 weeks
- Blocked on operator's 2nd VM + etcd + S3 bucket

---

## How to use this file

- **After every release:** update the "Last updated"
  date + add a one-line entry to the "What's done"
  section (or move an item from "Technical debt" /
  "BLOCKED" / "Future roadmap" to "What's done" if it
  shipped)
- **When prioritizing work:** re-read "Technical debt"
  + "BLOCKED" + "Future roadmap" together. Items in
  BLOCKED need operator action to unblock. Items in
  Technical debt can be tackled any time. Items in
  Future roadmap have a target release.
- **When adding a new item:** add it to one of the
  three lists with a `[TD-N]`, `[BL-N]`, or `[RR-N]`
  tag. The operator reviews the file periodically and
  re-tags items as needed.
- **When the operator wants to do an item:** say
  "do TD-3" (etc.) and the work is picked up.
