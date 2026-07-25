# Release Notes — v0.28.4

**Tag**: `v0.28.4`
**Date**: 2026-07-25
**Type**: Feature (per-device exit-node override)
**Build**: `v0.28.3-3-gfcec985+fcec985`
**Status**: Live on `192.0.2.1` (OLD VM), shipped to `origin/main`

---

## TL;DR

v0.28.3 закрыл exit-node bypass, но как побочный эффект — workstation-3 (а
также другие устройства, наследующие `admin`'s per-user via)
теперь pinned к **relay-1**, а не к **relay-3** (как раньше).
Оператор не мог это переопределить.

**v0.28.4** — per-device preferred exit-node. Конкретное устройство
(например workstation-3) можно pinned к другому exit-node, нежели per-user
default. UI в `/my/devices` (self-service) и `/admin/devices` (admin
override).

- workstation-3 → `tag:exit-relay-3` (specific override, перебивает admin → relay-1)
- workstation-1 (no override) → relay-1 (per-user default)
- relay-3's 148 PrimaryRoutes по-прежнему недоступны workstation-3 (via
  constraint relay-3≠relay-1 блокирует), но workstation-3 может **использовать
  relay-3 как exit-node** для autogroup:internet

---

## Data model

**Migration v0.46**:
```sql
CREATE TABLE device_exit_node_prefs (
  user_id INTEGER NOT NULL,
  device_hostname TEXT NOT NULL,
  exit_node_tag TEXT NOT NULL,
  set_by_user_id INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  PRIMARY KEY (user_id, device_hostname),
  FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
);
```

Композитный PK `(user_id, device_hostname)`. Один row на устройство
(upsert). Удаляется автоматически при `DELETE FROM portal_users`.

**Why a separate table (not a column on `user_exit_node_prefs`)**:
- `user_exit_node_prefs` имеет PK на `user_id` (1 row на user,
  v0.28.1 contract). Расширять его до composite key сломало бы
  v0.28.1 `SetUserExitNodePref`.
- Per-device pref — логически другая сущность: "user's default"
  vs. "this specific device's override". Могут сосуществовать
  (admin → relay-1, workstation-3 → relay-3).
- `set_by_user_id` в обоих случаях: self-service vs. admin
  override (audit trail).

---

## ACL builder

`GenerateACLWithViaForPlane` читает `device_exit_node_prefs` и
emit per-device grant **ПЕРЕД** per-user grant (Tailscale first-match):

```json
{ "src": ["tag:dev-admin-workstation-3"],
  "dst": ["autogroup:internet"],
  "ip":  ["*"],
  "via": ["tag:exit-relay-3"] }
```

Порядок важен: workstation-3's tag `tag:dev-admin-workstation-3` совпадает с per-device
grant ПЕРВЫМ (Tailscale first-match wins) → via=relay-3. Без
per-device override — falls through к per-user grant (src=admin@…
via=relay-1).

**Per-device grant покрывает ТОЛЬКО autogroup:internet**. User's own
stuff (own devices, own subnet) остаётся на per-user grant (для
прямого tailnet-трафика via не нужна).

Live verification на VM после `POST /admin/devices/preferred-exit`:
```
=== per-device grants (autogroup:internet) ===
  src=tag:dev-admin-workstation-3  via=['tag:exit-relay-3']  dst=['autogroup:internet']

=== per-user grants (with @tsnet) ===
  src=admin@tsnet.example.com  via=['tag:exit-relay-1']     dst=[..., 'autogroup:internet']
  src=user1@tsnet.example.com   via=['tag:exit-relay-2'] dst=[..., 'autogroup:internet']
  src=user2@tsnet.example.com    via=['tag:exit-relay-3']  dst=[..., 'autogroup:internet']

=== order check: per-device before per-user ===
  per-device at index(es) [0]
  per-user at index(es) [1, 2, 3]...
  ✓ per-device grant emitted BEFORE per-user grant
```

---

## UI

### `/my/devices` (self-service)

