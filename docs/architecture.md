# Architecture

This document describes the runtime architecture of Skygate: the
components, how they talk to each other, the request lifecycle, and the
goroutines that run in the background.

> **TL;DR for the impatient:** Skygate is a single Go binary
> (`cmd/skygate/main.go`) that talks to a headscale instance over its
> REST API (and falls back to `docker exec` for tag changes), persists
> state in a single SQLite file (WAL mode), embeds HTML templates at
> build time, and optionally drives a long-poll Telegram bot for ops
> notifications and per-user commands. The whole thing runs in one
> container; no Redis, no message broker, no service mesh.

## High-level component map

```
┌──────────────────────────────────────────────────────────────────────┐
│                         SKYGATE BINARY                               │
│                                                                      │
│  ┌─────────────────┐                                                 │
│  │  http.ServeMux  │  routes (cmd/skygate/main.go)                   │
│  └────────┬────────┘                                                 │
│           │                                                          │
│  ┌────────▼────────┐  ┌────────────────┐  ┌──────────────────┐      │
│  │   middlewares   │  │   App handlers │  │ Telegram bot     │      │
│  │  - RequireAuth  │  │ (handlers_*.go)│  │ (notify.go +     │      │
│  │  - LoginRate    │  │   (25 files)   │  │  commands*.go)   │      │
│  │  - APIRate      │  │                │  │                  │      │
│  └────────┬────────┘  └─────┬──────────┘  └──────┬───────────┘      │
│           │                 │                    │                  │
│           └─────────┬───────┴────────────────────┘                  │
│                     │                                                │
│  ┌──────────────────┼────────────────────────┐                       │
│  │                  │                        │                       │
│  │  ┌───────────────▼──────────────┐         │                       │
│  │  │     internal/db              │         │                       │
│  │  │  - queries.go (SQL consts)   │         │                       │
│  │  │  - portal_users, preauth_*   │         │                       │
│  │  │  - device_rules, acl_*       │         │                       │
│  │  │  - audit_log, exit_servers   │         │                       │
│  │  │  - node_owner_map, secrets   │         │                       │
│  │  │  - telegram_bindings, prefs  │         │                       │
│  │  └─────────────┬───────────────┘         │                       │
│  │                │   *sql.DB                │                       │
│  └────────────────┼────────────────────────┘                       │
│                   │                                                  │
│  ┌────────────────▼────────────────┐  ┌──────────────────────────┐  │
│  │   internal/headscale            │  │  internal/auth            │  │
│  │  REST client (split into 9      │  │  - JWT (HS256) cookie     │  │
│  │  files) + CLI fallback for tags │  │  - bcrypt cost 12         │  │
│  │  + SSH for advertised-routes    │  │  - Personal API tokens    │  │
│  └─────────┬───────────────────────┘  └──────────────────────────┘  │
│            │                                                          │
│  ┌─────────▼───────────────────────┐                                 │
│  │  internal/i18n                  │                                 │
│  │  - 270+ catalog keys EN+RU      │                                 │
│  │  - funcmap Tr/Trf               │                                 │
│  │  - per-request locale           │                                 │
│  └─────────────────────────────────┘                                 │
│                                                                      │
│  ┌─────────────────────────────────┐  ┌──────────────────────────┐  │
│  │  internal/middleware            │  │ internal/ratelimit       │  │
│  │  - RequireAuth                  │  │  in-memory token bucket  │  │
│  │  - RequireLoginLimit            │  │  sweep every 5 min       │  │
│  │  - RequireAPILimit              │  │                          │  │
│  └─────────────────────────────────┘  └──────────────────────────┘  │
│                                                                      │
│  Background goroutines:                                              │
│    - RunDomainAutoUpdater (every 5 min, or DNS_AUTO_CHECK)           │
│    - staggeredSync (batched route sync to exit-nodes)                 │
│    - telegram.Run (getUpdates long-poll, 5s sleep when no token)     │
│    - RateLimiter.Sweep (every 5 min)                                 │
└──────────────────────────────────────────────────────────────────────┘
        │                                          │
        │ REST API + CLI fallback                   │
        │                                          │
┌───────▼─────────────────────┐         ┌──────────▼──────────┐
│  HEADSCALE                  │         │  TELEGRAM           │
│  - /api/v1/* REST           │         │  - getUpdates poll  │
│  - `headscale` CLI (in      │         │  - sendMessage      │
│    same container) for tag   │         │  - 6 phases         │
│  - SQLite (headscale db)    │         │                     │
└─────────────────────────────┘         └─────────────────────┘
        │
┌───────▼─────────────────────┐
│  TAILSCALE TAILNET          │
│  - exit-nodes (tagged)      │
│  - user devices (tag:private│
│    per portal user)         │
│  - shared devices (tag:public)│
└─────────────────────────────┘
```

