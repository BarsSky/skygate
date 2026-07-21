# v0.23.0 — One-click per-user headscale provisioning (Phase 1)

**Tag**: `v0.23.0`
**Date**: 2026-07-21
**Previous**: [v0.22.3](RELEASE-NOTES-v0.22.3.md) (subnet status reflects device ownership)

The "operator can finally provision a per-user headscale in one
click" release. Closes the v0.12.0 capability gap that left per-user
control planes as a manual ssh + docker + headscale CLI flow.

## What changed

### The operator flow before v0.23.0

To give a user a per-user headscale, the operator had to:
1. ssh to the VM
2. set up a headscale container (docker compose, config file,
   noise key generation, port allocation)
3. create a user in headscale via `docker exec ... headscale users create ...`
4. issue an API key via `docker exec ... headscale apikeys create ...`
5. copy the URL + key into `/admin/users/{id}/plane` form
6. click Save
7. verify the form pre-fills the right values
8. (optionally) test connectivity

### The operator flow after v0.23.0

To give a user a per-user headscale, the operator:
1. opens `/admin/users/{id}/plane`
2. clicks **"Provision per-user headscale"**
3. waits ~15s
4. sees a green "Provisioned: container headscale-skyadmin is
   listening on http://headscale-skyadmin:50451..." flash

The whole thing is one click. The decom path is also one click
("Decommission") and preserves the per-user data directory
(moved to `.decommissioned-<ts>`) for recovery.

## Architecture

The per-user headscale is a separate Docker container on the
same `headscale_default` network as the global one. Inside the
network, skygate talks to it via the container name
(`http://headscale-<username>:<port>`). Each user gets:

- **their own headscale container** (`headscale-<username>`)
- **their own sqlite DB** (bind-mounted to
  `/home/skyadmin/headscale/users/<username>/data/`)
- **their own config** (bind-mounted to
  `/home/skyadmin/headscale/users/<username>/config/`)
- **their own headscale user** (created with the same username
  as the skygate portal user — Phase 1 keeps the names
  aligned for simplicity; Phase 2 may add a naming convention
  like `portal-<username>` to avoid clashes if a user exists
  in the global headscale already)
- **their own 10-year API key** (encrypted with SKYGATE_SECRET_KEY,
  stored in `portal_users.headscale_api_key_enc`)

### Port allocation

- HTTP API:  `50450 + (uid % 50)` — range 50450..50499
- gRPC API:  HTTP + 1000 — range 51450..51499
- metrics:   HTTP + 2000 — range 52450..52499

Per-user instances are NOT exposed on the host's public
network — only on the docker network. Skygate's per-user
client talks to them via the docker DNS name. This is
deliberate: per-user headscales are for skygate→headscale
API calls, not for public Tailscale client registration
(which still goes through the global headscale).

### Script-side details

`deploy/headscale-users/headscale-bootstrap.sh` does the heavy
lifting (~270 lines). It:
1. validates the username (lowercase + digits + _ -)
2. refuses to clobber an existing container or override file
3. creates the per-user directory tree
4. generates a `config.yaml` with the per-user base_domain
   (`<username>.tsnet.skynas.ru`) and MagicDNS enabled
5. generates a `docker-compose.user-<username>.yml` override
   that joins the existing `headscale_default` network
6. starts the container via
   `docker compose -f docker-compose.yml -f docker-compose.user-<username>.yml up -d`
7. waits for the gRPC + HTTP listeners to be ready (30s
   timeout, healthcheck-driven loop)
8. creates the headscale user (idempotent: `users create`
   returns non-zero on "already exists", we ignore that)
9. issues a 10-year API key
10. outputs JSON with `{username, container, url, api_key, ports, headscale_user_id}`

`deploy/headscale-users/headscale-deprovision.sh` is the
reverse (~75 lines). It:
1. stops + removes the container
2. removes the docker-compose override
3. MOVES the per-user directory to
   `/home/skyadmin/headscale/.decommissioned-<username>-<ts>/`
   (the operator can manually recover / inspect / re-bootstrap
   from the preserved copy)
4. outputs a single "OK: deprovisioned ..." line

Both scripts are pure bash + docker + headscale CLI. No
Python, no Go runtime needed on the host. They are
intentionally transparent — every command is visible in
the script so the operator can debug by reading the source.

### Go-side details

`internal/headscale/provision.go` (~210 lines) is a thin shell
wrapper. Key design points:
- always goes through `bash` explicitly (not direct `exec`)
  so the same code works on Linux production AND on Windows
  dev (git-bash / WSL). On Windows we translate
  `C:\foo\bar` → `/mnt/c/foo/bar` because git-bash mounts
  Windows drives under /mnt.
- `parseBootstrapJSON` finds the FIRST `{` in the script's
  output and parses from there, ignoring any docker
  progress messages that might precede the JSON line.
- validates required fields (`url`, `api_key`, `container`)
  so a half-configured per-user doesn't accidentally get
  persisted (the caller's `db.SetUserHeadscaleConfig`
  would otherwise store an empty row).
- `IsProvisioned(username)` is a cheap `docker ps -a` lookup
  — no headscale round-trip. The /admin/users/{id}/plane
  page uses it to decide which card (Provision vs
  Decommission) to render.

