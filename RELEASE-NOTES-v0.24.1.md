# v0.24.1 — /my/devices shows tag:subnet-router + advertised routes (2026-07-21)

The "what does this device actually do" UI fix.

## Why this change

Before v0.24.1, `/my/devices` showed only:

| Hostname | IP | Status | Last seen | Tag |

The IP was always `100.64.0.X` (Tailscale's CGNAT range, the
same for every node). The tag column had two states:
`tag:private` (default) or `tag:public` (shared exit-node).

A user with a `tag:subnet-router` node (e.g. a RPi running
`deploy/subnet-router/setup.sh` that bridges their
`10.0.6.0/24` LAN) couldn't tell that this device was
anything special — same row as their phone, same
`100.64.0.X` IP, no indication that it bridges a LAN.
Likewise for `tag:exit-node` (relay nodes), although those
appear in the `tag:public` table at the top of the page.

The user asked: "не понятно есть ли у устройства
пользователя subnet route виден только headscale ip" —
exactly this gap.

## What changed

1. **New "Subnets" column** on the `/my/devices` table. For
   every node, shows the node's `AvailableRoutes` (CIDRs
   the node is asking headscale to advertise) as small
   badges. If `AvailableRoutes > ApprovedRoutes` (some
   routes pending admin approval), a "pending" pill is
   shown next to the list with a tooltip explaining the
   gap.

2. **Tag column now distinguishes** four states
   (subnet-router wins over exit-node wins over public
   wins over private):
   - `tag:subnet-router` → blue "subnet router" badge
   - `tag:exit-node` → amber "exit node" badge
   - `tag:public` → green "public" badge
   - (no tag) → grey "tag:private" badge as before

3. **Updated tag_hint** (RU + EN):
   > tag:private — ваше, tag:public — общее, tag:subnet-router — мост в вашу LAN, tag:exit-node — публичный выход в интернет

4. **`myNodeRow` extended** with `Tags`, `AvailableRoutes`,
   `ApprovedRoutes`, `IsSubnetRouter`, `IsExitNode`. The
   handler fills them from `headscale.NodeView` (which
   already carries this data — no API change needed).

5. **Inline `hasTag` helper** in
   `internal/handlers/handlers_my_devices.go` so this
   file stays free of cross-package imports for a small
   utility.

## i18n

5 new keys × 2 langs (10 entries):

- `devices.tag_subnet_router` / `devices.tag_exit_node`
- `devices.subnets` / `devices.routes_pending`
- `devices.routes_pending_help`
- `devices.tag_hint_v0_24_1` (extended `tag_hint`, the
  old key is left untouched for backward compat — the
  templates now reference the new key)

## What did NOT change

- No schema changes, no env-var changes, no new packages.
- No new env vars.
- All 17/17 packages green; the bilingual smoke test
  passes; templates_test's `TestTemplateArgsMatchCatalog`
  still passes (the new keys are in both `ru` and `en`
  catalogs).

## Files

- `internal/handlers/handlers_my_devices.go` — +22 lines
  (extended `myNodeRow`, inline `hasTag`).
- `internal/handlers/templates/user/devices.html` — +24
  lines (new "Subnets" column, four-state tag column).
- `internal/i18n/catalog.go` — +20 lines (10 entries: 5
  keys × 2 langs).

## Verification (live, on the operator's VM)

```
$ curl -fsS -b cookie http://localhost:8080/my/devices \
  | grep -oE '<th[^>]*>[^<]*</th>'
<th>CIDR</th>
<th>Статус</th>
<th>Hostname</th>
<th>Owner</th>
<th>IP</th>
<th>Статус</th>
<th>Hostname</th>
<th>IP</th>
<th>Статус</th>
<th>Последний раз</th>
<th>Тег</th>
<th>Подсети</th>   <-- new in v0.24.1
```

The "Тег" and "Подсети" columns are the new ones. For
skyadmin's 6 devices (all `tag:private`, no subnet-router
yet), the Subnets column shows `—` (the muted glyph for
"no advertised routes"), and the Tag column shows the
existing `tag:private` badge. When a user runs
`deploy/subnet-router/setup.sh` on their router host and
the node registers, the new "subnet router" badge will
appear and the Subnets column will show `10.0.<uid>.0/24`
plus any LAN segments the operator chose to advertise.

---

## What is still left for "full migration to per-user subnets + mesh"

This was the operator's second question. The 4 production
users (skyadmin / michail / guest / daniil) are at the
following state as of v0.24.0 + v0.24.1:

| User     | Subnet           | Subnet-router | Used mesh? | Used share? |
|----------|------------------|---------------|------------|-------------|
| skyadmin | 10.0.1.0/24 act. | none          | no         | no          |
| michail  | 10.0.6.0/24 pen. | none          | no         | no          |
| guest    | 10.0.9.0/24 pen. | none          | no         | no          |
| daniil   | 10.0.10.0/24 pen.| none          | no         | no          |

"Full migration" has four separate legs; the operator
should decide which are actually wanted for this tailnet:

### 1. Subnet-router up for each user (mechanical, no code)

- For each of michail/guest/daniil, the user needs to
  follow `docs/subnet-router.md` on a host that runs 24/7
  in their LAN.
- After they run `setup.sh`, `sidecar.SyncOnce` auto-
  approves the route and the status pill flips from
  `pending` to `router_active` within ~30s.
- No code change. The 5-step procedure is documented in
  `docs/subnet-router.md` (5-step setup). This is the
  primary thing the operator should drive.

### 2. Cross-user subnet sharing (code is done, not used)

`/my/meshes` (web UI, v0.22.1) and `/admin/meshes` (admin
read-only) are the entry points. Bot commands
`/mesh create|join|leave` and `/share_subnet <user>`
(v0.17.1 bot path) are also wired up. The state is empty
(0 mesh_members, 0 user_subnet_shares rows). To start
using it: have one user (say michail) click
`/my/meshes → Create mesh`, share the code with skyadmin
in chat, skyadmin pastes it into `/my/meshes → Join`.
After 30s the per-plane ACL goroutine auto-reapplies the
new policy.

### 3. v0.19.1: `exitnode.skygate-subnet-<user>` DNS record

Still blocked on `headscale 0.30+` for
`dns.extra_records` support. The 0.29.2 policy parser
rejects the `dns` key with `unknown field: "dns"`. Code
preserved in commit `646f8fb`. mavis cron
`headscale-milestone-16-check` polls weekly for any
progress on headscale milestone #16 (DNS Work).

### 4. Per-user exit-node assignment (UI exists, no DNS)

`user_subnets.preferred_exit_node_id` column exists since
v0.19.0 (the reverted commit) but the DNS record it
should back is exactly the v0.19.1 item above. Currently
the operator picks the exit-node per-machine via the
Tailscale client GUI (`tailscale up --exit-node=emilia`
or via the menu). No further code needed until v0.19.1
unblocks.

### Summary

- **Code leg**: 100% done (per-user subnets + sidecar +
  ACL sharing + mesh + MagicDNS + v0.24.1 UI).
- **Operator leg**: 0/4 subnet-routers live, 0/4 users
  on mesh, 0 cross-user shares. Each user has the
  `docs/subnet-router.md` instructions available; the
  per-user subnet is allocated and waiting.
- **External leg**: headscale 0.30+ for v0.19.1
  (`exitnode.skygate-subnet-<user>` DNS record). Not
  blocking for any current use case.
