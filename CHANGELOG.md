# Changelog

All notable changes to Skygate are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/) and the project follows
[Semantic Versioning](https://semver.org/) (best-effort; we don't ship API
stability promises yet — pin to a tag if you depend on a specific shape).

> **Note on historical entries:** the per-version detail (root cause +
> fix + files + live verify) lives in [RELEASE-NOTES.md](RELEASE-NOTES.md)
> for every shipped tag. This file is the at-a-glance list of what
> changed in each version; the v0.6 → v0.32 history is preserved in
> git history (`git log v0.6.0..v0.33.0 -- CHANGELOG.md`).

## [v0.33.1.17] — 2026-08-06

### Added

- **Exit-rule / preferred exit-node cross-check** — `device_rule`
  pointing at exit-node X only takes effect on device D if D's
  preferred exit-node is also X (per-device pref > per-user pref >
  unset). Catches the "rule saved but Tailscale ignores it" bug.
  - Helpers in `internal/feature/exit_rules/preferred_check.go`
    (`PreferredExitNodeForRule`, `IsRuleApplicable`, `TagToHostname`,
    `RulesByDeviceHostname`) with 6 unit tests.
  - `/my/exit-rules`: top-of-page warning banner with "Use device's
    preferred exit-node" button when `MismatchCount > 0`; per-rule
    "Preferred" column with green/red icon.
  - `/admin/exit-rules`: same banner + per-row "Preferred" column
    on the new `AnnotatedRules` slice.
  - `/admin/devices`: per-device "dead rules" count badge with
    tooltip explaining what "dead" means.
  - `/admin/system_tests` → `exit_rules.preferred_mismatch`:
    in-process system test (3 SQL queries + Go cross-check,
    backend-dispatching for SQLite/PG; threshold 0 = pass,
    1–5 = pass with warn, > 5 = fail).
  - 18 new i18n keys (RU + EN) for banner text, button label,
    column header, per-row title tooltips.
  - `B66` verify-pre check (13 grep-pins).

## [v0.33.1.16] — 2026-08-06

### Fixed

- **`SKYGATE_TS_LOGIN_SERVER` not picked up by the entrypoint** —
  the hardcoded `https://head.example.com` in
  `docker-compose.yml:environment:` was overriding the `.env`
  value (docker-compose precedence: `environment:` > `env_file:`).
  Removed the hardcoded value; added a "Restart skygate" card on
  `/admin/tailscale` that writes the current effective value to
  `.env` atomically (`.tmp` + rename) and triggers
  `docker compose restart skygate`. Setsid'd subprocess survives
  the SIGTERM that hits the parent. 5 new i18n keys, 5 new
  tests. `B65` verify-pre check.

## [v0.33.1.15] — 2026-08-05

### Fixed

- **Per-device exit_node_pref device tag missing from
  `tagOwners`** — headscale rejected the per-device grant
  because the device's tag (e.g. `tag:dev-<user>-<device>`)
  was not in `tagOwners`. ACL builder now augments `tagOwners`
  with per-device-pref device tags. `B64` verify-pre check.

## [v0.33.1.14] — 2026-08-04

### Fixed

- **`placeholdersList(1)+placeholdersList(1)` 2-arg PG-unsafe
  query** — produced `WHERE user_id = $1 AND device_hostname = $1`
  on PG (two refs to the same param). Replaced with `PlaceholderAt(2, i)`.
  `B63` verify-pre check (comprehensive sweep).

## [v0.33.1.13] — 2026-08-03

### Fixed

- **`SKYGATE_TS_LOGIN_SERVER` DB-persisted but ignored by
  `tailscale up`** — the entrypoint read the env var at start, not
  the DB value. Wired the entrypoint to fall back to the DB
  value when the env var is empty. `B62` verify-pre check.

## [v0.33.1.12] — 2026-08-02

### Fixed

- **Multiple `?` placeholder PG-unsafe queries** — found by
  `?` → `$N` sweep script. `B60` verify-pre check (full sweep
  across the codebase).

## [v0.33.1.11] — 2026-08-01

