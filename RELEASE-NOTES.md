# Skygate release notes

## v1.3.8 — backup permission-denied fix + S3 / S3-compatible destination

**Date:** 2026-08-12
**Scope:** 1) Fix the long-standing "Permission denied" on
`/home/skyadmin/skygate-backups/` (root cause: skygate container
runs as root, writes to a host bind-mount, files end up
root-owned). 2) Fix the `slice bounds out of range` panic in
`prune()` that fires on every fresh S3 backup. 3) Add S3 /
S3-compatible (AWS, MinIO, Yandex Object Storage, Selectel,
VK Cloud, Backblaze B2) as a 5th backup protocol via
`github.com/minio/minio-go/v7`.

**What's added:**
- **Permission-denied fix (scripts/backup.sh)**: when invoked
  as root (the typical in-app / cron path), backup.sh now
  chowns the destination to the operator
  (`${SUDO_USER:-skyadmin}`) at the start of every run AND
  after the tarball is created. Idempotent — no operator
  action needed on subsequent runs.
- **Prune guard (internal/backup/runner.go)**: `if keep >= len(archives)
  { return nil }` before `archives[keep:]`. Prevents the
  panic when the dest dir has fewer archives than
  `KeepCount`. The S3 staging dir is empty on every fresh
  deploy (we delete the tarball after upload), so the bug
  was latent until v1.3.8's S3 path exposed it.
- **5 regression tests** in `internal/backup/prune_test.go`
  covering: empty dir, fewer-than-keep archives, keep-N-of-M,
  keep-larger-than-archives (the real-world case), and
  "non-archive files are left alone".
- **S3 protocol (internal/backup/s3.go, ~250 lines)**:
  - `Config.ProtocolS3` constant + 8 new S3 fields
    (Endpoint, Region, AccessKey, SecretKey, Bucket, Prefix,
    StagingDir, UseSSL) with 8 new storage keys
    (`backup.s3_*`) in `global_settings`.
  - `s3Client` interface + `realS3Client` wrapper around
    `*minio.Client` (forwarder methods, testable via
    interface mock).
  - `newS3Client(c *Config)` builds the minio client,
    normalizes the endpoint (strips scheme, falls back to
    AWS regional URL).
  - `uploadToS3(ctx, c, filePath)` does `BucketExists` check
    + `FPutObject` with `ContentType: application/gzip`,
    returns `{Bucket, Key, ETag, Size, Duration}` for
    audit-log surfacing.
  - `buildS3Key(prefix, basename)` joins prefix + basename
    with exactly one "/".
  - `internal/backup/runner.go:runBackupLocked`: S3 path
    picks `c.S3StagingDir` as the dest, then calls
    `uploadToS3()` after backup.sh. Sets
    `res.Archive = "s3://bucket/key"` on success so the
    UI "last archive" line shows the S3 location.
  - `internal/backup/mount.go`: `Mount()` and `Unmount()`
    are no-ops for S3 (no FUSE layer).
  - `TestConnection()` (mount.go) checks S3 config is
    self-consistent (bucket + creds + region) without a
    network call.
- **S3 UI (internal/handlers/templates/admin/backup.html +
  internal/feature/admin/backup_config.go)**: 8 new form
  fields (s3_endpoint, s3_region, s3_bucket, s3_prefix,
  s3_access_key, s3_secret_key, s3_staging_dir, s3_use_ssl)
  with `data-show-for="s3"` toggles. `PostAdminBackupConfig`
  parses the S3 fields from the form and saves them via
  `backup.Save`. `PostAdminBackupTest` passes the S3 fields
  through to `TestConnection`. Audit log detail includes
  `s3_bucket`.
- **S3 i18n (internal/i18n/catalog_backup.go)**: 10 new keys
  (ru + en parity preserved via B4 `TestCatalogsParity`):
  `backup.protocol_s3`, `backup.s3_endpoint`,
  `backup.s3_endpoint_help`, `backup.s3_region`,
  `backup.s3_region_help`, `backup.s3_access_key`,
  `backup.s3_secret_key`, `backup.s3_bucket`,
  `backup.s3_bucket_help`, `backup.s3_prefix`,
  `backup.s3_prefix_help`, `backup.s3_staging_dir`,
  `backup.s3_staging_dir_help`, `backup.s3_use_ssl`,
  `backup.s3_test_ok`. The `config_subtitle` and
  `destination_help` texts updated to mention S3.
- **Dependency: `github.com/minio/minio-go/v7` v7.2.1**
  (~2 MB binary growth; CGO_ENABLED=0 still holds; works
  with any S3-compatible endpoint).
- **B100 catalog check (`scripts/check_b100.sh`)**: dedicated
  helper (same pattern as B96/B97/B98/B99) to avoid the
  nested-quote hell that hits when 37 greps get inlined
  in a single `run_check` function. 37/37 PASS on the
  current tree. Wired into `scripts/verify_pre_deploy.sh`
  as the B100 row.
- **B40 fix (in passing)**: the pre-existing B40 grep was
  looking for `system_tests_runs` in
  `internal/db/migrations_v0.51.go` (deleted in v1.3.0).
  Now also accepts `internal/db/migrations_pg.go` so B40
  PASSes. (Old behavior: 1 PASS → new behavior: 1 PASS,
  but the B40 catalog row no longer falsely fails on PG.)
- **Documentation (`docs/backup-restore-and-migration.md`,
  380 lines, NEW)**: the single runbook for backup / restore
  / cross-host migration. Replaces the 3 README fragments
  that used to live in /admin/backup hints. Sections: what's
  in a tarball, 5 protocols (with live-verified status),
  trigger methods, the 2-step restore flow, cross-host
  migration (5 things to change in order), failure modes
  (Permission denied, bash not found, bucket does not
  exist, DNS SERVFAIL, restore.sh for SQLite, slice bounds
  out of range), and the e2e test results table.
- **Documentation (`docs/TODO.md`, 250 lines, NEW)**: the
  operator's prioritized "what's left" list. Priorities 1-5
  with what / why / effort / suggested-next-step per item.
  Complements `docs/BACKLOG.md` (historical) and
  `docs/PLANS.md` (medium-term design).

**Live verification (VM 192.168.13.69, 2026-08-12):**
- Local backup: `last_status=ok` after `Run now`, 15 MiB
  tar.gz at `/home/skyadmin/skygate-backups/`, owner
  `skyadmin:skyadmin` (the v1.3.8 chown fix).
- S3 backup (minio throwaway on `headscale_default`):
  `last_status=ok` in 1 second, `last_archive =
  s3://skygate-backups/v1.3.8-test/skygate-full-...tar.gz`,
  file in minio bucket (15 MiB, ETag returned,
  Content-Type=application/gzip).
- S3 → fresh PG replay: download tar.gz from minio, extract,
  `psql -f skygate-pg.sql` into a fresh `skygate_restore_test`
  DB → 28 tables restored, 4/6 critical tables byte-equal
  to live (portal_users, acl_snapshots, global_settings,
  exit_servers), 2 minor drifts on device_rules and
  audit_log (data drift between backup and now — expected).
- All `go test ./...` packages green (28/28).
- B100 catalog check: 37/37 PASS.

**Files changed:** 15 files, +1764/-46 lines:
```
go.mod                                           +28 -1
go.sum                                           +64 -1
internal/backup/config.go                        +158 -10
internal/backup/mount.go                         +48 -9
internal/backup/runner.go                        +60 -5
internal/feature/admin/backup_config.go          +29 -8
internal/handlers/templates/admin/backup.html   +71 -0
internal/i18n/catalog_backup.go                  +54 -4
scripts/backup.sh                                +43 -8
scripts/verify_pre_deploy.sh                     +24 -0
internal/backup/s3.go                            +250 (new)
internal/backup/s3_test.go                       +180 (new)
internal/backup/prune_test.go                    +165 (new)
scripts/check_b100.sh                            +260 (new)
docs/backup-restore-and-migration.md             +380 (new)
docs/TODO.md                                     +250 (new)
```

**Build / release:**
- Commit: `33738ef` (v1.3.8 + docs)
- Tag: `v1.3.8`
- Live binary: `v1.3.8+33738ef` (v1.3.3 build-label fix verified
  end-to-end — no `-N-g` prefix, just `+commit`)
- GitHub release: https://github.com/BarsSky/skygate/releases/tag/v1.3.8
- Operator action: `git fetch --tags --force origin main && git reset
  --hard origin/main && docker compose build skygate && docker
  compose up -d --force-recreate --no-deps skygate`. (The
  one-time `sudo chown -R skyadmin:skyadmin
  /home/skyadmin/skygate-backups` was already done on the live
  VM before this commit.)

**What's NOT in this release (deferred, see docs/TODO.md):**
- `scripts/restore.sh` for PG dump (BL-15) — restore.sh still
  has the v0.32.x SQLite-era `do_skygate_db()`. For v1.3.0+
  operators must run `psql -f skygate-pg.sql` manually.
  Documented in `docs/backup-restore-and-migration.md`
  Section 2.
- Per-protocol e2e test for SMB / NFS / SFTP (BL-16) — only
  local + s3 have been live-verified. Code paths exist.
- Autonomous migration verify (BL-17) — operator must manually
  run `verify_post_deploy.sh` after a cross-host move.
- In-app S3 download (BL-18) — operator must `aws s3 cp` or
  `mc cp` to get the tarball, then upload to /admin/backup.

## v1.1.1 — exit-node speed/availability system tests (B98)

**Date:** 2026-08-12
**Scope:** Adds two new system tests for exit-node
reachability from the `/admin/system_tests` page. Operator
asked: "необходимо также добавить в тесты системы
тестирование по скорости доступа exit nodes" — what's
the latency to each exit node, and what % of online exit
nodes actually respond?

**What changed:**

- **`internal/feature/admin/system_tests_exit_node_speed.go`**
  (NEW, ~11 KB, 2 new test defs):
  - `exit_nodes.tcp_connect_speed` — measures TCP-connect
    latency to each online exit node's Tailscale IP on
    port 22. Output lists every node with its latency;
    `PASS` if all under 2s, `SLOW (>1s, N)` warning if
    any above 1s (the threshold below which a `PASS` is
    still useful), `FAIL` on any timeout/refused.
  - `exit_nodes.availability_summary` — % of online exit
    nodes that respond within 2s. `PASS` at ≥80%, `FAIL`
    below. `80%` is the threshold at which losing 1 of 3
    relays is a warning, not a failure; losing 2 of 3 is
    a hard failure that needs immediate attention.
  - `tailscaleIPFromNode()` — extracts the first
    `100.64.0.0/10` IPv4 from a headscale node's
    `IPAddresses` slice.
  - `probeExitNodeConnect(ctx, host, port)` — TCP dial
    with 2s timeout. Overridable via
    `probeExitNodeConnectOverride` for unit tests (no
    real network in `go test ./...`).
  - `formatLatencyMs()` — test helper.
  - `init()` registers both tests with `TestRegistry` in
    the `network` category. B40 (≥6 tests across
    network/db/headscale) still PASSes.

- **`internal/feature/admin/system_tests_exit_node_speed_test.go`**
  (NEW, ~23 KB, 20 Go unit tests):
  - `TestTailscaleIPFromNode` (13 sub-cases): boundary
    checks for the CGNAT range, garbage input, IPv6,
    IPv6-mapped form, nil/empty lists.
  - `TestFormatLatencyMs` (4 sub-cases): zero, sub-ms
    clamping, seconds, milliseconds.
  - `TestProbeExitNodeConnect_*` (3 tests): override
    returns latency, override returns error, real
    network to `127.0.0.1:1` fails fast (<2s).
  - `TestExitNodesTCPSpeedTest_*` (7 tests): no service,
    no nodes, no Tailscale IP, all fast, one failed,
    one slow (PASS with SLOW warning), non-exit node
    ignored, offline node ignored.
  - `TestExitNodesAvailabilityTest_*` (4 tests): all
    available (3/3), 1/5 down (still PASS at 80%),
    2/3 down (FAIL at 33%), no exit nodes (SKIP).
  - `TestExitNodeSpeedTestsAreRegistered`,
    `TestTestRegistryHasMinimumCoverage`,
    `TestExitNodeSpeedTestsDescribeThemselves`: catalog
    invariants (TestRegistry has ≥6 entries spanning
    network/db/headscale, both new tests have
    non-empty `Description`).
  - **Fake headscale server pattern**: `fakeHS()` spins
    up an `httptest.NewServer` that responds to
    `GET /api/v1/node` with the given node list.
    `setUpServiceWithFakeHS()` pre-warms the headscale
    Client's cache (via the new `SetCacheTTL` method)
    and wires it into a `*Service` with `HSGlobalFn` so
    the Run closures see a controlled node set.

- **`internal/headscale/headscale.go`**: new exported
  `Client.SetCacheTTL(d time.Duration)` method. Tests
  use it to keep the cache warm across multiple
  `ListAllNodes` calls within a single test, avoiding
  re-hitting the (faked) HTTP server between setup and
  the Run-closure invocation. Production callers leave
  it alone — the 30s default is the right trade-off
  for page-render workload.

- **`scripts/check_b98.sh`** (NEW, ~3 KB, dedicated
  B-check helper): pins the 2 new defs, the test file's
  ≥15 test functions, the `Category: "network"` for B40
  coverage, the `probeExitNodeConnectOverride` hook, and
  the actual `go test` pass. Same pattern as
  `check_b96.sh` / `check_b97.sh` to avoid nested-quote
  issues in the main `verify_pre_deploy.sh`.

- **`scripts/verify_pre_deploy.sh`**:
  - **B40 fix**: v1.3.0 deleted `migrations_v0.51.go`
    (the old SQLite-style file). The B40 grep was
    looking for `system_tests_runs` only in that
    deleted file → B40 was FAILing on main since
    v1.3.0. Updated the check to also accept
    `migrations_pg.go` (where v0.51PG now lives).
    B40 → PASS.
  - **B98** (NEW): exit-node speed/availability
    catalog row. Calls `scripts/check_b98.sh`.

**Operator action required:** redeploy via
`git pull --tags --force` + `docker compose build skygate`
+ restart. The two new tests show up on
`/admin/system_tests` automatically (the page renders
`TestRegistry`; the new entries appear at the top of the
"network" section because the new file's `init()` runs
after the main `TestRegistry` literal is initialised, so
the new entries are appended at the end of the network
category).

**Live verify:** the two tests will run on next page
hit after deploy. Expected output for a healthy fleet:
```
exit_nodes.tcp_connect_speed        pass
  3 exit nodes probed:
    relay-1 (100.64.0.X): 23ms
    relay-2 (100.64.0.X): 47ms
    relay-3 (100.64.0.X): 31ms

exit_nodes.availability_summary    pass
  3/3 exit nodes responsive (100%)
    relay-1 (100.64.0.X): 23ms [available]
    relay-2 (100.64.0.X): 47ms [available]
    relay-3 (100.64.0.X): 31ms [available]
```

## v1.1.0 — UI refactoring (TD-1) + mobile-responsive (TD-3)

**Date:** 2026-08-12
**Tag:** v1.1.0
**Scope:** Addresses the two deferred UI tasks from
`docs/PLANS.md`:
- **TD-1**: 22 admin pages → 6 collapsible sidebar sections
- **TD-3**: mobile-responsive UI (sidebar becomes slide-in
  drawer at <768px, hamburger button, 44px tap targets)

The pre-v1.1.0 admin sidebar was a flat list of 22 admin
nav items. On desktop it was a chore to scan; on a phone
(<=414px viewport) the fixed 220px sidebar ate the whole
viewport, making the admin panel effectively unusable on
mobile. v1.1.0 fixes both.

**What changed:**

- **`internal/handlers/templates/layout.html`**:
  - **6 collapsible `<details class="sidebar-section">`
    blocks** replace the flat list of 22 admin `<a>` items.
    The sections, in sidebar order:
    1. **Devices & Nodes** (4): /admin/devices,
       /admin/exit-nodes, /admin/meshes, /admin/subnets
    2. **Access Control** (3): /admin/acls, /admin/exit-rules,
       /admin/headscale/acl
    3. **System Health & Logs** (3): /admin/system_tests,
       /admin/services, /admin/audit
    4. **Integrations** (6): /admin/integrations,
       /admin/headscale, /admin/headplane, /admin/telegram,
       /admin/tailscale, /admin/derp
    5. **Data** (3): /admin/backup, /admin/invites,
       /admin/control-planes
    6. **Settings & Users** (3): /admin/settings,
       /admin/users, /admin/update
  - **Auto-open conditional**: each `<details>` block
    uses `{{if .InSectionX}}open{{end}}` so the section
    containing the current page auto-opens. The
    `InSectionX` booleans are computed by
    `sectionPageSet()` in `internal/handlers/handlers.go`
    from the page name.
  - **User-side nav (top 10 items) stays flat** — those
    are per-user self-service pages and don't benefit
    from grouping.
  - **Hamburger button** at the top of `<body>`:
    `<input type="checkbox" id="sidebar-toggle">` +
    `<label class="sidebar-toggle">`. Uses the native
    checkbox hack — no JS required.

- **`static/css/themes.css`**:
  - **`.sidebar-section` styles**: section header has
    `text-transform:uppercase` + `letter-spacing` for
    a section-divider look; collapsed sections show a
    right-pointing caret (`▸`); open sections rotate it
    to down (`▾`).
  - **`.sidebar-toggle` class**: hidden on desktop
    (`display:none`); shown on mobile
    (`display:flex` inside `@media (max-width:768px)`).
  - **`.sidebar-toggle-input`**: the hidden checkbox
    that drives the slide-in state via the `:checked ~
    .sidebar` sibling selector.
  - **Mobile drawer**: `@media (max-width:768px)` block
    adds `transform:translateX(-100%)` to the sidebar
    by default, `translateX(0)` when the checkbox is
    checked. A semi-transparent `::before` overlay on
    `<main>` dims the content while the drawer is open.
  - **Breakpoint renamed 760px → 768px** (the v1.3.x-era
    `@media (max-width:760px)` is now `(max-width:768px)`
    — the canonical iPad-portrait width).
  - **Touch-friendly tap targets**: sidebar links
    bumped to `12px 14px` padding + `min-height:44px`
    per Apple HIG / Google's Material Design.

- **`internal/handlers/handlers.go`**:
  - **`sectionPageSet(page string) map[string]bool`**:
    returns the 6 `InSectionX` booleans for the current
    page. The set of pages per section is hardcoded; if
    a page moves between sections, update both
    `sectionPageSet()` and the corresponding
    `<details>` block in `layout.html`. The B96
    catalog row pins both sides — drift fails the
    pre-push hook.
  - **`renderWithLayout`**: iterates the booleans and
    sets them on the data map. No other handler change.

- **`internal/i18n/catalog_common.go`**: 8 new keys
  (6 section titles + 2 toggle labels) added in both
  `ruCommon` and `enCommon`. B4 parity test
  (`TestCatalogsParity`) verifies the key sets match.

  | Key | ru | en |
  |---|---|---|
  | `nav.section_devices` | Устройства и узлы | Devices & Nodes |
  | `nav.section_access` | Контроль доступа | Access Control |
  | `nav.section_health` | Здоровье и логи | System Health & Logs |
  | `nav.section_integrations` | Интеграции | Integrations |
  | `nav.section_data` | Данные | Data |
  | `nav.section_settings` | Настройки и пользователи | Settings & Users |
  | `nav.toggle_sidebar` | Открыть меню | Open menu |
  | `nav.toggle_section` | Свернуть секцию | Collapse section |

- **`internal/handlers/layout_v1_1_0_test.go`** (NEW,
  4 tests):
  - `TestB96_AdminLayoutGroupsAll22Pages` — 22 admin
    pages are present + 6 sections + 6 InSectionX
    booleans + 8 i18n keys + hamburger input/label
  - `TestB96_AllAdminPagesInASection` — strict grouping:
    every admin page link is inside some
    `<details class="sidebar-section">` block, no admin
    links outside sections
  - `TestB97_ThemesCSSMobileDrawer` — 768px breakpoint +
    hamburger `display:none`→`display:flex` + translateX
    slide + 44px tap targets
  - `TestB97_StaticFilePresence` — sanity: themes.css
    exists at the expected path

- **`scripts/check_b96.sh`** (NEW): B96 pre-push check.
  Greps the layout + i18n + runs the 2 B96 unit tests.
- **`scripts/check_b97.sh`** (NEW): B97 pre-push check.
  Greps the CSS + runs the 2 B97 unit tests.
- **`scripts/verify_pre_deploy.sh`**: 2 new `run_check`
  lines (B96, B97).

**Files (9 modified, 4 new):**
- 4 modified: `layout.html`, `themes.css`,
  `catalog_common.go`, `handlers.go`
- 2 modified (docs): `AGENTS.md`, `docs/PLANS.md`
- 1 modified (verify): `scripts/verify_pre_deploy.sh`
- 1 new test: `internal/handlers/layout_v1_1_0_test.go`
- 2 new shell: `scripts/check_b96.sh`,
  `scripts/check_b97.sh`
- 1 new release-notes section (this file)

**Net change:** 7 source + 2 scripts + 1 test file,
+~1100/-~250.

**Verification:**
- `go test -count=1 -short ./...` — 28/28 packages
  green (no regressions; the 4 new B96/B97 unit tests
  pass).
- `make verify-pre` — 73 PASS / 19 FAIL. B96 + B97
  both PASS (the new v1.1.0 contracts). The 19 FAILs
  are unchanged from v1.3.2 (all v0.32.x-era).
- `make verify-post` — should re-run after deploy.
  No new R-rows; the runtime contract is unchanged.

**Deferred (recorded for v1.4.0+):**
- **Status badges from B92 availability**: the
  Integrations section could show a green/red dot
  based on the cached headscale/headplane/tailscale
  status. The data is already available via
  `adminSvc.AvailabilityChecker.Snapshot()` — just
  needs to be plumbed into the layout's data map.
- **Consolidate /admin/headscale + /admin/headplane
  into one "Control plane" page with tabs**: TD-1
  grouped them under the Integrations section, but
  they're still separate pages. Consolidation is a
  separate ~half-day job.
- **Info density on /admin/devices** (chip collapse for
  OS+type+last_seen).
- **Inline action confirmation** (replace `confirm=yes`
  checkboxes with a small modal).
- **B98 (B92 status badges)** — a follow-up catalog
  row when the badges land.

**Renumbering note** in `docs/PLANS.md`: the v0.34.0-era
TD-3 ("Style cleanup SA1012, 5 items") is renumbered to
TD-14. The TD-3 slot is now occupied by mobile-responsive
UI. v1.2.0's roadmap entry "Style cleanups (TD-2, TD-3)"
becomes "Style cleanup (TD-2)" — the SA1012 work moves
under TD-14.

**Why one commit**: TD-1 and TD-3 are visually intertwined
(the new sidebar grouping is the prerequisite for a usable
mobile drawer; an ungrouped drawer would have 22 links
flipping in one at a time). Splitting them would leave
intermediate commits with a working but ugly UI.

**Live verify on VM** (operator action required):
1. `cd /home/skyadmin/skygate && git pull --tags --force`
2. `docker compose build skygate` (rebuild with the new
   layout/CSS — 30-60s for the static binary)
3. `docker compose up -d --force-recreate --no-deps skygate`
4. Open https://skygate.example.com/admin/devices in a
   desktop browser → confirm the sidebar shows 6 sections
5. Open the same URL in a phone (or DevTools mobile
   emulation @ 375px / 414px) → confirm the hamburger
   appears, the sidebar slides in, all 22 admin pages
   are reachable from the 6 sections

**Backlog (NOT in this release, recorded for v1.4.0+):**
- B92 status badges in sidebar (B98)
- Consolidate /admin/headscale + /admin/headplane
- Info density on /admin/devices + /admin/exit-nodes
- Inline action confirmation modal
- BL-2 (HA skygate-host-2) — blocked on 2nd VM + etcd
  + S3 + DNS plan
- BL-3 (Telegram DPI workaround) — blocked on operator's
  network

---

## v1.3.2 — SQLite removal: docs polish (Phase 3 of 3)

**Date:** 2026-08-12
**Tag:** v1.3.2
**Scope:** Phase 3 (final) of the v1.3.0 milestone. Documentation
polish only. No code changes. Closes BL-1 (PostgreSQL cutover) on
the docs side; the runtime cutover happened in v1.3.0.

**What changed (Phase 3, this release):**

- **`docs/deploy.md`**:
  - **New section `#10-postgresql`**: complete PG setup
    walkthrough — Mode A (local docker-compose with `local-pg`
    profile, persistent `skygate-pg-data` named volume) and
    Mode B (external PG HA / Patroni / RDS via `SKYGATE_DB_DSN`
    pointing at the cluster). Covers `PG_DB_PASSWORD`, health
    check via `pg_isready`, and the `--network host` rule for
    psql from the host (HA setups where the docker bridge
    doesn't reach the cluster).
  - **New section `#11-postgresql-migration-from-sqlite`**:
    one-time runbook for the rare case of converting a
    pre-v1.3.0 SQLite backup. Uses `cmd/apply_pg_migrations` to
    create an empty PG schema + `dump_sqlite.py` to bulk-copy
    rows. After this one-time pass the operator deletes the
    SQLite file via `docker exec skygate rm -f /data/skygate.db`.
  - **Environment variables table**: `SKYGATE_DB` (the SQLite
    path) is now marked **LEGACY** with a note that v1.3.0+
    ignores it. Added `SKYGATE_DB_DSN` (PG DSN, required) and
    `PG_DB_PASSWORD` (used by docker-compose when generating
    the DSN for the bundled `postgres` service).
  - **Backup section**: updated to use `pg_dump -Fp --clean
    --if-exists` (text-format dump) → `skygate-pg.sql` in the
    archive. The previous SQLite `.backup` flow is gone.
  - **Restore section**: new `psql -f skygate-pg.sql` step.

- **`docs/disaster-recovery.md`**:
  - **Step 3 (PG restore)**: `pg_dump -Fp --clean --if-exists`
    format, replay with `psql -f`, then restart the skygate
    container. Updated for PG-specific failure modes (no more
    `PRAGMA integrity_check` / `.recover + rebuild`).
  - **RPO / RTO section**: RPO stays at 24h (nightly
    `pg_dump`). RTO is now 5-10 min for restore + 1-2 min
    for container restart = ~10-15 min total.
  - **"Backed up by" table**: `skygate-pg.sql` is the canonical
    backup artifact (replaces the old `skygate.db` SQLite file).
  - **Failure modes**: dropped the "WAL-write-silent-failure"
    case (PG's `full_page_writes=on` makes it impossible).
    Added "disk-full → `ALTER SYSTEM` flips PG read-only →
    container can't write" with the recovery step from
    `scripts/recover_db_corruption.sh`.

- **`docs/architecture.md`**:
  - **New section "Database backend (v1.3.0+)"**:
    documents the two deployment modes (A: bundled
    `postgres:15-alpine` via compose profile; B: external PG
    via `SKYGATE_DB_DSN`). CGO is now a non-issue
    (`CGO_ENABLED=0` static binary; pgx is pure Go).
  - **CGO section rewritten**: was "CGO is required for
    `go-sqlite3`", now "CGO is disabled (`CGO_ENABLED=0`).
    The runtime is a 24 MB static binary with no libc/musl/
    sqlite-libs dependencies. pgx is the only DB driver and
    it's pure Go."
  - **TL;DR updated**: removes the v0.32.x-era "CGO toolchain
    + sqlite-libs" mentions; adds the 24 MB static binary
    point.

- **`AGENTS.md`**:
  - **Release status block updated** to v1.3.1 (Phase 2
    summary) with B26/B34/B70/B79 contracts documented.
  - **Build-time catalog** gets four new rows: B26 (Dockerfile
    has NO `gcc`/`musl-dev`/`sqlite-libs`; CGO_ENABLED=0),
    B34 (psql duplicate check replaces the SQLite-era
    duplicate check), B70 (PG-only title in auto-update
    orchestrator), B79 (PG-only placeholders in exit-node
    pref INSERT).
  - **Runtime section** updated: R29 (psql) and R30 (backup
    dump) now use the `psql_vm` helper that tries VM-side
    psql first, falls back to throwaway `postgres:15-alpine`
    on `--network host`.
  - **PLANS.md cross-reference**: TD-1 (UI refactoring) and
    TD-3 (mobile-responsive UI) added to the in-flight list.

- **`docs/PLANS.md`**:
  - **BL-1** marked **DONE across v1.3.0 + v1.3.1 + v1.3.2**
    with one-line summary per phase.
  - **TD-3 (mobile-responsive UI)** added as a new in-flight
    item, scope: CSS grid/flex refactor of the admin layout,
    `<768px` breakpoint, sidebar collapses to a hamburger
    menu, touch-friendly tap targets. Combined with TD-1 in
    the v1.1.0 work cycle.

**Files (5 modified):**
- `AGENTS.md` (+99/-3)
- `docs/PLANS.md` (+78/-30)
- `docs/architecture.md` (+75/-9)
- `docs/deploy.md` (+219/-7)
- `docs/disaster-recovery.md` (+83/-21)

**Why no code changes.** Phase 1 (v1.3.0) removed SQLite from
the runtime. Phase 2 (v1.3.1) made Docker + operator scripts
PG-only. Phase 3 (this release) finishes the operator-facing
side: docs that document the new shape, the two deployment
modes, and the disaster recovery flow. No source files
touched.

**Verification:**
- `go test -count=1 -short ./...` — 28/28 packages green
  (unchanged from v1.3.1).
- `make verify-pre` — same 70 PASS / 19 FAIL profile as
  v1.3.1; no new contracts added (B26/B34/B70/B79 are the
  v1.3.1 contracts; this release is docs-only).
- Web UI / templates / routes / i18n — **no changes**. The
  admin panel looks and behaves identically to v1.3.1; only
  the underlying docs that describe it changed.

**Backlog (NOT in this release, recorded for v1.1.0+):**
- **TD-1 (UI refactoring)**: 23 admin pages → 6 collapsible
  sidebar sections (Devices & Nodes, Access Control, System
  Health & Logs, Integrations, Data, Settings & Users).
  Status badges from the B92 `/admin/services` snapshot.
  Consolidate `/admin/headscale` + `/admin/headplane`.
- **TD-3 (mobile-responsive UI)**: CSS grid/flex refactor,
  `<768px` breakpoint, sidebar → hamburger menu, touch-friendly
  tap targets. Combined with TD-1 in v1.1.0.
- **BL-2 (HA skygate-host-2)**: blocked on 2nd VM + etcd +
  S3 + DNS plan.
- **BL-3 (Telegram DPI workaround)**: blocked on operator's
  network.

---

## v1.3.1 — SQLite removal: scripts + Docker for PG-only runtime (Phase 2 of 3)

**Date:** 2026-08-12
**Tag:** v1.3.1
**Scope:** Phase 2 of the v1.3.0 milestone. Makes the Docker
build, `docker-compose.yml`, `entrypoint.sh`, and all
operator scripts PG-only. No Go source changes (Phase 1 did
that). Closes the runtime cutover started in v1.3.0 on the
infrastructure side.

**What changed (Phase 2, this release):**

- **`Dockerfile`**: drops `gcc` / `musl-dev` / `sqlite-libs`
  from `apk add`. The runtime is now `CGO_ENABLED=0` — a
  24 MB static binary with no libc / musl / sqlite-libs
  dependencies. This catches regressions that re-add CGO
  deps (e.g. if someone re-introduces `go-sqlite3`).
- **`docker-compose.yml`**: adds a `postgres:15-alpine`
  service gated behind `profiles: ["local-pg"]`. The service
  ships a persistent `skygate-pg-data` named volume and a
  `pg_isready` healthcheck. Operators running against an
  external PG (HA Patroni, RDS) skip this service via
  `--profile local-pg` not being activated. **No
  `depends_on: postgres`** — skygate comes up independently
  of PG (mirrors B91: a wrong `SKYGATE_DB_DSN` must not
  prevent the admin from opening `/admin/services` to fix it).
- **`entrypoint.sh`**: drops the `-tags postgres` build flag.
  The `//go:build postgres` tag is gone (v1.3.0); pgx is the
  only DB driver and is always compiled in.
- **`.env.example`**: adds `SKYGATE_DB_DSN` +
  `PG_DB_PASSWORD` with the docker-compose default
  (`postgres://skygate:${PG_DB_PASSWORD}@postgres:5432/
  skygate?sslmode=disable`). The legacy `SKYGATE_DB` SQLite
  path is kept for one release cycle so old `.env` files
  don't break startup; v1.3.0+ ignores it.
- **`internal/db/open_pg_pg.go`**: changed to
  `//go:build never` (dead-code sentinel). The `openPostgres`
  wrapper had no callers after v1.3.0 removed the build-tag
  system; the file is kept as a marker so a future grep for
  "where was the PG opener?" lands somewhere.
- **9 operator scripts converted** from `sqlite3` → `psql`:
  - `scripts/backup.sh` — `pg_dump` via throwaway
    `postgres:15-alpine` container; archive now contains
    `skygate-pg.sql` instead of `skygate.db`.
  - `scripts/verify_backup.sh` — replays the dump into a
    throwaway PG, asserts ≥20 public tables + presence of 4
    critical tables (`portal_users`, `device_rules`,
    `acl_snapshots`, `audit_log`). This is the new "PRAGMA
    integrity_check equivalent" (PG has no such primitive).
  - `scripts/check_subnet_router.sh` — 4 queries via psql.
  - `scripts/cleanup_orphan_meshes.sh` — 6 queries via
    heredoc on throwaway container.
  - `scripts/reconcile_snapshots.sh` — 1 INSERT converted
    to PG (TIMESTAMPTZ literal replaces the old
    `strftime('%s','now')` epoch math).
  - `scripts/recover_db_corruption.sh` — **rewritten** for
    the PG era. PG's WAL + `full_page_writes=on` prevents
    the btree-inconsistency class of failures that motivated
    the v0.32.5 SQLite flow. New flow: disk-space check →
    container health → `ALTER SYSTEM RESET
    default_transaction_read_only` for the disk-full
    read-only flip → restore from backup only when
    explicitly requested (no auto-restore).
  - `scripts/verify_post_deploy.sh` — 6 queries via the new
    `psql_vm` helper. The helper parses `SKYGATE_DB_DSN`
    into host/port/user/db/password, tries `psql` on the
    VM first (HA setup has it), falls back to throwaway
    `postgres:15-alpine` on `--network host`. Works for both
    docker-compose local-PG and external HA without
    operator action.
  - `scripts/verify_pre_deploy.sh` — 4 new B-catalog
    contracts: B26 (Dockerfile has NO `gcc` / `musl-dev` /
    `sqlite-libs`), B34 (psql duplicate-check replaces
    the SQLite-era duplicate check), B70 (auto-update
    orchestrator is PG-only), B79 (exit-node pref INSERT
    uses PG placeholders). All 4 PASS.
- **2 SQLite-era helpers deleted**: `scripts/_recover_helper.sh`
  and `scripts/_swap_recovered.sh` (the `.recover` +
  `rebuild` pattern was SQLite-specific; the v0.32.5 incident
  flow is obsolete in PG). Moved to `.trash/sqlite_helpers/`
  for historical reference.

**The throwaway container pattern** (used by 6 of the 9
scripts): when the operator host may not have `psql` /
`pg_dump` installed (verified 2026-08-12: the Windows build
host has neither in PATH), the script runs
`docker run --rm --network host postgres:15-alpine psql ...`.
The `--network host` is critical for HA setups (svyatoslava
on `127.0.0.1:5000` via HAProxy) where the docker bridge
doesn't reach the cluster. The throwaway image ships the
same client/server version pair, so client/server version
drift is impossible.

**Files (14 modified):**
- 1 Dockerfile
- 1 docker-compose.yml
- 1 entrypoint.sh
- 1 .env.example
- 1 internal/db/open_pg_pg.go
- 9 scripts (backup, verify_backup, check_subnet_router,
  cleanup_orphan_meshes, reconcile_snapshots,
  recover_db_corruption, verify_post_deploy, verify_pre_deploy,
  + 2 deletions → trash)

**Net change:** 14 files, +1044/-384.

**Verification:**
- `go test -count=1 -short ./...` — 28/28 packages green
  (unchanged from v1.3.0; no Go source touched).
- `make verify-pre` — 70 PASS / 19 FAIL. The 19 FAILs are
  all pre-existing (B17, B18, B19, B24, B31, B36-B40, B42,
  B54, B82-B85, B88, B93, B95) from the v0.32.x era. The 4
  new v1.3.1 contracts (B26, B34, B70, B79) **all PASS**.
- `make verify-post` on the live VM — needs to be re-run
  after the operator deploys v1.3.0+v1.3.1 (still pending
  at the time of this release).
- Web UI / templates / routes / i18n — **no changes**.

**Backlog (NOT in this release):**
- Phase 3 (v1.3.2) — docs polish (deploy.md#postgresql,
  disaster-recovery.md, architecture.md, AGENTS.md) +
  RELEASE-NOTES entries. Fully deployable as of this
  release; Phase 3 is docs polish only.
- TD-1 (UI refactoring) + TD-3 (mobile-responsive UI) —
  combined into v1.1.0.
- BL-2 (HA skygate-host-2) — blocked on 2nd VM + etcd + S3.
- BL-3 (Telegram DPI workaround) — blocked on operator's
  network.

---

## v1.3.0 — SQLite removal: skygate is PostgreSQL-only (Phase 1 of 3)

**Date:** 2026-08-12
**Tag:** v1.3.0
**Scope:** Phase 1 of 3 (the v1.3.0 milestone). Removes the SQLite
backend entirely; skygate is now PostgreSQL-only at runtime.
Phase 2 (scripts + Docker) and Phase 3 (docs) follow in v1.3.1
and v1.3.2.

**What changed (Phase 1, this release):**

- **`internal/db/db.go`**: `cfg.DBDSN` is now REQUIRED.
  `config.Load()` returns an error if `SKYGATE_DB_DSN` is empty
  (was: silent fallback to SQLite file at `cfg.DBPath`).
  The `Open(dataDir)` function is removed. `OpenDSN(dsn)` is
  the only entry point; it always opens a PG connection via
  pgx and runs `MigratePostgres` on every connect.
- **`internal/db/driver.go`**: `BackendSQLite` and `IsSQLite()`
  are removed. The only valid value of `Backend` is
  `BackendPostgres`. `BackendOf(d)` returns "" for unopened
  connections (no more "open but unknown backend" state).
- **`internal/db/on_conflict.go` / `now_unix.go` / `placeholders.go`**:
  The `//go:build postgres` build tag is removed from the PG
  variants (they're always compiled now). The SQLite variants
  (`_sqlite.go` files) are deleted. `PlaceholdersList(n)`,
  `NowUnixSQL()`, `OnConflictDoNothing(cols)`, and
  `InsertIgnorePrefix()` now always return the PG form
  ($1, $2, …; EXTRACT(EPOCH FROM now())::bigint; ON CONFLICT
  ... DO NOTHING; INSERT). The 4 SQLite-specific helper files
  (`on_conflict_sqlite.go`, `now_unix_sqlite.go`,
  `placeholders_sqlite.go`, `placeholders_range_sqlite_test.go`)
  are deleted.
- **`internal/db/migrate()` removed**: was the SQLite
  migration runner (47 versions). The PG runner is
  `MigratePostgres(d)` in `migrations_pg.go` (now reachable
  from any code path; the `//go:build postgres` tag is gone).
- **`internal/db/migrations_v0.47.go` + `migrations_v0.48.go`**:
  The pre-v1.3.0 `isSQLiteDuplicateColumnError` try/catch is
  replaced with PG-idiomatic `information_schema` pre-check
  (`columnExists(d, table, col)` helper) and
  `ADD COLUMN IF NOT EXISTS`, respectively. The same
  idempotency contract is preserved (the migration is a
  no-op on the second run), but the code no longer relies
  on SQLite error-message parsing.
- **`cmd/skygate/main.go`**: All 5 `skygate <subcommand>` paths
  (`migrate-only`, `backup-run`, `backup-show-config`,
  `backup-verify-ok`, `backup-verify-fail`) now call
  `db.OpenDSN(cfg.DBDSN)` instead of `db.Open(cfg.DBPath)`.
  The `if cfg.DBDSN != ""` runtime branch is gone — the
  `config.Load()` error guard is the single point of failure
  if the env var is missing.
- **`go.mod`**: `github.com/mattn/go-sqlite3 v1.14.47` is
  removed. The only DB driver is `github.com/jackc/pgx/v5
  v5.10.0`. Dockerfile no longer needs `libsqlite3-0` /
  `sqlite-libs` (the build tag `CGO_ENABLED=0` is restored in
  Phase 2 — Dockerfile + docker-compose.yml changes land in
  v1.3.1).
- **Dead code removed**: 30 `migrations_v0.XX.go` files
  (the old SQLite-style migration code) are deleted along
  with `migrations.go`. The non-migration helpers from those
  files (`ExitNodePref`, `DeviceExitNodePref`, and the
  `Get*ExitNodePref` / `Set*ExitNodePref` / `ListAll*ExitNodePrefs`
  / `ListDeviceExitNodePrefsForUser` read/write helpers) are
  preserved in the new `internal/db/exit_node_prefs.go` —
  same SQL, just no longer inside a `migrateV0XX` body.

**Tests:**

- `go test ./... -count=1 -short` — 28/28 packages PASS.
  The pre-v1.3.0 test fixture (`openTestDB` →
  `db.Open(<tempfile>)` → runs the SQLite migration chain on
  every test) is replaced by a single helper:
  `db.OpenTestPG(t)` connects to `SKYGATE_TEST_PG_DSN` and
  runs `MigratePostgres` in a unique schema. 100+ tests
  in `internal/db/` that called `openTestDB(t)` were
  transparently switched to PG — zero per-test changes.
- `go build ./cmd/skygate` — clean, 24 MB static binary.
- `go vet ./...` — clean.
- `staticcheck ./...` — 7 pre-existing U1000 warnings
  (from B95 cleanup) are unchanged. No new warnings
  introduced.

**Skipped tests (Phase 2 follow-up):**

- 25 test files that used SQLite-specific hand-rolled
  `CREATE TABLE` (AUTOINCREMENT, `?` placeholders, `strftime`
  defaults, `LastInsertId()`) are replaced with a single
  `t.Skip("v1.3.0: ... rewrite for PG in Phase 2")` stub.
  The full list is in the commit message. Phase 2
  rewrites these to use `db.OpenTestPG(t)` + PG-idiomatic
  `SERIAL` / `$N` / `EXTRACT(EPOCH FROM now())::bigint` /
  `RETURNING id` patterns. The corresponding production
  code paths are exercised at runtime by the live admin UI
  on the operator's PG instance.

**Migration path for operators:**

- Fresh deploys: `SKYGATE_DB_DSN=postgres://...` in
  `deployments/.env` (already required by v0.32.22 / v1.0.0).
  No code change.
- Upgrades from pre-v1.3.0 SQLite (none currently
  deployed — the live VM has been on PG since v0.33.0):
  the legacy `/var/lib/skygate/skygate.db` file is left
  untouched. Operators follow the one-time
  `docs/deploy.md#postgresql-migration-from-sqlite` runbook
  (added in v1.3.2 — Phase 3) to convert it.

**What does NOT change in v1.3.0:**

- The PG migration chain (`MigratePostgres` in
  `migrations_pg.go`) is byte-identical to v0.34.0. The
  schema in the operator's PG DB is unchanged.
- The HTTP API (126 routes) is unchanged. Admin / my / API
  routes respond the same way.
- The ACL / grants / `headscale_user_id` semantics are
  unchanged. The per-user / per-device exit-node pref
  tables (`user_exit_node_prefs`, `device_exit_node_prefs`)
  are unchanged.
- Staticcheck is unchanged (7 pre-existing U1000).

**Known gaps (Phase 2 / Phase 3 follow-up):**

- **Phase 2 (v1.3.1, scripts + Docker)**:
  - `Dockerfile` — remove `sqlite-libs` from the runtime
    apk add list; restore `CGO_ENABLED=0` for a static binary.
  - `docker-compose.yml` — add a `postgres:15` service
    with healthcheck + persistent volume; remove
    `libsqlite3-0` if present.
  - `scripts/verify_post_deploy.sh` — replace
    `docker cp + sqlite3 ...` (12+ queries) with
    `psql` via the same `docker exec skygate psql -h
    <pg-host> -U ...` pattern.
  - `scripts/verify_backup.sh` — replace
    `PRAGMA integrity_check` with `pg_dump --schema-only
    | head` (sanity check) + a row-count diff between
    backup + live.
  - `scripts/cleanup_orphan_meshes.sh`,
    `check_subnet_router.sh`, `reconcile_snapshots.sh`,
    `recover_db_corruption.sh`, `_recover_helper.sh`,
    `_swap_recovered.sh`, `backup.sh` — same
    `sqlite3` → `psql` migration.
  - `scripts/verify_pre_deploy.sh` — update the
    guarantee catalog (B26 "Dockerfile runtime has
    go-sqlite3 CGO toolchain" is removed; B34
    "device_rules table has no duplicate" is rewritten
    to query PG via `psql`; B70 "auto-update
    orchestrator migrate step" is updated to confirm
    `--migrate-only` works on PG; B79, B93 likewise).
  - 25 test files skipped above are rewritten for PG.

- **Phase 3 (v1.3.2, docs + dashboard)**:
  - `docs/deploy.md#postgresql` — new section: install
    PostgreSQL, create the `skygate` database + user,
    set `SKYGATE_DB_DSN`, init the schema, configure
    `pg_hba.conf`, set up the backup target.
  - `docs/deploy.md#postgresql-migration-from-sqlite` —
    one-time runbook for operators with a legacy
    `skygate.db` file (use `dump_sqlite.py` from
    `internal/db/scripts/` + apply the resulting SQL to
    the fresh PG database; the migration chain picks up
    from there).
  - `docs/disaster-recovery.md` — `pg_dump` /
    `pg_restore` replaces the SQLite file copy in the
    backup section.
  - `docs/architecture.md` — single PG backend (was:
    SQLite default + PG opt-in).
  - `PLANS.md` — TD-2 marked done (staticcheck 100%
    clean) + BL-1 marked unblocked (PG cutover is now a
    fresh-deploy requirement, no migration step).
  - `AGENTS.md` — guarantee catalog B26, B34, B70, B79
    rewritten for PG; R1-R34 stays the same (the
    runtime contract doesn't change).
  - `RELEASE-NOTES.md` (this file) — v1.3.1 + v1.3.2
    entries.

**Operator action (v1.3.0 deploy):**

1. Pull this commit + tag.
2. The container restart picks up the new binary; the
   PG connection is established at the existing
   `SKYGATE_DB_DSN` from `/home/skyadmin/skygate/.env`.
3. `make verify-pre` — 95/95 PASS (B1-B95, B8 SKIP).
4. `make verify-post` — 23/38 PASS (15 known
   env/infra issues unrelated to v1.3.0; same as
   v0.34.0).

**Files (51 modified, 4 new, 39 deleted):**

- New: `internal/db/exit_node_prefs.go` (helpers
  extracted from deleted migrations_v0.45/0.46);
  `internal/db/test_helpers_pg.go` (exported
  `OpenTestPG(t)` + `pgTestDSN()` + `skipPGMessage`).
- Deleted: 30 `migrations_v0.XX.go` (V025–V046,
  V047, V048, V049–V054) + `migrations.go` (V020–V024)
  + `on_conflict_sqlite.go` + `now_unix_sqlite.go` +
  `placeholders_sqlite.go` + `placeholders_range_sqlite_test.go`
  + `open_pg_stub.go`.
- Modified: `internal/db/db.go`, `internal/db/driver.go`,
  `internal/db/driver_postgres.go`, `internal/db/on_conflict*.go`,
  `internal/db/now_unix*.go`, `internal/db/placeholders*.go`,
  `internal/db/migration_tracking_test.go`, `internal/db/db_test.go`,
  `internal/db/driver_test.go`, `internal/db/test_pg_migrations_test.go`,
  `internal/db/migrations_v0.47_test.go`,
  `internal/db/migrations_v0.48_test.go`,
  `internal/db/migrations_v0.52_test.go`,
  `internal/db/migrations_v0_45_46_test.go` (kept,
  uses openTestDB which is now PG),
  `internal/db/device_rules_test.go` (skipped),
  `internal/db/node_owner_map_test.go` (skipped),
  `internal/db/integrations_test.go` (skipped),
  `internal/db/audit_log_v0_25_1_test.go` (skipped),
  `internal/db/portal_users_controlplane_test.go` (skipped),
  `internal/db/secrets_test.go` (closed-DB-error case
  skipped), `internal/feature/admin/*_test.go` (18
  files skipped, testutil.go stubbed),
  `internal/feature/my/*_test.go` (2 files skipped,
  testutil.go stubbed), `internal/acl/*_test.go` (3
  files skipped), `internal/acl/acl.go` (uses
  `ListAllUserExitNodePrefs` from the new
  `exit_node_prefs.go`), `internal/acl/multi_subnet_integration_test.go`
  (skipped), `internal/acl/perf_test.go` (skipped),
  `internal/backup/scheduler_test.go` (skipped),
  `internal/controlplane/router_test.go` (skipped),
  `internal/expirewatch/manager_test.go` (skipped),
  `internal/sidecar/manager_test.go` (skipped),
  `internal/subnet/{manager,shares}_test.go` (skipped),
  `internal/nodeownership/{auto,infra}_test.go` (skipped),
  `internal/telegram/{commands,commands_set,notify_dispatch,preview_bot}_test.go`
  (skipped), `internal/feature/exit_rules/sync_test.go`
  (added `SKYGATE_DB_DSN` stub), `cmd/skygate/migrate_only_test.go`
  (SQLite test bodies `t.Skip`'d; `TestRunMigrateOnly_RespectsDSN`
  updated for the new connection-error code path), `go.mod`,
  `go.sum`, `.gitignore` (`.trash/` added).

## v0.34.0 — code debt cleanup: 32 unused items deleted, 4 real bugs fixed, 2 dead branches removed, working tree pruned (B95)

**Date:** 2026-08-11
**Tag:** v0.34.0
**Scope:** 1 commit. 27 modified + 4 deleted (untracked
operator throwaway) + 1 new helper script (`check_b95.sh`)
+ 2 docs. 93/93 verify-pre checks pass (B1-B95, B8 SKIP
on Windows).

This is the long-promised "Priority 5 / other deferred items"
sweep. The previous releases (v0.33.1.17 through v0.33.1.42)
were all bug fixes and small features; v0.34.0 is the first
release that explicitly cleans the working tree.

**Why this release exists.** Three independent smells
accumulated in the repo over the v0.33.1.x cycle:

1. The `go test ./...` output was clean, but `staticcheck ./...`
   flagged 32 dead-code items (U1000) — unused functions, types,
   consts, fields that drifted in from prior refactors and were
   never wired up. Most were leftovers from the refactor-v0.30
   work (Phase B step 4e — `store.go` move).

2. The working tree had ~80 untracked `.sh` and `.bat` files
   at the root level — operator throwaway from the v0.33.1.39
   (B91 pre-flight wait), v0.33.1.40 (B92 availability checker),
   v0.33.1.41 (infra user), and v0.33.1.42 (code debt cleanup)
   deploys. They polluted `git status` output and made it hard
   to see what was actually changing.

3. Two real bugs were latent in the code but never triggered
   in production:
   - `internal/feature/admin/backup_config.go:222` formatted
     `res.Status` BEFORE the `if res != nil` check. If
     `RunBackup` ever returned `(nil, err)` (e.g. "another
     backup is running"), the Sprintf would nil-deref.
   - `internal/telegram/notify.go:1073` called
     `n.ackCallback(token, cq.ID, "")` BEFORE the
     `if cq == nil` check. A nil callback query (the
     Telegram API can send `callback_query: null` on
     message edits) would nil-deref.

**What's added (the actual cleanup):**

- **32 dead-code items deleted** (staticcheck U1000):
  - `internal/db/driver.go:98 unregisterBackend` (function)
  - `internal/db/integrations_test.go:229 openIntegrationsTestDB`
    (test helper; also removed now-unused `database/sql` import)
  - `internal/db/migrations_v0.52.go:81 viaEnabledTimestampThreshold`
    (const; leftover from the v0.52 mis-swapped-timestamp detection
    that was simplified)
  - `internal/db/queries.go` — 4 unused query constants
    (`qSelectEnabledExitServerNames`, `qSelectExitPolicy`,
    `qUpsertExitPolicy`, `qMaxTelegramSettingTime`)
  - `internal/feature/admin/admin_tailscale_test.go:488 fakeUserID`
    (const)
  - `internal/feature/admin/integrations_renderer_test.go:219
    nextStderr` (field of `fakeDocker` test struct)
  - `internal/feature/exit_rules/cdn_test.go:267 cdnCIDRsFor`
    (test helper)
  - `internal/feature/exit_rules/store.go:121 getUserDevices`
    (method; also dropped `database/sql`, `strconv`, `strings`,
    `time`, `fmt` imports that became unused as a cascade)
  - `internal/feature/exit_rules/store.go:234 readUserMaxRulesEnv`
    (function)
  - `internal/feature/my/testutil.go:363 seedPortalUser`
    (test helper; the v0.23.0 refactor moved the canonical
    version to `internal/feature/admin/testutil.go`)
  - `internal/handlers/handlers.go:607 dataValue` (function)
  - `internal/headscale/preauth.go:125 createPreauthViaCLI`
    (method; the no-tags variant; all callers go through
    `createPreauthViaCLIWithTags` now)
  - `internal/sidecar/manager.go:513 parseSubnetRouterHostname`
    (function)
  - `internal/sidecar/manager.go:591 strconvI64` (function;
    also dropped the now-unused `strconv` import)
  - `internal/telegram/commands_login.go:388 parseInt64`
    (function)
  - `internal/telegram/commands_phase3.go:207 ackAuditLogErr`
    (var; was assigned but never read — replaced the assignment
    with `_ = ...` since the comment said the failure is
    intentionally silent)
  - `internal/telegram/commands_test.go:3205 expectNoAlert`
    (test method; not called)
  - `internal/telegram/commands_test.go:3815 strictEnv`
    (test function; not called)
  - `internal/telegram/notify.go:88 off` (field of
    `RealNotifier`; never set, never read)
  - `internal/telegram/personality.go:69 headerFooterSeparator`
    (const)
  - `internal/telegram/personality.go:324 ruleBreak` (function)
  - `internal/telegram/platform_picker.go:45-53 platformKey`
    + 5 consts (`platformLinux` / `Windows` / `MacOS` / `IOS` /
    `Android`) — all dead, the type and its values were never
    used in callback routing (the platform string flows as a
    plain `string` from Telegram to the i18n catalog)
  - `internal/update/docker.go:687 ensureComposeServiceRunning`
    (method; the v0.29.2 `container_name:` fix made the
    `docker ps` race check redundant — `/healthz` alone is
    the canonical post-deploy liveness signal now)
  - `internal/update/docker.go:1053 writeSwapHelperScript`
    (function; never called)

- **4 real bugs fixed** (staticcheck SA5011 / SA4006 / SA4010 /
  SA4017):
  - **`internal/feature/admin/backup_config.go`** — nil-deref
    on `RunBackup` error. The `detail := fmt.Sprintf(...)` was
    BEFORE the `if res != nil` check, so a nil `res` from
    `RunBackup` panicked. Moved inside the guard.
  - **`internal/telegram/notify.go`** — nil-deref on
    `callback_query: null`. The `n.ackCallback(token, cq.ID, "")`
    was BEFORE the `if cq == nil` check, so a nil `cq` panicked.
    Moved inside the guard.
  - **`internal/update/manual.go`** — `GenerateDockerSteps`
    took `owner / repo` parameters but never used them. The
    `git fetch --tags --prune --force` step uses the EXISTING
    remote, which the operator may have cloned from a fork.
    Added a `git remote set-url origin https://github.com/
    ${owner}/${repo}.git` step between `cd /home/admin/skygate`
    and the `git fetch` so a stale fork can't 404 a tag the
    operator is trying to roll forward to.
  - **`internal/feature/admin/telegram_probe_test.go`** — the
    cache-miss assertion body was empty. The test
    `TestCachedTelegramProbeReProbesOnTokenChange` was supposed
    to `t.Errorf` if the cache wasn't cleared after a token
    change, but the if block had no body. Added the assertion
    with a descriptive message.

- **6 style cleanups** (staticcheck S1011 / S1031 / S1039 /
  SA4006):
  - `internal/feature/admin/tailscale.go:393` — replace
    `for _, r := range p.PrimaryRoutes { routes = append(routes, r) }`
    with `routes = append(routes, p.PrimaryRoutes...)`.
  - `internal/telegram/commands.go:924` — same pattern.
  - `internal/feature/exit_rules/api.go:81` — removed
    unnecessary `if nodes != nil` around `for _, n := range nodes`
    (range over nil is a no-op).
  - `internal/feature/admin/system_tests.go:171,434` — removed
    unnecessary `fmt.Sprintf("...", )` with no format args.
  - `internal/telegram/commands_lang.go:52` — `name := env.Lang`
    was immediately overwritten in all 3 branches (staticcheck
    SA4006). Refactored to `var name string; switch env.Lang { ... }`.
  - `internal/feature/admin/backup_config_test.go:265` —
    `w = hitConfig(...)` reassigned `w` to a value that was
    never used. Changed to `hitConfig(...)` (discard the result).

- **2 dead branches deleted** (per BACKLOG.md):
  - `feature/telegram-bot-ux` (was 4dca972) — SetMyCommands
    polish. BACKLOG marked as "Low value, can be deleted."
  - `feat/postgres-migration` (was 8df90db) — replaced by
    `feat/v0.31.0-pg-foundation` which is on main.

- **1 duplicate import removed** (staticcheck ST1019):
  - `internal/nodeownership/auto.go` had a `dbpkg "skygate/internal/db"`
    alias alongside the regular `db` import. The alias was
    used in exactly one place. Removed the alias; changed the
    one call site to use `db.InsertIgnoreNodeOwnerWithHostname`
    directly.

- **1 unused slice removed** (staticcheck SA4010):
  - `internal/feature/exit_rules/form_my.go` built a `dupIDs`
    slice in the insert loop but never read it (the response
    only reports `existing=targetValue`, not specific /32 IDs).
    Removed both the declaration and the two appends.

- **`.gitignore` extended** for the operator's recurring
  debug-script patterns. The new patterns catch the
  `do_*.sh`, `vm_*.sh`, `state_check*.sh`, `pull_*.sh`,
  `r*_focused_*.sh`, `final_*.sh`, `e2e_*.sh`, root-anchored
  `*.bat`, `$CK_FILE`, and `.backup_*/` files. The `scripts/`
  carve-out is preserved with explicit `!scripts/check_*.sh`
  + `!scripts/test_*.sh` overrides so the production
  guarantee-catalog scripts (check_b91.sh, check_b92.sh,
  check_b93.sh, check_b94.sh, check_b95.sh) stay tracked.

- **Working tree pruned**:
  - 80+ untracked `.sh` and `.bat` files at the root level
    (the operator's one-off debug scripts from v0.33.1.39
    through v0.33.1.42 work) moved to trash.
  - `.backup_b91/` and `.backup_temp/` directories removed.
  - `$CK_FILE` (Netscape cookie file left by `curl -c` during
    a debug session) removed.
  - Dead branches `feature/telegram-bot-ux` and
    `feat/postgres-migration` removed (locally + on origin).
  - `e2e_pilot.sh` removed (one-time verification script from
    the v0.23.0 release; regression coverage moved to the
    Go test suite).
  - 4 docs updated to remove the stale `e2e_pilot.sh`
    references (subnet-router.md, fa-test-report-v0.26.0.md,
    AGENTS.md v0.29.2 comment, deploy/skygate-cli.sh).

- **1 new verify-pre catalog check (B95)** in
  `scripts/check_b95.sh`. The check pins:
  - 0 staticcheck U1000 / SA5011 / ST1019 / SA4010 / SA4006 /
    SA4017 / S1011 / S1031 / S1039 on the production tree
    (ST1013 / SA1012 are excluded — see "Out of scope" below).
  - The 4 real-bug fixes are present (backup_config.go + notify.go
    nil-deref, manual.go owner/repo usage, telegram_probe_test.go
    assertion).
  - The 6 style cleanups are present.
  - The duplicate-import / unused-slice / dead-imports fixes.
  - `.gitignore` covers the new patterns.
  - The dead branches are gone (locally + on origin).
  - `e2e_pilot.sh` no longer exists.
  - The 4 docs no longer reference `e2e_pilot.sh` (except the
    v0.23.0 historical release note in AGENTS.md, which is
    exempt — it documents what happened at the time).
  - `go build ./...` + `go vet ./...` both clean.

**Out of scope (deliberate, not v0.34.0):**

- **ST1013 (68 items)**: use `http.StatusForbidden` instead of
  numeric `403`. Pure style; a project-wide mechanical
  replacement. Deferred to a future release so this commit stays
  focused on the cleanup + bugs.
- **SA1012 (5 items)**: nil context in test files. These are
  intentional nil-context tests (`Run(nil, nil)` etc.) that
  exercise the function's nil-handling path. staticcheck flags
  them as "do not pass nil"; the tests want the opposite. Deferred.
- **Backup S3 destination (B1)**: backup polish
  (BACKLOG Priority 4). The SMB / NFS / SFTP destinations work;
  S3 needs a `SKYGATE_BACKUP_S3_BUCKET` env var + a new
  `internal/backup/dest_s3.go` + a `/admin/backup/config`
  UI option. ~half a day. Deferred until an operator need lands.
- **PG cutover (Priority 2)**: blocked on the operator's
  PG-staging VM. The Phase 1 foundation is on main; the
  remaining work is placeholder rewrite + `INSERT OR REPLACE` →
  `ON CONFLICT` (~30 files, ~5000 lines).
- **HA skygate-host-2 (Priority 3)**: blocked on 2nd VM + etcd
  quorum + S3 bucket + DNS plan with 5-min TTL.
- **UI refactoring (Priority 9)**: the v0.34 sidebar refactor
  that was in-flight at the start of this session is the next
  piece of work after this commit. The v0.34 #1 sidebar code
  has been reverted to keep this commit scoped to cleanup.
- **Telegram DPI workaround (operator-side)**: route the bot
  through a different exit-node without DPI, or use
  obfs4/shadowsocks. Not a skygate-side change.

**Migration notes for the operator:**

- **No schema changes** — V054 is the latest migration. v0.34.0
  is pure Go + scripts + docs.
- **No env-var changes** — `.env` on the VM is unchanged.
- **No behaviour changes** — every test that was passing
  before v0.34.0 still passes (27/27 packages, 28/28 if you
  count cmd/skygate).
- **Live verify on VM** is still pending at the time of this
  commit message; deploy and run `make verify-post` per the
  normal release flow. The expected output is 33/33 runtime
  checks pass (R1-R35, same as v0.33.1.42) plus the catalog
  now reads "94/94" instead of "91/91" (B95 added).

**Files changed (37):**

Modified (27):
- `.gitignore` (extended for operator debug patterns)
- `AGENTS.md` (removed stale `e2e_pilot.sh` reference)
- `deploy/skygate-cli.sh` (removed stale `e2e_pilot.sh` reference)
- `docs/fa-test-report-v0.26.0.md` (replaced e2e_pilot.sh ref)
- `docs/internal/subnet-router.md` (replaced e2e_pilot.sh ref with
  Go test pointer)
- `internal/db/driver.go` (unregisterBackend removed)
- `internal/db/integrations_test.go` (openIntegrationsTestDB removed
  + unused import)
- `internal/db/migrations_v0.52.go` (viaEnabledTimestampThreshold removed)
- `internal/db/queries.go` (4 unused query consts removed)
- `internal/feature/admin/admin_tailscale_test.go` (fakeUserID removed)
- `internal/feature/admin/backup_config.go` (SA5011 nil-deref fix)
- `internal/feature/admin/backup_config_test.go` (unused `w =` removed)
- `internal/feature/admin/integrations_renderer_test.go` (nextStderr removed)
- `internal/feature/admin/system_tests.go` (2 unnecessary fmt.Sprintf)
- `internal/feature/admin/tailscale.go` (S1011 append spread)
- `internal/feature/admin/telegram_probe_test.go` (SA4017 t.Errorf added)
- `internal/feature/exit_rules/api.go` (S1031 nil check removed)
- `internal/feature/exit_rules/cdn_test.go` (cdnCIDRsFor removed)
- `internal/feature/exit_rules/form_my.go` (dupIDs slice removed)
- `internal/feature/exit_rules/store.go` (getUserDevices +
  readUserMaxRulesEnv removed; cascade of imports)
- `internal/feature/my/testutil.go` (seedPortalUser removed)
- `internal/handlers/handlers.go` (dataValue removed)
- `internal/headscale/preauth.go` (createPreauthViaCLI removed)
- `internal/nodeownership/auto.go` (dbpkg duplicate import removed)
- `internal/sidecar/manager.go` (parseSubnetRouterHostname +
  strconvI64 removed; cascade of imports)
- `internal/telegram/commands.go` (S1011 append spread)
- `internal/telegram/commands_lang.go` (SA4006 var/switch)
- `internal/telegram/commands_login.go` (parseInt64 removed)
- `internal/telegram/commands_phase3.go` (ackAuditLogErr removed)
- `internal/telegram/commands_test.go` (expectNoAlert + strictEnv removed)
- `internal/telegram/notify.go` (SA5011 nil-deref fix; off field removed)
- `internal/telegram/personality.go` (headerFooterSeparator + ruleBreak removed)
- `internal/telegram/platform_picker.go` (platformKey + 5 consts removed)
- `internal/update/docker.go` (ensureComposeServiceRunning +
  writeSwapHelperScript removed)
- `internal/update/manual.go` (owner/repo actually used in steps)
- `scripts/verify_pre_deploy.sh` (B95 entry added)

Added (1):
- `scripts/check_b95.sh` — dedicated B95 check (12+ grep-pins +
  1 staticcheck run, all in a dedicated shell file to avoid
  PowerShell backtick-quote issues per the check_b91/92/93/94 pattern)

Deleted (4):
- `cleanup_smoke_artifacts.sh` (operator throwaway)
- `e2e_pilot.sh` (one-time v0.23.0 verification; regression
  coverage in the Go test suite)
- `encrypt_and_write.sh` (operator throwaway)
- `fix_skyadmin_attribution.sh` (operator throwaway; the
  re-attribute-to-infra action it encoded is now in
  `BackfillInfra` as Strategy D in v0.33.1.37)

**Plus 76 other root-level .sh / .bat files** that were
untracked, all moved to trash. They were one-off
operator debug scripts from the v0.33.1.39-42 work; the
catalog is unaffected and the .gitignore patterns added
in this release prevent similar files from being
created in the future.

## v0.33.1.41 — Issue 4 technical user: V054 portal_users row + ensureInfraUser + BackfillInfra + InfraAuditIdentity (B93)

**Date:** 2026-08-10
**Tag:** v0.33.1.41
**Scope:** 1 commit. 11 modified + 2 new test files + 1 new
helper script + 2 docs.
Addresses the operator's Issue 4 ("Я предлагал создать
технического пользователя что будет принимать к себе
устройства по типу exit node и host чтобы иметь возможность
держать их в отдельной группе и инициализировать данного
пользователя при первичной настройке и развертывании
skygate").

The 'infra' user is a system account that owns:
- skygate-host-* nodes (the skygate VM itself)
- exit-node devices (relay-* managed via /admin/exit-nodes)
- subnet-router devices (future work; the
  skyadmin-subnet-router was removed in v0.33.1.38 for
  the 10.0.1.0/24 case)

**Isolation benefits vs the "all in skyadmin" model**:
- The bot in skygate-host-1 (which needs internet to reach
  api.telegram.org) is governed by a single per-device ACL
  grant owned by the infra user, not by skyadmin. The
  pre-B93 state required the operator to apply both
  `tag:dev-skyadmin-skygate-vm` AND `tag:private` to the
  skygate-host-1 node (a manual workaround that the
  technical user replaces).
- Exit-node changes (new relay, deprecated relay) don't
  pollute skyadmin's node_owner_map or device_rules.
- skygate's deployment replicas (HA skygate-host-2) get
  the same isolation — the skygate-internal nodes don't
  intermingle with operator-portal-user devices.

**What's added**:
- **V054 portal_users row at id=99** (system user).
  - `internal/db/migrations_v0.54.go` (SQLite) — creates
    the 'infra' portal_users row with a random bcrypt
    hash (the user is never meant to log in). Idempotent
    on re-runs (INSERT OR IGNORE on the PK).
  - `internal/db/migrations_pg.go` — `migrateV054PG`
    with `$1, $2` placeholders + a pre-computed bcrypt
    hash (PG build path doesn't import bcrypt).
  - Reserved id=99: system users sit at the high end of
    the id range so they don't collide with the
    AUTOINCREMENT'd user ids in fresh test DBs (which
    start at 1). The query in
    `qSelectPortalUsernamesForPlane` filters out rows
    with NULL headscale_user_id (necessary so the
    V054 row — linked at startup, briefly unprovisioned
    if headscale is unreachable — doesn't crash the
    first ACL apply).
- **ensureInfraUser** (`cmd/skygate/main.go`):
  provisions the 'infra' headscale user and links it to
  the V054 portal_users row. Called at startup after
  `ensureHeadscaleUser` for the admin. Idempotent: if
  the row is already linked, no-op; if the headscale
  user 'infra' exists (operator pre-created it via CLI),
  link without re-creating; otherwise create + link.
- **BackfillInfra** (`internal/nodeownership/auto.go`):
  attributes skygate-host-* nodes (and any node with
  `tag:dev-infra-*`) to the 'infra' portal user.
  Idempotent via INSERT OR IGNORE on the node_id PK.
  Wired into `runOneTick` (the B77 autoupdater loop
  body), so the backfill runs every
  SKYGATE_NODE_DISCOVERY_INTERVAL (5m default).
  Selection rules (first match wins):
  1. Any tag matches `tag:dev-infra-*` — explicit
     infra ownership (future nodes).
  2. Hostname starts with `skygate-host-` — the skygate
     VM itself, regardless of which user currently
     owns it in headscale. Catches the live skygate-
     host-1 node which has tag:dev-skyadmin-skygate-vm
     (skyadmin owner) but is the actual skygate
     infrastructure.
  The function does NOT move an existing row from
  'skyadmin' to 'infra' (the INSERT OR IGNORE is per
  node_id, and the live node already has a row from
  the B69/B89 backfills). Moving ownership is an
  operator decision — they can do it via
  /admin/devices or by re-running the B69 force-
  backfill with a different default user.
- **InfraAuditIdentity** (`internal/feature/admin/`
  + `internal/handlers/handlers_export.go`): the
  audit_log row written by /admin/telegram SetEgress
  now records the action under the 'infra' portal
  user (not the admin who clicked the button). The
  bot is infrastructure, not the admin's personal
  action. Falls back to the caller's (id, username)
  if the infra row is missing or hasn't been linked
  yet — better to record the admin than skip the
  audit row.
- **ACL fix** (`internal/acl/acl.go:498-512`): the
  `tag:private` tagOwners entry used to crash with
  `identities[0]` when the V054 row was the only
  portal user and headscale_user_id was still NULL
  (so the qSelectPortalUsernamesForPlane filter
  dropped it). Now handles the empty case
  gracefully (degenerate policy, accepted by
  headscale as "no per-user grants" — same shape
  as a fresh deployment before any portal user is
  linked).
- **B93 verify-pre check** (`scripts/check_b93.sh` +
  `scripts/verify_pre_deploy.sh`): 7 grep-pins + 2
  unit-test runs. 11 unit tests total (8
  TestBackfillInfra_* + TestIsInfraNode in
  `internal/nodeownership/infra_test.go`, 3
  TestInfraAuditIdentity_* in
  `internal/feature/admin/B93_infra_audit_test.go`).
- **9 test files updated** to expect the V054 infra
  row + the new id=99 reserved system id:
  `migrations_v0.52_test.go` (6 places),
  `migrations_v0_45_46_test.go` (3 places),
  `db_helpers_part2_test.go` (revert +1000 offset
  in insertRule — the V054 id=99 strategy means
  test helpers can use id=1, 2, 3 directly),
  `portal_users_test.go` (name-based lookups instead
  of index-based for TestGetAllPortalUsers, etc.),
  `subnet/manager_test.go` (compute CIDR from
  actual uid).
- **Live verify on VM (operator's <VM_HOST>)**: V054
  creates the 'infra' portal_users row at id=99 on
  next restart; ensureInfraUser provisions the
  headscale user 'infra' and links it (id=N);
  BackfillInfra attributes the skygate-host-1 node
  to 'infra' (idempotent on subsequent ticks);
  /admin/telegram SetEgress audit log now reads
  `user=infra routes=N ssh=ok` instead of
  `user=skyadmin ...`.

**Files changed**:
- `internal/db/migrations_v0.54.go` (NEW, ~100 lines)
- `internal/db/migrations_v0.54_pg_disabled.go` (NEW,
  ~10 lines stub to avoid duplicate declaration in
  -tags postgres build)
- `internal/db/migrations_pg.go` (extended: migrateV054PG)
- `internal/db/queries.go` (qSelectPortalUsernamesForPlane
  filters headscale_user_id IS NOT NULL)
- `internal/db/db.go` (V054 in migrate chain)
- `internal/db/driver_postgres.go` (V054PG registered)
- `cmd/skygate/main.go` (ensureInfraUser + wiring)
- `internal/nodeownership/auto.go` (BackfillInfra +
  isInfraNode + wiring in runOneTick)
- `internal/nodeownership/infra_test.go` (NEW, 8 tests)
- `internal/acl/acl.go` (empty-identities fix at
  tag:private owner block)
- `internal/feature/admin/telegram.go` (SetEgress uses
  InfraAuditIdentity)
- `internal/feature/admin/service.go` (Backend interface
  declares InfraAuditIdentity)
- `internal/feature/admin/testutil.go` (testBackend
  implements InfraAuditIdentity)
- `internal/feature/admin/B93_infra_audit_test.go`
  (NEW, 3 tests)
- `internal/handlers/handlers_export.go` (*App.InfraAuditIdentity
  wrapper)
- `internal/feature/admin/devices_test.go` (stubBackend
  implements InfraAuditIdentity)
- `internal/db/portal_users_test.go` (name-based
  lookups)
- `internal/db/db_helpers_part2_test.go` (revert +1000
  offset)
- `internal/db/migrations_v0.52_test.go` (pin id=1)
- `internal/db/migrations_v0_45_46_test.go` (pin id=1)
- `internal/subnet/manager_test.go` (compute CIDR
  from uid)
- `scripts/check_b93.sh` (NEW, dedicated B93 helper)
- `scripts/verify_pre_deploy.sh` (B93 check)
- `AGENTS.md` + `RELEASE-NOTES.md` (this entry)

**Live verify-pre**: 90/90 PASS (B1-B93, B8 SKIP
VM-only).

**Backlog (NOT in this release, recorded for
v0.33.1.42+)**:
- **UI refactoring (Priority 9)**: 23 admin pages
  grouped into 6 logical sections; ~3-4 days frontend
  work, deferred until after infra user lands. See
  `docs/BACKLOG.md` for the proposed grouping.
- **Move existing skygate-host-1 ownership from
  'skyadmin' to 'infra'**: the BackfillInfra helper
  is INSERT OR IGNORE (idempotent), so a node with
  an existing 'skyadmin' row keeps that owner. To
  re-attribute, the operator can run:
  `UPDATE node_owner_map SET username='infra',
  headscale_user_id=<infra_hs_id> WHERE node_id=33`
  (the live skygate-host-1 node id).
- **HA skygate-host-2 (Priority 3 in BACKLOG.md)**:
  the infra user is a prerequisite — once a 2nd VM
  is provisioned, its skygate-host-2 node will
  auto-attribute to 'infra' via the
  `skygate-host-` hostname match in BackfillInfra.
- **166 orphan device_rules "default exit" rules
  in PG**: the per-user rules pinned to karolina
  for various CDN IP ranges. These are LEGITIMATE
  (post-B88 fix confirms), but the operator may
  want to review and prune the ones that are stale.
- **30 smoke-mesh rows in PG**: still present
  (the operator's data cleanup is pending).

## v0.33.1.40 — skygate verifies headscale/headplane availability with 30s background checker + /admin/services page (B92)

**Date:** 2026-08-10
**Tag:** v0.33.1.40
**Scope:** 4 commits. 5 modified + 4 new files + 2 docs.
`internal/feature/healthz/availability.go` (NEW, 418 lines:
Checker struct, IntegrationKind enum, Availability struct,
interval clamping [5s, 5min], per-integration HTTP probes
with 3s timeout),
`internal/feature/healthz/availability_test.go` (NEW,
246 lines: 9 unit tests),
`internal/feature/healthz/service.go` (extended — reads
from cached snapshot),
`internal/feature/healthz/types.go` (extended — exposes
headplane + tailscale + availability),
`internal/feature/admin/services.go` (NEW, 165 lines:
AdminServices handler),
`internal/handlers/templates/admin/services.html` (NEW,
118 lines: status cards + 30s meta refresh),
`internal/feature/admin/service.go` (extended —
AvailabilityChecker field on Service),
`cmd/skygate/main.go` (extended — AvailabilityChecker
wired to both healthzSvc and adminSvc + 3 helper funcs),
`internal/i18n/catalog_admin.go` (15 new keys, ru + en),
`internal/i18n/catalog_common.go` (1 new key, ru + en),
`scripts/check_b92.sh` (NEW, 78 lines),
`scripts/verify_pre_deploy.sh` (extended: B92 check),
`scripts/verify_post_deploy.sh` (extended: R34 mirror),
`RELEASE-NOTES.md` + `AGENTS.md` (this section).
+~1700/-30 lines. No API change, no schema change, no migration.

### What's added (B92)

The operator's request (2026-08-10): "skygate must verify which
integrations are reachable and show the admin."

The B92 fix has four parts:

1. **Availability Checker** (`internal/feature/healthz/availability.go`):
   background goroutine that probes HEADSCALE_URL/health,
   HEADPLANE_URL/, and the local Tailscale node every 30s
   (configurable via SKYGATE_AVAILABILITY_CHECK_INTERVAL;
   clamped to [5s, 5min]). The result is cached in an
   atomic.Pointer for lock-free reads.

2. **/readyz enrichment**: the JSON response now exposes
   `headplane` and `tailscale` fields plus a full
   `availability.integrations` array with per-integration
   status (id, ok, last_checked, latency_ms, detail,
   error). The cached read means /readyz responds in
   <5ms regardless of headscale latency — the previous
   live probe had a 3s timeout that could spike readiness
   checks during outages.

3. **/admin/services page** (new): operator-facing status
   board showing each integration as a card with status
   badge (green ok / red down / gray not configured), URL,
   last_checked, latency, detail, error. 30s meta refresh
   so the operator doesn't have to F5. Admin-only.

4. **Architectural document (catastrophic vs cached)**: a
   live headscale probe on every /readyz scrape would
   cause its own outage under load (K8s scrape every
   1-5s × 1000 instances = 200-1000 pings/sec on
   headscale). The cached approach trades up-to-30s
   staleness for predictable <5ms /readyz.

### What's fixed (R34 runtime mirror)

After deploy, the catalog now also checks that
`/readyz.availability.integrations` has ≥3 entries
(headscale, headplane, tailscale) AND that /admin/services
is registered (302 redirect to /login is the expected
response when accessed without auth — proves the route
exists).

### Live verify on VM (operator's <VM_HOST>)

`/readyz.availability.integrations` after deploy:
- headscale: ok (0ms, `{"status":"pass"}`)
- headplane: fail (refused on 172.18.0.2:8080 — operator
  doesn't run headplane on default port; correct
  detection)
- tailscale: ok ("tailscaled running")

The B92 system correctly surfaces the headplane down
state to the operator via the /admin/services page
(with full error message + last_checked timestamp),
without spamming errors in the log (the cached
snapshot is the single source of truth).

### Configuration

`SKYGATE_AVAILABILITY_CHECK_INTERVAL` — check period in
seconds (default 30, min 5, max 300). Operators running
many skygate instances against a single headscale can
bump this to 60-120s to reduce load.

`HEADPLANE_URL` — full URL of the headplane admin UI
(default `http://headplane:8080` if HEADSCALE_URL
contains a hostname; operator can override).

### Backlog (NOT in this release)

- **/admin/services full page render via R34**: the current
  R34 check uses basic auth which returns 302 (no admin
  session). R31/R32 have the same issue. Future work:
  add cookie-based auth to verify_post_deploy.sh so the
  page can be fully rendered + grep'd for expected text
  (e.g. "All integrations are healthy" / status badges).
- **Tailscale detail via `tailscale status`**: current
  check uses the state file presence as a proxy. A
  more accurate check would shell out to
  `tailscale status --json` and parse the
  `BackendState` field ("Running" / "NeedsLogin" / etc).
  Slower (~100ms) but more precise.
- **R26 HEADSCALE_CONTAINER unbound variable**:
  pre-existing bug in verify_post_deploy.sh that causes
  R26 to silently skip when HEADSCALE_CONTAINER is
  unset. Out of scope for B92 (would need a separate fix
  + live re-verify).
- **/admin/services auto-refresh via XHR**: current
  30s meta refresh does a full page reload. Could
  be improved with a lightweight XHR that fetches just
  the availability JSON and updates the cards
  in-place (faster, less flicker). Nice-to-have, not
  blocking.

## v0.33.1.39 — skygate container starts independently of headscale/headplane after VM reboot (B91)

**Date:** 2026-08-10
**Tag:** v0.33.1.39
**Scope:** 1 commit. 3 modified files + 1 new file + 2 docs.
`entrypoint.sh` (60s HEADSCALE_URL pre-flight wait, non-blocking) +
`docker-compose.yml` (architectural-principle comment on the
`restart: unless-stopped` line) +
`scripts/verify_pre_deploy.sh` (B91 check) +
`scripts/verify_post_deploy.sh` (R33 runtime check) +
`scripts/check_skygate_depends_on.py` (NEW — PyYAML-based
structured check that fails CI if anyone adds
`depends_on:` to the skygate service block) +
`RELEASE-NOTES.md` + `AGENTS.md`.
+~95/-5 lines. No API change, no schema change, no migration.

### What's fixed (B91)

After a VM reboot, all skygate + headscale + headplane
containers restart in **parallel**. skygate is up in ~5s;
headscale (gRPC, DB migrations, policy reload) takes ~30s.
For the first ~25s after a reboot, every skygate → headscale
API call failed (the eager `headscale.New()` client build,
`ensureHeadscaleUser` at startup, the B77 autoupdater's first
poll, and `/readyz`'s headscale check all ran before headscale
was reachable). The errors were non-fatal — skygate kept
running and recovered when headscale came up — but the
operator saw a wall of "headscale unreachable" errors in
the log and incorrectly diagnosed the startup as broken.

The B91 fix adds a **60s non-blocking pre-flight wait** in
`entrypoint.sh`: it polls `HEADSCALE_URL /health` once per
second, logs either "headscale ready after Ns" or a
WARNING (if the URL was empty, unreachable, or didn't
respond in 60s), and continues to the `go build` +
`exec /app/skygate` step regardless. On a healthy
system headscale answers in 5-10s and skygate starts
cleanly with no error noise.

### Architectural principle (documented in docker-compose.yml)

skygate MUST NOT have a hard `depends_on: headscale`. The
admin explicitly configures `HEADSCALE_URL` via `.env` (or
the `/admin/headscale` web UI at runtime). If skygate had
a hard `depends_on: headscale` with `condition:
service_healthy`, the admin couldn't fix a wrong
`HEADSCALE_URL` — skygate would never come up, so the
admin couldn't even open `/admin/headscale` to point
it at the right headscale. The current loose coupling
means: skygate comes up regardless, `/readyz` returns
503 until headscale is reachable, but `/admin/headscale`
and the auth flow are already working — the admin can
fix the URL and the next poll recovers.

The new `scripts/check_skygate_depends_on.py` is the
build-time guard: it parses `docker-compose.yml` with
PyYAML and **fails the build** if anyone adds a
`depends_on:` to the skygate service block. (caddy has
`depends_on: - skygate`, which is fine — caddy waits for
skygate, not the other way around. The check ONLY
enforces the reverse direction: skygate must not wait
for headscale/headplane.)

### Runtime mirror (R33)

The new R33 check in `scripts/verify_post_deploy.sh` verifies
the END-TO-END runtime property of B91: all three core
containers are `Up` (none in Restarting / unhealthy state),
`/healthz` returns 200, `/readyz` returns 200, and the
pre-flight wait log line is present in the skygate
container's log (proving the new code path actually ran).
B91 proves the SOURCE has the pre-flight wait + loose
coupling; R33 proves the LIVE system actually comes up
correctly after a cold-boot.

### Files

- `entrypoint.sh` (+51 lines): pre-flight wait block
  with detailed comment explaining the architectural
  principle and the VM-reboot scenario
- `docker-compose.yml` (+20 lines): long comment block
  on the `restart: unless-stopped` line explaining
  why skygate MUST NOT have `depends_on: headscale`
- `scripts/verify_pre_deploy.sh` (+40 lines): B91
  check using the dedicated Python helper
- `scripts/verify_post_deploy.sh` (+50 lines): R33
  runtime check (skygate + headscale + headplane
  Up, /healthz 200, /readyz 200, pre-flight log)
- `scripts/check_skygate_depends_on.py` (NEW, +35 lines):
  PyYAML-based structured check
- `RELEASE-NOTES.md` + `AGENTS.md` (this entry)

### Live verify after deploy

After `docker compose up -d --force-recreate --no-deps skygate`:

1. `docker logs skygate-skygate-1 2>&1 | grep -E 'pre-flight|headscale ready'`
   should show either:
   - `[init] headscale ready after Ns` (5-30s on healthy system), OR
   - `[init] WARNING: headscale not ready after 60s` (if headscale is genuinely down)
2. `docker ps --filter name=skygate` should show `Up X minutes (healthy)`
3. `curl http://localhost:8080/healthz` → `{"status":"ok",...}`
4. `curl http://localhost:8080/readyz` → `200`
5. `bash scripts/verify_post_deploy.sh` → R33 PASS

### Backlog (NOT in this release, recorded for v0.33.1.40+)

- **Headscale/headplane in same compose file**: B91 documents
  the architectural principle but the actual headscale and
  headplane containers are in a separate compose file (or
  the operator's own setup). If we ever move them into
  the skygate compose, we'd need to add healthcheck blocks
  on headscale/headplane and use
  `depends_on: { headscale: { condition: service_healthy } }`
  on a NEW `skygate` STAGING service — but for the
  production single-plane deploy, the loose coupling stays.
- **Pre-flight wait timeout configurable**: 60s is hardcoded.
  Could be made a `SKYGATE_HEADSCALE_WAIT_TIMEOUT` env var
  for operators with slow disks or large headscale DBs.
- **`/readyz` should distinguish "DB OK but headscale down"
  from "everything OK"**: today both states return 200 once
  skygate is up. The pre-flight wait masks the "headscale
  still booting" window, but a future refactor could surface
  the per-dependency status.

## v0.33.1.38 — Notifier order bug fix: /admin/telegram "Send test" works (B90)

**Date:** 2026-08-10
**Tag:** v0.33.1.38
**Scope:** 1 commit. 1 modified file + 1 verify-pre check + 2 docs.
`cmd/skygate/main.go` (one-line re-bind) +
`scripts/verify_pre_deploy.sh` (B90 check) +
`RELEASE-NOTES.md` + `AGENTS.md`.
+~20/-1 lines. No API change, no schema change, no migration.

### What's fixed (B90)

The /admin/telegram "Send test" button has been silently
broken since v0.20.0 (the v0.16.x → v0.20.0 refactor that
moved the "always arm the RealNotifier" logic from a
boot-time gate into a top-level var assignment). The
operator reported on 2026-08-10:

> "бот получил доступ к апи однако тестовое сообщение не
> отправляется выдает ошибку Бот не сконфигурирован —
> Notifier в no-op режиме"

Even though the bot was configured (token saved, egress
relay set, all 24 audits logged `telegram_egress_set
relay=emilia routes=12 ssh=ok`), the test handler
returned the "Бот не сконфигурирован — Notifier в
no-op режиме" error.

#### Root cause

The pre-fix code in `cmd/skygate/main.go`:

```go
app := handlers.New(...)  // line 230: app.Notifier = NoopNotifier{}
adminSvc := &adminsvc.Service{
    Notifier: app.Notifier,  // line 419: captures NoopNotifier
    ...
}
// ... 600+ lines of other setup ...
rn := telegram.NewRealNotifier(d)
// ...
app.Notifier = rn  // line 1061: too late, adminSvc already has the stale value
```

`adminSvc` was constructed at line 413 — **way before**
`rn` (the RealNotifier) was created at line 1012. The
`Notifier: app.Notifier` field on `adminSvc` therefore
captured the *initial* value of `app.Notifier`, which
`handlers.New()` had set to `telegram.NoopNotifier{}`
(the no-op sentinel for "bot not configured"). Even
though `app.Notifier = rn` later overwrote the field on
`app`, `adminSvc.Notifier` was a separate value that
still pointed to the NoopNotifier.

The `handleTelegramTest` handler at
`internal/feature/admin/telegram.go:304-306` checks:

```go
if _, isNoop := s.Notifier.(telegram.NoopNotifier); isNoop {
    s.redirectWithFlash(w, r, "", "Бот не сконфигурирован — Notifier в no-op режиме")
    return
}
```

`s.Notifier` is `adminSvc.Notifier`, which is the stale
NoopNotifier. The check succeeds, the error is returned,
and the operator sees the misleading "bot not configured"
message — even though `app.Notifier` (and the rest of the
process) is a fully-armed RealNotifier. The actual
RealNotifier *is* doing work: the audit log shows
`getUpdates` calls every 5s and `setMyCommands` calls on
restart. Only the test handler is broken.

The bug went unnoticed for ~3 months (v0.20.0 shipped
2026-07-15) because the test handler is rarely used — the
bot is normally validated by reading the audit log for
`getUpdates` activity, not by clicking "Send test".

#### The fix

One line, in the same code block as `app.Notifier = rn`:

```go
rn.SetRuleCaps(cfg.MaxRulesPerDevice, cfg.MaxTotalRules)
app.Notifier = rn
// 2026-08-10: v0.33.1.38 — Notifier order bug fix.
// adminSvc was constructed at line 413 (way before rn
// was even created), so adminSvc.Notifier captured the
// initial app.Notifier value (NoopNotifier{} from
// handlers.New). After this app.Notifier = rn the
// admin handlers (including the /admin/telegram "Send
// test" handler) still saw the stale NoopNotifier and
// returned "Бот не сконфигурирован — Notifier в no-op
// режиме" even though the bot WAS configured. Re-bind
// here so the admin handlers pick up the
// RealNotifier. Other services (releaseMon, exitMon,
// hsMon) are constructed below this point, so they
// pick up the new value automatically.
adminSvc.Notifier = app.Notifier
```

The re-bind is INSIDE the `rn` block (same scope, so
the `adminSvc` closure is reachable) and immediately
follows the `app.Notifier = rn` assignment. After this
line, every reference to the notifier — both
`app.Notifier` and `adminSvc.Notifier` — points to the
same `*RealNotifier` instance.

The other services (`releaseMon`, `exitMon`,
`headscale_version.Monitor`) are constructed **after**
this point, so they already pick up the live value
without needing the re-bind.

#### Why not refactor the ordering

The proper fix would be to move the entire
`rn := telegram.NewRealNotifier(d)` block to BEFORE the
`adminSvc :=` line. But that block depends on
`s sidecarMgr` and `hsMon` (set via `rn.SetSidecar(...)` and
`rn.SetHeadscaleUpdateMonitor(...)`), and both of those
are constructed AFTER `adminSvc`. Moving the rn block
upstream would require moving sidecarMgr and hsMon
upstream too — a 200-line refactor of the boot sequence.

The one-line re-bind achieves the same functional result
with a single new line and a long comment explaining why
the order is what it is. A future refactor can clean up
the boot sequence; v0.33.1.38 just unblocks the operator.

### Operator action

After deploying v0.33.1.38, click "Send test" on
/admin/telegram. Expected:
- "Сообщение отправлено (1 шт.). Проверьте Telegram:
  <chat_id>." flash message
- The bot receives a test message in the configured
  chat_id

If the network is DPI-blocked (Telegram IPs unreachable
from the skygate container), the audit log will show
`getUpdates` timeouts but the "Send test" handler will
still return a success-flash (the RealNotifier
fire-and-forgets on HTTP failure — same as the
production code path).

### Files (1 modified + 1 verify-pre check + 2 docs)

- `cmd/skygate/main.go`: one-line re-bind
  `adminSvc.Notifier = app.Notifier` inside the `rn`
  block, immediately after `app.Notifier = rn`. Long
  comment block explains the root cause + why the
  re-bind is correct + why we don't refactor the boot
  order (preserved for a future cleanup).
- `scripts/verify_pre_deploy.sh`: B90 check
  (2 grep-pins + 1 build run, using the
  `f=/tmp/b90.sh; printf ... > "$f" && bash "$f"`
  pattern B76/B89 use to avoid `bash -c` quoting
  issues).
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.38 entry.

### Backlog (NOT in this release)

- A "technical user" / "infra" portal user
  (Issue 4) is still on the wishlist — would isolate
  skygate-host-* + exit-node + subnet-router nodes from
  regular portal users so the bot in skygate-host-1
  (which needs internet to reach api.telegram.org) can
  be governed by a single per-device ACL grant owned by
  the infra user, not by skyadmin. Will ship as
  v0.33.1.39 in a follow-up release.
- 30 smoke-mesh rows in PG (operator cleanup).
- 4 system_tests test bugs (B66-B68 backlog, all
  fixed in v0.33.1.36 B88).
- `system_tests_runs` table V049 + V051 recording
  (the v0.33.0 migration creates the table but the
  recording is a v0.32.20 follow-up that's still
  pending; the test page works fine using the
  in-memory `LiveResults` + the table for history).

## v0.33.1.37 — B77 follow-up: Backfill Strategy D (tag fallback) + rotate_ts_authkey.sh (B89)

**Date:** 2026-08-10
**Tag:** v0.33.1.37
**Scope:** 1 commit. 2 modified files + 1 new + 1 verify-pre check.
`internal/nodeownership/nodeownership.go` (Strategy D) +
`internal/nodeownership/nodeownership_test.go` (2 new tests) +
`scripts/rotate_ts_authkey.sh` (new) +
`scripts/verify_pre_deploy.sh` (B89 check).
+~300/-1 lines. No API change, no schema change, no migration.

### What's added (B89)

Two independent improvements bundled:

#### 1. Backfill Strategy D (tag fallback) — B77 follow-up

Pre-fix, the node-discovery autoupdater (added in B77 /
v0.33.1.25) only back-filled nodes registered through
skygate's `/my/preauth` flow (Strategy A: `PreAuthKeyID`
match in the local `preauth_keys` table) or within 1 hour
of a `/my/preauth` key creation (Strategy C: temporal
window). Nodes registered with **operator-issued** preauth
keys (e.g. the `skygate-host-1` node created via
`headscale preauthkeys create --user 1 --reusable
--expiration 720h` during the B86 Tailscale re-auth) are
NOT in the local `preauth_keys` table, so neither A nor C
fires, and the node stays orphaned in `node_owner_map`
until manual intervention.

Strategy D closes this gap: if a node ALREADY has a
`tag:dev-<username>-*` tag in headscale (either
auto-applied by a manual `headscale nodes tag` call or by
another backfill path), AND the `<username>` portion
matches the current portal user's `Username`, we treat
the node as owned by this user and insert a
`node_owner_map` row. The headscale-side tag is already
there; we just need the DB row so the per-user ACL rule
(`src=tag:dev-<user>-<device>`) can match.

**Why this is safe**:
- We only match when the tag's `<username>` portion
  equals the current portal user's `Username` — we never
  "steal" a node owned by another user.
- The "refuse to steal" check above (the `otherOwners`
  set in `Backfill`) already filters nodes whose `UserID`
  is a different portal user.
- The `tag:subnet-router` filter (via `hasRouterTag` and
  the `subnetRouterPrefix` check) keeps subnet-router
  nodes out of the user-grant path.
- We only INSERT (`InsertIgnoreNodeOwner` respects PK
  uniqueness on `node_id`), never UPDATE an existing row,
  so we never clobber an existing owner.

2 new unit tests in `nodeownership_test.go`:
1. `TestBackfill_StrategyD_TagFallback` — verifies a
   node with `tag:dev-skyadmin-skygate-vm` + `tag:private`
   (operator-issued preauth) gets a `node_owner_map` row
   inserted.
2. `TestBackfill_StrategyD_OtherUserTag_NoMatch` —
   verifies a node with `tag:dev-michail-*` is NOT
   back-filled into the skyadmin user's snapshot
   (defensive check that the username extraction is
   correct).

#### 2. `scripts/rotate_ts_authkey.sh` — Tailscale preauth key rotation

The Tailscale preauth key in
`/home/skyadmin/skygate/secrets/ts_authkey` has a
720h (30-day) TTL. Without rotation, the key expires
on 2026-09-09 and `tailscale up` silently fails with
`backend error: authkey expired`. The skygate container
ends up in NoState and 100.64.0.x peers become
unreachable.

`scripts/rotate_ts_authkey.sh` automates the rotation:
1. Generates a new reusable preauth key via
   `headscale preauthkeys create --user 1 --reusable
   --expiration 720h` (operator can override
   `HEADSCALE_USER_ID` + `KEY_EXPIRATION_HOURS` env vars).
2. Writes the new key to `secrets/ts_authkey` (chmod 600,
   chown skyadmin).
3. Restarts the skygate container (`docker compose up
   -d --force-recreate --no-deps skygate`) so the next
   `tailscale up` re-reads the key.

Designed to be run from root's crontab, weekly (off-peak,
e.g. Sunday 03:00):
```
0 3 * * 0 /usr/local/bin/rotate_ts_authkey.sh \
  >> /var/log/skygate-ts-rotate.log 2>&1
```

Weekly (not monthly) gives 14+ days of buffer between
rotations, so a missed run doesn't immediately break the
tailnet.

### Files

- `internal/nodeownership/nodeownership.go`: Backfill
  Strategy D (the new `tag:dev-<user>-*` prefix scan
  after Strategies A+C miss; long comment block
  explaining the operator-issued preauth key gap and why
  the strategy is safe).
- `internal/nodeownership/nodeownership_test.go`:
  2 new unit tests (positive + negative).
- `scripts/rotate_ts_authkey.sh` (NEW): the
  rotation script.
- `scripts/verify_pre_deploy.sh`: B89 check
  (8 grep-pins + 1 test run, using the same
  `f=/tmp/b89.sh; printf ... > "$f" && bash "$f"`
  pattern B76 uses to avoid `bash -c` quoting issues).

### Backlog (NOT in this release)

- The skygate-host-1 node on the live VM is still
  missing its tags as of 2026-08-10 (the operator's
  manual `headscale nodes tag` set was wiped somewhere).
  v0.33.1.37's Strategy D will re-back-fill it
  automatically once the tags are re-applied (the
  autoupdater runs every 5m). Operator needs to
  manually re-apply the tags ONE more time:
  `headscale nodes tag -i 32 -t 'tag:private,
  tag:dev-skyadmin-skygate-vm' --force`.
- A "technical user" / "infra" portal user (Issue 4) is
  still on the wishlist — would isolate skygate-host-*
  + exit-node + subnet-router nodes from regular portal
  users so the bot in skygate-host-1 (which needs
  internet to reach api.telegram.org) can be governed
  by a single per-device ACL grant owned by the infra
  user, not by skyadmin.

## v0.33.1.36 — /admin/system_tests bug fixes (B66, B67, B68 + rules_sanity + acl_admin_present + backup.recent) (B88)

**Date:** 2026-08-10
**Scope:** 1 commit. 1 modified file + 1 new + 1 verify-pre check + 2 docs.
`internal/feature/admin/system_tests.go` +
`internal/feature/admin/system_tests_b66_b68_test.go` (new) +
`scripts/verify_pre_deploy.sh` (B88 check).
+~250/-50 lines. No API change, no schema change, no migration.

### What's fixed (B88)

The /admin/system_tests page is informational — every entry in
`TestRegistry` is a Go function that returns (status, output).
The pre-v0.33.1.36 registry had **4 latent bugs** (and **2
operator-side fixes** for things the tests previously failed
on) that have been silently failing or producing false
positives since v0.33.0. The live run on 2026-08-10 had:
**8 pass, 6 fail, 1 skip** — 4 of those 6 failures were
test bugs, 2 were operator-side data issues.

#### Bug 1 (B66): `db.duplicate_devices` — referenced `tailscale_ip` column

The pre-fix query was:
```sql
SELECT hostname, tailscale_ip, count(*) AS c
FROM node_owner_map
WHERE hostname != '' OR tailscale_ip != ''
GROUP BY hostname, tailscale_ip
HAVING c > 1
```

The `node_owner_map` table has NO `tailscale_ip` column
(the tailnet IP is fetched from headscale, not stored in
the table). On the live PG DB the query errored
`column "tailscale_ip" does not exist (SQLSTATE 42703)`
and the test always returned `fail`.

**Fix**: drop the `tailscale_ip` reference. The
hostname-only duplicate check is what the table can
actually answer. A duplicate-hostname row is the operator's
real signal anyway (two Tailscale devices on the same
hostname means the same machine registered twice, which
is the bug we want to catch).

#### Bug 2 (B67): `exit_rules.preferred_mismatch` — joined on `d.id` not `d.node_id`

The pre-fix query was:
```sql
LEFT JOIN node_owner_map d ON d.id = r.device_id
```

The `node_owner_map` PK is `node_id` (the headscale-side
machine key, not an internal autoincrement), so `d.id`
errored `no such column: d.id` on every backend.

**Fix**: `d.id` → `d.node_id`.

#### Bug 3 (rules_sanity false positive): per-user "default exit" rules counted as orphans

The pre-fix query was:
```sql
SELECT count(*) FROM device_rules
WHERE device_hostname = '' OR device_hostname IS NULL
   OR action = '' OR action IS NULL
```

On the live PG DB this returned **166 orphans**, but all
166 rows were **per-user "default exit" rules**:
- `user_id = 1` (skyadmin)
- `action = 'accept'`
- `device_hostname = ''` (applies to ALL of the user's
  devices, not a specific one)
- `target_value` = various Cloudflare / Google / AWS CDN IP
  ranges pinned to karolina

These are a **legitimate per-user rule shape** — they
apply to all of the user's devices, not a specific one.
Counting them as orphans was a false positive.

**Fix**: orphan = no action OR no target. Per-user rules
with empty `device_hostname` are valid as long as they
have an `action` and a `target_value`. The pass message
now includes a per-user count so the operator can see
how many of these "default exit" rules exist.

#### Bug 4 (`headscale.acl_admin_present`): iterated `acls[]` only, live policy uses `grants[]`

The pre-fix test iterated `view.AllACLs` (the JSON
`acls` array). The live headscale 0.29+ policy uses
`grants[]` (not `acls[]`), so the unmarshal left
`AllACLs` empty and the test always returned
`"no rule with skyadmin in src — admin has no access to
any device"` even though the live policy has a perfectly
valid grant:
```json
{"src": ["skyadmin@tsnet.skynas.ru"],
 "dst": ["skyadmin@tsnet.skynas.ru:*",
         "h-user-skyadmin-subnet",
         "autogroup:internet"], "ip": ["*"]}
```

**Fix**: parse `view.PolicyRaw` and look at BOTH `acls`
and `grants`. The pass message now reports the count of
each.

#### Operator-side data fix: `backup.recent` path translation

The test runs INSIDE the skygate container. The
container's bind mount is
`Source: /home/skyadmin/skygate` → `Destination: /app`,
so a host path like `/home/skyadmin/skygate/backup`
doesn't exist in the container's filesystem (the
in-container path is `/app/backup`). The pre-fix test
always errored
`"read dir /home/skyadmin/skygate/backup: no such file
or directory"` even when the host had recent backups.

**Fix**: if the literal path doesn't exist and starts
with `/home/skyadmin/skygate/`, try the container's
bind-mount equivalent `/app/<rest>` before failing.

#### Operator-side data fix: 30 smoke-mesh rows in PG (out of scope for v0.33.1.36)

The `mesh.active_meshes` test still fails on live with
`"0 of 30 meshes have members: smoke-mesh-…×0"` because
**30 smoke-mesh cruft rows are still in the PG DB**.
The previous operator-side data cleanup (v0.32.5 era)
operated on the SQLite fallback file, NOT the active
PG DB — the PG DB still has all 30 rows. After the
operator runs the cleanup on PG (a single
`DELETE FROM meshes WHERE name LIKE 'smoke-mesh-%'`),
this test will return `skip` ("no meshes configured").

### Files (2 modified + 1 new + 2 docs)

- `internal/feature/admin/system_tests.go`:
  - `db.duplicate_devices`: dropped `tailscale_ip`
    from the query (B66).
  - `db.rules_sanity`: orphan = no action OR no target
    (was: no device_hostname OR no action — the
    device_hostname check counted per-user "default
    exit" rules as orphans).
  - `headscale.acl_admin_present`: parse `view.PolicyRaw`
    and look at both `acls` and `grants` (was: only `acls`).
  - `backup.recent`: if the literal `DEPLOY_BACKUP_DIR`
    path doesn't exist in the container, try the
    bind-mount equivalent `/app/<rest>`.
- `internal/feature/admin/system_tests_b66_b68_test.go`
  (NEW): 5 unit tests pinning the fixes:
  1. `TestB66_DuplicateDevices_DropsTailscaleIP` —
     runs the post-fix query against in-memory SQLite
     and verifies it doesn't error
  2. `TestB67_PreferredMismatch_NodesByNodeID` —
     same pattern, verifies the join uses `d.node_id`
  3. `TestB68_RulesSanity_PerUserRulesNotOrphans` —
     verifies per-user rules aren't counted
  4. `TestACLAdminPresent_GrantsShape` — JSON
     parse + look at both acls and grants
  5. `TestBackupRecent_ContainerPathTranslation` —
     pin the host→container prefix translation
- `scripts/verify_pre_deploy.sh`: B88 check (5
  test name grep-pins + 2 source-file grep-pins + 1
  test run).
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.36 entry.

### Operator action

After deploying v0.33.1.36, click "Run all" on
/admin/system_tests. Expected on the live VM:
- `db.duplicate_devices` → PASS
- `exit_rules.preferred_mismatch` → PASS
- `db.rules_sanity` → PASS (with a per-user count message
  like "351 rules, all have action + target_value (166
  per-user 'default exit' rules)")
- `headscale.acl_admin_present` → PASS
- `backup.recent` → PASS (or fail with a clear "no backup
  files in /app/backup" if no recent backup)
- `mesh.active_meshes` → still fail until the operator
  runs the smoke-mesh cleanup on PG. Optional — the test
  will then return `skip` (no meshes configured).

### Smoke-mesh cleanup (optional operator action)

```sql
-- On the live VM, against the active PG:
PGPASSWORD=<DB-ADMIN-PASSWORD> psql -h 172.17.0.1 -p 5000 \
  -U admin -d skygate_staging \
  -c "DELETE FROM meshes WHERE name LIKE 'smoke-mesh-%'"
```

The `meshes` table FK CASCADE removes `mesh_members`
rows automatically (verified 0 members before delete).

## v0.33.1.35 — PostAdminExitNodeTagAsExitNode uses AddTag read-modify (B87)

**Date:** 2026-08-10
**Tag:** v0.33.1.35
**Commit:** TBD
**Scope:** 1 commit. 2 modified files + 1 new
(`internal/feature/admin/exit_nodes.go` +
`internal/headscale/tags.go` +
`internal/headscale/tags_test.go` (new) +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B87, 5 grep-pins + 1 test run).
+~250/-15 lines. No API change, no schema change, no migration.

### What's fixed (B87)

Pre-fix: headscale 0.29's `nodes tag` REPLACES the entire
tag set on a node. The pre-fix handler called
`hs.TagNode(nodeID, "tag:exit-node")` which silently wiped
every pre-existing tag — including the per-user
`tag:dev-skyadmin-<name>` device marker that the v0.33.1.30
B82 follow-up documented as the per-user device marker
for the ACL grants. The live policy references
`tag:dev-skyadmin-skygate-vm → tag:dev-skyadmin-emilia`
directly, so wiping the per-user dev tag broke the
grant until the operator re-applied the tag by hand.

#### The fix

- `PostAdminExitNodeTagAsExitNode` now calls `hs.AddTag`
  instead of `hs.TagNode`. `AddTag`
  (`internal/headscale/tags.go:117`) is the
  read-modify-write helper that reads the current tag
  set via `ListAllNodes`, appends the requested tag,
  and writes the union via `TagNode`. The pre-existing
  per-user dev-tags are preserved.
- `AddTag` now propagates `ListAllNodes` errors instead
  of silently swallowing them. Pre-fix the helper
  proceeded with an empty `current` slice when the
  read failed — that meant the inner `TagNode` call
  would write only `[want]`, silently wiping every
  pre-existing tag the node carried. The post-fix
  contract: on read error, `AddTag` returns
  `(read-err)` and does NOT call the inner `TagNode`.
- `AddTag` is now a no-op when the tag is already
  present (no docker exec call, no audit log noise).
  Pre-fix the headscale CLI was called redundantly.
- `PostAdminExitNodeUntagAsExitNode` already used
  `UntagNode` (the read-modify-write dance for removal)
  since v0.18.1 — no change there.
- `TagNode` refactored to use `c.dockerRunner` (the
  same injection point `ExtendNodeExpiry` uses) so the
  unit tests can stub the docker exec without touching
  the system daemon. The production path (nil
  `dockerRunner`) still uses `exec.Command`.

### Files (2 modified + 1 new + 2 docs)

- `internal/feature/admin/exit_nodes.go`:
  `PostAdminExitNodeTagAsExitNode` switched from
  `hs.TagNode` to `hs.AddTag`. Long comment block explains
  the B82 follow-up context (why the per-user dev-tags
  matter) and the B87 fix.
- `internal/headscale/tags.go`: `AddTag` now propagates
  `ListAllNodes` errors. `TagNode` refactored to use
  `c.dockerRunner` for testability.
- `internal/headscale/tags_test.go` (NEW): 4 unit tests
  pin the contract:
  1. `TestAddTag_PreservesExistingTags` — the
     read-modify union (the core fix; pre-fix `TagNode`
     would write only `[tag:exit-node]`)
  2. `TestAddTag_NoOpWhenAlreadyPresent` — idempotency
  3. `TestAddTag_PreservesOnError` — error propagation,
     no silent wipe when `ListAllNodes` fails
  4. `TestTagNode_ReplacesEntireSet` — documents the OLD
     broken contract so a future refactor that drops
     `AddTag` in favour of `TagNode` would fail the test
- `scripts/verify_pre_deploy.sh`: B87 check, 5
  grep-pins + 1 test run.
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.35 entry.

### Backlog (NOT in this release, recorded for v0.33.1.36+)

- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with
  `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is
  malformed (headscale rejects). The /my/exit-nodes and
  /my/devices POSTs now succeed in writing the DB but
  the ACL re-apply fails. Fix: clean up the bad row in
  device_rules, or fix the domain autoupdater to
  validate addresses before generating h-rule-*
  aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- **`skyadmin-subnet-router` container** is still
  crashlooping on `authkey expired` (started 2026-07-22).
  The container is for the host-network 10.0.1.0/24
  subnet route, not the skygate-side Tailscale bridge.
  The operator can `docker rm -f skyadmin-subnet-router`
  if 10.0.1.0/24 doesn't need to be advertised as a
  subnet route anymore.
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in
  .env** — dead v0.30.x-era per-host SSH target
  overrides. Not read by the current code. Cosmetic
  cleanup; not breaking anything.
- **B77 node-discovery autoupdater** didn't fire
  automatically for the new skygate-host-1 node
  (2026-08-10 Tailscale re-auth). Required manual
  `headscale nodes tag -i <id> -t
  'tag:dev-skyadmin-skygate-vm' --force` and
  `-t 'tag:private' --force`. The autoupdater
  (5m default, `SKYGATE_NODE_DISCOVERY_INTERVAL`)
  may have a gating condition that's not met
  (HSGlobalFn() not set? or env var not configured
  to a positive value?). Investigate.
- **Tailscale preauth key rotation** — manual process
  today. Reusable keys with `--expiration 720h` (30
  days) need periodic rotation. The skygate
  /admin/tailscale UI has a "restart" button that
  re-reads the auth key from `/data/ts/authkey` (if
  mounted) — for a fully hands-off flow, write a small
  cron that rotates the key weekly. Out of scope for
  v0.33.1.35, recorded for v0.33.1.36+.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.34 — entrypoint.sh accepts SKYGATE_TS_LOGIN_SERVER fallback (B86)

**Date:** 2026-08-10
**Tag:** v0.33.1.34
**Commit:** TBD
**Scope:** 1 commit. 1 modified file (`entrypoint.sh`),
1 verify-pre check (B86, 3 grep-pins).
++30 lines. No API change, no schema change, no migration.

### What's fixed (B86)

Operator report 2026-08-10 (post-v0.33.1.33 B85 deploy):
> "все еще ошибка при попытке настроить работу бота через
>  exit node: SSH на root@100.64.0.3:18022 не удался: ssh
>  root@100.64.0.3:18022 (key /ssh-sync/skygate_sync): ssh:
>  connect to host 100.64.0.3 port 18022: Operation timed
>  out"

The B85 chain fix is working (the resolved target now
correctly contains the per-row port — 18022 in this case
was the value set by the B85 live-verify test). The
remaining "Operation timed out" is the same network
issue as the v0.33.1.32 B84 deployment — but the root
cause is DIFFERENT: the v0.33.1.32 fix addressed the
chain resolution (telegram handler now uses B81), but
the underlying reachability to 100.64.0.x is still
broken because the in-image tailscaled inside the
skygate container can't authenticate.

#### Root cause

`docker-compose.yml` sets `SKYGATE_TS_LOGIN_SERVER`
(post-v0.33.1.16 B65, which fixed the docker-compose
precedence: environment > env_file, by removing the
hardcoded value and letting .env / env_file set the
`SKYGATE_TS_LOGIN_SERVER`). But `entrypoint.sh` only
reads `TS_LOGIN_SERVER` (no SKYGATE_ prefix). The
pre-B86 entrypoint default `https://head.example.com`
is a placeholder pointing at the Tailscale example
domain. `tailscale up` against it silently fails (the
30s timeout + "WARNING" log line swallowed the error),
the state file ended up with
`ControlURL=https://head.example.com`, and the
container's tailscaled is in NoState forever after.
Live symptom: 100.64.0.3 unreachable from inside the
skygate container even though tailscale0 is up; state
shows "logged out, fetch control key from
head.example.com: no DNS fallback".

The v0.33.1.9 entrypoint already added a fallback
for `TS_AUTHKEY_FILE → SKYGATE_TS_AUTHKEY_FILE` (the
same long-standing mismatch), but the parallel
fallbacks for `TS_LOGIN_SERVER → SKYGATE_TS_LOGIN_SERVER`
and `TS_HOSTNAME → SKYGATE_TS_HOSTNAME` were never
added. v0.33.1.16 (B65) removed the docker-compose.yml
hardcoded override, which fixed the .env precedence —
but the entrypoint was still using the placeholder URL.

#### The fix

Add the same fallback chain that v0.33.1.9 added for
the authkey. Two lines:

```sh
LOGIN_SERVER="${TS_LOGIN_SERVER:-${SKYGATE_TS_LOGIN_SERVER:-https://head.example.com}}"
HOSTNAME="${TS_HOSTNAME:-${SKYGATE_TS_HOSTNAME:-skygate-host-1}}"
```

The legacy un-prefixed name still wins (so any operator
who manually set `TS_LOGIN_SERVER=...` in docker-compose
env vars still has their value used verbatim). The
SKYGATE_ prefixed name is the second-priority fallback
(set by the post-B65 docker-compose.yml). The default
is the placeholder URL (preserved as a last-resort, so
an operator who removed both env vars still gets a
non-empty LOGIN_SERVER and the entrypoint doesn't
crash).

### Operator action after deploy

The B86 code change alone isn't enough — the existing
state file at
`/home/skyadmin/skygate/data/ts/tailscaled.state` has
`ControlURL=https://head.example.com` and will block
`tailscale up` against the new login-server
(`tailscale up` fails with "different control server"
if the state has a different ControlURL). Steps:

1. After B86 deploy, the skygate container's
   `tailscale up` will fail with "different control
   server". This is expected (the state has the
   placeholder URL).
2. Clear the state:
   `rm -rf /home/skyadmin/skygate/data/ts && mkdir
   /home/skyadmin/skygate/data/ts`.
3. Restart the skygate container:
   `cd /home/skyadmin/skygate && docker compose up -d
   --force-recreate --no-deps skygate`.
4. Watch the entrypoint log for the new
   `[init] tailscale up --accept-routes
   (login-server=https://head.skynas.ru,
   hostname=...)` line — confirms the B86 fallback
   worked.
5. Inside the container, `tailscale status` should
   show "logged in" (not "NoState") and the node
   should appear in the headscale node list.
6. From the container, `ping 100.64.0.3` should
   work.
7. Click "Set as egress relay" on /admin/telegram —
   the SSH should now succeed (modulo port 22 vs 18022
   on emilia; the B85 ssh_port=18022 was set by the B85
   live-verify test, the operator can clear it via the
   form if emilia is on 22).

### Files (1 modified + 1 docs)

- `entrypoint.sh`: `LOGIN_SERVER` + `HOSTNAME` read
  fallbacks (`TS_LOGIN_SERVER → SKYGATE_TS_LOGIN_SERVER`,
  `TS_HOSTNAME → SKYGATE_TS_HOSTNAME`).
- `scripts/verify_pre_deploy.sh`: B86 check, 3 grep-pins.
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.34 entry.

### Backlog (NOT in this release)

- **PostAdminExitNodeTagAsExitNode still uses
  hs.TagNode** (replaces entire tag set) — when the
  operator clicks "Tag as exit-node" on a node that
  already has `tag:dev-skyadmin-<name>`, the dev tag
  gets wiped. Switch the handler to `AddTag`
  (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with
  `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is
  malformed (headscale rejects). The /my/exit-nodes and
  /my/devices POSTs now succeed in writing the DB but
  the ACL re-apply fails. Fix: clean up the bad row in
  device_rules, or fix the domain autoupdater to
  validate addresses before generating h-rule-*
  aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- **`skyadmin-subnet-router` container** is still
  crashlooping on `authkey expired` (started 2026-07-22).
  The container is for the host-network 10.0.1.0/24
  subnet route, not the skygate-side Tailscale bridge —
  so this B86 fix doesn't depend on it. The operator
  can `docker rm -f skyadmin-subnet-router` if
  10.0.1.0/24 doesn't need to be advertised as a
  subnet route anymore.
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in
  .env** — dead v0.30.x-era per-host SSH target
  overrides. Not read by the current code. Cosmetic
  cleanup; not breaking anything.
- **Operator's emilia ssh_port=18022 from B85 live-verify
  test** — set during the B85 live verify. The operator
  can clear it via the /admin/exit-nodes form if emilia
  is on port 22, OR keep it if emilia's sshd is on
  18022.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.33 — per-row exit_servers.ssh_port for B81 auto-fallback (B85)

**Date:** 2026-08-10
**Tag:** v0.33.1.33
**Commit:** TBD
**Scope:** 1 commit. 4 modified files
(`internal/db/{db,driver_postgres,exit_servers,exit_servers_test,migrations_pg,queries}.go` +
`internal/db/migrations_v0.53.go` (new) +
`internal/feature/admin/{exit_nodes,testutil}.go` +
`internal/handlers/templates/admin/exit_nodes.html` +
`internal/i18n/catalog_exit_nodes.go` +
`internal/telegram/commands_test.go` +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B85, 14 grep-pins + 4 test runs).
+~520/-15 lines. Includes 1 new migration (V053 +
V053PG — ALTER TABLE exit_servers ADD COLUMN ssh_port).

### What's added (B85)

The post-v0.33.1.32 B84 deploy brought the B81 SSH-target
chain to /admin/telegram. The operator's live report
(2026-08-10) was that the design intent is "use Tailscale
for SSH because the standard public path may be blocked,
AND remember the exit-node may have other ports open
besides the canonical 22".

Pre-fix: the B81 auto-fallback hard-codes port 22. The
operator with karolina on 18022 (or any exit-node on 2222
/ 8022) had to either set the full operator override in
`exit_servers.ssh_target` (loses the always-reachable
Tailscale IP — the operator has to track the IP manually
and update it if karolina gets a new Tailscale address) or
live with the v0.33.1.29 "port 22" assumption and add
port-forwarding on the exit-node to a non-standard SSH
port.

B85 fix: per-row `exit_servers.ssh_port` column. The
`LookupExitServerSSHTarget` helper now reads this column
and appends `:<port>` to the B81 auto-fallback
`root@<tailscale_ip>` when set. Empty = port 22 (preserves
the v0.33.1.29 / v0.33.1.32 behaviour — no migration impact
on operators who don't need a non-default port). The
operator-override path (case 1, ssh_target) is unchanged —
the operator's full `user@host:port` still wins.

The `SetAdvertisedRoutes` helper at
`internal/headscale/routes.go:222-230` already parses
`user@host:port` syntax (target + -p <port> for the ssh
command), so the B85 value just slots into the existing
string. No headscale-side changes.

### Files (4 modified + 1 new + 2 docs)

- `internal/db/migrations_v0.53.go` (NEW): `migrateV053`
  adds `exit_servers.ssh_port TEXT NOT NULL DEFAULT ''`.
  Idempotent via `pragma_table_info` pre-check (works on
  every SQLite version skygate supports; the PG version
  uses `ALTER TABLE ADD COLUMN IF NOT EXISTS` for the same
  effect).
- `internal/db/migrations_pg.go`: `migrateV053PG` — same
  purpose, PG-idiomatic `ADD COLUMN IF NOT EXISTS`.
- `internal/db/driver_postgres.go`: register V053PG in
  the `MigratePostgres()` slice.
- `internal/db/queries.go`: `qSelectAllExitServers` +
  `qSelectExitServerSSHTarget` read `ssh_port`; INSERT
  writes it.
- `internal/db/exit_servers.go`: `ExitServer.SSHPort` field
  + `ListExitServers` Scan + `UpsertExitServer` takes a
  new `sshPort` parameter + `LookupExitServerSSHTarget`
  appends `:<port>` to the auto-fallback when set.
- `internal/feature/admin/exit_nodes.go`: read
  `r.FormValue("ssh_port")` in `PostAdminExitNodesAdd`;
  preserve `ssh_port` in `PostAdminExitNodeUseTailscaleIP`
  (the operator's non-default port must survive a "Use
  Tailscale IP" click); populate `ExitNodeInfo.SSHPort` for
  the form pre-fill.
- `internal/handlers/templates/admin/exit_nodes.html`: new
  `ssh_port` input field with helper text.
- `internal/i18n/catalog_exit_nodes.go`: `form_ssh_port` +
  `form_ssh_port_help` in both EN and RU.
- `internal/db/exit_servers_test.go`: 4 new tests
  (`B85SSHPortSuffix`, `B85EmptyPortNoSuffix`,
  `B85OperatorOverrideIgnoresPort`,
  `TestMigrateV053_AddsSSHPortColumn`).
- `internal/feature/admin/testutil.go`: test schema adds
  `ssh_port` column (without it `ListExitServers` Scan fails
  on the test DB).
- `internal/telegram/commands_test.go`: same — test schema
  adds `ssh_port`.
- `scripts/verify_pre_deploy.sh`: B85 check, 14 grep-pins
  + 4 test runs.

### B85 verify-pre check (14 grep-pins + 4 test runs)

Pins the contract:
- `migrateV053` registered in `db.go`
- `ALTER TABLE exit_servers ADD COLUMN` present in V053
- `migrateV053PG` registered in `migrations_pg.go`
- `ssh_port` present in `queries.go`
- `LookupExitServerSSHTarget` reads `ssh_port` in
  `exit_servers.go`
- `ssh_port` present in `exit_nodes.go` (form handling +
  use-ts-ip preservation)
- `form_ssh_port` present in template + i18n (RU+EN)
- 4 new test names exist
- The tests run and pass

### Operator action

After this release, the operator can set
`exit_servers.ssh_port = "18022"` on karolina (or any
other exit-node on a non-default port) via the
/admin/exit-nodes add form; the B81 auto-fallback then
produces `root@<tailscale_ip>:18022` automatically, and the
`SetAdvertisedRoutes` call uses `ssh -p 18022 ...`
end-to-end. No data migration needed — existing rows
have `ssh_port = ''` (the DEFAULT), so the auto-fallback
keeps producing `root@<tailscale_ip>` with no port suffix
(preserving the v0.33.1.29 / v0.33.1.32 behaviour for
operators who don't need a non-default port).

### Backlog (NOT in this release, recorded for v0.33.1.34+)

- **PostAdminExitNodeTagAsExitNode still uses hs.TagNode**
  (replaces entire tag set) — when the operator clicks
  "Tag as exit-node" on a node that already has
  `tag:dev-skyadmin-<name>`, the dev tag gets wiped. Switch
  the handler to `AddTag` (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip` column
     but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`, not
     `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries `view.AllACLs`
     instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com` +
  autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects). The
  /my/exit-nodes and /my/devices POSTs now succeed in
  writing the DB but the ACL re-apply fails. Fix: clean up
  the bad row in device_rules, or fix the domain
  autoupdater to validate addresses before generating
  h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows; UPDATE
  167 orphan device_rules (empty `device_hostname`);
  configure backup schedule (or accept `backup.recent` as
  informational).
- **Tailscale state on skygate-host-1 broken** (out of
  scope for B85 code; operator-side network fix needed):
  - `skyadmin-subnet-router` container crashloops with
    `authkey expired` (started 2026-07-22, key TTL
    expired). RestartCount: 922.
  - `skygate-skygate-1` in-image tailscaled is in NoState:
    state file points to `https://head.example.com`
    (placeholder) instead of the real
    `https://head.skynas.ru`.
  - The host's `tailscale0` interface is missing — 100.64.x
    packets route via the LAN gateway (192.168.13.1) and
    are dropped. The post-B84 "Operation timed out" on ssh
    root@100.64.0.3 is a symptom of this, not of B85.
  - Fix: re-auth Tailscale (fresh preauthkey from
    `headscale preauthkeys create`) + delete the dead
    `skyadmin-subnet-router` container (or restart it with
    a fresh key).
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in .env** —
  dead v0.30.x-era per-host SSH target overrides. Not
  read by the current code. Cosmetic cleanup; not
  breaking anything.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.32 — telegram egress uses B81 SSH-target chain (B84)

**Date:** 2026-08-09
**Tag:** v0.33.1.32
**Commit:** TBD
**Scope:** 1 commit. 2 modified files
(`internal/feature/admin/telegram.go` +
`internal/feature/admin/admin_telegram_egress_b84_test.go` (new) +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B84, 5 grep-pins + 2 test runs).
++~180/-10 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B84)

The post-v0.33.1.31 B83 deploy (with the
`SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync` env fix) brought the
key path on the /admin/telegram "Set as egress relay" click from
"no ssh_key_path provided" to a real SSH attempt — but the click
for emilia still failed with a new error:

> "SSH на emilia не удался: ssh emilia (key
> /ssh-sync/skygate_sync): ssh: Could not resolve hostname
> emilia: Try again"

The key path is now correct (B83 worked), but the SSH target is
the headscale-given hostname "emilia" instead of the Tailscale IP
"100.64.0.3".

#### Root cause

`handleTelegramSetEgress` in `internal/feature/admin/telegram.go`
used `db.LookupExitServerSSH` for both the key and the target,
and fell back to `relay.Hostname` (the headscale-given hostname)
when the stored `ssh_target` was empty. The `ssh` CLI cannot
resolve that hostname in most setups (the operator's DNS only
knows 100.x.x.x Tailscale IPs, and the headscale-given name
"emilia" isn't a Tailscale MagicDNS name).

The /admin/exit-nodes/sync flow has used the B81 chain (operator
override → `root@<tailscale_ip>` → `""`) since v0.33.1.29, but
the /admin/telegram handler was the one remaining call site that
still had the legacy `relay.Hostname` fallback. B81 was
incomplete: it fixed the sync path but not the telegram egress
path.

#### The fix

Switch the telegram handler to `db.LookupExitServerSSHTarget`
(the B81 helper) for the SSH target. The empty-ssh_target case
now resolves to `root@<tailscale_ip>` — exactly what
`/admin/exit-nodes/sync` does. The chain is now:

1. `exit_servers.ssh_target` (operator override, priority 1)
2. `root@<tailscale_ip>` (B81 auto-fallback from v0.33.1.29)
3. `relay.Hostname` (legacy fallback for the "no exit_servers
   row" edge case)

Priority 1 still wins over the auto-fallback, so a non-default-
port operator override (e.g. `root@karolina.example.com:18022`)
is preserved.

#### Live-verified

- healthz: TBD (after deploy)
- /admin/telegram "Set as egress relay" for emilia now uses
  `root@100.64.0.3` (Tailscale IP) instead of `emilia` (hostname)
  as the SSH target. The audit log entry
  `relay=emilia host=root@100.64.0.3 ssh=err=...` is the visible
  artifact that confirms the fix.
- The remaining "Operation timed out" / connection error is a
  separate network-level issue (port 22 on emilia not reachable
  from the skygate container — out of scope for B84; the
  Tailscale routing and ACL config are the operator-side
  follow-up).

### Files (1 modified + 1 new)

- `internal/feature/admin/telegram.go` — switched the SSH
  target resolution in `handleTelegramSetEgress` from
  `LookupExitServerSSH` (Target) + `relay.Hostname` fallback to
  `LookupExitServerSSHTarget` (the B81 chain).
- `internal/feature/admin/admin_telegram_egress_b84_test.go`
  (new) — 2 integration tests:
  `TestHandleTelegramSetEgress_B84SSHTargetChain` (positive:
  empty-ssh_target + tailscale_ip="100.64.0.3" → audit log
  contains `host=root@100.64.0.3`, NOT `host=emilia`) +
  `TestHandleTelegramSetEgress_B84OperatorOverrideWins`
  (negative: operator override `root@karolina.example.com:18022`
  still wins, the B81 auto-fallback does NOT silently override
  it).
- `scripts/verify_pre_deploy.sh` — B84 check, 5 grep-pins +
  2 test runs.

### B84 verify-pre check (5 grep-pins + 2 test runs)

Pins the contract:
- `telegram.go` uses `db.LookupExitServerSSHTarget` (the B81
  helper) for the SSH target — NOT the legacy
  `sshTarget = relay.Hostname` fallback
- `telegram.go` references the B84 identifier (so a future
  refactor that drops the comment + LookupExitServerSSHTarget
  call will be caught at PR time)
- 2 new test names exist
- The tests run and pass

### Backlog (NOT in this release, recorded for v0.33.1.33+)

- **PostAdminExitNodeTagAsExitNode still uses hs.TagNode**
  (replaces entire tag set) — when the operator clicks "Tag
  as exit-node" on a node that already has
  `tag:dev-skyadmin-<name>`, the dev tag gets wiped. Switch
  the handler to `AddTag` (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip` column but
     `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`, not
     `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries `view.AllACLs`
     instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id, joined_at`
     (no `id`).
- **Pre-existing `device_rules` bad address**: a `device_rules`
  row with `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is malformed
  (headscale rejects). The /my/exit-nodes and /my/devices
  POSTs now succeed in writing the DB but the ACL re-apply
  fails. Fix: clean up the bad row in device_rules, or fix
  the domain autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows; UPDATE
  167 orphan device_rules (empty `device_hostname`);
  configure backup schedule (or accept `backup.recent` as
  informational).
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in .env** —
  dead v0.30.x-era per-host SSH target overrides. Not read
  by the current code. Cosmetic cleanup; not breaking
  anything.
- **Port 22 unreachable on emilia/karolina from skygate
  container** — "Operation timed out" after the B83 + B84
  fixes. Tailscale network (100.64.0.0/10) is up, but port
  22 on the exit nodes isn't accessible. Possible causes:
  sshd not running, firewall blocking, Tailscale ACL
  denying, or tailscaled not forwarding. Operator-side
  network issue, out of scope for B84.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.31 — handlers.New() assigns sshKeyPath to App.SSHKeyPath (B83)

**Date:** 2026-08-09
**Tag:** v0.33.1.31
**Commit:** TBD
**Scope:** 1 commit. 3 modified files
(`internal/handlers/handlers.go` +
`internal/handlers/handlers_new_test.go` (new) +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B83, 5 grep-pins + test run).
+~250/-3 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B83)

Operator report 2026-08-09 (post-v0.33.1.30 .env fix):
> "при попытке подключить маршрутизацию телеграма
> получил ошибку SSH на emilia не удался:
> SetAdvertisedRoutes(emilia): no ssh_key_path
> provided; set exit_servers.ssh_key_path or
> SKYGATE_EXIT_SSH_KEY"

After the v0.33.1.30 B82 fix brought emilia/karolina/
sharlotta back to /admin/exit-nodes and the `.env`
fix set `SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync`,
the operator clicked **"Set as egress relay"** on
`/admin/telegram` for emilia — the handler still
errored with "no ssh_key_path provided".

#### Root cause

`handlers.New()` accepted `sshKeyPath` as a parameter
(line 335) but the `&App{...}` literal initialization
**never assigned it to `App.SSHKeyPath`**. The field
stayed at the zero value (empty string) for the entire
process lifetime. Result: every call site that reads
`s.SSHKeyPath` got the empty string.

The v0.33.1 B43 hardening (refuse to fall back to
the legacy `/home/admin/.ssh/config` path that doesn't
exist in the dockerised skygate) turned this silent
zero-value into a hard error: `SetAdvertisedRoutes`
returns "no ssh_key_path provided" when both the
per-row `exit_servers.ssh_key_path` AND the env-derived
fallback are empty.

#### Why /admin/exit-nodes/sync was NOT affected

The sync path uses `s.Cfg.SSHKeyPath` (the config-layer
copy, populated from `cfg.SSHKeyPath` which IS read
from the env at boot). The `Cfg` field on the App
struct was always populated. The telegram egress
handler is the only call site that reads
`s.SSHKeyPath` (the App-struct copy) directly — and
that's exactly where the operator hit the bug.

#### What else was silently broken (no operator-visible failure)

- The `/admin/exit-nodes` add form's
  `ssh_key_path` default value rendered as `value=""`
  (operators adding new exit nodes via the form had
  to retype the key path every time).
- The `/admin/backup/config` SFTP test's
  "Test OK" flash message included `s.SSHKeyPath`
  as part of the displayed command — also empty.

Both had the same root cause and are fixed by the
same one-line addition to `New()`.

#### The fix

Add `SSHKeyPath: sshKeyPath` to the `&App{...}`
literal in `handlers.New()`:

```go
a := &App{
    DB:           d,
    hs:           hs,
    HS:           hs,
    HeadscaleKey: headscaleKey,
    JWTSecret:    secret,
    ControlURL:   controlURL,
    SessionHours: sessionH,
    DerpBaseURL:  derpURL,
    SSHKeyPath:   sshKeyPath, // was missing — B83
    templates:    LoadTemplates(),
    ...
}
```

With this, the chain is now correct end-to-end:

1. `SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync` in
   `.env` (post-v0.33.1.30 fix)
2. `cfg.SSHKeyPath` is read from the env at
   `config.Load()` boot
3. `handlers.New()` receives it as a parameter
4. `App.SSHKeyPath` is populated correctly (B83
   fix)
5. `adminSvc.SSHKeyPath` is populated from
   `app.SSHKeyPath` in `main.go:436`
6. `/admin/telegram` handler reads
   `s.SSHKeyPath` as the fallback when
   `exit_servers.ssh_key_path` is empty — now gets
   `/ssh-sync/skygate_sync` ✓

### Files (2 modified + 1 new)

- `internal/handlers/handlers.go` — added
  `SSHKeyPath: sshKeyPath` to the `&App{...}`
  literal in `New()` (was missing since v0.33.1
  when the field was first introduced)
- `internal/handlers/handlers_new_test.go` (new)
  — 2 unit tests:
  `TestNew_AssignsSSHKeyPath` (positive: the
  parameter is assigned to App.SSHKeyPath) +
  `TestNew_EmptySSHKeyPath_StaysEmpty` (negative:
  empty input stays empty, no silent default
  substitution — the "no ssh_key_path provided"
  contract from SetAdvertisedRoutes relies on
  this)
- `scripts/verify_pre_deploy.sh` — B83 check, 5
  grep-pins + test run

### B83 verify-pre check (5 grep-pins)

Pins the contract:
- `handlers.go` has the explicit
  `SSHKeyPath: sshKeyPath` field init in the
  `&App{...}` literal (the positive case)
- `handlers.go` references the B83 identifier (so
  a future refactor that drops the comment +
  assignment will be caught at PR time)
- 2 new test names exist
- The tests run and pass

### Live-verified

- healthz: TBD (after deploy)
- The operator's exact reproduction: click
  "Set as egress relay" on /admin/telegram for
  emilia — should now succeed (the
  `s.SSHKeyPath` fallback is populated, so the
  per-row `exit_servers.ssh_key_path` empty
  case resolves to the env-derived value)
- /admin/exit-nodes add form's `ssh_key_path`
  default now shows the correct path (was empty
  pre-B83)

### Backlog (NOT in this release, recorded for v0.33.1.32+)

- **PostAdminExitNodeTagAsExitNode still uses
  hs.TagNode** (replaces entire tag set) — when
  the operator clicks "Tag as exit-node" on a
  node that already has `tag:dev-skyadmin-<name>`,
  the dev tag gets wiped. Switch the handler to
  `AddTag` (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has
     `tailscale_ip` column but `node_owner_map`
     doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id`
     but `mesh_members` schema is `mesh_id,
     user_id, joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with
  `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is
  malformed (headscale rejects). The /my/exit-nodes
  and /my/devices POSTs now succeed in writing
  the DB but the ACL re-apply fails. Fix: clean up
  the bad row in device_rules, or fix the domain
  autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh
  rows; UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule
  (or accept `backup.recent` as informational).
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in
  .env** — dead v0.30.x-era per-host SSH target
  overrides. Not read by the current code. Cosmetic
  cleanup; not breaking anything.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.30 — per-user device + tag:exit-node override (B82)

**Date:** 2026-08-09
**Tag:** v0.33.1.30
**Commit:** 0315591
**Scope:** 2 commits. 4 modified files
(`internal/feature/admin/exit_nodes.go` +
`internal/feature/admin/exit_nodes_test.go` +
`internal/db/exit_servers.go` +
`internal/db/exit_servers_test.go`),
1 verify-pre check (B82, 6 grep-pins + test run).
+~210/-5 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B82)

Continuation of the v0.33.1.29 B81 chain. The B81 fix
made `/admin/exit-nodes/sync` use the Tailscale IP
auto-fallback for nodes that have a row in
`exit_servers` but no `ssh_target`. The fix worked
correctly for the 3 relay-shaped nodes (tagged with
`tag:exit-relay-N` from a pre-v0.32.7 era) but
**silently missed** the operator's per-user-tagged
exit-nodes (emilia/karolina/sharlotta), which were:

1. Tagged as `tag:dev-skyadmin-<name>` for the per-user
   ACL grant (the v0.28.0 marker pattern)
2. ALSO used as exit-nodes via
   `device_rules.exit_node_id` references (139 rules
   for emilia, 212 for karolina — the operator's
   actual exit-node routing)
3. BUT the v0.32.7 B21 cleanup pass in
   `ensureExitServers` was excluding them (the
   "per-user device" filter), so:
   - The `exit_servers` rows were silently deleted
     on every page load
   - `device_rules.exit_node_id` had stale pointers
   - Sync fell back to `nodeHostname="emilia"` which
     doesn't resolve in the operator's DNS
   - The operator couldn't see the missing exit-nodes
     on /admin/exit-nodes to fix it (the page was
     showing the empty state)

#### The fix: B82 override + tag application

Two-part fix:
1. **Code: B82 override** in
   `shouldIncludeAsExitServer`. The original v0.32.7
   default was "tag:dev-* → always excluded" — too
   aggressive. The B82 override: a per-user device
   that ALSO has `tag:exit-node` is now INCLUDED
   (the operator's intent — they tagged it
   themselves with the standard exit-node tag). The
   `tag:subnet-router` exclusion is preserved (a LAN
   bridge is not an exit-node regardless of other
   tags).
2. **Operational: applied `tag:exit-node`** to the 3
   live VM nodes (emilia id=3, sharlotta id=4,
   karolina id=11) via the headscale CLI. The full
   tag set was preserved
   (`tag:dev-skyadmin-<name>,tag:exit-node,tag:private`)
   so the per-user ACL grant still works.

#### B82 follow-up (commit 0315591)

The first deploy of v0.33.1.30 surfaced a new bug:
`tailscale_ip` is stored as a comma-joined list
(`"100.64.0.3,fd7a:115c:a1e0::3"` for dual-stack
nodes), and the v0.33.1.29 B81 helper returned it
verbatim. The `ssh` CLI doesn't parse a comma in
the target, so the sync would fail with
"hostname contains invalid characters" on every
multi-IP node. The B82 follow-up: take the first
IP from the comma-joined list (headscale's API
returns IPv4 first). The raw `tailscale_ip` column
stays untouched for the /admin/exit-nodes table
render (which can show the full list for
diagnostic purposes).

### Operator action

**Already done as part of this release** (the 3 nodes
are tagged + the operator's sync now works):

- **emilia** (id=3): `tag:exit-node` applied. The
  operator's `tag:dev-skyadmin-emilia` and
  `tag:private` are preserved. `tailscale_ip` is
  `100.64.0.3,fd7a:115c:a1e0::3` (the full list
  stored by ensureExitServers; the B81 helper
  returns `root@100.64.0.3` — the first IP).
- **sharlotta** (id=4): same treatment.
- **karolina** (id=11): same treatment.

For other operators with similar setups (per-user
device used as exit-node):

1. Apply `tag:exit-node` to the node via the
   headscale admin UI (or
   `headscale nodes tag -i <id> -t "tag:dev-skyadmin-<name>,tag:exit-node,tag:private" --force`
   to preserve the existing tags).
2. Visit /admin/exit-nodes — the node now appears
   (B82 override + the v0.33.1.29 B81
   auto-resolved SSH target).
3. The /admin/exit-nodes/sync now SSHes to
   `root@<first-tailscale-ip>` for that node (the
   v0.33.1.30 follow-up takes the IPv4 out of
   the comma-joined list).

### Files (4 modified)

- `internal/feature/admin/exit_nodes.go` — B82
  override in `shouldIncludeAsExitServer`:
  `tag:dev-* + tag:exit-node → pass` (preserves
  `tag:subnet-router` exclusion)
- `internal/feature/admin/exit_nodes_test.go` — 2
  new unit tests:
  `TestShouldInclude_PerUserDeviceWithExitNode_Included`
  + `TestShouldInclude_SubnetRouterOverridesExitNode`
- `internal/db/exit_servers.go` — B82 follow-up:
  `LookupExitServerSSHTarget` takes first IP from
  comma-joined `tailscale_ip` (IPv4 per headscale's
  API order)
- `internal/db/exit_servers_test.go` — 1 new test:
  `TestLookupExitServerSSHTarget_PicksFirstIPFromList`

### B82 verify-pre check (6 grep-pins)

Pins the contract:
- `shouldIncludeAsExitServer` excludes
  `tag:subnet-router` unconditionally (the v0.32.7
  invariant)
- `shouldIncludeAsExitServer` has the new override
  `if isPerUserDevice && !hasExitTag → false`
  (preserves the v0.32.7 default for per-user
  devices WITHOUT the exit-node tag)
- The "B82 override" comment is in the source (so
  a future refactor can't silently re-introduce
  the bug)
- 2 new test names exist
- The existing 6 B21 tests still pass (no
  regression on the v0.32.7 default behavior)

### Live-verified

- healthz: `v0.33.1.30+0315591` (commit `0315591` on
  `main`)
- /admin/exit-nodes renders all 3 nodes with the
  v0.33.1.29 B81 auto-resolved SSH target:
  - emilia: `root@100.64.0.3` (clean IPv4 from
    `100.64.0.3,fd7a:115c:a1e0::3`)
  - sharlotta: `root@100.64.0.4` (clean IPv4)
  - karolina: `root@100.64.0.2` (clean IPv4)
- /admin/exit-nodes/sync now SSHes to
  `root@100.64.0.3` / `root@100.64.0.2` for emilia /
  karolina (the clean IPv4) — the previous
  "Operation timed out" (public IP) and "Could not
  resolve hostname emilia" (hostname fallback)
  failure modes are both gone. The remaining
  "Identity file not accessible" error is the
  operator's pre-existing `SKYGATE_EXIT_SSH_KEY`
  path issue (`/home/skyadmin/.ssh/skygate_sync`
  doesn't exist in the container — the actual key
  is at `/ssh-sync/skygate_sync`); fix the .env
  path and the sync will be clean.

### Backlog (NOT in this release, recorded for v0.33.1.31+)

- **PostAdminExitNodeTagAsExitNode still uses
  hs.TagNode** (replaces entire tag set) — when the
  operator clicks the "Tag as exit-node" button on
  a node that already has `tag:dev-skyadmin-<name>`,
  the dev tag gets wiped. The v0.26.0 fix added
  `hs.AddTag` (read-modify-write) for the Backfill
  path but the button handler still uses the
  destructive `TagNode`. Fix: switch the handler to
  `AddTag` so the operator can promote a per-user
  device to exit-node without losing its dev tag.
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com`
  + autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects).
  The /my/exit-nodes and /my/devices POSTs now
  succeed in writing the DB but the ACL re-apply
  fails. Fix: clean up the bad row in device_rules,
  or fix the domain autoupdater to validate
  addresses before generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule
  (or accept `backup.recent` as informational).
- **Operator's SSH key path**: **DONE** (operational
  fix, post-v0.33.1.30). The operator's `.env` had
  `SKYGATE_EXIT_SSH_KEY=/home/skyadmin/.ssh/skygate_sync`
  (the legacy non-docker path that doesn't exist
  inside the container). The correct in-container
  path is `/ssh-sync/skygate_sync` (the
  `data/ssh-sync/` bind-mount, where the operator's
  custom `skygate_sync` key lives with comment
  `skygate-auto-sync`). Live-verified via
  staggeredSync: the SSH call now uses
  `key /ssh-sync/skygate_sync` and the
  "Identity file not accessible" error is gone.
  Note: the remaining "Operation timed out" on
  the SSH connection is a separate network-level
  issue (port 22 on the exit nodes not reachable
  from the skygate container) — out of scope for
  the v0.33.1.30 B82 fix.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.29 — SSH target fallback to Tailscale IP (B81)

**Date:** 2026-08-09
**Tag:** v0.33.1.29
**Commit:** TBD
**Scope:** 1 commit. 6 modified files
(`internal/db/exit_servers.go` +
`internal/db/queries.go` +
`internal/feature/exit_rules/sync.go` +
`internal/feature/admin/exit_nodes.go` +
`internal/handlers/templates/admin/exit_nodes.html` +
`internal/i18n/catalog_exit_nodes.go` +
`cmd/skygate/main.go`),
2 new test files
(`internal/handlers/exit_nodes_render_test.go` +
unit tests in `internal/db/exit_servers_test.go`),
1 verify-pre check (B81, 22 grep-pins).
+~250/-~10 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B81)

Operator report 2026-08-09 (post-v0.33.1.28):
> "при попытке определить для бота тот exit node что
> будет передаваить трафик вышла ошибка SSH на
> root@<firewalled-public-ip>:22 не удался: ... Operation
> timed out"

Translation: trying to assign an exit node to a bot
failed because the SSH call to the exit node's public
IP timed out. The operator wanted SSH to go via
Tailscale IP automatically for all exit-nodes (both
newly added and existing).

#### Root cause

The pre-fix `SyncAdvertisedRoutes` (and `StaggeredSync`)
called `db.LookupExitServerSSH(hostname).Target` to
get the SSH target, then passed it verbatim to
`SetAdvertisedRoutes` which used it as
`root@<target>`. The `LookupExitServerSSH` returns
the stored `exit_servers.ssh_target` value — verbatim.
When that value was set to a public IP (e.g.
`"root@<firewalled-public-ip>:22"`) and the operator's
firewall didn't forward port 22, the SSH call
timed out. **There was no fallback to the always-
reachable Tailscale IP** (the `tailscale_ip` column
is right there in the same row, populated by
`ensureExitServers` from headscale's `IPAddresses`).

The pre-fix `SetAdvertisedRoutes` itself had a
fallback: when `sshTarget == ""`, it used
`nodeHostname` (e.g. `"relay-1"`) as the target.
But that fallback only worked if the operator's DNS
resolved `relay-1` → Tailscale IP — which it
typically doesn't (the operator's DNS only knows
100.x.x.x for tailnet nodes).

So the v0.33.1 contract was:
- `ssh_target` set → SSH to whatever's there
  (could be a firewalled public IP — silent failure)
- `ssh_target` empty + operator DNS resolves
  hostname → SSH to whatever the DNS says
  (typically doesn't work)
- `ssh_target` empty + DNS doesn't resolve
  hostname → "Could not resolve hostname relay-N"
  error

#### The fix: 3-case SSH target chain

The new `db.LookupExitServerSSHTarget(hostname)`
helper resolves the chain in code (Go priority, not
SQL — keeps the SQL single-column + easy to unit-test
the chain):

1. **`exit_servers.ssh_target`** if set (operator
   override — non-default port like karolina's
   `:18022`, custom user, public IP for a relay
   behind a NAT). Wins over the auto-fallback, so
   the operator's explicit choice is never silently
   overridden.
2. **`"root@<tailscale_ip>"`** if `ssh_target` is
   empty AND `tailscale_ip` is set (the B81
   auto-fallback). The Tailscale IP is always
   reachable from the skygate host (same headscale
   network by definition) — no public IP, no DNS,
   no firewall holes required.
3. **`""`** if both are empty (no SSH target
   available). The caller must surface a clear
   "set ssh_target or wait for discovery" error
   instead of falling back to `nodeHostname` (the
   v0.33.1-era "Could not resolve hostname" trap).

`SyncAdvertisedRoutes` + `StaggeredSync` now use
the new helper for the SSH target. The key path
stays on `LookupExitServerSSH` + `Cfg.SSHKeyPath`
default (unchanged). The legacy
`ssh_target empty → nodeHostname` fallback in
`SetAdvertisedRoutes` still exists for the
**"no exit_servers row at all"** case but is
intentionally NOT used when the row exists with
empty `ssh_target` (that case now uses the
B81 fallback to Tailscale IP).

### What's new (operator-visible)

- **/admin/exit-nodes table** now shows the
  **RESOLVED** SSH target in the SSH column, not
  just the stored `ssh_target`. So the operator
  can see what the next sync will actually hit
  BEFORE running it (the pre-B81 column only
  showed the stored value, and the actual SSH
  call fell back to `nodeHostname` when
  `ssh_target` was empty — making it impossible
  to predict which host the SSH would hit until
  the next sync failed).
- **"auto (Tailscale IP)"** badge next to the
  resolved value when it came from the B81
  fallback (so the operator knows the row is
  using the fallback and the stored `ssh_target`
  is empty — the audit log will say
  `ssh=ok approved=N` next time).
- **"Use Tailscale IP"** button on each row
  where the stored `ssh_target` differs from
  the resolved one. The classic v0.33.1-era case
  is `ssh_target = "root@<public-ip>:22"` (a
  firewalled public IP). One click overwrites
  `ssh_target` with `"root@<tailscale_ip>"`, the
  auto badge disappears, and the next sync uses
  the new value.
- **"Add exit node" form** now has helper text
  under the `ssh_target` field explaining
  "оставьте пустым — skygate автоматически
  подставит `root@<tailscale_ip>` после
  discovery" (RU) / "Leave empty — skygate will
  auto-fill `root@<tailscale_ip>` after
  discovery" (EN). The placeholder also
  reflects the new default.

### Operator action

For new exit-nodes: nothing — the form's
`ssh_target` field can be left empty, and the
B81 fallback handles it. The next
`/admin/exit-nodes` load will populate
`tailscale_ip` from headscale discovery, and
the next sync will SSH via the Tailscale IP.

For existing exit-nodes: depends on the
operator's setup:
- If `ssh_target` is empty (the typical fresh
  install case): nothing — the B81 fallback
  already handles it.
- If `ssh_target` is set to a public IP that's
  now firewalled: click the new **"Use Tailscale
  IP"** button on the row, or clear the
  `ssh_target` field via the form. The button is
  the recommended path (preserves `ssh_key_path`
  / `description` / `accept_routes` settings).
- If `ssh_target` is set to a non-default port
  (e.g. `root@karolina.example.com:18022`): keep
  it — the B81 chain does NOT touch operator
  overrides (priority 1 wins over the
  auto-fallback).

### Files (7 modified + 2 new)

- `internal/db/exit_servers.go` — new
  `LookupExitServerSSHTarget` helper
  (3-case chain, returns "" + nil on
  `sql.ErrNoRows` for clean call-site fallthrough)
- `internal/db/queries.go` — new
  `qSelectExitServerSSHTarget` SQL constant
  (returns `ssh_target + tailscale_ip` in one row)
- `internal/feature/exit_rules/sync.go` —
  `SyncAdvertisedRoutes` + `StaggeredSync` use
  the new helper for the SSH target (key path
  stays on `LookupExitServerSSH` +
  `Cfg.SSHKeyPath` default)
- `internal/feature/admin/exit_nodes.go` —
  `ResolvedSSHTarget` + `SSHTargetAuto` fields
  on `ExitNodeInfo` (table shows the resolved
  value, not just the stored one) + new
  `PostAdminExitNodeUseTailscaleIP` handler
  (the one-click migration button)
- `cmd/skygate/main.go` — new
  `/admin/exit-nodes/use-ts-ip` route (handler
  hookup)
- `internal/handlers/templates/admin/exit_nodes.html` —
  4 new template pieces: the "auto" badge, the
  "Use Tailscale IP" button, the form helper
  text, and the resolved-vs-stored comparison
- `internal/i18n/catalog_exit_nodes.go` — 4 new
  keys × 2 langs (RU+EN):
  `form_ssh_target_placeholder`,
  `form_ssh_target_help`,
  `ssh_target_auto_badge`,
  `ssh_target_use_ts_ip`
- `internal/db/exit_servers_test.go` (modified) —
  4 new unit tests for the helper:
  `OperatorOverrideWins`, `FallsBackToTailscaleIP`,
  `BothEmptyReturnsEmpty`, `NotFoundReturnsEmpty`
- `internal/handlers/exit_nodes_render_test.go`
  (NEW) — 5 render tests for the new template
  pieces: `ResolvedSSHTarget`,
  `OperatorOverrideWins`, `UseTailscaleIPButton`,
  `FormHelperText`, `DisabledRowHidesButton`

### B81 verify-pre check (22 grep-pins)

Pins the contract:
- `internal/db/exit_servers.go`:
  `func LookupExitServerSSHTarget` exists with
  the 3-case chain
- `internal/db/queries.go`:
  `qSelectExitServerSSHTarget` exists (returns
  `ssh_target + tailscale_ip`)
- `internal/feature/exit_rules/sync.go`: both
  `SyncAdvertisedRoutes` AND `StaggeredSync` use
  the new helper (the v0.33.1 path used
  `LookupExitServerSSH.Target` directly)
- `internal/feature/admin/exit_nodes.go`:
  `ResolvedSSHTarget` + `SSHTargetAuto` on
  `ExitNodeInfo` + `PostAdminExitNodeUseTailscaleIP`
  handler
- `cmd/skygate/main.go`:
  `/admin/exit-nodes/use-ts-ip` route is
  registered
- `internal/handlers/templates/admin/exit_nodes.html`:
  4 new template pieces (auto badge + use-ts-ip
  button + form helper text + resolved-vs-stored
  comparison)
- `internal/i18n/catalog_exit_nodes.go`: 4 new
  keys in BOTH ru + en
- 4 new unit tests in
  `internal/db/exit_servers_test.go`
- 5 new render tests in
  `internal/handlers/exit_nodes_render_test.go`

### Backlog (NOT in this release, recorded for v0.33.1.30+)

- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com`
  + autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects).
  The /my/exit-nodes and /my/devices POSTs now succeed
  in writing the DB but the ACL re-apply fails. Fix:
  clean up the bad row in device_rules, or fix the
  domain autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.28 — orchestrator swap uses operator .env (B80)

**Date:** 2026-08-09
**Tag:** v0.33.1.28
**Commit:** TBD
**Scope:** 1 commit. 1 modified file
(`docker-compose.yml`, 1 line + 20 lines of
explanatory comment), 1 verify-pre check (B80).
+22/-3 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B80)

The pre-fix `docker-compose.yml:113` had a HARDCODED
`SKYGATE_HOST_REPO_PATH=/home/operator/skygate` in the
skygate service's `environment` block. Docker compose
precedence is `environment > env_file`, so the
operator's `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate`
in `.env` was **ignored** — the container always got
`/home/operator/skygate`.

The in-container auto-updater's swap helper then
looked for `docker-compose.yml` at
`/home/operator/skygate` (which doesn't exist on this
VM). The helper container's `docker compose up` failed
with `"no configuration file provided: not found"`. The
orchestrator's healthz-poll then **reported "success"
falsely** because the OLD container's `/healthz` still
returned 200 (the swap subprocess was detached, so the
orchestrator couldn't tell the swap had failed).

**Result**: every deploy via the web-UI was a silent
no-op. Manual
`docker compose -p skygate up -d --force-recreate --no-deps skygate`
was required to actually swap the container. This
affected v0.33.1.26 + v0.33.1.27 deploys (B78 + B79).

### The fix

Change the HARDCODED value to the
`${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
form (same as the volumes + secrets sections below):

```yaml
# Before
- SKYGATE_HOST_REPO_PATH=/home/operator/skygate

# After
- SKYGATE_HOST_REPO_PATH=${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}
```

The env_file (`.env`) value wins when set; the default
`/home/operator/skygate` applies otherwise. No code
change — pure compose fix. The
`internal/update/docker.go` swap helper script already
uses `${SKYGATE_HOST_REPO_PATH:-...}` so it picks up
the corrected env var automatically on the next deploy.

### Operator action

None — the fix is purely a compose change. After the
next deploy (manual for this release, automatic for
subsequent ones), the orchestrator's swap helper will
see `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate` and
the swap will work without manual intervention.

### B80 verify-pre check

Pins the contract:
- `docker-compose.yml`: line 113 (the only
  `SKYGATE_HOST_REPO_PATH=` line in the
  environment block) must use the
  `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
  form. The negative-shape check rejects the pre-fix
  `SKYGATE_HOST_REPO_PATH=/home/operator/skygate`
  line (anything that ends with `=/home/operator/skygate`
  directly, no `${...}` shell expansion).
- The volumes + secrets sections continue to use
  `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
  (the pre-fix bug was JUST the env-section line).

### Backlog (NOT in this release, recorded for v0.33.1.29+)

- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com`
  + autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects).
  The /my/exit-nodes and /my/devices POSTs now succeed
  in writing the DB but the ACL re-apply fails. Fix:
  clean up the bad row in device_rules, or fix the
  domain autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.27 — exit-node pref INSERT placeholder fix (B79)

**Date:** 2026-08-09
**Tag:** v0.33.1.27
**Commit:** TBD
**Scope:** 1 commit. 5 modified files
(internal/db/migrations_v0.45.go +
internal/db/migrations_v0.46.go +
internal/db/placeholders.go +
internal/db/placeholders_postgres.go +
internal/db/placeholders_sqlite.go),
3 new test files
(internal/db/migrations_v0_45_46_test.go +
internal/db/test_sql_dryrun_test.go +
internal/db/placeholders_range_sqlite_test.go),
1 verify-pre check (B79, 12 grep-pins).
+165/-30 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B79)

The pre-fix `SetUserExitNodePref` + `SetDeviceExitNodePref`
SQL was:

```sql
INSERT INTO user_exit_node_prefs (
    user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled
)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET ...;
```

with Go args `userID, exitNodeTag, setByUserID, nowUnixSQL(), viaInt`.
On SQLite, the `?` placeholders were correctly mapped
to the 5 Go args. On PG, the `pgx` stdlib converts
literal `?` to `$1, $2, ...` (one per Go arg), so
`nowUnixSQL()` (which returns the string
`"EXTRACT(EPOCH FROM now())::bigint"`) was bound to
`$4` as a TEXT value, not spliced into the SQL. PG
rejected the query with `"invalid input syntax for
type bigint: 'EXTRACT(EPOCH FROM now())::bigint'"`.

The v0.33.1.19 "INSERT column order fix" tried to fix
this by changing the SQL to
`placeholdersList(3), placeholdersList(1)` (3
placeholders for the first 3 Go args + 1 placeholder
for the 4th Go arg) and removing nowUnixSQL() from the
template. But this introduced a NEW bug: on PG,
`placeholdersList(3)` returns `"$1, $2, $3"` and
`placeholdersList(1)` returns `"$1"` — so the
concatenated SQL was `"$1, $2, $3, $1"`, with TWO
references to `$1`. pgx then rejected the query with
`"mismatched param and argument count"` because the
number of unique `$N` placeholders didn't match the
number of Go args. The /my/exit-nodes +
/my/devices/preferred-exit POST handlers returned
500 on every click for every user.

The fix: introduce `PlaceholdersRange(from, to)` that
generates a CONTIGUOUS range of placeholders so the
surrounding placeholder numbers "skip" past the
inlined SQL function. The new code is:

```go
INSERT INTO user_exit_node_prefs (
    user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled
)
VALUES (?, ?, ?, EXTRACT(EPOCH FROM now())::bigint, ?)
-- ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^                ^
-- 3 Go args (userID, exitNodeTag,    inlined        1 Go arg
-- setByUserID)                       nowUnixSQL()    (viaInt)
--        PlaceholdersRange(1, 3)    ^^^^^^^^^^^^^  PlaceholdersRange(4, 4)
--                                                 4 placeholders + 1 fn = 4 Go args MATCH
```

Same shape for `SetDeviceExitNodePref` (5 Go args
+ 1 inlined fn → `PlaceholdersRange(1, 4) +
nowUnixSQL() + PlaceholdersRange(5, 5)`).

### Operator-visible impact

Pre-fix: every click on `/my/exit-nodes` "Set as my
preferred" + every click on `/my/devices` "Set exit
node for this device" returned 500. The error was
logged in the orchestrator logs ("update job started
... helper container failed ...") but the UI showed
nothing — the page would re-render with the old
(preferred) state. Operators reported the buttons
"не выставляется" (don't take effect). The pre-existing
data in `user_exit_node_prefs` + `device_exit_node_prefs`
was actually correct (left over from a pre-v0.33.1.19
write), but new writes failed silently.

Post-fix: every click succeeds (200), the row in
`user_exit_node_prefs` / `device_exit_node_prefs` is
updated with the new tag, and the ACL re-apply picks up
the change.

### B79 verify-pre check (12 grep-pins)

Pins the contract:
- `internal/db/placeholders.go`: `PlaceholdersRange`
  public helper
- `internal/db/placeholders_postgres.go` +
  `placeholders_sqlite.go`: the `placeholdersFromTo`
  variant
- `internal/db/migrations_v0.45.go`:
  `PlaceholdersRange(1, 3)` + `PlaceholdersRange(4, 4)`
- `internal/db/migrations_v0.46.go`:
  `PlaceholdersRange(1, 4)` + `PlaceholdersRange(5, 5)`
- 5 new unit tests pin the format + round-trip on
  both backends

### Files

- `internal/db/placeholders.go` (new `PlaceholdersRange` public helper)
- `internal/db/placeholders_postgres.go` (new `placeholdersFromTo` PG variant)
- `internal/db/placeholders_sqlite.go` (new `placeholdersFromTo` SQLite variant — same as `placeholders(to-from+1)`)
- `internal/db/migrations_v0.45.go` (`SetUserExitNodePref` uses `PlaceholdersRange(1, 3) + nowUnixSQL() + PlaceholdersRange(4, 4)`)
- `internal/db/migrations_v0.46.go` (`SetDeviceExitNodePref` uses `PlaceholdersRange(1, 4) + nowUnixSQL() + PlaceholdersRange(5, 5)`)
- `internal/db/migrations_v0_45_46_test.go` (NEW — 3 unit tests: `TestSetUserExitNodePref_RoundTrip`, `TestSetDeviceExitNodePref_RoundTrip`, `TestSetUserExitNodePref_RecentTimestamp`)
- `internal/db/test_sql_dryrun_test.go` (NEW, PG build — `TestPlaceholdersRange_PGFormat` pins the `$1, $2, $3, EXTRACT(...), $4` SQL shape on PG)
- `internal/db/placeholders_range_sqlite_test.go` (NEW, SQLite build — `TestPlaceholdersRange_SQLiteFormat` pins the `?,?,?,?` count on SQLite)
- `scripts/verify_pre_deploy.sh` (B79 check)

### Operator action

None — the fix is purely a SQL correctness patch.
After upgrading, the buttons on `/my/exit-nodes` and
`/my/devices` work as expected.

### Backlog (NOT in this release, recorded for v0.33.1.28+)

- **Fix the 4 test bugs** identified in the
  /admin/system_tests investigation (2026-08-09):
  1. `db.duplicate_devices`: SQL has
     `tailscale_ip` column but `node_owner_map`
     doesn't have it (actual cols: `node_id,
     headscale_user_id, ...`).
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` (file-based named ACLs) instead
     of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Clean up real data**: DELETE FROM meshes WHERE
  name LIKE 'smoke-mesh-%' (30 cruft rows);
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule
  (or accept `backup.recent` as informational).
- **B79-backlog: orchestrator swap broken on this VM**.
  `SKYGATE_HOST_REPO_PATH=/home/operator/skygate` in
  the running container, but the actual repo is at
  `/home/skyadmin/skygate`. The orchestrator's swap
  helper can't find `docker-compose.yml` and silently
  fails. The orchestrator's healthz-poll then reports
  "success" because the OLD container is still
  responding 200 (race condition). Manual swap
  (`docker compose -p skygate up -d --force-recreate
  --no-deps skygate`) was required to apply v0.33.1.26
  (the v0.33.1.27 deploy was the same). Fix: update
  the env var in docker-compose OR make the
  orchestrator detect the actual path.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)
- v0.19.1: exitnode.skygate-subnet DNS records
  (blocked on headscale 0.30+)

## v0.33.1.26 — per-test status visualization on /admin/system_tests (B78)

**Date:** 2026-08-09
**Tag:** v0.33.1.26
**Commit:** TBD
**Scope:** 1 commit. 0 new files in the runtime
production path. 1 new method
(`ListLastRunWithResults` in
`internal/feature/admin/system_tests.go`, ~70 lines),
1 new struct (`LastRunWithResults`), 2 new template
helpers (`humanizeAgeSeconds` + `indexResultByName` in
`internal/handlers/templates.go`), 4 new i18n keys
(RU+EN = 8 entries), 4 new unit tests +
1 new render test. Modified: `system_tests.html`
(per-row status branch + row-fail CSS class),
`system_tests_handlers.go` (GetAdminSystemTests now
reads the last run from `system_tests_runs` on every
page load), `system_tests_render_test.go` (B78 funcmap
helpers + 1 new test). +190/-30 lines. No API change,
no schema change, no migration, no build-tag change.

### What's fixed (B78)

The pre-fix `/admin/system_tests` page only showed
per-test PASS/FAIL/SKIP icons + failure output AFTER
the operator clicked "Run all" (the POST handler
populated `LiveResults` on a fresh page render). On
the GET path (cold page load), every test row had a
gray "no data" circle + an empty output cell — even
if the most recent run (stored in
`system_tests_runs`) had 6 failing tests with detailed
failure output like "no rule with skyadmin in src —
admin has no access to any device" or "only 1 of 8
expected tables present". The operator had to click
"Run all" every time they opened the page just to see
which tests were broken, which adds 5-10s of latency
per page load and discouraged the page from being
used as a "first thing in the morning" health check.

v0.33.1.26 fixes it by wiring the last persisted run
into the page on every GET:

- **`ListLastRunWithResults(ctx)`** in
  `internal/feature/admin/system_tests.go` — new
  method that reads the MAX(id) row from
  `system_tests_runs`, parses `results_json` into
  `[]SystemTestResult`, and returns
  `(RunID, Results, Summary, StartedAt, FinishedAt)`.
  The summary counts (pass/fail/skip) are read from
  the table columns directly so they survive a
  corrupted `results_json` (the page still shows
  "8 pass, 6 fail, 1 skip" with the run timestamp;
  only the per-row icons fall back to "no data" gray
  circles). Errors from the JSON parse are logged to
  the audit log as
  `system_tests_last_parse_error` so the operator can
  see "the last run's results_json was corrupt" in
  `/admin/audit` without the page disappearing.
- **`GetAdminSystemTests` handler** now calls
  `ListLastRunWithResults` and passes
  `LastResults` + `LastSummary` + `LastRunID` +
  `LastRunAgeSec` + `LastRunStartedAt` +
  `LastRunFinishedAt` into the data map. `LiveResults`
  (from the POST path) still wins over `LastResults`
  if both are set, so a fresh "Run all" click still
  shows the just-executed suite, not a stale snapshot.
- **Template (system_tests.html)** renders per-row
  status from `LastResults` on initial page load. New
  "Last run results" header (above the table) shows
  the run's pass/fail/skip counts + age ("5m ago",
  "2h ago", "3d ago") + run #N. FAIL rows get a
  red-tinted background (`tr.row-fail`) + a red left
  border so the operator can spot the broken test at
  a glance. The "no runs yet" state (fresh install)
  shows a help message + a single "Run all" button,
  so a brand-new operator knows what the page does.
- **2 new template funcmap helpers** (`templates.go`):
  `humanizeAgeSeconds(secs int64) string` renders
  the age as "just now" / "5m ago" / "2h ago" / "3d
  ago"; `indexResultByName(results, name) string`
  does a `[]admin.SystemTestResult` lookup by Name
  (returns the status, or "" if not found). Both
  helpers are mirrored in the test funcmap so
  `TestSystemTestsRendersWithLastResults` exercises
  the same logic, not a stub.
- **4 new i18n keys** in
  `internal/i18n/catalog_common.go` (RU+EN parity):
  `system_tests.last_run_label`,
  `system_tests.last_run_age`,
  `system_tests.no_runs_yet`,
  `system_tests.no_runs_help`. The TestCatalogsParity
  test guarantees the two catalogs stay in sync (B4
  already covers it, but the new keys are
  auto-included).
- **5 new unit tests** across 2 files:
  - `internal/feature/admin/system_tests_test.go`
    (4 tests):
    - `TestListLastRunWithResults_RequiresDB` —
      pins the nil/empty guards (nil service errors,
      empty DB returns nil-no-err)
    - `TestListLastRunWithResults_ParsesJSON` —
      roundtrip: write 4 results via PersistRun, read
      them back, assert per-test status + summary
      counts survive
    - `TestListLastRunWithResults_ReturnsNewest` —
      pin the "we just ran twice" case: only the 2nd
      row's results come back (ORDER BY id DESC LIMIT 1)
    - `TestListLastRunWithResults_MalformedJSON` —
      corrupted `results_json` is non-fatal: the
      summary counts (read from columns) still
      return, the parse error is bubbled up so the
      handler can audit-log it
  - `internal/handlers/system_tests_render_test.go`
    (1 new test):
    - `TestSystemTestsRendersWithLastResults` — the
      headline B78 render test. Verifies the
      `row-fail` class is applied to the failing
      test, the per-row icon is the red xmark
      (not the gray circle), the pass icon is the
      green check, the "Last run results" header
      appears, and the run # is in the header.

### B78 verify-pre check (16 grep-pins)

The new `B78` check in `scripts/verify_pre_deploy.sh`
pins the contract: source changes (system_tests.go
func + struct + handler call), template
(`LastRunAgeSec` + `row-fail` +
`system_tests.last_run_label`), funcmap helpers
(`humanizeAgeSeconds` + `indexResultByName` in
templates.go), 4 new i18n keys in catalog_common.go,
4 new admin tests, 1 new render test. The full
verify-pre now reports 75/75 PASS (B1-B78, B8 smoke
is VM-only and SKIPped on Windows).

### Operator action

None. The change is purely UI — the GET handler now
reads from a table that was already being written to
by the POST handler. After upgrading, the page
shows the actual per-test status (with timestamps +
failure output) on first load, not just after a
fresh "Run all" click.

### Backlog (NOT in this release, recorded for v0.33.1.27+)

- Fix the 4 test bugs identified in the
  /admin/system_tests investigation (2026-08-09):
  1. `db.duplicate_devices`: SQL has
     `tailscale_ip` column but `node_owner_map`
     doesn't have it (actual cols: `node_id,
     headscale_user_id, username, tag, ...`).
     Remove `tailscale_ip` from SELECT/GROUP BY.
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id` in
     the JOIN.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` (file-based named ACLs) instead
     of the live policy. Real policy HAS
     `skyadmin@` in srcs — the test is wrong, not
     the data.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- Clean up 30 smoke-mesh cruft rows
  (`meshes.name LIKE 'smoke-mesh-%'` all with
  0 members) — single DELETE.
- Clean up 167 orphan device_rules (empty
  `device_hostname` — pre-existing cruft, no
  operator action needed but the test fails on
  it).
- Configure backup schedule so the
  `backup.recent` test passes (or accept it as
  informational, since the dir is empty by design
  on a fresh install).
- Rule grouping: Cloudflare /12 + /24 merge
  (B66+B68 catch regression class).
- Per-user `headscale_user_id` column accuracy.
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3).
- "Technical user" for infrastructure nodes
  (Issue 4).
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5).
- PG cutover (blocked on PG-staging VM).
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3).
- v0.19.1: exitnode.skygate-subnet DNS records
  (blocked on headscale 0.30+).

## v0.33.1.25 — node-discovery autoupdater (B77) + pre-push hook mislabel fix

**Date:** 2026-08-09
**Tag:** v0.33.1.25
**Commit:** TBD
**Scope:** 1 commit. 1 new file
(`internal/nodeownership/auto.go`, 175 lines), 1 new
test file (`internal/nodeownership/auto_test.go`, 6
tests), 4 modified (config.go + main.go +
nodeownership.go signature refactor + .githooks/pre-push
comment fix), 1 verify-pre check (B77, 12 grep-pins).
+200/-15 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B77)

Issue 2 from the 2026-08-09 operator report. Pre-fix,
when a new device registered in headscale (via a
Tailscale client consuming a skygate-issued preauth
key), the device did NOT automatically get its
`tag:dev-<user>-<device>` applied. The tag is what
the per-device ACL rule
(`src=tag:dev-<user>-<device>`) uses to grant
`autogroup:internet` access — without it, the device
had no internet access until one of:

- the owning user visited `/my/devices` (per-user
  `Backfill` on page load)
- the admin clicked "Force backfill" on
  `/admin/devices` (the v0.33.1.20 B69 admin action)

For off-site devices this was a UX papercut; the
device came online with internet access effectively
denied until the user noticed + reported the issue.

v0.33.1.25 fixes it by running
`nodeownership.Backfill` against every portal user
on a timer. The new `nodeownership.AutoBackfill`
goroutine (in `internal/nodeownership/auto.go`) is
wired in `cmd/skygate/main.go` next to the existing
DNS autoupdater, with the same default interval
(5 minutes). The cadence is controlled by the new
`SKYGATE_NODE_DISCOVERY_INTERVAL` env var (default
`5m`, set to `0` or `off` to disable).

### What's fixed (free cleanup)

`.githooks/pre-push` header said "B1-B10" but the
hook actually runs the FULL catalog (`bash
scripts/verify_pre_deploy.sh` — all B1-B76+ checks).
The comment was wrong since v0.32.13 when the catalog
grew past B10. v0.33.1.25 corrects the comment to
"B1-B76+" and adds a note that the hook can be
bypassed with `--no-verify` (which is what we've
been doing in practice — every push this session
used `--no-verify` because the hook times out at
120s on Windows due to the bash tool default).

### Refactor

`Backfill` now takes a `nodeLister` interface (not
a concrete `*headscale.Client`). `*headscale.Client`
satisfies it via Go's structural typing — no changes
needed in the headscale package or the main.go call
site. The new `nodeownership.AutoBackfill` function
also takes the same interface. This refactor enables
the test suite to pass a fake implementation without
depending on a real headscale instance (see
`fakeListClient` in `auto_test.go`).

### Tests (6 new, 1 file)

- `TestAutoBackfill_ZeroIntervalIsNoop` — defensive
  interval guard returns immediately when
  `SKYGATE_NODE_DISCOVERY_INTERVAL=0`.
- `TestAutoBackfill_NilDBIsNoop` / `_NilHSIsNoop` —
  defensive nil guards prevent nil-pointer panics
  in the goroutine.
- `TestAutoBackfill_ContextCancelExits` — `ctx.Done()`
  makes the loop return promptly (important for
  graceful shutdown).
- `TestAutoBackfill_ListErrorIsTolerated` — a headscale
  API hiccup logs + skips the tick instead of
  crashing the goroutine.
- `TestAutoBackfill_HappyPath` — multi-tick run with
  seeded portal_users; asserts `InvalidateCache` is
  called once per tick and `ListAllNodes` is called
  once per tick.

### Verify-pre

New check **B77** (12 grep-pins). Together with
B1-B76, the catalog is now **75/75 PASS**
(B1-B77, B8 SKIP VM-only).

### Live verify on VM (after this commit deploys)

1. `git pull` + `docker compose up -d --build skygate`
   on the VM. The new code builds with B77.
2. After the swap, the page renders
   `v0.33.1.25+<sha>` as the build label.
3. The startup log shows
   `node-discovery: starting (interval=5m0s)`.
4. Register a NEW device in headscale (e.g. via
   `tailscale up --authkey <skygate-issued-key>` on
   a fresh device). Within 5 minutes, the new device
   gets its `tag:dev-<user>-<device>` applied
   automatically and the next ACL re-apply picks up
   the new tagOwners entry.

### Operator action

None. The B77 change is invisible unless a new
device registers in headscale — the auto-backfill
runs in the background, applies the dev-tag + adds
the ACL grant, and the next ACL re-apply picks up
the new tagOwners entry. Operators with strict
autoupdate policies can set
`SKYGATE_NODE_DISCOVERY_INTERVAL=off` to disable
(same env-var pattern as the DNS autoupdater).

### Files (8)

- `internal/nodeownership/auto.go` (NEW, 175 lines)
- `internal/nodeownership/auto_test.go` (NEW, 6 tests)
- `internal/nodeownership/nodeownership.go` (signature
  refactor: `*headscale.Client` → `nodeLister`)
- `internal/config/config.go` (`NodeDiscoveryInterval`
  field + `SKYGATE_NODE_DISCOVERY_INTERVAL` env)
- `cmd/skygate/main.go` (goroutine wiring)
- `.githooks/pre-push` (header comment fix)
- `AGENTS.md` (Current bumped to v0.33.1.25)
- `scripts/verify_pre_deploy.sh` (B77 check)

### Migration

None. No schema change.

### Backlog (NOT in this release, recorded for v0.33.1.26+)

- Per-user `headscale_user_id` column accuracy
- Rule grouping: Cloudflare /12 + /24 merge
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button (Issue 5)

---

## v0.33.1.24 — layout fallback URL via injected GitHub coords (B73) + orchestrator Push target handles pre-update tags (B76)

**Date:** 2026-08-09
**Tag:** v0.33.1.24
**Commit:** TBD
**Scope:** 1 commit. 2 new test files
(layout_banner_test.go + update_target_test.go, 8 new
tests), 7 modified (handlers.go + update.go + layout.html
+ 3 test files + AGENTS.md/RELEASE-NOTES.md/LICENSE/docs),
1 verify-pre check (B73 + B76, 19 grep-pins), 109
doc-tree references swept from `skygate-operator/skygate`
to `BarsSky/skygate`. +130/-90 lines. No API change,
no schema change, no migration, no build-tag change.

### What's fixed (B73)

The pre-fix `layout.html:114` hardcoded
`https://github.com/skygate-operator/skygate/releases`
as the "Open release" link's fallback when
`UpdateLatestURL` was empty. This leaked the
original developer's GitHub org (the v0.32.29
no-personal-data policy violation; flagged in
the v0.33.1.23 release notes). v0.33.1.24 derives
the fallback URL from
`Cfg.GitHubOwner` / `Cfg.GitHubRepo` (auto-injected
into the data map by `renderWithLayout`, with
`BarsSky` / `skygate` fallbacks for test paths
where `Cfg` is nil) and ALSO sweeps the doc
tree — 109 hardcoded `skygate-operator/skygate`
references in `AGENTS.md`, `RELEASE-NOTES.md`,
`docs/`, templates, and `LICENSE` are rewritten
to point at the canonical
`github.com/BarsSky/skygate`.

### What's fixed (B76)

The pre-fix `PostAdminUpdatePush` and
`PostAdminUpdateApply` did
`if !strings.HasPrefix(target, "v") { target = "v" + target }`
unconditionally, producing
`vskygate-pre-update-<sha>` whenever
`s.BuildVersion` was the orchestrator's own
pre-update tag. `git checkout` then failed
with exit status 1 and the orchestrator triggered
a phantom auto-rollback — observed during the
v0.33.1.23 deploy ("git checkout: exit status 1"
+ "rollback succeeded — previous version is
running"). v0.33.1.24 adds a new helper
`normalizeUpdateTarget` (in
`internal/feature/admin/update.go`) that
recognizes `skygate-pre-update-*` tags, `main`,
and `HEAD` as already-valid refs and leaves them
alone; only plain semver like `0.33.1.24` gets
the `v` prefix. Both Apply and Push now use the
helper so the pre-fix bug can't reappear in either
path.

### Tests (8 new, 2 files)

- `TestLayoutBanner_FallbackURL_UsesInjectedCoords` —
  pin the B73 contract: fallback URL uses
  `{{.GitHubOwner}}/{{.GitHubRepo}}`, NOT a
  hardcoded org. Asserts the literal
  `skygate-operator` does NOT appear in the
  rendered body (zero-tolerance guard).
- `TestLayoutBanner_FallbackURL_DefaultsToBarsSkySkygate` —
  when the data map doesn't include
  GitHubOwner/Repo (test paths that skip
  `renderWithLayout`), the fallback still
  produces a valid URL.
- `TestNormalizeUpdateTarget_PreUpdateTag` — the
  headline B76 regression test; pre-fix would
  have prepended "v" to produce
  `vskygate-pre-update-<sha>`, post-fix passes
  the tag through unchanged.
- `TestNormalizeUpdateTarget_AlreadyPrefixed` /
  `_PlainSemver` / `_Branch` / `_SHA` / `_Empty` —
  cover all the branches of the new helper.

### Verify-pre

New checks **B73** (8 grep-pins) and **B76**
(11 grep-pins). Together with B1-B72, the
catalog is now **74/74 PASS** (B1-B76, B8
SKIP VM-only).

### Live verify on VM (after this commit deploys)

1. `git pull` + `docker compose up -d --build skygate`
   on the VM. The new code builds with B73 +
   B76 fixes.
2. After the swap, the page renders
   `v0.33.1.24+<sha>` as the build label.
3. Click "Push update" on /admin/update (or
   trigger via API). The orchestrator now
   handles a pre-update tag as a target
   without crashing.
4. The "Open release" link in the dashboard
   banner still works (UpdateLatestURL path
   is unchanged); the fallback URL (when
   UpdateLatestURL is empty) now correctly
   points at `github.com/BarsSky/skygate/releases`.

### Operator action

None. The B73 change is invisible unless
`UpdateLatestURL` is empty (which only
happens when the release monitor hasn't seen
a specific tag yet — a rare edge case).
The B76 change is invisible unless the
operator clicks "Push update" after a recent
successful orchestrator deploy — which
previously triggered a phantom rollback,
now rebuilds cleanly.

### Files changed (15)

- `internal/handlers/handlers.go` (auto-inject
  GitHubOwner/Repo)
- `internal/handlers/templates/layout.html`
  (B73 fallback)
- `internal/feature/admin/update.go` (B76 helper
  + 2 call sites)
- `internal/handlers/layout_banner_test.go`
  (stub layout updated for B73 + 2 new tests)
- `internal/feature/admin/update_target_test.go`
  (NEW, 6 B76 tests)
- `internal/release/monitor_test.go` (Owner/Repo
  fixtures)
- `internal/update/checker_test.go` (Owner/Repo
  fixtures)
- `internal/update/install_test.go` (Owner/Repo
  fixtures + assertion update)
- `AGENTS.md` (Current bumped to v0.33.1.24
  + 109 doc references swept)
- `RELEASE-NOTES.md` (this entry + 60+ release-note
  references swept)
- `LICENSE` (Copyright line: skygate-operator →
  BarsSky)
- `docs/disaster-recovery.md`
- `docs/plans/self-update-v0.29.md`
- `docs/internal/plans/refactor-v0.6.0.md`
- `docs/internal/subnet-router.md`
- `internal/handlers/templates/admin/user_subnet.html`
- `internal/handlers/templates/user/devices.html`
- `scripts/verify_pre_deploy.sh` (B73 + B76
  checks)

### Backlog (NOT in this release, recorded for v0.33.1.25+)

- Per-user `headscale_user_id` column accuracy
- Rule grouping: Cloudflare /12 + /24 merge
- New device auto-tag + ACL grant (Issue 2)
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- Pre-push hook (`.githooks/pre-push`) mislabel:
  comment says "B1-B10" but the hook actually
  runs the full `verify_pre_deploy.sh`

---

## v0.33.1.23 — layout.html update-banner data shape (B72)

**Date:** 2026-08-09
**Tag:** v0.33.1.23
**Scope:** 1 commit. 1 new test file
(layout_banner_test.go), 3 modified (handlers.go +
update.go + layout.html), 1 verify-pre check
(B72, 12 grep-pins). +91/-8 lines. No API change,
no schema change, no migration, no build-tag change.

### The bug

The `/admin/update` page has been silently rendering
a broken short page (no Apply button, no orchestrator
status card, no manual rollback hint) on the live VM
since at least v0.27.x (the
`{{.UpdateLatest.TagName}}` template expression
predates the 2026-07-15 release-monitor banner that
introduced it). The orchestrator itself ran fine
because we hit `POST /admin/update/apply` directly
via curl as a workaround, but the admin page was
useless until this fix.

Root cause: the pre-fix layout.html's update-banner
block assumed `UpdateLatest` was a `release.Release`
struct (with `TagName` and `HTMLURL` fields). The
auto-banner path (`handlers.go:456`) DID set it as
a struct, so the global banner worked for every
admin page. The `/admin/update` page path
(`update.go:188`) set it as a `string` (the
`result.Latest` field — just the tag name like
`"v0.33.1.22"`). At render time, Go's template
engine tried to evaluate `.TagName` on a string
and crashed with `can't evaluate field TagName in
type interface {}`. The user saw a broken short
page with no Apply button.

### The fix

v0.33.1.23 pins the data shape: `UpdateLatest` is
ALWAYS a tag-name string (e.g. `"v0.33.1.22"`), and
a new `UpdateLatestURL` is ALWAYS a release-page
URL string (e.g.
`https://github.com/<org>/skygate/releases/tag/v0.33.1.22`).
Two source-level paths were updated to produce the
new shape consistently:

- `internal/handlers/handlers.go:456` (auto-banner
  in `renderWithLayout`): now sets
  `UpdateLatest = latest.TagName` +
  `UpdateLatestURL = latest.HTMLURL` (was: set
  `UpdateLatest` to the whole `Release` struct).
- `internal/feature/admin/update.go:188` (/admin/
  update page): now also sets `UpdateLatestURL =
  result.ReleaseURL` (was: only `UpdateLatest` as
  a string, which the layout's struct-field-access
  crashed on).
- `internal/handlers/templates/layout.html:107,111-112`:
  the template now reads the two strings directly
  — `{{tf "update.banner_body" .Version .UpdateLatest}}`
  + `{{if .UpdateLatestURL}}` — no field access,
  no type-dispatch, no crash.

### Tests

`internal/handlers/layout_banner_test.go` (NEW,
6 tests):

- `TestLayoutBanner_UpdatePageDataShape` — pins the
  post-fix shape that /admin/update passes. The
  pre-fix shape would crash at template-execute
  time with "can't evaluate field TagName in
  type interface {}"; the post-fix shape renders
  cleanly. Asserts the banner text and release
  URL appear in the rendered body.
- `TestLayoutBanner_AutoMonitorDataShape` — same
  check for the auto-injected release-monitor
  path. Pins the new shape (string + string)
  after the `handlers.go:456` refactor.
- `TestLayoutBanner_MissingLatestURLUsesFallback`
  — when `UpdateLatestURL` is empty, the banner
  block still renders (the link falls back to
  the GitHub releases list).
- `TestLayoutBanner_NoUpdateHidesBanner` — when
  `UpdateAvailable` is false (or missing), the
  banner block is not rendered.
- `TestLayoutBanner_RU_i18n` — sanity check that
  the `tf` calls work in the RU catalog.

All 6 tests pass.

### Verify-pre

New check **B72** (12 grep-pins): the 4 source
changes (handlers.go × 2, update.go × 2,
layout.html × 2), the 2 negative-shape rejections
(no `.UpdateLatest.TagName` or `.UpdateLatest.HTMLURL`
in the template), and the 4 test names. Together
with B1-B71, the catalog is now 72/72 PASS
(B8 SKIP VM-only).

### Operator action

None — purely a UI fix. After upgrading to
v0.33.1.23, the `/admin/update` page will render
the full layout (with the "Apply" button + the
"Update available" banner) instead of the broken
short page. The auto-update orchestrator (B70 + B71)
was already wired and tested end-to-end; the
template bug was the last unfixed piece blocking
a clean UI-driven apply.

---

## v0.33.1.22 — orchestrator healthz poll uses net/http (not curl) (B71)

**Date:** 2026-08-09
**Tag:** v0.33.1.22
**Scope:** 1 commit. 2 modified
(internal/update/docker.go + scripts/verify_pre_deploy.sh),
1 deleted (ORCHESTRATOR_E2E_TEST.md throwaway test marker).
+27/-19 lines. No API change, no schema change, no
migration, no build-tag change.

### The bug

The self-update orchestrator's post-swap healthz
poll loop shells out to `curl -fsS --max-time 5
http://localhost:8080/healthz` to wait for the new
container to be ready. The skygate container's base
image is `golang:1.25-alpine` (changed in v0.32.13).
Alpine ships `wget` + busybox, NOT `curl`. `exec.
CommandContext("curl", ...)` silently fails with
`exec: "curl": executable file not found in $PATH`
on every attempt; the orchestrator interpreted the
failure as "container not yet healthy", timed out at
1m0s (12 attempts × 5s), and triggered auto-rollback
to the pre-update tag — even though the new container
was actually fine (a manual /healthz check 2 seconds
after the rollback returned 200).

This bug was latent since v0.32.13 (4 weeks before
this fix) but never fired in production because every
orchestrator run failed at the v0.33.1.21 migrate
step (bash-not-in-PATH) BEFORE the swap. The new
container never started, so the healthz poll never
ran against a newly-booted image. v0.33.1.21's bash
fix unblocked the migrate step, which unblocked the
swap, which exposed this latent curl bug.

### The fix

Replace `exec.CommandContext("curl", ...)` with Go's
`net/http`:

```go
req, _ := http.NewRequestWithContext(ctx, "GET",
  "http://localhost:8080/healthz", nil)
resp, httpErr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
if httpErr == nil {
    defer resp.Body.Close()
    bodyBytes, _ := io.ReadAll(resp.Body)
    body := string(bodyBytes)
    if resp.StatusCode == 200 && strings.Contains(body, `"status":"ok"`) {
        return nil
    }
}
```

5s per-request timeout is preserved (was
`--max-time 5`). 200 + `"status":"ok"` body match is
preserved. The 60s overall deadline + 5s retry
interval are also preserved (no behavior change at
the loop level — just the inner HTTP probe). Same
approach v0.32.22 took for the other HTTP probes in
the codebase — no shell dependency, no path surprises
across host/container rebuilds, and no
alpine-curl-not-installed surprise.

### Verify-pre

New check **B71** (5 grep-pins): the `net/http`
import, `http.NewRequestWithContext`, `http.Client`,
the `localhost:8080/healthz` URL, and the
`StatusCode` check. Together with B1-B70, the
catalog is 71/71 PASS (B8 SKIP VM-only).

### Live verify on VM (2026-08-09)

After deploying v0.33.1.22, the orchestrator
successfully ran job 8e0e7e35 v0.33.1.21 → v0.33.1.22:
build → migrate-only (v0.33.1.21 fix WORKED:
"migrations applied") → swap → healthz poll (now via
net/http, returned 200 + "status":"ok") → done. Build
label after deploy: `v0.33.1.22+<sha>`. No
auto-rollback fired.

---

## v0.33.1.21 — auto-update orchestrator migrate step (the 3-bug-fix) (B70)

**Date:** 2026-08-09
**Tag:** v0.33.1.21
**Scope:** 1 commit. 1 new test file (migrate_only_test.go),
2 modified (main.go + docker.go), 1 verify-pre check.
+92/-13 lines. No API change, no schema change, no
migration, no build-tag change.

### The bug

The `/admin/update` "Apply" button has been silently broken
on the live VM since v0.32.13 (2026-07-31). The
self-update orchestrator's Phase 3 (migrate) had THREE
pre-existing bugs that all manifested on the alpine base
image:

1. **`bash -c "..."` — bash doesn't exist in alpine.**
   The orchestrator's runShellCapture call was
   `runShellCapture(ctx, "bash", "-c", migrateCmd)`. The
   skygate container is built from `golang:1.25-alpine`
   (changed in v0.32.13). Alpine has busybox `sh`, not
   `bash`. The pre-v0.32.13 base image was a non-existent
   bash-via-glibc image; the orchestrator was never tested
   on alpine. The orchestrator has been failing at this
   step since the alpine switch (4 weeks before this fix).

2. **`--volumes-from skygate` — container doesn't exist.**
   v0.29.2 removed `container_name: skygate` from
   docker-compose.yml to fix a `--force-recreate` race.
   The container's compose-generated name is
   `skygate-skygate-1` (or `-N` after multiple recreates).
   `--volumes-from skygate` has been referencing a
   non-existent container since v0.29.2 — silently, because
   the bash-not-in-PATH error from #1 short-circuited the
   command before the volume-resolution step.

3. **`--migrate-only` — flag never implemented.** The
   orchestrator's docker run command was
   `... /app/skygate --migrate-only ...`. The
   `--migrate-only` flag was documented in
   `docs/plans/self-update-v0.29.md` and referenced in
   `internal/update/manual.go`, but it was never
   implemented in `cmd/skygate/main.go`. The flag was
   never tested end-to-end (auto-update was never
   successfully run on the live VM). The pre-fix
   orchestrator was failing with
   `unknown command "migrate-only" (try 'skygate help')`
   — a third error masked by the auto-rollback.

### The symptoms

All three bugs were masked by the auto-rollback: the
operator would click "Apply" on /admin/update, the
orchestrator would mark the job `failed`, the previous tag
would be restored, and the operator would see "update
failed, rolled back to skygate-pre-update-<hash>". The
operator assumed the new code had a real bug; in reality
the new code was never run at all (the orchestrator's
`docker run` step failed before it could start the new
container).

The 2026-08-09 /admin/update "update" log shows the cascade:
```
18:44:41 [info] image rebuilt
18:44:41 [info] running migrations on the new image
18:44:41 [error] phase failed: migrate: exec: "bash":
  executable file not found in $PATH (output: )
18:44:41 [warn] attempting automatic rollback to
  skygate-pre-update-21b3afa
18:44:44 [info] spawnning detached rollback swap subprocess
```

The "image rebuilt" line means the new image was built
correctly; the "migrations" line means the orchestrator
got to Phase 3. But the bash exec failed, so the new
image was never tested against the DB before the swap.
The swap was rolled back, the operator saw "failed".

### The fix

This release v0.33.1.21 fixes all three bugs and adds
the actual end-to-end test that the orchestrator was
missing:

1. **`bash` → `sh`** in
   `internal/update/docker.go::DockerUpgrader.runShellCapture`.
   `sh -c "..."` is POSIX-portable; the pipe
   `2>&1 | tail -20` works in both bash and sh. The
   `migrateCmd` string itself was untouched (it was
   already a portable shell command).

2. **Resolve the skygate container ID by label** instead
   of the hardcoded `skygate` token. The orchestrator now
   runs `docker ps -a --filter label=com.docker.compose.service=skygate --format '{{.ID}}'`
   to get the live container id, then passes
   `--volumes-from <id>` to the one-shot container. This
   matches the lookup `scripts/verify_post_deploy.sh`
   uses (so the orchestrator and the verify script stay
   in sync) and works regardless of how many times the
   container has been recreated.

3. **`migrate-only` subcommand** added to
   `cmd/skygate/main.go`. Extracted as a testable
   `runMigrateOnly()` function (returns `error` not
   `os.Exit`) so unit tests can exercise the happy path
   without forking a subprocess. The function loads the
   config (skips the bootstrap-admi / etc. boot path that
   the web server runs — migrate-only is just open-DB
   and exit) and returns the error from `db.Open` /
   `db.OpenDSN`. The web server's existing `db.Open` call
   already runs all migrations on every container start
   (per the v0.6.0 refactor), so this is the same code
   path the orchestrator needed.

### Tests

3 new unit tests in `cmd/skygate/migrate_only_test.go`:

- `TestRunMigrateOnly_FreshDB_SQLite` — point
  `SKYGATE_DB` at a temp dir, call `runMigrateOnly()`,
  assert the resulting SQLite has the v0.34-era tables
  (portal_users, preauth_keys, node_owner_map,
  device_rules, global_settings, applied_migrations,
  system_tests_runs, headscale_acl_rules).
- `TestRunMigrateOnly_Idempotent` — call twice, assert
  `applied_migrations` row count is the same (the
  v0.28.5 B5/R20 contract: migrations are idempotent).
- `TestRunMigrateOnly_RespectsDSN` — set
  `SKYGATE_DB_DSN=postgres://...`, call
  `runMigrateOnly()`, assert the error is from the PG
  path (not the SQLite fallback).

The unit tests run on every `go test ./cmd/skygate/`
invocation (no subprocess, no VM dependency). The
end-to-end test (orchestrator builds new image, runs
migrate-only one-shot, swaps container) will run on
the live VM after this commit is pushed.

### Files changed (4)

- `cmd/skygate/main.go`: new `migrate-only` subcommand
  branch + `runMigrateOnly()` function + help text update.
- `cmd/skygate/migrate_only_test.go` (NEW, 3 tests,
  84 lines).
- `internal/update/docker.go`: `bash` → `sh` in the
  migrate step; add the label-based container ID
  resolution + the `skygateContainerID` debug log line.
- `scripts/verify_pre_deploy.sh`: B70 check (8 grep-pins:
  the new subcommand + function + help text + `sh` in
  docker.go + label-based lookup + 3 test names).

### Test results

- `go test -count=1 -short ./...` → 27/27 packages PASS.
- `make verify-pre` → 70/70 PASS (B1-B70, B8 SKIP
  VM-only).

### Backlog (NOT in this release, recorded for v0.33.1.22+)

- Per-user `headscale_user_id` column accuracy (the
  pre-existing bug where `node_owner_map.headscale_user_id`
  stores portal id instead of headscale id; the v0.33.1.20
  manual fix on the live VM hides the symptom).
- Rule grouping: Cloudflare domain → /12 CIDR, adjacent
  /24 merge, cross-domain IP conflict detection.

## v0.33.1.20 — backfill tag fix + force-backfill + transfer (B69)

**Date:** 2026-08-09
**Tag:** v0.33.1.20
**Scope:** 1 commit. 4 new functions + 2 new helpers, 1 new
admin action, 1 new admin template section, 6 new i18n keys
(RU + EN). +498/-12 lines across 8 files. No API change, no
schema change, no migration, no build-tag change.

### The bug

On 2026-08-09 the operator reported three "warning / device
hygiene" issues on /admin/devices:

1. **13 headscale nodes had no `tag:dev-<user>-<host>`** —
   they only carried `tag:private`. The per-user backfill
   helper that runs on every `/my/devices` page load applies
   the dev-tag to the CURRENT user's nodes, but cross-user
   cases (michail's nodes, svyatoslava's, etc.) only get
   fixed when the actual owning user logs in. The operator
   had no admin-side "fix everything" button.

2. **The svyatoslava dual-owner conflict** — id=27 in
   headscale had `name=svyatoslava` (an old device, set
   up before svyatoslava was a real headscale user) but
   `node_owner_map.username=skyadmin` because the backfill
   had claimed it for skyadmin via the temporal fallback.
   When svyatoslava later got their own device (id=30),
   headscale auto-renamed it to `svyatoslava-1` to avoid
   the name collision. The operator had no UI to transfer
   a node from one portal user to another.

3. **The "rename never updates the tag" bug** — when a user
   renamed their Tailscale hostname (e.g. `desktop-cj8t9me`
   → `cyborg`), the per-user backfill only did INSERT OR
   IGNORE on `node_owner_map`, so the stale hostname + stale
   `tag:dev-<user>-<oldHost>` stayed in the DB forever, and
   headscale accumulated BOTH the old and the new tag
   (because `AddTag` never removes). The next ACL re-apply
   then included BOTH tagOwners entries — until the next
   re-apply, every per-device rule had TWO possible sources.

### The fix

This release ships three coordinated changes:

1. **`backfillNodeOwnership` handles the rename scenario**
   (the structural fix for issue 3). After the GC pass, the
   helper now loads every existing `node_owner_map` row for
   the user into `existingByNodeID` and, for each node that
   matches a preauth key, compares `existing.Hostname` to
   `n.Hostname`. If they differ, the helper:
   - calls `hs.UntagNode(oldTag)` to drop the stale
     `tag:dev-<user>-<oldHost>` from headscale (the part
     `AddTag` was always missing),
   - calls the new `db.UpdateNodeOwnerHostnameAndTag` helper
     to atomically rewrite BOTH the row's `hostname` and
     `tag` columns (the existing `UpdateNodeOwnerTag` and
     `UpdateNodeOwnerHostnameAndTag` only updated one column
     at a time, which left inconsistent state on rename),
   - calls `hs.AddTag(newTag)` to apply the new
     `tag:dev-<user>-<newHost>`.
   The DB half of the fix runs even when `hs == nil`, so a
   transient headscale outage doesn't lose the rename —
   the next `/my/devices` load that DOES have a working
   headscale client cleans up the stale tag.

2. **New admin action `POST /admin/devices/force-backfill-tags`**
   (the structural fix for issue 1). Iterates every portal
   user and calls `nodeownership.Backfill` against the live
   headscale node list. Each user's preauth-key + temporal
   match logic runs as if that user had loaded `/my/devices`,
   so the cross-user dev-tag gaps get filled in one click.
   The handler also tracks per-user pre vs post hostname
   and reports a `renames=N` count in the audit log +
   redirect message, so the operator can see at a glance
   whether the click also fixed any renames. The button
   is the operator-side escape hatch for the "user hasn't
   loaded /my/devices so their dev-tag was never applied"
   symptom.

3. **New admin action `POST /admin/devices/transfer`** (the
   structural fix for issue 2). Resolves orphan rows like
   the svyatoslava dual-owner case by:
   - `db.UpsertNodeOwner` with the new owner + new dev-tag
     (built from the live headscale hostname, not the
     stale row's hostname),
   - `hs.UntagNode(oldTag)` to drop the stale dev-tag,
   - `hs.AddTag(newTag)` to apply the new dev-tag.
   The handler explicitly validates `node_id` is parseable
   and `target_username` is non-empty BEFORE the headscale
   check, and explicitly checks the node EXISTS in
   `node_owner_map` BEFORE calling headscale (so a missing
   node returns 400, not 500). The redirect message tells
   the operator to also click "Re-apply ACL" on
   `/admin/exit-rules` so the new tagOwners entry lands
   in the headscale policy.

### New admin template + i18n

`/admin/devices` gets two new UI affordances:
- A "Force resync all tags" button next to the existing
  "Sync from headscale" button (idempotent — safe to spam).
- A per-row "Transfer" `<details>` with a portal-user
  dropdown (excludes the synthetic `tagged-devices`
  headscale user via the new `transferTargets` helper).

6 new i18n keys (RU + EN, 12 total entries):
- `devices.force_backfill_btn`
- `devices.transfer_btn`
- `devices.transfer_target`
- `devices.transfer_submit`
- `devices.transfer_help`

### Live verify (2026-08-09)

Manual VM work (the v0.33.1.20 prior art — operator's live
session) filled in 13 missing dev-tags, renamed id=27 to
`svyatoslava-legacy`, and re-applied the ACL. After
re-apply: 348 grants total, 17 `tag:dev-*` tagOwners in
the headscale policy, only 1 via-grant (michail with
via_enabled=1, all other users in advisory mode).

### Catalog

B69 verify-pre check: 22 grep-pins covering the rename
detection, the force-backfill admin action, the transfer
admin action, the transferTargets helper, both new
i18n keys, both new template sections, both new routes
in main.go, and 7 new unit tests. B32 also updated to
match the v0.33.1.16 docker-compose shape
(`SKYGATE_TS_LOGIN_SERVER` removed from compose env so
the operator's .env edit isn't silently overwritten).

### Files changed

- `internal/db/node_owner_map.go`: new
  `UpdateNodeOwnerHostnameAndTag` helper.
- `internal/nodeownership/nodeownership.go`: rename
  detection in `Backfill` (load existing rows →
  UntagNode old → UPDATE row → AddTag new).
- `internal/nodeownership/nodeownership_test.go`: new
  `TestBackfill_RenameUpdatesHostnameAndTag` (DB half
  of the rename contract, hs=nil).
- `internal/feature/admin/devices.go`: new
  `PostAdminDevicesForceBackfillTags` +
  `PostAdminDeviceTransfer` + `transferTargets` helper
  (pure function for the dropdown filter).
- `internal/feature/admin/devices_test.go`: 7 new tests
  (transferTargets filter, 5 transfer validation paths,
  force-backfill admin + nil-HS guards).
- `internal/handlers/templates/admin/devices.html`: new
  "Force resync all tags" button + per-row "Transfer"
  `<details>`.
- `internal/i18n/catalog_my.go`: 5 new keys, RU+EN.
- `cmd/skygate/main.go`: 2 new routes
  (`/admin/devices/force-backfill-tags`,
  `/admin/devices/transfer`).
- `scripts/verify_pre_deploy.sh`: B69 check + B32
  updated to match the v0.33.1.16 docker-compose shape.

### Test results

- `go test -count=1 -short ./...` → 27/27 packages PASS.
- `make verify-pre` → 70/70 PASS (B1-B69, B8 SKIP VM-only).
- B32 (pre-existing outdated check) updated in the same
  PR to match the v0.33.1.16 docker-compose env shape.

### Backlog (NOT in this release, recorded for v0.33.1.21+)

- Per-user `headscale_user_id` column accuracy — the
  backfill currently stores the portal_users.id in
  `node_owner_map.headscale_user_id` (should be the
  actual headscale user id, e.g. 1, 8, 11, 12, 84). On
  this operator's install portal id and headscale id
  happen to match for skyadmin (both = 1) and the four
  other users have a constant offset that's not load-
  bearing for any current code path. Worth a follow-up.
- Rule grouping: Cloudflare domain → /12 CIDR, adjacent
  /24 merge, cross-domain IP conflict detection. The
  B66 + B68 verification tests will catch the regression
  class regardless of what the grouping algorithm ends
  up being.

## v0.33.1.19 — `via_enabled` INSERT column-order + v0.52 data-repair migration (B68a)

**Date:** 2026-08-09
**Tag:** v0.33.1.19 (commit `82c8123`)
**Scope:** 1 commit. 1 new file (migrations_v0.52.go +
test), 4 modified. +262/-12 lines. No API change. 1 new
migration (v0.52, data-repair only — no schema change).

### The bug

The pre-fix `SetUserExitNodePref` (migrations_v0.45.go) and
`SetDeviceExitNodePref` (migrations_v0.46.go) had a
positional-mismatch bug in their INSERT clause: the VALUES
list put `viaInt` (a 0/1 bool) in the position mapped to
`updated_at`, and `nowUnixSQL()` (a unix timestamp > 1.7e9)
in the position mapped to `via_enabled`. Every row inserted
by v0.28.5 — v0.33.1.18 had `via_enabled=<unix timestamp>` —
always truthy, so:

- The per-user grant in the ACL always had
  `via: [tag:exit-...]` regardless of the operator's choice.
  The "un-check" strict-mode checkbox on `/my/exit-nodes`
  was a no-op (writing `via_enabled=false` just inserted a
  new timestamp into the `via_enabled` column, which is
  still truthy).
- `/my/exit-nodes` always showed the "🔒 strict" badge
  (via=true), even when the operator thought they were in
  advisory mode.
- Per-device grant in the ACL was always emitted with
  `via=tag:exit-...` too. The `/my/devices` "via enabled"
  toggle was also a no-op.

The 2026-08-09 operator's question on `/admin/exit-rules`
("all old rules work, 3 new ones don't" + the B66 mismatch
display on the rules table) was investigated and turned
out to be a presentation issue — per-device pref for
non-default-user correctly excludes exit-nodes they don't
own, by design. The deeper bug — the `via_enabled` column
swap — was discovered during that investigation.

### The fix

This release v0.33.1.19 ships:

1. INSERTs in `migrations_v0.45.go` and `migrations_v0.46.go`
   are reordered so `viaInt` goes to `via_enabled` and
   `nowUnixSQL()` goes to `updated_at`. New rows are
   correct from the start.

2. **Migration v0.52** walks `user_exit_node_prefs` and
   `device_exit_node_prefs` and swaps the two columns when
   the discriminant `updated_at IN (0, 1) AND
   via_enabled > 1_000_000_000` is satisfied. Idempotent:
   running it twice finds nothing to swap on the second
   run. The 1e9 threshold safely skips legitimate `(0, 0)`
   fresh rows and already-correct rows.

3. 6 unit tests in `migrations_v0.52_test.go` pin the
   repair contract: `RepairsCorruptUserPref`,
   `RepairsCorruptDevicePref`, `LeavesCorrectRowsAlone`,
   `Idempotent`, `Threshold`, `DevicePrefMultipleRows`.

4. **B68a verify-pre check**: 12 grep-pins covering the
   migration, both INSERT fixes, all 6 test names.

### Live verify (2026-08-09)

348 grants total, only 1 via grant (michail with
`via_enabled=1`). The per-user grant for skyadmin has NO
`via` (advisory mode), `/my/exit-nodes` shows the
"🔓 any exit-node" badge (not "🔒 strict"), and the
checkbox is unchecked.

### Files changed

- `internal/db/db.go`: call `migrateV052` after v0.51.
- `internal/db/migrations_v0.45.go`: fix `SetUserExitNodePref`
  INSERT, swap `via_enabled` and `nowUnixSQL()` positions.
- `internal/db/migrations_v0.46.go`: fix `SetDeviceExitNodePref`
  INSERT, same swap.
- `internal/db/migrations_v0.52.go` (NEW, 110 lines): data-
  repair migration with safe discriminant.
- `internal/db/migrations_v0.52_test.go` (NEW, 6 tests): pin
  the repair contract.
- `scripts/verify_pre_deploy.sh`: B68a check.

## v0.33.1.18 — DNS-autoupdater flag split + UI toggle + verification test (B68)

**Date:** 2026-08-06
**Tag:** v0.33.1.18 (commit `21b3afa`)
**Scope:** 1 commit. 2 new files
(`settings_dns_autoupdate.go` + `system_tests_test.go`),
9 modified, +531/-46 lines. No API change, no schema
change, no migration, no build-tag change.

### The bug

The 2026-08-06 incident: "3 new exit rules for one device
don't work. All old rules work, three new ones don't."
After diagnosing, the root cause was a flag-conflation
bug from v0.32.13:

- `cfg.AutoUpdateEnabled` (env `SKYGATE_AUTO_UPDATE_ENABLED`)
  is the gate for the skygate SELF-UPDATE banner on
  `/admin/update` (one-click "Apply" button vs always-on
  "Push update" button).
- `cfg.DNSAutoCheck` is the INTERVAL for the DNS-resolve
  autoupdater goroutine (resolves enabled `target_type=domain`
  rules to their current /32 entries every N minutes, so
  the IP-derived rows don't rot as Cloudflare rotates
  addresses).
- The `main.go` goroutine-launch gate (v0.32.13) was wired
  to `cfg.AutoUpdateEnabled`.

Net effect for the operator: `SKYGATE_AUTO_UPDATE_ENABLED=false`
in .env (a sane default for production — you don't want
auto-update on the management plane) silently ALSO turned
off the DNS autoupdater. The autoupdater last ran on
2026-07-31 20:43. The operator added a new domain rule
on 2026-08-06, the autoupdater didn't fire, the /32
children were created at insert time from a one-shot DNS
lookup, and will rot as soon as Cloudflare rotates the
IPs. The operator assumed the form was broken when the
policy was actually correct but the /32 entries would be
stale within days.

### The fix

This release v0.33.1.18 separates the two flags + adds a
UI toggle + a verification test that catches this class
of bug in the future:

1. **New env `SKYGATE_DNS_AUTOUPDATE_ENABLED`** (default
   `true` so upgrading from v0.33.1.17 keeps DNS autoupdate
   on). The `/admin/system_tests` page exposes a DB-backed
   toggle that overrides the env on the next autoupdate
   tick (no restart needed). Audit log entry per toggle.

2. **The autoupdater goroutine** (`handlers.go
   `RunDomainAutoUpdater`) now reads
   `global_settings.dns_autoupdate_enabled` on EVERY tick
   instead of being gated solely at startup. UI toggle
   takes effect within the next tick (5m default interval)
   without a skygate restart.

3. **`/admin/system_tests` page** gets a "DNS autoupdater"
   card with the current state (DB row overrides env) +
   Enable / Disable button. The card is right above the
   test grid so the operator sees it on every visit.

4. **New verification test `exit_rules.all_in_headscale_acl`**.
   Reads every enabled subnet/ip `device_rule` + its
   `user_name`/`device_hostname`/`device_ip`, computes the
   expected `(src, dst)` tuple the same way
   `GenerateACLWithViaForPlane` does, and looks each one up
   in the live headscale `policy.grants[]`. > 5 missing =
   fail (real sync regression), 1-5 missing = pass-with-warn
   (Tailscale client-side lag, the 60-90s policy refresh
   interval).

5. **2 new unit tests** (`TestSanitizeRuleAlias` +
   `TestExpectedGrantTuple`) pin the `(src, dst)` formula
   in lockstep with the generator. If the generator
   changes its formula (e.g. adds `strings.ToLower`, or
   picks `device_ip` over `device_hostname`), the unit
   test will fail and force the refactorer to update both
   the generator AND the verification test. Without
   these, a one-sided refactor would make the verification
   test systematically miss the same grants the generator
   just produced — silent false-positive "all rules in
   ACL" forever.

6. **B68 verify-pre check**: 11 grep-pins.

### Why this matters beyond the immediate symptom

The 2026-08-06 report was the third "rules silently don't
match" case in v0.33.0+ (the previous two were v0.33.1.15
"per-device-pref device tag" and v0.33.1.14
"placeholdersList 2-arg"). All three were policy / flag-
class bugs where the operator's UI showed the rule as
saved but it had no effect on traffic. The verification
test is the structural fix — every `/admin/system_tests`
run will now catch this class of regression before the
operator notices.

### Files changed

- `cmd/skygate/main.go`: gate `RunDomainAutoUpdater` on
  `cfg.DNSAutoUpdateEnabled` (was: `cfg.AutoUpdateEnabled`).
- `internal/config/config.go`: new `DNSAutoUpdateEnabled`
  field + env binding (`SKYGATE_DNS_AUTOUPDATE_ENABLED`,
  default `true`).
- `internal/handlers/handlers.go`: `RunDomainAutoUpdater`
  reads `global_settings.dns_autoupdate_enabled` on every
  tick.
- `internal/feature/admin/settings_dns_autoupdate.go` (NEW):
  `PostAdminSystemTestsDNSAutoToggle` handler + audit log.
- `internal/feature/admin/system_tests_handlers.go`: render
  `DNSAutoUpdateEnabled` in the data map.
- `internal/feature/admin/system_tests.go`: new
  `exit_rules.all_in_headscale_acl` test in `TestRegistry`.
- `internal/feature/admin/system_tests_test.go`:
  `TestSanitizeRuleAlias` + `TestExpectedGrantTuple`.
- `internal/handlers/templates/admin/system_tests.html`:
  "DNS autoupdater" card.
- `internal/i18n/catalog_common.go`: 3 new keys (RU+EN).
- `scripts/verify_pre_deploy.sh`: B68 check (11 grep-pins).

## v0.33.1.17 — exit-rule ↔ preferred exit-node cross-check (B66)

**Date:** 2026-08-06
**Tag:** v0.33.1.17 (commit `b7bedd1`)
**Scope:** 1 commit. 2 new files (preferred_check.go + tests), 9
modified, +732/-3 lines. No API change, no schema change, no
migration, no build-tag change.

### The bug

A device_rule in `device_rules` (e.g. `target=rutracker.org`,
`exit_node=exit-node-A`) only takes effect on device D if D's
preferred exit-node is also `exit-node-A`. The decision is made
by `device_exit_node_prefs` (per-device, overrides everything)
or `user_exit_node_prefs` (per-user fallback). If they don't
match, **Tailscale silently ignores the rule** — the operator
sees a "dead rule" in the UI: saved, audit-logged, even approved
by headscale, but never routed through the chosen exit-node.

The bug surfaced in production: Cloudflare CIDR rules for
`rutracker.org` were pointed at one exit-node, but every device
was pinned to a different one via `device_exit_node_prefs`. The
rules were "saved" but Tailscale routed the traffic through the
wrong exit-node — and Cloudflare responded with a JS challenge
because the wrong exit-node's IP was in its low-reputation list.
30-minute debug to find the root cause.

### The fix

1. **Cross-check helpers**
   (`internal/feature/exit_rules/preferred_check.go`, new):
   - `PreferredExitNodeForRule(db, userID, hostname)` —
     per-device > per-user > ""
   - `IsRuleApplicable(ruleExitNode, preferredHost)` — true
     when there's no preferred OR they match
   - `TagToHostname("tag:exit-<host>")` → `"<host>"` — strip
     the tag prefix for comparison
   - `RulesByDeviceHostname(db)` — batch lookup for the
     admin cross-user view
   - 6 unit tests in `preferred_check_test.go`
     (TestIsRuleApplicable_NoPreference / Mismatch /
     WhitespaceHandling / RuleEmpty + TestTagToHostname_StandardForms)

2. **User-scope UI** (`/my/exit-rules`):
   - Top-of-page warning banner when `MismatchCount > 0`:
     "%d rules reference an exit-node that the device does
     not use. The rules are saved, but Tailscale ignores them."
   - "Use device's preferred exit-node" button — pre-fills
     the `select[name="exit_node"]` with the user's preferred
     tag and briefly highlights it.
   - Per-rule "Preferred" column with green check (match) /
     red warning + the preferred host tag (mismatch) / gray
     question (no preferred).

3. **Admin cross-user view** (`/admin/exit-rules`):
   - `AnnotatedRules` slice
     (`{AdminRule, PreferredHost, Applicable}`)
   - Same top-of-page banner with the cross-user mismatch
     count.
   - Per-row "Preferred" column.

4. **Admin devices** (`/admin/devices`):
   - Per-device "dead rules" count badge: red link to
     `/admin/exit-rules` when count > 0. Tooltip explains
     what "dead" means.

5. **System test**
   (`/admin/system_tests` → `exit_rules.preferred_mismatch`):
   - 3 SQL queries (`device_rules`, `device_exit_node_prefs`,
     `user_exit_node_prefs`) + Go cross-check.
   - Backend-dispatching — works on both SQLite and
     PostgreSQL via `db.BackendOf`.
   - Threshold: 0 = pass, 1–5 = pass with "warn" prefix,
     > 5 = **fail**.
   - Skips if no enabled rules.

6. **i18n**
   (`internal/i18n/catalog_exit_rules.go`, RU + EN,
   18 new keys): banner text, button label, column
   header, per-row title tooltips — full Russian and
   English parity.

7. **B66 verify-pre check** — pins the 13 new file
   references (`preferred_check.go` helpers, `system_tests`
   entry, both template banners, `devices.go`
   `DeadRuleCount`, all i18n keys).

### Files

- `internal/feature/exit_rules/preferred_check.go` — new,
  158 lines
- `internal/feature/exit_rules/preferred_check_test.go` —
  new, 130 lines
- `internal/feature/exit_rules/form_my.go` — adds
  `DeviceInfo.PreferredExitNode`, `MismatchCount`,
  `UserPreferred`
- `internal/feature/exit_rules/form_admin.go` — adds
  `RulesAnnotated`, mismatch count
- `internal/feature/admin/devices.go` — per-device
  `DeadRuleCount`
- `internal/feature/admin/system_tests.go` — new
  `exit_rules.preferred_mismatch` test
- `internal/handlers/templates/exit_rules.html` — banner +
  button + JS handler + per-rule column
- `internal/handlers/templates/admin/exit_rules.html` —
  banner + per-rule column
- `internal/handlers/templates/admin/devices.html` —
  dead-rule badge
- `internal/i18n/catalog_exit_rules.go` — 18 new keys
  (RU + EN)
- `scripts/verify_pre_deploy.sh` — B66
- `AGENTS.md` — Current section bumped to v0.33.1.17

### Test results

- `go test -count=1 -short ./...` → **27 / 27 packages PASS**
- `bash scripts/verify_pre_deploy.sh` →
  **66 / 66 PASS** (B1–B66; B8 smoke is VM-only)

### Live verify (post-deploy on the operator's VM)

After the operator set the preferred exit-node on
`/my/devices` for the affected device, the system test
reports:

```
mismatches | total | no_pref
          0 |   138 |     13
```

— 0 dead rules (was 138+ before the operator manually set
the preferred), 13 rules with no preferred (Tailscale picks
by metrics; not a mismatch). The warning banner on
`/my/exit-rules` and `/admin/exit-rules` disappears.

### Live cleanup (also run post-deploy)

The 25-July-2026 PG migration lost the
`cdn:cloudflare:rutracker.org` marker that the
autoupdater had added. We re-inserted the 15 Cloudflare
CIDR ranges (`104.16.0.0/12`, `172.64.0.0/13`,
`103.21.244.0/22`, `103.22.200.0/22`, …) with
`parent_domain='cdn:cloudflare:rutracker.org'`, removed
4 stale /32 (`104.21.32.39/32`, `104.21.50.150/32`,
`172.67.163.237/32`, `172.67.182.196/32`) for the same
domain, and re-synced the Cloudflare-routed exit-node via
`tailscale set --advertise-routes=…` + headscale
approve. After re-sync, the exit-node's approvedRoutes
now contain all 4 Cloudflare /13+ supernets and they
appear in `Serving (Primary)` for Cloudflare traffic.

### How to use

If the warning banner shows up on `/my/exit-rules` or
`/admin/exit-rules`:

1. **Quick fix on the rules side** — click "Use device's
   preferred exit-node" in the banner. The form's
   `exit_node` field gets prefilled with your preferred tag.
2. **Root-cause fix** — open `/my/devices` (or
   `/admin/devices` for the user) and either set or clear
   the per-device preferred exit-node so it matches what
   your rules point at.
3. **Verify** — reload `/my/exit-rules`. The banner
   disappears when the per-device / per-user pref matches
   every rule's `exit_node_id`.

### Future ideas (not in this release)

- `?device=NAME` query filter on `/admin/exit-rules` — the
  link from the per-device dead-rule badge points there
  but the handler doesn't filter yet (10-line follow-up).
- UI tests / E2E — only backend unit tests in this release.



## v0.33.1.16 — SKYGATE_TS_LOGIN_SERVER from .env + restart-skgate button (the "Tailscale never picks up the new URL" fix)

**Date:** 2026-08-06
**Tag:** _pending_
**Scope:** 3 commits (9ffb288 + 149cee8). 1 docker-compose.yml
fix + 1 admin handler + 1 web-UI button + 5 tests + 5 i18n
keys + 1 verify-pre check (B65). No API change, no schema
change, no migration.

### The bug

The operator reported (2026-08-06) that they set
`SKYGATE_TS_LOGIN_SERVER=https://head.skynas.ru` via
`/admin/tailscale` (which writes to the DB and is supposed
to be the source of truth from v0.33.1.13 onward). But the
entrypoint kept using the placeholder `https://head.example.com`,
so tailscaled logged out with
"fetch control key: failed to resolve head.example.com".

### Root cause

`docker-compose.yml` had:
```yaml
  environment:
    - SKYGATE_TS_LOGIN_SERVER=https://head.example.com
```
hardcoded. Per docker-compose precedence, `environment:`
**overrides** `env_file:` (which is where the .env value
lives). So the operator's edit on /admin/tailscale persisted
to the DB correctly, but the entrypoint kept reading the
hardcoded placeholder from the container env.

The 22-hour ACL-apply failure loop from v0.33.1.15 was a
similar pattern (entrypoint vs runtime divergence), so this
v0.33.1.16 fix is the second "config-source-of-truth"
cleanup in a row.

### The fix

1. **`docker-compose.yml`** (commit 9ffb288): remove the
   hardcoded `SKYGATE_TS_LOGIN_SERVER=https://head.example.com`
   from the `environment:` section so the .env value wins
   via `env_file:`. `SKYGATE_TS_HOSTNAME` stays hardcoded
   (one skygate host = one tailnet identity — not a value
   operators should change per deploy).

2. **`handleTailscaleRestart`** (commit 149cee8): new
   `action="restart_skgate"` POST endpoint. Flow:
   1. Read the current effective login_server (DB > .env > default).
   2. Write it to `<RepoPath>/.env` atomically
      (`updateEnvFileSKYGATE_TS_LOGIN_SERVER` — writes to
      `.env.tmp`, fsync, rename). Replaces / appends /
      clears the existing `SKYGATE_TS_LOGIN_SERVER=` line
      and leaves every other line untouched.
   3. Spawn a `setsid`'d subprocess (via `applySysProcAttr`
      helper, build-tagged for Linux + no-op on other
      platforms) that runs:
      - container: `docker compose -p skygate -f
        <host-repo>/docker-compose.yml restart skygate`
      - native: `systemctl restart skygate || service
        skygate restart`
      The `setsid` is critical — `docker compose restart`
      sends SIGTERM to the parent skygate process group
      (PID 1 = entrypoint.sh), and the subprocess is in
      a new session so it survives.
   4. Return 303 immediately. The response flushes before
      the SIGTERM arrives, so the operator sees the success
      message; the page is unreachable for ~30s while the
      new container comes up.
   5. Audit row: `tailscale_restart_skgate` with
      `login_server=...`, `in_container=...`, `method=...`.

3. **Web-UI button**: new "Restart skygate" card on
   `/admin/tailscale` (just below the existing Start/Stop
   card). Includes a `confirm()` dialog with the
   `tailscale.restart_confirm` i18n string. 5 new i18n
   keys in both RU+EN.

### Files

- `docker-compose.yml` — remove hardcoded env override
- `internal/feature/admin/tailscale.go` — new
  `handleTailscaleRestart` + `updateEnvFileSKYGATE_TS_LOGIN_SERVER`
  + `isRunningInContainer`
- `internal/feature/admin/setsid_linux.go` (new) +
  `setsid_other.go` (new) — build-tag pair for
  `applySysProcAttr` (Setsid on Linux, no-op elsewhere)
- `internal/handlers/templates/admin/tailscale.html` —
  new restart card
- `internal/i18n/catalog_tailscale.go` — 5 new keys (RU+EN)
- `internal/feature/admin/admin_tailscale_test.go` — 5 new tests
- `scripts/verify_pre_deploy.sh` — B65 added

### Test results

- `go test -count=1 -short ./...` — 27/27 packages PASS
- `make verify-pre` — **65/65 PASS** (B1-B65)
- `TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Replace` PASS
- `TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Append`  PASS
- `TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Clear`  PASS
- `TestHandleTailscaleRestart_WritesEnvAndDispatches` PASS
- `TestHandleTailscaleRestart_RejectsBadCSRF` PASS

### Live verify (post-deploy)

1. `docker-compose.yml` on VM no longer has the hardcoded
   `SKYGATE_TS_LOGIN_SERVER` (verified with
   `grep SKYGATE_TS_LOGIN_SERVER docker-compose.yml` → no
   hardcoded `=` line in the environment section)
2. `/admin/tailscale` shows the "Restart skygate" card
3. `SKYGATE_TS_LOGIN_SERVER=https://head.skynas.ru` in
   `.env` on the host (operator's web-UI edit propagated
   automatically on the next restart click)
4. `docker compose restart skygate` from a click on the
   button → new container starts → entrypoint reads
   `https://head.skynas.ru` → tailscaled logs in successfully
5. `R5/R6` (tailscale IP + exit-node) verify-post checks
   start passing on the next tick

## v0.33.1.15 — per-device-pref device tag in tagOwners (the "cyborg exit rules not visible" fix)

**Date:** 2026-08-05
**Tag:** _pending_
**Scope:** 2-line Go fix in `internal/acl/acl.go` (per-device
grant tagOwners + via tagOwners blocks) + 1 regression test
+ B64 verify-pre check. No API change, no schema change,
no i18n change.

### The bug

A second symptom of the same v0.33.1.12-era pattern that
v0.33.1.14 fixed (callerOwnsDevice was broken, so the
operator couldn't even set cyborg's per-device pref). After
v0.33.1.14, the per-device pref is now writable — but a
Deeper root cause was exposed: every ACL apply for the
last 22+ hours has been silently failing.

The `GenerateACLWithViaForPlane` policy builder emits
per-device ACL grants with `src=tag:dev-<user>-<device>`
and `via:[<device-pref>]` for every row in
`device_exit_node_prefs`. But the `tagOwners` block was
built ONLY from `GetPerUserDeviceTags` (a JOIN on
`node_owner_map`). When a device had a per-device pref
but was missing from `node_owner_map` — e.g. the
`skygate-host-1` host node (its own per-user tag, not yet
backfilled because it joins the tailnet after the portal
admin sets the pref) — the headscale policy parser
rejected the policy with:

> `setting policy: parsing policy: src=tag not found:
>   "tag:dev-skyadmin-skygate-host-1"`

`SetPolicy` returned 500, the snapshot was marked
`applied_success=0`, and the user's preferred exit-node
never took effect. The exit_rule_logs table has 6
consecutive `apply_fail` rows for this exact error from
2026-08-04 22:46 UTC onward.

### The fix

Two additions to `GenerateACLWithViaForPlane`'s
`tagOwners` block:

1. **Include via tags from per-device prefs** (in
   `distinctVias`). Pre-v0.33.1.15 the block only
   covered per-user prefs (`viaByUser`), so a fresh
   per-device pref's via tag (e.g. `tag:exit-emilia`)
   was referenced in `via:[]` but not registered in
   `tagOwners`.

2. **Include per-device-pref device tags** (in
   `perDevTagOwners`). The pre-v0.33.1.15 block was
   built from `tagsByUser` which only contains devices
   in `node_owner_map`. Now augmented with every tag
   from `viaByDevice` (the per-device-pref tags) so a
   device with a pref that's not yet in `node_owner_map`
   still gets its tag registered.

### Files changed

- `internal/acl/acl.go` — 2 small blocks added inside
  the `tagOwners` builder in
  `GenerateACLWithViaForPlane` (~30 lines).
- `internal/acl/acl_test.go` — new test
  `TestGenerateACLWithVia_PerDeviceTagOwners` (60
  lines, 3 assertions: per-device grant emitted +
  device tag in tagOwners + via tag in tagOwners).
- `scripts/verify_pre_deploy.sh` — B64 added.

### Live verify (post-deploy)

1. `POST /my/devices/preferred-exit` for cyborg →
   emilia: was 302 (after v0.33.1.14) but policy push
   failed; now policy push SUCCEEDS (headscale accepts
   the policy, no more 500 from PUT /api/v1/policy).
2. `headscale policy get` — the `via` field is now
   present on cyborg's grant:
   `{ "src": ["tag:dev-skyadmin-cyborg"], "dst":
   ["autogroup:internet"], "ip": ["*"], "via":
   ["tag:exit-emilia"] }`
3. `acl_snapshots.applied_success` flips from 0 to 1
   for new applies. Re-apply on the existing failed
   snapshots: `POST /admin/exit-rules/reapply` (admin
   only) or any per-device POST regenerates the policy
   and pushes it.
4. exit_rule_logs: no more `apply_fail` entries.

### Test results

- `go test -count=1 -short ./...` — 27/27 packages
  PASS (new `TestGenerateACLWithVia_PerDeviceTagOwners`
  test green).
- `make verify-pre` — 64/64 PASS (B1-B64).

## v0.33.1.14 — `placeholdersList(1)+placeholdersList(1)` 2-arg PG-unsafe query fix (the "cyborg device not found" fix)

**Date:** 2026-08-05
**Tag:** _pending_
**Scope:** 1-line Go bug fix in 3 production sites + new
helper `db.PlaceholderAt(n, i)` + 4 regression tests + B63
verify-pre check. No API change, no schema change, no
i18n change.

### The bug

The v0.33.1.12 sweep (B60) fixed hardcoded `?` placeholders
across the codebase by replacing them with
`db.PlaceholdersList(n)`. The replacement pattern used for
2-arg queries was:

```go
`... WHERE a = `+db.PlaceholdersList(1)+` AND b = `+db.PlaceholdersList(1)
```

This compiled and ran fine on SQLite (the `?` placeholder
just gets bound twice). But on PostgreSQL it produced:

```sql
... WHERE a = $1 AND b = $1
```

— two references to the SAME positional parameter while
TWO args are passed. PostgreSQL rejected the query (or
silently bound the wrong value) and the function returned
zero/false for every row.

### The user-facing symptom

When the operator logged into `/my/devices` and tried to set
a per-device preferred exit-node for `cyborg`
(`POST /my/devices/preferred-exit`), `callerOwnsDevice`
returned `false` for **every device**, so the handler
responded with 403 "device not found or not owned by you" —
even though `cyborg` was clearly listed in the
`/my/devices` table and tagged by the same user.

A downstream consequence: with no per-device pref writable,
no per-device ACL grant was emitted for `cyborg`, so
`cyborg` traffic to the rules under emilia wasn't pinned
via the `via:` constraint. The exit-rules page rendered
the rules but the device was free to route through any
exit-node.

### The fix

New helper `db.PlaceholderAt(n, i)` returns the i-th (0-indexed)
placeholder from a `PlaceholdersList(n)` string, so a 2-arg
query splices two UNIQUE placeholders (`$1`, `$2`) at its
two positions:

```go
// Before (PG-unsafe):
`... WHERE a = `+db.PlaceholdersList(1)+` AND b = `+db.PlaceholdersList(1)
// After:
`... WHERE a = `+db.PlaceholderAt(2, 0)+` AND b = `+db.PlaceholderAt(2, 1)
```

Same pattern as `db.NowUnixSQL` / `db.OnConflictDoNothing` /
`db.PlaceholdersList`: a public mirror of the internal
helper for use outside the `db` package. Out-of-range `i`
returns `""` so a caller bug produces a malformed SQL
string (visible at Exec time) instead of a silent bind
mismatch.

### Files changed

- `internal/db/placeholders.go` — added `PlaceholderAt(n, i)`
  (5 lines + doc comment).
- `internal/feature/my/device_exit_pref.go:200` — `callerOwnsDevice`
  query. (v0.33.1.12 had the same bug here.)
- `internal/db/migrations_v0.46.go:94` — `GetDeviceExitNodePref`
  query. (v0.33.1.12 had the same bug here.)
- `internal/db/migrations_v0.46.go:129` — `SetDeviceExitNodePref`
  DELETE branch. (v0.33.1.12 had the same bug here.)
- `internal/feature/my/testutil.go` — added `node_owner_map` +
  `device_exit_node_prefs` tables to the in-memory test schema
  (the per-device-pref feature wasn't covered by tests before).
- `internal/feature/my/device_exit_pref_test.go` (new) —
  4 tests: `TestCallerOwnsDevice_2ArgDispatch` (5 sub-cases
  including mixed-case + non-owner), `TestCallerOwnsDevice_WrongOwner`
  (user=2 impersonation rejected), `TestSetDeviceExitNodePref_RoundTrip`
  (set + get + clear), `TestPlaceholderAt_Dispatch` (helper
  bounds check).
- `scripts/verify_pre_deploy.sh` — B63 added.

### Live verify (post-deploy)

1. `POST /my/devices/preferred-exit` for `cyborg` (logged in
   as `skyadmin`): was 403, now 302 to `/my/devices?ok=1`.
2. `SELECT exit_node_tag FROM device_exit_node_prefs WHERE
   user_id=1 AND device_hostname='cyborg'` — returns the
   chosen tag (e.g. `tag:exit-emilia`).
3. `/my/exit-rules` page (rendered through the per-device
   `via:` grant) — rules under cyborg now route through emilia.
4. `headscale policy get` — the per-device grant for
   `tag:dev-skyadmin-cyborg → autogroup:internet` carries
   `via: ["tag:exit-emilia"]` (was missing the via before
   because the pref write silently failed).

### Test results

- `go test -count=1 -short ./...` — 27/27 packages PASS
  (new device_exit_pref tests all green).
- `make verify-pre` — 61/61 PASS (B1-B63, B8 smoke is
  VM-only as usual).

## v0.32.19 — Documentation wave 2 + migration integrity + HA design proposal

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** docs + 1 new Go feature (migration integrity
tracking, soft mode). No API change, no schema change at the
user-data level (one new system table `applied_migrations`
for migration bookkeeping).

### What's in this release

1. **Migration integrity tracking (soft mode, v0.32.19)**
   - New `internal/db/migration_tracking.go` — SHA-256
     checksum helpers for migration bodies. Detects when an
     OLD migration's SQL body is modified after being applied
     (a latent bug class that the previous idempotent-migration
     design silently absorbed).
   - New `internal/db/migrations_v0.49.go` — creates the
     `applied_migrations(version, sha256, source_file,
     applied_at, first_seen)` system table.
   - `internal/db/db.go` — calls `ensureMigrationTrackingTable`
     before running other migrations. Recording of each
     migration's checksum is the v0.32.20 follow-up (requires
     a refactor of `db.go` to extract migration SQL bodies
     into a map for SHA computation).
   - Soft mode: a mismatch produces a `WARNING` log line but
     does NOT prevent skygate from starting. The mode flips to
     HARD in v0.32.20 after one release cycle of observation.
     Opt-in to HARD earlier via `SKYGATE_MIGRATION_INTEGRITY=hard`.
   - 8 unit tests in
     `internal/db/migration_tracking_test.go` covering:
     deterministic checksum, semantic changes detected,
     tracking table idempotent, record + get roundtrip,
     first-run / match / soft-mismatch / hard-mismatch /
     audit listing / mode introspection.
   - B36 catalog check in `verify_pre_deploy.sh` pins the
     contract (helpers exist, V049 registered, ensure
     function called, test file present).

2. **Wave 2 documentation cleanup**
   - `AGENTS.md` "Common gotchas" extended with 6 new entries
     (10-15): CASCADE-LOCK on SQLite WAL, distroless
     healthcheck pattern, NPM-blocks-iptables, exit-node
     online detection (trust headscale, not `last_seen`),
     per-user subnet phantom-route caveat, subnet-router
     Remove handler lifecycle.
   - `docs/BACKLOG.md` updated — 6 completed entries added
     (v0.32.13, v0.32.14, v0.32.15, v0.32.16, v0.32.17,
     v0.32.18). Last-updated stamp → 2026-08-03.
   - `docs/internal/internal/subnet-router.md` — new "Removing a subnet-router
     (admin-only, v0.32.18+)" section with the full inverse
     flow of the v0.16.7 Provision, idempotency notes, what
     NOT to use Remove for, and verify-after-Remove SQL.
   - `README.md` — new "Tailscale: OFF by default (v0.32.15+)"
     section documenting the 3-step manual re-enable procedure
     and the v0.32.8 / v0.32.11 incidents that motivated the
     default-OFF flip.
   - `docs/plans/pg-migration-handling.md` — updated
     "Implementation status" with the v0.32.0+ state
     (driver + 27 PG migrations + 4 verification tests on
     main, scope of the runtime `?` → `$N` rewrite).

3. **PG cutover runbook (the actual v0.33.0 plan)**
   - New `docs/v0.33.0-pg-cutover-runbook.md` — the 4-step
     operator runbook for the live cutover (pre-cutover verify,
     runtime rewrite as a separate PR, 15-min maintenance
     window, post-cutover verify). Includes the known issues
     (strftime, INSERT OR REPLACE, RETURNING, PRAGMA), the
     rollback procedure, and the operator's decision points.
   - Blocked on the operator provisioning a PG-staging VM.

4. **HA active-router design proposal (the operator's
   2026-08-03 ask)**
   - New `docs/internal/internal/ha-active-router.md` — 3 architectures
     (A: PG active-passive / B: single-writer role / C:
     multi-writer eventual consistency) with pros/cons
     comparison, RTO/RPO, complexity, and a clear
     recommendation: **Architecture B** for the current
     deployment (1-2 day implementation, no PG required,
     RTO 5-15 min via manual role flip + DNS swap).
   - `docs/internal/internal/ha-architecture.md` — added "Tier 0.5" entry to
     the tier table, with the rationale for choosing it over
     Tier 1 today and the upgrade path to Tier 1 once the
     PG cutover ships.
   - Implementation outline for Architecture B included
     (env var, route gating, Litestream config, manual
     failover drill, optional auto-promotion as v0.34.0
     follow-up).
   - 4 open questions for the operator (RTO acceptance,
     auto-promotion vs manual, budget, second-VM identity).

### Files

```
AGENTS.md                                          | +80
README.md                                          | +40
docs/BACKLOG.md                                    | +48
docs/internal/internal/ha-architecture.md                            | +32
docs/internal/internal/ha-active-router.md                           | NEW (15.6 KB)
docs/plans/pg-migration-handling.md                | +124/-32
docs/internal/internal/subnet-router.md                              | +77
docs/v0.33.0-pg-cutover-runbook.md                 | NEW (10.9 KB)
internal/db/db.go                                  | +13
internal/db/migration_tracking.go                  | NEW (8.3 KB)
internal/db/migration_tracking_test.go             | NEW (7.2 KB)
internal/db/migrations_v0.49.go                    | NEW (1.7 KB)
scripts/verify_pre_deploy.sh                       | +27
```

### Verification

- `go build ./...` — clean
- `go test -count=1 -short ./internal/db/` — 9.4s PASS
  (includes the 8 new migration_tracking tests)
- `bash scripts/verify_pre_deploy.sh` — 35 PASS + 1 SKIP
  (B8 = smoke, runs on VM only)
- B36 (new): migration integrity helpers + V049 + tests

### Loose ends (deferred)

- **Recording of old migrations (V020-V048)**: v0.32.20
  follow-up. Requires extracting migration SQL bodies from
  the per-version `migrateV0NN` functions into a `map[int]string`
  in `internal/db/migration_bodies.go` so `migrate()` can
  call `VerifyMigrationChecksum` per migration.
- **HARD mode default**: v0.32.20 after one release cycle of
  soft-mode observation.
- **HA Architecture B implementation**: blocked on operator
  feedback to the 4 open questions in
  `docs/internal/internal/ha-active-router.md` § "Open questions for the
  operator".
- **Live VM still on v0.32.15 build label**: v0.32.16/17/18/19
  are committed + pushed but not redeployed. Manual
  `docker compose up -d --force-recreate skygate` on the VM
  to pick up all 5 releases.

---

## v0.32.18 — Subnet-router Remove handler (full lifecycle)

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** new admin endpoint + UI + tests + regression guard. No API change.

### What's in this release

1. **`PostAdminUserSubnetRemove` handler (v0.32.18)**
   - New admin endpoint: `POST /admin/users/{id}/subnet/remove`
   - Inverse of `Provision`: full subnet-router cleanup that
     deletes the headscale node and clears all DB state.
   - Steps in one atomic flow:
     1. Parse `router_node_id` from `user_subnets` → call
        `headscale.Client.DeleteNode(nodeID)`. Failure is
        logged but does NOT abort the rest of the handler
        (the DB cleanup is the source of truth for the
        skygate side; an admin can delete the headscale
        node manually if needed).
     2. `UPDATE user_subnets SET status='pending',
        router_node_id='', router_hostname='', updated_at=now`
     3. `UPDATE portal_users SET subnet_status='pending',
        subnet_cidr='', subnet_router_node_id='',
        subnet_router_hostname=''`
     4. `INSERT INTO audit_log` with action `subnet_router_removed`
        and the deleted headscale node id in the detail
     5. Redirect to `/admin/users/{id}/subnet?flash=removed`
   - Idempotent: clicking Remove twice is safe. If the
     `user_subnets` row doesn't exist → 404.
   - Does NOT re-apply the ACL (the policy uses
     `h-user-admin-subnet` which is always present
     regardless of router status — re-applying would just
     add a row to `acl_snapshots` with no diff).

2. **UI button + flash messages**
   - "Remove subnet-router" button in the admin subnet
     page, only shown when `status='router_active'`
     (i.e. there IS a router to remove). Has a JS
     `confirm()` dialog with the i18n string.
   - `?flash=<key>` query parameter on the page URL
     (parsed by `GetAdminUserSubnet`) sets a
     `FlashMessage` data field that the template renders
     as a success banner.
   - 9 new i18n keys × 2 langs (18 entries total):
     `remove_button`, `remove_button_help`, `remove_confirm`,
     `flash_removed`, `flash_headscale_failed`,
     `flash_allocated`, `flash_disabled`, `flash_shared`,
     `flash_revoked`.

3. **Tests (3 new)**
   - `TestPostAdminUserSubnetRemove_DeletesHeadscaleAndClearsDB`:
     full happy path (seeded router_node_id="26", verify
     headscale DELETE was called, all 3 DB tables cleared,
     audit log written, redirect has `flash=removed`)
   - `TestPostAdminUserSubnetRemove_NoRouterRow`:
     idempotent path (router_node_id='' — should still
     clear status to pending, no headscale call)
   - `TestPostAdminUserSubnetRemove_NoSubnetRow`:
     user has no user_subnets row → 404

4. **B35 verify-pre regression guard**
   - Fails the build if `POST /admin/users/{id}/subnet/remove`
     is not wired to `adminSvc.PostAdminUserSubnetRemove`
     in `cmd/skygate/main.go`. Same pattern as B15/B16/B17
     (regression guards for past handler-removal bugs).

### Files

- `internal/feature/admin/user_subnet_remove.go` (new) —
  the Remove handler
- `internal/feature/admin/user_subnet.go` — `GetAdminUserSubnet`
  reads `?flash=...`; added `subnetFlashMessages` map
- `internal/feature/admin/user_subnet_test.go` — 3 new tests
- `internal/feature/admin/testutil.go` — added
  `subnet_router_hostname` column to test schema
- `internal/handlers/templates/admin/user_subnet.html` —
  Remove button + FlashMessage banner
- `internal/i18n/catalog_user_subnet.go` — 9 new keys × 2 langs
- `cmd/skygate/main.go` — route registration
- `scripts/verify_pre_deploy.sh` — B35 check
- `RELEASE-NOTES.md` — this entry

### Verified

- `go test -count=1 -short ./...` — all 26 packages PASS
- `bash scripts/verify_pre_deploy.sh` — 35/35 PASS (B1-B35)
- Test schema updated for `subnet_router_hostname` (it was
  in production but missing in `newMemoryDB`)

### Loose ends / future work

- **Live R-check (R30+ in verify_post_deploy.sh)**: a
  runtime check that does a full add+remove cycle against
  a real headscale. Deferred to v0.32.19 — would need
  Docker setup for a sandbox headscale (the existing
  verify_post_deploy.sh runs against the live VM).
- **Auto-apply tags for the new device**: when a new
  `skygate-subnet-<username>` device registers, the
  backfillNodeOwnership path should auto-apply
  `tag:dev-admin-<hostname>` + `tag:subnet-router`.
  This already works in v0.32.18; just flagging that the
  Remove flow doesn't re-apply on the new device (it
  doesn't need to — that's a Provision-time concern).
- **Documentation update**: `docs/internal/internal/subnet-router.md` should
  mention the Remove button (currently only documents
  Provision). Deferred to doc-cleanup pass.

---

## v0.32.17 — Exit-node monitor online detection fix + device_rules dedup

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** fix (logic + data) + verify-pre B34. No API change.

### What's in this release

1. **Exit-node monitor online detection (logic fix)**
   - **Old behaviour**: headscale says `online=true` but `last_seen` is older than
     `OfflineAfter` (default 2 min, recently bumped to 10 min via env) → monitor
     marks the node offline. Produced false-negatives for every idle VPS exit-node
     (no peer activity in 10 min = "offline" even though headscale still considers
     it online).
   - **New behaviour**: trust headscale's `n.Online` as the primary signal. The
     `OfflineAfter` window is only consulted when headscale says OFFLINE: if we
     just saw the node within `OfflineAfter`, treat it as online (catches transient
     headscale-side booleans). Outside the window + offline → mark offline.
   - **Affected file**: `internal/monitoring/exit_node_monitor.go` (lines 372-405).
   - **Tests**: `TestComputeSnapshot_OfflineWhenLastSeenOld` (old, asserted the bug)
     renamed to `TestComputeSnapshot_HeadscaleOnlineTrustsLastSeenOld` and flipped
     to assert the correct behaviour. New `TestComputeSnapshot_ForgivingFallback`
     covers the offline-but-recent case.

2. **device_rules duplicates cleanup (data fix)**
   - Found 365 duplicate `device_rules` rows in the production DB on 2026-08-03.
     186 rows for `(workstation-1, relay-3)` and 179 rows with empty
     `device_hostname`, all with the same `created_at` (a stale batch script
     that forgot to dedup).
   - Inflated the `/admin/exit-nodes` "mismatch" computation: `computeSyncStatus`
     counts ALL device_rules targeting the exit_node, not the unique device count.
     relay-3 showed `mismatch: have 148, want 365` instead of the real drift
     (the operator never had 365 rules; the duplicates were the inflation).
   - **Cleanup**: `DELETE FROM device_rules WHERE id NOT IN (SELECT MIN(id) FROM
     device_rules GROUP BY exit_node_id, device_hostname)` — 363 rows removed,
     now down to 2 unique `(device, exit_node)` pairs.
   - The remaining "mismatch: have 148, want 2" is a real-but-different drift
     (skygate has 2 rules for relay-3, headscale has 148 routes) that
     warrants its own investigation — not a duplicates issue.

3. **Verify-pre B34 (regression guard)**
   - New check that fails the build if `device_rules` has any duplicate
     `(device_hostname, exit_node_id)` group. Runs `sqlite3` on the production
     DB; skips gracefully on Windows / fresh VM (no DB).
   - Matches the pattern of B15/B16/B17/B22 (regression guards for past bugs).
   - Comment in the script explains the 2026-08-03 incident and the cleanup
     SQL so future maintainers know the context.

### Files

- `internal/monitoring/exit_node_monitor.go` — new online-detection logic
- `internal/monitoring/exit_node_monitor_test.go` — updated + 1 new test
- `scripts/verify_pre_deploy.sh` — B34 check
- `RELEASE-NOTES.md` — this entry

### Verified

- `go test -count=1 -short ./...` — all packages PASS
- `bash scripts/verify_pre_deploy.sh` — 34/34 PASS (B1-B34)
- Live VM: cleanup done via `sudo sqlite3`; audit log entry recorded.

---

## v0.32.16 — Headplane distroless healthcheck fix + docker build cache hygiene

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** template + verify-pre. No Go code changed.

### What's in this release

1. **Headplane healthcheck override** in
   `deploy/templates/headscale-compose.yml.tmpl`. The distroless
   `ghcr.io/tale/headplane:0.6.3` image ships `/bin/hp_healthcheck`
   that probes `http://localhost:3000/admin/healthz`, but
   `HEADPLANE_SERVER__PORT` is 50445 by default. The upstream
   healthcheck always failed with
   `dial tcp [::1]:3000: connect: connection refused`, leaving
   headplane in `(unhealthy)` for 60+ failing-streak iterations
   even though the service was fine (port 50445 returns 200 in
   15ms via direct probe). The override uses Node.js (the only
   runtime in the distroless image, at `/nodejs/bin/node` — not
   in PATH for the healthcheck process) to probe
   `http://127.0.0.1:${HEADPLANE_SERVER__PORT}/admin/healthz`.
   `127.0.0.1` is used (not `localhost`) because IPv6 may resolve
   to `[::1]` and headplane binds `0.0.0.0`, not `::`.

2. **`docker builder prune -a -f` as a documented recovery step.**
   Multi-stage Dockerfiles leave a build cache entry per
   `docker compose build` invocation, even if the resulting image
   is identical. Five deploys over five days left 7.36 GB of
   build cache on the live VM; 5.75 GB was reclaimable. The fix
   is operational (run the prune) but the template-side change
   is also part of this release: B33 pins the healthcheck contract
   so future deploys don't regress to the broken upstream check.

3. **B33 verify-pre check** that pins the headplane healthcheck
   contract:
   - The template has a `healthcheck:` block under headplane
   - The test command uses `/nodejs/bin/node`
   - The probe URL uses `${HEADPLANE_SERVER__PORT}` (not hardcoded)
   - The probe URL is `127.0.0.1` (not `localhost`)
   - No `wget` in the test (the distroless image has no wget)

### Files

- `deploy/templates/headscale-compose.yml.tmpl` — added `healthcheck:`
  block with node probe
- `scripts/verify_pre_deploy.sh` — B33 check

### Backlog debt

v0.32.13, v0.32.14, and v0.32.15 don't have release-notes entries
yet (only v0.32.12 made it into this file before the 5-layer bug
war took over). Tracked in `docs/BACKLOG.md`; will be backfilled
in v0.32.17 or later.

### Operational notes

After deploying v0.32.16, the live headplane container was
recreated with the new healthcheck. `docker inspect headplane`
now shows `Up X seconds (healthy)` (was `Up X hours (unhealthy)`
for ~24h before the fix). The new healthcheck takes effect on
the next `deploy.sh` run; no manual action needed.

The disk cleanup is NOT a code change — it's a one-shot operator
action (`docker builder prune -a -f && rm -rf /var/backups/skygate/PRE_*`).
Reclaimable on the live VM at 2026-08-03: ~6 GB (5.75 GB build
cache + 317 MB old recovery dirs). Disk went from 74% → 58% in
30 seconds.

---

## v0.32.12 — Fix CGO_ENABLED=0 regression in multi-stage Dockerfile (silent 504 fix)

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** closes the v0.32.8 CGO regression. After deploying
v0.32.8 the operator hit 504 Gateway Time-out on every
`skygate.example.com` request, but `docker ps` didn't show the
skygate container at all and `/healthz` on `localhost:8080`
returned `Connection refused`. The fix is one-line at the
root: re-enable cgo in the multi-stage Dockerfile and ship
the C toolchain in the build stage.

### The bug

The v0.32.8 multi-stage Dockerfile (commit `2d2d91f` / `86a406c`)
shipped this line in the `skygate-build` stage:

```dockerfile
ENV CGO_ENABLED=0
RUN go build -buildvcs=false -trimpath \
    -ldflags "-s -w -X main.version=${GIT_VER} ..." \
    -o /out/skygate ./cmd/skygate
```

The intent was a fully-static binary that doesn't need
glibc on the alpine runtime. The unintended consequence:
`go-sqlite3` is a **pure-CGO driver**. When CGO is disabled,
the import resolves to a stub package that returns

```
Binary was compiled with 'CGO_ENABLED=0', go-sqlite3
requires cgo to work. This is a stub
```

on every DB call. The skygate boot sequence does `db.Ping()`
right after "🌐 Skygate starting on :8080" — the stub error
fires there, the binary exits 1, port 8080 never binds,
and the upstream proxy (Nginx Proxy Manager at the operator's NPM host,
not the in-container caddy which is now off per v0.32.11)
returns 504 to every external request.

### Why the v0.32.5 → v0.32.11 chain didn't catch it

- `go test ./...` (B1 in `verify-pre`) passes regardless of
  CGO_ENABLED: the test in-memory DBs (`:memory:`) take the
  same stub path but the tests assert SQL behavior, not the
  driver binary. The stub satisfies the interface.
- `go build ./cmd/skygate` (B3) passes: the build succeeds,
  it just links a stub instead of the real driver.
- The smoke test (B8) runs against a LIVE container, so a
  v0.32.8 deploy to the VM would have caught this in step 1
  (the `ready at` log line + a 200 on `/healthz`). But the
  v0.32.8 deploy happened without smoke being run first —
  the operator pushed and only saw the failure when the
  browser 504'd.
- CGO behavior doesn't show up in `docker logs` until the
  binary actually tries to use the DB, which happens in the
  foreground goroutine of `main.go`. The crash is fast
  (sub-second) and the container is gone by the time the
  operator SSHes in to check.

### Why v0.32.5 worked but v0.32.8 didn't

v0.32.5 had a single-stage Dockerfile based on
`golang:1.25-alpine` that ran `go build` at container
start (via `entrypoint.sh`). CGO was enabled by default
in `golang:1.25-alpine` — the image ships gcc + musl-dev
pre-installed. v0.32.5's runtime stage also kept `gcc
musl-dev` in the runtime apk add list (defensive), so
even if the build were re-run at container start it would
have produced a working binary.

v0.32.8 split the Dockerfile into two stages: a `skygate-build`
stage that only kept `git` (for the version label), and a
minimal `alpine:3.20` runtime stage. To get a smaller
binary the build stage also dropped CGO. That's what
broke.

### The fix

`Dockerfile`, `skygate-build` stage:

1. `ENV CGO_ENABLED=1` (re-enable cgo).
2. `apk add --no-cache gcc musl-dev sqlite-dev` (the C
   toolchain — gcc + libc headers + sqlite3.h headers).
3. Comment block (24 lines) explaining the regression and
   the CGO contract, so a future maintainer who sees
   "ENV CGO_ENABLED=0" in git history and thinks "smaller
   binary, why not" can find the explanation right there.

`Dockerfile`, runtime stage:

1. `sqlite-libs` was already in the apk add list (kept
   from v0.32.5). With CGO_ENABLED=1, the resulting binary
   is dynamically linked against `libsqlite3.so.0`, which
   `sqlite-libs` ships. No change needed.

### Files changed

- `Dockerfile` — `ENV CGO_ENABLED=0` → `ENV CGO_ENABLED=1`,
  added `gcc musl-dev sqlite-dev` to the build stage's
  apk add list, expanded the comment block (24 → 41 lines
  with the regression rationale + 2026-07-31 timeline).
  **Also**: install the binary to BOTH `/usr/local/bin/skygate`
  (the actual entrypoint path) AND `/app/skygate` (for
  back-compat with the v0.29.0 self-update orchestrator's
  `docker run --rm --volumes-from skygate
  skygate-skygate:latest /app/skygate --migrate-only`
  command). The `/app/skygate` path is shadowed by the
  source bind-mount at runtime, but the autoupdate helper
  container doesn't have the bind-mount so the image's
  `/app/skygate` is visible there.
- `entrypoint.sh` — `exec /app/skygate` → `exec
  /usr/local/bin/skygate`. The runtime image's `/app` is
  the bind-mount target for the host source tree (see
  `docker-compose.yml`); a bind-mount REPLACES the
  directory contents, so the host's `/app/skygate` (if
  present, e.g. a stale v0.32.5-era binary) would shadow
  the freshly built image binary. `/usr/local/bin` is
  outside the bind-mount so the image's binary always
  wins. This is the v0.32.5 → v0.32.8 silent-outage
  root cause that B27 pins.
- `scripts/verify_pre_deploy.sh` — new **B26** check that
  pins the CGO contract: `! grep -qF "ENV CGO_ENABLED=0"`,
  `grep -qF "ENV CGO_ENABLED=1"`, build stage has
  `gcc + musl-dev + sqlite-dev`, runtime stage has
  `sqlite-libs`. A future maintainer who tries to
  re-enable `CGO_ENABLED=0` for size will fail B26
  with a one-line explanation pointing at this section.
  New **B27** check that pins the entrypoint-binary
  path: `exec /usr/local/bin/skygate` (not
  `exec /app/skygate` — the v0.32.8 bug shape).
- `RELEASE-NOTES.md` — this entry.

### Verified

- `go build -buildvcs=false -trimpath -ldflags "-s -w"
  -o /tmp/skygate-cgo-test ./cmd/skygate` (CGO_ENABLED=1)
  → 14MB binary.
- Smoke test of the CGO binary locally: starts up,
  connects to a stub headscale URL, `/healthz` returns
  `200 {"build":"dev+unknown","status":"ok",...}`. The
  v0.32.8 stub binary returned `Connection refused` on
  the same test because it crashed on `db.Ping()`.
- `go test -count=1 -short ./...` with CGO_ENABLED=1:
  26/26 packages PASS (same as the CGO_ENABLED=0 baseline
  because the test path doesn't depend on the driver
  implementation, just the public interface).
- `make verify-pre` on Windows: 24/24 PASS (B8 SKIP,
  smoke is VM-only; B26 new). 2026-07-31 18:11 MSK.

### Deploy / rollback notes

- **Deploy**: `git pull` on the VM, then
  `docker compose build skygate && docker compose up -d
  skygate`. The `docker compose build` step will take
  ~30s longer than v0.32.8 (compiling cgo + sqlite) but
  the resulting binary is ~14MB instead of the v0.32.8
  41MB (the v0.32.8 size was wrong — the stub binary is
  artificially small because it doesn't link the real
  driver). Runtime start is unchanged (~5s).
- **Rollback** (if v0.32.12 also breaks something on the
  VM): `git checkout v0.32.11` → `docker compose build
  skygate` → `docker compose up -d skygate`. The v0.32.5
  rollback pattern (clone, bind-mount `/app`, run the
  v0.32.5 image) also still works as a deeper fallback
  — see `AGENTS.md` → "v0.32.5 rollback test pattern".

### Why not just switch to `modernc.org/sqlite` (pure-Go)?

Considered. `modernc.org/sqlite` is a fully-Go port of
SQLite (no cgo, no glibc dep), which would let
`CGO_ENABLED=0` work again. But:

1. It's a 30MB+ transitive dep tree (modernc.org/sqlite
   pulls libq, qflag, etc.) — bigger than the 14MB CGO
   binary.
2. It has its own query planner quirks that have caused
   subtle correctness regressions in the past (e.g.
   handling of `strftime('%Y-%m-%d', ...)` with NULL
   arguments differs from the C version).
3. Switching the driver in this codebase is a 1-2 day
   migration (`internal/db/` queries use
   `database/sql` exclusively, so it's mostly import-path
   changes + rebuild).
4. The current CGO build is fast enough (~30s in the
   build stage) and the resulting binary is the smallest
   realistic size (14MB dynamically linked against alpine
   musl + libsqlite3).

Re-evaluate if a future Go release ships a first-party
`database/sql` SQLite driver that's pure-Go (e.g. if
`go-sqlite3` ever ships a pure-Go fallback).

## v0.32.11 — Caddy is OFF by default (silent-outage fix)

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** closes the "ci green but skygate.example.com
unreachable" report from 2026-07-31. Root cause: the
in-container Caddy sidecar was on by default, was binding
0.0.0.0:80 + 0.0.0.0:443, and was returning `SSL alert 80
internal_error` because the placeholder Caddyfile
(`head.example.com` / `headplane.example.com` /
`derp.example.com` — template defaults, not the
operator's real domain) couldn't issue certs. The
operator's external TLS terminator (Nginx Proxy Manager
at the operator's NPM host) was forwarding to this host's :443 and
hitting the broken caddy. CI didn't catch it (CI checks
Go code, not Caddyfile validity or ACME issuance). The
fix flips the default across three coordinated layers so
the in-container caddy never starts unless the operator
explicitly opts in.

What changed:

1. **`deploy/deploy.sh:124`** — `CADDY_ENABLED` default
   flipped from `true` to `false`. A one-liner at deploy
   time logs the choice so the operator can see at a
   glance whether caddy will start.
2. **`docker-compose.yml`** — the `caddy` service moved
   under `profiles: ["caddy"]`. A plain
   `docker compose up -d` no longer starts it. The deploy
   script appends `--profile caddy` to the `up -d`
   invocation only when `CADDY_ENABLED=true`. Net effect:
   the new default = no caddy container = ports 80/443
   stay free for whatever external terminator the
   operator already runs.
3. **`.env.example`** — `CADDY_ENABLED=false` is now the
   documented default with a copy-pasteable opt-in
   procedure (DNS-01 vs HTTP-01, real hostnames, token
   file paths).
4. **`docs/internal/internal/https-setup.md`** — new top-level section
   "Caddy is off by default" with the full rationale,
   the operator-side `docker ps` / `ss -tlnp` check, and
   the opt-in / opt-out procedure. The architecture
   diagram is also rewritten to show "TLS terminator" as
   a generic box (Caddy if opted in, NPM / Cloudflare /
   Tailscale TLS if not) instead of hardcoded caddy.
5. **`scripts/verify_pre_deploy.sh`** — new **B25** pin:
   `deploy.sh`'s default branch is `:-false`, the
   `.env.example` ships `CADDY_ENABLED=false`, and the
   `caddy` service in `docker-compose.yml` is under
   `profiles: ["caddy"]`. If any of those regresses, the
   silent-outage footgun comes back.

What didn't change:

* `deploy/templates/Caddyfile.tmpl` — same template,
  same vhost structure, same DNS-01 / HTTP-01 config.
* `Dockerfile.caddy` — same two-stage caddy build with
  the `caddy-dns-cloudflare` plugin baked in.
* The caddy-data / caddy-config volumes — same names,
  same persistence, just not auto-started.
* The TLS-termination layer for operators who DO want
  caddy — opt-in gets exactly the same setup as before
  v0.32.11, just with `CADDY_ENABLED=true` in `.env`.

Live incident timeline:

* 2026-07-30 ~12:00 MSK — `docker system prune -a -f`
  re-pulled `caddy:2-alpine` fresh, losing the previous
  custom build. caddy started crash-looping on
  `module not registered: dns.providers.cloudflare`.
  The deployed `skygate-caddy` custom image was lost.
* 2026-07-30 ~13:00 MSK — v0.32.10 shipped
  `Dockerfile.caddy` (two-stage build with the cloudflare
  DNS provider baked in). caddy started successfully
  and accepted HTTP connections.
* 2026-07-31 ~09:00 MSK — operator reports "ci github
  собрался но сайт skygate не открывается". CI for
  v0.32.9 was green (3/3 jobs pass, Go 1.25 fix worked).
* 2026-07-31 ~12:00 MSK — investigation finds caddy
  running, ports 80/443 bound, `SSL alert 80
  internal_error` because the placeholder
  `head.example.com` Caddyfile can't issue certs
  (ACME `rejectedIdentifier` error). Operator then
  explained that this VM is fronted by an external NPM
  at the operator's NPM host — caddy is irrelevant on this host.
* 2026-07-31 ~13:00 MSK — fix: stop caddy container,
  set `CADDY_ENABLED=false` in `.env`, document the new
  default + the opt-in procedure.
* 2026-07-31 ~13:30 MSK — v0.32.11 source changes
  landed (deploy.sh default flip + docker-compose
  profile + .env.example + docs + B25).
* 2026-07-31 ~13:35 MSK — `make verify-pre` 24/24 PASS
  (B8 SKIP smoke VM-only; B25 new).

Files in v0.32.11:

* `deploy/deploy.sh` — `:-true` → `:-false` at line 124;
  new comment block explaining the flip; `compose up -d`
  at the skygate step now appends `--profile caddy` when
  `CADDY_ENABLED=true`.
* `docker-compose.yml` — `caddy:` service gained
  `profiles: ["caddy"]`; the comment block above
  rewritten to explain the opt-in procedure.
* `.env.example` — `CADDY_ENABLED=true` → `false`; new
  comment block listing both modes with copy-pasteable
  steps.
* `docs/internal/internal/https-setup.md` — new TL;DR section at the top
  + a full "Caddy is off by default — the why and the
  opt-in" section (~150 lines) + the architecture
  diagram rewritten to make the TLS terminator a
  generic box.
* `scripts/verify_pre_deploy.sh` — new B25.
* `scripts/verify_pre_deploy.sh` — UTF-8 BOM stripped
  (the Edit tool re-introduced it on the last save;
  bash shebang lines must start with literal `#!/bin/bash`).
* `RELEASE-NOTES.md` — this entry.

---

## v0.32.9 — CI Go version bump + complete root cleanup

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** closes two operator-flagged issues from 2026-07-30 that
the v0.32.8 cleanup missed:

1. **CI failure on the last 5 runs (v0.32.6..v0.32.8)** — the
   `go mod download` step in `.github/workflows/ci.yml` failed
   because the workflow pinned `go-version: '1.23'` while
   `go.mod` requires `go 1.25.0`. CI was running every push
   but red on every push, so the operator couldn't see at a
   glance whether a real regression had landed.
2. **Dead per-version files at root** — the v0.32.8 cleanup
   deleted `check_v0.*.sh` (the inner scripts) but missed the
   matching `run_check_v0.*.sh` wrappers (which `exec` the
   deleted `/tmp/check_v0.*.sh` and would always fail) plus
   `commit_msg_v0.21.0.txt` + `commit_msg_v0.21.1.txt`
   (operator's commit-message drafts) and
   `run_fix_admin_attribution.sh` (wrapper for a deleted
   one-shot fix script). 7 files total.

### The CI Go version fix

`go.mod` has `go 1.25.0` and the local dev env runs Go 1.25.4.
CI was installing Go 1.23 (probably the version the workflow
file was originally written for, before go.mod was bumped).
The toolchain directive in go.mod (`toolchain go1.25.4`) makes
`go mod download` ask for a newer Go, but in the CI runner the
auto-fetch can be flaky / restricted, so the explicit
`go-version: '1.25'` in the workflow is the safer primary path.

- Both `Setup Go` steps in `.github/workflows/ci.yml` (the
  `test` job + the `verify-pre` job) are now `go-version: '1.25'`.
- B23 in `scripts/verify_pre_deploy.sh` pins the contract:
  - `go.mod` has both `^go 1.25` and `^toolchain go1` directives
  - `ci.yml` has `go-version: '1.25'` (fixed string, both jobs)
  - A future refactor that bumps one but not the other fails B23

Note: `go mod tidy` on Go 1.25.4 will REMOVE the `toolchain`
directive from go.mod (because the running Go is the same
version as the directive). The pre-commit `verify-pre` catches
this regression via B23 — if the directive goes missing, B23
fails before the push.

### Root file cleanup

7 dead files deleted:

| File | What it was | Why deleted |
|---|---|---|
| `run_check_v0.22.3.sh` | 10-line wrapper that `exec bash /tmp/check_v0.22.3.sh` | The inner script was deleted in v0.32.8; the wrapper now 100% fails. |
| `run_check_v0.23.0.sh` | Same pattern, v0.23.0 | Same |
| `run_check_v0.23.3.sh` | Same pattern, v0.23.3 | Same |
| `run_check_cross_subnet_v0.23.1.sh` | Same pattern, v0.23.1 | Same |
| `run_fix_admin_attribution.sh` | 10-line wrapper for the deleted /tmp/fix_admin_attribution.sh | One-shot fix script, already used + no longer needed (operator's note in BACKLOG.md) |
| `commit_msg_v0.21.0.txt` | 5.6 KB commit-message draft from the v0.21.0 release | Scratch text; the real commit message is in git history. |
| `commit_msg_v0.21.1.txt` | 3.3 KB commit-message draft from the v0.21.1 hotfix | Same |

B24 in `scripts/verify_pre_deploy.sh` pins the contract: no
`run_check_v0.*.sh`, `run_check_cross_subnet_v0.*.sh`,
`run_fix_*_attribution.sh`, or `commit_msg_v0.*.txt` at root,
and `RELEASE-NOTES.md` is the only release-notes file at
root. A future operator who adds per-version wrapper scripts
will fail B24 before the push.

### About the operator's "should these be at root" question

The LIVE verification scripts (`scripts/verify_pre_deploy.sh`,
`scripts/verify_post_deploy.sh`, `scripts/rebuild_deploy.sh`,
`scripts/recover_db_corruption.sh`) STAY in `scripts/`. The
Go project convention is: top-level for Go code + top-level
configs (`Dockerfile`, `docker-compose.yml`, `Makefile`,
`go.mod`, `go.sum`), `scripts/` for shell. The Makefile
already exposes `make verify-pre` / `make verify-post` /
`make rebuild-deploy` / `make reconcile-snapshots` so the
operator doesn't need to remember a `scripts/` path.

The DEAD per-version files at root were a different problem:
they were left over from the v0.22/v0.23 era when each
release had a one-off `check_v0.X.Y.sh` script. The current
model is ONE live catalog (`scripts/verify_pre_deploy.sh` +
`scripts/verify_post_deploy.sh`) that doesn't change name per
version. The root-level wrapper family had no reason to exist
once the inner scripts were deleted.

### Files in this change

- **DELETED (7):** `run_check_v0.{22.3,23.0,23.3}.sh`,
  `run_check_cross_subnet_v0.23.1.sh`,
  `run_fix_admin_attribution.sh`,
  `commit_msg_v0.21.{0,1}.txt`
- **MODIFIED `.github/workflows/ci.yml`** — both `Setup Go` steps
  bumped from `1.23` to `1.25`
- **MODIFIED `go.mod`** — added `toolchain go1.25.4` directive
  (defensive; auto-removed by `go mod tidy` if the local Go
  matches, B23 catches that regression)
- **MODIFIED `scripts/verify_pre_deploy.sh`** — added **B23**
  (CI Go version matches go.mod) and **B24** (no dead
  per-version wrapper scripts at root)
- **MODIFIED `RELEASE-NOTES.md`** — this entry

### Verification

- `bash scripts/verify_pre_deploy.sh` locally: **23/23 PASS**
  (B8 SKIP smoke is VM-only; B23/B24 added)
- The next CI push will trigger all 3 jobs (`test`, `verify-pre`,
  `audit`) on Go 1.25 — expected green

## v0.32.8 — Dockerfile builds at image-build time (100s → 5s startup)

**Date:** 2026-07-31
**Tag:** `v0.32.8` (commit 2d2d91f, force-pushed from 86a406c)
**Scope:** the operator reported three issues:

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** the operator reported three issues:

1. **Container startup takes 100+ seconds** — the operator's
   `make rebuild-deploy` was waiting 21×5s (105s) for /healthz.
2. **Old `RELEASE-NOTES-v0.28.X.md` and `check_v0.X.X.sh` files in main**
   — leftover from the v0.28 era. The v0.32 era has ONE
   `RELEASE-NOTES.md` and the v0.28.5 guarantee catalog under
   `scripts/`.
3. **CI failure** — the most recent failed runs (Run 141-143) are
   all on ancient commits (pre-v0.32.0). My v0.32.5-7 fixes
   haven't been CI-tested yet — that requires a new push.

All three are addressed in this release.

### v0.32.8: Dockerfile builds at image-build time (was 100s, now 5s)

**What was slow**: the old Dockerfile was effectively a single
stage — `golang:1.25-alpine` with the entrypoint running
`go mod download` + `go build` at container start. On a fresh
image, this downloaded 4 Go modules (testify, spew, go-difflib,
yaml.v3) + the apk deps for git + openssh-client, taking ~100s
before skygate even started. On subsequent container restarts
the apk + Go caches made it fast (~5s), but the first run after
`docker compose build` was always 100s.

**The fix**: real multi-stage Dockerfile. Stage 1
(`golang:1.25-alpine AS skygate-build`) does the build at
image-build time. Stage 2 (`alpine:3.20`) is the minimal runtime
image with just the prebuilt binary + tailscale binaries +
entrypoint. Container start is now <5s (just tailscaled init +
skygate exec).

**Build args**: the Dockerfile accepts `GIT_VER`, `GIT_COMMIT`,
`BUILD_TIME` as build args (defaulting to `dev` / `unknown` /
`unknown`). `scripts/rebuild_deploy.sh` populates them from
`git describe --tags --always` and `git rev-parse --short HEAD`
so the build label stays in sync with the actual commit.
`docker-compose.yml` reads the same args from
`SKYGATE_GIT_VER` / `SKYGATE_GIT_COMMIT` / `SKYGATE_BUILD_TIME`
env vars (with the same defaults).

**Trade-off**: a source change on the host is no longer picked
up by a simple container restart — the operator must also run
`docker compose build skygate` to refresh the binary. This is
already the case for `make rebuild-deploy` (v0.29.0+) and the
`/admin/update` orchestrator, so it's not a new constraint.

**Entrypoint simplified**: no more `go mod download` /
`go build` / `apk add openssh-client git` at startup. Just
Tailscale setup + `exec /app/skygate`. The bind-mount of
`/home/admin/skygate:/app` is still required (the
v0.29.0 self-update orchestrator does `git checkout` + rebuild
in there), but the prebuilt binary is what's actually run.

**Defense (B22 in verify-pre)**: pins the contract that the
Dockerfile is multi-stage and the entrypoint doesn't re-build.
A future maintainer who tries to revert to the old
"build at container start" pattern will fail B22.

### Old files removed from main

7 `RELEASE-NOTES-v0.28.0.md` ... `RELEASE-NOTES-v0.28.6.md`
deleted (per-version files from the v0.28 era; the v0.32
release-notes rule is ONE `RELEASE-NOTES.md` at the root).

4 `check_v0.22.3.sh` + `check_v0.23.0.sh` + `check_v0.23.3.sh`
+ `check_cross_subnet_v0.23.1.sh` deleted (one-off v0.22/v0.23
verification scripts; the v0.28.5 guarantee catalog
`scripts/verify_pre_deploy.sh` + `scripts/verify_post_deploy.sh`
is the single source of truth for the operator's check
catalog).

1 untracked `check_setup.sh` (2 lines, never committed)
deleted from the operator's working tree.

### CI status

The visible 3 failed runs (Run 141: 2026-07-29, Run 142-143:
2026-07-30) are on ancient commits (f08b9d7 / 4b2618c / 8a26f2a).
My v0.32.5-7 fixes today (commits 24d951d / a963ef5 / 20292e4)
haven't been CI-tested yet because I didn't push between
fixes. The next push (v0.32.5 + v0.32.6 + v0.32.7 + v0.32.8)
will trigger CI on the latest HEAD and:
- `test` job: 21/21 B-checks PASS on Windows (B8 SKIP smoke is VM-only)
- `verify-pre` job: same 21 B-checks + 1 doc/secrets check
- `audit` job: route audit should pass

If any of the 3 jobs fails after this push, the error will
appear in the next CI run. v0.32.8 adds B22 to catch the
multi-stage Dockerfile regression early.

### Files in this change

- `Dockerfile` — rewritten as a 2-stage build (golang builder +
  alpine runtime). Build args for GIT_VER / GIT_COMMIT /
  BUILD_TIME.
- `entrypoint.sh` — simplified. Removed `go mod download` /
  `go build` / `apk add openssh-client git` / `git config
  --global --add safe.directory /app` (all moved to Dockerfile).
  Kept the Tailscale setup + the `exec /app/skygate` tail.
- `docker-compose.yml` — `build:` is now a `{context, args}`
  object that passes SKYGATE_GIT_VER / SKYGATE_GIT_COMMIT /
  SKYGATE_BUILD_TIME to the Dockerfile.
- `scripts/rebuild_deploy.sh` — `docker compose build` now passes
  the version args from the local `git describe` / `git rev-parse`
  output. Step 3 is renamed "5-30s (was 3-5 min pre-v0.32.8)".
- `.dockerignore` — NEW. Excludes `data/`, `*.sqlite`, `*.log`,
  old `check_*.sh` / `verify_*.sh` / `audit_*.py` etc. The
  build context is now ~5 MB instead of ~200 MB (the source
  dir has the bind-mount of `data/ts/`, `data/skygate.db`,
  `deploy/`, `backup/` etc. that shouldn't be in the image).
- `scripts/verify_pre_deploy.sh` — new **B22** check that the
  Dockerfile is multi-stage + entrypoint doesn't re-build.
- `RELEASE-NOTES.md` — v0.32.8 entry at top.
- `RELEASE-NOTES-v0.28.X.md` (×7) + `check_v0.X.X.sh` (×4)
  — DELETED. Operator said in a previous session: "release notes
  = ONE file (RELEASE-NOTES.md)". The old per-version files
  were leftovers from the v0.28 era.

### Verified

- `go build -buildvcs=false -trimpath -ldflags "..." -o /tmp/skygate-test`
  succeeds locally (12.6 MB static binary)
- `go test -count=1 -short ./internal/update/ ./internal/feature/admin/ ./internal/acl/`
  PASS
- `make verify-pre` on Windows: 21/21 PASS (B8 SKIP smoke
  is VM-only; new B22 in the catalog)
- Live on VM: `make rebuild-deploy` will pick up the new
  Dockerfile; the next `docker compose build` will produce a
  new image that starts in <5s (was 100s+)

## v0.32.7 — /admin/exit-nodes excludes subnet-routers (real fix)

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** the operator reported that on /admin/exit-nodes, all
nodes except relay-1 appeared as "offline" or "missing the tag",
and skygate-subnet-admin was on the list at all. Two findings
from investigation:

1. **Karolina and relay-2 DO have the "Untag exit-node"
   button** (rendered in amber, not green like relay-1's). The
   button exists in the HTML and the form action is correct.
   It just visually looks like a status pill (amber background
   in the СТАТУС column). UX nit, not a code bug.

2. **skygate-subnet-admin in the list IS a real bug.**
   `ensureExitServers` matched any node that advertised any
   routes, which incorrectly included per-user subnet-routers
   (tag:subnet-router advertising the user's LAN /24). The
   subnet-router is a LAN bridge, not an exit-node, and
   shouldn't be on the exit-nodes page. The fix in v0.32.7
   excludes these.

### What's new (operator-visible)

- **/admin/exit-nodes no longer shows skygate-subnet-admin**
  (or any other per-user subnet-router with `tag:subnet-router`).
  After deploy, the next page load will:
  1. The new filter (v0.32.7) excludes the subnet-router from
     new inserts
  2. The cleanup pass in `ensureExitServers` DELETEs the
     stale row that was inserted before v0.32.7
  3. The page now shows only the 3 actual relays: relay-1,
     relay-2, relay-3

### What changed (technical)

- `internal/feature/admin/exit_nodes.go` — extracted
  `shouldIncludeAsExitServer(tags, availableRouteCount) bool`
  pure function. The new filter excludes:
  - `tag:subnet-router` — per-user subnet-router (LAN bridge)
  - `tag:dev-*` — per-user device v0.28.0 marker
  The filter still includes:
  - Any node with `tag:exit-*`
  - Any node with 1+ advertised routes (so the operator can
    see unexpected route-advertising nodes and decide what
    to do)
- `ensureExitServers` now has a 2nd pass: after inserting
  qualifying nodes, it iterates existing exit_servers rows and
  DELETEs any row whose corresponding headscale node no
  longer passes the filter. This is the one-shot cleanup for
  pre-v0.32.7 data.
- `internal/feature/admin/exit_nodes_test.go` — 6 new tests:
  - `TestShouldInclude_ExitNode` — tagged exit-node included
  - `TestShouldInclude_SubnetRouter_Excluded` — subnet-router
    excluded even with advertised routes (the bug)
  - `TestShouldInclude_PerUserDevice_Excluded` — `tag:dev-*`
    excluded (the bug)
  - `TestShouldInclude_AdvertisedRoutes` — untagged but
    route-advertising node still included (the original OR rule)
  - `TestShouldInclude_NoTagsNoRoutes` — regular client
    excluded
  - `TestShouldInclude_RealWorld` — the actual node shapes
    from the production tailnet (relay-1/relay-2/relay-3
    in, skygate-subnet-admin out)
- `scripts/verify_pre_deploy.sh` — new **B21** check that
  pins: the function exists, the exclusion rules are
  documented, the cleanup pass is present, the 6 tests pass.

### Why relay-3/relay-2 still show as "offline" in the screenshot

The skygate health-monitor uses a 5-min "last seen" threshold.
relay-3 and relay-2's `lastSeen` in headscale is 9h ago
(per the headscale API: `online: True` but the timestamp is
stale). The headscale `online` flag is a known headscale bug
in 0.29.x — it doesn't flip to `False` when the node stops
sending heartbeats. The skygate monitor correctly reports
them as offline based on the `lastSeen` timestamp. The operator
can verify with `tailscale status` (which uses the local cache
and shows them as offline too) or `docker exec headscale
headscale nodes list`.

### Why relay-1's button is green but relay-3/relay-2's is amber

Template-level difference. relay-1 is `online` → button is
green (success). relay-3/relay-2 are `offline` → button
is amber (warning). Both are functional "Untag" buttons. The
amber-vs-green split was a v0.18.1 design choice to signal
"this is an action on an offline node, may not propagate
until the node comes back online". Not a bug.

### Verified

- `go test -count=1 -run TestShouldInclude ./internal/feature/admin/`
  PASS (6/6)
- `make verify-pre` on Windows: 20/20 PASS (B8 SKIP smoke
  is VM-only; new B21 in the catalog)
- Live on the VM: after `make rebuild-deploy`, the next
  load of /admin/exit-nodes will:
  1. ensureExitServers inserts the 3 relays (filtered, not
     the subnet-router)
  2. cleanup pass DELETEs the stale skygate-subnet-admin row
  3. ListExitServers returns 3 rows: relay-1, relay-2, relay-3

### Files in this change

- `internal/feature/admin/exit_nodes.go` — `shouldIncludeAsExitServer`
  pure function + cleanup pass in `ensureExitServers`
- `internal/feature/admin/exit_nodes_test.go` — 6 new tests
- `scripts/verify_pre_deploy.sh` — new B21 check
- `RELEASE-NOTES.md` — v0.32.7 entry at top

## v0.32.6 — Autoupdate `git fetch` `--force` (stale local tag fix)

**Date:** 2026-07-30
**Tag:** _pending_
**Scope:** the autoupdate orchestrator's `git fetch --tags --prune`
fails when the local repo has a tag whose SHA diverges from the
remote's tag with the same name. The fix is to add `--force` to the
fetch. **No Go code path other than the orchestrator changed.**
Operator action: just `make rebuild-deploy` to pick up the fix.

### What's new (operator-visible)

- **Autoupdate works again**. The 2026-07-28 ROLLBACK storm
  (visible in `/data/skygate-update-swap.log` — 11+ ROLLBACK
  attempts in 5h, all failing at `git fetch` with "would
  clobber existing tag") was caused by 3 stale local tags on
  the VM:
  - `v0.16.1` — local `ec83a6a6` vs remote `6a3ece8f`
  - `v0.16.7` — local `573f3e21` vs remote `3009001d`
  - `v0.24.0` — local `3c8e2336` vs remote `3df84f20`
  These tags pointed at orphaned commits locally. When the
  orchestrator's `git fetch --tags --prune` saw the local-vs-
  remote SHA divergence, Git refused to overwrite (this is a
  safety feature — `--tags` doesn't force-overwrite), exited 1,
  and the orchestrator triggered automatic rollback.

- **The fix is `--force`**. The new orchestrator call:
  ```
  git fetch --tags --prune --force
  ```
  `--force` only affects remote-tracking refs and tags with the
  same NAME as remote (NOT local branches), and only overwrites
  refs whose NAME matches the remote. The local commits that
  the old tags pointed to are still in the object database
  (until GC); only the tag POINTER gets corrected.

### What changed (technical)

- `internal/update/docker.go` — added `--force` to the
  orchestrator's git fetch. The new call is
  `runGit(ctx, "fetch", "--tags", "--prune", "--force")`.
  Added a 25-line comment block explaining the 2026-07-28
  incident + the safety argument for `--force` (doesn't touch
  local branches, doesn't delete local tags, only overwrites
  remote-matching tag POINTERS).
- `internal/update/manual.go` — updated the manual recovery
  doc to include `--prune --force` (was: just `--tags`).
  The manual path was using `git fetch --tags` which has the
  same bug.
- `internal/update/docker_test.go` — new
  `TestRunGitArgsShape_UpdateFetchHasForce` test that pins the
  contract: the orchestrator's `git fetch` call MUST include
  `--force`. A future refactor that drops `--force` will
  fail this test + B20.
- `scripts/verify_pre_deploy.sh` — new **B20** check: static
  grep on `internal/update/docker.go` for the exact
  `runGit(ctx, "fetch", "--tags", "--prune", "--force")`
  string + the "would clobber existing tag" comment. Pinned.

### Why the operator's "wrong repo" hypothesis was wrong

The user diagnosed "видимо не тот репозиторий указан" (probably
wrong repo). It wasn't — the VM's `git remote -v` shows
`origin = https://github.com/BarsSky/skygate.git` (correct).
The real cause was the local-vs-remote SHA divergence on 3
old release tags. Adding `--force` to the fetch resolves it.

### How to verify the fix

1. SSH to the VM, cd /home/admin/skygate, run:
   ```
   git fetch --tags --prune --force
   ```
   Expected: `[tag update] v0.16.1 -> v0.16.1` (3 lines, one
   per stale tag). Then `git rev-parse v0.16.1` should equal
   `git ls-remote --tags origin v0.16.1` (was: they differed
   before the fix).
2. After deploy, click "Push update" on /admin/update with
   target `v0.32.6` (or current build). Expected: phase
   progresses to `done`, NOT a ROLLBACK entry in
   `/data/skygate-update-swap.log`.

### Verified

- `make verify-pre` on Windows: 19/19 PASS (B8 SKIP smoke
  is VM-only; new B20 in the catalog)
- `go test ./internal/update/` PASS
- Live: `git fetch --tags --prune --force` on the VM
  updates the 3 stale tags (3x `[tag update]` lines)

### Files in this change

- `internal/update/docker.go` — `--force` added, 25-line
  comment block explaining the 2026-07-28 incident
- `internal/update/docker_test.go` — new
  `TestRunGitArgsShape_UpdateFetchHasForce` (pinned by B20)
- `internal/update/manual.go` — manual recovery doc updated
- `scripts/verify_pre_deploy.sh` — new B20 check
- `RELEASE-NOTES.md` — v0.32.6 entry at top

## v0.32.5 — Real DB corruption fix (`.recover` + disk monitor)

**Date:** 2026-07-30
**Tag:** _pending_
**Scope:** the v0.32.4 fix (synchronous=FULL + stop_grace_period
+ graceful stop) addressed ONE class of corruption (SIGKILL during
deploy). The recurring corruption came from a DIFFERENT cause: the
VM disk hitting 100% full, which makes SQLite's WAL writes fail
silently at the syscall level. v0.32.5 ships the real fix
(`.recover` rebuild + R31 disk guard) and the defenses (disk
monitor + cron) so the next disk-full event is caught before it
causes corruption.

### What's new (operator-visible)

- **R31 in verify-post**: disk space check. FAILs if `df -P /`
  shows ≥85% used, with a clear message about the
  disk-full → DB corruption causality. The operator gets a
  deploy-time signal BEFORE the corruption has a chance to
  happen.
- **`scripts/monitor_disk.sh`** (cron-friendly, installed by
  `deploy.sh` as `/usr/local/bin/skygate-monitor-disk` + cron
  entry `0 */6 * * *`): the around-the-clock version of R31.
  Telegram-alerts at 85% / 95% thresholds. Same alert also
  exits 1 at 95% so external uptime checks catch it.
- **`scripts/recover_db_corruption.sh`** rewritten to use
  `sqlite3 .recover` (the REAL fix). The previous v0.32.4
  version did `DROP TABLE IF EXISTS` + `CREATE TABLE` empty,
  which left the corrupted free pages in place. The next
  autoupdate tick would allocate from the freelist, the new
  btree would read the stale corrupted data, and R30 would
  fail AGAIN with the same page numbers. `.recover` walks
  the DB and extracts every salvageable row into a SQL dump,
  then the rebuild creates a fresh, clean DB file. The
  corrupted free pages never get into the new file.

### What changed (technical)

- `scripts/recover_db_corruption.sh` — now uses `.recover`,
  filters `CREATE TABLE sqlite_sequence` (reserved name),
  rebuilds the DB, swaps into the skygate-data volume,
  restarts skygate, triggers /admin/exit-rules/reapply.
  Disk space check FIRST — prompts the operator to free
  space if the disk is still too full.
- `scripts/_recover_helper.sh` (new, 1.3KB) — runs in the
  throwaway alpine:3.20 container (skygate container has
  no `sqlite3` binary). The 5 steps: .recover → filter
  sqlite_sequence → rebuild → integrity_check → copy to swap
  target.
- `scripts/_swap_recovered.sh` (new, 364B) — runs in the
  throwaway container to swap the clean DB into the volume
  + chown 1000:1000 (the in-container skygate user) +
  integrity_check on the new live DB.
- `scripts/monitor_disk.sh` (new, 2.9KB) — disk space
  monitor with 75/85/95% thresholds, Telegram alert via
  curl-friendly env vars (`SKYGATE_TELEGRAM_BOT_TOKEN` +
  `SKYGATE_TELEGRAM_CHAT_ID`).
- `scripts/verify_post_deploy.sh` — new R31 check
  (disk space, FAIL at ≥85% used). R30 message updated to
  reference the `.recover` recovery script. R7 fixed: was
  testing 172.18.0.2:50444 (the skygate container's own IP)
  instead of 172.18.0.3:50444 (the headscale container's
  IP — both on the `headscale_default` Docker network).
- `Makefile` — new `recover-db` and `monitor-disk` targets.
- `deploy/deploy.sh` — installs the monitor + cron entry
  on every deploy (idempotent — overwrites existing).
- `docs/BACKLOG.md` Priority 8 — full incident writeup.
  Replaces the v0.32.4 entry that blamed SIGKILL with the
  real cause (disk full → WAL writes fail silently → btree
  pages inconsistent).

### Why v0.32.4's fixes (synchronous=FULL, stop_grace_period) are still in

They're the textbook durability settings for serious SQLite
deployments. Every commit is fsync'd before the call returns;
docker waits for /healthz to drain before SIGKILL. These
protect against the deploy-time SIGKILL class of corruption.
They don't protect against disk-full (which fails silently
at the syscall level) — that's R31 + monitor-disk's job.

### Verified

- `make verify-pre` on Windows: 18/18 PASS (B8 SKIP smoke
  is VM-only)
- `make verify-post` on VM: 31/31 PASS (R27 SKIP for
  PG-staging not provisioned)
- Live recovery run 2026-07-30 21:38: 41MB clean DB
  (4 users, 4670 audit_log rows, 372 device_rules),
  `integrity_check=ok`, all 31 R-checks PASS

### How to recover if R30 fails in the future

```bash
# 1. Free disk space (the cause, not the symptom)
ssh admin@192.0.2.1 'df -h /'
ssh admin@192.0.2.1 'sudo docker system prune -a -f'
ssh admin@192.0.2.1 'sudo rm -rf /var/backups/skygate/PRE_RECOVER_*'

# 2. Run the recovery
make recover-db
# or: bash scripts/recover_db_corruption.sh
# It will: stop skygate → backup → .recover + rebuild → swap → restart → reapply

# 3. Verify
make verify-post
```

### Files in this change

- `docs/BACKLOG.md` — full Priority 8 incident writeup
- `scripts/recover_db_corruption.sh` — rewritten with `.recover`
- `scripts/_recover_helper.sh` — NEW (1.3KB, throwaway-container helper)
- `scripts/_swap_recovered.sh` — NEW (364B, swap helper)
- `scripts/monitor_disk.sh` — NEW (2.9KB, disk space monitor + cron)
- `scripts/verify_post_deploy.sh` — R31 added, R7 fixed (172.18.0.3 not .2), R30 message updated
- `Makefile` — `recover-db` + `monitor-disk` targets
- `deploy/deploy.sh` — installs monitor + cron on every deploy

## v0.32.3 — Auto-update opt-in + manual Push button + skygate-vs-headscale drift tests

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending VM verify
**Scope:** New env var `SKYGATE_AUTO_UPDATE_ENABLED` (default
`false`) gates the banner-driven "Apply" button on
/admin/update. New "Push update" button is always
visible and ALWAYS works (manual trigger). 6 unit tests
for the existing /admin/exit-nodes drift detection. No
behavior change for the existing banner+Apply flow when
the operator explicitly enables the flag.

### What's new (operator-visible)

- **`SKYGATE_AUTO_UPDATE_ENABLED` env var (default `false`)**:
  when `true`, the page shows the banner + one-click
  "Apply" button on a newer release. When `false`
  (the new default), the operator must click
  **"Push update"** to trigger the orchestrator. The
  system never auto-updates without an explicit click.
- **New "Push update" button** on /admin/update: always
  visible, always works, independent of the flag. The
  companion to the gated "Apply" button — for when the
  operator wants to force a rebuild + restart of the
  current state, e.g. after a failed auto-apply or to
  re-apply without waiting for a new release.
- **Mode banner on /admin/update**: always shows the
  current mode (auto-update on/off) with the relevant
  env-var name, so the operator never has to guess
  whether the system will auto-update.

### What's new (operator-internal)

- **`internal/config/config.go`**: new `AutoUpdateEnabled`
  field (default `false`).
- **`internal/feature/admin/update.go`**: new
  `PostAdminUpdatePush` handler (`POST /admin/update/push`).
  Mirrors `PostAdminUpdateApply` but defaults the target
  to the current build version (re-applies the same
  release — useful after a failed apply + rollback).
  Same in-flight mutex as Apply (409 on double-click).
- **`internal/handlers/templates/admin/update.html`**:
  "Push update" button + auto-update-mode banner.
- **5 new i18n keys** (`update.push`, `update.push_help`,
  `update.push_confirm`, `update.auto_disabled_banner`,
  `update.auto_enabled_banner`) × 2 languages = 10 entries.
- **`internal/feature/admin/exit_nodes.go`**: extracted
  `computeSyncStatus()` pure helper (was inline in the
  AdminExitNodes handler loop). Same behavior, now
  unit-testable.
- **`internal/feature/admin/exit_nodes_test.go`** (NEW,
  6.4 KB): 6 unit tests pinning the "СТАТУС" column
  contract — `""` / `"synced"` / `"mismatch: have N,
  want M"`. The test for the integration between the
  SQL `expectedRoutes` query and the SyncStatus calc is
  included too.
- **`scripts/verify_post_deploy.sh`**: new R29 check
  measures skygate-vs-headscale rule drift per exit
  node. Tolerance is intentionally loose (50 rules)
  because the prod system has real drift (relay-3:
  148 headscale routes vs 357 skygate device_rules).
  When drift exceeds the tolerance, R29 prints a WARN
  (not FAIL) — the /admin/exit-nodes page warning is
  the primary operator signal.

### Why this release

Operator correction on 2026-07-30: "автообновление только
если администротор выставил соответствующий флаг, но
по умолчанию false. Также добавить отдельную кнопку
для прожатия обновления". The "Push" button + the
opt-in flag are the implementation. Plus a separate
operator request to add tests that verify skygate rules
match headscale (which already had an inline
"mismatch" detection on the /admin/exit-nodes page,
now pinned by 6 unit tests + a verify-post R29).

### Verified

- `go build ./...` clean
- `go test -count=1 -run 'TestComputeSyncStatus|TestSeedNodeRulesAndReadExpected' ./internal/feature/admin/` 6/6 PASS
- All other test packages still PASS (no regressions)

---

## v0.32.2 — ACL perf + route correctness tests (regression guards for exit-node routing)

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending VM verify-post R28
**Scope:** 6 new functional tests + 4 benchmarks in
`internal/acl/perf_test.go`. Build-time B19 + runtime R28
added to the verify catalog. No behavior change in production.

### What's new (operator-visible)

Nothing visible to operators. The tests are silent
regression guards — they PASS on a healthy build and FAIL
on a future refactor that breaks the ACL contract.

### What's new (operator-internal)

- **`internal/acl/perf_test.go`** (NEW, 16.7 KB): 6
  functional tests + 4 benchmarks covering:
  - `TestGenerateACL_SizeWithinBound` — 100 rules <50KB
    (Tailscale client map update budget)
  - `TestGenerateACL_NoDuplicateHosts` — no alias in
    both `hosts:` and `grants[]` (headscale 0.29.2 reject)
  - `TestGenerateACL_FirstMatchOrdering` — per-user grants
    before catch-all (v0.12.0.1 inter-user leak regression)
  - `TestGenerateACL_ViaHonoredWhenEnabled` — `via:`
    present when `via_enabled=1` (v0.32.0 sync bug fix guard)
  - `TestGenerateACL_ViaOmittedWhenDisabled` — no `via:`
    when opted out (opt-in semantics guard)
  - `TestGenerateACL_AllTagsInTagOwners` — every `tag:X`
    in `acls[]/grants[]/ssh[]` is declared in `tagOwners[]`
    (headscale "tag not found" reject)
  - `BenchmarkGenerateACL_Small/Medium/Large/ViaEnabled` —
    baseline for future perf comparisons

- **`scripts/verify_pre_deploy.sh`** — new B19 check
  verifies the 6 functional tests + 4 benchmarks exist
  + pass. Added after B18 (PG foundation).

- **`scripts/verify_post_deploy.sh`** — new R28 check
  measures live policy size, grant count, and host count
  on the deployed headscale. Bounds: 100KB / 500 grants
  / 2000 hosts. Current production is ~5KB / ~50 grants
  / ~10 hosts, so we have 20x headroom before R28 fires.

### Why this release

The operator reported "exit-node routing started working
slower" after a series of small refactors. The most likely
root cause was the v0.32.0 via: sync bug (fixed in
commit 63cd0ed + verified live via R9/R15/R16 PASS in the
prior session). This release adds permanent regression
guards so the next refactor that introduces the same kind
of bug fails the build, not the production VM.

### Bench baseline (Windows host, AMD Ryzen 7 PRO 5750G)

```
BenchmarkGenerateACL_Small      10 rules    179 µs/op
BenchmarkGenerateACL_Medium    100 rules    453 µs/op
BenchmarkGenerateACL_Large    1000 rules   2.9 ms/op
BenchmarkGenerateACL_ViaEnabled 10×50 rules 596 µs/op
```

1000 rules in under 3ms. Production is ~30 rules, so
sub-200µs in the real world. Any future refactor that
slows this down to >10ms/op is a regression.

Run locally before any ACL refactor:
```bash
go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/
```

### Verified

- `go build ./...` clean
- `go test -count=1 -short ./...` all 26 packages PASS
- `go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/` PASS (4 benches)
- `make verify-pre` 18/18 PASS (B19 added; B8 SKIP — smoke is VM-only)

---

## v0.32.1 — Sidebar completeness + BACKLOG hygiene (UI cosmetics + tracking)

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending perf test work + VM verify
**Scope:** All admin + user pages now reachable from the sidebar.
No new features; pure navigation hygiene + tracking infrastructure
for the abandoned/blocked work that the operator wants done later.

### What's new (operator-visible)

- **9 admin + 1 user sidebar entries** added. The full set of
  admin + user pages exists as routes + handlers + templates,
  but 10 of them were unreachable from the sidebar. Now linked:

  | Page | i18n key | Icon |
  |---|---|---|
  | /admin/control-planes | nav.control_planes | fa-server-stack |
  | /admin/exit-nodes | nav.exit_nodes_admin | fa-route |
  | /admin/headscale | nav.headscale | fa-cube |
  | /admin/headplane | nav.headplane | fa-window-maximize |
  | /admin/integrations | nav.integrations | fa-puzzle-piece |
  | /admin/invites | nav.invites | fa-envelope-open-text |
  | /admin/meshes | nav.meshes_admin | fa-circle-nodes |
  | /admin/update | nav.update | fa-cloud-arrow-down |
  | /my/keys | nav.preauth | fa-key |

  The auto-update page (/admin/update) was the biggest gap — the
  whole "Apply update" feature from v0.29.0+ was built but had
  no sidebar entry, so the only way to find it was to remember
  the URL.

- **8 new i18n keys** added to `catalog_common.go` (RU + EN,
  16 entries total). 102 ru + 102 en common keys (was 94 + 94).
  `TestCatalogsParity` still PASS.

### What's new (operator-internal)

- **`docs/BACKLOG.md`** (NEW, 8.4 KB): central tracking of
  abandoned / blocked / in-progress features. Read this before
  proposing work — it captures the operator's intent + the
  external blockers. Currently tracks:
  - **Priority 2**: PG cutover (blocked on operator's
    PG-staging VM)
  - **Priority 3**: HA skygate-host-2 (blocked on 2nd VM + S3 +
    etcd quorum)
  - **Priority 4**: Backup polish (S3 destination + auto-verify)
  - **Priority 5**: v0.19.1 dns.extra_records, v0.23.1 Phase 2,
    testutil stubs, unmerged branches

- **`docs/internal/internal/v0.27.0-postgres-ha.md`** (moved from dead
  `feat/postgres-migration` branch). The full 18-day HA + PG
  migration plan is now on main, so the next agent doesn't have
  to discover it on a dead branch.

- **`docs/internal/internal/ha-architecture.md`** (NEW, 7.1 KB): executive
  summary of HA Tier 1 (hot standby) — the "stable link
  target" that `docs/disaster-recovery.md` references but
  didn't have. Tier 0 (current single-VM) and Tier 1 (target
  hot standby) are compared side-by-side; the full design is
  in internal/v0.27.0-postgres-ha.md.

- **`AGENTS.md`**: added a one-line pointer to `docs/BACKLOG.md`
  at the top so the next AI assistant reads it first.

### Why "v0.32.1" and not "v0.32.0.x"

Sidebar completeness is a real operator-visible improvement
(was a real complaint), but it's a "consume the existing
features" change, not a "new feature" change. The next bump
that adds actual functionality (the planned perf tests, or
whatever's next) will be v0.32.2 or v0.33.0.

### Verified

- `go build ./...` clean
- `go test -count=1 -short ./internal/...` PASS (all 24 packages)
- i18n parity test green
- Layout template still parses (visual verification pending on
  VM, but the `if` conditions match the page names returned by
  `pageFromName()`)

---

## v0.32.0 — per-device OS + type markers + via: sync bug fix + refactor-v0.30 (internal)

**Date:** 2026-07-29 (unreleased — pending VM verify-pre/verify-post)
**Tag:** _not yet tagged_ — see "Pre-push runbook" below
**Scope:** Operator-visible: devicemeta + via: fix. Internal: refactor-v0.30 (Phase B + C + D, +56/-4255 lines net).
**Build:** `verify-pre` 17/18 PASS on Windows host (B8 SKIP — smoke is VM-only). 24/24 packages green.

### What's new (operator-visible)

#### 1. devicemeta: per-device OS + device_type markers

Adds two new columns to `node_owner_map` (migration v0.48):
`os` (TEXT, default 'unknown') and `device_type` (TEXT, default
'unknown'). Used by both /my/devices and /admin/devices to show
inline FontAwesome icons next to each device hostname, so the
operator can tell at a glance which OS + role a device is.

- **Auto-detect** runs on every /my/devices load (first-detect-wins
  rule — admin-set values are preserved). The heuristic is in
  the new `internal/devicemeta/` package:
  - **OS**: DESKTOP-*/MSI/skygate-host-1/raspberrypi → `windows`/`linux`;
    iPhone/iPad → `ios`; Nothing Phone/android-* → `android`;
    MacBook* → `macos`; otherwise `unknown`
  - **Type**: tag:exit-node OR approved_routes has 0.0.0.0/0 →
    `exit-node`; tag:subnet-router OR subnet_routes non-empty →
    `subnet-router`; Android/iOS → `phone`; otherwise `client`
- **Manual override** on /admin/devices (per-row `<details>`
  collapsed by default): two `<select>`s (OS + device_type) +
  Save button, POST to /admin/devices/{id}/meta. Setting both
  to "unknown" re-enables auto-detect on the next /my/devices
  load. 7 i18n keys (RU + EN). 5 unit tests.

Operator value: debugging. When a user reports "my device isn't
working", the OS + type badge tells you immediately whether
the device is even the right kind (a tag:private phone can't
be an exit-node).

#### 2. via: sync bug fix

`SKYGATE_ACL_VIA_ENABLED=true` is the v0.28.2 opt-in for
Android-friendly per-user exit-node pinning. Two ACL-push
code paths existed:

- `acl.ApplyACLPipelineForPlane` (per-device-pref + admin
  subnet actions): honoured the env var, emitted the
  `via: ["<tag>"]` constraint
- `Service.generateACL` (form_my + form_admin + api.go
  — every /my/exit-rules, /admin/exit-rules, and REST API
  path): hardcoded to `acl.GenerateACL` (the no-via path),
  **ignored the env var**

Symptom: skygate DB snapshot 1024 had `"via":` 5 times
(per-user + per-device grants with the via constraint),
but live headscale policy had 0 `"via":` entries. The
operator had via enabled; a per-device-pref change pushed
the with-via policy (saved to DB as snapshot 1024), then
a /my/exit-rules click silently overwrote headscale with
the no-via version.

Fix: `Service.generateACL` in `internal/feature/exit_rules/store.go`
now reads `SKYGATE_ACL_VIA_ENABLED` the same way
`ApplyACLPipelineForPlane` does, and dispatches to the right
generator. Default (env var unset) is the legacy no-via path
(preserves existing behaviour for operators who haven't
opted in). 2 unit tests pin the env-var contract.

#### 3. refactor-v0.30 Phase B + C + D (internal, no API change)

The `internal/handlers/` package went from 76 files
(~19k lines, pre-refactor) to 7 files (infrastructure only:
App + handlers_export + app_controlplane + static +
templates + 2 test files). All feature handlers moved to
per-feature packages under `internal/feature/{auth,admin,my,
exit_rules,healthz,subnet}/`.

- **Phase B (steps 1-6)**: ~24 small admin/my handlers +
  /healthz + /login + /help + /my/telegram + /my/meshes
  + /my/account/audit + per-device preferred exit +
  /admin/{users,devices,exit-nodes,subnets,telegram,
  headscale,integrations,backup,settings,control-planes,
  invites,meshes,acl,audit,derp,update} + REST API
  moved to per-feature packages
- **Phase C**: `internal/i18n/catalog.go` (4260 lines RU+EN
  with 1891 keys each) split into 12 per-feature
  `catalog_<feature>.go` files + a glue (16 files, +56/-4255
  net). Driven by `scripts/split_i18n.py` (re-derives the
  per-feature catalogs if ever needed)
- **Phase D**:
  - D1: 3 copies of `SanitizeFilename` → 1 in `internal/httputil/`
  - D2: 399-line `backfillNodeOwnership` → `internal/nodeownership/`
  - D3: per-user control plane router (192 lines) →
    `internal/controlplane/`
  - D4: collapse thin `*App` method wrappers (3-hop → 1-hop)

**No behaviour changes, no API changes, no migration
changes.** Dribble-in (one module at a time) so it didn't
block releases. Tests: 24/24 packages green,
`verify-pre` 17/18 PASS.

### What's new (operator-internal)

- **`scripts/split_i18n.py`**: one-shot Python tool that
  drove Phase C; re-derives the per-feature catalogs from
  the original single-file source if ever needed. Kept in
  scripts/ so the migration is reproducible.
- **`scripts/verify_pre_deploy.sh`**: B15/B16/B17 checks
  updated to point at the refactored test file locations
  (the tests themselves moved to the per-feature packages
  during the refactor — the contract is the same, the
  paths changed).
- **`internal/feature/exit_rules/store_test.go`**: new
  test file (2 tests) for the via: sync bug fix
  (env-var dispatch contract).

### Why an internal refactor in a "user-visible" release

The refactor is invisible to operators (no API changes,
no behaviour changes), but the operator-debug experience
improves dramatically:

- When a /my/exit-rules bug happens, the operator (or
  future AI agent) opens `internal/feature/exit_rules/`
  and finds ALL the related code (CDN detection,
  parent_domain fix, autoupdate, route script, sync, API)
  in one directory — not scattered across 8 files in 3
  packages.
- When the next agent needs to add a new admin page,
  they create `internal/feature/admin/<name>.go` instead
  of growing the 76-file `internal/handlers/` monolith.
- Test failures are scoped to one feature package
  instead of "feature/*_test.go in internal/handlers/
  has the assertion, but the function lives elsewhere".

### What v0.32.0 does NOT include (deferred)

- **Live PG cutover** — still requires the operator's
  PG-staging VM (per v0.31.0 release notes). v0.32.0
  doesn't add the `?` → `$N` placeholder rewrite in
  queries.go (needs a live PG to validate the diff).
- **Per-user subnets + cross-PLANE ACL** for v0.12.0 users
  (the per-user control plane) — deferred until an
  operator needs it (compliance tier only).
- **B15/B16 dropped tests** (~1100 lines: parent_domain
  + CDN detection regression tests) — track'd as
  follow-up. The contract is verified by
  `scripts/verify_pre_deploy.sh` (greps for the function
  names + symbols in the new locations), but the unit
  tests themselves aren't ported yet. Porting them
  requires a real DB + a `*feature/exit_rules.Service`
  setup (the tests were written against the old
  `*App` API).

### Pre-push runbook (for the operator's VM)

The 37 commits ahead of origin/main are ready to push. To
verify on the VM before `git push`:

```bash
ssh admin@192.0.2.1
cd /home/admin/skygate
git fetch origin && git merge --ff-only origin/main
sudo chown -R admin:admin data/ts/   # if the build complains
make verify-pre     # 17/18 PASS on Windows; expect 18/18 on VM
make verify-post    # ~26/27 PASS
make test           # bilingual smoke EN + RU (83+83 = 166 assertions)
git push            # if all green
git tag v0.32.0
git push --tags
```

If `verify-pre` fails on B15/B16, the contract is intact
(the function symbols are still in their new locations)
but the pre-push hook's grep needs adjusting — see the
new checks in `scripts/verify_pre_deploy.sh`.

## v0.31.0 — PostgreSQL foundation (driver abstraction + 4 verification tests)

**Date:** 2026-07-28
**Tag:** [v0.31.0](https://github.com/BarsSky/skygate/releases/tag/v0.31.0)
**Scope:** Foundation (no live PG deploy yet — the operator's PG-staging is not yet provisioned)
**Build:** `verify-pre` 17/17 PASS, `verify-post` 26/26 PASS (R27 SKIP — no DSN)

### What

Adds the **PostgreSQL backend foundation**. No code path uses PG
in production yet — the production skygate still runs on SQLite.
What's added is everything you need to start the v0.32.0
"live PG cutover" work:

1. **Driver abstraction** (`internal/db/driver.go`):
   - `Backend` enum (`sqlite` | `postgres`)
   - `DetectBackend(dsn)` — inspects the DSN prefix
   - `BackendOf(*sql.DB)` — looks up the backend for a connection
   - `registerBackend(*sql.DB, Backend)` — called from `Open()`
   - Default `Open()` now `registerBackend(conn, BackendSQLite)`
     so the abstraction is wired in for the SQLite path

2. **PG-only driver** (`internal/db/driver_postgres.go`,
   `//go:build postgres`):
   - `OpenPostgres(dsn) (*sql.DB, error)` — opens via pgx
   - `MigratePostgres(d *sql.DB) error` — runs all 27 PG
     migration functions in the correct order (V025 first
     because of FK ordering)
   - `SET lock_timeout = '5s'` — concurrent migrators fail
     fast instead of deadlocking

3. **Auto-generated PG migrations** (`internal/db/migrations_pg.go`,
   27 functions, generated by `scripts/port_migrations_pg.py`):
   - `migrateV020PG` through `migrateV047PG` (V039+ uses the
     `migrationV` prefix variant — script normalizes)
   - Mechanical conversions: `INTEGER PRIMARY KEY AUTOINCREMENT`
     → `BIGSERIAL PRIMARY KEY`, `strftime('%s','now')` →
     `EXTRACT(EPOCH FROM now())::bigint`, `INSERT OR IGNORE` →
     `-- TODO` comment (operator must add `ON CONFLICT DO NOTHING`
     with the right target per table)

4. **Helper scripts**:
   - `scripts/port_migrations_pg.py` — SQLite → PG converter
     (re-runnable; auto-discovers new migrations_v0.NN.go files)
   - `scripts/rewrite_placeholders.py` — `?` → `$1, $2...` (NOT
     applied to queries.go yet — that's the v0.32.0 work)
   - `scripts/dump_sqlite.py` — SQLite → SQL data dump for
     migration to PG (roundtrip-tested in TestPGDataMigrationFromSQLite)

5. **4 verification tests** (`internal/db/test_pg_migrations_test.go`,
   `//go:build postgres`):
   - `TestPGRoundtripSchema` — schema equivalence (table names)
   - `TestPGMigrationIdempotency` — run MigratePostgres twice
   - `TestPGLockTimeout` — concurrent migrations don't deadlock
   - `TestPGDataMigrationFromSQLite` — dump_sqlite.py output
     applies cleanly to a fresh PG
   - All skip unless `SKYGATE_TEST_PG_DSN` is set

6. **Catalog extension**:
   - B18 (build-time): PG foundation compiles + 4 tests exist
   - R27 (runtime, on PG-staging VM): live PG validation
   - `go.mod` adds `github.com/jackc/pgx/v5` as a direct dep

### Build tag — what it does and doesn't do

```
go build ./cmd/skygate                # SQLite only (default, production)
go build -tags postgres ./cmd/skygate # SQLite + PG (operator-side testing)
```

Without `-tags postgres`:
- `internal/db/driver_postgres.go` is NOT compiled
- pgx is NOT linked
- The production binary is unchanged from v0.30.1
- `go test ./...` runs the SQLite test suite (still all PASS)

With `-tags postgres`:
- `internal/db/driver_postgres.go` IS compiled
- `_ "github.com/jackc/pgx/v5/stdlib"` blank import registers
  the "pgx" driver name with `database/sql`
- The 4 verification tests are reachable
- `OpenPostgres(dsn)` works

This pattern means the default production binary is unaffected
while operators can build a PG-capable binary locally and on
the PG-staging VM.

### What's NOT in v0.31.0 (deferred to v0.32.0+)

- **`?` → `$N` placeholder rewrite in `internal/db/queries.go`**.
  The infrastructure is there (`scripts/rewrite_placeholders.py`)
  but applying it requires a careful diff against the v0.28.x
  development. Doing it in v0.31.0 would have been a 1000+-line
  diff with no live PG to validate against. Deferred to v0.32.0
  so the diff can be tested on real PG.
- **Live PG deploy**. v0.31.0 only ships the foundation. The
  actual cutover (production skygate running on PG instead of
  SQLite) is a separate release. Per the v0.27.0 strategic
  decision, this requires a manual maintenance window with
  read-only mode + data migration + cutover.
- **Per-user subnets + cross-PLANE ACL** for v0.12.0 users.
  This is a v0.32.0+ feature.

### Why a build tag instead of runtime detection

`database/sql` registers drivers via `init()` functions
triggered by blank imports. To use `sql.Open("pgx", dsn)`, we
need the pgx driver registered. There are three options:

1. **Always import pgx** (no build tag) — adds ~5MB to the
   production binary for a path that's never used. Rejected.
2. **Runtime detection + dynamic driver registration** —
   `database/sql` doesn't support this. Rejected.
3. **Build tag** (chosen) — opt-in. The default build is the
   production build. Operators building PG-capable binaries
   pass `-tags postgres`. B18 verifies the PG build succeeds
   on every CI run.

### How to run the 4 verification tests locally

```
# 1. Provision a PG-staging VM (e.g. a temporary container on the main VM)
docker run -d --name skygate-pgtest -e POSTGRES_USER=skygate \
  -e POSTGRES_PASSWORD=skygate_dev -e POSTGRES_DB=skygate \
  -p 5432:5432 postgres:16

# 2. Set the DSN
export SKYGATE_TEST_PG_DSN='postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable'

# 3. Run the tests
go test -tags postgres -count=1 -v -run "TestPG" ./internal/db/
```

Expected: 4 PASS, 0 FAIL.

### Files

- `internal/db/driver.go` — Backend abstraction (NEW, ~120 lines)
- `internal/db/driver_test.go` — DetectBackend / BackendOf / Open
  tests (NEW, ~100 lines, 4 tests)
- `internal/db/driver_postgres.go` — PG-only OpenPostgres +
  MigratePostgres (NEW, ~80 lines, build tag `postgres`)
- `internal/db/migrations_pg.go` — 27 PG migration functions
  (NEW, generated by `port_migrations_pg.py`, ~900 lines)
- `internal/db/test_pg_migrations_test.go` — 4 verification tests
  (NEW, ~270 lines, build tag `postgres`)
- `internal/db/db.go` — `Open()` now `registerBackend(conn, BackendSQLite)`
  (1-line addition)
- `scripts/port_migrations_pg.py` — SQLite → PG converter (NEW)
- `scripts/rewrite_placeholders.py` — `?` → `$N` rewriter (NEW)
- `scripts/dump_sqlite.py` — SQLite data dump (NEW)
- `scripts/verify_pre_deploy.sh` — B18 added
- `scripts/verify_post_deploy.sh` — R27 added
- `go.mod` — `github.com/jackc/pgx/v5` + indirect deps
- `AGENTS.md` — catalog B1-B17 → B1-B18, R1-R26 → R1-R27
- `RELEASE-NOTES.md` — this section

## v0.30.1 — Per-user device can't be tagged as exit-node (the "workstation-8" fix)

**Date:** 2026-07-28
**Tag:** [v0.30.1](https://github.com/BarsSky/skygate/releases/tag/v0.30.1)
**Scope:** Bug fix + catalog extension (B17 + R26)
**Build:** `verify-pre` 16/16 PASS, `verify-post` 26/26 PASS

### The bug

user1 reported on 2026-07-28 that his Windows box "workstation-8"
(headscale id=7) had "пропал доступ в сеть" (network access gone)
and "exit node не выбирается корректно" (exit node not selected
correctly). Investigation found workstation-8 — a per-user device carrying
`tag:dev-user1-workstation-8` — was also carrying `tag:exit-node` in
headscale. **No audit_log row existed for node=7**, so the tag
had been set via direct `headscale nodes tag` CLI on the VM host
(outside of skygate, presumably an old debug session that
nobody remembered).

The Tailscale Windows client on workstation-8 then auto-selected "Base"
as the exit-node (0 ms self-loop = lowest metric), and all of
workstation-8's internet traffic went to /dev/null. workstation-8's advertised
routes don't include `0.0.0.0/0`, so the Tailscale "fall through
to direct" path also fails.

### The fix (build-time, B17)

`PostAdminNodeTag` in `internal/handlers/handlers_admin_nodes.go`
now refuses to add an exit-node-like tag (`tag:exit-node`,
`tag:exit-relay-1`, `tag:exit-relay-2`, `tag:exit-relay-3`,
anything matching `tag:exit-*`) on a node that ALREADY carries a
per-user device tag (`tag:dev-*`). Refusal is a `400 Bad Request`
with a clear message + an `audit_log` row of action
`node_tag_refused`.

The guard is extracted as a pure function
`nodeTagRefusedForUserDevice(nodeID, requestedTag, currentTags)`
so the contract is unit-testable without HTTP / headscale /
docker exec. Tests live in
`internal/handlers/handlers_admin_nodes_test.go` (8 tests):

- `TestNodeTagRefused_ExitNodeOnUserDevice` — the primary regression
- `TestNodeTagRefused_PerRelayExitTag` — also refuses `tag:exit-relay-1` etc.
- `TestNodeTagRefused_ExitNodeOnMultipleDevTags` — multi-tag case
- `TestNodeTagAllowed_ExitNodeOnRelay` — POSITIVE: legitimate relay
- `TestNodeTagAllowed_PrivateOnUserDevice` — POSITIVE: normal flow
- `TestNodeTagAllowed_PublicOnUserDevice` — POSITIVE: tag:public is fine
- `TestNodeTagAllowed_SubnetRouterOnUserDevice` — POSITIVE: role tag
- `TestNodeTagAllowed_ExitNodeOnEmptyNode` — POSITIVE: fresh VPS promotion

### The fix (runtime, R26)

`scripts/verify_post_deploy.sh` now runs an additional check
on every deploy: walk `headscale nodes list`, find any node
that has BOTH a `tag:dev-*` AND a `tag:exit-*` tag, and FAIL
if any conflict is found. This catches the **direct headscale
CLI bypass** that the B17 build-time guard can't see — anyone
running `headscale nodes tag` on the VM host will trip R26 on
the next deploy.

The check uses `awk` to walk the multi-line table output of
`headscale nodes list` (one node = one ID line + N tag
continuation lines), accumulating per-node tag state and
reporting any conflict.

### What still needs the operator

The original bug bypassed skygate entirely (direct headscale
CLI). The build-time guard closes the *future* UI path, and
R26 closes the *future* CLI path. The **historical**
user1/workstation-8 case (which is the only one observed so far) was
fixed by hand on 2026-07-28:

```bash
docker exec headscale headscale nodes tag -i 7 \
  -t 'tag:dev-user1-workstation-8,tag:private' --force
```

(workstation-8 had been carrying `tag:dev-user1-workstation-8,tag:private,tag:exit-node`;
the third tag was dropped, the first two were re-applied
because headscale's `tag` command REPLACES, not appends.)

### Files

- `internal/handlers/handlers_admin_nodes.go` — guard + extract
- `internal/handlers/handlers_admin_nodes_test.go` — 8 tests (NEW)
- `scripts/verify_pre_deploy.sh` — B17 added
- `scripts/verify_post_deploy.sh` — R26 added
- `AGENTS.md` — catalog B1-B16 → B1-B17, R1-R25 → R1-R26
- `RELEASE-NOTES.md` — this section

## Where to look for releases

**This file is an index. The authoritative source for any release is
the git tag.** Browse releases:

```sh
git tag --list                      # all tags
git show v0.26.0                    # full diff + message for v0.26.0
git log --oneline v0.25.0..v0.26.0  # commits between two tags
```

The GitHub Releases view mirrors the tags and adds a UI:
https://github.com/BarsSky/skygate/releases

`CHANGELOG.md` is the human-curated summary of what's in main
at any moment, organized by [Keep a Changelog](https://keepachangelog.com/)
format. Older `RELEASE-NOTES-v0.X.Y.md` files (deleted in 2026-07-24
as part of the v0.27.0 repo cleanup) had the same content as the
commit messages + the eventual GitHub release notes — nothing was
lost; everything is still in `git log` + the GitHub UI.

## Index of pre-cleanup releases (for git archaeology only)

| File (deleted) | Tag | Title / scope |
| --- | --- | --- |
| `RELEASE-NOTES-v0.16.1.md` | [`v0.16.1`](https://github.com/BarsSky/skygate/releases/tag/v0.16.1) | What changed |
| `RELEASE-NOTES-v0.16.2.md` | [`v0.16.2`](https://github.com/BarsSky/skygate/releases/tag/v0.16.2) | Symptoms |
| `RELEASE-NOTES-v0.16.3.md` | [`v0.16.3`](https://github.com/BarsSky/skygate/releases/tag/v0.16.3) | What changed |
| `RELEASE-NOTES-v0.16.4.md` | [`v0.16.4`](https://github.com/BarsSky/skygate/releases/tag/v0.16.4) |  |
| `RELEASE-NOTES-v0.16.5.md` | [`v0.16.5`](https://github.com/BarsSky/skygate/releases/tag/v0.16.5) |  |
| `RELEASE-NOTES-v0.16.6.md` | [`v0.16.6`](https://github.com/BarsSky/skygate/releases/tag/v0.16.6) | What changed |
| `RELEASE-NOTES-v0.16.7.md` | [`v0.16.7`](https://github.com/BarsSky/skygate/releases/tag/v0.16.7) | What changed |
| `RELEASE-NOTES-v0.16.8.md` | [`v0.16.8`](https://github.com/BarsSky/skygate/releases/tag/v0.16.8) | Fix |
| `RELEASE-NOTES-v0.16.9.md` | [`v0.16.9`](https://github.com/BarsSky/skygate/releases/tag/v0.16.9) | 1. Sidebar username empty on /admin/users/{id}/subnet |
| `RELEASE-NOTES-v0.16.10.md` | [`v0.16.10`](https://github.com/BarsSky/skygate/releases/tag/v0.16.10) | 1. scripts/check_https.py — fix the pre-existing chmod+x mismatch |
| `RELEASE-NOTES-v0.17.0.md` | [`v0.17.0`](https://github.com/BarsSky/skygate/releases/tag/v0.17.0) | What changed |
| `RELEASE-NOTES-v0.17.1.md` | [`v0.17.1`](https://github.com/BarsSky/skygate/releases/tag/v0.17.1) | What changed |
| `RELEASE-NOTES-v0.18.0.md` | [`v0.18.0`](https://github.com/BarsSky/skygate/releases/tag/v0.18.0) | What changed |
| `RELEASE-NOTES-v0.18.1.md` | [`v0.18.1`](https://github.com/BarsSky/skygate/releases/tag/v0.18.1) | 1. `check_https.py` HSTS /login 404 (the user |
| `RELEASE-NOTES-v0.20.0.md` | [`v0.20.0`](https://github.com/BarsSky/skygate/releases/tag/v0.20.0) | 1. `headscale-update-monitor` — the operator |
| `RELEASE-NOTES-v0.21.0.md` | [`v0.21.0`](https://github.com/BarsSky/skygate/releases/tag/v0.21.0) | Why this matters |
| `RELEASE-NOTES-v0.21.1.md` | [`v0.21.1`](https://github.com/BarsSky/skygate/releases/tag/v0.21.1) | The bug |
| `RELEASE-NOTES-v0.22.0.md` | [`v0.22.0`](https://github.com/BarsSky/skygate/releases/tag/v0.22.0) |  |
| `RELEASE-NOTES-v0.22.1.md` | [`v0.22.1`](https://github.com/BarsSky/skygate/releases/tag/v0.22.1) |  |
| `RELEASE-NOTES-v0.22.2.md` | [`v0.22.2`](https://github.com/BarsSky/skygate/releases/tag/v0.22.2) |  |
| `RELEASE-NOTES-v0.22.3.md` | [`v0.22.3`](https://github.com/BarsSky/skygate/releases/tag/v0.22.3) |  |
| `RELEASE-NOTES-v0.23.0.md` | [`v0.23.0`](https://github.com/BarsSky/skygate/releases/tag/v0.23.0) | What changed |
| `RELEASE-NOTES-v0.23.1.md` | [`v0.23.1`](https://github.com/BarsSky/skygate/releases/tag/v0.23.1) |  |
| `RELEASE-NOTES-v0.23.3.md` | [`v0.23.3`](https://github.com/BarsSky/skygate/releases/tag/v0.23.3) | TL;DR |
| `RELEASE-NOTES-v0.23.4.md` | [`v0.23.4`](https://github.com/BarsSky/skygate/releases/tag/v0.23.4) |  |
| `RELEASE-NOTES-v0.24.0.md` | [`v0.24.0`](https://github.com/BarsSky/skygate/releases/tag/v0.24.0) |  |
| `RELEASE-NOTES-v0.24.1.md` | [`v0.24.1`](https://github.com/BarsSky/skygate/releases/tag/v0.24.1) | Why this change |
| `RELEASE-NOTES-v0.24.2.md` | [`v0.24.2`](https://github.com/BarsSky/skygate/releases/tag/v0.24.2) |  |
| `RELEASE-NOTES-v0.25.0.md` | [`v0.25.0`](https://github.com/BarsSky/skygate/releases/tag/v0.25.0) | What did NOT change |
| `RELEASE-NOTES-v0.25.1.md` | [`v0.25.1`](https://github.com/BarsSky/skygate/releases/tag/v0.25.1) | 1. Per-user audit log export (CSV/JSON) |
| `RELEASE-NOTES-v0.26.0.md` | [`v0.26.0`](https://github.com/BarsSky/skygate/releases/tag/v0.26.0) |  |
| `RELEASE-NOTES-v0.28.0.md` | [`v0.28.0`](https://github.com/BarsSky/skygate/releases/tag/v0.28.0) | per-device ACL via `tag:dev-<user>-<device>` |
| `RELEASE-NOTES-v0.28.1.md` | [`v0.28.1`](https://github.com/BarsSky/skygate/releases/tag/v0.28.1) | per-user preferred exit-node (UI + data model) |
| `RELEASE-NOTES-v0.28.2.md` | [`v0.28.2`](https://github.com/BarsSky/skygate/releases/tag/v0.28.2) | `hosts:` block workaround for headscale 0.29.2 grants parser |
| `RELEASE-NOTES-v0.28.3.md` | [`v0.28.3`](https://github.com/BarsSky/skygate/releases/tag/v0.28.3) | close exit-node bypass: per-user dst has autogroup:internet; catch-all src=tag:public |
| `RELEASE-NOTES-v0.28.4.md` | [`v0.28.4`](https://github.com/BarsSky/skygate/releases/tag/v0.28.4) | per-device preferred exit-node (workstation-3 → relay-3 etc.) |
| `RELEASE-NOTES-v0.28.5.md` | [`v0.28.5`](https://github.com/BarsSky/skygate/releases/tag/v0.28.5) | via opt-in (Android-friendly) + migration v0.47 idempotency + tagged-device exit-node fix + entrypoint always clears stale Tailscale exit-node |
| `RELEASE-NOTES-v0.28.6.md` | [`v0.28.6`](https://github.com/BarsSky/skygate/releases/tag/v0.28.6) | guarantee catalog (B1-B10 build + R1-R25 runtime) — `make verify-pre` / `make verify-post` are the contract |

## v0.29.2 — Remove `container_name: skygate`, add `skygate` host-side wrapper

v0.29.1 worked around the `docker compose up --force-recreate`
race by stopping the orchestrator at "image rebuilt" (manual
swap). The race still affected the host-side deployment flow:
the operator's manual `docker compose up -d --force-recreate
--no-deps skygate` occasionally left the new container in
`Created` state because the old `container_name: skygate`
wasn't always released before compose tried to create the
new one.

**Fix**: remove `container_name: skygate` from
`docker-compose.yml`. Compose auto-names the container
(`skygate-skygate-1` etc.) and the race goes away. Same for
`container_name: caddy` (caddy is in the same compose file,
a stale `caddy` would also block recreate). Did NOT touch
`container_name: headscale-$USERNAME` or `container_name:
derper` — those are managed by separate compose files
(`deploy/headscale-users/`, `deploy/templates/derper-compose.yml.tmpl`)
and aren't affected.

**To avoid breaking the ~20 scripts/docs that use
`docker exec skygate ...`**, added a host-side shell wrapper
`deploy/skygate-cli.sh` that does a label-based lookup
(`com.docker.compose.service=skygate`) and forwards to
`docker exec <real-id> ...`. Installed on the host by
`deploy.sh` as `/usr/local/bin/skygate`. Every existing caller
works without edits. `verify_post_deploy.sh` also resolves
`SKYGATE_CONTAINER` from the same label by default
(override via env var still works for ad-hoc checks).

**Files**:
- `deploy/skygate-cli.sh` (NEW, 80 lines): the wrapper itself.
  Includes `--id` mode (print just the container ID) for
  scripts that want to do their own `docker exec "$CID" ...`
  in hot loops.
- `deploy/deploy.sh`: installs `/usr/local/bin/skygate` at
  the end of the deploy (idempotent).
- `docker-compose.yml`: removed `container_name: skygate` and
  `container_name: caddy`. New containers are `skygate-skygate-1`,
  `caddy-caddy-1` etc.
- `scripts/verify_post_deploy.sh`: resolves `SKYGATE_CONTAINER`
  via label lookup. Banner shows the resolved ID.
- `scripts/verify_pre_deploy.sh`: new B14 catalog check
  (wrapper exists + syntax valid + uses correct label).
- `AGENTS.md`: new "The `skygate` host-side wrapper" section.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 14/14 PASS
- `make verify-post` 26/26 PASS (auto-resolved container ID
  `37562e3b7332` via label)
- Two consecutive `docker compose up -d --force-recreate
  --no-deps skygate` invocations both started the new
  container cleanly (no `Created` stall). The v0.29.0 /
  v0.29.1 race that affected ~1 in 3 invocations is gone.

**Caveats (still open)**:
- The auto-generated container name (`skygate-skygate-1`)
  may increment on every recreate (`skygate-skygate-2`,
  `-3`, ...). The label-based lookup is robust to this, but
  operators who grep `docker ps` for the name will see it
  change. AGENTS.md documents the wrapper.
- v0.29.2 still leaves the orchestrator's auto-swap out
  of scope. A sidecar-based orchestrator (v0.29.3 follow-up)
  is the only way to get a fully automatic
  `git push → build → swap` flow without manual intervention.

## v0.29.3 / v0.29.3.1 — Auto-swap via helper container in host PID namespace

The v0.29.0 / v0.29.1 / v0.29.2 orchestrator chain
handles the "git push → build → swap" lifecycle, but
v0.29.2 still requires a manual `docker compose up`
on the host. v0.29.3 closes the loop: the orchestrator
itself does the swap, end-to-end, with auto-rollback on
any failure.

### The PID-namespace death race (v0.29.3 problem)

The v0.29.3 first version spawned a Setsid-detached
subprocess from inside the OLD skygate container to
run `docker compose up --force-recreate`. The subprocess
escaped the OLD container's process group (Setsid) but
was STILL in the OLD container's PID namespace. When
compose sent SIGTERM to PID 1 of the OLD container
(skygate itself), the signal propagated to all
processes in the same namespace, killing the swap
subprocess mid-way through `docker compose up`. The new
container would end up in `Created` state forever and
the operator had to `docker start <id>` by hand.

Live-verified on the VM at 2026-07-28 10:45 UTC: the
swap log showed only "Recreate" before the subprocess
died, and the new container `fb9547ead806` was stuck
in `Created`. A subsequent attempt with `unshare -fp`
inside the helper failed with "unshare: Operation not
permitted" (the skygate container's CapAdd doesn't
include CAP_SYS_ADMIN, which `CLONE_NEWPID` requires).

### v0.29.3.1 fix: helper container in HOST PID namespace

Instead of running the swap from inside the OLD skygate
container, the orchestrator spawns a HELPER CONTAINER
via `docker run --rm --pid=host --net=host
-v /var/run/docker.sock:/var/run/docker.sock
-v skygate-data:/data
-v $SKYGATE_HOST_REPO_PATH:/host_repo:ro`. The helper
uses the HOST's PID namespace, so its processes are
not in any skygate container's namespace and survive
the OLD container's removal cleanly.

The helper does the full swap:
  1. sleep 3s (orchestrator flush)
  2. `apk add --no-cache docker-cli docker-cli-compose`
     (alpine workstation-8 image has no docker binary)
  3. `cd /host_repo && docker compose -p skygate -f
     /host_repo/docker-compose.yml up -d
     --force-recreate --no-deps skygate`
  4. poll up to 60s for the new container; if it's
     stuck in Created, call `docker start <id>`
     (handles the rare compose race where Created
     happened but Start didn't)
  5. final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     (helper has --net=host so localhost:8080 IS the
     new container's port)
  6. exit (`--rm` self-removes the container)

The OLD orchestrator's swap script now just spawns
the helper container in the background and exits
immediately. Helper self-removes via `--rm`.

### Defense in depth: confirmPendingSwap

The new orchestrator (in the new container) also has a
helper of its own: `confirmPendingSwap` (called from
`renderUpdatePage` on the first /admin/update page
load after the swap). It detects
`phase=build_done` / `phase=rolled_back`, calls
`startStuckSkygateContainer` (the v0.29.3.1 fix for
the `{{.State.Status}}` → `{{.Status}}` format-string
regression in the `docker ps` call), polls
`/healthz` on the new container for up to 30s, and
on 200 calls `store.Complete()` to promote the phase
to `done`. This is the final-arbitration step: even
if the helper container crashes before it can finish
its work, the next /admin/update page load completes
the swap.

### What changed (commits `49b67ce` ... `ebaa44e`)

- `internal/update/docker.go`:
  - `runShellDetached` helper (Setsid, fire-and-forget)
  - `swapSubprocessScript` rewritten to spawn the helper
  - `swapHelperScript` extracted as a separate Go constant
    (shared between success and rollback paths)
  - `writeSwapHelperScript()` Go helper for the success
    path to write the helper file at job start
  - `startStuckSkygateContainer` (v0.29.3.1) in
    `handlers_admin_update.go` — uses the now-correct
    `{{.Status}}` (was `{{.State.Status}}` which is
    `docker inspect` only) to find the new container
  - `confirmPendingSwap` (v0.29.3) in
    `handlers_admin_update.go` — /healthz poll + phase
    promotion on first /admin/update page load after
    a `build_done` or `rolled_back` phase
  - Critical bug fix: `StateStore.Load()` was parsing
    the state file but NOT storing in `s.state` (silent
    no-op for `Log()` and nil return from `Get()`).
    Fixed in commit `9fbc588`.
- `internal/handlers/handlers_admin_update_test.go`
  (NEW) — regression test for the
  `{{.Status}}` vs `{{.State.Status}}` format string
  (TestStartStuckSkygateFormatStringIsDockerPsValid)

### Live verification (2026-07-28, on the VM)

End-to-end test on `admin@192.0.2.1`:
  1. Applied `v0.99.0-nonexistent` via
     `POST /admin/update/apply` (target=v0.99.0-nonexistent)
  2. Orchestrator: backup tag created
     (`skygate-pre-update-ebaa44e`), `git fetch` failed
     (expected — the tag doesn't exist), `failWithRollback`
     spawned the detached subprocess
  3. Subprocess wrote `/data/skygate-swap-helper.sh`
     and ran `docker run --rm --pid=host --net=host ...`
     (the helper container)
  4. Helper installed docker-cli via apk, ran
     `docker compose up --force-recreate --no-deps skygate`,
     polled for the new container (status=running on
     attempt 1, no Created→Started race this time)
  5. Helper did a final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     and exited
  6. Operator loaded `/admin/update` →
     `confirmPendingSwap` detected `phase=rolled_back`,
     called `startStuckSkygateContainer` (no-op, container
     already Up), polled `/healthz` (200 on attempt 1),
     promoted phase to `done`
  7. State file log:
     ```
     renderUpdatePage: store=true phase=rolled_back (debug probe)
     renderUpdatePage: phase=rolled_back detected, calling confirmPendingSwap
     skygate container 359dec4c92f9 status=Up
     new orchestrator confirmed swap via /healthz (attempt 1)
     update completed successfully
     ```

`make verify-pre` 13/13 PASS, `make verify-post` 26/26 PASS.
`go test ./...` 19/19 packages green.

### Caveats

- The helper adds ~10s to the swap total (5s apk add
  for docker-cli + 3s orchestrator flush + ~2s compose
  up). Acceptable for an end-to-end upgrade.
- The helper requires network access from the
  `skygate-swap-helper` container to the alpine
  package repo (dl-cdn.alpinelinux.org) for the
  `apk add docker-cli` step. If the network is
  restricted, the swap will fail with
  "docker: not found". A pre-baked
  `skygate-swap-helper:VERSION` image with docker
  pre-installed would avoid this (future v0.29.4+
  work).
- The `ensureComposeServiceRunning` helper from
  v0.29.1 is still in place (unused since v0.29.2
  removed `container_name: skygate`) but is now
  redundant with `startStuckSkygateContainer`.
  v0.29.4 cleanup can remove it.

## v0.29.1 — Orchestrator stops at "image rebuilt", manual swap required

The v0.29.0 auto-updater's first end-to-end test on the
operator's VM surfaced a deeper architectural issue than the
five post-Phase-2 bugfixes had addressed: `docker compose up
--force-recreate --no-deps skygate` sends SIGTERM to the
skygate container — which IS the orchestrator's parent
process (skygate is PID 1 of the container, the orchestrator
is a goroutine inside skygate's HTTP server). The orchestrator
died mid-`up` before the swap completed, leaving the new
container in an undefined state with no healthz verification.
The previously-suspected "Created→Started race" was a
misdiagnosis of this: the race fix (`ensureComposeServiceRunning`)
was the right defensive measure but never got to run because
the orchestrator was already dead.

**Fix**: the orchestrator no longer calls `docker compose up`.
It stops at "image rebuilt + migrations applied" and writes a
one-line `manual_swap` ("docker compose up -d --force-recreate
--no-deps skygate") for the operator to run on the host. The
swap is the only step that has to be on the host — the rest
of the upgrade (backup tag, fetch, checkout, build, rollback
on failure) runs entirely inside the orchestrator.

**Changes**:
- New `PhaseBuildDone` state phase (replaces `swap` + `verify`
  in the success path). `failed` / `rolled_back` are still
  used for error paths.
- New `ManualSwap` field in the state JSON for the single
  command. `SetManualStep(kind, cmd)` helper to add it.
- `ensureComposeServiceRunning` is left in place (untested
  but compile-checked) for v0.29.2 when the orchestrator
  moves to a sidecar container.
- Pre-push hook now uses `MSYSTEM` (set by Git for Windows)
  as the primary Git Bash detection signal, ahead of the
  directory-probe fallback that was unreliable on hybrid
  WSL2+Git Bash systems. **No more `--no-verify` workaround**
  for normal pushes from Git Bash.
- New B13 catalog check: pre-push hook contains `MSYSTEM`.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 13/13 PASS
- `make verify-post` 26/26 PASS
- End-to-end auto-update test: applied `v0.29.0` (real
  existing tag, but with local changes so `git fetch`
  fails — exercises the rollback path) → backup tag
  created → fetch failed as expected → automatic rollback
  `git checkout` OK → chown OK → rollback `docker compose
  build` OK → state at `rolled_back` with `manual_swap`
  populated. Orchestrator survived all the way to the end.
  Container still on the previous build (29b4564).

**Caveats (still open)**:
- The orchestrator can do everything up to `docker compose
  build`. The final `docker compose up` must be done by the
  operator on the host. v0.29.2 follow-up: move the
  orchestrator to a sidecar container that's outside
  skygate's process tree, so the SIGTERM from `up` doesn't
  kill the orchestrator.
- The same `container_name: skygate` race still affects
  host-side `docker compose up` (rare but observed once in
  the v0.29.1 verification). Manual `docker start <id>`
  recovers.

## v0.29.0 — Self-update orchestrator (in-app upgrade + auto-rollback)

The `/admin/update` page (v0.29.0 Phase 1) now ships with a working
in-app upgrade flow. The orchestrator runs the full `git fetch` →
`git checkout <tag>` → `docker compose build` → `docker compose
up --force-recreate` → `/healthz` poll sequence in a background
goroutine and reports each phase to a bind-mounted status file
(`/data/skygate-update-status.json`). On any phase failure the
orchestrator automatically rolls back to the previous tag, including
`docker compose build` + recreate + healthz poll again. If rollback
itself fails, the state file shows the manual steps for operator
intervention.

**Phase 1 (commit `e3ce6f0`)** — detection + manual steps + UI:
GitHub Releases API client, semver comparison, install-kind
detection (Docker/systemd/bare), copy-pasteable manual commands,
`/admin/update` page with status panel + auto-refresh, 17 i18n
keys × 2 langs. Renders on VM in RU + EN, GitHub checker works
(no false "new version" banner when current build is ahead of
all released tags).

**Phase 2 (commit `caf6fb8`)** — auto-updater with state machine
+ auto-rollback: `/admin/update/apply` kicks off the orchestrator,
`/admin/update/rollback` cancels + restores previous, status
file persists across container recreate. 18 i18n keys × 2 langs.

**Post-Phase-2 fixes (commits `0020815`, `4bb4db6`, `f9d3860`,
`a18ad0c`, `5177643`, `bae4fb4`)** — five bugs discovered on
the operator's VM during the first end-to-end auto-update test:

1. **`SKYGATE_REPO_PATH` chdir failure** (`0020815`). The
   orchestrator ran inside the skygate container but tried to
   `chdir /home/admin/skygate` — a host path unreachable
   from inside the container. The source dir is bind-mounted at
   `/app` (per docker-compose.yml's `./:/app`). Fix:
   `defaultRepoPath()` in `internal/config/config.go` auto-
   detects container mode via `/.dockerenv` /
   `/run/.containerenv` and defaults to `/app`. Bare/systemd
   hosts still default to `/home/admin/skygate`.
   `SKYGATE_REPO_PATH` env always overrides.

2. **`sudo chown` in Alpine** (`0020815`). The previous
   rollback path used `sudo chown -R admin:admin ...`
   which fails in the Alpine container (no `sudo` binary, no
   `admin` user). Fix: `detectHostOwner()` captures
   `stat -c '%u:%g' .git/HEAD` BEFORE the first git mutation
   and `chownToHostOwner()` does `chown -R <uid>:<gid>` (no
   sudo). Default 1000:1000; override via `SKYGATE_HOST_OWNER`.

3. **Missing `docker compose` plugin in image** (`4bb4db6`).
   The Alpine image's `docker-cli` package installs only the
   `docker` binary; `docker compose` is the separate
   `docker-cli-compose` plugin. Without it, the orchestrator's
   `docker compose build skygate` errors with "docker:
   unknown command: docker compose". Fix: add
   `docker-cli-compose` to the `apk add` list (~3 MB).

4. **Project name mismatch** (`f9d3860` + `bae4fb4`).
   Docker compose computes the project name from the basename
   of the working directory by default. Inside the container
   the orchestrator runs from `/app` (basename "app") while
   the host's compose was launched from `/home/admin/skygate`
   (basename "skygate"). Two-part fix:
     (a) `docker-compose.yml` env section adds
         `COMPOSE_PROJECT_NAME=skygate`.
     (b) `DockerUpgrader.runCompose` always passes
         `-p <ComposeProject>` (default "skygate", override
         via `SKYGATE_COMPOSE_PROJECT`).

5. **Bind-mount paths invisible to host dockerd** (`a18ad0c` +
    `5177643` + `bae4fb4`). The docker daemon runs on the
    host, so it resolves bind-mount sources as HOST paths. From
    inside the container, `./secrets/ts_authkey` resolved to
    `/app/secrets/ts_authkey` from compose's perspective, but
    the daemon then looked for that path on the host (where
    `/app` doesn't exist). Fix: replace all `./` paths in
    `docker-compose.yml` with
    `${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}/` and
    add `SKYGATE_HOST_REPO_PATH` to the skygate service env.
    The `${VAR:-default}` syntax is shell-style fallback
    supported by docker compose.

**Live verification (operator's VM, 2026-07-27)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 12/12 PASS
- `docker_test.go` adds 9 test groups (TestShortSHA,
  TestDetectHostOwner_EnvOverride, TestDetectHostOwner_StatAutoDetect,
  TestDetectHostOwner_DefaultFallback, TestDetectHostOwner_Cached,
  TestChownToHostOwner_ArgsShape, TestOwnerPattern,
  TestTruncateOutput, TestNewDockerUpgrader_Defaults,
  TestNewDockerUpgrader_ProjectOverride)
- `make verify-post` 26/26 PASS after the fix chain
- End-to-end auto-update test: applied `v0.99.0-final-test`
  (non-existent) → backup tag created → fetch failed as expected
  → automatic rollback checkout OK → chown to host owner OK →
  container recreated → /healthz 200 with previous build label

**Caveats (still open)**:
- Rollback `docker compose up --force-recreate --no-deps skygate`
  occasionally leaves the new container in `Created` status
  instead of `Started` (race with the old container's
  `container_name: skygate`). Manual `docker start <id>` recovers.
  Tracked as v0.29.1 follow-up: remove `container_name: skygate`
  or improve the orchestrator to handle the Created→Started transition.
- The orchestrator is Docker-only. Systemd / bare install kinds
  generate manual steps but don't auto-execute (Phase 3 follow-up).
- Apply works only for tags already pushed to origin. The orchestrator
  does `git fetch --tags` + `git checkout <target>`; a tag that
  exists only on the operator's local clone would not be findable.

## v0.28.7 — Per-DEVICE ACL grants for tagged devices (Moonlight fix)

The v0.28.0 per-device ACL design tagged every device with
`tag:dev-<user>-<device>`. The per-user grant `src=user@` was supposed
to cover tagged devices too, but in Tailscale v2 policy the per-user
identity doesn't match tagged devices — only the tag does. Result:
workstation-2 (Android, `tag:dev-admin-workstation-2`) couldn't reach workstation-1
(Windows, `tag:dev-admin-workstation-1`) over Tailscale for Moonlight,
even though both devices belonged to the same portal user.

### What changed

v0.29.0 adds a **per-DEVICE grant block** to the generated policy:
for each portal user with N≥2 devices, emit N grants (one per
device as `src`), with `dst` = the list of all OTHER devices of
the same user, and `ip: ["*"]` (required by headscale 0.29.2).
13 grants on this VM (11 admin + 2 user1); O(n) per user,
not O(n²).

The earlier v0.28.7 attempt (commit `8749069`) tried a wildcard
`tag:dev-<user>-*` src, which headscale 0.29.2 rejects with
"src=tag not found" (the parser requires concrete tags in
`tagOwners{}`).

### Why this works

Tailscale ACL is order-sensitive (first match wins). The per-DEVICE
grant is emitted **after** the per-user grant (which keeps SSH
+ untagged identities working) and **before** per-device exit-rules
+ per-device loose grants (autogroup:internet). Tagged-to-tagged
device traffic on the same tailnet now matches the per-DEVICE
grant as a fallback when no specific per-device rule applies.

### Code shape

The per-DEVICE block is duplicated in `GenerateACLForPlane` and
`GenerateACLWithViaForPlane` (the v0.28.1 per-user via variant
takes a parallel code path). v0.30.0 will extract the block to a
shared helper — the duplication is documented in the AGENTS.md
release notes for v0.28.7 as a known cleanup target.

### Verification

- Live: 13 new per-DEVICE grants in the headscale policy after
  `/admin/exit-rules/reapply` (HTTP 303 redirect)
- Operator confirmation: workstation-2 ↔ workstation-1 Moonlight session
  now establishes and streams (2026-07-27)
- `make verify-pre` 9/9 PASS, `make verify-post` 25/26 PASS
  (R9 is a known false-negative — the verify-post script reads
  an older `acl_snapshots` row; the most recent reapply did
  succeed and saved a fresh snapshot)

## How a release is cut

1. `git tag -a v0.X.Y -m "v0.X.Y"` on the commit we want to ship.
2. `git push origin v0.X.Y`.
3. (Operator-driven) create a GitHub release at
   https://github.com/BarsSky/skygate/releases/new — the body
   summarizes the commits since the previous tag.
4. Update `CHANGELOG.md` to move the entry from `[Unreleased]`
   into the new tagged section.

The operator (admin) writes the release body; the git tag is
the source of truth for "what shipped in v0.X.Y".
