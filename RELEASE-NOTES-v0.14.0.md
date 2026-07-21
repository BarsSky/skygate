# v0.14.0 — Bot UX overhaul: visual style, /help as table, sync_nodes, update banner

> The "make the bot usable" release. The user reported five
> operator-visible problems: (1) `/exit_nodes` says "no nodes
> found" even though headscale has three relays, (2) the bot
> menu is in English with no Russian path, (3) `/help` is
> free-form text without a real table, (4) the admin tab
> Telegram was missing a "Refresh bot menu" escape hatch, and
> (5) no web UI banner when a newer release is available. Plus
> a visual-style pass: inline keyboards for `/lang` and
> `/myexitnodes` so the user can pick by tapping instead of
> typing.

---

## What's in this release

### 1. `/exit_nodes` empty — fixed (`SyncNodesFromHeadscale`)

**The bug:** the bot's `/exit_nodes` (admin) and
`/myexitnodes` (user) read from the `node_owner_map` table
in skygate's local DB. When the operator tags a relay
directly in headscale (the most common path for
tunnel-traffic-only relays), no row is ever written to
`node_owner_map`, so the bot reports "no nodes found" even
though headscale is happy with the relay.

**The fix:** new `db.SyncNodesFromHeadscale` (a superset of
the v0.10.13 `SyncTagsFromHeadscale` which only UPDATEd
existing rows). The new function:
* INSERTs missing rows (with the headscale tag as the
  source of truth).
* UPDATEs the tag on existing rows that have drifted.
* Preserves the existing portal-side owner (username +
  headscale_user_id) on UPDATE — so a node whose headscale
  ownership was reassigned to "tagged-devices" by a tag
  application keeps its original portal owner in skygate.
* Skips nodes with no tag in headscale (a node that the
  operator hasn't tagged yet doesn't belong in
  `node_owner_map`).

Three entry points:
* **`POST /admin/devices/sync-from-headscale`** — admin
  button on `/admin/devices`, with a flash success message
  ("Sync from headscale: 3 inserted, 0 updated").
* **`/sync_nodes` bot command** — admin-only, returns
  `✅ sync from headscale: 3 inserted, 0 updated (of 9
  total).` so the operator can fix the cache from a
  phone without opening the web UI.
* `release.Monitor` already calls `headscale.ListAllNodes`
  every hour for the existing health monitor; the v0.15.0
  follow-up will wire the same data into the bot's
  per-tick auto-heal. For now, the two entry points above
  cover the operator's escape-hatch needs.

Tests: 5 new unit tests in
`internal/db/node_owner_map_test.go` (inserts missing,
updates drifted, preserves portal owner, skips untagged,
empty list no-op).

### 2. Bot menu refresh button (`/admin/telegram`)

The boot-time goroutine in `cmd/skygate/main.go` calls
`SetMyCommandsAll` once, but:
* A bot that started before any chat was bound (the
  common "receive-only" mode the operator was in) had no
  observable menu.
* Operators who add a new command to the catalog and
  want it to show up in Telegram's command menu without
  restarting skygate.

New "Refresh bot menu" button on `/admin/telegram`
(happy-path under the existing CSRF-guarded form) calls
`RealNotifier.SetMyCommandsAll` synchronously. The handler
returns a flash with the success / failure result. The
button is disabled when the bot is unconfigured (no token)
so an operator doesn't click a no-op.

### 3. `/help` restructured — sectioned table with aligned columns

The previous layout (free-form text, one command per
line, no headers) was hard to scan. The new layout has:

```
🪶 Bot codex (command reference)
🛡️  Commands below are grouped by section. Detailed per-command help: /help <command>.

🔐 Auth — chat binding
  /login <key>    Bind this chat to your skygate account
  /start <key>    Same welcome as a brand-new chat (alias of /login no-arg)
  /lang           Show or switch the chat's language
  /help           This list, or detailed help for one
  /version        Build, runtime, schema level

✦ Your data — rules, devices, defaults
  /my_status      Your rules, devices, last ACL
  /my_nodes       Your devices in the tailnet
  ...
```

