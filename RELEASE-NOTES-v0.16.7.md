# v0.16.7 — per-user subnet sidecar (auto-approver + preauth)

2026-07-17

Real sidecar provisioning for the per-user subnet feature.
v0.16.6 shipped the data model (`user_subnets` table +
3 denorm columns on `portal_users`), v0.16.8 added the
UI navigation, v0.16.9 fixed the sidebar — v0.16.7 ships
the actual runtime that turns the schema into a working
subnet.

## What changed

### 1. `internal/sidecar/` — the Manager package (new)

  - `Manager.GeneratePreauth(userID)` — issues a
    per-user preauth key (tag:subnet-router, 1h TTL,
    single-use) via headscale.
  - `Manager.SyncOnce()` — scans headscale for
    tag:subnet-router nodes, auto-approves the user's
    CIDR route when the sidecar advertises it, flips
    `user_subnets.status` to active, and disables
    rows whose node disappeared (last_seen > 5 min).
  - `Manager.Run(ctx)` — ticker loop (default 30s,
    configurable via `SKYGATE_SIDECAR_SYNC_PERIOD`).
    Launched as a goroutine by `cmd/skygate/main.go`.
  - `Manager.LastSync()` / `LastStats()` — exposed for
    the admin UI to show "last sync: approved 1,
    disabled 0".
  - 11 unit tests with httptest fake headscale:
    hostname parsing, containsCIDR, hasTag, full
    sync cycle (4 sub-cases — pending, active,
    stale-disabled, noop), GeneratePreauth success
    + error, BuildPreauthInfo, listAllSubnets,
    distinctPlaneURLs.

### 2. `internal/headscale/preauth.go` — tagged preauth keys

  - `CreatePreauthKeyWithTags(userID, expiration,
    reusable, tags)` — like `CreatePreauthKey` but
    also tags the key (e.g. `tag:subnet-router`).
    API body now includes `tags: [...]`; CLI path
    appends `--tags tag1,tag2`. headscale 0.23+
    requires the flag for tag-restricted preauth.
  - The old `CreatePreauthKey` now delegates to the
    tagged version with `tags=nil` for backward compat.

### 3. `internal/headscale/nodes.go` — ApprovedRoutes

  - Added `ApprovedRoutes []string` to `HSNode` and
    `NodeView` (the API returns both `availableRoutes`
    — what the node wants — and `approvedRoutes` —
    what headscale has approved). Used by the
    auto-approver to detect "node registered with
    tag:subnet-router, route is now approved" and
    flip status to active.

### 4. Admin UI — `/admin/users/{id}/subnet` Provision

  - `POST /admin/users/{id}/subnet/provision` issues
    a preauth key + renders it in a flash card with
    the suggested `tailscale up` command (Linux /
    macOS / Docker variant).
  - 8 new i18n keys (RU+EN): `provision_button`,
    `preauth_title`, `preauth_help`, `preauth_key`,
    `preauth_hostname`, `preauth_routes`,
    `preauth_expires`, `preauth_note`.
  - 1 new admin handler test.

### 5. Bot — `/mysubnet provision`

  - Issues the same preauth key + replies with the
    key + command in a butler-gated chat reply.
  - 9 new bot i18n keys (RU+EN): `provision_header`,
    `provision_key_label`, etc.
  - 2 new bot tests: success path + no-manager hint.

### 6. Configuration

  - `SKYGATE_SIDECAR_SYNC_PERIOD` (default `30s`):
    how often the auto-approver polls headscale. Set
    to `0` to disable the auto-approver (preauth
    issuance on `/admin/users/{id}/subnet` still
    works; admin has to approve routes manually via
    `headscale nodes approve-routes` CLI).

## Architecture

The `sidecar.Manager` is shared between three callers:

  - `cmd/skygate/main.go`: starts the auto-approver
    goroutine on app boot.
  - `internal/handlers/admin_user_subnet.go`:
    `PostAdminUserSubnetProvision` calls
    `GeneratePreauth`.
  - `internal/telegram/notify.go`: hands the same
    manager to the bot's `BotEnv` via `SetSidecar()`
    so `mySubnetProvisionReply` can call it.

The full flow:

  1. Admin clicks "Issue preauth key" on
     `/admin/users/1/subnet` (or user runs
     `/mysubnet provision` in chat).
  2. Skygate calls headscale's `/api/v1/preauthkey`
     with `tags: ["tag:subnet-router"]` + 1h expiry.
  3. The user pastes the key into
     `sudo tailscale up --authkey=...` on their
     sidecar host. The tailscaled registers as a
     node with `tag:subnet-router`.
  4. Within ~30s, the auto-approver scans headscale,
     sees the new node, calls
     `ApproveAllRoutesWithList(hostname, [10.0.1.0/24])`,
     then flips `user_subnets.status` to `active`
     and sets `router_node_id`.
  5. `/admin/users/1/subnet` and `/mysubnet` both
     show `status=active`, `router_node_id=42`.
  6. If the sidecar disappears, the next sync cycle
     flips status back to `disabled` (no manual
     cleanup needed).

## Regression-prevention note

The first v0.16.7 deploy had `sidecarMgr.Run(ctx)`
called inline (not in a goroutine), which blocked
main() before the HTTP server could bind, so the
process was up but unreachable. The auto-approver
was the only thing still running. Fix: `go
sidecarMgr.Run(ctx)`. Plus
`HSForUser(userID=0)` short-circuit in
`app_controlplane.go` to avoid log spam every 30s
for the global-plane sentinel.

## Tests

  - 12/12 packages green
  - 11 new sidecar tests
  - 1 new admin handler test
  - 2 new bot tests
  - go vet ./... clean
  - go build ./cmd/skygate clean

## Manual verification on VM

  - `/admin/users/1/subnet` shows "Issue preauth
    key" button.
  - Clicking it issues a preauth key (key_id visible
    in the body) + suggested hostname
    `skygate-subnet-skyadmin` + suggested routes
    `10.0.1.0/24` + ready-to-paste `tailscale up`
    command.
  - Smoke 118/118.

Deployed to VM, live at build `ac73b8c`.