### Added

- **`/admin/system_tests` works on both backends** — tests
  dispatch on `db.BackendOf` so the same registry runs on
  SQLite (default) and PostgreSQL (v0.31+ opt-in). 7 new tests
  covering exit-nodes, integrations, DNS resolution, duplicate
  devices, rule sanity, recent backups, and active meshes.
  `B59` verify-pre check.

## [v0.33.1.10] — 2026-07-30

### Fixed

- **Tailscale auto-generate preauth key** — `/admin/tailscale`
  now has a one-click "Generate automatically" button. `B58`
  verify-pre check.

## [v0.33.1.9] — 2026-07-29

### Added

- **Tailscale web-UI management** — `SKYGATE_TS_LOGIN_SERVER` is
  editable from `/admin/tailscale` (DB-persisted). `B56`
  verify-pre check (env-source fix).

## [v0.33.0] — 2026-07-28

### Added — Network Access Manager + Admin Test Page

- **`devicemeta` package** (new `internal/devicemeta/`): per-device
  `os` + `device_type` columns on `node_owner_map`. Auto-detect
  heuristic (`DetectOS`/`DetectType`). Auto-detect runs on every
  `/my/devices` load. Manual override form on `/admin/devices`.
  5 unit tests, RU + EN i18n keys.
- **`via:` sync bug fix** in `Service.generateACL`: the
  `/my/exit-rules` + `/admin/exit-rules` + REST API paths
  hardcoded the no-via generator; per-device-pref path
  already used `acl.ApplyACLPipelineForPlane`. Unified dispatch
  honours `SKYGATE_ACL_VIA_ENABLED`.
- **refactor-v0.30 Phase C + D** (internal, no API change):
  catalog.go split into 12 per-feature `catalog_*.go` files
  + glue; `SanitizeFilename` → `internal/httputil/`;
  `backfillNodeOwnership` → `internal/nodeownership/`;
  per-user control plane router → `internal/controlplane/`.
- **Network Access Manager** (`internal/feature/admin/headscale_acl.go`):
  `/admin/headscale/acl` for adding/removing skygate-managed
  headscale ACL rules. Read-modify-write of the live policy
  preserves every other field. Idempotent on rule fingerprint.
- **Admin Test Page** (`internal/feature/admin/system_tests.go`):
  `/admin/system_tests` runs 6 in-process tests (network, db,
  headscale, disk, wal-g, replication) and stores results in
  `system_tests_runs`.
- Catalog extended: B38–B42 (build-time) + R31, R32 (runtime).

## [v0.33.1.x history] — Recent fixes and features

The full v0.33 series shipped in 2026-Q3 and added: per-feature
refactor of the handlers package, the in-app Update orchestrator
with auto-rollback, the headscale release monitor
(`/admin/headscale`), the per-user exit-rule cleanup helper, the
postgreSQL foundation (27 PG migrations, 4 verification tests),
the `?` → `$N` placeholder sweep, the Telegram egress relay
selector (`/admin/telegram` per-chat pick of which enabled
exit-node runs the canonical Telegram CIDR list), the
`SKYGATE_TS_LOGIN_SERVER` config-source fix, and the
exit-rule / preferred cross-check.

## [v0.32.x → v0.16.x] — Major feature history

- **v0.32.x** — PostgreSQL foundation + driver abstraction;
  refactor-v0.30 (handlers split into `internal/feature/*`);
  Tailscale in-image integration; exit-rules CDN-range
  substitution (Cloudflare/Fastly/Google/Akamai); mesh
  visibility; v0.32.14+ CASCADE-LOCK fix (SQLite WAL +
  MaxOpenConns=15 + synchronous=NORMAL); v0.32.16+ distroless
  healthcheck pattern for headplane.
