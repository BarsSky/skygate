# v0.28.5 — Android-friendly `via` opt-in + tagged-device exit-node fix

## What changed

Three releases in one:

### v0.28.5 (commit `206d26b`) — explicit opt-in for `via`

The per-user / per-device `via` constraint in `grants[]` (Tailscale 1.42+)
is implemented in the client, and older clients (notably Android 1.78
and earlier, and some Tailscale-fork clients) reject the entire policy
when they see a `via` they don't understand — blocking ALL exit-node
access for that device, not just the pinning. The operator reported on
2026-07-25 11:41 MSK that Android phones had completely lost exit-node
access after v0.28.3 deployed.

Fix: per-row `via_enabled` flag (default OFF). When OFF, the
per-user / per-device grant has `dst=autogroup:internet` but **no
`via`** — the user can pick any exit-node (Android-friendly). When
ON, the strict pinning is emitted as before.

Schema (migration v0.47):
```sql
ALTER TABLE user_exit_node_prefs   ADD COLUMN via_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE device_exit_node_prefs ADD COLUMN via_enabled INTEGER NOT NULL DEFAULT 0;
```

Backwards compat: existing rows from v0.28.1-v0.28.4 are backfilled to
`via_enabled=1` (preserves prior pinning). The operator has to
explicitly flip to 0 to un-pin. New rows default to 0 (safe side).

UI: lock (🔒) / unlock (🔓) icon next to the pinned tag in:
- `/my/exit-nodes` (self-service)
- `/my/devices` (per-device lock/unlock icon next to the pin)
- `/admin/users/{id}/subnet` (admin override)
- `/admin/devices` (per-device lock/unlock icon for admin)

3 new ACL tests: `PerUserViaSkippedWhenOptOut`,
`PerDeviceGrantSkippedWhenOptOut`, `BackwardsCompat_PerUserViaEnabled`.
6 new i18n keys × 2 langs (12 entries).

### v0.28.5a (commit `1346f7d`) — migration v0.47 idempotency fix

The migration v0.47 had a critical bug: the backfill UPDATE
(`UPDATE user_exit_node_prefs SET via_enabled = 1 WHERE via_enabled = 0`)
ran UNCONDITIONALLY on every skygate startup, clobbering any
operator-set `via_enabled=0` back to 1. The symptom: the "un-pin"
UI control was a no-op — every time the operator unchecked the box
and restarted skygate (or the reapply triggered a restart), the
migration would re-backfill the row.

Fix: track whether THIS run was the one that added the column
(freshlyAdded). The UPDATE only runs on the first-time migration.
On every subsequent startup, the column already exists → the ALTER
returns "duplicate column name" → UPDATE is skipped. Operator's
`via_enabled=0` survives restarts.

3 new tests pin the invariant:
- `TestMigrateV047_FirstRun_AddsColumnAndBackfills` — fresh install path
- `TestMigrateV047_SecondRun_PreservesViaZero` — the regression test
- `TestMigrateV047_FirstRun_BackfillsExistingRows` — backwards-compat

### v0.28.5b (commit `1872f06`) — tagged-device exit-node fix

Pre-v0.28.0 the per-user grant had `src="*"` (catch-all), which
matched any device including tagged ones. v0.28.0 removed the
catch-all for security (the operator's `workstation-3 → relay-3` bypass
issue) and switched to `src=user@`. In Tailscale v2 policy, the
source must match the device's identity directly: a tagged device's
source is the tag (`tag:dev-<user>-<device>`), not the user. The
per-user grant with `src=user@` does NOT match tagged devices via
tagOwners.

The result: every tagged device WITHOUT a per-device pref had NO
grant covering `autogroup:internet`, so exit-node routing was
silently rejected by the client. Symptom: 100% packet loss on
skygate-host-1 after v0.28.3, even with the per-user grant dst
including `autogroup:internet`. The Tailscale log showed
`open-conn-track: flow ... rejected due to acl` on every packet.

Fix: emit a "loose" per-device grant for every device tag the user
owns:
```json
{ "src": ["tag:dev-<user>-<device>"],
  "dst": ["autogroup:internet"],
  "ip":  ["*"] }
```
NO `via`. Emitted AFTER per-device rules (specific dst) and AFTER
the per-user grant, so it acts as the fallback for tagged devices
without a per-device pref. The per-device pref grant (with `via=`)
is emitted FIRST when `via_enabled=1` and has higher priority by
virtue of position in the grants[] list.

