# Skygate v0.12.0 — per-user headscale control plane

**Date:** 2026-07-15
**Branch:** `feature/v0.10.12-bot-ux`
**Previous:** v0.11.1 — runtime renderer (Apply + Test URL)

## What this release adds

Skygate-as-shell step 2: each `portal_users` row can now carry its
own `(headscale_url, headscale_api_key)` override, so a single Skygate
portal serves users on different headscale control planes. The use
cases from `docs/skygate-as-shell.md`:

- **Multi-region tailnets** — US users hit `head-us.example.com`,
  EU users hit `head-eu.example.com`. Skygate is the single portal
  for both.
- **Migration** — moving users from one control plane to another
  is a "change their headscale_url" admin action, not a redeploy.
- **Read-only audits** — operator can give Skygate a read-only
  API key against a headscale they don't own.

User-scoped requests (`/my/devices`, `/my/preauth`, `/my/keys`,
`/my/exit-nodes`, `/dashboard`) now route to the user's own
headscale, not the operator's primary one. Cross-user admin pages
(`/admin/devices`) still use the global plane.

## What changed for the operator

### 1. New `/admin/control-planes` landing page

Lists every distinct headscale plane (global + per-user) with the
user count and a "Test" button for the global plane (per-user
health has to be tested from the per-user form, since the per-user
key is encrypted and not exposed on the landing page).

### 2. New `/admin/users/{id}/plane` per-user edit form

Edit form for one user's `(url, key)` override. URL field is
pre-filled with the current value. The API key field is always
empty on GET (we never echo back the secret); saving without
entering a key keeps the existing key. A separate "Clear" button
resets to the global default.

The API key is AES-GCM encrypted with the server-side
`SKYGATE_SECRET_KEY` (32 bytes hex). Empty `SKYGATE_SECRET_KEY`
= per-user planes disabled (the form rejects the save with a
banner pointing the operator at `openssl rand -hex 32`).

### 3. SKYGATE_SECRET_KEY env var

Required to enable per-user control plane keys. Generate with:

```bash
openssl rand -hex 32
```

Add to `.env`:

```
SKYGATE_SECRET_KEY=<the hex string>
```

Restart skygate. The new env var is plumbed through `config.Config`
to `App.SecretKeyHex`; the per-user router uses it on every
read/write.

### 4. `App.HSForUser(userID)` is the new routing primitive

Two methods on `App`:

- `HSGlobal()` — the operator's primary headscale (built at
  startup from `HEADSCALE_URL` + `HEADSCALE_API_KEY`). Same
  instance every time, no extra alloc.
- `HSForUser(userID)` — returns the user's own client (built
  from the per-user `(url, key)` row), or the global client if
  the user has no override. Cached by `url` so a 2nd call for
  the same user returns the same instance; rebuilt on key
  rotation.

`HSForUser` falls through to the global client when:
- `SKYGATE_SECRET_KEY` is unset
- the stored key is corrupt (wrong key after a rotation)
- the user has no override (the common case)

Failures are logged so the operator sees the degraded state in
`docker logs skygate` — a corrupt key is operator-fixable, not
user-fixable.

### 5. User-scoped handlers now use HSForUser

Refactored:
- `/my/devices` — `ListAllNodes` on the user's plane
- `/my/preauth` — `CreatePreauthKey` on the user's plane
- `/my/keys` — `ListAllNodes` (for the "used" check) and
  `ExpirePreauthKey` on the user's plane
- `/my/exit-nodes` — `ListExitNodes` on the user's plane
- `/dashboard` — per-user `ListAllNodes` + `ListUsers` for the
  metrics section; admin still gets the global view

Cross-user handlers explicitly use `HSGlobal()`:
- `/admin/devices` — tag/untag still on the global plane
- `/admin/users` — user CRUD on the global plane

Bot handlers (`/my_nodes`, `/admin_nodes` in the Telegram bot)
still use the global `env.HS` for v0.12.0. Per-user bot
routing is a v0.12.1 follow-up — it requires extending
`BotEnv` with a `HeadscaleRouter` interface and a new
dispatcher in `notify.go`. The bot still works for the
common case (one Skygate = one control plane).

### 6. Per-plane ACL is deferred to v0.13.0

`GenerateACL()` still writes to the global headscale. A future
v0.13.0 release will split the per-user ACL by control plane
(separate policy per plane, with the operator's-eye view of
all planes on `/admin/acls`). See `docs/skygate-as-shell.md`
for the design.

## Architecture

### Migration v0.35

`portal_users` grows two new columns:

```sql
ALTER TABLE portal_users ADD COLUMN headscale_url TEXT NOT NULL DEFAULT '';
ALTER TABLE portal_users ADD COLUMN headscale_api_key_enc TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_portal_users_hs_url
  ON portal_users (headscale_url) WHERE headscale_url != '';
```

