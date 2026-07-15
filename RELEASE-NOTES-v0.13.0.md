# v0.13.0 — Exit-node health monitor

> The "is my tailnet's egress actually working?" release. A
> background goroutine polls headscale every 5 min, classifies
> each configured exit-node as `online` / `degraded` / `offline`,
> surfaces the result on `/admin/exit-nodes` and the
> `/exit_nodes_health` bot command, and dispatches calm-mode
> alerts (online↔offline only) via the Notifier. Plus a
> `--strict` flag on the deploy-time `check_exit_nodes.py`
> so CI / automated deploys can hard-fail when an exit-node is
> offline.

---

## What's in this release

### 1. Continuous health monitor (background goroutine)

A new `internal/monitoring` package owns a single
`ExitNodeMonitor` instance per process. On a configurable
interval (default 5 min via `SKYGATE_EXIT_NODE_CHECK_INTERVAL`),
the monitor:

1. Calls `headscale.ListAllNodes()` to get the live view.
2. For each node, computes a state:
   * `online`  — `headscale.Online` AND `last_seen` within
     `SKYGATE_EXIT_NODE_OFFLINE_AFTER` (default 2 min) AND
     has `tag:exit-node` AND `0.0.0.0/0` + `::/0` approved.
   * `degraded` — online + tagged, but routes are not
     fully approved.
   * `offline` — not online, or no `tag:exit-node`.
3. Upserts the snapshot into `exit_node_health` (one row per
   node).
4. Detects state transitions, appends a row to
   `exit_node_state_changes` (the dedup log).
5. Dispatches pending alerts (rows with `alerted_at = 0`) via
   `Notifier.SendAlert`.

The monitor runs an **immediate pre-tick at startup**
(`SKYGATE_EXIT_NODE_CHECK_ON_STARTUP=true` by default), so a
fresh skygate that boots when all exit-nodes are down sends
the "0 healthy" alert on the first tick — not in 5 minutes.

### 2. Calm-mode alerts (online↔offline only)

Per the operator's choice: only the two transitions that
matter for "is the tailnet's internet egress up?" trigger a
Telegram alert:

* `online → offline`  — "🛰️ exit-node emilia: online → offline (went offline, 2026-07-15 14:23Z)"
* `offline → online`  — "🛰️ exit-node emilia: offline → online (came back online, 2026-07-15 14:30Z)"

`degraded` transitions are recorded in the audit log (so the
operator can see them in `exit_node_state_changes` if they
look) but don't spam the bot. Dedup is via the
`LatestExitNodeState` check: if the most recent recorded
transition for a node has the same `to_state`, the new
observation is a no-op for the alert path. Tested in
`internal/monitoring/exit_node_monitor_test.go::TestTick_Dedup_DoesNotReAlertSameState`.

### 3. Admin web UI (`/admin/exit-nodes`)

* **0-healthy banner** at the top of the page when 0 of N
  configured exit-nodes are healthy. The 0/0 case (no nodes
  configured at all) is intentionally NOT flagged — the
  empty table is a sufficient signal.
* **Health columns** added to the node table: state
  (online/offline/degraded), last_seen relative ("3m ago"),
  per-row ●/◐/○ marker next to the hostname. Empty cells
  mean "no snapshot yet" (the monitor hasn't ticked for this
  node).
* **"Run health check now"** button (admin only) calls the
  monitor's `CheckNow` synchronously and reloads the page.
  Useful when the operator fixes something and wants to
  see the fresh state without waiting up to 5 min.
* **X/N counter** in the page subtitle ("1/3 healthy").

### 4. Bot command (`/exit_nodes_health`)

New admin-only command. Lists the monitor's current view,
grouped by state (offline first → degraded → online) so a
quick scan surfaces what needs attention. Each row shows
hostname, state, last_seen relative, last_check timestamp.
Distinct from the existing `/exit_nodes` (which is the
per-user device list) and `/nodes` (every device).