## Module layout (where to look)

```
cmd/skygate/main.go                                — entry point, route table, bootstrap
internal/
  config/        — env-based config, no defaults that would surprise prod
  auth/          — bcrypt + JWT + personal API tokens (Bearer)
  db/            — SQLite layer
    db.go        — Open() + migrate() loop
    migrations.go + migrations_v0.20..v0.30.go
    queries.go   — all SQL strings as consts (Этап 9)
    portal_users.go, preauth_keys.go, device_rules.go, …
    *_test.go    — table-driven tests
  handlers/      — HTTP handlers
    handlers.go  — App struct + render + audit + getMaxRules (257 lines)
    handlers_auth.go, handlers_my_*.go, handlers_admin_*.go
    exit_rules*.go (form_my, form_admin, form_rollback, api, sync,
                     cleanup, routescript + _body variants)
    admin_*.go (backup, telegram, exit_nodes, settings)
    handlers_derp*.go (derp, derp_collect, derp_classify)
    templates.go + templates/*.html  — //go:embed
  headscale/     — REST client (split into 9 focused files)
    headscale.go (struct + do() + cache), users.go, preauth.go,
    nodes.go, tags.go, acl.go, routes.go, route_args.go
  i18n/          — EN/RU catalog + funcmap
  middleware/    — RequireAuth, RequireLoginLimit, RequireAPILimit
  ratelimit/     — in-memory token bucket
  telegram/      — bot (notify, commands, alerts, phase 2/3/4)
deploy/          — deploy.sh, backup.sh, validate.sh + lib/ + templates/
docs/            — user-facing documentation (you are here)
scripts/         — smoke.sh, check_exit_nodes.py, audit_routes.py
Makefile         — build / run / smoke / check-nodes / test / restart
AGENTS.md        — AI-hint file map + gotchas (read first if you’re a bot)
```

## Request lifecycle

A normal authenticated GET (e.g. `/my/exit-rules`):

```
1.   http.Request lands on http.ServeMux
2.   mux dispatches to the registered handler wrapper:
       mux.Handle("GET /my/exit-rules", authMW(http.HandlerFunc(app.GetMyExitRules)))
3.   middleware.RequireAuth(cfg.JWTSecret)
       - reads cookie "session"
       - parses + validates JWT (HS256, 24h TTL)
       - looks up portal_user from claims.subject
       - on miss → 302 /login
       - on hit → puts *portal_user into r.Context()
4.   app.GetMyExitRules (handlers/exit_rules_form_my.go)
       - reads user_id from context
       - calls db.ListDeviceRulesForUser(uid)
       - computes counters (used / max, per-device, total)
       - calls renderWithLayout(w, r, "exit_rules.html", data)
5.   renderWithLayout (handlers/handlers.go)
       - resolves i18n locale from r (cookie > Accept-Language > "en")
       - funcmap.Tr/Trf funcs read the catalog
       - executes layout.html → page.html with data
       - writes the response
6.   on the way out: audit (if action triggered a write)
```

