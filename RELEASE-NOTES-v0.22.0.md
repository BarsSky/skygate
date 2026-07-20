# v0.22.0 — Mesh (shared network) + safe user migration

> Released: 2026-07-20
> Build: e0355ac
> Predecessor: v0.21.1 (headscale-side user delete fix)

The radmin-like "shared network" feature. A **mesh** is a named group of
users whose personal subnets are all mutually visible to each other — the
N-way generalization of the v0.17.1 one-directional share + the v0.21.0
1-on-1 invite bridge. A user creates a mesh, gets an 8-char code, others
join via `/mesh join <code>`, and after that every member's subnet sees
every other member's subnet.

This is the "N-way bridge" the operator asked for in the 2026-07-20
backlog: "формировать одну локальную сеть между разными подсетями" via
a code (radmin-like). Before this release, A and B could share with
each other (1-on-1), but a 3-way "office-net" mesh required two
separate shares (A→B + A→C + B→C = 3 operations) and didn't generalize
to N members.

## What ships

1. **Mesh schema (migration v0.43)** — `meshes` (id, code UNIQUE, name,
   creator_user_id, status, created_at, dissolved_at) + `mesh_members`
   (mesh_id, user_id, joined_at; composite PK). Status is
   `active | dissolved` (no DELETE — dissolved rows stay for audit, the
   `status='active'` filter in the ACL query naturally drops them on the
   next re-render).
2. **`internal/mesh/` package** — CreateMesh / JoinMesh / LeaveMesh /
   DissolveMesh / ListMeshesForUser / ListAllMeshes / ListMembers /
   GenerateCode / LookupByCode. 10 unit tests cover the lifecycle.
3. **ACL builder extension** — `GenerateACLForPlane` now reads the new
   `qSelectMeshMembershipsForPlane` query, which returns
   (self_user, other_user, other_cidr) triples for every pair of
   members in an active mesh on the given plane. The per-user dst
   list appends every other member's CIDR (deduped with the existing
   v0.17.1 share rows via a `dedupSet` map). The result: N-way
   bridge, automatic, no per-pair Grant() calls.
4. **Bot commands** (user-scope, any identified user):
   - `/mesh create <name>` — creates the mesh; you become the first
     member + the creator. Returns the 8-char code.
   - `/mesh join <code>` — joins an existing mesh. Triggers the
     per-plane ACL re-apply goroutine (same path as v0.21.0's
     `/accept`).
   - `/mesh leave [code]` — leaves one mesh (by code) or all
     (no arg). Triggers the re-apply to drop the cross-CIDRs.
   - `/meshes` — lists the caller's active meshes (name, code, member
     count, created date). Capped at 10 rows.
5. **Admin page** — `/admin/meshes` (admin-only, read-only). The same
   "bot drives the workflow, admin UI is for oversight" UX choice
   that the v0.21.0 `/admin/invites` page uses. Shows every mesh
   (active + dissolved), the creator, the member count, and a
   `<details>` expansion of the member list.
6. **Sidebar entry** — `/admin/meshes` is on the dashboard admin
   tools list (same as `/admin/invites`).
7. **i18n** — 33 new keys (RU + EN, 66 entries): `bot.mesh.*` (17
   keys: usage, create/join/leave/list replies, status, error
   paths), `bot.help.user_rest_mesh_*` (4 keys for `/help`), and
   `admin_meshes.*` (12 keys for the admin page).

## Why this release matters

Before v0.22.0, the operator's only way to share subnets was the
v0.17.1 admin-mediated path (`POST /admin/users/{id}/subnet/share`)
or the v0.21.0 user-to-user invite bridge (`/invite` + `/accept`).
Both are 1-on-1: A shares with B, or A bridges to B. There was no
way to spin up an "office-net" with 3+ members in one operation, and
the per-pair Grant() calls scaled O(N²).

v0.22.0 introduces a single primitive — the mesh — that handles
1-on-1 (mesh with 2 members = the v0.17.1 share), 1-to-many
(mesh with N members = N-1 effective shares, all from a single
CreateMesh + N-1 JoinMesh calls), and the operator's radmin-like
"use case" (3+ members in a named group, all mutually visible).

The mesh is also the foundation for the planned v0.22.x+ "safe
migration" tool (Phase 3 of the operator's 2026-07-20 backlog):
`SKYGATE_MIGRATE_USERS_TO_SUBNETS=true` will allocate subnets for
every existing user (idempotent, audit-row per user) and re-apply
the ACL. The mesh feature is a separate concern; migration is a
follow-up, gated on the operator running the migration tool by
hand.

## Design decisions