Sample output:

```
Exit-node health: 2/3 healthy

Online (2):
• emilia           online    last_seen: 1m ago    last_check: 2026-07-15 14:32Z
• sharlotta        online    last_seen: 1m ago    last_check: 2026-07-15 14:32Z

Offline (1):
• karolina         offline   last_seen: 2h ago    last_check: 2026-07-15 14:32Z
```

### 5. Deploy-time check (`scripts/check_exit_nodes.py`)

Extended with a `--strict` flag and an **online check**.

* **Without `--strict`** (default, used by `make test`):
  * Routes-approved check (pre-existing): hard-fail on
    missing routes.
  * Online check (new): warn (`WARN: ...`) on offline
    exit-nodes, exit 0. Lets `make test` pass during planned
    maintenance when one relay is briefly down.
* **With `--strict`** (new `make check-nodes-strict`):
  * Same routes check (hard-fail on missing).
  * Online check: hard-fail (`FAIL: ...`) on offline
    exit-nodes. For CI / automated deploys that want to
    enforce "no deploy with an offline exit-node".

A node is "offline" iff `headscale.Online = false` AND
`last_seen` is older than `--offline-after` (default 120 s,
overridable via flag or env). The forgiving fallback for
`last_seen` covers transient WireGuard session drops (e.g.
a sleeping laptop).

### 6. Configurable knobs

New env vars in `.env.example`:

* `SKYGATE_EXIT_NODE_CHECK_INTERVAL=5m` — monitor tick
  interval. `off` or `0` disables the monitor entirely
  (the deploy check still runs from `check_exit_nodes.py`).
* `SKYGATE_EXIT_NODE_CHECK_ON_STARTUP=true` — immediate
  pre-tick at boot. Default true.
* `SKYGATE_EXIT_NODE_OFFLINE_AFTER=2m` — `last_seen`
  grace window before a node is treated as offline.

---

## Files changed

**New:**

* `internal/db/migrations_v0.36.go` — `exit_node_health` +
  `exit_node_state_changes` tables + indexes.
* `internal/db/exit_node_health.go` — typed helpers
  (Upsert/Get/List/Count/Record/Mark/Delete + the
  `LatestExitNodeState` dedup helper).
* `internal/db/exit_node_health_test.go` — 8 tests
  (round-trip, replace, ordered list, count, dedup,
  mark-alerted idempotency, transition log survival across
  snapshot delete).
* `internal/monitoring/exit_node_monitor.go` —
  `ExitNodeMonitor` struct, `Start` (background loop),
  `CheckNow` (admin trigger), `tick` (one pass). Pure
  functions separated from the goroutine so tests can
  exercise them without involving the runtime.
* `internal/monitoring/exit_node_monitor_test.go` — 10
  tests (computeSnapshot branches, tick online→offline,
  recovery, degraded-not-alerted, dedup, garbage
  collection, alert format).
* `internal/telegram/commands_exit_node_health.go` —
  `exitNodesHealthReply` + `formatAgo` helper.
* `RELEASE-NOTES-v0.13.0.md` — this file.

**Modified:**

* `internal/db/db.go` — wired `migrateV036`, added
  `OpenForTest` (exported so the monitoring package can
  build a real schema for tests).
* `internal/config/config.go` — three new env-driven fields
  (`ExitNodeCheckInterval`, `ExitNodeOnStartup`,
  `ExitNodeOfflineAfter`).
* `internal/handlers/handlers.go` — `App.ExitNodeMonitor`
  field.
* `internal/handlers/admin_exit_nodes.go` — overlay
  health-snapshot on every row in `AdminExitNodes`; new
  `PostAdminExitNodesHealthNow` handler; `humanizeDuration`
  helper for the "Xm ago" column.
* `internal/handlers/templates/admin/exit_nodes.html` —
  banner, new columns, Run-now button, sync-status JS
  via `{{t | safeJS}}`.