A write that touches the ACL (e.g. `POST /my/exit-rules`):

```
1.  … through middleware, just like above
2.  app.PostMyExitRule (handlers/exit_rules_form_my.go)
3.  Validate input (target format, IP/domain, not exit-node source)
4.  Check limits: per-device (200 default), per-user (from cfg.UserMaxRules), global
5.  db.InsertRuleUnique(tx, rule) — INSERT OR IGNORE on (device_id, target_type, target_value, action)
6.  db.AppendExitRuleLog(tx, version, "create", detail)
7.  db.SaveACLSnapshot(tx, version, GenerateACL(...), actor)  — writes BLOB
8.  HS.SetPolicy(jsonBytes)  — REST → headscale; on 404/405 falls back to
                                docker run -v … alpine; on 5xx returns
                                *APIError (typed) and stops
9.  db.MarkACLApplied(snapshot.ID, success=true)
10. Write audit_log row
11. app.Notifier.SendAlert("🛡️ ACL #N by <user>")  — prefixes [#<id>],
                                                  rows telegram_alerts
12. Redirect back to /my/exit-rules with flash message
```

If step 8 fails (5xx from headscale), step 9 is **not** called, the
snapshot stays with `applied_success=0`, and step 11 sends a
`❌ ACL apply failed` instead of `🛡️`. The user sees an error on the
form; admin can retry via `/admin/exit-rules/rollback`.

## Background goroutines

| Goroutine | Where | What it does |
|---|---|---|
| **RunDomainAutoUpdater** | `cmd/skygate/main.go:238` | Every `DNS_AUTO_CHECK` (default 5m), walks domain-type rules in `device_rules`, resolves A/AAAA, inserts/updates /32 child rules with `parent_domain` set, prunes stale /32 rows. Disabled when `SKYGATE_DNS_AUTO_CHECK=0` or `off`. |
| **staggeredSync** | `internal/handlers/exit_rules_sync.go` | On `POST /admin/exit-rules/sync` (or periodic via autoupdater), groups all enabled rules per exit-node, sends `tailscale set --accept-routes=… --advertise-routes=…` over SSH to each node, with `StaggerBatchSize` rules per batch and `StaggerInterval` between batches. |
| **telegram.Run** | `internal/telegram/notify.go:Run` | Long-poll `getUpdates` from Telegram. When token is configured, dispatches `/`-prefixed messages through `HandleCommand`. When token is **not** configured, sleeps 5s and re-checks DB on each tick (hot-swap, no restart needed). |
| **RateLimiter.Sweep** | `cmd/skygate/main.go:103` | Every 5 min, drops stale entries from the in-memory token bucket so it can’t grow unbounded under low-rate traffic. |
| **http.Server.Serve** | `cmd/skygate/main.go:230` | The actual web server. Stops gracefully on SIGINT/SIGTERM (`signal.NotifyContext` + `srv.Shutdown(5s)`). |

## Authentication paths

```
+-------------------------+           +-------------------------+
|        Browser          |           |       CLI / script      |
+-------------------------+           +-------------------------+
            │                                     │
            │ cookie "session" (JWT HS256, 24h)    │ Authorization: Bearer <token>
            │                                     │
            ▼                                     ▼
   middleware.RequireAuth              middleware.RequireAuth
   (uses r.Context() to                 (uses r.Context() to
    stash *portal_user)                   stash *portal_user)
            │                                     │
            └─────────────┬───────────────────────┘
                          ▼
                  handler reads
                  user_id from ctx
```

The `Authorization: Bearer` path is for the public REST API at
`/my/exit-rules/api`. The cookie path is for browser traffic. The same
middleware handles both — see `internal/middleware/auth.go`.

## i18n

- Catalog lives in `internal/i18n/catalog.go` (270+ keys)
- `Tr(key)` returns a plain string, `Trf(key, args...)` returns a
  formatted string (placeholders: `%s`, `%d`, counted at test time)
