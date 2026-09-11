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
- `<polygon-vm-hostname>` VM available, S3 bucket configured, Patroni + etcd in place
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