Empty `(url, key)` = use the global default (the v0.11.x
behaviour). Non-empty `url` + `key_enc` (encrypted) = the user
is bound to that plane.

### AES-GCM envelope encryption (db/secrets.go)

Two helpers:
- `EncryptForColumn(plaintext, keyHex string) (string, error)`
- `DecryptForColumn(ciphertext, keyHex string) (string, error)`

Format: `base64(12-byte nonce ‖ ciphertext ‖ 16-byte GCM tag)`.
Fails loudly on:
- missing `SKYGATE_SECRET_KEY` (`ErrSecretKeyUnset`)
- wrong key length (not 32 bytes)
- wrong key / tampered ciphertext (`ErrSecretCiphertextCorrupt`)

Tests pin: roundtrip, fresh nonce per call, tamper detection,
wrong-key rejection, empty-input no-op.

### Per-user client cache (handlers/app_controlplane.go)

`App.hsCache` is a `sync.Mutex`-protected map keyed by URL.
Cache hit checks the api key matches the stored one (an admin
key rotation invalidates the cached client). `InvalidateHSCache`
drops entries when the admin saves a new override.

Cache size is unbounded (operators have 1-5 planes in
practice; we don't bother with LRU). `InvalidateHSCache("")`
drops all entries.

### DB helpers (db/portal_users_controlplane.go)

- `GetUserHeadscaleConfig(d, userID, keyHex)` — returns the
  decrypted `(url, key)` pair or `ErrNoUserControlPlane`
- `SetUserHeadscaleConfig(d, userID, url, key, keyHex)` —
  encrypts + writes; empty `url` clears the override
- `ClearUserHeadscaleConfig(d, userID)` — clears both columns
- `AllUsersHeadscaleConfig(d)` — every `(username, url,
  key_enc)` row, for the per-plane summary
- `SummariseControlPlanes(rows, globalURL)` — buckets the
  rows into per-plane summaries; the global default is always
  first

## Files

**New:**

- `internal/db/migrations_v0.35.go` — schema migration
- `internal/db/portal_users_controlplane.go` — DB helpers +
  `UserControlPlane`, `PortalUserControlPlaneRow`,
  `ControlPlaneSummary` types
- `internal/db/portal_users_controlplane_test.go` — 14 tests
- `internal/handlers/app_controlplane.go` — `HSForUser`,
  `HSGlobal`, `InvalidateHSCache`, `InitHSForUserState`
- `internal/handlers/app_controlplane_test.go` — 7 tests
- `internal/handlers/admin_control_planes.go` — 5 new
  admin handlers (GET landing, GET user form, POST save,
  POST clear, POST test-plane)
- `internal/handlers/admin_control_planes_test.go` — 14
  tests (e2e with `newTestApp` + `authedReqFor` + `fakeDocker`
  patterns)
- `internal/handlers/templates/admin/control_planes.html` —
  landing
- `internal/handlers/templates/admin/user_control_plane.html` —
  per-user edit form

**Modified:**

- `internal/db/secrets.go` — added `EncryptForColumn` /
  `DecryptForColumn` + `ErrSecretKeyUnset` /
  `ErrSecretCiphertextCorrupt`
- `internal/db/node_owner_map_test.go` — `openNodeOwnerMapTestDB`
  now also creates `portal_users` with the v0.12.0 columns
- `internal/handlers/handlers_my_telegram_test.go` —
  `newMemoryDB` portal_users schema includes the v0.12.0
  columns
- `internal/handlers/handlers.go` — `App` gets `hs` (new
  unexported), `hsCache`, `hsCacheMu`, `SecretKeyHex`; the
  historical `HS` field is kept as a deprecated alias
- `internal/handlers/handlers_dashboard.go` —
  `computeTailnetMetrics` takes an explicit `*headscale.Client`
  so callers can route to the user's plane
- `internal/handlers/handlers_my_devices.go` — uses
  `HSForUser(c.UserID)` for `ListAllNodes`
- `internal/handlers/handlers_my_preauth.go` — uses
  `HSForUser(c.UserID)` for `CreatePreauthKey`
- `internal/handlers/handlers_my_keys.go` — uses
  `HSForUser(c.UserID)` for `ListAllNodes` + `ExpirePreauthKey`
- `internal/handlers/handlers_my_exit_nodes.go` — uses
  `HSForUser(c.UserID)` for `ListExitNodes`
- `internal/handlers/handlers_admin_nodes.go` — uses
  `HSGlobal()` explicitly for `ListAllNodes`, `TagNode`,
  `UntagNode`, `InvalidateCache`
- `internal/handlers/templates/admin/integrations.html` —
  adds a "Control planes" card linking to the new page
- `internal/i18n/catalog.go` — 22 new keys (`control_planes.*`)
  × 2 languages
- `internal/headscale/headscale.go` — `Client.ApiKeyForCache`
  getter (used by the cache-comparison path)
- `internal/config/config.go` — `SecretKeyHex` field
- `cmd/skygate/main.go` — wires `cfg.SecretKeyHex` into
  `app.SecretKeyHex`; adds 5 new admin routes
- `.env.example` — documents `SKYGATE_SECRET_KEY`

**No deploy script change.** The migration runs at next skygate
start; the schema is idempotent (CREATE IF NOT EXISTS + ALTER
with duplicate-column guards, the same pattern as the other
migrations).

## Tests

12/12 packages green, 35 new tests:

- `TestEncryptDecryptRoundtrip` — basic roundtrip
- `TestEncryptDifferentNoncePerCall` — fresh nonce per call
- `TestDecryptEmptyCiphertext` — empty stored value is no-op
- `TestDecryptBadBase64` — corrupt base64
- `TestDecryptTamperedCiphertext` — GCM auth fail
- `TestDecryptWrongKey` — wrong key
- `TestEncryptEmptyKey` — empty key
- `TestEncryptWrongKeyLength` — short key
- `TestSetGetUserHeadscaleConfig_Roundtrip` — DB roundtrip
- `TestGetUserHeadscaleConfig_NoOverride` — no override
- `TestGetUserHeadscaleConfig_WrongKey` — corrupted key
- `TestClearUserHeadscaleConfig` — clear
- `TestSetUserHeadscaleConfig_EmptyURLClears` — clear via set
- `TestSetUserHeadscaleConfig_URLWithoutKey` — config error
- `TestAllUsersHeadscaleConfig_ListsEveryone` — list
- `TestSummariseControlPlanes` — bucket logic
- `TestHSForUser_NoOverride_ReturnsGlobal` — default
- `TestHSForUser_WithOverride_ReturnsPerUser` — override
- `TestHSForUser_CachesClientByURL` — cache
- `TestHSForUser_InvalidatesOnKeyRotation` — rotation
- `TestHSForUser_CorruptCiphertext_FallsBackToGlobal` —
  fall-through
- `TestHSForUser_EmptySecretKey_FallsBackToGlobal` — no key
- `TestInvalidateHSCache_DropsAll` — clear cache
- `TestHSGlobal_SameInstance` — same instance
- `TestGetAdminControlPlanes_200ForAdmin` — landing
- `TestGetAdminControlPlanes_403ForNonAdmin` — admin only
- `TestGetAdminUserControlPlane_GET` — edit form
- `TestPostAdminUserControlPlane_SaveAndReflect` — save +
  encrypted in DB
- `TestPostAdminUserControlPlane_MissingSecret` — reject
  save without `SKYGATE_SECRET_KEY`
- `TestPostAdminUserControlPlane_Clear` — clear
- `TestPostAdminControlPlanesTest_GlobalPlaneOK` — test
- `TestPostAdminControlPlanesTest_PerUserRejected` —
  per-user test rejected
- `TestHSForUser_AfterAdminSave` — e2e
- `TestAdminRoutes_403ForNonAdmin` — 5 sub-tests

## VM verification

`make test` on the VM (`skyadmin@192.168.13.69`):

- 12/12 packages green
- smoke 118/118 PASS
- `SKYGATE_SECRET_KEY` added to `/home/skyadmin/skygate/.env`
  (32 bytes hex via `openssl rand -hex 32`)
- `/admin/control-planes` returns 200 with the new "Все
  плоскости" + "Per-user" sections
- `/admin/users/1/plane` returns 200 with the per-user
  edit form
- `/admin/integrations` returns 200 with the new
  "Control planes" card

## GitHub

https://github.com/BarsSky/skygate/releases/tag/v0.12.0

## Deferred (not in this release)

- **v0.12.1** — bot handlers (`/my_nodes`, `/admin_nodes` in
  the Telegram bot) still use the global `env.HS`. Per-user
  bot routing requires `BotEnv` to carry a `HeadscaleRouter`
  interface and a new dispatcher in `notify.go`. The bot
  still works for the common case (one Skygate = one
  control plane); multi-plane bot support is a small
  follow-up.
- **v0.13.0** — ACL import/export with dry-run preview +
  per-plane `GenerateACL()` (per the user's v0.12.0 scope
  selection).
- **v0.13.0+** — per-user admin pages (an admin whose job
  it is to manage users on a specific plane would benefit
  from a per-plane admin view; the current admin view is
  the global plane's eye view).
- **Indefinite** — Butler voice v3 (urgency marks), Personal
  API token rotation, more polish rounds.