Order in `GenerateACLWithViaForPlane`:
1. Per-device pref grants (with `via=`, when `via_enabled=1`)
2. Per-user grants (with `via=` when enabled, else without)
3. Per-device rules (specific dst like h-rule-91-108-12-0-22)
4. **NEW**: loose per-device grants (one per device, NO `via`)
5. Catch-alls (`* → tag:public`, `* → tag:exit-node`, `tag:public → autogroup:internet`)

## Upgrade path

1. Pull the new code, restart skygate.
2. Migration v0.47a runs the FIRST time (after the pre-fix
   deployments): adds the `via_enabled` columns and backfills
   existing rows to `via_enabled=1`. Subsequent restarts are
   no-ops for the backfill — operator's un-pin survives.
3. Un-pin the per-device prefs in the UI (or via direct SQL
   `UPDATE device_exit_node_prefs SET via_enabled = 0 WHERE
   via_enabled = 1` for a global reset). The un-pin now persists
   across restarts.
4. Re-apply the ACL (`POST /admin/exit-rules/reapply`).
5. Test exit-node on each device. The policy should now allow
   autogroup:internet for every tagged device, regardless of
   whether they have a per-device pref.

## Operator verification

After deploying, verify exit-node works for a tagged device
(skygate-host-1 is a good test since it has no per-device pref):

```bash
docker exec -w /tmp skygate tailscale --socket=/var/run/tailscale/tailscaled.sock set --exit-node=relay-1
docker exec -w /tmp skygate ping -c 3 -W 2 8.8.8.8
# Expect: 3/3 received, 0% loss
```

If the Tailscale log shows `rejected due to acl` on the exit-node
packets, the v0.28.5b loose per-device grant isn't in the live
policy — re-apply and check that the per-device grants (tagged
device → autogroup:internet) are present at the top of the grants[]
list.

## Files

- `internal/acl/acl.go` — 3 sets of changes (v0.28.5, v0.28.4 already
  in place, v0.28.5b loose per-device grant)
- `internal/acl/acl_test.go` — 6 new tests (3 v0.28.5 + 3 v0.28.5b)
- `internal/db/migrations_v0.47.go` — idempotency fix
- `internal/db/migrations_v0.47_test.go` — 3 new tests
- `internal/db/db.go` — registered migration v0.47
- `internal/db/migrations_v0.45.go` — `ExitNodePref.ViaEnabled`
- `internal/db/migrations_v0.46.go` — `DeviceExitNodePref.ViaEnabled`
- `internal/handlers/admin_user_subnet.go` — via checkbox
- `internal/handlers/handlers_admin_nodes.go` — via icon
- `internal/handlers/handlers_device_exit_pref.go` — via param
- `internal/handlers/handlers_my_devices.go` — via icon
- `internal/handlers/handlers_my_exit_nodes.go` — via checkbox
- `internal/handlers/templates/admin/devices.html` — via icon
- `internal/handlers/templates/admin/user_subnet.html` — via checkbox
- `internal/handlers/templates/user/devices.html` — via icon
- `internal/handlers/templates/user/exit_nodes.html` — via checkbox
- `internal/i18n/catalog.go` — 6 new keys × 2 langs

## Live state (snapshot at 2026-07-25 16:00 MSK)

- skygate build: `dev+unknown` (binary built on VM, git label is
  "dev" because the binary path didn't have the `.git` directory
  available at build time — will be fixed in v0.28.5 tag)
- Live policy: 272 grants, 17 tagOwners, 2 ssh, 209 hosts
- DB: 3 per-user prefs (all `via_enabled=0`), 6 per-device prefs
  (workstation-3 and workstation-2 `via_enabled=1` per the operator's last choice,
  others `via_enabled=0`)
- Exit-node on skygate-host-1: WORKS (145ms latency to 8.8.8.8 via
  relay-1). Per-device grants emitted for all 11 tagged devices
  (a71, workstation-4, relay-1, relay-3, workstation-3, relay-2,
  workstation-2, workstation-2-1, skygate-subnet-admin, skygate-host-1,
  workstation-1).
- Operator: re-test workstation-3 (Windows) and Android (workstation-2).
  Expected: both can pick any exit-node in Tailscale client (no
  more `via` enforcement blocking the selection). workstation-3 should
  also use relay-3 (its `via_enabled=1` pref), workstation-2 should
  use relay-1 (its `via_enabled=1` pref).