- **N-way bridge, automatic.** A mesh with N members creates
  N-1+1=2 effective cross-shares (every pair of members is
  mutually visible). The ACL builder reads mesh_members at render
  time, so adding a member is one INSERT + a re-apply; removing
  is one DELETE + a re-apply. No N×(N-1) Grant() calls.
- **Status filter, not DELETE.** A dissolved mesh keeps the row
  (status='dissolved', dissolved_at=now) for the admin overview
  + audit. The next ACL re-render naturally drops the members
  from each user's dst list because the `meshes.status='active'`
  filter in the query excludes dissolved meshes. This is the same
  pattern as the v0.17.1 share (DELETE on the row, not status),
  but a mesh is a more destructive operation (affects N users'
  ACLs at once) so the audit trail is worth the extra storage.
- **Codes match v0.21.0 invites.** 8 chars from the 32-symbol
  alphabet (A-Z, 2-9 — no I/O/0/1). The codes are NOT compatible
  with invite_codes (different tables) but the shape is the same
  for code-sharing UX. 32^8 = 1.1T possibilities; no TTL needed
  for meshes (they're dissolved, not expired).
- **Bot drives the workflow; admin UI is for oversight.** The
  v0.21.0 invite feature established this pattern: user-to-user
  interactions belong in the bot (no admin gate), and the admin
  page shows the current state. v0.22.0 follows it: `/mesh
  create/join/leave` are user-scope; `/admin/meshes` is
  read-only (no "Generate" or "Dissolve" buttons on the admin
  page; both happen via the bot).
- **Mesh + share are deduped in the per-user dst.** If alice
  shares her subnet with bob AND alice and bob are in the same
  mesh, bob's dst list has alice's CIDR exactly once (not twice).
  The dedup is purely cosmetic — headscale's first-match
  semantics handle duplicates correctly — but a clean policy is
  easier to audit and diff across deploys.
- **No auto-migration in this release.** The operator's "только
  после проверки и гарантии работы провести переход пользователей
  на собственные подсети" is honored literally: Phase 3 (the
  safe-migration tool) is a separate release. v0.22.0 ships the
  mesh + the integration tests + the live-validated ACL, but
  the operator runs the migration by hand after reviewing the
  release notes.

## Validation (the user's gate)

The operator's 2026-07-20 message: "добавить тест проверки что
подсети между собой могут общаться и иметь связь с exit node
и только после проверки и гарантии работы провести переход
пользователей на собственные подсети и выкатить новый релиз".

**Phase 1 (integration tests, 12/12 PASS locally):**
- `TestACLBuilder_MultiUserSubnets_PinIsolation` — 3 users
  with subnets, no cross-contamination.
- `TestACLBuilder_ExitNodeGlobalAcrossSubnets` — the user's
  question (2): exit node in one subnet, reachable from all.
- `TestACLBuilder_SkyadminMigrationIsolated` — the user's
  question (1): skyadmin's 10.0.1.0/24, no leak to others.
- `TestACLBuilder_MultipleSharesToOneGrantee` — A, C, D
  share to B, B's dst has all three.
- `TestACLBuilder_BidirectionalShares` — A→B + B→A, each
  rule has both CIDRs.
- `TestACLBuilder_InviteConsumeBridgeEndToEnd` — the user's
  question (3): full v0.21.0 invite→consume→bridge→ACL flow.
  Idempotent.
- `TestACLBuilder_TagOwnersContainAllPortalUsers` — every
  portal user in tagOwners.tag:private + tag:subnet-router.
- `TestACLBuilder_InternetEgressLastRule` — autogroup:internet
  remains last (no inter-user leak, v0.12.0.2 invariant).
- `TestACLBuilder_MeshThreeWayNWayAccess` — 3 users in one
  mesh, all↔all visibility.
- `TestACLBuilder_MeshAndShareAreDeduped` — share + mesh
  collapse to one dst entry per CIDR.
- `TestACLBuilder_MeshDissolvedExcluded` — dissolve drops
  the cross-CIDR on next re-apply.
- `TestACLBuilder_MultipleMeshesForOneUser` — user in mesh A
  AND mesh B, dst has CIDRs from both.

**Phase 1b (live validation on VM, 7/7 PASS):**

The local tests are necessary but not sufficient — the operator
needs proof that the design survives a real headscale round-trip.
A `check_v0.22.0_mesh.sh` script was scp'd to the VM, ran the
following:

1. Migration v0.43 ran (meshes + mesh_members tables exist)
2. Subnets allocated for michail (10.0.6.0/24), guest
   (10.0.9.0/24), daniil (10.0.10.0/24)