* `internal/telegram/commands.go` — `/exit_nodes_health`
  registered in `commandContext`, `adminOnly`, dispatch.
* `internal/telegram/commands_phase4.go` — `/exit_nodes_health`
  help text.
* `internal/i18n/catalog.go` — 18 new keys × 2 langs
  (`exit_nodes.health.*` for the web banner + columns +
  Run-now button; `bot.exit_nodes_health.*` for the bot
  command; `exit_nodes.sync_*` for the in-page sync status
  text).
* `cmd/skygate/main.go` — new route `POST
  /admin/exit-nodes/health-now`; `monitoring.ExitNodeMonitor`
  wired in `main()`; `app.ExitNodeMonitor = exitMon` so
  handlers can call `CheckNow`.
* `scripts/check_exit_nodes.py` — extended with
  `--strict`, `--offline-after`, online check, argparse.
* `Makefile` — new `check-nodes-strict` target.
* `.env.example` — three new env vars documented.

**No changes to:**

* `internal/acl/acl.go` — the v0.12.0.2 fix is still the
  final state.
* `internal/handlers/exit_rules*.go` — unchanged.
* `internal/headscale/*` — using the existing
  `ListAllNodes` interface.
* `scripts/smoke.sh` — health is not on the user-flow
  paths; smoke assertions are unchanged.

---

## Deployment notes

Same pattern as v0.12.0.2:

```bash
ssh skyadmin@192.168.13.69
cd /home/skyadmin/skygate
git pull
docker compose up -d --force-recreate --no-deps skygate
# wait ~5 min for the in-container go build
make test
```

After smoke reports `[ru] 59 pass, 0 fail` and `[en] 59 pass,
0 fail`, the monitor is already running and ticking. To
verify locally:

```bash
# In /admin/exit-nodes, the new "State" and "Last seen"
# columns should be populated within 5 min. The "Run
# health check now" button is immediate.

# In Telegram (as admin): /exit_nodes_health shows the
# current snapshot.

# Deploy-time check:
make check-nodes            # warn-only (default)
make check-nodes-strict      # hard-fail (CI)
```

To simulate a degraded exit-node for the alert test:

1. `docker exec headscale headscale nodes tag -i 11 -r tag:exit-node`
   (removes the tag from karolina, id=11).
2. Wait one monitor tick (or click "Run health check now").
3. Telegram should receive:
   `🛰️ exit-node karolina: online → degraded (tag:exit-node removed, 2026-07-15 14:35Z)`.
   Note: degraded transitions are recorded in the audit
   log but NOT alerted (calm mode).

To simulate an offline exit-node:

1. `tailscale down` on emilia (or power it off).
2. Wait until `last_seen` is older than 2 min.
3. `🛰️ exit-node emilia: online → offline (went offline, ...)` is sent.
4. When emilia comes back, `🛰️ exit-node emilia: offline → online (came back online, ...)` is sent.

---

## Test results

* 12 / 12 packages green (`go test ./...`)
* Smoke 118 / 118 (no changes to smoke scenarios)
* 10 monitor unit tests + 8 DB unit tests (new in v0.13.0)
* Live verification on VM: monitor starts, /admin/exit-nodes
  shows state within 5 min, `/exit_nodes_health` works, the
  0-healthy banner fires when all relays are intentionally
  untagged.

---

## What's next

* **v0.13.1** — per-plane ACL (split per-user ACL by
  control plane). See `docs/skygate-as-shell.md`.
* **v0.13.2** — ACL import/export with dry-run preview.
* **Butler voice v3** — urgency marks (`🪶` / `🪶!` /
  `🪶!!` based on alert severity).
* **Personal API token rotation** — TTL + auto-rotate field
  for bot integration.
* **Auto-tag exit nodes from Skygate** — today operators
  have to manually `headscale nodes tag -i N -t
  tag:exit-node,tag:public` after deploy. v0.13.x could
  auto-tag nodes in the `exit_servers` table on first
  health check.
