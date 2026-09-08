# Skygate plans & technical debt

**Last updated:** 2026-09-08 (B237.22 + B237.23 deployed on top of v1.5.2)
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
- **Status:** DONE in v1.2.0 (commit `38b2fb9e` + `d6f7b6b2`).
  The actual count was 5, not 68 (the v0.34.0-era estimate
  was wildly off — most of the 68 had been fixed by the
  B62-B188 refactor work that landed before v1.2.0).
  2 stragglers (the 404 in `tags_test.go:66` + the 302
  in `e2e_test.go:399`) slipped back in via the v1.5.0
  work and were fixed in **B237.16** (v1.5.2+, commit
  `7a3dcfd7`). 10-contract B-check in
  `scripts/check_b237_16.sh` pins the contract so a
  regression fails the verify-pre catalog.
- **Effort:** DONE (was ~1-2 hours when it actually ran)
- **Scope:** DONE — `staticcheck ./...` reports 0 ST1013
  on the current tree. The 23 remaining staticcheck
  warnings are all U1000 (unused test stubs) — separate
  ticket, not part of TD-2.
- **Why high:** DONE

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
- **Status:** DONE — the 5 false-positives never materialized
  in the current tree. `staticcheck ./...` reports 0 SA1012.
  B237.16's B-check (section B.1) pins the continued
  absence so a future test that DOES trip SA1012 fails
  the verify-pre catalog.
- **Effort:** ~30 min (was the estimate; ended up being
  0 — the false-positives were never created, the
  v0.34.0-era estimate was just wrong)
- **Scope:** DONE — no code changes needed
- **Why high:** DONE (pin in B237.16)

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
- **Status:** DONE in v1.4.0 (B140, commit `e869650f`).
  Per-row `<select>` for `1` / `0` / `-1` (true / default /
  false) on /admin/exit-nodes with `PostAdminExitNodeSetAcceptRoutes`
  handler + `parseAcceptRoutesFormValue` parser + 6 unit
  tests in `exit_nodes_b140_test.go` + 7-contract B-check
  in `scripts/check_b140.sh` (all green as of
  2026-09-07). The B-check was looking for `s.DB`
  (stale; pre-dbc() rename) and the
  fix landed in B237.16.
- **Effort:** DONE (was ~2 hours when it actually ran)
- **Scope:** DONE — `bash scripts/check_b140.sh` →
  7/7 pass
- **Why medium:** DONE

**[TD-7] /admin/users HSOrphans "Add as skygate user" button (Issue 5)**
- **Status:** DONE in v1.4.0 (B141, same commit `e869650f`
  as TD-6). `PostAdminHSOrphanAdopt` handler + the
  `?adopted=` / `?already_adopted=` flash banners +
  4 unit tests in `users_b141_test.go` + 8-contract
  B-check in `scripts/check_b141.sh` (all green as of
  2026-09-07). The B-check was looking for `s.DB`
  (stale; pre-dbc() rename) and the fix landed in
  B237.16.
- **Effort:** DONE (was ~4 hours when it actually ran)
- **Scope:** DONE — `bash scripts/check_b141.sh` →
  8/8 pass
- **Why medium:** DONE

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
- **Status:** DONE in **B237.17** (v1.5.2+, commit TBD).
  `scripts/cleanup_smoke_artifacts.sh` — idempotent
  daily script. `sudo -u postgres psql -d skygate_staging`
  (canonical operator-side pattern, matches
  `clear_test_dsn.sh:36` from B207-fix). BEGIN/COMMIT
  around the deletes (CASCADE handles mesh_members on
  the meshes table + devices/preauth_keys/exit_rules
  on the portal_users side). 24h grace window so an
  in-flight smoke.sh run is NOT killed. 24h-N row
  audit row written (`action=smoke_artifacts_purge`).
  `deploy/systemd/skymate-cleanup-smoke.{service,timer}` —
  Type=oneshot, daily at 04:00 local, RandomizedDelaySec
  300 (avoids fleet-wide thundering herd on HA),
  Persistent=true (catches up after VM reboot). The
  service runs as `skyadmin`, WorkingDirectory=
  `/home/skyadmin/skygate`, ExecStart= the script.
  16-contract B-check in `scripts/check_b237_17.sh`.