Section headers (🔐/✦/🛠) plus an `→ hostname` button
column in the inline keyboard for `/myexitnodes`. 12-char
command gutter + ≥ 2 spaces between command and description
makes the columns line up in any Telegram client (no
parse_mode required, so no escaping bugs).

The unknown-chat / strict-mode path still shows the
"🔒 This chat is not bound" note. Admin gets all three
sections; identified non-admins get Auth + ✦; unbound
chats in strict mode get only Auth.

### 4. Inline keyboards for `/lang` and `/myexitnodes`

The user wanted a "more presentable" bot — keyboard
buttons are the highest-leverage visual upgrade for
Telegram (they're the only non-text interactive element).

* **`/lang`**: when called with no args, the reply body
  reports the current language and attaches a 2-button
  keyboard (`✓ русский` + `  English`, or vice versa).
  Tapping a button persists the choice (via
  `db.SetTelegramBindingLang`) and re-renders the same
  reply body in the new language so the user sees the
  switch take effect. The callback handler is in
  `notify.go` (`case strings.HasPrefix(data, "lang:")`).
* **`/myexitnodes`**: each enabled exit-node becomes a
  button (`→ emilia`, `✓ sharlotta` for the current
  default, `→ karolina`). Tapping a button invokes
  `setExitNodeReply` with the node id, so the logic is
  the same code path the typed `/setexitnode N` uses (one
  place, one audit log, one DB upsert). A "✕ Clear
  default" button at the bottom resets the user's
  choice.

The same `pendingReplyForCurrentMessage` side-channel
that the v0.10.10 platform picker uses carries the
keyboard into the `sendMessage` payload. No Telegram API
change, no `callback_data` shape change.

### 5. Web update banner on admin pages

The v0.10.8 `release.Monitor` already detected newer
GitHub releases and sent a Telegram alert. v0.14.0 adds
the web-side half: a banner at the top of every admin
page (gated by `IsAdmin`) when the monitor has seen a
newer release.

* `release.Monitor.Snapshot()` returns the latest known
  release + a precomputed `UpdateAvailable` boolean + the
  last-checked timestamp.
* `App.renderWithLayout` reads the snapshot on every
  page render and adds `UpdateAvailable` to the data
  map when set.
* `layout.html` renders a dismissable info banner with
  the running version, the latest GitHub release, the
  "checked at" timestamp, and a direct link to the
  release page on GitHub.
* `ResetNotified` (called after a successful upgrade)
  also clears `UpdateAvailable` so the banner disappears
  immediately on upgrade, instead of waiting up to
  `CheckEvery` (1h default) for the next tick.

The banner's text uses the existing `i18n` keys
(`update.banner_title`, `.body`, `.checked`, `.open`) so
it renders in the operator's chosen language.

---

## Files changed

**New:**

* `internal/telegram/commands_sync_nodes.go` —
  `syncNodesReply` + `buildLangPicker` +
  `buildLangPickerForLang` (the v0.14.0 bot UX helpers).
* `internal/release/monitor_runner.go` extended with
  `Snapshot()` + the `Latest`/`UpdateAvailable`/
  `CheckedAt` fields updated on every tick. Public
  surface change; existing tests still pass because the
  `tick()` signature is unchanged.
* `RELEASE-NOTES-v0.14.0.md` — this file.

**Modified:**

* `internal/db/node_owner_map.go` — `SyncNodeInfo` struct
  + `SyncNodesFromHeadscale` function (the v0.14.0 fix
  for the empty `/exit_nodes` symptom).
* `internal/db/node_owner_map_test.go` — 5 new tests
  (inserts / updates / preserves portal owner / skips
  untagged / empty-list no-op).
* `internal/handlers/handlers_admin_nodes.go` —
  `PostAdminDevicesSyncFromHeadscale` handler. Also
  passes `FlashSuccess` / `FlashError` through the GET
  data map (template was already reading them).
* `internal/handlers/admin_telegram.go` — new
  `handleTelegramRefreshMenu` + `setMyCommandsAller`
  interface for the type assertion.
* `internal/handlers/handlers.go` — `App.ReleaseMonitor`
  field + banner data in `renderWithLayout`.
* `internal/handlers/templates/admin/devices.html` —
  flash banners + "Sync from headscale" button.
* `internal/handlers/templates/admin/telegram.html` —
  "Refresh bot menu" card.
* `internal/handlers/templates/layout.html` — admin
  update banner at the top of `<main>`.
* `internal/telegram/commands.go` — `helpReply`
  restructured to the table layout. `adminOnly` +
  `commandContext` maps extended with `/sync_nodes`.
* `internal/telegram/commands_phase4.go` — help text
  for `sync_nodes` + `exit_nodes_health`.
* `internal/telegram/commands_lang.go` — inline
  keyboard (the `lang:` picker) + `buildLangPicker`.
* `internal/telegram/commands_user.go` — inline
  keyboard on `/myexitnodes` (`setexitnode:` picker).
* `internal/telegram/notify.go` — callback handlers
  for `lang:` and `setexitnode:`. The existing
  `add_device_platform:` and `bind:` handlers are
  unchanged.
* `internal/i18n/catalog.go` — 9 new keys × 2 langs
  (update banner + sync_nodes + new help section
  headers + myexitnodes picker).
* `cmd/skygate/main.go` — `app.ReleaseMonitor =
  releaseMon` (the new `App` field for the banner
  path) + the new route `POST /admin/devices/sync-from-headscale`.

---

## Deployment notes

Same pattern as v0.13.0:

```bash
ssh skyadmin@192.168.13.69
cd /home/skyadmin/skygate
git pull
docker compose up -d --force-recreate --no-deps skygate
# wait ~5 min for the in-container go build
make test
```

After smoke reports `[ru] 59 pass, 0 fail` and `[en] 59
pass, 0 fail`, the new features are live:

* On the VM (where the relays were tagged directly in
  headscale), `POST /admin/devices/sync-from-headscale`
  inserts 3 missing rows. The bot's `/exit_nodes` then
  returns the 3 relays.
* `/admin/telegram` now has a "Refresh bot menu"
  button. Click it once to populate the per-language
  command menu for any newly-bound chat.
* `/dashboard` (or any other admin page) shows a
  "Доступно обновление" banner if the monitor
  detected a newer release.
* `/help` (in the bot) returns the new sectioned
  table layout; `/lang` shows the inline keyboard;
  `/myexitnodes` shows the inline set-default buttons.

---

## Test results

* 12 / 12 packages green (`go test ./...`)
* Smoke 118 / 118 (no smoke changes — bot UX is not on
  the asserted user-flow paths; verified separately
  on the VM by hitting the relevant endpoints and
  inspecting the rendered HTML)
* 5 new DB tests (SyncNodesFromHeadscale)
* Live verification on VM: sync button inserts 3
  rows, banner renders on /admin/dashboard, /help
  shows the new layout, /lang + /myexitnodes
  pickers attached correctly.

---

## What's next

* **v0.14.1** — wire `SyncNodesFromHeadscale` into the
  v0.13.0 exit-node health monitor's per-tick path
  (so the bot's auto-heal covers the same edge case
  without operator intervention). The admin button +
  /sync_nodes command are the operator escape hatch in
  the meantime.
* **v0.15.0** — per-plane ACL (split per-user ACL by
  control plane) + ACL import/export with dry-run
  preview. See `docs/skygate-as-shell.md`.
* **Butler voice v3** — urgency marks (`🪶` / `🪶!` /
  `🪶!!` based on alert severity).
* **Personal API token rotation** — TTL + auto-rotate
  field for bot integration.