Новая колонка "Exit node" в таблице устройств:
- **Pinned state**: tag-info badge с `tag:exit-<name>` + кнопка clear (×)
- **Unpinned state**: dropdown со списком доступных exit-nodes
  (relay-1/relay-2/relay-3) + кнопка pin (◎)

Форма постит на `POST /my/devices/preferred-exit` с `hostname` и
`tag`. Handler проверяет что caller owns the device (lookup в
`node_owner_map.hostname` + `tagged_by_user_id`) — prevents
impersonation (alice не может изменить pref bob's device).

### `/admin/devices` (operator override)

Та же колонка, но в admin-контексте. Operator может pinned
любое устройство любого пользователя. Реализация:

1. Handler строит `SkygateUserByHost` map: `lowercased hostname → skygate user_id`
2. **Source**: per-device ACL tag (`tag:dev-<user>-<device>`) — НЕ
   `n.UserName` (которая после tag-driven reassignment становится
   `"tagged-devices"`). The dev tag is the authoritative owner link.
3. Template uses `SkygateUserByHost[hostname]` → form `user_id=1` для workstation-3.

Hotfix: первая попытка deploy'а показала "—" для workstation-3 (потому что
template использовал `n.UserName` напрямую → "tagged-devices" → no
match). v0.28.4 hotfix 0e5394c→fcec985 переключил на dev-tag-based lookup.

---

## Endpoints

### `POST /my/devices/preferred-exit` (self-service)

```html
<form method="post" action="/my/devices/preferred-exit">
  <input type="hidden" name="hostname" value="MSI">
  <input type="hidden" name="tag" value="tag:exit-relay-3">
  <button>Pin</button>
</form>
```

Caller must own the device (validated via `node_owner_map.hostname +
tagged_by_user_id`).

### `POST /admin/devices/preferred-exit` (admin-only)

Same form, plus `user_id` field. Admin can target any user's device.

Both endpoints:
1. Write to `device_exit_node_prefs` (upsert)
2. Audit log row (`my_device_preferred_exit_set` /
   `admin_device_preferred_exit_set`)
3. `acl.ApplyACLPipelineForPlane` to push the new policy to headscale
4. Redirect to source page with `?ok=1` or `?err=...`

---

## Tests (3 NEW)

```
=== NEW: v0.28.4 invariants ===
1. TestGenerateACLWithVia_PerDeviceGrantEmittedBeforePerUser
   - per-device grant (via=relay-3) appears in grants[] BEFORE
     the per-user grant (via=relay-1). Tailscale first-match ordering.

2. TestGenerateACLWithVia_PerDeviceGrantOnlyCoversAutogroupInternet
   - per-device grant's dst is EXACTLY ["autogroup:internet"]
     (no own-identity, no own-subnet). User's own stuff stays
     on the per-user grant.

3. TestGenerateACLWithVia_NoPerDeviceGrantWhenNoPrefsSet
   - no per-device prefs set → no per-device grants emitted.
     The v0.28.3 catch-all (tag:public → autogroup:internet) is
     still present.
```

All 13 v0.28.x ACL tests PASS. All 17 Go packages green.

---

## Verification (live, OLD VM)

### Live policy

- 0 acls, **256 grants** (was 255 in v0.28.3; +1 for workstation-3's per-device grant)
- 1 per-device grant: `tag:dev-admin-workstation-3 → autogroup:internet via=[tag:exit-relay-3]`
- 5 per-user grants: 3 with via (admin→relay-1, user1→relay-2, user2→relay-3), 2 without
- Order: per-device first (index 0), per-user after (indices 1+)
- Catch-all: `* → tag:public`, `* → tag:exit-node`, `tag:public → autogroup:internet`

### Smoke

```
[en] ---- SUMMARY (en): 83 pass, 0 fail
[ru] ---- SUMMARY (ru): 83 pass, 0 fail
```

### /admin/devices (workstation-3 row)

```
id=15  workstation-3  tagged-devices  100.64.100.11  tag:private  tag:dev-admin-workstation-3
      [PIN: tag:exit-relay-3] [× clear]  online
```

### /healthz, /readyz

```
{"build":"v0.28.3-3-gfcec985+fcec985","status":"ok", ...}
{"healthy":true,"db":"ok","headscale":"ok", ...}
```

---

## How to use (operator)

### Pin workstation-3 → relay-3

1. Open `/admin/devices`
2. Find the workstation-3 row
3. In the "Exit node" column, the dropdown shows available exit-nodes
4. Select `relay-3` and click ◎ (pin)
5. Page reloads with `?ok=1`
6. The next headscale push (immediate on ApplyACLPipelineForPlane) makes
   workstation-3's `tag:dev-admin-workstation-3` packets to autogroup:internet use
   relay-3 as the exit-node

### Pin a different device (e.g. workstation-1 → relay-3)

Same flow, just pick a different device row. The pin is
per-device, doesn't affect the user's default.

### Clear a pin

Click the × button next to the pinned tag. Page reloads, the
device falls back to the per-user default (or no via, if no
default).

### Pin via the bot (future)

The v0.28.4 endpoints aren't wired to the bot yet (out of scope
for this release). The /my and /admin web paths are the
operator's primary surface. Bot wiring is a v0.28.5 follow-up.