- **Effort:** DONE (was ~30 min; actual was about the same)
- **Scope:** DONE — `bash scripts/check_b237_17.sh` → 16/16 pass
- **Why medium:** DONE (operator enables the timer with
  `systemctl enable --now skymate-cleanup-smoke.timer` —
  one command, then the daily tick runs the cleanup)

### LOW priority — nice to have

**[TD-10] Per-user `headscale_user_id` column accuracy**
- **Status:** DONE in **B237.18** (v1.5.2+, commit TBD).
  `internal/headscale/reconcile.go` (the per-row
  reconciliation function, 4 outcomes) +
  `internal/headscale/reconcile_cron.go` (the cron
  entry points: `StartReconcileCron` + `RunOnceNow`,
  1h default interval). The reconciliation detects
  3 states per portal_users row: ok (link still
  valid), linked (was NULL/0, found by username in
  headscale → updated), relinked (had a stale ID,
  found by username → updated), orphan (had an ID
  but neither the ID nor the username exists in
  headscale → audit row, NEVER auto-deleted). Wired
  in `cmd/skygate/main.go` AFTER `headscale.New` +
  `ensureHeadscaleUser` (so the headscale client
  exists and the admin user is in headscale by the
  first tick). Gated on `cfg.ReconcileHeadscaleUsers`
  (default true; air-gapped installs flip the env
  var to false). Env vars:
  `SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED` +
  `SKYGATE_RECONCILE_HEADSCALE_USERS_INTERVAL`. 17-
  contract B-check in `scripts/check_b237_18.sh`.
- **Effort:** DONE (was ~2h; actual was about the same)
- **Scope:** DONE — `bash scripts/check_b237_18.sh` → 17/17 pass;
  10 unit tests pass; `go build ./...` clean
- **Why low:** DONE (the cron runs on every skygate
  start; the operator's first cycle log line shows
  "reconcile: N portal_users checked (ok=X, linked=Y,
  relinked=Z, orphans=W, errors=0)")

**[B237.24 / release.yml lowercase] `ghcr.io/BarsSky/skygate`
mixed-case docker tag bug**
- **Status:** DONE in **B237.24** (v1.5.2+, 2026-09-08). The
  `.github/workflows/release.yml` `Build + push` step used
  `${{ github.repository_owner }}` directly in the `tags:`
  field. GitHub evaluates this to `BarsSky` (the operator's
  GitHub account, mixed-case). Docker / GHCR require
  **lowercase** repository paths. Both v1.5.0 (2026-09-04)
  and v1.5.2 (2026-09-07) release workflow runs failed with
  the same error:
  `ERROR: failed to build: invalid tag
  "ghcr.io/BarsSky/skygate:v1.5.0": repository name must be
  lowercase`. The docker image was never pushed to
  `ghcr.io/barssky/skygate` for either release. The v1.5.0
  release was published manually (the operator clicked
  "Publish" in the GitHub UI without docker); v1.5.2 stayed
  as a draft. **Fix**: append `| lower` to the GitHub Actions
  expression (4 tag lines total). **Trigger**: the operator
  force-moved the v1.5.2 tag to a new commit (218a02ac) and
  asked me to investigate; the `gh run view --log-failed`
  surfaced the lowercase error. **The v1.5.2 tag was deleted**
  (both local + remote) + the draft v1.5.2 GitHub release
  cleaned up, per the operator's rule: "delete the tag if
  CI failed". The release will be re-tagged after B237.24
  is committed + the release workflow re-runs and passes.
  **Permanent guard**: `scripts/check_b237_24.sh` (10
  contracts) pins the lowercase contract + checks that the
  v1.5.2 tag is absent (so a future force-move + bypass
  without re-running CI gets caught at the next
  `verify_pre_deploy.sh`).