- **v0.31.0** — PostgreSQL foundation: 27 PG migrations + 4
  verification tests; `pg-ha` deploy with wal-g; build tag
  `-tags postgres`. See
  [release notes](https://github.com/BarsSky/skygate/releases/tag/v0.31.0)
  for the full breakdown.
- **v0.30.1** — per-user device can't be tagged as exit-node
  (the "workstation-8" fix). 8 unit tests; R26 added to
  verify-post.
- **v0.30.0** — Self-update orchestrator (v0.29.0) +
  Auto-swap via helper container in host PID namespace
  (v0.29.3.1) + `skygate` host-side wrapper (v0.29.2) +
  per-user device can't be exit-node (v0.30.1).
- **v0.29.x** — Self-update orchestrator with auto-rollback
  (`/admin/update`); `skygate` host-side wrapper (`skygate-cli.sh`)
  using `com.docker.compose.service=skygate` label; helper
  container in host PID namespace for safe swap; Tailscale
  in-image integration (off by default since v0.32.15).
- **v0.28.x** — explicit opt-in for `via` constraint
  (Android-friendly) + tagged-device exit-node fix + idempotent
  migration + entrypoint always clears stale Tailscale exit-node.
  4 patches (v0.28.5 / v0.28.5a / v0.28.5b / v0.28.5c). The
  v0.28.5 guarantee catalog was the reason B1–B18 + R1–R27
  exist.
- **v0.27.x → v0.16.x** — per-user subnets, per-user headscale
  control plane (compliance tier), mesh, per-device preferred
  exit-node, subnet router, personal API tokens, self-service
  password change, rate limits, cleanup of orphaned /32, Bilingual
  EN/RU web UI (1 000+ keys), Telegram bot (real ops:
  preauth / rules / devices / restart / version), exit-node
  rules with per-device accept/deny ACL, automatic DNS-driven
  /32 resolution for domain rules, multi-user, per-user rule
  limits, headscale integration, headplane integration.

## [v0.6.0 and earlier] — Pre-refactor baseline

- v0.6.1-amnezia-fix — preserves workstation-8 exit-node
  routes in `SetAdvertisedRoutes` and `SyncAdvertisedRoutes`
- v0.6.0 — first refactored release: exit-node rules with
  per-device accept/deny ACL, automatic DNS-driven /32
  resolution, multi-user, per-user rule limits, per-device
  limits, cleanup of orphaned /32, sync to exit-node
  advertised-routes, tag-aware device ownership, hierarchical
  view, backup integrity verification, in-process Telegram
  mock harness, `docs/SYNC.md` for agent workflow, `Makefile`
- v0.5.0 and earlier — pre-refactor baseline (see
  `git log v0.5.0`)

## [v0.33.1.0 — v0.6.0] — Compact history (one line per minor)

(For users who want a quick scan of what shipped in each
minor without drilling into every patch. Detailed entries
are in [RELEASE-NOTES.md](RELEASE-NOTES.md) and git history.)

- **v0.33.0** — Network Access Manager (`/admin/headscale/acl`)
  + Admin Test Page (`/admin/system_tests`)
- **v0.32.0** — refactor-v0.30 (handlers → `internal/feature/*`),
  PostgreSQL opt-in, CASCADE-LOCK fix, distroless healthcheck
- **v0.31.0** — PostgreSQL foundation (27 migrations + 4
  verification tests, build-tag `-tags postgres`)
- **v0.30.x** — per-user device can't be exit-node
  (workstation-8 fix), self-update orchestrator
- **v0.29.x** — in-app update, host-side wrapper, helper
  container swap
- **v0.28.5** — `via` opt-in (Android-friendly) + tagged-device
  exit-node fix + idempotent migration + entrypoint clears
  stale Tailscale exit-node. The v0.28.5 guarantee catalog
  (B1–B18 / R1–R27) was created in this release.
- **v0.28.x** — per-user preferred exit-node (v0.28.1) +
  per-device preferred (v0.28.4) + via flag (v0.28.5)
- **v0.27.x** — PostgreSQL HA (multi-host) foundation
- **v0.26.0** — HA-ready: `/healthz` + `/readyz`, subnet-router
  tooling, e2e pilot script
- **v0.25.0** — mesh visibility on `/my/devices` + operator
  overview
- **v0.24.x** — download bundle for per-user subnet-router,
  per-device preferred exit-node (v0.28.4 superset)
- **v0.23.x** — per-user headscale control plane (compliance
  tier) + safe user migration
- **v0.22.x** — mesh (N-way bridge) + safe user migration design
- **v0.21.x** — user-to-user subnet bridge (invite codes +
  bot `/invite` + `/accept` + `/admin/invites`)
- **v0.20.x** — headscale-update-monitor +
  auto-allocate subnet on user create
- **v0.19.x** — `exitnode.skygate-subnet-<user>` DNS records
  (BLOCKED on headscale 0.29 — `dns.extra_records` unsupported)
- **v0.18.x** — MagicDNS for personal subnets
- **v0.17.x** — share subnet cross-user (admin-mediated)
- **v0.16.x** — per-user subnets foundation (v0.16.6+) +
  subnet router (v0.16.7+)
- **v0.10.x → v0.15.x** — full release history in
  [RELEASE-NOTES.md](RELEASE-NOTES.md) and git tags


### Added

- **Telegram bot — Phase 11–14 (real operations, not stubs)**
  - `/add_device` issues a real 1-hour single-use preauth key through
    `headscale.CreatePreauthKey`
  - `/add_rule` adds an exit-rule using the user's default device + default
    exit-node, with full ACL sync and audit
  - `/delrule` deletes a single rule (id-aware) with cascading /32 cleanup
  - `/clearrules` is a two-phase "nuclear" wipe: 1st confirm lists scope,
    2nd confirm runs the wipe
  - `/myexitnodes` shows the user's reachable exit-nodes (tag:exit-node,
    user-scope filter)
  - `/version` reports build label + commit + Go runtime + DB schema version
  - `/restart` with 6-char token confirmation + 30s TTL; sends SIGTERM via
    `os.FindProcess` (cross-platform compile)
  - `/help <command>` per-command detailed help
