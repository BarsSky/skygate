# v0.17.1 — cross-user IP-level subnet sharing + auto-reapply ACL

2026-07-17

The next step of the v0.16.0+ per-user subnets roadmap:
explicit "share my subnet with user X" mechanism.
v0.17.0 added `tag:subnet-router` in tagOwners + the
per-user dst rule with `10.0.<uid>.0/24`, but the
per-user dst was still empty for everyone else —
subnets were islands. v0.17.1 closes that gap.

## What changed

### 1. Schema (migrations_v0.39.go)

  - New `user_subnet_shares` table
    `(grantor_user_id, grantee_user_id, created_at)`
    with PRIMARY KEY (grantor, grantee) and
    FK CASCADE on portal_users.id.
  - Index on `grantee_user_id` for the
    "what subnets can I (the grantee) access"
    scan pattern that the ACL builder uses.
  - Sharing is one-directional: a row means
    "grantor's subnet is accessible to grantee".
    The reverse requires a separate Grant() call.

### 2. `internal/subnet/shares.go` (new)

  - `subnet.Grant(d, grantorID, granteeID)` —
    idempotent (PRIMARY KEY + INSERT OR IGNORE),
    returns `ErrSelfShare` on self-share and
    `ErrNotFound` if grantor has no subnet row.
  - `subnet.Revoke(d, grantorID, granteeID)` —
    idempotent in spirit, returns
    `ErrShareNotFound` so the bot can show a
    useful reply.
  - `subnet.ListSharedBy` / `subnet.ListSharedWith`
    — the two directions the admin UI needs.

### 3. ACL extension (internal/acl/acl.go)

  - `db.GetSharedSubnetsForPlane` — INNER JOIN
    user_subnet_shares with user_subnets + portal_users.
  - `GenerateACLForPlane` now collects every
    (grantee, grantor, cidr) triple. For each
    user's per-user dst list, appends the grantor's
    CIDR for every share where the user is the
    grantee.
  - The dst list becomes deterministic per user:
    `["<user>:*", "10.0.<user>.0/24:*", ...shares]`.
    First-match semantics keep isolation: alice can
    reach 10.0.<alice>.0/24 (her own) and
    10.0.<bob>.0/24 (shared), but not
    10.0.<charlie>.0/24.

### 4. Auto-reapply ACL on subnet state changes

  - The v0.17.0 release notes flagged this as a
    follow-up: when a user got a new subnet via
    `POST /admin/users/{id}/subnet/allocate`, the
    ACL wasn't re-pushed automatically. The operator
    had to click "Re-apply ACL" on /admin/exit-rules.
    v0.17.1 fixes that — `PostAdminUserSubnetAllocate`
    now calls `acl.ApplyACLPipelineForPlane` after
    `subnet.Create`. Same for
    `PostAdminUserSubnetShare` and
    `PostAdminUserSubnetRevoke`.
  - Best-effort: a push failure is logged but
    doesn't fail the operation (the row is in the
    DB; the operator can manually re-apply if
    needed).

### 5. Admin UI

  - New "Cross-user sharing" card on
    `/admin/users/{id}/subnet` (only shown when the
    user has a subnet allocated). Two columns:
    "I shared with" (grantees + revoke button) and
    "Shared with me by" (grantors, read-only).
  - Share form: username input + "Share" button.
    POSTs to `/admin/users/{id}/subnet/share` with
    `grantee_username` in the form body.
  - Revoke: per-row form with hidden `grantee_id`
    field. POSTs to `/admin/users/{id}/subnet/revoke`.
  - 12 new i18n keys (RU+EN): `sharing_title`,
    `sharing_help`, `sharing_i_shared`,
    `sharing_shared_with`, `sharing_none_i`,
    `sharing_none_with`, `sharing_grantee_label`,
    `sharing_grantee_placeholder`, `share_button`,
    `revoke_button`.

### 6. Bot

  - `/mysubnet share <username>` — same as the
    admin's POST /share, callable by the bound
    portal user directly. ACL is re-pushed to the
    caller's plane.
  - `/mysubnet revoke <username>` — symmetric.
  - 11 new bot i18n keys (RU+EN): `share_usage`,
    `share_no_user`, `share_no_subnet`, `share_self`,
    `share_error`, `share_ok`, `revoke_usage`,
    `revoke_no_user`, `revoke_not_shared`,
    `revoke_error`, `revoke_ok`.

### 7. Tests

  - 12/12 packages green
  - 6 new subnet tests (Grant/Revoke roundtrip,
    idempotent, self-share errors, no-subnet grantor
    errors, missing revoke errors, FK CASCADE)
  - 2 new ACL tests
    (`TestGenerateACL_SharedSubnetsExtendDst`,
    `TestGenerateACL_SharedSubnetsAreIdempotent`)
  - Test schema updated in `acl_test.go` and
    `commands_test.go` to include `user_subnet_shares`
  - `TestCatalogsParity` green (23 new i18n keys
    across 2 langs, no orphans)

## Architecture note

Sharing is one-directional. The asymmetry matters:
bob shares his subnet with alice (grantor=bob,
grantee=alice) means alice gets access to bob's
subnet. NOT the other way around. A future v0.17.x
follow-up could add symmetric sharing (Grant in one
direction is the same as granting the reverse) but
for v0.17.1 we keep it explicit so the operator
can audit each direction independently.

The auto-reapply on Allocate means the v0.17.0
architecture-note caveat ("a future patch could add
a 'sync ACL' hook") is now closed. New subnets are
routable within ~1 second of allocation.

The bot path uses `env.HSForPortalUser(env.PortalUserID)`
+ `env.PortalPlaneURL(env.PortalUserID)` for the
correct per-user / per-plane HS client. This is
consistent with the v0.12.1 per-user bot routing
and the v0.13.0 per-plane ACL.

## Live verification

  - Allocate a subnet on `/admin/users/1/subnet`
    → ACL is re-pushed automatically (no need to
    click "Re-apply ACL"). v0.17.0 caveat closed.
  - Share form on the same page accepts a username,
    shows up in "I shared with" list, and the
    grantee's per-user rule now includes the
    grantor's CIDR (verified: michail's dst list
    after sharing goes from `["michail:*"]` to
    `["michail:*", "10.0.1.0/24:*"]`).
  - Revoke drops the CIDR from the grantee's dst
    (verified: back to `["michail:*"]`).
  - `/mysubnet share alice` (in chat) does the same
    via the bot path.
  - Smoke 118/118.

Deployed to VM, live at build `2c8176c`.