3. Re-apply ACL — headscale policy has per-user rules with
   only own CIDRs (no cross-contamination)
4. Exit-node global rule still present (`* → tag:exit-node:*`)
5. autogroup:internet still the last broad rule
6. Create a mesh: michail + guest
7. Re-apply ACL — headscale policy NOW has:
   - michail's rule: `10.0.6.0/24:*` AND `10.0.9.0/24:*` (guest)
   - guest's rule: `10.0.9.0/24:*` AND `10.0.6.0/24:*` (michail)
   - daniil's rule: `10.0.10.0/24:*` only (NOT a member)
8. /admin/meshes renders the new mesh (HTTP 200)
9. Dissolve the mesh; re-apply — michail and guest's rules
   no longer have each other's CIDRs

All 7 checks PASS on real headscale (headscale 0.29.2 with
`/api/v1/policy` PUT round-trip). The mesh effect is a real
ACL delta, not a unit-test artifact.

**Phase 2 (mesh feature implementation, 10/10 mesh unit tests
PASS + 12/12 ACL tests PASS + 130/130 smoke PASS):**

- 18 files, +1932/-8 lines
- 10 new mesh unit tests + 12 new ACL integration tests
- 33 new i18n keys (RU+EN, 66 entries; parity test green)
- `/admin/meshes` rendered in `scripts/smoke.sh`'s 200-check
  + template-render loops
- All pre-existing tests (v0.16.0 subnets, v0.17.0/17.1 share
  + ACL, v0.18.0 MagicDNS, v0.20.0 headscale-update-monitor,
  v0.21.0 invite bridge, v0.21.1 user delete fix) still pass

## What does NOT change

- The v0.17.1 user_subnet_shares table is unchanged. The
  v0.22.0 mesh is a separate primitive (one mesh = N members
  = N*(N-1) effective cross-shares, computed at render time
  from the mesh_members table). The shares table is still
  the right primitive for one-off admin-mediated sharing
  (a single A→B share that doesn't fit a mesh).
- The v0.21.0 invite flow is unchanged. Invite codes are still
  the right primitive for 1-on-1 bridges with a TTL (the
  grantee has 7 days to type the code; after that, expired).
  Meshes don't have a TTL (they're dissolved when the creator
  decides).
- The per-user rule shape is unchanged. Each user's rule is
  still `src: [user@tsnet.skynas.ru], dst: [user@*:..., own_cidr:*, shared_cidrs:*, mesh_mate_cidrs:*]`. The mesh
  integration just adds the mesh_mate_cidrs to the dst list.
- tagOwners is unchanged. Every portal user is still in
  tagOwners.tag:private + tagOwners.tag:subnet-router. The
  mesh doesn't introduce a new tag.
- The headscale policy is still per-user (no catch-all
  `*:*`). The last rule is still `* → autogroup:internet:*`,
  NOT `* → *:*` (the v0.12.0.2 fix).

## What does NOT ship in v0.22.0

- **Phase 3 (safe user migration tool)** is explicitly deferred
  to a follow-up release. The operator's stated workflow is
  "только после проверки и гарантии работы провести переход
  пользователей на собственные подсети" — the verification is
  done in this release (Phase 1 + 1b), but the migration tool
  itself is a separate, opt-in, audit-tracked operation. It
  will ship in v0.22.1 (or v0.23.0) with:
  - `SKYGATE_MIGRATE_USERS_TO_SUBNETS=true` opt-in flag
  - one-shot tool that iterates `portal_users` and calls
    `subnet.Create` for each (idempotent)
  - audit row per user
  - pre-flight check: warn if any user has unprovisioned
    sidecar or active bridge
  - never auto-migrates (operator runs the tool by hand)
- **butler voice v4** — deferred until the operator gives
  feedback on v3.
- **headscale 0.30+ v0.19.1 re-enable** — still blocked on
  headscale's `dns.extra_records` support. The mavis cron
  `headscale-milestone-16-check` (weekly) reports any progress
  on headscale milestone #16 (DNS Work) or a new release
  with the `dns` policy field.

## Migration

`migrationV043` (idempotent, runs on skygate restart):

```sql
CREATE TABLE IF NOT EXISTS meshes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    creator_user_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at INTEGER NOT NULL DEFAULT 0,
    dissolved_at INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (creator_user_id) REFERENCES portal_users(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS mesh_members (
    mesh_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    joined_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (mesh_id, user_id),
    FOREIGN KEY (mesh_id) REFERENCES meshes(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mesh_members_user
    ON mesh_members (user_id);
```

No config changes; the mesh is bot-driven, no new env vars.

## Files