The HTTP handler in `internal/handlers/admin_control_planes.go`
(~165 lines) does:
1. POST → `headscale.ProvisionUser(username, uid)`
2. POST → `db.SetUserHeadscaleConfig(d, uid, url, apiKey, keyHex)`
3. POST → `a.InvalidateHSCache(url)`
4. POST → `a.audit(...)` with action
   `user_provision_headscale.ok`
5. POST → redirect with a green flash

The decom handler reverses the steps in opposite order
(clear DB → invalidate cache → audit). Audit log entries
have `.start` / `.ok` / `.fail` suffixes for traceability
on the rare case the script fails partway through.

## What's NOT in v0.23.0 (future phases)

Phase 1 is **infrastructure only**. No data migration. skyadmin
still uses the global headscale for everything; the per-user
headscale exists, has skyadmin as a user, has the API key
stored encrypted — but **no actual nodes have been migrated to
it**. `HSForUser(1)` now returns the per-user client (the
client exists, the URL is in the DB), but the next time
skyadmin calls `HSForUser(1).ListAllNodes()`, it'll see an
empty list because no nodes are registered in the per-user
headscale.

The three follow-up phases from the 2026-07-21 plan:

- **v0.23.1 (Phase 2)** — migrate all 4 prod users to per-plane
  (michail, guest, daniil in addition to skyadmin). Two
  options: re-register (user re-auths with a per-plane
  preauth), or `node_owner_map.headscale_user_id` rewrite +
  node state import (more complex, less invasive).
- **v0.23.2 (Phase 3)** — cross-plane coordination via
  per-user `subnet-router` in a shared `headscale-shared`
  container. This is what makes `tag:exit-node` continue
  working when users are on different planes.
- **v0.23.3 (Phase 4)** — per-plane backup, monitoring, admin
  UI polish. Decommission is currently one click but doesn't
  auto-backup the per-user DB first (it's just moved
  aside); a polish release would auto-snapshot before
  teardown.

## Tests

8 new unit tests in `internal/headscale/provision_test.go`:
- `TestProvisionUser_ParsesValidJSON` — happy path
- `TestProvisionUser_StripsPreJSONOutput` — docker compose
  noise stripped
- `TestProvisionUser_ScriptFails` — exit code + stderr
  surface in the error message
- `TestProvisionUser_MalformedJSON` — clear "no JSON
  found" error rather than silent zero result
- `TestProvisionUser_MissingRequiredFields` — defensive
  against half-configured script output
- `TestProvisionUser_EmptyUsername` — defensive guard
- `TestProvisionUser_InvalidUID` — defensive guard
- `TestProvisionResult_JSONRoundTrip` — JSON tags stable
  (catches accidental field renames)

All 8 PASS on Windows (git-bash) and on Linux. The tests
use `t.TempDir()` + a tiny shell-script fixture so they
don't depend on docker or a real headscale container.

`TestCatalogsParity` (i18n) and `TestTemplateArgsMatchCatalog`
(templates) all green. `go vet` clean. `go test ./...` all
green. Smoke 83/83 (RU + EN) still green.

## Files changed

| File | Lines | What |
|---|---|---|
| `deploy/headscale-users/headscale-bootstrap.sh` | +273 new | Per-user container creation (config, compose override, user, API key) |
| `deploy/headscale-users/headscale-deprovision.sh` | +74 new | Reverse: tear down + preserve data dir |
| `internal/headscale/provision.go` | +210 new | Go wrapper (shell-out, JSON parse, Windows compat) |
| `internal/headscale/provision_test.go` | +263 new | 8 unit tests |
| `internal/handlers/admin_control_planes.go` | +165 | PostAdminUserControlPlaneProvision + Decommission handlers |
| `internal/handlers/templates/admin/user_control_plane.html` | +24 | Provision / Decommission card with confirm dialogs |
| `internal/i18n/catalog.go` | +32 | 8 new i18n keys × 2 langs (RU+EN) |
| `cmd/skygate/main.go` | +5 | Two new routes: `/admin/users/{id}/plane/{provision,decommission}` |
| `docker-compose.yml` | +6 | Mounts bootstrap/deprovision scripts + headscale dir into skygate container |
| `check_v0.23.0.sh` + `run_check_v0.23.0.sh` | +273 | 11-step live verification |
| `encrypt_and_write.sh` | +97 | One-off: simulate the handler's DB write path (used to populate the form for visual verification) |

**Total**: 11 files, +1422 lines (1058 code + 273 live verify + 91 one-off).

## Live verification

`check_v0.23.0.sh` (11 steps, all PASS):
1. login as skyadmin
2. /admin/users/1/plane shows the per-user URL
3. Decommission card visible (user is already provisioned)
4. Provision card correctly hidden
5. /admin/control-planes landing still works
6. /admin/control-planes lists the per-user plane URL
7. DB: portal_users.headscale_url = 'http://headscale-skyadmin:50451'
8. DB: portal_users.headscale_api_key_enc is set (length: 68 chars, encrypted)
9. per-user headscale container is up + healthy (docker healthcheck green)
10. per-user headscale has user 'skyadmin' in its DB
11. form pre-fills the per-user URL correctly + i18n keys present

## What's next

v0.23.1 (Phase 2): migrate the 4 prod users to per-plane. Needs
the data migration strategy decision (re-register vs DB
rewrite). Will start as soon as the operator gives the go-ahead.
