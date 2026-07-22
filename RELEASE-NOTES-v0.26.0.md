# v0.26.0 — end-to-end subnet-router pilot + HA-ready probes

2026-07-22

This is a **"close the loose ends before HA"** release. Two
distinct things in one:

1. **Real end-to-end proof that the per-user subnet-router
   flow works** — not just unit tests + the sidecar's
   `SyncOnce` approving routes, but the full
   admin-downloads-bundle → user-runs-setup.sh →
   tailscale-registers → sidecar-auto-approves →
   /my/devices-loads → status-pill-flips-to-router_active
   pipeline, verified live on the operator's VM (skyadmin
   pilot, node id=26, 2026-07-22).

2. **Two bugs the pilot caught** — both tag-preservation
   issues that silently broke the v0.24.x subnet-router
   flow:
   - `TagNode` is destructive (headscale 0.29's `nodes tag
     --force` REPLACES the tag set, not appends). The
     backfill was clobbering `tag:subnet-router` →
     `tag:private` on every `/my/devices` load.
   - The sidecar's `SyncOnce` was setting
     `status='active'` (the pre-v0.22.3 value) instead of
     `status='router_active'`, flickering the status pill
     every 30s.

3. **HA-ready health probes** — `GET /healthz` (always
   200) + `GET /readyz` (DB + headscale ping, 1s cache,
   200 or 503). The hooks are in place so a future
   Tier-1 hot-standby deploy is a 1-day operation, not
   a 1-week refactor. No actual HA infrastructure yet
   (no PostgreSQL, no replica, no failover); just the
   probes + the `SKYGATE_INSTANCE_ID` plumbing so
   multi-VM operators can see which instance answered.

## What changed

### 1. End-to-end subnet-router pilot — e2e_pilot.sh

A new `e2e_pilot.sh` (root, ~165 lines) that automates
the full flow against a live VM:

1. Logs in as `skyadmin`, downloads the bundle from
   `/admin/users/1/subnet/download` (HTTP 200, ~4.5KB tar.gz).
2. Extracts the preauth key from `commands.txt`.
3. Pulls `tailscale/tailscale:latest` and starts a
   sidecar container with `--cap-add=NET_ADMIN
   --device /dev/net/tun --network=host`, env vars
   `TS_AUTHKEY`, `TS_LOGIN_SERVER=https://head.skynas.ru`,
   `TS_HOSTNAME=skygate-subnet-skyadmin`.
4. Waits for the new node to register in headscale
   (typically 5-10s).
5. Inspects the node: `tags: [tag:subnet-router]`,
   `available_routes: [10.0.1.0/24]`, `subnet_routes:
   [10.0.1.0/24]`, `online: true`.
6. Waits up to 60s for `sidecar.SyncOnce` to auto-approve
   the route.
7. Confirms the sidecar log line
   `sidecar: approved 10.0.1.0/24 on node skygate-subnet-skyadmin (user=1)`.
8. Triggers `/my/devices` and confirms the DB flips
   `user_subnets.status` from `active` → `router_active`
   (and the denorm `portal_users.subnet_status` matches).

**Live result (2026-07-22, skyadmin pilot on
`192.168.13.69`):** node id=26 registered with
`tag:subnet-router`, route approved in 21s, status
flipped to `router_active` and stayed there across
multiple `SyncOnce` ticks (verified: `T+0` and
`T+35s` both report `router_active`).

### 2. Two subnet-router tag/status fixes

#### 2a. `headscale.AddTag` (non-destructive add)

The pre-v0.26.0 flow was:

```
backfillNodeOwnership → matchedTag = "tag:private" →
HS.TagNode(nodeID, "tag:private")
```

But headscale 0.29's `headscale nodes tag --force`
**REPLACES** the entire tag set, it doesn't append.
The net effect: a node that registered with
`tag:subnet-router` (via the sidecar's preauth key) got
its tag silently wiped to `[tag:private]` on the next
`/my/devices` load. The route stayed approved but the
ACL rule `user → tag:subnet-router → user_subnet:*`
stopped applying because the node was no longer
`tag:subnet-router`.

**Fix** in `internal/headscale/tags.go`:

- New `Client.AddTag(nodeID, want)`: reads the current
  tag set via `ListAllNodes`, appends `want` if not
  already there, and writes the union via `TagNode`.
  No-op if `want` is already present.
- Documented `TagNode` as destructive in its
  godoc (the previous comment said it "sets tags",
  which is technically true but operationally
  misleading).
- `handlers_node_ownership.go` Strategy C now respects
  existing tags: if the node already has any tag
  (e.g. `tag:subnet-router` from a preauth), use
  `firstTagOrFallback(n)` like Strategy A does,
  instead of unconditionally setting
  `matchedTag='tag:private'`.
- The `TagNode(..., "tag:private")` post-match call
  is now `AddTag(..., "tag:private")` so even if
  Strategy C does set `matchedTag='tag:private'`
  for a tagless node, the existing `tag:subnet-router`
  is preserved (we append `tag:private`, don't replace).

#### 2b. Sidecar `SyncOnce` sets `router_active` (not `active`)

Pre-v0.22.3, "the route is approved ⇒ status is `active`"
was the binary status. v0.22.3 split that into
`active` (no router) vs `router_active` (router up).
The sidecar's `activateIfRoutesApproved` was never
updated, so every `SyncOnce` tick silently clobbered
the `router_active` that `backfillNodeOwnership` had
just set, reverting the status pill to `active`. The
pill would flicker `router_active` → `active` →
`router_active` → `active` on a 30s cycle.

**Fix** in `internal/sidecar/manager.go`:

```go
if containsCIDR(node.ApprovedRoutes, sub.CIDR) {
    if sub.Status != subnet.StatusRouterActive &&
       sub.Status != subnet.StatusDisabled {
        if err := subnet.SetStatus(m.DB, userID, subnet.StatusRouterActive); err != nil {
            return err
        }
    }
}
```

The manual `disabled` override is preserved. The
sidecar's logic now matches the v0.22.3 semantics
exactly: a tag:subnet-router node with an approved
route IS a live subnet-router → `router_active`.

The matching unit test
`TestSyncOnce_FlipsToActiveWhenRouteApproved` was
renamed to `...RouterActive` and updated to assert
`StatusRouterActive` (it was silently passing for
~1 year with the v0.16.7 binary value — the e2e
pilot was the first to catch the regression in
production).

### 3. Health probes (HA-ready)

```
GET /healthz    → 200 OK    always (process is up)
                 {build, instance_id, status, timestamp}

GET /readyz     → 200 OK    process is up + DB pingable + headscale pingable
                 {healthy, db, headscale, checks, build, instance_id, uptime_sec, timestamp}
                 1s result cache (so 100ms Prometheus scrapes don't DDoS the DB)
GET /readyz     → 503       any check failed
                 {healthy: false, db: "fail: ...", ...}
```

Implementation:

- `internal/headscale/healthz.go` — `Pingable`
  interface + `Client.PingContext` (HEADs
  `/api/v1/node` — 2xx AND 4xx both mean "reachable"
  since 4xx = auth issue, not network issue; only 5xx
  is a real problem).
- `internal/handlers/handlers_healthz.go` —
  `GetHealthz` (200 always) + `GetReadyz`
  (DB+headscale checks, 1s TTL cache, 200/503).
- `internal/handlers/handlers.go` — `App.InstanceID/
  BuildVersion/StartedAt` fields.
- `cmd/skygate/main.go` — `mux.HandleFunc("GET
  /healthz", ...)`, `mux.HandleFunc("GET /readyz",
  ...)`, `app.BuildVersion = version + "+" + commit`.
- `SKYGATE_INSTANCE_ID` env var (defaults to
  `"unconfigured"`) — multi-VM operators see which
  instance answered the probe.

Useful for:

- `curl localhost:8080/healthz` from cron every 5 min
  to catch a dead skygate.
- `curl -fsS localhost:8080/readyz | jq .healthy` as
  a `load_balancer.health_check.path` in a future
  Tier-1 setup (nginx, Caddy, ALB, etc.).
- The `/build` field in the response is the exact
  git commit (e.g. `v0.25.1-7-g894495d+894495d`) so
  you can confirm which build is running without
  ssh-ing to the box.

### 4. Operator-side health check script

`scripts/check_subnet_router.sh <user-or-id>` —
runs the full subnet-router state check from the
operator's shell:

- DB row + denorm column match
- Live headscale node with `tag:subnet-router` exists
- The per-user CIDR is in the node's `approved_routes`
- Status pill (UI) shows the right value
- Recent audit events (sidecar preauth + approval)

Exits 0 on `[OK]`, prints `[WARN]` for known-soft
failures, `[FAIL]` for hard ones. Use this in cron
+ alerting to catch a dead subnet-router within 30s
(much faster than the operator noticing their LAN
went unreachable).

Companion `scripts/_check_subnet_nodes.py` is the
Python helper that `check_subnet_router.sh` shells
out to (avoids shell-quoting headaches with bash
variable expansion inside `python3 -c`).

### 5. Bundle flag fix (`--netfiltermode` → `--netfilter-mode`)

Pre-v0.26.0 the bundle's `commands.txt` (and the
canonical `setup.sh`) used `--netfiltermode=off`.
That's not a valid Tailscale flag — the correct form
is `--netfilter-mode=off` (with a dash). The first
e2e pilot run hit this: `tailscale up` printed the
help and exited 1, the preauth was wasted, the
sidecar container had to be restarted. Fixed in
6 files:

- `internal/handlers/admin_user_subnet_download.go`
  (the bundle's `commands.txt`)
- `internal/handlers/bundles/setup.sh` (embedded copy)
- `deploy/subnet-router/setup.sh` (canonical)
- `internal/handlers/bundles/README.md`
- `deploy/subnet-router/README.md`
- `docs/subnet-router.md`

`make sync-bundles` now uses `cp -p` (preserves the
executable bit — the old plain `cp` left
`bundles/setup.sh` at 0644 while `deploy/setup.sh`
was 0755, which made `make check-bundles` report a
spurious diff on every sync).

### 6. docs/subnet-router.md rewrite (use cases + e2e proof)

The user-facing guide now opens with 6 concrete
scenarios from the operator's tailnet:

1. **Home NAS / media server** — Plex, Jellyfin,
   Synology reachable from anywhere via tailnet.
2. **Smart home / IoT hub** — Home Assistant, MQTT,
   cameras on the LAN, exposed to the tailnet only.
3. **Small-office / home-office server room** —
   Proxmox, UniFi, NAS, printers, dev boards.
4. **Family sharing** — parents' NAS accessible to
   kids' tablets without exposing the admin UI.
5. **Lab / dev environment** — VMs, test DBs,
   Grafana, all on `10.0.42.0/24`.
6. **Cross-site backup / replication** — two physical
   sites with their own LANs, connected via tailnet.

Plus a "When NOT to use" sidebar (1-2 devices → just
install Tailscale on each; subnet-router is for 5+
devices or devices that can't run Tailscale).

The "End-to-end verification (operator-side)" section
documents the actual output of the 2026-07-22 skyadmin
pilot, the troubleshooting entry for the status
flicker, and the `scripts/check_subnet_router.sh`
operator health check.

## Tests green

- 17/17 Go packages (all green, including the renamed
  `TestSyncOnce_FlipsToRouterActiveWhenRouteApproved`).
- `make check-bundles` — green (with the `cp -p` fix).
- `make check-nodes` — green (all 3 exit-nodes
  advertise 0.0.0.0/0 + ::/0).
- `make check-https` — green (TLS, SAN, validity,
  HSTS, HTTP→HTTPS redirect).
- `make smoke` — 79+79 pass, **4 fail in step 13
  (multi-user mesh)** — pre-existing in v0.25.1,
  unrelated to v0.26.0. The mesh page (`/my/meshes`)
  is not refreshing the members list correctly when
  a new member joins. Filed as v0.26.1 follow-up.

## Architecture note — HA is design-only in v0.26.0

This release ships the probes (`/healthz` + `/readyz`)
and the per-instance ID plumbing. The actual HA
infrastructure — PostgreSQL dual-mode, sessions in
DB, read replica, `docs/ha-architecture.md` — is
**not** part of v0.26.0. The Tier 1 (hot-standby
$20-30/mo) is a 1-2 day follow-up, not in this
release. For the operator's 4 prod users, Tier 1 is
overkill — the DR drill from v0.25.1
(`docs/disaster-recovery.md`, RTO 30 min, RPO 1h)
is the right cost/benefit point.

### 7. UI: "why would I want this?" + "how to set this up"

Operator feedback (2026-07-22) was that the
per-user subnet card on `/my/devices` only said
"logical namespace for your devices" — no
explanation of what a subnet-router is for, no
onboarding steps. Same for `/admin/users/{id}/subnet`:
a status pill and 4 buttons, but no copy explaining
why a user would want a subnet-router or how the
operator onboards them.

v0.26.0 adds two new collapsible sections on each page.

**`/my/devices` — inside the "Your personal subnet" card:**

- "What does this give me?" — 5 concrete use cases
  (home NAS / media server, smart home / IoT, office
  server room, family sharing, lab / dev environment)
  + a "when NOT to use" sidebar (1-2 devices →
  install Tailscale per-device, no subnet-router).
- "How to set this up" — 3 status-aware variants
  (`pending` / `active` / `router_active`) so the
  user sees the right next step for their state.
  The `pending` variant includes a direct
  [tailscale.com/download](https://tailscale.com/download)
  link + a step-by-step of "ask admin → run
  `sudo bash commands.txt` → status flips".
- Inline link to the full `docs/subnet-router.md`
  guide (so the user can read the use-cases +
  troubleshooting without searching).

**`/admin/users/{id}/subnet` — at the bottom:**

- "Why would a user want this?" — 6 use cases (the
  above + cross-site backup) + the "when NOT to
  use" sidebar. Helps the operator justify the
  feature when talking to a new user who asks
  "what's this for?".
- "How to onboard a user" — 3-step ordered list
  with the exact button names ("Allocate subnet" →
  "Download bundle" or "Issue preauth key" → wait
  for auto-approve). Operator can copy-paste this
  verbatim into a message to the user.
- Inline link to the full `docs/subnet-router.md`
  guide.

14 new i18n keys × 2 langs = 28 new catalog entries
(see `internal/i18n/catalog.go` for the exact
wording). Both pages tested live with `Accept-Language:
ru` and `Accept-Language: en` — the use-cases body
in Russian reads "Домашний NAS / медиа-сервер —
Plex, Jellyfin, Synology, TrueNAS, фотобиблиотека,
paperless — доступ с телефона в дороге без
публичного интернета.", in English the same
block reads "Home NAS / media server — Plex,
Jellyfin, Synology, TrueNAS, family photo library,
paperless — reachable from your phone on the road,
with no public-internet exposure."

## Files

New (5):

- `e2e_pilot.sh` (root, 165 lines) — end-to-end
  subnet-router pilot script
- `internal/handlers/handlers_healthz.go` (196 lines)
- `internal/headscale/healthz.go` (~80 lines)
- `scripts/check_subnet_router.sh` (165 lines)
- `scripts/_check_subnet_nodes.py` (98 lines)

Modified (10):

- `internal/handlers/handlers_node_ownership.go`
  — Strategy C respects existing tags + `AddTag` call
- `internal/headscale/tags.go` — `AddTag` + TagNode
  godoc warning
- `internal/sidecar/manager.go` — set
  `StatusRouterActive` (not `StatusActive`) on route
  approval
- `internal/sidecar/manager_test.go` — renamed test
  + updated assertion
- `internal/handlers/handlers.go` — `App.InstanceID
  /BuildVersion/StartedAt` fields
- `cmd/skygate/main.go` — health probe routes +
  BuildVersion set
- `internal/handlers/admin_user_subnet_download.go`
  — `--netfilter-mode` fix
- `internal/handlers/bundles/setup.sh` — same
- `deploy/subnet-router/setup.sh` — same
- `Makefile` — `sync-bundles` uses `cp -p`
- `docs/subnet-router.md` — use cases + e2e proof

## Upgrade procedure

1. `git pull origin main`
2. `make check-bundles` (should be green)
3. `docker compose up -d --force-recreate --no-deps
   skygate`
4. `bash /tmp/wait_ready.sh` (waits for `/healthz` to
   return 200, default 90s timeout)
5. `curl -s http://localhost:8080/readyz | jq .` —
   confirm `db: "ok"`, `headscale: "ok"`,
   `instance_id` matches the host's `SKYGATE_INSTANCE_ID`
6. `make test` — should show 17/17 packages green
7. (Optional, recommended) `bash e2e_pilot.sh` to
   re-prove the subnet-router flow on this build.
8. (Optional) `bash scripts/check_subnet_router.sh
   skyadmin` — should report `[OK]` for all 5
   checks.

No env-var changes. No schema migration. No breaking
changes.
