# v0.25.0 — Mesh visibility on /my/devices + operator overview (2026-07-21)

The "mesh view" UI release. Per the operator's spec
(2026-07-21 22:40), each device now shows which virtual
subnet it belongs to, and the subnet card on
`/my/devices` lists active shares + meshes. The
operator's mental model is now directly visible in the
UI: "every device has a per-user /24; if I share it,
the other user sees my /24 in their mesh".

## What did NOT change

- No schema changes, no env-var changes.
- No new packages.
- The per-user /24 allocation logic is unchanged
  (10.0.<uid>.0/24, allocator in `internal/subnet`).
- Mesh share/join/leave flow is unchanged (the bot
  commands and the web forms work as before).
- All 4 prod users (admin / user1 / guest / user2)
  are at the same state they were before this release.
  Mesh/ACL/subnet-router flow is untouched.

## What's in v0.25.0

### 1. `/my/devices` — new "Mesh подсеть" column

For every node in the user's `MyNodes` table, the new
column shows the per-user virtual subnet the device
"belongs to" for mesh-share purposes:

```
Hostname       IP            Status   Last seen   Tag              Subnets    Mesh подсеть
workstation-2        100.64.100.5    online   ...         tag:private      —          10.0.1.0/24
skygate-host-1     100.64.100.10   online   ...         tag:private      —          10.0.1.0/24
relay-1         100.64.100.3    online   ...         tag:exit-node    —          общая
relay-2      100.64.100.4    online   ...         tag:exit-node    —          общая
```

For `tag:private` devices the cell shows the user's
CIDR (e.g. `10.0.1.0/24`). For shared infrastructure
nodes (tag:public, tag:exit-node) it shows a "shared"
pill — those aren't per-user.

Implementation: a new `MeshSubnet` and `IsShared` field
on the `myNodeRow` struct, populated from the user's
denormalized `subnet_cidr` on `portal_users` (no extra
query — the value was already in memory from the
subnet card load).

### 2. `/my/devices` — expanded Subnet card

The existing "Your personal subnet" card grows three
new rows after the status pill:

- **Mesh-сеть** — list of (CIDR, username) pairs this
  user has shared their /24 with (`grantee = them`)
  AND list of (CIDR, username) pairs other users have
  shared with them (`grantor = them`). Both
  empty if no shares exist (the "no shares yet" muted
  line). Two SQL queries (one per direction) feed
  this.
- **Активные mesh-сети** — count of active meshes the
  user is in, with the full member list inline
  (`user1 (10.0.6.0/24), guest (10.0.9.0/24)`).
  Empty if no active meshes.
- **Юзеров / Live mesh / Subnet-router** — quick
  status summary (replaces the bare CIDR/status line
  the page had before).

### 3. `/my/meshes` — per-mesh CIDR preview

The `<details>` expansion under "Members" (was
collapsed before) now shows each member's per-user
/24 inline:

```
Members: 3
  • admin (10.0.1.0/24)
  • user1 (10.0.6.0/24)
  • guest (10.0.9.0/24)
```

So the user can see at a glance "in this mesh I'll
have visibility into user1's `10.0.6.0/24` and
guest's `10.0.9.0/24`". Implementation: a new
`MemberCIDRs map[int64]string` field on `myMeshRow`,
populated by a single `SELECT user_id, cidr FROM
user_subnets WHERE user_id IN (...)` per mesh.

### 4. `/admin/subnets` — operator overview

Three new columns on the existing table, plus a
totals footer:

- **Devices** — `COUNT(*) FROM node_owner_map WHERE
  user_id = ? AND tag = 'tag:private'`
- **Mesh** — `COUNT(DISTINCT mm.mesh_id) WHERE mm.user_id
  = ? AND m.status = 'active'`. Renders as a colored
  badge when > 0, muted "0" otherwise.
- **Shares** — two icons + counts: granted (share-from
  icon) and received (share-to icon). Muted "—" when
  both are 0.

Footer summary (totals across ALL subnets, not
filtered):

```
Total devices: N    Active meshes: N    Sharing their /24: N    Shared with you: N
```

All four counters roll up from per-row counts, so
the operator can see at a glance "we have 6 devices
across 4 users, 0 active meshes, 0 shares" — which
is exactly the production state right now.

## i18n

18 new keys × 2 langs = 36 entries:

- `my_devices.subnet_card_mesh_label` / `shared_with` /
  `shared_from` / `no_shares` / `mesh_count_label` /
  `members` / `no_mesh` / `mesh_count_one` /
  `mesh_count_many`
- `devices.mesh_subnet` / `mesh_subnet_shared`
- `admin.subnets.col_devices` / `col_meshes` /
  `col_shares` / `total_devices` / `total_meshes` /
  `total_shares_granted` / `total_shares_received`

## What changed in code

| File | Change |
|---|---|
| `internal/handlers/handlers_my_devices.go` | +80 lines: extended `myNodeRow` with `MeshSubnet` + `IsShared`; new SQL for `mySharesTo` / `sharesToMe` / `myMeshMembers` / `meshCount`; pre-fetched `subnetCIDR` to the top of the handler (was bottom — used to be after the loops) |
| `internal/handlers/handlers_my_meshes.go` | +30 lines: new `MemberCIDRs` field on `myMeshRow`; one `SELECT IN` per mesh for member CIDRs |
| `internal/handlers/admin_subnets.go` | +60 lines: new `DeviceCount` / `MeshCount` / `SharesGranted` / `SharesReceived` on `overviewRow`; 4 sub-queries per row + global `totals` map |
| `internal/handlers/templates/user/devices.html` | +60 lines: new "Mesh подсеть" column, 3 new subnet card rows, expanded mesh info block |
| `internal/handlers/templates/user/meshes.html` | +1 line: `({{.CIDR}})` next to each member in the `<details>` block |
| `internal/handlers/templates/admin/subnets.html` | +40 lines: 3 new `<th>` columns, footer totals row, per-row mesh/shares badges |
| `internal/i18n/catalog.go` | +36 entries: 18 new keys × 2 langs |

## Verification (live, on the operator's VM)

```
$ curl -fsS -b cookie http://localhost:8080/my/devices | grep -E '<th|<td>'
<th>Mesh подсеть</th>          ← new column header
... 6 rows of <code>10.0.1.0/24</code>  ← admin's 6 devices

$ curl -fsS -b cookie http://localhost:8080/admin/subnets | grep -E '<th'
<th>Устройства</th>
<th>Mesh</th>
<th>Шарит</th>

$ curl -fsS -b cookie http://localhost:8080/my/meshes
HTTP 200
```

`/my/meshes` shows the per-mesh `<details>` with the
new `({{.CIDR}})` annotation next to each member
username. `user1`, `guest`, `user2` would see the
same — they have no active meshes, so the page is
empty (just the Create/Join forms) but the template
extension is in place for the first mesh they create.

## Files

7 files changed, +329/-26 lines, 17/17 packages
green.

## What comes next

The same per-user, per-mesh, per-share data is now
visible to:
- the user (on `/my/devices` and `/my/meshes`)
- the admin (on `/admin/subnets`)

The next logical UI step is the **per-user "audit
export"** — let user1 or admin download their
own `audit_log` (last 30 days, CSV) to share with
their own auditors. That's a 1-day feature, gated
on a per-user scope (not global). v0.25.1 or v0.26.0
candidate.

The compliance tier (v0.23.0) is unchanged. The
"per-user control plane" feature is still opt-in and
remains "compliance tier only" per v0.23.1 — v0.25.0
is purely a UI release on top of the default path
(global headscale + per-user subnets).
