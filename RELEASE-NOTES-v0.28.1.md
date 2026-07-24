# Skygate v0.28.1 — per-user preferred exit-node (UI + data model)

Released 2026-07-24. Live on the operator's VM (192.0.2.1).
Tag: `v0.28.1`. Commit: `1981b27`.

## TL;DR

The v0.28.1 release ships the **data model + UI** for per-user
preferred exit-nodes. The `via`-field enforcement (the actual
ACL semantics that pin a user's traffic to one specific exit-node)
is **disabled by default** in this release — see the *Known
limitation* section below for the headscale 0.29.2 reason.

What you can do today (with the default settings):
- Run the `user_exit_node_prefs` migration (idempotent, runs on
  every `docker compose up -d --force-recreate skygate`).
- See a new **"Preferred exit-node (v0.28.1)"** card on
  `/admin/users/{id}/subnet` with a dropdown listing every
  headscale node that carries `tag:exit-node` (relay-1,
  relay-2, relay-3 today). Save → row in
  `user_exit_node_prefs` → re-apply ACL.
- See a new **"Preferred"** column on `/my/exit-nodes` with a
  "Set as my preferred" button per exit-node row, plus a
  "Clear" button at the top of the card. POST → same DB row,
  same ACL re-apply.

What you cannot do today (waiting on headscale):
- The `via: ["tag:exit-relay-1"]` field in `grants[]` is
  generated correctly by the new `GenerateACLWithViaForPlane`
  function, but **headscale 0.29.2's policy v2 parser rejects
  any grant that has a CIDR+port in `dst`**. This is a
  headscale v2-policy / `AliasEnc` parsing constraint, not a
  skygate bug. The fix is in headscale 0.30+ (per the headscale
  release notes). Until then, `SKYGATE_ACL_VIA_ENABLED=true`
  triggers a SetPolicy error — **do not flip the env var on
  headscale 0.29.2**.

## What changed

### 1. Migration v0.45 — `user_exit_node_prefs` table

```sql
CREATE TABLE user_exit_node_prefs (
    user_id INTEGER NOT NULL PRIMARY KEY,
    exit_node_tag TEXT NOT NULL,
    set_by_user_id INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
)
```

Idempotent (CREATE TABLE IF NOT EXISTS). One row per user
(UNIQUE on `user_id`). Cascade on user delete. The
`exit_node_tag` column is the headscale-friendly tag name
(`tag:exit-relay-1`, `tag:exit-relay-2`, `tag:exit-relay-3`)
— NOT the hostname. The v0.28.1 UI derives the tag from the
hostname automatically.

No backfill — the table is opt-in. Day-1 deploys have no rows,
which means no per-user `via` filter; the catch-all
`* → autogroup:internet:*` in `acls[]` keeps working as before
(users can use any available exit-node).

### 2. `GenerateACLWithViaForPlane` — the new policy builder

Reads `user_exit_node_prefs` and renders a `grants[]`-based
policy with `via: ["<user's preferred exit-node>"]` for each
user who has a row. The legacy `GenerateACLForPlane` (acls[]
output) is preserved; the dispatch is on the
`SKYGATE_ACL_VIA_ENABLED` env var (default `false`).

The new builder also emits the per-exit-node tagOwners entries
(`"tag:exit-relay-1": ["admin@..."]`) so headscale's parser
accepts the `via` references.

The function is wired into `ApplyACLPipelineForPlane` (one
extra `useVia bool` parameter) and `ApplyACLForAllPlanes` (same
flag). Existing call sites pass `false` (the legacy default).

### 3. UI — `/admin/users/{id}/subnet`

New "Preferred exit-node (v0.28.1)" card between the subnet
status card and the sharing card. Lists every headscale node
that carries `tag:exit-node` in a `<select>`. The selected tag
is submitted to `POST /admin/users/{id}/subnet/preferred-exit`,
which writes the row and re-applies the ACL. The currently
active preference is shown in green below the dropdown; a
"Currently active" line at the top explains the `via` semantics
(operator-eye-view).

The dropdown also includes a "no preference" option that
deletes the row (`exit_node_tag=''` → DELETE).

### 4. UI — `/my/exit-nodes`

New "Preferred" column on each exit-node row. The row whose
derived tag (`tag:exit-<hostname>`) matches the user's current
preference shows a green "✓ preferred" pill; every other row
shows a "Set as my preferred" button. POST goes to
`/my/exit-nodes/preferred` with the derived tag in a hidden
form field.

Above the table, when a preference is set, a green line shows
"Currently preferred: tag:exit-relay-1" plus a "Clear" button
(POST with `tag=''`).

### 5. Routes

```
POST /my/exit-nodes/preferred          (auth: any user)
POST /admin/users/{id}/subnet/preferred-exit  (auth: admin)
```

### 6. i18n — 16 new keys × 2 languages (32 entries)

- `exit_nodes.preferred_column` (table column header)
- `exit_nodes.preferred_set_button` (button on each row)
- `exit_nodes.preferred_currently` (✓ preferred pill)
- `exit_nodes.preferred_currently_help` (tooltip)
- `exit_nodes.preferred_currently_set` (banner above table)
- `exit_nodes.preferred_clear_button` / `preferred_clear_help`
- `exit_nodes.preferred_set_ok` (success flash message)
- `user_subnet.preferred_exit_title` (admin card title)
- `user_subnet.preferred_exit_help` (admin card help)
- `user_subnet.preferred_exit_label` (form label)
- `user_subnet.preferred_exit_none` (dropdown default)
- `user_subnet.preferred_exit_button` (save button)
- `user_subnet.preferred_exit_currently` (active status)
- `user_subnet.preferred_exit_default_help` (no-pref hint)
- `user_subnet.no_exit_nodes` (empty state)

### 7. Tests — 4 new tests pin the v0.28.1 invariants

- `TestGenerateACLWithVia_OutputUsesGrants` — output uses
  the `grants` key, not `acls`. Required for headscale 0.29+
  where the two are distinct policy sections.
- `TestGenerateACLWithVia_NoPreferencesWhenNoneSet` — no
  `via` field in the output when no row exists in
  `user_exit_node_prefs` (day-1 case).
- `TestGenerateACLWithVia_UserPrefTriggersViaAndTagOwners` —
  a row with `tag:exit-relay-1` adds `via: ["tag:exit-relay-1"]`
  to the per-user grant AND emits the matching tagOwners
  entry.
- `TestGenerateACLWithVia_PerExitNodeTagOwnersAreDistinct` —
  two users sharing the same preferred exit-node → one
  tagOwners entry, two `via` lines. De-dup by tag.

All 18 Go packages green (`go test ./...`). i18n parity
green. Bilingual smoke (`make test`) green: 83/83 EN + 83/83 RU.

### 8. Migration safety

- `migrations_v0.45.go` uses `CREATE TABLE IF NOT EXISTS` —
  re-running the migration is a no-op.
- `recoverOwnerUsernameFromPreauth` (added in v0.28.0) is
  unchanged. The new `user_exit_node_prefs` reads join
  `portal_users` (no migration to `node_owner_map`).

## Known limitation — `via` is not enforced on headscale 0.29.2

When the operator flips `SKYGATE_ACL_VIA_ENABLED=true` and
re-applies, the SetPolicy call returns HTTP 500 with:

```
"json: cannot unmarshal JSON string into Go v2.AliasEnc
 within \"/grants/0/dst/1\": invalid alias format:
 \"10.0.1.0/24:*\""
```

This is a **headscale v2-policy parser** limitation. The grants
section uses `AliasEnc` for `src` and `dst`, and `parseAlias`
checks `isPrefix()` / `isUser()` / `isGroup()` / `isTag()` /
`isAutoGroup()` / `isHost()` in that order — but **the entire
string must pass exactly one of those checks**, and
`10.0.1.0/24:*` doesn't because the `:*` port suffix isn't
allowed on a Prefix. The `acls[]` section is more lenient
because it has its own parser that knows about CIDR+port.

Two workarounds exist, neither is in v0.28.1:
- **A.** Use a `hosts:` block to define each CIDR as a named
  alias, then reference the alias in the grant's `dst`. This
  is the v0.28.2 path if we want to land the `via` enforcement
  without a headscale upgrade.
- **B.** Wait for headscale 0.30+ to fix the parser. The
  weekly `headscale-milestone-16-check` cron will alert when
  a relevant headscale release ships.

In the meantime, the v0.28.1 deploy is **safe** because
`SKYGATE_ACL_VIA_ENABLED` defaults to `false` and the legacy
`acls[]` policy is what actually reaches headscale. The
`user_exit_node_prefs` rows that the UI saves are inert
metadata; the `via` semantics is wired but inactive.

## How to verify on a live deployment

```bash
# 1. Migration applied (user_exit_node_prefs table exists)
docker cp skygate:/data/skygate.db /tmp/check.db
sqlite3 /tmp/check.db ".schema user_exit_node_prefs"

# 2. /admin/users/1/subnet shows the new card
curl -sS -c /tmp/ck -b /tmp/ck -X POST \
  --data-urlencode "username=admin" \
  --data-urlencode "password=$PASS" \
  http://localhost:8080/login -o /dev/null
curl -sS -c /tmp/ck -b /tmp/ck http://localhost:8080/admin/users/1/subnet \
  | grep -oE 'preferred_exit_title|preferred_exit_label' | sort -u
# expect: both keys present

# 3. /my/exit-nodes shows the Preferred column
curl -sS -c /tmp/ck -b /tmp/ck http://localhost:8080/my/exit-nodes \
  | grep -oE 'tag:exit-relay-1|tag:exit-relay-2|tag:exit-relay-3' | sort -u
# expect: all three (one per exit-node row, in the hidden
# form field of the "Set as my preferred" button)

# 4. /healthz / /readyz still green
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

## Live verification (this release)

- `docker logs skygate | grep version=` →
  `v0.28.0-1-g1981b27+1981b27` (built 2026-07-24T21:38:31Z)
- `/healthz={"status":"ok"}`, `/readyz={"healthy":true,"db":"ok","headscale":"ok"}`
- `make test` (EN) → 83 pass, 0 fail
- All 21 admin/user pages return 200
- `headscale policy get` → 252 ACL rules (legacy acls[]) — the
  `via` grants are NOT active because SKYGATE_ACL_VIA_ENABLED=false

## What's next

- **v0.28.2** (when headscale 0.30+ lands OR we ship the `hosts:`
  workaround): flip `SKYGATE_ACL_VIA_ENABLED=true`, add per-device
  preferred exit-nodes (extend `user_exit_node_prefs` to
  `(user_id, device_id) UNIQUE` keyed on the per-device tag
  the v0.28.0 backfill already provides). Per-device `via` is
  one extra line in the grants loop.
- **v0.28.3**: cleanup the 21 legacy `device_ip` rules that
  pre-date v0.28.0 — the `node_owner_map` join is now
  comprehensive enough to backfill them.
- **v0.29.0**: Provisioning UI (DERP / storage config) per the
  v0.28.0 plan document.