---

## Files changed

- `cmd/skygate/main.go` — register `POST /my/devices/preferred-exit`
  and `POST /admin/devices/preferred-exit`
- `internal/db/migrations_v0.46.go` — NEW: schema + helpers
  (`DeviceExitNodePref` struct, `GetDeviceExitNodePref`,
  `SetDeviceExitNodePref`, `ClearDeviceExitNodePref`,
  `ListAllDeviceExitNodePrefs`, `ListDeviceExitNodePrefsForUser`)
- `internal/db/db.go` — register `migrateV046`
- `internal/acl/acl.go` — `GenerateACLWithViaForPlane` reads
  `device_exit_node_prefs`, emits per-device grants BEFORE per-user
  grants, adds per-exit-node tagOwners entries
- `internal/acl/acl_test.go` — 3 NEW tests
- `internal/handlers/handlers_device_exit_pref.go` — NEW: two
  POST handlers (`PostMyDevicePreferredExit`,
  `PostAdminDevicePreferredExit`) + `callerOwnsDevice` check +
  `itoa64` helper
- `internal/handlers/handlers_my_devices.go` — fetch
  `devicePrefByHost` map, pass `DeviceExitPrefs`,
  `AvailableExitNodes` to template; per-row `DeviceExitPref` field
- `internal/handlers/handlers_admin_nodes.go` — fetch
  `deviceExitPrefMap` + `SkygateUserByHost`, pass to template
- `internal/handlers/templates.go` — `tolower` template func
- `internal/handlers/templates/user/devices.html` — "Exit node"
  column with pin/clear form
- `internal/handlers/templates/admin/devices.html` — same column
  for admin (uses dev-tag-based owner lookup)
- `internal/i18n/catalog.go` — 6 new keys × 2 langs (12 entries)

---

## Operational notes

### Rollback

If v0.28.4 breaks the ACL push, revert by:
1. `DELETE FROM device_exit_node_prefs WHERE 1=1` (removes all
   per-device grants, falls back to per-user only)
2. `POST /admin/exit-rules/reapply` (regenerates policy without
   per-device grants)

The per-user via (v0.28.1 + v0.28.3) stays intact.

### Future: bot integration

The /my and /admin web paths are the operator's primary surface.
A v0.28.5 follow-up could add:
- `/devices pin <hostname> <exit-node>` (admin bot)
- `/my device pin <hostname> <exit-node>` (user bot)

Out of scope for v0.28.4.

---

## What comes next

### v0.28.5 (small follow-up, ~1-2 days)

- Bot integration for per-device pref (`/my device pin ...`,
  `/devices pin ...`).
- `add_per_exit_node_tags.sh` deploy helper (already
  documented; just haven't shipped it yet).
- Cleanup 41 legacy `device_ip` rules (orphaned, pre-v0.28.0).

### v0.29.0 — Provisioning UI redesign (deferred from earlier backlog)

~8 days. Not blocking v0.28.x. The v0.28.x series is the
"per-device isolation + UI" foundation; v0.29.0 is the "operator
can ship this to multiple customers" productization layer.