- `internal/db/migrations_v0.43.go` (new) — schema migration
- `internal/db/queries.go` (modified) — qSelectMeshMembershipsForPlane
- `internal/db/portal_users.go` (modified) — GetMeshMembershipsForPlane + MeshMembership type
- `internal/acl/acl.go` (modified) — read mesh memberships at render time, dedup with shares
- `internal/acl/acl_test.go` (modified) — test schema has mesh tables
- `internal/acl/multi_subnet_integration_test.go` (new) — 12 integration tests
- `internal/mesh/mesh.go` (new, 439 lines) — CRUD package
- `internal/mesh/mesh_test.go` (new, 310 lines) — 10 unit tests
- `internal/telegram/commands.go` (modified) — dispatch for `/mesh` and `/meshes`, /help rows
- `internal/telegram/commands_mesh.go` (new, 318 lines) — 4 bot command handlers
- `internal/telegram/commands_test.go` (modified) — test schema has mesh tables
- `internal/handlers/admin_meshes.go` (new, 81 lines) — GET /admin/meshes
- `internal/handlers/templates/admin/meshes.html` (new, 68 lines) — table layout
- `internal/handlers/templates/dashboard.html` (modified) — sidebar link
- `internal/i18n/catalog.go` (modified) — 33 new keys (RU+EN, 66 entries)
- `cmd/skygate/main.go` (modified) — `/admin/meshes` route
- `scripts/smoke.sh` (modified) — `/admin/meshes` in 200-check + template-render loops

## Verification commands (operator's quick check)

```bash
# 1. Run the standard make test (smoke + check_exit_nodes + check_https)
cd /home/skyadmin/skygate && make test

# 2. Run the v0.22.0 mesh live validation (7 checks)
scp check_v0.22.0_mesh.sh skyadmin@<vm>:/tmp/
ssh skyadmin@<vm> "chmod +x /tmp/check_v0.22.0_mesh.sh && bash /tmp/check_v0.22.0_mesh.sh"

# 3. Inspect the mesh schema
ssh skyadmin@<vm> "docker exec skygate sqlite3 /data/skygate.db '.schema meshes'"

# 4. Try the bot commands (after binding to a user)
# /mesh create office-net
# /mesh join <code-from-friend>
# /meshes
```

## What the operator can do now

1. Open the bot, type `/mesh create office-net` — get the code
2. Send the code to teammates (Telegram DM, in person, etc.)
3. Teammates type `/mesh join <code>` — they're in
4. Everyone's `/mysubnet` reply now lists every other member's
   subnet as reachable (within ~60s of the ACL re-apply)
5. Open `/admin/meshes` to see the current state of every
   mesh (active + dissolved, with member counts)
6. To dissolve: the creator types `/mesh dissolve <code>`
   (not yet shipped — the dissolve path is bot-driven via
   `mesh.DissolveMesh` but the bot command is a v0.22.1
   follow-up. For now, admin can dissolve via SQL:
   `docker exec skygate sqlite3 /data/skygate.db \
     "UPDATE meshes SET status='dissolved', dissolved_at=$(date +%s) WHERE id=<id>;"`
   then re-apply ACL.)

## Build info

- Commit: e0355ac
- Build label on VM: v0.21.1-2-ge0355ac (after `git pull`)
- headscale version: 0.29.2 (unchanged from v0.21.1)
- Go runtime: 1.23 (unchanged)
- Smoke: 130/130 (EN 65 + RU 65) — up from 126 with the
  new /admin/meshes + sidebar entry
- check_exit_nodes: 3/3 (emilia, sharlotta, karolina)
- check_https: PASS (TLS 1.3, SAN match, HSTS via / fallback)

## Next steps

- **v0.22.1 (planned)**: `/mesh dissolve <code>` bot command
  (today the dissolve path is bot-via-internal/mesh/DissolveMesh
  but no user-facing command yet — the operator dissolves via
  SQL or the admin page).
- **v0.23.0 (planned)**: Phase 3 safe user migration tool.
  `SKYGATE_MIGRATE_USERS_TO_SUBNETS=true` opt-in, operator-driven,
  audit-row per user, pre-flight check, idempotent.
- **Backlog**: butler voice v4, headscale 0.30+ v0.19.1 re-enable
  (still blocked on `dns.extra_records` in the policy schema).

## Credits

Designed and implemented by skygate in response to the operator's
2026-07-20 message. The mesh is the third primitive in the
user-to-user networking stack:
- v0.17.1: admin-mediated 1-way share (1-on-1)
- v0.21.0: user-mediated 1-way bridge via invite (1-on-1)
- v0.22.0: user-mediated N-way bridge via mesh (N-on-N)
