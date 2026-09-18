# Skygate internals

This is the code map / internals handbook for contributors and AI agents: what lives
where, which package owns which responsibility, the invariants a change must respect,
and how the automated **B-check** contract system works.

It **replaces the pre-v1.6 internal architecture documents** removed in the 2026-09-18
documentation restructure (`docs/internal/architecture/README.md`, `modules.md`,
`cluster-management.md`, `ha-architecture.md`, `wal-g-notes.md`). Their full text stays
in git history. Everything below was re-verified against the source tree on 2026-09-18;
where the old `modules.md` disagreed with the code, **the code wins** and the drift is
called out (see [§2](#plugin-api--module-interface-internalmodule) and [§9](#9-known-structural-debt)).

Operator-facing material is not duplicated here — see `docs/README.md`, `docs/deploy.md`,
`docs/operations.md`, `docs/architecture.md`, `docs/UPDATE.md`, `docs/db-schema.md`,
`docs/backup-restore-and-migration.md`, `docs/troubleshooting.md`.

## Table of contents

1. [Repository layout](#1-repository-layout)
2. [Package map](#2-package-map) · [entrypoint wiring](#entrypoint-wiring-order) · [Plugin API / Module interface](#plugin-api--module-interface-internalmodule)
3. [Data layer](#3-data-layer)
4. [HTTP layer](#4-http-layer)
5. [Self-update internals](#5-self-update-internals)
6. [Conventions and invariants](#6-conventions-and-invariants)
7. [The B-check contract system](#7-the-b-check-contract-system)
8. [Where to look when…](#8-where-to-look-when)
9. [Known structural debt](#9-known-structural-debt)

---

## 1. Repository layout

| Path | What it is |
|---|---|
| `cmd/skygate/` | **The product binary.** `main.go` (~3 970 lines) is the boot sequence *and* the whole route table; siblings are subcommand families (`cluster.go`, `init.go`, `join.go`, `migrate.go`, `db_migrate.go`, `acl_apply.go`, `derp_probe.go`, `regapi_credentials.go`). |
| `cmd/apply_pg_migrations/`, `cmd/jwt-mint/` | Maintenance binaries: apply the PG chain without starting the server; mint a session JWT for local API calls. |
| `internal/` | All library code: ~44 top-level packages, ~57 package dirs with nesting (`internal/feature/*`, `internal/db/pgmigrate`, `internal/module/tailscale`). |
| `internal/feature/` | "One package per user-facing surface": `admin` (~1.2 MB!), `my`, `auth`, `exit_rules`, `cluster`, `healthz`, `subnet` (empty stub). Handlers live here, not in `internal/handlers`. |
| `internal/handlers/` | The `App` struct (shared deps), template renderer, static assets, embedded bundles, the last legacy handlers. |
| `internal/db/` | Everything DB: open paths, both migration chains, dialect detection, placeholders, per-table query modules, the `db-migrate` converter. |
| `internal/headscale/`, `internal/update/`, `internal/module/` | headscale v0.29 API client + CLI fallback; self-update for every install kind; the Plugin API + Module #1. |
| `internal/i18n/`, `internal/acl/`, `internal/devicedelete/`, `internal/nodeownership/` | RU+EN catalogs (14 files); the single ACL pipeline; the shared device-delete coordinator; owner-map backfill + tag autoupdater. |
| `internal/cluster/`, `internal/ha/`, `internal/elector/`, `internal/watchdog/` | Multi-node cluster, active/passive HA chain, leader election, `cluster_database` hot-swap watchdog. |
| `internal/telegram/`, `internal/backup/`, `internal/certsync/`, `internal/derphealth/`, `internal/metrics/`, `internal/monitoring/`, `internal/keynotify/`, `internal/tokenrotate/`, `internal/mesh/` | Bot + the background services (each owns a scheduler entry point called from `main.go`). |
| `deploy/` | Installers (`install*.sh`, `Setup-Skygate-Win.ps1`), `deploy.sh`, the privileged applier `skygate-apply-update.sh`, `lib/`, templates, systemd units, the local `docker/derper/` image, `scripts/`, `snippets/`, `subnet-router/`. |
| `scripts/` | The B-check catalogue (`check_b*.sh` — **205 files**, measured), the gate (`verify_pre_deploy.sh`, 4 266 lines), `verify_post_deploy.sh`, smoke/e2e helpers, `lib/skip_if_no_docker.sh`, `ha-state/`, 5 `//go:build ignore` verifiers under `scripts/*.go`. |
| `docs/` | Operator + contributor docs; `docs/README.md` is the index. The `internal/`, `plans/`, `runbooks/`, `superpowers/` subtrees are pre-restructure areas. |
| `.github/workflows/ci.yml` / `release.yml` | `vet` + `go test -race` + `build`, then the same gate the pre-push hook runs; and the **linux/amd64-only** ghcr image + Go tarball (linux/darwin amd64+arm64, windows-amd64) release. |
| `Makefile` | `build`, `run`, `test`, `verify-pre`, `verify-post`, `verify`, `rebuild-deploy`, `check-bundles`, `sync-bundles`, `clean-tmp`. |
| `Dockerfile`, `Dockerfile.prebuilt`, `Dockerfile.caddy` | Runtime image (`golang:1.25-alpine` + tailscale binaries; **the Go build happens at container start**); prebuilt-binary variant; optional Caddy (off by default). |
| `docker-compose.yml`, `.ghcr.yml`, `.sqlite.yml`, `.lite.yml` | Canonical in-container-build stack; image-pull variant (what makes the fast update path possible); SQLite single-host; reduced service set. |
| `entrypoint.sh`, `.env.example`, `AGENTS.md` | Container start (build + build label + optional tailscaled); the schema for every `SKYGATE_*` knob incl. the `SKYGATE_DB` DSN forms; agent working rules (conventions, not architecture). |

---

## 2. Package map

### Foundations

`config` (env → `Config`; DSN precedence via `resolveDBDSN`; `deriveControlURL`) ·
`db` (the `*sql.DB`, both migration chains, `DetectDSN`/`OpenWithDialect`,
`MigratePostgres`/`MigrateSQLite`, `BackendOf`, `ResettableDB`, `execSQLiteDDL` —
imported by ~129 files, the most-imported package) ·
`auth` (`Claims`, `HashPassword`/`CheckPassword`, `IssueJWT`/`ParseJWT`,
`CheckAPIToken`) · `i18n` (`Catalog`, `T`/`Tf`, `LangFromRequest`, `SetLang`) ·
`middleware` (**the only** middleware: `RequireAuth`, `RequireLoginLimit`,
`RequireAPILimit` — imported by `main.go` only) · `ratelimit` (token buckets,
`AllowLogin`/`AllowAPI`/`ClientIP`/`Sweep`) · `httputil` (`SanitizeFilename`) ·
`staticfs` (`embed.FS` for `static/`).

### Control plane, headscale and the shared workflows

| Package | Responsibility — key API — who calls it |
|---|---|
| `internal/headscale` | The REST client + CLI/file fallbacks, one file per concern (`users`, `nodes`, `preauth`, `tags`, `acl`, `routes`, `route_args`, `reconcile`). `Client`, `ListAllNodes`, `DeleteNode`, `ExtendNodeExpiry`, `TagNode`, `SetPolicy`, `SetAdvertisedRoutes`, `InvalidateCache` — 45 files. |
| `internal/controlplane`, `internal/acl` | Per-user control-plane routing ("which headscale URL is this user's?") + client cache (`Router`, `handlers` only, surfaced as `App.HSForUser`/`HSGlobal`); and the **only** place that turns `device_rules`/prefs into a headscale policy — the pipeline is deliberately narrow (generate → snapshot → `SetPolicy` → mark/log) with caller-specific side effects left at the call site (`GenerateACL`, `GenerateACLWithVia`, `ApplyACLPipelineForPlane`). |
| `internal/nodeownership`, `internal/devicedelete`, `internal/devicemeta` | Owner-map backfill + tag autoupdater — strategies **A** `PreAuthKeyID`, **C** temporal window, **D** existing `tag:dev-<user>-*`, **E** OIDC (`PreAuthKeyID=="" && UserName==portal username`); the shared "delete this device everywhere" coordinator (node + owner map + prefs + rules + ACL regen + cache + audit) used by **both** delete buttons; device OS/type classification. |
| `internal/sidecar`, `internal/monitoring`, `internal/expirewatch`, `internal/headscale_version`, `internal/release` | Subnet-router auto-approver; exit-node health monitor; node-expiry watcher (works around clients that register a 2–4 s expiry); release monitors for headscale-vs-pin (banners, bot answers) and skygate-vs-latest (dashboard banner). |

### Feature layer (`internal/feature/*`)

| Package | Responsibility — key API |
|---|---|
| `feature/admin` | **Biggest package in the tree (~1.2 MB, 115 files).** Every `/admin/*` GET+POST: users, devices, exit-nodes, DERP (status/relays/config/cert-sync/dashboard), database + Patroni failover, HA, cluster, deploy runs, backup, certificates, OIDC settings+sync, headplane, modules, tailscale, telegram, acls, system tests, update. `Service`, `GetAdmin*`/`PostAdmin*` pairs, `RedirectWithFlash`, `InitDerpClassifier`, `EnsureBundledDerpRelay`, `StartCertSyncCron`. |
| `feature/my`, `feature/auth` | Every `/my/*` page — dashboard, devices, keys, notifications, meshes, exit-nodes, telegram binding, account, audit export (`GetMyDevices`, `PostMyDeviceDelete`, `PostMyKeyReissue`, `parseLastSeenAndClassify`) — plus `/login`, `/logout`, `/lang`, `/help`, `/my/account`, `/my/tokens` (`GetLogin`, `PostLogin`, `safeNextRedirect`). |
| `feature/exit_rules` | `/my/exit-rules` + `/admin/exit-rules`: rule CRUD, REST API, advertised-route sync, DNS/CDN autoupdater, route-script generation, preferred-exit reconciler, CDN grouping. `Store`, `DomainAutoUpdater`, `annotateRulesWithPrefs`, `GroupRulesByCDN`. |
| `feature/healthz` | `/healthz`, `/readyz`, `/db/health` bodies + the DB-health sampler with degraded/recovered transitions. `Service`, `Sampler`, `DBHealthConfig`. |
| `feature/cluster`, `feature/subnet` | Thin HTTP surface over `internal/cluster`; and an **empty placeholder** (only `doc.go` — the real logic is `internal/subnet`). |

### Background services and integrations

Each owns a `Run`/`Start` entry point launched from `main.go` behind its own knob.

| Package | Responsibility — key API |
|---|---|
| `update` | Self-update for every install kind ([§5](#5-self-update-internals)). `StateStore`, `DockerUpgrader`, `ImagePullStrategy`, `NativeUpgrader`, `ConfirmNativeSwap`, `DetectInstallKind`, `DetectPlatform`. |
| `cluster` | Cluster model + operations: nodes, invites, join, drain, upgrade orchestration, Tailscale discovery. `Node`, `ApproveNode`, `DrainNode`, `RejoinNode`, `UpgradeAll`, `DiscoverNewNodes`. |
| `ha` + `ha/dnsexternal` | Active/passive HA chain: heartbeat, role transitions, reclaim, external-DNS bridging + provider credentials. `Elector`, `NewElector`, `Run`. |
| `elector`, `watchdog` | Leader election over `cluster_node` (distinct from the HA chain role); 5 s `cluster_database` poll that hot-swaps the `*sql.DB` through `ResettableDB.Reset()` and alerts on consecutive read failures. `elector.Elector`, `watchdog.DBSwap`, `DefaultConfig`. |
| `oidc` | OIDC provider for headscale: RSA keypair, discovery, JWKS, `/oidc/authorize|token|userinfo`, config auto-sync wrapper. `Service`, `NewService`, `UserLookup`, `readSession`, `RunSync`, `ShouldAutoSync`. |
| `module` + `module/tailscale` | Plugin API + Module #1 (see below). `Module`, `Manager`, `State`. |
| `telegram`, `backup` | Bot dispatcher (`notify.go`), one file per command family, alert senders — **and the `Notifier` interface the rest of the app talks to** (`Notifier`, `NoopNotifier`, `BotEnv`, `SendAlert`); backup config + scheduler + in-app verify + S3/SMB/NFS/SFTP runners (`Scheduler`, `RunBackup`). |
| `certsync`, `derphealth`, `metrics` | HA cert sync; 5-minute DERP probe cron (`StartCron`); in-house Prometheus text exporter (deliberately no `client_golang` dependency) + 30 s collector (`Registry`, `Counter`, `Gauge`, `GaugeVec`, `Handler`). |
| `dbmigrate` + `steps` | Orchestrated DB-to-DB migration (PG↔SQLite): runs, steps, streaming HTTP progress. `Service`, `Run`, `Step`. |
| `deploy`, `deployrun` + `steps`, `dns`, `dnsexternal` | `skygate deploy <verb>` shared by CLI and web; persisted deployment-run records with their own step machine; the DNS provider abstraction (`external`/`cloudflare`/`route53`/`rfc2136`) + per-provider clients/creds. Note the **two** `dnsexternal` packages (debt §9). |
| `mesh`, `invite`, `subnet` | Smoke-mesh create/join/leave + cleanup scheduler (`RunCleanup`); invite-token bridge shared by web UI and bot (`Bridge`); real subnet allocation / MagicDNS / share logic (`Allocator`, `Manager`). |
| `keynotify`, `tokenrotate`, `notifications` | Key-expiry notifier; personal-API-token rotation; thin façade over `db` for the in-web notification inbox. |

### Entrypoint wiring order

`main()` first dispatches subcommands (`backup-run`, `backup-show-config`,
`backup-verify-ok|-fail`, `cleanup-smoke-meshes`, `deploy-push|pull|sync|status`,
`ha-promote|demote|reclaim`, `acl-apply`, `derp-probe`, `regapi-credentials`,
`migrate-only`, `db-migrate`, `cluster <verb>`, `init`, `join`, `migrate <verb>`,
`version`, `help`). **No subcommand starts the web server.** With no argument:

| # | Step |
|---|---|
| 1 | `config.Load()`; `adminsvc.InitDerpClassifier(cfg.DerpPeerNPM, cfg.DerpLANNet)`. |
| 2 | `db.DetectDSN(cfg.DBDSN).Kind` (logged — a hardcoded "postgres" label used to lie on SQLite); `db.OpenDSNWithRetry(cfg.DBDSN, 5, 2s)`; wrap in `db.NewResettableDB(pool)` → `d`. **Migrations run inside the open** (`openPostgres` → `MigratePostgres`, or `openSQLite` → `MigrateSQLite`). |
| 3 | Cheap boot crons + identity: `derphealth.StartCron`, `adminsvc.StartCertSyncCron`, `EnsureBundledDerpRelay`, `bootstrapAdmin`, `headscale.New(...)` → `hs`, `ensureHeadscaleUser`, headscale-user reconcile cron, `backfillNodeOwners`, optional `runFirstRunAutoSync`. |
| 4 | `handlers.New(d, hs, cfg.HeadscaleKey, cfg.JWTSecret, cfg.ControlURL, cfg.SSHKeyPath, cfg.SessionHours, cfg)` → `app`; `update.NewStateStore(cfg.UpdateStatePath).Load()`; `update.ConfirmNativeSwap(updateStore, cfg.UpdateDir)`. |
| 5 | `ratelimit.New()` + sweeper goroutine; `middleware.RequireLoginLimit` / `RequireAPILimit`; `app.Version` / `app.BuildVersion` (dedupes the `-g<sha>` suffix). |
| 6 | `mux := http.NewServeMux()`; public routes: `/login`, `/logout`, `/lang`, `/healthz`, `/readyz`, `/db/health`, `/metrics`, `/.well-known/`, `/oidc/`. |
| 7 | Feature services **in order**: `authSvc` → `oidcSvc` (startup aborts if `NewService` fails) → `healthzSvc` → `adminSvc` → `clusterAPI` → `deployrunSvc` → `moduleMgr` → `exitRulesSvc` → `mySvc` → `migrateSvc`. |
| 8 | `authMW := middleware.RequireAuth(cfg.JWTSecret)`, then ~250 `mux.Handle("METHOD /path", authMW(...))` registrations (lines ~1010–2290). |
| 9 | Background goroutines, each behind its own knob: `sidecarMgr.Run` (`SidecarSyncPeriod>0`), `watchdog.NewDBSwap`, `elector.NewElector(...).Run`, `expireWatchMgr.Run` (`ExpireWatchEnabled`), DNS autoupdater (`DNSAutoUpdateEnabled`), preferred-exit reconciler (`PrefReconcileInterval>0`), `nodeownership.AutoBackfill` (`NodeDiscoveryInterval>0`), release/exit-node/headscale-version monitors, update + backup-verify + cert-sync + mesh-cleanup + token-rotate + key-notify schedulers, HA elector (`HAEnabled`). |
| 10 | `&http.Server{...}` + `ListenAndServe` in a goroutine with SIGINT/SIGTERM handling; `defer moduleMgr.StopAll(ctx)`, `defer d.Close()`. |

### Plugin API / `Module interface` (`internal/module`)

This is the **Plugin API** skygate uses to make optional capabilities opt-in. Tailscale is
Module #1. Registration is deliberately static — compile-time `Register()`, no `.so`
loading, no marketplace, no cross-module dependency graph.

**The `Module interface`** (`internal/module/module.go`) is exactly nine methods, all of
which must be safe for concurrent use (the Manager runs `Health()` in a loop while HTTP
handlers call `Status()` in parallel). `Name() string` is a stable lowercase id, also used
as the state directory name and the `/admin/modules/{name}` URL slug. `Init(ctx, cfg
ModuleConfig) error` validates prerequisites (`ErrNotInstalled` or a wrapped error) and
loads persisted state without starting anything. `Start(ctx) error` is an idempotent
bring-up — on error the Manager moves the module to `StateError` and records `LastError` —
and `Stop(ctx) error` is an idempotent graceful shutdown. `Status() ModuleStatus` must be
cheap (no I/O; it runs on every `/admin/modules` render) and `Health() HealthStatus` must
return within 5 s (it runs on the 30 s loop and is persisted to `state.json`).
`SubFeatures() []SubFeature` is what the Manager renders toggles from and validates
`EnableSubFeature` against, and `EnableSubFeature(ctx, name) error` /
`DisableSubFeature(ctx, name) error` leave the side effects (e.g. advertise/un-advertise a
route) to the module — the next `SubFeatures()` call must reflect the new state. An
optional `Installer` interface (`Install(ctx) error`) covers modules with an out-of-band
install step; Tailscale implements both and the Manager never auto-installs.

**Supporting types** (all in `module.go`): `ModuleConfig{DataDir, SocketDir, Env
map[string]string, DBC func() *sql.DB, AuditLog func(action, detail string)}` — passed to
`Init`, **must not be mutated**, with the data-dir convention
`/var/lib/skygate/modules/<name>/` and sockets `/var/run/skygate/modules/<name>/`;
`AuditLog` action strings follow `module.<name>.<verb>`
(`module.tailscale.install`, `module.tailscale.start`,
`module.tailscale.subfeature.enable`). `ModuleStatus{State, InstalledAt, StartedAt,
LastError, Info map[string]string}` — `Info` must never carry secrets (it lands in admin
HTML). `HealthStatus{Healthy, LastCheck, Checks map[string]bool, LastError}` — `Checks`
keys are free-form (`auth_ok`, `interface_up`, `peers_visible`, …) and render as a
checklist. `SubFeature{Name, Description, Enabled, Requires []string, Impact}` —
`Requires` is enforced by the Manager, `Impact` is display-only. Lifecycle constants:
`StateNotInstalled`, `StateInstalled`, `StateStarting`, `StateRunning`, `StateStopping`,
`StateStopped`, `StateError`. **Sentinel errors**, matched with `errors.Is`:
`ErrNotInstalled`, `ErrAlreadyRunning`, `ErrAlreadyStopped`, `ErrSubFeatureNotFound`,
`ErrSubFeatureRequires`.

**Persisted state** (`internal/module/state.go`): one file per module at
`<dataDir>/<name>/state.json` holding `State{Name, State, Enabled, InstallMode,
InstalledAt, StartedAt, SubFeatures map[string]bool, LastHealth, LastError, Info}`.
JSON tags are stable; additions must be zero-value compatible; fields must not be
renamed or removed without a migration path. `saveState` is the atomic-write
chokepoint: marshal indented → write `state.json.tmp` (0640) under the `0750` module
dir → `os.Rename` onto `state.json` (with the mutex held, so concurrent saves
serialise). `loadState` returns a fresh `&State{Name, SubFeatures: {}}` when the file
is absent and repairs a missing `Name`/`SubFeatures` on read. `stateChanged(a, b)`
suppresses the write when nothing changed, so the 30 s health tick does not touch the
disk. Public `LoadState`/`SaveState` wrappers exist for out-of-package modules.

**`Manager`** (`internal/module/manager.go`): `NewManager(dataDir, socketDir, auditLog)`;
setters `SetEnv`, `SetDBC`, `SetHealthInterval` (default **30 s**); lifecycle
`Register` → `InitAll` (`initOne` per module; a failure marks that module and does not
block the others) → `StartEnabled` (starts modules whose persisted `Enabled=true`) →
`StopAll`; per-module `Start`, `Stop`, `Enable`, `Disable`, `EnableSubFeature`,
`DisableSubFeature`; queries `Get`, `List` (`ModuleInfo`); the health loop
`StartHealthLoop` / `StopHealthLoop` / `healthLoop` / `checkAllHealth` / `checkOneHealth`.

**Wiring in `main.go`**: `module.NewManager("/var/lib/skygate/modules",
"/var/run/skygate/modules", <callback writing `module.*` audit rows>)`,
`SetEnv(collectModuleEnv(cfg))`,
`SetDBC(func() *sql.DB { return app.DB.Current() })`,
`Register(tailscalemod.NewModule())`, `InitAll(ctx)`, `StartHealthLoop(ctx)`,
`defer StopAll(ctx)`, then `adminSvc.Modules = moduleMgr` for `/admin/modules`.

**Drift vs the deleted `modules.md`:** that document described a
`ModuleConfig` carrying `AuditLog`/`DBC` directly and a two-argument
`manager.Register(...)`; the code uses the setters above. It also pointed at
`internal/module/tailscale/module.go` (the file is `tailscale.go`) and at
`docs/internal/architecture/modules.md` (now this file). Its roadmap B-blocks
(`B-mod-core`, `B-mod-tailscale`, `B-mod-admin`, `B-mod-install`, `B-mod-bcheck`) all
shipped; the four Tailscale sub-features (`cluster`, `telegram`, `derp`, `exit`) are
declared in code but only `cluster`/`telegram`/`derp`/`exit` support is partial — see
the module's own `subfeatures.go`.

---

## 3. Data layer

Skygate runs on **either SQLite (pure-Go `modernc.org/sqlite`) or PostgreSQL
(`jackc/pgx/v5`)**, chosen by the DSN. Both chains are first-class; SQLite was restored
in v1.5.4 and is the self-host default.

### DSN shapes (`db.DetectDSN`, `internal/db/dialect.go`)

| DSN | Dialect | Notes |
|---|---|---|
| `sqlite:/var/lib/skygate/skygate.db` | SQLite | **Exactly what the installers write** (`deploy/install-common.sh:resolve_db_type`) and what `.env.example` ships. |
| `sqlite::memory:`, `:memory:` | SQLite | Test mode; private DB per `*sql.DB`. |
| `file:/path/to.db`, `file::memory:?cache=shared` | SQLite | modernc URI form; `_pragma=` params appended unless the caller supplied some. |
| bare `/abs/path`, `./rel/path`, `C:\path\to.db` | SQLite | A lone `C:` colon is a drive letter, not a scheme. |
| `""` (empty) | SQLite | Default; caller falls back to `cfg.DBPath` unless `SKYGATE_DB_REQUIRE=1`. |
| `postgres://…`, `postgresql://…` | Postgres | libpq URL, e.g. `postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable`. |
| anything else containing `://` | Unknown | Callers must error: "unrecognised DSN scheme". |

Precedence: `SKYGATE_DB` → legacy `SKYGATE_DB_DSN` → `""`.

**The `sqlite:` prefix is stripped inside `openSQLite`** (`internal/db/open_sqlite.go`,
2026-09-18). The driver does not know that scheme, so pre-fix SQLite created a *relative*
file literally named `sqlite:/…`, failed with `SQLITE_CANTOPEN`, and the driver reported
`unable to open database file: out of memory (14)` — every native SQLite install was dead
on arrival. All SQLite entry points must keep going through `openSQLite` so this
normalisation applies. On a file DSN it also appends
`_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)`.

### Migration chains — the parity rule

| Backend | Registry | Runner | Migration functions |
|---|---|---|---|
| PostgreSQL | `pgMigrations []MigrationEntry`, `internal/db/driver_postgres.go` | `MigratePostgres` — `SET lock_timeout='5s'`, ensure the tracking table, run entries in order, `RecordMigrationApplied` each | `migrations_pg.go` (v20–v62) + `migrations_v0_{62,63}_b194.go`, `_64_b195.go`, `_65_b198.go`, `_66_b211.go`, `_67_b221.go`, `_68_b232.go`, `_69_b235_3.go`, `_70_b238.go`, `_71_derp_cert_sync.go` |
| SQLite | `sqliteMigrations []MigrationEntry`, `internal/db/driver_sqlite.go` | `MigrateSQLite` — same shape, same bookkeeping policy | `migrations_sqlite.go` (v20–v70) + `migrations_v0_71_derp_cert_sync.go` |

Measured today: **50 entries in each registry, same version numbers, same order**, with
the historical gaps (40, 52) preserved on both sides.

**Rule: every schema change lands in BOTH chains.** Add `migrateV0NNPG` and
`migrateV0NNSQLite`, add **one** `MigrationEntry` to **each** registry with the same
`Version`/`Name`/`SourceFile`, and keep `SourceFile` pointing at the file that really
defines the function. `ApplyMigrations(db, kind)` dispatches when you have a `*sql.DB`
but no `Dialect`. `applied_migrations` (created by v49, checksum helpers in
`internal/db/migration_tracking.go`) records what ran; `skygate migrate status` compares
the registry against it and reports `pending` / `extra` (extra = binary downgrade).

### The single SQLite DDL chokepoint

`internal/db/sqlite_ddl.go` exists because `migrations_sqlite.go` began as a mechanical
reverse-port of the PG source, and several statements were copied verbatim as
`ALTER TABLE … ADD COLUMN IF NOT EXISTS …` — a **syntax error in SQLite**. The port
wrapped them in error-swallowing loops, so the syntax error read as "column already
exists" and four columns never appeared on a fresh SQLite DB:
`exit_servers.ssh_target`, `exit_servers.ssh_key_path`, `exit_servers.accept_routes`,
`device_rules.device_ip`.

`execSQLiteDDL(d *sql.DB, stmts []string) error` is now **the only** way a SQLite
migration executes statements:

- anything matching `sqliteAddColumnRe` (it tolerates the PG `IF NOT EXISTS` spelling)
  goes to `addColumnIfMissingSQLite(d, table, column, ddl)`, which asks the catalog
  first via `sqliteColumnExists(d, table, column)` (`PRAGMA table_info`), then issues a
  plain `ALTER TABLE … ADD COLUMN`;
- everything else (`CREATE TABLE/INDEX IF NOT EXISTS`, `INSERT OR IGNORE`, …) is
  executed and its error **returned** — those forms are idempotent by construction, so a
  failure means the schema is genuinely broken and the migration must stop.

Never re-introduce `d.Exec(q); continue` loops or swallow an error in a SQLite
migration. Table/column names are interpolated into the PRAGMA, so they must be
hardcoded literals — never user input.

### Cross-dialect SQL rules

- **Placeholders:** `$1, $2, …` is universal on both drivers; `?` is a bug in shared SQL
  (pgx does not translate it — SQLSTATE 42601). Use `db.PlaceholdersList(n)`,
  `db.PlaceholdersRange(from, to)`, `db.PlaceholderAt(n, i)`
  (`internal/db/placeholders.go`) rather than typing either form by hand.
- **Fragments:** `DialectKind.Placeholders`, `.UnixEpoch`, `.InsertIgnore`,
  `.BooleanType`, `.BooleanLiteral` (`internal/db/dialect.go`), plus
  `internal/db/{now_unix,on_conflict}*.go`.
- **Dispatch at runtime:** `registerBackend` / `BackendOf(d)` tag a `*sql.DB`.
- **Hot reload:** always re-read the pool from a `ResettableDB` (`d.Current()`); a
  captured `*sql.DB` field fails forever with `sql: database is closed` after the
  watchdog's first swap.

---

## 4. HTTP layer

**Router.** One `http.NewServeMux()` in `cmd/skygate/main.go`, Go 1.22+ method-prefixed
patterns (`mux.Handle("POST /admin/devices/{id}/delete", …)`), ~250 registrations in one
contiguous block, grouped `/my/*` then `/admin/*`, public routes mounted first. No
framework.

**Middleware, in order.**

1. `middleware.RequireAuth(cfg.JWTSecret)` (`authMW`) — checks *presence* of the
   `skygate_session` cookie (or a `Bearer` token ≥32 chars) and otherwise redirects to
   `/login`. Verification happens later: `App.currentUser(r)`
   (`internal/handlers/handlers.go`) is what calls `auth.ParseJWT` or walks the
   bcrypt-hashed personal API tokens.
2. `middleware.RequireLoginLimit(app.RateLimiter)` — on `POST /login` only; on block it
   redirects to `/login?err=rate_limited` with `Retry-After: 30`.
3. `middleware.RequireAPILimit(app.RateLimiter)` — on the two JSON exit-rules endpoints,
   nested inside auth: `authMW(apiMW(handler))`.

**There is no admin middleware.** Authorization is per-handler and uniform — essentially
every `/admin/*` handler opens with

```go
c := s.Backend.CurrentUser(r)
if c == nil || !c.IsAdmin { http.Redirect(w, r, "/dashboard", http.StatusFound); return }
```

~170 such guards exist across `feature/admin` and `feature/exit_rules`. A new admin page
must copy that guard; forgetting it exposes the page to any logged-in user.

**Templates.** Embedded with `//go:embed templates/*.html templates/*/*.html`
(`internal/handlers/templates.go`). `LoadTemplates()` parses every body file first (each
is a `{{define "body-…"}}`), then re-registers the real `renderBody` funcmap, then parses
`layout.html` last. `renderWithLayout(w, r, name, claims, data)` is the standard path; it
auto-injects `Lang`, `Page` (from `pageFromName` — this drives the active sidebar item),
`Username`, `IsAdmin`, `Theme`/`ThemeLabel`, `DisplayFont`/`DisplayScale`/`DisplaySelBg`,
and the notification bell's `UnreadCount` + list. `App.render` is the layout-less variant.

**i18n in templates** goes through the `t` / `tf` funcmap helpers, which read the
per-request language from `i18n.GlobalLang` (an `atomic.Value`). That is why both render
helpers call `LangFromRequest` + `i18n.SetLang(lang)` before `ExecuteTemplate`. Other
helpers: `safeJS`, `safeHTML`, `safeJSON` (use this one for catalog strings inside
`<script>`), `tolower`, `add`, `usageLevel`, `datetimeformat`, `bytesfmt`,
`humanizeAgeSeconds`, `indexResultByName`, `hasPrefix`, `dividefloat`.

**Flash-message pattern for POST handlers.** Every state-changing POST **redirects**; it
never renders. The convention is `303 See Other` to the page's own path with
`?ok=<msg>` and/or `?err=<msg>` (URL-encoded); the GET handler reads them into
`FlashSuccess`/`FlashError` which the template renders as an alert. Canonical helper:
`adminsvc.RedirectWithFlash(w, r, path, okMsg, errMsg)`
(`internal/feature/admin/helpers.go`); `feature/exit_rules` and `feature/my` use the same
shape inline. A handler that writes JSON or renders directly produces the "operator
clicked a button and got raw JSON" class of bug that B180 exists to prevent.

**i18n catalogs.** 14 files, `internal/i18n/catalog_*.go` (`catalog.go` + `common`,
`admin`, `my`, `bot`, `help`, `derp`, `exit_rules`, `exit_nodes`, `tailscale`,
`telegram`, `backup`, `update`, `user_subnet`, `modules`); measured ~778 KB, ~2 711 key
definition lines. Each file declares `var ruXxx` and `var enXxx` and registers both in
the `perFeatureRU`/`perFeatureEN` slices in `catalog.go`; `i18n.New()` merges per
language; `T(lang, key)` falls back RU → key itself. **Parity is enforced by test, not by
the type system** — `TestCatalogsParity` (`internal/i18n/i18n_test.go`) fails when a key
exists in one merged catalog only, and it is gate check **B4**, so a new key must be
added to both maps in the same commit. `TestHTMLSafeCatalog` additionally forbids raw
`<word>` in the `bot.*` prefixes sent with Telegram `parse_mode=HTML` (escape to
`&lt;`). Language resolution: `lang` cookie → `Accept-Language` → RU.

---

## 5. Self-update internals

The operator view is `docs/UPDATE.md`; this is the code map.

**Install-kind detection** (`internal/update/install.go`): `InstallUnknown`, `InstallDocker`,
`InstallSystemd`, `InstallBare`, `InstallOpenRC` (appended **last** on purpose — state and
audit rows carry the string, but ordinals must not move). `DetectInstallKind()` prefers
`SKYGATE_INSTALL_KIND`, then markers (`/.dockerenv`, `/run/.containerenv` → docker;
`/run/systemd/system` → systemd; `/run/openrc` → OpenRC), then bare, then unknown.
`IsNative()` covers the three native kinds. `internal/update/platform.go` renders the
`runtime.GOOS/GOARCH` + in-container + `systemctl`/`docker`/`rc-service` + helper-present
snapshot on `/admin/update`.

**Docker path** (`internal/update/docker.go`, `state.go`): a goroutine, not a synchronous
handler. State machine `pending → backup → pull_build → migrate → swap → verify → done`,
any phase dropping to `failed` after `rollback()`; the whole job (including a
`ManualFallback` copy of the manual steps) is persisted through `StateStore` to
`cfg.UpdateStatePath` (container default `/data/skygate-update-status.json`) so reloads
and restarts keep the job. One job at a time — `PostAdminUpdateApply` returns 409 when one
is in flight.

**Image-pull path** (`internal/update/image.go`): the fast lane (`docker pull
<registry>/skygate:<tag>` + `docker compose up -d` — seconds instead of a full rebuild).
It requires a registry-backed image (`ghcr.io/<owner>/skygate:<tag>`, i.e. the
`docker-compose.ghcr.yml` variant with `SKYGATE_IMAGE` set); `imageIsFromRegistry` refuses
a locally-built `skygate-skygate:latest` because `pull` would only re-tag the local image.
It backs the current image up under a `-prev` tag, sanitises the tag, short-circuits a
same-tag pull, and rolls back if compose-up + healthz polling fails.
`internal/update/shell.go` hides `exec` behind a `shellExec` var for tests.

**Native path** (`internal/update/native.go` + `deploy/skygate-apply-update.sh`): the
trust split exists because the native unit runs skygate **unprivileged** under
`ProtectSystem=strict` (`deploy/install-common.sh:write_systemd_unit`) — the process
cannot write `/usr/local/bin/skygate` and cannot restart its own unit, and restarting it
from inside the unit kills the whole systemd cgroup (so a `setsid` child dies too).

| Boundary | Artifact | Rule |
|---|---|---|
| unprivileged → root | `<update_dir>/request.props` | **Data only**: job id, target tag, previous build, pid. No paths. The applier never `source`s it — `read_prop` + strict `valid_target` parse and validate it. |
| root trigger (systemd) | `skygate-update.path` → `skygate-update.service` | Root-owned units installed by `install-common.sh`; no sudo/polkit anywhere. Alpine/OpenRC has no path unit and uses the sudoers trigger written by `install-alpine.sh`. |
| root worker | `deploy/skygate-apply-update.sh` → `/usr/local/lib/skygate/` | Backup → download the release tarball → verify `SHA256SUMS` (falling back to the GitHub Releases API per-asset `digest: sha256:<hex>` when a release ships no `SHA256SUMS`, failing closed when neither exists) → run `<new binary> migrate-only` **as the service user, before the swap** → atomic rename swap (a direct `install` onto a running binary is `ETXTBSY`) → restart → poll `/healthz` for the **build string** of the target tag → on failure restore `skygate.prev`, restart, verify, report `rolled_back`. |
| root worker → unprivileged | `<update_dir>/result.status`, `result.build`, `result.error`, `apply.log` | `update.ConfirmNativeSwap(store, updateDir)` folds these into the update state on the next `/admin/update` render **and at boot in `main.go`**, so the verdict lands even if nobody logs in. |
| config | `/etc/skygate/update-helper.conf` (root-owned), `/etc/skygate/skygate.env` (service-user-owned) | Every path the applier uses comes from the root-owned file; `skygate.env` is parsed as data and passed via `env(1)`, never sourced. |

Spelling note: the applier runs `migrate-only` as a **subcommand**; `--migrate-only` is
not a flag (`main.go` dispatches on `os.Args[1]`).

---

## 6. Conventions and invariants

**Build hygiene.** `gofmt` clean, `go vet ./...` clean, and **`staticcheck ./...` must
report zero issues** (`B237.20`); use `http.StatusXxx` constants, never bare `404`/`302`
(`B237.16`). `go.mod` requires `go 1.25.0` with `toolchain go1.25.4`, and CI installs Go
`'1.25'` in **both** jobs (`B23`). Do not add a CGO dependency: the runtime image is
`CGO_ENABLED=0` and installs no `gcc`/`musl-dev`/`sqlite-libs` (`B26`), and the Go build
happens at **container start** via `entrypoint.sh`, not at image build (`B22`, `B27`).

**The B-check rule.** One `scripts/check_b<NNN>_<slug>.sh` grep-contract script per
feature or bug fix, **registered in `scripts/verify_pre_deploy.sh`** with a `run_check`
entry whose description states what is pinned and how many contracts the script has. An
unregistered script is invisible to the gate; a registered script missing from disk fails
the gate.

**Secrets and hostnames.** `<VM_HOST>` is the placeholder for the operator's VM address.
`.githooks/pre-commit` blocks the operator's private LAN IP, the `skyadmin@…` literal, the
historical admin password and the default DB password in the staged diff; its header
states the private-LAN policy (`10.x`, `172.16-31.x`, `192.168.x`, excluding `192.0.2.x`
TEST-NET-1 and the Docker bridge ranges `172.17.0.x`/`172.18.0.x`). Documentation must use
`<VM_HOST>`, `example.com`/`<domain>` and RFC 5737 addresses (`192.0.2.x`, `198.51.100.x`,
`203.0.113.x`) — which is what `config.Load()`'s own defaults do
(`SKYGATE_DERP_PEER_NPM=192.0.2.67`, `SKYGATE_DERP_LAN_NET=192.0.2.0/24`,
`SKYGATE_BASE_DOMAIN=tsnet.example.com`). One exception survives in
`internal/config/config.go` (a real operator hostname as the default
`SKYGATE_OIDC_REDIRECT_URIS`) — see debt §9; do not copy that pattern. Never commit
`.env`, `secrets/` or `.log` files (`B10`).

**Editing rules.** Never edit Go (or shell) source with PowerShell line-index tricks —
no `Get-Content`/`Set-Content` slicing, no whole-file `-replace`; use the file tools so
the diff stays reviewable and CRLF/BOM state survives. Do not reformat files you are not
changing (`gofmt -l` is part of the gate, and reformat noise hides regressions). The
embedded bundle copies `internal/handlers/bundles/{setup.sh,README.md}` must stay
byte-identical to `deploy/subnet-router/{setup.sh,README.md}` (`make check-bundles`;
refresh with `make sync-bundles`).

**Do not break the three operator-managed files on the reference host.**
(1) `docker-compose.yml` — carries the operator's `extra_hosts` block and
`SKYGATE_HOST_TS` hostname; (2) `go.mod` and (3) `go.sum`. Run `git status` on the VM
before any `git reset` / `git checkout`, and never let a template or script overwrite
them. Related: the deployment `.env` and headscale's `config.yaml` are read as data and
never `source`d. Two operational consequences: `docker compose` interpolation is
**CWD-relative** (always `cd` into the project dir or pass `--env-file`, otherwise `.env`
placeholders silently resolve to their defaults), and `--force-recreate` is required after
`extra_hosts`/volume changes while a plain `restart` keeps the old network config.
Source-only changes need `docker compose restart` only, because `entrypoint.sh` rebuilds
the binary at start.

**Distribution invariants.** **The Docker image is intentionally linux/amd64-only**
(Issue #4 closure, 2026-09-11): the pre-v1.5.4 `release.yml` matrix pushed amd64 and arm64
to the same tag and the second push overwrote the first, so `:v1.5.2` had "no matching
manifest for linux/amd64" on amd64 hosts. Re-enabling arm64 is a future B-block; the
correct shapes are one `build-push-action` with `platforms: linux/amd64,linux/arm64`, or
two jobs plus `docker buildx imagetools create`. Do not re-add a naive matrix. The **Go
binary tarballs stay multi-arch** (linux/darwin × amd64/arm64, windows-amd64) — that is
the supported ARM path. **ghcr repository paths and tags must be lowercase**; `release.yml`
lowercases the owner before pushing, and a mixed-case path silently 404s the manifest.

---

## 7. The B-check contract system

**What a check script is.** `scripts/check_b<NNN>_<slug>.sh` is an executable, replayable
statement of "this feature is still wired the way the fix required". It is not a unit
test: it pins what a unit test cannot see — route registration, template fields, i18n
keys, wiring order, deployment artefacts, and "the old buggy string is gone". A typical
script pins 5–50 individual contracts.

**Structure.** `#!/usr/bin/env bash`, usually `set -euo pipefail` and often `set -f`
(patterns contain globs); a long header comment recording the pre-fix bug, the fix and the
A/B/C… contract list (keep it accurate — it is the historical record); local `ok`/`fail`/
`hdr` helpers (`printf` colours + `✓`/`✗`) or `PASS`/`FAIL` counters with a summary block.
Most scripts `cd "$(dirname "$0")/.."` so they work from any CWD, and most locate `go` on
PATH with a `/mnt/c/Program Files/Go/bin` / `/c/Program Files/Go/bin` fallback for Windows
hosts. A failing contract either exits 1 immediately or is accumulated into a summary —
either way the exit code is what the gate reads. Shared helpers live in `scripts/lib/`;
today that is one file, `scripts/lib/skip_if_no_docker.sh`, sourced as
`. "$(dirname "$0")/lib/skip_if_no_docker.sh"` (5 checks use it).

**Running one check, and the full gate.**

```bash
bash scripts/check_b261_native_self_update.sh   # one check
bash scripts/verify_pre_deploy.sh               # the whole gate (slow)
bash scripts/verify_pre_deploy.sh --quick       # skips the slow checks
make verify-pre                                 # same as the gate
```

`verify_pre_deploy.sh` is the single registration point. Measured: **291 `run_check`
invocations** (every contract the gate runs, including the inline `B1`–`B261` catalogue),
of which **186 reference a `check_b*.sh` script**. The canonical registration shape:

```bash
run_check "B261" "one-paragraph description: what broke, the fix, how many \
contracts the script has" \
  'test -f scripts/check_b261_native_self_update.sh && bash scripts/check_b261_native_self_update.sh'
```

`run_check` captures output, prints `PASS`/`FAIL` + the description, dumps the first 20
lines of a failure, and feeds the final summary table; `run_check_slow` is the
`--quick`-aware variant used by the VM-only smoke check. Environment-gated SKIPs are
explicit — `B8` (smoke) skips on a non-VM host unless `VERIFY_RUN_SMOKE=1`.

**How many checks exist today.**

| Measurement | Count |
|---|---|
| `scripts/check_b*.sh` on disk | **205** |
| `scripts/check_*.sh` (includes non-`b` verifiers) | 216 |
| `.sh` files under `scripts/` total | 293 |
| Distinct `check_b*.sh` names referenced by `verify_pre_deploy.sh` | 186 |
| Referenced by the gate but missing on disk | 1 (`check_b202.5.sh`) |
| On disk but never registered in the gate | 20 (e.g. `check_b199.sh`, `check_b200.sh`, `check_b201.sh`, `check_b202.sh`, `check_b238.sh`, `check_b258_tailscale_disabled.sh`, `check_b259_tailscale_toggle.sh`, `check_b260_2_derper_in_docker.sh`, plus the `check_b_*` family) |
| `run_check` invocations in the gate | 291 |

**The SKIP-not-FAIL rule.** A check whose question can only be answered against live state
(a running docker stack, a reachable PG, the operator's VM) must report **SKIP with exit
0** when that state is absent — never FAIL. A red gate must mean "something regressed",
not "this laptop has no docker". The implemented shape is
`scripts/lib/skip_if_no_docker.sh`: it exits 0 with a `SKIP:` line **only** when
`docker info` itself fails; if the daemon is up but an expected container is missing, the
check fails as before, because on the VM that *is* a finding. `SKYGATE_SKIP_DOCKER_GATE=1`
forces the gate off for debugging the check itself. Checks needing a live DB or VM follow
the same convention inline, and contracts needing root or a real binary swap are
deliberately left out of the script and recorded as "live canary verified" in the header
instead — `check_b261_native_self_update.sh` (contracts A–I, with the root-only swap
covered by a recorded canary run) is the reference example.

---

## 8. Where to look when…

| I need to… | Go to |
|---|---|
| **Change the DB schema** | `internal/db/migrations_pg.go` or a new `internal/db/migrations_v0_NN_*.go` **and** `internal/db/migrations_sqlite.go`; register in `pgMigrations` (`driver_postgres.go`) **and** `sqliteMigrations` (`driver_sqlite.go`); use `execSQLiteDDL` on the SQLite side; add a B-check; document the shape in `docs/db-schema.md`. |
| **Add an admin page** | `internal/feature/admin/<page>.go` (`GetAdminX` + `PostAdminX`, each opening with the `CurrentUser` + `IsAdmin` guard); route pair in `cmd/skygate/main.go` under `authMW`; `internal/handlers/templates/admin/<page>.html` with `{{define "body-admin/<page>.html"}}`; i18n keys (RU+EN) in `internal/i18n/catalog_*.go`; sidebar entry in `templates/layout.html` + `pageFromName`; POSTs redirect via `RedirectWithFlash`; then a new `scripts/check_bNNN.sh` registered in the gate. |
| **Add an i18n string** | Both `ruXxx` and `enXxx` maps in the same `internal/i18n/catalog_*.go` (parity = `TestCatalogsParity` / gate B4). Templates `{{t "key"}}` / `{{tf "key" .Arg}}`; JS `{{t "key" \| safeJSON}}`. |
| **Touch the updater** | `internal/update/` (`docker.go`, `image.go`, `native.go`, `state.go`, `install.go`, `platform.go`, `scheduler.go`, `manual.go`), `internal/feature/admin/update.go`, `templates/admin/update.html`, `deploy/skygate-apply-update.sh` + `deploy/install-common.sh` for the helper/units, and `docs/UPDATE.md` for the operator contract. Re-run the B261 native + B249 image-pull checks. |
| **Add a B-check** | Copy an existing script's structure, name it `scripts/check_b<NNN>_<slug>.sh`, `chmod +x`, add `run_check "B<NNN>" "<description incl. contract count>" 'test -f scripts/check_bNNN_<slug>.sh && bash scripts/check_bNNN_<slug>.sh'` to `scripts/verify_pre_deploy.sh`, and register the block in `AGENTS.md`. |
| **Change the ACL pipeline** | `internal/acl/acl.go` (`GenerateACL`, `GenerateACLWithVia`, `ApplyACLPipelineForPlane`). Do not re-implement the order (generate → snapshot → `SetPolicy` → mark/log) at a call site. See `docs/acl-rules-reference.md`. |
| **Change device deletion** | `internal/devicedelete/devicedelete.go` — both the user and admin paths call the same `Delete`; never add cleanup to only one handler. |
| **Add a module or sub-feature** | `internal/module/module.go` (implement `Module`, optionally `Installer`), `internal/module/tailscale/*` as the reference, register in `main.go`, surface in `internal/feature/admin/modules.go` + `templates/admin/modules.html`, and update §2 here (the `Plugin API` / `Module interface` strings are contract-checked). |
| **Add a background service** | Its own `internal/<pkg>` with a `Run(ctx)`/`Start(ctx, deps)` entry point, launched from `main.go` **behind its own config knob**, reading the pool through `ResettableDB.Current()`, and exposing a `SendAlert`-shaped sink instead of importing `internal/telegram` (that cycle is why the sink interfaces exist). |
| **Debug a SQLite-vs-PG difference** | `internal/db/dialect.go` (classification), `internal/db/sqlite_ddl.go` (an ADD COLUMN that vanished), `internal/db/placeholders.go` (the `?`-vs-`$N` rule). |

---

## 9. Known structural debt

Measured 2026-09-18; ordered roughly by how likely it is to bite.

1. **`cmd/skygate/main.go` and `internal/feature/admin` are oversized.** `main.go` is
   **3 971 lines / 182 KB**, one `main()` spanning ~2 950 lines (117–3068) holding the whole
   route table (~250 registrations) plus every background-service launch.
   `internal/feature/admin` is **115 files / 1.2 MB** (`tailscale.go` 73.7 KB,
   `telegram.go` 55.9 KB, `system_tests.go` 53.2 KB, `exit_nodes.go` 46.4 KB). There is no
   route-group abstraction, so adding a page edits three large files; a mechanical
   `routes_*.go` split would shrink every future diff.

2. **The migration chain is duplicated by design and drifts by hand.**
   `migrations_pg.go` (60.7 KB) and `migrations_sqlite.go` (57.4 KB) are separately
   hand-maintained ports with two independent registries, and `driver_sqlite.go`'s header
   still calls the SQLite side "generated by `scripts/port_migrations_sqlite.py`" — a
   generator that no longer exists in `scripts/`. Evidence: `sqliteMigrations` was
   **missing v71 entirely** (so `derp_cert_sync` never existed on SQLite) until
   2026-09-18, and its v70 `SourceFile` named the wrong file. Nothing structural prevents
   the next miss; parity rests on the 50/50 count coincidence and whichever B-check looks.

3. **`applied_migrations` metadata points at test files.** `MigrationEntry.SourceFile` for
   v0.60/v0.61 is `migrations_v0_60_b183_test.go` / `migrations_v0_61_b188_test.go` in
   **both** registries, and the names for v0.43–v0.59 are stubs like `"v0.43 (B0):"`.
   `SourceFile` is written into the DB, so `skygate migrate status` and the audit page
   report authorship that does not exist.

4. **20 check scripts are never run, and one registered script does not exist.** 291
   `run_check` invocations vs 205 scripts on disk (the unregistered 20 are listed in §7);
   `check_b202.5.sh` is referenced by the gate but absent, so that entry can only ever
   fail. (The related hole — `check_b_module_core.sh` contract L requiring the deleted
   `docs/internal/architecture/modules.md` — was closed in the 2026-09-18 restructure:
   contract L now points at the module section of this file.)

5. **The i18n catalogs are a 778 KB table with no compile-time parity.** 14 files,
   ~2 711 key lines; `catalog_admin.go` alone is 190 KB / 1 736 lines, `catalog_bot.go`
   126.7 KB, `catalog_my.go` 104.3 KB. RU and EN are independent `map[string]string`
   literals merged at runtime, and `T()` silently falls back RU → raw key, so a mistyped
   key renders the key name on the page instead of failing. Only `TestCatalogsParity`
   (gate B4) catches it, after the code is written.

6. **Dead and placeholder packages.** `internal/feature/subnet/` holds only `doc.go` (the
   real logic is `internal/subnet/`); there are **two** `dnsexternal` packages
   (`internal/dnsexternal` client, `internal/ha/dnsexternal` credentials);
   `internal/db/migrations_v0.54_pg_disabled.go` is a 0-byte tombstone. And
   `deploy/skygate-apply-update.sh` still prints `--migrate-only` in two log strings
   (~lines 60, 597) although the working call is the `migrate-only` subcommand.

7. **Duplicated ~250-line backfill logic kept in sync by hand.**
   `internal/feature/my/devices.go`'s own header says the `backfillNodeOwnership` helper
   "is a local copy — the canonical version lives in
   `internal/handlers/handlers_node_ownership.go` … kept in sync; dedup is left as a future
   refactor". The comment is doubly stale (the canonical version is now
   `internal/nodeownership/`, and the file has grown to 1 572 lines), and two copies of an
   ownership-matching algorithm is the class of bug B175/B176/B177 had to fix twice.

8. **CI carries a stale CGO assumption.** `.github/workflows/ci.yml` sets
   `CGO_ENABLED: 1` citing `github.com/mattn/go-sqlite3` — out of `go.mod` since v1.3.0,
   with both drivers pure Go. CI therefore never exercises the shipped `CGO_ENABLED=0`
   build mode.

9. **A real operator hostname is a code default.** `internal/config/config.go` ships
   `SKYGATE_OIDC_REDIRECT_URIS=https://head.<operator-domain>/oidc/callback`; every other
   operator-specific value moved to placeholders in v0.32.29. An allowlist entry rather
   than a secret, but it sits in the file contributors copy from.

10. **Repo-root scratch is tracked and the ignore rules are contorted.** `.gitignore` has
    ~12 overlapping blocks of one-off patterns (`deploy_*.sh`, `diag_*.sh`, `verify_*.sh`,
    `check_*.sh`, `__*.sh`, …) plus a late "re-assert the production allow-list" section
    explaining that the unanchored patterns had silently overridden `!scripts/check_*.sh`,
    leaving 15 files under `scripts/` untracked while `verify_pre_deploy.sh` referenced
    them. Two scratch artefacts were also tracked (`tmp_rename.sh` and
    `test_sql_dryrun_test.go.txt`); they were deleted in the 2026-09-18
    restructure.

11. **`internal/feature/admin/tailscale.go` mixes four concerns** at 73.7 KB (state reader,
    process-control handlers, subnet-route management, preferred-exit helpers) plus a local
    `urlQueryEscape` that `deploy.go` carries a comment telling people not to duplicate.
    Splitting it would let the B258/B258.1/B259 state machine be tested in isolation.

12. **The native update path is a two-language boundary with almost no automated
    coverage.** Go (`internal/update/native.go`, 23.5 KB) stages a request and folds a
    result; the shell applier owns the safety sequence; the contract between them is file
    names plus a strict target validator. One check script covers the source shape, while
    the root-only steps are explicitly *not* covered by any automated check ("live canary
    verified" in a plan document). Highest-consequence untested boundary in the tree.