**[B237.23 / B183+B232 regression] autoupdate ON CONFLICT
code/index drift**
- **Status:** DONE in **B237.23** (v1.5.2+, 2026-09-07). Live
  investigation of the B237.22-deployed ⏳ orange
  status badges surfaced a silent autoupdate failure:
  every `DomainAutoUpdater` INSERT hit `no unique or
  exclusion constraint matching` and was silently
  swallowed by `if err != nil { continue }`. V056
  (B125) + B188.2 (6-col design) + B183 (V060, 5-col
  revert) + B232 (V068, 6-col index repair, but no
  code revert) produced a code/index drift that
  persisted for 3 days on the live DB. B237.23
  restores 6-col `ON CONFLICT` in `sync.go` to match
  the live 6-col index + `qInsertDeviceRule` + B184
  status check. 14 contracts in `scripts/check_b237_23.sh`.
  See AGENTS.md B237.23 entry for the full timeline.

**[TD-11] Rule grouping: Cloudflare /12 + /24 merge**
- **Status:** DONE in **B237.22** (v1.5.2+, 2026-09-07) via
  Approach G (UI-only). The pre-B237.22 `/my/exit-rules` +
  `/admin/exit-rules` pages showed all 15 per-CIDR rows
  for a Cloudflare-backed domain as a flat list — correct
  but visually noisy (live data 2026-08: 46/151 = 30% of
  device_rules rows are CDN-derivable from 5 distinct
  parent_domains). Approach G groups them at the VIEW
  LAYER under a collapsible `<details>` header showing
  Source + CDN badge + "X диапазонов" count; the
  per-CIDR rows stay individually editable (each keeps
  its own remove button + audit log entry). Storage
  unchanged: no migration, no schema change, no
  autoupdate change, no SyncAdvertisedRoutes change.
  Natural key (user_id, device_id, exit_node_id,
  target_type, target_value) preserved.
- **Why low (resolved):** the visual noise was the
  cosmetic complaint; with the grouping it's now
  manageable. The 15-row list collapses to a 1-row
  header for the operator, while power users can still
  expand to see the per-CIDR details.
- **Decision rationale (Approach G chosen over A-F):**
  - "не наложит ли это ограничения на текущую работу
    правил и доступа" — Approach B (storage grouping)
    would lose the ability to lock specific CIDR / block
    specific Cloudflare ranges.
  - "ресур cloudflare может быть залочен как и любой
    другой внешний ресурс" — per-CIDR rows MUST stay
    individually editable.
  - "нужны именно правила на конкретный ресурс делать
    полный проброс не надо - ломает всю логику" — each
    row = specific CIDR, not opaque marker.
- **Files:**
  `internal/feature/exit_rules/cdn_group.go` (helpers
  + my-side struct), `cdn_group_admin.go` (admin-side
  struct), `internal/feature/exit_rules/form_my.go`
  + `form_admin.go` (wire-up),
  `internal/handlers/templates/exit_rules.html` +
  `admin/exit_rules.html` (UI),
  `internal/i18n/catalog_exit_rules.go` (1 new key
  `exit_rules.cdn_group_count` RU+EN).
  32-contract B-check in
  `scripts/check_b237_22.sh`.

**[TD-12] 30 ST1013-style noise items**
- **Status:** DONE in **B237.20** (v1.5.2+). The actual
  count was 23 (not 30 — PLANS.md was an estimate):
  22 U1000 "unused code" on test stubs that satisfy
  interface contracts + 1 S1021 "merge variable +
  assignment" in `dbmigrate/ssh_transport.go:279`.
  B237.20 fix: 3 outright deletions (`queryReachable`
  + 2 unused `mu sync.Mutex` fields) + 19
  `//lint:ignore U1000` directives on the test stubs
  + 1 S1021 fix. `staticcheck ./...` reports 0
  issues (was 23 pre-fix). 9-contract B-check in
  `scripts/check_b237_20.sh` pins the contract.
- **Effort:** DONE (was ~1h; actual was about the same)
- **Scope:** DONE — `bash scripts/check_b237_20.sh` → 9/9 pass;
  `go build ./...` clean; the 3 affected packages
  (db, elector, feature/healthz) all green
- **Why low:** DONE