- **Telegram — chat_id → portal_user bindings** (migration v0.29): regular
  users can now bind their own Telegram chat to their portal account;
  command dispatch is user-aware, not admin-only
- **Telegram — per-user default device + default exit_node** (migration v0.30):
  bot can take shortcuts ("/add_rule" → use the defaults the user picked)
- **Telegram — alert ring buffer** (migration v0.27): every alert is
  recorded in `telegram_alerts` with stable id; `/ack <id>` flips
  `acked_at` in place (idempotent) and writes `audit_log` mirror row
- **Per-user headscale ACL** (commit `fadf872`): each portal user gets
  `username@tsnet.example.com:*` as the first rule; the catch-all `*:*`
  that used to be first is gone — fixes the "Tailscale Android shows all
  nodes" bug
- **node_owner_map backfill** (Strategy C temporal preauth→tag:private
  match): new portal users' nodes are auto-attributed to the right
  portal user, even when the preauth key wasn't pre-registered
- **Self-service password change** at `/my/account`
- **Personal API tokens** (Bearer auth) at `/my/tokens`
- **Rate limits** (in-memory token bucket, single-instance only):
  - `POST /login`: 5 attempts per username per 15s, 20 per IP per 30s
  - `/api` endpoints: 30 requests per IP per 60s
  - 429 + `Retry-After` header on block; sweep every 5 min
- **Bilingual i18n web UI**: 270+ catalog keys EN+RU, lang toggle in sidebar,
  per-request locale via `atomic.Value` + funcmap `Tr/Trf`
- **Cleanup orphan /32 rules** admin endpoint at
  `/admin/exit-rules/cleanup/apply` (idempotent merge of duplicate
  device_ids)
- **Audit log filters**: `/admin/audit?action=…&user=…` (date still TODO)
- **`docs/scripts/skygate_exit_node_setup.sh` + `_rollback.sh`** — first-time
  client setup helpers, kept in `docs/` so they aren't embedded in the
  binary
- **Unit tests** for `internal/acl`, `internal/headscale` (incl.
  `parseDuration`, `durationFlag`, `hasExitNodeTag`, `IsPublic*`),
  `internal/telegram` (`HandleCommand`), `internal/i18n` (catalog parity,
  placeholder order)
