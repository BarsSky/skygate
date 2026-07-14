# AGENTS.md — AI hints for Skygate

This file is for AI assistants (Hermes, Claude, Cline, Cursor, etc.) working on
or with Skygate. Read this **first** before suggesting changes or running tasks.

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

`GenerateACL()` in `internal/handlers/exit_rules.go` builds a **per-user** headscale
ACL using identities from `portal_users`. The very first rule is `*:*` was REMOVED.

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
    "tag:private": ["skyadmin@...", "michail@...", ...ALL portal users...],
    "tag:public": ["skyadmin@tsnet.skynas.ru"],
    "tag:exit-node": ["skyadmin@tsnet.skynas.ru"]
  },
  "groups": { "group:skyadmin": [...], "group:michail": [...], ... }
}
```

Tailscale ACL semantics: **first matching rule wins**. The catch-all `*:*` rule
that used to be first is gone; only the per-user rule applies to most traffic.
Each user can only talk to their own tag:private devices. tag:public /
tag:exit-node are visible to everyone (so users can pick exit-nodes).

**When editing `GenerateACL()`**: do NOT add `{"*", "*:*"}` as the first rule.
First-match semantics make it override everything else. The internet egress
must remain LAST, after per-user and tag rules.

The headscale base domain is hard-coded as `tsnet.skynas.ru` for now — it
is the only deployment. If you add another deployment, refactor to read it
from `config.Config`.

---

## Tailscale in skygate (Этап 14 v2, 2026-07-14)

The skygate container runs `tailscaled` in its own network namespace
and joins the tailnet with `tailscale up --accept-routes --accept-dns=false`.
**No `--exit-node` is ever set on skygate.** A separate node in the
tailnet (a "relay", e.g. emilia) advertises the canonical Telegram IP
ranges as subnet routes; skygate accepts them, so api.telegram.org
traffic flows through the relay while everything else (headscale, etc.)
stays direct. The bot is in this subnet-route path, NOT in a global
exit-node path.

### Why not a sidecar or an exit-node?

* **Sidecar (skygate-ts, removed in Этап 14 v2)**: `network_mode:
  service:tailscale` broke docker's embedded DNS (127.0.0.11:53
  refused UDP). Also the sidecar's entrypoint.sh called `tailscale up
  --state=...` with a flag `tailscale up` doesn't accept, so the
  sidecar died at startup and took skygate down with it (exit 137).
* **Exit-node on skygate**: `tailscale set --exit-node=<X>` replaces
  the default route for skygate — every non-tailnet packet goes via
  the relay, including unrelated future traffic. Subnet routes are
  scoped to just the Telegram IP ranges; cleaner and auditable.
* **Subnets-route mode wins**: per-destination routing, no global
  default-route hijack, no DNS collisions with Docker.

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

### Relay setup (one-time, on a separate node)

* `deploy/tailscale-relay/setup.sh` — run on the relay host
  (emilia/sharlotta/karolina). Calls `tailscale set
  --advertise-routes=...` with the canonical 8 v4 + 4 v6 CIDRs.
  Headscale admin must then approve them via
  `headscale nodes approve-routes --identifier N --routes ...`.
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

### Relay failover (2026-07-14, Этап 14 v3)

Both **emilia** and **sharlotta** advertise the same 8 v4 + 4 v6
Telegram CIDRs; Tailscale picks one based on metric. If emilia
goes down, the bot automatically starts using sharlotta within
~60s. Both have a weekly cron (`0 4 * * 1`) running
`/usr/local/bin/skygate-update-telegram-routes` to refresh the
routes from DNS. **karolina** is available as a third-tier backup
(not yet configured). See `docs/telegram-relay.md` for the
re-deploy recipe after a fresh relay setup.

### Files for this feature

* `Dockerfile` — multi-stage with tailscale binaries
* `entrypoint.sh` — tailscaled + tailscale up --accept-routes
* `docker-compose.yml` — caps + tun + secret
* `internal/handlers/handlers_telegram_probe.go` — probe logic
* `internal/handlers/handlers_telegram_probe_test.go` — 8 tests
* `internal/handlers/admin_telegram.go` — integrates probe
* `internal/handlers/templates/admin/telegram.html` — banner
* `static/css/themes.css` — probe-state CSS
* `deploy/tailscale-relay/setup.sh` — one-time relay setup
* `deploy/tailscale-relay/update-routes.sh` — IP refresh
* `docs/telegram-relay.md` — full procedure + troubleshooting

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