**[TD-13] ~2850 lines of testutil.go stubs**
- **Status:** DONE in **B237.20** (v1.5.2+). The actual
  count was 917 lines (not 2850 — PLANS.md was a
  v0.32-era estimate). The audit found: the 917
  lines were mostly legitimate test fixtures, not
  stubs to delete. The productive part of the audit
  (3 deletions + 19 //lint:ignore + 1 S1021 fix)
  is captured under TD-12; the rest of testutil.go
  is a follow-up if a real duplication problem
  surfaces.
- **Effort:** DONE (was ~1d; actual was about 30 min
  for the cleanup, the audit itself was ~15 min)
- **Scope:** DONE
- **Why low:** DONE

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

**v1.2.0 — Style cleanup (TD-2, TD-14)**
- **DONE** in v1.2.0 (commit `38b2fb9e` + `d6f7b6b2`).
  Replaced 5 numeric HTTP status codes with `http.StatusXxx`
  constants (the v0.34.0-era estimate of 68 was wildly
  off — most had been fixed by the B62-B188 refactor work
  that landed before v1.2.0). The 5 SA1012 false-positives
  (TD-14) never materialized in the current tree.
- 2 stragglers (the 404 in `tags_test.go:66` + the 302 in
  `e2e_test.go:399`) slipped back in via the v1.5.0 work
  and were fixed in **B237.16** (v1.5.2+, commit `7a3dcfd7`).
  10-contract B-check in `scripts/check_b237_16.sh` pins
  the contract so a regression fails the verify-pre catalog.

**v1.3.0 — Backup S3 (TD-4)**
- Add S3 destination to /admin/backup/config
- New `internal/backup/dest_s3.go` (aws-sdk-go-v2)
- Verify-pre catalog check for S3 endpoint connectivity
- Effort: ~half a day

**v1.4.0 — UI quality-of-life (TD-6, TD-7)**
- **DONE** in v1.4.0 (B140 + B141, commit `e869650f`).
  /admin/exit-nodes `accept_routes` per-row toggle +
  /admin/users HSOrphans "Add as skygate user" button.
  Both B-checks (`check_b140.sh` + `check_b141.sh`) are
  green; both were briefly broken by the `s.DB` → `s.dbc()`
  rename that landed in v1.5.0 and the B-check fixes
  were bundled into B237.16.
- Effort: was ~6 hours; actual was about the same.

**v1.5.0 — HA Tier 1 (BL-2) — UNBLOCKED 2026-08-18**
- `svyatoslava-1` VM available, S3 bucket configured, Patroni + etcd in place
- Active-Passive with priority chain (`skygate` P1 / `skygate-standby` P2)
- Patroni auto-failover (existing config, **NOT touched**)
- external DNS provider failover (pluggable adapter)
- Tailscale Funnel: NO (operator's network doesn't expose Tailscale)
- Certsync: S3 as single source of truth, both nodes sync
- /admin/certificates: upload + DNS-01 toggle
- /admin/ha: chain editor + failover policy + force promote/demote
- /admin/deploy + `skygate deploy {push,pull}` + `skygate ha {promote,demote,reclaim}`
- Auto-failover with manual override (default ON)
- Auto-reclaim: OFF (avoid flap)
- Effort: ~3-4 weeks
- Execution tracker: `docs/internal/ha-v1.5.0-execution.md`
- **Open**: 10 questions awaiting operator answers (DNS provider creds, S3 IAM, etc.)

**v1.5.1 — DNS records (TD-5)**
- Requires headscale 0.30+ (release-not-yet)
- Per-user `exitnode.<user>.<your-domain>` DNS
- Effort: ~1 day
- Blocked on headscale release

**v1.5.2 — System tests persistence + reporting (TD-8) — DONE in v1.4.4**
- Verify /admin/system_tests records to system_tests_runs ✓
- Add "history" tab to /admin/system_tests ✓
- (delivered early as part of v1.4.4 — promoted from v1.6.0)

**v1.5.3 — Subnet-router auto-cleanup cron (TD-9) — DONE in v1.4.3**
- Daily cron to remove smoke-mesh test data ✓
- (delivered early as part of v1.4.3 — promoted from v1.7.0)

**v1.5.4+ — PostgreSQL cutover (BL-1) — DONE across v1.3.0 + v1.3.1 + v1.3.2**
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