- **Static route audit** `scripts/audit_routes.py`: cross-checks every
  `mux.HandleFunc(...)` in `cmd/skygate/main.go` against the actual
  `func (a *App) Foo(...)` declarations in `internal/handlers/*.go` —
  wires into `make test`
- **CI** `.github/workflows/ci.yml` — `go vet` + `go test -race` + `go build`
  + `audit_routes.py` on `ubuntu-24.04`; pins `actions/checkout@v5` +
  `actions/setup-go@v6`
- **Build label via `-ldflags`**: `git describe --tags --always` flows
  through to web footer + Telegram `/version`

### Changed

- **DB refactor (Этап 9–10)**: 57 raw SQL strings in handlers → 30+
  typed helpers in `internal/db/*.go` + `queries.go` central registry
  (portal_users, preauth_keys, personal_api_tokens, node_owner_map,
  exit_servers, audit_log, device_rules, acl_snapshots, exit_rule_logs)
- **`headscale.go` split** (757 lines → 9 focused files in
  `internal/headscale/`): `headscale.go`, `users.go`, `preauth.go`,
  `nodes.go`, `tags.go`, `acl.go`, `routes.go`, `route_args.go`, plus
  `*_test.go` files
- **`handlers.go` decomposed** (1750 lines → 257 lines, pure shared
  infrastructure only: App struct + render helpers + audit + getMaxRules)
- **Route-setup script split** (300 lines of inline bash → 42-line
  orchestrator + pure `.cmd` builder + pure `.sh` builder; the
  `_windows_body.go` / `_linux_body.go` filename suffix avoids
  GOOS build constraints on cross-compile)
- **Smoke test — bilingual fan-out** (`scripts/smoke.sh`): when
  `SMOKE_LANG` is unset, the script re-invokes itself once per language
  (ru, then en) and prints two `SUMMARY` lines; 59+59 = 118 assertions
- **Smoke uses device 8 (not 3)** — relay-1 is now an exit-node, can't
  be a rule target
- **`GenerateACL()` uses tag-based rules** so Tailscale shows each user
  only their own devices in the client UI
- **Staggered sync keeps workstation-8 exit-node routes** (0.0.0.0/0, ::/0) —
  regression after v0.6.0 cleanup
- **`SetPolicy` no longer hides 5xx**: typed `*APIError` separates
  404/405 (file-mode fallback is OK) from 5xx (real failure →
  `MarkACLFail` + bot "NOT applied" reply). Fixes a prod bug where
  headscale 5xx mid-restart was silently masked by the docker
  fallback path on hosts that have docker

### Fixed

- **Tailscale Android "all nodes visible"** — per-user ACL pushed
  with first-match-wins `username@tsnet.example.com:*` instead of `*:*`
- **Telegram bot on hosts without docker** — `TagNode` falls back to
  `docker exec headscale headscale nodes tag`; admin API lacks the
  permission for `/api/v1/node/{id}/tag`
- **DNS auto-updater for domain rules** — keeps the parent_domain
  pointer so the next refresh can re-derive the /32 list
- **Smoke test step 8** — API now returns `ids: [N]` after POST so
  smoke can clean up its own test rules (was: 198.51.100.x orphans
  accumulating)
- **Multi-delete accepts `?id=N&ids=N1&ids=N2`** (union of single + many)
  — `r.ParseForm()` is called before reading `ids` (Go net/http gotcha)
- **COALESCE preauth_keys nullable columns** — legacy DBs where
  `headscale_preauth_id` is NULL don't crash on SELECT
- **Race-free `killProcess` + test cleanup** in `commands_phase4.go`
- **Build label stuck at "v0.3"** — was hardcoded; now injected via
  `-ldflags -X main.version=...`
- **Go 1.24+ auto VCS stamping** — `-buildvcs=false` in entrypoint
  so the binary still builds when git history is missing
- **Cross-user device ownership** — `PostMyExitRule` rejects attempts
  to add rules against devices owned by other users, blocks exit-node
  rule sources
