# v0.22.3 — Subnet status reflects device ownership (not subnet-router)

**Tag**: `v0.22.3`
**Date**: 2026-07-21
**Previous**: [v0.22.2](RELEASE-NOTES-v0.22.2.md) (MSI tag:private auto-apply fix)

The "why is my subnet `pending`?" release. After v0.16.6 auto-allocated
`10.0.<uid>.0/24` to every user, the status stayed `pending` because the
`tag:subnet-router` machinery in v0.16.7 only fires when the user has
actually deployed a sidecar — which most users don't (and don't need to).
v0.22.3 flips the semantics: status now reflects **device ownership**,
not **subnet-router deployment**.

## What changed

### Status semantics

| Old (≤v0.22.2)               | New (v0.22.3)                                       |
|------------------------------|-----------------------------------------------------|
| `pending` = no subnet-router | `pending` = **no devices** in the tailnet           |
| `active` = subnet-router up  | `active` = **user has ≥1 device** (logical namespace) |
| `disabled` = admin override  | `router_active` = bonus: devices + subnet-router up |
|                              | `disabled` = admin override (unchanged)             |

The 4th status `router_active` is new in v0.22.3 — same green pill as
`active`, but with a tower-broadcast icon and a "this is a real routable
subnet" tooltip.

For the 4 production users (skyadmin, michail, guest, daniil) this means
their subnets immediately flip to `active` on the next `/my/devices`
load. The user's Tailscale devices already work today via `100.64.0.0/10`
(they always have), and the `10.0.<uid>.0/24` now correctly shows as
"active logical namespace" instead of "broken pending state".

### `subnet.SyncStatus(db, userID, hasRouter)` — new function

`internal/subnet/manager.go` gains a `SyncStatus` helper that
encapsulates the new status logic. It reads `node_owner_map` for the
user's device count, checks the `hasRouter` flag from the caller, and
updates `user_subnets.status` + `portal_users.subnet_status` if the
computed value differs from the current. Idempotent.

Called from `backfillNodeOwnership` after every `/my/devices` load, so
the status stays in sync with the actual headscale state.

### UI changes

- **`/admin/users/{id}/subnet`**: bare `<code>{{.Subnet.Status}}</code>`
  replaced with a colored pill (green active / green router_active /
  yellow pending / muted disabled) + tooltips + v0.22.3 explainer card.
- **`/admin/users`** subnet column: new `router_active` case before
  `active` (so the icon shows up first when both would match).
- **`/my/devices`**: new "Your personal subnet" card at the top with
  CIDR + status pill + short help text. Reads from
  `portal_users.subnet_cidr` / `subnet_status` denorm columns (no JOIN).

### Sidecar semantics — unchanged

The v0.16.7 sidecar (`internal/sidecar/manager.go`) still does its
existing thing: scan `tag:subnet-router` nodes, approve routes, flip
status to `active`. v0.22.3 just adds a new "if you also have a router
up, your status is `router_active`" path on top. The sidecar code
itself is untouched.

## New constants

```go
// internal/subnet/manager.go
StatusPending      = "pending"       // unchanged
StatusActive       = "active"        // unchanged (but re-defined semantics)
StatusRouterActive = "router_active" // NEW in v0.22.3
StatusDisabled     = "disabled"      // unchanged
```

`SetStatus` accepts all 4 values. Backwards-compatible — pre-v0.22.3
callers passing `active` still work, the new `router_active` value
needs an explicit opt-in.

## Tests

7 new unit tests in `internal/subnet/manager_test.go`:
- `TestSyncStatus_PendingWhenNoDevices` — the headline contract
- `TestSyncStatus_ActiveWhenDevicesExist` — devices present, no router
- `TestSyncStatus_RouterActiveWhenHasRouter` — bonus status path
- `TestSyncStatus_DisabledPreserved` — manual override wins
- `TestSyncStatus_NoSubnetRow` — ErrNotFound on user without row
- `TestSyncStatus_Idempotent` — re-run with same input doesn't UPDATE
- `TestSetStatusAcceptsRouterActive` — `router_active` is a valid
  status string for `SetStatus`

All 7 pass locally. `TestCatalogsParity` +
`TestTemplateArgsMatchCatalog` + `TestLoadTemplates` all green.

## Files changed

| File | Lines | What |
|---|---|---|
| `internal/subnet/manager.go` | +90/-10 | `StatusRouterActive` const + `SyncStatus` fn + `SetStatus` switch update |
| `internal/subnet/manager_test.go` | +190/-1 | 7 new tests + 2 helpers (`seedNodeOwnerMap`, `mustReadUpdatedAt`) |
| `internal/handlers/handlers_node_ownership.go` | +40/-1 | `hasRouter` tracking + `subnet.SyncStatus` call after backfill loop |
| `internal/handlers/handlers_my_devices.go` | +15/-1 | Read `subnet_cidr`/`subnet_status` denorm, pass to template |
| `internal/handlers/templates/admin/users.html` | +1 | New `router_active` case (icon + tooltip) |
| `internal/handlers/templates/admin/user_subnet.html` | +25/-5 | Colored pill instead of bare `<code>`, plus v0.22.3 explainer |
| `internal/handlers/templates/user/devices.html` | +30 | New "Your personal subnet" card |
| `internal/i18n/catalog.go` | +14 | 7 new keys × 2 langs = 14 entries |

**Total**: 8 files, +405/-18 lines, 7 new tests.

## Live verification plan

After deploy to VM:

1. **smoke 83/83** (EN + RU) still green — no behavior change for smoke
2. **`/my/devices` shows new card** for skyadmin/michail — CIDR +
   `active` pill
3. **`/admin/users` subnet column** for skyadmin/michail flips from
   `pending` (yellow) to `active` (green) on the first /my/devices
   load (admin sees it via /admin/users immediately)
4. **`/admin/users/{id}/subnet`** for skyadmin: status pill + v0.22.3
   explainer card visible
5. **`guest` (0 devices) stays `pending`** — confirms the "0 devices
   → pending" rule works (no regression to "all users active")
6. **Guest gets a device** via skygate preauth → on next /my/devices
   load the status flips to `active` (live transition test)

A `check_v0.22.3.sh` script will encode these 6 checks and run on
the real headscale.

## What's next

**v0.22.4 — nothing planned.** v0.22.3 closes the immediate
"why pending" question. The next major work is the per-user headscale
phased plan (v0.23.0+):

- **v0.23.0** (Phase 1) — Infrastructure + skyadmin pilot:
  bootstrap script + provisioning Go API + first user migrated
  to their own headscale container
- **v0.23.1** (Phase 2) — Migrate all 4 prod users to per-plane
- **v0.23.2** (Phase 3) — Cross-plane coordination via shared
  subnet-router per user (Option C from the 2026-07-21 design
  discussion)
- **v0.23.3** (Phase 4) — Polish: per-plane backup, monitoring,
  admin UI

See `AGENTS.md` "What we're working on next" section for full context.
