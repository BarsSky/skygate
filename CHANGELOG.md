# Changelog

All notable changes to Skygate are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/) and the project follows
[Semantic Versioning](https://semver.org/) (best-effort; we don't ship API
stability promises yet — pin to a tag if you depend on a specific shape).

## [Unreleased] — v0.9.1-dev (work in progress on main)

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
  `username@tsnet.skynas.ru:*` as the first rule; the catch-all `*:*`
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
- **Smoke uses device 8 (not 3)** — emilia is now an exit-node, can't
  be a rule target
- **`GenerateACL()` uses tag-based rules** so Tailscale shows each user
  only their own devices in the client UI
- **Staggered sync keeps base exit-node routes** (0.0.0.0/0, ::/0) —
  regression after v0.6.0 cleanup
- **`SetPolicy` no longer hides 5xx**: typed `*APIError` separates
  404/405 (file-mode fallback is OK) from 5xx (real failure →
  `MarkACLFail` + bot "NOT applied" reply). Fixes a prod bug where
  headscale 5xx mid-restart was silently masked by the docker
  fallback path on hosts that have docker

### Fixed

- **Tailscale Android "all nodes visible"** — per-user ACL pushed
  with first-match-wins `username@tsnet.skynas.ru:*` instead of `*:*`
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

Hotfix release — preserves base exit-node routes (0.0.0.0/0, ::/0) in
`SetAdvertisedRoutes` and `SyncAdvertisedRoutes`. The v0.6.0 cleanup had
accidentally stripped them, breaking exit-node connectivity for all
clients.

## [v0.6.0] — 2026-07-08

First refactored release.

### Added

- Exit-node rules with per-device accept/deny ACL
- Automatic DNS-driven /32 resolution for domain rules (autoupdater)
- Multi-user, per-user rule limits (`SKYGATE_USER_MAX_RULES=skyadmin:2000`)
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