- **Empty staggeredSync message** — replaced with "ok" + dashboard
  links to `/admin/exit-rules` and `/admin/exit-nodes`

### Migration notes (v0.6.0 → v0.9.0-dev)

- Migrations v0.20 → v0.30 are all idempotent and run on first start
  of v0.7.0+ binaries
- The `v0.28` migration backfills `device_rules.parent_domain`,
  `node_owner_map.tag/tagged_by_user_id/tagged_at`, and
  `preauth_keys.headscale_preauth_id` — needed by the node ownership
  backfill code
- Personal API tokens (v0.6.0+) are new; existing deployments have
  zero rows in `personal_api_tokens` until users create one at
  `/my/tokens`

## [v0.8.0] — 2026-07-11

### Added

- Per-user headscale ACL with granular visibility
- Auto-tag new nodes as `tag:private`; sync UI state to headscale
- API returns `ids[]` after POST; smoke deletes properly; cleanup orphans
- `/admin/audit` action + user filter
- Telegram hot-swap fix, `/nodes` + `/rules` + `/audit`, exit-rule triggers
- Refactored `headscale.go` (757 → 9 files)
- Refactored `handlers.go` (1750 → 257 lines) and `exit_rules.go` (1915 → 1225)
- Tailscale Android bug fix (per-user ACL pushed)

## [v0.7.0] — 2026-07-10

### Added

- `/my/exit-rules` page with multi-delete, cascade, filter, search
- `/my/exit-rules/help` full help page with API reference
- Per-user and per-device usage counters in UI
- `/admin/exit-rules` cross-user hierarchical view
- `/admin/exit-rules/cleanup` admin UI
- `/admin/exit-rules/rollback` to restore a previous ACL snapshot
- `/admin/telegram` bot config UI (token in `global_settings`)
- Per-exit-node Tailscale `AcceptRoutes` policy (avoids Amnezia-AWG
  conflict on co-hosted VPN nodes)
- `AcceptRoutes` + route aggregation logic extracted from synology
- Self-service password change at `/my/account` (commit `c30044b`)
- In-memory rate limit for `/login` and `/api` endpoints
- i18n English/Russian infrastructure + lang toggle in sidebar
- `AGENTS.md` with AI hints for Skygate development
- `scripts/smoke.sh` (HTTP smoke) + `scripts/check_exit_nodes.py` +
  `Makefile`
- Inline CSS extracted to `static/css/themes.css`
- 21 obsolete `.bak` files removed (9354 lines)

## [v0.6.1-amnezia-fix] — 2026-07-09

Hotfix release — preserves workstation-8 exit-node routes (0.0.0.0/0, ::/0) in
`SetAdvertisedRoutes` and `SyncAdvertisedRoutes`. The v0.6.0 cleanup had
accidentally stripped them, breaking exit-node connectivity for all
clients.

## [v0.6.0] — 2026-07-08

First refactored release.

### Added

- Exit-node rules with per-device accept/deny ACL
- Automatic DNS-driven /32 resolution for domain rules (autoupdater)
- Multi-user, per-user rule limits (`SKYGATE_USER_MAX_RULES=admin:2000`)
- Per-device limits (`SKYGATE_MAX_RULES_PER_DEVICE=500`)
- Cleanup of orphaned /32 (admin endpoint)
- Sync to exit-node advertised-routes (staggered per node)
- Tag-aware device ownership (`tag:private` per portal user,
  `tag:public` shared exit-nodes)
- Hierarchical view (User → Device → Exit-Node → Rules)
- Backup integrity verification on restore
- In-process Telegram mock harness for tests
- `Setup-SkygateOnKnaga.ps1` for knaga clone provisioning
- `docs/SYNC.md` for agent-knaga workflow

### Refactored

- 21 `.bak` files removed
- `exit_rules.go` (1749 → 1225 lines)
- `handlers.go` (1750 → 1592 lines)
- Inline CSS → `static/css/themes.css`
- `Makefile` introduced (`build / run / smoke / check-nodes / test / deploy`)

## [v0.5.0] and earlier

Pre-refactor baseline. See git history (`git log v0.5.0`).