- `lang` resolves from cookie `lang` → `Accept-Language` → `"en"`
- Per-request locale via `atomic.Value` swap on the funcmap — no
  per-request allocation, no map lock contention
- `internal/i18n/i18n_test.go` enforces parity: every `en` key must
  have an `ru` counterpart and vice versa, with matching placeholder
  counts

## Rate limits

In-memory only. **Single-instance deployments only.** If you ever
scale Skygate horizontally, the rate limits become per-replica and
lose their meaning.

| Endpoint | Limit | Window | Bucket |
|---|---|---|---|
| `POST /login` | 5 attempts | per 15s | per username |
| `POST /login` | 20 attempts | per 30s | per IP |
| `GET/POST /api` | 30 requests | per 60s | per IP |

429 response includes `Retry-After: <seconds>`. Sweep every 5 min
drops stale entries.

## What talks to what

| Component | Talks to | Protocol | How |
|---|---|---|---|
| HTTP handlers | Skygate DB | `database/sql` + `mattn/go-sqlite3` (CGO) | direct |
| HTTP handlers | Headscale | REST over HTTP | `internal/headscale/*` |
| HTTP handlers (tag ops) | Headscale | CLI via `docker exec` | `internal/headscale/tags.go` |
| HTTP handlers (route sync) | Exit-nodes | SSH | `internal/headscale/routes.go` |
| Telegram bot | Telegram | HTTPS long-poll | `internal/telegram/notify.go` |
| `deploy.sh` | Headscale DB | `docker run alpine … cp` | deploy/lib/docker.sh |
| `check_exit_nodes.py` | Headscale REST | `urllib.request` + `HEADSCALE_API_KEY` | script |

## Failure modes & their recovery

| Failure | What users see | What admin sees | Recovery |
|---|---|---|---|
| Headscale 5xx during SetPolicy | Form error “ACL not applied” | `❌ ACL apply failed` in Telegram + `/admin/audit` row | Re-add the rule, or `POST /admin/exit-rules/rollback` to an earlier snapshot |
| Headscale down at startup | Skygate still boots (just `warn: headscale list`) | startup log line | `docker compose restart headscale` then skygate auto-recovers on next call |
| Telegram API down | `SendAlert` returns error, `telegram_alerts` still records (id stays) | log line | Telegram comes back, next send goes through |
| SQLite WAL corruption | `db: disk I/O error` at startup | log + crash | `sqlite3 skygate.db "PRAGMA wal_checkpoint(FULL);"`; restore from backup if still bad |
| `/api` 429 | `429 Too Many Requests` + `Retry-After` | n/a | Wait, or back off |
| Forgot admin password | can’t log in | n/a | drop user from `portal_users`, set new `SKYGATE_ADMIN_PASS`, restart |

## CGO & cross-compile

- `mattn/go-sqlite3` needs CGO. The official Go installer ships gcc
  via MinGW, so `go build` works on Windows. Linux/macOS need gcc
  installed.
- `GOTOOLCHAIN=local` in the Makefile pins the local toolchain (no
  surprise auto-upgrade during `make build`).
- `-ldflags "-X main.version=$(git describe --tags --always)"` in
  the entrypoint flows the build label through to the web footer +
  Telegram `/version`.

## Versioning

- Build label: `git describe --tags --always` → `main.version`
- Commit SHA: `git rev-parse --short HEAD` → `main.commit`
- Build time: `date -u +%Y-%m-%dT%H:%M:%SZ` → `main.buildTime`
- `/version` (Telegram) reports all three + Go runtime + DB schema version
- Web footer shows the version only

## See also

- [docs/db-schema.md](db-schema.md) — every table + column
- [docs/api.md](api.md) — every HTTP endpoint + curl
- [docs/deploy.md](deploy.md) — install / backup / restore flow
- [AGENTS.md](../AGENTS.md) — file map + gotchas for AI assistants
- [CHANGELOG.md](../CHANGELOG.md) — version history
